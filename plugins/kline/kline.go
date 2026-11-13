// Package kline 提供 K 线面板与技术分析：K 线序列、均线、形态、支撑阻力与多空解读。
//
// 复权诚实原则：技术研判应基于**前复权**价（除权跳空不是趋势），因此 Analyze 固定请求 qfq。
// 数据层 qfq 与 hfq 都由腾讯 fqkline 提供（qfqday / hfqday 两套键），任一口径不可用时
// 返回 datasource.ErrAdjustUnsupported；那时本插件降级为不复权并在返回体里写 adjust="none" 与
// warning，让面板显式标注口径——绝不拿不复权数据冒充前复权去算结论。
//
// 注意：数据层 adj=none 是「腾讯原始序列 → 新浪日线 → 同花顺兜底」，
// 只有最后兜底（同花顺 v6 line 实测是前复权）才会出现口径降级，正常路径拿到的是真不复权。
//
// 同时通过 Services.Register("kline.engine") 暴露引擎，供其它插件复用同一套指标口径。
package kline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/internal/datasource"
	"github.com/yazzyk/stock-agent/internal/market"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

const (
	pluginID            = "kline"
	serviceName         = "kline.engine"
	defaultPeriod       = contract.PeriodDay
	defaultBarsLimit    = 120
	maxBarsLimit        = 250
	analyzeBarsLimit    = 250
	adjustNoneLabel     = "none"
	warningNoAdjustment = "当前行情源不提供复权序列，已按不复权展示（均值与指标基于不复权价）"
)

func init() { all.Register(New) }

// New 是插件工厂（签名由 docs/API.md §8 冻结）。
func New() plugin.Plugin { return &Plugin{} }

// Plugin 是 K 线插件。
type Plugin struct {
	plugin.Base
	ctx    *plugin.Context
	engine *Engine
}

var _ plugin.Plugin = (*Plugin)(nil)

// Manifest 描述插件身份与贡献点。
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "K线分析",
		Version:     "1.0.0",
		Description: "K 线与技术指标、形态识别、支撑阻力与多空解读（只读）",
		Tags:        []string{"market", "quant"},
		Provides: []string{
			"method:bars", "method:analyze", "method:minute",
			"tool:market.bars", "tool:kline.analyze", "tool:market.minute",
			"service:" + serviceName,
			"ui:kline",
		},
	}
}

// Activate 注册方法、工具、服务与面板。
func (p *Plugin) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	p.engine = &Engine{data: ctx.Data}

	methods := []plugin.Method{
		{Name: "bars", Description: "K 线序列与均线", Fn: p.methodBars},
		{Name: "analyze", Description: "技术分析与形态识别", Fn: p.methodAnalyze},
		{Name: "minute", Description: "当日分时走势与均价", Fn: p.methodMinute},
	}
	for _, m := range methods {
		if err := ctx.Methods.Add(m); err != nil {
			return err
		}
	}

	tools := []plugin.Tool{
		{
			Name:    "market.bars",
			Title:   "K 线序列",
			Summary: "取指定周期的 K 线，判断趋势与量价时用",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","minLength":1,"description":"股票代码，如 600000"},
					"period":{"type":"string","enum":["day","week","month","min60"],"description":"周期，默认 day"},
					"limit":{"type":"integer","minimum":1,"maximum":250,"description":"返回根数，默认 120"},
					"adjust":{"type":"string","enum":["","qfq","hfq"],"description":"复权：空=不复权（默认）、qfq 前复权、hfq 后复权（均来自腾讯 fqkline）"}
				},
				"required":["code"],
				"additionalProperties":false
			}`),
			Run: p.toolBars,
		},
		{
			Name:    "kline.analyze",
			Title:   "技术分析",
			Summary: "看均线/RSI/MACD/形态与支撑阻力，判断买卖时机用",
			Tags:    []string{"market", "quant"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","minLength":1,"description":"股票代码，如 600000"},
					"period":{"type":"string","enum":["day","week","month","min60"],"description":"周期，默认 day"}
				},
				"required":["code"],
				"additionalProperties":false
			}`),
			Run: p.toolAnalyze,
		},
		{
			Name:    "market.minute",
			Title:   "分时走势",
			Summary: "看个股当日分时走势与均价，判断盘中强弱时用",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"code":{"type":"string","minLength":1,"description":"股票代码，如 600000"}
				},
				"required":["code"],
				"additionalProperties":false
			}`),
			Run: p.toolMinute,
		},
	}
	for _, t := range tools {
		if err := ctx.Tools.Add(t); err != nil {
			return err
		}
	}

	if err := ctx.Services.Register(serviceName, p.engine); err != nil {
		return err
	}

	return ctx.UI.Add(plugin.UISlot{
		ID:        "kline",
		Title:     "K线分析",
		Icon:      "candles",
		Group:     "行情",
		Component: "kline",
		Order:     30,
	})
}

// Engine 是暴露给其它插件的 K 线引擎。
type Engine struct {
	data contract.DataService
}

var _ = (*Engine)(nil)

// BarsReply 是 bars 方法的返回体（字段名与 docs/API.md §3 逐字对齐，adjust/warning 为增量字段）。
type BarsReply struct {
	Code       string            `json:"code"`
	Name       string            `json:"name"`
	Period     string            `json:"period"`
	Adjust     string            `json:"adjust"`
	Warning    string            `json:"warning,omitempty"`
	Bars       []contract.Bar    `json:"bars"`
	Indicators market.Indicators `json:"indicators"`
}

// AnalyzeReply 是 analyze 方法的返回体。
// 顶层 summary/patterns/support/resistance 保留给旧调用方；Horizons 是短线/长线双尺度增量字段。
type AnalyzeReply struct {
	Code       string                   `json:"code"`
	Name       string                   `json:"name"`
	Period     string                   `json:"period"`
	Adjust     string                   `json:"adjust"`
	Warning    string                   `json:"warning,omitempty"`
	Last       contract.Bar             `json:"last"`
	Summary    market.Summary           `json:"summary"`
	Patterns   []market.Pattern         `json:"patterns"`
	Support    []float64                `json:"support"`
	Resistance []float64                `json:"resistance"`
	Horizons   []market.HorizonAnalysis `json:"horizons"`
}

type barsArgs struct {
	Code   string          `json:"code"`
	Period contract.Period `json:"period"`
	Limit  int             `json:"limit"`
	Adjust contract.Adjust `json:"adjust"`
}

type analyzeArgs struct {
	Code   string          `json:"code"`
	Period contract.Period `json:"period"`
}

// minuteService 是数据层的可选扩展（结构化断言）：提供当日分时序列。
// 与 fundflow 的可选接口一致，不扩大 contract.DataService，避免所有 stub 连锁改动。
type minuteService interface {
	Minute(ctx context.Context, code string) (contract.MinuteSeries, error)
}

type minuteArgs struct {
	Code string `json:"code"`
}

func (p *Plugin) methodBars(ctx context.Context, payload json.RawMessage) (any, error) {
	var args barsArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(args.Code)
	if code == "" {
		return nil, fmt.Errorf("code 不能为空")
	}
	period := normalizePeriod(args.Period)
	limit := args.Limit
	if limit <= 0 {
		limit = defaultBarsLimit
	}
	if limit > maxBarsLimit {
		limit = maxBarsLimit
	}
	reply, err := p.engine.Bars(ctx, code, period, limit, normalizeAdjust(args.Adjust))
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (p *Plugin) methodAnalyze(ctx context.Context, payload json.RawMessage) (any, error) {
	var args analyzeArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(args.Code)
	if code == "" {
		return nil, fmt.Errorf("code 不能为空")
	}
	return p.engine.Analyze(ctx, code, normalizePeriod(args.Period))
}

// methodMinute 返回个股/指数当日分时序列（价格、均价、分钟量/额）。
func (p *Plugin) methodMinute(ctx context.Context, payload json.RawMessage) (any, error) {
	var args minuteArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(args.Code)
	if code == "" {
		return nil, fmt.Errorf("code 不能为空")
	}
	service, ok := p.ctx.Data.(minuteService)
	if !ok {
		return nil, fmt.Errorf("当前数据层不支持分时走势（datasource 版本不匹配），请升级应用后重试")
	}
	series, err := service.Minute(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("取 %s 分时失败: %w", code, err)
	}
	return series, nil
}

// Bars 取 K 线并算均线；复权拿不到时降级为不复权并带 warning。
func (e *Engine) Bars(ctx context.Context, code string, period contract.Period, limit int, adjust contract.Adjust) (BarsReply, error) {
	reply := BarsReply{Code: code, Period: string(period), Adjust: adjustLabel(adjust)}
	bars, err := e.data.Bars(ctx, code, period, limit, adjust)
	if err != nil && adjust != contract.AdjustNone && errors.Is(err, datasource.ErrAdjustUnsupported) {
		// 明确降级：不复权 + warning，绝不冒充复权
		bars, err = e.data.Bars(ctx, code, period, limit, contract.AdjustNone)
		if err == nil {
			reply.Adjust = adjustNoneLabel
			reply.Warning = warningNoAdjustment
		}
	}
	if err != nil {
		return BarsReply{}, fmt.Errorf("取 %s K 线失败: %w", code, err)
	}
	reply.Bars = bars
	reply.Indicators = market.ComputeIndicators(bars)
	reply.Name = e.nameOf(ctx, code)
	return reply, nil
}

// Analyze 汇总技术分析；逻辑与口径全部在 internal/market（纯函数、可单测）。
//
// 口径：优先前复权（除权跳空会让均线/形态/支撑阻力全错），拿不到时按 ErrAdjustUnsupported
// 降级为不复权并带 warning，reply.Adjust 如实反映**实际**用的口径（qfq / none）。
func (e *Engine) Analyze(ctx context.Context, code string, period contract.Period) (AnalyzeReply, error) {
	adjust := contract.AdjustQfq
	bars, err := e.data.Bars(ctx, code, period, analyzeBarsLimit, adjust)
	warning := ""
	if err != nil && errors.Is(err, datasource.ErrAdjustUnsupported) {
		// 明确降级：不复权 + warning，绝不冒充前复权
		adjust = contract.AdjustNone
		bars, err = e.data.Bars(ctx, code, period, analyzeBarsLimit, adjust)
		if err == nil {
			warning = warningNoAdjustment
		}
	}
	if err != nil {
		return AnalyzeReply{}, fmt.Errorf("取 %s K 线失败: %w", code, err)
	}
	if len(bars) == 0 {
		return AnalyzeReply{}, fmt.Errorf("%s 没有可用于分析的 K 线", code)
	}
	analysis := market.Analyze(bars)
	return AnalyzeReply{
		Code:       code,
		Name:       e.nameOf(ctx, code),
		Period:     string(period),
		Adjust:     adjustLabel(adjust),
		Warning:    warning,
		Last:       analysis.Last,
		Summary:    analysis.Summary,
		Patterns:   analysis.Patterns,
		Support:    analysis.Support,
		Resistance: analysis.Resistance,
		Horizons:   market.AnalyzeHorizons(bars),
	}, nil
}

// nameOf 用一次快照补名称；失败不影响主流程（名称缺失比整个分析失败好）。
func (e *Engine) nameOf(ctx context.Context, code string) string {
	quotes, err := e.data.Quote(ctx, []string{code})
	if err != nil && len(quotes) == 0 {
		return ""
	}
	for _, quote := range quotes {
		if quote.Name != "" {
			return quote.Name
		}
	}
	return ""
}

// ---------- 工具 ----------

func (p *Plugin) toolBars(ctx context.Context, payload json.RawMessage) (any, error) {
	reply, err := p.methodBars(ctx, payload)
	if err != nil {
		return nil, err
	}
	view := reply.(BarsReply)
	return view.Bars, nil
}

func (p *Plugin) toolAnalyze(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodAnalyze(ctx, payload)
}

// toolMinute 与 methodMinute 同源：Agent 直接拿分时序列做盘中强弱判断。
func (p *Plugin) toolMinute(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodMinute(ctx, payload)
}

func normalizePeriod(period contract.Period) contract.Period {
	switch period {
	case contract.PeriodDay, contract.PeriodWeek, contract.PeriodMonth, contract.PeriodMin60:
		return period
	}
	return defaultPeriod
}

func normalizeAdjust(adjust contract.Adjust) contract.Adjust {
	switch adjust {
	case contract.AdjustQfq, contract.AdjustHfq:
		return adjust
	}
	return contract.AdjustNone
}

func adjustLabel(adjust contract.Adjust) string {
	switch adjust {
	case contract.AdjustQfq:
		return "qfq"
	case contract.AdjustHfq:
		return "hfq"
	}
	return adjustNoneLabel
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
