package llm

// WorkBuddy 登录态：读取本地库、自动续期、拼请求头。
// 对外只暴露「状态」与「请求头」，token 本身不落日志、不回传前端。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// WorkBuddyCNBase 国内版网关。
	WorkBuddyCNBase = "https://www.workbuddy.cn"
	// WorkBuddyGlobalBase 国际版网关。
	WorkBuddyGlobalBase = "https://www.workbuddy.ai"
	// workBuddyCNLoginFallback 国内版备用登录网关（同一套 sign-in 接口）。
	workBuddyCNLoginFallback = "https://www.codebuddy.cn"
	// workBuddyRefreshPath 刷新登录态的接口。
	workBuddyRefreshPath = "/v2/plugin/auth/token/refresh"
	// workBuddyChatPath 对话接口（只支持 SSE 流式）。
	workBuddyChatPath = "/v2/chat/completions"
	// workBuddyModelsPath 模型目录接口（/models 是 404）。
	workBuddyModelsPath = "/v2/enterprises/personal/models"
	// workBuddyAccountPath 账号资料接口（昵称/UID/账号 ID）。
	workBuddyAccountPath = "/v2/plugin/login/account"
	// workBuddyAuthStatePath 申请登录 state 的接口。
	workBuddyAuthStatePath = "/v2/plugin/auth/state?platform=workbuddy"
	// workBuddyAuthTokenPath 用 state 换 token 的接口前缀。
	workBuddyAuthTokenPath = "/v2/plugin/auth/token?state="
	// workBuddyFallbackSystemPrompt 网关要求首条消息必须是 system，缺失时补这条。
	workBuddyFallbackSystemPrompt = "You are a helpful assistant."
	// workBuddyRefreshSkew 提前续期的安全余量（秒）。
	workBuddyRefreshSkew = 120
)

// isWorkBuddyBaseURL 判断地址是否指向 WorkBuddy 网关。
func isWorkBuddyBaseURL(base string) bool {
	lower := strings.ToLower(strings.TrimSpace(base))
	return strings.Contains(lower, "workbuddy.cn") ||
		strings.Contains(lower, "workbuddy.ai") ||
		strings.Contains(lower, "codebuddy.cn")
}

// isWorkBuddyCN 判断域名是否属于国内版。
func isWorkBuddyCN(domain string) bool {
	lower := strings.ToLower(strings.TrimSpace(domain))
	return strings.HasSuffix(lower, "workbuddy.cn") || strings.HasSuffix(lower, "codebuddy.cn")
}

// baseOf 由 domain 决定走国内还是国际网关。
func (w *workBuddy) baseOf(domain string) string {
	if w.baseOverride != nil {
		return strings.TrimSuffix(w.baseOverride(domain), "/")
	}
	if isWorkBuddyCN(domain) {
		return WorkBuddyCNBase
	}
	return WorkBuddyGlobalBase
}

// loginBase 返回发起 OAuth 登录的网关。
func (w *workBuddy) loginBase(region string) string {
	if w.loginBaseOverride != nil {
		return strings.TrimSuffix(w.loginBaseOverride(region), "/")
	}
	if normalizeWorkBuddyRegion(region) == workBuddyRegionGlobal {
		return WorkBuddyGlobalBase
	}
	return WorkBuddyCNBase
}

// apiEndpoint 把网关根地址与 /v2 路径拼成完整接口地址。
// 兼容用户把 BaseURL 填成 https://host 或 https://host/v2 两种形态。
func workBuddyAPIEndpoint(base, path string) string {
	root := strings.TrimSuffix(strings.TrimSpace(base), "/")
	root = strings.TrimSuffix(root, "/v2")
	return root + path
}

// workBuddyUUID 生成一个随机 UUID（v4），用于网关的会话身份头。
func workBuddyUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// requestHeaders 返回调用 WorkBuddy 接口所需的完整请求头（token 只在内存里拼接）。
// 网关会按客户端归属/会话身份做渠道校验，因此除 Authorization 外还要补齐会话三连。
func workBuddyRequestHeaders(cred *workBuddyCredential, base string) map[string]string {
	headers := map[string]string{
		"Authorization":             "Bearer " + cred.AccessToken,
		"X-Requested-With":          "XMLHttpRequest",
		"X-CodeBuddy-Request":       "1",
		"X-Agent-Purpose":           "conversation",
		"X-IDE-Name":                "WorkBuddy",
		"X-IDE-Type":                "WorkBuddy",
		"X-Product":                 "WorkBuddy",
		"X-Conversation-ID":         workBuddyUUID(),
		"X-Conversation-Request-ID": workBuddyUUID(),
		"X-Root-Request-ID":         workBuddyUUID(),
		"X-Conversation-Message-ID": workBuddyUUID(),
		"Origin":                    base,
		"Referer":                   base + "/",
	}
	if domain := strings.TrimSpace(cred.Domain); domain != "" {
		headers["X-Domain"] = domain
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(cred.Domain)), "workbuddy.ai") {
		headers["Accept-Language"] = "en-US"
	} else {
		headers["Accept-Language"] = "zh-CN"
	}
	return headers
}

// applyWorkBuddyHeaders 把请求头写进 http.Request。
func applyWorkBuddyHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}

// cachedCredential 返回内存缓存（不读盘、不联网）。
func (w *workBuddy) cachedCredential() *workBuddyCredential {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cached
}

// storeCached 更新内存缓存。
func (w *workBuddy) storeCached(cred *workBuddyCredential) {
	w.mu.Lock()
	w.cached = cred
	w.mu.Unlock()
}

// resetCredentialCache 丢弃凭证与模型目录缓存（登录、退出、导入后调用）。
func (w *workBuddy) resetCredentialCache() {
	w.mu.Lock()
	w.cached = nil
	w.mu.Unlock()
	w.resetModelsCache()
}

// credentialNeedsRefresh 判断 accessToken 是否即将/已经过期。
func credentialNeedsRefresh(cred *workBuddyCredential) bool {
	if cred == nil || cred.ExpiresAt <= 0 {
		return false
	}
	return time.Now().Unix() >= cred.ExpiresAt-workBuddyRefreshSkew
}

// credential 返回当前可用的 WorkBuddy 凭证；必要时自动续期并回写本地库。
// 返回值只供内部构造请求头使用，调用方不得打印或回传。
func (w *workBuddy) credential(ctx context.Context) (*workBuddyCredential, error) {
	if cred := w.cachedCredential(); cred != nil && cred.AccessToken != "" && !credentialNeedsRefresh(cred) {
		return cred, nil
	}
	cred, err := w.bestCredentialFromVault()
	if err != nil {
		return nil, err
	}
	if credentialNeedsRefresh(cred) && strings.TrimSpace(cred.RefreshToken) != "" {
		refreshed, refreshErr := w.refreshCredential(ctx, cred)
		if refreshErr == nil {
			cred = refreshed
		} else {
			// 续期失败不直接报错：原 token 也许还能用，交给上游判定。
			w.log.Warn("WorkBuddy 登录态续期失败", "domain", cred.Domain, "error", refreshErr.Error())
		}
	}
	w.storeCached(cred)
	return cred, nil
}

// refreshCredential 用 refreshToken 换新的 accessToken，成功后回写本地库。
func (w *workBuddy) refreshCredential(ctx context.Context, cred *workBuddyCredential) (*workBuddyCredential, error) {
	if cred == nil || strings.TrimSpace(cred.RefreshToken) == "" {
		return nil, errors.New("WorkBuddy refreshToken 为空，无法续期")
	}
	base := w.baseOf(cred.Domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workBuddyAPIEndpoint(base, workBuddyRefreshPath), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Refresh-Token", cred.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "workbuddy")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/")
	resp, err := workBuddyHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("WorkBuddy 登录态续期失败：HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			ExpiresAt        int64  `json:"expiresAt"`
			RefreshExpiresAt int64  `json:"refreshExpiresAt"`
			Domain           string `json:"domain"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("WorkBuddy 登录态续期返回了非 JSON 响应")
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("WorkBuddy 登录态续期被拒绝：code %d", envelope.Code)
	}
	if strings.TrimSpace(envelope.Data.AccessToken) == "" {
		return nil, errors.New("WorkBuddy 登录态续期未返回 accessToken，请重新登录")
	}
	refreshed := *cred
	refreshed.AccessToken = strings.TrimSpace(envelope.Data.AccessToken)
	if envelope.Data.RefreshToken != "" {
		refreshed.RefreshToken = strings.TrimSpace(envelope.Data.RefreshToken)
	}
	if envelope.Data.ExpiresAt > 0 {
		refreshed.ExpiresAt = normalizeWorkBuddyTimestamp(envelope.Data.ExpiresAt)
	}
	if envelope.Data.RefreshExpiresAt > 0 {
		refreshed.RefreshAt = normalizeWorkBuddyTimestamp(envelope.Data.RefreshExpiresAt)
	}
	if envelope.Data.Domain != "" {
		refreshed.Domain = strings.TrimSpace(envelope.Data.Domain)
	}
	refreshed.LastRefresh = time.Now().Unix()
	refreshed.Source = workBuddyCredSourceLocal
	if path, saveErr := w.saveCredential(&refreshed); saveErr != nil {
		w.log.Warn("WorkBuddy 续期凭证落盘失败", "error", saveErr.Error())
	} else {
		refreshed.FilePath = path
	}
	return &refreshed, nil
}

// peekCredential 只读本地库与导入源，挑一份凭证用于展示与登录状态判断（不联网）。
func (w *workBuddy) peekCredential() *workBuddyCredential {
	if cred := w.cachedCredential(); cred != nil && cred.AccessToken != "" {
		return cred
	}
	candidates := w.candidates()
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		isLocal := candidate.source == workBuddyCredSourceLocal
		bestLocal := best.source == workBuddyCredSourceLocal
		if isLocal != bestLocal {
			if isLocal {
				best = candidate
			}
			continue
		}
		if candidate.cred.ExpiresAt > best.cred.ExpiresAt {
			best = candidate
		}
	}
	return best.cred
}

// hasLoginState 判断本机是否已有可用的 WorkBuddy 登录态（用于 List 的 hasKey）。
// 过期的 accessToken 只要 refreshToken 还能用，也算可用（下次请求会自动续期）。
func (w *workBuddy) hasLoginState() bool {
	cred := w.peekCredential()
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		return false
	}
	now := time.Now().Unix()
	if cred.ExpiresAt == 0 || now < cred.ExpiresAt {
		return true
	}
	return strings.TrimSpace(cred.RefreshToken) != "" && (cred.RefreshAt == 0 || now < cred.RefreshAt)
}

// status 返回给界面展示的登录状态；只读本地库，不触发网络刷新。
func (w *workBuddy) status() WorkBuddyStatus {
	status := WorkBuddyStatus{VaultPath: w.vaultPath()}
	cred := w.peekCredential()
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		status.Message = errWorkBuddyNotLoggedIn.Error()
		return status
	}
	status.LoggedIn = true
	status.AccountName = cred.AccountName
	status.Domain = cred.Domain
	if cred.ExpiresAt > 0 {
		status.ExpiresAt = time.Unix(cred.ExpiresAt, 0).Format(time.RFC3339)
	}
	now := time.Now().Unix()
	switch {
	case cred.ExpiresAt > 0 && now >= cred.ExpiresAt && (cred.RefreshAt == 0 || now < cred.RefreshAt):
		status.Message = "access token 已过期，下次调用时会自动续期"
	case cred.RefreshAt > 0 && now >= cred.RefreshAt:
		status.Message = "登录态已过期，请重新登录 WorkBuddy"
	}
	return status
}
