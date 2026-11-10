package chatmodel

import (
"context"
"fmt"
"sync"

"github.com/cloudwego/eino/components/model"
"github.com/cloudwego/eino/schema"
)

// Script 是一个脚本化假模型：按预设顺序逐次返回消息，不联网、不花钱。
//
// 学习时的价值：
//  1. 跑 demo 不需要 API key，CI 里也能跑；
//  2. 你完全掌控「模型下一步会说什么」，可以精确复现 ReAct / 工具调用 / 死循环；
//  3. 它同时记录了 ADK 到底把哪些工具传给了模型——这是框架的隐藏契约。
type Script struct {
mu        sync.Mutex
steps     []*schema.Message
idx       int
toolsSeen []string
calls     int
// ChunkSize 控制 Stream 时每帧多少字符，0 表示整段一次给出。
ChunkSize int
}

var (
_ model.BaseChatModel        = (*Script)(nil)
_ model.ToolCallingChatModel = (*Script)(nil)
)

// NewScript 用一串预置回复构造假模型。
func NewScript(steps ...*schema.Message) *Script {
cp := make([]*schema.Message, len(steps))
copy(cp, steps)
return &Script{steps: cp, ChunkSize: 6}
}

// WithTools 记录本次绑定看到的工具名（Eino 要求返回新实例）。
func (s *Script) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
s.mu.Lock()
defer s.mu.Unlock()
s.toolsSeen = s.toolsSeen[:0]
for _, t := range tools {
s.toolsSeen = append(s.toolsSeen, t.Name)
}
return s, nil
}

// ToolsSeen 返回最近一次调用中模型看到的工具名，用于验证「工具确实送到了模型」。
func (s *Script) ToolsSeen() []string {
s.mu.Lock()
defer s.mu.Unlock()
out := make([]string, len(s.toolsSeen))
copy(out, s.toolsSeen)
return out
}

// Calls 返回模型被调用的次数，用于断言循环次数。
func (s *Script) Calls() int {
s.mu.Lock()
defer s.mu.Unlock()
return s.calls
}

func (s *Script) next(input []*schema.Message, opts []model.Option) (*schema.Message, error) {
s.mu.Lock()
defer s.mu.Unlock()

// 从每次调用的 model.Option 里读出工具（ADK 就是这样传工具的）
if o := model.GetCommonOptions(nil, opts...); len(o.Tools) > 0 {
s.toolsSeen = s.toolsSeen[:0]
for _, t := range o.Tools {
s.toolsSeen = append(s.toolsSeen, t.Name)
}
}

s.calls++
if s.idx >= len(s.steps) {
return nil, fmt.Errorf("chatmodel: 脚本已用尽（第 %d 次调用，输入 %d 条消息）", s.calls, len(input))
}
msg := s.steps[s.idx]
s.idx++

// 返回副本，避免调用方改动脚本
cp := *msg
cp.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
return &cp, nil
}

// Generate 返回下一条预置消息。
func (s *Script) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
if err := ctx.Err(); err != nil {
return nil, err
}
return s.next(input, opts)
}

// Stream 把下一条预置消息切成若干帧下发，模拟真实流式。
func (s *Script) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
msg, err := s.next(input, opts)
if err != nil {
return nil, err
}

chunk := s.ChunkSize
if chunk <= 0 {
chunk = len([]rune(msg.Content)) + 1
}

sr, sw := schema.Pipe[*schema.Message](8)
go func() {
defer sw.Close()
runes := []rune(msg.Content)
for i := 0; i < len(runes); i += chunk {
end := i + chunk
if end > len(runes) {
end = len(runes)
}
part := &schema.Message{Role: schema.Assistant, Content: string(runes[i:end])}
if sw.Send(part, nil) {
return
}
}
// 末帧带元信息与工具调用
last := &schema.Message{
Role:             schema.Assistant,
ReasoningContent: msg.ReasoningContent,
ResponseMeta:     msg.ResponseMeta,
ToolCalls:        msg.ToolCalls,
}
if last.ResponseMeta == nil {
last.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
}
sw.Send(last, nil)
}()
return sr, nil
}
