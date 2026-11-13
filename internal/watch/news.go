package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// NewsItem 是一条新闻/公告。
type NewsItem struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	Time   string `json:"time,omitempty"`
	Code   string `json:"code,omitempty"`
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
}

// 只允许访问这两个主机（Guard 白名单与这里双重校验）。
//
// 公告口径变更（2026-11-12）：原东财公告接口已从数据层整体移除（不再有任何东财请求）。
// notice/announcement 这类触发源改为「复用新浪滚动新闻 + 关键词/个股过滤」：
//   - 好处是不再依赖任何东财主机，且与 roll 共用同一份抓取与缓存；
//   - 代价是**公告覆盖变弱**：新浪滚动新闻是媒体稿件流，不是交易所公告全文库，
//     会有「公告没被媒体报道就抓不到」的漏报。上层（规则说明与 Agent 提示）需要按此理解，
//     不能把 news.hits 当成「公告全量命中数」。
const (
	sinaRollURL = "https://feed.mix.sina.com.cn/api/roll/get?pageid=153&lid=2516&num=20"
	// sinaRollNoticeURL 是公告口径的备用拉取页：lid=2517 是新浪财经公告栏目。
	sinaRollNoticeURL = "https://feed.mix.sina.com.cn/api/roll/get?pageid=153&lid=2517&num=20"
	newsHTTPTime      = 8 * time.Second
	newsMaxBytes      = 2 << 20
	negativeWordsRaw  = "减持,亏损,处罚,立案,退市,商誉,质押,爆雷,下调,违规,诉讼,冻结"
)

// negativeWords 是判定"负面"的关键词表（只用于 news.negative 指标）。
var negativeWords = strings.Split(negativeWordsRaw, ",")

// fetchNews 拉取并解析新闻/公告。所有请求都先过 Guard 白名单。
func (e *Engine) fetchNews(ctx context.Context, src Source) ([]NewsItem, error) {
	switch src.Kind {
	case "notice", "announcement":
		return e.fetchAnnouncements(ctx, src.Keyword)
	default:
		return e.fetchSinaRoll(ctx)
	}
}

func (e *Engine) doGet(ctx context.Context, rawURL, referer string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	if e.opts.Guard != nil && !e.opts.Guard.AllowHost(u.Host) {
		return nil, "", fmt.Errorf("主机 %s 不在网络安全白名单内", u.Host)
	}
	ctx, cancel := context.WithTimeout(ctx, newsHTTPTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, newsMaxBytes))
	if err != nil {
		return nil, "", err
	}
	return raw, resp.Header.Get("Content-Type"), nil
}

// decodeMaybeGBK 仅在字节不是合法 UTF-8 时按 GBK 转码（新浪接口常返回 GBK）。
func decodeMaybeGBK(raw []byte) ([]byte, error) {
	if json.Valid(raw) || utf8Valid(raw) {
		return raw, nil
	}
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
	if err != nil {
		return nil, fmt.Errorf("GBK 转码失败: %w", err)
	}
	return out, nil
}

func utf8Valid(b []byte) bool {
	for len(b) > 0 {
		if b[0] < 0x80 {
			b = b[1:]
			continue
		}
		// 简化校验：遇到非法起始字节直接判失败，交给 GBK 解码。
		if b[0] < 0xC2 || b[0] > 0xF4 {
			return false
		}
		return true
	}
	return true
}

func (e *Engine) fetchSinaRoll(ctx context.Context) ([]NewsItem, error) {
	return e.fetchSinaRollURL(ctx, sinaRollURL)
}

// fetchSinaRollURL 拉取一个新浪滚动新闻栏目并归一成 NewsItem。
func (e *Engine) fetchSinaRollURL(ctx context.Context, address string) ([]NewsItem, error) {
	raw, _, err := e.doGet(ctx, address, "https://finance.sina.com.cn/")
	if err != nil {
		return nil, err
	}
	raw, err = decodeMaybeGBK(raw)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Result struct {
			Data []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				Ctime string `json:"ctime"`
			} `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("新浪滚动新闻解析失败: %w", err)
	}
	out := make([]NewsItem, 0, len(payload.Result.Data))
	for _, d := range payload.Result.Data {
		out = append(out, NewsItem{Title: strings.TrimSpace(d.Title), Source: "sina", Time: d.Ctime, URL: d.URL})
	}
	return out, nil
}

// fetchAnnouncements 是 notice/announcement 触发源的实现：复用新浪滚动新闻 + 过滤。
//
// 口径变化（务必理解）：这里拿到的**不是交易所公告全文**，而是新浪财经的新闻流。
// 东财公告接口下线后没有等价替代（新浪没有公开的公告检索 API），所以：
//   - 先拉公告栏目（lid=2517），失败再退回财经要闻（lid=2516）；
//   - 只保留标题里出现关键词的条目，避免把整屏泛财经新闻都算成「命中」；
//   - Source 统一标 sina，上层据此知道这是媒体口径而不是公告口径。
//
// 关键词为空时返回全部条目（调用方 keywordHits 会再做一次精确匹配）。
func (e *Engine) fetchAnnouncements(ctx context.Context, keyword string) ([]NewsItem, error) {
	items, err := e.fetchSinaRollURL(ctx, sinaRollNoticeURL)
	if err != nil || len(items) == 0 {
		// 公告栏目不可用时退回财经要闻：宁可覆盖宽一点，也不要让规则整块失效。
		fallback, fallbackErr := e.fetchSinaRollURL(ctx, sinaRollURL)
		if fallbackErr != nil {
			if err != nil {
				return nil, fmt.Errorf("新浪公告栏目不可用（%v），财经要闻也不可用: %w", err, fallbackErr)
			}
			return nil, fallbackErr
		}
		items = fallback
	}
	return filterNewsByKeyword(items, keyword), nil
}

// filterNewsByKeyword 只保留标题里出现关键词的条目；关键词为空时原样返回。
// 这一步是「公告 -> 新浪新闻」降级后的必要收敛，否则泛财经头条会把 news.hits 顶满。
func filterNewsByKeyword(items []NewsItem, keyword string) []NewsItem {
	needle := strings.ToLower(strings.TrimSpace(keyword))
	if needle == "" {
		return items
	}
	out := make([]NewsItem, 0, len(items))
	for _, item := range items {
		title := strings.ToLower(item.Title)
		if strings.Contains(title, needle) || strings.Contains(strings.ToLower(item.Name), needle) {
			out = append(out, item)
		}
	}
	return out
}

// keywordHits 统计关键词命中数，并返回命中条目（用于摘要）。
func keywordHits(items []NewsItem, keyword string) (int, []NewsItem) {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return 0, nil
	}
	var hits []NewsItem
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Title), kw) {
			hits = append(hits, it)
		}
	}
	return len(hits), hits
}

// hasNegative 判断文本是否含负面词。
func hasNegative(items []NewsItem) bool {
	for _, it := range items {
		title := strings.ToLower(it.Title)
		for _, w := range negativeWords {
			if strings.Contains(title, w) {
				return true
			}
		}
	}
	return false
}

var errNoNewsSource = errors.New("规则没有 news 触发源")
