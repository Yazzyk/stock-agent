package datasource

// 官方其他域：A 股打新日历、港股打新、ETF 申赎净流入（客户端专用）、
// 全市场 Parquet 数据导出（预签名链接）。
//
// 注意：market-dumps 返回的 presigned_url 有时效（通常数小时），不要落盘长期复用；
// 链接指向 o.thsi.cn，不在默认网络白名单内，下载动作应由用户在浏览器完成。

import (
	"context"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoOtherViews 是「其他」域视图 → 端点 ID 的白名单。
var fuyaoOtherViews = map[string]string{
	"a-ipo":          "a-share.ipo.calendar-list",
	"hk-application": "hk-share.ipo.application-list",
	"hk-calendar":    "hk-share.ipo.calendar-list",
	"hk-detail":      "hk-share.ipo.issue-detail",
	"etf-flow":       "fund.etf-flow.net-inflow-historical",
	"dump-10y":       "dump.market-dumps.daily-k.download-url",
	"dump-10d":       "dump.market-dumps.daily-k-10d.download-url",
	"dump-adjust":    "dump.market-dumps.adjustment-factors.download-url",
}

// FuyaoOtherView 执行一次「其他」域查询。
// 常用参数：a-ipo 需要 market（sh_sz/bj）与 status（upcoming/subscribable/awaiting_listing）；
// hk-calendar 需要 status；hk-detail 需要 issue_code；dump 无参数。
func (s *Service) FuyaoOtherView(ctx context.Context, view string, params map[string]string, limit int) (contract.FuyaoData, error) {
	endpoint, err := fuyaoViewEndpoint(fuyaoOtherViews, view, "其他")
	if err != nil {
		return contract.FuyaoData{}, err
	}
	return s.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: limit})
}

// FuyaoIPOCalendar 取 A 股打新日历（market=sh_sz/bj；status=upcoming/subscribable/awaiting_listing）。
func (s *Service) FuyaoIPOCalendar(ctx context.Context, market, status string) ([]map[string]any, error) {
	market = strings.ToLower(strings.TrimSpace(market))
	status = strings.ToLower(strings.TrimSpace(status))
	if market == "" {
		market = "sh_sz"
	}
	if status == "" {
		status = "upcoming"
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.ipo.calendar-list",
		Params:   map[string]string{"market": market, "status": status},
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoMarketDumpURL 取全市场 Parquet 下载链接。
// kind：10y（10 年日 K）/10d（最近 10 交易日）/adjust（复权因子）。
func (s *Service) FuyaoMarketDumpURL(ctx context.Context, kind string) (map[string]any, error) {
	view := map[string]string{"10y": "dump-10y", "10d": "dump-10d", "adjust": "dump-adjust"}[strings.ToLower(strings.TrimSpace(kind))]
	if view == "" {
		return nil, fmt.Errorf("datasource: kind 只支持 10y/10d/adjust")
	}
	data, err := s.FuyaoOtherView(ctx, view, nil, 1)
	if err != nil {
		return nil, err
	}
	if data.Raw != nil {
		return data.Raw, nil
	}
	if len(data.Items) > 0 {
		return data.Items[0], nil
	}
	return map[string]any{}, nil
}
