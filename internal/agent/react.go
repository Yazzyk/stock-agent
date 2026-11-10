package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
func (t *invokableTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	args := json.RawMessage(argumentsInJSON)
	if strings.TrimSpace(argumentsInJSON) == "" {
		args = json.RawMessage("{}")
	}
	out, err := t.def.Run(ctx, args)
	if err != nil {
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
	if r := []rune(text); len(r) > maxToolOutputRunes {
		text = string(r[:maxToolOutputRunes]) + "…（结果过长已截断，请缩小查询范围）"
	}
	return text, nil
}

func einoTools(gw *progressive.Gateway) []tool.BaseTool {
	if gw == nil {
		return nil
	}
	metas := gw.MetaTools()
	out := make([]tool.BaseTool, 0, len(metas))
	for _, m := range metas {
		out = append(out, &invokableTool{def: m})
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
	if usage := normalizeUsage(reply.Usage); usage != nil {
		msg.ResponseMeta = &schema.ResponseMeta{Usage: usage}
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

func fromContractMessage(m contract.Message) *schema.Message {
	msg := &schema.Message{
		Role:       toSchemaRole(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
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
	messages    []*schema.Message
	steps       int
	toolCalls   int
	promptTok   int
	complTok    int
	totalTok    int
	maxSteps    int
	maxTokens   int
	exhausted   bool
	exhaustNote string
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
}

// ReactResult 是一次 ReAct 运行的产物。
type ReactResult struct {
	Content          string
	Steps            int
	ToolCalls        int
	Tokens           int
	PromptTokens     int
	CompletionTokens int
	Exhausted        bool
	Note             string
	Transcript       []*schema.Message
}

// normalizeUsage 把模型返回的 usage 补全：只有分项时补 total，三者皆空时返回 nil。
func normalizeUsage(u contract.Usage) *schema.TokenUsage {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return &schema.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      total,
	}
}

// usageTotal 兜底计算总 token：上游常常只给分项或只给 total。
func usageTotal(u *schema.TokenUsage) int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

type reactRun struct {
	runnable compose.Runnable[[]*schema.Message, *schema.Message]
	state    *reactState
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
		cfg.MaxOutputTokens = 2048
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
		Tools:               einoTools(cfg.Gateway),
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
			}
			return st.messages, nil
		}),
		compose.WithStatePostHandler(func(_ context.Context, out *schema.Message, st *reactState) (*schema.Message, error) {
			st.steps++
			st.messages = append(st.messages, out)
			if out.ResponseMeta != nil && out.ResponseMeta.Usage != nil {
				u := out.ResponseMeta.Usage
				st.promptTok += u.PromptTokens
				st.complTok += u.CompletionTokens
				st.totalTok += usageTotal(u)
			}
			if st.maxSteps > 0 && st.steps >= st.maxSteps {
				st.exhausted = true
				st.exhaustNote = fmt.Sprintf("已达到最大步数 %d", st.maxSteps)
			}
			if st.maxTokens > 0 && st.totalTok > st.maxTokens {
				st.exhausted = true
				st.exhaustNote = fmt.Sprintf("已超出 token 预算 %d（实际 %d）", st.maxTokens, st.totalTok)
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
	branch := compose.NewGraphBranch(func(_ context.Context, msg *schema.Message) (string, error) {
		if len(msg.ToolCalls) == 0 || state.exhausted {
			return compose.END, nil
		}
		return "tools", nil
	}, map[string]bool{"tools": true, compose.END: true})
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
	return &reactRun{runnable: runnable, state: state}, nil
}

// Run 执行一次 ReAct 循环。
func (r *reactRun) Run(ctx context.Context, userMessage string) (ReactResult, error) {
	out, err := r.runnable.Invoke(ctx, []*schema.Message{schema.UserMessage(userMessage)})
	res := ReactResult{
		Steps:            r.state.steps,
		ToolCalls:        r.state.toolCalls,
		Tokens:           r.state.totalTok,
		PromptTokens:     r.state.promptTok,
		CompletionTokens: r.state.complTok,
		Exhausted:        r.state.exhausted,
		Note:             r.state.exhaustNote,
		Transcript:       append([]*schema.Message{}, r.state.messages...),
	}
	if err != nil {
		// 已跑出的内容仍然返回，让上层能给出部分结论。
		if out != nil && strings.TrimSpace(out.Content) != "" {
			res.Content = out.Content
		}
		return res, err
	}
	if out != nil {
		res.Content = out.Content
	}
	return res, nil
}
