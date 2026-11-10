package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
}

// maxRetriesPerTask 是单个任务允许的自动重试次数（看护器超时用）。
const maxRetriesPerTask = 1

type dedupedReport struct {
	report Report
	at     time.Time
	key    string
}

// Runtime 是 harness 的运行时：Router 决策 → Worker 执行 → Session 落盘 → Watchdog 看护。
type Runtime struct {
	opts     RuntimeOptions
	sessions *SessionStore
	router   *Router
	log      *slog.Logger

	mu       sync.Mutex
	running  map[string]*runHandle
	recent   []dedupedReport
	attempts map[string]int
	watch    chan struct{}
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
	r := &Runtime{
		opts:     opts,
		sessions: NewSessionStore(opts.Store, opts.Clock),
		router:   NewRouter(opts.Clock, opts.LLM),
		log:      opts.Logger,
		running:  map[string]*runHandle{},
		attempts: map[string]int{},
	}
	return r
}

// Workers 返回 worker 列表。
func (r *Runtime) Workers() []WorkerView { return WorkerViews() }

// Sessions 返回最近会话列表。
func (r *Runtime) Sessions(limit int) ([]SessionSummary, error) { return r.sessions.Summaries(limit) }

// Session 返回会话详情（事件按 seq 升序，可直接回放）。
func (r *Runtime) Session(sessionID string) (SessionDetail, error) {
	if strings.TrimSpace(sessionID) == "" {
		return SessionDetail{}, fmt.Errorf("session 需要 sessionId")
	}
	return r.sessions.Get(sessionID)
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
	r.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel()
	return true
}

// Run 执行一次任务：路由 → 建会话 → ReAct 循环 → 回写知识库 → 事件广播。
// 运行期错误写进 Report.Error（便于前端展示部分结论），只有参数/限频错误才返回 error。
func (r *Runtime) Run(ctx context.Context, worker, input, sessionID string) (Report, error) {
	input = strings.TrimSpace(input)
	decision, err := r.router.Plan(ctx, RouteInput{Worker: worker, Input: input, SessionID: sessionID})
	if err != nil {
		return Report{}, err
	}
	spec, ok := LookupWorker(string(decision.Worker))
	if !ok {
		return Report{}, fmt.Errorf("未知 worker：%s（可用：premarket/intraday/postmarket/quant/chat）", worker)
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

	return r.execute(ctx, spec, decision, input, sessionID)
}

// execute 是去掉路由与去重之后的执行体：建会话、跑 ReAct、落事件、回写知识库。
// 自动重试直接走这里，避免被去重窗口拦住。
func (r *Runtime) execute(ctx context.Context, spec WorkerSpec, decision RouteDecision, input, sessionID string) (Report, error) {
	dedupeKey := string(decision.Worker) + "|" + normalizeInput(input)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = r.opts.IDGen()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	meta := SessionMeta{
		ID:        sessionID,
		Worker:    string(spec.ID),
		Title:     sessionTitle(spec, input),
		Input:     input,
		StartedAt: r.opts.Clock(),
		Status:    StatusRunning,
	}
	if err := r.sessions.Create(meta); err != nil {
		return Report{}, err
	}
	_, _ = r.sessions.Append(sessionID, Event{Kind: EventUser, Role: string(contract.RoleUser), Content: input})

	taskKey := string(spec.ID) + "|" + normalizeInput(input)
	r.track(sessionID, cancel, spec.ID, input, taskKey, r.attemptOf(taskKey))
	defer r.untrack(sessionID)
	r.emitState(sessionID, "thinking", spec.Title+" 正在分析…")

	onDelta := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		r.emit("agent.delta", map[string]string{"sessionId": sessionID, "text": text})
	}

	react, err := buildReact(runCtx, reactConfig{
		LLM:       r.opts.LLM,
		Gateway:   r.opts.Gateway,
		System:    spec.System,
		MaxSteps:  r.maxSteps(),
		MaxTokens: r.maxTokens(),
		OnDelta:   onDelta,
	})
	if err != nil {
		r.finishFailed(sessionID, meta, err)
		return Report{SessionID: sessionID, Worker: string(spec.ID), Error: err.Error()}, nil
	}

	res, runErr := react.Run(runCtx, buildUserMessage(spec, decision, input))

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
		SessionID: sessionID,
		Worker:    string(spec.ID),
		Report:    strings.TrimSpace(res.Content),
		Steps:     res.Steps,
		Usage:     usage,
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
		report.Report = joinNonEmpty(report.Report,
			"（已停机："+note+"。以上是当前可得结论；缩小问题范围后重试可获得更完整分析。）")
	}
	if runErr != nil {
		report.Error = r.redact(runErr.Error())
		if report.Report == "" {
			report.Report = "本次运行未产出结论：" + report.Error
		}
	}
	if report.Report == "" && report.Error == "" {
		report.Report = "模型没有返回内容（可能被内容过滤或超时），请重试。"
	}

	ended := r.opts.Clock()
	meta.EndedAt = &ended
	meta.Steps = report.Steps
	meta.Tokens = usage.TotalTokens
	meta.Report = report.Report
	meta.Error = report.Error
	meta.Status = StatusOK
	if report.Error != "" {
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
	if report.Error != "" {
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

func (r *Runtime) maxTokens() int {
	if r.opts.Policy.MaxTokensPerRun > 0 {
		return r.opts.Policy.MaxTokensPerRun
	}
	return 60000
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
				go r.retry(h.worker, h.input)
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

func (r *Runtime) retry(worker WorkerID, input string) {
	spec, ok := LookupWorker(string(worker))
	if !ok {
		return
	}
	// 重试使用独立 context：原 ctx 已被取消；同时跳过去重窗口，否则会被自己拦住。
	decision := RouteDecision{Worker: worker, Fast: true, Phase: TradingPhase(r.opts.Clock()), Note: "自动重试"}
	if _, err := r.execute(context.Background(), spec, decision, input, ""); err != nil {
		r.log.Warn("自动重试失败", "worker", worker, "error", err)
	}
}

func (r *Runtime) track(sessionID string, cancel context.CancelFunc, worker WorkerID, input, taskKey string, attempt int) {
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
