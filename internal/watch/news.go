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
const (
	sinaRollURL      = "https://feed.mix.sina.com.cn/api/roll/get?pageid=153&lid=2516&num=20"
	emAnnounceURL    = "https://np-anotice-stock.eastmoney.com/api/security/ann?sr=-1&page_size=20&page_index=1&ann_type=A&client_source=web"
	newsHTTPTime     = 8 * time.Second
	newsMaxBytes     = 2 << 20
	negativeWordsRaw = "减持,亏损,处罚,立案,退市,商誉,质押,爆雷,下调,违规,诉讼,冻结"
)

// negativeWords 是判定"负面"的关键词表（只用于 news.negative 指标）。
var negativeWords = strings.Split(negativeWordsRaw, ",")

// fetchNews 拉取并解析新闻/公告。所有请求都先过 Guard 白名单。
func (e *Engine) fetchNews(ctx context.Context, src Source) ([]NewsItem, error) {
	switch src.Kind {
	case "notice", "announcement":
		return e.fetchAnnouncements(ctx)
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
	raw, _, err := e.doGet(ctx, sinaRollURL, "https://finance.sina.com.cn/")
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

func (e *Engine) fetchAnnouncements(ctx context.Context) ([]NewsItem, error) {
	raw, _, err := e.doGet(ctx, emAnnounceURL, "https://data.eastmoney.com/")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data struct {
			List []struct {
				Title      string `json:"title"`
				NoticeDate string `json:"notice_date"`
				Codes      []struct {
					StockCode string `json:"stock_code"`
					ShortName string `json:"short_name"`
				} `json:"codes"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("东财公告解析失败: %w", err)
	}
	out := make([]NewsItem, 0, len(payload.Data.List))
	for _, d := range payload.Data.List {
		item := NewsItem{Title: strings.TrimSpace(d.Title), Source: "eastmoney", Time: d.NoticeDate}
		if len(d.Codes) > 0 {
			item.Code = d.Codes[0].StockCode
			item.Name = d.Codes[0].ShortName
		}
		out = append(out, item)
	}
	return out, nil
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
