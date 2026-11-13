package datasource

// 官方资讯域：快讯列表/刷新、公告详情、资讯详情、热门专题、个股分时新闻、
// 日期范围异动解读、资讯事件库（客户端专用）。
//
// 单位：所有时间字段是毫秒 Unix 时间戳（Asia/Shanghai 展示）；正文长文本在
// 通用查询层会被截断到 600 字，需要全文请用详情端点单独取。

import (
	"context"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoNewsViews 是资讯视图 → 端点 ID 的白名单。
var fuyaoNewsViews = map[string]string{
	"flash":          "news.flash.list",
	"flash-refresh":  "news.flash.refresh",
	"notice":         "news.notice.detail",
	"article":        "news.article.detail",
	"topic":          "news.topic.hot-list",
	"stock-timeline": "news.stock.timeline-list",
	"anomaly":        "news.anomaly.analysis",
	"events":         "news.events.search",
}

// FuyaoNewsView 执行一次资讯视图查询（view 见 fuyaoNewsViews）。
//
// 常用参数：
//   - flash:          tag_id, seq（首屏 seq="0"）
//   - flash-refresh:  tag_id, seq, create_time_ms（毫秒且能整除 1000）
//   - notice:         seq
//   - article:        client_type（WEB/WEB_ENCRYPTED/PC/PC_LIST）, encoded_seq
//   - topic:          tab_id, cursor, limit, important_only
//   - stock-timeline: thscode, offset（首屏 "first"）, size
//   - anomaly:        begin_date, end_date, thscode
//   - events:         query, standard_start_time, standard_end_time, size（客户端专用）
func (s *Service) FuyaoNewsView(ctx context.Context, view string, params map[string]string, limit int) (contract.FuyaoData, error) {
	endpoint, err := fuyaoViewEndpoint(fuyaoNewsViews, view, "资讯")
	if err != nil {
		return contract.FuyaoData{}, err
	}
	// 个股分时新闻必须传官方 thscode，这里把 300033 归一成 300033.SZ。
	if endpoint == "news.stock.timeline-list" {
		if raw := strings.TrimSpace(params["thscode"]); raw != "" {
			if code, parseErr := ParseCode(raw); parseErr == nil {
				if ths := code.THSCode(); ths != "" {
					params = cloneParams(params)
					params["thscode"] = ths
				}
			}
		}
		if strings.TrimSpace(params["offset"]) == "" {
			params = cloneParams(params)
			params["offset"] = "first"
		}
	}
	return s.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: limit})
}

// cloneParams 复制参数 map，避免修改调用方传入的 map（工具层可能复用）。
func cloneParams(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// FuyaoNewsViews 返回资讯视图名列表（工具 schema 的 enum 用，稳定排序）。
func FuyaoNewsViews() []string {
	return sortedKeys(fuyaoNewsViews)
}

// FuyaoFundViews 返回基金视图名列表（工具 schema 的 enum 用）。
func FuyaoFundViews() []string { return sortedKeys(fuyaoFundViews) }

// FuyaoFuturesViews 返回期货视图名列表。
func FuyaoFuturesViews() []string { return sortedKeys(fuyaoFuturesViews) }

// FuyaoOptionsViews 返回期权视图名列表。
func FuyaoOptionsViews() []string { return sortedKeys(fuyaoOptionsViews) }

// sortedKeys 返回 map 的键（升序），供工具 schema 枚举复用。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
