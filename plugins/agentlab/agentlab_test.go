package agentlab

import (
	"context"
	"encoding/json"
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

// fakeLLM 按脚本回放，HasKey 决定"有没有配模型"。
type fakeLLM struct {
	mu     sync.Mutex
	script []contract.Message
	hasKey bool
	calls  int
}

func (f *fakeLLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.script) == 0 {
		return contract.Reply{Message: contract.Message{Role: contract.RoleAssistant, Content: "默认结论"}}, nil
	}
	msg := f.script[0]
	f.script = f.script[1:]
	return contract.Reply{Message: msg, Usage: contract.Usage{TotalTokens: 42}}, nil
}

func (f *fakeLLM) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	reply, err := f.Chat(ctx, req)
	if err == nil && onDelta != nil && reply.Message.Content != "" {
		onDelta(reply.Message.Content)
	}
	return reply, err
}

func (f *fakeLLM) Models(context.Context) ([]contract.ModelInfo, error) {
	return []contract.ModelInfo{{Provider: "fake", Model: "fake", HasKey: f.hasKey, IsDefault: true}}, nil
}

func (f *fakeLLM) Default(context.Context) (contract.ModelInfo, error) {
	return contract.ModelInfo{Provider: "fake", Model: "fake", HasKey: f.hasKey, IsDefault: true}, nil
}

// fakeHost 模拟 kernel 的 ToolHost。
type fakeHost struct{ tools []plugin.Tool }

func (h *fakeHost) Tools() []plugin.Tool { return h.tools }

func (h *fakeHost) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	for _, t := range h.tools {
		if t.Name != name {
			continue
		}
		out, err := t.Run(ctx, args)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("工具未注册：%s", name)
}

func hostWithMarket() *fakeHost {
	return &fakeHost{tools: []plugin.Tool{{
		Name:    "market.quote",
		Title:   "实时快照",
		Summary: "批量个股实时行情快照",
		Tags:    []string{"market"},
		Risk:    plugin.RiskNetwork,
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"code": "600000", "price": 12.34}, nil
		},
	}}}
}

func testContext(t *testing.T, store contract.Store, llm contract.LLMService, host plugin.ToolHost) *plugin.Context {
	t.Helper()
	return &plugin.Context{
		ID:       "agentlab",
		Manifest: plugin.Manifest{ID: "agentlab", Name: "Agent 工作台", Version: "1.0.0"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:    time.Now,
		Tools:    plugin.NewToolRegistry("agentlab"),
		UI:       plugin.NewUIRegistry("agentlab"),
		Methods:  plugin.NewMethodRegistry("agentlab"),
		Services: plugin.NewServiceRegistry(),
		Events:   plugin.NewBus(),
		Store:    store,
		LLM:      llm,
		Policy:   contract.Policy{MaxToolCalls: 6, MaxTokensPerRun: 8000},
		ToolHost: host,
	}
}

func TestAgentLabActivateRegistersEverything(t *testing.T) {
	c := testContext(t, newMemStore(), &fakeLLM{hasKey: true}, hostWithMarket())
	p := New()
	if err := p.Manifest().Validate(); err != nil {
		t.Fatal(err)
	}
	m := p.Manifest()
	if len(m.DependsOn) != 1 || m.DependsOn[0] != "knowledge" {
		t.Fatalf("agentlab 必须依赖 knowledge，实际 %+v", m.DependsOn)
	}
	if err := p.Activate(c); err != nil {
		t.Fatalf("Activate 失败: %v", err)
	}
	defer p.Deactivate(context.Background())

	for _, name := range []string{"agent.run", "agent.status", "tool.search", "tool.describe", "tool.call"} {
		if _, ok := c.Tools.Get(name); !ok {
			t.Fatalf("缺少工具 %s", name)
		}
	}
	for _, name := range []string{"workers", "run", "stop", "sessions", "session"} {
		if _, ok := c.Methods.Get(name); !ok {
			t.Fatalf("缺少方法 %s", name)
		}
	}
	if _, ok := c.Services.Get("agent.runtime"); !ok {
		t.Fatal("必须注册服务 agent.runtime")
	}
	for _, tool := range c.Tools.List() {
		if n := len([]rune(tool.Summary)); n == 0 || n > 40 {
			t.Fatalf("工具 %s 的 summary 为 %d 字（要求 1..40）：%q", tool.Name, n, tool.Summary)
		}
	}
	slots := c.UI.List()
	if len(slots) != 1 || slots[0].ID != "agentlab" || slots[0].Component != "agentlab" ||
		slots[0].Title != "Agent 工作台" || slots[0].Group != "研究" || slots[0].Order != 40 {
		t.Fatalf("UI slot 不符合 API.md：%+v", slots)
	}
}

func TestAgentLabRunAndSession(t *testing.T) {
	llm := &fakeLLM{hasKey: true, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "结论：情绪回暖，关注连板高度。"},
	}}
	c := testContext(t, newMemStore(), llm, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	run, _ := c.Methods.Get("run")
	raw, _ := json.Marshal(map[string]any{"worker": "postmarket", "input": "复盘今天"})
	out, err := run.Fn(context.Background(), raw)
	if err != nil {
		t.Fatalf("run 失败: %v", err)
	}
	buf, _ := json.Marshal(out)
	if !strings.Contains(string(buf), "情绪回暖") || !strings.Contains(string(buf), "sessionId") {
		t.Fatalf("run 应返回 report 与 sessionId，实际: %s", string(buf))
	}

	sessionsFn, _ := c.Methods.Get("sessions")
	raw, _ = json.Marshal(map[string]any{"limit": 5})
	sessions, err := sessionsFn.Fn(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	buf, _ = json.Marshal(sessions)
	if !strings.Contains(string(buf), "postmarket") {
		t.Fatalf("sessions 应包含刚跑的会话，实际: %s", string(buf))
	}

	// 取第一个会话 id 并查看详情。
	var list []map[string]any
	if err := json.Unmarshal(buf, &list); err != nil || len(list) == 0 {
		t.Fatalf("sessions 返回结构不对: %s", string(buf))
	}
	sessionID, _ := list[0]["id"].(string)
	sessionFn, _ := c.Methods.Get("session")
	raw, _ = json.Marshal(map[string]any{"sessionId": sessionID})
	detail, err := sessionFn.Fn(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	buf, _ = json.Marshal(detail)
	if !strings.Contains(string(buf), "events") || !strings.Contains(string(buf), "user") {
		t.Fatalf("session 详情应含事件，实际: %s", string(buf))
	}

	stopFn, _ := c.Methods.Get("stop")
	raw, _ = json.Marshal(map[string]any{"sessionId": "不存在"})
	stopped, err := stopFn.Fn(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(stopped), "false") {
		t.Fatalf("停止不存在的会话应返回 ok=false，实际: %v", stopped)
	}
}

func TestAgentLabNoModelFailsFast(t *testing.T) {
	llm := &fakeLLM{hasKey: false}
	c := testContext(t, newMemStore(), llm, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	run, _ := c.Methods.Get("run")
	raw, _ := json.Marshal(map[string]any{"worker": "premarket"})
	start := time.Now()
	_, err := run.Fn(context.Background(), raw)
	if err == nil {
		t.Fatal("没有 API Key 时必须返回错误，而不是硬跑")
	}
	if !strings.Contains(err.Error(), "未配置模型") || !strings.Contains(err.Error(), "模型设置") {
		t.Fatalf("错误文案必须是可操作中文，实际: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("无模型应立刻失败，实际耗时 %v", elapsed)
	}

	// 工具路径也要同样优雅失败。
	tool, ok := c.Tools.Get("agent.run")
	if !ok {
		t.Fatal("缺少 agent.run 工具")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"worker":"premarket"}`)); err == nil ||
		!strings.Contains(err.Error(), "未配置模型") {
		t.Fatalf("agent.run 工具也应返回未配置模型错误，实际: %v", err)
	}
	// 没有请求过模型。
	if llm.calls != 0 {
		t.Fatalf("无 key 时不应发起模型调用，实际 %d 次", llm.calls)
	}
}

func TestAgentLabStatusTool(t *testing.T) {
	c := testContext(t, newMemStore(), &fakeLLM{hasKey: true}, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	tool, _ := c.Tools.Get("agent.status")
	out, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("agent.status 传空参数不应报错: %v", err)
	}
	buf, _ := json.Marshal(out)
	for _, want := range []string{"running", "sessions", "workers", "premarket"} {
		if !strings.Contains(string(buf), want) {
			t.Fatalf("agent.status 返回缺少 %q: %s", want, string(buf))
		}
	}
}
