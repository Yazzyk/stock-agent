package llm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ---------- 测试替身 ----------

type memStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

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
	var out []contract.KVItem
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: v})
		}
	}
	return out, nil
}

func (s *memStore) Close() error { return nil }

// seedID 返回默认配置的 id（新建服务时自动生成一条可编辑的 DeepSeek 配置）。
func seedID(t *testing.T, svc *Service) string {
	t.Helper()
	views, id, err := svc.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(views) == 0 || id == "" {
		t.Fatal("默认配置缺失")
	}
	return id
}

func newTestService(t *testing.T) (*Service, *memStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := newMemStore()
	svc, err := New(Config{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		DataDir: dir,
	})
	if err != nil {
		t.Fatalf("创建 service 失败: %v", err)
	}
	return svc, store, dir
}

// fakeChatServer 是最小的 OpenAI 兼容服务，用于验证适配层（不触网）。
func fakeChatServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("意外的请求路径: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("缺少 Authorization: %q", got)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func completionBody(content string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-test",
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
	})
	return string(b)
}

// ---------- 密钥环 ----------

func TestKeyringSealOpenAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring")
	ring, err := openKeyring(path)
	if err != nil {
		t.Fatalf("创建密钥环失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("密钥环权限 = %o, want 600", perm)
	}
	enc, err := ring.Seal("sk-secret-value")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if strings.Contains(enc, "sk-secret-value") {
		t.Fatal("密文里不能出现明文")
	}
	plain, err := ring.Open(enc)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if plain != "sk-secret-value" {
		t.Fatalf("解密结果 = %q", plain)
	}
	if _, err := ring.Open("AAAAnotvalid"); err == nil {
		t.Fatal("非法密文应当报错")
	}
	// 重新打开同一文件，密钥必须可复用
	again, err := openKeyring(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	if got, err := again.Open(enc); err != nil || got != "sk-secret-value" {
		t.Fatalf("重开后解密失败: %q, %v", got, err)
	}
	if again.ID() != ring.ID() {
		t.Fatalf("密钥 ID 应保持稳定: %q vs %q", again.ID(), ring.ID())
	}
}

func TestSealEmptyStringIsNoop(t *testing.T) {
	ring, err := openKeyring(filepath.Join(t.TempDir(), "keyring"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := ring.Seal("")
	if err != nil || enc != "" {
		t.Fatalf("空 key 应返回空密文, got %q, %v", enc, err)
	}
	plain, err := ring.Open("")
	if err != nil || plain != "" {
		t.Fatalf("空密文应返回空明文, got %q, %v", plain, err)
	}
}

// ---------- 配置管理 ----------

func TestDefaultProviderAndHasKeyFromEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	svc, _, _ := newTestService(t)
	views, defaultID, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("默认应有一条可编辑配置, got %d", len(views))
	}
	if views[0].Provider != "deepseek" || views[0].Model == "" {
		t.Fatalf("默认 provider 不符: %+v", views[0])
	}
	if views[0].HasKey {
		t.Fatal("没有 key 时 HasKey 应为 false")
	}
	if defaultID == "" || !views[0].IsDefault {
		t.Fatal("默认模型标记缺失")
	}

	t.Setenv("DEEPSEEK_API_KEY", "sk-env-only")
	views, _, _ = svc.List()
	if !views[0].HasKey {
		t.Fatal("环境变量提供 key 时 HasKey 应为 true")
	}
}

func TestSaveEncryptsKeyAndKeepsItWhenEmpty(t *testing.T) {
	svc, store, _ := newTestService(t)
	id := seedID(t, svc)
	views, _, err := svc.Save(SaveInput{ID: id, Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-abc123"})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if len(views) != 1 || !views[0].HasKey {
		t.Fatalf("保存后 HasKey 应为 true: %+v", views)
	}
	raw, err := store.Get(settingsBucket, settingsKey)
	if err != nil {
		t.Fatalf("配置未落库: %v", err)
	}
	if strings.Contains(string(raw), "sk-abc123") {
		t.Fatal("落库内容不能包含明文 key")
	}

	// 空 apiKey → 保留旧 key
	views, _, err = svc.Save(SaveInput{ID: id, Provider: "deepseek", Model: "deepseek-reasoner"})
	if err != nil {
		t.Fatalf("二次保存失败: %v", err)
	}
	if !views[0].HasKey {
		t.Fatal("空 apiKey 不应清掉已有 key")
	}
	if views[0].Model != "deepseek-reasoner" {
		t.Fatalf("模型未更新: %+v", views[0])
	}
	if views[0].ID != id {
		t.Fatal("更新不应改变 ID")
	}
}

func TestSaveRejectsUnknownProviderAndID(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{Provider: "unknown"}); err == nil {
		t.Fatal("未知 provider 应报错")
	}
	if _, _, err := svc.Save(SaveInput{ID: "nope", Provider: "deepseek"}); err == nil {
		t.Fatal("不存在的 ID 应报错")
	}
}

func TestRemoveDefaultFallsBack(t *testing.T) {
	svc, _, _ := newTestService(t)
	views, defaultID, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	first := views[0].ID
	views, _, err = svc.Save(SaveInput{ID: seedID(t, svc), Provider: "openai", Model: "gpt-4o-mini", SetDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("更新后仍应只有一条配置, got %d", len(views))
	}
	openaiID := views[0].ID
	if views[0].Provider != "openai" {
		t.Fatalf("provider 未更新: %+v", views[0])
	}
	views, _, err = svc.Save(SaveInput{Provider: "qwen", Model: "qwen-plus"})
	if err != nil || len(views) != 2 {
		t.Fatalf("新增配置失败: %v", err)
	}
	views, defaultID, err = svc.Remove(openaiID)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if defaultID == openaiID {
		t.Fatal("默认模型被删后应回落")
	}
	if len(views) != 1 {
		t.Fatalf("删除后应剩一条, got %d", len(views))
	}
	if views[0].ID != defaultID {
		t.Fatalf("应回落到剩余的第一条, got %q want %q", defaultID, views[0].ID)
	}
	_ = first
}

// ---------- 模型调用 ----------

func TestChatAgainstFakeOpenAI(t *testing.T) {
	srv := fakeChatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completionBody("pong")))
	})
	svc, _, _ := newTestService(t)
	views, _, err := svc.Save(SaveInput{ID: seedID(t, svc), Provider: "openai", Model: "gpt-test", BaseURL: srv.URL + "/v1", APIKey: "sk-test", SetDefault: true})
	if err != nil || len(views) != 1 {
		t.Fatalf("保存失败: %v (%d view)", err, len(views))
	}
	temp := 0.2
	reply, err := svc.Chat(context.Background(), contract.ChatRequest{
		System:      "你是研究员",
		Messages:    []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   64,
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if reply.Message.Content != "pong" {
		t.Fatalf("内容不符: %+v", reply.Message)
	}
	if reply.Usage.TotalTokens != 14 || reply.Usage.PromptTokens != 11 {
		t.Fatalf("用量解析不符: %+v", reply.Usage)
	}
	if reply.Model != "gpt-test" {
		t.Fatalf("模型名不符: %q", reply.Model)
	}
}

func TestChatParsesToolCalls(t *testing.T) {
	srv := fakeChatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"id": "x", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "market.quote", "arguments": "{\"codes\":[\"600000\"]}"},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
		b, _ := json.Marshal(body)
		_, _ = w.Write(b)
	})
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{Provider: "openai", Model: "gpt-test", BaseURL: srv.URL + "/v1", APIKey: "sk-test", SetDefault: true}); err != nil {
		t.Fatal(err)
	}
	schemaJSON := json.RawMessage(`{"type":"object","properties":{"codes":{"type":"array","items":{"type":"string"}}},"required":["codes"]}`)
	reply, err := svc.Chat(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "浦发银行多少钱"}},
		Tools: []contract.ToolInfo{{
			Name: "market.quote", Description: "查行情", Schema: schemaJSON,
		}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if len(reply.Message.ToolCalls) != 1 {
		t.Fatalf("应解析出 1 个工具调用, got %+v", reply.Message)
	}
	tc := reply.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "market.quote" || !strings.Contains(tc.Arguments, "600000") {
		t.Fatalf("工具调用解析不符: %+v", tc)
	}
}

func TestChatWithoutKeyReturnsActionableError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{ID: seedID(t, svc), Provider: "openai", Model: "gpt-4o-mini", SetDefault: true}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Chat(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("缺 key 应当报错")
	}
	if !strings.Contains(err.Error(), "API Key") || !strings.Contains(err.Error(), "模型设置") {
		t.Fatalf("错误信息不够可操作: %v", err)
	}
}

func TestStreamAggregatesDeltas(t *testing.T) {
	srv := fakeChatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, part := range []string{"po", "ng"} {
			chunk := map[string]any{
				"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "gpt-test",
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": part}, "finish_reason": nil}},
			}
			b, _ := json.Marshal(chunk)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{Provider: "openai", Model: "gpt-test", BaseURL: srv.URL + "/v1", APIKey: "sk-test", SetDefault: true}); err != nil {
		t.Fatal(err)
	}
	var deltas []string
	reply, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if reply.Message.Content != "pong" {
		t.Fatalf("合并内容不符: %q", reply.Message.Content)
	}
	if strings.Join(deltas, "") != "pong" {
		t.Fatalf("delta 回调不符: %v", deltas)
	}
}

func TestTestReportsFailureWithoutPanic(t *testing.T) {
	svc, _, _ := newTestService(t)
	res, err := svc.Test(context.Background(), "")
	if err != nil {
		t.Fatalf("Test 不应返回 error: %v", err)
	}
	if res.OK {
		t.Fatal("没有 key 时测试应失败")
	}
	if !strings.Contains(res.Message, "API Key") {
		t.Fatalf("失败原因不够清楚: %q", res.Message)
	}
}

func TestUnsupportedMessageRole(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := toEinoMessages(contract.ChatRequest{Messages: []contract.Message{{Role: "weird", Content: "x"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "未知消息角色") {
		t.Fatalf("未知角色应报错: %v", err)
	}
	_ = svc
}

// DeepSeek/OpenAI 兼容服务要求 function.name 匹配 ^[a-zA-Z0-9_-]+$；
// 我们的点号工具名必须在出站时转义、回程时还原。
func TestToolNameMappingRoundTrip(t *testing.T) {
	srv := fakeChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Tools) != 1 || body.Tools[0].Function.Name != "market_quote" {
			t.Errorf("出站工具名应为 market_quote，实际 %+v", body.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]any{
			"id": "x", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "market_quote", "arguments": "{}"},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		_, _ = w.Write(b)
	})
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{ID: seedID(t, svc), Provider: "openai", Model: "gpt-test", BaseURL: srv.URL + "/v1", APIKey: "sk-test", SetDefault: true}); err != nil {
		t.Fatal(err)
	}
	reply, err := svc.Chat(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "查行情"}},
		Tools:    []contract.ToolInfo{{Name: "market.quote", Description: "查行情"}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if len(reply.Message.ToolCalls) != 1 {
		t.Fatalf("工具调用丢失: %+v", reply.Message)
	}
	if name := reply.Message.ToolCalls[0].Name; name != "market.quote" {
		t.Fatalf("回程工具名应还原为 market.quote，实际 %q", name)
	}
	// 历史回放：assistant 的 tool call 也必须转成 provider 形式
	if err := newToolNameMapCheck(t); err != nil {
		t.Fatal(err)
	}
}

func newToolNameMapCheck(t *testing.T) error {
	t.Helper()
	if _, err := newToolNameMap([]contract.ToolInfo{{Name: "a.b"}, {Name: "a_b"}}); err == nil {
		t.Fatal("映射冲突应报错")
	}
	m, err := newToolNameMap([]contract.ToolInfo{{Name: "market.quote"}})
	if err != nil {
		return err
	}
	if got := m.provider("market.quote"); got != "market_quote" {
		t.Fatalf("provider 映射错误: %q", got)
	}
	if got := m.local("market_quote"); got != "market.quote" {
		t.Fatalf("local 映射错误: %q", got)
	}
	return nil
}

func TestToolMessageRequiresToolCallID(t *testing.T) {
	_, err := toEinoMessages(contract.ChatRequest{Messages: []contract.Message{{Role: contract.RoleTool, Content: "x"}}}, nil)
	if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "toolCallId") {
		t.Fatalf("缺少 toolCallId 应报错: %v", err)
	}
}

// Agent 的模型下拉用 provider/model 形式；pick 必须能精确匹配到该条配置。
func TestPickByProviderSlashModel(t *testing.T) {
	svc, _, _ := newTestService(t)
	views, _, err := svc.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(views) == 0 {
		t.Fatal("没有可用配置")
	}
	view := views[0]
	key := view.Provider + "/" + view.Model
	picked, err := svc.pick(key)
	if err != nil {
		t.Fatalf("pick(%q) 失败: %v", key, err)
	}
	if picked.ID != view.ID {
		t.Fatalf("pick(%q) 命中了错误配置: got %s want %s", key, picked.ID, view.ID)
	}
}

// TestSetDefault 验证默认模型可以在已保存的配置之间切换，且不改动配置本身。
func TestSetDefault(t *testing.T) {
	svc, _, _ := newTestService(t)
	first := seedID(t, svc)

	views, _, err := svc.Save(SaveInput{
		Provider: "openai",
		Model:    "gpt-test",
		BaseURL:  "http://127.0.0.1:1/v1",
		APIKey:   "sk-test",
	})
	if err != nil {
		t.Fatalf("新增第二条配置失败: %v", err)
	}
	second := ""
	for _, v := range views {
		if v.ID != first {
			second = v.ID
		}
	}
	if second == "" {
		t.Fatal("没有找到第二条模型配置")
	}

	views, defaultID, err := svc.SetDefault(second)
	if err != nil {
		t.Fatalf("SetDefault 失败: %v", err)
	}
	if defaultID != second {
		t.Fatalf("默认 ID 应为 %q，实际 %q", second, defaultID)
	}
	for _, v := range views {
		if v.IsDefault != (v.ID == second) {
			t.Fatalf("模型 %q 的 isDefault=%v，与默认 ID 不一致", v.ID, v.IsDefault)
		}
	}

	if _, _, err := svc.SetDefault("missing"); err == nil {
		t.Fatal("不存在的 id 应报错")
	}
	if _, _, err := svc.SetDefault("  "); err == nil {
		t.Fatal("空 id 应报错")
	}
}

// ---------- 缓存命中（提供方上报） ----------

// TestUsageOfCachedTokens 验证 usageOf 能取到 eino 的 PromptTokenDetails.CachedTokens。
func TestUsageOfCachedTokens(t *testing.T) {
	msg := &schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens:       1200,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 900},
		CompletionTokens:   80,
		TotalTokens:        1280,
	}}}
	u := usageOf(msg)
	if u.PromptTokens != 1200 || u.CompletionTokens != 80 || u.TotalTokens != 1280 {
		t.Fatalf("基础用量解析不符: %+v", u)
	}
	if u.CachedTokens != 900 {
		t.Fatalf("必须取到提示词缓存命中数，实际 %d", u.CachedTokens)
	}
	if empty := usageOf(nil); empty.CachedTokens != 0 {
		t.Fatalf("空消息不应编造缓存命中: %+v", empty)
	}
}

// TestStreamUsageKeepsCachedTokensFromChunks 钉住流式对账：
// eino 的 ConcatMessages 只逐字段取最大，若合并后的消息丢了 PromptTokenDetails，
// 必须用逐 chunk 累计到的缓存命中数兜底，不能静默变成 0。
func TestStreamUsageKeepsCachedTokensFromChunks(t *testing.T) {
	acc := contract.Usage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, CachedTokens: 640}
	merged := &schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens:     1000,
		CompletionTokens: 50,
		TotalTokens:      1050,
	}}}
	u := streamUsage(merged, acc)
	if u.CachedTokens != 640 {
		t.Fatalf("合并后丢失细节时应回落到逐 chunk 累计值，实际 %d", u.CachedTokens)
	}
	if u.PromptTokens != 1000 || u.TotalTokens != 1050 {
		t.Fatalf("其余字段应以合并后的消息为准: %+v", u)
	}

	// 合并后自带细节时以它为准（它是完整的一条消息）。
	mergedWithDetails := &schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens:       1000,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 512},
		CompletionTokens:   50,
		TotalTokens:        1050,
	}}}
	if u := streamUsage(mergedWithDetails, acc); u.CachedTokens != 512 {
		t.Fatalf("合并后已有明细时应以它为准，实际 %d", u.CachedTokens)
	}

	// 合并后完全没有用量时整体回落到逐 chunk 累计值。
	if u := streamUsage(&schema.Message{}, acc); u.CachedTokens != 640 || u.TotalTokens != 1050 {
		t.Fatalf("没有合并用量时应回落到累计值: %+v", u)
	}
}

// TestStreamParsesCachedTokensFromProvider 验证真实流式路径（OpenAI 兼容 SSE）能拿到缓存命中数。
func TestStreamParsesCachedTokensFromProvider(t *testing.T) {
	srv := fakeChatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk := map[string]any{
			"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "pong"}, "finish_reason": "stop"}},
			"usage": map[string]any{
				"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105,
				"prompt_tokens_details": map[string]any{"cached_tokens": 64},
			},
		}
		b, _ := json.Marshal(chunk)
		_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{Provider: "openai", Model: "gpt-test", BaseURL: srv.URL + "/v1", APIKey: "sk-test", SetDefault: true}); err != nil {
		t.Fatal(err)
	}
	reply, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if reply.Usage.PromptTokens != 100 || reply.Usage.CachedTokens != 64 {
		t.Fatalf("流式路径必须带出提供方上报的缓存命中数: %+v", reply.Usage)
	}
}
