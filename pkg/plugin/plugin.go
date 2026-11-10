// Package plugin 定义插件契约：Manifest / Plugin / Context 与四类贡献点
// （工具、UI 面板、前端方法、服务）。kernel 负责生命周期与聚合。
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ---------- Manifest ----------

type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      string   `json:"author,omitempty"`
	DependsOn   []string `json:"dependsOn,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Provides    []string `json:"provides,omitempty"`
}

func (m Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("plugin: manifest.id 不能为空")
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("plugin: manifest.name 不能为空")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("plugin: manifest.version 不能为空")
	}
	return nil
}

type Plugin interface {
	Manifest() Manifest
	Activate(*Context) error
	Deactivate(context.Context) error
}

// Base 提供空的 Deactivate，插件内嵌即可少写一个方法。
type Base struct{}

func (Base) Deactivate(context.Context) error { return nil }

// ---------- 风险等级 ----------

type RiskLevel string

const (
	RiskRead    RiskLevel = "read"
	RiskNetwork RiskLevel = "network"
	RiskWrite   RiskLevel = "write"
	RiskExec    RiskLevel = "exec"
)

var riskOrder = map[RiskLevel]int{RiskRead: 0, RiskNetwork: 1, RiskWrite: 2, RiskExec: 3}

// AtMost 判断风险等级是否在允许上限内。
func (r RiskLevel) AtMost(limit RiskLevel) bool {
	lv, ok := riskOrder[r]
	if !ok {
		return false
	}
	max, ok := riskOrder[limit]
	if !ok {
		return false
	}
	return lv <= max
}

// ---------- 工具 ----------

// Tool 是一个可被 LLM 调用的能力。字段分别服务渐进式披露的三级：
// Name/Title/Summary/Tags = L1；Schema/Examples = L2；Run = L3。
type Tool struct {
	Name     string                                                    `json:"name"`
	Title    string                                                    `json:"title"`
	Summary  string                                                    `json:"summary"`
	Tags     []string                                                  `json:"tags,omitempty"`
	Schema   json.RawMessage                                           `json:"schema,omitempty"`
	Examples []string                                                  `json:"examples,omitempty"`
	Risk     RiskLevel                                                 `json:"risk"`
	Run      func(ctx context.Context, args json.RawMessage) (any, error) `json:"-"`
	Owner    string                                                    `json:"owner,omitempty"`
}

// Validate 在注册时兜底，避免半成品工具进索引。
func (t Tool) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("plugin: tool.name 不能为空")
	}
	if strings.TrimSpace(t.Summary) == "" {
		return fmt.Errorf("plugin: tool %q 缺少 summary（L1 渐进式披露需要）", t.Name)
	}
	if t.Run == nil {
		return fmt.Errorf("plugin: tool %q 缺少 Run", t.Name)
	}
	if t.Risk == "" {
		return fmt.Errorf("plugin: tool %q 缺少 risk", t.Name)
	}
	return nil
}

type ToolRegistry struct {
	mu    sync.RWMutex
	owner string
	tools map[string]Tool
}

func NewToolRegistry(owner string) *ToolRegistry {
	return &ToolRegistry{owner: owner, tools: map[string]Tool{}}
}

func (r *ToolRegistry) Add(t Tool) error {
	if err := t.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("plugin: tool %q 重复注册", t.Name)
	}
	t.Owner = r.owner
	if t.Title == "" {
		t.Title = t.Name
	}
	r.tools[t.Name] = t
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------- UI ----------

// UISlot 是插件贡献给前端的面板。Component 是前端组件注册表里的键。
type UISlot struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Icon      string `json:"icon,omitempty"`
	Group     string `json:"group,omitempty"`
	Component string `json:"component"`
	Order     int    `json:"order"`
	Owner     string `json:"owner,omitempty"`
}

type UIRegistry struct {
	mu    sync.RWMutex
	owner string
	slots map[string]UISlot
}

func NewUIRegistry(owner string) *UIRegistry {
	return &UIRegistry{owner: owner, slots: map[string]UISlot{}}
}

func (r *UIRegistry) Add(s UISlot) error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Component) == "" {
		return errors.New("plugin: ui slot 需要 id 与 component")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.slots[s.ID]; exists {
		return fmt.Errorf("plugin: ui slot %q 重复注册", s.ID)
	}
	s.Owner = r.owner
	r.slots[s.ID] = s
	return nil
}

func (r *UIRegistry) List() []UISlot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]UISlot, 0, len(r.slots))
	for _, s := range r.slots {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// ---------- 前端方法 ----------

type MethodFunc func(ctx context.Context, payload json.RawMessage) (any, error)

type Method struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Fn          MethodFunc `json:"-"`
	Owner       string     `json:"owner,omitempty"`
}

type MethodRegistry struct {
	mu      sync.RWMutex
	owner   string
	methods map[string]Method
}

func NewMethodRegistry(owner string) *MethodRegistry {
	return &MethodRegistry{owner: owner, methods: map[string]Method{}}
}

func (r *MethodRegistry) Add(m Method) error {
	if strings.TrimSpace(m.Name) == "" || m.Fn == nil {
		return errors.New("plugin: method 需要 name 与 Fn")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.methods[m.Name]; exists {
		return fmt.Errorf("plugin: method %q 重复注册", m.Name)
	}
	m.Owner = r.owner
	r.methods[m.Name] = m
	return nil
}

func (r *MethodRegistry) Get(name string) (Method, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.methods[name]
	return m, ok
}

func (r *MethodRegistry) List() []Method {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Method, 0, len(r.methods))
	for _, m := range r.methods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------- 服务 ----------

type ServiceRegistry struct {
	mu   sync.RWMutex
	svcs map[string]any
}

func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{svcs: map[string]any{}}
}

func (r *ServiceRegistry) Register(name string, impl any) error {
	if strings.TrimSpace(name) == "" || impl == nil {
		return errors.New("plugin: service 需要 name 与非空 impl")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.svcs[name]; exists {
		return fmt.Errorf("plugin: service %q 重复注册", name)
	}
	r.svcs[name] = impl
	return nil
}

func (r *ServiceRegistry) Get(name string) (any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.svcs[name]
	return v, ok
}

func (r *ServiceRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.svcs))
	for k := range r.svcs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- 事件 ----------

type Event struct {
	Topic   string          `json:"topic"`
	Time    time.Time       `json:"time"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Bus struct {
	mu   sync.RWMutex
	next int
	subs map[string]map[int]func(Event)
}

func NewBus() *Bus {
	return &Bus{subs: map[string]map[int]func(Event){}}
}

// Subscribe 订阅 topic；topic 为 "*" 时接收所有事件。返回取消订阅函数。
func (b *Bus) Subscribe(topic string, fn func(Event)) func() {
	b.mu.Lock()
	id := b.next
	b.next++
	if b.subs[topic] == nil {
		b.subs[topic] = map[int]func(Event){}
	}
	b.subs[topic][id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs[topic], id)
		b.mu.Unlock()
	}
}

// Publish 异步广播，慢订阅者不阻塞发布者。
func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.RLock()
	exact := make([]func(Event), 0, len(b.subs[e.Topic]))
	for _, fn := range b.subs[e.Topic] {
		exact = append(exact, fn)
	}
	all := make([]func(Event), 0, len(b.subs["*"]))
	for _, fn := range b.subs["*"] {
		all = append(all, fn)
	}
	b.mu.RUnlock()
	for _, fn := range append(exact, all...) {
		f := fn
		go f(e)
	}
}

// ---------- 工具宿主 ----------

// ToolHost 让插件能跨插件发现与执行工具（ReAct 循环、渐进式披露网关需要）。
// kernel 是唯一实现：Tools 返回全局已注册工具，CallTool 走统一的风险校验与审计。
type ToolHost interface {
	Tools() []Tool
	CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// ---------- Context ----------

// Context 是插件在 Activate 期间拿到的全部能力入口。
type Context struct {
	ID       string
	Manifest Manifest
	Log      *slog.Logger
	Clock    func() time.Time
	// DataDir 是本应用的本地数据根目录（~/.guanlan），插件落盘必须限制在其中。
	DataDir string

	Tools    *ToolRegistry
	ToolHost ToolHost
	UI       *UIRegistry
	Methods  *MethodRegistry
	Services *ServiceRegistry
	Events   *Bus

	Store  contract.Store
	Data   contract.DataService
	LLM    contract.LLMService
	Guard  contract.Guard
	Audit  contract.Audit
	Policy contract.Policy

	// Settings 读取本插件持久化配置（JSON），SaveSettings 写回。
	Settings     func() json.RawMessage
	SaveSettings func(json.RawMessage) error
}
