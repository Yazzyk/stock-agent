package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// TestReactWaitsForBackgroundChild 验证双工的核心协议：
// 模型想结束时后台还有子智能体在跑，ReAct 不会直接结束，而是先等到子结果并把它注入上下文。
func TestReactWaitsForBackgroundChild(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "我先派一个子智能体去查资金流，等它回来再给你结论。"},
		{Role: contract.RoleAssistant, Content: "汇总：子智能体确认主力净流入 1.2 亿，结论如下……"},
	}}
	hub := newChildHub()
	if err := hub.begin("child-1", "资金流核查", nil); err != nil {
		t.Fatal(err)
	}
	// 模拟后台 worker：稍后完成并把结果交回通道。
	go func() {
		time.Sleep(20 * time.Millisecond)
		hub.finish(childResult{
			SessionID: "child-1",
			Label:     "资金流核查",
			Worker:    "quant",
			Status:    StatusOK,
			Report:    "主力净流入 1.2 亿",
			Tokens:    100,
			Steps:     2,
			ElapsedMs: 1500,
		})
	}()

	react, err := buildReact(context.Background(), reactConfig{
		LLM:             llm,
		MaxSteps:        6,
		PendingChildren: hub.pending,
		WaitChild: func(ctx context.Context) (string, bool) {
			res, ok := hub.waitOne(ctx)
			if !ok {
				return "", false
			}
			return childResultMessage(res), true
		},
	})
	if err != nil {
		t.Fatalf("构建 ReAct 图失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := react.Run(ctx, "查一下资金流")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Steps < 2 {
		t.Fatalf("模型必须被再次调用一次（等子结果后继续），实际步数 %d", res.Steps)
	}
	seen := llm.messagesAt(1)
	joined := joinMessages(seen)
	if !strings.Contains(joined, "【子智能体完成】") || !strings.Contains(joined, "主力净流入 1.2 亿") {
		t.Fatalf("第二次模型调用必须拿到子智能体结果，实际: %q", joined)
	}
	if !strings.Contains(res.Content, "汇总") {
		t.Fatalf("最终内容应是子结果之后的汇总，实际: %q", res.Content)
	}
}

// TestReactDrainsFinishedChildren 验证「模型还有工具要调」时不阻塞：
// 已完成的子结果在步骤边界直接并入上下文。
func TestReactDrainsFinishedChildren(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "第一步：先派发。"},
		{Role: contract.RoleAssistant, Content: "子结果已并入，继续收尾。"},
	}}
	drains := 0
	pending := 1
	react, err := buildReact(context.Background(), reactConfig{
		LLM:      llm,
		MaxSteps: 6,
		DrainChildren: func() []string {
			drains++
			// 第一次模型调用前还没有结果；等 wait 之后（第二次调用）再交回结果。
			if drains < 2 {
				return nil
			}
			pending = 0
			return []string{childResultMessage(childResult{
				SessionID: "child-9",
				Label:     "盘前计划",
				Worker:    "premarket",
				Status:    StatusOK,
				Report:    "今日主线：算力",
			})}
		},
		PendingChildren: func() int { return pending },
		WaitChild: func(context.Context) (string, bool) {
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("构建 ReAct 图失败: %v", err)
	}
	if _, err := react.Run(context.Background(), "做盘前计划"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	joined := joinMessages(llm.messagesAt(1))
	if !strings.Contains(joined, "今日主线：算力") {
		t.Fatalf("第二次模型调用必须带上已完成的子结果，实际: %q", joined)
	}
}

// TestChildHubConcurrency 验证并发上限与结果排队。
func TestChildHubConcurrency(t *testing.T) {
	hub := newChildHub()
	if err := hub.begin("c1", "一", nil); err != nil {
		t.Fatalf("第一个子智能体不应被拒: %v", err)
	}
	if err := hub.begin("c2", "二", nil); err != nil {
		t.Fatalf("第二个子智能体不应被拒: %v", err)
	}
	if err := hub.begin("c3", "三", nil); err == nil {
		t.Fatal("超过并发上限必须被拒")
	}
	hub.finish(childResult{SessionID: "c1", Label: "一", Status: StatusOK, Report: "完成一"})
	if got := hub.pending(); got != 1 {
		t.Fatalf("完成后在跑数量应为 1，实际 %d", got)
	}
	results := hub.drain()
	if len(results) != 1 || results[0].SessionID != "c1" {
		t.Fatalf("drain 必须取到已完成的子结果，实际 %+v", results)
	}
	if again := hub.drain(); len(again) != 0 {
		t.Fatalf("drain 后不应有残留，实际 %+v", again)
	}
}

// joinMessageTail 取最后一次模型调用最后两条消息（测试调试用）。
func joinMessageTail(msgs []contract.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	start := len(msgs) - 2
	if start < 0 {
		start = 0
	}
	return truncateRunes(joinMessages(msgs[start:]), 300)
}

// messagesAt 取第 i 次模型调用的上下文（越界返回 nil）。
func (f *fakeLLM) messagesAt(i int) []contract.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.seen) {
		return nil
	}
	return f.seen[i]
}

// joinMessages 把一次模型调用的消息拼成纯文本（断言用）。
func joinMessages(msgs []contract.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// duplexLLM 是双工端到端测试用的模型：按 system prompt 区分前台与子智能体，
// 前台第 1 步派发后台子任务、第 2 步只汇报进度、第 3 步收结果汇总。
type duplexLLM struct {
	mu          sync.Mutex
	parentCalls int
	parentSeen  [][]contract.Message
}

func (l *duplexLLM) Chat(_ context.Context, req contract.ChatRequest) (contract.Reply, error) {
	if strings.Contains(req.System, "RouterAgent") {
		l.mu.Lock()
		l.parentCalls++
		n := l.parentCalls
		l.parentSeen = append(l.parentSeen, append([]contract.Message{}, req.Messages...))
		l.mu.Unlock()
		switch n {
		case 1:
			return contract.Reply{Message: contract.Message{
				Role: contract.RoleAssistant,
				ToolCalls: []contract.ToolCall{{
					ID:   "spawn-1",
					Name: "tool.call",
					Arguments: `{"name":"agent.spawn","args":{"task":"梳理今日主线与观察清单",` +
						`"worker":"premarket","label":"盘前计划","background":true}}`,
				}},
			}, Usage: contract.Usage{TotalTokens: 10}, Model: "fake"}, nil
		case 2:
			return contract.Reply{Message: contract.Message{
				Role:    contract.RoleAssistant,
				Content: "已派出「盘前计划」在后台梳理主线，我先等它回来再汇总。",
			}, Usage: contract.Usage{TotalTokens: 10}, Model: "fake"}, nil
		default:
			return contract.Reply{Message: contract.Message{
				Role:    contract.RoleAssistant,
				Content: "汇总：今日主线为算力，盘前计划已给出观察清单（来源：盘前计划）。",
			}, Usage: contract.Usage{TotalTokens: 10}, Model: "fake"}, nil
		}
	}
	// 子智能体（premarket 的 system prompt 不含 RouterAgent）：
	// 故意慢 30ms，保证前台第 2 次模型调用发生时它还在跑（否则测不到 wait 节点）。
	time.Sleep(30 * time.Millisecond)
	return contract.Reply{Message: contract.Message{
		Role:    contract.RoleAssistant,
		Content: "子结论：算力主线延续，观察 2 只补涨标的。",
	}, Usage: contract.Usage{TotalTokens: 8}, Model: "fake"}, nil
}

func (l *duplexLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	reply, err := l.Chat(ctx, req)
	if err == nil && onDelta != nil && reply.Message.Content != "" {
		onDelta(reply.Message.Content)
	}
	return reply, err
}

func (l *duplexLLM) Models(context.Context) ([]contract.ModelInfo, error) {
	return []contract.ModelInfo{{Provider: "fake", Model: "fake", HasKey: true, IsDefault: true}}, nil
}

func (l *duplexLLM) Default(context.Context) (contract.ModelInfo, error) {
	return contract.ModelInfo{Provider: "fake", Model: "fake", HasKey: true, IsDefault: true}, nil
}

// TestDuplexSpawnEndToEnd 验证「前台 RouterAgent 派发 → 后台 Worker 执行 → 结果回收 → 汇总」全链路：
//   - 派发即刻返回（不阻塞在一个工具调用里）；
//   - 前台在等待期间仍然能产出一段汇报（第 2 次模型调用）；
//   - 子结果作为【子智能体完成】消息回传给前台，最终汇总包含子结论；
//   - 编队事件（agent.child）与进度事件（agent.progress）都发出。
func TestDuplexSpawnEndToEnd(t *testing.T) {
	llm := &duplexLLM{}
	var rt *Runtime
	rt, _, _, sink := newTestRuntime(t, llm, func(o *RuntimeOptions) {
		// 把 agent.spawn 注册进测试宿主，Run 里用 rt.SpawnAsync（双工路径）。
		o.Gateway = progressive.NewGateway(&fakeHost{tools: []plugin.Tool{{
			Name:    "agent.spawn",
			Title:   "派生子智能体",
			Summary: "把子任务派给指定 worker（后台执行）",
			Tags:    []string{"agent"},
			Risk:    plugin.RiskRead,
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Task       string `json:"task"`
					Worker     string `json:"worker"`
					Label      string `json:"label"`
					Background *bool  `json:"background"`
				}
				_ = json.Unmarshal(args, &in)
				if in.Background != nil && !*in.Background {
					res, err := rt.Spawn(ctx, SpawnRequest{Task: in.Task, Worker: in.Worker, Label: in.Label})
					if err != nil {
						return err.Error(), nil
					}
					return res.ToolText(), nil
				}
				res, err := rt.SpawnAsync(ctx, SpawnRequest{Task: in.Task, Worker: in.Worker, Label: in.Label})
				if err != nil {
					return err.Error(), nil
				}
				return DispatchText(res), nil
			},
		}}}, nil, nil, plugin.RiskRead)
	})
	report, err := rt.Run(context.Background(), "router", "做今天的盘前计划", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(report.Report, "汇总") {
		t.Fatalf("最终报告应是汇总，实际: %q", report.Report)
	}
	for i, seen := range llm.parentSeen {
		t.Logf("parent call %d messages=%d tail=%q", i+1, len(seen), joinMessageTail(seen))
	}
	if len(llm.parentSeen) < 3 {
		t.Fatalf("前台至少要跑 3 次模型调用（派发/汇报/汇总），实际 %d", len(llm.parentSeen))
	}
	third := joinMessages(llm.parentSeen[2])
	if !strings.Contains(third, "【子智能体完成】") || !strings.Contains(third, "算力主线延续") {
		t.Fatalf("第三次模型调用必须带上子智能体结果，实际: %q", third)
	}
	// 子会话必须落盘且挂在父会话下。
	metas, err := rt.sessions.List(20)
	if err != nil {
		t.Fatal(err)
	}
	var child *SessionMeta
	for i := range metas {
		if metas[i].ParentID == report.SessionID {
			child = &metas[i]
		}
	}
	if child == nil {
		t.Fatalf("必须有一个子会话挂在父会话 %s 下，实际 %+v", report.SessionID, metas)
	}
	if child.ModeOrDefault() != ModeSubagent || child.Status != StatusOK {
		t.Fatalf("子会话元信息不对: %+v", child)
	}
	// 编队与前台播报事件都要发出来。
	if len(sink.byTopic("agent.child")) == 0 {
		t.Fatal("必须发 agent.child（编队树依赖它）")
	}
	if len(sink.byTopic("agent.progress")) == 0 {
		t.Fatal("必须发 agent.progress（前台播报依赖它）")
	}
}

// duplexScenario 跑一次完整的双工编排（RouterAgent 派发 → 后台 worker 执行 → 结果回收 → 汇总），
// 返回最终报告、模型调用记录、事件汇与运行时（报告过滤 / 双工回收测试共用）。
func duplexScenario(t *testing.T) (Report, *duplexLLM, *eventSink, *Runtime) {
	t.Helper()
	llm := &duplexLLM{}
	var rt *Runtime
	rt, _, _, sink := newTestRuntime(t, llm, func(o *RuntimeOptions) {
		o.Gateway = progressive.NewGateway(&fakeHost{tools: []plugin.Tool{{
			Name:    "agent.spawn",
			Title:   "派生子智能体",
			Summary: "把子任务派给指定 worker（后台执行）",
			Tags:    []string{"agent"},
			Risk:    plugin.RiskRead,
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Task       string `json:"task"`
					Worker     string `json:"worker"`
					Label      string `json:"label"`
					Background *bool  `json:"background"`
				}
				_ = json.Unmarshal(args, &in)
				if in.Background != nil && !*in.Background {
					res, err := rt.Spawn(ctx, SpawnRequest{Task: in.Task, Worker: in.Worker, Label: in.Label})
					if err != nil {
						return err.Error(), nil
					}
					return res.ToolText(), nil
				}
				res, err := rt.SpawnAsync(ctx, SpawnRequest{Task: in.Task, Worker: in.Worker, Label: in.Label})
				if err != nil {
					return err.Error(), nil
				}
				return DispatchText(res), nil
			},
		}}}, nil, nil, plugin.RiskRead)
	})
	report, err := rt.Run(context.Background(), "router", "做今天的盘前计划", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	return report, llm, sink, rt
}

// TestDuplexReportEventsScopedToOwningSession 验证「报告 = 交付物」的后端契约：
//   - 每次运行只发一条 agent.report，且带**产生它的会话 id**；
//   - 根会话与子会话的 sessionId 不同 → 前端能据此把子报告过滤到「过程产出」容器，
//     报告册里永远只有根会话的最终交付物；
//   - 同时证明双工没有丢结果：父会话上下文里拿到【子智能体完成】，最终报告是汇总。
func TestDuplexReportEventsScopedToOwningSession(t *testing.T) {
	report, llm, sink, rt := duplexScenario(t)

	var childSid string
	metas, err := rt.sessions.List(20)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	for _, m := range metas {
		if m.ParentID == report.SessionID {
			childSid = m.ID
		}
	}
	if childSid == "" {
		t.Fatalf("必须有一个子会话挂在父会话 %s 下，实际 %+v", report.SessionID, metas)
	}

	type reportPayload struct {
		SessionID string `json:"sessionId"`
		Worker    string `json:"worker"`
		Report    string `json:"report"`
	}
	// 事件是异步广播的：等根会话与子会话的两条 report 都落地再断言（避免读得太早）。
	if !waitFor(t, 3*time.Second, func() bool { return len(sink.byTopic("agent.report")) >= 2 }) {
		t.Fatalf("必须收到根会话与子会话两条 agent.report，实际 %d 条", len(sink.byTopic("agent.report")))
	}
	events := sink.byTopic("agent.report")
	perSession := map[string]int{}
	for _, e := range events {
		var p reportPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("agent.report 载荷无法解析: %v", err)
		}
		if p.SessionID == "" {
			t.Fatal("每条 agent.report 都必须带 sessionId，否则前端无法区分交付物与子过程产出")
		}
		perSession[p.SessionID]++
		if p.SessionID == childSid && !strings.Contains(p.Report, "子结论") {
			t.Fatalf("子会话的报告应是子智能体的过程产出，实际: %q", p.Report)
		}
		if p.SessionID == report.SessionID && !strings.Contains(p.Report, "汇总") {
			t.Fatalf("根会话的报告应是整合后的交付物，实际: %q", p.Report)
		}
	}
	if perSession[report.SessionID] != 1 {
		t.Fatalf("根会话一次运行只能有一份交付物，实际 %d 条", perSession[report.SessionID])
	}
	if perSession[childSid] != 1 {
		t.Fatalf("子会话应正好产出一份过程报告，实际 %d 条", perSession[childSid])
	}

	// 双工不丢结果：父会话必须收到子结果消息，并把子结论整合进最终交付物。
	lastSeen := joinMessages(llm.parentSeen[len(llm.parentSeen)-1])
	if !strings.Contains(lastSeen, "【子智能体完成】") || !strings.Contains(lastSeen, "算力主线延续") {
		t.Fatalf("父会话必须回收子智能体结果，实际最后一次上下文: %q", lastSeen)
	}
	if !strings.Contains(report.Report, "汇总") {
		t.Fatalf("最终交付物应是汇总，实际: %q", report.Report)
	}
}
