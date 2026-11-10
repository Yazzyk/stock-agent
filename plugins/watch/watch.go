// Package watch 把 Watchdog 暴露成插件：面板、方法、工具、服务。
// 用户在这里声明"盯什么"，Agent（agentlab）可以用 watch.create 自己生成规则；每条规则可单独开关。
package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yazzyk/stock-agent/internal/agent"
	"os"
	"path/filepath"

	"github.com/yazzyk/stock-agent/internal/watch"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

func init() { all.Register(New) }

type Plugin struct {
	plugin.Base
	engine *watch.Engine
	cancel context.CancelFunc
}

func New() plugin.Plugin { return &Plugin{} }

func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "watch",
		Name:        "Watch 哨兵",
		Version:     "0.1.0",
		Description: "用户自定义盯盘规则：行情/资金流/板块/K线/新闻条件命中后，自动派一次 Agent 任务；规则文件即插件，可热加载与开关。",
		Author:      "guanlan",
		Tags:        []string{"研究", "监控", "watchdog"},
		DependsOn:   []string{"agentlab"},
		Provides:    []string{"watch.engine"},
	}
}

func (p *Plugin) Activate(c *plugin.Context) error {
	trigger, err := p.triggerFunc(c)
	if err != nil {
		return err
	}
	dataDir := c.DataDir
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	engine, err := watch.New(watch.Options{
		Store:              c.Store,
		Data:               c.Data,
		Guard:              c.Guard,
		Audit:              c.Audit,
		Events:             c.Events,
		Trigger:            trigger,
		Clock:              c.Clock,
		Logger:             c.Log,
		DataDir:            dataDir,
		TradingOnly:        true,
		MaxConcurrent:      1,
		MaxTriggersPerHour: 6,
	})
	if err != nil {
		return err
	}
	p.engine = engine

	// 首次运行播种三条模板规则（全部关闭），让面板有东西可看、可一键启用。
	if len(engine.List()) == 0 {
		for _, r := range watch.ExampleRules() {
			if _, err := engine.Save(r, "template"); err != nil {
				c.Log.Warn("播种模板规则失败", "name", r.Name, "error", err)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	engine.Start(ctx)

	if err := c.Services.Register("watch.engine", engine); err != nil {
		return err
	}
	for _, m := range []plugin.Method{
		{Name: "list", Description: "列出 Watch 规则", Fn: p.list},
		{Name: "save", Description: "新增/更新规则", Fn: p.save},
		{Name: "toggle", Description: "开关规则", Fn: p.toggle},
		{Name: "remove", Description: "删除规则", Fn: p.remove},
		{Name: "test", Description: "试运行（只求值不触发）", Fn: p.test},
		{Name: "logs", Description: "触发日志", Fn: p.logs},
		{Name: "templates", Description: "规则模板", Fn: p.templates},
		{Name: "metrics", Description: "可用指标白名单", Fn: p.metrics},
		{Name: "state", Description: "引擎状态", Fn: p.state},
	} {
		if err := c.Methods.Add(m); err != nil {
			return err
		}
	}

	for _, t := range p.tools() {
		if err := c.Tools.Add(t); err != nil {
			return err
		}
	}
	return c.UI.Add(plugin.UISlot{
		ID: "watch", Title: "Watch 哨兵", Icon: "radar", Group: "研究",
		Component: "watch", Order: 35,
	})
}

func (p *Plugin) Deactivate(context.Context) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.engine != nil {
		p.engine.Stop()
	}
	return nil
}

// triggerFunc 把引擎的触发接到 agent.runtime 服务。
func (p *Plugin) triggerFunc(c *plugin.Context) (watch.TriggerFunc, error) {
	v, ok := c.Services.Get("agent.runtime")
	if !ok {
		return nil, errors.New("需要 agent.runtime 服务（agentlab 插件未激活）")
	}
	rt, ok := v.(*agent.Runtime)
	if !ok {
		return nil, fmt.Errorf("agent.runtime 类型不符: %T", v)
	}
	return func(ctx context.Context, worker, prompt string) (string, error) {
		report, err := rt.Run(ctx, worker, prompt, "")
		if err != nil {
			return "", err
		}
		return report.Report, nil
	}, nil
}

// ---------- 方法 ----------

type listResp struct {
	Rules   []watch.Rule       `json:"rules"`
	Running bool               `json:"running"`
	Metrics []watch.MetricSpec `json:"metrics"`
	Logs    []watch.RunLog     `json:"logs"`
}

func (p *Plugin) list(context.Context, json.RawMessage) (any, error) {
	return listResp{Rules: p.engine.List(), Running: p.engine.Running(), Metrics: watch.MetricList(), Logs: p.engine.Logs(20)}, nil
}

type saveInput struct {
	watch.Rule
}

func (p *Plugin) save(_ context.Context, payload json.RawMessage) (any, error) {
	var in watch.Rule
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	saved, err := p.engine.Save(in, "user")
	if err != nil {
		return nil, err
	}
	return map[string]any{"rule": saved, "rules": p.engine.List()}, nil
}

func (p *Plugin) toggle(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	r, err := p.engine.Toggle(in.ID, in.Enabled)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rule": r}, nil
}

func (p *Plugin) remove(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	if in.ID == "" {
		return nil, errors.New("缺少 id")
	}
	if err := p.engine.Delete(in.ID); err != nil {
		return nil, err
	}
	return map[string]any{"rules": p.engine.List()}, nil
}

func (p *Plugin) test(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(payload, &in)
	return p.engine.Test(ctx, in.ID)
}

func (p *Plugin) logs(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(payload, &in)
	if in.Limit <= 0 {
		in.Limit = 50
	}
	return map[string]any{"logs": p.engine.Logs(in.Limit)}, nil
}

func (p *Plugin) templates(context.Context, json.RawMessage) (any, error) {
	return map[string]any{"templates": watch.ExampleRules(), "metrics": watch.MetricList()}, nil
}

func (p *Plugin) metrics(context.Context, json.RawMessage) (any, error) {
	return map[string]any{"metrics": watch.MetricList()}, nil
}

func (p *Plugin) state(context.Context, json.RawMessage) (any, error) {
	return map[string]any{"running": p.engine.Running(), "rules": len(p.engine.List())}, nil
}

// ---------- 工具 ----------

func (p *Plugin) tools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name:    "watchdog.list",
			Title:   "列出 Watch 规则",
			Summary: "查看当前所有盯盘规则与其开关状态，排障或复用规则时先用",
			Tags:    []string{"watch"},
			Risk:    plugin.RiskRead,
			Schema:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run: func(context.Context, json.RawMessage) (any, error) {
				return listResp{Rules: p.engine.List(), Running: p.engine.Running()}, nil
			},
		},
		{
			Name:    "watchdog.create",
			Title:   "创建 Watch 规则",
			Summary: "把用户的盯盘需求写成规则并注册（条件只读白名单指标，不执行代码）",
			Tags:    []string{"watch"},
			Risk:    plugin.RiskWrite,
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "规则名，如 半导体资金异动"},
    "description": {"type": "string"},
    "enabled": {"type": "boolean", "description": "是否立即开启"},
    "sources": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string", "enum": ["quote","sector","kline","news","schedule"]},
          "codes": {"type": "array", "items": {"type": "string"}},
          "sector": {"type": "string"},
          "kind": {"type": "string", "enum": ["sector","concept","roll","notice"]},
          "keyword": {"type": "string"},
          "period": {"type": "string"},
          "times": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["type"]
      }
    },
    "condition": {"type": "string", "description": "如 sector.mainNetYi > 20 and quote.changePct > 3"},
    "action": {
      "type": "object",
      "properties": {
        "worker": {"type": "string", "enum": ["chat","intraday","premarket","postmarket","quant"]},
        "prompt": {"type": "string", "description": "可用 {指标名} 与 {summary}/{code}/{sector} 占位"}
      }
    },
    "cooldownSeconds": {"type": "integer", "description": "同类信号冷却秒数，≥30"}
  },
  "required": ["name","sources","condition"]
}`),
			Examples: []string{
				`{"name":"半导体资金异动","sources":[{"type":"sector","sector":"半导体","kind":"sector"}],"condition":"sector.mainNetYi > 20","action":{"worker":"intraday","prompt":"板块 {sector} 主力净流入 {sector.mainNetYi} 亿元，请判断持续性"}}`,
			},
			Run: func(_ context.Context, args json.RawMessage) (any, error) {
				var in watch.Rule
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, fmt.Errorf("参数格式错误：%w", err)
				}
				saved, err := p.engine.Save(in, "agent")
				if err != nil {
					return nil, err
				}
				return map[string]any{"rule": saved, "hint": "规则已注册；用户可在 Watch 面板开关或调整"}, nil
			},
		},
		{
			Name:    "watchdog.toggle",
			Title:   "开关 Watch 规则",
			Summary: "按 id 启用或停用某条盯盘规则，避免误报时临时关闭",
			Tags:    []string{"watch"},
			Risk:    plugin.RiskWrite,
			Schema:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"enabled":{"type":"boolean"}},"required":["id","enabled"]}`),
			Run: func(_ context.Context, args json.RawMessage) (any, error) {
				var in struct {
					ID      string `json:"id"`
					Enabled bool   `json:"enabled"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, fmt.Errorf("参数格式错误：%w", err)
				}
				return p.engine.Toggle(in.ID, in.Enabled)
			},
		},
		{
			Name:    "watchdog.test",
			Title:   "试运行 Watch 规则",
			Summary: "只取数求值、不触发 Agent，用于确认条件是否会命中",
			Tags:    []string{"watch"},
			Risk:    plugin.RiskNetwork,
			Schema:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, fmt.Errorf("参数格式错误：%w", err)
				}
				return p.engine.Test(ctx, in.ID)
			},
		},
		{
			Name:    "watchdog.logs",
			Title:   "查看触发日志",
			Summary: "看最近哪条规则触发过、Agent 结论与失败原因",
			Tags:    []string{"watch"},
			Risk:    plugin.RiskRead,
			Schema:  json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`),
			Run: func(_ context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Limit int `json:"limit"`
				}
				_ = json.Unmarshal(args, &in)
				if in.Limit <= 0 {
					in.Limit = 10
				}
				return map[string]any{"logs": p.engine.Logs(in.Limit)}, nil
			},
		},
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".guanlan")
}
