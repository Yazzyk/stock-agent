package test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- Store 假实现 ----------

type fakeStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string]map[string][]byte{}}
}

func (s *fakeStore) Get(bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return append([]byte(nil), v...), nil
		}
	}
	return nil, contract.ErrNotFound
}

func (s *fakeStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = append([]byte(nil), val...)
	return nil
}

func (s *fakeStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}

func (s *fakeStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.KVItem, 0, len(s.data[bucket]))
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: append([]byte(nil), v...)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *fakeStore) Close() error { return nil }

// ---------- Guard 假实现 ----------

// fakeGuard 复刻安全策略语义：域名后缀匹配、路径根目录前缀匹配、脱敏。
type fakeGuard struct {
	mu     sync.Mutex
	policy contract.Policy
}

func newFakeGuard(roots ...string) *fakeGuard {
	if len(roots) == 0 {
		roots = []string{filepath.Join(string(filepath.Separator), "tmp", "guanlan-test")}
	}
	return &fakeGuard{policy: contract.Policy{
		NetworkAllowlist: []string{"eastmoney.com", "10jqka.com.cn", "gtimg.cn", "sinajs.cn"},
		PathRoots:        roots,
		AllowExec:        false,
		MaxReqPerMin:     60,
		MaxToolCalls:     8,
		MaxTokensPerRun:  10000,
	}}
}

func (g *fakeGuard) setPolicy(p contract.Policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policy = p
}

func (g *fakeGuard) AllowHost(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.Index(h, ":"); i > 0 {
		h = h[:i]
	}
	for _, allowed := range g.policy.NetworkAllowlist {
		a := strings.ToLower(allowed)
		if h == a || strings.HasSuffix(h, "."+a) {
			return true
		}
	}
	return false
}

func (g *fakeGuard) CheckPath(p string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	for _, root := range g.policy.PathRoots {
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) {
			return nil
		}
	}
	return &pathDeniedError{path: p}
}

type pathDeniedError struct{ path string }

func (e *pathDeniedError) Error() string { return "安全策略：路径不在允许目录内: " + e.path }

var redactPatterns = []string{"sk-", "Bearer ", "token=", "cookie=", "password=", "api_key="}

func (g *fakeGuard) Redact(s string) string {
	out := s
	for _, p := range redactPatterns {
		for {
			idx := strings.Index(out, p)
			if idx < 0 {
				break
			}
			end := idx + len(p)
			for end < len(out) && !strings.ContainsRune(" \t\n\r,;\"'", rune(out[end])) {
				end++
			}
			out = out[:idx] + p + "***" + out[end:]
		}
	}
	return out
}

func (g *fakeGuard) Policy() contract.Policy {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.policy
}

// ---------- Audit 假实现 ----------

type fakeAudit struct {
	mu     sync.Mutex
	events []contract.AuditEvent
}

func newFakeAudit() *fakeAudit { return &fakeAudit{events: make([]contract.AuditEvent, 0, 32)} }

func (a *fakeAudit) Record(e contract.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

// List 返回最近 limit 条，最新在前（与产品语义一致）。
func (a *fakeAudit) List(limit int) ([]contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit <= 0 || limit > len(a.events) {
		limit = len(a.events)
	}
	out := make([]contract.AuditEvent, 0, limit)
	for i := len(a.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, a.events[i])
	}
	return out, nil
}

func (a *fakeAudit) all() []contract.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]contract.AuditEvent(nil), a.events...)
}

func (a *fakeAudit) countIf(pred func(contract.AuditEvent) bool) int {
	n := 0
	for _, e := range a.all() {
		if pred(e) {
			n++
		}
	}
	return n
}

// lastOf 返回最后一条 kind+action 匹配的审计事件；不存在则 t.Fatal。
func (a *fakeAudit) lastOf(t *testing.T, kind, action string) contract.AuditEvent {
	t.Helper()
	var found contract.AuditEvent
	ok := false
	for _, e := range a.all() {
		if e.Kind == kind && e.Action == action {
			found = e
			ok = true
		}
	}
	if !ok {
		t.Fatalf("审计缺少 kind=%q action=%q；现有事件: %s", kind, action, a.dump())
	}
	return found
}

func (a *fakeAudit) dump() string {
	var sb strings.Builder
	for _, e := range a.all() {
		sb.WriteString("[" + e.Kind + "/" + e.Action + " ok=" + boolText(e.OK) + "] ")
	}
	return sb.String()
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---------- 插件夹具 ----------

// orderLog 记录激活/停用顺序。
type orderLog struct {
	mu  sync.Mutex
	seq []string
}

func newOrderLog() *orderLog { return &orderLog{} }

func (l *orderLog) add(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq = append(l.seq, id)
}

func (l *orderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seq...)
}

type fakePlugin struct {
	man        plugin.Manifest
	activateFn func(*plugin.Context) error
	deactFn    func(context.Context) error
	log        *orderLog

	mu        sync.Mutex
	activated int
	deact     int
}

func (p *fakePlugin) Manifest() plugin.Manifest { return p.man }

func (p *fakePlugin) Activate(c *plugin.Context) error {
	p.mu.Lock()
	p.activated++
	p.mu.Unlock()
	if p.log != nil {
		p.log.add(p.man.ID)
	}
	if p.activateFn != nil {
		return p.activateFn(c)
	}
	return nil
}

func (p *fakePlugin) Deactivate(ctx context.Context) error {
	p.mu.Lock()
	p.deact++
	p.mu.Unlock()
	if p.deactFn != nil {
		return p.deactFn(ctx)
	}
	return nil
}

func (p *fakePlugin) activateCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activated
}

func (p *fakePlugin) deactivateCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deact
}

// newPlugin 造一个空插件（可带依赖）。
func newPlugin(id string, deps ...string) *fakePlugin {
	return &fakePlugin{man: plugin.Manifest{ID: id, Name: id, Version: "0.0.1", DependsOn: deps}}
}

// withTools 让插件在 Activate 时注册一批工具。
func withTools(p *fakePlugin, tools ...plugin.Tool) *fakePlugin {
	p.activateFn = func(c *plugin.Context) error {
		for _, tl := range tools {
			if err := c.Tools.Add(tl); err != nil {
				return err
			}
		}
		return nil
	}
	return p
}

// withMethod 让插件在 Activate 时注册一个前端方法。
func withMethod(p *fakePlugin, name string, fn plugin.MethodFunc) *fakePlugin {
	p.activateFn = func(c *plugin.Context) error {
		return c.Methods.Add(plugin.Method{Name: name, Description: "测试方法", Fn: fn})
	}
	return p
}

// echoTool 造一个把入参原样回显的只读工具。
func echoTool(name string) plugin.Tool {
	return plugin.Tool{
		Name:    name,
		Summary: "回显入参（测试用）",
		Tags:    []string{"test"},
		Risk:    plugin.RiskRead,
		Run: func(_ context.Context, args json.RawMessage) (any, error) {
			return map[string]any{"args": string(args)}, nil
		},
	}
}

// ---------- kernel 夹具 ----------

type kernelFixture struct {
	k     *kernel.Kernel
	audit *fakeAudit
	guard *fakeGuard
	store *fakeStore
}

// newKernel 造一个可用 kernel：内存 Store、假 Guard、假 Audit、丢弃日志。
func newKernel(t *testing.T) *kernelFixture {
	t.Helper()
	store := newFakeStore()
	guard := newFakeGuard()
	audit := newFakeAudit()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k, err := kernel.New(kernel.Options{
		Logger: logger,
		Store:  store,
		Guard:  guard,
		Audit:  audit,
		Clock:  time.Now,
	})
	if err != nil {
		t.Fatalf("kernel.New 失败: %v", err)
	}
	return &kernelFixture{k: k, audit: audit, guard: guard, store: store}
}

func (f *kernelFixture) register(t *testing.T, ps ...plugin.Plugin) {
	t.Helper()
	for _, p := range ps {
		if err := f.k.Register(p); err != nil {
			t.Fatalf("Register(%s) 失败: %v", p.Manifest().ID, err)
		}
	}
}

func (f *kernelFixture) start(t *testing.T) {
	t.Helper()
	if err := f.k.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
}

// pluginView 取插件视图（不存在则 t.Fatal）。
func (f *kernelFixture) pluginView(t *testing.T, id string) kernel.PluginView {
	t.Helper()
	for _, v := range f.k.Plugins() {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("插件 %q 不在 kernel 视图中", id)
	return kernel.PluginView{}
}
