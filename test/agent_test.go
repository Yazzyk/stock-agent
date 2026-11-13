package test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/agent"
	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- fake LLM：按脚本回放，并记录每次请求 ----------

type fakeLLM struct {
	mu       sync.Mutex
	requests []contract.ChatRequest
	script   []contract.Reply
	index    int
	// repeatLast 为 true 时，脚本用完后一直回放最后一条（用来测步数上限）
	repeatLast bool
}

func newFakeLLM(script ...contract.Reply) *fakeLLM {
	return &fakeLLM{script: script}
}

func (f *fakeLLM) next() contract.Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.script) == 0 {
		return textReply("（脚本为空）", 0)
	}
	if f.index < len(f.script) {
		reply := f.script[f.index]
		f.index++
		return reply
	}
	if f.repeatLast {
		return f.script[len(f.script)-1]
	}
	return f.script[len(f.script)-1]
}

func (f *fakeLLM) Chat(_ context.Context, req contract.ChatRequest) (contract.Reply, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return f.next(), nil
}

func (f *fakeLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	reply, err := f.Chat(ctx, req)
	if err == nil && onDelta != nil && reply.Message.Content != "" {
		onDelta(reply.Message.Content)
	}
	return reply, err
}

func (f *fakeLLM) Models(context.Context) ([]contract.ModelInfo, error) {
	return []contract.ModelInfo{{Provider: "fake", Model: "fake-chat", HasKey: true, IsDefault: true}}, nil
}

func (f *fakeLLM) Default(context.Context) (contract.ModelInfo, error) {
	return contract.ModelInfo{Provider: "fake", Model: "fake-chat", HasKey: true, IsDefault: true}, nil
}

func (f *fakeLLM) snapshot() []contract.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.ChatRequest(nil), f.requests...)
}

func textReply(content string, usage int) contract.Reply {
	return contract.Reply{
		Message: contract.Message{Role: contract.RoleAssistant, Content: content},
		Usage:   contract.Usage{PromptTokens: usage / 2, CompletionTokens: usage - usage/2, TotalTokens: usage},
		Model:   "fake-chat",
	}
}

func toolCallReply(usage int, name, args string) contract.Reply {
	return contract.Reply{
		Message: contract.Message{
			Role:      contract.RoleAssistant,
			ToolCalls: []contract.ToolCall{{Name: name, Arguments: args}},
		},
		Usage: contract.Usage{PromptTokens: usage / 2, CompletionTokens: usage - usage/2, TotalTokens: usage},
		Model: "fake-chat",
	}
}

// ---------- 装配 ----------

type agentHarness struct {
	runtime *agent.Runtime
	kernel  *kernelFixture
	llm     *fakeLLM
	events  *plugin.Bus
}

func newAgentHarness(t *testing.T, llm *fakeLLM, policy contract.Policy) *agentHarness {
	t.Helper()
	f := newKernel(t)
	registered := 0
	quoteTool := plugin.Tool{
		Name:    "market.quote",
		Title:   "行情快照",
		Summary: "取个股实时快照（测试用）",
		Tags:    []string{"market"},
		Risk:    plugin.RiskNetwork,
		Schema:  json.RawMessage(`{"type":"object","properties":{"codes":{"type":"array"}}}`),
		Run: func(_ context.Context, args json.RawMessage) (any, error) {
			registered++
			return map[string]any{"code": "600000", "name": "浦发银行", "price": 10.2, "echo": string(args)}, nil
		},
	}
	f.register(t, withTools(newPlugin("market"), quoteTool))
	f.start(t)

	bus := plugin.NewBus()
	if policy.MaxToolCalls == 0 {
		policy.MaxToolCalls = 8
	}
	if policy.MaxTokensPerRun == 0 {
		policy.MaxTokensPerRun = 100000
	}
	gw := progressive.NewGateway(f.k, f.guard, f.audit, plugin.RiskNetwork)
	rt := agent.NewRuntime(agent.RuntimeOptions{
		LLM:     llm,
		Guard:   f.guard,
		Audit:   f.audit,
		Events:  bus,
		Policy:  policy,
		Store:   f.store,
		Gateway: gw,
		Clock:   time.Now,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &agentHarness{runtime: rt, kernel: f, llm: llm, events: bus}
}

// 工具结果必须回填进下一轮上下文，且步数/token 统计要对得上。
func TestAgentLoopToolResultFeedback(t *testing.T) {
	llm := newFakeLLM(
		toolCallReply(100, "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		textReply("600000 现价 10.20，结论：观察量能。", 20),
	)
	h := newAgentHarness(t, llm, contract.Policy{MaxToolCalls: 8, MaxTokensPerRun: 100000})

	rep, err := h.runtime.Run(context.Background(), "chat", "600000 现在多少钱", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.Steps != 2 {
		t.Fatalf("步数应为 2（一次工具调用 + 一次收尾），实际 %d", rep.Steps)
	}
	if rep.Usage.TotalTokens != 120 {
		t.Fatalf("token 统计应为 100+20=120，实际 %d", rep.Usage.TotalTokens)
	}
	if !strings.Contains(rep.Report, "10.20") {
		t.Fatalf("最终结论应包含模型输出，实际: %q", rep.Report)
	}
	if rep.SessionID == "" {
		t.Fatal("应生成会话 ID")
	}

	reqs := llm.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("模型应被调用 2 次，实际 %d", len(reqs))
	}
	// 第一次请求：带工具清单（渐进式披露的三个元工具）
	if len(reqs[0].Tools) != 3 {
		t.Fatalf("第一次请求应带 3 个元工具，实际 %d", len(reqs[0].Tools))
	}
	// 第二次请求：历史里必须有 tool 角色消息，且内容是工具输出（结果回填）
	var toolMsg *contract.Message
	var assistantWithCall *contract.Message
	for i := range reqs[1].Messages {
		m := reqs[1].Messages[i]
		if m.Role == contract.RoleTool {
			toolMsg = &m
		}
		if m.Role == contract.RoleAssistant && len(m.ToolCalls) > 0 {
			assistantWithCall = &m
		}
	}
	if toolMsg == nil {
		t.Fatalf("第二次请求缺少 tool 结果消息，历史: %s", dumpRoles(reqs[1].Messages))
	}
	if !strings.Contains(toolMsg.Content, "10.2") {
		t.Fatalf("tool 消息应包含工具输出（价格 10.2），实际: %.200s", toolMsg.Content)
	}
	if assistantWithCall == nil {
		t.Fatal("第二次请求应保留 assistant 的工具调用消息（否则模型会报 tool message without preceding tool_calls）")
	}
	if toolMsg.ToolCallID == "" {
		t.Fatal("tool 消息必须带 ToolCallID，否则无法与调用配对")
	}

	// 审计：kernel 记工具调用，网关记 gateway 事件，会话状态可回放
	if e := h.kernel.audit.lastOf(t, "tool", "market.quote"); !e.OK {
		t.Fatalf("工具调用应落成功审计: %+v", e)
	}
	if e := h.kernel.audit.lastOf(t, "gateway", "market.quote"); !e.OK {
		t.Fatalf("网关应落成功审计: %+v", e)
	}
	detail, err := h.runtime.Session(rep.SessionID)
	if err != nil {
		t.Fatalf("会话应可回放: %v", err)
	}
	if detail.Session.Steps != 2 || detail.Session.Tokens != 120 {
		t.Fatalf("会话元信息应记录步数与 token: steps=%d tokens=%d", detail.Session.Steps, detail.Session.Tokens)
	}
	if len(detail.Events) < 2 {
		t.Fatalf("会话事件应至少包含运行与报告，实际 %d 条", len(detail.Events))
	}
}

// 模型一直要调工具时，必须被步数上限拦住（不能无限循环）。
func TestAgentLoopStopsAtStepLimit(t *testing.T) {
	llm := newFakeLLM(toolCallReply(10, "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`))
	llm.repeatLast = true
	h := newAgentHarness(t, llm, contract.Policy{MaxToolCalls: 3, MaxTokensPerRun: 100000})

	rep, err := h.runtime.Run(context.Background(), "chat", "一直查行情", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	reqs := llm.snapshot()
	// 3 轮 ReAct + 1 次不带工具的收口调用（步数耗尽后仍要产出报告）。
	if len(reqs) != 4 {
		t.Fatalf("步数上限 3 时应有 3 轮 ReAct + 1 次收口调用，实际 %d 次（说明循环没被拦住或没做收口）", len(reqs))
	}
	if len(reqs[3].Tools) != 0 {
		t.Fatalf("收口调用不能带工具清单，实际 %d 个", len(reqs[3].Tools))
	}
	if rep.Steps != 3 {
		t.Fatalf("报告步数应为 3（收口调用不计步），实际 %d", rep.Steps)
	}
	// 口径：MaxToolCalls 是"模型轮次上限"。到达上限的那一轮虽然模型仍请求了工具，
	// 但循环已经在 model 节点后判定 exhausted，不再执行该请求（避免越界花钱）。
	// 因此 3 轮模型调用 = 2 次真实工具执行，这个边界必须在测试里钉死，避免以后被改坏。
	if got := h.kernel.audit.countIf(func(e contract.AuditEvent) bool {
		return e.Kind == "tool" && e.Action == "market.quote"
	}); got != rep.Steps-1 {
		t.Fatalf("上限轮次的工具请求不应执行：期望 %d 次执行，实际 %d", rep.Steps-1, got)
	}
	// 被拦下时必须留下可见原因，而不是静默截断
	if !strings.Contains(rep.Report, "最大步数") && !strings.Contains(rep.Report, "已停机") {
		t.Fatalf("达到步数上限应在结论里说明原因，实际: %q", rep.Report)
	}
}

// token 预算超了也要停，并且原因可读。
func TestAgentLoopStopsAtTokenBudget(t *testing.T) {
	llm := newFakeLLM(
		toolCallReply(40, "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		toolCallReply(40, "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		textReply("## 结论\n预算耗尽，量比按量/额口径看。\n## 依据\n- market.quote 返回 12.34\n## 不确定性\n- 只取到一次快照\n## 风险提示\n- 仅作研究参考", 20),
	)
	h := newAgentHarness(t, llm, contract.Policy{MaxToolCalls: 20, MaxTokensPerRun: 50})

	rep, err := h.runtime.Run(context.Background(), "chat", "解释一下量比口径", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	reqs := llm.snapshot()
	// token 预算 50、每次 40：第 2 次调用后停机，再补一次不带工具的收口调用。
	if len(reqs) != 3 {
		t.Fatalf("token 预算 50、每次 40 时应有 2 次 ReAct + 1 次收口调用，实际调用 %d 次", len(reqs))
	}
	if len(reqs[2].Tools) != 0 {
		t.Fatalf("收口调用不能带工具清单，实际 %d 个", len(reqs[2].Tools))
	}
	lastText := ""
	for _, m := range reqs[2].Messages {
		lastText += m.Content
	}
	if !strings.Contains(lastText, "market.quote") {
		t.Fatalf("收口调用应带上已获得的工具结果，实际: %.600s", lastText)
	}
	if rep.Usage.TotalTokens != 100 {
		t.Fatalf("已用 token 应记录为 40+40+20=100，实际 %d", rep.Usage.TotalTokens)
	}
	if !strings.Contains(rep.Report, "## 结论") || !strings.Contains(rep.Report, "量比") {
		t.Fatalf("预算耗尽后报告正文应包含收口结论，实际: %q", rep.Report)
	}
	if !strings.Contains(rep.Report, "token") && !strings.Contains(rep.Report, "预算") {
		t.Fatalf("超出 token 预算应在结论里说明，实际: %q", rep.Report)
	}
}

// 模型不按约定、直接猜业务工具名（market.quote）时：
// 不能整轮失败，必须回一条"改用 tool.call"的可操作提示，并把上下文交回模型继续。
func TestAgentLoopUnknownToolNameDoesNotBreakLoop(t *testing.T) {
	llm := newFakeLLM(
		// 故意跳过 tool.call，直接猜业务工具名
		toolCallReply(30, "market.quote", `{"codes":["600000"]}`),
		textReply("按提示改用 tool.call 后拿到了行情。", 10),
	)
	h := newAgentHarness(t, llm, contract.Policy{MaxToolCalls: 8, MaxTokensPerRun: 100000})

	rep, err := h.runtime.Run(context.Background(), "chat", "直接猜工具名的场景", "")
	if err != nil {
		t.Fatalf("猜错工具名不应让整轮失败: %v", err)
	}
	if rep.Error != "" {
		t.Fatalf("猜错工具名不应写进 Report.Error: %s", rep.Error)
	}
	reqs := llm.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("应继续跑到收尾（2 次模型调用），实际 %d 次", len(reqs))
	}
	var hint string
	for _, m := range reqs[1].Messages {
		if m.Role == contract.RoleTool {
			hint = m.Content
		}
	}
	if hint == "" {
		t.Fatalf("应回填一条 tool 消息说明怎么改，历史: %s", dumpRoles(reqs[1].Messages))
	}
	if !strings.Contains(hint, "tool.call") {
		t.Fatalf("提示应引导模型改用 tool.call，实际: %.200s", hint)
	}
	// 猜错的业务工具绝不能被执行
	if got := h.kernel.audit.countIf(func(e contract.AuditEvent) bool {
		return e.Kind == "tool" && e.Action == "market.quote"
	}); got != 0 {
		t.Fatalf("未通过 tool.call 的业务工具不应被执行，实际执行 %d 次", got)
	}
}

// 未配置模型时给出可操作的中文错误，而不是 panic 或空跑。
func TestAgentLoopWithoutLLM(t *testing.T) {
	h := newAgentHarness(t, newFakeLLM(), contract.Policy{})
	h.runtime = agent.NewRuntime(agent.RuntimeOptions{
		LLM:     nil,
		Guard:   h.kernel.guard,
		Audit:   h.kernel.audit,
		Events:  plugin.NewBus(),
		Policy:  contract.Policy{MaxToolCalls: 4, MaxTokensPerRun: 1000},
		Store:   h.kernel.store,
		Gateway: progressive.NewGateway(h.kernel.k, h.kernel.guard, h.kernel.audit, plugin.RiskNetwork),
		Clock:   time.Now,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rep, err := h.runtime.Run(context.Background(), "chat", "随便问问", "")
	if err == nil && rep.Error == "" {
		t.Fatalf("没有模型时应给出错误（run error 或 Report.Error），实际 rep=%+v err=%v", rep, err)
	}
}

func dumpRoles(msgs []contract.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(string(m.Role))
		if len(m.ToolCalls) > 0 {
			sb.WriteString("(toolcalls=" + m.ToolCalls[0].Name + ")")
		}
		sb.WriteString(" ")
	}
	return sb.String()
}
