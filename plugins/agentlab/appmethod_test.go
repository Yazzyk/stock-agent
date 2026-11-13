package agentlab

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/internal/agent"
	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// fakeKernelMethods 模拟 kernel.methods 服务：实现 InvokeMethod + MethodCatalog。
// 这里刻意注册**三个不同插件**的方法，用来验证 app.method 的通用性（不是只对 agentlab 生效）。
type fakeKernelMethods struct {
	mu      sync.Mutex
	methods map[string][]string // plugin -> methods
	calls   []string            // 记录 plugin.method 调用顺序
	payload map[string]string   // plugin.method -> 收到的 payload
	// denyMethodControl 模拟 Policy.AllowMethodControl=false。
	denyMethodControl bool
}

func newFakeKernelMethods() *fakeKernelMethods {
	return &fakeKernelMethods{
		methods: map[string][]string{
			"agentlab":      {"schedule", "setSchedule", "tree"},
			"modelsettings": {"list", "save"},
			"console":       {"status", "setEnabled"},
			"knowledge":     {"import"},
		},
		payload: map[string]string{},
	}
}

func (f *fakeKernelMethods) InvokeMethod(_ context.Context, pluginID, method string, payload json.RawMessage) (json.RawMessage, error) {
	if f.denyMethodControl {
		return nil, fmt.Errorf("策略已关闭方法控制（AllowMethodControl=false）：请在设置中心打开后再试")
	}
	allowed := false
	for _, m := range f.methods[pluginID] {
		if m == method {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("kernel: 方法不存在: %s.%s", pluginID, method)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := pluginID + "." + method
	f.calls = append(f.calls, key)
	f.payload[key] = string(payload)
	return json.Marshal(map[string]any{"plugin": pluginID, "method": method, "ok": true})
}

func (f *fakeKernelMethods) MethodCatalog() []kernelMethodInfo {
	out := make([]kernelMethodInfo, 0, 8)
	for p, ms := range f.methods {
		for _, m := range ms {
			out = append(out, kernelMethodInfo{Plugin: p, Method: m, Description: p + " 的 " + m})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func (f *fakeKernelMethods) callNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// activateWithKernel 在本包测试上下文的基础上注册 kernel.methods 服务并激活插件。
func activateWithKernel(t *testing.T, kernel *fakeKernelMethods) (*plugin.Context, *AgentLab) {
	t.Helper()
	c := testContext(t, newMemStore(), &fakeLLM{hasKey: true}, hostWithMarket())
	if kernel != nil {
		if err := c.Services.Register(methodControlService, methodInvoker(kernel)); err != nil {
			t.Fatalf("注册方法控制面失败: %v", err)
		}
	}
	p := New().(*AgentLab)
	if err := p.Activate(c); err != nil {
		t.Fatalf("Activate 失败: %v", err)
	}
	t.Cleanup(func() { _ = p.Deactivate(context.Background()) })
	return c, p
}

// TestAppMethodControlsThreeDifferentPlugins 集成验证：
// app.method 能调到至少 3 个不同插件的 method（agentlab / modelsettings / console / knowledge）。
func TestAppMethodControlsThreeDifferentPlugins(t *testing.T) {
	kernel := newFakeKernelMethods()
	c, _ := activateWithKernel(t, kernel)
	tool, ok := c.Tools.Get("app.method")
	if !ok {
		t.Fatal("必须注册 app.method 工具")
	}

	cases := []struct {
		plugin string
		method string
		body   string
	}{
		{"agentlab", "setSchedule", `{"phase":"intraday","everyMinutes":15}`},
		{"modelsettings", "list", "{}"},
		{"console", "setEnabled", `{"id":"marketcenter","enabled":false}`},
		{"knowledge", "import", `{"title":"复盘笔记"}`},
	}
	for _, tc := range cases {
		args, _ := json.Marshal(map[string]any{
			"plugin":  tc.plugin,
			"method":  tc.method,
			"payload": json.RawMessage(tc.body),
		})
		out, err := tool.Run(context.Background(), args)
		if err != nil {
			t.Fatalf("app.method %s.%s 不应抛错: %v", tc.plugin, tc.method, err)
		}
		buf, _ := json.Marshal(out)
		if !strings.Contains(string(buf), `"ok":true`) || !strings.Contains(string(buf), tc.method) {
			t.Fatalf("app.method %s.%s 应成功，实际: %s", tc.plugin, tc.method, string(buf))
		}
		if got := kernel.payload[tc.plugin+"."+tc.method]; got != tc.body {
			t.Fatalf("payload 必须原样透传，期望 %s 实际 %s", tc.body, got)
		}
	}
	if calls := kernel.callNames(); len(calls) != 4 {
		t.Fatalf("应调用 4 次，实际 %v", calls)
	}
	plugins := map[string]bool{}
	for _, name := range kernel.callNames() {
		plugins[strings.SplitN(name, ".", 2)[0]] = true
	}
	if len(plugins) < 3 {
		t.Fatalf("必须能调到至少 3 个不同插件，实际 %v", plugins)
	}
}

// TestAppMethodRejectsUnknownMethod 白名单：不在目录里的方法一律拒绝，并列出可用方法。
func TestAppMethodRejectsUnknownMethod(t *testing.T) {
	kernel := newFakeKernelMethods()
	c, _ := activateWithKernel(t, kernel)
	tool, _ := c.Tools.Get("app.method")

	// 1) 插件存在、方法不存在。
	args, _ := json.Marshal(map[string]any{"plugin": "agentlab", "method": "dropAllTables"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("白名单拒绝不应抛错（要回给模型可读文本）: %v", err)
	}
	text := fmt.Sprint(out)
	if !strings.Contains(text, "没有方法") || !strings.Contains(text, "setSchedule") {
		t.Fatalf("应报方法不存在并列出可用方法，实际: %s", text)
	}
	if calls := kernel.callNames(); len(calls) != 0 {
		t.Fatalf("被拒的调用不能穿透到 kernel，实际 %v", calls)
	}

	// 2) 插件根本不存在。
	args, _ = json.Marshal(map[string]any{"plugin": "nope", "method": "x"})
	out, _ = tool.Run(context.Background(), args)
	text = fmt.Sprint(out)
	if !strings.Contains(text, "不在可调用列表") || !strings.Contains(text, "agentlab") {
		t.Fatalf("应报插件不存在并列出可用插件，实际: %s", text)
	}

	// 3) 缺参数。
	out, _ = tool.Run(context.Background(), json.RawMessage(`{}`))
	if text := fmt.Sprint(out); !strings.Contains(text, "需要 plugin 与 method") {
		t.Fatalf("缺参数应给可操作提示，实际: %s", text)
	}

	// 4) 非法 payload（payload 不是合法 JSON 对象）。
	// 直接拼原始 args：json.RawMessage 本身不合法时 Marshal 会失败，构造不出这个入参。
	out, _ = tool.Run(context.Background(), json.RawMessage(`{"plugin":"agentlab","method":"schedule","payload":not-json}`))
	if text := fmt.Sprint(out); !strings.Contains(text, "payload") {
		t.Fatalf("非法 payload 应被拒，实际: %s", text)
	}
	if calls := kernel.callNames(); len(calls) != 0 {
		t.Fatalf("任何被拒的调用都不能穿透到 kernel，实际 %v", calls)
	}
}

// TestAppMethodRespectsAllowMethodControl Policy.AllowMethodControl=false 时直接拒绝。
func TestAppMethodRespectsAllowMethodControl(t *testing.T) {
	kernel := newFakeKernelMethods()
	kernel.denyMethodControl = true
	c, _ := activateWithKernel(t, kernel)
	tool, _ := c.Tools.Get("app.method")
	args, _ := json.Marshal(map[string]any{"plugin": "agentlab", "method": "schedule"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("策略关闭应返回文本而不是 error: %v", err)
	}
	text := fmt.Sprint(out)
	if !strings.Contains(text, "AllowMethodControl") {
		t.Fatalf("应明确说明是策略关闭，实际: %s", text)
	}
	if calls := kernel.callNames(); len(calls) != 0 {
		t.Fatalf("策略关闭时不能有任何调用，实际 %v", calls)
	}
}

// TestAppMethodWithoutServiceDegradesGracefully 老版本 kernel（没有 kernel.methods）：
// 插件照样能激活，工具给出可操作中文错误（不能 panic，也不能让整个插件起不来）。
func TestAppMethodWithoutServiceDegradesGracefully(t *testing.T) {
	c, _ := activateWithKernel(t, nil)
	tool, ok := c.Tools.Get("app.method")
	if !ok {
		t.Fatal("服务缺失时 app.method 仍必须注册（前端要知道有这个能力）")
	}
	args, _ := json.Marshal(map[string]any{"plugin": "agentlab", "method": "schedule"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("服务缺失应返回文本而不是 error: %v", err)
	}
	if text := fmt.Sprint(out); !strings.Contains(text, "方法控制面不可用") {
		t.Fatalf("应给可操作中文错误，实际: %s", text)
	}

	// app.catalog 同样优雅降级。
	cat, ok := c.Tools.Get("app.catalog")
	if !ok {
		t.Fatal("必须注册 app.catalog")
	}
	out, err = cat.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("app.catalog 不应抛错: %v", err)
	}
	if text := fmt.Sprint(out); !strings.Contains(text, "false") {
		t.Fatalf("app.catalog 应返回 ok=false 与原因，实际: %s", text)
	}
}

// TestCapabilityCatalogTeachesRouterWhatItControls 总控的能力目录：
// 必须把"能控制什么 + 插件方法索引 + 工具索引"写进 system prompt，
// 而且用户改模板也覆盖不掉（由代码追加）。
func TestCapabilityCatalogTeachesRouterWhatItControls(t *testing.T) {
	kernel := newFakeKernelMethods()
	c, p := activateWithKernel(t, kernel)

	llm := &fakeLLM{hasKey: true, script: []contract.Message{{Role: contract.RoleAssistant, Content: "结论"}}}
	p.llm = llm
	// 用同一个 runtime（服务已注入），只换 LLM 记录 system prompt。
	p.runtime = agent.NewRuntime(agent.RuntimeOptions{
		LLM:     llm,
		Store:   c.Store,
		Events:  c.Events,
		Clock:   c.Clock,
		Gateway: p.gateway,
		Policy:  contract.Policy{MaxToolCalls: 6, MaxTokensPerRun: 10000},
	})
	p.refreshCapabilityCatalog()
	if _, err := p.runtime.Run(context.Background(), "", "你能帮我改盯盘频率吗", ""); err != nil {
		t.Fatalf("总控运行失败: %v", err)
	}
	system := llm.systemAt(0)
	for _, want := range []string{
		"【你能控制什么",
		"查数据", "跑回测", "读写知识库", "改盯盘规则", "改值班时间", "改模型", "看审计",
		"【插件方法索引",
		"agentlab", "setSchedule", "modelsettings", "console",
		"【工具索引",
		"market.quote",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("能力目录缺少 %q：%s", want, system)
		}
	}
	// 能力目录必须在披露协议之前（用户模板覆盖不掉）。
	if strings.Index(system, "【你能控制什么") > strings.Index(system, "禁止猜测工具名") {
		t.Fatal("能力目录必须由代码追加在披露协议之前")
	}
}

// TestWorkersMethodReportsMainAndKinds workers 方法的最终形状：
// 前端按 kind/main 判断主控，不再用 id 字符串或 mode。
func TestWorkersMethodReportsMainAndKinds(t *testing.T) {
	c, _ := activateWithKernel(t, nil)
	out := mustOut(t, c, "workers", map[string]any{})
	buf, _ := json.Marshal(out)
	var resp struct {
		Workers []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Kind        string `json:"kind"`
			Main        bool   `json:"main"`
			Preset      bool   `json:"preset"`
			Builtin     bool   `json:"builtin"`
			Custom      bool   `json:"custom"`
			Description string `json:"description"`
			Model       string `json:"model,omitempty"`
		} `json:"workers"`
		MainID  string `json:"mainId"`
		Total   int    `json:"total"`
		Presets int    `json:"presets"`
		Customs int    `json:"customs"`
	}
	if err := json.Unmarshal(buf, &resp); err != nil {
		t.Fatalf("workers 返回结构不对: %s", string(buf))
	}
	if resp.MainID != "router" || resp.Workers[0].ID != "router" || !resp.Workers[0].Main {
		t.Fatalf("mainId 必须是 router 且在第一位: %s", string(buf))
	}
	if !resp.Workers[0].Builtin || resp.Workers[0].Custom {
		t.Fatalf("总控必须 builtin=true custom=false: %+v", resp.Workers[0])
	}
	if resp.Presets == 0 || resp.Total != len(resp.Workers) {
		t.Fatalf("presets/total 必须自洽: %s", string(buf))
	}
	found := false
	for _, w := range resp.Workers[1:] {
		if w.ID == "premarket" {
			found = true
			if !w.Preset || w.Kind != "preset" || w.Main {
				t.Fatalf("premarket 应是预设: %+v", w)
			}
		}
		if w.Description == "" || w.Title == "" {
			t.Fatalf("每个 worker 都要有 title/description: %+v", w)
		}
	}
	if !found {
		t.Fatalf("workers 必须包含 premarket: %s", string(buf))
	}
}

// TestMethodInvokerIsKernelInterface 守住最容易踩的坑：
// 服务里放的必须是"实现了 kernel.MethodInvoker"的真实类型，
// 本地重新定义 MethodCatalog 的返回类型会让接口断言永远失败（app.method 永远不可用）。
func TestMethodInvokerIsKernelInterface(t *testing.T) {
	var _ methodInvoker = (kernel.MethodInvoker)(nil)
	if methodControlService != "kernel.methods" {
		t.Fatalf("服务名必须与 kernel.MethodControlService 一致，实际 %q", methodControlService)
	}
	info := kernelMethodInfo{Plugin: "agentlab", Method: "schedule", Description: "查看调度"}
	buf, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != `{"plugin":"agentlab","method":"schedule","description":"查看调度"}` {
		t.Fatalf("MethodInfo JSON 形状变了: %s", string(buf))
	}
}

// TestServiceRegistryRoundTripRealInvoker 走一遍真实取用路径：
// 注册到 ServiceRegistry → Get → 断言成 methodInvoker（Activate 里发生的就是这件事）。
func TestServiceRegistryRoundTripRealInvoker(t *testing.T) {
	kernelStub := newFakeKernelMethods()
	svc := plugin.NewServiceRegistry()
	if err := svc.Register(methodControlService, methodInvoker(kernelStub)); err != nil {
		t.Fatal(err)
	}
	v, ok := svc.Get(methodControlService)
	if !ok {
		t.Fatal("服务必须能取到")
	}
	inv, ok := v.(methodInvoker)
	if !ok {
		t.Fatalf("取到的值必须实现 methodInvoker，实际类型 %T", v)
	}
	if _, err := inv.InvokeMethod(context.Background(), "agentlab", "schedule", json.RawMessage("{}")); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if len(inv.MethodCatalog()) == 0 {
		t.Fatal("目录不能为空")
	}
}
