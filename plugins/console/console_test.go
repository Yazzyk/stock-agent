package console

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- 测试替身 ----------

type memStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.data[bucket][key] = append([]byte(nil), val...)
	return nil
}

func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.KVItem, 0, len(s.data[bucket]))
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: append([]byte(nil), v...)})
		}
	}
	return out, nil
}

func (s *memStore) Close() error { return nil }

// guardWithHome 的策略里带家目录路径，用来验证 console 的路径脱敏。
type guardWithHome struct{ home string }

func (g guardWithHome) AllowHost(string) bool  { return true }
func (g guardWithHome) CheckPath(string) error { return nil }
func (g guardWithHome) Redact(s string) string { return s }
func (g guardWithHome) Policy() contract.Policy {
	return contract.Policy{
		NetworkAllowlist: []string{"d.10jqka.com.cn", "gtimg.cn", "sina.com.cn"},
		PathRoots:        []string{g.home, g.home + string(os.PathSeparator) + ".guanlan"},
		AllowExec:        false,
		MaxReqPerMin:     120,
	}
}

type collectingAudit struct {
	mu     sync.Mutex
	events []contract.AuditEvent
}

func (a *collectingAudit) Record(e contract.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *collectingAudit) List(int) ([]contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]contract.AuditEvent(nil), a.events...), nil
}

// fakeInventory 是 kernel.Inventory 的替身：console 只依赖接口，不依赖 bootstrap。
type fakeInventory struct {
	mu       sync.Mutex
	disabled []string
	restart  bool
	calls    []string
}

func (f *fakeInventory) Plugins() []kernel.PluginView {
	return []kernel.PluginView{
		{ID: "console", Name: "设置中心", Version: "0.1.0", Active: true, Methods: []kernel.MethodView{{Name: "inventory"}}},
		{
			// 故意把可选字段都填上，用来钉死前端读取的 JSON 字段名。
			ID: "watch", Name: "自选", Version: "0.1.0", Description: "自选股", Author: "guanlan",
			DependsOn: []string{"agentlab"}, Tags: []string{"行情"},
			Active: false, Skipped: true, Disabled: true, Error: kernel.DisabledReason,
			UI:      []plugin.UISlot{{ID: "watch", Title: "自选", Component: "watch", Order: 10}},
			Methods: []kernel.MethodView{{Name: "list"}, {Name: "sync"}},
		},
	}
}

func (f *fakeInventory) Status() kernel.StatusView {
	return kernel.StatusView{Started: true, Plugins: 2, Active: 2, Tools: 3, UI: 2, Services: []string{"kernel.inventory", "llm"}}
}

func (f *fakeInventory) Tools() []kernel.ToolView {
	return []kernel.ToolView{{Name: "watch.list", Title: "自选列表", Summary: "读取自选股", Risk: "read", Owner: "watch"}}
}

func (f *fakeInventory) Disabled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.disabled...)
}

func (f *fakeInventory) SetEnabled(id string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	f.restart = true
	if enabled {
		next := make([]string, 0, len(f.disabled))
		for _, v := range f.disabled {
			if v != id {
				next = append(next, v)
			}
		}
		f.disabled = next
		return nil
	}
	f.disabled = append(f.disabled, id)
	return nil
}

func (f *fakeInventory) RestartRequired() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restart
}

func newKernelWithInventory(t *testing.T) (*kernel.Kernel, *fakeInventory, *collectingAudit) {
	t.Helper()
	inv := &fakeInventory{}
	audit := &collectingAudit{}
	home, _ := os.UserHomeDir()
	k, err := kernel.New(kernel.Options{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Store:  newMemStore(),
		Guard:  guardWithHome{home: home},
		Audit:  audit,
	})
	if err != nil {
		t.Fatalf("创建 kernel 失败: %v", err)
	}
	if err := k.Services().Register(serviceName, inv); err != nil {
		t.Fatalf("注册设置中心服务失败: %v", err)
	}
	if err := k.Register(New()); err != nil {
		t.Fatalf("注册插件失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	return k, inv, audit
}

func invoke(t *testing.T, k *kernel.Kernel, method string, payload any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	out, err := k.Invoke(context.Background(), "console", method, raw)
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", method, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("返回不是 JSON 对象: %s", out)
	}
	return m
}

// ---------- 测试 ----------

func TestPluginRegistersPanelAndMethods(t *testing.T) {
	k, _, _ := newKernelWithInventory(t)
	var found bool
	for _, p := range k.Plugins() {
		if p.ID != "console" {
			continue
		}
		found = true
		if !p.Active {
			t.Fatalf("插件未激活: %s", p.Error)
		}
		// 东财登录态的三个方法（emCookieStatus/emCookieCapture/emCookieSave）已随东财源一起下线；
		// 官方数据源配置新增 fuyao/fuyaoSave/fuyaoTest 三个方法。
		if len(p.Methods) != 6 {
			t.Fatalf("应有 6 个方法（inventory/setEnabled/policy/fuyao/fuyaoSave/fuyaoTest），got %d", len(p.Methods))
		}
		wantMethods := map[string]bool{
			"inventory": false, "setEnabled": false, "policy": false,
			"fuyao": false, "fuyaoSave": false, "fuyaoTest": false,
		}
		for _, m := range p.Methods {
			if _, ok := wantMethods[m.Name]; ok {
				wantMethods[m.Name] = true
			}
		}
		for name, seen := range wantMethods {
			if !seen {
				t.Fatalf("缺少方法 %s", name)
			}
		}
		if len(p.UI) != 1 {
			t.Fatalf("应有 1 个面板, got %d", len(p.UI))
		}
		slot := p.UI[0]
		if slot.ID != "console" || slot.Component != "console" || slot.Group != "系统" || slot.Order != 95 {
			t.Fatalf("面板声明不符: %+v", slot)
		}
		if !strings.Contains(slot.Title, "设置中心") {
			t.Fatalf("面板标题不符: %q", slot.Title)
		}
	}
	if !found {
		t.Fatal("console 插件未注册")
	}
}

func TestInventoryShape(t *testing.T) {
	k, _, audit := newKernelWithInventory(t)
	audit.Record(contract.AuditEvent{Plugin: "watch", Kind: "method", Action: "list", OK: true})
	audit.Record(contract.AuditEvent{Plugin: "fundflow", Kind: "method", Action: "rank", OK: false, Detail: "上游超时"})

	m := invoke(t, k, "inventory", map[string]any{})
	for _, key := range []string{"plugins", "status", "tools", "disabled", "restartRequired", "audit", "protected"} {
		if _, exists := m[key]; !exists {
			t.Fatalf("inventory 缺少字段 %q: %v", key, m)
		}
	}
	plugins, _ := m["plugins"].([]any)
	if len(plugins) != 2 {
		t.Fatalf("插件数 = %d, want 2", len(plugins))
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("工具数 = %d, want 1", len(tools))
	}
	first, _ := tools[0].(map[string]any)
	if first["owner"] != "watch" {
		t.Fatalf("工具应带来源插件: %v", first)
	}
	status, _ := m["status"].(map[string]any)
	if status["started"] != true {
		t.Fatalf("status 不符: %v", status)
	}
	// kernel 自身也会写 lifecycle 审计，这里只核对本用例手工写入的两条业务事件。
	events, _ := m["audit"].([]any)
	biz := 0
	for _, ev := range events {
		rec, _ := ev.(map[string]any)
		if rec["kind"] == "method" {
			biz++
		}
	}
	if biz != 2 {
		t.Fatalf("业务审计条数 = %d, want 2（总 %d 条）", biz, len(events))
	}
	if m["restartRequired"] != false {
		t.Fatalf("初始不应要求重启: %v", m["restartRequired"])
	}
}

// console.inventory 的 plugins 沿用 kernel.PluginView 的 camelCase 字段；
// 前端按这些名字取数，改名必须先让这条测试红。
func TestInventoryJSONFieldNames(t *testing.T) {
	k, _, _ := newKernelWithInventory(t)
	raw, err := k.Invoke(context.Background(), "console", "inventory", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("inventory 失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("inventory 不是 JSON 对象: %s", raw)
	}
	if err := assertKeys(m, []string{"plugins", "status", "tools", "disabled", "restartRequired", "audit", "protected"}); err != nil {
		t.Fatalf("顶层字段: %v", err)
	}

	plugins, _ := m["plugins"].([]any)
	if len(plugins) != 2 {
		t.Fatalf("插件数 = %d, want 2", len(plugins))
	}
	watch, _ := plugins[1].(map[string]any)
	want := []string{
		"id", "name", "version", "description", "author", "dependsOn", "tags",
		"active", "skipped", "disabled", "error", "tools", "ui", "methods",
	}
	if err := assertKeys(watch, want); err != nil {
		t.Fatalf("plugin 字段: %v", err)
	}
	ui, _ := watch["ui"].([]any)
	if len(ui) != 1 {
		t.Fatalf("ui 条数不符: %v", watch["ui"])
	}
	slot, _ := ui[0].(map[string]any)
	// ui slot 的 id/title/component/order 必有；icon/group/owner 是 omitempty。
	slotAllowed := []string{"id", "title", "icon", "group", "component", "order", "owner"}
	for _, k := range []string{"id", "title", "component", "order"} {
		if _, ok := slot[k]; !ok {
			t.Fatalf("ui slot 缺少字段 %q: %v", k, slot)
		}
	}
	for k := range slot {
		if !containsStr(slotAllowed, k) {
			t.Fatalf("ui slot 出现契约外字段 %q", k)
		}
	}
	if watch["error"] != kernel.DisabledReason {
		t.Fatalf("停用原因未透出: %v", watch["error"])
	}
	if snake := findSnakeCase(m); snake != "" {
		t.Fatalf("出现 snake_case 字段: %q", snake)
	}
}

// assertKeys 要求必有字段齐全，且不出现契约外字段。
func assertKeys(obj map[string]any, want []string) error {
	allowed := map[string]bool{}
	for _, k := range want {
		allowed[k] = true
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("缺少字段 %q", k)
		}
	}
	for k := range obj {
		if !allowed[k] {
			return fmt.Errorf("出现契约外字段 %q", k)
		}
	}
	return nil
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func findSnakeCase(v any) string {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			if strings.Contains(k, "_") {
				return k
			}
			if got := findSnakeCase(sub); got != "" {
				return got
			}
		}
	case []any:
		for _, sub := range t {
			if got := findSnakeCase(sub); got != "" {
				return got
			}
		}
	}
	return ""
}

func TestSetEnabledReturnsRestartHint(t *testing.T) {
	k, inv, _ := newKernelWithInventory(t)
	m := invoke(t, k, "setEnabled", map[string]any{"id": "watch", "enabled": false})
	if m["ok"] != true {
		t.Fatalf("ok 应为 true: %v", m)
	}
	if m["restartRequired"] != true {
		t.Fatalf("应提示需要重启: %v", m)
	}
	msg, _ := m["message"].(string)
	if !strings.Contains(msg, "重启") {
		t.Fatalf("提示文案应可操作（含「重启」）: %q", msg)
	}
	disabled, _ := m["disabled"].([]any)
	if len(disabled) != 1 || disabled[0] != "watch" {
		t.Fatalf("disabled 应回显: %v", m["disabled"])
	}
	if len(inv.calls) != 1 || inv.calls[0] != "watch" {
		t.Fatalf("应把开关转交给 inventory 服务: %v", inv.calls)
	}
}

func TestSetEnabledRequiresID(t *testing.T) {
	k, _, _ := newKernelWithInventory(t)
	raw, _ := json.Marshal(map[string]any{"enabled": false})
	if _, err := k.Invoke(context.Background(), "console", "setEnabled", raw); err == nil {
		t.Fatal("缺少 id 应报错")
	} else if !strings.Contains(err.Error(), "插件 id") {
		t.Fatalf("错误文案不符: %v", err)
	}
}

func TestPolicyMasksHomePath(t *testing.T) {
	k, _, _ := newKernelWithInventory(t)
	m := invoke(t, k, "policy", map[string]any{})
	roots, _ := m["pathRoots"].([]any)
	if len(roots) != 2 {
		t.Fatalf("pathRoots 数目不符: %v", m["pathRoots"])
	}
	for _, r := range roots {
		s, _ := r.(string)
		if !strings.HasPrefix(s, "~") {
			t.Fatalf("路径未脱敏: %q", s)
		}
	}
	allow, _ := m["networkAllowlist"].([]any)
	if len(allow) != 3 {
		t.Fatalf("白名单应保留: %v", m["networkAllowlist"])
	}
	for _, rule := range []string{"d.10jqka.com.cn", "gtimg.cn", "sina.com.cn"} {
		found := false
		for _, item := range allow {
			if item == rule {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("白名单应包含 %q，实际 %v", rule, m["networkAllowlist"])
		}
	}
	// 东财已从数据层移除，白名单里不能再出现它 —— 这是「彻底移除」的最后一道校验。
	for _, item := range allow {
		if s, _ := item.(string); strings.Contains(s, "eastmoney") {
			t.Fatalf("白名单不应再含 eastmoney，实际 %v", m["networkAllowlist"])
		}
	}
	if m["allowExec"] != false {
		t.Fatalf("allowExec 应原样展示: %v", m["allowExec"])
	}
}
