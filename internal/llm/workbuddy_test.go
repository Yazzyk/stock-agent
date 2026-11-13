package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// newWorkBuddyTestService 创建隔离的 Service：外部导入源默认关闭，避免读到开发者本机的真实凭证。
func newWorkBuddyTestService(t *testing.T) (*Service, *workBuddy) {
	t.Helper()
	svc, _, _ := newTestService(t)
	svc.wb.externalRootsOverride = func() []workBuddyExternalRoot { return nil }
	return svc, svc.wb
}

// testWorkBuddyCredential 构造一份未过期凭证（国内版）。
func testWorkBuddyCredential(accountID string) *workBuddyCredential {
	return &workBuddyCredential{
		AccessToken:  "access-token-" + accountID,
		RefreshToken: "refresh-token-" + accountID,
		Domain:       "www.workbuddy.cn",
		ExpiresAt:    time.Now().Add(24 * time.Hour).Unix(),
		RefreshAt:    time.Now().Add(30 * 24 * time.Hour).Unix(),
		LastRefresh:  time.Now().Unix(),
		AccountID:    accountID,
		UID:          "uid-" + accountID,
		AccountName:  "研究员",
		Source:       workBuddyCredSourceLocal,
	}
}

// writeWorkBuddyVaultFile 在指定根目录写一份凭证，模拟 dsh / go-stock 的落盘形态。
func writeWorkBuddyVaultFile(t *testing.T, root, region string, cred *workBuddyCredential) string {
	t.Helper()
	dir := filepath.Join(root, region)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	doc := workBuddyVaultDocOf(cred)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, cred.AccountID+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkBuddyVaultRoundTripAndLogout(t *testing.T) {
	svc, wb := newWorkBuddyTestService(t)
	cred := testWorkBuddyCredential("acc1")
	path, err := wb.saveCredential(cred)
	if err != nil {
		t.Fatalf("保存凭证失败: %v", err)
	}
	if !strings.Contains(path, string(filepath.Separator)+workBuddyRegionCN+string(filepath.Separator)) {
		t.Fatalf("凭证未落到 cn 区域: %s", path)
	}
	if filepath.Base(path) != "acc1.json" {
		t.Fatalf("凭证文件名应以账号 ID 命名: %s", path)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("凭证文件不存在: %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("凭证文件权限 = %v, want 0600", info.Mode().Perm())
	}
	parsed, parseErr := wb.parseVaultFile(path)
	if parseErr != nil {
		t.Fatalf("解析凭证失败: %v", parseErr)
	}
	if parsed.AccessToken != cred.AccessToken || parsed.RefreshToken != cred.RefreshToken {
		t.Fatal("token 未正确往返")
	}
	if parsed.AccountName != "研究员" || parsed.AccountID != "acc1" {
		t.Fatalf("账号信息不符: %+v", parsed)
	}
	if parsed.ExpiresAt != cred.ExpiresAt {
		t.Fatalf("到期时间不符: %d want %d", parsed.ExpiresAt, cred.ExpiresAt)
	}

	status := svc.WorkBuddyStatus()
	if !status.LoggedIn || status.AccountName != "研究员" || status.Domain != "www.workbuddy.cn" {
		t.Fatalf("登录状态不符: %+v", status)
	}
	if status.ExpiresAt == "" || !strings.Contains(status.ExpiresAt, "T") {
		t.Fatalf("到期时间应为 RFC3339: %q", status.ExpiresAt)
	}

	if err := svc.WorkBuddyLogout(); err != nil {
		t.Fatalf("退出登录失败: %v", err)
	}
	if svc.WorkBuddyStatus().LoggedIn {
		t.Fatal("退出后不应再报告已登录")
	}
	if _, err := os.Stat(wb.vaultPath()); !os.IsNotExist(err) {
		t.Fatalf("退出后凭证目录应被删除: %v", err)
	}
}

func TestWorkBuddyVaultMigratesFromDSH(t *testing.T) {
	svc, wb := newWorkBuddyTestService(t)
	dshRoot := filepath.Join(t.TempDir(), workBuddyVaultDirName)
	cred := testWorkBuddyCredential("dsh-acc")
	writeWorkBuddyVaultFile(t, dshRoot, workBuddyRegionCN, cred)
	wb.externalRootsOverride = func() []workBuddyExternalRoot {
		return []workBuddyExternalRoot{{root: dshRoot, source: workBuddyCredSourceDSH}}
	}

	got, err := wb.credential(context.Background())
	if err != nil {
		t.Fatalf("从 dsh 读取凭证失败: %v", err)
	}
	if got.AccessToken != cred.AccessToken {
		t.Fatalf("读到的 token 不符: %q", got.AccessToken)
	}
	localPath := filepath.Join(wb.vaultRoot(), workBuddyRegionCN, "dsh-acc.json")
	if _, statErr := os.Stat(localPath); statErr != nil {
		t.Fatalf("dsh 凭证应被迁移到本地库: %v", statErr)
	}

	// 迁移后即不再依赖 dsh：外部源消失也照样可用。
	wb.externalRootsOverride = func() []workBuddyExternalRoot { return nil }
	wb.resetCredentialCache()
	if !svc.WorkBuddyStatus().LoggedIn {
		t.Fatal("迁移后本地库应可独立提供登录态")
	}
}

func TestParseWorkBuddyModels(t *testing.T) {
	body := []byte(`{
	  "code": 0,
	  "data": {"models": [
	    {"id":"glm-5.3","name":"GLM 5.3","maxInputTokens":200000,"maxOutputTokens":32000,"credits":"x0.29 credits","supportsImages":true,"descriptionZh":"通用对话"},
	    {"id":"minimax-m3","name":"MiniMax M3","maxInputTokens":100000,"maxOutputTokens":16000,"credits":0,"descriptionEn":"cheap"},
	    {"id":"image-3","name":"Image 3","maxInputTokens":0,"maxOutputTokens":0},
	    {"id":"","name":"坏条目"}
	  ]}
	}`)
	models, err := parseWorkBuddyModels(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("模型数 = %d, want 3（空 id 应跳过）", len(models))
	}
	if models[0].ContextWindow != 200000 || models[0].MaxOutputTokens != 32000 {
		t.Fatalf("窗口解析不符: %+v", models[0])
	}
	if models[0].CreditHint != "x0.29" || !models[0].SupportsImages || models[0].Description != "通用对话" {
		t.Fatalf("积分/多模态/描述解析不符: %+v", models[0])
	}
	if models[1].CreditHint != "免费" || models[1].Description != "cheap" {
		t.Fatalf("数字积分与英文描述解析不符: %+v", models[1])
	}
	if !models[2].IsImageModel {
		t.Fatalf("图像模型应被标记: %+v", models[2])
	}

	if _, err := parseWorkBuddyModels([]byte(`{"code":0,"data":{"models":[]}}`)); err == nil {
		t.Fatal("空目录应报错")
	}
}

func TestNormalizeWorkBuddyBody(t *testing.T) {
	body := map[string]any{
		"stream":           false,
		"reasoning_effort": "off",
		"messages": []any{
			map[string]any{"role": "developer", "content": "规则"},
			map[string]any{"role": "user", "content": "你好"},
		},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "market_quote"}},
	}
	normalizeWorkBuddyBody(body)
	if body["stream"] != true {
		t.Fatal("必须强制 stream=true")
	}
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatal("reasoning_effort=off 应被移除")
	}
	if body["tool_choice"] != "market_quote" {
		t.Fatalf("tool_choice 应拍平成工具名: %v", body["tool_choice"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("developer→system 后消息数应不变: %d", len(messages))
	}
	head, _ := messages[0].(map[string]any)
	if head["role"] != "system" {
		t.Fatalf("首条应为 system: %v", head["role"])
	}

	// 首条不是 system 时补一条兜底 system。
	plain := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	normalizeWorkBuddyBody(plain)
	plainMessages, _ := plain["messages"].([]any)
	if len(plainMessages) != 2 {
		t.Fatalf("应插入兜底 system: %d", len(plainMessages))
	}
	plainHead, _ := plainMessages[0].(map[string]any)
	if plainHead["role"] != "system" || plainHead["content"] != workBuddyFallbackSystemPrompt {
		t.Fatalf("兜底 system 不符: %+v", plainHead)
	}

	// tool_choice=none 时连同 tools 一起删除。
	none := map[string]any{
		"messages":    []any{map[string]any{"role": "system", "content": "s"}},
		"tool_choice": map[string]any{"type": "none"},
		"tools":       []any{map[string]any{"type": "function"}},
	}
	normalizeWorkBuddyBody(none)
	if _, exists := none["tool_choice"]; exists {
		t.Fatal("tool_choice=none 应被删除")
	}
	if _, exists := none["tools"]; exists {
		t.Fatal("tool_choice=none 时 tools 应被删除")
	}
}

func TestBuildWorkBuddyBodyValidatesToolMessages(t *testing.T) {
	_, err := buildWorkBuddyBody([]*schema.Message{schema.ToolMessage("x", "")}, nil, "glm-5.3", 8192)
	if err == nil || !strings.Contains(err.Error(), "toolCallId") {
		t.Fatalf("缺 tool_call_id 应报可操作错误: %v", err)
	}
}

func TestWorkBuddyRefreshWritesBackToVault(t *testing.T) {
	_, wb := newWorkBuddyTestService(t)
	var refreshCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, workBuddyRefreshPath) {
			t.Errorf("意外的请求路径: %s", r.URL.Path)
			return
		}
		if got := r.Header.Get("X-Refresh-Token"); got != "refresh-token-acc1" {
			t.Errorf("X-Refresh-Token = %q", got)
		}
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"accessToken":"new-access","refreshToken":"new-refresh","expiresAt":%d,"domain":"www.workbuddy.cn"}}`, time.Now().Add(2*time.Hour).Unix())))
	}))
	defer srv.Close()
	wb.baseOverride = func(string) string { return srv.URL }

	expired := testWorkBuddyCredential("acc1")
	expired.ExpiresAt = time.Now().Add(-10 * time.Second).Unix()
	path, err := wb.saveCredential(expired)
	if err != nil {
		t.Fatal(err)
	}
	got, err := wb.credential(context.Background())
	if err != nil {
		t.Fatalf("续期失败: %v", err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" {
		t.Fatalf("续期结果不符: %+v", got)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh 调用次数 = %d, want 1", refreshCalls.Load())
	}
	stored, err := wb.parseVaultFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "new-access" {
		t.Fatalf("续期结果未回写本地库: %q", stored.AccessToken)
	}
}

// workBuddyChatServer 是同时支持模型目录与 SSE 对话的假网关。
func workBuddyChatServer(t *testing.T, onChat func(body map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, workBuddyModelsPath):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"models":[{"id":"glm-5.3","name":"GLM 5.3","maxInputTokens":200000,"maxOutputTokens":32000}]}}`))
		case strings.HasSuffix(r.URL.Path, workBuddyChatPath):
			if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
				t.Errorf("缺少 Authorization: %q", got)
			}
			if r.Header.Get("X-Requested-With") == "" || r.Header.Get("Origin") == "" {
				t.Errorf("缺少 WorkBuddy 归属头")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("解析请求体失败: %v", err)
			}
			if onChat != nil {
				onChat(body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			frames := []string{
				`{"id":"x","model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"好"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"market_quote","arguments":"{\"codes\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"[\"600000\"]}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`,
			}
			for _, frame := range frames {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			t.Errorf("意外的请求路径: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestWorkBuddyChatAndStream(t *testing.T) {
	svc, wb := newWorkBuddyTestService(t)
	var chatBodies []map[string]any
	srv := workBuddyChatServer(t, func(body map[string]any) { chatBodies = append(chatBodies, body) })
	defer srv.Close()
	wb.baseOverride = func(string) string { return srv.URL }
	if _, err := wb.saveCredential(testWorkBuddyCredential("acc1")); err != nil {
		t.Fatal(err)
	}
	views, _, err := svc.Save(SaveInput{Provider: "workbuddy", Model: "glm-5.3", SetDefault: true})
	if err != nil {
		t.Fatalf("保存 WorkBuddy 配置失败: %v", err)
	}
	var wbView *ProviderView
	for i := range views {
		if views[i].Provider == "workbuddy" {
			wbView = &views[i]
		}
	}
	if wbView == nil || !wbView.HasKey || wbView.AuthMode != "workbuddy" || wbView.AccountName != "研究员" {
		t.Fatalf("列表视图不符: %+v", views)
	}

	reply, err := svc.Chat(context.Background(), contract.ChatRequest{
		System:   "你是研究员",
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "查一下"}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if reply.Message.Content != "你好" {
		t.Fatalf("内容不符: %q", reply.Message.Content)
	}
	if len(reply.Message.ToolCalls) != 1 {
		t.Fatalf("工具调用数 = %d, want 1", len(reply.Message.ToolCalls))
	}
	call := reply.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "market_quote" || call.Arguments != `{"codes":["600000"]}` {
		t.Fatalf("工具调用解析不符: %+v", call)
	}
	if reply.Usage.TotalTokens != 12 || reply.Usage.PromptTokens != 7 {
		t.Fatalf("用量解析不符: %+v", reply.Usage)
	}

	var deltas strings.Builder
	streamed, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "查一下"}},
	}, func(delta string) { deltas.WriteString(delta) })
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if deltas.String() != "你好" || streamed.Message.Content != "你好" {
		t.Fatalf("流式内容不符: delta=%q merged=%q", deltas.String(), streamed.Message.Content)
	}
	if len(streamed.Message.ToolCalls) != 1 || streamed.Message.ToolCalls[0].Arguments != `{"codes":["600000"]}` {
		t.Fatalf("流式工具调用合并不符: %+v", streamed.Message.ToolCalls)
	}

	if len(chatBodies) != 2 {
		t.Fatalf("对话请求次数 = %d, want 2", len(chatBodies))
	}
	for _, body := range chatBodies {
		if body["stream"] != true {
			t.Fatal("请求体必须 stream=true")
		}
		messages, _ := body["messages"].([]any)
		if len(messages) == 0 {
			t.Fatal("请求体缺少 messages")
		}
		head, _ := messages[0].(map[string]any)
		if head["role"] != "system" {
			t.Fatalf("首条消息应为 system: %v", head["role"])
		}
		if _, isObject := body["tool_choice"].(map[string]any); isObject {
			t.Fatal("tool_choice 不能是对象")
		}
	}
}

func TestWorkBuddyRejectsUnknownModel(t *testing.T) {
	svc, wb := newWorkBuddyTestService(t)
	srv := workBuddyChatServer(t, nil)
	defer srv.Close()
	wb.baseOverride = func(string) string { return srv.URL }
	if _, err := wb.saveCredential(testWorkBuddyCredential("acc1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Save(SaveInput{Provider: "workbuddy", Model: "not-in-catalog", SetDefault: true}); err != nil {
		t.Fatalf("保存不该联网校验: %v", err)
	}
	_, err := svc.Chat(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "不在当前账号的模型目录") {
		t.Fatalf("未知模型应给出目录提示: %v", err)
	}
}

func TestWorkBuddyLoginFlow(t *testing.T) {
	svc, wb := newWorkBuddyTestService(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/state"):
			if r.URL.Query().Get("platform") != "workbuddy" {
				t.Errorf("platform 参数缺失: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"state":"state-1","authUrl":"https://example.test/login?state=state-1"}}`))
		case strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token"):
			if r.URL.Query().Get("state") != "state-1" {
				t.Errorf("state 参数不符: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"accessToken":"login-access","refreshToken":"login-refresh","expiresAt":%d,"refreshExpiresAt":%d,"domain":"www.workbuddy.cn"}}`, time.Now().Add(time.Hour).Unix(), time.Now().Add(24*time.Hour).Unix())))
		case strings.HasSuffix(r.URL.Path, workBuddyAccountPath):
			if got := r.Header.Get("Authorization"); got != "Bearer login-access" {
				t.Errorf("账号资料请求缺少 Bearer token: %q", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"uid":"10086","nickname":"老王","domain":"www.workbuddy.cn"}}`))
		default:
			t.Errorf("意外的请求路径: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	wb.baseOverride = func(string) string { return srv.URL }
	wb.loginBaseOverride = func(string) string { return srv.URL }

	session, err := svc.WorkBuddyLoginStart(context.Background(), "cn")
	if err != nil {
		t.Fatalf("发起登录失败: %v", err)
	}
	if session.LoginID == "" || !strings.Contains(session.URL, "state-1") {
		t.Fatalf("登录会话不符: %+v", session)
	}
	result := svc.WorkBuddyLoginPoll(context.Background(), session.LoginID)
	if !result.OK || result.AccountName != "老王" {
		t.Fatalf("轮询登录结果不符: %+v", result)
	}
	status := svc.WorkBuddyStatus()
	if !status.LoggedIn || status.AccountName != "老王" {
		t.Fatalf("登录后状态不符: %+v", status)
	}
	if _, statErr := os.Stat(filepath.Join(wb.vaultRoot(), workBuddyRegionCN, "10086.json")); statErr != nil {
		t.Fatalf("登录凭证未按账号 ID 落盘: %v", statErr)
	}
	if missing := svc.WorkBuddyLoginPoll(context.Background(), "nope"); missing.OK || missing.Message == "" {
		t.Fatalf("未知会话应给出可操作提示: %+v", missing)
	}
}

func TestWorkBuddyImportAndLogout(t *testing.T) {
	svc, wb := newWorkBuddyTestService(t)
	extRoot := filepath.Join(t.TempDir(), workBuddyVaultDirName)
	extCred := testWorkBuddyCredential("ext-acc")
	extCred.Domain = "www.workbuddy.ai"
	writeWorkBuddyVaultFile(t, extRoot, workBuddyRegionGlobal, extCred)
	wb.externalRootsOverride = func() []workBuddyExternalRoot {
		return []workBuddyExternalRoot{{root: extRoot, source: workBuddyCredSourceGoStock}}
	}
	if _, _, err := svc.WorkBuddyImport(context.Background()); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if !svc.WorkBuddyStatus().LoggedIn {
		t.Fatal("导入后应处于登录状态")
	}
	if _, statErr := os.Stat(filepath.Join(wb.vaultRoot(), workBuddyRegionGlobal, "ext-acc.json")); statErr != nil {
		t.Fatalf("导入的凭证未落本地库: %v", statErr)
	}
	// 退出只清本地库；外部源仍存在时会按读取优先级再次被发现（与 dsh/go-stock 语义一致）。
	wb.externalRootsOverride = func() []workBuddyExternalRoot { return nil }
	if err := svc.WorkBuddyLogout(); err != nil {
		t.Fatalf("退出失败: %v", err)
	}
	if svc.WorkBuddyStatus().LoggedIn {
		t.Fatal("退出后不应再登录")
	}

	if _, _, err := svc.WorkBuddyImport(context.Background()); err == nil {
		t.Fatal("没有外部凭证库时应给出可操作错误")
	}
}

// TestWorkBuddyUsageCachedTokens 验证 WorkBuddy 也兼容两种缓存命中字段（拿不到就为 0）。
func TestWorkBuddyUsageCachedTokens(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"deepseek 风格", `{"prompt_tokens":100,"prompt_cache_hit_tokens":64}`, 64},
		{"openai 风格", `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40}}`, 40},
		{"两种都给取较大值", `{"prompt_tokens":100,"prompt_cache_hit_tokens":64,"prompt_tokens_details":{"cached_tokens":40}}`, 64},
		{"上游没给", `{"prompt_tokens":100}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var usage workBuddyUsage
			if err := json.Unmarshal([]byte(c.raw), &usage); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got := usage.cachedTokens(); got != c.want {
				t.Fatalf("cachedTokens()=%d, want %d", got, c.want)
			}
		})
	}
	var missing *workBuddyUsage
	if missing.cachedTokens() != 0 {
		t.Fatal("空 usage 不能编造缓存命中")
	}
}
