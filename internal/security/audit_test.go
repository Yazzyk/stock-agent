package security

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// memStore 是测试用内存 Store。
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

func TestAuditAppendAndListOrder(t *testing.T) {
	a := NewAudit(newMemStore())
	base := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		a.Record(contract.AuditEvent{
			Time:   base.Add(time.Duration(i) * time.Second),
			Plugin: "agentlab",
			Kind:   "tool",
			Action: fmt.Sprintf("call-%d", i),
			OK:     true,
		})
	}
	got, err := a.List(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("List(2) 条数=%d, want 2", len(got))
	}
	if got[0].Action != "call-2" || got[1].Action != "call-1" {
		t.Fatalf("List 必须倒序（最新在前），实际: %s,%s", got[0].Action, got[1].Action)
	}
	all, err := a.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[2].Action != "call-0" {
		t.Fatalf("全量应为 3 条且最旧在末尾，实际 %d 条", len(all))
	}
}

func TestAuditAppendOnlySequence(t *testing.T) {
	store := newMemStore()
	a := NewAudit(store)
	a.Record(contract.AuditEvent{Plugin: "p", Kind: "k", Action: "first"})

	// 模拟重启：新实例必须从已有序号之后继续追加，不能覆盖旧记录。
	b := NewAudit(store)
	b.Record(contract.AuditEvent{Plugin: "p", Kind: "k", Action: "second"})

	items, err := store.List("audit", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("append-only 应保留 2 条，实际 %d", len(items))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if items[0].Key >= items[1].Key {
		t.Fatalf("key 必须严格递增: %s >= %s", items[0].Key, items[1].Key)
	}
	var last contract.AuditEvent
	if err := json.Unmarshal(items[1].Value, &last); err != nil {
		t.Fatal(err)
	}
	if last.Action != "second" {
		t.Fatalf("最大序号记录应为 second，实际 %q", last.Action)
	}
}

func TestAuditConcurrent(t *testing.T) {
	store := newMemStore()
	a := NewAudit(store)
	const n = 60
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a.Record(contract.AuditEvent{Plugin: "p", Kind: "concurrent", Action: fmt.Sprintf("a-%d", i)})
		}(i)
	}
	wg.Wait()

	got, err := a.List(n)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("并发写入应有 %d 条，实际 %d", n, len(got))
	}
	seen := map[string]bool{}
	for _, e := range got {
		if seen[e.Action] {
			t.Fatalf("审计出现重复记录: %s", e.Action)
		}
		seen[e.Action] = true
	}
}

func TestAuditListLimitBounds(t *testing.T) {
	a := NewAudit(nil) // 纯内存兜底
	for i := 0; i < 5; i++ {
		a.Record(contract.AuditEvent{Action: fmt.Sprintf("x-%d", i)})
	}
	got, err := a.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("limit<=0 应使用默认值并返回全部 5 条，实际 %d", len(got))
	}
	if got[0].Action != "x-4" {
		t.Fatalf("倒序首条应为最新 x-4，实际 %s", got[0].Action)
	}
	if got, _ := a.List(1000); len(got) != 5 {
		t.Fatalf("limit 超上限时仍返回全部，实际 %d", len(got))
	}
}
