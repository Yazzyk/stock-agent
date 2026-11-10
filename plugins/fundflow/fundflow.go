// Package fundflow 提供资金流面板：个股资金流、板块/概念资金流排名与板块列表。
//
// 数据口径（与 internal/datasource 一致）：
//   - 个股：东财 fflow（主力/超大/大/中/小）优先，新浪 r0~r3 兜底；
//   - 板块/概念：东财 bkzj 排名（主力净流入降序）与 clist 列表（含涨跌幅）。
//
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
		Description: "个股资金流、板块与概念资金流排名和列表（只读，不涉及交易）",
		Tags:        []string{"flow", "market"},
		Provides: []string{
			"method:stock", "method:rank", "method:sectors",
			"tool:flow.stock", "tool:flow.rank", "tool:sector.list",
			"ui:fundflow",
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
			Summary: "看资金流入最多的板块或概念，找主线时用",
			Tags:    []string{"flow", "market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"kind":{"type":"string","enum":["sector","concept"],"description":"sector 行业板块（默认），concept 概念"},
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
	Kind  string `json:"kind"`
	Limit int    `json:"limit"`
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
	limit := args.Limit
	if limit <= 0 {
		limit = defaultRankLimit
	}
	if limit > maxRankLimit {
		limit = maxRankLimit
	}
	flows, err := p.ctx.Data.FundFlowRank(ctx, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("取板块资金流排名失败: %w", err)
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
