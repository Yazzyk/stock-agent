package test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- Manifest / Tool 校验 ----------

func TestManifestValidate(t *testing.T) {
	cases := []struct {
		name string
		man  plugin.Manifest
		ok   bool
		want string
	}{
		{"完整", plugin.Manifest{ID: "a", Name: "A", Version: "1.0.0"}, true, ""},
		{"缺 ID", plugin.Manifest{Name: "A", Version: "1"}, false, "id"},
		{"ID 只有空白", plugin.Manifest{ID: "   ", Name: "A", Version: "1"}, false, "id"},
		{"缺 Name", plugin.Manifest{ID: "a", Version: "1"}, false, "name"},
		{"缺 Version", plugin.Manifest{ID: "a", Name: "A"}, false, "version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.man.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("应通过校验，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应校验失败: %+v", tc.man)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("错误信息应提到 %q，实际: %v", tc.want, err)
			}
		})
	}
}

func TestToolValidate(t *testing.T) {
	run := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	cases := []struct {
		name string
		tool plugin.Tool
		ok   bool
		want string
	}{
		{"完整", plugin.Tool{Name: "a.b", Summary: "摘要", Risk: plugin.RiskRead, Run: run}, true, ""},
		{"缺 Name", plugin.Tool{Summary: "摘要", Risk: plugin.RiskRead, Run: run}, false, "name"},
		{"缺 Summary（L1 披露需要）", plugin.Tool{Name: "a.b", Risk: plugin.RiskRead, Run: run}, false, "summary"},
		{"缺 Run", plugin.Tool{Name: "a.b", Summary: "摘要", Risk: plugin.RiskRead}, false, "run"},
		{"缺 Risk", plugin.Tool{Name: "a.b", Summary: "摘要", Run: run}, false, "risk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tool.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("应通过校验，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应校验失败: %+v", tc.tool)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("错误信息应提到 %q，实际: %v", tc.want, err)
			}
		})
	}
}

// ---------- 四类注册表 ----------

func TestToolRegistry(t *testing.T) {
	reg := plugin.NewToolRegistry("owner-a")
	run := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }

	t1 := plugin.Tool{Name: "b.tool", Summary: "工具 B", Risk: plugin.RiskRead, Run: run}
	if err := reg.Add(t1); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	stored, ok := reg.Get("b.tool")
	if !ok {
		t.Fatal("Get 应命中刚注册的工具")
	}
	if stored.Owner != "owner-a" {
		t.Fatalf("工具应自动打上归属，实际 %q", stored.Owner)
	}
	if stored.Title != "b.tool" {
		t.Fatalf("Title 缺省应回落到 Name，实际 %q", stored.Title)
	}
	if err := reg.Add(t1); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复注册应报错，实际: %v", err)
	}
	// 排序稳定，便于 L1 索引输出可复现
	if err := reg.Add(plugin.Tool{Name: "a.tool", Summary: "工具 A", Risk: plugin.RiskRead, Run: run}); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	list := reg.List()
	if len(list) != 2 || list[0].Name != "a.tool" || list[1].Name != "b.tool" {
		t.Fatalf("List 应按名称排序: %+v", list)
	}
	if _, ok := reg.Get("nope"); ok {
		t.Fatal("未注册的工具不应命中")
	}
}

func TestUIRegistry(t *testing.T) {
	reg := plugin.NewUIRegistry("owner-a")
	if err := reg.Add(plugin.UISlot{ID: "x", Title: "X"}); err == nil {
		t.Fatal("缺 Component 应报错")
	}
	if err := reg.Add(plugin.UISlot{Component: "x"}); err == nil {
		t.Fatal("缺 ID 应报错")
	}
	if err := reg.Add(plugin.UISlot{ID: "p2", Title: "P2", Component: "c2", Order: 20}); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	if err := reg.Add(plugin.UISlot{ID: "p1", Title: "P1", Component: "c1", Order: 10}); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	if err := reg.Add(plugin.UISlot{ID: "p1", Title: "dup", Component: "c1"}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复面板应报错，实际: %v", err)
	}
	list := reg.List()
	if len(list) != 2 || list[0].ID != "p1" || list[1].ID != "p2" {
		t.Fatalf("面板应按 Order 升序: %+v", list)
	}
	if list[0].Owner != "owner-a" {
		t.Fatalf("面板应带归属，实际 %q", list[0].Owner)
	}
}

func TestMethodRegistry(t *testing.T) {
	reg := plugin.NewMethodRegistry("owner-a")
	fn := func(context.Context, json.RawMessage) (any, error) { return 1, nil }
	if err := reg.Add(plugin.Method{Name: "list"}); err == nil {
		t.Fatal("缺 Fn 应报错")
	}
	if err := reg.Add(plugin.Method{Fn: fn}); err == nil {
		t.Fatal("缺 Name 应报错")
	}
	if err := reg.Add(plugin.Method{Name: "sync", Fn: fn}); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	if err := reg.Add(plugin.Method{Name: "sync", Fn: fn}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复方法应报错，实际: %v", err)
	}
	m, ok := reg.Get("sync")
	if !ok {
		t.Fatal("Get 应命中刚注册的方法")
	}
	if m.Owner != "owner-a" {
		t.Fatalf("方法应带归属，实际 %q", m.Owner)
	}
	if err := reg.Add(plugin.Method{Name: "add", Fn: fn}); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
	list := reg.List()
	if len(list) != 2 || list[0].Name != "add" || list[1].Name != "sync" {
		t.Fatalf("方法应按名称排序: %+v", list)
	}
}

func TestServiceRegistry(t *testing.T) {
	reg := plugin.NewServiceRegistry()
	if err := reg.Register("", struct{}{}); err == nil {
		t.Fatal("空服务名应报错")
	}
	if err := reg.Register("svc", nil); err == nil {
		t.Fatal("nil 实现应报错")
	}
	if err := reg.Register("svc.b", "impl-b"); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	if err := reg.Register("svc.a", "impl-a"); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	if err := reg.Register("svc.a", "dup"); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复服务应报错，实际: %v", err)
	}
	names := reg.Names()
	if len(names) != 2 || names[0] != "svc.a" || names[1] != "svc.b" {
		t.Fatalf("服务名应排序: %v", names)
	}
	if v, ok := reg.Get("svc.a"); !ok || v.(string) != "impl-a" {
		t.Fatalf("Get 结果不符: %v %v", v, ok)
	}
}

// ---------- 风险等级 ----------

func TestRiskLevelAtMost(t *testing.T) {
	cases := []struct {
		risk  plugin.RiskLevel
		limit plugin.RiskLevel
		want  bool
	}{
		{plugin.RiskRead, plugin.RiskRead, true},
		{plugin.RiskNetwork, plugin.RiskRead, false},
		{plugin.RiskNetwork, plugin.RiskWrite, true},
		{plugin.RiskWrite, plugin.RiskNetwork, false},
		{plugin.RiskExec, plugin.RiskWrite, false},
		{plugin.RiskExec, plugin.RiskExec, true},
		{plugin.RiskLevel("unknown"), plugin.RiskExec, false},
		{plugin.RiskRead, plugin.RiskLevel("unknown"), false},
	}
	for _, tc := range cases {
		if got := tc.risk.AtMost(tc.limit); got != tc.want {
			t.Fatalf("%q.AtMost(%q) = %v，期望 %v", tc.risk, tc.limit, got, tc.want)
		}
	}
}

// ---------- 事件总线 ----------

func TestBusPublishSubscribe(t *testing.T) {
	bus := plugin.NewBus()
	recv := make(chan plugin.Event, 4)
	unsub := bus.Subscribe("watch.updated", func(e plugin.Event) { recv <- e })

	bus.Publish(plugin.Event{Topic: "watch.updated", Payload: json.RawMessage(`{"n":1}`)})
	select {
	case e := <-recv:
		if e.Topic != "watch.updated" || !strings.Contains(string(e.Payload), "1") {
			t.Fatalf("事件内容不符: %+v", e)
		}
		if e.Time.IsZero() {
			t.Fatal("事件应自动带时间戳")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内未收到事件")
	}

	// 取消订阅后不再收到
	unsub()
	bus.Publish(plugin.Event{Topic: "watch.updated"})
	select {
	case <-recv:
		t.Fatal("取消订阅后不应再收到事件")
	case <-time.After(100 * time.Millisecond):
	}

	// 通配订阅能收到任意 topic
	all := make(chan plugin.Event, 1)
	bus.Subscribe("*", func(e plugin.Event) { all <- e })
	bus.Publish(plugin.Event{Topic: "kernel.plugin.activated"})
	select {
	case e := <-all:
		if e.Topic != "kernel.plugin.activated" {
			t.Fatalf("通配订阅话题不符: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("通配订阅 2s 内未收到事件")
	}
}
