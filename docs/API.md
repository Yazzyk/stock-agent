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

## 6. 插件 knowledge（面板组件 `knowledge`）

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
| `audit` | security |

## 10. 面板 / 导航

UI slot 的 `component` 与前端 `src/panels/<name>.vue` 文件名一致；
group 取值：`行情`（watchlist/fundflow/kline）、`研究`（agentlab/quant/knowledge）、`设置`（modelsettings）。
order：watchlist 10、fundflow 20、kline 30、agentlab 40、quant 45、knowledge 50、modelsettings 90。
