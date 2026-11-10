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

var validProviders = map[string]bool{"deepseek": true, "openai": true, "qwen": true, "ark": true, "ollama": true}

// List 返回全部模型的展平视图与默认 ID。HasKey 只表达"是否已配置"，从不返回明文。
func (s *Service) List() ([]ProviderView, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]ProviderView, 0, len(s.settings.Providers))
	for _, p := range s.settings.Providers {
		hasKey := p.APIKeyEnc != "" || envKey(p.Provider) != ""
		views = append(views, ProviderView{
			ID: p.ID,
			ModelInfo: contract.ModelInfo{
				Provider:    p.Provider,
				Model:       p.Model,
				DisplayName: p.Name,
				BaseURL:     p.BaseURL,
				HasKey:      hasKey,
				IsDefault:   p.ID == s.settings.DefaultID,
				MaxTokens:   p.MaxTokens,
			},
		})
	}
	return views, s.settings.DefaultID, nil
}

// Save 新增或更新一条模型配置；apiKey 为空时保留旧密文。
func (s *Service) Save(in SaveInput) ([]ProviderView, string, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if !validProviders[provider] {
		return nil, "", fmt.Errorf("不支持的 provider %q（可选 deepseek/openai/qwen/ark/ollama）", in.Provider)
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
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
		p.MaxTokens = in.MaxTokens
		if encKey != "" {
			p.APIKeyEnc = encKey
			p.EncKeyID = s.ring.ID()
		}
		s.settings.Providers[idx] = p
	} else {
		p := Provider{
			ID: newID(), Provider: provider, Name: name, BaseURL: baseURL, Model: model,
			MaxTokens: in.MaxTokens, APIKeyEnc: encKey,
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
		hasKey := p.APIKeyEnc != "" || envKey(p.Provider) != ""
		views = append(views, ProviderView{
			ID: p.ID,
			ModelInfo: contract.ModelInfo{
				Provider:    p.Provider,
				Model:       p.Model,
				DisplayName: p.Name,
				BaseURL:     p.BaseURL,
				HasKey:      hasKey,
				IsDefault:   p.ID == s.settings.DefaultID,
				MaxTokens:   p.MaxTokens,
			},
		})
	}
	return views
}

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func timeDuration(sec int) time.Duration { return time.Duration(sec) * time.Second }
