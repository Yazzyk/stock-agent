package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 双工（前台 RouterAgent + 后台 Worker） ----------
//
// 设计目标（superAgent 双工策略）：
//  1. RouterAgent 是前台：只做拆解、派发、汇报，不在自己的上下文里跑重活；
//  2. Worker 是后台：派发后立刻返回，子智能体在后台跑完整 ReAct；
//  3. 子结果异步回收到父会话的 ReAct 步骤边界（模型调用前），
//     父会话因此可以「边派发、边汇报、边收结果」，而不是阻塞在一个工具调用里；
//  4. 父会话想结束时若仍有子智能体在跑，ReAct 图会进入 wait 节点阻塞等一个结果，
//     直到全部子智能体结束或父会话被停止——绝不静默丢掉子报告。

// childResult 是一次后台子智能体的终态结果。
type childResult struct {
	SessionID string
	Label     string
	Worker    string
	Status    string
	Report    string
	Error     string
	Tokens    int
	Steps     int
	ElapsedMs int64
	Truncated bool
}

// childHub 是某个父会话的后台子智能体集合：
// 负责并发上限、终态结果排队，以及「等一个结果」的唤醒。
// 所有方法都可并发调用（派发发生在工具执行 goroutine，回收发生在模型节点 goroutine）。
type childHub struct {
	mu      sync.Mutex
	running map[string]string             // childSessionID -> label
	cancels map[string]context.CancelFunc // childSessionID -> 取消函数
	done    []childResult
	notify  chan struct{}
}

// newChildHub 构造一个空的子智能体集合。
func newChildHub() *childHub {
	return &childHub{
		running: map[string]string{},
		cancels: map[string]context.CancelFunc{},
		notify:  make(chan struct{}, 1),
	}
}

// begin 登记一个正在跑的子智能体；超过并发上限时返回错误。
func (h *childHub) begin(sessionID, label string, cancel context.CancelFunc) error {
	if h == nil {
		return fmt.Errorf("当前会话没有后台派发通道：只能在运行中的主会话里派发")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.running) >= maxSpawnChildren {
		return fmt.Errorf("最多同时 %d 个子智能体在跑（当前 %d 个），请等其中一个结束再派生",
			maxSpawnChildren, len(h.running))
	}
	h.running[sessionID] = label
	if cancel != nil {
		h.cancels[sessionID] = cancel
	}
	return nil
}

// pending 返回仍在跑的子智能体数量。
func (h *childHub) pending() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.running)
}

// finish 记录一个子智能体的终态结果并唤醒等待方。
func (h *childHub) finish(res childResult) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.running, res.SessionID)
	delete(h.cancels, res.SessionID)
	h.done = append(h.done, res)
	h.mu.Unlock()
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

// drain 取走所有已完成的子结果（不阻塞）。
func (h *childHub) drain() []childResult {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.done) == 0 {
		return nil
	}
	out := h.done
	h.done = nil
	return out
}

// waitOne 阻塞等待一个子结果：ctx 取消时返回 false（父会话被停止/超时）。
func (h *childHub) waitOne(ctx context.Context) (childResult, bool) {
	if h == nil {
		return childResult{}, false
	}
	for {
		h.mu.Lock()
		if len(h.done) > 0 {
			res := h.done[0]
			h.done = h.done[1:]
			h.mu.Unlock()
			return res, true
		}
		running := len(h.running)
		h.mu.Unlock()
		if running == 0 {
			return childResult{}, false
		}
		select {
		case <-ctx.Done():
			return childResult{}, false
		case <-h.notify:
		}
	}
}

// cancelAll 取消所有仍在跑的子智能体（父会话结束时收尾，避免留下没人收结果的孤儿）。
func (h *childHub) cancelAll() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(h.cancels))
	for _, cancel := range h.cancels {
		cancels = append(cancels, cancel)
	}
	n := len(h.running)
	h.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return n
}

// labels 返回仍在跑的子智能体标签（用于报告里的收尾说明）。
func (h *childHub) labels() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.running))
	for _, label := range h.running {
		out = append(out, label)
	}
	return out
}

// childResultMessage 把子智能体结果包装成喂回父会话的消息：
// 明确「这是后台 worker 的结论，不是用户消息」，并要求父会话先向前台汇报再继续。
func childResultMessage(res childResult) string {
	var b strings.Builder
	b.WriteString("【子智能体完成】" + res.Label)
	b.WriteString("（worker=" + res.Worker + "，会话 " + res.SessionID)
	b.WriteString("，状态 " + res.Status)
	b.WriteString("，步数 " + strconv.Itoa(res.Steps))
	b.WriteString("，token " + strconv.Itoa(res.Tokens))
	b.WriteString("，耗时 " + formatElapsed(res.ElapsedMs) + "）")
	b.WriteString(nl + nl)
	if strings.TrimSpace(res.Report) != "" {
		b.WriteString(res.Report)
	} else {
		b.WriteString("（这份子报告没有正文）")
	}
	if res.Truncated {
		b.WriteString(nl + nl + "（报告过长，以上只是前 " + strconv.Itoa(maxSpawnReportRunes) + " 字）")
	}
	if strings.TrimSpace(res.Error) != "" {
		b.WriteString(nl + nl + "【子任务错误】" + res.Error)
	}
	b.WriteString(nl + nl + "（这是后台 worker 的结论，不是用户发言。先用一两句话向用户汇报这条子结果的关键结论，" +
		"再决定下一步：继续派发、自己补充核查，或汇总收尾。最终交付物里引用它只能作为证据行文" +
		"（如「据盘前计划子任务：…」），不要整块粘贴，也不要原样复述整份子报告。）")
	return b.String()
}

// DispatchText 是后台派发后回给父会话的工具结果文案：
// 让模型明确「已经有人在干了」，并给出前台播报的动作要求。
func DispatchText(res SpawnResult) string {
	var b strings.Builder
	b.WriteString("【已派发】子智能体「" + res.Label + "」已在后台运行")
	b.WriteString("（worker=" + res.Worker + "，会话 " + res.SessionID + "）。")
	b.WriteString(nl + nl + "它是异步执行的：你现在就可以继续用一两句话向用户汇报「派了什么、为什么、要等多久」，" +
		"也可以继续派发其它子任务或自己补充查证。子任务结束时系统会把结果作为一条消息递给你，" +
		"收到后再汇报这条子结果的要点。不要假装已经拿到它的结论。")
	return b.String()
}

// formatElapsed 把毫秒格式化成 1.3s / 12s / 2m10s。
func formatElapsed(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	sec := float64(ms) / 1000
	if sec < 10 {
		return strconv.FormatFloat(sec, 'f', 1, 64) + "s"
	}
	if sec < 60 {
		return strconv.FormatInt(int64(sec+0.5), 10) + "s"
	}
	m := int64(sec) / 60
	s := int64(sec) % 60
	if s == 0 {
		return strconv.FormatInt(m, 10) + "m"
	}
	return strconv.FormatInt(m, 10) + "m" + strconv.FormatInt(s, 10) + "s"
}

// ---------- Runtime 上的后台派发通道 ----------

// registerChildHub 为一次运行登记后台派发通道（重复登记直接复用，测试可重复调用）。
func (r *Runtime) registerChildHub(sessionID string) *childHub {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.children == nil {
		r.children = map[string]*childHub{}
	}
	h, ok := r.children[sessionID]
	if !ok || h == nil {
		h = newChildHub()
		r.children[sessionID] = h
	}
	return h
}

// childHubOf 取某会话的后台派发通道（没有就返回 nil）。
func (r *Runtime) childHubOf(sessionID string) *childHub {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.children[sessionID]
}

// releaseChildHub 运行结束后释放通道（仍在跑的子智能体由看护器/ctx 负责收尾）。
func (r *Runtime) releaseChildHub(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.children, sessionID)
}

// emitProgress 发一条「前台播报」进度事件：前端据此展示后台子智能体的派发/完成状态。
func (r *Runtime) emitProgress(payload map[string]any) {
	if payload == nil {
		return
	}
	payload["at"] = r.timestamp(r.opts.Clock())
	r.emit("agent.progress", payload)
}

// nowRFC 是进度事件里统一的毫秒时间戳。
func nowRFC(t time.Time) string {
	return t.In(shanghaiLoc()).Format(time.RFC3339Nano)
}
