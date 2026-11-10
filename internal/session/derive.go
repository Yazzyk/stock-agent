package session

import (
"encoding/json"
"fmt"

"github.com/cloudwego/eino/schema"
)

// Payload 类型：日志里存的是这些结构，不是 schema.Message 本身。
// 这样事件格式可以独立演进，而模型消息结构保持在 Eino 的词汇里。
type (
userPayload struct {
Message *schema.Message `json:"message"`
}
assistantPayload struct {
Message *schema.Message `json:"message"`
Turn    int             `json:"turn"`
Step    int             `json:"step"`
}
toolCallPayload struct {
CallID string `json:"call_id"`
Name   string `json:"name"`
Args   string `json:"args"`
}
toolResultPayload struct {
CallID  string `json:"call_id"`
Name    string `json:"name"`
Content string `json:"content"`
IsError bool   `json:"is_error"`
}
prunePayload struct {
// ShadowedSeqs 里的 seq 不会进入派生历史。
ShadowedSeqs []int64 `json:"shadowed_seqs"`
Reason       string  `json:"reason"`
}
)

// Derive 从日志重建模型历史。这是全局唯一的历史来源。
//
// 注意：它不是「重放请求」，而是「重放可见性」——只把消息生产型事件变成消息。
func (l *Log) Derive() ([]*schema.Message, error) {
events := l.Events()

shadows, err := l.shadowed(events)
if err != nil {
return nil, err
}

out := make([]*schema.Message, 0, len(events))
for _, ev := range events {
if shadows[ev.Seq] {
continue
}
switch ev.Type {
case EvUserMessage:
var p userPayload
if err := json.Unmarshal(ev.Data, &p); err != nil {
return nil, fmt.Errorf("derive: seq %d 损坏: %w", ev.Seq, err)
}
if p.Message != nil {
out = append(out, p.Message)
}

case EvAssistant:
var p assistantPayload
if err := json.Unmarshal(ev.Data, &p); err != nil {
return nil, fmt.Errorf("derive: seq %d 损坏: %w", ev.Seq, err)
}
// 空内容且无工具调用的 assistant 消息不进历史
if p.Message == nil || (p.Message.Content == "" && len(p.Message.ToolCalls) == 0) {
continue
}
out = append(out, p.Message)

case EvToolResult:
var p toolResultPayload
if err := json.Unmarshal(ev.Data, &p); err != nil {
return nil, fmt.Errorf("derive: seq %d 损坏: %w", ev.Seq, err)
}
out = append(out, schema.ToolMessage(p.Content, p.CallID))
}
}
return out, nil
}

// shadowed 收集所有被压缩事件遮蔽的 seq。
func (l *Log) shadowed(events []Event) (map[int64]bool, error) {
out := map[int64]bool{}
for _, ev := range events {
if ev.Type != EvPrune {
continue
}
var p prunePayload
if err := json.Unmarshal(ev.Data, &p); err != nil {
return nil, fmt.Errorf("derive: 压缩事件 seq %d 损坏: %w", ev.Seq, err)
}
for _, s := range p.ShadowedSeqs {
out[s] = true
}
}
return out, nil
}

// Prune 记录一次「遮蔽」，把指定 seq 从模型视角摘掉。
// 历史本身不动，日志只增不减。
func (l *Log) Prune(seqs []int64, reason string) (int64, error) {
return l.Append(EvPrune, prunePayload{ShadowedSeqs: seqs, Reason: reason})
}

// Stats 是给学习用的日志概览。
type Stats struct {
Events      int
UserMsgs    int
Assistant   int
ToolCalls   int
ToolResults int
Prunes      int
}

// Stats 统计各类事件数量，便于观察压缩前后变化。
func (l *Log) Stats() Stats {
var s Stats
for _, ev := range l.Events() {
s.Events++
switch ev.Type {
case EvUserMessage:
s.UserMsgs++
case EvAssistant:
s.Assistant++
case EvToolCall:
s.ToolCalls++
case EvToolResult:
s.ToolResults++
case EvPrune:
s.Prunes++
}
}
return s
}
