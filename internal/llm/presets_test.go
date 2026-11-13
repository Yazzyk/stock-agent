package llm

import (
	"context"
	"strings"
	"testing"
)

// 契约 §3.8 的官方数值表：1K 按 1024 计，1M = 1048576，384K = 393216。
func TestPresetsMatchContractNumbers(t *testing.T) {
	cases := []struct {
		provider        string
		model           string
		contextWindow   int
		maxOutput       int
		defaultMaxToken int
		legacy          bool
	}{
		{provider: "deepseek", model: "deepseek-flash", contextWindow: 1048576, maxOutput: 393216, defaultMaxToken: 8192},
		{provider: "deepseek", model: "deepseek-v4-pro", contextWindow: 1048576, maxOutput: 393216, defaultMaxToken: 8192},
		{provider: "deepseek", model: "deepseek-chat", contextWindow: 131072, maxOutput: 8192, defaultMaxToken: 4096, legacy: true},
		{provider: "openai", model: "gpt-5", contextWindow: 409600, maxOutput: 131072, defaultMaxToken: 16384},
		{provider: "openai", model: "gpt-5-mini", contextWindow: 409600, maxOutput: 131072, defaultMaxToken: 16384},
		{provider: "qwen", model: "qwen3-max", contextWindow: 262144, maxOutput: 32768, defaultMaxToken: 8192},
		{provider: "qwen", model: "qwen-plus", contextWindow: 131072, maxOutput: 16384, defaultMaxToken: 4096},
		{provider: "qwen", model: "qwen-turbo", contextWindow: 1048576, maxOutput: 16384, defaultMaxToken: 4096},
		{provider: "ark", model: "doubao-seed-2-1-pro", contextWindow: 1048576, maxOutput: 262144, defaultMaxToken: 8192},
		{provider: "ark", model: "doubao-seed-2-1-turbo", contextWindow: 262144, maxOutput: 262144, defaultMaxToken: 8192},
		{provider: "ollama", model: "qwen3:8b", contextWindow: 32768, maxOutput: 8192, defaultMaxToken: 2048},
		{provider: "ollama", model: "llama3.1:8b", contextWindow: 131072, maxOutput: 8192, defaultMaxToken: 2048},
	}
	for _, c := range cases {
		t.Run(c.provider+"/"+c.model, func(t *testing.T) {
			m, ok := LookupModel(c.provider, c.model)
			if !ok {
				t.Fatalf("预设缺失: %s/%s", c.provider, c.model)
			}
			if m.ContextWindow != c.contextWindow || m.MaxOutput != c.maxOutput || m.DefaultMaxTokens != c.defaultMaxToken {
				t.Fatalf("数值不符: got %+v want ctx=%d out=%d max=%d", m, c.contextWindow, c.maxOutput, c.defaultMaxToken)
			}
			isLegacy := strings.Contains(strings.ToLower(m.Note), "legacy") || strings.Contains(m.Note, "旧")
			if isLegacy != c.legacy {
				t.Fatalf("legacy 标注不符: note=%q want legacy=%v", m.Note, c.legacy)
			}
			if ContextWindowFor(c.provider, c.model) != c.contextWindow {
				t.Fatalf("ContextWindowFor 不一致")
			}
			// 官方模型给出单次输出上限时，DefaultMaxTokensFor 必须等于 MaxOutput（面板按它只读显示）。
			if DefaultMaxTokensFor(c.provider, c.model) != c.maxOutput {
				t.Fatalf("DefaultMaxTokensFor = %d, want MaxOutput %d", DefaultMaxTokensFor(c.provider, c.model), c.maxOutput)
			}
		})
	}
}

func TestPresetsCoverAllProviders(t *testing.T) {
	want := []string{"deepseek", "openai", "qwen", "ark", "ollama", "opencode", "workbuddy", "openai-compat"}
	presets := Presets()
	if len(presets) != len(want) {
		t.Fatalf("provider 数 = %d, want %d", len(presets), len(want))
	}
	seen := map[string]bool{}
	for i, p := range presets {
		if p.Provider != want[i] {
			t.Fatalf("第 %d 个 provider = %q, want %q（顺序即前端下拉顺序）", i, p.Provider, want[i])
		}
		seen[p.Provider] = true
		if p.Label == "" {
			t.Fatalf("%s 缺少中文 Label", p.Provider)
		}
		if p.Provider != "openai-compat" && p.BaseURL == "" {
			t.Fatalf("%s 缺少 BaseURL", p.Provider)
		}
		if p.Provider != "openai-compat" && p.DocsURL == "" {
			t.Fatalf("%s 缺少 DocsURL", p.Provider)
		}
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("缺少 provider %q", id)
		}
	}
	// openai-compat 按要求不预置模型，需手填；workbuddy 的模型来自账号目录，也不预置。
	if len(presets[len(presets)-1].Models) != 0 {
		t.Fatalf("openai-compat 不应预置模型")
	}
	if wb, ok := LookupProvider("workbuddy"); !ok || len(wb.Models) != 0 {
		t.Fatalf("workbuddy 的模型应来自模型目录，预设里不应写死")
	}
	// 有 Key 申请入口的 provider 必须给出 KeyURL（ollama 本机部署、workbuddy 登录态都不需要 Key）。
	for _, p := range presets {
		switch p.Provider {
		case "deepseek", "openai", "qwen", "ark", "opencode":
			if p.KeyURL == "" {
				t.Fatalf("%s 缺少 KeyURL", p.Provider)
			}
		}
	}
}

// AuthMode 是前端区分「API Key / 本机登录 / 兼容网关」的依据，只应出现在这两类 provider 上。
func TestPresetsAuthMode(t *testing.T) {
	want := map[string]string{"workbuddy": "workbuddy", "opencode": "opencode"}
	for _, p := range Presets() {
		if got := p.AuthMode; got != want[p.Provider] {
			t.Fatalf("%s 的 AuthMode = %q, want %q", p.Provider, got, want[p.Provider])
		}
	}
	if p, ok := LookupProvider("opencode"); !ok {
		t.Fatal("缺少 opencode 预设")
	} else if p.BaseURL != "https://opencode.ai/inference/openai/v1" {
		t.Fatalf("opencode BaseURL 不符: %q", p.BaseURL)
	}
}

func TestPresetsReturnsDeepCopy(t *testing.T) {
	first := Presets()
	first[0].BaseURL = "http://changed"
	first[0].Models[0].Model = "changed"
	second := Presets()
	if second[0].BaseURL == "http://changed" {
		t.Fatal("Presets 返回了共享切片，调用方修改污染了包内状态")
	}
	if second[0].Models[0].Model == "changed" {
		t.Fatal("Models 不是深拷贝")
	}
}

func TestDefaultModelNameFollowsOfficial(t *testing.T) {
	cases := map[string]string{
		"deepseek": "deepseek-flash",
		"openai":   "gpt-5-mini",
		"ark":      "doubao-seed-2-1-pro",
	}
	for provider, want := range cases {
		if got := defaultModelName(provider); got != want {
			t.Fatalf("%s 默认模型 = %q, want %q", provider, got, want)
		}
	}
}

// 所有给出官方单次输出上限的预设，maxTokens 缺省值都必须等于该上限；未知模型仍返回 0。
func TestDefaultMaxTokensForPrefersMaxOutput(t *testing.T) {
	for _, p := range Presets() {
		for _, m := range p.Models {
			if m.MaxOutput <= 0 {
				continue
			}
			if got := DefaultMaxTokensFor(p.Provider, m.Model); got != m.MaxOutput {
				t.Fatalf("%s/%s 的缺省 maxTokens = %d, want %d", p.Provider, m.Model, got, m.MaxOutput)
			}
		}
	}
	if got := DefaultMaxTokensFor("deepseek", "my-own-model"); got != 0 {
		t.Fatalf("未知模型应返回 0（不填），got %d", got)
	}
}

// 保存时 maxTokens 缺省 → 官方模型按官方单次输出上限填写；显式值优先；未知模型保持 0。
func TestSaveFillsOfficialMaxTokens(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		model     string
		maxTokens int
		wantMax   int
		wantCtx   int
	}{
		{name: "deepseek-flash 缺省按官方上限", provider: "deepseek", model: "deepseek-flash", wantMax: 393216, wantCtx: 1048576},
		{name: "qwen-turbo 缺省按官方上限", provider: "qwen", model: "qwen-turbo", wantMax: 16384, wantCtx: 1048576},
		{name: "ollama 缺省按官方上限", provider: "ollama", model: "qwen3:8b", wantMax: 8192, wantCtx: 32768},
		{name: "显式值优先", provider: "deepseek", model: "deepseek-flash", maxTokens: 512, wantMax: 512, wantCtx: 1048576},
		{name: "未知模型不填", provider: "deepseek", model: "my-own-model", wantMax: 0, wantCtx: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, _ := newTestService(t)
			views, _, err := svc.Save(SaveInput{
				ID: seedID(t, svc), Provider: c.provider, Model: c.model, MaxTokens: c.maxTokens, APIKey: "sk-test",
			})
			if err != nil {
				t.Fatalf("保存失败: %v", err)
			}
			if len(views) != 1 {
				t.Fatalf("配置数 = %d, want 1", len(views))
			}
			if views[0].MaxTokens != c.wantMax {
				t.Fatalf("MaxTokens = %d, want %d", views[0].MaxTokens, c.wantMax)
			}
			if views[0].ContextWindow != c.wantCtx {
				t.Fatalf("ContextWindow = %d, want %d", views[0].ContextWindow, c.wantCtx)
			}
		})
	}
}

// eino 的 deepseek 适配器只校验模型名非空（v0.1.7 实测），因此新模型名可原样透传。
// 这里通过 build 构造客户端（不发起网络请求）验证：若适配器有白名单，这里就会报错。
func TestDeepseekAdapterAcceptsPresetModels(t *testing.T) {
	svc, _, _ := newTestService(t)
	id := seedID(t, svc)
	deepseek, ok := LookupProvider("deepseek")
	if !ok {
		t.Fatal("缺少 deepseek 预设")
	}
	for _, m := range deepseek.Models {
		if _, _, err := svc.Save(SaveInput{ID: id, Provider: "deepseek", Model: m.Model, APIKey: "sk-test"}); err != nil {
			t.Fatalf("保存 %s 失败: %v", m.Model, err)
		}
		p, err := svc.pick(id)
		if err != nil {
			t.Fatalf("pick %s 失败: %v", m.Model, err)
		}
		cm, err := svc.build(context.Background(), p)
		if err != nil {
			t.Fatalf("deepseek 适配器拒绝模型 %s: %v（需要改用 openai 兼容适配器）", m.Model, err)
		}
		if cm == nil {
			t.Fatalf("模型 %s 构造出空客户端", m.Model)
		}
	}
}

// 本机/自定义端点允许不填 Key：ollama 与 openai-compat 不应被"未配置 API Key"挡住。
func TestProvidersWithoutKeyBuild(t *testing.T) {
	cases := []struct{ provider, model, baseURL string }{
		{provider: "ollama", model: "qwen3:8b"},
		{provider: "openai-compat", model: "local-model", baseURL: "http://127.0.0.1:9/v1"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			svc, _, _ := newTestService(t)
			id := seedID(t, svc)
			if _, _, err := svc.Save(SaveInput{ID: id, Provider: c.provider, Model: c.model, BaseURL: c.baseURL}); err != nil {
				t.Fatalf("保存失败: %v", err)
			}
			p, err := svc.pick(id)
			if err != nil {
				t.Fatalf("pick 失败: %v", err)
			}
			if _, err := svc.build(context.Background(), p); err != nil {
				t.Fatalf("不填 Key 时应可构造客户端: %v", err)
			}
		})
	}
	// 反例：deepseek 仍要求 Key，错误要可操作（先清掉环境变量，避免本机 key 影响断言）。
	t.Setenv("DEEPSEEK_API_KEY", "")
	svc, _, _ := newTestService(t)
	p, err := svc.pick(seedID(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.build(context.Background(), p); err == nil {
		t.Fatal("deepseek 缺 Key 应当报错")
	} else if !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("错误不够可操作: %v", err)
	}
}
