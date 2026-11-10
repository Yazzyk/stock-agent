// Package llm 用 Eino 适配多 provider 的 ChatModel，并把模型配置与 API Key 落在本地。
// 依赖方向：llm 实现 pkg/contract.LLMService，不依赖 agent / plugin。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	einopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	settingsBucket = "settings"
	settingsKey    = "llm"
	httpTimeoutSec = 120
)

// Config 是 bootstrap 注入的依赖。
type Config struct {
	Store   contract.Store
	Guard   contract.Guard
	Logger  *slog.Logger
	DataDir string
}

// Provider 是一条模型配置（持久化形态）。APIKeyEnc 是密文，永不出现在返回值里。
type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"baseUrl,omitempty"`
	Model     string `json:"model"`
	APIKeyEnc string `json:"apiKeyEnc,omitempty"`
	EncKeyID  string `json:"encKeyId,omitempty"`
	MaxTokens int    `json:"maxTokens,omitempty"`
}

type settingsFile struct {
	Providers []Provider `json:"providers"`
	DefaultID string     `json:"defaultId"`
}

// ProviderView 是给前端的展平视图：contract.ModelInfo + id。
type ProviderView struct {
	ID string `json:"id"`
	contract.ModelInfo
}

// Service 同时实现 contract.LLMService 与模型管理能力。
type Service struct {
	cfg     Config
	log     *slog.Logger
	ring    *keyring
	mu      sync.RWMutex
	settings settingsFile
}

// chatModel 是 Eino 两个适配器共同满足的最小接口，避免依赖具体实现类型。
type chatModel interface {
	Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error)
	Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

// New 打开密钥环并载入模型配置；配置为空时给出一条可编辑的默认 DeepSeek 配置。
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("llm: 需要 Store")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Guard == nil {
		cfg.Guard = passthroughGuard{}
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(os.TempDir(), "guanlan")
	}
	ring, err := openKeyring(filepath.Join(cfg.DataDir, "keyring"))
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, log: cfg.Logger, ring: ring}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.cfg.Store.Get(settingsBucket, settingsKey)
	if err != nil && !errors.Is(err, contract.ErrNotFound) {
		return fmt.Errorf("llm: 读取模型配置失败: %w", err)
	}
	if len(raw) > 0 {
		var f settingsFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("llm: 模型配置损坏: %w", err)
		}
		s.settings = f
	}
	if len(s.settings.Providers) == 0 {
		s.settings.Providers = []Provider{defaultProvider("deepseek")}
	}
	if s.settings.DefaultID == "" {
		s.settings.DefaultID = s.settings.Providers[0].ID
	}
	return nil
}

func (s *Service) persist() error {
	raw, err := json.Marshal(s.settings)
	if err != nil {
		return fmt.Errorf("llm: 序列化模型配置失败: %w", err)
	}
	if err := s.cfg.Store.Put(settingsBucket, settingsKey, raw); err != nil {
		return fmt.Errorf("llm: 保存模型配置失败: %w", err)
	}
	return nil
}

// ---------- contract.LLMService ----------

func (s *Service) Chat(ctx context.Context, req contract.ChatRequest) (contract.Reply, error) {
	p, err := s.pick(req.Model)
	if err != nil {
		return contract.Reply{}, err
	}
	cm, err := s.build(ctx, p)
	if err != nil {
		return contract.Reply{}, err
	}
	names, err := newToolNameMap(req.Tools)
	if err != nil {
		return contract.Reply{}, err
	}
	msgs, err := toEinoMessages(req, names)
	if err != nil {
		return contract.Reply{}, err
	}
	opts, err := toOptions(req, names)
	if err != nil {
		return contract.Reply{}, err
	}
	out, err := cm.Generate(ctx, msgs, opts...)
	if err != nil {
		return contract.Reply{}, s.wrapErr(p, err)
	}
	return contract.Reply{Message: fromEinoMessage(out, names), Usage: usageOf(out), Model: p.Model}, nil
}

func (s *Service) Stream(ctx context.Context, req contract.ChatRequest, onDelta func(string)) (contract.Reply, error) {
	p, err := s.pick(req.Model)
	if err != nil {
		return contract.Reply{}, err
	}
	cm, err := s.build(ctx, p)
	if err != nil {
		return contract.Reply{}, err
	}
	names, err := newToolNameMap(req.Tools)
	if err != nil {
		return contract.Reply{}, err
	}
	msgs, err := toEinoMessages(req, names)
	if err != nil {
		return contract.Reply{}, err
	}
	opts, err := toOptions(req, names)
	if err != nil {
		return contract.Reply{}, err
	}
	sr, err := cm.Stream(ctx, msgs, opts...)
	if err != nil {
		return contract.Reply{}, s.wrapErr(p, err)
	}
	defer sr.Close()

	var chunks []*schema.Message
	var acc contract.Usage
	for {
		msg, err := sr.Recv()
		if err != nil {
			break
		}
		chunks = append(chunks, msg)
		// 逐 chunk 显式累计 usage：不同上游把 usage 放在不同 chunk（常见是最后一个），
		// 不能依赖 ConcatMessages 的隐式合并（它是逐字段取最大值，缺失时仍为 0）。
		if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
			u := msg.ResponseMeta.Usage
			acc.PromptTokens = maxInt(acc.PromptTokens, u.PromptTokens)
			acc.CompletionTokens = maxInt(acc.CompletionTokens, u.CompletionTokens)
			acc.TotalTokens = maxInt(acc.TotalTokens, u.TotalTokens)
		}
		if msg.Content != "" && onDelta != nil {
			onDelta(msg.Content)
		}
	}
	if len(chunks) == 0 {
		return contract.Reply{}, fmt.Errorf("llm: 模型 %s 未返回任何内容", p.Model)
	}
	merged, err := schema.ConcatMessages(chunks)
	if err != nil {
		return contract.Reply{}, fmt.Errorf("llm: 合并流式结果失败: %w", err)
	}
	usage := usageOf(merged)
	if usage.TotalTokens == 0 {
		usage = acc
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return contract.Reply{Message: fromEinoMessage(merged, names), Usage: usage, Model: p.Model}, nil
}

func (s *Service) Models(context.Context) ([]contract.ModelInfo, error) {
	views, _, _ := s.List()
	out := make([]contract.ModelInfo, 0, len(views))
	for _, v := range views {
		out = append(out, v.ModelInfo)
	}
	return out, nil
}

func (s *Service) Default(context.Context) (contract.ModelInfo, error) {
	views, defaultID, _ := s.List()
	for _, v := range views {
		if v.ID == defaultID {
			return v.ModelInfo, nil
		}
	}
	if len(views) > 0 {
		return views[0].ModelInfo, nil
	}
	return contract.ModelInfo{}, errors.New("llm: 尚未配置任何模型")
}

// ---------- 内部 ----------

func (s *Service) pick(modelOrID string) (Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.settings.Providers) == 0 {
		return Provider{}, errors.New("llm: 尚未配置任何模型，请在「模型设置」里添加")
	}
	key := strings.TrimSpace(modelOrID)
	if key == "" {
		for _, p := range s.settings.Providers {
			if p.ID == s.settings.DefaultID {
				return p, nil
			}
		}
		return s.settings.Providers[0], nil
	}
	for _, p := range s.settings.Providers {
		if p.ID == key || p.Model == key {
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("llm: 未找到模型 %q（可用: %s）", key, s.modelNamesLocked())
}

func (s *Service) modelNamesLocked() string {
	names := make([]string, 0, len(s.settings.Providers))
	for _, p := range s.settings.Providers {
		names = append(names, p.Model)
	}
	return strings.Join(names, ", ")
}

func (s *Service) apiKey(p Provider) (string, error) {
	key, err := s.ring.Open(p.APIKeyEnc)
	if err != nil {
		return "", err
	}
	if key != "" {
		return key, nil
	}
	if env := envKey(p.Provider); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("llm: 未配置 %s 的 API Key，请在「模型设置」里填写", displayProvider(p.Provider))
}

func (s *Service) build(ctx context.Context, p Provider) (chatModel, error) {
	key, err := s.apiKey(p)
	if err != nil {
		return nil, err
	}
	baseURL := p.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL(p.Provider)
	}
	maxTokens := p.MaxTokens
	timeout := timeDuration(httpTimeoutSec)

	switch p.Provider {
	case "deepseek":
		return deepseek.NewChatModel(ctx, &deepseek.ChatModelConfig{
			APIKey:  key,
			BaseURL: baseURL,
			Model:   p.Model,
			Timeout: timeout,
		})
	case "openai", "qwen", "ark", "ollama":
		cfg := &einopenai.ChatModelConfig{
			APIKey:  key,
			BaseURL: baseURL,
			Model:   p.Model,
			Timeout: timeout,
		}
		if maxTokens > 0 {
			cfg.MaxTokens = &maxTokens
		}
		return einopenai.NewChatModel(ctx, cfg)
	default:
		return nil, fmt.Errorf("llm: 不支持的 provider %q（可选 deepseek/openai/qwen/ark/ollama）", p.Provider)
	}
}

func (s *Service) wrapErr(p Provider, err error) error {
	msg := s.cfg.Guard.Redact(err.Error())
	s.log.Warn("模型调用失败", "provider", p.Provider, "model", p.Model, "error", msg)
	if strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized") || strings.Contains(msg, "invalid api key") {
		return fmt.Errorf("模型 %s 认证失败：API Key 无效或已过期（%s）", p.Model, msg)
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") {
		return fmt.Errorf("模型 %s 触发限流，请稍后重试（%s）", p.Model, msg)
	}
	return fmt.Errorf("模型 %s 调用失败：%s", p.Model, msg)
}

// ---------- 工具名映射 ----------
//
// OpenAI 兼容服务（含 DeepSeek）对 function.name 有字符集限制（^[a-zA-Z0-9_-]+$），
// 而本项目工具名采用 <域>.<动作>（如 market.quote / tool.call）。
// 这里在适配层做双向映射：出站把 '.' 换成 '_'，回程再换回来，内核与前端无需感知。
type toolNameMap struct {
	toProvider map[string]string
	toLocal    map[string]string
}

func newToolNameMap(tools []contract.ToolInfo) (*toolNameMap, error) {
	m := &toolNameMap{toProvider: map[string]string{}, toLocal: map[string]string{}}
	for _, t := range tools {
		provider := strings.ReplaceAll(t.Name, ".", "_")
		if prev, exists := m.toLocal[provider]; exists && prev != t.Name {
			return nil, fmt.Errorf("llm: 工具名映射冲突：%q 与 %q 都会映射为 %q，请改名", prev, t.Name, provider)
		}
		m.toProvider[t.Name] = provider
		m.toLocal[provider] = t.Name
	}
	return m, nil
}

func (m *toolNameMap) provider(name string) string {
	if m == nil {
		return name
	}
	if v, ok := m.toProvider[name]; ok {
		return v
	}
	return strings.ReplaceAll(name, ".", "_")
}

func (m *toolNameMap) local(name string) string {
	if m == nil {
		return name
	}
	if v, ok := m.toLocal[name]; ok {
		return v
	}
	return name
}

// ---------- 消息与工具转换 ----------

func toEinoMessages(req contract.ChatRequest, names *toolNameMap) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		out = append(out, schema.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		switch m.Role {
		case contract.RoleSystem:
			out = append(out, schema.SystemMessage(m.Content))
		case contract.RoleUser, "":
			out = append(out, schema.UserMessage(m.Content))
		case contract.RoleAssistant:
			calls := make([]schema.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, schema.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: schema.FunctionCall{
						Name:      names.provider(tc.Name),
						Arguments: tc.Arguments,
					},
				})
			}
			out = append(out, schema.AssistantMessage(m.Content, calls))
		case contract.RoleTool:
			if m.ToolCallID == "" {
				return nil, errors.New("llm: tool 消息缺少 toolCallId（模型会拒绝整段历史）")
			}
			out = append(out, schema.ToolMessage(m.Content, m.ToolCallID))
		default:
			return nil, fmt.Errorf("llm: 未知消息角色 %q", m.Role)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("llm: 消息为空")
	}
	return out, nil
}

func toOptions(req contract.ChatRequest, names *toolNameMap) ([]model.Option, error) {
	var opts []model.Option
	if req.Temperature != nil {
		t := float32(*req.Temperature)
		opts = append(opts, model.WithTemperature(t))
	}
	if req.MaxTokens > 0 {
		opts = append(opts, model.WithMaxTokens(req.MaxTokens))
	}
	if len(req.Tools) > 0 {
		tools, err := toToolInfos(req.Tools, names)
		if err != nil {
			return nil, err
		}
		opts = append(opts, model.WithTools(tools))
	}
	return opts, nil
}

func toToolInfos(tools []contract.ToolInfo, names *toolNameMap) ([]*schema.ToolInfo, error) {
	out := make([]*schema.ToolInfo, 0, len(tools))
	for _, t := range tools {
		info := &schema.ToolInfo{Name: names.provider(t.Name), Desc: t.Description}
		if len(t.Schema) > 0 {
			var js jsonschema.Schema
			if err := json.Unmarshal(t.Schema, &js); err != nil {
				return nil, fmt.Errorf("llm: 工具 %s 的 JSON Schema 非法: %w", t.Name, err)
			}
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&js)
		}
		out = append(out, info)
	}
	return out, nil
}

func fromEinoMessage(m *schema.Message, names *toolNameMap) contract.Message {
	if m == nil {
		return contract.Message{}
	}
	calls := make([]contract.ToolCall, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		calls = append(calls, contract.ToolCall{ID: tc.ID, Name: names.local(tc.Function.Name), Arguments: tc.Function.Arguments})
	}
	out := contract.Message{Content: m.Content, ToolCalls: calls}
	switch m.Role {
	case schema.System:
		out.Role = contract.RoleSystem
	case schema.User:
		out.Role = contract.RoleUser
	case schema.Tool:
		out.Role = contract.RoleTool
		out.ToolCallID = m.ToolCallID
		out.Name = m.ToolName
	default:
		out.Role = contract.RoleAssistant
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func usageOf(m *schema.Message) contract.Usage {
	if m == nil || m.ResponseMeta == nil || m.ResponseMeta.Usage == nil {
		return contract.Usage{}
	}
	u := m.ResponseMeta.Usage
	return contract.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}

// ---------- 默认值 ----------

func defaultProvider(provider string) Provider {
	p := Provider{ID: newID(), Provider: provider, Model: defaultModelName(provider), BaseURL: defaultBaseURL(provider)}
	p.Name = displayProvider(provider)
	return p
}

func defaultModelName(provider string) string {
	switch provider {
	case "deepseek":
		return "deepseek-chat"
	case "openai":
		return "gpt-4o-mini"
	case "qwen":
		return "qwen-plus"
	case "ark":
		return "doubao-1-5-pro-32k"
	case "ollama":
		return "llama3.1"
	default:
		return provider
	}
}

func defaultBaseURL(provider string) string {
	switch provider {
	case "deepseek":
		return "https://api.deepseek.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "qwen":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	case "ark":
		return "https://ark.cn-beijing.volces.com/api/v3"
	case "ollama":
		return "http://127.0.0.1:11434/v1"
	default:
		return ""
	}
}

func displayProvider(provider string) string {
	switch provider {
	case "deepseek":
		return "DeepSeek"
	case "openai":
		return "OpenAI"
	case "qwen":
		return "通义千问"
	case "ark":
		return "火山方舟"
	case "ollama":
		return "Ollama"
	default:
		return provider
	}
}

func envKey(provider string) string {
	switch provider {
	case "deepseek":
		return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	case "openai":
		return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	case "qwen":
		return strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
	case "ark":
		return strings.TrimSpace(os.Getenv("ARK_API_KEY"))
	default:
		return ""
	}
}

type passthroughGuard struct{}

func (passthroughGuard) AllowHost(string) bool   { return true }
func (passthroughGuard) CheckPath(string) error  { return nil }
func (passthroughGuard) Redact(s string) string  { return s }
func (passthroughGuard) Policy() contract.Policy { return contract.Policy{} }
