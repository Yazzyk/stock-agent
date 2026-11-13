package datasource

// 官方期货 / 期权域（按文档可用范围接入）。
//
// 重要事实（2026-11-13 实测）：
//   - 品种/合约/主连/持仓/仓单/交易时间轴等基础与持仓数据对外部 Key 可用；
//   - futures/prices/daily 与 options/prices/daily 当前上游返回 5003（数据源不可用），
//     这是上游状态而不是权限问题，工具需把它当「暂时没有」而不是 Key 配错；
//   - futures/fundamentals/* 为客户端专用（code=2004）。
//
// 单位：价格按合约计价货币；成交/持仓单位「手」；金额单位元（字段口径见 docs/FUYAO-API.md）。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoFuturesViews 是期货视图 → 端点 ID 的白名单。
var fuyaoFuturesViews = map[string]string{
	"varieties":          "futures.varieties.list",
	"plates":             "futures.variety-plates.list",
	"contracts":          "futures.contracts.list",
	"contract":           "futures.contracts.detail",
	"main":               "futures.contracts.main-list",
	"main-continuous":    "futures.contracts.main-continuous-list",
	"secondary":          "futures.contracts.secondary-main-list",
	"commodity-index":    "futures.contracts.commodity-index-list",
	"positions-variety":  "futures.positions.variety-daily",
	"positions-company":  "futures.positions.company-variety-daily",
	"positions-contract": "futures.positions.contract-daily",
	"positions-history":  "futures.positions.contract-historical",
	"companies":          "futures.positions.company-list",
	"basis":              "futures.basis.main-continuous-latest",
	"basis-history":      "futures.basis.historical",
	"warehouse":          "futures.warehouse-receipts.historical",
	"kline":              "futures.prices.daily",
	"intraday":           "futures.prices.intraday",
	"schedule":           "futures.calendar.trading-schedule",
	"session":            "futures.calendar.session-timeline",
	"fundamentals":       "futures.fundamentals.indicators-historical",
}

// fuyaoOptionsViews 是期权视图 → 端点 ID 的白名单。
var fuyaoOptionsViews = map[string]string{
	"varieties": "options.varieties.list",
	"contracts": "options.contracts.list",
	"contract":  "options.contracts.detail",
	"kline":     "options.prices.daily",
	"intraday":  "options.prices.intraday",
	"session":   "options.calendar.session-timeline",
}

// fuyaoViewEndpoint 在视图白名单里查端点，未知视图返回可用视图列表。
func fuyaoViewEndpoint(views map[string]string, view, domain string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(view))
	if key == "" {
		keys := make([]string, 0, len(views))
		for k := range views {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		key = keys[0]
	}
	endpoint, ok := views[key]
	if !ok {
		keys := make([]string, 0, len(views))
		for k := range views {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "", fmt.Errorf("%w: 未知%s视图 %q（可用：%s）", contract.ErrFuyaoUnsupported, domain, view, strings.Join(keys, ", "))
	}
	return endpoint, nil
}

// FuyaoFuturesView 执行一次期货视图查询（view 见 fuyaoFuturesViews）。
func (s *Service) FuyaoFuturesView(ctx context.Context, view string, params map[string]string, limit int) (contract.FuyaoData, error) {
	endpoint, err := fuyaoViewEndpoint(fuyaoFuturesViews, view, "期货")
	if err != nil {
		return contract.FuyaoData{}, err
	}
	return s.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: limit})
}

// FuyaoOptionsView 执行一次期权视图查询（view 见 fuyaoOptionsViews）。
func (s *Service) FuyaoOptionsView(ctx context.Context, view string, params map[string]string, limit int) (contract.FuyaoData, error) {
	endpoint, err := fuyaoViewEndpoint(fuyaoOptionsViews, view, "期权")
	if err != nil {
		return contract.FuyaoData{}, err
	}
	return s.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: limit})
}
