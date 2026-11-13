package datasource

// 官方财务域：三大报表（单标的 + 批量 ≤10）与五类财务指标。
//
// 单位：报表金额单位「元」；财务指标 value 是**上游原始数值字符串**（保留精度），
// 百分比类指标按百分数值表达（"89.12" 表示 89.12%），不要转成 0.8912。
// 报告期 period：annual（仅年报）/quarterly（每个季度末）；indicators 用 report=yyyy-1..4。

import (
	"context"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoFinancialsEndpoint 把报表类型映射成端点 ID。
func fuyaoFinancialsEndpoint(kind string, batch bool) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "", "income", "income-statements", "利润表":
		if batch {
			return "a-share.financials.income-statements-batch", nil
		}
		return "a-share.financials.income-statements", nil
	case "balance", "balance-sheets", "资产负债表":
		if batch {
			return "a-share.financials.balance-sheets-batch", nil
		}
		return "a-share.financials.balance-sheets", nil
	case "cash", "cash-flow", "cash-flow-statements", "现金流量表":
		if batch {
			return "a-share.financials.cash-flow-statements-batch", nil
		}
		return "a-share.financials.cash-flow-statements", nil
	}
	return "", fmt.Errorf("%w: kind 只支持 income/balance/cash，实际 %q", contract.ErrFuyaoUnsupported, kind)
}

// FuyaoFinancials 取单只 A 股的利润表/资产负债表/现金流量表。
// period=annual/quarterly；limit 1..20（与 start/end 互斥）。
func (s *Service) FuyaoFinancials(ctx context.Context, kind, code, period string, limit int) ([]map[string]any, error) {
	endpoint, err := fuyaoFinancialsEndpoint(kind, false)
	if err != nil {
		return nil, err
	}
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	ths := parsed.THSCode()
	if ths == "" {
		return nil, fmt.Errorf("%w: %s 无法映射成官方 thscode", contract.ErrFuyaoUnsupported, code)
	}
	if period = strings.ToLower(strings.TrimSpace(period)); period == "" {
		period = "annual"
	}
	if period != "annual" && period != "quarterly" {
		return nil, fmt.Errorf("datasource: period 只支持 annual/quarterly")
	}
	if limit <= 0 || limit > 20 {
		limit = 4
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: endpoint,
		Params: map[string]string{
			"thscode": ths,
			"period":  period,
			"limit":   fmt.Sprintf("%d", limit),
		},
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoFinancialsBatch 批量取多只 A 股的同类报表（单次最多 10 个代码，自动分批）。
// 返回官方分组结构（thscode + item[]），不把不同公司的报表混在一张表里。
func (s *Service) FuyaoFinancialsBatch(ctx context.Context, kind string, codes []string, period string, limit int) ([]map[string]any, error) {
	endpoint, err := fuyaoFinancialsEndpoint(kind, true)
	if err != nil {
		return nil, err
	}
	thsCodes := make([]string, 0, len(codes))
	for _, raw := range codes {
		code, err := parseFuyaoCode(raw)
		if err != nil {
			return nil, err
		}
		if ths := code.THSCode(); ths != "" {
			thsCodes = append(thsCodes, ths)
		}
	}
	if len(thsCodes) == 0 {
		return nil, fmt.Errorf("datasource: codes 不能为空")
	}
	if period = strings.ToLower(strings.TrimSpace(period)); period == "" {
		period = "annual"
	}
	if limit <= 0 || limit > 20 {
		limit = 4
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: endpoint,
		Params: map[string]string{
			"thscodes": strings.Join(thsCodes, ","),
			"period":   period,
			"limit":    fmt.Sprintf("%d", limit),
		},
		Limit: 200,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoFinancialIndicators 取单只 A 股某报告期的五类财务指标。
// report 格式 yyyy-1..4（1 一季报、2 中报、3 三季报、4 年报）。
func (s *Service) FuyaoFinancialIndicators(ctx context.Context, code, report string) (map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	report = strings.TrimSpace(report)
	if report == "" {
		return nil, fmt.Errorf("datasource: report 必填，格式 yyyy-1..4（如 2025-3）")
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.financials.indicators",
		Params: map[string]string{
			"thscode": parsed.THSCode(),
			"report":  report,
		},
	})
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
