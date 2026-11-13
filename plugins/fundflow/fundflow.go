// Package fundflow 提供资金流面板：个股资金流、板块/概念资金流排名与板块列表。
//
// 数据口径（与 internal/datasource 一致，全部来自新浪 MoneyFlow）：
//   - 个股当日：ssi_ssfx_flzjtj，超大/大/中/小单净额用 rN_in - rN_out，
//     主力净额 = 超大单 + 大单（与旧东财同名口径等价）；
//   - 个股多日：ssl_qsfx_zjlrqs，只给「超大单净额 r0_net」，没有大/中/小拆分，
//     所以多日序列里的 MainNet 是**超大单净额**而不是「主力净额」——与当日接口口径不同，
//     不要把两条曲线当成同一指标直接比较；
//   - 板块/概念：ssl_bkzj_bk 的资金净额榜（netamount，不是主力净额），方向由本地重排；
//     板块多日走 ssl_bkzj_zjlrqs?bankuai=<category>。
//
// 板块代码是新浪 category（new_dzxx / gn_hwqc），旧东财 BK1201 已下线。
// 金额单位统一为「元」，涨跌幅为百分数。
package fundflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

const (
	pluginID         = "fundflow"
	defaultKind      = "sector"
	defaultRankLimit = 10
	maxRankLimit     = 100
	// 多日资金流与批量快照的边界（契约见 docs/ITERATION-2026-11.md §3.2）：
	// 默认 20 日、上限 120 日；批量一次最多 50 只。
	defaultHistoryDays = 20
	maxHistoryDays     = 120
	maxBatchCodes      = 50
	// 板块/概念多日资金流：单次批量最多 20 个新浪 category（上游并发有限，再多会把上下文和上游都压满）。
	maxSectorBatchCodes = 20
)

func init() { all.Register(New) }

// New 是插件工厂（签名由 docs/API.md §8 冻结）。
func New() plugin.Plugin { return &Plugin{} }

// Plugin 是资金流插件。
type Plugin struct {
	plugin.Base
	ctx *plugin.Context
}

var _ plugin.Plugin = (*Plugin)(nil)

// Manifest 描述插件身份与贡献点。
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "资金流向",
		Version:     "1.0.0",
		Description: "个股与板块/概念资金流排名、列表与多日趋势（只读，不涉及交易）",
		Tags:        []string{"flow", "market"},
		Provides: []string{
			"method:stock", "method:rank", "method:sectors", "method:history", "method:batch",
			"method:sectorHistory", "method:sectorBatch",
			"tool:flow.stock", "tool:flow.rank", "tool:sector.list", "tool:flow.history",
			"tool:sector.history", "ui:fundflow",
		},
	}
}

// Activate 注册方法、工具与面板。
func (p *Plugin) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	methods := []plugin.Method{
		{Name: "stock", Description: "个股资金流", Fn: p.methodStock},
		{Name: "rank", Description: "板块/概念资金流排名", Fn: p.methodRank},
		{Name: "sectors", Description: "板块/概念列表", Fn: p.methodSectors},
		{Name: "history", Description: "个股多日资金流", Fn: p.methodHistory},
		{Name: "batch", Description: "批量个股资金流快照", Fn: p.methodBatch},
		{Name: "sectorHistory", Description: "板块/概念多日资金流", Fn: p.methodSectorHistory},
		{Name: "sectorBatch", Description: "批量板块/概念多日资金流", Fn: p.methodSectorBatch},
	}
	for _, m := range methods {
		if err := ctx.Methods.Add(m); err != nil {
			return err
		}
	}

	tools := []plugin.Tool{
		{
			Name:    "flow.stock",
			Title:   "个股资金流",
			Summary: "看某只票今日主力与超大单净额，判断资金进出时用",
			Tags:    []string{"flow", "market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"code":{"type":"string","minLength":1,"description":"股票代码，如 600000"}},
				"required":["code"],
				"additionalProperties":false
			}`),
			Run: p.toolStock,
		},
		{
			Name:    "flow.rank",
			Title:   "板块资金流排名",
			Summary: "看资金流入或流出最多的板块与概念，找主线时用",
			Tags:    []string{"flow", "market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"kind":{"type":"string","enum":["sector","concept"],"description":"sector 行业板块（默认），concept 概念"},
					"direction":{"type":"string","enum":["in","out","all"],"description":"in 主力净流入榜（默认），out 主力净流出榜，all 按净额降序"},
					"limit":{"type":"integer","minimum":1,"maximum":100,"description":"返回条数，默认 10"}
				},
				"additionalProperties":false
			}`),
			Run: p.toolRank,
		},
		{
			Name:    "sector.list",
			Title:   "板块列表",
			Summary: "列出板块或概念的涨跌幅与主力净额，做横向对比时用",
			Tags:    []string{"flow", "market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"kind":{"type":"string","enum":["sector","concept"],"description":"sector 行业板块（默认），concept 概念"}
				},
				"additionalProperties":false
			}`),
			Run: p.toolSectors,
		},
		{
			Name:    "flow.history",
			Title:   "个股多日资金流",
			Summary: "看个股近 N 日主力净流入，判断资金趋势时用",
			Tags:    []string{"flow", "market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","minLength":1,"description":"股票代码，如 600519"},
					"days":{"type":"integer","minimum":1,"maximum":120,"description":"回看交易日数，默认 20"}
				},
				"required":["code"],
				"additionalProperties":false
			}`),
			Run: p.toolHistory,
		},
		{
			Name:    "sector.history",
			Title:   "板块多日资金流",
			Summary: "看板块/概念近 N 日主力净流入趋势",
			Tags:    []string{"flow", "market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","minLength":1,"description":"板块代码，如 BK1201"},
					"days":{"type":"integer","minimum":1,"maximum":120,"description":"回看交易日数，默认 20"}
				},
				"required":["code"],
				"additionalProperties":false
			}`),
			Run: p.toolSectorHistory,
		},
	}
	for _, t := range tools {
		if err := ctx.Tools.Add(t); err != nil {
			return err
		}
	}

	return ctx.UI.Add(plugin.UISlot{
		ID:        "fundflow",
		Title:     "资金流向",
		Icon:      "waves",
		Group:     "行情",
		Component: "fundflow",
		Order:     20,
	})
}

type stockArgs struct {
	Code string `json:"code"`
}

func (p *Plugin) methodStock(ctx context.Context, payload json.RawMessage) (any, error) {
	var args stockArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(args.Code)
	if code == "" {
		return nil, fmt.Errorf("code 不能为空")
	}
	flow, err := p.ctx.Data.StockFundFlow(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("取 %s 资金流失败: %w", code, err)
	}
	return flow, nil
}

type rankArgs struct {
	Kind      string `json:"kind"`
	Limit     int    `json:"limit"`
	Direction string `json:"direction"`
}

func (p *Plugin) methodRank(ctx context.Context, payload json.RawMessage) (any, error) {
	var args rankArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	kind, err := normalizeKind(args.Kind)
	if err != nil {
		return nil, err
	}
	direction, err := normalizeDirection(args.Direction)
	if err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultRankLimit
	}
	if limit > maxRankLimit {
		limit = maxRankLimit
	}
	var flows []contract.FundFlow
	if service, ok := p.rankService(); ok {
		flows, err = service.FundFlowRankBy(ctx, kind, direction, limit)
	} else if direction == "out" {
		return nil, fmt.Errorf("当前数据层不支持主力净流出榜（datasource 版本不匹配），请升级应用后重试")
	} else {
		flows, err = p.ctx.Data.FundFlowRank(ctx, kind, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("取板块资金流排名失败: %w", err)
	}
	if flows == nil {
		flows = []contract.FundFlow{}
	}
	return flows, nil
}

type sectorsArgs struct {
	Kind string `json:"kind"`
}

func (p *Plugin) methodSectors(ctx context.Context, payload json.RawMessage) (any, error) {
	var args sectorsArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	kind, err := normalizeKind(args.Kind)
	if err != nil {
		return nil, err
	}
	sectors, err := p.ctx.Data.Sectors(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("取板块列表失败: %w", err)
	}
	return sectors, nil
}

// ---------- 工具 ----------

func (p *Plugin) toolStock(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodStock(ctx, payload)
}

func (p *Plugin) toolRank(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodRank(ctx, payload)
	if err != nil {
		return nil, err
	}
	flows := reply.([]contract.FundFlow)
	return map[string]any{"count": len(flows), "items": flows}, nil
}

func (p *Plugin) toolSectors(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodSectors(ctx, payload)
	if err != nil {
		return nil, err
	}
	sectors := reply.([]contract.Sector)
	return map[string]any{"count": len(sectors), "items": sectors}, nil
}

// normalizeDirection 归一资金榜方向：空值默认 in，只接受 in/out/all。
func normalizeDirection(direction string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "", "in":
		return "in", nil
	case "out":
		return "out", nil
	case "all":
		return "all", nil
	default:
		return "", fmt.Errorf("direction 只支持 in、out 或 all，实际 %q", direction)
	}
}

// normalizeKind 空值默认 sector —— product 明确要求空参数不报错。
func normalizeKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "sector", "bk", "industry":
		return defaultKind, nil
	case "concept":
		return "concept", nil
	default:
		return "", fmt.Errorf("kind 只支持 sector 或 concept，实际 %q", kind)
	}
}

func decodeArgs(raw json.RawMessage, dst any) error {
	payload := strings.TrimSpace(string(raw))
	if payload == "" {
		payload = "{}"
	}
	if err := json.Unmarshal([]byte(payload), dst); err != nil {
		return fmt.Errorf("参数格式错误: %w", err)
	}
	return nil
}

// flowRankService 是数据层的可选扩展：按方向（净流入/净流出）取资金榜。
type flowRankService interface {
	FundFlowRankBy(ctx context.Context, kind, direction string, limit int) ([]contract.FundFlow, error)
}

// ---------- 多日资金流 / 批量快照 ----------

// flowHistoryService 是数据层的可选扩展（结构化断言）：实现方提供多日序列与批量快照。
type flowHistoryService interface {
	FundFlowHistory(ctx context.Context, code string, days int) (contract.FundFlowHistory, error)
	FundFlowSnapshot(ctx context.Context, codes []string) ([]contract.FundFlow, error)
}

type historyArgs struct {
	Code string `json:"code"`
	Days int    `json:"days"`
}

// methodHistory 返回个股最近 days 个交易日的资金流明细（单位：元）。
func (p *Plugin) methodHistory(ctx context.Context, payload json.RawMessage) (any, error) {
	var args historyArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(args.Code)
	if code == "" {
		return nil, fmt.Errorf("code 不能为空")
	}
	days := args.Days
	if days <= 0 {
		days = defaultHistoryDays
	}
	if days > maxHistoryDays {
		days = maxHistoryDays
	}
	service, ok := p.historyService()
	if !ok {
		return nil, fmt.Errorf("当前数据层不支持多日资金流（datasource 版本不匹配），请升级应用后重试")
	}
	history, err := service.FundFlowHistory(ctx, code, days)
	if err != nil {
		return nil, fmt.Errorf("取 %s 多日资金流失败: %w", code, err)
	}
	return history, nil
}

type batchArgs struct {
	Codes []string `json:"codes"`
}

// batchReply 与契约 §3.2 一致：items 是成功项，date/source 取自首条。
type batchReply struct {
	Items  []contract.FundFlow `json:"items"`
	Date   string              `json:"date"`
	Source string              `json:"source"`
}

// methodBatch 批量返回个股当日资金流；数据层负责 60s 缓存与逐只降级。
func (p *Plugin) methodBatch(ctx context.Context, payload json.RawMessage) (any, error) {
	var args batchArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	codes := normalizeCodes(args.Codes)
	if len(codes) == 0 {
		return nil, fmt.Errorf("codes 不能为空（最多 %d 只）", maxBatchCodes)
	}
	if len(codes) > maxBatchCodes {
		return nil, fmt.Errorf("一次最多查询 %d 只，实际 %d 只", maxBatchCodes, len(codes))
	}
	service, ok := p.historyService()
	if !ok {
		return nil, fmt.Errorf("当前数据层不支持批量资金流（datasource 版本不匹配），请升级应用后重试")
	}
	items, err := service.FundFlowSnapshot(ctx, codes)
	if err != nil && len(items) == 0 {
		return nil, fmt.Errorf("批量取资金流失败: %w", err)
	}
	if err != nil {
		// ErrPartial：有成功项就照常返回，把缺的代码写进日志（契约里这个返回没有 warnings 字段）。
		p.ctx.Log.Debug("批量资金流部分失败", "error", err)
	}
	if items == nil {
		// 保证前端拿到的是数组而不是 null（空数组可以直接 .length）。
		items = []contract.FundFlow{}
	}
	reply := batchReply{Items: items, Source: "sina"}
	if len(items) > 0 {
		reply.Date = items[0].Date
		if items[0].Source != "" {
			reply.Source = items[0].Source
		}
	}
	return reply, nil
}

// toolHistory 给 LLM 用：返回序列本身（点数有限，不会吃满上下文）。
func (p *Plugin) toolHistory(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodHistory(ctx, payload)
}

// ---------- 板块/概念多日资金流 ----------

// sectorFlowService 是数据层的可选扩展（结构化断言）：实现方提供板块/概念多日序列与批量。
// 单独成一个接口而不是塞进 flowHistoryService，旧数据层仍可只实现个股部分。
type sectorFlowService interface {
	SectorFundFlowHistory(ctx context.Context, code string, days int) (contract.FundFlowHistory, error)
	SectorFundFlowBatch(ctx context.Context, codes []string, days int) ([]contract.FundFlowHistory, error)
}

type sectorHistoryArgs struct {
	Code string `json:"code"`
	Days int    `json:"days"`
}

// methodSectorHistory 返回板块/概念最近 days 个交易日的资金流明细（单位：元）。
func (p *Plugin) methodSectorHistory(ctx context.Context, payload json.RawMessage) (any, error) {
	var args sectorHistoryArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(args.Code)
	if code == "" {
		return nil, fmt.Errorf("code 不能为空（新浪板块代码，如 new_dzxx）")
	}
	days := args.Days
	if days <= 0 {
		days = defaultHistoryDays
	}
	if days > maxHistoryDays {
		days = maxHistoryDays
	}
	service, ok := p.sectorService()
	if !ok {
		return nil, fmt.Errorf("当前数据层不支持板块多日资金流（datasource 版本不匹配），请升级应用后重试")
	}
	history, err := service.SectorFundFlowHistory(ctx, code, days)
	if err != nil {
		return nil, fmt.Errorf("取板块 %s 多日资金流失败: %w", code, err)
	}
	return history, nil
}

type sectorBatchArgs struct {
	Codes []string `json:"codes"`
	Days  int      `json:"days"`
}

// sectorBatchReply 只包 items：批量结果的代码/名称/天数都在每条 FundFlowHistory 里。
type sectorBatchReply struct {
	Items []contract.FundFlowHistory `json:"items"`
}

// methodSectorBatch 批量返回板块/概念多日资金流（codes ≤20）；部分失败照常返回成功项。
func (p *Plugin) methodSectorBatch(ctx context.Context, payload json.RawMessage) (any, error) {
	var args sectorBatchArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	codes := normalizeCodes(args.Codes)
	if len(codes) == 0 {
		return nil, fmt.Errorf("codes 不能为空（最多 %d 个，形如 BK1201）", maxSectorBatchCodes)
	}
	if len(codes) > maxSectorBatchCodes {
		return nil, fmt.Errorf("一次最多查询 %d 个板块，实际 %d 个", maxSectorBatchCodes, len(codes))
	}
	days := args.Days
	if days <= 0 {
		days = defaultHistoryDays
	}
	if days > maxHistoryDays {
		days = maxHistoryDays
	}
	service, ok := p.sectorService()
	if !ok {
		return nil, fmt.Errorf("当前数据层不支持板块多日资金流（datasource 版本不匹配），请升级应用后重试")
	}
	items, err := service.SectorFundFlowBatch(ctx, codes, days)
	if err != nil && len(items) == 0 {
		return nil, fmt.Errorf("批量取板块资金流失败: %w", err)
	}
	if err != nil {
		// ErrPartial：有成功项就照常返回，把缺的代码写进日志。
		p.ctx.Log.Debug("批量板块资金流部分失败", "error", err)
	}
	if items == nil {
		// 保证前端拿到的是数组而不是 null。
		items = []contract.FundFlowHistory{}
	}
	return sectorBatchReply{Items: items}, nil
}

// toolSectorHistory 给 LLM 用：返回单个板块的区间序列。
func (p *Plugin) toolSectorHistory(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodSectorHistory(ctx, payload)
}

// rankService 取数据层的资金榜扩展；没有时退回旧 FundFlowRank（仅支持净流入）。
func (p *Plugin) rankService() (flowRankService, bool) {
	if p.ctx == nil || p.ctx.Data == nil {
		return nil, false
	}
	service, ok := p.ctx.Data.(flowRankService)
	return service, ok
}

// historyService 取数据层的可选扩展；没有就由调用方给中文降级说明。
func (p *Plugin) historyService() (flowHistoryService, bool) {
	if p.ctx == nil || p.ctx.Data == nil {
		return nil, false
	}
	service, ok := p.ctx.Data.(flowHistoryService)
	return service, ok
}

// sectorService 取数据层的板块多日资金流扩展；没有就由调用方给中文降级说明。
func (p *Plugin) sectorService() (sectorFlowService, bool) {
	if p.ctx == nil || p.ctx.Data == nil {
		return nil, false
	}
	service, ok := p.ctx.Data.(sectorFlowService)
	return service, ok
}

// normalizeCodes 去空白、去重并保持入参顺序。
func normalizeCodes(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, code := range raw {
		text := strings.TrimSpace(code)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}
