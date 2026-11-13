package datasource

// 官方特色数据域：涨跌停池、连板天梯、涨停统计、热榜、个股异动、异动监控、
// 龙虎榜（榜单/机构/营业部）、筹码形态、新增概念、成交额预测。
//
// 权限：chip-shape-*、turnover-prediction 实测对外部 Key 返回 code=2004；
// 其余（涨跌停池、热榜、异动、龙虎榜）外部 Key 可用。
//
// 口径：涨跌幅、封板率等均为百分数原值；seal_money/max_seal_money 单位元；
// heat 是上游热度字符串，不是金额。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoLimitPoolPath 把 pool 映射成官方路径与默认排序字段。
func fuyaoLimitPoolPath(pool string) (path, sortField string, err error) {
	switch strings.ToLower(strings.TrimSpace(pool)) {
	case "", "up", "limit-up", "zt":
		return "/api/a-share/special-data/limit-up-pool", "continue_day_cnt", nil
	case "down", "limit-down", "dt":
		return "/api/a-share/special-data/limit-down-pool", "last_limit_time", nil
	case "break", "broken", "zb":
		return "/api/a-share/special-data/limit-break-pool", "price_change_ratio_pct", nil
	}
	return "", "", fmt.Errorf("%w: pool 只支持 up/down/break，实际 %q", contract.ErrFuyaoUnsupported, pool)
}

// FuyaoLimitRows 取官方涨停/跌停/炸板池（pool=up/down/break），最多 limit 条。
// 涨停池字段最全（连板数、封单额、涨停原因）；跌停/炸板池复用同一结构，缺失字段为空。
func (s *Service) FuyaoLimitRows(ctx context.Context, pool string, limit int) ([]contract.FuyaoLimitRow, error) {
	path, sortField, err := fuyaoLimitPoolPath(pool)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	ep := "a-share.special-data." + strings.TrimPrefix(strings.TrimPrefix(path, "/api/a-share/special-data/"), "/")
	// 目录 ID 与路径一一对应，上面的拼接只为避免调用方依赖具体端点名。
	if _, ok := fuyaoEndpointByID(ep); !ok {
		return nil, fmt.Errorf("%w: %s", contract.ErrFuyaoUnsupported, ep)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: ep,
		Params: map[string]string{
			"size":       fmt.Sprintf("%d", limit),
			"sort_field": sortField,
			"sort_dir":   "desc",
		},
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoLimitRow](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 涨跌停池字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoLimitLadder 取连板天梯（近 30 个交易日的连板分布）。
func (s *Service) FuyaoLimitLadder(ctx context.Context) ([]map[string]any, error) {
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{Endpoint: "a-share.special-data.limit-up-ladder"})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoLimitStatistics 取涨停强度与晋级统计（封板率、开板家数、连板家数等）。
func (s *Service) FuyaoLimitStatistics(ctx context.Context, date string) (map[string]any, error) {
	params := map[string]string{}
	if strings.TrimSpace(date) != "" {
		params["date"] = strings.TrimSpace(date)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.limit-up-statistics",
		Params:   params,
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

// FuyaoHotRows 取官方热榜。
//
// kind：skyrocket（飙升榜）/ hot（A 股热股榜）/ history（历史热股榜，取昨日）；
// period：day / hour（history 忽略）；limit 1..100。
func (s *Service) FuyaoHotRows(ctx context.Context, kind, period string, limit int) ([]contract.FuyaoHotRow, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	period = strings.ToLower(strings.TrimSpace(period))
	if period == "" {
		period = "day"
	}
	var (
		endpoint string
		params   = map[string]string{}
	)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "hot", "hot-stock":
		endpoint = "a-share.special-data.hot-stock-list"
		params["period"] = period
		params["limit"] = fmt.Sprintf("%d", limit)
	case "skyrocket", "rising":
		endpoint = "a-share.special-data.skyrocket-list"
		params["period"] = period
		params["limit"] = fmt.Sprintf("%d", limit)
	case "history", "hot-history":
		endpoint = "a-share.special-data.hot-stock-list-history"
		params["date"] = s.now().In(cstZone()).AddDate(0, 0, -1).Format("2006-01-02")
	default:
		return nil, fmt.Errorf("%w: kind 只支持 skyrocket/hot/history，实际 %q", contract.ErrFuyaoUnsupported, kind)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: limit})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoHotRow](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 热榜字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoHotRankTrend 取单只个股的热榜排名走势。
func (s *Service) FuyaoHotRankTrend(ctx context.Context, code, startDate, endDate string) ([]map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(startDate) == "" || strings.TrimSpace(endDate) == "" {
		return nil, fmt.Errorf("datasource: start_date/end_date 必填（YYYY-MM-DD）")
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.hot-stock-rank-trend",
		Params: map[string]string{
			"thscode":    parsed.THSCode(),
			"start_date": strings.TrimSpace(startDate),
			"end_date":   strings.TrimSpace(endDate),
		},
		Limit: 200,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoAnomalyList 取个股异动原因列表（tagCodes 为空查全部标签）。
func (s *Service) FuyaoAnomalyList(ctx context.Context, tagCodes []string, limit int) ([]map[string]any, error) {
	params := map[string]string{}
	if len(tagCodes) > 0 {
		params["tag_codes"] = strings.Join(tagCodes, ",")
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.anomaly-analysis-list",
		Params:   params,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoAnomalyByStock 按股票代码批量查异动原因（单次最多 50 个）。
func (s *Service) FuyaoAnomalyByStock(ctx context.Context, codes []string) ([]map[string]any, error) {
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
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.anomaly-analysis-stock",
		Params:   map[string]string{"thscodes": strings.Join(thsCodes, ",")},
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoAbnormalMonitor 取严重异动与重点监控记录（state=current/history）。
func (s *Service) FuyaoAbnormalMonitor(ctx context.Context, state string, limit int) ([]map[string]any, error) {
	if state = strings.ToLower(strings.TrimSpace(state)); state == "" {
		state = "current"
	}
	if state != "current" && state != "history" {
		return nil, fmt.Errorf("datasource: state 只支持 current/history")
	}
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.abnormal-monitor-list",
		Params:   map[string]string{"state": state, "size": fmt.Sprintf("%d", limit)},
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoDragonTiger 取龙虎榜（boardType=all/org/hot_money，date 为空走最近交易日）。
func (s *Service) FuyaoDragonTiger(ctx context.Context, boardType, date string) (map[string]any, error) {
	params := map[string]string{}
	if bt := strings.TrimSpace(boardType); bt != "" {
		params["board_type"] = bt
	}
	if d := strings.TrimSpace(date); d != "" {
		params["date"] = d
	}
	// 龙虎榜 data 同时含 stock_items 与 hot_money_items；通用层只会摊平其中一个，
	// 这里直接解包原始对象，避免丢失游资榜。
	var raw map[string]any
	if err := s.fuyaoGetWith(ctx, "/api/a-share/special-data/dragon-tiger-list", params, &raw); err != nil {
		if errors.Is(err, contract.ErrFuyaoPermission) {
			if data, ok := s.fuyaoPermissionData(ctx, contract.FuyaoQuery{
				Endpoint: "a-share.special-data.dragon-tiger-list", Params: params,
			}); ok {
				return data, nil
			}
		}
		return nil, err
	}
	// 控制输出规模：龙虎榜单日通常 <100 只，超过 100 截断并标注。
	if items, ok := raw["stock_items"].([]any); ok && len(items) > 100 {
		raw["stock_items"] = items[:100]
		raw["truncated"] = true
	}
	return raw, nil
}

// fuyaoPermissionData 在原始解包失败时补一次权限探测：若错误是 2004/2003，
// 返回 permission=client 的说明对象，避免上层把「无权限」当成网络故障。
func (s *Service) fuyaoPermissionData(ctx context.Context, query contract.FuyaoQuery) (map[string]any, bool) {
	data, err := s.FuyaoQuery(ctx, query)
	if err != nil || data.Permission == "" {
		return nil, false
	}
	return map[string]any{
		"permission": data.Permission,
		"note":       data.Note,
		"items":      []any{},
	}, true
}

// FuyaoDragonTigerInstitution 取个股龙虎榜机构交易详情。
func (s *Service) FuyaoDragonTigerInstitution(ctx context.Context, code, date string, rangeDays int) ([]map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	params := map[string]string{"thscode": parsed.THSCode()}
	if strings.TrimSpace(date) != "" {
		params["date"] = strings.TrimSpace(date)
	}
	if rangeDays == 1 || rangeDays == 3 {
		params["range_days"] = fmt.Sprintf("%d", rangeDays)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.dragon-tiger-stock-institution",
		Params:   params,
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoDepartmentClassicRecord 取营业部经典战绩。
func (s *Service) FuyaoDepartmentClassicRecord(ctx context.Context, departmentID string) (map[string]any, error) {
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.dragon-tiger-department-classic-record",
		Params:   map[string]string{"department_id": strings.TrimSpace(departmentID)},
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

// FuyaoDepartmentHistory 取营业部历史上榜记录。
func (s *Service) FuyaoDepartmentHistory(ctx context.Context, departmentID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.dragon-tiger-department-history",
		Params: map[string]string{
			"department_id": strings.TrimSpace(departmentID),
			"size":          fmt.Sprintf("%d", limit),
		},
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoChipShape 取筹码形态股票（shape 必填；该能力客户端专用，2004 优雅降级）。
func (s *Service) FuyaoChipShape(ctx context.Context, shape, date string, limit int) ([]map[string]any, error) {
	params := map[string]string{"shape": strings.TrimSpace(shape)}
	if strings.TrimSpace(date) != "" {
		params["date"] = strings.TrimSpace(date)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.chip-shape-stocks",
		Params:   params,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoNewConcepts 取同花顺新增概念（客户端专用）。
func (s *Service) FuyaoNewConcepts(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 15 {
		limit = 15
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share-index.baseinfo.new-concepts",
		Params:   map[string]string{"size": fmt.Sprintf("%d", limit)},
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoTurnoverPrediction 取市场成交额预测快照（客户端专用）。
func (s *Service) FuyaoTurnoverPrediction(ctx context.Context) (map[string]any, error) {
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.special-data.turnover-prediction-snapshot",
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
