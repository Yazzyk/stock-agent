package datasource

// 官方基金域：资料、行情、净值/业绩、持仓、持有人、经理、公司、分红、诊断、
// 财务、募集、资讯、QDII 额度、ETF 申赎净流入（客户端专用）。
//
// 统一入口 FuyaoFundView(view, params) 用白名单把中文视图名映射到官方端点，
// 避免工具层直接暴露 30 多个端点 ID；强类型便捷方法只覆盖最常用的快照与 K 线。
//
// 单位：净值/价格=元；收益率/占比/回撤=百分数原值；规模=元（官方字段口径见 docs/FUYAO-API.md）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoFundViews 是基金视图 → 端点 ID 的白名单。
var fuyaoFundViews = map[string]string{
	"profile":              "fund.profile.detail",
	"snapshot":             "fund.market.snapshot",
	"historical":           "fund.market.historical",
	"historical-batch":     "fund.market.historical-batch",
	"nav":                  "fund.performance.nav",
	"returns":              "fund.performance.returns",
	"performance":          "fund.performance.indicators-historical",
	"drawdown":             "fund.performance.drawdowns",
	"drawdowns":            "fund.performance.drawdowns",
	"holdings":             "fund.portfolio.holdings",
	"asset-allocation":     "fund.portfolio.asset-allocation",
	"industry-allocation":  "fund.portfolio.industry-allocation",
	"stock-history":        "fund.portfolio.stock-history",
	"bond-history":         "fund.portfolio.bond-history",
	"stock-report-dates":   "fund.portfolio.stock-report-dates",
	"bond-report-dates":    "fund.portfolio.bond-report-dates",
	"holders":              "fund.holders.detail",
	"holders-top":          "fund.holders.top",
	"manager-style":        "fund.managers.investment-style",
	"manager-performance":  "fund.managers.performance",
	"manager-experience":   "fund.managers.experience",
	"manager":              "fund.managers.detail",
	"company":              "fund.companies.detail",
	"dividends":            "fund.corporate-actions.dividends",
	"diagnostics":          "fund.diagnostics.detail",
	"financial-indicators": "fund.financials.indicators",
	"financial-income":     "fund.financials.income-statements",
	"financial-balance":    "fund.financials.balance-sheets",
	"quota-summary":        "fund.quota.summary",
	"quota-list":           "fund.quota.list",
	"offerings":            "fund.offerings.list",
	"news":                 "fund.news.article-list",
	"backtest-indicators":  "fund.backtest.indicators",
	"backtest-result":      "fund.backtest.result",
	"etf-flow":             "fund.etf-flow.net-inflow-historical",
}

// fuyaoFundView 返回 view 对应的端点 ID（未知视图给出可用列表）。
func fuyaoFundView(view string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(view))
	if key == "" {
		key = "profile"
	}
	endpoint, ok := fuyaoFundViews[key]
	if !ok {
		keys := make([]string, 0, len(fuyaoFundViews))
		for k := range fuyaoFundViews {
			keys = append(keys, k)
		}
		return "", fmt.Errorf("%w: 未知基金视图 %q（可用：%s）", contract.ErrFuyaoUnsupported, view, strings.Join(keys, ", "))
	}
	return endpoint, nil
}

// FuyaoFundView 执行一次基金视图查询。
// params 的键会交给目录校验（例如 thscode/manager_id/company_id/tab/subscribe）。
func (s *Service) FuyaoFundView(ctx context.Context, view string, params map[string]string, limit int) (contract.FuyaoData, error) {
	endpoint, err := fuyaoFundView(view)
	if err != nil {
		return contract.FuyaoData{}, err
	}
	return s.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: limit})
}

// FuyaoFundSnapshot 取场内基金（ETF/LOF）行情快照（单次最多 100 个代码）。
func (s *Service) FuyaoFundSnapshot(ctx context.Context, codes []string) ([]contract.FuyaoQuoteRow, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("datasource: codes 不能为空")
	}
	thsCodes := make([]string, 0, len(codes))
	for _, raw := range codes {
		raw = strings.ToUpper(strings.TrimSpace(raw))
		if raw != "" {
			thsCodes = append(thsCodes, raw)
		}
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "fund.market.snapshot",
		Params:   map[string]string{"thscode": strings.Join(thsCodes, ",")},
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoQuoteRow](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 基金快照字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoFundKline 取场内基金历史日线（批量最多 10 个，仅 1d、近 5 年窗口）。
func (s *Service) FuyaoFundKline(ctx context.Context, codes []string, limit int) ([]contract.FuyaoBarsSeries, error) {
	return s.FuyaoKline(ctx, codes, "1d", time.Time{}, time.Time{}, "none", "fund", limit)
}
