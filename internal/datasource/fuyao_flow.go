package datasource

// 官方资金域：主力资金（实时/历史）与高频动向（历史/分时）。
//
// 权限事实（2026-11-13 实测）：capital-flow 与 high-frequency 对**外部 API Key
// 返回 code=2004（同花顺 AI 客户端专用）**。因此这里的首要职责不是「幻想能取到」，
// 而是把权限事实准确、可降级地暴露给上层：返回 contract.ErrFuyaoPermission，
// 由工具转成「该能力需要客户端」的可读说明，而不是让用户反复重试。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// FuyaoCapitalFlow 取单只 A 股的主力资金实时快照（单位：元）。
// 返回 MainNet = 超大单净额 + 大单净额，便于与现有「主力」口径对齐。
func (s *Service) FuyaoCapitalFlow(ctx context.Context, code string) (contract.FuyaoCapitalFlowRow, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return contract.FuyaoCapitalFlowRow{}, err
	}
	ths := parsed.THSCode()
	if ths == "" {
		return contract.FuyaoCapitalFlowRow{}, fmt.Errorf("%w: %s 无法映射成官方 thscode", contract.ErrFuyaoUnsupported, code)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.capital-flow.snapshot",
		Params:   map[string]string{"thscode": ths},
	})
	if err != nil {
		return contract.FuyaoCapitalFlowRow{}, err
	}
	raw := data.Raw
	if raw == nil {
		return contract.FuyaoCapitalFlowRow{}, fmt.Errorf("%w: 官方资金流返回空数据", ErrNoData)
	}
	side := func(key string) contract.FuyaoFlowSide {
		obj := mapObj(raw, key)
		return contract.FuyaoFlowSide{
			Inflow:  mapFloat(obj, "inflow_amount"),
			Outflow: mapFloat(obj, "outflow_amount"),
			Net:     mapFloat(obj, "net_amount"),
		}
	}
	row := contract.FuyaoCapitalFlowRow{
		Thscode:   ths,
		Timestamp: data.Timestamp,
		Super:     side("super_large"),
		Large:     side("large"),
		Medium:    side("medium"),
		Small:     side("small"),
		Source:    sourceFuyao,
	}
	row.MainNet = row.Super.Net + row.Large.Net
	return row, nil
}

// FuyaoCapitalFlowHistory 取单只 A 股的 1 分钟/日级资金流历史。
// 说明：官方历史接口只给 inflow/outflow，不给 net_amount；净额由本方法计算（流入-流出）。
func (s *Service) FuyaoCapitalFlowHistory(ctx context.Context, code, interval string, start, end time.Time) ([]map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	interval = strings.ToLower(strings.TrimSpace(interval))
	if interval == "" {
		interval = "1d"
	}
	if interval != "1m" && interval != "1d" {
		return nil, fmt.Errorf("datasource: interval 只支持 1m/1d，实际 %q", interval)
	}
	if end.IsZero() {
		end = s.now()
	}
	if start.IsZero() {
		if interval == "1m" {
			start = end.Add(-6 * 24 * time.Hour)
		} else {
			start = end.AddDate(-1, 0, 0)
		}
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.capital-flow.historical",
		Params: map[string]string{
			"thscode":  parsed.THSCode(),
			"interval": interval,
			"start":    fmt.Sprintf("%d", start.UnixMilli()),
			"end":      fmt.Sprintf("%d", end.UnixMilli()),
		},
		Limit: fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	// 补 net_amount（流入-流出），字段名与实时快照保持一致，前端可复用同一渲染。
	for _, item := range data.Items {
		for _, key := range []string{"super_large", "large", "medium", "small"} {
			obj := mapObj(item, key)
			if obj == nil {
				continue
			}
			if _, exists := obj["net_amount"]; !exists {
				obj["net_amount"] = mapFloat(obj, "inflow_amount") - mapFloat(obj, "outflow_amount")
			}
		}
	}
	return data.Items, nil
}

// FuyaoHighFrequency 取单只个股/指数的高频历史（interval=1d/1m）。
// 该能力同样属于客户端专用；无权限时返回 contract.ErrFuyaoPermission。
func (s *Service) FuyaoHighFrequency(ctx context.Context, code, interval string, start, end time.Time) ([]map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	interval = strings.ToLower(strings.TrimSpace(interval))
	if interval == "" {
		interval = "1d"
	}
	if interval != "1d" && interval != "1m" {
		return nil, fmt.Errorf("datasource: interval 只支持 1d/1m，实际 %q", interval)
	}
	if end.IsZero() {
		end = s.now()
	}
	if start.IsZero() {
		start = end.Add(-24 * time.Hour)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.high-frequency.historical",
		Params: map[string]string{
			"thscode":  parsed.THSCode(),
			"interval": interval,
			"start":    fmt.Sprintf("%d", start.UnixMilli()),
			"end":      fmt.Sprintf("%d", end.UnixMilli()),
		},
		Limit: fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoHighFrequencyIntraday 取单日高频分时（默认最近交易日）。
func (s *Service) FuyaoHighFrequencyIntraday(ctx context.Context, code, date string) ([]map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	params := map[string]string{"thscode": parsed.THSCode()}
	if strings.TrimSpace(date) != "" {
		params["date"] = strings.TrimSpace(date)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.high-frequency.intraday",
		Params:   params,
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}
