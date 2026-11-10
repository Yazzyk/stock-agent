package datasource

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fakeGuard 是测试用的安全策略：默认放行全部主机与路径，可显式拒绝某些主机。
type fakeGuard struct {
	mu        sync.Mutex
	denyHosts map[string]bool
	denyPaths []string
}

func newFakeGuard() *fakeGuard {
	return &fakeGuard{denyHosts: map[string]bool{}}
}

func (g *fakeGuard) AllowHost(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.Index(h, ":"); i > 0 {
		h = h[:i]
	}
	return !g.denyHosts[h]
}

func (g *fakeGuard) CheckPath(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	for _, denied := range g.denyPaths {
		if strings.HasPrefix(abs, denied) {
			return errors.New("fake guard: 路径被拒绝")
		}
	}
	return nil
}

func (g *fakeGuard) Redact(s string) string { return s }

func (g *fakeGuard) Policy() contract.Policy {
	return contract.Policy{NetworkAllowlist: []string{"*"}, PathRoots: []string{os.TempDir()}, MaxReqPerMin: 600}
}

// memStore 是内存版 contract.Store，行为与 sqlite 版对齐（含 ErrNotFound 与按 key 升序的 List）。
type memStore struct {
	mu     sync.Mutex
	data   map[string]map[string][]byte
	closed bool
}

func newMemStore() *memStore {
	return &memStore{data: map[string]map[string][]byte{}}
}

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bucketData, ok := s.data[bucket]; ok {
		if value, ok := bucketData[key]; ok {
			return value, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.KVItem, 0, 8)
	for key, value := range s.data[bucket] {
		if strings.HasPrefix(key, prefix) {
			out = append(out, contract.KVItem{Key: key, Value: value})
		}
	}
	// 与 sqlite 版一致的 key 升序语义
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (s *memStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// fakeClock 让 TTL 缓存测试不依赖真实时间。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 10, 15, 0, 0, 0, time.FixedZone("CST", 8*3600))}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestService 组装一个不联网的 Service（transport 由各测试注入）。
func newTestService(t *testing.T, cfg Config) *Service {
	t.Helper()
	if cfg.Guard == nil {
		cfg.Guard = newFakeGuard()
	}
	if cfg.Logger == nil {
		cfg.Logger = testLogger(t)
	}
	data, err := New(cfg)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	service, ok := data.(*Service)
	if !ok {
		t.Fatal("New 返回值不是 *Service")
	}
	return service
}

// testLogger 把日志写进 t.Log，测试失败时能看到真实上下文。
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// readTestdata 读取 testdata 下的原始字节（GBK 样本不能当文本读）。
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取 fixture %s 失败: %v", name, err)
	}
	return raw
}
