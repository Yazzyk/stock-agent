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
	if detail.Events[0].Kind != EventUser {
		t.Fatalf("首条事件应为 user，实际 %s", detail.Events[0].Kind)
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
