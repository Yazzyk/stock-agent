// 本文件是「模型设置」的内置预设表（契约 §3.8）。
// 数值以 2026-11 官方文档为准，只作为**新建配置时的填充默认值**：
// 用户可在面板里覆盖，保存后以本地配置为准，预设表本身不读网络、不落盘。
package llm

// 上下文窗口单位换算：预设表按 1K = 1024 计（与 DSH 等工具的窗口口径一致）。
const (
	presetK = 1024
	presetM = 1024 * presetK // 1M = 1048576
)

// ModelPreset 是一个模型在官方文档里的能力参数。
type ModelPreset struct {
	Model            string `json:"model"`
	Label            string `json:"label"`
	ContextWindow    int    `json:"contextWindow"`    // 上下文窗口（token）
	MaxOutput        int    `json:"maxOutput"`        // 单次最大输出（token）
	DefaultMaxTokens int    `json:"defaultMaxTokens"` // maxOutput 未知时的 maxTokens 兜底值（有官方上限时以 MaxOutput 为准）
	Note             string `json:"note,omitempty"`
}

// ProviderPreset 是一个 provider 的接入信息与内置模型清单。
// AuthMode 是接入方式：workbuddy 走本机登录态、opencode 走兼容网关；其余为空（按 API Key）。
type ProviderPreset struct {
	Provider string        `json:"provider"`
	Label    string        `json:"label"`
	BaseURL  string        `json:"baseUrl"`
	KeyURL   string        `json:"keyUrl,omitempty"`
	DocsURL  string        `json:"docsUrl,omitempty"`
	AuthMode string        `json:"authMode,omitempty"`
	Models   []ModelPreset `json:"models"`
	Note     string        `json:"note,omitempty"`
}

// providerPresets 是内置预设的唯一真源，顺序即前端下拉顺序。
var providerPresets = []ProviderPreset{
	{
		Provider: "deepseek",
		Label:    "DeepSeek",
		BaseURL:  "https://api.deepseek.com",
		KeyURL:   "https://platform.deepseek.com/api_keys",
		DocsURL:  "https://api-docs.deepseek.com/",
		Models: []ModelPreset{
			{Model: "deepseek-flash", Label: "DeepSeek Flash", ContextWindow: presetM, MaxOutput: 384 * presetK, DefaultMaxTokens: 8192, Note: "日常研究与工具调用首选"},
			{Model: "deepseek-v4-pro", Label: "DeepSeek V4 Pro", ContextWindow: presetM, MaxOutput: 384 * presetK, DefaultMaxTokens: 8192, Note: "复杂推理更强，成本更高"},
			{Model: "deepseek-chat", Label: "DeepSeek Chat（旧版）", ContextWindow: 128 * presetK, MaxOutput: 8 * presetK, DefaultMaxTokens: 4096, Note: "legacy：旧模型名，仅为兼容已有配置保留，建议迁移到 deepseek-flash"},
		},
	},
	{
		Provider: "openai",
		Label:    "OpenAI",
		BaseURL:  "https://api.openai.com/v1",
		KeyURL:   "https://platform.openai.com/api-keys",
		DocsURL:  "https://platform.openai.com/docs/models",
		Models: []ModelPreset{
			{Model: "gpt-5", Label: "GPT-5", ContextWindow: 400 * presetK, MaxOutput: 128 * presetK, DefaultMaxTokens: 16384},
			{Model: "gpt-5-mini", Label: "GPT-5 mini", ContextWindow: 400 * presetK, MaxOutput: 128 * presetK, DefaultMaxTokens: 16384, Note: "日常研究性价比更高"},
		},
	},
	{
		Provider: "qwen",
		Label:    "通义千问",
		BaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
		KeyURL:   "https://bailian.console.aliyun.com/?apiKey=1",
		DocsURL:  "https://help.aliyun.com/zh/model-studio/models",
		Models: []ModelPreset{
			{Model: "qwen3-max", Label: "通义千问3 Max", ContextWindow: 256 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192},
			{Model: "qwen-plus", Label: "通义千问 Plus", ContextWindow: 128 * presetK, MaxOutput: 16 * presetK, DefaultMaxTokens: 4096},
			{Model: "qwen-turbo", Label: "通义千问 Turbo", ContextWindow: presetM, MaxOutput: 16 * presetK, DefaultMaxTokens: 4096},
		},
	},
	{
		Provider: "ark",
		Label:    "火山方舟",
		BaseURL:  "https://ark.cn-beijing.volces.com/api/v3",
		KeyURL:   "https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey",
		DocsURL:  "https://www.volcengine.com/docs/82379",
		Note:     "方舟需先开通模型并创建接入点，模型名可用预设名或接入点 ID",
		Models: []ModelPreset{
			{Model: "doubao-seed-2-1-pro", Label: "豆包 Seed 2.1 Pro", ContextWindow: presetM, MaxOutput: 256 * presetK, DefaultMaxTokens: 8192},
			{Model: "doubao-seed-2-1-turbo", Label: "豆包 Seed 2.1 Turbo", ContextWindow: 256 * presetK, MaxOutput: 256 * presetK, DefaultMaxTokens: 8192},
		},
	},
	{
		Provider: "ollama",
		Label:    "Ollama（本机）",
		BaseURL:  "http://127.0.0.1:11434/v1",
		DocsURL:  "https://github.com/ollama/ollama/blob/main/docs/openai.md",
		Note:     "本机部署，默认不需要 API Key；模型名需与 ollama list 一致",
		Models: []ModelPreset{
			{Model: "qwen3:8b", Label: "Qwen3 8B", ContextWindow: 32 * presetK, MaxOutput: 8 * presetK, DefaultMaxTokens: 2048},
			{Model: "llama3.1:8b", Label: "Llama 3.1 8B", ContextWindow: 128 * presetK, MaxOutput: 8 * presetK, DefaultMaxTokens: 2048},
		},
	},
	{
		Provider: "opencode",
		Label:    "OpenCode 兼容网关",
		BaseURL:  "https://opencode.ai/inference/openai/v1",
		KeyURL:   "https://opencode.ai/console",
		DocsURL:  "https://opencode.ai/docs/",
		AuthMode: "opencode",
		Note:     "OpenAI Chat Completions 兼容网关，需要填写 OpenCode 控制台签发的 API Key；模型 ID 按 OpenCode 目录（2026-11），具体以控制台为准",
		Models: []ModelPreset{
			{Model: "qwen3.8-max", Label: "Qwen3.8 Max", ContextWindow: 200 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192, Note: "按 OpenCode 目录，具体以控制台为准"},
			{Model: "deepseek-v4.1-flash", Label: "DeepSeek V4.1 Flash", ContextWindow: 200 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192, Note: "按 OpenCode 目录，具体以控制台为准"},
			{Model: "deepseek-v4-pro", Label: "DeepSeek V4 Pro", ContextWindow: 200 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192, Note: "按 OpenCode 目录，具体以控制台为准"},
			{Model: "kimi-k2.5", Label: "Kimi K2.5", ContextWindow: 200 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192, Note: "按 OpenCode 目录，具体以控制台为准"},
			{Model: "glm-5.3", Label: "GLM 5.3", ContextWindow: 200 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192, Note: "按 OpenCode 目录，具体以控制台为准"},
			{Model: "minimax-m3", Label: "MiniMax M3", ContextWindow: 200 * presetK, MaxOutput: 32 * presetK, DefaultMaxTokens: 8192, Note: "按 OpenCode 目录，具体以控制台为准"},
		},
	},
	{
		Provider: "workbuddy",
		Label:    "WorkBuddy（本机登录）",
		BaseURL:  "https://www.workbuddy.cn/v2",
		DocsURL:  "https://www.workbuddy.cn",
		AuthMode: "workbuddy",
		Note:     "无需 API Key：使用本机 WorkBuddy 登录态（国内 workbuddy.cn / 国际 workbuddy.ai），模型从账号的模型目录里选",
		Models:   []ModelPreset{},
	},
	{
		Provider: "openai-compat",
		Label:    "OpenAI 兼容（自定义）",
		Note:     "任何 OpenAI 兼容端点（vLLM / LM Studio / 自建网关），Base URL 与模型名需手填",
		Models:   []ModelPreset{},
	},
}

// Presets 返回内置预设的深拷贝；调用方可自由修改，不会影响包内状态，也不发起任何网络请求。
func Presets() []ProviderPreset {
	out := make([]ProviderPreset, 0, len(providerPresets))
	for _, p := range providerPresets {
		cp := p
		cp.Models = append([]ModelPreset(nil), p.Models...)
		out = append(out, cp)
	}
	return out
}

// LookupProvider 按 provider 名查预设。
func LookupProvider(provider string) (ProviderPreset, bool) {
	for _, p := range providerPresets {
		if p.Provider == provider {
			cp := p
			cp.Models = append([]ModelPreset(nil), p.Models...)
			return cp, true
		}
	}
	return ProviderPreset{}, false
}

// LookupModel 按 provider + 模型名查预设；自定义模型名返回 ok=false（不是错误）。
func LookupModel(provider, model string) (ModelPreset, bool) {
	for _, p := range providerPresets {
		if p.Provider != provider {
			continue
		}
		for _, m := range p.Models {
			if m.Model == model {
				return m, true
			}
		}
	}
	return ModelPreset{}, false
}

// DefaultMaxTokensFor 返回模型预设里的 maxTokens 缺省值：
// 命中预设且官方给出了单次输出上限（MaxOutput > 0）时直接用该上限，与面板「官方 Max Tokens 只读」保持一致；
// 否则回落到预设的 DefaultMaxTokens；未知模型返回 0（表示"不填"）。
func DefaultMaxTokensFor(provider, model string) int {
	m, ok := LookupModel(provider, model)
	if !ok {
		return 0
	}
	if m.MaxOutput > 0 {
		return m.MaxOutput
	}
	return m.DefaultMaxTokens
}

// ContextWindowFor 返回模型预设里的上下文窗口；未知模型返回 0（前端据此不显示百分比）。
func ContextWindowFor(provider, model string) int {
	if m, ok := LookupModel(provider, model); ok {
		return m.ContextWindow
	}
	return 0
}
