package agentlab

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/internal/agent"
	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- app.method：让主控 Agent 控制观澜自身 ----------
//
// 背景：Agent 原本只能通过 tool.search / tool.describe / tool.call 摸到**插件工具**。
// 但观澜的很多能力是用插件 method 暴露给前端的（modelsettings.save、
// agentlab.setSchedule、console.setEnabled、knowledge.import …），模型够不到，
// 于是「帮我改一下盯盘频率」这类请求只能由用户手点。
//
// 落地方式：kernel 把 Invoke 能力包成 service "kernel.methods" 注册进 ServiceRegistry
// （见 internal/kernel/methodcontrol.go），这里取到后注册 app.method 工具。
//
// 安全边界（三层，缺一不可）：
//  1. 风险等级 write：kernel.CallTool 按 Policy 判风险，exec 永不放开；
//  2. Policy.AllowMethodControl（默认 true，可在设置中心关掉）：关掉后直接拒绝；
//  3. 白名单：只允许调用 kernel 目录里**已激活插件已注册**的方法，
//     plugin 大小写不敏感，method 必须精确命中；不在目录里的一律拒绝并列出可用方法。

// methodControlService 是 kernel 暴露的服务名（直接引用 kernel 的常量，避免两边字面量漂移）。
const methodControlService = kernel.MethodControlService

// methodInvoker 就是 kernel 的真实接口。
//
// 这里**必须**用 kernel.MethodInvoker 而不是本地鸭子类型：接口断言比的是方法签名，
// MethodCatalog 的返回类型若在本地重新定义成另一个 struct，断言会永远失败
// （plugins/console 也是直接 import kernel 的，避免循环依赖没有问题）。
type methodInvoker = kernel.MethodInvoker

// kernelMethodInfo 保留为 kernel.MethodInfo 的别名，测试与 JSON 形状保持不变。
type kernelMethodInfo = kernel.MethodInfo

// appMethodMaxResultBytes 是 app.method 回给模型的结果上限（超长截断并标注）。
const appMethodMaxResultBytes = 12000

// appMethodTool 返回 app.method 工具定义。
//
// schema（给模型看的，稳定）：
//
//	{"plugin":"agentlab","method":"setSchedule","payload":{"phase":"intraday","everyMinutes":15}}
//
// plugin / method 必填；payload 是可选的对象（缺省按 {} 处理）。
func (a *AgentLab) appMethodTool() plugin.Tool {
	return plugin.Tool{
		Name:    "app.method",
		Title:   "调用观澜插件方法",
		Summary: "调插件方法改观澜自身：值班/模型/盯盘/插件开关/知识库",
		Tags:    []string{"agent", "control"},
		Risk:    plugin.RiskWrite,
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"plugin":{"type":"string","description":"插件 id，例如 agentlab / modelsettings / console / knowledge / watch"},` +
			`"method":{"type":"string","description":"方法名，必须命中该插件已注册的方法（不确定就先 app.catalog）"},` +
			`"payload":{"type":"object","description":"方法参数对象，缺省为 {}；字段与前端调用时一致"}` +
			`},"required":["plugin","method"]}`),
		Examples: []string{
			`{"plugin":"agentlab","method":"setSchedule","payload":{"phase":"intraday","everyMinutes":15}}`,
			`{"plugin":"agentlab","method":"schedule","payload":{}}`,
		},
		Run: func(ctx context.Context, args json.RawMessage) (any, error) {
			return a.runAppMethod(ctx, args)
		},
	}
}

// appCatalogTool 返回 app.catalog 工具定义：列出可被 app.method 调用的方法。
// 它是只读的：模型不确定方法名时先查这里，而不是盲试。
func (a *AgentLab) appCatalogTool() plugin.Tool {
	return plugin.Tool{
		Name:    "app.catalog",
		Title:   "观澜方法目录",
		Summary: "列出可被 app.method 调用的插件方法（按插件分组）",
		Tags:    []string{"agent", "control"},
		Risk:    plugin.RiskRead,
		Schema:  json.RawMessage(`{"type":"object","properties":{"plugin":{"type":"string","description":"只看某个插件的方法（可选）"}},"required":[]}`),
		Examples: []string{
			`{"plugin":"agentlab"}`,
		},
		Run: func(_ context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Plugin string `json:"plugin"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, fmt.Errorf("app.catalog 参数不是合法 JSON：%w", err)
				}
			}
			items, err := a.methodCatalog()
			if err != nil {
				// 服务缺失是环境问题：作为文本返回，让模型如实告诉用户，而不是整轮失败。
				return map[string]any{"ok": false, "error": err.Error()}, nil
			}
			want := strings.ToLower(strings.TrimSpace(in.Plugin))
			out := make([]kernelMethodInfo, 0, len(items))
			for _, it := range items {
				if want != "" && strings.ToLower(it.Plugin) != want {
					continue
				}
				out = append(out, it)
			}
			return map[string]any{
				"ok":      true,
				"total":   len(out),
				"methods": out,
				"usage":   `app.method {"plugin":"<插件>","method":"<方法>","payload":{...}}`,
			}, nil
		},
	}
}

// methodCatalog 取 kernel 的方法目录（服务缺失时返回可操作错误）。
func (a *AgentLab) methodCatalog() ([]kernelMethodInfo, error) {
	inv, err := a.requireMethodControl()
	if err != nil {
		return nil, err
	}
	items := inv.MethodCatalog()
	out := make([]kernelMethodInfo, 0, len(items))
	for _, it := range items {
		out = append(out, kernelMethodInfo{Plugin: it.Plugin, Method: it.Method, Description: it.Description})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

// requireMethodControl 取出 kernel 的方法控制面。
// 服务缺失（老版本 kernel / 未激活）时给可操作中文错误，不 panic、不让插件激活失败。
func (a *AgentLab) requireMethodControl() (methodInvoker, error) {
	if a.services == nil {
		return nil, fmt.Errorf("方法控制面不可用：插件没有拿到服务注册表，请重启应用后再试")
	}
	v, ok := a.services.Get(methodControlService)
	if !ok {
		return nil, fmt.Errorf("方法控制面不可用：kernel 没有注册 %q 服务（版本不匹配？），请升级到最新版观澜后重启", methodControlService)
	}
	inv, ok := v.(methodInvoker)
	if !ok {
		return nil, fmt.Errorf("方法控制面类型不符：%q 服务未实现 InvokeMethod/MethodCatalog，请重启应用", methodControlService)
	}
	return inv, nil
}

// runAppMethod 是 app.method 的执行体：校验参数 → 过白名单 → 调 kernel → 截断结果。
//
// 失败一律作为**给模型看的文本**返回（不抛 error），这样 ReAct 循环能自我纠正：
// 模型可以先 app.catalog 看有哪些方法，再重试。
func (a *AgentLab) runAppMethod(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Plugin  string          `json:"plugin"`
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			// 参数不合法也回文本：让 ReAct 能自我纠正，而不是整轮失败。
			return "调用失败：app.method 参数不是合法 JSON（" + err.Error() +
				"）。正确写法：{\"plugin\":\"agentlab\",\"method\":\"setSchedule\",\"payload\":{...}}", nil
		}
	}
	pluginID := strings.ToLower(strings.TrimSpace(in.Plugin))
	method := strings.TrimSpace(in.Method)
	if pluginID == "" || method == "" {
		return "调用失败：app.method 需要 plugin 与 method（先用 app.catalog 看有哪些方法）。", nil
	}
	// 白名单：只允许目录里明确存在的组合，杜绝伪造/路径式写法。
	catalog, err := a.methodCatalog()
	if err != nil {
		return "调用失败：" + err.Error(), nil
	}
	if !methodAllowed(catalog, pluginID, method) {
		return "调用失败：" + unknownMethodHint(catalog, pluginID, method) + "（先用 app.catalog 确认方法名，再重试）", nil
	}
	payload := in.Payload
	if len(payload) == 0 || strings.TrimSpace(string(payload)) == "null" {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return "调用失败：payload 不是合法 JSON 对象。", nil
	}

	inv, err := a.requireMethodControl()
	if err != nil {
		return "调用失败：" + err.Error(), nil
	}
	out, err := inv.InvokeMethod(ctx, pluginID, method, payload)
	if err != nil {
		return "调用 " + pluginID + "." + method + " 失败：" + err.Error(), nil
	}
	text := strings.TrimSpace(string(out))
	if text == "" || text == "null" {
		text = "{}"
	}
	if r := []rune(text); len(r) > appMethodMaxResultBytes {
		text = string(r[:appMethodMaxResultBytes]) + "…（结果过长已截断）"
	}
	return map[string]any{
		"ok":      true,
		"plugin":  pluginID,
		"method":  method,
		"result":  json.RawMessage(text),
		"summary": "已调用 " + pluginID + "." + method,
	}, nil
}

// methodAllowed 判断 (plugin, method) 是否在目录里（plugin 大小写不敏感，方法名精确比）。
func methodAllowed(catalog []kernelMethodInfo, pluginID, method string) bool {
	for _, it := range catalog {
		if strings.EqualFold(it.Plugin, pluginID) && it.Method == method {
			return true
		}
	}
	return false
}

// unknownMethodHint 生成"方法不存在"的可操作提示：先说清是插件问题还是方法问题，
// 再把该插件可用的方法名列出来（最多 12 个，避免把提示撑爆）。
func unknownMethodHint(catalog []kernelMethodInfo, pluginID, method string) string {
	available := make([]string, 0, 16)
	hasPlugin := false
	for _, it := range catalog {
		if !strings.EqualFold(it.Plugin, pluginID) {
			continue
		}
		hasPlugin = true
		available = append(available, it.Method)
	}
	if !hasPlugin {
		plugins := make([]string, 0, 16)
		seen := map[string]bool{}
		for _, it := range catalog {
			if seen[it.Plugin] {
				continue
			}
			seen[it.Plugin] = true
			plugins = append(plugins, it.Plugin)
		}
		sort.Strings(plugins)
		return fmt.Sprintf("插件 %q 不在可调用列表里（可用插件：%s）", pluginID, strings.Join(plugins, " / "))
	}
	if len(available) > 12 {
		available = append(available[:12], "…")
	}
	return fmt.Sprintf("%s 没有方法 %q（可用：%s）", pluginID, method, strings.Join(available, " / "))
}

// agentMethodCatalog 把 kernel 目录转成 agent 包的能力目录项（供总控 system prompt 用）。
func agentMethodCatalog(items []kernelMethodInfo) []agent.MethodInfo {
	out := make([]agent.MethodInfo, 0, len(items))
	for _, it := range items {
		out = append(out, agent.MethodInfo{Plugin: it.Plugin, Method: it.Method, Description: it.Description})
	}
	return out
}
