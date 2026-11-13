package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// customWorkerPrompt 是一段合法的自定义 worker 提示词（≥20 字）。
const customWorkerPrompt = "你是 ETF 资金研究助手：先查份额变化与资金流，再给结论，并写清数据口径与不确定性。"

// createTestWorker 创建一条测试用自定义 worker 并在失败时终止测试。
func createTestWorker(t *testing.T, rt *Runtime, id string) []WorkerView {
	t.Helper()
	views, err := rt.CreateWorker(CreateWorkerRequest{
		ID:          id,
		Title:       "ETF 资金",
		Description: "跟踪 ETF 份额与资金流",
		Prompt:      customWorkerPrompt,
		CreatedBy:   "test",
	})
	if err != nil {
		t.Fatalf("创建自定义 worker 失败: %v", err)
	}
	return views
}

// viewByID 在 worker 视图里按 id 查找。
func viewByID(views []WorkerView, id string) (WorkerView, bool) {
	for _, v := range views {
		if v.ID == id {
			return v, true
		}
	}
	return WorkerView{}, false
}

func TestRouterIsDefaultWorker(t *testing.T) {
	// 收敛后：静态内置表只有总控，但 worker 视图是"总控 + 5 个预设 + 自定义"。
	empty := &Runtime{}
	views := empty.workerViews()
	if len(workerMetas) != 1 || string(workerMetas[0].ID) != string(WorkerRouter) {
		t.Fatalf("内置常驻 worker 只应有总控，实际 %+v", workerMetas)
	}
	if len(views) != 1+len(presetWorkerMetas) {
		t.Fatalf("worker 视图应是 总控+%d 个预设，实际 %d 个：%+v", len(presetWorkerMetas), len(views), views)
	}
	if views[0].ID != string(WorkerRouter) || views[0].Kind != WorkerKindMain || !views[0].Main {
		t.Fatalf("总控必须是第一个 worker 且 kind=main，实际 %+v", views[0])
	}
	for _, v := range views[1:] {
		if v.Kind != WorkerKindPreset || !v.Preset || !v.Custom {
			t.Fatalf("预设 worker 必须 kind=preset/preset=true/custom=true，实际 %+v", v)
		}
	}
	if NormalizeWorker("") != WorkerRouter {
		t.Fatalf("空 worker 必须归一化成总控，实际 %s", NormalizeWorker(""))
	}
	if NormalizeWorker("chat") != WorkerChat {
		t.Fatalf("显式 chat 仍应是普通问答，实际 %s", NormalizeWorker("chat"))
	}

	llm := &fakeLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: "总控结论"}}}
	rt, _, _, _ := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), "", "你好", "")
	if err != nil {
		t.Fatalf("空 worker 运行失败: %v", err)
	}
	if report.Worker != string(WorkerRouter) {
		t.Fatalf("空 worker 应走总控，实际 %s", report.Worker)
	}
}

func TestRouterSystemPromptListsWorkers(t *testing.T) {
	llm := &systemLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: "结论"}}}
	rt, _, _, _ := newTestRuntime(t, llm)
	createTestWorker(t, rt, "etf_flow")

	// 用户改总控模板也覆盖不掉动态目录与披露协议。
	if _, err := rt.Templates().Save(WorkerRouter, customPrompt, nil); err != nil {
		t.Fatalf("保存总控模板失败: %v", err)
	}
	if _, err := rt.Run(context.Background(), "", "帮我看看半导体", ""); err != nil {
		t.Fatalf("总控运行失败: %v", err)
	}
	system := llm.systemAt(0)
	for _, want := range []string{customPrompt, "- premarket｜", "- quant｜", "- etf_flow｜", "（预设）", "（自定义）", "tool.search"} {
		if !strings.Contains(system, want) {
			t.Fatalf("总控 system prompt 缺少 %q：%s", want, system)
		}
	}
	// 目录（能力目录 + worker 目录）必须由代码追加在披露协议之前：
	// "禁止猜测工具名" 只出现在披露协议里，是协议段的可靠锚点。
	if anchor := strings.Index(system, "禁止猜测工具名"); anchor < 0 {
		t.Fatal("总控 system prompt 必须含披露协议")
	} else if strings.Index(system, "etf_flow") > anchor {
		t.Fatal("worker 目录必须由代码追加在披露协议之前")
	}
}

func TestCustomWorkerLifecycle(t *testing.T) {
	llm := &fakeLLM{}
	rt, _, _, _ := newTestRuntime(t, llm)

	views := createTestWorker(t, rt, "etf_flow")
	got, ok := viewByID(views, "etf_flow")
	if !ok || !got.Custom || got.Title != "ETF 资金" {
		t.Fatalf("worker 列表应包含自定义 worker（custom=true），实际 %+v", got)
	}
	if views[len(views)-1].ID != "etf_flow" {
		t.Fatalf("自定义 worker 应排在内置之后，实际 %+v", views)
	}

	// 重复 id 与内置 id 冲突都要给可操作错误。
	if _, err := rt.CreateWorker(CreateWorkerRequest{ID: "etf_flow", Title: "重复", Prompt: customWorkerPrompt}); err == nil ||
		!strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复 id 应报错，实际: %v", err)
	}
	if _, err := rt.CreateWorker(CreateWorkerRequest{ID: "chat", Title: "冲突", Prompt: customWorkerPrompt}); err == nil ||
		!strings.Contains(err.Error(), "内置") {
		t.Fatalf("内置 id 冲突应报错，实际: %v", err)
	}

	// 持久化：同一 Store 新开 Runtime 仍能看到。
	reopened := NewRuntime(RuntimeOptions{Store: rt.opts.Store, LLM: llm, Clock: time.Now})
	if _, ok := viewByID(reopened.Workers(), "etf_flow"); !ok {
		t.Fatal("重启后应能从 Store 读回自定义 worker")
	}

	// 模板：自定义 worker 可编辑，恢复默认回到自定义 prompt。
	if _, err := rt.Templates().SaveWithModel(WorkerID("etf_flow"), "改过的提示词，至少二十个字，用来验证自定义 worker 的模板覆盖行为。", nil, strPtr("fake/model")); err != nil {
		t.Fatalf("保存自定义 worker 模板失败: %v", err)
	}
	tpl, ok := rt.Templates().Get(WorkerID("etf_flow"))
	if !ok || !tpl.Edited || tpl.Model != "fake/model" {
		t.Fatalf("自定义 worker 模板应可编辑并保存模型: %+v", tpl)
	}
	if _, err := rt.Templates().Reset(WorkerID("etf_flow")); err != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
	tpl, _ = rt.Templates().Get(WorkerID("etf_flow"))
	if tpl.Prompt != customWorkerPrompt || tpl.Model != "fake/model" {
		t.Fatalf("恢复默认应回到自定义 prompt 且保留模型: %+v", tpl)
	}

	// 删除：内置不可删、不存在报错、删除后模板覆盖记录一并清理。
	if _, err := rt.RemoveWorker("chat"); err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("内置 worker 不可删，实际: %v", err)
	}
	if _, err := rt.RemoveWorker("nope"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("不存在应报错，实际: %v", err)
	}
	views, err := rt.RemoveWorker("etf_flow")
	if err != nil {
		t.Fatalf("删除自定义 worker 失败: %v", err)
	}
	if _, ok := viewByID(views, "etf_flow"); ok {
		t.Fatalf("删除后列表不应再有该 worker: %+v", views)
	}
	if _, ok := rt.Templates().Get(WorkerID("etf_flow")); ok {
		t.Fatal("删除 worker 后应清理它的模板覆盖记录")
	}
	// 同 id 重建：不应复活旧提示词。
	createTestWorker(t, rt, "etf_flow")
	tpl, _ = rt.Templates().Get(WorkerID("etf_flow"))
	if tpl.Prompt != customWorkerPrompt {
		t.Fatalf("重建后不应复活旧模板，实际 %q", tpl.Prompt)
	}
}

func TestCustomWorkerValidationAndLimit(t *testing.T) {
	rt, _, _, _ := newTestRuntime(t, &fakeLLM{})
	cases := []struct {
		name string
		in   CreateWorkerRequest
		want string
	}{
		{"id 大写", CreateWorkerRequest{ID: "Bad", Title: "t", Prompt: customWorkerPrompt}, "不合法"},
		{"id 数字开头", CreateWorkerRequest{ID: "1bad", Title: "t", Prompt: customWorkerPrompt}, "不合法"},
		{"id 太短", CreateWorkerRequest{ID: "a", Title: "t", Prompt: customWorkerPrompt}, "不合法"},
		{"标题为空", CreateWorkerRequest{ID: "ok_id", Prompt: customWorkerPrompt}, "标题"},
		{"标题太长", CreateWorkerRequest{ID: "ok_id", Title: strings.Repeat("长", maxWorkerTitleRunes+1), Prompt: customWorkerPrompt}, "标题"},
		{"描述太长", CreateWorkerRequest{ID: "ok_id", Title: "t", Description: strings.Repeat("长", maxWorkerDescriptionRunes+1), Prompt: customWorkerPrompt}, "描述"},
		{"提示词太短", CreateWorkerRequest{ID: "ok_id", Title: "t", Prompt: strings.Repeat("短", MinPromptRunes-1)}, "提示词"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := rt.CreateWorker(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("应报「%s」，实际: %v", c.want, err)
			}
		})
	}

	// 上限只算用户自建 worker（5 个系统预设不占额度），所以要建满 maxCustomWorkers 个。
	for i := 0; i < maxCustomWorkers; i++ {
		id := "w" + strings.Repeat("x", 1) + string(rune('a'+i))
		createTestWorker(t, rt, id)
	}
	if _, err := rt.CreateWorker(CreateWorkerRequest{ID: "overflow", Title: "t", Prompt: customWorkerPrompt}); err == nil ||
		!strings.Contains(err.Error(), "上限") {
		t.Fatalf("超过 %d 个应报上限，实际: %v", maxCustomWorkers, err)
	}
}

func TestRunWithModelPriority(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "显式模型结论"},
		{Role: contract.RoleAssistant, Content: "模板模型结论"},
		{Role: contract.RoleAssistant, Content: "显式优先结论"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm)

	report, err := rt.RunWithModel(context.Background(), "chat", "你好", "", "fake/explicit")
	if err != nil {
		t.Fatalf("RunWithModel 失败: %v", err)
	}
	if report.Model != "fake/explicit" || llm.modelAt(0) != "fake/explicit" {
		t.Fatalf("显式 model 必须透传给 LLM，report=%q req=%q", report.Model, llm.modelAt(0))
	}

	if _, err := rt.Templates().SaveWithModel(WorkerPremarket, customPrompt, nil, strPtr("fake/template")); err != nil {
		t.Fatal(err)
	}
	report, err = rt.Run(context.Background(), "premarket", "模板任务", "")
	if err != nil {
		t.Fatalf("模板模型运行失败: %v", err)
	}
	if report.Model != "fake/template" || llm.modelAt(1) != "fake/template" {
		t.Fatalf("未显式指定时应使用模板 model，report=%q req=%q", report.Model, llm.modelAt(1))
	}

	report, err = rt.RunWithModel(context.Background(), "premarket", "显式覆盖任务", "", "fake/override")
	if err != nil {
		t.Fatal(err)
	}
	if report.Model != "fake/override" || llm.modelAt(2) != "fake/override" {
		t.Fatalf("显式 model 必须优先于模板，report=%q req=%q", report.Model, llm.modelAt(2))
	}

	// 会话与列表都要带上实际模型。
	detail, err := rt.Session(report.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Session.Model != "fake/override" {
		t.Fatalf("会话元信息应记录模型，实际 %q", detail.Session.Model)
	}
	sessions, err := rt.Sessions(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sessions {
		if s.ID == report.SessionID {
			found = true
			if s.Model != "fake/override" {
				t.Fatalf("会话列表应返回模型，实际 %q", s.Model)
			}
		}
	}
	if !found {
		t.Fatal("会话列表缺少刚运行的会话")
	}
}

func TestRunScheduledUsesTemplateModel(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "值班结论一"},
		{Role: contract.RoleAssistant, Content: "值班结论二"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm)
	if _, err := rt.Templates().SaveWithModel(WorkerPremarket, customPrompt, nil, strPtr("fake/tpl")); err != nil {
		t.Fatal(err)
	}
	report, err := rt.RunScheduled(context.Background(), WorkerPremarket, customPrompt)
	if err != nil {
		t.Fatalf("RunScheduled 失败: %v", err)
	}
	if report.Model != "fake/tpl" || llm.modelAt(0) != "fake/tpl" {
		t.Fatalf("自动值班应使用模板 model，report=%q req=%q", report.Model, llm.modelAt(0))
	}
	report, err = rt.RunScheduledWithModel(context.Background(), WorkerPremarket, customPrompt, "fake/sched")
	if err != nil {
		t.Fatal(err)
	}
	if report.Model != "fake/sched" || llm.modelAt(1) != "fake/sched" {
		t.Fatalf("RunScheduledWithModel 应覆盖模板 model，report=%q req=%q", report.Model, llm.modelAt(1))
	}
}

func TestSpawnCustomWorkerInheritsModel(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "父会话结论"},
		{Role: contract.RoleAssistant, Content: "子会话继承模型结论"},
		{Role: contract.RoleAssistant, Content: "子会话显式模型结论"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm)
	createTestWorker(t, rt, "etf_flow")

	parent, err := rt.RunWithModel(context.Background(), "chat", "调度一次", "", "fake/parent")
	if err != nil {
		t.Fatalf("父会话运行失败: %v", err)
	}
	child, err := rt.Spawn(WithSession(context.Background(), parent.SessionID), SpawnRequest{
		Task:   "查一下 ETF 份额",
		Worker: "etf_flow",
		Label:  "ETF 份额",
	})
	if err != nil {
		t.Fatalf("派生自定义 worker 失败: %v", err)
	}
	if child.Worker != "etf_flow" || child.Model != "fake/parent" {
		t.Fatalf("子会话应使用自定义 worker 并继承父模型，实际 %+v", child)
	}
	if got := llm.modelAt(1); got != "fake/parent" {
		t.Fatalf("子会话请求模型应继承父会话，实际 %q", got)
	}

	child, err = rt.Spawn(WithSession(context.Background(), parent.SessionID), SpawnRequest{
		Task:   "再查一次",
		Worker: "etf_flow",
		Model:  "fake/child",
	})
	if err != nil {
		t.Fatalf("显式模型派生失败: %v", err)
	}
	if child.Model != "fake/child" || llm.modelAt(2) != "fake/child" {
		t.Fatalf("显式 model 必须优先于父会话模型，实际 %+v req=%q", child, llm.modelAt(2))
	}

	// 未知 worker 的错误文案要列出可用 worker（含自定义）。
	if _, err := rt.Spawn(WithSession(context.Background(), parent.SessionID), SpawnRequest{Task: "x", Worker: "nope"}); err == nil ||
		!strings.Contains(err.Error(), "etf_flow") {
		t.Fatalf("未知 worker 应列出可用 worker，实际: %v", err)
	}
}

// strPtr 返回字符串指针，便于构造可选的 model 入参。
func strPtr(s string) *string { return &s }
