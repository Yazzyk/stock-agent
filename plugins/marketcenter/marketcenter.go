// Package marketcenter 提供 A 股市场中心面板：指数、市场宽度、涨停梯队与龙虎榜。
//
// 数据口径由 internal/datasource 的 Market* 方法负责（腾讯/新浪指数 + 新浪全市场快照统计涨跌家数
// + 同花顺涨停池与龙虎榜，失败按块降级）。本插件只做参数收敛、只读工具暴露与 UI 注册。
package marketcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/internal/datasource"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

const (
	pluginID             = "marketcenter"
	defaultLadder        = 50
	maxLadder            = 200
	defaultLongHu        = 50
	maxLongHu            = 200
	defaultSnapshotLimit = 50
)

func init() { all.Register(New) }

// New 是插件工厂（签名由 docs/API.md §8 冻结）。
func New() plugin.Plugin { return &Plugin{} }

// Plugin 是市场中心插件。
type Plugin struct {
	plugin.Base
	ctx *plugin.Context
}

var _ plugin.Plugin = (*Plugin)(nil)

// Manifest 描述插件身份与贡献点。
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "市场中心",
		Version:     "1.0.0",
		Description: "A 股指数、涨跌家数、涨停梯队与龙虎榜（只读，不涉及交易）",
		Tags:        []string{"market", "flow"},
		Provides: []string{
			"method:indices", "method:breadth", "method:ladder", "method:longhu", "method:hot", "method:snapshot",
			"tool:market.indices", "tool:market.breadth", "tool:market.ladder", "tool:market.longhu", "tool:market.hot",
			"ui:marketcenter",
		},
	}
}

// Activate 注册方法、工具与面板。
func (p *Plugin) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	methods := []plugin.Method{
		{Name: "indices", Description: "主要指数快照", Fn: p.methodIndices},
		{Name: "breadth", Description: "涨跌家数与涨跌停统计", Fn: p.methodBreadth},
		{Name: "ladder", Description: "涨停梯队", Fn: p.methodLadder},
		{Name: "longhu", Description: "龙虎榜", Fn: p.methodLongHu},
		{Name: "hot", Description: "同花顺热门榜（小时/日榜）", Fn: p.methodHot},
		{Name: "snapshot", Description: "市场中心数据快照", Fn: p.methodSnapshot},
	}
	for _, m := range methods {
		if err := ctx.Methods.Add(m); err != nil {
			return err
		}
	}

	tools := []plugin.Tool{
		{
			Name:    "market.indices",
			Title:   "主要指数",
			Summary: "看上证/深证/科创50/创业板指/北证50的点位与涨跌幅，判断大盘状态时用（只读）",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Run:     p.toolIndices,
		},
		{
			Name:    "market.breadth",
			Title:   "市场宽度",
			Summary: "看涨跌家数、涨停/跌停/炸板数与两市成交额，判断市场情绪与赚钱效应时用（只读）",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Run:     p.toolBreadth,
		},
		{
			Name:    "market.ladder",
			Title:   "涨停梯队",
			Summary: "看涨停股的连板数、封板时间与涨停原因，判断题材高度与主线时用（只读）",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"limit":{"type":"integer","minimum":1,"maximum":200,"description":"返回只数，默认 50"}},
				"additionalProperties":false
			}`),
			Run: p.toolLadder,
		},
		{
			Name:    "market.longhu",
			Title:   "龙虎榜",
			Summary: "看当日龙虎榜个股、上榜原因与净买入，判断资金博弈时用（只读）",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"limit":{"type":"integer","minimum":1,"maximum":200,"description":"返回只数，默认 50"}},
				"additionalProperties":false
			}`),
			Run: p.toolLongHu,
		},
		{
			Name:    "market.hot",
			Title:   "同花顺热门榜",
			Summary: "看同花顺小时榜/日榜的人气股、热度、排名变化与题材标签，判断市场关注度与情绪焦点时用（只读）",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"type":{"type":"string","enum":["hour","day"],"description":"hour 小时榜（默认），day 日榜"},
					"limit":{"type":"integer","minimum":1,"maximum":100,"description":"返回只数，默认 30"}
				},
				"additionalProperties":false
			}`),
			Run: p.toolHot,
		},
	}
	for _, t := range tools {
		if err := ctx.Tools.Add(t); err != nil {
			return err
		}
	}

	return ctx.UI.Add(plugin.UISlot{
		ID:        "marketcenter",
		Title:     "市场中心",
		Icon:      "pulse",
		Group:     "行情",
		Component: "marketcenter",
		Order:     15,
	})
}

// marketService 是数据层的可选扩展；没有实现时给出中文升级提示。
type marketService interface {
	MarketIndices(ctx context.Context) ([]datasource.MarketIndex, error)
	MarketBreadth(ctx context.Context) (datasource.MarketBreadth, error)
	MarketLadder(ctx context.Context, limit int) ([]datasource.LimitUpStock, error)
	MarketLongHu(ctx context.Context, limit int) ([]datasource.LongHuRow, error)
	MarketHot(ctx context.Context, listType string, limit int) ([]datasource.HotStock, error)
	MarketSnapshot(ctx context.Context, ladderLimit, longhuLimit int) (datasource.MarketSnapshot, error)
}

// service 取数据层的市场中心扩展。
func (p *Plugin) service() (marketService, error) {
	if p.ctx == nil || p.ctx.Data == nil {
		return nil, fmt.Errorf("市场中心数据层未装配")
	}
	svc, ok := p.ctx.Data.(marketService)
	if !ok {
		return nil, fmt.Errorf("当前数据层不支持市场中心（datasource 版本不匹配），请升级应用后重试")
	}
	return svc, nil
}

// decodeArgs 解析方法/工具入参；空字符串按空对象处理。
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

// limitArgs 是 ladder/longhu 的入参。
type limitArgs struct {
	Limit int `json:"limit"`
}

// hotArgs 是同花顺热榜的入参。
type hotArgs struct {
	Type  string `json:"type"`
	Limit int    `json:"limit"`
}

// snapshotArgs 是 snapshot 的入参（面板默认传空对象）。
type snapshotArgs struct {
	LadderLimit int `json:"ladderLimit"`
	LongHuLimit int `json:"longhuLimit"`
}

// clampLimit 收敛 limit：非正数用默认值，超过上限截断。
func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	return limit
}

// ---------- 方法 ----------

// methodIndices 返回主要指数；部分缺失时仍返回已有指数（数据层已写 ErrPartial）。
func (p *Plugin) methodIndices(ctx context.Context, payload json.RawMessage) (any, error) {
	svc, err := p.service()
	if err != nil {
		return nil, err
	}
	items, err := svc.MarketIndices(ctx)
	if err != nil && len(items) == 0 {
		return nil, fmt.Errorf("取指数快照失败: %w", err)
	}
	if items == nil {
		items = []datasource.MarketIndex{}
	}
	return items, nil
}

// methodBreadth 返回市场宽度。
func (p *Plugin) methodBreadth(ctx context.Context, payload json.RawMessage) (any, error) {
	svc, err := p.service()
	if err != nil {
		return nil, err
	}
	breadth, err := svc.MarketBreadth(ctx)
	if err != nil && breadth.Up+breadth.Down+breadth.Flat+breadth.LimitUp+breadth.LimitDown == 0 {
		return nil, fmt.Errorf("取市场宽度失败: %w", err)
	}
	return breadth, nil
}

// methodLadder 返回涨停梯队（连板数降序）。
func (p *Plugin) methodLadder(ctx context.Context, payload json.RawMessage) (any, error) {
	var args limitArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	limit := clampLimit(args.Limit, defaultLadder, maxLadder)
	svc, err := p.service()
	if err != nil {
		return nil, err
	}
	items, err := svc.MarketLadder(ctx, limit)
	if err != nil && len(items) == 0 {
		return nil, fmt.Errorf("取涨停梯队失败: %w", err)
	}
	if items == nil {
		items = []datasource.LimitUpStock{}
	}
	return items, nil
}

// methodLongHu 返回龙虎榜（净买入绝对值降序）。
func (p *Plugin) methodLongHu(ctx context.Context, payload json.RawMessage) (any, error) {
	var args limitArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	limit := clampLimit(args.Limit, defaultLongHu, maxLongHu)
	svc, err := p.service()
	if err != nil {
		return nil, err
	}
	items, err := svc.MarketLongHu(ctx, limit)
	if err != nil && len(items) == 0 {
		return nil, fmt.Errorf("取龙虎榜失败: %w", err)
	}
	if items == nil {
		items = []datasource.LongHuRow{}
	}
	return items, nil
}

// methodHot 返回同花顺热门榜；type 为空默认小时榜。
func (p *Plugin) methodHot(ctx context.Context, payload json.RawMessage) (any, error) {
	var args hotArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	limit := clampLimit(args.Limit, 30, 100)
	svc, err := p.service()
	if err != nil {
		return nil, err
	}
	items, err := svc.MarketHot(ctx, args.Type, limit)
	if err != nil && len(items) == 0 {
		return nil, fmt.Errorf("取同花顺热门榜失败: %w", err)
	}
	if items == nil {
		items = []datasource.HotStock{}
	}
	return items, nil
}

// methodSnapshot 一次返回五块数据 + updatedAt + warnings，面板优先用它减少 IPC。
func (p *Plugin) methodSnapshot(ctx context.Context, payload json.RawMessage) (any, error) {
	var args snapshotArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	ladderLimit := clampLimit(args.LadderLimit, defaultSnapshotLimit, maxLadder)
	longhuLimit := clampLimit(args.LongHuLimit, defaultSnapshotLimit, maxLongHu)
	svc, err := p.service()
	if err != nil {
		return nil, err
	}
	snapshot, err := svc.MarketSnapshot(ctx, ladderLimit, longhuLimit)
	if err != nil {
		return nil, fmt.Errorf("取市场中心快照失败: %w", err)
	}
	return snapshot, nil
}

// ---------- 工具 ----------

// toolIndices 返回主要指数列表。
func (p *Plugin) toolIndices(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodIndices(ctx, payload)
	if err != nil {
		return nil, err
	}
	items := reply.([]datasource.MarketIndex)
	return map[string]any{"count": len(items), "items": items}, nil
}

// toolBreadth 返回市场宽度对象。
func (p *Plugin) toolBreadth(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodBreadth(ctx, payload)
}

// toolLadder 返回涨停梯队列表与条数。
func (p *Plugin) toolLadder(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodLadder(ctx, payload)
	if err != nil {
		return nil, err
	}
	items := reply.([]datasource.LimitUpStock)
	return map[string]any{"count": len(items), "items": items}, nil
}

// toolLongHu 返回龙虎榜列表与条数。
func (p *Plugin) toolLongHu(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodLongHu(ctx, payload)
	if err != nil {
		return nil, err
	}
	items := reply.([]datasource.LongHuRow)
	return map[string]any{"count": len(items), "items": items}, nil
}

// toolHot 返回同花顺热门榜与条数。
func (p *Plugin) toolHot(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodHot(ctx, payload)
	if err != nil {
		return nil, err
	}
	items := reply.([]datasource.HotStock)
	return map[string]any{"count": len(items), "items": items}, nil
}
