package llm

// WorkBuddy 对话请求适配。
//
// 实测（2026-11，www.workbuddy.cn /v2/chat/completions）WorkBuddy 不是标准 OpenAI 兼容端点：
//   - 必须 stream=true（非流式直接 400 code 11101）；
//   - messages 里不能出现 role=developer，且首条必须是 system（否则 400 code 11128）；
//   - tool_choice 只能是字符串，传对象会 400；
//   - 模型 ID 必须来自 /v2/enterprises/personal/models；
//   - 鉴权走本机登录态的 Bearer token + 归属/会话身份头。
//
// 因此这里用自定义 chatModel 直接解析 SSE：Generate 内部强制流式再聚合，
// Stream 逐帧下发 *schema.Message；工具调用的 id/name/arguments 按 index 累加，语义与 eino 一致。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// workBuddyChatModel 是 WorkBuddy 的 eino chatModel 实现。
type workBuddyChatModel struct {
	wb             *workBuddy
	model          string
	configuredBase string
	maxTokens      int
	timeout        time.Duration
}

// chatModel 构造一条 WorkBuddy 对话模型；configuredBase 仅为自定义代理保留，网关地址默认按凭证 domain 推导。
func (w *workBuddy) chatModel(model, configuredBase string, maxTokens int, timeout time.Duration) chatModel {
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return &workBuddyChatModel{wb: w, model: model, configuredBase: configuredBase, maxTokens: maxTokens, timeout: timeout}
}

// baseFor 选择本次请求的网关根地址：测试注入 > 自定义代理 > 按 domain 取国内/国际网关。
func (m *workBuddyChatModel) baseFor(cred *workBuddyCredential) string {
	if m.wb.baseOverride != nil {
		return strings.TrimSuffix(m.wb.baseOverride(cred.Domain), "/")
	}
	if base := strings.TrimSpace(m.configuredBase); base != "" && !isWorkBuddyBaseURL(base) {
		return strings.TrimSuffix(base, "/")
	}
	return m.wb.baseOf(cred.Domain)
}

// Generate 以流式请求上游并聚合出完整回复，供非流式的 Chat()/连通性测试使用。
func (m *workBuddyChatModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	resp, err := m.start(ctx, in, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	agg := &workBuddyAggregate{}
	if scanErr := scanWorkBuddySSE(io.LimitReader(resp.Body, 32<<20), agg.apply); scanErr != nil {
		return nil, fmt.Errorf("读取 WorkBuddy 流式响应失败: %w", scanErr)
	}
	out := agg.message()
	if out == nil || (out.Content == "" && out.ReasoningContent == "" && len(out.ToolCalls) == 0) {
		return nil, errors.New("WorkBuddy 未返回任何内容")
	}
	return out, nil
}

// Stream 逐帧返回增量消息；连接与 HTTP 状态错误在返回前同步报出，避免上层拿到空流。
func (m *workBuddyChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	resp, err := m.start(ctx, in, opts)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](8)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer sw.Close()
		agg := &workBuddyAggregate{}
		sent := false
		scanErr := scanWorkBuddySSE(io.LimitReader(resp.Body, 32<<20), func(frame workBuddySSEFrame) {
			chunk := agg.chunk(frame)
			if chunk == nil {
				return
			}
			sent = true
			sw.Send(chunk, nil)
		})
		if scanErr != nil {
			sw.Send(nil, fmt.Errorf("读取 WorkBuddy 流式响应失败: %w", scanErr))
			return
		}
		if !sent {
			sw.Send(nil, errors.New("WorkBuddy 未返回任何内容"))
		}
	}()
	return sr, nil
}

// start 完成鉴权、模型校验、请求体规范化并发出 HTTP 请求，返回 2xx 的 SSE 响应。
func (m *workBuddyChatModel) start(ctx context.Context, in []*schema.Message, opts []model.Option) (*http.Response, error) {
	cred, err := m.wb.credential(ctx)
	if err != nil {
		return nil, err
	}
	if err = m.wb.ensureModelAllowed(ctx, m.model); err != nil {
		return nil, err
	}
	common := model.GetCommonOptions(nil, opts...)
	body, err := buildWorkBuddyBody(in, common, m.model, m.maxTokens)
	if err != nil {
		return nil, err
	}
	normalizeWorkBuddyBody(body)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化 WorkBuddy 请求失败: %w", err)
	}
	base := m.baseFor(cred)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		workBuddyAPIEndpoint(base, workBuddyChatPath), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	applyWorkBuddyHeaders(req, workBuddyRequestHeaders(cred, base))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := workBuddyHTTPClient(m.timeout).Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 WorkBuddy 网关失败：%w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return nil, workBuddyStatusError(resp.StatusCode, raw)
}

// workBuddyStatusError 把非 2xx 响应转成可操作的中文错误（只保留上游 code/msg，不含任何凭证）。
func workBuddyStatusError(status int, raw []byte) error {
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	detail := ""
	if json.Unmarshal(raw, &envelope) == nil {
		detail = strings.TrimSpace(envelope.Msg)
		if envelope.Code != 0 {
			detail = fmt.Sprintf("code %d %s", envelope.Code, detail)
		}
	}
	if detail == "" {
		detail = strings.TrimSpace(string(raw))
	}
	if len(detail) > 240 {
		detail = detail[:240] + "…"
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("WorkBuddy 登录态已失效（HTTP %d）：请重新登录或从 dsh/go-stock 导入（%s）", status, detail)
	}
	if strings.TrimSpace(detail) == "" {
		return fmt.Errorf("WorkBuddy 网关拒绝请求：HTTP %d", status)
	}
	return fmt.Errorf("WorkBuddy 网关拒绝请求：HTTP %d（%s）", status, detail)
}

// buildWorkBuddyBody 把 eino 消息与选项转成 OpenAI 形态的请求体。
func buildWorkBuddyBody(in []*schema.Message, opts *model.Options, modelName string, fallbackMaxTokens int) (map[string]any, error) {
	if opts == nil {
		opts = &model.Options{}
	}
	messages := make([]any, 0, len(in)+1)
	for _, msg := range in {
		if msg == nil {
			continue
		}
		item := map[string]any{"role": string(msg.Role)}
		switch msg.Role {
		case schema.Assistant:
			item["content"] = msg.Content
			if len(msg.ToolCalls) > 0 {
				calls := make([]any, 0, len(msg.ToolCalls))
				for _, call := range msg.ToolCalls {
					calls = append(calls, map[string]any{
						"id":   call.ID,
						"type": "function",
						"function": map[string]any{
							"name":      call.Function.Name,
							"arguments": call.Function.Arguments,
						},
					})
				}
				item["tool_calls"] = calls
			}
		case schema.Tool:
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return nil, errors.New("llm: tool 消息缺少 toolCallId（WorkBuddy 会拒绝整段历史）")
			}
			item["content"] = msg.Content
			item["tool_call_id"] = msg.ToolCallID
			if msg.ToolName != "" {
				item["name"] = msg.ToolName
			}
		default:
			item["content"] = msg.Content
		}
		messages = append(messages, item)
	}
	if len(messages) == 0 {
		return nil, errors.New("llm: 消息为空")
	}

	body := map[string]any{
		"model":    modelName,
		"messages": messages,
		"stream":   true,
	}
	if opts.Temperature != nil {
		body["temperature"] = *opts.Temperature
	}
	if opts.TopP != nil {
		body["top_p"] = *opts.TopP
	}
	if len(opts.Stop) > 0 {
		body["stop"] = opts.Stop
	}
	maxTokens := fallbackMaxTokens
	if opts.MaxTokens != nil && *opts.MaxTokens > 0 {
		maxTokens = *opts.MaxTokens
	}
	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}
	if len(opts.Tools) > 0 {
		tools := make([]any, 0, len(opts.Tools))
		for _, tool := range opts.Tools {
			if tool == nil {
				continue
			}
			params := any(map[string]any{"type": "object", "properties": map[string]any{}})
			if tool.ParamsOneOf != nil {
				schemaJSON, err := tool.ParamsOneOf.ToJSONSchema()
				if err != nil {
					return nil, fmt.Errorf("llm: 工具 %s 的参数 Schema 非法: %w", tool.Name, err)
				}
				if schemaJSON != nil {
					params = schemaJSON
				}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Desc,
					"parameters":  params,
				},
			})
		}
		body["tools"] = tools
	}
	if opts.ToolChoice != nil {
		if choice := workBuddyToolChoiceString(string(*opts.ToolChoice)); choice != "" {
			body["tool_choice"] = choice
		}
	}
	return body, nil
}

// workBuddyToolChoiceString 把 eino 的 tool_choice 枚举映射成网关接受的字符串。
func workBuddyToolChoiceString(choice string) string {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "forbidden", "none":
		return "none"
	case "forced", "required":
		return "required"
	case "allowed", "auto":
		return "auto"
	default:
		return ""
	}
}

// normalizeWorkBuddyBody 就地规范化请求体，使其满足 WorkBuddy 网关校验（幂等）。
func normalizeWorkBuddyBody(body map[string]any) {
	if body == nil {
		return
	}
	body["stream"] = true
	if effort, ok := body["reasoning_effort"].(string); ok && strings.EqualFold(strings.TrimSpace(effort), "off") {
		delete(body, "reasoning_effort")
	}
	normalizeWorkBuddyToolChoice(body)

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return
	}
	for _, item := range messages {
		msg, isMap := item.(map[string]any)
		if !isMap {
			continue
		}
		if role, _ := msg["role"].(string); strings.EqualFold(strings.TrimSpace(role), "developer") {
			msg["role"] = "system"
		}
	}
	head, _ := messages[0].(map[string]any)
	headRole := ""
	if head != nil {
		headRole, _ = head["role"].(string)
	}
	if !strings.EqualFold(strings.TrimSpace(headRole), "system") {
		messages = append([]any{map[string]any{
			"role":    "system",
			"content": workBuddyFallbackSystemPrompt,
		}}, messages...)
		body["messages"] = messages
	}
}

// normalizeWorkBuddyToolChoice 把 OpenAI 对象形态的 tool_choice 拍平成网关接受的字符串。
func normalizeWorkBuddyToolChoice(body map[string]any) {
	raw, exists := body["tool_choice"]
	if !exists {
		return
	}
	choice, isMap := raw.(map[string]any)
	if !isMap {
		if text, isString := raw.(string); isString && strings.EqualFold(strings.TrimSpace(text), "none") {
			delete(body, "tool_choice")
			delete(body, "tools")
			delete(body, "functions")
		}
		return
	}
	typeName, _ := choice["type"].(string)
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "none":
		delete(body, "tool_choice")
		delete(body, "tools")
		delete(body, "functions")
	case "auto", "required":
		body["tool_choice"] = strings.ToLower(strings.TrimSpace(typeName))
	case "function":
		name := ""
		if fn, ok := choice["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			name, _ = choice["name"].(string)
		}
		if name = strings.TrimSpace(name); name != "" {
			body["tool_choice"] = name
		} else {
			body["tool_choice"] = "auto"
		}
	default:
		delete(body, "tool_choice")
	}
}

// ---------- SSE 解析 ----------

// workBuddyUsage 是上游返回的 token 用量。
type workBuddyUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// PromptCacheHitTokens 是 DeepSeek 风格的缓存命中数（上游给了就取，没给为 0，不估算）。
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	// PromptTokensDetails 是 OpenAI 风格的提示词分项（只有一个 cached_tokens 用得上）。
	PromptTokensDetails *workBuddyPromptTokenDetails `json:"prompt_tokens_details"`
}

// workBuddyPromptTokenDetails 是 OpenAI 风格的提示词分项。
type workBuddyPromptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// cachedTokens 返回上游上报的提示词缓存命中数：
// 兼容 DeepSeek（prompt_cache_hit_tokens）与 OpenAI（prompt_tokens_details.cached_tokens）
// 两种字段名，两者都不存在时返回 0（不自行估算）。
func (u *workBuddyUsage) cachedTokens() int {
	if u == nil {
		return 0
	}
	cached := u.PromptCacheHitTokens
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > cached {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if cached < 0 {
		return 0
	}
	return cached
}

// workBuddyToolCallDelta 是 tool_calls 的增量片段。
type workBuddyToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// workBuddySSEDelta 是 delta / message 的公共形状。
type workBuddySSEDelta struct {
	Role             string                   `json:"role"`
	Content          string                   `json:"content"`
	ReasoningContent string                   `json:"reasoning_content"`
	Reasoning        string                   `json:"reasoning"`
	ToolCalls        []workBuddyToolCallDelta `json:"tool_calls"`
}

// workBuddySSEChoice 是单个 choice。
type workBuddySSEChoice struct {
	Index        int                `json:"index"`
	Delta        workBuddySSEDelta  `json:"delta"`
	Message      *workBuddySSEDelta `json:"message"`
	FinishReason string             `json:"finish_reason"`
}

// workBuddySSEFrame 是一条 SSE data 帧。
type workBuddySSEFrame struct {
	ID      string               `json:"id"`
	Model   string               `json:"model"`
	Choices []workBuddySSEChoice `json:"choices"`
	Usage   *workBuddyUsage      `json:"usage"`
}

// scanWorkBuddySSE 逐行解析 SSE，把每帧交给 onFrame；心跳/注释/坏帧直接跳过。
func scanWorkBuddySSE(body io.Reader, onFrame func(workBuddySSEFrame)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var frame workBuddySSEFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			continue
		}
		onFrame(frame)
	}
	return scanner.Err()
}

// workBuddyToolCallAcc 是 tool_calls 的累加器（arguments 按片段拼接）。
type workBuddyToolCallAcc struct {
	id        string
	name      string
	arguments strings.Builder
}

// workBuddyAggregate 把 SSE 增量聚合成一条完整消息。
type workBuddyAggregate struct {
	content   strings.Builder
	reasoning strings.Builder
	calls     map[int]*workBuddyToolCallAcc
	order     []int
	finish    string
	usage     *schema.TokenUsage
}

// apply 吸收一帧（Generate 与 Stream 共用）。
func (a *workBuddyAggregate) apply(frame workBuddySSEFrame) {
	if frame.Usage != nil {
		usage := &schema.TokenUsage{
			PromptTokens:       frame.Usage.PromptTokens,
			PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: frame.Usage.cachedTokens()},
			CompletionTokens:   frame.Usage.CompletionTokens,
			TotalTokens:        frame.Usage.TotalTokens,
		}
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
		a.usage = usage
	}
	for _, choice := range frame.Choices {
		a.applyDelta(choice.Delta)
		if choice.Message != nil {
			a.applyDelta(*choice.Message)
		}
		if choice.FinishReason != "" {
			a.finish = choice.FinishReason
		}
	}
}

// applyDelta 吸收一段增量（content / reasoning / tool_calls）。
func (a *workBuddyAggregate) applyDelta(delta workBuddySSEDelta) {
	a.content.WriteString(delta.Content)
	a.reasoning.WriteString(delta.ReasoningContent)
	if delta.ReasoningContent == "" {
		a.reasoning.WriteString(delta.Reasoning)
	}
	for _, call := range delta.ToolCalls {
		if a.calls == nil {
			a.calls = map[int]*workBuddyToolCallAcc{}
		}
		acc, exists := a.calls[call.Index]
		if !exists {
			acc = &workBuddyToolCallAcc{}
			a.calls[call.Index] = acc
			a.order = append(a.order, call.Index)
		}
		if call.ID != "" {
			acc.id = call.ID
		}
		if call.Function.Name != "" {
			acc.name = call.Function.Name
		}
		acc.arguments.WriteString(call.Function.Arguments)
	}
}

// message 把累加结果转成 eino 消息（Generate 用）。
func (a *workBuddyAggregate) message() *schema.Message {
	out := &schema.Message{Role: schema.Assistant, Content: a.content.String(), ReasoningContent: a.reasoning.String()}
	for _, index := range a.order {
		acc := a.calls[index]
		if acc == nil {
			continue
		}
		id := acc.id
		if id == "" {
			id = fmt.Sprintf("workbuddy_call_%d", index)
		}
		out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
			ID:   id,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      acc.name,
				Arguments: acc.arguments.String(),
			},
		})
	}
	finishReason := a.finish
	if finishReason == "" && len(out.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	if finishReason != "" || a.usage != nil {
		out.ResponseMeta = &schema.ResponseMeta{FinishReason: finishReason, Usage: a.usage}
	}
	return out
}

// chunk 把一帧转成流式增量消息；无内容且无元信息时返回 nil。
func (a *workBuddyAggregate) chunk(frame workBuddySSEFrame) *schema.Message {
	a.apply(frame)
	out := &schema.Message{Role: schema.Assistant}
	finishReason := ""
	for _, choice := range frame.Choices {
		out.Content += choice.Delta.Content
		out.ReasoningContent += choice.Delta.ReasoningContent
		if choice.Delta.ReasoningContent == "" {
			out.ReasoningContent += choice.Delta.Reasoning
		}
		for _, call := range choice.Delta.ToolCalls {
			index := call.Index
			out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
				Index: &index,
				ID:    call.ID,
				Type:  "function",
				Function: schema.FunctionCall{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
	}
	var usage *schema.TokenUsage
	if frame.Usage != nil {
		usage = &schema.TokenUsage{
			PromptTokens:       frame.Usage.PromptTokens,
			PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: frame.Usage.cachedTokens()},
			CompletionTokens:   frame.Usage.CompletionTokens,
			TotalTokens:        frame.Usage.TotalTokens,
		}
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	}
	if finishReason != "" || usage != nil {
		out.ResponseMeta = &schema.ResponseMeta{FinishReason: finishReason, Usage: usage}
	}
	if out.Content == "" && out.ReasoningContent == "" && len(out.ToolCalls) == 0 && out.ResponseMeta == nil {
		return nil
	}
	return out
}
