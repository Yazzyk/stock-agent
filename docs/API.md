# 接口冻结合同（v1）

前端 `Invoke(pluginID, method, payloadJSON)` 的 payload 与返回必须是这里定义的形状。
Go 侧结构体 tag 用 camelCase，与本文件字段名逐字一致。**任何一侧要改，先改本文件并通知 lead。**

## 0. 全局约定

- 时间：`RFC3339` 字符串（例 `2026-11-10T15:00:00+08:00`）；交易日 `YYYY-MM-DD`。
- 金额：元；成交量：手；涨跌幅：百分数（`2.34` 表示 +2.34%）。
- 分页：一律 `limit`（默认 50，上限 200）。
- 错误：Wails 方法返回 Go error → 前端 catch；错误文案中文、可操作。
- 事件：`kernel:event` → `{topic, time, payload}`（payload 是 JSON 字符串）。

## 1. 插件 watchlist（面板组件 `watchlist`）

| method | 入参 | 返回 |
|---|---|---|
| `list` | `{}` | `{items: WatchItem[], groups: string[], updatedAt: string, source: string}` |
| `sync` | `{}` | `SyncResult` |
| `add` | `{code: string, name?: string, group?: string}` | `{items: WatchItem[]}` |
| `remove` | `{code: string}` | `{items: WatchItem[]}` |
| `groups` | `{}` | `{groups: string[]}` |

工具：`watch.list`、`watch.sync`、`market.quote {codes: string[]}` → `Quote[]`、`market.search {keyword, limit?}` → `StockRef[]`。

## 2. 插件 fundflow（面板组件 `fundflow`）

| method | 入参 | 返回 |
|---|---|---|
| `stock` | `{code: string}` | `FundFlow` |
| `rank` | `{kind: "sector"\|"concept", limit?: number}` | `FundFlow[]` |
| `sectors` | `{kind: "sector"\|"concept"}` | `Sector[]` |

工具：`flow.stock {code}`、`flow.rank {kind, limit}`、`sector.list {kind}`。

## 3. 插件 kline（面板组件 `kline`）

| method | 入参 | 返回 |
|---|---|---|
| `bars` | `{code: string, period?: "day"\|"week"\|"month"\|"min60", limit?: number（默认 120，上限 250）, adjust?: ""\|"qfq"\|"hfq"}` | `{code, name, period, bars: Bar[], indicators: {ma5: number[], ma10: number[], ma20: number[], ma60: number[], volume: number[]}}`（数组与 bars 等长，缺口为 null） |
| `analyze` | `{code: string, period?: string}` | `{code, name, period, adjust?: string, warning?: string, last: Bar, summary: {trend: string, ma: string, rsi: string, macd: string, atr: string}, patterns: [{name, index, confidence, note}], support: number[], resistance: number[]}` |

工具：`market.bars {code, period, limit, adjust}` 返回 `Bar[]`；`kline.analyze {code, period}` 返回同上。

## 4. 插件 modelsettings（面板组件 `modelsettings`）

| method | 入参 | 返回 |
|---|---|---|
| `list` | `{}` | `{providers: ModelInfo[], defaultId: string}` |
| `save` | `{id?: string, provider: string, name?: string, baseUrl?: string, model: string, apiKey?: string, maxTokens?: number, setDefault?: boolean}` | `{providers: ModelInfo[], defaultId: string}` |
| `test` | `{id?: string}` | `{ok: boolean, latencyMs: number, message: string}` |
| `remove` | `{id: string}` | `{providers: ModelInfo[]}` |

`apiKey` 只写不读；`ModelInfo.hasKey` 表示是否已配置。**任何返回都不得包含明文 key。**

## 5. 插件 agentlab（面板组件 `agentlab`）

| method | 入参 | 返回 |
|---|---|---|
| `workers` | `{}` | `[{id, title, description}]`（id: premarket/intraday/postmarket/quant/chat） |
| `run` | `{worker: string, input?: string, sessionId?: string}` | `{sessionId, worker, report, steps, usage: Usage, error?: string, artifacts?: [{name, path}]}` |
| `stop` | `{sessionId: string}` | `{ok: boolean}` |
| `sessions` | `{limit?: number}` | `[{id, worker, title, startedAt, status: "ok"\|"failed"\|"running", tokens}]` |
| `session` | `{sessionId: string}` | `{session, events: [{seq, time, kind, role?, content, toolName?, ok?}]}` |

流式事件（Subscribe `kernel:event`）：
- `agent.delta` → `{sessionId, text}`
- `agent.state` → `{sessionId, state: "thinking"\|"tool"\|"done"\|"error", detail?}`
- `agent.report` → `{sessionId, worker, report, usage}`
工具：`agent.run {worker, input}`、`agent.status {}`，
以及渐进式披露元工具（agentlab 注册，供 ReAct 使用）：`tool.search {query, limit?}`、`tool.describe {name}`、`tool.call {name, args}`。

## 5.5 插件 quant（面板组件 `quant`）

| method | 入参 | 返回 |
|---|---|---|
| `strategies` | `{}` | `{strategies: [{id, name, desc, params: [{key,label,type,min,max,step}], default}]}` |
| `run` | `{code, period?, strategy?, params?, limit?, initialCash?, adjust?}` | `{code, period, adjust: "qfq"\|"none", warning?, result: Result, report: string}` |
| `walkforward` | `{code, strategy?, folds?, limit?}` | `{result: {folds: [...], isReturn, oosReturn, overfitGap, verdict}, report: string}` |

`Result` 字段：`{strategy, code, start, end, bars, initialCash, finalEquity, totalReturn, cagr, maxDrawdown, sharpe, sortino, winRate, turnover, tradeCount, trades: Trade[], equity: [{date,equity,cash,position,shares}], costs: {commission,stampTax,transfer,slippage}, warnings}`。
工具：`quant.backtest {code, strategy?, params?}` → 摘要 + report（不含 equity 数组）。
服务：`quant.engine`（供 agentlab 的 quant worker 调用）。
UI slot：id=quant、component=quant、title=量化研究、group=研究、order=45。

## 5.6 插件 watch（面板组件 \`watch\`，Watch 哨兵）

规则结构：

\`\`\`json
{
  "id": "w1a2b3c4d5e6",
  "name": "半导体资金异动",
  "enabled": true,
  "sources": [
    {"type": "sector", "sector": "半导体", "kind": "sector"},
    {"type": "quote", "codes": ["603278", "688981"]},
    {"type": "kline", "codes": ["600000"], "period": "day"},
    {"type": "news", "keyword": "并购重组", "kind": "notice"},
    {"type": "schedule", "times": ["09:20", "14:50"]}
  ],
  "condition": "sector.mainNetYi > 20 and quote.changePct > 3",
  "action": {"worker": "intraday", "prompt": "板块 {sector} 主力净流入 {sector.mainNetYi} 亿元，{code} 涨 {quote.changePct}%。请判断持续性。"},
  "cooldownSeconds": 600,
  "quietWindows": ["11:30-13:00"],
  "lastError": "",
  "triggerCount": 3
}
\`\`\`

| method | 入参 | 返回 |
|---|---|---|
| \`list\` | \`{}\` | \`{rules: Rule[], running: boolean, metrics: [{name,label,unit}], logs: RunLog[]}\` |
| \`save\` | \`Rule\`（id 空=新建） | \`{rule: Rule, rules: Rule[]}\` |
| \`toggle\` | \`{id, enabled}\` | \`{rule: Rule}\` |
| \`remove\` | \`{id}\` | \`{rules: Rule[]}\` |
| \`test\` | \`{id}\` | \`{ruleId, matched, scenarios: [{code?,name?,sector?,metrics,matched}], summary, prompt, error?, elapsedMs}\` |
| \`logs\` | \`{limit}\` | \`{logs: RunLog[]}\` |
| \`templates\` | \`{}\` | \`{templates: Rule[], metrics: []}\` |
| \`metrics\` | \`{}\` | \`{metrics: [{name,label,unit}]}\` |
| \`state\` | \`{}\` | \`{running, rules}\` |

RunLog：\`{time, ruleId, ruleName, worker, triggered, matched, summary, metrics, report, error, elapsedMs}\`。

**条件 DSL（安全边界）**：只支持数字、布尔、比较（\`> >= < <= == !=\`）、逻辑（\`and/&&\`、\`or/||\`、\`not/!\`）与括号；
标识符只能取白名单指标（\`watch.metrics\` 返回），**不支持函数调用、下标、属性链之外的访问、字符串、网络或任何副作用**；
未知指标在保存时即被拒绝。规则里没有可执行代码，这是"Agent 可以生成规则但不能生成脚本"的边界。

指标清单（与 \`internal/watch/expr.go\` 同源）：
quote.price / quote.changePct / quote.volumeRatio / quote.turnover / quote.amount；
flow.mainNet（元）/ flow.mainNetYi（亿）/ flow.mainNetPct（净额÷成交额）；
sector.changePct / sector.mainNet / sector.mainNetYi；
kline.rsi / kline.ma20dev / kline.volumeRatio / kline.macd / kline.aboveMA20 / kline.atrPct；
news.hits / news.negative；time.trading / time.hour / time.minute。

工具：\`watchdog.list\`、\`watchdog.create\`（Risk=write，Agent 用它把用户需求写成规则）、\`watchdog.toggle\`（write）、\`watchdog.test\`（network）、\`watchdog.logs\`。
服务：\`watch.engine\`。
UI slot：id=watch、component=watch、title="Watch 哨兵"、group=研究、order=35。
规则文件：\`<DataDir>/watches/<id>.json\`（改文件即热加载，相当于一个可开关的插件）。

## 6. 插件 knowledge（面板组件 \`knowledge\`）

| method | 入参 | 返回 |
|---|---|---|
| `list` | `{limit?: number}` | `{docs: [{id, title, summary, tags: string[], updatedAt}], total: number}` |
| `search` | `{query: string, limit?: number}` | `{hits: [{id, docId, title, snippet, score}]}` |
| `open` | `{id: string}` | `{id, title, content, tags: string[], sections?: [{heading, content}]}` |
| `import` | `{title?: string, content: string, tags?: string[]}` | `{id, title, chars: number}` |
| `skills` | `{}` | `{skills: [{name, description, tags: string[]}]}` |
| `skill` | `{name: string}` | `{name, description, content, resources: string[]}` |

工具：`kb.search {query, limit}`、`kb.open {id}`、`kb.list {limit}`、`kb.import {title?, content, tags?}`（Risk=write，只写本地 kb_* bucket）、`skill.list {}`、`skill.view {name}`、`skill.read {name, path}`。

## 7. 前端可直接调用的内核方法（app.go，Wails 绑定）

`Plugins() []PluginView`、`Status() StatusView`、`Tools() []ToolView`、`Audit(limit) []AuditRow`、
`Invoke(pluginID, method, payloadJSON) (string, error)`、`CallTool(name, argsJSON) (string, error)`。

## 8. 构造函数签名（bootstrap 集成，不得改）

```go
store.Open(path string) (*store.DB, error)                    // *DB 实现 contract.Store
security.NewGuard(policy contract.Policy, roots []string) contract.Guard
security.NewAudit(store contract.Store) contract.Audit
datasource.New(cfg datasource.Config) (contract.DataService, error)
llm.New(cfg llm.Config) (contract.LLMService, error)
// 每个插件包：func New() plugin.Plugin
```

`datasource.Config{Guard contract.Guard, Store contract.Store, Logger *slog.Logger, DataDir string, HTTPTimeout time.Duration}`
`llm.Config{Store contract.Store, Guard contract.Guard, Logger *slog.Logger, DataDir string}`

## 9. Store bucket 命名（禁止越界写别人的 bucket）

| bucket | owner |
|---|---|
| `settings` | lead / modelsettings（键：`llm`、`policy`、`ui`） |
| `plugin_settings` | kernel |
| `watchlist` | watchlist 插件 + datasource（键布局两边共用：`item/<市场+代码>` 存条目 JSON，`meta` 存 updatedAt/source/groups；改布局必须两边同步） |
| `market_cache` | datasource |
| `kb_docs` `kb_chunks` `kb_meta` | knowledge / progressive |
| `sessions` `session_events` | agent |
| `watch_rules` `watch_runs` | watch（规则与触发日志） |
| `audit` | security |

## 10. 面板 / 导航

UI slot 的 `component` 与前端 `src/panels/<name>.vue` 文件名一致；
group 取值：`行情`（watchlist/fundflow/kline）、`研究`（watch/agentlab/quant/knowledge）、`设置`（modelsettings）。
order：watchlist 10、fundflow 20、kline 30、watch 35、agentlab 40、quant 45、knowledge 50、modelsettings 90。
