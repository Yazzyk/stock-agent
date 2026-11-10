# 插件开发指南

> 目标：**新增一个功能 = 新增一个 `plugins/<name>` 包**，不改 kernel、不改前端侧边栏、不改别人的插件。

## 0. 插件能提供什么

一个插件在 `Activate` 阶段通过 `*plugin.Context` 交出四类能力：

| 能力 | API | 谁消费 |
|---|---|---|
| 工具 | `ctx.Tools.Add(Tool{Name, Title, Summary, Tags, Schema, Examples, Risk, Run})` | LLM（经渐进式披露网关） |
| 面板 | `ctx.UI.Add(UISlot{ID, Title, Component, Group, Order})` | 前端动态导航（`Plugins().ui`） |
| 方法 | `ctx.Methods.Add(Method{Name, Fn})` | 前端 `Invoke(pluginID, method, payload)` |
| 服务 | `ctx.Services.Register("name", impl)` | 其它插件（无 import 依赖） |

另有 `ctx.Store / Data / LLM / Guard / Audit / Events` 作为基础设施。

## 1. 最小插件（30 行）

```go
package hello

import (
	"context"
	"encoding/json"

	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

func init() { all.Register(New) }

type Plugin struct{ plugin.Base }

func New() plugin.Plugin { return &Plugin{} }

func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID: "hello", Name: "示例", Version: "0.1.0",
		Description: "最小插件示例",
		Tags:        []string{"示例"},
	}
}

func (p *Plugin) Activate(c *plugin.Context) error {
	if err := c.Methods.Add(plugin.Method{
		Name: "ping",
		Fn: func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"pong": "pong"}, nil
		},
	}); err != nil {
		return err
	}
	return c.UI.Add(plugin.UISlot{
		ID: "hello", Title: "示例", Component: "hello", Group: "研究", Order: 50,
	})
}
```

## 2. 三处接线（都只需一行）

1. `plugins/builtin/builtin.go` 加 `_ "github.com/yazzyk/stock-agent/plugins/hello"`
2. 前端 `frontend/src/panels/hello.vue`（组件键 = `UISlot.Component`），`import.meta.glob` 自动收录
3. `docs/API.md` 登记方法契约（前后端字段逐字对齐）

## 3. 工具的三级披露义务

`Tool` 的字段不是随便填的，它们直接决定模型能不能用对：

- `Summary` ≤ 40 字，写**什么场景用**（不是实现细节）——这是模型看到的 L1 索引；
- `Tags` 至少一个域标签（market/flow/watch/agent/kb/skill/quant/model）；
- `Schema` 必须是合法 JSON Schema；模型只在 `tool.describe` 后才看到它（L2）；
- `Risk`：`read`（纯计算）/ `network`（取数）/ `write`（写本地配置）；`exec` 永不开放；
- 返回体尽量 ≤ 8KB；列表类工具必须支持 `limit` 并有默认值。

## 4. 依赖其它能力

- 取行情：`ctx.Data`（`contract.DataService`），**不要**直接 `http.Get`；
- 调模型：`ctx.LLM`；要模型管理能力：`ctx.Services.Get("llm")` → `*llm.Service`；
- 调其它插件的工具：`ctx.ToolHost.Tools()` / `ctx.ToolHost.CallTool(...)`；
- 用回测：`ctx.Services.Get("quant.engine")`；
- 用知识库：`ctx.Services.Get("kb.store")`。

插件之间**不要 import**；跨插件只走服务名（`docs/API.md` 第 9 节有服务与 bucket 归属表）。

## 5. 安全红线

```go
if !c.Guard.AllowHost(host) { return err }       // 出网前必须校验
if err := c.Guard.CheckPath(path); err != nil {  // 落盘前必须校验
	return err
}
c.Audit.Record(contract.AuditEvent{Plugin: c.ID, Kind: "tool", Action: name, OK: true})
```

- 密钥只能通过 `*llm.Service` 存取，插件自己不要碰明文；
- 抓取内容进模型前标注来源与时间，视为不可信数据；
- 不实现任何下单/交易接口。

## 6. 生命周期与失败语义

- `Activate` 失败只影响本插件与依赖它的插件，kernel 继续跑（`PluginView.Error` 里能看到原因）；
- 工具跨插件重名会让**后激活的插件整体回滚**（不会留下半套工具）；
- `Deactivate` 用 `plugin.Base` 的空实现即可，除非你启动了 goroutine/定时器（要自己停）；
- 面板的 `Order` 决定导航顺序：行情 10/20/30、研究 40/45/50、设置 90。

## 7. 测试写法

```go
k, _ := kernel.New(kernel.Options{Store: fakeStore, Data: fakeData, Guard: fakeGuard, Audit: fakeAudit})
_ = k.Register(New())
_ = k.Start(ctx)
out, err := k.Invoke(ctx, "hello", "ping", nil)        // 方法
json, err := k.CallTool(ctx, "hello.tool", argsJSON)  // 工具
```

参考现成实现：`plugins/modelsettings`（方法+面板）、`plugins/quant`（工具+服务+面板）。
