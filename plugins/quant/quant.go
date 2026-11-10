// Package quant 把回测引擎包装成插件：面板、工具、服务三件套。
// 只读研究：不产生任何交易指令，只输出可复现的回测与 walk-forward 结论。
package quant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/internal/backtest"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

func init() { all.Register(New) }

type Plugin struct {
	plugin.Base
	data contract.DataService
	log  logger
}

type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func New() plugin.Plugin { return &Plugin{} }

func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "quant",
		Name:        "量化研究",
		Version:     "0.1.0",
		Description: "在 A 股日线上跑回测与 walk-forward：T+1、涨跌停不可成交、四项真实成本、样本内外对照。",
		Author:      "guanlan",
		Tags:        []string{"研究", "量化", "回测"},
		Provides:    []string{"quant.engine"},
	}
}

func (p *Plugin) Activate(c *plugin.Context) error {
	if c.Data == nil {
		return errors.New("量化插件需要数据服务（ctx.Data 为空）")
	}
	p.data, p.log = c.Data, c.Log

	engine := &Engine{data: c.Data}
	if err := c.Services.Register("quant.engine", engine); err != nil {
		return err
	}
	for _, m := range []plugin.Method{
		{Name: "strategies", Description: "列出内置策略与参数", Fn: p.strategies},
		{Name: "run", Description: "单次回测", Fn: p.run},
		{Name: "walkforward", Description: "walk-forward 过拟合检验", Fn: p.walkforward},
	} {
		if err := c.Methods.Add(m); err != nil {
			return err
		}
	}
	if err := c.Tools.Add(plugin.Tool{
		Name:    "quant.backtest",
		Title:   "回测策略",
		Summary: "对单只 A 股跑日线回测，返回收益/回撤/成本明细与报告，用于检验策略而非选股",
		Tags:    []string{"quant", "research"},
		Risk:    plugin.RiskNetwork,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "code":    {"type": "string", "description": "股票代码，如 600000"},
    "strategy":{"type": "string", "enum": ["ma", "rsi", "momentum", "buyhold"], "description": "策略 ID"},
    "params":  {"type": "object", "description": "策略参数，缺省用默认值"},
    "period":  {"type": "string", "enum": ["day", "week", "month"], "description": "K 线周期，默认 day"},
    "limit":   {"type": "integer", "description": "回看 K 线数量，默认 500，上限 1200"}
  },
  "required": ["code"]
}`),
		Examples: []string{`{"code":"600000","strategy":"ma","params":{"fast":5,"slow":20}}`},
		Run:      p.toolBacktest,
	}); err != nil {
		return err
	}
	return c.UI.Add(plugin.UISlot{
		ID: "quant", Title: "量化研究", Icon: "chart", Group: "研究",
		Component: "quant", Order: 45,
	})
}

// Engine 是暴露给其它插件（agentlab 的 quant worker）的服务。
type Engine struct {
	data contract.DataService
}

// Run 取数并回测，返回结果与所用复权方式。
func (e *Engine) Run(ctx context.Context, code, period, strategy string, params backtest.ParamSet, limit int, initialCash float64) (backtest.Result, string, error) {
	bars, warning, err := e.bars(ctx, code, period, limit)
	if err != nil {
		return backtest.Result{}, "", err
	}
	opt := backtest.Options{InitialCash: initialCash, Cost: backtest.DefaultCost(), Warmup: 2}
	if opt.InitialCash <= 0 {
		opt.InitialCash = 100000
	}
	res := backtest.Run(code, bars, backtest.Build(strategy, params), opt)
	if warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}
	return res, warning, nil
}

// WalkForward 跑 walk-forward。
func (e *Engine) WalkForward(ctx context.Context, code, strategy string, folds, limit int) (backtest.WalkForwardResult, error) {
	bars, _, err := e.bars(ctx, code, "day", limit)
	if err != nil {
		return backtest.WalkForwardResult{}, err
	}
	if folds <= 0 {
		folds = 3
	}
	return backtest.WalkForward(code, bars, strategy, backtest.DefaultGrid(strategy), folds,
		backtest.Options{InitialCash: 100000, Cost: backtest.DefaultCost(), Warmup: 2}), nil
}

// bars 优先前复权；数据源不支持复权时降级为不复权并明确告知。
func (e *Engine) bars(ctx context.Context, code, period string, limit int) ([]contract.Bar, string, error) {
	if strings.TrimSpace(code) == "" {
		return nil, "", errors.New("缺少股票代码")
	}
	if period == "" {
		period = "day"
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 1200 {
		limit = 1200
	}
	bars, err := e.data.Bars(ctx, code, contract.Period(period), limit, contract.AdjustQfq)
	if err == nil {
		return bars, "", nil
	}
	bars, err2 := e.data.Bars(ctx, code, contract.Period(period), limit, contract.AdjustNone)
	if err2 != nil {
		return nil, "", fmt.Errorf("取 %s 的 K 线失败：%v", code, err2)
	}
	return bars, "复权数据不可用，本次按不复权计算（结论请谨慎）", nil
}

// ---------- 方法 ----------

type runInput struct {
	Code        string             `json:"code"`
	Period      string             `json:"period"`
	Strategy    string             `json:"strategy"`
	Params      backtest.ParamSet  `json:"params"`
	Limit       int                `json:"limit"`
	InitialCash float64            `json:"initialCash"`
	Adjust      string             `json:"adjust"`
}

type runOutput struct {
	Code    string            `json:"code"`
	Period  string            `json:"period"`
	Adjust  string            `json:"adjust"`
	Warning string            `json:"warning,omitempty"`
	Result  backtest.Result   `json:"result"`
	Report  string            `json:"report"`
}

func (p *Plugin) strategies(context.Context, json.RawMessage) (any, error) {
	return map[string]any{"strategies": backtest.Specs()}, nil
}

func (p *Plugin) run(ctx context.Context, payload json.RawMessage) (any, error) {
	var in runInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	engine := &Engine{data: p.data}
	res, warning, err := engine.Run(ctx, in.Code, in.Period, in.Strategy, in.Params, in.Limit, in.InitialCash)
	if err != nil {
		return nil, err
	}
	adjust := "qfq"
	if warning != "" {
		adjust = "none"
	}
	return runOutput{Code: in.Code, Period: in.Period, Adjust: adjust, Warning: warning, Result: res, Report: res.Report()}, nil
}

type wfInput struct {
	Code     string `json:"code"`
	Strategy string `json:"strategy"`
	Folds    int    `json:"folds"`
	Limit    int    `json:"limit"`
}

func (p *Plugin) walkforward(ctx context.Context, payload json.RawMessage) (any, error) {
	var in wfInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	if in.Strategy == "" {
		in.Strategy = "ma"
	}
	res, err := (&Engine{data: p.data}).WalkForward(ctx, in.Code, in.Strategy, in.Folds, in.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": res, "report": res.Report()}, nil
}

// toolBacktest 给 LLM 用：只回摘要 + 报告，不回完整净值序列（省上下文）。
func (p *Plugin) toolBacktest(ctx context.Context, args json.RawMessage) (any, error) {
	var in runInput
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	if in.Strategy == "" {
		in.Strategy = "ma"
	}
	engine := &Engine{data: p.data}
	res, warning, err := engine.Run(ctx, in.Code, in.Period, in.Strategy, in.Params, in.Limit, in.InitialCash)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"code": in.Code, "strategy": res.Strategy, "start": res.Start, "end": res.End,
		"totalReturn": res.TotalReturn, "cagr": res.CAGR, "maxDrawdown": res.MaxDrawdown,
		"sharpe": res.Sharpe, "sortino": res.Sortino, "winRate": res.WinRate,
		"tradeCount": res.TradeCount, "turnover": res.Turnover,
		"costs": res.Costs, "warning": warning, "report": res.Report(),
	}, nil
}
