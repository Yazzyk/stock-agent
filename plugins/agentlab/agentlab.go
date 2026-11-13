// Package agentlab 是 Agent 工作台插件：把 harness（internal/agent）接到 kernel 上，
// 提供 run/stop/sessions/session 等运行方法与 templates/schedule/context/tree 等
// 值班（自动调度）/上下文/编队方法，注册 agent.run/agent.status/agent.spawn 工具，
// 并注册渐进式披露的元工具（tool.search/tool.describe/tool.call）。
package agentlab

import (
	"context"
	"encoding/json"
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
// 真源在 internal/agent，这里只是本地别名，保证文案只有一份。
var errNoModel = agent.ErrNoModel

func init() { all.Register(New) }

// New 构造 agentlab 插件。
func New() plugin.Plugin { return &AgentLab{} }

// AgentLab 是 Agent 工作台插件。
type AgentLab struct {
	plugin.Base

	runtime     *agent.Runtime
	scheduler   *agent.Scheduler
	llm         contract.LLMService
	watchCancel context.CancelFunc
	schedCancel context.CancelFunc

	// services 是插件的服务注册表：app.method 从这里取 kernel.methods 方法控制面。
	// 取不到时 app.method 返回可操作中文错误，**不会**让插件激活失败（老版本 kernel 也能起）。
	services *plugin.ServiceRegistry
	// gateway 是渐进式披露网关：能力目录要读它的工具索引。
	gateway *progressive.Gateway
}

// Manifest 返回插件元信息。依赖 knowledge：agent 的 kb.search/kb.open 来自知识库插件。
func (a *AgentLab) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "agentlab",
		Name:        "Agent 工作台",
		Version:     "1.0.0",
		Description: "RouterAgent 是唯一对话入口：拆解任务、按需生成任务 worker、汇总结论；盘前/盯盘/复盘值班，走三级渐进式披露（检索→展开→执行）",
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
	a.services = c.Services
	a.gateway = gw
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

	// 自动值班调度器：Activate 时启动、Deactivate 时停止（20s tick，Asia/Shanghai）。
	a.scheduler = agent.NewScheduler(agent.SchedulerOptions{
		Runtime:   a.runtime,
		Templates: a.runtime.Templates(),
		Store:     c.Store,
		Clock:     c.Clock,
		Logger:    c.Log,
	})

	// 能力目录：把"有哪些插件方法 / 有哪些工具"注入总控 system prompt。
	// 注入失败不影响激活：目录只是让模型更聪明，不是功能本身。
	a.refreshCapabilityCatalog()

	// 元工具：模型只能通过 tool.search → tool.describe → tool.call 访问真实数据。
	for _, t := range gw.MetaTools() {
		if err := c.Tools.Add(t); err != nil {
			return fmt.Errorf("注册元工具 %s 失败：%w", t.Name, err)
		}
	}
	// app.method / app.catalog 自注册在 a.tools() 里，但方法控制面的就绪状态更晚
	// （kernel 在插件激活前注册服务，这里只是再次确认目录是最新的）。
	for _, t := range a.tools() {
		if err := c.Tools.Add(t); err != nil {
			return fmt.Errorf("注册工具 %s 失败：%w", t.Name, err)
		}
	}

	methods := []plugin.Method{
		{Name: "workers", Description: "列出 agent worker（内置 + 自定义，RouterAgent 第一）", Fn: a.workersMethod},
		{Name: "createWorker", Description: "创建自定义 worker", Fn: a.createWorkerMethod},
		{Name: "removeWorker", Description: "删除自定义 worker", Fn: a.removeWorkerMethod},
		{Name: "models", Description: "列出可用于 Agent 的模型（不含 key）", Fn: a.modelsMethod},
		{Name: "run", Description: "运行一次 agent 任务", Fn: a.runMethod},
		{Name: "steer", Description: "给正在运行的任务插话（下一次模型调用前生效）", Fn: a.steerMethod},
		{Name: "send", Description: "单一入口：运行中自动插话，空闲则启动运行", Fn: a.sendMethod},
		{Name: "stop", Description: "停止正在运行的任务", Fn: a.stopMethod},
		{Name: "sessions", Description: "列出历史会话", Fn: a.sessionsMethod},
		{Name: "session", Description: "查看会话详情与事件", Fn: a.sessionMethod},
		{Name: "templates", Description: "列出值班模板（提示词/默认值/开关/调度）", Fn: a.templatesMethod},
		{Name: "saveTemplate", Description: "保存值班提示词（可同时改开关）", Fn: a.saveTemplateMethod},
		{Name: "resetTemplate", Description: "恢复默认值班提示词", Fn: a.resetTemplateMethod},
		{Name: "schedule", Description: "查看盘前/盘中/复盘的自动值班调度", Fn: a.scheduleMethod},
		{Name: "setSchedule", Description: "修改自动值班调度（开关/时间/频率）", Fn: a.setScheduleMethod},
		{Name: "context", Description: "查看会话上下文用量（分项估算）", Fn: a.contextMethod},
		{Name: "tree", Description: "查看编队树（主会话 + 子智能体）", Fn: a.treeMethod},
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
		Order:     1,
	}); err != nil {
		return err
	}

	// 盯盘心跳 + 超时看护：30s 一次心跳，单任务 5 分钟超时（超时自动重试一次）。
	watchCtx, cancel := context.WithCancel(context.Background())
	a.watchCancel = cancel
	a.runtime.StartWatchdog(watchCtx, 30*time.Second, 5*time.Minute)

	// 自动值班：20s tick，按模板提示词在盘前/盘中/复盘触发（无模型时只记 lastStatus）。
	schedCtx, schedCancel := context.WithCancel(context.Background())
	a.schedCancel = schedCancel
	a.scheduler.Start(schedCtx)
	return nil
}

// Deactivate 停掉看护与调度协程。
func (a *AgentLab) Deactivate(context.Context) error {
	if a.schedCancel != nil {
		a.schedCancel()
		a.schedCancel = nil
	}
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
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
	if a.runtime != nil {
		return a.runtime.HasModel(ctx)
	}
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

// refreshCapabilityCatalog 把方法目录与工具索引注入运行时，用于生成总控的「能力目录」。
// 任何一步取不到都静默降级：目录是增强项，不该阻断激活或运行。
func (a *AgentLab) refreshCapabilityCatalog() {
	if a.runtime == nil {
		return
	}
	if items, err := a.methodCatalog(); err == nil {
		a.runtime.SetMethodCatalog(agentMethodCatalog(items))
	}
	if a.gateway != nil {
		entries := a.gateway.ToolIndex()
		out := make([]agent.ToolIndexEntry, 0, len(entries))
		for _, e := range entries {
			out = append(out, agent.ToolIndexEntry{Name: e.Name, Summary: e.Summary, Tags: e.Tags})
		}
		a.runtime.SetToolCatalog(out)
	}
}

// ---------- 工具 ----------

func (a *AgentLab) tools() []plugin.Tool {
	return []plugin.Tool{
		a.appMethodTool(),
		a.appCatalogTool(),
		{
			Name:     "agent.run",
			Title:    "运行研究 agent",
			Summary:  "跑一次盘前/盯盘/复盘/量化/问答任务，返回结论与依据",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskNetwork,
			Schema:   json.RawMessage(`{"type":"object","properties":{"worker":{"type":"string","description":"任务类型：router（RouterAgent）/ premarket / intraday / postmarket / quant / chat，或自定义 worker id"},"input":{"type":"string","description":"用户请求，可为空（空则按 worker 职责产出默认报告）"},"model":{"type":"string","description":"可选：模型 id（provider/model）；空则跟随默认"}},"required":["worker"]}`),
			Examples: []string{`{"worker":"postmarket","input":"复盘今天的涨停梯队"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Worker string `json:"worker"`
					Input  string `json:"input"`
					Model  string `json:"model"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("agent.run 参数不是合法 JSON：%w", err)
					}
				}
				if strings.TrimSpace(in.Worker) == "" {
					return nil, fmt.Errorf("agent.run 需要 worker（router（RouterAgent），或 premarket/intraday/postmarket/quant/chat 等 worker id）")
				}
				if err := a.requireModel(ctx); err != nil {
					return nil, err
				}
				return a.runtime.RunWithModel(ctx, in.Worker, in.Input, "", in.Model)
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
		{
			Name:     "agent.spawn",
			Title:    "派生子智能体",
			Summary:  "派生一个子智能体跑子任务（必须指定 worker）；默认后台双工派发",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskNetwork,
			Schema:   json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":"子任务描述：写清目标、范围与要交付的东西"},"worker":{"type":"string","description":"**必填**：子智能体用哪个 worker（用 worker.list 或系统提示词里的目录取 id；不再默认 chat，RouterAgent 不能作为子智能体）"},"label":{"type":"string","description":"编队树里显示的名字，默认取任务前 20 字"},"model":{"type":"string","description":"可选：子会话模型；空则继承父会话模型"},"background":{"type":"boolean","description":"可选，默认 true：后台派发（双工）。派发后立即返回，子结果稍后自动递给你，你可以先向用户汇报进度再继续；传 false 时同步等待子报告（单次子任务、无需播报时用）"}},"required":["task","worker"]}`),
			Examples: []string{`{"task":"复盘 603278 最近 5 日的资金流与量价结构","worker":"quant","label":"个股复盘"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Task       string `json:"task"`
					Worker     string `json:"worker"`
					Label      string `json:"label"`
					Model      string `json:"model"`
					Background *bool  `json:"background"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("agent.spawn 参数不是合法 JSON：%w", err)
					}
				}
				req := agent.SpawnRequest{Task: in.Task, Worker: in.Worker, Label: in.Label, Model: in.Model}
				// 默认后台派发（双工）：RouterAgent 派完立刻能继续汇报进度；
				// 显式 background=false 才同步等子报告（单次子任务用）。
				if in.Background == nil || *in.Background {
					res, err := a.runtime.SpawnAsync(ctx, req)
					if err != nil {
						// 边界条件（只能在主会话派生 / 并发上限）作为文本回给模型，父流程自己决定下一步。
						return "派生子智能体失败：" + err.Error(), nil
					}
					if res.Async {
						return agent.DispatchText(res), nil
					}
					// 没有后台回收通道时 SpawnAsync 退化成了同步执行：直接回子报告。
					return res.ToolText(), nil
				}
				res, err := a.runtime.Spawn(ctx, req)
				if err != nil {
					return "派生子智能体失败：" + err.Error(), nil
				}
				return res.ToolText(), nil
			},
		},
		{
			Name:     "worker.create",
			Title:    "创建自定义 worker",
			Summary:  "按需创建一个专用 worker（id/标题/描述/提示词/模型）",
			Tags:     []string{"agent", "worker"},
			Risk:     plugin.RiskWrite,
			Schema:   json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"小写字母开头，后续小写字母/数字/下划线/中划线，2~32 位"},"title":{"type":"string","description":"短标题，1~40 字"},"description":{"type":"string","description":"一句话职责，最多 200 字"},"prompt":{"type":"string","description":"行为描述，20~8000 字"},"model":{"type":"string","description":"可选：默认模型 id（provider/model）"}},"required":["id","title","description","prompt"]}`),
			Examples: []string{`{"id":"etf_flow","title":"ETF 资金","description":"跟踪 ETF 份额与资金流","prompt":"你是 ETF 资金研究助手：先查份额与资金流，再给结论。"}`},
			Run: func(_ context.Context, args json.RawMessage) (any, error) {
				var in agent.CreateWorkerRequest
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("worker.create 参数不是合法 JSON：%w", err)
					}
				}
				in.CreatedBy = "agent"
				workers, err := a.runtime.CreateWorker(in)
				if err != nil {
					return nil, err
				}
				return map[string]any{"workers": workers}, nil
			},
		},
		{
			Name:     "worker.list",
			Title:    "Worker 列表",
			Summary:  "列出全部 worker（内置 + 自定义，RouterAgent 第一）",
			Tags:     []string{"agent", "worker"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
			Examples: []string{`{}`},
			Run: func(_ context.Context, _ json.RawMessage) (any, error) {
				return a.workersMethod(context.Background(), nil)
			},
		},
	}
}

// ---------- 方法 ----------

// workersMethod 返回全部 worker 视图（后端契约，前端按此渲染）。
//
// 返回：{workers: WorkerView[], mainId, total, presets, customs}
//   - workers 顺序固定：总控（kind=main）→ 预设（kind=preset）→ 自定义（kind=custom）；
//   - mainId 是主控 id（恒为 "router"），前端编队根节点用它判断，**不要**再看 id 字符串或 mode；
//   - 每一项都带 kind/main/preset/builtin/custom 布尔，前端不需要再推断。
func (a *AgentLab) workersMethod(context.Context, json.RawMessage) (any, error) {
	views := a.runtime.Workers()
	mainID := ""
	presets := 0
	customs := 0
	for _, v := range views {
		switch {
		case v.Main:
			mainID = v.ID
		case v.Preset:
			presets++
		default:
			customs++
		}
	}
	return map[string]any{
		"workers": views,
		"mainId":  mainID,
		"total":   len(views),
		"presets": presets,
		"customs": customs,
	}, nil
}

// createWorkerMethod 创建自定义 worker，返回合并后的 worker 列表。
func (a *AgentLab) createWorkerMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in agent.CreateWorkerRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("createWorker 参数不是合法 JSON：%w", err)
		}
	}
	in.CreatedBy = "user"
	workers, err := a.runtime.CreateWorker(in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workers": workers}, nil
}

// removeWorkerMethod 删除自定义 worker，返回合并后的 worker 列表。
func (a *AgentLab) removeWorkerMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("removeWorker 参数不是合法 JSON：%w", err)
		}
	}
	workers, err := a.runtime.RemoveWorker(in.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workers": workers}, nil
}

// AgentModelChoice 是 models 方法返回的模型选项（不含 key 等敏感字段）。
type AgentModelChoice struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName,omitempty"`
	IsDefault   bool   `json:"isDefault"`
	HasKey      bool   `json:"hasKey"`
}

// modelsMethod 列出可用于 Agent 的模型：{models, defaultId}；不暴露任何 key。
func (a *AgentLab) modelsMethod(ctx context.Context, _ json.RawMessage) (any, error) {
	choices := make([]AgentModelChoice, 0)
	defaultID := ""
	if a.llm != nil {
		models, err := a.llm.Models(ctx)
		if err != nil {
			return nil, fmt.Errorf("读取模型配置失败：%v（请在“模型设置”里检查）", err)
		}
		for _, m := range models {
			id := strings.TrimSpace(m.Provider + "/" + m.Model)
			if strings.TrimSpace(m.Provider) == "" {
				id = strings.TrimSpace(m.Model)
			}
			choice := AgentModelChoice{
				ID:          id,
				Model:       m.Model,
				DisplayName: m.DisplayName,
				IsDefault:   m.IsDefault,
				HasKey:      m.HasKey,
			}
			choices = append(choices, choice)
			if m.IsDefault && m.HasKey && defaultID == "" {
				defaultID = id
			}
		}
		for _, c := range choices {
			if defaultID == "" && c.HasKey {
				defaultID = c.ID
			}
		}
	}
	return map[string]any{"models": choices, "defaultId": defaultID}, nil
}

func (a *AgentLab) runMethod(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Worker    string `json:"worker"`
		Input     string `json:"input"`
		SessionID string `json:"sessionId"`
		Model     string `json:"model"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("run 参数不是合法 JSON：%w", err)
		}
	}
	if err := a.requireModel(ctx); err != nil {
		return nil, err
	}
	return a.runtime.RunWithModel(ctx, in.Worker, in.Input, in.SessionID, in.Model)
}

// steerMethod 把用户插话排进运行中会话的队列（step boundary 注入）。
func (a *AgentLab) steerMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
		Text      string `json:"text"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("steer 参数不是合法 JSON：%w", err)
		}
	}
	if err := a.runtime.Steer(in.SessionID, in.Text); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":        true,
		"queued":    a.runtime.SteerQueueLen(in.SessionID),
		"sessionId": in.SessionID,
	}, nil
}

// sendMethod 是前端单一入口：会话在跑就插话，否则启动运行。
func (a *AgentLab) sendMethod(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Worker    string `json:"worker"`
		Input     string `json:"input"`
		SessionID string `json:"sessionId"`
		Model     string `json:"model"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("send 参数不是合法 JSON：%w", err)
		}
	}
	if err := a.requireModel(ctx); err != nil {
		return nil, err
	}
	return a.runtime.SendWithModel(ctx, in.Worker, in.Input, in.SessionID, in.Model)
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

// ---------- 值班模板 / 调度 / 上下文 / 编队 ----------

// templateList 返回合并了调度状态的模板列表（调度器缺失时回落到模板存储本身）。
func (a *AgentLab) templateList() []agent.AgentTemplate {
	if a.scheduler != nil {
		return a.scheduler.Templates()
	}
	if a.runtime != nil {
		if store := a.runtime.Templates(); store != nil {
			return store.All()
		}
	}
	return nil
}

// templatesMethod 返回模板列表：{templates:[{id,title,description,prompt,defaultPrompt,edited,enabled,schedule}]}。
func (a *AgentLab) templatesMethod(context.Context, json.RawMessage) (any, error) {
	return map[string]any{"templates": a.templateList()}, nil
}

// saveTemplateMethod 保存提示词（可同时改开关和模型），返回保存后的模板列表。
func (a *AgentLab) saveTemplateMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID      string  `json:"id"`
		Prompt  string  `json:"prompt"`
		Enabled *bool   `json:"enabled"`
		Model   *string `json:"model"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("saveTemplate 参数不是合法 JSON：%w", err)
		}
	}
	if strings.TrimSpace(in.ID) == "" {
		return nil, fmt.Errorf("saveTemplate 需要 id（内置 worker 或自定义 worker）")
	}
	id, ok := a.runtime.WorkerFromID(in.ID)
	if !ok {
		return nil, fmt.Errorf("未知模板 id：%s（可用：内置 worker 或已创建的自定义 worker）", in.ID)
	}
	if a.scheduler == nil {
		return nil, fmt.Errorf("调度器未就绪，请重启应用后再试")
	}
	tpls, err := a.scheduler.SaveTemplateWithModel(id, in.Prompt, in.Enabled, in.Model)
	if err != nil {
		return nil, err
	}
	return map[string]any{"templates": tpls}, nil
}

// resetTemplateMethod 恢复默认提示词，返回模板列表。
func (a *AgentLab) resetTemplateMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("resetTemplate 参数不是合法 JSON：%w", err)
		}
	}
	id, ok := a.runtime.WorkerFromID(in.ID)
	if !ok {
		return nil, fmt.Errorf("未知模板 id：%s（可用：内置 worker 或已创建的自定义 worker）", in.ID)
	}
	if a.scheduler == nil {
		return nil, fmt.Errorf("调度器未就绪，请重启应用后再试")
	}
	tpls, err := a.scheduler.ResetTemplate(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"templates": tpls}, nil
}

// scheduleMethod 返回盘前/盘中/复盘的调度状态。
func (a *AgentLab) scheduleMethod(context.Context, json.RawMessage) (any, error) {
	if a.scheduler == nil {
		return map[string]any{"phases": []agent.PhaseSchedule{}}, nil
	}
	return map[string]any{"phases": a.scheduler.Phases()}, nil
}

// setScheduleMethod 修改某个阶段的调度（enabled/at/everyMinutes 都可选）。
func (a *AgentLab) setScheduleMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Phase        string `json:"phase"`
		Enabled      *bool  `json:"enabled"`
		At           string `json:"at"`
		EveryMinutes *int   `json:"everyMinutes"`
		Window       string `json:"window"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("setSchedule 参数不是合法 JSON：%w", err)
		}
	}
	if a.scheduler == nil {
		return nil, fmt.Errorf("调度器未就绪，请重启应用后再试")
	}
	phases, err := a.scheduler.SetSchedule(agent.SchedulePatch{
		Phase:        in.Phase,
		Enabled:      in.Enabled,
		At:           in.At,
		EveryMinutes: in.EveryMinutes,
		Window:       in.Window,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"phases": phases}, nil
}

// contextMethod 回读会话的上下文用量（运行中优先用每步快照，否则由事件重建）。
func (a *AgentLab) contextMethod(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("context 参数不是合法 JSON：%w", err)
		}
	}
	// sessionId 为空时**不报错**：回退成「最近一次会话」的上下文。
	// 前端首次打开上下文页 / 上下文环时还没有选中会话，直接调 context 就该拿到可渲染结果
	// （与 tree 的兜底保持一致）。
	id := strings.TrimSpace(in.SessionID)
	if id == "" {
		recent, err := a.runtime.Sessions(1)
		if err != nil {
			return nil, fmt.Errorf("还没有任何会话可以读取上下文：先发一条消息再打开上下文页")
		}
		if len(recent) == 0 {
			return nil, fmt.Errorf("还没有任何会话可以读取上下文：先发一条消息再打开上下文页")
		}
		id = recent[0].ID
	}
	return a.runtime.Context(ctx, id)
}

// treeMethod 返回某个会话所在编队的树（根 + 全部后代）。
//
// sessionId 为空时**不报错**：回退成"最近一次主会话的树"。
// 前端首次打开编队面板时还没有选中会话，直接调 tree 就该拿到可渲染结果。
func (a *AgentLab) treeMethod(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("tree 参数不是合法 JSON：%w", err)
		}
	}
	return a.runtime.Tree(in.SessionID)
}
