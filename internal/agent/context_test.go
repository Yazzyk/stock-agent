package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"空", "", 0},
		{"纯中文", "中文四个字", 5},
		{"纯中文标点", "，。；：", 4},
		{"纯英文整词", "abcdefgh", 2},
		{"英文不足四字符向上取整", "abc", 1},
		{"中英混排", "中文 abcd", 4},
		{"带换行英文", "a\nb\nc\nd", 2},
		{"中英混排长文本", "结论：现价 12.34 元（来源：market.quote）", 16},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EstimateTokens(c.text); got != c.want {
				t.Fatalf("EstimateTokens(%q)=%d, want %d", c.text, got, c.want)
			}
		})
	}
	// 中文按 1 token/字，所以中文字数与 token 至少是线性相关（不允许把中文除以 4）。
	zh := EstimateTokens(strings.Repeat("研", 100))
	if zh < 100 {
		t.Fatalf("中文必须按 1 字 1 token 估算，100 字至少 100 token，实际 %d", zh)
	}
}

func TestPercentOf(t *testing.T) {
	cases := []struct{ used, window, want int }{
		{0, 1000, 0},
		{100, 1000, 10},
		{999, 1000, 99},
		{2000, 1000, 100}, // 上限 100
		{500, 0, 0},       // 窗口未知不显示百分比
	}
	for _, c := range cases {
		if got := percentOf(c.used, c.window); got != c.want {
			t.Fatalf("percentOf(%d,%d)=%d, want %d", c.used, c.window, got, c.want)
		}
	}
}

// TestContextSnapshotDuringRun 验证运行中每步发 agent.context，且分项求和等于 usedTokens。
func TestContextSnapshotDuringRun(t *testing.T) {
	llm := &systemLLM{window: 1000, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "结论：情绪回暖（来源：kb.search）"},
	}}
	rt, _, _, sink := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), "postmarket", "复盘今天", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	waitForTopic(t, sink, "agent.context")

	events := sink.byTopic("agent.context")
	if len(events) == 0 {
		t.Fatal("每步模型调用前都应发 agent.context 事件")
	}
	var snap AgentContext
	if err := json.Unmarshal(events[len(events)-1].Payload, &snap); err != nil {
		t.Fatalf("agent.context payload 必须是 JSON: %v", err)
	}
	if snap.SessionID != report.SessionID {
		t.Fatalf("context 快照应绑定当前会话，实际 %q", snap.SessionID)
	}
	sum := snap.Breakdown.SystemTokens + snap.Breakdown.ToolsTokens + snap.Breakdown.MessageTokens
	if sum != snap.UsedTokens || sum == 0 {
		t.Fatalf("分项之和必须等于 usedTokens 且 >0：%+v", snap)
	}
	if snap.Breakdown.SystemTokens == 0 {
		t.Fatal("system 分项必须 >0（模板 prompt + 披露协议）")
	}
	if snap.ContextWindow != 1000 || snap.Percent != snap.UsedTokens*100/1000 {
		t.Fatalf("窗口/百分比不对：%+v", snap)
	}
	if snap.Messages == 0 || snap.Steps == 0 {
		t.Fatalf("消息条数/步数应记录：%+v", snap)
	}
	if !strings.Contains(snap.Note, "口径") {
		t.Fatalf("必须带估算口径说明，实际 %q", snap.Note)
	}
}

// TestContextRebuildAfterRestart 验证 context 方法能由会话事件重建（进程重启后仍可回读）。
func TestContextRebuildAfterRestart(t *testing.T) {
	llm := &systemLLM{window: 2000, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "第一步结论"},
	}}
	rt, store, _, _ := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), "premarket", "", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	// 新的 Runtime（内存快照为空）只能靠会话事件 + 当前模板重建。
	rt2 := NewRuntime(RuntimeOptions{LLM: llm, Store: store, Clock: time.Now})
	snap, err := rt2.Context(context.Background(), report.SessionID)
	if err != nil {
		t.Fatalf("重建 context 失败: %v", err)
	}
	if snap.UsedTokens == 0 || snap.Breakdown.MessageTokens == 0 {
		t.Fatalf("重建后应估算出 system 与 messages：%+v", snap)
	}
	if snap.Steps != report.Steps {
		t.Fatalf("步数应取会话元信息（%d），实际 %d", report.Steps, snap.Steps)
	}
	if snap.Model != "fake" || snap.ContextWindow != 2000 {
		t.Fatalf("模型/窗口应来自当前默认模型：%+v", snap)
	}
	if snap.Percent != snap.UsedTokens*100/2000 {
		t.Fatalf("百分比应按窗口折算：%+v", snap)
	}
	// 模板换掉后重建的 system 分项应跟着变。
	if _, err := rt2.Templates().Save(WorkerPremarket, customPrompt+strings.Repeat("补", 200), nil); err != nil {
		t.Fatal(err)
	}
	rt3 := NewRuntime(RuntimeOptions{LLM: llm, Store: store, Clock: time.Now})
	bigger, err := rt3.Context(context.Background(), report.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if bigger.Breakdown.SystemTokens <= snap.Breakdown.SystemTokens {
		t.Fatalf("模板变长后 system 分项应变大：%d → %d", snap.Breakdown.SystemTokens, bigger.Breakdown.SystemTokens)
	}
}

// TestContextWithoutWindow 验证窗口未知时只给已用 token，不编假百分比。
func TestContextWithoutWindow(t *testing.T) {
	llm := &systemLLM{window: 0, script: []contract.Message{{Role: contract.RoleAssistant, Content: "结论"}}}
	rt, _, _, _ := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), "chat", "什么是集合竞价", "")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := rt.Context(context.Background(), report.SessionID)
	if err != nil {
		t.Fatalf("context 失败: %v", err)
	}
	if snap.ContextWindow != 0 || snap.Percent != 0 {
		t.Fatalf("窗口未知时不能编造百分比：%+v", snap)
	}
	if snap.UsedTokens == 0 {
		t.Fatal("窗口未知也必须给出已用 token")
	}

	if _, err := rt.Context(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "sessionId") {
		t.Fatalf("空 sessionId 应报错，实际: %v", err)
	}
	if _, err := rt.Context(context.Background(), "不存在的会话"); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}

// TestToolIndexTokensOnlyCountsL1 验证 tools 分项只算 L1 索引（名称+摘要+标签），不展开 schema。
func TestToolIndexTokensOnlyCountsL1(t *testing.T) {
	rt, _, _, _ := newTestRuntime(t, &systemLLM{})
	tokens := rt.toolIndexTokens()
	if tokens <= 0 {
		t.Fatalf("有工具时 L1 索引估算应 >0，实际 %d", tokens)
	}
	// 索引只含名称/摘要/标签：对测试工具集来说远小于把 schema 全算进去的量级。
	entries := rt.opts.Gateway.ToolIndex()
	for _, e := range entries {
		if e.Name == "" || e.Summary == "" {
			t.Fatalf("L1 索引项必须含名称与摘要：%+v", e)
		}
	}
	// 口径固定：只拼"名称 + 标签 + 摘要"，一行一个工具。
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name)
		if len(e.Tags) > 0 {
			b.WriteString(" " + strings.Join(e.Tags, ","))
		}
		b.WriteString(" " + e.Summary + "\n")
	}
	if want := EstimateTokens(b.String()); tokens != want {
		t.Fatalf("tools 分项必须只按 L1 文本估算：got %d want %d", tokens, want)
	}
	// 把 schema 全算进去会明显更大——分项不能被 schema 撑大（渐进披露）。
	full := tokens
	for _, tool := range rt.opts.Gateway.Host.Tools() {
		if len(tool.Schema) > 0 {
			full += EstimateTokens(string(tool.Schema))
		}
	}
	if full == tokens {
		t.Fatal("测试工具集应含带 schema 的工具，才能验证 schema 不计入 tools 分项")
	}
	if tokens >= full {
		t.Fatalf("tools 分项不能包含 schema：L1=%d 全量=%d", tokens, full)
	}
}

// TestContextProviderUsageAndHistory 验证一次真实运行后的上下文快照：
// 提供方占用（pressureTokens）、来源标记、缓存命中、会话累计用量与逐轮历史。
func TestContextProviderUsageAndHistory(t *testing.T) {
	llm := &fakeLLM{
		window: 8000,
		script: []contract.Message{
			asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
			{Role: contract.RoleAssistant, Content: "结论：600000 现价 12.34（来源：market.quote）"},
		},
		usage: &contract.Usage{PromptTokens: 400, CachedTokens: 250, CompletionTokens: 80, TotalTokens: 480},
	}
	rt, _, _, sink := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), "postmarket", "复盘今天", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if report.Steps != 2 {
		t.Fatalf("应有 2 次模型调用，实际 %d", report.Steps)
	}
	snap := waitContextSnapshot(t, sink, 2)
	if snap.PressureTokens != 400 {
		t.Fatalf("pressureTokens 应取最近一次请求的提供方 prompt，实际 %+v", snap)
	}
	if snap.Sample != "provider" {
		t.Fatalf("有提供方样本时 sample 应为 provider，实际 %q", snap.Sample)
	}
	if snap.Cache.CacheReadTokens != 500 || snap.Cache.UncachedInputTokens != 300 || snap.Cache.OutputTokens != 160 {
		t.Fatalf("缓存/输出累计不符（2 次调用 × 250/400/80）：%+v", snap.Cache)
	}
	if h := snap.Cache.HitPercent; h < 0 || h > 100 || h != 62 {
		t.Fatalf("命中率应是 0..100 的整数（500/800=62），实际 %d", h)
	}
	if snap.Usage.PromptTokens != 800 || snap.Usage.CachedTokens != 500 ||
		snap.Usage.CompletionTokens != 160 || snap.Usage.TotalTokens != 960 {
		t.Fatalf("会话累计用量不符：%+v", snap.Usage)
	}
	if snap.Percent != 400*100/8000 {
		t.Fatalf("有 pressure 时百分比应按 pressure 折算：%+v", snap)
	}
	if len(snap.History) != 2 {
		t.Fatalf("两次模型调用应留下 2 条历史，实际 %d: %+v", len(snap.History), snap.History)
	}
	for i, p := range snap.History {
		if p.Step != i+1 {
			t.Fatalf("step 应从 1 起连续，第 %d 条是 %d", i, p.Step)
		}
		if p.PromptTokens != 400 || p.CachedTokens != 250 || p.Used != 400 || p.CompletionTokens != 80 {
			t.Fatalf("采样点不符：%+v", p)
		}
		if _, err := time.Parse(time.RFC3339, p.At); err != nil {
			t.Fatalf("采样时间必须是 RFC3339：%q (%v)", p.At, err)
		}
	}

	// Context 方法必须返回与事件一致的历史。
	via, err := rt.Context(context.Background(), report.SessionID)
	if err != nil {
		t.Fatalf("Context 失败: %v", err)
	}
	if !reflect.DeepEqual(via.History, snap.History) {
		t.Fatalf("Context 的 history 应与事件一致：\n事件 %+v\n方法 %+v", snap.History, via.History)
	}
	if via.PressureTokens != 400 || via.Sample != "provider" || via.Cache.CacheReadTokens != 500 {
		t.Fatalf("Context 的提供方字段应与事件一致：%+v", via)
	}
}

// TestContextWithoutProviderUsage 验证没有 usage 时：pressure=0、sample 非 provider、不 panic，
// 逐轮采样仍然给出估算的 used（前端会回退到启发式）。
func TestContextWithoutProviderUsage(t *testing.T) {
	llm := &fakeLLM{window: 4000, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "结论：情绪回暖"},
	}}
	rt, _, _, sink := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), "chat", "什么是集合竞价", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	snap := waitContextSnapshot(t, sink, 1)
	if snap.PressureTokens != 0 {
		t.Fatalf("没有提供方样本时 pressureTokens 必须是 0，实际 %d", snap.PressureTokens)
	}
	if snap.Sample != "" && snap.Sample != "estimated" {
		t.Fatalf("没有样本时 sample 只能为空或 estimated，实际 %q", snap.Sample)
	}
	if snap.Cache.CacheReadTokens != 0 || snap.Cache.HitPercent != 0 {
		t.Fatalf("没有缓存上报时不能编造命中：%+v", snap.Cache)
	}
	if len(snap.History) != 1 {
		t.Fatalf("一次模型调用应留下 1 条历史，实际 %d", len(snap.History))
	}
	if p := snap.History[0]; p.PromptTokens != 0 || p.Used <= 0 {
		t.Fatalf("没有提供方 prompt 时 used 要回落到估算值：%+v", p)
	}
	if snap.Percent != percentOf(snap.UsedTokens, 4000) {
		t.Fatalf("没有 pressure 时百分比应按估算 usedTokens 折算：%+v", snap)
	}
	if _, err := rt.Context(context.Background(), report.SessionID); err != nil {
		t.Fatalf("没有 usage 时 Context 不应报错: %v", err)
	}
}

// TestContextHistoryCapAndSessionIsolation 验证逐轮历史的 120 条上限（超出丢最旧、step 继续递增），
// 以及按 sessionID 分桶：别的会话不会混进累计值。
func TestContextHistoryCapAndSessionIsolation(t *testing.T) {
	rt, _, _, _ := newTestRuntime(t, &systemLLM{})
	const session = "cap-session"
	total := contextHistoryCap + 10
	wantPrompt, wantCached, wantTotal := 0, 0, 0
	for i := 0; i < total; i++ {
		usage := contract.Usage{PromptTokens: 100 + i, CachedTokens: 10, CompletionTokens: 2, TotalTokens: 112 + i}
		wantPrompt += usage.PromptTokens
		wantCached += usage.CachedTokens
		wantTotal += usage.TotalTokens
		rt.recordUsage(session, usage)
	}
	snap := rt.buildContext(session, "fake", 10000, "系统提示", 0, nil, total, 0)
	if len(snap.History) != contextHistoryCap {
		t.Fatalf("历史最多保留 %d 条，实际 %d", contextHistoryCap, len(snap.History))
	}
	first := snap.History[0]
	last := snap.History[len(snap.History)-1]
	if first.Step != total-contextHistoryCap+1 || last.Step != total {
		t.Fatalf("截断后应丢最旧的、step 继续递增：first=%d last=%d", first.Step, last.Step)
	}
	for i, p := range snap.History {
		if want := first.Step + i; p.Step != want {
			t.Fatalf("第 %d 条 step=%d，应为 %d（必须连续）", i, p.Step, want)
		}
	}
	if snap.PressureTokens != 100+total-1 {
		t.Fatalf("pressureTokens 应取最近一次请求，实际 %d", snap.PressureTokens)
	}
	if snap.Cache.CacheReadTokens != wantCached || snap.Usage.PromptTokens != wantPrompt || snap.Usage.TotalTokens != wantTotal {
		t.Fatalf("累计值应覆盖全部调用（含被截断的历史）：%+v", snap)
	}

	// 另一个会话从 0 开始，不继承任何累计值。
	other := rt.buildContext("other-session", "fake", 10000, "", 0, nil, 0, 0)
	if len(other.History) != 0 || other.PressureTokens != 0 || other.Sample != "estimated" {
		t.Fatalf("新会话不能混入上一个会话的样本：%+v", other)
	}
	if other.Cache != (ContextCache{}) || other.Usage != (ContextUsage{}) {
		t.Fatalf("新会话的累计值必须为 0：cache=%+v usage=%+v", other.Cache, other.Usage)
	}
}

// waitContextSnapshot 等待并返回 history 条数达到 want 的一帧 agent.context。
// 事件总线是异步广播（顺序不保证），所以按内容挑选而不是取"最后一条"。
func waitContextSnapshot(t *testing.T, sink *eventSink, want int) AgentContext {
	t.Helper()
	var (
		best  AgentContext
		found bool
	)
	ok := waitFor(t, 2*time.Second, func() bool {
		for _, e := range sink.byTopic("agent.context") {
			var snap AgentContext
			if err := json.Unmarshal(e.Payload, &snap); err != nil {
				continue
			}
			if len(snap.History) >= want && (!found || len(snap.History) > len(best.History)) {
				best, found = snap, true
			}
		}
		return found
	})
	if !ok {
		t.Fatalf("等待 agent.context 事件超时（需要 %d 条历史）", want)
	}
	return best
}
