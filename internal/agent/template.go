package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ---------- 值班模板（盘前/盘中/复盘 + 量化/问答） ----------

const (
	// templateBucket 存用户改过的提示词与开关（key = worker id）。
	templateBucket = "agent_templates"
	// scheduleBucket 存三个阶段（盘前/盘中/复盘）的调度状态（key = "phases"）。
	scheduleBucket = "agent_schedule"
	scheduleDocKey = "phases"
)

// 提示词长度约束（trim 后按 rune 计）。
const (
	MinPromptRunes = 20
	MaxPromptRunes = 8000
)

// AgentTemplate 是一个值班模板。
// 默认 prompt 取 worker 的行为描述；用户可改、可恢复默认、可单独关掉；
// disclosureProtocol 永远由 composeSystem 追加，模板改不掉它。
type AgentTemplate struct {
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	Prompt        string        `json:"prompt"`
	DefaultPrompt string        `json:"defaultPrompt"`
	Edited        bool          `json:"edited"`
	Enabled       bool          `json:"enabled"`
	Model         string        `json:"model,omitempty"`
	Schedule      PhaseSchedule `json:"schedule"`
}

// storedTemplate 是落盘的最小集合：其余字段由代码与默认值派生，避免数据漂移。
type storedTemplate struct {
	Prompt  string `json:"prompt,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	Model   string `json:"model,omitempty"`
}

// WorkerFromID 严格校验模板/阶段的 worker id（不像 NormalizeWorker 那样把未知 id 回落成 chat）。
// 旧 id（premarket/intraday/postmarket/quant/chat）保留在 defaultPrompts 里，
// 所以即使它们已经不再是内置常驻 worker，旧模板仍然能被解析。
func WorkerFromID(id string) (WorkerID, bool) {
	v := WorkerID(strings.ToLower(strings.TrimSpace(id)))
	if _, ok := defaultPrompts[v]; ok {
		return v, true
	}
	return "", false
}

// TemplateStore 管理值班模板的持久化（bucket agent_templates）。
// 懒加载：首次访问时才读盘，读不到就当"全是默认值"，不影响启动。
type TemplateStore struct {
	store contract.Store
	// custom 提供自定义 worker 的默认 prompt/title/model（与 runtime 共用同一实例）。
	custom *CustomWorkerStore

	mu     sync.Mutex
	saved  map[WorkerID]storedTemplate
	loaded bool
}

// NewTemplateStore 构造模板存储；store 为 nil 时只保留内存态（测试/降级用）。
func NewTemplateStore(store contract.Store) *TemplateStore {
	return NewTemplateStoreWith(store, NewCustomWorkerStore(store))
}

// NewTemplateStoreWith 用共享的自定义 worker 存储构造模板存储：
// 保证创建/删除自定义 worker 后模板列表立刻反映变化。
func NewTemplateStoreWith(store contract.Store, custom *CustomWorkerStore) *TemplateStore {
	if custom == nil {
		custom = NewCustomWorkerStore(store)
	}
	return &TemplateStore{store: store, custom: custom, saved: map[WorkerID]storedTemplate{}}
}

func (s *TemplateStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.saved == nil {
		s.saved = map[WorkerID]storedTemplate{}
	}
	if s.store == nil {
		return
	}
	items, err := s.store.List(templateBucket, "")
	if err != nil {
		// 没数据（ErrNotFound）与读失败都按默认值处理：模板不是关键路径，不阻断启动。
		return
	}
	for _, it := range items {
		var raw storedTemplate
		if err := json.Unmarshal(it.Value, &raw); err != nil {
			continue
		}
		s.saved[WorkerID(it.Key)] = raw
	}
}

func (s *TemplateStore) buildLocked(id WorkerID) AgentTemplate {
	tpl := AgentTemplate{
		ID:            string(id),
		Prompt:        defaultPrompt(id),
		DefaultPrompt: defaultPrompt(id),
		Enabled:       true,
	}
	if m, ok := workerMetaOf(id); ok {
		tpl.Title = m.Title
		tpl.Description = m.Description
	}
	if raw, ok := s.saved[id]; ok {
		if p := strings.TrimSpace(raw.Prompt); p != "" {
			tpl.Prompt = p
			tpl.Edited = p != tpl.DefaultPrompt
		}
		if raw.Enabled != nil {
			tpl.Enabled = *raw.Enabled
		}
		if m := strings.TrimSpace(raw.Model); m != "" {
			tpl.Model = m
		}
	}
	return tpl
}

// buildCustomLocked 构造自定义 worker 的模板：默认 prompt/标题来自自定义定义，
// 用户在模板里改过的 prompt 与 model 优先；披露协议同样由 composeSystem 追加。
func (s *TemplateStore) buildCustomLocked(cw CustomWorker) AgentTemplate {
	tpl := AgentTemplate{
		ID:            cw.ID,
		Title:         cw.Title,
		Description:   cw.Description,
		Prompt:        cw.Prompt,
		DefaultPrompt: cw.Prompt,
		Enabled:       true,
		Model:         cw.Model,
	}
	if raw, ok := s.saved[WorkerID(cw.ID)]; ok {
		if p := strings.TrimSpace(raw.Prompt); p != "" {
			tpl.Prompt = p
			tpl.Edited = p != tpl.DefaultPrompt
		}
		if raw.Enabled != nil {
			tpl.Enabled = *raw.Enabled
		}
		if m := strings.TrimSpace(raw.Model); m != "" {
			tpl.Model = m
		}
	}
	return tpl
}

// knownLocked 判断 id 是否是已知 worker（内置 / 预设 / 自定义）。调用方需持锁。
// 预设 id 即使还没落成自定义 worker 也算已知：升级后的第一次启动就要能保存它的提示词。
func (s *TemplateStore) knownLocked(id WorkerID) bool {
	if isBuiltinWorker(id) {
		return true
	}
	if _, ok := presetWorkerMetaOf(id); ok {
		return true
	}
	if s.custom == nil {
		return false
	}
	_, ok := s.custom.Get(string(id))
	return ok
}

// All 返回全部模板（内置顺序固定，自定义按创建时间排在后面）。
func (s *TemplateStore) All() []AgentTemplate {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.snapshotLocked()
}

// Get 按 id 取单个模板（支持内置与自定义 worker）。
func (s *TemplateStore) Get(id WorkerID) (AgentTemplate, bool) {
	if s == nil {
		return AgentTemplate{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if isBuiltinWorker(id) {
		return s.buildLocked(id), true
	}
	if s.custom != nil {
		if cw, ok := s.custom.Get(string(id)); ok {
			return s.buildCustomLocked(cw), true
		}
	}
	return AgentTemplate{}, false
}

// Prompt 返回该 worker 当前生效的 prompt（未改过就是默认值）。
func (s *TemplateStore) Prompt(id WorkerID) string {
	if tpl, ok := s.Get(id); ok {
		return tpl.Prompt
	}
	return defaultPrompt(id)
}

// Model 返回该 worker 的默认模型（模板覆盖值或自定义定义，空表示跟随全局默认）。
func (s *TemplateStore) Model(id WorkerID) string {
	if tpl, ok := s.Get(id); ok {
		return tpl.Model
	}
	return ""
}

// Save 保存模板 prompt（enabled 为 nil 时不动开关），返回保存后的全部模板。
func (s *TemplateStore) Save(id WorkerID, prompt string, enabled *bool) ([]AgentTemplate, error) {
	return s.SaveWithModel(id, prompt, enabled, nil)
}

// SaveWithModel 保存模板 prompt/开关/模型：model 为 nil 时保持原值，传空串表示清空。
func (s *TemplateStore) SaveWithModel(id WorkerID, prompt string, enabled *bool, model *string) ([]AgentTemplate, error) {
	if s == nil {
		return nil, fmt.Errorf("模板存储未就绪，请重启应用后再试")
	}
	s.mu.Lock()
	s.loadLocked()
	if !s.knownLocked(id) {
		s.mu.Unlock()
		return nil, fmt.Errorf("未知模板 id：%s（可用：premarket / intraday / postmarket / quant / chat / router 或自定义 worker）", id)
	}
	text := strings.TrimSpace(prompt)
	n := len([]rune(text))
	if n < MinPromptRunes {
		s.mu.Unlock()
		return nil, fmt.Errorf("提示词太短（%d 字），至少 %d 字：请写清这个 worker 要盯什么、输出什么、什么情况下判定失效", n, MinPromptRunes)
	}
	if n > MaxPromptRunes {
		s.mu.Unlock()
		return nil, fmt.Errorf("提示词太长（%d 字），最多 %d 字：请把可复用的规则留在提示词里，把当天细节留给任务输入", n, MaxPromptRunes)
	}

	raw := s.saved[id]
	raw.Prompt = text
	if enabled != nil {
		v := *enabled
		raw.Enabled = &v
	}
	if model != nil {
		raw.Model = strings.TrimSpace(*model)
	}
	s.saved[id] = raw
	snapshot := s.snapshotLocked()
	s.mu.Unlock()

	if err := s.persist(id, raw); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Reset 恢复默认 prompt（开关与模型保持用户设置），返回全部模板。
func (s *TemplateStore) Reset(id WorkerID) ([]AgentTemplate, error) {
	if s == nil {
		return nil, fmt.Errorf("模板存储未就绪，请重启应用后再试")
	}
	s.mu.Lock()
	s.loadLocked()
	if !s.knownLocked(id) {
		s.mu.Unlock()
		return nil, fmt.Errorf("未知模板 id：%s（可用：premarket / intraday / postmarket / quant / chat / router 或自定义 worker）", id)
	}

	raw := s.saved[id]
	raw.Prompt = ""
	s.saved[id] = raw
	snapshot := s.snapshotLocked()
	s.mu.Unlock()

	if err := s.persist(id, raw); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Delete 删除某个 worker 的模板覆盖记录（删除自定义 worker 时清理，避免重建复活旧配置）。
func (s *TemplateStore) Delete(id WorkerID) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.loadLocked()
	delete(s.saved, id)
	s.mu.Unlock()
	if s.store == nil {
		return nil
	}
	if err := s.store.Delete(templateBucket, string(id)); err != nil {
		return fmt.Errorf("删除模板记录失败：%w", err)
	}
	return nil
}

// snapshotLocked 生成全部模板快照：与 worker 列表共用同一份顺序（workerEntries），
// 保证前端拿到的模板顺序与 worker 顺序永远一致。
//
// 每个 id 的取数规则：
//   - 自定义 worker 表里有这个 id → 用自定义定义（标题/prompt/model 都可被模板覆盖）；
//   - 没有 → 用 buildLocked（内置与预设共用 defaultPrompt 的静态文案）。
//     这样 EnsurePresetWorkers 还没跑成功时，值班卡也照样有 premarket/intraday/postmarket。
//
// 调用方需持锁。
func (s *TemplateStore) snapshotLocked() []AgentTemplate {
	entries := (&Runtime{custom: s.custom}).workerEntries()
	out := make([]AgentTemplate, 0, len(entries))
	for _, e := range entries {
		if s.custom != nil {
			if cw, ok := s.custom.Get(string(e.ID)); ok {
				out = append(out, s.buildCustomLocked(cw))
				continue
			}
		}
		out = append(out, s.buildLocked(e.ID))
	}
	return out
}

func (s *TemplateStore) persist(id WorkerID, raw storedTemplate) error {
	if s.store == nil {
		return nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("模板序列化失败：%w", err)
	}
	if err := s.store.Put(templateBucket, string(id), buf); err != nil {
		return fmt.Errorf("保存模板失败：%w", err)
	}
	return nil
}

// systemPromptOf 返回某个 worker 当前生效的完整 system prompt（模板 + 披露协议）。
func (s *TemplateStore) systemPromptOf(id WorkerID) string {
	return composeSystem(s.Prompt(id))
}
