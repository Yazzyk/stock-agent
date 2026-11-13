package datasource

// 官方基础域：交易日历、标的检索、标的列表分页、估值快照。
//
// 说明：快照不返回中文名，检索/列表接口是官方给的名字来源；本层会把名称写入
// market_cache（与腾讯/新浪补名同一份缓存），下一次自选列表可以直接离线展示。

import (
	"context"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// FuyaoCalendar 取 A 股交易日历（start/end 格式 yyyyMMdd；都为空时取近一年）。
// 返回 date_ms（Asia/Shanghai 当日零点毫秒戳）与 yyyyMMdd。
func (s *Service) FuyaoCalendar(ctx context.Context, startDate, endDate string) ([]contract.FuyaoCalendarDay, error) {
	params := map[string]string{}
	if strings.TrimSpace(startDate) != "" {
		params["start_date"] = strings.TrimSpace(startDate)
	}
	if strings.TrimSpace(endDate) != "" {
		params["end_date"] = strings.TrimSpace(endDate)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.calendar.trading-days",
		Params:   params,
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoCalendarDay](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 交易日历字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoSearchTickers 按关键词检索证券/基金/期货/期权标的（官方 q 接口，limit ≤50）。
// 命中后会把名称写入本地名称缓存，供快照补名使用。
func (s *Service) FuyaoSearchTickers(ctx context.Context, keyword, assetType string, limit int) ([]contract.FuyaoTickerRef, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("datasource: keyword 不能为空")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	params := map[string]string{"q": keyword, "limit": fmt.Sprintf("%d", limit)}
	if strings.TrimSpace(assetType) != "" {
		params["asset_type"] = strings.TrimSpace(assetType)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "meta.tickers.search",
		Params:   params,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoTickerRef](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 标的检索字段解析失败: %w", err)
	}
	for _, row := range rows {
		if row.Name == "" {
			continue
		}
		if code, parseErr := parseFuyaoCode(row.Thscode); parseErr == nil {
			s.rememberName(code, row.Name)
			s.saveCachedName(code, row.Name)
		}
	}
	return rows, nil
}

// FuyaoTickers 分页拉取某类资产的代码表（asset_type 省略时返回全部类型）。
// 注意：limit 官方最大 10000，本方法默认 1000；全量拉取请自行翻页。
func (s *Service) FuyaoTickers(ctx context.Context, assetType string, limit, offset int) ([]contract.FuyaoTickerRef, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	if offset < 0 {
		offset = 0
	}
	params := map[string]string{
		"limit":  fmt.Sprintf("%d", limit),
		"offset": fmt.Sprintf("%d", offset),
	}
	if strings.TrimSpace(assetType) != "" {
		params["asset_type"] = strings.TrimSpace(assetType)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "meta.tickers.list",
		Params:   params,
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoTickerRef](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 标列表字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoCalendarText 把交易日历格式化成 yyyy-MM-dd 列表（工具输出用，避免前端再转）。
func FuyaoCalendarText(days []contract.FuyaoCalendarDay) []string {
	out := make([]string, 0, len(days))
	for _, day := range days {
		d := strings.TrimSpace(day.Date)
		if len(d) == 8 {
			out = append(out, d[0:4]+"-"+d[4:6]+"-"+d[6:8])
			continue
		}
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}
