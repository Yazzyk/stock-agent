package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// systemLLM 只记录每次模型调用拿到的 system prompt / 工具清单（模板与上下文测试用）。
type systemLLM struct {
	mu      sync.Mutex
	systems []string
	script  []contract.Message
	window  int
	// noKey 为 true 时模拟"没有配置 API Key"的模型服务（调度器测试用）。
	noKey bool
}

func (s *systemLLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	s.mu.Lock()
	s.systems = append(s.systems, req.System)
	var msg contract.Message
	if len(s.script) > 0 {
		msg = s.script[0]
		s.script = s.script[1:]
	} else {
		msg = contract.Message{Role: contract.RoleAssistant, Content: "默认结论"}
	}
	s.mu.Unlock()
	return contract.Reply{Message: msg, Usage: contract.Usage{TotalTokens: 20}, Model: "fake"}, nil
}

func (s *systemLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	reply, err := s.Chat(ctx, req)
	if err == nil && onDelta != nil && reply.Message.Content != "" {
		onDelta(reply.Message.Content)
	}
	return reply, err
}

func (s *systemLLM) Models(context.Context) ([]contract.ModelInfo, error) {
	return []contract.ModelInfo{{Provider: "fake", Model: "fake", HasKey: !s.noKey, IsDefault: true, ContextWindow: s.window}}, nil
}

func (s *systemLLM) Default(context.Context) (contract.ModelInfo, error) {
	return contract.ModelInfo{Provider: "fake", Model: "fake", HasKey: !s.noKey, IsDefault: true, ContextWindow: s.window}, nil
}

func (s *systemLLM) systemAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.systems) {
		return ""
	}
	return s.systems[i]
}

// customPrompt 是一段合法的自定义提示词（≥20 字，且刻意不含任何披露线索）。
const customPrompt = "你是盘前助手。今天只做两件事：先看资金主线，再列三个观察条件，并写清计划失效的判据。"

func templateByID(t *testing.T, tpls []AgentTemplate, id string) AgentTemplate {
	t.Helper()
	for _, tpl := range tpls {
		if tpl.ID == id {
			return tpl
		}
	}
	t.Fatalf("模板列表里没有 %s", id)
	return AgentTemplate{}
}

func TestTemplateDefaultsComeFromWorkerBehaviour(t *testing.T) {
	store := NewTemplateStore(newMemStore())
	tpls := store.All()
	// 模板的顺序与 worker 视图一致：总控 + 5 个预设（workerMetas 只剩总控）。
	wantCount := len(workerMetas) + len(presetWorkerMetas)
	if len(tpls) != wantCount {
		t.Fatalf("模板数应是 内置(%d)+预设(%d)=%d，实际 %d", len(workerMetas), len(presetWorkerMetas), wantCount, len(tpls))
	}
	specs := append(Workers(), presetWorkerSpecs()...)
	for i, tpl := range tpls {
		spec := specs[i]
		if tpl.ID != string(spec.ID) || tpl.Title != spec.Title {
			t.Fatalf("模板顺序/标题应与 worker 一致：%+v vs %+v", tpl, spec)
		}
		if tpl.Prompt != tpl.DefaultPrompt || tpl.Edited {
			t.Fatalf("未改过时 prompt 必须等于 defaultPrompt 且 edited=false：%+v", tpl)
		}
		if !tpl.Enabled {
			t.Fatalf("模板默认应开启：%+v", tpl)
		}
		// 默认 prompt 只含行为描述，披露协议由代码追加。
		if strings.Contains(tpl.DefaultPrompt, "tool.search") || strings.Contains(tpl.DefaultPrompt, "tool.describe") {
			t.Fatalf("%s 的默认 prompt 不应包含披露协议（协议必须由代码统一追加）", tpl.ID)
		}
		if !strings.Contains(spec.System, tpl.DefaultPrompt) || !strings.Contains(spec.System, "tool.search") {
			t.Fatalf("%s 的 system 应等于 默认prompt+披露协议", tpl.ID)
		}
	}
}

func TestTemplateSaveResetAndPersistence(t *testing.T) {
	raw := newMemStore()
	store := NewTemplateStore(raw)

	tpls, err := store.Save(WorkerPremarket, "  "+customPrompt+"  ", nil)
	if err != nil {
		t.Fatalf("保存模板失败: %v", err)
	}
	tpl := templateByID(t, tpls, string(WorkerPremarket))
	if tpl.Prompt != customPrompt {
		t.Fatalf("prompt 应 trim 后落盘，实际: %q", tpl.Prompt)
	}
	if !tpl.Edited {
		t.Fatal("改过之后 edited 必须为 true")
	}
	if tpl.DefaultPrompt == tpl.Prompt {
		t.Fatal("defaultPrompt 必须保持默认值不变")
	}

	// 换一个实例（模拟重启）后仍在。
	reopened := NewTemplateStore(raw)
	if got := reopened.Prompt(WorkerPremarket); got != customPrompt {
		t.Fatalf("重启后应读回自定义 prompt，实际: %q", got)
	}

	// 关掉开关后再恢复默认：prompt 回默认，开关保持关闭。
	off := false
	if _, err := store.Save(WorkerQuant, customPrompt, &off); err != nil {
		t.Fatal(err)
	}
	tpls, err = store.Reset(WorkerQuant)
	if err != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
	quant := templateByID(t, tpls, string(WorkerQuant))
	if quant.Prompt != quant.DefaultPrompt || quant.Edited {
		t.Fatalf("恢复默认后 prompt 应回到默认值：%+v", quant)
	}
	if quant.Enabled {
		t.Fatal("恢复默认不应改动开关（开关应保持关闭）")
	}

	// 再恢复一次不报错（幂等）。
	if _, err := store.Reset(WorkerQuant); err != nil {
		t.Fatalf("重复恢复默认应幂等: %v", err)
	}
}

func TestTemplateSaveValidation(t *testing.T) {
	store := NewTemplateStore(newMemStore())
	cases := []struct {
		name   string
		id     WorkerID
		prompt string
		want   string
	}{
		{"太短", WorkerPremarket, strings.Repeat("短", MinPromptRunes-1), "太短"},
		{"空", WorkerPremarket, "   ", "太短"},
		{"太长", WorkerPostmarket, strings.Repeat("长", MaxPromptRunes+1), "太长"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := store.Save(c.id, c.prompt, nil); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s：应报「%s」，实际: %v", c.name, c.want, err)
			}
		})
	}
	// 未知 id 必须报错，而不是像 NormalizeWorker 那样静默回落到 chat。
	if _, err := store.Save(WorkerID("不存在"), customPrompt, nil); err == nil || !strings.Contains(err.Error(), "未知模板") {
		t.Fatalf("未知 id 应报错，实际: %v", err)
	}
	if _, ok := WorkerFromID(" chat "); !ok {
		t.Fatal("WorkerFromID 应容忍首尾空白")
	}
	if _, ok := WorkerFromID("不存在"); ok {
		t.Fatal("WorkerFromID 必须严格：未知 id 返回 false")
	}
}

// TestManualRunUsesTemplateButKeepsDisclosureProtocol 验证：
// 手动运行用当前模板 prompt，但披露协议永远由代码追加，用户改不掉。
func TestManualRunUsesTemplateButKeepsDisclosureProtocol(t *testing.T) {
	llm := &systemLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: "结论"}}}
	rt, _, _, _ := newTestRuntime(t, llm)
	if _, err := rt.Templates().Save(WorkerPremarket, customPrompt, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Run(context.Background(), "premarket", "", ""); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	system := llm.systemAt(0)
	if !strings.Contains(system, customPrompt) {
		t.Fatalf("手动运行必须使用当前模板 prompt，实际: %q", system)
	}
	if !strings.Contains(system, "禁止猜测工具名") || !strings.Contains(system, "tool.describe") {
		t.Fatalf("模板改不掉披露协议：system 里必须仍有完整协议，实际: %q", system)
	}
	if strings.Index(system, customPrompt) > strings.Index(system, "tool.search") {
		t.Fatal("协议必须追加在模板 prompt 之后")
	}
}

// TestRunScheduledSkipsRouterDedupe 验证自动值班不会被 60s 去重窗口误判成重复触发。
func TestRunScheduledSkipsRouterDedupe(t *testing.T) {
	llm := &systemLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "第一次盘前"},
		{Role: contract.RoleAssistant, Content: "第二次盘前"},
	}}
	rt, _, _, _ := newTestRuntime(t, llm)

	first, err := rt.RunScheduled(context.Background(), WorkerPremarket, customPrompt)
	if err != nil {
		t.Fatalf("RunScheduled 失败: %v", err)
	}
	if first.Deduped || strings.TrimSpace(first.Report) == "" {
		t.Fatalf("自动值班不应被去重: %+v", first)
	}
	second, err := rt.RunScheduled(context.Background(), WorkerPremarket, customPrompt)
	if err != nil {
		t.Fatalf("第二次 RunScheduled 失败: %v", err)
	}
	if second.Deduped {
		t.Fatal("自动值班（input 为空）不能被 Router 去重窗口拦住")
	}
	if second.SessionID == first.SessionID {
		t.Fatal("两次自动值班必须是两个独立会话")
	}
	if !strings.Contains(llm.systemAt(0), customPrompt) {
		t.Fatalf("自动值班必须用模板 prompt 当 system，实际: %q", llm.systemAt(0))
	}
}

// TestRouterPromptRequiresIntegratedDeliverable 是 RouterAgent 系统提示词的契约断言：
// 最终报告必须是**整合后的交付物**（结论 / 依据 / 口径与时间戳 / 风险与待验证点），
// 禁止整块粘贴子智能体原文，引用子内容只能作为证据行文。
// 同时必须保留「等/回收子结果」的双工要求——不展示子报告 ≠ 丢掉子结果。
func TestRouterPromptRequiresIntegratedDeliverable(t *testing.T) {
	prompt := defaultPrompt(WorkerRouter)
	musts := []string{
		"整合后的交付物",
		"禁止整块粘贴子智能体原文",
		"数据口径与时间戳",
		"风险与待验证点",
		"据半导体资金子任务",
		"子结果必须全部回收",
	}
	for _, want := range musts {
		if !strings.Contains(prompt, want) {
			t.Fatalf("RouterAgent 提示词必须包含 %q（报告=交付物的口径），实际 prompt:\n%s", want, prompt)
		}
	}
	// 双工语义不能因为「不展示子报告」被删掉：仍要派发、播报、等结果。
	for _, want := range []string{"agent.spawn", "【子智能体完成】", "不要原样复述整份子报告"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("RouterAgent 提示词必须保留双工要求 %q，实际 prompt:\n%s", want, prompt)
		}
	}
	// 披露协议仍然由 composeSystem 追加，且改不掉（模板契约不变）。
	system := composeSystem(prompt)
	if !strings.Contains(system, prompt) || !strings.Contains(system, "禁止猜测工具名") {
		t.Fatal("composeSystem 必须保留行为 prompt 与披露协议")
	}
}
