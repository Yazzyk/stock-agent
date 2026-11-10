package knowledge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// nl 是换行符（用 rune 构造，避免源码转义被工具链改写）。
var nl = string(rune(10))

type memStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.data[bucket][key] = append([]byte{}, val...)
	return nil
}

func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contract.KVItem, 0, len(s.data[bucket]))
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: append([]byte{}, v...)})
		}
	}
	return out, nil
}

func (s *memStore) Close() error { return nil }

func testContext(t *testing.T, store contract.Store) *plugin.Context {
	t.Helper()
	return &plugin.Context{
		ID:       "knowledge",
		Manifest: plugin.Manifest{ID: "knowledge", Name: "知识库", Version: "1.0.0"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:    nil,
		Tools:    plugin.NewToolRegistry("knowledge"),
		UI:       plugin.NewUIRegistry("knowledge"),
		Methods:  plugin.NewMethodRegistry("knowledge"),
		Services: plugin.NewServiceRegistry(),
		Events:   plugin.NewBus(),
		Store:    store,
		Policy:   contract.Policy{},
	}
}

func call(t *testing.T, c *plugin.Context, name string, payload any) any {
	t.Helper()
	m, ok := c.Methods.Get(name)
	if !ok {
		t.Fatalf("方法 %s 未注册", name)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Fn(context.Background(), raw)
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", name, err)
	}
	return out
}

func TestKnowledgeActivateRegistersEverything(t *testing.T) {
	store := newMemStore()
	c := testContext(t, store)
	p := New()
	if err := p.Manifest().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(c); err != nil {
		t.Fatalf("Activate 失败: %v", err)
	}

	for _, name := range []string{"kb.search", "kb.open", "kb.list", "skill.list", "skill.view", "skill.read"} {
		if _, ok := c.Tools.Get(name); !ok {
			t.Fatalf("缺少工具 %s", name)
		}
	}
	for _, name := range []string{"list", "search", "open", "import", "skills", "skill"} {
		if _, ok := c.Methods.Get(name); !ok {
			t.Fatalf("缺少方法 %s", name)
		}
	}
	if _, ok := c.Services.Get("kb.store"); !ok {
		t.Fatal("必须注册服务 kb.store")
	}
	slots := c.UI.List()
	if len(slots) != 1 || slots[0].ID != "knowledge" || slots[0].Component != "knowledge" ||
		slots[0].Title != "知识库" || slots[0].Group != "研究" || slots[0].Order != 50 {
		t.Fatalf("UI slot 不符合 API.md：%+v", slots)
	}
}

func TestKnowledgeImportSearchOpenFlow(t *testing.T) {
	store := newMemStore()
	c := testContext(t, store)
	if err := New().Activate(c); err != nil {
		t.Fatal(err)
	}

	imported := call(t, c, "import", map[string]any{
		"title":   "涨停板战法",
		"content": "# 涨停板战法" + nl + nl + "涨停后的分歧转一致是短线核心买点，需要看封单强度。",
		"tags":    []string{"tech"},
	})
	buf, _ := json.Marshal(imported)
	if !strings.Contains(string(buf), "chars") {
		t.Fatalf("import 应返回 id/title/chars，实际: %s", string(buf))
	}

	searched := call(t, c, "search", map[string]any{"query": "分歧转一致"})
	buf, _ = json.Marshal(searched)
	if !strings.Contains(string(buf), "涨停板战法") {
		t.Fatalf("search 应命中刚导入的文档，实际: %s", string(buf))
	}

	var parsed struct {
		Hits []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(buf, &parsed); err != nil || len(parsed.Hits) == 0 {
		t.Fatalf("search 返回结构不对: %s", string(buf))
	}
	opened := call(t, c, "open", map[string]any{"id": parsed.Hits[0].ID})
	buf, _ = json.Marshal(opened)
	if !strings.Contains(string(buf), "封单强度") {
		t.Fatalf("open 应返回全文，实际: %s", string(buf))
	}

	listed := call(t, c, "list", map[string]any{"limit": 10})
	buf, _ = json.Marshal(listed)
	if !strings.Contains(string(buf), "涨停板战法") {
		t.Fatalf("list 应列出文档，实际: %s", string(buf))
	}
}

func TestKnowledgeImportDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# 情绪周期"+nl+nl+"启动期看分歧，加速期看补涨。"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("# 仓位管理"+nl+nl+"单票不超过三成。"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	c := testContext(t, store)
	if err := New().Activate(c); err != nil {
		t.Fatal(err)
	}
	out := call(t, c, "import", map[string]any{"dir": dir, "tags": []string{"content"}})
	buf, _ := json.Marshal(out)
	if !strings.Contains(string(buf), "imported") {
		t.Fatalf("目录导入应返回统计，实际: %s", string(buf))
	}
	listed := call(t, c, "list", map[string]any{})
	buf, _ = json.Marshal(listed)
	if !strings.Contains(string(buf), "情绪周期") || !strings.Contains(string(buf), "仓位管理") {
		t.Fatalf("目录导入应写入两篇文档，实际: %s", string(buf))
	}
	// 再次导入应全部跳过（mtime/size 未变）。
	again := call(t, c, "import", map[string]any{"dir": dir})
	buf, _ = json.Marshal(again)
	if !strings.Contains(string(buf), "\"skipped\":2") {
		t.Fatalf("二次导入应跳过 2 个文件，实际: %s", string(buf))
	}
}

func TestKnowledgeImportRequiresContentOrDir(t *testing.T) {
	c := testContext(t, newMemStore())
	if err := New().Activate(c); err != nil {
		t.Fatal(err)
	}
	m, _ := c.Methods.Get("import")
	if _, err := m.Fn(context.Background(), json.RawMessage("{}")); err == nil {
		t.Fatal("既无 content 也无 dir 时必须报错")
	}
	if _, err := m.Fn(context.Background(), json.RawMessage("not-json")); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
}

func TestKnowledgeSkillsAndMissingSkill(t *testing.T) {
	c := testContext(t, newMemStore())
	if err := New().Activate(c); err != nil {
		t.Fatal(err)
	}
	out := call(t, c, "skills", map[string]any{})
	buf, _ := json.Marshal(out)
	if !strings.Contains(string(buf), "skills") {
		t.Fatalf("skills 应返回 skills 字段，实际: %s", string(buf))
	}
	view, _ := c.Methods.Get("skill")
	if _, err := view.Fn(context.Background(), json.RawMessage(`{"name":"不存在的技能"}`)); err == nil {
		t.Fatal("不存在的技能必须报错")
	}
}
