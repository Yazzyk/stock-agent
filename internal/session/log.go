// Package session 实现「只追加事件日志」：Agent 的唯一真相源。
//
// 这是整个项目里最值得反复读的一层。核心不变量只有一句：
//
//模型能看到的一切，都必须能从日志重建。
//
// 一旦接受它，你就免费得到：可回放、可审计、可 fork、可压缩、可评测。
package session

import (
"bufio"
"encoding/json"
"fmt"
"os"
"path/filepath"
"sync"
)

// Type 是事件类型。真实框架（如 dsh）用声明合并扩展；
// Go 里用字符串常量 + 各包自定义 payload 达到同样效果。
type Type string

const (
EvTurnStart   Type = "turn/start"
EvStepStart   Type = "step/start"
EvUserMessage Type = "user/message"
EvAssistant   Type = "assistant/message"
EvToolCall    Type = "tool/call"
EvToolResult  Type = "tool/result"
EvStepEnd     Type = "step/end"
EvTurnEnd     Type = "turn/end"

// EvPrune 是压缩事件：它不删除历史，只声明「这些 seq 从模型视角遮蔽」。
EvPrune Type = "compaction/prune"
)

// Event 是日志中的一条记录。
type Event struct {
// Seq 由日志自己发号，调用方不能指定——这样才能保证严格递增、可校验。
Seq  int64           `json:"seq"`
Type Type            `json:"type"`
Data json.RawMessage `json:"data"`
}

// Log 是单会话的 append-only 日志。
type Log struct {
mu     sync.Mutex
id     string
path   string
events []Event
f      *os.File
w      *bufio.Writer
}

// Open 打开（或创建）一个 JSONL 日志文件。
func Open(path, id string) (*Log, error) {
if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
return nil, err
}
f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
if err != nil {
return nil, err
}
return &Log{id: id, path: path, f: f, w: bufio.NewWriter(f)}, nil
}

// ID 返回会话 id。
func (l *Log) ID() string { return l.id }

// Path 返回日志文件路径。
func (l *Log) Path() string { return l.path }

// Append 追加一条事件。
//
// 顺序很重要：先做无损 JSON 快照，再入日志。序列化失败就整体拒绝，
// 绝不留一条「半截」事件——这是 append-only 能成立的物理保证。
func (l *Log) Append(t Type, payload any) (int64, error) {
raw, err := json.Marshal(payload)
if err != nil {
return 0, fmt.Errorf("session: 事件 %s 无法序列化: %w", t, err)
}

l.mu.Lock()
defer l.mu.Unlock()

ev := Event{Seq: int64(len(l.events)), Type: t, Data: raw}
line, err := json.Marshal(ev)
if err != nil {
return 0, err
}
if _, err := l.w.Write(append(line, '\n')); err != nil {
return 0, err
}
if err := l.w.Flush(); err != nil {
return 0, err
}
l.events = append(l.events, ev)
return ev.Seq, nil
}

// Events 返回日志的只读副本。
func (l *Log) Events() []Event {
l.mu.Lock()
defer l.mu.Unlock()
out := make([]Event, len(l.events))
copy(out, l.events)
return out
}

// Len 返回事件条数。
func (l *Log) Len() int {
l.mu.Lock()
defer l.mu.Unlock()
return len(l.events)
}

// Close 关闭底层文件。
func (l *Log) Close() error {
l.mu.Lock()
defer l.mu.Unlock()
if err := l.w.Flush(); err != nil {
return err
}
return l.f.Close()
}
