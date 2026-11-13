package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// testClock 是可拨动的测试时钟（避免与调度协程的数据竞争）。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// shTime 构造上海时区的时刻。
func shTime(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, shanghaiLoc())
}

func newTestScheduler(t *testing.T, rt *Runtime, store contract.Store, clock func() time.Time) *Scheduler {
	t.Helper()
	return NewScheduler(SchedulerOptions{
		Runtime:   rt,
		Templates: rt.Templates(),
		Store:     store,
		Clock:     clock,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tick:      10 * time.Millisecond,
	})
}

func schedIdle(s *Scheduler) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.busy
}

func setPhaseEnabled(t *testing.T, s *Scheduler, phase string, enabled bool) {
	t.Helper()
	v := enabled
	if _, err := s.SetSchedule(SchedulePatch{Phase: phase, Enabled: &v}); err != nil {
		t.Fatalf("设置 %s 开关失败: %v", phase, err)
	}
}

func phaseOf(t *testing.T, s *Scheduler, phase string) PhaseSchedule {
	t.Helper()
	for _, p := range s.Phases() {
		if p.Phase == phase {
			return p
		}
	}
	t.Fatalf("调度里没有阶段 %s", phase)
	return PhaseSchedule{}
}

func sessionCount(t *testing.T, rt *Runtime) int {
	t.Helper()
	list, err := rt.Sessions(50)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return len(list)
}

func TestScheduleDue(t *testing.T) {
	pre := func() PhaseSchedule {
		return PhaseSchedule{Phase: PhasePremarket, Enabled: true, At: DefaultPremarketAt}
	}
	post := func() PhaseSchedule {
		return PhaseSchedule{Phase: PhasePostmarket, Enabled: true, At: DefaultPostmarketAt}
	}
	intra := func(every int) PhaseSchedule {
		return PhaseSchedule{Phase: PhaseIntraday, Enabled: true, EveryMinutes: every, Window: DefaultIntradayWindow}
	}
	// 2026-11-10 是周二。
	cases := []struct {
		name string
		now  time.Time
		p    PhaseSchedule
		last time.Time
		want bool
	}{
		{"盘前到点", shTime(2026, 11, 10, 9, 16), pre(), time.Time{}, true},
		{"盘前还没到点", shTime(2026, 11, 10, 9, 10), pre(), time.Time{}, false},
		{"盘前当天已跑", shTime(2026, 11, 10, 9, 30), pre(), shTime(2026, 11, 10, 9, 15), false},
		{"盘前昨天跑过今天还没跑", shTime(2026, 11, 10, 9, 16), pre(), shTime(2026, 11, 9, 9, 15), true},
		{"盘前超过补跑宽限", shTime(2026, 11, 10, 11, 30), pre(), time.Time{}, false},
		{"周六不跑", shTime(2026, 11, 14, 9, 16), pre(), time.Time{}, false},
		{"周日不跑", shTime(2026, 11, 15, 9, 16), post(), time.Time{}, false},
		{"复盘到点", shTime(2026, 11, 10, 15, 20), post(), time.Time{}, true},
		{"复盘未到点", shTime(2026, 11, 10, 15, 0), post(), time.Time{}, false},
		{"复盘超宽限", shTime(2026, 11, 10, 18, 0), post(), time.Time{}, false},
		{"盘中窗口内首次", shTime(2026, 11, 10, 10, 0), intra(30), time.Time{}, true},
		{"盘中距上次不足频率", shTime(2026, 11, 10, 10, 0), intra(30), shTime(2026, 11, 10, 9, 45), false},
		{"盘中已满频率", shTime(2026, 11, 10, 10, 20), intra(30), shTime(2026, 11, 10, 9, 45), true},
		{"午休不跑", shTime(2026, 11, 10, 12, 0), intra(30), time.Time{}, false},
		{"开盘前不跑", shTime(2026, 11, 10, 9, 0), intra(15), time.Time{}, false},
		{"收盘后不跑", shTime(2026, 11, 10, 15, 30), intra(15), time.Time{}, false},
		{"阶段关闭", shTime(2026, 11, 10, 9, 16), PhaseSchedule{Phase: PhasePremarket, Enabled: false, At: DefaultPremarketAt}, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := scheduleDue(c.now, c.p, c.last)
			if got != c.want {
				t.Fatalf("%s：scheduleDue=%v（%s），want %v", c.name, got, reason, c.want)
			}
			if !got && strings.TrimSpace(reason) == "" {
				t.Fatal("不触发时必须给中文原因，供 lastStatus 与事件展示")
			}
		})
	}
}

func TestNextRunTime(t *testing.T) {
	pre := PhaseSchedule{Phase: PhasePremarket, Enabled: true, At: DefaultPremarketAt}

	// 还没到点 → 今天 09:15。
	next := nextRunTime(shTime(2026, 11, 10, 8, 0), pre, time.Time{})
	if next.Hour() != 9 || next.Minute() != 15 || next.Day() != 10 {
		t.Fatalf("未到点应返回今天 09:15，实际 %s", next.Format(time.RFC3339))
	}

	// 今天已跑 → 下一个工作日 09:15（周五 → 下周一）。
	friday := shTime(2026, 11, 13, 9, 30)
	next = nextRunTime(friday, pre, shTime(2026, 11, 13, 9, 15))
	if next.Weekday() != time.Monday || next.Day() != 16 || next.Hour() != 9 {
		t.Fatalf("周二到周五跑过之后应排到下周一 09:15，实际 %s", next.Format(time.RFC3339))
	}

	// 盘中：窗口内且已满频率 → last+every。
	intra := PhaseSchedule{Phase: PhaseIntraday, Enabled: true, EveryMinutes: 30, Window: DefaultIntradayWindow}
	next = nextRunTime(shTime(2026, 11, 10, 10, 5), intra, shTime(2026, 11, 10, 9, 50))
	if next.Hour() != 10 || next.Minute() != 20 {
		t.Fatalf("盘中下次应为上次+30 分钟（10:20），实际 %s", next.Format(time.RFC3339))
	}
	// 盘中：午休时 → 下午开盘 13:00。
	next = nextRunTime(shTime(2026, 11, 10, 12, 0), intra, shTime(2026, 11, 10, 11, 20))
	if next.Hour() != 13 || next.Minute() != 0 {
		t.Fatalf("午休时下次应为 13:00，实际 %s", next.Format(time.RFC3339))
	}
}

// TestSchedulerFiresOncePerDayAndSingleFlight 验证：
// 到点触发一次、当天去重、单飞（同一时刻只跑一个自动任务）。
func TestSchedulerFiresOncePerDayAndSingleFlight(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "盘前计划：关注半导体主线"},
		{Role: contract.RoleAssistant, Content: "不应被调用"},
	}}
	rt, store, _, _ := newTestRuntime(t, llm)
	clock := &testClock{t: shTime(2026, 11, 10, 9, 16)}
	sched := newTestScheduler(t, rt, store, clock.Now)
	setPhaseEnabled(t, sched, PhaseIntraday, false)
	setPhaseEnabled(t, sched, PhasePostmarket, false)

	sched.TickOnce(context.Background(), clock.Now())
	if !waitFor(t, 3*time.Second, func() bool { return sessionCount(t, rt) == 1 && schedIdle(sched) }) {
		t.Fatalf("到点应触发一次自动值班，实际会话数 %d", sessionCount(t, rt))
	}
	if got := phaseOf(t, sched, PhasePremarket).LastStatus; got != "完成" {
		t.Fatalf("跑完 lastStatus 应为「完成」，实际 %q", got)
	}
	if phaseOf(t, sched, PhasePremarket).LastSessionID == "" {
		t.Fatal("跑完应记录 lastSessionId")
	}

	// 同一天再 tick：当天去重，不能再跑。
	clock.Set(shTime(2026, 11, 10, 9, 40))
	sched.TickOnce(context.Background(), clock.Now())
	time.Sleep(120 * time.Millisecond)
	if n := sessionCount(t, rt); n != 1 {
		t.Fatalf("当天去重失效：应仍只有 1 个会话，实际 %d", n)
	}
	if llm.callCount() != 1 {
		t.Fatalf("当天去重失效：模型只应被调用 1 次，实际 %d", llm.callCount())
	}

	// 第二天到点：可以再跑。
	clock.Set(shTime(2026, 11, 11, 9, 16))
	sched.TickOnce(context.Background(), clock.Now())
	if !waitFor(t, 3*time.Second, func() bool { return sessionCount(t, rt) == 2 && schedIdle(sched) }) {
		t.Fatalf("第二个交易日应再跑一次，实际会话数 %d", sessionCount(t, rt))
	}
}

// TestSchedulerNoModelRecordsStatus 验证无可用模型时不触发、只记 lastStatus。
func TestSchedulerNoModelRecordsStatus(t *testing.T) {
	rt, store, _, sink := newTestRuntime(t, &systemLLM{noKey: true})
	clock := &testClock{t: shTime(2026, 11, 10, 9, 16)}
	sched := newTestScheduler(t, rt, store, clock.Now)
	setPhaseEnabled(t, sched, PhaseIntraday, false)
	setPhaseEnabled(t, sched, PhasePostmarket, false)

	sched.TickOnce(context.Background(), clock.Now())
	if n := sessionCount(t, rt); n != 0 {
		t.Fatalf("没有模型时不能触发运行，实际会话数 %d", n)
	}
	if got := phaseOf(t, sched, PhasePremarket).LastStatus; got != "未配置模型" {
		t.Fatalf("没有模型时 lastStatus 应为「未配置模型」，实际 %q", got)
	}
	if got := phaseOf(t, sched, PhasePremarket).LastRunAt; got != "" {
		t.Fatalf("没有模型时不能记 lastRunAt（否则当天再也不会补跑），实际 %q", got)
	}
	// 只认"阶段级"事件（setSchedule 会发一条带 phases 的全局事件，这里要的是前者）。
	if !waitFor(t, 2*time.Second, func() bool { return phaseSchedulePayload(t, sink, PhasePremarket) != nil }) {
		t.Fatal("应发 agent.schedule 事件说明未触发原因")
	}
	payload := phaseSchedulePayload(t, sink, PhasePremarket)
	for _, key := range []string{"phase", "running", "reason"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("agent.schedule 事件缺少 %q: %+v", key, payload)
		}
	}
	if payload["reason"] != "未配置模型" {
		t.Fatalf("事件原因应为「未配置模型」，实际 %v", payload["reason"])
	}
}

// TestSchedulerYieldsToManualRun 验证用户手动运行优先，自动任务顺延。
func TestSchedulerYieldsToManualRun(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	llm := &fakeLLM{block: gate}
	rt, store, _, _ := newTestRuntime(t, llm)
	clock := &testClock{t: shTime(2026, 11, 10, 9, 16)}
	sched := newTestScheduler(t, rt, store, clock.Now)

	go func() { _, _ = rt.Run(context.Background(), "postmarket", "复盘今天", "sess-manual") }()
	if !waitFor(t, 2*time.Second, func() bool { return rt.isRunning("sess-manual") }) {
		t.Fatal("手动运行应进入运行中")
	}
	sched.TickOnce(context.Background(), clock.Now())
	time.Sleep(120 * time.Millisecond)
	if n := sessionCount(t, rt); n != 1 {
		t.Fatalf("手动运行期间不能启动自动任务，实际会话数 %d", n)
	}
	if got := phaseOf(t, sched, PhasePremarket).LastStatus; got != "" {
		t.Fatalf("顺延时不应改 lastStatus，实际 %q", got)
	}
}

func TestSchedulerRespectsTemplateSwitch(t *testing.T) {
	rt, store, _, _ := newTestRuntime(t, &fakeLLM{})
	clock := &testClock{t: shTime(2026, 11, 10, 9, 16)}
	sched := newTestScheduler(t, rt, store, clock.Now)
	setPhaseEnabled(t, sched, PhaseIntraday, false)
	setPhaseEnabled(t, sched, PhasePostmarket, false)

	off := false
	if _, err := sched.SaveTemplate(WorkerPremarket, customPrompt, &off); err != nil {
		t.Fatal(err)
	}
	sched.TickOnce(context.Background(), clock.Now())
	time.Sleep(80 * time.Millisecond)
	if n := sessionCount(t, rt); n != 0 {
		t.Fatalf("模板关闭时不应触发，实际会话数 %d", n)
	}
	if got := phaseOf(t, sched, PhasePremarket).LastStatus; !strings.Contains(got, "模板已关闭") {
		t.Fatalf("模板关闭应写进 lastStatus，实际 %q", got)
	}
}

func TestSchedulerSetSchedule(t *testing.T) {
	rt, store, _, _ := newTestRuntime(t, &fakeLLM{})
	clock := &testClock{t: shTime(2026, 11, 10, 9, 16)}
	sched := newTestScheduler(t, rt, store, clock.Now)

	bad := []struct {
		name  string
		patch SchedulePatch
		want  string
	}{
		{"未知阶段", SchedulePatch{Phase: "unknown"}, "未知阶段"},
		{"at 格式错", SchedulePatch{Phase: PhasePremarket, At: "9点半"}, "HH:MM"},
		{"盘中不支持 at", SchedulePatch{Phase: PhaseIntraday, At: "09:30"}, "everyMinutes"},
		{"频率过小", SchedulePatch{Phase: PhaseIntraday, EveryMinutes: intPtr(1)}, "everyMinutes"},
		{"频率过大", SchedulePatch{Phase: PhaseIntraday, EveryMinutes: intPtr(999)}, "everyMinutes"},
		{"非盘中给频率", SchedulePatch{Phase: PhasePostmarket, EveryMinutes: intPtr(30)}, "只有盘中"},
		{"window 格式错", SchedulePatch{Phase: PhaseIntraday, Window: "9点半-11点半"}, "HH:MM-HH:MM"},
		{"window 起止反了", SchedulePatch{Phase: PhaseIntraday, Window: "15:00-09:30"}, "早于"},
		{"盘前给 window", SchedulePatch{Phase: PhasePremarket, Window: "09:30-11:30"}, "只有盘中"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := sched.SetSchedule(c.patch); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("应报「%s」，实际: %v", c.want, err)
			}
		})
	}

	// 正常修改：开关 + 频率 + at，并且会重算 nextRunAt。
	phases, err := sched.SetSchedule(SchedulePatch{
		Phase:        PhaseIntraday,
		Enabled:      boolPtr(true),
		EveryMinutes: intPtr(15),
	})
	if err != nil {
		t.Fatalf("修改盘中调度失败: %v", err)
	}
	var intra PhaseSchedule
	for _, p := range phases {
		if p.Phase == PhaseIntraday {
			intra = p
		}
	}
	if intra.EveryMinutes != 15 || !intra.Enabled {
		t.Fatalf("盘中调度修改未生效: %+v", intra)
	}
	if intra.NextRunAt == "" || parseTime(intra.NextRunAt).IsZero() {
		t.Fatalf("nextRunAt 应可解析，实际 %q", intra.NextRunAt)
	}

	// 交易时段可编辑，并做规范化（个位数小时补零）。
	if _, err := sched.SetSchedule(SchedulePatch{Phase: PhaseIntraday, Window: "9:45-11:30,13:00-14:57"}); err != nil {
		t.Fatalf("改盘中交易时段失败: %v", err)
	}
	if got := phaseOf(t, sched, PhaseIntraday).Window; got != "09:45-11:30,13:00-14:57" {
		t.Fatalf("盘中 window 应为规范化后的 09:45-11:30,13:00-14:57，实际 %q", got)
	}

	if _, err := sched.SetSchedule(SchedulePatch{Phase: PhasePostmarket, At: "16:00"}); err != nil {
		t.Fatalf("改复盘时间失败: %v", err)
	}
	if got := phaseOf(t, sched, PhasePostmarket).At; got != "16:00" {
		t.Fatalf("复盘 at 应为 16:00，实际 %q", got)
	}

	// 重新构造调度器（模拟重启）后配置仍在。
	reopened := newTestScheduler(t, rt, store, clock.Now)
	if got := phaseOf(t, reopened, PhaseIntraday).EveryMinutes; got != 15 {
		t.Fatalf("重启后应读回 everyMinutes=15，实际 %d", got)
	}
	if got := phaseOf(t, reopened, PhaseIntraday).Window; got != "09:45-11:30,13:00-14:57" {
		t.Fatalf("重启后应读回 window，实际 %q", got)
	}
}

func TestSchedulerStartStop(t *testing.T) {
	rt, store, _, _ := newTestRuntime(t, &fakeLLM{})
	clock := &testClock{t: shTime(2026, 11, 10, 9, 16)}
	sched := newTestScheduler(t, rt, store, clock.Now)
	setPhaseEnabled(t, sched, PhaseIntraday, false)
	setPhaseEnabled(t, sched, PhasePostmarket, false)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	if !waitFor(t, 3*time.Second, func() bool { return sessionCount(t, rt) >= 1 }) {
		t.Fatal("Start 后应按时触发一次自动值班")
	}
	cancel()
	sched.Stop()
	if sched.cancel != nil {
		t.Fatal("Stop 之后 cancel 应被清空")
	}
}

// phaseSchedulePayload 取某个阶段最后一条 agent.schedule 事件（跳过 setSchedule 的全局事件）。
func phaseSchedulePayload(t *testing.T, sink *eventSink, phase string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, e := range sink.byTopic("agent.schedule") {
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			continue
		}
		if p, _ := payload["phase"].(string); p == phase {
			found = payload
		}
	}
	return found
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }
