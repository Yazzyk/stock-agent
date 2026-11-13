package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ---------- 上下文用量估算 ----------

// ContextNote 是估算口径说明（前端展示在上下文明细弹层底部）。
const ContextNote = "估算口径：中文≈1字/1token，英文≈4字符/1token；工具只算 L1 索引（名称+摘要+标签），不含 schema。"

// ctxSnapshotCap 是内存里保留的上下文快照条数上限（超了淘汰最旧的）。
const ctxSnapshotCap = 64

// EstimateTokens 估算一段文本的 token 数：
// 非 ASCII 字符（中文、中文标点、全角符号）按 1 token/字，ASCII 按 4 字符/token，向上取整。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii, other := 0, 0
	for _, r := range text {
		if r < 0x80 {
			ascii++
			continue
		}
		other++
	}
	tokens := other
	if ascii > 0 {
		tokens += (ascii + 3) / 4
	}
	return tokens
}

// ContextBreakdown 是上下文占用的分项估算。
type ContextBreakdown struct {
	SystemTokens  int `json:"systemTokens"`
	ToolsTokens   int `json:"toolsTokens"`
	MessageTokens int `json:"messageTokens"`
}

// ContextCache 是会话累计的提示词缓存命中统计（数值全部来自提供方上报）。
type ContextCache struct {
	UncachedInputTokens int `json:"uncachedInputTokens"` // prompt - cached
	CacheReadTokens     int `json:"cacheReadTokens"`     // 提供方上报的命中数
	OutputTokens        int `json:"outputTokens"`
	HitPercent          int `json:"hitPercent"` // cached/prompt*100，整数；prompt=0 时为 0
}

// ContextUsage 是会话累计的用量（提示词/输出/总量），用于上下文洞察页的「会话累计」一栏。
type ContextUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CachedTokens     int `json:"cachedTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// ContextPoint 是一次模型调用的上下文采样（后端权威的逐轮历史）。
type ContextPoint struct {
	Step             int    `json:"step"`         // 第几次模型调用（从 1 起）
	PromptTokens     int    `json:"promptTokens"` // 该请求 prompt（提供方；缺失则 0）
	CachedTokens     int    `json:"cachedTokens"`
	CompletionTokens int    `json:"completionTokens"`
	Used             int    `json:"used"` // 该请求的上下文规模：PromptTokens>0 时用它，否则用估算的 usedTokens
	At               string `json:"at"`   // RFC3339（上海时区，与现有 UpdatedAt 一致）
}

// AgentContext 是上下文用量快照：既是 context 方法的返回，也是 agent.context 事件的 payload。
type AgentContext struct {
	SessionID     string           `json:"sessionId"`
	Model         string           `json:"model"`
	ContextWindow int              `json:"contextWindow"`
	UsedTokens    int              `json:"usedTokens"`
	Percent       int              `json:"percent"`
	Breakdown     ContextBreakdown `json:"breakdown"`
	Messages      int              `json:"messages"`
	Steps         int              `json:"steps"`
	ToolCalls     int              `json:"toolCalls"`
	UpdatedAt     string           `json:"updatedAt"`
	Note          string           `json:"note,omitempty"`

	// PressureTokens 是最近一次请求实际发送的提示词规模（提供方上报；0 表示还没有样本）。
	PressureTokens int `json:"pressureTokens,omitempty"`
	// Sample 标记占用数字的来源："provider"（有提供方样本）/"estimated"（纯估算）。
	Sample string `json:"sample,omitempty"`
	// Cache 是会话累计的提示词缓存命中（提供方上报的累计值）。
	Cache ContextCache `json:"cache,omitempty"`
	// Usage 是会话累计的用量（提示词/输出/总量），用于面板「会话累计」一栏。
	Usage ContextUsage `json:"usage,omitempty"`
	// History 是本会话逐次模型调用的上下文采样（后端权威，最多 120 点，按 step 升序）。
	History []ContextPoint `json:"history,omitempty"`
}

const (
	// contextHistoryCap 是每个会话保留的逐次上下文采样上限（超出丢最旧的）。
	contextHistoryCap = 120
	// contextAccountCap 是内存里保留记账的会话数上限（超了淘汰最久未更新的）。
	contextAccountCap = 128
)

// contextAccount 是某个会话的累计用量与逐次上下文采样（后端权威）。
// 全部字段由 recordUsage 写入，读写都由 Runtime.mu 保护。
type contextAccount struct {
	prompt     int
	cached     int
	completion int
	total      int
	// pressure 是最近一次提供方上报的 promptTokens（0 表示还没有样本）。
	pressure int
	// step 是已经发生的模型调用次数（历史被截断后仍保持 step 连续递增）。
	step    int
	used    int // 该会话最近一次快照的 UsedTokens（没有 prompt 样本时的兜底）
	history []ContextPoint
	at      time.Time
}

// percentOf 计算占用百分比；窗口未知（0）时返回 0，前端据此只显示已用 token。
func percentOf(used, window int) int {
	if window <= 0 || used <= 0 {
		return 0
	}
	p := used * 100 / window
	if p > 100 {
		p = 100
	}
	return p
}

// contextPercent 计算上下文占用百分比：优先用提供方上报的 pressureTokens，
// 没有样本时回落到启发式 usedTokens（percentOf），保持旧行为。
func contextPercent(pressure, used, window int) int {
	if pressure > 0 {
		return percentOf(pressure, window)
	}
	return percentOf(used, window)
}

// cacheHitPercent 计算缓存命中率（整数百分比）；prompt=0 时为 0，上限 100。
func cacheHitPercent(cached, prompt int) int {
	if prompt <= 0 || cached <= 0 {
		return 0
	}
	p := cached * 100 / prompt
	if p > 100 {
		p = 100
	}
	return p
}

// uncachedTokens 返回未命中缓存的提示词 token（prompt - cached）；异常数据按 0 处理。
func uncachedTokens(prompt, cached int) int {
	if prompt <= 0 {
		return 0
	}
	if cached <= 0 {
		return prompt
	}
	if cached >= prompt {
		return 0
	}
	return prompt - cached
}

// messageTokens 估算消息列表的 token：正文 + 工具调用的名称与参数。
func messageTokens(msgs []contract.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		for _, tc := range m.ToolCalls {
			total += EstimateTokens(tc.Name) + EstimateTokens(tc.Arguments)
		}
	}
	return total
}

// modelInfo 取默认模型名与上下文窗口；取不到时返回空名与 0 窗口（只显示已用 token）。
func (r *Runtime) modelInfo(ctx context.Context) (string, int) {
	if r.opts.LLM == nil {
		return "", 0
	}
	info, err := r.opts.LLM.Default(ctx)
	if err != nil {
		return "", 0
	}
	return info.Model, info.ContextWindow
}

// toolIndexTokens 估算 L1 工具索引（名称+摘要+标签）的 token；不含 schema（渐进披露）。
func (r *Runtime) toolIndexTokens() int {
	if r.opts.Gateway == nil {
		return 0
	}
	entries := r.opts.Gateway.ToolIndex()
	if len(entries) == 0 {
		return 0
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name)
		if len(e.Tags) > 0 {
			b.WriteString(" ")
			b.WriteString(strings.Join(e.Tags, ","))
		}
		b.WriteString(" ")
		b.WriteString(e.Summary)
		b.WriteString(nl)
	}
	return EstimateTokens(b.String())
}

// buildContext 组装一次上下文快照（估算分项 + 该会话的提供方占用/缓存/逐轮历史）。
func (r *Runtime) buildContext(sessionID, model string, window int, system string, toolsTokens int, msgs []contract.Message, steps, toolCalls int) AgentContext {
	breakdown := ContextBreakdown{
		SystemTokens:  EstimateTokens(system),
		ToolsTokens:   toolsTokens,
		MessageTokens: messageTokens(msgs),
	}
	used := breakdown.SystemTokens + breakdown.ToolsTokens + breakdown.MessageTokens
	snap := AgentContext{
		SessionID:     sessionID,
		Model:         model,
		ContextWindow: window,
		UsedTokens:    used,
		Breakdown:     breakdown,
		Messages:      len(msgs),
		Steps:         steps,
		ToolCalls:     toolCalls,
		UpdatedAt:     r.opts.Clock().In(shanghaiLoc()).Format(time.RFC3339),
		Note:          ContextNote,
	}
	r.decorateContext(&snap)
	return snap
}

// decorateContext 把会话记账填进快照：提供方占用、缓存/累计用量、逐轮历史，
// 并按「有提供方样本就用 pressure，否则用启发式 used」重算百分比。
// 同时把本次快照的 UsedTokens 记为该会话的兜底估算：
// 某次请求拿不到 prompt 样本时，采样点（ContextPoint.Used）就用它。
func (r *Runtime) decorateContext(snap *AgentContext) {
	if snap == nil || strings.TrimSpace(snap.SessionID) == "" {
		return
	}
	r.mu.Lock()
	acc := r.contextAccountLocked(snap.SessionID)
	acc.used = snap.UsedTokens
	pressure := acc.pressure
	snap.PressureTokens = 0
	snap.Sample = "estimated"
	if pressure > 0 {
		snap.PressureTokens = pressure
		snap.Sample = "provider"
	}
	snap.Cache = ContextCache{
		UncachedInputTokens: uncachedTokens(acc.prompt, acc.cached),
		CacheReadTokens:     acc.cached,
		OutputTokens:        acc.completion,
		HitPercent:          cacheHitPercent(acc.cached, acc.prompt),
	}
	snap.Usage = ContextUsage{
		PromptTokens:     acc.prompt,
		CachedTokens:     acc.cached,
		CompletionTokens: acc.completion,
		TotalTokens:      acc.total,
	}
	snap.History = append([]ContextPoint(nil), acc.history...)
	r.pruneContextAccountsLocked(snap.SessionID)
	r.mu.Unlock()
	snap.Percent = contextPercent(snap.PressureTokens, snap.UsedTokens, snap.ContextWindow)
}

// contextAccountLocked 取某个会话的记账桶（懒创建）；调用方必须持有 r.mu。
func (r *Runtime) contextAccountLocked(sessionID string) *contextAccount {
	if r.ctxUse == nil {
		r.ctxUse = map[string]*contextAccount{}
	}
	acc := r.ctxUse[sessionID]
	if acc == nil {
		acc = &contextAccount{}
		r.ctxUse[sessionID] = acc
	}
	return acc
}

// recordUsage 按会话累计一次模型调用的用量（prompt/cached/completion/total），
// 追加一条 ContextPoint（step 从 1 递增，最多 contextHistoryCap 条，超出丢最旧的），
// 并把本次 promptTokens 记为该会话的 pressure（最近一次请求的实际提示词规模）。
func (r *Runtime) recordUsage(sessionID string, usage contract.Usage) {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	prompt := usage.PromptTokens
	cached := usage.CachedTokens
	completion := usage.CompletionTokens
	total := usage.TotalTokens
	if total == 0 {
		total = prompt + completion
	}
	at := r.opts.Clock()

	r.mu.Lock()
	defer r.mu.Unlock()
	acc := r.contextAccountLocked(id)
	acc.prompt += prompt
	acc.cached += cached
	acc.completion += completion
	acc.total += total
	if prompt > 0 {
		acc.pressure = prompt
	}
	acc.step++
	point := ContextPoint{
		Step:             acc.step,
		PromptTokens:     prompt,
		CachedTokens:     cached,
		CompletionTokens: completion,
		Used:             prompt,
		At:               at.In(shanghaiLoc()).Format(time.RFC3339),
	}
	if point.Used <= 0 {
		// 上游没给 prompt：用该会话最近一次快照的估算值兜底，别写 0。
		point.Used = acc.used
	}
	acc.history = append(acc.history, point)
	if len(acc.history) > contextHistoryCap {
		acc.history = append([]ContextPoint(nil), acc.history[len(acc.history)-contextHistoryCap:]...)
	}
	acc.at = at
	r.pruneContextAccountsLocked(id)
}

// pruneContextAccountsLocked 控制记账桶数量：超出上限时淘汰最久未更新的会话（当前会话除外）。
func (r *Runtime) pruneContextAccountsLocked(keep string) {
	if len(r.ctxUse) <= contextAccountCap {
		return
	}
	oldestID := ""
	var oldest time.Time
	for id, acc := range r.ctxUse {
		if id == keep {
			continue
		}
		if oldestID == "" || acc.at.Before(oldest) {
			oldestID, oldest = id, acc.at
		}
	}
	if oldestID != "" {
		delete(r.ctxUse, oldestID)
	}
}

// rememberContext 记下某会话最新一次上下文快照（供 context 方法回读）。
func (r *Runtime) rememberContext(snap AgentContext) {
	if strings.TrimSpace(snap.SessionID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctxSnap == nil {
		r.ctxSnap = map[string]AgentContext{}
	}
	r.ctxSnap[snap.SessionID] = snap
	if len(r.ctxSnap) <= ctxSnapshotCap {
		return
	}
	// 超出上限：淘汰 UpdatedAt 最旧的一条。
	oldestID, oldestAt := "", ""
	for id, s := range r.ctxSnap {
		if oldestID == "" || s.UpdatedAt < oldestAt {
			oldestID, oldestAt = id, s.UpdatedAt
		}
	}
	if oldestID != "" && oldestID != snap.SessionID {
		delete(r.ctxSnap, oldestID)
	}
}

// Context 回读某个会话的上下文用量：
// 运行中优先用每步记录的快照；否则由会话事件 + 当前模板 + 模型窗口重建。
func (r *Runtime) Context(ctx context.Context, sessionID string) (AgentContext, error) {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return AgentContext{}, fmt.Errorf("context 需要 sessionId")
	}
	detail, err := r.sessions.Get(id)
	if err != nil {
		return AgentContext{}, err
	}
	model, window := r.modelInfo(ctx)

	r.mu.Lock()
	snap, ok := r.ctxSnap[id]
	r.mu.Unlock()
	if ok {
		// 窗口/模型可能变了（用户改了模型设置），以当前为准。
		snap.Model = model
		snap.ContextWindow = window
		// 从记账桶刷新提供方占用/缓存/累计用量/逐轮历史（运行中读到的是最新值），
		// 并按「有 pressure 用 pressure」重算百分比。
		r.decorateContext(&snap)
		return snap, nil
	}

	spec, _ := r.lookupWorker(detail.Session.Worker)
	msgs := eventsToMessages(detail.Events)
	snap = r.buildContext(id, model, window, r.systemPrompt(spec), r.toolIndexTokens(), msgs, detail.Session.Steps, countToolCalls(detail.Events))
	r.rememberContext(snap)
	return snap, nil
}

// eventsToMessages 把只追加的会话事件还原成消息列表（用于事后重建上下文）。
func eventsToMessages(events []Event) []contract.Message {
	out := make([]contract.Message, 0, len(events))
	for _, e := range events {
		switch e.Kind {
		case EventUser, EventAssistant, EventTool:
		default:
			continue
		}
		content := e.Content
		if strings.TrimSpace(content) == "" {
			continue
		}
		msg := contract.Message{Content: content}
		switch e.Role {
		case string(contract.RoleAssistant):
			msg.Role = contract.RoleAssistant
		case string(contract.RoleTool):
			msg.Role = contract.RoleTool
			msg.Name = e.ToolName
		default:
			msg.Role = contract.RoleUser
		}
		out = append(out, msg)
	}
	return out
}

// countToolCalls 统计会话里发生过多少次工具调用（assistant 侧的 tool 事件）。
func countToolCalls(events []Event) int {
	n := 0
	for _, e := range events {
		if e.Kind == EventTool && e.Role == string(contract.RoleAssistant) {
			n++
		}
	}
	return n
}
