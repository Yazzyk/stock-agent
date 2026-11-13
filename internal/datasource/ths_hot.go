package datasource

// 同花顺热门榜（个股人气榜）：dq.10jqka.com.cn 的 fuyao 热榜接口。
//
// 实测字段（2026-11-11）：
//
//	market 17=沪 / 33=深；code、name、rate(热度分)、rise_and_fall(涨跌幅%)、order(排名)
//	hot_rank_chg(排名变化，正数=上升)、tag.concept_tag(题材标签)、tag.popularity_tag(人气标签)
//	analyse_title / analyse(上榜原因，小时榜有、日榜可能为空)
//
// 该接口不直接给现价，这里用现有 Quote 批量补一次（失败不影响榜单本身）。
// type=hour 小时榜、type=day 日榜；缓存 60 秒。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	thsHotURLBase = "https://dq.10jqka.com.cn/fuyao/hot_list_data/out/hot_list/v1/stock?"
	thsHotReferer = "https://eq.10jqka.com.cn/"
	ttlThsHot     = 60 * time.Second
	defaultThsHot = 30
	maxThsHot     = 100
)

// HotStock 是热门榜的一行（金额单位：元，涨跌幅为百分数）。
type HotStock struct {
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Market        string   `json:"market,omitempty"`
	Price         float64  `json:"price,omitempty"`
	ChangePct     float64  `json:"changePct"`
	Heat          float64  `json:"heat"`
	Rank          int      `json:"rank"`
	RankChange    int      `json:"rankChange,omitempty"`
	ConceptTags   []string `json:"conceptTags,omitempty"`
	PopularityTag string   `json:"popularityTag,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

// thsHotRaw 是同花顺热榜的原始条目。
type thsHotRaw struct {
	Market       int     `json:"market"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Rate         string  `json:"rate"`
	RiseAndFall  float64 `json:"rise_and_fall"`
	HotRankChg   int     `json:"hot_rank_chg"`
	Order        int     `json:"order"`
	AnalyseTitle string  `json:"analyse_title"`
	Analyse      string  `json:"analyse"`
	Tag          struct {
		ConceptTag    []string `json:"concept_tag"`
		PopularityTag string   `json:"popularity_tag"`
	} `json:"tag"`
}

// normalizeHotType 归一榜单类型：hour（默认）/ day；其它值报中文错误。
func normalizeHotType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "hour":
		return "hour", nil
	case "day":
		return "day", nil
	default:
		return "", fmt.Errorf("datasource: 热门榜 type 只支持 hour 或 day，实际 %q", raw)
	}
}

// normalizeHotLimit 收敛条数：默认 30，上限 100。
func normalizeHotLimit(limit int) int {
	if limit <= 0 {
		return defaultThsHot
	}
	if limit > maxThsHot {
		return maxThsHot
	}
	return limit
}

// thsHotURL 组装热榜地址。
func thsHotURL(listType string) string {
	query := url.Values{}
	query.Set("stock_type", "a")
	query.Set("type", listType)
	query.Set("list_type", "normal")
	return thsHotURLBase + query.Encode()
}

// parseThsHot 解析同花顺热榜响应；坏行跳过，全空时报错。
func parseThsHot(body []byte, limit int) ([]HotStock, error) {
	var envelope struct {
		StatusCode int `json:"status_code"`
		Data       *struct {
			StockList []thsHotRaw `json:"stock_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("datasource: 同花顺热榜格式异常: %w", err)
	}
	if envelope.StatusCode != 0 || envelope.Data == nil {
		return nil, fmt.Errorf("%w: 同花顺热榜返回异常（status_code=%d）", ErrEmptyResponse, envelope.StatusCode)
	}
	out := make([]HotStock, 0, len(envelope.Data.StockList))
	for _, row := range envelope.Data.StockList {
		code := strings.TrimSpace(row.Code)
		name := strings.TrimSpace(row.Name)
		if code == "" || name == "" {
			continue
		}
		reason := strings.TrimSpace(row.AnalyseTitle)
		if reason == "" {
			reason = truncateRunes(strings.TrimSpace(row.Analyse), 120)
		}
		out = append(out, HotStock{
			Code:          code,
			Name:          name,
			Market:        thsMarketName(row.Market),
			ChangePct:     row.RiseAndFall,
			Heat:          parseFloatString(row.Rate),
			Rank:          row.Order,
			RankChange:    row.HotRankChg,
			ConceptTags:   row.Tag.ConceptTag,
			PopularityTag: strings.TrimSpace(row.Tag.PopularityTag),
			Reason:        reason,
		})
	}
	if len(out) == 0 {
		return nil, ErrEmptyResponse
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].Heat > out[j].Heat
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// thsMarketName 把同花顺 market 编码映射成 SH/SZ；未知返回空串。
func thsMarketName(market int) string {
	switch market {
	case 17:
		return "SH"
	case 33:
		return "SZ"
	}
	return ""
}

// MarketHot 返回同花顺热门榜；Quote 补价失败时仍然返回榜单。
func (s *Service) MarketHot(ctx context.Context, listType string, limit int) ([]HotStock, error) {
	normalized, err := normalizeHotType(listType)
	if err != nil {
		return nil, err
	}
	limit = normalizeHotLimit(limit)
	cacheKey := fmt.Sprintf("ths:hot:%s:%d", normalized, limit)
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]HotStock); isItems {
			return append([]HotStock(nil), items...), nil
		}
	}
	body, err := s.client.get(ctx, request{
		source:  sourceTHS,
		url:     thsHotURL(normalized),
		referer: thsHotReferer,
	})
	if err != nil {
		return nil, fmt.Errorf("取同花顺热门榜失败: %w", err)
	}
	out, err := parseThsHot(body, limit)
	if err != nil {
		return nil, fmt.Errorf("取同花顺热门榜失败: %w", err)
	}
	// 热榜不含现价：批量补一次；失败时保留榜单，前端价格列显示 —。
	codes := make([]string, 0, len(out))
	for _, item := range out {
		codes = append(codes, item.Code)
	}
	if quotes, qerr := s.Quote(ctx, codes); qerr == nil {
		byCode := make(map[string]contract.Quote, len(quotes))
		for _, q := range quotes {
			byCode[q.Code] = q
		}
		for i := range out {
			if q, ok := byCode[out[i].Code]; ok && q.Price > 0 {
				out[i].Price = q.Price
			}
		}
	}
	s.cache.put(cacheKey, append([]HotStock(nil), out...), ttlThsHot)
	return out, nil
}

// truncateRunes 按 rune 截断，避免把中文切成半个字符。
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return string(r[:1])
	}
	return string(r[:limit-1]) + "…"
}
