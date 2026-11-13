package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// SaveInput 是「模型设置」保存一条配置的入参。APIKey 为空表示保留原值。
type SaveInput struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Name       string `json:"name"`
	BaseURL    string `json:"baseUrl"`
	Model      string `json:"model"`
	APIKey     string `json:"apiKey"`
	MaxTokens  int    `json:"maxTokens"`
	SetDefault bool   `json:"setDefault"`
}

// TestResult 是一次连通性测试的结果。
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Message   string `json:"message"`
	Model     string `json:"model,omitempty"`
}

var validProviders = map[string]bool{
	"deepseek": true, "openai": true, "qwen": true, "ark": true,
	"ollama": true, "openai-compat": true, "opencode": true, "workbuddy": true,
}

// List 返回全部模型的展平视图与默认 ID。HasKey 只表达"是否已配置"，从不返回明文。
func (s *Service) List() ([]ProviderView, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listLocked(), s.settings.DefaultID, nil
}

// Save 新增或更新一条模型配置；apiKey 为空时保留旧密文。
func (s *Service) Save(in SaveInput) ([]ProviderView, string, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if !validProviders[provider] {
		return nil, "", fmt.Errorf("不支持的 provider %q（可选 deepseek/openai/qwen/ark/ollama/openai-compat/opencode/workbuddy）", in.Provider)
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		if provider == "workbuddy" {
			return nil, "", fmt.Errorf("WorkBuddy 需要先选择模型：请登录后从模型目录里选一个（未登录可先用「从 dsh/go-stock 导入」）")
		}
		model = defaultModelName(provider)
	}
	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL(provider)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = displayProvider(provider)
	}
	// maxTokens 缺省时按模型预设填官方默认值；模型不在预设里（自定义名）则保持 0（由适配器默认）。
	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokensFor(provider, model)
	}

	var encKey string
	if k := strings.TrimSpace(in.APIKey); k != "" {
		v, err := s.ring.Seal(k)
		if err != nil {
			return nil, "", err
		}
		encKey = v
	}

	s.mu.Lock()
	idx := -1
	for i, p := range s.settings.Providers {
		if p.ID == in.ID && in.ID != "" {
			idx = i
			break
		}
	}
	if in.ID != "" && idx < 0 {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("模型配置 %q 不存在", in.ID)
	}
	if idx >= 0 {
		p := s.settings.Providers[idx]
		p.Provider = provider
		p.Name = name
		p.BaseURL = baseURL
		p.Model = model
		p.MaxTokens = maxTokens
		if encKey != "" {
			p.APIKeyEnc = encKey
			p.EncKeyID = s.ring.ID()
		}
		s.settings.Providers[idx] = p
	} else {
		p := Provider{
			ID: newID(), Provider: provider, Name: name, BaseURL: baseURL, Model: model,
			MaxTokens: maxTokens, APIKeyEnc: encKey,
		}
		if encKey != "" {
			p.EncKeyID = s.ring.ID()
		}
		s.settings.Providers = append(s.settings.Providers, p)
		if in.SetDefault || s.settings.DefaultID == "" {
			s.settings.DefaultID = p.ID
		}
	}
	if in.SetDefault && in.ID != "" {
		s.settings.DefaultID = in.ID
	}
	err := s.persist()
	views := s.listLocked()
	defaultID := s.settings.DefaultID
	s.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return views, defaultID, nil
}

// SetDefault 把指定模型配置设为全局默认；只改默认指向，不动其他字段。
func (s *Service) SetDefault(id string) ([]ProviderView, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", fmt.Errorf("缺少模型配置 id")
	}
	s.mu.Lock()
	found := false
	for _, p := range s.settings.Providers {
		if p.ID == id {
			found = true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("模型配置 %q 不存在", id)
	}
	s.settings.DefaultID = id
	err := s.persist()
	views := s.listLocked()
	defaultID := s.settings.DefaultID
	s.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return views, defaultID, nil
}

// Remove 删除一条配置；若删的是默认模型，自动回落到第一条。
func (s *Service) Remove(id string) ([]ProviderView, string, error) {
	s.mu.Lock()
	idx := -1
	for i, p := range s.settings.Providers {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("模型配置 %q 不存在", id)
	}
	s.settings.Providers = append(s.settings.Providers[:idx], s.settings.Providers[idx+1:]...)
	if s.settings.DefaultID == id {
		s.settings.DefaultID = ""
		if len(s.settings.Providers) > 0 {
			s.settings.DefaultID = s.settings.Providers[0].ID
		}
	}
	err := s.persist()
	views := s.listLocked()
	defaultID := s.settings.DefaultID
	s.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return views, defaultID, nil
}

// Test 发起一次最小调用验证可用性；错误已脱敏。
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	p, err := s.pick(id)
	if err != nil {
		return TestResult{OK: false, Message: err.Error()}, nil
	}
	cm, err := s.build(ctx, p)
	if err != nil {
		return TestResult{OK: false, Message: err.Error(), Model: p.Model}, nil
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := cm.Generate(ctx, []*schema.Message{schema.UserMessage("ping")})
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{OK: false, LatencyMs: latency, Message: s.wrapErr(p, err).Error(), Model: p.Model}, nil
	}
	reply := ""
	if out != nil {
		reply = strings.TrimSpace(out.Content)
	}
	if len(reply) > 40 {
		reply = reply[:40] + "…"
	}
	return TestResult{OK: true, LatencyMs: latency, Message: "连接成功：" + reply, Model: p.Model}, nil
}

// DefaultID 返回默认模型配置 ID。
func (s *Service) DefaultID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.DefaultID
}

// listLocked 要求调用方已持锁。
func (s *Service) listLocked() []ProviderView {
	views := make([]ProviderView, 0, len(s.settings.Providers))
	for _, p := range s.settings.Providers {
		views = append(views, s.viewLocked(p))
	}
	return views
}

// viewLocked 把一条持久化配置转成前端视图；WorkBuddy 的 hasKey/账号信息来自本机登录态。
func (s *Service) viewLocked(p Provider) ProviderView {
	view := ProviderView{
		ID:       p.ID,
		AuthMode: authModeOf(p.Provider),
		ModelInfo: contract.ModelInfo{
			Provider:      p.Provider,
			Model:         p.Model,
			DisplayName:   p.Name,
			BaseURL:       p.BaseURL,
			HasKey:        s.hasKeyOf(p),
			IsDefault:     p.ID == s.settings.DefaultID,
			MaxTokens:     p.MaxTokens,
			ContextWindow: ContextWindowFor(p.Provider, p.Model),
		},
	}
	if p.Provider == "workbuddy" {
		status := s.wb.status()
		view.AccountName = status.AccountName
		view.ExpiresAt = status.ExpiresAt
	}
	return view
}

// hasKeyOf 判断一条配置是否具备可用的鉴权材料；WorkBuddy 以本机登录态为准。
func (s *Service) hasKeyOf(p Provider) bool {
	if p.Provider == "workbuddy" {
		return s.wb.hasLoginState()
	}
	return p.APIKeyEnc != "" || envKey(p.Provider) != ""
}

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func timeDuration(sec int) time.Duration { return time.Duration(sec) * time.Second }
