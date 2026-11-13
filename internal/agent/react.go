package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// maxToolOutputRunes 是单次工具结果喂给模型的上限，防止一步把上下文撑爆。
const maxToolOutputRunes = 12000

// defaultMaxOutputTokens 是单次模型调用的默认输出预算。
// 推理模型（DeepSeek V4 等）先花预算写 reasoning_content，2048 常常只够推理、
// 正文被挤成空串；8192 能覆盖绝大多数推理模型的思维链 + 正文。
// 可用 Policy.MaxTokensPerRun 收紧（见 Runtime.maxOutputTokens），不会放大成本上限。
const defaultMaxOutputTokens = 8192

// 收口调用的边界：预算/步数耗尽后只取最近若干条轨迹，并对单条输出截断，
// 避免"收口这一次调用"本身超窗。
const (
	// minFinalizeRunes 是判定"最后一条 assistant 已给出实质结论"的最短字数。
	minFinalizeRunes = 60
	// finalizeMaxMessages 是收口调用最多回看的轨迹条数。
	finalizeMaxMessages = 16
	// finalizeMaxRunes 是收口调用轨迹文本的总字数上限。
	finalizeMaxRunes = 12000
	// finalizeToolRunes 是单条工具结果的截断上限。
	finalizeToolRunes = 1500
	// finalizeAssistRunes 是单条模型中间结论的截断上限。
	finalizeAssistRunes = 800
	// finalizeUserRunes 是原始任务/用户消息的截断上限。
	finalizeUserRunes = 1200
)

// finalizeInstruction 是收口调用的固定指令：要求模型不再调工具，直接按四段式给出报告。
const finalizeInstruction = `token 预算/步数已经用尽，这是本轮最后一次模型调用，不能再调用任何工具。
请立刻基于下面已经获得的信息写出最终报告，必须包含四个二级标题：
## 结论
## 依据
## 不确定性
## 风险提示
要求：结论先给判断；依据只引用下面出现过的工具与数据；不确定性与风险提示要具体；不要重复罗列原始工具输出。`

// ---------- JSON Schema → eino 参数（不引额外依赖） ----------

type jsonSchemaProp struct {
	Type        string                     `json:"type"`
	Description string                     `json:"description"`
	Items       *jsonSchemaProp            `json:"items,omitempty"`
	Properties  map[string]*jsonSchemaProp `json:"properties,omitempty"`
	Required    []string                   `json:"required,omitempty"`
	Enum        []string                   `json:"enum,omitempty"`
}

type jsonSchemaDoc struct {
	Type       string                     `json:"type"`
	Properties map[string]*jsonSchemaProp `json:"properties"`
	Required   []string                   `json:"required"`
}

// paramsFromSchema 把工具自带的 JSON Schema 转成 eino 的参数描述。
// 解析失败返回 nil（视为无参数），不影响工具注册——schema 合法性由插件作者负责。
func paramsFromSchema(raw json.RawMessage) *schema.ParamsOneOf {
	if len(raw) == 0 {
		return nil
	}
	var doc jsonSchemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Properties) == 0 {
		return nil
	}
	required := map[string]bool{}
	for _, r := range doc.Required {
		required[r] = true
	}
	params := make(map[string]*schema.ParameterInfo, len(doc.Properties))
	for name, p := range doc.Properties {
		params[name] = toParameterInfo(p, required[name])
	}
	return schema.NewParamsOneOfByParams(params)
}

func toParameterInfo(p *jsonSchemaProp, required bool) *schema.ParameterInfo {
	if p == nil {
		return &schema.ParameterInfo{Type: schema.String, Required: required}
	}
	info := &schema.ParameterInfo{
		Type:     dataTypeOf(p.Type),
		Desc:     p.Description,
		Required: required,
		Enum:     p.Enum,
	}
	switch info.Type {
	case schema.Array:
		if p.Items != nil {
			info.ElemInfo = toParameterInfo(p.Items, false)
		}
	case schema.Object:
		if len(p.Properties) > 0 {
			sub := make(map[string]*schema.ParameterInfo, len(p.Properties))
			subRequired := map[string]bool{}
			for _, r := range p.Required {
				subRequired[r] = true
			}
			for name, sp := range p.Properties {
				sub[name] = toParameterInfo(sp, subRequired[name])
			}
			info.SubParams = sub
		}
	}
	return info
}

func dataTypeOf(t string) schema.DataType {
	switch t {
	case "string":
		return schema.String
	case "integer":
		return schema.Integer
	case "number":
		return schema.Number
	case "boolean":
		return schema.Boolean
	case "array":
		return schema.Array
	case "null":
		return schema.Null
	default:
		return schema.Object
	}
}

// ---------- 元工具 → eino 工具 ----------

// invokableTool 把插件的元工具（tool.search/describe/call）包成 eino 可执行工具。
// 元工具在进程内直接执行；其中 tool.call 内部再经渐进式披露网关走 kernel（L3 落点）。
type invokableTool struct {
	def plugin.Tool
	// onTool 非空时在工具执行前后各回调一次（agent.tool 轨迹）；
	// 为空时完全跳过轨迹构造，行为与未加轨迹前一致。
	onTool func(ToolTrace)
	// seq 是同一次 ReAct 运行内共享的工具调用序号计数器，
	// 让同一次调用的 start/end 携带同一个 CallSeq（Runtime 据此配对 step/id）。
	seq *atomic.Int64
	// now 便于测试注入时钟；为空时用 time.Now。
	now func() time.Time
}

// trace 构造一条轨迹：tool.call 时把展示用的工具名/参数解析成内层工具；
// 解析失败保留元工具自身（轨迹是旁路观测，任何情况下都不报错）。
func (t *invokableTool) trace(seq int, phase, argsJSON string, at time.Time) ToolTrace {
	tool, args := t.def.Name, argsJSON
	if t.def.Name == "tool.call" {
		if name, inner, ok := parseToolCallTarget(argsJSON); ok {
			tool, args = name, inner
		}
	}
	return ToolTrace{
		CallSeq: seq,
		Phase:   phase,
		Meta:    t.def.Name,
		Tool:    tool,
		Args:    args,
		At:      at,
	}
}

func (t *invokableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	desc := t.def.Summary
	if len(t.def.Examples) > 0 {
		desc += "；示例：" + t.def.Examples[0]
	}
	return &schema.ToolInfo{
		Name:        t.def.Name,
		Desc:        desc,
		ParamsOneOf: paramsFromSchema(t.def.Schema),
	}, nil
}

// InvokableRun 把工具错误变成"给模型看的错误文本"，让 ReAct 循环能自我纠正而不是整轮失败。
// 工具执行前后各回调一次 onTool（若已设置），用于向事件总线发 agent.tool 轨迹。
func (t *invokableTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	args := json.RawMessage(argumentsInJSON)
	if strings.TrimSpace(argumentsInJSON) == "" {
		args = json.RawMessage("{}")
		argumentsInJSON = "{}"
	}
	now := t.now
	if now == nil {
		now = time.Now
	}
	onTool := t.onTool
	seq := 0
	startAt := now()
	if onTool != nil {
		if t.seq != nil {
			seq = int(t.seq.Add(1))
		}
		onTool(t.trace(seq, toolTraceStart, argumentsInJSON, startAt))
	}
	out, err := t.def.Run(ctx, args)
	endAt := now()
	if err != nil {
		if onTool != nil {
			tr := t.trace(seq, toolTraceEnd, argumentsInJSON, endAt)
			tr.OK = false
			tr.Error = err.Error()
			tr.MS = endAt.Sub(startAt).Milliseconds()
			onTool(tr)
		}
		return "调用失败：" + err.Error() + "（先 tool.search 确认工具名，再 tool.describe 确认参数）", nil
	}
	text := ""
	if raw, ok := out.(json.RawMessage); ok {
		text = string(raw)
	} else if buf, mErr := json.Marshal(out); mErr == nil {
		text = string(buf)
	} else {
		text = fmt.Sprint(out)
	}
	if onTool != nil {
		// 轨迹用截断前的原始结果做预览（Runtime 再按 rune 截到 1200）。
		tr := t.trace(seq, toolTraceEnd, argumentsInJSON, endAt)
		tr.OK = true
		tr.Result = text
		tr.MS = endAt.Sub(startAt).Milliseconds()
		onTool(tr)
	}
	if r := []rune(text); len(r) > maxToolOutputRunes {
		text = string(r[:maxToolOutputRunes]) + "…（结果过长已截断，请缩小查询范围）"
	}
	return text, nil
}

// einoTools 把网关的元工具包装成 eino 工具；onTool 非空时附带工具轨迹回调。
func einoTools(gw *progressive.Gateway, onTool func(ToolTrace), now func() time.Time) []tool.BaseTool {
	if gw == nil {
		return nil
	}
	metas := gw.MetaTools()
	out := make([]tool.BaseTool, 0, len(metas))
	// 同一次运行的多个元工具共享一个序号计数器，保证 step 连续且 start/end 可配对。
	seq := &atomic.Int64{}
	if now == nil {
		now = time.Now
	}
	for _, m := range metas {
		out = append(out, &invokableTool{def: m, onTool: onTool, seq: seq, now: now})
	}
	return out
}

func contractTools(gw *progressive.Gateway) []contract.ToolInfo {
	if gw == nil {
		return nil
	}
	metas := gw.MetaTools()
	out := make([]contract.ToolInfo, 0, len(metas))
	for _, m := range metas {
		desc := m.Summary
		if len(m.Examples) > 0 {
			desc += "；示例：" + m.Examples[0]
		}
		out = append(out, contract.ToolInfo{Name: m.Name, Description: desc, Schema: m.Schema})
	}
	return out
}

// ---------- contract.LLMService → eino BaseChatModel ----------

type chatAdapter struct {
	llm         contract.LLMService
	system      string
	model       string
	maxTokens   int
	temperature *float64
	tools       []contract.ToolInfo
	onDelta     func(string)

	mu  sync.Mutex
	seq int
}

func (a *chatAdapter) Generate(ctx context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	req := contract.ChatRequest{
		Model:       a.model,
		System:      a.system,
		Messages:    toContractMessages(msgs),
		Tools:       a.tools,
		Temperature: a.temperature,
		MaxTokens:   a.maxTokens,
	}
	var (
		reply contract.Reply
		err   error
	)
	if a.onDelta != nil {
		reply, err = a.llm.Stream(ctx, req, a.onDelta)
	} else {
		reply, err = a.llm.Chat(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	msg := fromContractMessage(reply.Message)
	a.fillToolCallIDs(msg)
	meta := &schema.ResponseMeta{}
	hasMeta := false
	if usage := normalizeUsage(reply.Usage); usage != nil {
		meta.Usage = usage
		hasMeta = true
	}
	// finish_reason 与推理内容是"正文为空"时唯一的排障线索，必须一路带到 reactState。
	if fr := strings.TrimSpace(reply.Message.FinishReason); fr != "" {
		meta.FinishReason = fr
		hasMeta = true
	}
	if hasMeta {
		msg.ResponseMeta = meta
	}
	return msg, nil
}

// Stream 保证 eino 图在 Stream 模式下也能拿到完整消息（本 harness 用 Invoke，不逐块消费）。
func (a *chatAdapter) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := a.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// fillToolCallIDs 给缺失 id 的工具调用补一个稳定 id，否则 ToolsNode 无法把结果配回调用。
func (a *chatAdapter) fillToolCallIDs(msg *schema.Message) {
	if len(msg.ToolCalls) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range msg.ToolCalls {
		if msg.ToolCalls[i].ID != "" {
			continue
		}
		a.seq++
		msg.ToolCalls[i].ID = fmt.Sprintf("call_%d", a.seq)
	}
}

func toContractMessages(msgs []*schema.Message) []contract.Message {
	out := make([]contract.Message, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		cm := contract.Message{
			Role:       toContractRole(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.ToolName,
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, contract.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		out = append(out, cm)
	}
	return out
}

// fromContractMessage 把 contract 消息还原成 eino 消息。
//
// 关键点：正文为空但 ReasoningContent 非空时，把推理内容当正文并**明确标注**，
// 而不是让内容无声蒸发（推理模型把正文放 reasoning_content 是常态）。
//
// 但**带工具调用的消息不能回退**：那种消息正文本来就该为空（模型在说"我要调工具"），
// 把思维链填进 content 会被当成 assistant 的历史正文回灌给模型，
// 既污染上下文、又会在每一步重发几万 token。回退只针对"最终答复"。
func fromContractMessage(m contract.Message) *schema.Message {
	content := m.Content
	reasoning := m.ReasoningContent
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) != "" && len(m.ToolCalls) == 0 {
		content = reasoningAsContent(reasoning)
	}
	msg := &schema.Message{
		Role:             toSchemaRole(m.Role),
		Content:          content,
		ReasoningContent: reasoning,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
	}
	return msg
}

func toContractRole(r schema.RoleType) contract.Role {
	switch r {
	case schema.System:
		return contract.RoleSystem
	case schema.User:
		return contract.RoleUser
	case schema.Assistant:
		return contract.RoleAssistant
	case schema.Tool:
		return contract.RoleTool
	}
	return contract.RoleUser
}

func toSchemaRole(r contract.Role) schema.RoleType {
	switch r {
	case contract.RoleSystem:
		return schema.System
	case contract.RoleAssistant:
		return schema.Assistant
	case contract.RoleTool:
		return schema.Tool
	}
	return schema.User
}

// ---------- ReAct 图 ----------

// reactState 是在图内各节点间共享的运行状态（用 eino 的 LocalState 传递）。
type reactState struct {
	messages  []*schema.Message
	steps     int
	toolCalls int
	promptTok int
	complTok  int
	totalTok  int
	steers    int
	// childMsgs 是并入上下文的后台子智能体结果条数（统计用，不占步数预算）。
	childMsgs   int
	maxSteps    int
	maxTokens   int
	exhausted   bool
	exhaustKind string
	exhaustNote string
	// reasoning 是最后一次带推理内容的模型输出（正文为空时用它做诊断/回退）。
	reasoning string
	// finishReason 是最后一次非空的上游结束原因（stop / length / tool_calls / content_filter）。
	finishReason string
}

// ReactStep 是一次模型调用前的上下文快照（用于上下文用量估算，见 context.go）。
type ReactStep struct {
	// Step 是即将发生的第几次模型调用（从 1 起）。
	Step int
	// Messages 是本次调用实际发送的上下文（不含 system prompt）。
	Messages []contract.Message
	// ToolCalls 是到目前为止已经完成的工具调用次数。
	ToolCalls int
}

type reactConfig struct {
	LLM             contract.LLMService
	Gateway         *progressive.Gateway
	Model           string
	System          string
	MaxSteps        int
	MaxTokens       int
	MaxOutputTokens int
	Temperature     *float64
	OnDelta         func(string)
	// OnStep 在每次模型调用前回调（上下文用量估算用）；为空则不算，行为与之前一致。
	OnStep func(ReactStep)
	// OnReply 在每次模型调用拿到 usage 后回调一次（按会话记账用）：
	// 参数是提供方上报的用量（含 promptTokens / cachedTokens / completionTokens / totalTokens）。
	// 为空则不计账，行为与未加记账前完全一致。
	OnReply func(contract.Usage)
	// DrainSteers 在每次模型调用前调用：返回用户中途插话（返回后队列清空）。
	// 为空表示不支持插话，行为与未实现插话前完全一致。
	DrainSteers func() []string
	// DrainChildren 在每次模型调用前调用：取走已完成的后台子智能体结果（返回后清空）。
	// 双工模式下它是「后台 worker → 前台 RouterAgent」的回传通道。
	DrainChildren func() []string
	// PendingChildren 返回仍在后台跑的子智能体数量：
	// 模型想结束时若它 > 0，图会先去 wait 节点等一个结果，而不是直接结束。
	PendingChildren func() int
	// WaitChild 阻塞等待一个后台子智能体结果（带指令包装的文本）；
	// ctx 取消时返回 false（父会话被停止/超时）。
	WaitChild func(ctx context.Context) (string, bool)
	// OnTool 在每次元工具执行前后各回调一次（工具调用轨迹用）；
	// 为空则表示不采集轨迹，行为与未加轨迹前完全一致。
	OnTool func(ToolTrace)
	// Now 便于测试注入时钟；为空时用 time.Now。
	Now func() time.Time
}

// ReactResult 是一次 ReAct 运行的产物。
type ReactResult struct {
	Content          string
	Steps            int
	ToolCalls        int
	Tokens           int
	PromptTokens     int
	CompletionTokens int
	Steers           int
	Exhausted        bool
	// ExhaustKind 区分停机原因："budget"（token 预算）/ "steps"（步数上限）/ 空串（未停机）。
	ExhaustKind string
	// Finalized 表示停机后成功做了一次不带工具的收口调用，Content 即收口结论。
	Finalized  bool
	Note       string
	Transcript []*schema.Message
	// FinishReason 是最后一次非空的上游结束原因（stop / length / tool_calls / content_filter）。
	// 正文为空时它是判断"被截断 / 被过滤 / 正常结束但没内容"的关键线索。
	FinishReason string
	// Reasoning 是最后一次模型输出的推理内容（DeepSeek V4 等推理模型的思维链）。
	// 正文为空而它非空时，上层会把推理内容当正文回退并明确标注。
	Reasoning string
	// ContentFromReasoning 表示 Content 是由推理内容回退而来（正文其实是空的）。
	ContentFromReasoning bool
}

// normalizeUsage 把模型返回的 usage 补全：只有分项时补 total，三者皆空时返回 nil。
// 提供方上报的缓存命中数（cachedTokens）必须带进 PromptTokenDetails，
// 否则经过 eino 消息这一层时会被无声丢掉，上下文面板就拿不到缓存命中率。
func normalizeUsage(u contract.Usage) *schema.TokenUsage {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 && u.CachedTokens == 0 {
		return nil
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return &schema.TokenUsage{
		PromptTokens:       u.PromptTokens,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: u.CachedTokens},
		CompletionTokens:   u.CompletionTokens,
		TotalTokens:        total,
	}
}

// contractUsageOf 把 eino 的 TokenUsage 转回契约用量（含提供方上报的缓存命中数）。
func contractUsageOf(u *schema.TokenUsage) contract.Usage {
	if u == nil {
		return contract.Usage{}
	}
	return contract.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.PromptTokenDetails.CachedTokens,
	}
}

// usageTotal 兜底计算总 token：上游常常只给分项或只给 total。
func usageTotal(u *schema.TokenUsage) int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

// steerMessage 把用户插话包成一条 user 消息：带前缀便于回放识别，并明确不要丢原任务。
func steerMessage(text string) *schema.Message {
	return schema.UserMessage(steerPrefix + strings.TrimSpace(text) + nl +
		nl + "（这是用户在任务执行中补充的信息/约束，请把它并入当前任务继续；不要丢弃原任务目标，也不要因此重启分析。）")
}

// containsMessage 判断一条消息（按指针身份）是否已在轨迹里。
// wait 节点送来的新消息是新分配的指针，因此不会与已有轨迹误判为同一条。
func containsMessage(msgs []*schema.Message, target *schema.Message) bool {
	for _, m := range msgs {
		if m == target {
			return true
		}
	}
	return false
}

type reactRun struct {
	runnable compose.Runnable[[]*schema.Message, *schema.Message]
	state    *reactState
	cfg      reactConfig
}

// buildReact 用 eino compose.Graph 搭一个 ReAct 循环：
// model →（有工具调用）→ tools → model → … → END。
// 步数上限取 Policy.MaxToolCalls，token 上限取 Policy.MaxTokensPerRun，超限后优雅收尾。
func buildReact(ctx context.Context, cfg reactConfig) (*reactRun, error) {
	if cfg.LLM == nil {
		return nil, fmt.Errorf("未配置模型服务：请先在“模型设置”里保存一个可用的模型")
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 24
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = defaultMaxOutputTokens
	}
	state := &reactState{maxSteps: cfg.MaxSteps, maxTokens: cfg.MaxTokens}
	adapter := &chatAdapter{
		llm:         cfg.LLM,
		system:      cfg.System,
		model:       cfg.Model,
		maxTokens:   cfg.MaxOutputTokens,
		temperature: cfg.Temperature,
		tools:       contractTools(cfg.Gateway),
		onDelta:     cfg.OnDelta,
	}
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools:               einoTools(cfg.Gateway, cfg.OnTool, cfg.Now),
		ExecuteSequentially: true,
		// 模型直接写业务工具名（market.quote 等）时不要整轮失败，把纠正路径回给它。
		UnknownToolsHandler: func(_ context.Context, name, input string) (string, error) {
			args := strings.TrimSpace(input)
			if args == "" {
				args = "{}"
			}
			return fmt.Sprintf("工具 %q 不能直接调用。你只能调用 tool.search / tool.describe / tool.call；"+
				"要执行它请改成 tool.call 并传 name=%q、args=%s。"+
				"若不确认该工具是否存在，先用 tool.search 检索。", name, name, args), nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("构建工具节点失败：%w", err)
	}

	g := compose.NewGraph[[]*schema.Message, *schema.Message](
		compose.WithGenLocalState(func(context.Context) *reactState { return state }),
	)

	if err := g.AddChatModelNode("model", adapter,
		compose.WithStatePreHandler(func(_ context.Context, in []*schema.Message, st *reactState) ([]*schema.Message, error) {
			if len(st.messages) == 0 {
				st.messages = in
			} else if len(in) > 0 {
				// wait 节点（双工等待子智能体）会把新消息当成模型输入送进来；
				// 模型节点本身不消费输入（只读 st.messages），所以这里必须把「没见过的消息」
				// 并进轨迹，否则子结果会被静默丢掉。
				for _, m := range in {
					if m == nil || containsMessage(st.messages, m) {
						continue
					}
					st.messages = append(st.messages, m)
				}
			}
			// 用户中途插话：在下一次模型调用前并入上下文（不新增步数预算）。
			if cfg.DrainSteers != nil {
				for _, text := range cfg.DrainSteers() {
					st.messages = append(st.messages, steerMessage(text))
					st.steers++
				}
			}
			// 后台子智能体结果：同样在步骤边界并入（双工模式的核心回传路径）。
			if cfg.DrainChildren != nil {
				for _, text := range cfg.DrainChildren() {
					if strings.TrimSpace(text) == "" {
						continue
					}
					st.messages = append(st.messages, schema.UserMessage(text))
					st.childMsgs++
				}
			}
			// 上下文用量：模型调用前回调（注意此时 st.steps 尚未 +1，所以是下一次调用）。
			if cfg.OnStep != nil {
				cfg.OnStep(ReactStep{
					Step:      st.steps + 1,
					Messages:  toContractMessages(st.messages),
					ToolCalls: st.toolCalls,
				})
			}
			return st.messages, nil
		}),
		compose.WithStatePostHandler(func(_ context.Context, out *schema.Message, st *reactState) (*schema.Message, error) {
			st.steps++
			// 推理内容与 finish_reason 必须在追加前抓下来：正文为空时它们是唯一线索。
			if strings.TrimSpace(out.ReasoningContent) != "" {
				st.reasoning = out.ReasoningContent
			}
			if fr := reasoningFinishReason(out); fr != "" {
				st.finishReason = fr
			}
			st.messages = append(st.messages, out)
			if out.ResponseMeta != nil && out.ResponseMeta.Usage != nil {
				u := out.ResponseMeta.Usage
				st.promptTok += u.PromptTokens
				st.complTok += u.CompletionTokens
				st.totalTok += usageTotal(u)
				// 按次回调一次（Runtime 据此按会话记账、追加逐轮上下文采样）。
				if cfg.OnReply != nil {
					cfg.OnReply(contractUsageOf(u))
				}
			}
			if st.maxSteps > 0 && st.steps >= st.maxSteps {
				st.exhausted = true
				st.exhaustKind = "steps"
				st.exhaustNote = fmt.Sprintf("已达到最大步数上限 %d，工具调用未再继续", st.maxSteps)
			}
			if st.maxTokens > 0 && st.totalTok > st.maxTokens {
				st.exhausted = true
				st.exhaustKind = "budget"
				st.exhaustNote = fmt.Sprintf("已超出 token 预算上限 %d（实际累计 %d）", st.maxTokens, st.totalTok)
			}
			return out, nil
		}),
	); err != nil {
		return nil, fmt.Errorf("添加模型节点失败：%w", err)
	}

	if err := g.AddToolsNode("tools", toolsNode,
		compose.WithStatePreHandler(func(_ context.Context, in *schema.Message, _ *reactState) (*schema.Message, error) {
			return in, nil
		}),
		compose.WithStatePostHandler(func(_ context.Context, out []*schema.Message, st *reactState) ([]*schema.Message, error) {
			st.toolCalls += len(out)
			st.messages = append(st.messages, out...)
			return out, nil
		}),
	); err != nil {
		return nil, fmt.Errorf("添加工具节点失败：%w", err)
	}

	if err := g.AddEdge(compose.START, "model"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("tools", "model"); err != nil {
		return nil, err
	}
	// wait 节点：双工模式下，模型想结束但后台子智能体还在跑时先等一个结果回来。
	// 没有后台子智能体时这个节点永远不会被走到（分支不会选它），行为与单线程完全一致。
	if cfg.WaitChild != nil {
		// 注意类型方向：模型节点的输入是 []*schema.Message、输出是 *schema.Message，
		// 所以 wait 必须吃 *schema.Message、吐 []*schema.Message 才能接回 model。
		waitNode := compose.InvokableLambda[*schema.Message, []*schema.Message](
			func(ctx context.Context, _ *schema.Message) ([]*schema.Message, error) {
				text, ok := cfg.WaitChild(ctx)
				if !ok {
					return []*schema.Message{schema.UserMessage("（后台子智能体已全部结束或已停止，请基于已有信息直接收尾。）")}, nil
				}
				return []*schema.Message{schema.UserMessage(text)}, nil
			})
		if err := g.AddLambdaNode("wait", waitNode); err != nil {
			return nil, fmt.Errorf("添加等待节点失败：%w", err)
		}
		if err := g.AddEdge("wait", "model"); err != nil {
			return nil, err
		}
	}
	endpoints := map[string]bool{"tools": true, compose.END: true}
	if cfg.WaitChild != nil {
		endpoints["wait"] = true
	}
	branch := compose.NewGraphBranch(func(_ context.Context, msg *schema.Message) (string, error) {
		if len(msg.ToolCalls) == 0 || state.exhausted {
			// 双工：模型没有工具要调，但后台还有子智能体在跑——先去等一个结果，再继续总结。
			if !state.exhausted && cfg.WaitChild != nil && cfg.PendingChildren != nil && cfg.PendingChildren() > 0 {
				return "wait", nil
			}
			return compose.END, nil
		}
		return "tools", nil
	}, endpoints)
	if err := g.AddBranch("model", branch); err != nil {
		return nil, fmt.Errorf("添加分支失败：%w", err)
	}

	runnable, err := g.Compile(ctx,
		compose.WithGraphName("agent.react"),
		compose.WithMaxRunSteps(cfg.MaxSteps*2+4),
	)
	if err != nil {
		return nil, fmt.Errorf("编译 ReAct 图失败：%w", err)
	}
	return &reactRun{runnable: runnable, state: state, cfg: cfg}, nil
}

// Run 执行一次 ReAct 循环；若因预算/步数停机且最后一条回复没有实质结论，
// 会再补一次不带工具的收口调用，尽量产出完整可读的报告。
func (r *reactRun) Run(ctx context.Context, userMessage string) (ReactResult, error) {
	out, err := r.runnable.Invoke(ctx, []*schema.Message{schema.UserMessage(userMessage)})
	res := ReactResult{
		Steps:            r.state.steps,
		ToolCalls:        r.state.toolCalls,
		Tokens:           r.state.totalTok,
		PromptTokens:     r.state.promptTok,
		CompletionTokens: r.state.complTok,
		Steers:           r.state.steers,
		Exhausted:        r.state.exhausted,
		ExhaustKind:      r.state.exhaustKind,
		Note:             r.state.exhaustNote,
		Transcript:       append([]*schema.Message{}, r.state.messages...),
		FinishReason:     r.state.finishReason,
		Reasoning:        r.state.reasoning,
	}
	if err != nil {
		// 已跑出的内容仍然返回，让上层能给出部分结论。
		if out != nil && strings.TrimSpace(out.Content) != "" {
			res.Content = out.Content
		}
		r.applyReasoningFallback(&res)
		return res, err
	}
	if out != nil {
		res.Content = out.Content
	}
	if res.Exhausted && ctx.Err() == nil && needsFinalize(r.state.messages) {
		r.applyFinalize(ctx, userMessage, &res)
	}
	r.applyReasoningFallback(&res)
	return res, nil
}

// applyReasoningFallback 在"正文为空但推理内容非空"时把推理内容当正文并明确标注。
// 绝不静默丢弃：否则上层只能看到"模型没有返回内容"，把真实原因彻底吞掉。
//
// 两条路径都会走到这里：
//  1. fromContractMessage 已经用推理内容填过正文（带标注前缀）→ 这里只补标记；
//  2. 正文确实全空但轨迹里留下过推理内容 → 这里兜底填充。
func (r *reactRun) applyReasoningFallback(res *ReactResult) {
	if res == nil {
		return
	}
	if isReasoningFallback(res.Content) {
		res.ContentFromReasoning = true
		return
	}
	if strings.TrimSpace(res.Content) != "" {
		return
	}
	text := reasoningAsContent(res.Reasoning)
	if text == "" {
		return
	}
	res.Content = text
	res.ContentFromReasoning = true
}

// isReasoningFallback 判断一段正文是不是"推理内容回退"生成的（靠固定标注前缀识别）。
func isReasoningFallback(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), reasoningFallbackNote)
}

// reasoningFinishReason 提取一条消息的 finish_reason（不依赖 eino 版本细节）。
func reasoningFinishReason(m *schema.Message) string {
	if m == nil || m.ResponseMeta == nil {
		return ""
	}
	return strings.TrimSpace(m.ResponseMeta.FinishReason)
}

// applyFinalize 尝试做一次收口调用；失败时保持原结果不动（不吞掉已获得的部分结论）。
func (r *reactRun) applyFinalize(ctx context.Context, userMessage string, res *ReactResult) {
	text, usage, err := r.finalize(ctx, userMessage)
	if err != nil {
		return
	}
	res.Content = text
	res.Finalized = true
	res.PromptTokens += usage.PromptTokens
	res.CompletionTokens += usage.CompletionTokens
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	res.Tokens += total
	// 轨迹里补一条 assistant 消息：会话回放与知识库证据都能看到这份收口结论。
	r.state.promptTok += usage.PromptTokens
	r.state.complTok += usage.CompletionTokens
	r.state.totalTok += total
	r.state.messages = append(r.state.messages, schema.AssistantMessage(text, nil))
	res.Transcript = append([]*schema.Message{}, r.state.messages...)
}

// finalize 用同一个模型发一次"不带工具"的收口调用：只喂最近若干条轨迹（单条截断），
// 要求模型按 结论/依据/不确定性/风险提示 四段输出。返回文本、本次 usage 与错误。
func (r *reactRun) finalize(ctx context.Context, userMessage string) (string, contract.Usage, error) {
	if r.cfg.LLM == nil {
		return "", contract.Usage{}, fmt.Errorf("未配置模型服务")
	}
	digest := finalizeDigest(r.state.messages)
	if digest == "" {
		return "", contract.Usage{}, fmt.Errorf("没有可用于收口的轨迹")
	}
	prompt := finalizeInstruction + nl + nl +
		"原始任务：" + truncateRunes(strings.TrimSpace(userMessage), finalizeUserRunes) + nl + nl +
		"已获得的轨迹：" + nl + digest
	req := contract.ChatRequest{
		Model:       r.cfg.Model,
		System:      r.cfg.System,
		Messages:    []contract.Message{{Role: contract.RoleUser, Content: prompt}},
		Temperature: r.cfg.Temperature,
		MaxTokens:   r.cfg.MaxOutputTokens,
	}
	var (
		reply contract.Reply
		err   error
	)
	if r.cfg.OnDelta != nil {
		reply, err = r.cfg.LLM.Stream(ctx, req, r.cfg.OnDelta)
	} else {
		reply, err = r.cfg.LLM.Chat(ctx, req)
	}
	if err != nil {
		return "", contract.Usage{}, err
	}
	// 收口一次调用也是一次真实模型请求：同样计入按会话记账（不在图内，所以显式回调）。
	if r.cfg.OnReply != nil {
		r.cfg.OnReply(reply.Usage)
	}
	text := strings.TrimSpace(reply.Message.Content)
	if text == "" {
		return "", contract.Usage{}, fmt.Errorf("收口调用没有返回内容")
	}
	return text, reply.Usage, nil
}

// needsFinalize 判断停机时最后一条 assistant 消息是否缺少实质结论：
// 完全没有正文，或只带工具调用且正文过短，都需要补一次收口。
func needsFinalize(msgs []*schema.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m == nil || m.Role != schema.Assistant {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			return true
		}
		return len([]rune(content)) < minFinalizeRunes && len(m.ToolCalls) > 0
	}
	return true
}

// finalizeDigest 把最近的分析轨迹压成纯文本：assistant 中间结论、工具调用参数、
// 工具结果（单条截断）与用户消息，总量超限时从最早一条开始丢。
func finalizeDigest(msgs []*schema.Message) string {
	start := len(msgs) - finalizeMaxMessages
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, finalizeMaxMessages)
	for _, m := range msgs[start:] {
		if m == nil {
			continue
		}
		switch m.Role {
		case schema.Assistant:
			if content := strings.TrimSpace(m.Content); content != "" {
				lines = append(lines, "模型中间结论："+truncateRunes(content, finalizeAssistRunes))
			}
			for _, tc := range m.ToolCalls {
				lines = append(lines, "请求工具 "+tc.Function.Name+" 参数 "+truncateRunes(tc.Function.Arguments, 240))
			}
		case schema.Tool:
			if text := strings.TrimSpace(m.Content); text != "" {
				lines = append(lines, "工具 "+m.ToolName+" 结果："+truncateRunes(text, finalizeToolRunes))
			}
		case schema.User:
			if text := strings.TrimSpace(m.Content); text != "" {
				lines = append(lines, "用户："+truncateRunes(text, finalizeUserRunes))
			}
		}
	}
	total := 0
	for _, l := range lines {
		total += len([]rune(l))
	}
	for len(lines) > 1 && total > finalizeMaxRunes {
		total -= len([]rune(lines[0]))
		lines = lines[1:]
	}
	return strings.Join(lines, nl)
}
