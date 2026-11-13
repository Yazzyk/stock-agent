package bootstrap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// inventory 取设置中心总览（console 插件是唯一入口）。
func readInventory(t *testing.T, ctx context.Context, k interface {
	Invoke(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
}) map[string]any {
	t.Helper()
	out, err := k.Invoke(ctx, "console", "inventory", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("console.inventory 失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("inventory 不是 JSON 对象: %s", out)
	}
	return m
}

// 启停开关必须落盘 settings/plugins.disabled，并在重启后以"已停用但可见"的形态生效。
func TestPluginTogglePersistsAndSkipsOnRestart(t *testing.T) {
	k, cfg := newTestKernel(t)
	ctx := context.Background()

	inv := readInventory(t, ctx, k)
	if disabled, _ := inv["disabled"].([]any); len(disabled) != 0 {
		t.Fatalf("初始不应有停用插件: %v", disabled)
	}
	if inv["restartRequired"] != false {
		t.Fatalf("初始不应要求重启: %v", inv["restartRequired"])
	}

	payload, _ := json.Marshal(map[string]any{"id": "watch", "enabled": false})
	res, err := k.Invoke(ctx, "console", "setEnabled", payload)
	if err != nil {
		t.Fatalf("setEnabled 失败: %v", err)
	}
	var toggle struct {
		OK              bool     `json:"ok"`
		RestartRequired bool     `json:"restartRequired"`
		Message         string   `json:"message"`
		Disabled        []string `json:"disabled"`
	}
	if err := json.Unmarshal(res, &toggle); err != nil {
		t.Fatalf("setEnabled 返回异常: %s", res)
	}
	if !toggle.OK || !toggle.RestartRequired || toggle.Message == "" {
		t.Fatalf("setEnabled 返回不符: %+v", toggle)
	}
	if len(toggle.Disabled) != 1 || toggle.Disabled[0] != "watch" {
		t.Fatalf("停用列表未回显: %+v", toggle)
	}
	if err := k.Stop(ctx); err != nil {
		t.Fatalf("停止失败: %v", err)
	}

	// 同一个 DataDir 重启：watch 被跳过、仍可见，其它插件照常激活。
	k2, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("二次启动失败: %v", err)
	}
	if err := k2.Start(ctx); err != nil {
		t.Fatalf("二次 kernel 启动失败: %v", err)
	}
	defer func() { _ = k2.Stop(ctx) }()

	st := k2.Status()
	if st.Disabled != 1 {
		t.Fatalf("重启后停用数 = %d, want 1", st.Disabled)
	}
	if st.Active+st.Disabled != st.Plugins {
		t.Fatalf("激活+停用应等于插件总数: %+v", st)
	}
	var sawWatch bool
	for _, p := range k2.Plugins() {
		if p.ID != "watch" {
			continue
		}
		sawWatch = true
		if p.Active {
			t.Fatal("停用的插件重启后不应激活")
		}
		if !p.Skipped || !p.Disabled {
			t.Fatalf("停用插件应标记 skipped+disabled: %+v", p)
		}
		if p.Error == "" {
			t.Fatal("停用插件应给出原因")
		}
	}
	if !sawWatch {
		t.Fatal("停用的插件仍应在列表里可见")
	}
	inv2 := readInventory(t, ctx, k2)
	if disabled, _ := inv2["disabled"].([]any); len(disabled) != 1 || disabled[0] != "watch" {
		t.Fatalf("重启后待停用列表不符: %v", inv2["disabled"])
	}

	// 再启用回来：写盘后下次重启恢复激活。
	payload, _ = json.Marshal(map[string]any{"id": "watch", "enabled": true})
	if _, err := k2.Invoke(ctx, "console", "setEnabled", payload); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	_ = k2.Stop(ctx)
	k3, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("三次启动失败: %v", err)
	}
	if err := k3.Start(ctx); err != nil {
		t.Fatalf("三次 kernel 启动失败: %v", err)
	}
	defer func() { _ = k3.Stop(ctx) }()
	if st := k3.Status(); st.Disabled != 0 || st.Active != st.Plugins {
		t.Fatalf("重新启用后应全部激活: %+v", st)
	}
}

// 停用设置中心会自锁（打不开设置界面就再也改不回来），必须被拒绝。
func TestSetEnabledRefusesToDisableConsole(t *testing.T) {
	k, _ := newTestKernel(t)
	defer func() { _ = k.Stop(context.Background()) }()
	payload, _ := json.Marshal(map[string]any{"id": "console", "enabled": false})
	_, err := k.Invoke(context.Background(), "console", "setEnabled", payload)
	if err == nil {
		t.Fatal("停用设置中心应被拒绝")
	}
	if !strings.Contains(err.Error(), "设置中心") {
		t.Fatalf("错误文案不够可操作: %v", err)
	}
}

// setEnabled 对未知插件要给中文可操作错误，且不得写盘。
func TestSetEnabledRejectsUnknownPlugin(t *testing.T) {
	k, cfg := newTestKernel(t)
	defer func() { _ = k.Stop(context.Background()) }()
	payload, _ := json.Marshal(map[string]any{"id": "no-such-plugin", "enabled": false})
	if _, err := k.Invoke(context.Background(), "console", "setEnabled", payload); err == nil {
		t.Fatal("未知插件应报错")
	}
	// 新实例读到的停用列表必须仍为空。
	k2, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("二次启动失败: %v", err)
	}
	defer func() { _ = k2.Stop(context.Background()) }()
	if err := k2.Start(context.Background()); err != nil {
		t.Fatalf("二次 kernel 启动失败: %v", err)
	}
	if st := k2.Status(); st.Disabled != 0 {
		t.Fatalf("失败调用不应写盘: %+v", st)
	}
}
