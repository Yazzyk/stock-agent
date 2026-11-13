package kernel

import "sort"

// Inventory 是「设置中心」需要的运行时总览与启停开关（契约 §3.9）。
//
// 接口定义在 kernel 而不是 bootstrap，原因：bootstrap 会 import plugins/builtin，
// 而 builtin 又 blank import 了 plugins/console —— 若 console import bootstrap 就是循环依赖。
// kernel 不 import 任何插件，因此是这套视图类型与接口的唯一合适宿主：
// bootstrap 用 kernel.Inventory 的实现注册服务 "kernel.inventory"，console 插件断言取用。
type Inventory interface {
	// Plugins 返回全部已注册插件（含已停用/失败，保持可见）。
	Plugins() []PluginView
	// Status 返回运行时状态（含数据源健康）。
	Status() StatusView
	// Tools 返回全部工具视图（含来源插件）。
	Tools() []ToolView
	// Disabled 返回当前已停用的插件 ID（按持久化配置，重启生效）。
	Disabled() []string
	// SetEnabled 持久化启停开关；运行中的插件不做热停用，统一提示重启。
	SetEnabled(id string, enabled bool) error
	// RestartRequired 表示自启动以来是否有需要重启才生效的变更。
	RestartRequired() bool
}

// DisabledReason 是启动时因被停用而跳过的原因文案，设置中心直接展示。
const DisabledReason = "已停用（可在设置中心启用，重启生效）"

// ToolViews 返回全部工具的前端视图，并标明来源插件。
func (k *Kernel) ToolViews() []ToolView {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]ToolView, 0, len(k.tools))
	for _, t := range k.tools {
		out = append(out, ToolView{
			Name: t.Name, Title: t.Title, Summary: t.Summary,
			Tags: t.Tags, Risk: string(t.Risk), Owner: t.Owner,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
