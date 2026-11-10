// Package agentlab 是 Agent 工作台插件：把 harness（internal/agent）接到 kernel 上，
// 提供 run/stop/sessions/session 四个方法与 agent.run/agent.status 两个工具，
// 并注册渐进式披露的元工具（tool.search/tool.describe/tool.call）。
package agentlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/internal/agent"
	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

// errNoModel 是"没配模型"的统一中文错误文案（验收会在无 key 环境跑）。
var errNoModel = errors.New("未配置模型：请在“模型设置”里填写 API Key 后再运行")

func init() { all.Register(New) }

// New 构造 agentlab 插件。
func New() plugin.Plugin { return &AgentLab{} }

// AgentLab 是 Agent 工作台插件。
type AgentLab struct {
	plugin.Base

	runtime     *agent.Runtime
	llm         contract.LLMService
	watchCancel context.CancelFunc
}

// Manifest 返回插件元信息。依赖 knowledge：agent 的 kb.search/kb.open 来自知识库插件。
func (a *AgentLab) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "agentlab",
		Name:        "Agent 工作台",
		Version:     "1.0.0",
		Description: "多 worker 研究型 agent：盘前/盯盘/复盘/量化/问答，走三级渐进式披露（检索→展开→执行）",
		DependsOn:   []string{"knowledge"},
		Tags:        []string{"agent", "harness"},
	}
}

// Activate 装配运行时、工具、方法与面板。
func (a *AgentLab) Activate(c *plugin.Context) error {
	kb := resolveKB(c)
	// 上限设为 write：agent 需要把复盘结论通过 kb.import 沉淀进本地知识库；
	// exec 仍然永不开放（Policy.AllowExec=false 时 kernel 也会再拦一次）。
	gw := progressive.NewGateway(c.ToolHost, c.Guard, c.Audit, plugin.RiskWrite)

	a.llm = c.LLM
	a.runtime = agent.NewRuntime(agent.RuntimeOptions{
		LLM:     c.LLM,
		Guard:   c.Guard,
		Audit:   c.Audit,
		Events:  c.Events,
		Policy:  c.Policy,
		Store:   c.Store,
		Gateway: gw,
		KB:      kb,
		Clock:   c.Clock,
		Logger:  c.Log,
	})

	if err := c.Services.Register("agent.runtime", a.runtime); err != nil {
		c.Log.Warn("agent.runtime 服务注册失败", "error", err)
	}

	// 元工具：模型只能通过 tool.search → tool.describe → tool.call 访问真实数据。
	for _, t := range gw.MetaTools() {
		if err := c.Tools.Add(t); err != nil {
			return fmt.Errorf("注册元工具 %s 失败：%w", t.Name, err)
		}
	}
	for _, t := range a.tools() {
		if err := c.Tools.Add(t); err != nil {
			return fmt.Errorf("注册工具 %s 失败：%w", t.Name, err)
		}
	}

	methods := []plugin.Method{
		{Name: "workers", Description: "列出 agent worker", Fn: a.workersMethod},
		{Name: "run", Description: "运行一次 agent 任务", Fn: a.runMethod},
		{Name: "stop", Description: "停止正在运行的任务", Fn: a.stopMethod},
		{Name: "sessions", Description: "列出历史会话", Fn: a.sessionsMethod},
		{Name: "session", Description: "查看会话详情与事件", Fn: a.sessionMethod},
	}
	for _, m := range methods {
		if err := c.Methods.Add(m); err != nil {
			return err
		}
	}

	if err := c.UI.Add(plugin.UISlot{
		ID:        "agentlab",
		Title:     "Agent 工作台",
		Icon:      "robot",
		Group:     "研究",
		Component: "agentlab",
		Order:     40,
	}); err != nil {
		return err
	}

	// 盯盘心跳 + 超时看护：30s 一次心跳，单任务 5 分钟超时（超时自动重试一次）。
	watchCtx, cancel := context.WithCancel(context.Background())
	a.watchCancel = cancel
	a.runtime.StartWatchdog(watchCtx, 30*time.Second, 5*time.Minute)
	return nil
}

// Deactivate 停掉看护协程。
func (a *AgentLab) Deactivate(context.Context) error {
	if a.watchCancel != nil {
		a.watchCancel()
		a.watchCancel = nil
	}
	if a.runtime != nil {
		a.runtime.StopWatchdog()
	}
	return nil
}

// resolveKB 优先复用 knowledge 插件注册的 kb.store，保证同一份索引。
func resolveKB(c *plugin.Context) *progressive.KB {
	if v, ok := c.Services.Get("kb.store"); ok {
		if kb, ok := v.(*progressive.KB); ok {
			return kb
		}
	}
	return progressive.NewKB(c.Store)
}

// requireModel 在没有可用模型时立刻给出中文可操作错误，避免无 key 环境长时间挂起。
func (a *AgentLab) requireModel(ctx context.Context) error {
	if a.llm == nil {
		return errNoModel
	}
	models, err := a.llm.Models(ctx)
	if err != nil {
		return fmt.Errorf("读取模型配置失败：%v（请在“模型设置”里检查）", err)
	}
	for _, m := range models {
		if m.HasKey {
			return nil
		}
	}
	return errNoModel
}

// ---------- 工具 ----------

func (a *AgentLab) tools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name:     "agent.run",
			Title:    "运行研究 agent",
			Summary:  "跑一次盘前/盯盘/复盘/量化/问答任务，返回结论与依据",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskNetwork,
			Schema:   json.RawMessage(`{"type":"object","properties":{"worker":{"type":"string","enum":["premarket","intraday","postmarket","quant","chat"],"description":"任务类型"},"input":{"type":"string","description":"用户请求，可为空（空则按 worker 职责产出默认报告）"}},"required":["worker"]}`),
			Examples: []string{`{"worker":"postmarket","input":"复盘今天的涨停梯队"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Worker string `json:"worker"`
					Input  string `json:"input"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("agent.run 参数不是合法 JSON：%w", err)
					}
				}
				if strings.TrimSpace(in.Worker) == "" {
					return nil, fmt.Errorf("agent.run 需要 worker（premarket/intraday/postmarket/quant/chat）")
				}
				if err := a.requireModel(ctx); err != nil {
					return nil, err
				}
				return a.runtime.Run(ctx, in.Worker, in.Input, "")
			},
		},
		{
			Name:     "agent.status",
			Title:    "Agent 运行状态",
			Summary:  "查看正在运行的任务与最近会话，排障先看这里",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"最近会话条数，默认 5"}}}`),
			Examples: []string{`{"limit":3}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Limit int `json:"limit"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("agent.status 参数不是合法 JSON：%w", err)
					}
				}
				if in.Limit <= 0 {
					in.Limit = 5
				}
				sessions, err := a.runtime.Sessions(in.Limit)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"running":  a.runtime.Running(),
					"sessions": sessions,
					"workers":  a.runtime.Workers(),
				}, nil
			},
		},
	}
}

// ---------- 方法 ----------

func (a *AgentLab) workersMethod(context.Context, json.RawMessage) (any, error) {
	return a.runtime.Workers(), nil
}

func (a *AgentLab) runMethod(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Worker    string `json:"worker"`
		Input     string `json:"input"`
		SessionID string `json:"sessionId"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("run 参数不是合法 JSON：%w", err)
		}
	}
	if err := a.requireModel(ctx); err != nil {
		return nil, err
	}
	return a.runtime.Run(ctx, in.Worker, in.Input, in.SessionID)
}

func (a *AgentLab) stopMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("stop 参数不是合法 JSON：%w", err)
		}
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, fmt.Errorf("stop 需要 sessionId")
	}
	return map[string]any{"ok": a.runtime.Stop(in.SessionID)}, nil
}

func (a *AgentLab) sessionsMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Limit int `json:"limit"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("sessions 参数不是合法 JSON：%w", err)
		}
	}
	if in.Limit <= 0 {
		in.Limit = 20
	}
	return a.runtime.Sessions(in.Limit)
}

func (a *AgentLab) sessionMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("session 参数不是合法 JSON：%w", err)
		}
	}
	return a.runtime.Session(in.SessionID)
}
