package test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- 注册 ----------

// 重复插件 ID 必须直接报错：不能出现"看起来装了其实被覆盖"。
func TestRegisterDuplicateID(t *testing.T) {
	f := newKernel(t)
	f.register(t, newPlugin("dup"))
	err := f.k.Register(newPlugin("dup"))
	if err == nil {
		t.Fatal("重复插件 ID 应报错，实际为 nil")
	}
	if !strings.Contains(err.Error(), "重复注册") {
		t.Fatalf("错误信息应说明重复注册，实际: %v", err)
	}
	if got := len(f.k.Plugins()); got != 1 {
		t.Fatalf("重复注册不应增加插件数，期望 1 实际 %d", got)
	}
}

// Manifest 不合法时在注册阶段就被拦下。
func TestRegisterInvalidManifest(t *testing.T) {
	cases := []struct {
		name string
		man  plugin.Manifest
		want string
	}{
		{"ID 为空", plugin.Manifest{Name: "x", Version: "1"}, "id"},
		{"Name 为空", plugin.Manifest{ID: "x", Version: "1"}, "name"},
		{"Version 为空", plugin.Manifest{ID: "x", Name: "x"}, "version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKernel(t)
			err := f.k.Register(&fakePlugin{man: tc.man})
			if err == nil {
				t.Fatalf("非法 manifest 应被拒绝: %+v", tc.man)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("错误信息应提到 %q，实际: %v", tc.want, err)
			}
		})
	}
}

// kernel.New 缺少 Store/Guard/Audit 时必须失败（依赖注入不能是 nil）。
func TestNewKernelRequiresDeps(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		opts kernel.Options
	}{
		{"缺 Store", kernel.Options{Logger: logger, Guard: newFakeGuard(), Audit: newFakeAudit()}},
		{"缺 Guard", kernel.Options{Logger: logger, Store: newFakeStore(), Audit: newFakeAudit()}},
		{"缺 Audit", kernel.Options{Logger: logger, Store: newFakeStore(), Guard: newFakeGuard()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := kernel.New(tc.opts); err == nil {
				t.Fatal("缺少必需依赖时应返回错误")
			}
		})
	}
}

// ---------- 拓扑 ----------

// 依赖成环：Start 必须报错而不是死循环/部分激活。
func TestTopoOrderCycle(t *testing.T) {
	f := newKernel(t)
	a := newPlugin("a", "b")
	b := newPlugin("b", "a")
	f.register(t, a, b)
	err := f.k.Start(context.Background())
	if err == nil {
		t.Fatal("依赖成环应报错")
	}
	if !strings.Contains(err.Error(), "成环") {
		t.Fatalf("错误信息应说明成环，实际: %v", err)
	}
	if a.activateCount() != 0 || b.activateCount() != 0 {
		t.Fatalf("成环时不应激活任何插件，实际 a=%d b=%d", a.activateCount(), b.activateCount())
	}
}

// 依赖缺失：报错并指出是谁缺了谁。
func TestTopoOrderMissingDependency(t *testing.T) {
	f := newKernel(t)
	f.register(t, newPlugin("a", "ghost"))
	err := f.k.Start(context.Background())
	if err == nil {
		t.Fatal("依赖缺失应报错")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("错误信息应提到缺失依赖 ghost，实际: %v", err)
	}
}

// 激活顺序必须满足依赖先于依赖方，与注册顺序无关。
func TestActivationOrderRespectsDeps(t *testing.T) {
	f := newKernel(t)
	rec := newOrderLog()
	a := newPlugin("a")
	b := newPlugin("b", "a")
	c := newPlugin("c", "b")
	for _, p := range []*fakePlugin{a, b, c} {
		p.log = rec
	}
	// 故意逆序注册：c -> b -> a
	f.register(t, c, b, a)
	f.start(t)

	got := rec.snapshot()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("激活次数不符：期望 %v 实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("激活顺序应为 %v（依赖先于依赖方），实际 %v", want, got)
		}
	}
	for _, p := range []*fakePlugin{a, b, c} {
		if p.activateCount() != 1 {
			t.Fatalf("插件 %s 应激活 1 次，实际 %d", p.man.ID, p.activateCount())
		}
	}
}

// 单个插件 Activate panic 被兜底：Start 不崩，其它插件照常激活。
func TestActivatePanicRecovered(t *testing.T) {
	f := newKernel(t)
	boom := newPlugin("boom")
	boom.activateFn = func(*plugin.Context) error { panic("插件内部炸了") }
	ok := withTools(newPlugin("ok"), echoTool("ok.tool"))

	f.register(t, boom, ok)
	if err := f.k.Start(context.Background()); err != nil {
		t.Fatalf("单插件 panic 不应让 Start 失败，实际: %v", err)
	}

	view := f.pluginView(t, "boom")
	if view.Active {
		t.Fatal("panic 的插件不应标记为已激活")
	}
	if !strings.Contains(view.Error, "panic") {
		t.Fatalf("插件视图应保留 panic 原因，实际: %q", view.Error)
	}
	if !f.pluginView(t, "ok").Active {
		t.Fatal("其它插件应正常激活")
	}
	e := f.audit.lastOf(t, "lifecycle", "activate")
	_ = e
	if n := f.audit.countIf(func(e contract.AuditEvent) bool {
		return e.Kind == "lifecycle" && e.Action == "activate" && e.Plugin == "boom" && !e.OK
	}); n != 1 {
		t.Fatalf("激活失败应落一条审计，实际 %d 条", n)
	}
}

// Activate 返回 error：自身未激活，依赖它的插件被跳过（不连带崩溃）。
func TestActivateErrorSkipsDependents(t *testing.T) {
	f := newKernel(t)
	base := newPlugin("base")
	base.activateFn = func(*plugin.Context) error { return errors.New("初始化失败") }
	mid := newPlugin("mid", "base")
	top := newPlugin("top", "mid")
	f.register(t, base, mid, top)
	if err := f.k.Start(context.Background()); err != nil {
		t.Fatalf("插件激活失败不应让 Start 返回错误，实际: %v", err)
	}
	for _, id := range []string{"base", "mid", "top"} {
		v := f.pluginView(t, id)
		if v.Active {
			t.Fatalf("插件 %s 不应激活（错误链上游失败）", id)
		}
	}
	if v := f.pluginView(t, "base"); !strings.Contains(v.Error, "初始化失败") {
		t.Fatalf("base 应保留原始错误，实际: %q", v.Error)
	}
	for _, id := range []string{"mid", "top"} {
		v := f.pluginView(t, id)
		if !v.Skipped {
			t.Fatalf("插件 %s 应标记为 skipped", id)
		}
		if !strings.Contains(v.Error, "依赖未激活") {
			t.Fatalf("插件 %s 应说明依赖未激活，实际: %q", id, v.Error)
		}
	}
}

// 跨插件工具重名：后激活者整体撤销（含 Deactivate），先到者保留。
func TestToolNameConflictAcrossPlugins(t *testing.T) {
	f := newKernel(t)
	first := withTools(newPlugin("first"), echoTool("dup.tool"))
	second := withTools(newPlugin("second"), echoTool("dup.tool"))
	f.register(t, first, second)
	f.start(t)

	if !f.pluginView(t, "first").Active {
		t.Fatal("先注册工具的插件应保持激活")
	}
	v := f.pluginView(t, "second")
	if v.Active {
		t.Fatal("工具重名的插件不应激活")
	}
	if !strings.Contains(v.Error, "重名") {
		t.Fatalf("错误信息应说明工具重名，实际: %q", v.Error)
	}
	if second.deactivateCount() != 1 {
		t.Fatalf("重名冲突应回滚调用 Deactivate 一次，实际 %d", second.deactivateCount())
	}
	tools := f.k.Tools()
	if len(tools) != 1 {
		t.Fatalf("kernel 工具表应只保留 1 个工具，实际 %d", len(tools))
	}
	if tools[0].Owner != "first" {
		t.Fatalf("重名工具应归属先到者 first，实际 %q", tools[0].Owner)
	}
}

// Start 重复调用必须报错（避免重复激活注册重复工具）。
func TestStartTwice(t *testing.T) {
	f := newKernel(t)
	f.register(t, withTools(newPlugin("a"), echoTool("a.tool")))
	f.start(t)
	err := f.k.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "已启动") {
		t.Fatalf("重复 Start 应报错，实际: %v", err)
	}
}

// ---------- 调用路由 ----------

func TestInvokeErrors(t *testing.T) {
	t.Run("插件不存在", func(t *testing.T) {
		f := newKernel(t)
		f.start(t)
		_, err := f.k.Invoke(context.Background(), "nope", "list", nil)
		if !errors.Is(err, kernel.ErrPluginNotFound) {
			t.Fatalf("应返回 ErrPluginNotFound，实际: %v", err)
		}
	})
	t.Run("插件未激活", func(t *testing.T) {
		f := newKernel(t)
		broken := newPlugin("broken")
		broken.activateFn = func(*plugin.Context) error { return errors.New("初始化失败") }
		f.register(t, broken)
		f.start(t)
		_, err := f.k.Invoke(context.Background(), "broken", "list", nil)
		if err == nil || !strings.Contains(err.Error(), "未激活") {
			t.Fatalf("未激活插件应报错，实际: %v", err)
		}
		if !strings.Contains(err.Error(), "初始化失败") {
			t.Fatalf("错误应透出未激活原因，实际: %v", err)
		}
	})
	t.Run("方法不存在", func(t *testing.T) {
		f := newKernel(t)
		m := withMethod(newPlugin("m"), "list", func(context.Context, json.RawMessage) (any, error) {
			return nil, nil
		})
		f.register(t, m)
		f.start(t)
		_, err := f.k.Invoke(context.Background(), "m", "missing", nil)
		if !errors.Is(err, kernel.ErrMethodNotFound) {
			t.Fatalf("应返回 ErrMethodNotFound，实际: %v", err)
		}
	})
}

func TestCallToolUnknown(t *testing.T) {
	f := newKernel(t)
	f.register(t, withTools(newPlugin("a"), echoTool("a.tool")))
	f.start(t)
	_, err := f.k.CallTool(context.Background(), "a.missing", nil)
	if !errors.Is(err, kernel.ErrToolNotFound) {
		t.Fatalf("未知工具应返回 ErrToolNotFound，实际: %v", err)
	}
}

// 空 payload 归一为 {}：工具/方法不应拿到 nil 参数。
func TestPayloadDefaultsToEmptyObject(t *testing.T) {
	f := newKernel(t)
	tool := echoTool("a.echo")
	m := withMethod(newPlugin("m"), "echo", func(_ context.Context, payload json.RawMessage) (any, error) {
		return map[string]string{"payload": string(payload)}, nil
	})
	f.register(t, withTools(newPlugin("a"), tool), m)
	f.start(t)

	out, err := f.k.CallTool(context.Background(), "a.echo", nil)
	if err != nil {
		t.Fatalf("CallTool 失败: %v", err)
	}
	var toolRes struct {
		Args string `json:"args"`
	}
	if err := json.Unmarshal(out, &toolRes); err != nil {
		t.Fatalf("工具返回不是合法 JSON: %v", err)
	}
	if toolRes.Args != "{}" {
		t.Fatalf("空参数应归一为 {}，实际 %q", toolRes.Args)
	}

	raw, err := f.k.Invoke(context.Background(), "m", "echo", nil)
	if err != nil {
		t.Fatalf("Invoke 失败: %v", err)
	}
	var methodRes struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(raw, &methodRes); err != nil {
		t.Fatalf("方法返回不是合法 JSON: %v", err)
	}
	if methodRes.Payload != "{}" {
		t.Fatalf("空 payload 应归一为 {}，实际 %q", methodRes.Payload)
	}
}

// 方法 panic 被兜底为 error，且落一条失败审计。
func TestMethodPanicRecovered(t *testing.T) {
	f := newKernel(t)
	m := withMethod(newPlugin("m"), "boom", func(context.Context, json.RawMessage) (any, error) {
		panic("方法内部炸了")
	})
	f.register(t, m)
	f.start(t)
	_, err := f.k.Invoke(context.Background(), "m", "boom", nil)
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("方法 panic 应转成 error，实际: %v", err)
	}
	e := f.audit.lastOf(t, "method", "boom")
	if e.OK {
		t.Fatal("panic 的方法调用审计应为失败")
	}
	if !strings.Contains(e.Detail, "panic") {
		t.Fatalf("审计应记录 panic 原因，实际: %q", e.Detail)
	}
}

// 工具 panic 同样被兜底。
func TestToolPanicRecovered(t *testing.T) {
	f := newKernel(t)
	tool := plugin.Tool{
		Name:    "a.boom",
		Summary: "总是 panic 的工具",
		Risk:    plugin.RiskRead,
		Run: func(context.Context, json.RawMessage) (any, error) {
			panic("工具内部炸了")
		},
	}
	f.register(t, withTools(newPlugin("a"), tool))
	f.start(t)
	_, err := f.k.CallTool(context.Background(), "a.boom", nil)
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("工具 panic 应转成 error，实际: %v", err)
	}
	if e := f.audit.lastOf(t, "tool", "a.boom"); e.OK {
		t.Fatal("panic 的工具调用审计应为失败")
	}
}

// ---------- 结果截断 ----------

// 超过 256KB 的结果必须被截断成合法 JSON，并带 truncated 标记。
func TestToolResultTruncated(t *testing.T) {
	limit := kernel.MaxToolResultBytes

	t.Run("普通对象", func(t *testing.T) {
		f := newKernel(t)
		big := strings.Repeat("x", limit+4096)
		tool := plugin.Tool{
			Name:    "a.big",
			Summary: "返回超长结果",
			Risk:    plugin.RiskRead,
			Run: func(context.Context, json.RawMessage) (any, error) {
				return map[string]string{"blob": big}, nil
			},
		}
		f.register(t, withTools(newPlugin("a"), tool))
		f.start(t)

		out, err := f.k.CallTool(context.Background(), "a.big", nil)
		if err != nil {
			t.Fatalf("截断不应变成错误: %v", err)
		}
		var payload struct {
			Truncated bool   `json:"truncated"`
			Limit     int    `json:"limit"`
			Preview   string `json:"preview"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("截断结果必须是合法 JSON: %v（前 80 字节: %.80s）", err, string(out))
		}
		if !payload.Truncated {
			t.Fatalf("超限结果应标记 truncated=true，实际: %.200s", string(out))
		}
		if payload.Limit != limit {
			t.Fatalf("limit 字段应为 %d，实际 %d", limit, payload.Limit)
		}
		if len(payload.Preview) != limit {
			t.Fatalf("preview 应恰好 %d 字节，实际 %d", limit, len(payload.Preview))
		}
		if len(out) > limit+512 {
			t.Fatalf("截断后仍超上限：%d 字节", len(out))
		}
		if e := f.audit.lastOf(t, "tool", "a.big"); !e.OK {
			t.Fatal("截断是正常结果，审计应记为成功")
		}
	})

	t.Run("RawMessage", func(t *testing.T) {
		f := newKernel(t)
		raw := json.RawMessage("{\"blob\":\"" + strings.Repeat("y", limit) + "\"}")
		tool := plugin.Tool{
			Name:    "a.rawbig",
			Summary: "返回超长 RawMessage",
			Risk:    plugin.RiskRead,
			Run: func(context.Context, json.RawMessage) (any, error) {
				return raw, nil
			},
		}
		f.register(t, withTools(newPlugin("a"), tool))
		f.start(t)
		out, err := f.k.CallTool(context.Background(), "a.rawbig", nil)
		if err != nil {
			t.Fatalf("CallTool 失败: %v", err)
		}
		var payload struct {
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("截断结果必须是合法 JSON: %v", err)
		}
		if !payload.Truncated {
			t.Fatal("超长 RawMessage 也应被截断")
		}
	})

	t.Run("未超限不截断", func(t *testing.T) {
		f := newKernel(t)
		f.register(t, withTools(newPlugin("a"), echoTool("a.small")))
		f.start(t)
		out, err := f.k.CallTool(context.Background(), "a.small", json.RawMessage(`{"k":"v"}`))
		if err != nil {
			t.Fatalf("CallTool 失败: %v", err)
		}
		if strings.Contains(string(out), "\"truncated\"") {
			t.Fatalf("小结果不应被截断: %s", string(out))
		}
	})
}

// ---------- 风险闸门 ----------

// exec 风险工具永不放行（红线：不引入 shell 执行工具），并落安全审计。
func TestExecToolDenied(t *testing.T) {
	cases := []struct {
		name      string
		allowExec bool
	}{
		{"策略禁止 exec", false},
		{"策略允许 exec，但红线仍拒绝", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKernel(t)
			policy := f.guard.Policy()
			policy.AllowExec = tc.allowExec
			f.guard.setPolicy(policy)

			ran := false
			tool := plugin.Tool{
				Name:    "a.exec",
				Summary: "模拟执行外部命令",
				Risk:    plugin.RiskExec,
				Run: func(context.Context, json.RawMessage) (any, error) {
					ran = true
					return "should not run", nil
				},
			}
			f.register(t, withTools(newPlugin("a"), tool))
			f.start(t)

			_, err := f.k.CallTool(context.Background(), "a.exec", nil)
			if !errors.Is(err, kernel.ErrToolDenied) {
				t.Fatalf("exec 工具应被拒绝（ErrToolDenied），实际: %v", err)
			}
			if ran {
				t.Fatal("被拒绝的工具绝不能执行")
			}
			e := f.audit.lastOf(t, "security", "a.exec")
			if e.OK {
				t.Fatal("安全拒绝应记为 OK=false")
			}
			if e.Risk != string(plugin.RiskExec) {
				t.Fatalf("审计应记录风险等级 exec，实际 %q", e.Risk)
			}
		})
	}
}

// 写风险工具在默认策略下放行（本地配置写入），但审计带 risk=write。
func TestWriteToolAllowed(t *testing.T) {
	f := newKernel(t)
	tool := plugin.Tool{
		Name:    "a.write",
		Summary: "写本地配置",
		Risk:    plugin.RiskWrite,
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]bool{"ok": true}, nil
		},
	}
	f.register(t, withTools(newPlugin("a"), tool))
	f.start(t)
	if _, err := f.k.CallTool(context.Background(), "a.write", nil); err != nil {
		t.Fatalf("write 风险工具应放行，实际: %v", err)
	}
	if e := f.audit.lastOf(t, "tool", "a.write"); e.Risk != string(plugin.RiskWrite) {
		t.Fatalf("审计应记录 risk=write，实际 %q", e.Risk)
	}
}

// ---------- 审计 ----------

func TestAuditRecordsLifecycleToolMethod(t *testing.T) {
	f := newKernel(t)
	okTool := echoTool("a.ok")
	badTool := plugin.Tool{
		Name:    "a.bad",
		Summary: "总是失败的工具",
		Risk:    plugin.RiskNetwork,
		Run: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("上游返回 502")
		},
	}
	p := withTools(newPlugin("a"), okTool, badTool)
	good := withMethod(newPlugin("m"), "run", func(context.Context, json.RawMessage) (any, error) {
		return map[string]int{"n": 1}, nil
	})
	badMethod := withMethod(newPlugin("bm"), "run", func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("方法炸了")
	})
	f.register(t, p, good, badMethod)
	f.start(t)

	ctx := context.Background()
	if _, err := f.k.CallTool(ctx, "a.ok", nil); err != nil {
		t.Fatalf("CallTool(a.ok) 失败: %v", err)
	}
	if _, err := f.k.CallTool(ctx, "a.bad", nil); err == nil {
		t.Fatal("CallTool(a.bad) 应返回错误")
	}
	if _, err := f.k.Invoke(ctx, "m", "run", nil); err != nil {
		t.Fatalf("Invoke(m.run) 失败: %v", err)
	}
	if _, err := f.k.Invoke(ctx, "bm", "run", nil); err == nil {
		t.Fatal("Invoke(bm.run) 应返回错误")
	}

	// 工具成功：plugin=工具归属，risk/elapsed 齐备
	ok := f.audit.lastOf(t, "tool", "a.ok")
	if !ok.OK || ok.Plugin != "a" || ok.Risk != string(plugin.RiskRead) {
		t.Fatalf("工具成功审计不符: %+v", ok)
	}
	if ok.Time.IsZero() {
		t.Fatal("审计事件必须带时间")
	}
	// 工具失败：OK=false + 错误详情
	bad := f.audit.lastOf(t, "tool", "a.bad")
	if bad.OK || !strings.Contains(bad.Detail, "502") {
		t.Fatalf("工具失败审计不符: %+v", bad)
	}
	// 方法成功/失败
	if e := f.audit.lastOf(t, "method", "run"); e.Plugin != "bm" && e.Plugin != "m" {
		t.Fatalf("方法审计插件归属异常: %+v", e)
	}
	if n := f.audit.countIf(func(e contract.AuditEvent) bool {
		return e.Kind == "method" && e.Plugin == "bm" && !e.OK
	}); n != 1 {
		t.Fatalf("失败方法应有 1 条审计，实际 %d", n)
	}
	// 生命周期：三个插件各一条成功激活
	if n := f.audit.countIf(func(e contract.AuditEvent) bool {
		return e.Kind == "lifecycle" && e.Action == "activate" && e.OK
	}); n != 3 {
		t.Fatalf("应有 3 条成功激活审计，实际 %d（%s）", n, f.audit.dump())
	}
	// List 最新在前
	events, err := f.audit.List(2)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("List(2) 应返回 2 条，实际 %d", len(events))
	}
	if events[0].Plugin != "bm" || events[0].OK {
		t.Fatalf("List 应最新在前，实际首条: %+v", events[0])
	}
	all, err := f.audit.List(0)
	if err != nil {
		t.Fatalf("List(0) 失败: %v", err)
	}
	if len(all) != len(f.audit.all()) {
		t.Fatalf("List(0) 应返回全部事件：%d vs %d", len(all), len(f.audit.all()))
	}
}

// ---------- 视图与聚合 ----------

func TestStatusAndAggregation(t *testing.T) {
	f := newKernel(t)
	a := withTools(newPlugin("a"), echoTool("a.x"))
	a.activateFn = func(c *plugin.Context) error {
		if err := c.Tools.Add(echoTool("a.x")); err != nil {
			return err
		}
		if err := c.UI.Add(plugin.UISlot{ID: "a.panel", Title: "面板 A", Component: "a", Order: 20}); err != nil {
			return err
		}
		if err := c.Methods.Add(plugin.Method{Name: "list", Fn: func(context.Context, json.RawMessage) (any, error) {
			return []string{}, nil
		}}); err != nil {
			return err
		}
		return c.Services.Register("svc.a", struct{}{})
	}
	b := withTools(newPlugin("b"), echoTool("b.y"))
	b.activateFn = func(c *plugin.Context) error {
		if err := c.Tools.Add(echoTool("b.y")); err != nil {
			return err
		}
		return c.UI.Add(plugin.UISlot{ID: "b.panel", Title: "面板 B", Component: "b", Order: 10})
	}
	f.register(t, a, b)
	f.start(t)

	st := f.k.Status()
	if !st.Started {
		t.Fatal("Status.Started 应为 true")
	}
	if st.Plugins != 2 || st.Active != 2 {
		t.Fatalf("插件统计不符: %+v", st)
	}
	if st.Tools != 2 {
		t.Fatalf("工具数应为 2，实际 %d", st.Tools)
	}
	if st.UI != 2 {
		t.Fatalf("面板数应为 2，实际 %d", st.UI)
	}
	if len(st.Services) != 1 || st.Services[0] != "svc.a" {
		t.Fatalf("服务列表不符: %v", st.Services)
	}
	if st.Policy.MaxToolCalls != f.guard.Policy().MaxToolCalls {
		t.Fatalf("Status 应透出安全策略: %+v", st.Policy)
	}
	if st.Time.IsZero() {
		t.Fatal("Status 应带时间戳")
	}

	// 工具索引按名称排序，便于 L1 索引稳定输出
	tools := f.k.Tools()
	if len(tools) != 2 || tools[0].Name != "a.x" || tools[1].Name != "b.y" {
		t.Fatalf("工具索引应按名称排序: %+v", tools)
	}
	if _, ok := f.k.Tool("a.x"); !ok {
		t.Fatal("工具索引应能按名称查到 a.x")
	}
	if _, ok := f.k.Tool("nope"); ok {
		t.Fatal("不存在的工具不应命中")
	}

	// 面板按 Order 升序聚合
	slots := f.k.UISlots()
	if len(slots) != 2 {
		t.Fatalf("面板数应为 2，实际 %d", len(slots))
	}
	if slots[0].ID != "b.panel" || slots[1].ID != "a.panel" {
		t.Fatalf("面板应按 Order 升序: %+v", slots)
	}
	if slots[0].Owner != "b" {
		t.Fatalf("面板应带归属插件，实际 %q", slots[0].Owner)
	}

	// 插件视图里能看到自己的工具/面板/方法
	v := f.pluginView(t, "a")
	if len(v.Tools) != 1 || len(v.UI) != 1 || len(v.Methods) != 1 {
		t.Fatalf("插件视图聚合不符: tools=%d ui=%d methods=%d", len(v.Tools), len(v.UI), len(v.Methods))
	}
	if v.Tools[0].Risk != string(plugin.RiskRead) {
		t.Fatalf("插件视图工具应带风险等级，实际 %q", v.Tools[0].Risk)
	}
}

// ---------- 生命周期 ----------

// Stop 逆序停用，且只停已激活的插件。
func TestStopReverseOrder(t *testing.T) {
	f := newKernel(t)
	deacts := newOrderLog()
	p1 := newPlugin("p1")
	p2 := newPlugin("p2")
	p3 := newPlugin("p3")
	broken := newPlugin("broken")
	broken.activateFn = func(*plugin.Context) error { return errors.New("起不来") }
	for _, p := range []*fakePlugin{p1, p2, p3, broken} {
		p.deactFn = func(context.Context) error {
			deacts.add(p.man.ID)
			return nil
		}
	}
	f.register(t, p1, p2, p3, broken)
	f.start(t)

	if err := f.k.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	got := deacts.snapshot()
	want := []string{"p3", "p2", "p1"}
	if len(got) != len(want) {
		t.Fatalf("停用次数不符：期望 %v 实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("应逆序停用 %v，实际 %v", want, got)
		}
	}
	if f.k.Status().Started {
		t.Fatal("Stop 后 Status.Started 应为 false")
	}
	if broken.deactivateCount() != 0 {
		t.Fatal("未激活的插件不应被停用")
	}
}

// 插件配置读写落在 plugin_settings bucket（kernel 统一托管）。
func TestSettingsPersist(t *testing.T) {
	f := newKernel(t)
	var firstRead string
	cfg := newPlugin("cfg")
	cfg.activateFn = func(c *plugin.Context) error {
		firstRead = string(c.Settings())
		return c.SaveSettings(json.RawMessage(`{"model":"deepseek-chat"}`))
	}
	f.register(t, cfg)
	f.start(t)
	if firstRead != "{}" {
		t.Fatalf("首次激活应读到空配置 {}，实际 %q", firstRead)
	}
	raw, err := f.store.Get("plugin_settings", "cfg")
	if err != nil {
		t.Fatalf("配置未落库: %v", err)
	}
	if string(raw) != `{"model":"deepseek-chat"}` {
		t.Fatalf("配置内容不符: %s", raw)
	}

	// 同一个 Store 起第二个 kernel：激活时应读回上次保存的配置
	k2, err := kernel.New(kernel.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:  f.store,
		Guard:  f.guard,
		Audit:  f.audit,
		Clock:  time.Now,
	})
	if err != nil {
		t.Fatalf("kernel.New 失败: %v", err)
	}
	var secondRead string
	cfg2 := newPlugin("cfg")
	cfg2.activateFn = func(c *plugin.Context) error {
		secondRead = string(c.Settings())
		return nil
	}
	if err := k2.Register(cfg2); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	if err := k2.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if secondRead != `{"model":"deepseek-chat"}` {
		t.Fatalf("重启后应读回配置，实际 %q", secondRead)
	}
}
