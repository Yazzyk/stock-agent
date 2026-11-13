package llm

// WorkBuddy 浏览器登录（三步，与官方客户端一致）：
//  1. POST {base}/v2/plugin/auth/state?platform=workbuddy → state + authUrl
//  2. 用户在浏览器完成登录（扫码/账号密码都行）
//  3. GET  {base}/v2/plugin/auth/token?state=<state> → accessToken
// 拿到 token 后再取账号资料（昵称/UID），最后写进本地凭证库。
//
// 会话只保存在进程内，不落盘；进程重启即作废。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// workBuddyLoginTTL 一次登录会话的有效期。
const workBuddyLoginTTL = 10 * time.Minute

// WorkBuddyLoginSession 是一次进行中的登录（供界面打开浏览器并轮询）。
type WorkBuddyLoginSession struct {
	LoginID   string `json:"loginId"`
	Region    string `json:"region,omitempty"`
	URL       string `json:"url"`
	ExpiresIn int    `json:"expiresIn,omitempty"`
}

// WorkBuddyLoginResult 是轮询结果。
// 约定：ok=true 表示凭证已落盘；ok=false 且 message 为空表示「仍在等待用户操作」；
// ok=false 且 message 非空表示登录已失败（会话不存在/超时/保存失败），界面应停止轮询。
type WorkBuddyLoginResult struct {
	OK          bool   `json:"ok"`
	Message     string `json:"message,omitempty"`
	AccountName string `json:"accountName,omitempty"`
}

// workBuddyLoginSession 是进程内的登录会话。
type workBuddyLoginSession struct {
	region    string
	base      string
	state     string
	expiresAt time.Time
	done      bool
	result    WorkBuddyLoginResult
}

// normalizeWorkBuddyRegion 归一登录区域：cn（默认）或 global。
func normalizeWorkBuddyRegion(region string) string {
	lower := strings.ToLower(strings.TrimSpace(region))
	if lower == "global" || lower == "intl" || lower == "ai" {
		return workBuddyRegionGlobal
	}
	return workBuddyRegionCN
}

// newWorkBuddyLoginID 生成不可预测的登录会话 ID。
func newWorkBuddyLoginID() string {
	return "wbo_" + workBuddyUUID()
}

// loginStateEnvelope 是申请 state 的响应。
type loginStateEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	} `json:"data"`
}

// requestLoginState 向指定登录网关申请一次 state 与授权地址。
func requestLoginState(ctx context.Context, base string) (loginStateEnvelope, error) {
	var envelope loginStateEnvelope
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+workBuddyAuthStatePath, strings.NewReader("{}"))
	if err != nil {
		return envelope, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := workBuddyHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return envelope, fmt.Errorf("无法连接 WorkBuddy 登录服务（%s）: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return envelope, readErr
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return envelope, fmt.Errorf("登录服务返回了非 JSON 响应（HTTP %d）", resp.StatusCode)
	}
	if envelope.Code != 0 {
		return envelope, fmt.Errorf("申请登录 state 被拒绝：code %d %s", envelope.Code, strings.TrimSpace(envelope.Msg))
	}
	return envelope, nil
}

// StartLogin 发起一次 WorkBuddy 登录，返回登录会话与需要用户在浏览器打开的地址。
func (w *workBuddy) StartLogin(ctx context.Context, region string) (WorkBuddyLoginSession, error) {
	region = normalizeWorkBuddyRegion(region)
	base := w.loginBase(region)
	envelope, err := requestLoginState(ctx, base)
	if err != nil && region == workBuddyRegionCN && w.loginBaseOverride == nil {
		// 主网关不可达时回退到备用网关（同一套 sign-in 接口）。
		if fallback, fallbackErr := requestLoginState(ctx, workBuddyCNLoginFallback); fallbackErr == nil {
			envelope, err, base = fallback, nil, workBuddyCNLoginFallback
		}
	}
	if err != nil {
		return WorkBuddyLoginSession{}, err
	}
	state := strings.TrimSpace(envelope.Data.State)
	if state == "" {
		return WorkBuddyLoginSession{}, errors.New("登录服务未返回 state，请稍后重试")
	}
	authURL := strings.TrimSpace(envelope.Data.AuthURL)
	if authURL == "" {
		authURL = base + "/login?platform=workbuddy&state=" + state
	}
	loginID := newWorkBuddyLoginID()
	session := &workBuddyLoginSession{
		region:    region,
		base:      base,
		state:     state,
		expiresAt: time.Now().Add(workBuddyLoginTTL),
	}
	w.loginMu.Lock()
	for id, existing := range w.logins {
		if existing.done || time.Now().After(existing.expiresAt) {
			delete(w.logins, id)
		}
	}
	w.logins[loginID] = session
	w.loginMu.Unlock()
	return WorkBuddyLoginSession{
		LoginID:   loginID,
		Region:    region,
		URL:       authURL,
		ExpiresIn: int(workBuddyLoginTTL.Seconds()),
	}, nil
}

// PollLogin 轮询一次登录结果；拿到 token 后写入本地凭证库并刷新缓存。
func (w *workBuddy) PollLogin(ctx context.Context, loginID string) WorkBuddyLoginResult {
	w.loginMu.Lock()
	session := w.logins[loginID]
	if session == nil {
		w.loginMu.Unlock()
		return WorkBuddyLoginResult{Message: "登录会话不存在，请重新发起登录"}
	}
	if session.done {
		result := session.result
		w.loginMu.Unlock()
		return result
	}
	if time.Now().After(session.expiresAt) {
		session.done = true
		session.result = WorkBuddyLoginResult{Message: "登录超时，请重新发起"}
		result := session.result
		w.loginMu.Unlock()
		return result
	}
	w.loginMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, session.base+workBuddyAuthTokenPath+session.state, nil)
	if err != nil {
		return WorkBuddyLoginResult{}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := workBuddyHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		// 网络抖动不算失败：让界面继续轮询。
		return WorkBuddyLoginResult{}
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return WorkBuddyLoginResult{}
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
		return WorkBuddyLoginResult{}
	}
	if envelope.Code != 0 {
		// code 11217「login ing...」表示仍在等待用户完成登录，继续轮询。
		return WorkBuddyLoginResult{}
	}
	accessToken := strings.TrimSpace(envelope.Data.AccessToken)
	if accessToken == "" {
		return WorkBuddyLoginResult{}
	}

	cred := &workBuddyCredential{
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(envelope.Data.RefreshToken),
		Domain:       strings.TrimSpace(envelope.Data.Domain),
		ExpiresAt:    normalizeWorkBuddyTimestamp(envelope.Data.ExpiresAt),
		RefreshAt:    normalizeWorkBuddyTimestamp(envelope.Data.RefreshExpiresAt),
		LastRefresh:  time.Now().Unix(),
		Source:       workBuddyCredSourceLocal,
	}
	// 补账号资料（失败不影响登录本身）。
	if profile, profileErr := w.fetchProfile(ctx, cred); profileErr == nil && profile != nil {
		cred.UID = profile.uid
		cred.AccountName = profile.nickname
		cred.AccountID = profile.accountID
		if cred.Domain == "" {
			cred.Domain = profile.domain
		}
	}
	if _, saveErr := w.saveCredential(cred); saveErr != nil {
		result := WorkBuddyLoginResult{Message: "保存 WorkBuddy 登录凭证失败：" + saveErr.Error()}
		w.loginMu.Lock()
		session.done = true
		session.result = result
		w.loginMu.Unlock()
		return result
	}
	w.resetCredentialCache()
	name := cred.AccountName
	if name == "" {
		name = cred.AccountID
	}
	result := WorkBuddyLoginResult{OK: true, AccountName: name}
	w.loginMu.Lock()
	session.done = true
	session.result = result
	w.loginMu.Unlock()
	return result
}

// workBuddyProfile 是账号资料（仅展示用）。
type workBuddyProfile struct {
	uid       string
	nickname  string
	accountID string
	domain    string
}

// fetchProfile 用 accessToken 拉账号资料（昵称/UID/账号 ID）。
func (w *workBuddy) fetchProfile(ctx context.Context, cred *workBuddyCredential) (*workBuddyProfile, error) {
	base := w.baseOf(cred.Domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, workBuddyAPIEndpoint(base, workBuddyAccountPath), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/")
	if cred.Domain != "" {
		req.Header.Set("X-Domain", cred.Domain)
	}
	resp, err := workBuddyHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, readErr
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			UID          json.RawMessage `json:"uid"`
			UIN          json.RawMessage `json:"uin"`
			Nickname     string          `json:"nickname"`
			ScreenName   string          `json:"screenName"`
			EnterpriseID json.RawMessage `json:"enterpriseId"`
			Domain       string          `json:"domain"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	uid := workBuddyRawText(envelope.Data.UID)
	profile := &workBuddyProfile{
		uid:      uid,
		nickname: firstNonEmptyText(envelope.Data.Nickname, envelope.Data.ScreenName),
		domain:   strings.TrimSpace(envelope.Data.Domain),
	}
	profile.accountID = firstNonEmptyText(uid, workBuddyRawText(envelope.Data.UIN), workBuddyRawText(envelope.Data.EnterpriseID))
	return profile, nil
}

// workBuddyRawText 把 string/number 两类 JSON 值统一成文本。
func workBuddyRawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

// firstNonEmptyText 返回第一个非空字符串。
func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Import 把 dsh / go-stock 已有的 WorkBuddy 登录态一次性拷进本地库。
func (w *workBuddy) Import(context.Context) (int, []string, error) {
	imported, names, err := w.importExternalCredentials()
	if err != nil {
		return 0, nil, err
	}
	w.resetCredentialCache()
	return imported, names, nil
}

// Logout 清空本应用自己的 WorkBuddy 凭证库（不影响 dsh / go-stock）。
func (w *workBuddy) Logout() error {
	if err := w.purgeVault(); err != nil {
		return fmt.Errorf("清空 WorkBuddy 凭证失败：%w", err)
	}
	w.resetCredentialCache()
	return nil
}
