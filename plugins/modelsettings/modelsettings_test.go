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
			if len(p.Methods) != 4 {
				t.Fatalf("应有 4 个方法, got %d", len(p.Methods))
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
