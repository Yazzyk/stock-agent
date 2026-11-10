package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// fakeLLM 按脚本回放模型回复，用来驱动 ReAct 循环。
type fakeLLM struct {
	mu      sync.Mutex
	script  []contract.Message
	calls   int
	entered int
	seen    [][]contract.Message
	// block 不为 nil 时 Chat/Stream 会阻塞到 ctx 结束，用来测超时与 Stop。
	block chan struct{}
	// blockFirst 只阻塞第 1 次调用，用来测"运行中插话"。
	blockFirst chan struct{}
	// afterCall 在每次 Chat 结束时回调（参数是第几次调用），用来在同一步之后插话。
	afterCall func(n int)
	// usage 不为 nil 时按它返回 token 用量，用来测 usage 兜底口径。
	usage *contract.Usage
}

func (f *fakeLLM) next() (contract.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.script) == 0 {
		return contract.Message{Role: contract.RoleAssistant, Content: "（脚本已空）"}, nil
	}
	msg := f.script[0]
	f.script = f.script[1:]
	return msg, nil
}

func (f *fakeLLM) record(req contract.ChatRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, append([]contract.Message{}, req.Messages...))
}

func (f *fakeLLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	f.mu.Lock()
	f.entered++
	n := f.entered
	f.mu.Unlock()
	f.record(req)
	if f.blockFirst != nil && n == 1 {
		select {
		case <-ctx.Done():
			return contract.Reply{}, ctx.Err()
		case <-f.blockFirst:
		}
	}
	if f.block != nil {
		select {
		case <-ctx.Done():
			return contract.Reply{}, ctx.Err()
		case <-f.block:
		}
	}
	if f.afterCall != nil {
		f.afterCall(n)
	}
	msg, err := f.next()
	if err != nil {
		return contract.Reply{}, err
	}
	usage := contract.Usage{TotalTokens: 100}
	if f.usage != nil {
		usage = *f.usage
	}
	return contract.Reply{Message: msg, Usage: usage, Model: "fake"}, nil
}

func (f *fakeLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	reply, err := f.Chat(ctx, req)
	if err != nil {
		return contract.Reply{}, err
	}
	if strings.TrimSpace(reply.Message.Content) != "" && onDelta != nil {
		onDelta(reply.Message.Content)
	}
	return reply, nil
}

func (f *fakeLLM) Models(context.Context) ([]contract.ModelInfo, error) {
	return []contract.ModelInfo{{Provider: "fake", Model: "fake", HasKey: true, IsDefault: true}}, nil
}

func (f *fakeLLM) Default(context.Context) (contract.ModelInfo, error) {
	return contract.ModelInfo{Provider: "fake", Model: "fake", HasKey: true, IsDefault: true}, nil
}

func (f *fakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLLM) enteredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entered
}

// fakeHost 提供跨插件工具（模拟 kernel 的 ToolHost）。
type fakeHost struct {
	tools []plugin.Tool
	calls []string
	mu    sync.Mutex
}

func (h *fakeHost) Tools() []plugin.Tool { return h.tools }

func (h *fakeHost) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	h.mu.Lock()
	h.calls = append(h.calls, name)
	h.mu.Unlock()
	for _, t := range h.tools {
		if t.Name != name {
			continue
		}
		out, err := t.Run(ctx, args)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("工具未注册：%s", name)
}

func (h *fakeHost) callNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.calls...)
}

func testHost() *fakeHost {
	return &fakeHost{tools: []plugin.Tool{
		{
			Name:    "market.quote",
			Title:   "实时快照",
			Summary: "批量个股实时行情快照",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema:  json.RawMessage(`{"type":"object","properties":{"codes":{"type":"array","items":{"type":"string"},"description":"股票代码"}},"required":["codes"]}`),
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Codes []string `json:"codes"`
				}
				_ = json.Unmarshal(args, &in)
				return []map[string]any{{"code": in.Codes[0], "price": 12.34, "source": "fake"}}, nil
			},
		},
		{
			Name:    "kline.analyze",
			Title:   "K线分析",
			Summary: "K线趋势结构分析",
			Tags:    []string{"kline"},
			Risk:    plugin.RiskRead,
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]any{"trend": "多头"}, nil
			},
		},
	}}
}

func asstToolCall(id, name, args string) contract.Message {
	return contract.Message{
		Role:      contract.RoleAssistant,
		ToolCalls: []contract.ToolCall{{ID: id, Name: name, Arguments: args}},
	}
}

func TestReactLoopFollowsDisclosureProtocol(t *testing.T) {
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.search", `{"query":"quote"}`),
		asstToolCall("c2", "tool.describe", `{"name":"market.quote"}`),
		asstToolCall("c3", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		{Role: contract.RoleAssistant, Content: "结论：600000 现价 12.34（来源：market.quote）"},
	}}
	run, err := buildReact(context.Background(), reactConfig{
		LLM:      llm,
		Gateway:  gw,
		System:   "测试用 system prompt",
		MaxSteps: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Run(context.Background(), "看一下 600000")
	if err != nil {
		t.Fatalf("ReAct 循环不应失败: %v", err)
	}
	if res.Steps != 4 {
		t.Fatalf("应走 model→tools→model→tools→model→tools→model 共 4 次模型调用，实际 %d", res.Steps)
	}
	if res.ToolCalls != 3 {
		t.Fatalf("应有 3 次工具调用，实际 %d", res.ToolCalls)
	}
	if !strings.Contains(res.Content, "结论") || !strings.Contains(res.Content, "12.34") {
		t.Fatalf("最终结论应包含模型输出，实际: %q", res.Content)
	}
	// 三级披露：search → describe → call 都必须真的执行到宿主。
	names := host.callNames()
	if len(names) != 1 || names[0] != "market.quote" {
		t.Fatalf("tool.call 必须经宿主执行 market.quote，实际: %v", names)
	}
	var searched, described, called bool
	for _, m := range res.Transcript {
		if m.Role != "tool" {
			continue
		}
		switch m.ToolName {
		case "tool.search":
			searched = strings.Contains(m.Content, "market.quote")
		case "tool.describe":
			described = strings.Contains(m.Content, "properties") || strings.Contains(m.Content, "codes")
		case "tool.call":
			called = strings.Contains(m.Content, "600000")
		}
	}
	if !searched || !described || !called {
		t.Fatalf("L1/L2/L3 三级结果都必须回到模型：search=%v describe=%v call=%v", searched, described, called)
	}
	if llm.callCount() != 4 {
		t.Fatalf("模型应被调用 4 次，实际 %d", llm.callCount())
	}
}

func TestReactLoopToolErrorFedBackToModel(t *testing.T) {
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.unknown","args":{}}`),
		{Role: contract.RoleAssistant, Content: "工具名不存在，我改用 tool.search 重新检索后回答。"},
	}}
	run, err := buildReact(context.Background(), reactConfig{LLM: llm, Gateway: gw, MaxSteps: 6})
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Run(context.Background(), "试一个不存在的工具")
	if err != nil {
		t.Fatalf("单个工具失败不应中断整轮循环: %v", err)
	}
	if len(host.callNames()) != 0 {
		t.Fatalf("未注册工具不应被宿主执行，实际: %v", host.callNames())
	}
	var errText string
	for _, m := range res.Transcript {
		if m.Role == "tool" && m.ToolName == "tool.call" {
			errText = m.Content
		}
	}
	if !strings.Contains(errText, "未注册") || !strings.Contains(errText, "tool.search") {
		t.Fatalf("工具错误必须作为可自纠的提示回给模型，实际: %q", errText)
	}
	if !strings.Contains(res.Content, "tool.search") {
		t.Fatalf("模型应基于错误提示继续，实际: %q", res.Content)
	}
}

func TestReactLoopCorrectsDirectBusinessToolCall(t *testing.T) {
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "market.quote", `{"codes":["600000"]}`), // 模型猜了业务工具名
		{Role: contract.RoleAssistant, Content: "应该用 tool.call 调 market.quote。"},
	}}
	run, err := buildReact(context.Background(), reactConfig{LLM: llm, Gateway: gw, MaxSteps: 6})
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Run(context.Background(), "直接调业务工具")
	if err != nil {
		t.Fatalf("猜错工具名不应中断整轮循环: %v", err)
	}
	if len(host.callNames()) != 0 {
		t.Fatalf("猜的业务工具不应被执行，实际: %v", host.callNames())
	}
	var hint string
	for _, m := range res.Transcript {
		if m.Role == "tool" && m.ToolName == "market.quote" {
			hint = m.Content
		}
	}
	if !strings.Contains(hint, "tool.call") || !strings.Contains(hint, "tool.search") {
		t.Fatalf("应回给模型可操作的纠正提示，实际: %q", hint)
	}
	if !strings.Contains(res.Content, "tool.call") {
		t.Fatalf("模型应据此纠正，实际: %q", res.Content)
	}
}

func TestReactLoopStopsAtStepBudget(t *testing.T) {
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &fakeLLM{}
	for i := 0; i < 10; i++ {
		llm.script = append(llm.script, asstToolCall(fmt.Sprintf("c%d", i), "tool.search", `{"query":"quote"}`))
	}
	run, err := buildReact(context.Background(), reactConfig{LLM: llm, Gateway: gw, MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Run(context.Background(), "无限循环测试")
	if err != nil {
		t.Fatalf("达到步数上限应优雅收尾而不是报错: %v", err)
	}
	if !res.Exhausted {
		t.Fatal("应标记 Exhausted")
	}
	if res.Steps > 2 {
		t.Fatalf("步数不能超过上限 2，实际 %d", res.Steps)
	}
	if !strings.Contains(res.Note, "步数") {
		t.Fatalf("停机原因应说明步数上限，实际 %q", res.Note)
	}
}

func TestRouterFastPathAndDedupe(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 11, 10, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }
	r := NewRouter(clock, nil)

	cases := []struct {
		in   RouteInput
		want WorkerID
	}{
		{RouteInput{Worker: "premarket"}, WorkerPremarket},
		{RouteInput{Worker: "plan"}, WorkerPremarket},
		{RouteInput{Worker: "review"}, WorkerPostmarket},
		{RouteInput{Worker: "unknown-thing"}, WorkerChat},
		{RouteInput{Input: "帮我复盘一下今天的涨停梯队"}, WorkerPostmarket},
		{RouteInput{Input: "回测一下首板次日溢价"}, WorkerQuant},
		{RouteInput{Input: "盯盘提醒我异动"}, WorkerIntraday},
		{RouteInput{Input: "什么是集合竞价"}, WorkerChat},
	}
	for _, c := range cases {
		d, err := r.Plan(context.Background(), c.in)
		if err != nil {
			t.Fatalf("Plan(%+v) 失败: %v", c.in, err)
		}
		if d.Worker != c.want {
			t.Fatalf("Plan(%+v): worker=%s, want %s", c.in, d.Worker, c.want)
		}
		if !d.Fast {
			t.Fatalf("无模型时应走规则快路径: %+v", c.in)
		}
	}

	// 去重：同一 worker+输入在窗口内第二次触发被抑制。
	first, err := r.Plan(context.Background(), RouteInput{Worker: "chat", Input: "什么是涨停"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Deduped {
		t.Fatal("首次触发不应被去重")
	}
	second, _ := r.Plan(context.Background(), RouteInput{Worker: "chat", Input: "  什么是涨停  "})
	if !second.Deduped {
		t.Fatal("窗口内相同请求必须去重")
	}

	// 限频：1 分钟内超过 MaxRunsPerMin 报错（用全新 router，避免被上面的用例预占配额）。
	limited := NewRouter(clock, nil)
	limited.MaxRunsPerMin = 2
	limited.DedupeWindow = 0
	if _, err := limited.Plan(context.Background(), RouteInput{Worker: "chat", Input: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Plan(context.Background(), RouteInput{Worker: "chat", Input: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Plan(context.Background(), RouteInput{Worker: "chat", Input: "c"}); err == nil {
		t.Fatal("超过每分钟上限必须报错")
	}
}

func TestRouterSlowPathUsesModel(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: `{"worker":"quant","intent":"因子研究"}`},
	}}
	r := NewRouter(time.Now, llm)
	d, err := r.Plan(context.Background(), RouteInput{Input: "帮我看看这个思路能不能赚钱"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Worker != WorkerQuant || d.Intent != "因子研究" {
		t.Fatalf("慢路径应使用模型判意图，实际: %+v", d)
	}
	if d.Fast {
		t.Fatal("慢路径不应标记 Fast")
	}

	// 模型返回垃圾时回落到规则/聊天。
	bad := &fakeLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: "不是 JSON"}}}
	r2 := NewRouter(time.Now, bad)
	d2, err := r2.Plan(context.Background(), RouteInput{Input: "随便聊聊"})
	if err != nil {
		t.Fatal(err)
	}
	if d2.Worker != WorkerChat || !d2.Fast {
		t.Fatalf("模型失败应回落到规则，实际: %+v", d2)
	}
}

func TestTradingPhase(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 10, 8, 30, 0, 0, loc), "premarket"},
		{time.Date(2026, 11, 10, 9, 15, 0, 0, loc), "intraday"},
		{time.Date(2026, 11, 10, 11, 30, 0, 0, loc), "intraday"},
		{time.Date(2026, 11, 10, 12, 0, 0, 0, loc), "lunch"},
		{time.Date(2026, 11, 10, 14, 30, 0, 0, loc), "intraday"},
		{time.Date(2026, 11, 10, 15, 30, 0, 0, loc), "postmarket"},
		{time.Date(2026, 11, 14, 10, 0, 0, 0, loc), "closed"}, // 周六
	}
	for _, c := range cases {
		if got := TradingPhase(c.at); got != c.want {
			t.Fatalf("TradingPhase(%s)=%s, want %s", c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestWorkerPromptsTeachDisclosure(t *testing.T) {
	if len(Workers()) != 5 {
		t.Fatalf("应有 5 个 worker，实际 %d", len(Workers()))
	}
	for _, w := range Workers() {
		for _, keyword := range []string{"tool.search", "tool.describe", "tool.call", "skill.list", "kb.search"} {
			if !strings.Contains(w.System, keyword) {
				t.Fatalf("%s 的 system prompt 必须教三级披露（缺 %s）", w.ID, keyword)
			}
		}
		if !strings.Contains(w.System, "禁止猜测工具名") {
			t.Fatalf("%s 必须明确禁止猜工具名", w.ID)
		}
	}
	if _, ok := LookupWorker("review"); !ok {
		t.Fatal("别名 review 应能解析到 postmarket")
	}
	// 未知 worker 回落到 chat，而不是报错卡住前端。
	if spec, ok := LookupWorker("不存在"); !ok || spec.ID != WorkerChat {
		t.Fatalf("未知 worker 应回落到 chat，实际: %+v ok=%v", spec, ok)
	}
}
