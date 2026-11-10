package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- 测试替身 ----------

type testStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newTestStore() *testStore { return &testStore{data: map[string]map[string][]byte{}} }

func (s *testStore) Get(bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return v, nil
		}
	}
	return nil, contract.ErrNotFound
}

func (s *testStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = val
	return nil
}

func (s *testStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *testStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []contract.KVItem
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: v})
		}
	}
	return out, nil
}

func (s *testStore) Close() error { return nil }

type testGuard struct{}

func (testGuard) AllowHost(string) bool   { return true }
func (testGuard) CheckPath(string) error  { return nil }
func (testGuard) Redact(s string) string  { return s }
func (testGuard) Policy() contract.Policy { return contract.Policy{AllowExec: false, MaxToolCalls: 8} }

type testAudit struct {
	mu     sync.Mutex
	events []contract.AuditEvent
}

func (a *testAudit) Record(e contract.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *testAudit) List(limit int) ([]contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]contract.AuditEvent, 0, len(a.events))
	for i := len(a.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, a.events[i])
	}
	return out, nil
}

func (a *testAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

// fakePlugin 是可配置行为的插件替身。
type fakePlugin struct {
	plugin.Base
	id       string
	deps     []string
	activate func(*plugin.Context) error
	tools    []plugin.Tool
	order    *[]string
}

func (p *fakePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: p.id, Version: "0.0.1", DependsOn: p.deps}
}

func (p *fakePlugin) Activate(c *plugin.Context) error {
	if p.order != nil {
		*p.order = append(*p.order, p.id)
	}
	if p.activate != nil {
		if err := p.activate(c); err != nil {
			return err
		}
	}
	for _, t := range p.tools {
		if err := c.Tools.Add(t); err != nil {
			return err
		}
	}
	return nil
}

func newKernel(t *testing.T) (*Kernel, *testAudit) {
	t.Helper()
	audit := &testAudit{}
	k, err := New(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:  newTestStore(),
		Guard:  testGuard{},
		Audit:  audit,
		Clock:  time.Now,
	})
	if err != nil {
		t.Fatalf("kernel 创建失败: %v", err)
	}
	return k, audit
}

func echoTool(name string) plugin.Tool {
	return plugin.Tool{
		Name:    name,
		Summary: "测试用回声工具",
		Risk:    plugin.RiskRead,
		Run: func(_ context.Context, args json.RawMessage) (any, error) {
			return map[string]any{"name": name, "args": string(args)}, nil
		},
	}
}

// ---------- 测试 ----------

func TestRegisterRejectsDuplicateID(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "a"}); err != nil {
		t.Fatalf("首次注册不应失败: %v", err)
	}
	err := k.Register(&fakePlugin{id: "a"})
	if err == nil {
		t.Fatal("重复 ID 应当报错")
	}
	if !strings.Contains(err.Error(), "重复注册") {
		t.Fatalf("错误信息不明确: %v", err)
	}
}

func TestStartActivateOrderRespectsDependencies(t *testing.T) {
	k, _ := newKernel(t)
	var order []string
	// 注册顺序故意与依赖顺序相反。
	if err := k.Register(&fakePlugin{id: "consumer", deps: []string{"provider"}, order: &order}); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(&fakePlugin{id: "provider", order: &order}); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	want := []string{"provider", "consumer"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("激活顺序 = %v, want %v", order, want)
	}
}

func TestStartRejectsDependencyCycle(t *testing.T) {
	k, _ := newKernel(t)
	if err := k.Register(&fakePlugin{id: "a", deps: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(&fakePlugin{id: "b", deps: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	err := k.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "成环") {
		t.Fatalf("应当检测到依赖成环，实际: %v", err)
	}
}

func TestActivatePanicIsContained(t *testing.T) {
	k, audit := newKernel(t)
	bad := &fakePlugin{id: "bad", activate: func(*plugin.Context) error { panic("boom") }}
	good := &fakePlugin{id: "good", tools: []plugin.Tool{echoTool("good.echo")}}
	if err := k.Register(bad); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(good); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("单个插件 panic 不应影响整体启动: %v", err)
	}
	views := k.Plugins()
	if len(views) != 2 {
		t.Fatalf("插件数 = %d, want 2", len(views))
	}
	for _, v := range views {
		switch v.ID {
		case "bad":
			if v.Active {
				t.Fatal("panic 的插件不应被标记激活")
			}
			if !strings.Contains(v.Error, "panic") {
				t.Fatalf("应记录 panic 原因，实际: %q", v.Error)
			}
		case "good":
			if !v.Active {
				t.Fatal("健康插件应被激活")
			}
		}
	}
	if _, err := k.CallTool(context.Background(), "good.echo", nil); err != nil {
		t.Fatalf("健康插件的工具应可调用: %v", err)
	}
	if audit.count() == 0 {
		t.Fatal("应当写入审计事件")
	}
}

func TestToolNameCollisionRejectsSecondPlugin(t *testing.T) {
	k, _ := newKernel(t)
	a := &fakePlugin{id: "a", tools: []plugin.Tool{echoTool("same.echo")}}
	b := &fakePlugin{id: "b", tools: []plugin.Tool{echoTool("same.echo")}}
	if err := k.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	var activeB bool
	for _, v := range k.Plugins() {
		if v.ID == "b" {
			activeB = v.Active
			if !strings.Contains(v.Error, "重名") {
				t.Fatalf("应记录重名原因，实际: %q", v.Error)
			}
		}
	}
	if activeB {
		t.Fatal("工具重名的插件不应激活")
	}
	if len(k.Tools()) != 1 {
		t.Fatalf("工具数 = %d, want 1（冲突不落库）", len(k.Tools()))
	}
}

func TestInvokeUnknownMethodAndInactivePlugin(t *testing.T) {
	k, _ := newKernel(t)
	p := &fakePlugin{id: "p", activate: func(c *plugin.Context) error {
		return c.Methods.Add(plugin.Method{Name: "ping", Fn: func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"pong": "1"}, nil
		}})
	}}
	if err := k.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := k.Invoke(context.Background(), "p", "ping", nil)
	if err != nil {
		t.Fatalf("正常调用失败: %v", err)
	}
	if !strings.Contains(string(out), "pong") {
		t.Fatalf("返回内容不符: %s", out)
	}
	if _, err := k.Invoke(context.Background(), "p", "nope", nil); !errors.Is(err, ErrMethodNotFound) {
		t.Fatalf("未知方法错误应可判定，实际: %v", err)
	}
	if _, err := k.Invoke(context.Background(), "missing", "ping", nil); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("未知插件错误应可判定，实际: %v", err)
	}
}

func TestCallToolUnknownAndTruncation(t *testing.T) {
	k, _ := newKernel(t)
	big := strings.Repeat("x", MaxToolResultBytes+1024)
	p := &fakePlugin{id: "p", tools: []plugin.Tool{
		{Name: "big.blob", Summary: "返回超大结果", Risk: plugin.RiskRead, Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"blob": big}, nil
		}},
	}}
	if err := k.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := k.CallTool(context.Background(), "nope", nil); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("未知工具错误应可判定，实际: %v", err)
	}
	out, err := k.CallTool(context.Background(), "big.blob", nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if len(out) > MaxToolResultBytes+512 {
		t.Fatalf("结果未截断: %d 字节", len(out))
	}
	if !strings.Contains(string(out), "truncated") {
		t.Fatalf("截断后应带 truncated 标记: %s", string(out)[:80])
	}
}

func TestToolResultSerializesNilAsNull(t *testing.T) {
	k, _ := newKernel(t)
	p := &fakePlugin{id: "p", tools: []plugin.Tool{
		{Name: "noop", Summary: "返回 nil", Risk: plugin.RiskRead, Run: func(context.Context, json.RawMessage) (any, error) {
			return nil, nil
		}},
	}}
	if err := k.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := k.CallTool(context.Background(), "noop", nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if strings.TrimSpace(string(out)) != "null" {
		t.Fatalf("nil 应序列化为 null，实际: %s", out)
	}
}

func TestToolHostAllowsCrossPluginCalls(t *testing.T) {
	k, _ := newKernel(t)
	provider := &fakePlugin{id: "provider", tools: []plugin.Tool{echoTool("provider.echo")}}
	var got string
	consumer := &fakePlugin{id: "consumer", deps: []string{"provider"}, activate: func(c *plugin.Context) error {
		if c.ToolHost == nil {
			t.Fatal("Context.ToolHost 未注入")
		}
		found := false
		for _, tool := range c.ToolHost.Tools() {
			if tool.Name == "provider.echo" {
				found = true
			}
		}
		if !found {
			t.Fatal("ToolHost.Tools 应能看到其它插件注册的工具")
		}
		out, err := c.ToolHost.CallTool(context.Background(), "provider.echo", json.RawMessage(`{"x":1}`))
		if err != nil {
			t.Fatalf("跨插件调用失败: %v", err)
		}
		got = string(out)
		return nil
	}}
	if err := k.Register(provider); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(consumer); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if !strings.Contains(got, "provider.echo") {
		t.Fatalf("调用结果不符: %s", got)
	}
}

func TestUISlotsSortedAndScoped(t *testing.T) {
	k, _ := newKernel(t)
	a := &fakePlugin{id: "a", activate: func(c *plugin.Context) error {
		return c.UI.Add(plugin.UISlot{ID: "a2", Title: "A2", Component: "a2", Order: 20})
	}}
	b := &fakePlugin{id: "b", activate: func(c *plugin.Context) error {
		if err := c.UI.Add(plugin.UISlot{ID: "b1", Title: "B1", Component: "b1", Order: 10}); err != nil {
			return err
		}
		return c.UI.Add(plugin.UISlot{ID: "b3", Title: "B3", Component: "b3", Order: 30})
	}}
	if err := k.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	slots := k.UISlots()
	if len(slots) != 3 {
		t.Fatalf("面板数 = %d, want 3", len(slots))
	}
	want := []string{"b1", "a2", "b3"}
	for i, s := range slots {
		if s.ID != want[i] {
			t.Fatalf("排序错误: %v", func() []string {
				out := make([]string, 0, len(slots))
				for _, x := range slots {
					out = append(out, x.ID)
				}
				return out
			}())
		}
	}
}
