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

	"github.com/yazzyk/stock-agent/internal/agent"
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
	mu         sync.Mutex
	script     []contract.Message
	hasKey     bool
	calls      int
	entered    int
	seen       []contract.ChatRequest
	blockFirst chan struct{}
}

func (f *fakeLLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	f.mu.Lock()
	f.entered++
	n := f.entered
	f.seen = append(f.seen, req)
	f.mu.Unlock()
	if f.blockFirst != nil && n == 1 {
		select {
		case <-ctx.Done():
			return contract.Reply{}, ctx.Err()
		case <-f.blockFirst:
		}
	}
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

func (f *fakeLLM) enteredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entered
}

// systemAt 返回第 i 次模型调用拿到的 system prompt（能力目录测试用）。
func (f *fakeLLM) systemAt(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.seen) {
		return ""
	}
	return f.seen[i].System
}

func (f *fakeLLM) requestAt(i int) contract.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.seen) {
		return contract.ChatRequest{}
	}
	return f.seen[i]
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

	for _, name := range []string{"agent.run", "agent.status", "agent.spawn", "worker.create", "worker.list", "tool.search", "tool.describe", "tool.call"} {
		if _, ok := c.Tools.Get(name); !ok {
			t.Fatalf("缺少工具 %s", name)
		}
	}
	for _, name := range []string{"workers", "createWorker", "removeWorker", "models", "run", "stop", "sessions", "session"} {
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
		if len(tool.Schema) > 0 && !json.Valid(tool.Schema) {
			t.Fatalf("工具 %s 的 schema 不是合法 JSON：%s", tool.Name, string(tool.Schema))
		}
	}
	slots := c.UI.List()
	if len(slots) != 1 || slots[0].ID != "agentlab" || slots[0].Component != "agentlab" ||
		slots[0].Title != "Agent 工作台" || slots[0].Group != "研究" || slots[0].Order != 1 {
		t.Fatalf("UI slot 不符合 API.md（Agent 面板必须排第一）：%+v", slots)
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

func TestAgentLabSteerMethod(t *testing.T) {
	gate := make(chan struct{})
	llm := &fakeLLM{hasKey: true, blockFirst: gate, script: []contract.Message{
		{Role: contract.RoleAssistant, ToolCalls: []contract.ToolCall{
			{ID: "c1", Name: "tool.search", Arguments: `{"query":"quote"}`},
		}},
		{Role: contract.RoleAssistant, Content: "已并入补充约束后的结论"},
	}}
	c := testContext(t, newMemStore(), llm, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	run, _ := c.Methods.Get("run")
	raw, _ := json.Marshal(map[string]any{"worker": "chat", "input": "给个方案", "sessionId": "sess-plugin"})
	done := make(chan any, 1)
	go func() {
		out, err := run.Fn(context.Background(), raw)
		if err != nil {
			done <- err
			return
		}
		done <- out
	}()
	if !waitForPlugin(t, 2*time.Second, func() bool { return llm.enteredCount() >= 1 }) {
		t.Fatal("模型第 1 次调用应已开始")
	}

	steer, ok := c.Methods.Get("steer")
	if !ok {
		t.Fatal("必须提供 steer 方法")
	}
	raw, _ = json.Marshal(map[string]any{"sessionId": "sess-plugin", "text": "只看主板"})
	out, err := steer.Fn(context.Background(), raw)
	if err != nil {
		t.Fatalf("运行中插话应成功: %v", err)
	}
	buf, _ := json.Marshal(out)
	if !strings.Contains(string(buf), `"ok":true`) || !strings.Contains(string(buf), `"queued":1`) ||
		!strings.Contains(string(buf), `"sessionId":"sess-plugin"`) {
		t.Fatalf("steer 应返回 {ok,queued,sessionId}，实际: %s", string(buf))
	}
	close(gate)

	select {
	case res := <-done:
		if e, isErr := res.(error); isErr {
			t.Fatalf("run 应成功返回: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("插话后 run 应正常结束")
	}
	second := llm.requestAt(1)
	joined := ""
	for _, m := range second.Messages {
		joined += string(m.Role) + ":" + m.Content + " || "
	}
	if !strings.Contains(joined, "[用户插话] 只看主板") {
		t.Fatalf("第 2 次模型调用必须带上插件插话，实际: %s", joined)
	}

	// 结束后插话必须报错；空文本也必须报错。
	if _, err := steer.Fn(context.Background(), mustJSON(map[string]any{"sessionId": "sess-plugin", "text": "太晚了"})); err == nil ||
		!strings.Contains(err.Error(), "已结束") {
		t.Fatalf("会话结束后插话应报错，实际: %v", err)
	}
	if _, err := steer.Fn(context.Background(), mustJSON(map[string]any{"sessionId": "sess-plugin", "text": "  "})); err == nil {
		t.Fatal("空插话必须报错")
	}
}

func TestAgentLabSendRoutesWhileRunning(t *testing.T) {
	gate := make(chan struct{})
	llm := &fakeLLM{hasKey: true, blockFirst: gate, script: []contract.Message{
		{Role: contract.RoleAssistant, ToolCalls: []contract.ToolCall{
			{ID: "c1", Name: "tool.search", Arguments: `{"query":"quote"}`},
		}},
		{Role: contract.RoleAssistant, Content: "完成"},
	}}
	c := testContext(t, newMemStore(), llm, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	run, _ := c.Methods.Get("run")
	raw, _ := json.Marshal(map[string]any{"worker": "chat", "input": "给个方案", "sessionId": "sess-send"})
	go func() { _, _ = run.Fn(context.Background(), raw) }()
	if !waitForPlugin(t, 2*time.Second, func() bool { return llm.enteredCount() >= 1 }) {
		t.Fatal("模型第 1 次调用应已开始")
	}

	send, ok := c.Methods.Get("send")
	if !ok {
		t.Fatal("必须提供 send 方法（前端单入口）")
	}
	out, err := send.Fn(context.Background(), mustJSON(map[string]any{"worker": "chat", "input": "补充：只看主板", "sessionId": "sess-send"}))
	if err != nil {
		t.Fatalf("运行中 send 应转插话: %v", err)
	}
	buf, _ := json.Marshal(out)
	if !strings.Contains(string(buf), "已插话") {
		t.Fatalf("运行中 send 应返回插话回执，实际: %s", string(buf))
	}
	close(gate)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func waitForPlugin(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// callMethod 是测试里调用插件方法的便捷包装。
func callMethod(t *testing.T, c *plugin.Context, name string, payload any) (any, error) {
	t.Helper()
	m, ok := c.Methods.Get(name)
	if !ok {
		t.Fatalf("缺少方法 %s", name)
	}
	return m.Fn(context.Background(), mustJSON(payload))
}

func TestAgentLabTemplatesAndSchedule(t *testing.T) {
	c := testContext(t, newMemStore(), &fakeLLM{hasKey: true}, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	out, err := callMethod(t, c, "templates", map[string]any{})
	if err != nil {
		t.Fatalf("templates 失败: %v", err)
	}
	buf, _ := json.Marshal(out)
	var tpls struct {
		Templates []struct {
			ID            string `json:"id"`
			Prompt        string `json:"prompt"`
			DefaultPrompt string `json:"defaultPrompt"`
			Edited        bool   `json:"edited"`
			Enabled       bool   `json:"enabled"`
			Schedule      struct {
				Phase string `json:"phase"`
				At    string `json:"at"`
			} `json:"schedule"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(buf, &tpls); err != nil {
		t.Fatalf("templates 返回结构不对: %s", string(buf))
	}
	if len(tpls.Templates) != 6 {
		t.Fatalf("应有 6 个内置模板（总控 + 5），实际 %d: %s", len(tpls.Templates), string(buf))
	}
	if tpls.Templates[0].ID != "router" {
		t.Fatalf("第一个模板应是总控 router: %+v", tpls.Templates[0])
	}
	if tpls.Templates[1].ID != "premarket" || tpls.Templates[1].Prompt != tpls.Templates[1].DefaultPrompt {
		t.Fatalf("第二个模板应是 premarket 且 prompt=默认值: %+v", tpls.Templates[1])
	}
	if tpls.Templates[1].Schedule.Phase != "premarket" || tpls.Templates[1].Schedule.At != "09:15" {
		t.Fatalf("模板里应合并该阶段的调度状态（含 at 与 nextRunAt）: %s", string(buf))
	}

	// 保存太短的提示词：中文可操作错误。
	prompt := "你是盘前助手。今天只做两件事：先看资金主线，再列三个观察条件，并写清计划失效的判据。"
	if _, err := callMethod(t, c, "saveTemplate", map[string]any{"id": "premarket", "prompt": "太短"}); err == nil ||
		!strings.Contains(err.Error(), "太短") {
		t.Fatalf("过短提示词应报中文错误，实际: %v", err)
	}
	if _, err := callMethod(t, c, "saveTemplate", map[string]any{"id": "不存在", "prompt": prompt}); err == nil ||
		!strings.Contains(err.Error(), "未知模板") {
		t.Fatalf("未知 id 应报错，实际: %v", err)
	}

	if _, err := callMethod(t, c, "saveTemplate", map[string]any{"id": "premarket", "prompt": prompt}); err != nil {
		t.Fatalf("保存模板失败: %v", err)
	}
	buf, _ = json.Marshal(mustOut(t, c, "templates", map[string]any{}))
	if !strings.Contains(string(buf), `"edited":true`) || !strings.Contains(string(buf), "先看资金主线") {
		t.Fatalf("保存后应回显自定义 prompt 并标记 edited: %s", string(buf))
	}

	if _, err := callMethod(t, c, "resetTemplate", map[string]any{"id": "premarket"}); err != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
	buf, _ = json.Marshal(mustOut(t, c, "templates", map[string]any{}))
	if strings.Contains(string(buf), "先看资金主线") {
		t.Fatalf("恢复默认后不应再有自定义 prompt: %s", string(buf))
	}

	// 调度：默认三个阶段，改盘中频率。
	out, err = callMethod(t, c, "schedule", map[string]any{})
	if err != nil {
		t.Fatalf("schedule 失败: %v", err)
	}
	buf, _ = json.Marshal(out)
	if !strings.Contains(string(buf), `"phase":"premarket"`) || !strings.Contains(string(buf), `"phase":"postmarket"`) {
		t.Fatalf("schedule 应返回三个阶段: %s", string(buf))
	}
	if _, err := callMethod(t, c, "setSchedule", map[string]any{"phase": "noop"}); err == nil ||
		!strings.Contains(err.Error(), "未知阶段") {
		t.Fatalf("未知阶段应报错，实际: %v", err)
	}
	if _, err := callMethod(t, c, "setSchedule", map[string]any{"phase": "intraday", "everyMinutes": 15}); err != nil {
		t.Fatalf("改盘中频率失败: %v", err)
	}
	buf, _ = json.Marshal(mustOut(t, c, "schedule", map[string]any{}))
	if !strings.Contains(string(buf), `"everyMinutes":15`) {
		t.Fatalf("盘中频率应为 15: %s", string(buf))
	}
	if _, err := callMethod(t, c, "setSchedule", map[string]any{"phase": "intraday", "window": "09:40-11:30,13:00-14:57"}); err != nil {
		t.Fatalf("改盘中交易时段失败: %v", err)
	}
	buf, _ = json.Marshal(mustOut(t, c, "schedule", map[string]any{}))
	if !strings.Contains(string(buf), `"window":"09:40-11:30,13:00-14:57"`) {
		t.Fatalf("盘中交易时段应保存: %s", string(buf))
	}
	off := false
	if _, err := callMethod(t, c, "setSchedule", map[string]any{"phase": "postmarket", "enabled": off}); err != nil {
		t.Fatalf("关掉复盘失败: %v", err)
	}
	if _, err := callMethod(t, c, "setSchedule", map[string]any{"phase": "postmarket", "at": "25:00"}); err == nil ||
		!strings.Contains(err.Error(), "HH:MM") {
		t.Fatalf("非法 at 应报错，实际: %v", err)
	}
}

func mustOut(t *testing.T, c *plugin.Context, name string, payload any) any {
	t.Helper()
	out, err := callMethod(t, c, name, payload)
	if err != nil {
		t.Fatalf("%s 失败: %v", name, err)
	}
	return out
}

func TestAgentLabSpawnToolContextAndTree(t *testing.T) {
	llm := &fakeLLM{hasKey: true, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "主会话结论：情绪回暖"},
		{Role: contract.RoleAssistant, Content: "子任务结论：半导体主力净流入 3 日"},
	}}
	c := testContext(t, newMemStore(), llm, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	// 先跑一个主会话，作为派生者。
	if _, err := callMethod(t, c, "run", map[string]any{"worker": "postmarket", "input": "复盘今天", "sessionId": "sess-parent"}); err != nil {
		t.Fatalf("主会话运行失败: %v", err)
	}

	spawnTool, ok := c.Tools.Get("agent.spawn")
	if !ok {
		t.Fatal("必须注册 agent.spawn 工具")
	}
	// 工具从 ctx 里取会话 id（ReAct 会把它透传下来）。
	ctx := agent.WithSession(context.Background(), "sess-parent")
	args := mustJSON(map[string]any{"task": "复盘 603278 的资金流", "worker": "quant", "label": "个股复盘"})
	out, err := spawnTool.Run(ctx, args)
	if err != nil {
		t.Fatalf("agent.spawn 不应把错误抛出: %v", err)
	}
	text, _ := out.(string)
	if !strings.Contains(text, "【子智能体完成】") || !strings.Contains(text, "半导体主力净流入") {
		t.Fatalf("agent.spawn 应返回子报告：%q", text)
	}

	// 子会话不能再派生：返回中文错误字符串而不是 error。
	childID := ""
	treeOut := mustOut(t, c, "tree", map[string]any{"sessionId": "sess-parent"})
	buf, _ := json.Marshal(treeOut)
	var tree struct {
		Root struct {
			SessionID string `json:"sessionId"`
		} `json:"root"`
		Nodes []struct {
			SessionID string `json:"sessionId"`
			ParentID  string `json:"parentId"`
			Mode      string `json:"mode"`
			Worker    string `json:"worker"`
			Status    string `json:"status"`
			Label     string `json:"label"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(buf, &tree); err != nil {
		t.Fatalf("tree 返回结构不对: %s", string(buf))
	}
	if tree.Root.SessionID != "sess-parent" || len(tree.Nodes) != 2 {
		t.Fatalf("编队树应是 1 主 + 1 子: %s", string(buf))
	}
	childID = tree.Nodes[1].SessionID
	if tree.Nodes[1].Mode != "subagent" || tree.Nodes[1].Worker != "quant" || tree.Nodes[1].Label != "个股复盘" {
		t.Fatalf("子节点信息不对: %s", string(buf))
	}

	out, err = spawnTool.Run(agent.WithSession(context.Background(), childID), mustJSON(map[string]any{"task": "再套一层"}))
	if err != nil {
		t.Fatalf("子会话派生应返回文本而不是 error: %v", err)
	}
	if text, _ := out.(string); !strings.Contains(text, "子智能体不能再次派生") {
		t.Fatalf("子会话派生应被拒并给中文原因：%q", text)
	}

	// 没有会话 id 的 ctx（例如外部直接调工具）也要给可操作中文提示。
	out, err = spawnTool.Run(context.Background(), mustJSON(map[string]any{"task": "无会话"}))
	if err != nil {
		t.Fatalf("缺会话 id 时应返回文本: %v", err)
	}
	if text, _ := out.(string); !strings.Contains(text, "只能在主会话里调用") {
		t.Fatalf("缺会话 id 应给中文提示：%q", text)
	}

	// context：能回读上下文用量。
	ctxOut := mustOut(t, c, "context", map[string]any{"sessionId": "sess-parent"})
	buf, _ = json.Marshal(ctxOut)
	if !strings.Contains(string(buf), "usedTokens") || !strings.Contains(string(buf), "breakdown") ||
		!strings.Contains(string(buf), "contextWindow") {
		t.Fatalf("context 返回缺字段: %s", string(buf))
	}
	// context 缺 sessionId 与 tree 一致：回退成「最近一次会话」的上下文。
	// 前端在空闲状态（还没选中会话）也要能画出上下文环 / 打开上下文页。
	fallbackCtx := mustOut(t, c, "context", map[string]any{})
	buf, _ = json.Marshal(fallbackCtx)
	if !strings.Contains(string(buf), "usedTokens") || !strings.Contains(string(buf), "sessionId") {
		t.Fatalf("缺 sessionId 时应回退到最近会话的上下文: %s", string(buf))
	}
	// tree 缺 sessionId 不再报错：回退成最近一次主会话的树（前端首次进面板要能直接渲染）。
	treeOut = mustOut(t, c, "tree", map[string]any{})
	buf, _ = json.Marshal(treeOut)
	var fallback struct {
		Root struct {
			SessionID string `json:"sessionId"`
			ParentID  string `json:"parentId"`
		} `json:"root"`
		Nodes []struct {
			SessionID string `json:"sessionId"`
		} `json:"nodes"`
		Total         int    `json:"total"`
		RootSessionID string `json:"rootSessionId"`
		UpdatedAt     string `json:"updatedAt"`
	}
	if err := json.Unmarshal(buf, &fallback); err != nil {
		t.Fatalf("tree 返回结构不对: %s", string(buf))
	}
	if fallback.Root.SessionID == "" || fallback.Root.ParentID != "" {
		t.Fatalf("缺 sessionId 时应回退到最近主会话（parentId 为空），实际: %s", string(buf))
	}
	if fallback.RootSessionID != fallback.Root.SessionID || fallback.Total != len(fallback.Nodes) {
		t.Fatalf("rootSessionId/total 必须与 root/nodes 自洽: %s", string(buf))
	}
	if fallback.UpdatedAt == "" {
		t.Fatalf("tree 必须带 updatedAt（前端据此判新旧）: %s", string(buf))
	}
}

func TestAgentLabModelsAndCustomWorkers(t *testing.T) {
	llm := &fakeLLM{hasKey: true, script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "模型透传结论"},
	}}
	c := testContext(t, newMemStore(), llm, hostWithMarket())
	p := New()
	if err := p.Activate(c); err != nil {
		t.Fatal(err)
	}
	defer p.Deactivate(context.Background())

	// models：只回 id/model/displayName/isDefault/hasKey，不暴露 key。
	out := mustOut(t, c, "models", map[string]any{})
	buf, _ := json.Marshal(out)
	for _, want := range []string{`"id":"fake/fake"`, `"hasKey":true`, `"defaultId":"fake/fake"`} {
		if !strings.Contains(string(buf), want) {
			t.Fatalf("models 返回缺少 %q: %s", want, string(buf))
		}
	}
	if strings.Contains(strings.ToLower(string(buf)), "apikey") {
		t.Fatalf("models 不应暴露 key 字段: %s", string(buf))
	}

	// createWorker：成功后列表含自定义 worker 与模型。
	out = mustOut(t, c, "createWorker", map[string]any{
		"id":          "etf_flow",
		"title":       "ETF 资金",
		"description": "跟踪 ETF 份额与资金流",
		"prompt":      "你是 ETF 资金研究助手：先查份额与资金流，再给结论，并写清数据口径与不确定性。",
		"model":       "fake/fake",
	})
	buf, _ = json.Marshal(out)
	if !strings.Contains(string(buf), `"id":"etf_flow"`) || !strings.Contains(string(buf), `"custom":true`) ||
		!strings.Contains(string(buf), `"model":"fake/fake"`) {
		t.Fatalf("createWorker 应返回含自定义 worker 的列表: %s", string(buf))
	}
	workersOut := mustOut(t, c, "workers", map[string]any{})
	buf, _ = json.Marshal(workersOut)
	if !strings.Contains(string(buf), `"id":"router"`) {
		t.Fatalf("workers 列表必须包含总控: %s", string(buf))
	}

	// run 的 model 透传到 LLM。
	runOut := mustOut(t, c, "run", map[string]any{"worker": "chat", "input": "你好", "model": "fake/fake"})
	buf, _ = json.Marshal(runOut)
	if !strings.Contains(string(buf), `"model":"fake/fake"`) {
		t.Fatalf("run 响应应带实际模型: %s", string(buf))
	}
	if got := llm.requestAt(0).Model; got != "fake/fake" {
		t.Fatalf("run 的 model 必须透传给 LLM，实际 %q", got)
	}

	// removeWorker：删除后列表不再有该 worker。
	out = mustOut(t, c, "removeWorker", map[string]any{"id": "etf_flow"})
	buf, _ = json.Marshal(out)
	if strings.Contains(string(buf), `"id":"etf_flow"`) {
		t.Fatalf("removeWorker 后不应再返回该 worker: %s", string(buf))
	}
	if _, err := callMethod(t, c, "removeWorker", map[string]any{"id": "chat"}); err == nil ||
		!strings.Contains(err.Error(), "内置") {
		t.Fatalf("内置 worker 不可删，实际: %v", err)
	}

	// worker.create / worker.list 工具已注册且可用于总控。
	createTool, ok := c.Tools.Get("worker.create")
	if !ok {
		t.Fatal("缺少 worker.create 工具")
	}
	if _, err := createTool.Run(context.Background(), mustJSON(map[string]any{
		"id":          "etf_flow",
		"title":       "ETF 资金",
		"description": "跟踪 ETF 份额与资金流",
		"prompt":      "你是 ETF 资金研究助手：先查份额与资金流，再给结论，并写清数据口径与不确定性。",
	})); err != nil {
		t.Fatalf("worker.create 工具失败: %v", err)
	}
	listTool, ok := c.Tools.Get("worker.list")
	if !ok {
		t.Fatal("缺少 worker.list 工具")
	}
	out, err := listTool.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("worker.list 工具失败: %v", err)
	}
	buf, _ = json.Marshal(out)
	if !strings.Contains(string(buf), `"id":"etf_flow"`) || !strings.Contains(string(buf), `"id":"router"`) {
		t.Fatalf("worker.list 应列出内置与自定义 worker: %s", string(buf))
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
