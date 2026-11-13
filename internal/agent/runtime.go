package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// RuntimeOptions 是 Runtime 的依赖集合（全部由插件在 Activate 时注入）。
type RuntimeOptions struct {
	LLM     contract.LLMService
	Guard   contract.Guard
	Audit   contract.Audit
	Events  *plugin.Bus
	Policy  contract.Policy
	Store   contract.Store
	Gateway *progressive.Gateway
	KB      *progressive.KB
	Clock   func() time.Time
	Logger  *slog.Logger
	IDGen   func() string
}

// Report 是一次运行的结论（API.md 第 5 节 run 方法）。
type Report struct {
	SessionID string         `json:"sessionId"`
	Worker    string         `json:"worker"`
	Report    string         `json:"report"`
	Steps     int            `json:"steps"`
	Usage     contract.Usage `json:"usage"`
	Error     string         `json:"error,omitempty"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	Deduped   bool           `json:"deduped,omitempty"`
	// UsageNote 说明 usage 的口径限制（例如上游只给总 token），前端可据此提示。
	UsageNote string `json:"usageNote,omitempty"`
	// Steers 是本次运行实际并入上下文的插话条数。
	Steers int `json:"steers,omitempty"`
	// Model 是本次运行实际使用的模型（空表示 LLM 默认模型）。
	Model string `json:"model,omitempty"`

	// FinishReason 是最后一次模型调用的上游结束原因
	// （stop / length / tool_calls / content_filter；上游没给则为空）。
	FinishReason string `json:"finishReason,omitempty"`
	// EmptyReason 是"正文为空"的机器可读原因（见 EmptyReason* 常量）；
	// 有正文时为空。前端据此给不同的修复入口，不要靠解析文案。
	EmptyReason string `json:"emptyReason,omitempty"`
	// Diagnostics 是逐条排查线索（触顶截断 / 空 chunk / 推理模型 / 预算耗尽…）。
	// 有正文也可能有诊断（例如报告已生成但被步数上限截断）。
	Diagnostics []string `json:"diagnostics,omitempty"`
	// Reasoning 是本次运行最后一次模型输出的推理内容（推理模型才有；可能很长）。
	// 正文为空、报告由推理内容回退而来时，前端可以据此展示"正文其实是推理过程"。
	Reasoning string `json:"reasoning,omitempty"`
	// ContentFromReasoning 表示 Report.Report 是推理内容回退而来（正文实际为空）。
	ContentFromReasoning bool `json:"contentFromReasoning,omitempty"`
}

// Artifact 是运行产出的文件（当前只登记知识库回写）。
type Artifact struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type runHandle struct {
	cancel    context.CancelFunc
	started   time.Time
	retried   bool
	worker    WorkerID
	input     string
	sessionID string
	taskKey   string
	attempt   int
	// model 是本次运行实际使用的模型（自动重试沿用同一个模型）。
	model string
	// stopped 记录「用户主动停止」：用于把取消错误与真正失败区分开（状态显示「已停止」）。
	stopped bool
}

// maxRetriesPerTask 是单个任务允许的自动重试次数（看护器超时用）。
const maxRetriesPerTask = 1

// 插话（steering）相关上限与文案。
const (
	steerPrefix   = "[用户插话] "
	maxSteerRunes = 1000
	maxSteerQueue = 5
)

// steerQueue 是某个运行中会话的插话队列（step boundary 注入）。
type steerQueue struct {
	mu      sync.Mutex
	pending []string
}

type dedupedReport struct {
	report Report
	at     time.Time
	key    string
}

// Runtime 是 harness 的运行时：Router 决策 → Worker 执行 → Session 落盘 → Watchdog 看护。
type Runtime struct {
	opts      RuntimeOptions
	sessions  *SessionStore
	router    *Router
	templates *TemplateStore
	// custom 是自定义 worker 的唯一真源；templates 与它共享同一实例。
	custom *CustomWorkerStore
	log    *slog.Logger

	mu      sync.Mutex
	running map[string]*runHandle
	steer   map[string]*steerQueue
	// children 是按父会话分桶的后台子智能体通道（双工：派发 / 回收）。
	children map[string]*childHub
	recent   []dedupedReport
	attempts map[string]int
	watch    chan struct{}
	// ctxSnap 是这个进程里各会话最新的上下文用量快照（context 方法优先读它）。
	ctxSnap map[string]AgentContext
	// ctxUse 是按 sessionID 分桶的用量记账（累计用量 + 逐轮上下文采样，提供方上报为准）。
	ctxUse map[string]*contextAccount
	// methods / tools 是总控能力目录的两份索引：由 agentlab 注入（kernel.methods + 网关）。
	methods []MethodInfo
	tools   []ToolIndexEntry
}

// NewRuntime 构造运行时。
func NewRuntime(opts RuntimeOptions) *Runtime {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.IDGen == nil {
		opts.IDGen = func() string { return NewSessionID(opts.Clock()) }
	}
	custom := NewCustomWorkerStore(opts.Store)
	r := &Runtime{
		opts:      opts,
		sessions:  NewSessionStore(opts.Store, opts.Clock),
		router:    NewRouter(opts.Clock, opts.LLM),
		templates: NewTemplateStoreWith(opts.Store, custom),
		custom:    custom,
		log:       opts.Logger,
		running:   map[string]*runHandle{},
		steer:     map[string]*steerQueue{},
		children:  map[string]*childHub{},
		attempts:  map[string]int{},
		ctxSnap:   map[string]AgentContext{},
		ctxUse:    map[string]*contextAccount{},
	}
	// 收敛 worker 体系：只保留总控为内置常驻，其它 worker 由总控/系统生成。
	// 这里把 5 个预设 worker 落成自定义 worker（幂等，已存在则跳过）。
	// 失败不阻断构造：lookupWorker / 模板 / 调度都有静态定义兜底。
	if created, err := r.EnsurePresetWorkers(); err != nil {
		r.log.Warn("预置 worker 初始化失败（已回落静态定义）", "error", err)
	} else if len(created) > 0 {
		r.log.Info("已预置 worker", "workers", strings.Join(created, ", "))
	}
	return r
}

// Templates 返回值班模板存储（插件用它构造调度器、读写提示词）。
func (r *Runtime) Templates() *TemplateStore { return r.templates }

// systemPrompt 取 worker 当前生效的 system prompt：模板 prompt + 统一披露协议。
// 总控额外插入代码生成的动态 worker 目录，用户模板覆盖不了它。
func (r *Runtime) systemPrompt(spec WorkerSpec) string {
	prompt := ""
	if r.templates != nil {
		prompt = r.templates.Prompt(spec.ID)
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultPrompt(spec.ID)
	}
	if spec.ID == WorkerRouter {
		return composeSystemWithCatalog(prompt, r.workerCatalogText())
	}
	return composeSystem(prompt)
}

// ErrNoModel 是"没配模型"的统一中文错误文案（无 key 环境也要能立刻给出可操作提示）。
var ErrNoModel = errors.New("未配置模型：请在“模型设置”里填写 API Key 后再运行")

// HasModel 探测是否有可用模型：没有返回 ErrNoModel，读配置失败返回可操作错误。
// 调度器用它实现"未配置模型时不触发，只记 lastStatus"。
func (r *Runtime) HasModel(ctx context.Context) error {
	if r.opts.LLM == nil {
		return ErrNoModel
	}
	models, err := r.opts.LLM.Models(ctx)
	if err != nil {
		return fmt.Errorf("读取模型配置失败：%v（请在“模型设置”里检查）", err)
	}
	for _, m := range models {
		if m.HasKey {
			return nil
		}
	}
	return ErrNoModel
}

// RunScheduled 跑一次自动值班任务：用模板 prompt 当 system，input 为空。
// 走 execute 而不是 Run，避免被 Router 的 60s 去重窗口/每分钟限频误判成"重复触发"；
// 会话落盘、事件广播、知识库回写与手动运行完全一致。
func (r *Runtime) RunScheduled(ctx context.Context, worker WorkerID, prompt string) (Report, error) {
	return r.RunScheduledWithModel(ctx, worker, prompt, "")
}

// RunScheduledWithModel 是 RunScheduled 的显式模型版本：
// model 为空时回落到该 worker 模板里保存的模型，再空则用 LLM 默认模型。
func (r *Runtime) RunScheduledWithModel(ctx context.Context, worker WorkerID, prompt, model string) (Report, error) {
	spec, ok := r.lookupWorker(string(worker))
	if !ok {
		return Report{}, fmt.Errorf("未知 worker：%s（可用：%s）", worker, r.workerIDList())
	}
	text := strings.TrimSpace(prompt)
	if text == "" {
		text = defaultPrompt(spec.ID)
	}
	decision := RouteDecision{
		Worker: spec.ID,
		Fast:   true,
		Phase:  TradingPhase(r.opts.Clock()),
		Note:   "自动值班",
	}
	return r.executeWith(ctx, execRequest{
		Spec:           spec,
		Decision:       decision,
		Input:          "",
		SystemOverride: composeSystem(text),
		Model:          model,
	})
}

// Workers 返回全部 worker 列表（内置 + 自定义，总控固定第一）。
func (r *Runtime) Workers() []WorkerView { return r.workerViews() }

// Sessions 返回最近会话列表。
func (r *Runtime) Sessions(limit int) ([]SessionSummary, error) { return r.sessions.Summaries(limit) }

// Session 返回会话详情（事件按 seq 升序，可直接回放）。
func (r *Runtime) Session(sessionID string) (SessionDetail, error) {
	if strings.TrimSpace(sessionID) == "" {
		return SessionDetail{}, fmt.Errorf("session 需要 sessionId")
	}
	return r.sessions.Get(sessionID)
}

// Steer 给正在运行的会话插话：消息会在下一次模型调用前并入上下文（step boundary 注入）。
// 只对运行中的会话有效；已结束/不存在、空文本、超长、队列已满都返回可操作的中文错误。
func (r *Runtime) Steer(sessionID, text string) error {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return fmt.Errorf("插话需要 sessionId")
	}
	content := strings.TrimSpace(text)
	if content == "" {
		return fmt.Errorf("插话内容不能为空")
	}
	if n := len([]rune(content)); n > maxSteerRunes {
		return fmt.Errorf("插话内容过长（%d 字），请控制在 %d 字以内", n, maxSteerRunes)
	}

	r.mu.Lock()
	_, running := r.running[id]
	q := r.steer[id]
	if !running || q == nil {
		r.mu.Unlock()
		return fmt.Errorf("会话 %s 已结束，无法插话", id)
	}
	q.mu.Lock()
	if len(q.pending) >= maxSteerQueue {
		queued := len(q.pending)
		q.mu.Unlock()
		r.mu.Unlock()
		return fmt.Errorf("插话队列已满（%d 条），请等当前步骤执行完再发", queued)
	}
	q.pending = append(q.pending, content)
	queued := len(q.pending)
	q.mu.Unlock()
	r.mu.Unlock()

	// 事件先发（前端立刻看到），会话事件随后落盘，保证重放可见。
	r.emit("agent.steer", map[string]any{"sessionId": id, "text": content, "queued": queued})
	if _, err := r.sessions.Append(id, Event{Kind: EventUser, Role: string(contract.RoleUser), Content: steerPrefix + content}); err != nil {
		r.log.Warn("插话会话事件落盘失败", "session", id, "error", err)
	}
	return nil
}

// isRunning 判断会话是否正在运行。
func (r *Runtime) isRunning(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[sessionID]
	return ok
}

// Send 是前端唯一入口：会话正在运行时自动转为插话，空闲则启动一次运行并同步返回报告。
func (r *Runtime) Send(ctx context.Context, worker, input, sessionID string) (Report, error) {
	return r.SendWithModel(ctx, worker, input, sessionID, "")
}

// SendWithModel 是 Send 的显式模型版本；插话路径不涉及模型选择。
func (r *Runtime) SendWithModel(ctx context.Context, worker, input, sessionID, model string) (Report, error) {
	if strings.TrimSpace(sessionID) != "" && r.isRunning(sessionID) {
		if err := r.Steer(sessionID, input); err != nil {
			return Report{}, err
		}
		return Report{
			SessionID: sessionID,
			Worker:    r.displayWorker(worker),
			Report:    "已插话：这条消息会在下一次模型调用前并入上下文",
			Steers:    1,
		}, nil
	}
	return r.RunWithModel(ctx, worker, input, sessionID, model)
}

// displayWorker 把任意 worker 入参转成展示用 id（自定义 worker 保留原 id）。
func (r *Runtime) displayWorker(worker string) string {
	if cw, ok := r.custom.Get(worker); ok {
		return cw.ID
	}
	return string(NormalizeWorker(worker))
}

// workerIDList 返回可用 worker id 的逗号串（用于错误文案，含内置与自定义）。
func (r *Runtime) workerIDList() string {
	views := r.workerViews()
	ids := make([]string, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.ID)
	}
	return strings.Join(ids, " / ")
}

// resolveModel 决定一次运行实际使用的模型：显式 model > 模板 model > 空（LLM 默认）。
func (r *Runtime) resolveModel(spec WorkerSpec, explicit string) string {
	if m := strings.TrimSpace(explicit); m != "" {
		return m
	}
	if r.templates != nil {
		return strings.TrimSpace(r.templates.Model(spec.ID))
	}
	return ""
}

// SteerQueueLen 返回某个会话当前待注入的插话条数（未运行返回 0）。
func (r *Runtime) SteerQueueLen(sessionID string) int {
	r.mu.Lock()
	q := r.steer[sessionID]
	r.mu.Unlock()
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// beginSteer 为一次运行登记插话队列，返回"取走全部待注入插话"的 drain 函数。
func (r *Runtime) beginSteer(sessionID string) func() []string {
	q := &steerQueue{}
	r.mu.Lock()
	r.steer[sessionID] = q
	r.mu.Unlock()
	return func() []string {
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.pending) == 0 {
			return nil
		}
		out := q.pending
		q.pending = nil
		return out
	}
}

func (r *Runtime) endSteer(sessionID string) {
	r.mu.Lock()
	delete(r.steer, sessionID)
	r.mu.Unlock()
}

// Running 返回正在运行的会话 id 列表。
func (r *Runtime) Running() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.running))
	for id := range r.running {
		out = append(out, id)
	}
	return out
}

// Stop 取消一次正在运行的任务。
func (r *Runtime) Stop(sessionID string) bool {
	r.mu.Lock()
	h, ok := r.running[sessionID]
	if ok {
		h.stopped = true
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel()
	r.emitState(sessionID, "stopped", "已按用户请求停止")
	return true
}

// stoppedByUser 判断某次运行是否被用户主动停止（停止后取消错误不应显示成"失败"）。
func (r *Runtime) stoppedByUser(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.running[sessionID]; ok {
		return h.stopped
	}
	return false
}

// Run 执行一次任务：路由 → 建会话 → ReAct 循环 → 回写知识库 → 事件广播。
// 运行期错误写进 Report.Error（便于前端展示部分结论），只有参数/限频错误才返回 error。
func (r *Runtime) Run(ctx context.Context, worker, input, sessionID string) (Report, error) {
	return r.RunWithModel(ctx, worker, input, sessionID, "")
}

// RunWithModel 是 Run 的显式模型版本：
// worker 为空或 router 时走总控；显式内置 worker 走别名归一化；自定义 worker 精确匹配后直接执行。
//
// 预设 worker（premarket/intraday/postmarket/quant/chat）虽然也是自定义 worker，
// 但它们的 id 是**稳定别名**：必须继续走 Router，才能保住去重窗口与每分钟限频，
// 否则用户连点两下就会并发跑两份同样的盘前/复盘。用户自建的 worker 保持精确直通。
func (r *Runtime) RunWithModel(ctx context.Context, worker, input, sessionID, model string) (Report, error) {
	input = strings.TrimSpace(input)
	// 用户自建的自定义 worker id 不在别名表里，必须先精确匹配，避免被归一化成总控。
	if cw, ok := r.custom.Get(worker); ok && cw.CreatedBy != presetCreatedBy {
		spec := r.customSpec(cw)
		decision := RouteDecision{
			Worker: spec.ID,
			Fast:   true,
			Phase:  TradingPhase(r.opts.Clock()),
			Note:   "自定义 worker",
		}
		return r.executeWith(ctx, execRequest{Spec: spec, Decision: decision, Input: input, SessionID: sessionID, Model: model})
	}

	decision, err := r.router.Plan(ctx, RouteInput{Worker: worker, Input: input, SessionID: sessionID})
	if err != nil {
		return Report{}, err
	}
	spec, ok := r.lookupWorker(string(decision.Worker))
	if !ok {
		return Report{}, fmt.Errorf("未知 worker：%s（可用：%s）", worker, r.workerIDList())
	}
	dedupeKey := string(decision.Worker) + "|" + normalizeInput(input)
	if decision.Deduped {
		if prev, ok := r.replay(dedupeKey); ok {
			prev.Deduped = true
			return prev, nil
		}
		return Report{
			Worker:  string(decision.Worker),
			Report:  "重复触发已抑制：去重窗口内的相同请求不再重复执行，请稍后再试或换个问法。",
			Deduped: true,
		}, nil
	}

	return r.executeWith(ctx, execRequest{Spec: spec, Decision: decision, Input: input, SessionID: sessionID, Model: model})
}

// execRequest 是执行体的入参（字段较多，用具名结构避免位置错乱）。
type execRequest struct {
	Spec      WorkerSpec
	Decision  RouteDecision
	Input     string
	SessionID string
	// Model 是显式指定的模型；空表示回落到模板 model，再空用 LLM 默认。
	Model string
	// Extras 是额外的会话元信息（子智能体用：ParentID / Mode / Label）。
	Extras SessionMeta
	// SystemOverride 非空时用它当 system prompt（自动值班直接用模板 prompt）。
	SystemOverride string
}

// executeWith 是真正的执行体：建会话、跑 ReAct、落事件、回写知识库。
func (r *Runtime) executeWith(ctx context.Context, req execRequest) (Report, error) {
	spec, decision, input := req.Spec, req.Decision, req.Input
	dedupeKey := string(decision.Worker) + "|" + normalizeInput(input)
	sessionID := req.SessionID
	if strings.TrimSpace(sessionID) == "" {
		sessionID = r.opts.IDGen()
	}
	if r.isRunning(sessionID) {
		return Report{}, fmt.Errorf("会话 %s 正在运行，请用 steer 插话或先 stop", sessionID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 会话 id 放进 ctx：ReAct → Gateway → 工具（agent.spawn 等）据此定位当前会话。
	runCtx = WithSession(runCtx, sessionID)

	// system prompt：自动值班用模板 prompt，手动运行用当前模板，两者都经 composeSystem 追加披露协议。
	// 必须在建会话之前算好：系统提示词要作为会话的第一条事件落盘，轨迹才能审计「模型到底收到了什么」。
	system := req.SystemOverride
	if strings.TrimSpace(system) == "" {
		system = r.systemPrompt(spec)
	}

	meta := req.Extras
	meta.ID = sessionID
	meta.Worker = string(spec.ID)
	meta.Title = sessionTitle(spec, input)
	meta.Input = input
	meta.Model = r.resolveModel(spec, req.Model)
	meta.StartedAt = r.opts.Clock()
	meta.Status = StatusRunning
	if meta.Mode == "" {
		meta.Mode = ModeMain
	}
	if strings.TrimSpace(meta.Label) == "" {
		meta.Label = spec.Title
	}
	if err := r.sessions.Create(meta); err != nil {
		return Report{}, err
	}
	// 第一条事件就是系统提示词：轨迹 / 历史会话回放都能看到完整口径，不再只存在内存里。
	_, _ = r.sessions.Append(sessionID, Event{Kind: EventSystem, Role: string(contract.RoleSystem), Content: system})
	_, _ = r.sessions.Append(sessionID, Event{Kind: EventUser, Role: string(contract.RoleUser), Content: input})

	// 双工：为本次运行登记后台子智能体通道（派发与结果回收都走它）。
	hub := r.registerChildHub(sessionID)
	defer r.releaseChildHub(sessionID)

	drainSteers := r.beginSteer(sessionID)
	defer r.endSteer(sessionID)
	taskKey := string(spec.ID) + "|" + normalizeInput(input)
	r.track(sessionID, cancel, spec.ID, input, meta.Model, taskKey, r.attemptOf(taskKey))
	defer r.untrack(sessionID)
	r.emitState(sessionID, "thinking", spec.Title+" 正在分析…")

	onDelta := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		r.emit("agent.delta", map[string]string{"sessionId": sessionID, "text": text})
	}

	// 上下文用量：模型窗口/工具索引一次取齐，每步模型调用前回调一次。
	model, window := r.modelInfo(runCtx)
	toolsTokens := r.toolIndexTokens()
	onStep := func(step ReactStep) {
		snap := r.buildContext(sessionID, model, window, system, toolsTokens, step.Messages, step.Step, step.ToolCalls)
		r.rememberContext(snap)
		r.emit("agent.context", snap)
	}
	// 用量记账：每次模型调用拿到提供方 usage 后按会话累计一次（供上下文洞察页用）。
	onReply := func(usage contract.Usage) {
		r.recordUsage(sessionID, usage)
	}

	// 工具轨迹：step 是本次运行内的工具调用序号（从 1 起）。
	// start 分配 step、end 复用同一个 step（按 CallSeq 配对），因此同一个 id
	// 在 start/end 两条事件里保持一致；并发安全由 traceMu 保证。
	var (
		traceMu    sync.Mutex
		traceStep  int
		traceSteps = map[int]int{}
	)
	onTool := func(tr ToolTrace) {
		traceMu.Lock()
		step, found := traceSteps[tr.CallSeq]
		if tr.Phase == toolTraceStart || !found {
			traceStep++
			step = traceStep
			traceSteps[tr.CallSeq] = step
		}
		if tr.Phase == toolTraceEnd {
			delete(traceSteps, tr.CallSeq)
		}
		traceMu.Unlock()

		at := tr.At
		if at.IsZero() {
			at = r.opts.Clock()
		}
		payload := map[string]any{
			"sessionId": sessionID,
			"worker":    string(spec.ID),
			"id":        fmt.Sprintf("%s-%d", sessionID, step),
			"phase":     tr.Phase,
			"tool":      tr.Tool,
			"meta":      tr.Meta,
			"args":      r.redact(clipTrace(tr.Args, toolTraceArgsRunes)),
			"step":      step,
			"at":        at.Format(time.RFC3339),
		}
		if tr.Phase == toolTraceEnd {
			payload["result"] = r.redact(clipTrace(tr.Result, toolTraceResultRunes))
			payload["ok"] = tr.OK
			payload["error"] = r.redact(tr.Error)
			payload["ms"] = tr.MS
		}
		r.emit("agent.tool", payload)
	}

	outputTokens := r.maxOutputTokens()
	// 双工回调：后台子智能体的结果在模型的步骤边界注入，父会话不必阻塞在一个工具调用里。
	drainChildren := func() []string {
		results := hub.drain()
		if len(results) == 0 {
			return nil
		}
		out := make([]string, 0, len(results))
		for _, res := range results {
			out = append(out, childResultMessage(res))
		}
		return out
	}
	waitChild := func(ctx context.Context) (string, bool) {
		// 前台状态：等子智能体时明确告诉用户「在等谁」，而不是停在"思考中"假装还在想。
		r.emitState(sessionID, "waiting", "等待后台子智能体结果…")
		res, ok := hub.waitOne(ctx)
		if !ok {
			return "", false
		}
		r.emitState(sessionID, "thinking", "子智能体结果已收到，正在汇总结论…")
		return childResultMessage(res), true
	}
	react, err := buildReact(runCtx, reactConfig{
		LLM:             r.opts.LLM,
		Gateway:         r.opts.Gateway,
		Model:           meta.Model,
		System:          system,
		MaxSteps:        r.maxSteps(),
		MaxTokens:       r.maxTokens(),
		MaxOutputTokens: outputTokens,
		OnDelta:         onDelta,
		OnStep:          onStep,
		OnReply:         onReply,
		DrainSteers:     drainSteers,
		DrainChildren:   drainChildren,
		PendingChildren: hub.pending,
		WaitChild:       waitChild,
		OnTool:          onTool,
		Now:             r.opts.Clock,
	})
	if err != nil {
		r.finishFailed(sessionID, meta, err)
		return Report{SessionID: sessionID, Worker: string(spec.ID), Model: meta.Model, Error: err.Error()}, nil
	}

	res, runErr := react.Run(runCtx, buildUserMessage(spec, decision, input))
	// 用户主动停止：取消错误按「已停止」处理，不写成运行失败（两者修复动作完全不同）。
	stoppedByUser := runErr != nil && r.stoppedByUser(sessionID)
	// 停机时仍有后台子智能体在跑：把事实写进报告，不要让它静默消失。
	if still := hub.labels(); len(still) > 0 {
		res.Content = strings.TrimSpace(res.Content) + nl + "（本轮结束时仍有 " +
			strconv.Itoa(len(still)) + " 个子智能体在后台运行：" + strings.Join(still, "、") +
			"。可到「编队」页查看进度与各自报告。）"
	}

	// 运行结束后再发一帧 agent.context：此时记账里已有本轮全部提供方样本，
	// 最后一帧的 PressureTokens 才是「最近一次请求的实际提示词规模」而不是上一次的，
	// 顺便让事件流与 context 方法返回同一份 History。
	finalSnap := r.buildContext(sessionID, model, window, system, toolsTokens, toContractMessages(res.Transcript), res.Steps, res.ToolCalls)
	r.rememberContext(finalSnap)
	r.emit("agent.context", finalSnap)

	// 把对话轨迹落成只追加事件，便于前端回放与复盘。
	for _, e := range transcriptEvents(res.Transcript) {
		_, _ = r.sessions.Append(sessionID, e)
	}

	usage := contract.Usage{
		PromptTokens:     res.PromptTokens,
		CompletionTokens: res.CompletionTokens,
		TotalTokens:      res.Tokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	report := Report{
		SessionID:            sessionID,
		Worker:               string(spec.ID),
		Report:               strings.TrimSpace(res.Content),
		Steps:                res.Steps,
		Usage:                usage,
		Steers:               res.Steers,
		Model:                meta.Model,
		FinishReason:         res.FinishReason,
		Reasoning:            res.Reasoning,
		ContentFromReasoning: res.ContentFromReasoning,
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens > 0 {
		// 上游（如 DeepSeek 流式）可能只回 total；此时分项不可得，明确告知前端不要显示 0。
		report.UsageNote = "上游只返回总 token，分项不可得；展示请用 totalTokens"
	}
	if res.Exhausted {
		note := res.Note
		if note == "" {
			note = "已达到执行上限"
		}
		// 区分「预算停机」与「步数停机」，避免只给一句笼统的“已达上限”。
		reason := "执行上限"
		switch res.ExhaustKind {
		case "budget":
			reason = "token 预算耗尽"
		case "steps":
			reason = "步数上限"
		}
		tail := "（已停机 · " + reason + "：" + note + "。"
		if res.Finalized {
			tail += "报告已基于已获得的工具结果收口生成，原任务未完整跑完；缩小问题范围后重试可获得更完整分析。）"
		} else {
			tail += "以上是当前可得结论；缩小问题范围后重试可获得更完整分析。）"
		}
		report.Report = joinNonEmpty(report.Report, tail)
	}
	if stoppedByUser {
		report.Error = "已停止（用户中止）"
		report.Diagnostics = append(report.Diagnostics, "本轮由用户主动停止，已获得的部分结论保留")
		if report.Report == "" {
			report.Report = "本轮已按用户请求停止，暂无结论。"
		}
	} else if runErr != nil {
		report.Error = r.redact(runErr.Error())
		report.EmptyReason = EmptyReasonError
		report.Diagnostics = append(report.Diagnostics, "运行期错误："+report.Error)
		if report.Report == "" {
			report.Report = "本次运行未产出结论：" + report.Error
		}
	}
	// 触顶截断即使有正文也要留诊断：报告是"基于已获得信息收口"的，用户需要知道为什么停。
	if diags := truncationDiagnostics(res); len(diags) > 0 {
		report.Diagnostics = append(report.Diagnostics, diags...)
	}
	// 正文为空：不再套一句笼统的"可能被内容过滤或超时"，而是给出能定位的结论。
	// 有错误时错误本身就是原因，只补诊断，不覆盖报告。
	if report.Report == "" && report.Error == "" {
		diag := diagnoseEmptyReply(res, outputTokens)
		report.EmptyReason = diag.Reason
		report.Diagnostics = append(report.Diagnostics, diag.Diagnostics...)
		report.Report = describeEmptyReply(diag)
		if r.log != nil {
			r.log.Warn("本次运行没有产出正文", "session", sessionID, "worker", spec.ID,
				"reason", diag.Reason, "finish", res.FinishReason,
				"steps", res.Steps, "toolCalls", res.ToolCalls, "tokens", res.Tokens)
		}
	}
	if res.ContentFromReasoning {
		// 正文由推理内容回退而来：报告能看，但必须明确告诉前端"这不是正文"。
		if report.EmptyReason == "" {
			report.EmptyReason = EmptyReasonReasoningOnly
		}
		report.Diagnostics = append(report.Diagnostics,
			"检测到 reasoning_content 非空而 content 为空：该模型是推理模型，正文被推理占满",
			fmt.Sprintf("回退策略：已把推理内容当作正文展示并加标注（单次输出预算 %d；"+
				"调大它可以让模型腾出预算写正文）", outputTokens),
		)
	}

	ended := r.opts.Clock()
	meta.EndedAt = &ended
	meta.Steps = report.Steps
	meta.Tokens = usage.TotalTokens
	meta.Report = report.Report
	meta.Error = report.Error
	meta.Status = StatusOK
	if report.Error != "" && !stoppedByUser {
		meta.Status = StatusFailed
	}
	if err := r.sessions.Update(meta); err != nil {
		r.log.Warn("会话状态写回失败", "session", sessionID, "error", err)
	}
	reportOK := report.Error == ""
	_, _ = r.sessions.Append(sessionID, Event{Kind: EventReport, Content: report.Report, OK: &reportOK})

	if art := r.writebackKB(runCtx, report, res); art != nil {
		report.Artifacts = append(report.Artifacts, *art)
	}
	r.remember(dedupeKey, report)
	r.emitReport(report)

	state := "done"
	detail := fmt.Sprintf("完成：%d 步，%d 工具调用", report.Steps, res.ToolCalls)
	if stoppedByUser {
		state = "stopped"
		detail = "已停止（用户中止）"
	} else if report.Error != "" {
		state = "error"
		detail = report.Error
	}
	r.emitState(sessionID, state, detail)
	return report, nil
}

func (r *Runtime) finishFailed(sessionID string, meta SessionMeta, err error) {
	ended := r.opts.Clock()
	meta.EndedAt = &ended
	meta.Status = StatusFailed
	meta.Error = r.redact(err.Error())
	if uerr := r.sessions.Update(meta); uerr != nil {
		r.log.Warn("会话状态写回失败", "session", sessionID, "error", uerr)
	}
	bad := false
	_, _ = r.sessions.Append(sessionID, Event{Kind: EventError, Content: meta.Error, OK: &bad})
	r.emitState(sessionID, "error", meta.Error)
}

// maxSteps 取安全策略里的工具调用上限（Policy.MaxToolCalls）。
func (r *Runtime) maxSteps() int {
	if r.opts.Policy.MaxToolCalls > 0 {
		return r.opts.Policy.MaxToolCalls
	}
	return 24
}

// maxTokens 返回整次运行的 token 预算；0 表示**不设上限**。
//
// 默认策略就是 0（Token 不设预算上限）：长任务不会被预算中途截停，
// 停机只由步数上限（Policy.MaxToolCalls）与模型单次输出上限决定。
// 需要成本闸门时在策略里显式写一个 >0 的值，行为与旧版一致。
func (r *Runtime) maxTokens() int {
	if r.opts.Policy.MaxTokensPerRun > 0 {
		return r.opts.Policy.MaxTokensPerRun
	}
	return 0
}

func (r *Runtime) redact(s string) string {
	if r.opts.Guard == nil {
		return s
	}
	return r.opts.Guard.Redact(s)
}

// ---------- 事件 ----------

func (r *Runtime) emit(topic string, payload any) {
	if r.opts.Events == nil {
		return
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		r.log.Warn("事件序列化失败", "topic", topic, "error", err)
		return
	}
	r.opts.Events.Publish(plugin.Event{Topic: topic, Payload: buf})
}

func (r *Runtime) emitState(sessionID, state, detail string) {
	r.emit("agent.state", map[string]string{"sessionId": sessionID, "state": state, "detail": r.redact(detail)})
}

func (r *Runtime) emitReport(report Report) {
	r.emit("agent.report", map[string]any{
		"sessionId": report.SessionID,
		"worker":    report.Worker,
		"report":    report.Report,
		"usage":     report.Usage,
		"model":     report.Model,
	})
}

// ---------- 去重回放 ----------

const (
	recentReportCap  = 32
	recentReportLife = 10 * time.Minute
)

func (r *Runtime) remember(key string, report Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recent = append(r.recent, dedupedReport{report: report, at: r.opts.Clock(), key: key})
	if len(r.recent) > recentReportCap {
		r.recent = r.recent[len(r.recent)-recentReportCap:]
	}
}

func (r *Runtime) replay(key string) (Report, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.opts.Clock()
	kept := r.recent[:0]
	var found Report
	ok := false
	for _, item := range r.recent {
		if now.Sub(item.at) > recentReportLife {
			continue
		}
		kept = append(kept, item)
		if item.key == key {
			found = item.report
			ok = true
		}
	}
	r.recent = kept
	return found, ok
}

// ---------- 知识库回写 ----------

func (r *Runtime) writebackKB(ctx context.Context, report Report, res ReactResult) *Artifact {
	if r.opts.KB == nil || strings.TrimSpace(report.Report) == "" {
		return nil
	}
	now := r.opts.Clock()
	tag := "run/" + now.Format("2006-01-02") + "/" + report.Worker
	title := fmt.Sprintf("%s 运行记录 %s", report.Worker, now.Format("2006-01-02 15:04"))
	var b strings.Builder
	b.WriteString("# " + title + nl + nl)
	b.WriteString("## 结论" + nl + nl + report.Report + nl + nl)
	evidence := toolEvidence(res.Transcript)
	if evidence != "" {
		b.WriteString("## 证据" + nl + nl + evidence + nl + nl)
	}
	b.WriteString(fmt.Sprintf("## 运行信息%s%s会话 %s；步数 %d；工具调用 %d；token %d%s", nl, nl, report.SessionID, report.Steps, res.ToolCalls, report.Usage.TotalTokens, nl))
	if report.Error != "" {
		b.WriteString(nl + "## 错误" + nl + nl + report.Error + nl)
	}
	result, err := r.opts.KB.Import(ctx, progressive.ImportInput{
		Title:   title,
		Content: b.String(),
		Tags:    []string{tag},
		Source:  "session:" + report.SessionID,
	})
	if err != nil {
		r.log.Warn("结论回写知识库失败", "session", report.SessionID, "error", err)
		return nil
	}
	return &Artifact{Name: "知识库文档", Path: "kb://" + result.ID}
}

// transcriptEvents 把 eino 轨迹转成只追加的会话事件。
func transcriptEvents(transcript []*schema.Message) []Event {
	out := make([]Event, 0, len(transcript))
	for _, m := range transcript {
		if m == nil {
			continue
		}
		switch m.Role {
		case schema.Assistant:
			if strings.TrimSpace(m.Content) != "" {
				out = append(out, Event{Kind: EventAssistant, Role: string(contract.RoleAssistant), Content: m.Content})
			}
			for _, tc := range m.ToolCalls {
				out = append(out, Event{
					Kind:     EventTool,
					Role:     string(contract.RoleAssistant),
					ToolName: tc.Function.Name,
					Content:  tc.Function.Arguments,
				})
			}
		case schema.Tool:
			ok := !strings.HasPrefix(strings.TrimSpace(m.Content), "调用失败")
			out = append(out, Event{
				Kind:     EventTool,
				Role:     string(contract.RoleTool),
				ToolName: m.ToolName,
				Content:  truncateRunes(m.Content, 2000),
				OK:       &ok,
			})
		}
	}
	return out
}

// toolEvidence 把轨迹里的工具调用与结果整理成知识库用的证据清单。
func toolEvidence(transcript []*schema.Message) string {
	var b strings.Builder
	for _, m := range transcript {
		if m == nil {
			continue
		}
		for _, tc := range m.ToolCalls {
			b.WriteString("- 调用 " + tc.Function.Name + " 参数 " + truncateRunes(tc.Function.Arguments, 160) + nl)
		}
		if m.Role == schema.Tool && strings.TrimSpace(m.Content) != "" {
			b.WriteString("  - " + m.ToolName + " 返回：" + truncateRunes(strings.ReplaceAll(m.Content, nl, " "), 200) + nl)
		}
	}
	return b.String()
}

// ---------- Watchdog ----------

// StartWatchdog 启动心跳看护：任务超时先取消并重试一次，再超时则标记失败。
func (r *Runtime) StartWatchdog(ctx context.Context, interval, timeout time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	r.mu.Lock()
	if r.watch != nil {
		close(r.watch)
	}
	done := make(chan struct{})
	r.watch = done
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				r.tick(timeout)
			}
		}
	}()
}

// StopWatchdog 停止心跳。
func (r *Runtime) StopWatchdog() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watch != nil {
		close(r.watch)
		r.watch = nil
	}
}

func (r *Runtime) tick(timeout time.Duration) {
	now := r.opts.Clock()
	r.mu.Lock()
	handles := make([]*runHandle, 0, len(r.running))
	for _, h := range r.running {
		handles = append(handles, h)
	}
	r.mu.Unlock()

	for _, h := range handles {
		if h.worker == WorkerIntraday {
			// 盯盘心跳：只对正在运行的任务提示，避免空闲时刷屏。
			r.emitState(h.sessionID, "thinking", "盯盘心跳：任务仍在执行")
		}
		if now.Sub(h.started) < timeout {
			continue
		}
		if !h.retried {
			h.retried = true
			if r.tryRetry(h.taskKey) {
				r.log.Warn("任务超时，取消并重试一次", "session", h.sessionID, "worker", h.worker, "timeout", timeout.String())
				r.emitState(h.sessionID, "error", "任务超时，已中止并将自动重试一次")
				h.cancel()
				go r.retry(h.worker, h.input, h.model)
				continue
			}
			r.log.Warn("任务超时且重试次数已用尽，标记失败", "session", h.sessionID, "worker", h.worker)
			r.emitState(h.sessionID, "error", "任务超时且重试次数已用尽，已标记失败")
			h.cancel()
			continue
		}
		r.log.Warn("任务重试后仍超时，标记失败", "session", h.sessionID, "worker", h.worker)
		r.emitState(h.sessionID, "error", "任务重试后仍超时，已标记失败")
		h.cancel()
	}
}

// retry 用同一 worker/模型重跑一次（看护器超时自愈用）。
func (r *Runtime) retry(worker WorkerID, input, model string) {
	spec, ok := r.lookupWorker(string(worker))
	if !ok {
		return
	}
	// 重试使用独立 context：原 ctx 已被取消；同时跳过去重窗口，否则会被自己拦住。
	decision := RouteDecision{Worker: worker, Fast: true, Phase: TradingPhase(r.opts.Clock()), Note: "自动重试"}
	if _, err := r.executeWith(context.Background(), execRequest{Spec: spec, Decision: decision, Input: input, Model: model}); err != nil {
		r.log.Warn("自动重试失败", "worker", worker, "error", err)
	}
}

// track 登记运行中的会话句柄（含模型，供自动重试沿用）。
func (r *Runtime) track(sessionID string, cancel context.CancelFunc, worker WorkerID, input, model, taskKey string, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running[sessionID] = &runHandle{
		cancel:    cancel,
		started:   r.opts.Clock(),
		worker:    worker,
		input:     input,
		sessionID: sessionID,
		taskKey:   taskKey,
		attempt:   attempt,
		model:     model,
	}
}

// attemptOf 返回某个逻辑任务已经用掉的重试次数。
func (r *Runtime) attemptOf(taskKey string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[taskKey]
}

// tryRetry 占用一次重试名额；用尽后返回 false，避免超时任务无限自愈。
func (r *Runtime) tryRetry(taskKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempts[taskKey] >= maxRetriesPerTask {
		return false
	}
	r.attempts[taskKey]++
	if len(r.attempts) > 256 {
		for k, v := range r.attempts {
			if v == 0 {
				delete(r.attempts, k)
			}
		}
	}
	return true
}

func (r *Runtime) untrack(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, sessionID)
}

// ---------- 组装辅助 ----------

func sessionTitle(spec WorkerSpec, input string) string {
	if input == "" {
		return spec.Title
	}
	return spec.Title + " · " + truncateRunes(input, 20)
}

func buildUserMessage(spec WorkerSpec, decision RouteDecision, input string) string {
	var b strings.Builder
	b.WriteString("时段：" + phaseText(decision.Phase))
	b.WriteString(nl + "任务类型：" + spec.Title)
	if input == "" {
		b.WriteString(nl + nl + "请按你的职责给出当前最有价值的一份分析报告。")
		return b.String()
	}
	b.WriteString(nl + "用户请求：" + input)
	b.WriteString(nl + nl + "先按三级披露协议找工具/技能/知识，再给结论；结论必须标明依据。")
	return b.String()
}

func phaseText(phase string) string {
	switch phase {
	case "premarket":
		return "盘前（开盘前）"
	case "intraday":
		return "盘中（交易时段）"
	case "lunch":
		return "午间休市"
	case "postmarket":
		return "盘后"
	case "closed":
		return "非交易日"
	}
	return "未知时段"
}

func joinNonEmpty(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, nl+nl)
}
