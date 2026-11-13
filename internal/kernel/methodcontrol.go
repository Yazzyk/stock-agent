package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------- 主控 Agent 的方法控制面 ----------
//
// 背景：harness 里的 Agent 只能通过 tool.search / tool.describe / tool.call 摸到
// **插件注册的工具**；插件 method（modelsettings.save、agentlab.setSchedule、
// console.setEnabled、knowledge.import …）是前端 IPC 的入口，模型够不到。
// 于是「帮我改一下盯盘规则」「把盘中频率改成 15 分钟」这类请求只能由用户手点。
//
// 这里把 kernel 的 Invoke 能力包成一个 service（kernel.methods）注册进 ServiceRegistry，
// agentlab 取到后注册 app.method 工具，主控就能调任意插件方法。

// MethodControl 是注册到 ServiceRegistry 的服务名。插件用 Services.Get(MethodControlService) 取用。
const MethodControlService = "kernel.methods"

// MethodInfo 是一个可被 Agent 调用的插件方法（只含元信息，不含函数）。
type MethodInfo struct {
	Plugin      string `json:"plugin"`
	Method      string `json:"method"`
	Description string `json:"description,omitempty"`
}

// MethodInvoker 由 kernel 实现，经 services 暴露给插件（避免插件直接依赖 kernel 包）。
type MethodInvoker interface {
	// InvokeMethod 调用某个插件方法；plugin 不在激活列表、method 不存在都返回可操作错误。
	InvokeMethod(ctx context.Context, pluginID, method string, payload json.RawMessage) (json.RawMessage, error)
	// MethodCatalog 列出当前可调用方法（按 plugin 名 + method 名排序）。
	MethodCatalog() []MethodInfo
}

// methodInvoker 把 Kernel 适配成 MethodInvoker，并在这里统一做安全边界。
type methodInvoker struct{ k *Kernel }

// InvokeMethod 是 app.method 的落点：
//   - 策略 AllowMethodControl=false 时直接拒绝（默认 true）；
//   - 只允许调用**已激活**插件的**已注册**方法，不允许跨插件伪造；
//   - 不放开 kernel 内部方法（Invoke 本来就只查 MethodRegistry）。
func (m *methodInvoker) InvokeMethod(ctx context.Context, pluginID, method string, payload json.RawMessage) (json.RawMessage, error) {
	if m == nil || m.k == nil {
		return nil, errors.New("kernel: 方法控制面未就绪")
	}
	if !m.k.opts.Guard.Policy().AllowMethodControl {
		return nil, errors.New("策略已关闭方法控制（AllowMethodControl=false）：请在设置中心打开后再试")
	}
	id := strings.TrimSpace(pluginID)
	name := strings.TrimSpace(method)
	if id == "" || name == "" {
		return nil, errors.New("app.method 需要 plugin 与 method")
	}
	return m.k.Invoke(ctx, id, name, payload)
}

// MethodCatalog 返回全部可调用方法，供 Agent 侧生成能力目录。
func (m *methodInvoker) MethodCatalog() []MethodInfo {
	if m == nil || m.k == nil {
		return nil
	}
	out := make([]MethodInfo, 0, 64)
	for _, e := range m.k.snapshot() {
		if !e.active || e.ctx == nil {
			continue
		}
		for _, method := range e.ctx.Methods.List() {
			out = append(out, MethodInfo{
				Plugin:      e.manifest.ID,
				Method:      method.Name,
				Description: strings.TrimSpace(method.Description),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// pluginServiceNames 返回**插件注册的**服务名（按名排序）。
// kernel 自己的方法控制面也登记在同一个 registry 里，但它不是插件贡献，
// 不该出现在"服务"统计里（那会让 Status 把内建能力算成插件能力）。
func (k *Kernel) pluginServiceNames() []string {
	names := k.services.Names()
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == MethodControlService {
			continue
		}
		out = append(out, n)
	}
	return out
}

// registerMethodControl 在第一个插件激活前把方法控制面注册进 services。
// 注册失败（例如已被别处注册）不阻断启动：agentlab 取不到时会给出可操作提示。
func (k *Kernel) registerMethodControl() {
	if _, ok := k.services.Get(MethodControlService); ok {
		return
	}
	if err := k.services.Register(MethodControlService, MethodInvoker(&methodInvoker{k: k})); err != nil {
		k.log.Warn("方法控制面注册失败", "error", err)
	}
}

// CatalogText 把方法目录压成给模型看的一行行文本（agentlab 生成能力目录时复用）。
func CatalogText(items []MethodInfo, maxPerPlugin int) string {
	if len(items) == 0 {
		return ""
	}
	if maxPerPlugin <= 0 {
		maxPerPlugin = 12
	}
	var b strings.Builder
	current := ""
	shown := 0
	for _, it := range items {
		if it.Plugin != current {
			if current != "" {
				b.WriteString("\n")
			}
			current = it.Plugin
			shown = 0
			b.WriteString("- " + it.Plugin + "：")
			continue
		}
		if shown >= maxPerPlugin {
			continue
		}
		b.WriteString(it.Method + "、")
		shown++
	}
	text := strings.TrimRight(b.String(), "、")
	if text == "" {
		return ""
	}
	return fmt.Sprintf("%s", text)
}
