package datasource

// 官方集合竞价域：竞价快照（live/final）与短线风向标竞价基准。
//
// 口径：auction_pct / auction_turnover_pct / auction_yesterday_ratio_pct 都是
// 百分数原值（0.35 表示 +0.35%）；auction_volume 单位「股」；auction_amount 与
// float_market_cap 单位「元」。

import (
	"context"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// FuyaoAuction 取一批 A 股的集合竞价快照（单次最多 100 个 thscode，自动分批）。
func (s *Service) FuyaoAuction(ctx context.Context, codes []string, stage string) ([]contract.FuyaoAuctionRow, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("datasource: codes 不能为空")
	}
	thsCodes := make([]string, 0, len(codes))
	for _, raw := range codes {
		code, err := parseFuyaoCode(raw)
		if err != nil {
			return nil, err
		}
		ths := code.THSCode()
		if ths == "" {
			return nil, fmt.Errorf("%w: %s 无法映射成官方 thscode", contract.ErrFuyaoUnsupported, raw)
		}
		thsCodes = append(thsCodes, ths)
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if stage == "" {
		stage = "final"
	}
	if stage != "live" && stage != "final" {
		return nil, fmt.Errorf("datasource: stage 只支持 live/final，实际 %q", stage)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.auction.snapshot",
		Params: map[string]string{
			"thscodes": strings.Join(thsCodes, ","),
			"stage":    stage,
		},
		Limit: fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoAuctionRow](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 竞价快照字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoAuctionBenchmark 取短线风向标竞价基准（date 为空时用上海时区当日）。
func (s *Service) FuyaoAuctionBenchmark(ctx context.Context, date string) ([]map[string]any, error) {
	params := map[string]string{}
	if strings.TrimSpace(date) != "" {
		params["date"] = strings.TrimSpace(date)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.auction.short-term-benchmark",
		Params:   params,
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoAuctionOpportunities 取集合竞价个股机会（warn_types 为空时查全部类型）。
func (s *Service) FuyaoAuctionOpportunities(ctx context.Context, date string, warnTypes []string, limit int) ([]map[string]any, error) {
	params := map[string]string{}
	if strings.TrimSpace(date) != "" {
		params["date"] = strings.TrimSpace(date)
	}
	if len(warnTypes) > 0 {
		params["warn_types"] = strings.Join(warnTypes, ",")
	}
	if limit <= 0 {
		limit = 20
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.auction-stock-opportunities",
		Params:   params,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoAuctionDates 返回集合竞价可选交易日列表。
func (s *Service) FuyaoAuctionDates(ctx context.Context) ([]string, error) {
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.auction-opportunity-dates",
	})
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(data.Items))
	for _, item := range data.Items {
		if d := mapString(item, "date"); d != "" {
			dates = append(dates, d)
		}
	}
	return dates, nil
}
