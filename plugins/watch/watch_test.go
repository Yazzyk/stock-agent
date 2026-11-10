package watch

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

type memStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

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
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []contract.KVItem
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: v})
		}
	}
	return out, nil
}
func (s *memStore) Close() error { return nil }

type fakeData struct{}

func (fakeData) Quote(_ context.Context, codes []string) ([]contract.Quote, error) {
	out := make([]contract.Quote, 0, len(codes))
	for _, c := range codes {
		out = append(out, contract.Quote{Code: c, Name: "测试股", Price: 10, ChangePct: 7.2, Amount: 1e9})
	}
	return out, nil
}
func (fakeData) Bars(context.Context, string, contract.Period, int, contract.Adjust) ([]contract.Bar, error) {
	return nil, nil
}
func (fakeData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{MainNet: 1e8, MainPct: 10}, nil
}
func (fakeData) FundFlowRank(context.Context, string, int) ([]contract.FundFlow, error) {
	return nil, nil
}
func (fakeData) Sectors(context.Context, string) ([]contract.Sector, error) { return nil, nil }
func (fakeData) Watchlist(context.Context) ([]contract.WatchItem, error)    { return nil, nil }
func (fakeData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{}, nil
}
func (fakeData) Search(context.Context, string) ([]contract.StockRef, error) { return nil, nil }
func (fakeData) Health(context.Context) map[string]string                    { return nil }

type okGuard struct{}

func (okGuard) AllowHost(string) bool   { return true }
func (okGuard) CheckPath(string) error  { return nil }
func (okGuard) Redact(s string) string  { return s }
func (okGuard) Policy() contract.Policy { return contract.Policy{} }

type noAudit struct{}

func (noAudit) Record(contract.AuditEvent)              {}
func (noAudit) List(int) ([]contract.AuditEvent, error) { return nil, nil }

// fakeAgentlab 只声明 ID，不提供服务，用于验证 watch 在依赖缺失时的失败语义。
type fakeAgentlab struct{ plugin.Base }

func (fakeAgentlab) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "agentlab", Name: "fake", Version: "0.0.0"}
}
func (fakeAgentlab) Activate(*plugin.Context) error { return nil }

// 通过 kernel 装配 watch 插件（dependsOn agentlab 会让它跳过，这里只断言工具与面板声明）。
func TestPluginManifestAndDeclarations(t *testing.T) {
	p := New()
	m := p.Manifest()
	if m.ID != "watch" || len(m.DependsOn) != 1 || m.DependsOn[0] != "agentlab" {
		t.Fatalf("manifest 不符: %+v", m)
	}
	wp, ok := p.(*Plugin)
	if !ok {
		t.Fatal("New 应返回 *Plugin")
	}
	tools := wp.tools()
	if len(tools) != 5 {
		t.Fatalf("工具数 = %d, want 5", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		if err := tl.Validate(); err != nil {
			t.Fatalf("工具 %s 非法: %v", tl.Name, err)
		}
		if len([]rune(tl.Summary)) > 40 {
			t.Fatalf("工具 %s 摘要超 40 字: %d", tl.Name, len([]rune(tl.Summary)))
		}
		names[tl.Name] = true
	}
	for _, want := range []string{"watchdog.list", "watchdog.create", "watchdog.toggle", "watchdog.test", "watchdog.logs"} {
		if !names[want] {
			t.Fatalf("缺少工具 %s", want)
		}
	}
}

// 依赖不满足时插件应明确失败而不是 panic。
func TestActivateWithoutAgentRuntimeFailsClearly(t *testing.T) {
	k, _ := kernel.New(kernel.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:  &memStore{data: map[string]map[string][]byte{}},
		Data:   fakeData{}, Guard: okGuard{}, Audit: noAudit{}, DataDir: t.TempDir(),
	})
	if err := k.Register(fakeAgentlab{}); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(New()); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动不应失败: %v", err)
	}
	defer func() { _ = k.Stop(context.Background()) }()
	var pv *kernel.PluginView
	for _, v := range k.Plugins() {
		if v.ID == "watch" {
			pv = &v
		}
	}
	if pv == nil {
		t.Fatal("未注册 watch 插件")
	}
	if pv.Active {
		t.Fatal("缺少 agent.runtime 时不应激活")
	}
	if !strings.Contains(pv.Error, "agent.runtime") {
		t.Fatalf("错误信息应指向缺失服务，实际: %q", pv.Error)
	}
}
