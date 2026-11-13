package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// 停用插件在 Start 时被 markSkipped：可见、不可用、不贡献工具/面板。
func TestDisabledPluginIsSkippedButVisible(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "keep", tools: []plugin.Tool{echoTool("keep.echo")}}); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(&fakePlugin{id: "off", tools: []plugin.Tool{echoTool("off.echo")}}); err != nil {
		t.Fatal(err)
	}
	if err := k.SetDisabled([]string{"off"}); err != nil {
		t.Fatalf("SetDisabled 失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	views := k.Plugins()
	if len(views) != 2 {
		t.Fatalf("停用后插件仍应可见, got %d", len(views))
	}
	for _, v := range views {
		switch v.ID {
		case "off":
			if v.Active {
				t.Fatal("停用插件不应激活")
			}
			if !v.Skipped || !v.Disabled {
				t.Fatalf("停用插件应标记 skipped+disabled: %+v", v)
			}
			if !strings.Contains(v.Error, "已停用") || !strings.Contains(v.Error, "重启") {
				t.Fatalf("停用原因不够可操作: %q", v.Error)
			}
		case "keep":
			if !v.Active {
				t.Fatalf("未停用插件应激活: %+v", v)
			}
		}
	}
	if _, ok := k.Tool("off.echo"); ok {
		t.Fatal("停用插件不应贡献工具")
	}
	if _, ok := k.Tool("keep.echo"); !ok {
		t.Fatal("其它插件的工具应正常注册")
	}
	if got := k.Disabled(); len(got) != 1 || got[0] != "off" {
		t.Fatalf("Disabled() = %v, want [off]", got)
	}
	if st := k.Status(); st.Active != 1 || st.Disabled != 1 || st.Plugins != 2 {
		t.Fatalf("状态统计不符: %+v", st)
	}
	if len(k.UISlots()) != 0 {
		t.Fatal("停用插件不应贡献面板")
	}
}

// 依赖被停用时，下游插件要给出"依赖已停用"，而不是含糊的"依赖未激活"。
func TestDisabledDependencyReasonsAreDistinct(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "base"}); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(&fakePlugin{id: "child", deps: []string{"base"}}); err != nil {
		t.Fatal(err)
	}
	if err := k.SetDisabled([]string{"base"}); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	for _, v := range k.Plugins() {
		if v.ID != "child" {
			continue
		}
		if v.Active {
			t.Fatal("依赖停用的插件不应激活")
		}
		if !strings.Contains(v.Error, "依赖已停用: base") {
			t.Fatalf("原因文案不符: %q", v.Error)
		}
	}
}

// 运行时停用会留下悬空贡献，因此只允许 Start 之前设置。
func TestSetDisabledAfterStartIsRejected(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := k.SetDisabled([]string{"a"}); err == nil {
		t.Fatal("启动后再设置停用应当报错")
	}
}

// 设置中心直接渲染 PluginView：tools/ui/methods 必须是数组（空也为 []），不能是 null。
func TestPluginViewEmptyCollectionsAreArrays(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "bare"}); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(k.Plugins()[0])
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for _, key := range []string{"tools", "ui", "methods"} {
		if _, ok := m[key].([]any); !ok {
			t.Fatalf("%s 应为数组（空也要是 []）: %v", key, m[key])
		}
	}
}

// 设置中心需要工具的来源插件；ToolViews 要带 owner 且按名称排序。
func TestToolViewsCarryOwner(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "p2", tools: []plugin.Tool{echoTool("b.two")}}); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(&fakePlugin{id: "p1", tools: []plugin.Tool{echoTool("a.one")}}); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	views := k.ToolViews()
	if len(views) != 2 {
		t.Fatalf("工具数 = %d, want 2", len(views))
	}
	if views[0].Name != "a.one" || views[0].Owner != "p1" {
		t.Fatalf("第一条工具不符: %+v", views[0])
	}
	if views[1].Name != "b.two" || views[1].Owner != "p2" {
		t.Fatalf("第二条工具不符: %+v", views[1])
	}
}
