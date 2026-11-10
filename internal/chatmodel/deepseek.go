// Package chatmodel 提供两个 Eino ChatModel 实现：
//
//   - DeepSeek：真实的 OpenAI 兼容客户端（零第三方依赖，只靠 net/http）
//   - Script：  脚本化假模型，离线可跑，用于测试与学习
//
// 为什么要自己写 DeepSeek 客户端？
// Eino 的 model.BaseChatModel 只有两个方法，实现它就能接入任何供应商。
// 自己写一遍，你会真正理解「工具调用是怎么通过 HTTP 传的」——
// 这是 Agent 框架里最容易被框架掩盖的一层。
package chatmodel

import (
"bufio"
"bytes"
"context"
"encoding/json"
"errors"
"fmt"
"io"
"net/http"
"os"
"strings"
"time"

"github.com/cloudwego/eino/components/model"
"github.com/cloudwego/eino/schema"
)

// DeepSeekConfig 是 DeepSeek 客户端配置。
type DeepSeekConfig struct {
// APIKey 为空时回退到环境变量 DEEPSEEK_API_KEY。
APIKey string
// BaseURL 默认 https://api.deepseek.com，可指向任何 OpenAI 兼容端点。
BaseURL string
// Model 默认 deepseek-chat。
Model string
// Timeout 默认 120s。
Timeout time.Duration
}

// DeepSeek 实现 model.BaseChatModel + model.ToolCallingChatModel。
type DeepSeek struct {
cfg    DeepSeekConfig
client *http.Client
tools  []*schema.ToolInfo
}

var (
_ model.BaseChatModel        = (*DeepSeek)(nil)
_ model.ToolCallingChatModel = (*DeepSeek)(nil)
)

// NewDeepSeek 构造客户端。APIKey 为空时读环境变量，便于本地开发。
func NewDeepSeek(cfg DeepSeekConfig) (*DeepSeek, error) {
if cfg.APIKey == "" {
cfg.APIKey = os.Getenv("DEEPSEEK_API_KEY")
}
if cfg.APIKey == "" {
return nil, errors.New("chatmodel: 缺少 DEEPSEEK_API_KEY（可写进 .env 或环境变量）")
}
if cfg.BaseURL == "" {
cfg.BaseURL = "https://api.deepseek.com"
}
if cfg.Model == "" {
cfg.Model = "deepseek-chat"
}
if cfg.Timeout == 0 {
cfg.Timeout = 120 * time.Second
}
return &DeepSeek{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

// WithTools 返回绑定了工具副本的新实例（Eino 要求的「安全绑定」语义）。
func (d *DeepSeek) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
cp := *d
cp.tools = tools
return &cp, nil
}

// ---------------------------------------------------------------------------
// wire 类型：这里就是「模型协议」本身
// ---------------------------------------------------------------------------

type wireToolCall struct {
Index    *int   `json:"index,omitempty"`
ID       string `json:"id,omitempty"`
Type     string `json:"type,omitempty"`
Function struct {
Name      string `json:"name,omitempty"`
Arguments string `json:"arguments,omitempty"`
} `json:"function"`
}

type wireMessage struct {
Role             string         `json:"role"`
Content          string         `json:"content"`
ReasoningContent string         `json:"reasoning_content,omitempty"`
ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
Type     string `json:"type"`
Function struct {
Name        string          `json:"name"`
Description string          `json:"description"`
Parameters  json.RawMessage `json:"parameters,omitempty"`
} `json:"function"`
}

type wireRequest struct {
Model         string          `json:"model"`
Messages      []wireMessage   `json:"messages"`
Tools         []wireTool      `json:"tools,omitempty"`
Stream        bool            `json:"stream,omitempty"`
StreamOptions *streamOptions  `json:"stream_options,omitempty"`
Temperature   *float32        `json:"temperature,omitempty"`
MaxTokens     *int            `json:"max_tokens,omitempty"`
ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
}

type streamOptions struct {
IncludeUsage bool `json:"include_usage"`
}

type wireUsage struct {
PromptTokens     int `json:"prompt_tokens"`
CompletionTokens int `json:"completion_tokens"`
TotalTokens      int `json:"total_tokens"`
}

type wireResponse struct {
Choices []struct {
Index   int         `json:"index"`
Message wireMessage `json:"message"`
Delta   wireMessage `json:"delta"`
Finish  string      `json:"finish_reason"`
} `json:"choices"`
Usage *wireUsage `json:"usage"`
Error *struct {
Message string `json:"message"`
Type    string `json:"type"`
} `json:"error"`
}

// ---------------------------------------------------------------------------
// 请求构造
// ---------------------------------------------------------------------------

func toWireMessages(msgs []*schema.Message) []wireMessage {
out := make([]wireMessage, 0, len(msgs))
for _, m := range msgs {
if m == nil {
continue
}
wm := wireMessage{
Role:             string(m.Role),
Content:          m.Content,
ReasoningContent: m.ReasoningContent,
ToolCallID:       m.ToolCallID,
}
for _, tc := range m.ToolCalls {
wc := wireToolCall{ID: tc.ID, Type: tc.Type}
if wc.Type == "" {
wc.Type = "function"
}
wc.Function.Name = tc.Function.Name
wc.Function.Arguments = tc.Function.Arguments
wm.ToolCalls = append(wm.ToolCalls, wc)
}
out = append(out, wm)
}
return out
}

func toWireTools(tools []*schema.ToolInfo) ([]wireTool, error) {
out := make([]wireTool, 0, len(tools))
for _, ti := range tools {
if ti == nil {
continue
}
var wt wireTool
wt.Type = "function"
wt.Function.Name = ti.Name
wt.Function.Description = ti.Desc

params := json.RawMessage(`{"type":"object","properties":{}}`)
if ti.ParamsOneOf != nil {
js, err := ti.ParamsOneOf.ToJSONSchema()
if err != nil {
return nil, fmt.Errorf("chatmodel: 工具 %q 的 schema 转换失败: %w", ti.Name, err)
}
if js != nil {
raw, err := json.Marshal(js)
if err != nil {
return nil, fmt.Errorf("chatmodel: 工具 %q 的 schema 序列化失败: %w", ti.Name, err)
}
params = raw
}
}
wt.Function.Parameters = params
out = append(out, wt)
}
return out, nil
}

// buildRequest 把 Eino 的入参翻译成 HTTP 请求体。
func (d *DeepSeek) buildRequest(input []*schema.Message, opts []model.Option, stream bool) (*wireRequest, error) {
o := model.GetCommonOptions(nil, opts...)

tools := d.tools
if len(o.Tools) > 0 {
tools = o.Tools
}
wt, err := toWireTools(tools)
if err != nil {
return nil, err
}

modelName := d.cfg.Model
if o.Model != nil && *o.Model != "" {
modelName = *o.Model
}

req := &wireRequest{
Model:       modelName,
Messages:    toWireMessages(input),
Tools:       wt,
Stream:      stream,
Temperature: o.Temperature,
MaxTokens:   o.MaxTokens,
}
if stream {
req.StreamOptions = &streamOptions{IncludeUsage: true}
}
return req, nil
}

func (d *DeepSeek) do(ctx context.Context, req *wireRequest) (*http.Response, error) {
body, err := json.Marshal(req)
if err != nil {
return nil, err
}
httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
strings.TrimRight(d.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
if err != nil {
return nil, err
}
httpReq.Header.Set("Content-Type", "application/json")
httpReq.Header.Set("Authorization", "Bearer "+d.cfg.APIKey)
if req.Stream {
httpReq.Header.Set("Accept", "text/event-stream")
}

resp, err := d.client.Do(httpReq)
if err != nil {
return nil, fmt.Errorf("chatmodel: 请求失败: %w", err)
}
if resp.StatusCode != http.StatusOK {
defer resp.Body.Close()
msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
return nil, fmt.Errorf("chatmodel: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}
return resp, nil
}

// ---------------------------------------------------------------------------
// Generate：非流式
// ---------------------------------------------------------------------------

// Generate 一次性拿到完整回复。
func (d *DeepSeek) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
req, err := d.buildRequest(input, opts, false)
if err != nil {
return nil, err
}
resp, err := d.do(ctx, req)
if err != nil {
return nil, err
}
defer resp.Body.Close()

var wr wireResponse
if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
return nil, fmt.Errorf("chatmodel: 解析响应失败: %w", err)
}
if wr.Error != nil {
return nil, fmt.Errorf("chatmodel: %s", wr.Error.Message)
}
if len(wr.Choices) == 0 {
return nil, errors.New("chatmodel: 响应里没有 choices")
}

c := wr.Choices[0]
msg := &schema.Message{
Role:             schema.Assistant,
Content:          c.Message.Content,
ReasoningContent: c.Message.ReasoningContent,
}
for _, tc := range c.Message.ToolCalls {
typ := tc.Type
if typ == "" {
typ = "function"
}
msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
ID:   tc.ID,
Type: typ,
Function: schema.FunctionCall{
Name:      tc.Function.Name,
Arguments: tc.Function.Arguments,
},
})
}
msg.ResponseMeta = &schema.ResponseMeta{FinishReason: c.Finish}
if wr.Usage != nil {
msg.ResponseMeta.Usage = &schema.TokenUsage{
PromptTokens:     wr.Usage.PromptTokens,
CompletionTokens: wr.Usage.CompletionTokens,
TotalTokens:      wr.Usage.TotalTokens,
}
}
return msg, nil
}

// ---------------------------------------------------------------------------
// Stream：SSE 流式
// ---------------------------------------------------------------------------

// Stream 返回逐块到达的增量。文本增量会立刻下发；
// 工具调用需要拼装完整参数才对模型有意义，因此在最后一并给出。
func (d *DeepSeek) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
req, err := d.buildRequest(input, opts, true)
if err != nil {
return nil, err
}
resp, err := d.do(ctx, req)
if err != nil {
return nil, err
}

sr, sw := schema.Pipe[*schema.Message](16)
go func() {
defer resp.Body.Close()
defer sw.Close()

var (
text      strings.Builder
reasoning strings.Builder
finish    string
usage     *schema.TokenUsage
calls     []*wireToolCall // 按 index 归并
sentRole  bool
)

sc := bufio.NewScanner(resp.Body)
sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

for sc.Scan() {
line := strings.TrimSpace(sc.Text())
if line == "" || !strings.HasPrefix(line, "data:") {
continue
}
payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
if payload == "[DONE]" {
break
}

var wr wireResponse
if err := json.Unmarshal([]byte(payload), &wr); err != nil {
sw.Send(nil, fmt.Errorf("chatmodel: 解析 SSE 分片失败: %w", err))
return
}
if wr.Error != nil {
sw.Send(nil, fmt.Errorf("chatmodel: %s", wr.Error.Message))
return
}
if wr.Usage != nil {
usage = &schema.TokenUsage{
PromptTokens:     wr.Usage.PromptTokens,
CompletionTokens: wr.Usage.CompletionTokens,
TotalTokens:      wr.Usage.TotalTokens,
}
}
if len(wr.Choices) == 0 {
continue
}

ch := wr.Choices[0]
if ch.Finish != "" {
finish = ch.Finish
}
if ch.Delta.ReasoningContent != "" {
reasoning.WriteString(ch.Delta.ReasoningContent)
}

// 文本增量：边收边发
if ch.Delta.Content != "" {
text.WriteString(ch.Delta.Content)
chunk := &schema.Message{Role: schema.Assistant, Content: ch.Delta.Content}
if !sentRole {
sentRole = true
}
if sw.Send(chunk, nil) {
return // 下游已关闭
}
}

// 工具调用增量：按 index 归并
for _, tc := range ch.Delta.ToolCalls {
idx := 0
if tc.Index != nil {
idx = *tc.Index
}
for len(calls) <= idx {
calls = append(calls, &wireToolCall{})
}
slot := calls[idx]
if tc.ID != "" {
slot.ID = tc.ID
}
if tc.Type != "" {
slot.Type = tc.Type
}
if tc.Function.Name != "" {
slot.Function.Name += tc.Function.Name
}
if tc.Function.Arguments != "" {
slot.Function.Arguments += tc.Function.Arguments
}
}
}
if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
sw.Send(nil, fmt.Errorf("chatmodel: 读取流失败: %w", err))
return
}

// 末帧：把拼装好的工具调用与元信息交出去
final := &schema.Message{
Role:             schema.Assistant,
Content:          "",
ReasoningContent: reasoning.String(),
ResponseMeta:     &schema.ResponseMeta{FinishReason: finish, Usage: usage},
}
for _, c := range calls {
if c == nil || c.Function.Name == "" {
continue
}
typ := c.Type
if typ == "" {
typ = "function"
}
final.ToolCalls = append(final.ToolCalls, schema.ToolCall{
ID:   c.ID,
Type: typ,
Function: schema.FunctionCall{
Name:      c.Function.Name,
Arguments: c.Function.Arguments,
},
})
}
sw.Send(final, nil)
}()

return sr, nil
}
