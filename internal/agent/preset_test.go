package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 本文件覆盖 C 项「worker 收敛」：只有总控是内置常驻，其它 worker 由总控/系统生成，
// 而**自动值班不能因此失效**（这是硬要求）。

// TestPresetWorkersAreMaterialized 首次启动必须把 5 个预设落成自定义 worker（CreatedBy=preset）。
func TestPresetWorkersAreMaterialized(t *testing.T) {
	store := newMemStore()
	rt := NewRuntime(RuntimeOptions{Store: store, LLM: &fakeLLM{}, Clock: time.Now})

	views := rt.Workers()
	if len(views) != 1+len(presetWorkerMetas) {
		t.Fatalf("应是 总控+%d 个预设，实际 %d：%+v", len(presetWorkerMetas), len(views), views)
	}
	if views[0].ID != string(WorkerRouter) || !views[0].Main || views[0].Custom {
		t.Fatalf("总控必须是内置主控（main=true, custom=false）：%+v", views[0])
	}
	for _, want := range []string{string(WorkerPremarket), string(WorkerIntraday), string(WorkerPostmarket), string(WorkerQuant), string(WorkerChat)} {
		v, ok := viewByID(views, want)
		if !ok {
			t.Fatalf("缺少预设 worker %s：%+v", want, views)
		}
		if !v.Preset || !v.Custom || v.Kind != WorkerKindPreset || v.Main {
			t.Fatalf("预设 worker %s 的种类字段不对：%+v", want, v)
		}
	}

	// 幂等：再跑一次不重复创建。
	created, err := rt.EnsurePresetWorkers()
	if err != nil {
		t.Fatalf("EnsurePresetWorkers 应幂等: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("已存在时不应重复创建，实际新建 %v", created)
	}
	if got := len(rt.Workers()); got != 1+len(presetWorkerMetas) {
		t.Fatalf("幂等调用后数量不应变化，实际 %d", got)
	}

	// 重启后仍从 Store 读回（落盘成功）。
	reopened := NewRuntime(RuntimeOptions{Store: store, LLM: &fakeLLM{}, Clock: time.Now})
	if _, ok := viewByID(reopened.Workers(), string(WorkerPostmarket)); !ok {
		t.Fatal("重启后应能从 Store 读回预设 worker")
	}
}

// TestScheduledPhasesStillRunAfterConvergence 硬要求：
// premarket/intraday/postmarket 现在是自定义（预设）worker，值班调度仍必须能精确找到并跑起来。
func TestScheduledPhasesStillRunAfterConvergence(t *testing.T) {
	for _, phase := range []string{PhasePremarket, PhaseIntraday, PhasePostmarket} {
		t.Run(phase, func(t *testing.T) {
			llm := &fakeLLM{script: []contract.Message{
				{Role: contract.RoleAssistant, Content: "自动值班结论：" + "" + phase + ""},
			}}
			rt, _, _, _ := newTestRuntime(t, llm)
			report, err := rt.RunScheduled(context.Background(), WorkerID(phase), "")
			if err != nil {
				t.Fatalf("%s 自动值班失败: %v", phase, err)
			}
			if report.Worker != phase {
				t.Fatalf("报告 worker 应为 %s，实际 %s", phase, report.Worker)
			}
			if !strings.Contains(report.Report, "自动值班结论") {
				t.Fatalf("%s 应产出报告，实际 %q", phase, report.Report)
			}
			if report.SessionID == "" {
				t.Fatal("自动值班必须落会话")
			}
		})
	}
}

// TestSchedulerTickFiresIntradayWithPresetWorker 调度器 tick 到点必须真的触发（按 id 精确查预设 worker）。
func TestSchedulerTickFiresIntradayWithPresetWorker(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: "盘中巡检结论"}}}
	// 盘中时段，且 everyMinutes 已满足。
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	rt, store, _, _ := newTestRuntime(t, llm)
	rt.opts.Clock = func() time.Time { return now }
	sched := NewScheduler(SchedulerOptions{
		Runtime:   rt,
		Templates: rt.Templates(),
		Store:     store,
		Clock:     func() time.Time { return now },
	})
	// 单飞语义：同一 tick 只触发一个阶段，所以先关掉盘前/复盘，专测盘中。
	off := false
	if _, err := sched.SetSchedule(SchedulePatch{Phase: PhasePremarket, Enabled: &off}); err != nil {
		t.Fatalf("关掉盘前失败: %v", err)
	}
	if _, err := sched.SetSchedule(SchedulePatch{Phase: PhasePostmarket, Enabled: &off}); err != nil {
		t.Fatalf("关掉复盘失败: %v", err)
	}
	if _, err := sched.SetSchedule(SchedulePatch{Phase: PhaseIntraday, EveryMinutes: intPtr(15)}); err != nil {
		t.Fatalf("设置盘中频率失败: %v", err)
	}
	sched.TickOnce(context.Background(), now)
	if !waitFor(t, 3*time.Second, func() bool { return llm.enteredCount() > 0 }) {
		t.Fatal("到点后必须触发盘中自动值班（预设 worker 不能因不再内置而失效）")
	}
	if !waitFor(t, 3*time.Second, func() bool {
		for _, p := range sched.Phases() {
			if p.Phase == PhaseIntraday && p.LastSessionID != "" {
				return true
			}
		}
		return false
	}) {
		for _, p := range sched.Phases() {
			t.Logf("phase=%s status=%q session=%q lastRun=%q", p.Phase, p.LastStatus, p.LastSessionID, p.LastRunAt)
		}
		t.Fatal("调度器必须记录 LastSessionId")
	}
}

// TestTemplateForPhasesSurvivesWithoutPresetMaterialization 预设还没落成时，
// 值班模板也必须能取到（否则升级后的第一次启动会静默不跑）。
func TestTemplateForPhasesSurvivesWithoutPresetMaterialization(t *testing.T) {
	store := NewTemplateStore(newMemStore())
	for _, phase := range []string{PhasePremarket, PhaseIntraday, PhasePostmarket} {
		tpl, ok := store.Get(WorkerID(phase))
		if !ok {
			t.Fatalf("%s 模板必须可取（预设未落成时也要能解析）", phase)
		}
		if strings.TrimSpace(tpl.Prompt) == "" || tpl.Title == "" || tpl.Title == phase {
			t.Fatalf("%s 模板应有默认 prompt 与中文标题，实际 %+v", phase, tpl)
		}
	}
}

// intPtr 复用包内已有定义（schedule_test.go 里已有一个同签名 helper 时改用局部变量）。
