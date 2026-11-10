// Package kernel 是插件运行时：注册、拓扑激活、聚合工具/面板/方法、调用路由与审计。
// 它不认识任何具体插件，插件在 Activate 阶段通过 Context 把自己的能力交出来。
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

var (
	ErrPluginNotFound = errors.New("kernel: 插件不存在")
	ErrMethodNotFound = errors.New("kernel: 方法不存在")
	ErrToolNotFound   = errors.New("kernel: 工具不存在")
	ErrToolDenied     = errors.New("kernel: 工具风险等级被安全策略拒绝")
)

const (
	// MaxToolResultBytes 是单次工具结果上限，超出截断，防止上下文被单条结果撑爆。
	MaxToolResultBytes = 256 * 1024
)

type Options struct {
	Logger  *slog.Logger
	Store   contract.Store
	Data    contract.DataService
	LLM     contract.LLMService
	Guard   contract.Guard
	Audit   contract.Audit
	Clock   func() time.Time
	DataDir string
}

type entry struct {
	plugin      plugin.Plugin
	manifest    plugin.Manifest
	ctx         *plugin.Context
	active      bool
	skipped     bool
	activateErr string
}

type Kernel struct {
	opts Options
	log  *slog.Logger

	mu      sync.RWMutex
	entries map[string]*entry
	order   []string
	tools   map[string]plugin.Tool
	started bool

	services *plugin.ServiceRegistry
	events   *plugin.Bus
}

func New(opts Options) (*Kernel, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Store == nil {
		return nil, errors.New("kernel: 需要 Store")
	}
	if opts.Guard == nil {
		return nil, errors.New("kernel: 需要 Guard")
	}
	if opts.Audit == nil {
		return nil, errors.New("kernel: 需要 Audit")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Kernel{
		opts:     opts,
		log:      opts.Logger,
		entries:  map[string]*entry{},
		tools:    map[string]plugin.Tool{},
		services: plugin.NewServiceRegistry(),
		events:   plugin.NewBus(),
	}, nil
}

// Register 登记插件；只登记不激活。重复 ID 直接报错，避免"看起来装了其实没装"。
func (k *Kernel) Register(p plugin.Plugin) error {
	if p == nil {
		return errors.New("kernel: 插件为 nil")
	}
	m := p.Manifest()
	if err := m.Validate(); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.entries[m.ID]; exists {
		return fmt.Errorf("kernel: 插件 %q 重复注册", m.ID)
	}
	k.entries[m.ID] = &entry{plugin: p, manifest: m}
	k.order = append(k.order, m.ID)
	return nil
}

// Start 按依赖拓扑序激活全部插件。单个插件失败只影响其自身与依赖它的插件，不影响整体。
func (k *Kernel) Start(ctx context.Context) error {
	k.mu.Lock()
	if k.started {
		k.mu.Unlock()
		return errors.New("kernel: 已启动")
	}
	k.started = true
	ids := append([]string{}, k.order...)
	k.mu.Unlock()

	ordered, err := k.topoOrder(ids)
	if err != nil {
		return err
	}
	for _, id := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		k.activate(ctx, id)
	}
	active := 0
	for _, e := range k.snapshot() {
		if e.active {
			active++
		}
	}
	k.log.Info("kernel 启动完成", "plugins", len(ids), "active", active, "tools", len(k.Tools()))
	return nil
}

func (k *Kernel) topoOrder(ids []string) ([]string, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	state := map[string]int{} // 0 未访问 1 访问中 2 完成
	var out []string
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("kernel: 插件依赖成环: %v", append(path, id))
		case 2:
			return nil
		}
		state[id] = 1
		e, ok := k.entries[id]
		if !ok {
			return fmt.Errorf("kernel: 依赖的插件 %q 未注册（来自 %v）", id, path)
		}
		deps := append([]string{}, e.manifest.DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		out = append(out, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (k *Kernel) activate(ctx context.Context, id string) {
	k.mu.RLock()
	e := k.entries[id]
	k.mu.RUnlock()
	if e == nil {
		return
	}
	for _, dep := range e.manifest.DependsOn {
		k.mu.RLock()
		d := k.entries[dep]
		k.mu.RUnlock()
		if d == nil || !d.active {
			k.markSkipped(id, "依赖未激活: "+dep)
			return
		}
	}

	tools := plugin.NewToolRegistry(id)
	ui := plugin.NewUIRegistry(id)
	methods := plugin.NewMethodRegistry(id)
	settingsFn, saveFn := k.settingsFuncs(id)
	c := &plugin.Context{
		ID:           id,
		Manifest:     e.manifest,
		Log:          k.log.With("plugin", id),
		Clock:        k.opts.Clock,
		DataDir:      k.opts.DataDir,
		Tools:        tools,
		ToolHost:     k,
		UI:           ui,
		Methods:      methods,
		Services:     k.services,
		Events:       k.events,
		Store:        k.opts.Store,
		Data:         k.opts.Data,
		LLM:          k.opts.LLM,
		Guard:        k.opts.Guard,
		Audit:        k.opts.Audit,
		Policy:       k.opts.Guard.Policy(),
		Settings:     settingsFn,
		SaveSettings: saveFn,
	}

	if err := safeActivate(e.plugin, c); err != nil {
		k.mu.Lock()
		e.activateErr = err.Error()
		k.mu.Unlock()
		k.log.Error("插件激活失败", "plugin", id, "error", err)
		k.opts.Audit.Record(contract.AuditEvent{Time: k.opts.Clock(), Plugin: id, Kind: "lifecycle", Action: "activate", OK: false, Detail: err.Error()})
		return
	}

	// 工具合并：先整体查重再提交，避免"一半工具进索引、插件又没激活"的脏状态。
	k.mu.Lock()
	conflict := ""
	conflictWith := ""
	newTools := tools.List()
	for _, t := range newTools {
		if prev, exists := k.tools[t.Name]; exists {
			conflict = t.Name
			conflictWith = prev.Owner
			break
		}
	}
	if conflict != "" {
		e.activateErr = "工具与其它插件重名: " + conflict + "（已有: " + conflictWith + "）"
		k.mu.Unlock()
		k.log.Error("工具重名，插件激活撤销", "plugin", id, "tool", conflict, "conflictWith", conflictWith)
		_ = e.plugin.Deactivate(ctx)
		k.opts.Audit.Record(contract.AuditEvent{Time: k.opts.Clock(), Plugin: id, Kind: "lifecycle", Action: "activate", OK: false, Detail: e.activateErr})
		return
	}
	for _, t := range newTools {
		k.tools[t.Name] = t
	}
	e.ctx = c
	e.active = true
	k.mu.Unlock()

	k.log.Info("插件已激活", "plugin", id, "name", e.manifest.Name, "tools", len(tools.List()), "ui", len(ui.List()), "methods", len(methods.List()))
	k.opts.Audit.Record(contract.AuditEvent{Time: k.opts.Clock(), Plugin: id, Kind: "lifecycle", Action: "activate", OK: true})
	k.events.Publish(plugin.Event{Topic: "kernel.plugin.activated", Payload: mustJSON(map[string]string{"id": id})})
}

func (k *Kernel) markSkipped(id, reason string) {
	k.mu.Lock()
	if e := k.entries[id]; e != nil {
		e.skipped = true
		e.activateErr = reason
	}
	k.mu.Unlock()
	k.log.Warn("插件跳过", "plugin", id, "reason", reason)
}

// Stop 逆序停用，尽量让依赖方先停。
func (k *Kernel) Stop(ctx context.Context) error {
	k.mu.Lock()
	ids := make([]string, 0, len(k.order))
	for i := len(k.order) - 1; i >= 0; i-- {
		ids = append(ids, k.order[i])
	}
	k.started = false
	k.mu.Unlock()

	for _, id := range ids {
		k.mu.RLock()
		e := k.entries[id]
		k.mu.RUnlock()
		if e == nil || !e.active {
			continue
		}
		if err := safeDeactivate(e.plugin, ctx); err != nil {
			k.log.Error("插件停用失败", "plugin", id, "error", err)
			continue
		}
		k.mu.Lock()
		e.active = false
		k.mu.Unlock()
	}
	return nil
}

// ---------- 视图 ----------

type PluginView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Author      string          `json:"author,omitempty"`
	DependsOn   []string        `json:"dependsOn,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Active      bool            `json:"active"`
	Skipped     bool            `json:"skipped,omitempty"`
	Error       string          `json:"error,omitempty"`
	Tools       []ToolView      `json:"tools"`
	UI          []plugin.UISlot `json:"ui"`
	Methods     []MethodView    `json:"methods"`
}

type ToolView struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags,omitempty"`
	Risk    string   `json:"risk"`
}

type MethodView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type StatusView struct {
	Started    bool              `json:"started"`
	Plugins    int               `json:"plugins"`
	Active     int               `json:"active"`
	Tools      int               `json:"tools"`
	UI         int               `json:"ui"`
	Services   []string          `json:"services"`
	Policy     contract.Policy   `json:"policy"`
	DataHealth map[string]string `json:"dataHealth,omitempty"`
	Time       time.Time         `json:"time"`
}

func (k *Kernel) Plugins() []PluginView {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]PluginView, 0, len(k.order))
	for _, id := range k.order {
		e := k.entries[id]
		if e == nil {
			continue
		}
		v := PluginView{
			ID:          e.manifest.ID,
			Name:        e.manifest.Name,
			Version:     e.manifest.Version,
			Description: e.manifest.Description,
			Author:      e.manifest.Author,
			DependsOn:   e.manifest.DependsOn,
			Tags:        e.manifest.Tags,
			Active:      e.active,
			Skipped:     e.skipped,
			Error:       e.activateErr,
		}
		if e.ctx != nil {
			for _, t := range e.ctx.Tools.List() {
				v.Tools = append(v.Tools, ToolView{Name: t.Name, Title: t.Title, Summary: t.Summary, Tags: t.Tags, Risk: string(t.Risk)})
			}
			v.UI = e.ctx.UI.List()
			for _, m := range e.ctx.Methods.List() {
				v.Methods = append(v.Methods, MethodView{Name: m.Name, Description: m.Description})
			}
		}
		out = append(out, v)
	}
	return out
}

// UISlots 返回所有活跃插件贡献的面板，前端据此动态生成侧边栏与路由。
func (k *Kernel) UISlots() []plugin.UISlot {
	k.mu.RLock()
	defer k.mu.RUnlock()
	var out []plugin.UISlot
	for _, id := range k.order {
		if e := k.entries[id]; e != nil && e.active && e.ctx != nil {
			out = append(out, e.ctx.UI.List()...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// Tools 返回全部已注册工具（跨插件），供渐进式披露层构建 L1 索引。
func (k *Kernel) Tools() []plugin.Tool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]plugin.Tool, 0, len(k.tools))
	for _, t := range k.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (k *Kernel) Tool(name string) (plugin.Tool, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	t, ok := k.tools[name]
	return t, ok
}

func (k *Kernel) Status() StatusView {
	k.mu.RLock()
	defer k.mu.RUnlock()
	active := 0
	for _, e := range k.entries {
		if e.active {
			active++
		}
	}
	v := StatusView{
		Started:  k.started,
		Plugins:  len(k.order),
		Active:   active,
		Tools:    len(k.tools),
		Policy:   k.opts.Guard.Policy(),
		Services: k.services.Names(),
		Time:     k.opts.Clock(),
	}
	for _, e := range k.entries {
		if e.active && e.ctx != nil {
			v.UI += len(e.ctx.UI.List())
		}
	}
	if k.opts.Data != nil {
		v.DataHealth = k.opts.Data.Health(context.Background())
	}
	return v
}

// ---------- 调用 ----------

// Invoke 是前端到插件的唯一入口（Wails 绑定它）。
func (k *Kernel) Invoke(ctx context.Context, pluginID, method string, payload json.RawMessage) (json.RawMessage, error) {
	k.mu.RLock()
	e := k.entries[pluginID]
	k.mu.RUnlock()
	if e == nil {
		return nil, fmt.Errorf("%w: %s", ErrPluginNotFound, pluginID)
	}
	if !e.active || e.ctx == nil {
		return nil, fmt.Errorf("kernel: 插件 %s 未激活: %s", pluginID, e.activateErr)
	}
	m, ok := e.ctx.Methods.Get(method)
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s", ErrMethodNotFound, pluginID, method)
	}
	start := k.opts.Clock()
	out, err := safeCall(func() (any, error) {
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		return m.Fn(ctx, payload)
	})
	elapsed := k.opts.Clock().Sub(start).Milliseconds()
	k.opts.Audit.Record(contract.AuditEvent{
		Time: start, Plugin: pluginID, Kind: "method", Action: method,
		OK: err == nil, Elapsed: elapsed, Detail: errText(err),
	})
	if err != nil {
		return nil, err
	}
	return toJSON(out)
}

// CallTool 执行工具。这是渐进式披露 L3 的唯一落点，风险与审计都在这里把关。
func (k *Kernel) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	t, ok := k.Tool(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}
	policy := k.opts.Guard.Policy()
	maxRisk := plugin.RiskWrite
	if !policy.AllowExec {
		maxRisk = plugin.RiskWrite
	}
	if !t.Risk.AtMost(maxRisk) {
		k.opts.Audit.Record(contract.AuditEvent{Time: k.opts.Clock(), Plugin: t.Owner, Kind: "security", Action: name, OK: false, Risk: string(t.Risk), Detail: "风险等级超出策略"})
		return nil, fmt.Errorf("%w: %s(%s)", ErrToolDenied, name, t.Risk)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	start := k.opts.Clock()
	out, err := safeCall(func() (any, error) { return t.Run(ctx, args) })
	elapsed := k.opts.Clock().Sub(start).Milliseconds()
	k.opts.Audit.Record(contract.AuditEvent{
		Time: start, Plugin: t.Owner, Kind: "tool", Action: name,
		Risk: string(t.Risk), OK: err == nil, Elapsed: elapsed, Detail: errText(err),
	})
	if err != nil {
		return nil, err
	}
	raw, err := toJSON(out)
	if err != nil {
		return nil, err
	}
	return truncateJSON(raw, MaxToolResultBytes), nil
}

func (k *Kernel) Services() *plugin.ServiceRegistry { return k.services }
func (k *Kernel) Events() *plugin.Bus               { return k.events }
func (k *Kernel) Audit() contract.Audit             { return k.opts.Audit }
func (k *Kernel) Guard() contract.Guard             { return k.opts.Guard }
func (k *Kernel) Data() contract.DataService        { return k.opts.Data }
func (k *Kernel) LLM() contract.LLMService          { return k.opts.LLM }
func (k *Kernel) Store() contract.Store             { return k.opts.Store }
func (k *Kernel) Logger() *slog.Logger              { return k.log }

// ---------- 内部 ----------

func (k *Kernel) snapshot() []*entry {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]*entry, 0, len(k.entries))
	for _, e := range k.entries {
		out = append(out, e)
	}
	return out
}

func (k *Kernel) settingsFuncs(id string) (func() json.RawMessage, func(json.RawMessage) error) {
	bucket := "plugin_settings"
	get := func() json.RawMessage {
		raw, err := k.opts.Store.Get(bucket, id)
		if err != nil || len(raw) == 0 {
			return json.RawMessage("{}")
		}
		return raw
	}
	save := func(v json.RawMessage) error {
		return k.opts.Store.Put(bucket, id, v)
	}
	return get, save
}

func safeActivate(p plugin.Plugin, c *plugin.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("插件 Activate panic: %v", r)
		}
	}()
	return p.Activate(c)
}

func safeDeactivate(p plugin.Plugin, ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("插件 Deactivate panic: %v", r)
		}
	}()
	return p.Deactivate(ctx)
}

func safeCall(fn func() (any, error)) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("调用 panic: %v", r)
		}
	}()
	return fn()
}

func toJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return truncateJSON(raw, MaxToolResultBytes), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("kernel: 结果序列化失败: %w", err)
	}
	return truncateJSON(b, MaxToolResultBytes), nil
}

func truncateJSON(raw json.RawMessage, limit int) json.RawMessage {
	if len(raw) <= limit {
		return raw
	}
	payload := map[string]any{
		"truncated": true,
		"limit":     limit,
		"preview":   string(raw[:limit]),
	}
	b, _ := json.Marshal(payload)
	return b
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
