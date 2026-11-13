package modelsettings

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/internal/llm"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"

	"log/slog"
	"os"
)

type memStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

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

type noopGuard struct{}

func (noopGuard) AllowHost(string) bool   { return true }
func (noopGuard) CheckPath(string) error  { return nil }
func (noopGuard) Redact(s string) string  { return s }
func (noopGuard) Policy() contract.Policy { return contract.Policy{} }

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

func (a *collectingAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

func newKernelWithLLM(t *testing.T) (*kernel.Kernel, *llm.Service) {
	t.Helper()
	store := &memStore{data: map[string]map[string][]byte{}}
	svc, err := llm.New(llm.Config{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("创建 llm service 失败: %v", err)
	}
	audit := &collectingAudit{}
	k, err := kernel.New(kernel.Options{Store: store, LLM: svc, Guard: noopGuard{}, Audit: audit})
	if err != nil {
		t.Fatalf("创建 kernel 失败: %v", err)
	}
	if err := k.Services().Register("llm", svc); err != nil {
		t.Fatalf("注册 llm 服务失败: %v", err)
	}
	if err := k.Register(New()); err != nil {
		t.Fatalf("注册插件失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	return k, svc
}

func invoke(t *testing.T, k *kernel.Kernel, method string, payload any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	out, err := k.Invoke(context.Background(), "modelsettings", method, raw)
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", method, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("返回不是 JSON 对象: %s", out)
	}
	return m
}

func TestPluginRegistersUIAndMethods(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	var slot *plugin.UISlot
	for _, p := range k.Plugins() {
		if p.ID == "modelsettings" {
			if !p.Active {
				t.Fatalf("插件未激活: %s", p.Error)
			}
			if len(p.Methods) != 12 {
				t.Fatalf("应有 12 个方法（list/save/remove/test/setDefault/presets + WorkBuddy 6 个）, got %d", len(p.Methods))
			}
			wantMethods := map[string]bool{
				"setDefault":      false,
				"workbuddyStatus": false, "workbuddyModels": false, "workbuddyLogin": false,
				"workbuddyLoginPoll": false, "workbuddyImport": false, "workbuddyLogout": false,
			}
			for _, m := range p.Methods {
				if _, ok := wantMethods[m.Name]; ok {
					wantMethods[m.Name] = true
				}
				if m.Description == "" {
					t.Fatalf("方法 %s 缺少中文描述", m.Name)
				}
			}
			for name, seen := range wantMethods {
				if !seen {
					t.Fatalf("缺少方法 %s", name)
				}
			}
			if len(p.UI) == 1 {
				slot = &p.UI[0]
			}
		}
	}
	if slot == nil {
		t.Fatal("缺少 modelsettings 面板")
	}
	if slot.Component != "modelsettings" || slot.Order != 90 || slot.Group != "设置" {
		t.Fatalf("面板声明不符: %+v", slot)
	}
}

func TestListNeverReturnsPlaintextKey(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	list := invoke(t, k, "list", map[string]any{})
	seeded, _ := list["providers"].([]any)
	if len(seeded) != 1 {
		t.Fatalf("默认配置应有一条: %v", list)
	}
	seed := seeded[0].(map[string]any)["id"].(string)
	m := invoke(t, k, "save", map[string]any{
		"id": seed, "provider": "deepseek", "model": "deepseek-chat", "apiKey": "sk-plain-secret",
	})
	blob, _ := json.Marshal(m)
	if strings.Contains(string(blob), "sk-plain-secret") {
		t.Fatalf("返回值泄漏明文 key: %s", blob)
	}
	providers, ok := m["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("providers 形状不符: %v", m)
	}
	first, _ := providers[0].(map[string]any)
	if first["hasKey"] != true {
		t.Fatalf("hasKey 应为 true: %v", first)
	}
	if _, exists := first["apiKey"]; exists {
		t.Fatal("返回值不应包含 apiKey 字段")
	}
	if first["displayName"] == nil || first["model"] == nil {
		t.Fatalf("展平字段缺失: %v", first)
	}
}

// presets 是「新增配置」下拉的数据源：无网络、只读，DeepSeek 官方数值必须与契约一致。
func TestPresetsReturnsOfficialDefaults(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	m := invoke(t, k, "presets", map[string]any{})
	providers, ok := m["providers"].([]any)
	if !ok || len(providers) == 0 {
		t.Fatalf("presets 形状不符: %v", m)
	}
	var deepseek map[string]any
	for _, raw := range providers {
		p, _ := raw.(map[string]any)
		if p["provider"] == "deepseek" {
			deepseek = p
		}
	}
	if deepseek == nil {
		t.Fatalf("缺少 deepseek 预设: %v", providers)
	}
	if deepseek["baseUrl"] != "https://api.deepseek.com" {
		t.Fatalf("baseUrl 不符: %v", deepseek["baseUrl"])
	}
	if deepseek["keyUrl"] == nil || deepseek["docsUrl"] == nil {
		t.Fatalf("缺少 Key/Docs 链接: %v", deepseek)
	}
	models, _ := deepseek["models"].([]any)
	if len(models) == 0 {
		t.Fatalf("deepseek 应有模型预设: %v", deepseek)
	}
	var flash map[string]any
	for _, raw := range models {
		mp, _ := raw.(map[string]any)
		if mp["model"] == "deepseek-flash" {
			flash = mp
		}
	}
	if flash == nil {
		t.Fatalf("缺少 deepseek-flash 预设: %v", models)
	}
	if flash["contextWindow"].(float64) != 1048576 {
		t.Fatalf("contextWindow 应为 1M: %v", flash["contextWindow"])
	}
	if flash["maxOutput"].(float64) != 393216 {
		t.Fatalf("maxOutput 应为 384K: %v", flash["maxOutput"])
	}
	if flash["defaultMaxTokens"].(float64) != 8192 {
		t.Fatalf("defaultMaxTokens 应为 8192: %v", flash["defaultMaxTokens"])
	}
}

func TestSaveRequiresProviderAndRemoveUnknownFails(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	raw, _ := json.Marshal(map[string]any{"model": "x"})
	if _, err := k.Invoke(context.Background(), "modelsettings", "save", raw); err == nil {
		t.Fatal("缺少 provider 应报错")
	}
	raw, _ = json.Marshal(map[string]any{"id": "missing"})
	if _, err := k.Invoke(context.Background(), "modelsettings", "remove", raw); err == nil {
		t.Fatal("删除不存在的配置应报错")
	}
}

func TestTestMethodReturnsFailureShape(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	m := invoke(t, k, "test", map[string]any{})
	if _, exists := m["ok"]; !exists {
		t.Fatalf("test 缺少 ok 字段: %v", m)
	}
	if _, exists := m["message"]; !exists {
		t.Fatalf("test 缺少 message 字段: %v", m)
	}
	if m["ok"] == true {
		t.Fatal("没有 key 时不应测试成功")
	}
}

// workbuddyStatus 只读本地凭证库：未登录时也要返回可操作提示，且绝不回传 token。
func TestWorkBuddyStatusShape(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	m := invoke(t, k, "workbuddyStatus", map[string]any{})
	loggedIn, ok := m["loggedIn"].(bool)
	if !ok {
		t.Fatalf("workbuddyStatus 缺少 loggedIn: %v", m)
	}
	if !loggedIn && m["message"] == "" {
		t.Fatalf("未登录时应给出可操作提示: %v", m)
	}
	if m["vaultPath"] == nil || m["vaultPath"] == "" {
		t.Fatalf("应返回凭证库路径便于排查: %v", m)
	}
	for _, leaked := range []string{"accessToken", "refreshToken", "token"} {
		if _, exists := m[leaked]; exists {
			t.Fatalf("状态里不能出现 %s: %v", leaked, m)
		}
	}
}

// workbuddyLogout 在没有本地凭证时也应成功（幂等），并返回中文提示。
func TestWorkBuddyLogoutIdempotent(t *testing.T) {
	k, _ := newKernelWithLLM(t)
	for i := 0; i < 2; i++ {
		m := invoke(t, k, "workbuddyLogout", map[string]any{})
		if msg, _ := m["message"].(string); msg == "" {
			t.Fatalf("第 %d 次退出缺少提示: %v", i+1, m)
		}
	}
}
