package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// waitForTopic 等待异步事件广播落地（最多 2s），避免与 goroutine 调度抢跑。
func waitForTopic(t *testing.T, sink *eventSink, topic string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.byTopic(topic)) > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待事件广播超时：%s", topic)
}

// memStore 是测试用内存 Store。
type memStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return v, nil
		}
	}
	return nil, contract.ErrNotFound
}

func (s *memStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = append([]byte{}, val...)
	return nil
}

func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contract.KVItem, 0, len(s.data[bucket]))
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: append([]byte{}, v...)})
		}
	}
	return out, nil
}

func (s *memStore) Close() error { return nil }

// eventSink 收集 Bus 上的事件，便于断言 agent.delta / agent.state / agent.report。
type eventSink struct {
	mu     sync.Mutex
	events []plugin.Event
}

func (s *eventSink) attach(bus *plugin.Bus) {
	bus.Subscribe("*", func(e plugin.Event) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.events = append(s.events, e)
	})
}

func (s *eventSink) byTopic(topic string) []plugin.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []plugin.Event
	for _, e := range s.events {
		if e.Topic == topic {
			out = append(out, e)
		}
	}
	return out
}

// waitFor 轮询等待条件成立（异步事件/看护器需要）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func newTestRuntime(t *testing.T, llm contract.LLMService, opts ...func(*RuntimeOptions)) (*Runtime, *memStore, *progressive.KB, *eventSink) {
	t.Helper()
	store := newMemStore()
	kb := progressive.NewKB(store)
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	bus := plugin.NewBus()
	sink := &eventSink{}
	sink.attach(bus)
	rtOpts := RuntimeOptions{
		LLM:     llm,
		Events:  bus,
		Policy:  contract.Policy{MaxToolCalls: 6, MaxTokensPerRun: 10000},
		Store:   store,
		Gateway: gw,
		KB:      kb,
		Clock:   time.Now,
	}
	for _, o := range opts {
		o(&rtOpts)
	}
	return NewRuntime(rtOpts), store, kb, sink
}

func TestRuntimeRunPersistsSessionAndEmitsEvents(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "结论：情绪回暖，关注连板高度（来源：kb.search）"},
	}}
	rt, _, kb, sink := newTestRuntime(t, llm)

	report, err := rt.Run(context.Background(), "postmarket", "复盘今天", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if report.SessionID == "" || report.Worker != "postmarket" {
		t.Fatalf("报告缺少会话信息: %+v", report)
	}
	if !strings.Contains(report.Report, "情绪回暖") {
		t.Fatalf("报告内容不对: %q", report.Report)
	}
	if report.Steps != 1 || report.Usage.TotalTokens == 0 {
		t.Fatalf("步数/token 统计不对: %+v", report)
	}
	if len(report.Artifacts) == 0 {
		t.Fatal("运行结束应把结论回写知识库并登记 artifact")
	}

	sessions, err := rt.Sessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != report.SessionID || sessions[0].Status != StatusOK {
		t.Fatalf("会话列表不对: %+v", sessions)
	}

	detail, err := rt.Session(report.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Session.Status != StatusOK || detail.Session.Worker != "postmarket" {
		t.Fatalf("会话元信息不对: %+v", detail.Session)
	}
	if len(detail.Events) < 3 {
		t.Fatalf("事件应包含 user/assistant/report，实际 %d 条", len(detail.Events))
	}
	for i, e := range detail.Events {
		if e.Seq != i+1 {
			t.Fatalf("事件序号必须从 1 递增，实际第 %d 条 seq=%d", i, e.Seq)
		}
	}
	// 第一条事件必须是 system（完整 system prompt），第二条才是 user：
	// 轨迹要能审计"模型到底收到了什么口径"，而不是只有上下文里的 systemTokens 数字。
	if detail.Events[0].Kind != EventSystem {
		t.Fatalf("首条事件应为 system，实际 %s", detail.Events[0].Kind)
	}
	if detail.Events[0].Content == "" || !strings.Contains(detail.Events[0].Content, "三级披露协议") {
		t.Fatalf("system 事件必须落完整系统提示词，实际前 40 字: %q", truncateRunes(detail.Events[0].Content, 40))
	}
	if detail.Events[1].Kind != EventUser {
		t.Fatalf("第二条事件应为 user，实际 %s", detail.Events[1].Kind)
	}
	last := detail.Events[len(detail.Events)-1]
	if last.Kind != EventReport {
		t.Fatalf("末条事件应为 report，实际 %s", last.Kind)
	}

	// plugin.Bus 的 Publish 是异步广播（goroutine），Run 返回不代表订阅者已收完。
	// 这里显式等事件落地，避免测试与调度器抢跑造成偶发失败。
	waitForTopic(t, sink, "agent.state")
	waitForTopic(t, sink, "agent.report")
	waitForTopic(t, sink, "agent.delta")

	deltas := sink.byTopic("agent.delta")
	var payload map[string]string
	if err := json.Unmarshal(deltas[0].Payload, &payload); err != nil {
		t.Fatalf("agent.delta payload 必须是 JSON: %v", err)
	}
	if payload["sessionId"] != report.SessionID || payload["text"] == "" {
		t.Fatalf("agent.delta payload 不对: %+v", payload)
	}

	list, err := kb.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("结论应回写知识库 1 篇，实际 %d", list.Total)
	}
	tagged := false
	for _, tag := range list.Docs[0].Tags {
		if strings.HasPrefix(tag, "run/") && strings.HasSuffix(tag, "/postmarket") {
			tagged = true
		}
	}
	if !tagged {
		t.Fatalf("回写文档应带 run/<date>/<worker> 标签，实际 %+v", list.Docs[0].Tags)
	}
}

func TestRuntimeDedupeReturnsCachedReport(t *testing.T) {
	fixed := time.Date(2026, 11, 10, 9, 40, 0, 0, time.FixedZone("CST", 8*3600))
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "第一次结论"},
		{Role: contract.RoleAssistant, Content: "第二次结论（不应被调用）"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm, func(o *RuntimeOptions) {
		o.Clock = func() time.Time { return fixed }
	})

	first, err := rt.Run(context.Background(), "chat", "什么是涨停", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := rt.Run(context.Background(), "chat", "  什么是涨停 ", "")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Deduped {
		t.Fatal("去重窗口内重复触发应被抑制")
	}
	if second.SessionID != first.SessionID || second.Report != first.Report {
		t.Fatalf("重复触发应回放上次结论: %+v vs %+v", first, second)
	}
	if llm.callCount() != 1 {
		t.Fatalf("重复触发不应再次调用模型，实际调用 %d 次", llm.callCount())
	}
	sessions, _ := rt.Sessions(10)
	if len(sessions) != 1 {
		t.Fatalf("重复触发不应新建会话，实际 %d 个", len(sessions))
	}
}

func TestRuntimeStopCancelsRun(t *testing.T) {
	llm := &fakeLLM{block: make(chan struct{})}
	defer close(llm.block)
	rt, _, _, _ := newTestRuntime(t, llm)

	done := make(chan Report, 1)
	go func() {
		report, _ := rt.Run(context.Background(), "intraday", "盯盘", "sess-stop")
		done <- report
	}()
	if !waitFor(t, 2*time.Second, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		_, ok := rt.running["sess-stop"]
		return ok
	}) {
		t.Fatal("任务应进入运行中状态")
	}
	if !rt.Stop("sess-stop") {
		t.Fatal("Stop 应返回 true")
	}
	select {
	case report := <-done:
		if report.Error == "" {
			t.Fatalf("被取消的任务应记录错误: %+v", report)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后任务应及时退出")
	}
	if rt.Stop("sess-stop") {
		t.Fatal("已结束的任务 Stop 应返回 false")
	}
}

func TestRuntimeWatchdogRetriesOnce(t *testing.T) {
	llm := &fakeLLM{block: make(chan struct{})}
	defer close(llm.block)
	rt, _, _, sink := newTestRuntime(t, llm)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.StartWatchdog(ctx, 10*time.Millisecond, 25*time.Millisecond)
	defer rt.StopWatchdog()

	go func() {
		_, _ = rt.Run(context.Background(), "intraday", "盯盘超时测试", "")
	}()

	if !waitFor(t, 3*time.Second, func() bool { return llm.enteredCount() >= 2 }) {
		t.Fatalf("超时后应自动重试一次（模型调用应 ≥2），实际 %d", llm.enteredCount())
	}
	timeoutReported := false
	for _, e := range sink.byTopic("agent.state") {
		if strings.Contains(string(e.Payload), "超时") {
			timeoutReported = true
		}
	}
	if !timeoutReported {
		t.Fatal("超时应经 agent.state 广播给前端")
	}

	// 收拾还在跑的句柄，避免测试泄漏 goroutine。
	rt.mu.Lock()
	ids := make([]string, 0, len(rt.running))
	for id := range rt.running {
		ids = append(ids, id)
	}
	rt.mu.Unlock()
	for _, id := range ids {
		rt.Stop(id)
	}
}

func TestRuntimeUsageFallbacks(t *testing.T) {
	answer := contract.Message{Role: contract.RoleAssistant, Content: "结论：只用总量口径"}

	// ① 上游只给 total：分项为 0，但 UI 必须能拿到 total，并知道分项不可得。
	onlyTotal := contract.Usage{TotalTokens: 17574}
	llmA := &fakeLLM{usage: &onlyTotal, script: []contract.Message{answer}}
	rtA, _, _, _ := newTestRuntime(t, llmA)
	reportA, err := rtA.Run(context.Background(), "chat", "只给 total 的情况", "")
	if err != nil {
		t.Fatal(err)
	}
	if reportA.Usage.TotalTokens != 17574 || reportA.Usage.PromptTokens != 0 || reportA.Usage.CompletionTokens != 0 {
		t.Fatalf("total-only usage 应原样保留 total，实际: %+v", reportA.Usage)
	}
	if reportA.UsageNote == "" {
		t.Fatal("分项不可得时必须给 UsageNote，前端才不会把 0 当成真实分项")
	}
	summaryA, err := rtA.Sessions(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaryA) != 1 || summaryA[0].Tokens != 17574 {
		t.Fatalf("会话列表的 tokens 必须是 total（不能显示 0），实际: %+v", summaryA)
	}

	// ② 上游只给分项：total 必须由分项求和补上，且不产生误导性 UsageNote。
	split := contract.Usage{PromptTokens: 120, CompletionTokens: 80}
	llmB := &fakeLLM{usage: &split, script: []contract.Message{answer}}
	rtB, _, _, _ := newTestRuntime(t, llmB)
	reportB, err := rtB.Run(context.Background(), "chat", "只给分项的情况", "")
	if err != nil {
		t.Fatal(err)
	}
	if reportB.Usage.PromptTokens != 120 || reportB.Usage.CompletionTokens != 80 || reportB.Usage.TotalTokens != 200 {
		t.Fatalf("分项齐全时 total 应求和为 200，实际: %+v", reportB.Usage)
	}
	if reportB.UsageNote != "" {
		t.Fatalf("分项齐全时不应有 UsageNote，实际: %q", reportB.UsageNote)
	}
}

// messagesText 把一次模型调用的上下文拼成可断言的大字符串。
func messagesText(msgs []contract.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, string(m.Role)+":"+m.Content)
	}
	return strings.Join(parts, " || ")
}

func TestSteerInjectedBeforeNextModelCall(t *testing.T) {
	gate := make(chan struct{})
	// 第 1 次调用先查一次工具，让 ReAct 循环必须进入第 2 次模型调用。
	llm := &fakeLLM{blockFirst: gate, script: []contract.Message{
		asstToolCall("c1", "tool.search", `{"query":"quote"}`),
		{Role: contract.RoleAssistant, Content: "已按新增约束修正：仓位上限三成"},
	}}
	rt, _, _, sink := newTestRuntime(t, llm)

	done := make(chan Report, 1)
	go func() {
		rep, _ := rt.Run(context.Background(), "chat", "分析今天怎么做", "sess-steer")
		done <- rep
	}()
	if !waitFor(t, 2*time.Second, func() bool { return llm.enteredCount() >= 1 }) {
		t.Fatal("模型第 1 次调用应已开始")
	}
	if err := rt.Steer("sess-steer", "把仓位上限压到三成"); err != nil {
		t.Fatalf("运行中插话应成功: %v", err)
	}
	if got := rt.SteerQueueLen("sess-steer"); got != 1 {
		t.Fatalf("插话应入队 1 条，实际 %d", got)
	}
	close(gate)

	var rep Report
	select {
	case rep = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("插话后运行应正常结束")
	}
	if rep.Steers != 1 {
		t.Fatalf("报告应记录 1 次插话，实际 %d", rep.Steers)
	}
	if len(llm.seen) < 2 {
		t.Fatalf("应有至少 2 次模型调用，实际 %d", len(llm.seen))
	}
	second := messagesText(llm.seen[1])
	if !strings.Contains(second, steerPrefix+"把仓位上限压到三成") {
		t.Fatalf("第 2 次模型调用必须带上用户插话，实际上下文: %s", second)
	}
	if !strings.Contains(second, "不要丢弃原任务") {
		t.Fatalf("插话消息必须包含「不要丢弃原任务」的说明，实际: %s", second)
	}
	if strings.Contains(messagesText(llm.seen[0]), steerPrefix) {
		t.Fatal("第 1 次调用发生在插话之前，不应包含插话")
	}

	// 事件：agent.steer 恰好一次，payload 固定 {sessionId,text,queued}
	// plugin.Bus.Publish 是异步广播，Run 返回不代表订阅者已收完，先等事件落地。
	waitForTopic(t, sink, "agent.steer")
	steers := sink.byTopic("agent.steer")
	if len(steers) != 1 {
		t.Fatalf("agent.steer 事件应恰好 1 条，实际 %d", len(steers))
	}
	var payload map[string]any
	if err := json.Unmarshal(steers[0].Payload, &payload); err != nil {
		t.Fatalf("agent.steer payload 必须是 JSON: %v", err)
	}
	if payload["sessionId"] != "sess-steer" || payload["text"] != "把仓位上限压到三成" {
		t.Fatalf("agent.steer payload 不对: %+v", payload)
	}
	if _, ok := payload["queued"]; !ok {
		t.Fatalf("agent.steer payload 必须带 queued: %+v", payload)
	}

	// 会话事件：重放能看到一条 kind=user 且以 [用户插话] 开头的事件
	detail, err := rt.Session("sess-steer")
	if err != nil {
		t.Fatal(err)
	}
	steerEvents := 0
	for _, e := range detail.Events {
		if e.Kind == EventUser && strings.HasPrefix(e.Content, steerPrefix) {
			steerEvents++
		}
	}
	if steerEvents != 1 {
		t.Fatalf("重放应看到恰好 1 条插话事件，实际 %d", steerEvents)
	}
}

func TestSteerTwoInjectionsKeepOrder(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.search", `{"query":"quote"}`),
		asstToolCall("c2", "tool.search", `{"query":"kline"}`),
		{Role: contract.RoleAssistant, Content: "第 3 步：两条约束都体现"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm)
	// 第 1 次调用之后插第一条（会在第 2 次调用前生效），第 2 次之后插第二条（第 3 次前生效）。
	llm.afterCall = func(n int) {
		if n == 1 {
			_ = rt.Steer("sess-two", "第一条：只看主板")
		}
		if n == 2 {
			_ = rt.Steer("sess-two", "第二条：不要碰 ST")
		}
	}
	rep, err := rt.Run(context.Background(), "chat", "任务：给方案", "sess-two")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Steers != 2 {
		t.Fatalf("应记录 2 次插话，实际 %d", rep.Steers)
	}
	if len(llm.seen) < 3 {
		t.Fatalf("应有至少 3 次模型调用，实际 %d", len(llm.seen))
	}
	second := messagesText(llm.seen[1])
	third := messagesText(llm.seen[2])
	if !strings.Contains(second, "第一条：只看主板") {
		t.Fatalf("第 2 次调用应带第 1 条插话，实际: %s", second)
	}
	if strings.Contains(second, "第二条：不要碰 ST") {
		t.Fatalf("第 2 次调用不应提前带上第 2 条插话，实际: %s", second)
	}
	if !strings.Contains(third, "第一条：只看主板") || !strings.Contains(third, "第二条：不要碰 ST") {
		t.Fatalf("第 3 次调用应带两条插话，实际: %s", third)
	}
	if strings.Index(third, "第一条：只看主板") > strings.Index(third, "第二条：不要碰 ST") {
		t.Fatalf("插话顺序必须稳定（先到先得），实际: %s", third)
	}
}

func TestSteerValidationAndQueueLimit(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	llm := &fakeLLM{block: gate}
	rt, _, _, _ := newTestRuntime(t, llm)

	// 未运行 / 不存在
	if err := rt.Steer("nope", "x"); err == nil || !strings.Contains(err.Error(), "已结束") {
		t.Fatalf("未运行会话应报「已结束」，实际: %v", err)
	}
	if err := rt.Steer("sess-q", "   "); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空插话应报错，实际: %v", err)
	}
	if err := rt.Steer("sess-q", strings.Repeat("长", maxSteerRunes+1)); err == nil || !strings.Contains(err.Error(), "过长") {
		t.Fatalf("超长插话应报错，实际: %v", err)
	}

	go func() { _, _ = rt.Run(context.Background(), "chat", "队列上限测试", "sess-q") }()
	if !waitFor(t, 2*time.Second, func() bool { return rt.isRunning("sess-q") }) {
		t.Fatal("会话应进入运行中")
	}
	for i := 0; i < maxSteerQueue; i++ {
		if err := rt.Steer("sess-q", fmt.Sprintf("插话 %d", i+1)); err != nil {
			t.Fatalf("第 %d 条插话应成功: %v", i+1, err)
		}
	}
	if err := rt.Steer("sess-q", "第 6 条"); err == nil || !strings.Contains(err.Error(), "队列已满") {
		t.Fatalf("超过队列上限应报错，实际: %v", err)
	}
	if got := rt.SteerQueueLen("sess-q"); got != maxSteerQueue {
		t.Fatalf("队列应保持 %d 条，实际 %d", maxSteerQueue, got)
	}
}

func TestRunRejectsRunningSessionAndSendRoutesToSteer(t *testing.T) {
	gate := make(chan struct{})
	llm := &fakeLLM{block: gate, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "结束"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm)

	done := make(chan Report, 1)
	go func() {
		rep, _ := rt.Run(context.Background(), "chat", "第一轮", "sess-dup")
		done <- rep
	}()
	if !waitFor(t, 2*time.Second, func() bool { return rt.isRunning("sess-dup") }) {
		t.Fatal("会话应进入运行中")
	}
	// 同一 sessionId 再 Run 必须报可操作错误，而不是静默另起。
	if _, err := rt.Run(context.Background(), "chat", "第二轮", "sess-dup"); err == nil ||
		!strings.Contains(err.Error(), "正在运行") || !strings.Contains(err.Error(), "steer") {
		t.Fatalf("运行中重复 Run 应报错并提示 steer，实际: %v", err)
	}
	// Send 在运行中自动转插话。
	rep, err := rt.Send(context.Background(), "chat", "补充：只看主板", "sess-dup")
	if err != nil {
		t.Fatalf("Send 运行中应转插话: %v", err)
	}
	if rep.Steers != 1 || !strings.Contains(rep.Report, "已插话") || rep.SessionID != "sess-dup" {
		t.Fatalf("Send 插话返回不对: %+v", rep)
	}
	if got := rt.SteerQueueLen("sess-dup"); got != 1 {
		t.Fatalf("Send 应把消息排进队列，实际 %d", got)
	}
	close(gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("运行应正常结束")
	}
}

func TestRuntimeRejectsTooFrequent(t *testing.T) {
	llm := &fakeLLM{}
	rt, _, _, _ := newTestRuntime(t, llm)
	rt.router.MaxRunsPerMin = 1
	rt.router.DedupeWindow = 0
	if _, err := rt.Run(context.Background(), "chat", "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Run(context.Background(), "chat", "b", ""); err == nil {
		t.Fatal("超过每分钟上限应返回错误")
	}
}

func TestSessionStoreReplayIsAppendOnly(t *testing.T) {
	store := newMemStore()
	clock := func() time.Time { return time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC) }
	s := NewSessionStore(store, clock)
	if err := s.Create(SessionMeta{ID: "s1", Worker: "chat", Title: "测试"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append("s1", Event{Kind: EventUser, Content: "e"}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) != 3 || detail.Events[2].Seq != 3 {
		t.Fatalf("事件应只追加且序号递增: %+v", detail.Events)
	}
	// 重新打开存储（模拟重启）后继续追加，序号必须接着来而不是覆盖。
	s2 := NewSessionStore(store, clock)
	e, err := s2.Append("s1", Event{Kind: EventAssistant, Content: "after restart"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 4 {
		t.Fatalf("重启后序号应继续为 4，实际 %d", e.Seq)
	}
	detail2, _ := s2.Get("s1")
	if len(detail2.Events) != 4 || detail2.Events[0].Content != "e" {
		t.Fatalf("旧事件不能被覆盖: %+v", detail2.Events)
	}
	if _, err := s.Get("nope"); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}

// recordingLLM 在 fakeLLM 之上记录完整请求（fakeLLM.seen 只保存 messages），
// 用来断言收口调用是否带了工具清单。
type recordingLLM struct {
	*fakeLLM
	mu   sync.Mutex
	reqs []contract.ChatRequest
}

// record 保存一次完整请求。
func (r *recordingLLM) record(req contract.ChatRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
}

// Chat 记录请求并委托给脚本回放。
func (r *recordingLLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	r.record(req)
	return r.fakeLLM.Chat(ctx, req)
}

// Stream 与 Chat 同口径，只在成功且有正文时回调增量。
func (r *recordingLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	r.record(req)
	reply, err := r.fakeLLM.Chat(ctx, req)
	if err != nil {
		return contract.Reply{}, err
	}
	if onDelta != nil && strings.TrimSpace(reply.Message.Content) != "" {
		onDelta(reply.Message.Content)
	}
	return reply, nil
}

// lastRequest 返回最近一次完整请求。
func (r *recordingLLM) lastRequest() contract.ChatRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		return contract.ChatRequest{}
	}
	return r.reqs[len(r.reqs)-1]
}

// finalizeFailLLM 在第 failFrom 次调用起返回错误，用来测收口调用失败时保留原结果。
type finalizeFailLLM struct {
	*fakeLLM
	mu       sync.Mutex
	n        int
	failFrom int
}

// Chat 前 failFrom-1 次走脚本，之后返回错误（模拟收口调用失败）。
func (f *finalizeFailLLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n >= f.failFrom {
		return contract.Reply{}, errors.New("收口调用失败（测试注入）")
	}
	return f.fakeLLM.Chat(ctx, req)
}

// Stream 与 Chat 保持同样的错误注入（runtime 有 OnDelta 时走 Stream）。
func (f *finalizeFailLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	reply, err := f.Chat(ctx, req)
	if err != nil {
		return contract.Reply{}, err
	}
	if onDelta != nil && strings.TrimSpace(reply.Message.Content) != "" {
		onDelta(reply.Message.Content)
	}
	return reply, nil
}

// count 返回已发生的模型调用次数。
func (f *finalizeFailLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// 预算耗尽后应再发一次不带工具的收口调用，报告里既有结论也有停机说明。
func TestRuntimeBudgetExhaustionProducesFinalizedReport(t *testing.T) {
	conclusion := "## 结论\n600000 现价 12.34，短线偏强。\n## 依据\n- market.quote 返回 12.34\n## 不确定性\n- 只有一个快照点位\n## 风险提示\n- 不构成投资建议"
	llm := &recordingLLM{fakeLLM: &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		asstToolCall("c2", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		{Role: contract.RoleAssistant, Content: conclusion},
	}}}
	rt, _, _, _ := newTestRuntime(t, llm, func(o *RuntimeOptions) {
		o.Policy.MaxToolCalls = 10
		o.Policy.MaxTokensPerRun = 150
	})

	rep, err := rt.Run(context.Background(), "chat", "解释一下量比口径", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if len(llm.seen) != 3 {
		t.Fatalf("预算耗尽后应追加一次收口调用（共 3 次），实际 %d 次", len(llm.seen))
	}
	last := llm.lastRequest()
	if len(last.Tools) != 0 {
		t.Fatalf("收口调用必须不带工具，实际带 %d 个", len(last.Tools))
	}
	lastText := messagesText(last.Messages)
	if !strings.Contains(lastText, "最终报告") || !strings.Contains(lastText, "不能再调用任何工具") {
		t.Fatalf("收口调用应带明确的收口指令，实际: %.300s", lastText)
	}
	if !strings.Contains(lastText, "market.quote") || !strings.Contains(lastText, "12.34") {
		t.Fatalf("收口调用应带上已获得的工具结果，实际: %.300s", lastText)
	}
	if !strings.Contains(rep.Report, "## 结论") || !strings.Contains(rep.Report, "短线偏强") {
		t.Fatalf("报告正文应包含收口结论，实际: %q", rep.Report)
	}
	if !strings.Contains(rep.Report, "已停机") || !strings.Contains(rep.Report, "token 预算耗尽") {
		t.Fatalf("报告应保留可读的停机说明，实际: %q", rep.Report)
	}
	if rep.Usage.TotalTokens != 300 {
		t.Fatalf("收口调用的 token 应计入总量（100×3），实际 %d", rep.Usage.TotalTokens)
	}
	if rep.Error != "" {
		t.Fatalf("收口成功时不应有错误: %s", rep.Error)
	}
}

// 收口调用失败时不能吞掉原结果：停机说明仍在，token 也不虚增。
func TestRuntimeBudgetFinalizeFailureKeepsPartialResult(t *testing.T) {
	base := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		asstToolCall("c2", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
	}}
	llm := &finalizeFailLLM{fakeLLM: base, failFrom: 3}
	rt, _, _, _ := newTestRuntime(t, llm, func(o *RuntimeOptions) {
		o.Policy.MaxToolCalls = 10
		o.Policy.MaxTokensPerRun = 150
	})

	rep, err := rt.Run(context.Background(), "chat", "收口失败场景", "")
	if err != nil {
		t.Fatalf("收口失败不应让整轮 Run 报错: %v", err)
	}
	if llm.count() != 3 {
		t.Fatalf("收口调用确实发生过（共 3 次），实际 %d 次", llm.count())
	}
	if rep.Error != "" {
		t.Fatalf("收口失败已是降级路径，不应写入 Report.Error: %s", rep.Error)
	}
	if !strings.Contains(rep.Report, "已停机") || !strings.Contains(rep.Report, "token 预算耗尽") {
		t.Fatalf("收口失败时仍要给出停机说明，实际: %q", rep.Report)
	}
	if rep.Usage.TotalTokens != 200 {
		t.Fatalf("失败的收口调用不应计 token，实际 %d", rep.Usage.TotalTokens)
	}
}

// Token 不设预算上限（Policy.MaxTokensPerRun=0）：
// 累计用量远超旧的隐式默认（60k）也必须跑完脚本，不得出现「预算耗尽」停机。
func TestRuntimeTokenBudgetUnlimitedByDefault(t *testing.T) {
	llm := &fakeLLM{
		usage: &contract.Usage{PromptTokens: 180000, CompletionTokens: 20000, TotalTokens: 200000},
		script: []contract.Message{
			asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
			{Role: contract.RoleAssistant, Content: "## 结论\n不设预算上限时正常收口。\n## 依据\n- market.quote\n## 不确定性\n- 无\n## 风险提示\n- 仅研究参考"},
		},
	}
	rt, _, _, _ := newTestRuntime(t, llm, func(o *RuntimeOptions) {
		o.Policy.MaxToolCalls = 6
		o.Policy.MaxTokensPerRun = 0
	})

	rep, err := rt.Run(context.Background(), "chat", "长任务不设预算", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if rep.Steps != 2 {
		t.Fatalf("不设预算时应跑完脚本（2 步），实际 %d 步", rep.Steps)
	}
	if strings.Contains(rep.Report, "预算耗尽") || strings.Contains(rep.Report, "已停机") {
		t.Fatalf("不设预算上限时不应出现预算停机说明: %q", rep.Report)
	}
	if rep.Usage.TotalTokens != 400000 {
		t.Fatalf("两次调用的 token 都应计入（200k×2），实际 %d", rep.Usage.TotalTokens)
	}
}

// 步数上限同样要收口；收口调用不增加 Steps，也不带工具。
func TestReactStepExhaustionFinalizesWithoutCountingStep(t *testing.T) {
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &recordingLLM{fakeLLM: &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.search", `{"query":"quote"}`),
		{Role: contract.RoleAssistant, Content: "## 结论\n步数用尽，先给出已有结论。\n## 依据\n- tool.search\n## 不确定性\n- 未展开细节\n## 风险提示\n- 仅作研究参考"},
	}}}
	run, err := buildReact(context.Background(), reactConfig{LLM: llm, Gateway: gw, MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Run(context.Background(), "步数上限收口测试")
	if err != nil {
		t.Fatalf("步数上限应优雅收尾: %v", err)
	}
	if !res.Exhausted || res.ExhaustKind != "steps" {
		t.Fatalf("应标记步数停机，实际 exhausted=%v kind=%q", res.Exhausted, res.ExhaustKind)
	}
	if !res.Finalized {
		t.Fatal("步数耗尽后也应完成收口调用")
	}
	if res.Steps != 1 {
		t.Fatalf("收口调用不计入 Steps，期望 1，实际 %d", res.Steps)
	}
	if !strings.Contains(res.Content, "## 结论") {
		t.Fatalf("收口结论应成为最终内容，实际: %q", res.Content)
	}
	if len(llm.seen) != 2 {
		t.Fatalf("应有且仅有一次收口调用（共 2 次），实际 %d 次", len(llm.seen))
	}
	if len(llm.lastRequest().Tools) != 0 {
		t.Fatalf("收口调用必须不带工具，实际带 %d 个", len(llm.lastRequest().Tools))
	}
}
