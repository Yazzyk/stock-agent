package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ---------- 自定义 worker ----------

const (
	// customWorkerBucket 存用户/总控创建的自定义 worker（key = id）。
	customWorkerBucket = "agent_workers"
	// maxCustomWorkers 是自定义 worker 数量上限，避免目录失焦与成本失控。
	maxCustomWorkers = 20
	// 自定义 worker 的字段约束（按 rune 计）。
	maxWorkerTitleRunes       = 40
	maxWorkerDescriptionRunes = 200
)

// customWorkerIDPattern 约束自定义 worker id：小写字母开头，后续允许小写字母/数字/下划线/中划线。
var customWorkerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// CustomWorker 是一个自定义 worker 定义：id 稳定，prompt 是它自己的默认行为描述。
type CustomWorker struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Prompt      string    `json:"prompt"`
	Model       string    `json:"model,omitempty"`
	CreatedBy   string    `json:"createdBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// CreateWorkerRequest 是创建自定义 worker 的入参（插件方法与 worker.create 工具共用）。
type CreateWorkerRequest struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model,omitempty"`
	CreatedBy   string `json:"createdBy,omitempty"`
}

// CustomWorkerStore 用 contract.Store 持久化自定义 worker：
// 懒加载、全量缓存，写操作同步落盘（bucket=agent_workers，key=id）。
type CustomWorkerStore struct {
	store contract.Store

	mu     sync.Mutex
	items  map[string]CustomWorker
	loaded bool
}

// NewCustomWorkerStore 构造自定义 worker 存储；store 为 nil 时只保留内存态。
func NewCustomWorkerStore(store contract.Store) *CustomWorkerStore {
	return &CustomWorkerStore{store: store, items: map[string]CustomWorker{}}
}

// loadLocked 首次访问时从 Store 全量读入；读失败按空处理，不阻断启动。
func (s *CustomWorkerStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.items == nil {
		s.items = map[string]CustomWorker{}
	}
	if s.store == nil {
		return
	}
	items, err := s.store.List(customWorkerBucket, "")
	if err != nil {
		return
	}
	for _, it := range items {
		var cw CustomWorker
		if err := json.Unmarshal(it.Value, &cw); err != nil {
			continue
		}
		if strings.TrimSpace(cw.ID) == "" {
			continue
		}
		s.items[cw.ID] = cw
	}
}

// userCountLocked 统计用户/总控自建的 worker 数（不算系统预设）。调用方需持锁。
func (s *CustomWorkerStore) userCountLocked() int {
	n := 0
	for _, cw := range s.items {
		if cw.CreatedBy != presetCreatedBy {
			n++
		}
	}
	return n
}

// All 返回全部自定义 worker（按创建时间升序，同时间按 id 升序）。
func (s *CustomWorkerStore) All() []CustomWorker {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]CustomWorker, 0, len(s.items))
	for _, cw := range s.items {
		out = append(out, cw)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get 按 id 精确查自定义 worker（id 已规范化为小写）。
func (s *CustomWorkerStore) Get(id string) (CustomWorker, bool) {
	if s == nil {
		return CustomWorker{}, false
	}
	key := strings.ToLower(strings.TrimSpace(id))
	if key == "" {
		return CustomWorker{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	cw, ok := s.items[key]
	return cw, ok
}

// Create 校验并落盘一个新的自定义 worker；返回创建后的定义。
func (s *CustomWorkerStore) Create(in CreateWorkerRequest, now time.Time) (CustomWorker, error) {
	if s == nil {
		return CustomWorker{}, fmt.Errorf("自定义 worker 存储未就绪，请重启应用后再试")
	}
	cw, err := normalizeCustomWorker(in, now)
	if err != nil {
		return CustomWorker{}, err
	}

	s.mu.Lock()
	s.loadLocked()
	if _, exists := s.items[cw.ID]; exists {
		s.mu.Unlock()
		return CustomWorker{}, fmt.Errorf("worker id %q 已存在，请换一个", cw.ID)
	}
	// 上限只算**用户/总控自建**的 worker：5 个系统预置 worker 不占用户的额度，
	// 否则升级后用户可用额度会凭空少 5 个。
	if s.userCountLocked() >= maxCustomWorkers {
		s.mu.Unlock()
		return CustomWorker{}, fmt.Errorf("自定义 worker 已达上限 %d 个（不含系统预置的 %d 个），请先删除不用的再创建", maxCustomWorkers, len(presetWorkerMetas))
	}
	s.items[cw.ID] = cw
	s.mu.Unlock()

	if err := s.persist(cw); err != nil {
		s.mu.Lock()
		delete(s.items, cw.ID)
		s.mu.Unlock()
		return CustomWorker{}, err
	}
	return cw, nil
}

// Remove 删除一个自定义 worker；不存在返回可操作错误。
func (s *CustomWorkerStore) Remove(id string) error {
	if s == nil {
		return fmt.Errorf("自定义 worker 存储未就绪，请重启应用后再试")
	}
	key := strings.ToLower(strings.TrimSpace(id))
	s.mu.Lock()
	s.loadLocked()
	if _, ok := s.items[key]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("自定义 worker 不存在：%s", id)
	}
	delete(s.items, key)
	s.mu.Unlock()

	if s.store == nil {
		return nil
	}
	if err := s.store.Delete(customWorkerBucket, key); err != nil {
		return fmt.Errorf("删除自定义 worker 失败：%w", err)
	}
	return nil
}

// CreatePreset 是 EnsurePresetWorkers 专用入口：允许占用旧内置 id（premarket 等），
// 但不允许覆盖总控，也不做数量上限（预设必须全部落成）。
// 其它校验（标题/描述/提示词长度）与 Create 一致。
func (s *CustomWorkerStore) CreatePreset(in CreateWorkerRequest, now time.Time) (CustomWorker, error) {
	if s == nil {
		return CustomWorker{}, fmt.Errorf("自定义 worker 存储未就绪，请重启应用后再试")
	}
	id := strings.ToLower(strings.TrimSpace(in.ID))
	if WorkerID(id) == WorkerRouter {
		return CustomWorker{}, fmt.Errorf("worker id %q 是 RouterAgent，不能作为预设 worker 创建", in.ID)
	}
	if !customWorkerIDPattern.MatchString(id) {
		return CustomWorker{}, fmt.Errorf("worker id %q 不合法：需小写字母开头，后续仅小写字母/数字/下划线/中划线，长度 2~32", in.ID)
	}
	title := strings.TrimSpace(in.Title)
	if n := len([]rune(title)); n == 0 || n > maxWorkerTitleRunes {
		return CustomWorker{}, fmt.Errorf("worker 标题需 1~%d 字（当前 %d 字）", maxWorkerTitleRunes, n)
	}
	description := strings.TrimSpace(in.Description)
	if n := len([]rune(description)); n > maxWorkerDescriptionRunes {
		return CustomWorker{}, fmt.Errorf("worker 描述最多 %d 字（当前 %d 字）", maxWorkerDescriptionRunes, n)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if n := len([]rune(prompt)); n < MinPromptRunes || n > MaxPromptRunes {
		return CustomWorker{}, fmt.Errorf("worker 提示词需 %d~%d 字（当前 %d 字）", MinPromptRunes, MaxPromptRunes, n)
	}
	createdBy := strings.TrimSpace(in.CreatedBy)
	if createdBy == "" {
		createdBy = presetCreatedBy
	}
	if now.IsZero() {
		now = time.Now()
	}
	cw := CustomWorker{
		ID:          id,
		Title:       title,
		Description: description,
		Prompt:      prompt,
		Model:       strings.TrimSpace(in.Model),
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}

	s.mu.Lock()
	s.loadLocked()
	if _, exists := s.items[cw.ID]; exists {
		s.mu.Unlock()
		return CustomWorker{}, fmt.Errorf("worker id %q 已存在，请换一个", cw.ID)
	}
	s.items[cw.ID] = cw
	s.mu.Unlock()

	if err := s.persist(cw); err != nil {
		s.mu.Lock()
		delete(s.items, cw.ID)
		s.mu.Unlock()
		return CustomWorker{}, err
	}
	return cw, nil
}

// persist 把单个自定义 worker 写入 Store。
func (s *CustomWorkerStore) persist(cw CustomWorker) error {
	if s.store == nil {
		return nil
	}
	buf, err := json.Marshal(cw)
	if err != nil {
		return fmt.Errorf("自定义 worker 序列化失败：%w", err)
	}
	if err := s.store.Put(customWorkerBucket, cw.ID, buf); err != nil {
		return fmt.Errorf("保存自定义 worker 失败：%w", err)
	}
	return nil
}

// normalizeCustomWorker 校验并规范化创建入参（trim + id 小写）。
func normalizeCustomWorker(in CreateWorkerRequest, now time.Time) (CustomWorker, error) {
	rawID := strings.TrimSpace(in.ID)
	id := strings.ToLower(rawID)
	if rawID != id {
		return CustomWorker{}, fmt.Errorf("worker id %q 不合法：只能使用小写字母开头的小写 id", in.ID)
	}
	if !customWorkerIDPattern.MatchString(id) {
		return CustomWorker{}, fmt.Errorf("worker id %q 不合法：需小写字母开头，后续仅小写字母/数字/下划线/中划线，长度 2~32", in.ID)
	}
	// id 冲突：总控与 5 个旧内置 id 都是保留字（旧会话/旧模板/值班调度按它们解析）。
	// 由 EnsurePresetWorkers 以 CreatedBy=preset 预置时走内部 CreatePreset，不经过这里。
	if isReservedWorker(WorkerID(id)) {
		return CustomWorker{}, fmt.Errorf("worker id %q 与内置/预设 worker 冲突，请换一个", id)
	}
	title := strings.TrimSpace(in.Title)
	if n := len([]rune(title)); n == 0 || n > maxWorkerTitleRunes {
		return CustomWorker{}, fmt.Errorf("worker 标题需 1~%d 字（当前 %d 字）", maxWorkerTitleRunes, n)
	}
	description := strings.TrimSpace(in.Description)
	if n := len([]rune(description)); n > maxWorkerDescriptionRunes {
		return CustomWorker{}, fmt.Errorf("worker 描述最多 %d 字（当前 %d 字）", maxWorkerDescriptionRunes, n)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if n := len([]rune(prompt)); n < MinPromptRunes || n > MaxPromptRunes {
		return CustomWorker{}, fmt.Errorf("worker 提示词需 %d~%d 字（当前 %d 字）", MinPromptRunes, MaxPromptRunes, n)
	}
	createdBy := strings.TrimSpace(in.CreatedBy)
	if createdBy == "" {
		createdBy = "user"
	}
	if now.IsZero() {
		now = time.Now()
	}
	return CustomWorker{
		ID:          id,
		Title:       title,
		Description: description,
		Prompt:      prompt,
		Model:       strings.TrimSpace(in.Model),
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}, nil
}

// workerEntry 是 worker 列表/模板列表共用的统一条目（顺序、文案、种类都在这里定死）。
type workerEntry struct {
	ID          WorkerID
	Title       string
	Description string
	// Custom 表示它来自自定义 worker 表（含预置）；总控为 false。
	Custom bool
	// Preset 表示它是系统预置的 5 个 worker 之一。
	Preset bool
	// Builtin 表示内置常驻（只有总控）。
	Builtin bool
}

// workerEntries 是**唯一**的 worker 顺序来源：
// 总控（内置）→ 5 个预设 worker（还没落成自定义时以静态定义补位）→ 其它自定义 worker。
//
// "预设补位"很关键：模板列表与值班卡在升级后的第一次启动就必须能看到
// premarket/intraday/postmarket，不能等 EnsurePresetWorkers 一定跑成功。
func (r *Runtime) workerEntries() []workerEntry {
	all := r.custom.All()
	exists := make(map[string]CustomWorker, len(all))
	for _, cw := range all {
		exists[cw.ID] = cw
	}
	out := make([]workerEntry, 0, len(workerMetas)+len(presetWorkerMetas)+len(all))
	for _, m := range workerMetas {
		out = append(out, workerEntry{ID: m.ID, Title: m.Title, Description: m.Description, Builtin: true})
	}
	for _, m := range presetWorkerMetas {
		if cw, ok := exists[string(m.ID)]; ok {
			out = append(out, workerEntry{
				ID:          WorkerID(cw.ID),
				Title:       cw.Title,
				Description: cw.Description,
				Custom:      true,
				Preset:      cw.CreatedBy == presetCreatedBy,
			})
			continue
		}
		// 还没落成自定义 worker：用静态定义补位，保证值班卡/模板列表不缺项。
		out = append(out, workerEntry{ID: m.ID, Title: m.Title, Description: m.Description, Custom: true, Preset: true})
	}
	presetSet := make(map[string]bool, len(presetWorkerMetas))
	for _, m := range presetWorkerMetas {
		presetSet[string(m.ID)] = true
	}
	for _, cw := range all {
		if presetSet[cw.ID] {
			continue
		}
		out = append(out, workerEntry{
			ID:          WorkerID(cw.ID),
			Title:       cw.Title,
			Description: cw.Description,
			Custom:      true,
		})
	}
	return out
}

// Workers 返回全部 worker 的视图：内置（只有总控）+ 预设 + 已生成的自定义 worker。
// 内置 worker 的模型取模板里保存的值；自定义 worker 取模板覆盖值或定义里的 model。
func (r *Runtime) workerViews() []WorkerView {
	entries := r.workerEntries()
	out := make([]WorkerView, 0, len(entries))
	for _, e := range entries {
		view := WorkerView{
			ID:          string(e.ID),
			Title:       e.Title,
			Description: e.Description,
			Custom:      e.Custom,
			Builtin:     e.Builtin,
			Preset:      e.Preset,
			Model:       r.templates.Model(e.ID),
		}
		switch {
		case e.Builtin:
			view.Kind = WorkerKindMain
			view.Main = true
		case e.Preset:
			view.Kind = WorkerKindPreset
		default:
			view.Kind = WorkerKindCustom
		}
		out = append(out, view)
	}
	return out
}

// EnsurePresetWorkers 把 5 个预设 worker 落成自定义 worker（幂等）。
//
// 收敛后的模型：只有总控是内置常驻，其它 worker 都由总控（或系统代劳）生成。
// 首次启动时调用它，把 premarket/intraday/postmarket/quant/chat 预置进
// 自定义 worker 表（CreatedBy=preset），这样：
//   - Workers() 只返回 router + 已生成的自定义 worker；
//   - 值班调度按 id 精确查得到 premarket/intraday/postmarket；
//   - 旧模板继续按同一份文案生效。
//
// 幂等语义：已存在的 id 一律跳过（不覆盖用户改过的文案与提示词）；
// 返回本次真正新建的 worker id。store 只读失败时按"没有"处理，不阻断启动。
func (r *Runtime) EnsurePresetWorkers() ([]string, error) {
	if r == nil || r.custom == nil {
		return nil, fmt.Errorf("worker 存储未就绪，请重启应用后再试")
	}
	created := make([]string, 0, len(presetWorkerMetas))
	for _, spec := range presetWorkerSpecs() {
		id := string(spec.ID)
		if _, ok := r.custom.Get(id); ok {
			continue
		}
		_, err := r.custom.CreatePreset(CreateWorkerRequest{
			ID:          id,
			Title:       spec.Title,
			Description: spec.Description,
			Prompt:      defaultPrompt(spec.ID),
			CreatedBy:   presetCreatedBy,
		}, r.opts.Clock())
		if err != nil {
			// 已存在（并发/重复调用）不算失败，跳过；其它错误记录并继续，
			// 保证一个预设坏掉不会让整机起不来。
			if strings.Contains(err.Error(), "已存在") {
				continue
			}
			if r.log != nil {
				r.log.Warn("预置 worker 失败", "worker", id, "error", err)
			}
			continue
		}
		created = append(created, id)
	}
	return created, nil
}

// CreateWorker 创建自定义 worker，返回合并后的 worker 列表（含内置与全部自定义）。
func (r *Runtime) CreateWorker(in CreateWorkerRequest) ([]WorkerView, error) {
	if _, err := r.custom.Create(in, r.opts.Clock()); err != nil {
		return nil, err
	}
	return r.workerViews(), nil
}

// RemoveWorker 删除自定义 worker（内置不可删），返回合并后的 worker 列表。
// 若该 worker 有模板覆盖记录，一并清理，避免同 id 重建时复活旧提示词。
func (r *Runtime) RemoveWorker(id string) ([]WorkerView, error) {
	key := strings.ToLower(strings.TrimSpace(id))
	if key == "" {
		return nil, fmt.Errorf("removeWorker 需要 id")
	}
	if isBuiltinWorker(WorkerID(key)) {
		return nil, fmt.Errorf("内置 worker %q 不能删除，只能在提示词编辑器里改它的提示词", key)
	}
	if cw, ok := r.custom.Get(key); ok && cw.CreatedBy == presetCreatedBy {
		return nil, fmt.Errorf("预设 worker %q 不能删除：值班调度依赖它，只能改它的提示词或用 worker.create 另建一个", key)
	}
	if _, ok := r.custom.Get(key); !ok {
		return nil, fmt.Errorf("自定义 worker 不存在：%s", id)
	}
	if err := r.custom.Remove(key); err != nil {
		return nil, err
	}
	if err := r.templates.Delete(WorkerID(key)); err != nil {
		r.log.Warn("清理自定义 worker 模板失败", "worker", key, "error", err)
	}
	return r.workerViews(), nil
}

// WorkerFromID 严格解析 worker id（内置 / 预设 / 自定义），未知 id 返回 false（不像 NormalizeWorker 回落）。
// 预设 id 即使还没落成自定义 worker 也算已知：旧模板与值班卡在升级后第一次启动就该能读到。
func (r *Runtime) WorkerFromID(id string) (WorkerID, bool) {
	key := WorkerID(strings.ToLower(strings.TrimSpace(id)))
	if isBuiltinWorker(key) {
		return key, true
	}
	if _, ok := r.custom.Get(string(key)); ok {
		return key, true
	}
	if _, ok := presetWorkerMetaOf(key); ok {
		return key, true
	}
	return "", false
}

// strictLookupWorker 严格解析 worker id（不把未知 id 回落成 chat），用于 agent.spawn：
// 拼错 worker 时应直接报错并列出可用 worker，而不是静默跑成普通问答。
func (r *Runtime) strictLookupWorker(id string) (WorkerSpec, bool) {
	raw := strings.TrimSpace(id)
	if cw, ok := r.custom.Get(raw); ok {
		return r.customSpec(cw), true
	}
	if spec, ok := staticSpecOf(NormalizeWorker(raw)); ok && (isKnownWorkerAlias(raw) || WorkerID(strings.ToLower(raw)) == spec.ID) {
		return spec, true
	}
	if !isKnownWorkerAlias(raw) {
		return WorkerSpec{}, false
	}
	return r.lookupWorker(raw)
}

// lookupWorker 按 id 查 worker：自定义 worker 优先精确匹配，
// 其次是"旧内置 id"的静态定义（preset 还没落成自定义 worker 时也能跑），
// 最后走内置别名归一化（未知 id 回落成总控/chat）。
func (r *Runtime) lookupWorker(id string) (WorkerSpec, bool) {
	if cw, ok := r.custom.Get(id); ok {
		return r.customSpec(cw), true
	}
	// 预设/旧内置 id 的静态定义（别名也走这里归一化：review→postmarket）。
	// lookupWorker 是**宽容**路径：未知 id 按 NormalizeWorker 的约定回落到 chat，
	// 与收敛前的行为保持一致（前端传错 worker 不该让整个会话报错）。
	// 需要严格校验时用 strictLookupWorker。
	if spec, ok := staticSpecOf(NormalizeWorker(id)); ok {
		return spec, true
	}
	if spec, ok := LookupWorker(id); ok {
		return spec, true
	}
	return WorkerSpec{}, false
}

// customSpec 把自定义 worker 转成可执行的 WorkerSpec：
// system prompt 取模板覆盖值（未改过就是自定义 prompt），披露协议仍由代码统一追加。
func (r *Runtime) customSpec(cw CustomWorker) WorkerSpec {
	prompt := r.templates.Prompt(WorkerID(cw.ID))
	if strings.TrimSpace(prompt) == "" {
		prompt = cw.Prompt
	}
	return WorkerSpec{
		ID:          WorkerID(cw.ID),
		Title:       cw.Title,
		Description: cw.Description,
		System:      composeSystem(prompt),
	}
}

// workerCatalogText 生成总控 system prompt 的动态 worker 目录（含能力目录）。
//
// 目录由代码生成、运行时注入：用户改模板也覆盖不掉它（composeSystemWithCatalog
// 把它放在披露协议之前），所以模型永远看得到"有哪些 worker / 哪些插件方法 / 哪些工具"。
func (r *Runtime) workerCatalogText() string {
	return joinSections(
		capabilityCatalog(r.methodCatalog(), r.toolCatalog()),
		workerCatalog(r.workerViews()),
	)
}

// methodCatalog 返回可被 app.method 调用的插件方法索引。
// 由 agentlab 通过 SetMethodCatalog 注入（kernel.methods 服务的 Catalog 视图）。
func (r *Runtime) methodCatalog() []MethodInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]MethodInfo{}, r.methods...)
}

// toolCatalog 返回工具索引（由 agentlab 从渐进式网关注入）。
func (r *Runtime) toolCatalog() []ToolIndexEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ToolIndexEntry{}, r.tools...)
}

// SetMethodCatalog 注入可调用的插件方法索引（供总控能力目录使用）。
// 可重复调用（插件热重载/方法变化时刷新）。
func (r *Runtime) SetMethodCatalog(items []MethodInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append([]MethodInfo{}, items...)
}

// SetToolCatalog 注入工具索引（供总控能力目录使用）。
func (r *Runtime) SetToolCatalog(items []ToolIndexEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append([]ToolIndexEntry{}, items...)
}

// joinSections 拼接非空段落（每段之间空一行）。
func joinSections(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, nl+nl)
}
