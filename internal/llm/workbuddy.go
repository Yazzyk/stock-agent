// WorkBuddy 本机登录态接入：不填 API Key，凭证由本机 WorkBuddy 登录态（或 dsh / go-stock 导入）提供。
//
// 网关：国内 https://www.workbuddy.cn，国际 https://www.workbuddy.ai；
// 对话 /v2/chat/completions、模型目录 /v2/enterprises/personal/models、
// 刷新 /v2/plugin/auth/token/refresh。
//
// 红线：accessToken / refreshToken 只在内存里拼请求头，不写日志、不回传前端；
// 落盘只在用户自己的数据目录内（<DataDir>/workbuddy-vault/<region>/*.json，0600）。
package llm

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// workBuddy 持有 WorkBuddy 的凭证缓存、模型目录缓存与登录会话。
type workBuddy struct {
	root string
	log  *slog.Logger

	mu     sync.Mutex
	cached *workBuddyCredential

	modelsMu        sync.Mutex
	modelsAt        time.Time
	modelsTokenHash string
	modelsCache     []WorkBuddyModel

	loginMu sync.Mutex
	logins  map[string]*workBuddyLoginSession

	// 测试注入点：生产代码恒为 nil，仅单测把网关指到本地假上游。
	baseOverride          func(domain string) string
	loginBaseOverride     func(region string) string
	externalRootsOverride func() []workBuddyExternalRoot
}

// newWorkBuddy 创建 WorkBuddy 接入器；root 是本地凭证库根目录。
func newWorkBuddy(root string, log *slog.Logger) *workBuddy {
	if log == nil {
		log = slog.Default()
	}
	return &workBuddy{root: root, log: log, logins: map[string]*workBuddyLoginSession{}}
}

// WorkBuddyStatus 是给界面展示的登录状态，绝不包含 token。
type WorkBuddyStatus struct {
	LoggedIn    bool   `json:"loggedIn"`
	AccountName string `json:"accountName,omitempty"`
	Domain      string `json:"domain,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	VaultPath   string `json:"vaultPath,omitempty"`
	Message     string `json:"message,omitempty"`
}

// WorkBuddyStatus 返回本机 WorkBuddy 登录状态（只读本地库，不触发网络刷新）。
func (s *Service) WorkBuddyStatus() WorkBuddyStatus { return s.wb.status() }

// WorkBuddyModels 拉取当前登录账号的模型目录（过滤图像模型，带 5 分钟缓存）。
func (s *Service) WorkBuddyModels(ctx context.Context) ([]WorkBuddyModel, error) {
	models, err := s.wb.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WorkBuddyModel, 0, len(models))
	for _, m := range models {
		if m.IsImageModel {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// WorkBuddyLoginStart 发起一次 WorkBuddy 浏览器登录，返回登录会话与授权地址。
func (s *Service) WorkBuddyLoginStart(ctx context.Context, region string) (WorkBuddyLoginSession, error) {
	return s.wb.StartLogin(ctx, region)
}

// WorkBuddyLoginPoll 轮询一次登录结果；ok=true 表示凭证已落盘。
func (s *Service) WorkBuddyLoginPoll(ctx context.Context, loginID string) WorkBuddyLoginResult {
	return s.wb.PollLogin(ctx, loginID)
}

// WorkBuddyImport 从 dsh / go-stock 的凭证库导入一次，返回导入数量与账号名。
func (s *Service) WorkBuddyImport(ctx context.Context) (int, []string, error) {
	return s.wb.Import(ctx)
}

// WorkBuddyLogout 清空本应用自己的 WorkBuddy 凭证库（不影响 dsh / go-stock）。
func (s *Service) WorkBuddyLogout() error { return s.wb.Logout() }

// httpClient 返回带超时的 HTTP 客户端；WorkBuddy 的推理较慢，超时单独放宽。
func workBuddyHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}
