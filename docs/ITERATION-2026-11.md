# 观澜 · 第二轮体验与能力迭代（2026-11）

> 本文件是本轮 10 项优化的**唯一契约**：API 形状、文件归属、验收口径以这里为准。
> 任何偏离先在本文件修正，再改代码。作者：lead。

## 0. 总体目标

把「能用」升级为「顺手」：数据同步自助化、行情面板对齐 go-stock 的信息密度、
Agent 从"用户发消息"变成"按提示词自动值班"、把 harness 的上下文与编队可视化，
最后统一视觉语言。

**硬约束**

- 不引新的 npm 依赖，不新增 Go 依赖；图表继续用零依赖 canvas。
- 不接交易接口；密钥只写不读；所有结论仍只读研究。
- 现有公开方法/工具名不破坏；新增优先。
- 前端演示模式（无 Wails 注入）必须可用：新面板要有 mock。
- 中文 UI 文案；金额单位自动换算（元/万/亿）。

## 1. 文件归属（写作用域，互不越界）

| 成员 | 可写 | 只读 |
|---|---|---|
| lead | `frontend/src/style.css` `App.vue` `main.ts` `types.ts` `api.ts` `api.mock.ts` `lib/format.ts` `lib/canvas.ts` `lib/data.ts` `mocks/registry.ts` `components/{HealthDots,PanelHost,SearchBox,StateBlock,ToastHost}.vue` `pkg/contract/**` `docs/**` `README.md` `scripts/**` `test/**` | 其它 |
| backend-agent | `internal/agent/**` `plugins/agentlab/**` | 其它 |
| backend-data | `internal/datasource/**` `plugins/watchlist/**` `plugins/fundflow/**` `internal/market/**` | 其它 |
| backend-platform | `internal/llm/**` `plugins/modelsettings/**` `plugins/console/**`（新建） `internal/bootstrap/**` `internal/kernel/**` `plugins/builtin/**` `internal/headless/**` | 其它 |
| frontend-market | `frontend/src/panels/{watchlist,fundflow,kline,quant}.vue` `frontend/src/lib/chart.ts`（新建） `frontend/src/lib/api.market.ts`（新建） `frontend/src/components/{StockPicker,Sparkline}.vue`（新建） `frontend/src/mocks/market.ts`（新建） | 其它 |
| frontend-agent | `frontend/src/panels/{agentlab,modelsettings,console}.vue` `frontend/src/components/{AgentDrawer,ContextMeter,Formation,AppModal}.vue` `frontend/src/stores/agent.ts` `frontend/src/lib/api.agent.ts` `frontend/src/lib/api.settings.ts` `frontend/src/mocks/{agent,settings}.ts` | 其它 |

**红线**：不改别人的包；不改 `go.mod/go.sum`/`package.json`；不跑 `wails build`；不 git commit。
新增类型放在自己的 `api.*.ts` 里，不要往 `types.ts` 塞。

## 2. 共享前端设施（lead 负责，成员直接用）

### 2.1 mock 注册表

`frontend/src/mocks/registry.ts`：

```ts
export interface MockContext {
  sleep(ms: number): Promise<void>
  emit(topic: string, payload: unknown): void
}
export type MockHandler = (payload: Record<string, unknown>, ctx: MockContext) => unknown | Promise<unknown>
export function registerMock(plugin: string, method: string, fn: MockHandler): void
// api.mock 内部调用：
export function dispatchMock(plugin: string, method: string, payload: Record<string, unknown>): Promise<{ hit: boolean; value?: unknown }>
```

成员在自己的 `mocks/<domain>.ts` 里 `registerMock('agentlab','templates', ...)`；
lead 在 `api.mock.ts` 顶部 import 一次并让 `Invoke` 优先 `dispatchMock`。

### 2.2 样式与类名

沿用现有类：`.card .card-hd .card-title .card-sub .card-tools .btn .btn-sm .btn-accent .btn-ghost
.tag .tabs .tab .tbl .field .label .input .select .textarea .muted .tiny .num .mono .row .col
.grow .spacer .ellipsis .dot .hint-warn .kbd .md`。

新增全局类（lead 在 style.css 实现，成员可用）：

- `.panel` 内容卡片；`.panel-hd` / `.panel-bd` / `.panel-ft`
- `.metric` `.metric-label` `.metric-value`（指标块，支持 `.up/.down/.accent`）
- `.chip`（可选中的小标签）、`.seg`+`.seg-item`（分段控件）
- `.sheet` `.sheet-hd` `.sheet-bd` `.sheet-ft`（居中模态）、`.scrim`（遮罩）
- `.empty` `.skeleton` `.toolbar` `.table-wrap` `.split`
- `.scroll-x` `.sticky-hd` `.num-lg` `.divider`

### 2.3 全局联动

`stores/app.ts` 已有 `state.focus = {code,name}` 与 `focusStock(code,name)`。
- 自选行「K线」按钮 → `focusStock()`（切到 K 线面板并载入该标的）。
- 自选行「资金」按钮 → 打开资金抽屉（不发全局 focus），或先 focus 再打开抽屉。
- K 线/量化面板监听 `state.focus` 自动切换标的（已有逻辑保持）。

## 3. 各项契约

### 3.1 同步自动化（backend-data + frontend-market）

**目标**：从「失败后让用户自己找设置」变成「自动诊断 → 自动打开登录页/系统设置 → 授权后自动继续」。

`pkg/contract` 新增：

```go
type SyncSource struct {
    ID        string `json:"id"`        // ths-local | ths-web | manual
    Label     string `json:"label"`
    Available bool   `json:"available"`
    State     string `json:"state"`     // ok | denied | no-login | missing | empty
    Detail    string `json:"detail,omitempty"`
    Groups    int    `json:"groups,omitempty"`
    Items     int    `json:"items,omitempty"`
}
type SyncAction struct {
    Kind  string `json:"kind"`   // open-url | open-settings | copy | capture-cookie | paste-cookie | retry-sync | none
    Label string `json:"label"`
    Value string `json:"value,omitempty"`
}
type SyncStep struct {
    ID     string     `json:"id"`
    Title  string     `json:"title"`
    Detail string     `json:"detail"`
    Done   bool       `json:"done"`
    Action SyncAction `json:"action"`
}
type SyncCheck struct {
    Local       SyncSource `json:"local"`
    Web         SyncSource `json:"web"`
    Manual      SyncSource `json:"manual"`
    Recommended string     `json:"recommended"` // ths-local | ths-web | manual | none
    Ready       bool       `json:"ready"`
    Message     string     `json:"message"`
    Steps       []SyncStep `json:"steps"`
    CheckedAt   string     `json:"checkedAt"`
}
// SyncResult 追加字段（不破坏旧字段）
Check *SyncCheck `json:"check,omitempty"`
```

watchlist 插件新增方法：

| method | 入参 | 出参 |
|---|---|---|
| `check` | `{}` | `SyncCheck`（只读诊断，绝不触发同步） |
| `saveCookie` | `{cookie}` | `{ok,message,check}`：先验证（调一次网页接口）再落盘 `<DataDir>/ths_cookie.txt` 0600 |
| `captureCookie` | `{}` | `{ok,source,message,check}`：尝试从本机浏览器读同花顺 cookie（Safari binarycookies 优先，Chrome 尽力），失败给可读原因，不报 500 |
| `privacy` | `{}` | `{settingsUrl,appPath,dataDir,hint}`：`settingsUrl = x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles` |

行为要求：
- `check` 逐项探测本机容器（AuthorizationStatus 用现有 `scanTHSLocal` 的错误分类）、cookie 文件/环境变量、手工文件；给出 `Steps`（4 步，id 稳定）：
  1. `local` 本机同花顺数据（action: open-settings 或 none）
  2. `web-login` 网页登录（action: open-url = 同花顺登录 URL；done = 已探测到登录态）
  3. `web-capture` 读取登录态（action: capture-cookie；done = 已落盘可验证 cookie）
  4. `manual` 手工导入（action: copy 数据目录路径）
- `sync` 成功后 `Check` 回填最新诊断。
- 新增 `sync.auto` 不必要；由前端编排：打开面板 → check → 若 ready 且本地为空 → 自动 sync；若 needWebLogin/needFullDiskAccess → 显示向导步骤，按步骤执行后 2s 轮询 check，ready 自动 sync。

前端（watchlist.vue）要求：
- 顶部「数据状态」条：同花顺客户端 / 网页登录 / 手工文件三枚状态点 + 「一键同步」。
- 打开面板自动执行一次 check；空自选自动 sync，无需用户点按钮。
- 向导卡（条件出现）：每步一行，主按钮执行 action；`open-url` 用 `window.runtime.BrowserOpenURL`（无 Wails 时报"浏览器预览"）；`open-settings` 打开 deep link；`copy` 写剪贴板；`capture-cookie` 调 captureCookie；`paste-cookie` 弹出输入框保存。
- 授权后自动重试：轮询 check（2s，最多 60 次），ready 即自动 sync，成功后 toast + 刷新。

### 3.2 资金流（backend-data + frontend-market）

`pkg/contract` 新增（独立接口，避免改 DataService 造成 stub 连锁）：

```go
type FundFlowPoint struct {
    Date      string  `json:"date"`
    MainNet   float64 `json:"mainNet"`
    SuperNet  float64 `json:"superNet"`
    LargeNet  float64 `json:"largeNet"`
    MediumNet float64 `json:"mediumNet"`
    SmallNet  float64 `json:"smallNet"`
    MainPct   float64 `json:"mainPct,omitempty"`
    Close     float64 `json:"close,omitempty"`
    ChangePct float64 `json:"changePct,omitempty"`
}
type FundFlowHistory struct {
    Code   string          `json:"code"`
    Name   string          `json:"name"`
    Days   int             `json:"days"`
    Source string          `json:"source,omitempty"`
    Points []FundFlowPoint `json:"points"`
}
type FundFlowHistoryService interface {
    FundFlowHistory(ctx context.Context, code string, days int) (FundFlowHistory, error)
    FundFlowSnapshot(ctx context.Context, codes []string) ([]FundFlow, error)
}
```

数据源（**历史方案，已下线，勿再接入**；东方财富已从数据层完全移除，现在走新浪 `MoneyFlow`）：
- 历史：`https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get`，`lmt=days&klt=101&secid=<mkt.code>&fields1=f1,f2,f3,f7&fields2=f51..f65`。
  实测字段：f51 日期, f52 主力, f53 小单, f54 中单, f55 大单, f56 超大单, f57 主力%, f58 小单%, f59 中单%, f60 大单%, f61 超大单%, f62 收盘价, f63 涨跌幅。
- 批量快照：`https://push2.eastmoney.com/api/qt/ulist.np/get?fltt=2&secids=...&fields=f12,f14,f2,f3,f62,f184,f66,f69,f72,f75,f78,f81,f84,f87`
  实测：f62 主力净额, f184 主力%, f66 超大, f69 超大%, f72 大, f75 大%, f78 中, f81 中%, f84 小, f87 小%。
  单次 ≤ 50 个 secid；缓存 60s；失败降级为逐只 `StockFundFlow`。
- 单位约定：接口返回**元**，前端负责换算。

fundflow 插件新增方法：

| method | 入参 | 出参 |
|---|---|---|
| `history` | `{code,days?}` 默认 20，上限 120 | `FundFlowHistory` |
| `batch` | `{codes[]}` 上限 50 | `{items:FundFlow[],date,source}` |

工具：新增 `flow.history`（Tag flow，Summary ≤40 字），L3 可在 Agent 里调用。

前端（fundflow.vue + watchlist.vue）：
- fundflow 面板改成三段：
  1. 标的资金卡：`StockPicker` + 主力/超大/大/中/小五项指标 + 多日净流入图（柱=当日主力净额，线=累计净额，线=收盘价，双轴；图例可点击开关；hover 十字 + 读数条；区间 5/10/20/60 日）。
  2. 板块/概念排名：现有表格升级为「净额条形 + 涨跌幅 + 排名」，行点击切换标的资金卡。
  3. 资金动向摘要：连续 N 日净流入/流出、主力与股价背离提示（纯描述，不含建议）。
- 自选面板：
  - 列新增「主力净额」「主力占比」「5日主力」（默认显示前两列，列设置可关）。
  - 行 hover 出现「K线 / 资金」小按钮；点击「资金」右侧滑出 420px 抽屉，内嵌标的资金卡（复用组件/逻辑）。
  - 行点击仍是 focus（K 线联动），但不再弹 toast 干扰。
  - 表格加迷你走势 sparkline（可选列，默认开）。

### 3.3 K 线（frontend-market）

参考 go-stock：lightweight-charts 的交互与信息密度，但继续用 canvas 自绘。

要求：
- 顶部：`StockPicker`（可搜索自选 + 全市场）、周期段控件（日/周/月/60分）、指标开关（MA/BOLL/成交量）、「最新」、全屏（面板内最大化）。
- 主图：蜡烛 + 影线；MA5/10/20/60 可开关；BOLL 可开关；crosshair 竖线 + 右轴价格标签 + 顶部 OHLC 读数条（开高低收/涨跌幅/量额/振幅）。
- 副图：成交量（红绿）+ 可选 MACD 副图（切到 MACD 时成交量变窄条）。
- 缩放/平移：滚轮以鼠标位置为中心缩放、拖拽平移、双击复位、键盘 ←/→ 平移、+/- 缩放。
- 自选快捷条：K 线上方横向 chips（来自 watchlist.list，最多 20 只），点击切换。
- 右侧研判栏可折叠（宽度 300，窄屏自动收起）。
- 从自选跳转：`state.focus` 变化立即加载并高亮；加载期间显示 skeleton，不闪空白。
- 空态/错误态可操作（重试、切标的）。

`frontend/src/lib/chart.ts`：抽成可复用的类/函数：
```ts
export interface CandleChartOptions { ... }
export class CandleChart { constructor(canvas, opts); setData(bars, inds); setVisible(n, offset); draw(); destroy(); }
export function drawFlowChart(ctx, points, opts) // 资金流双轴图
export function drawSparkline(ctx, values, opts)
```
K 线与量化共用。

### 3.4 Agent 工作台：自动值班 + 可编辑提示词（backend-agent + frontend-agent）

**目标**：盘前/盘中/复盘由调度器按提示词自动触发，用户不发言也能出报告；提示词可改、可恢复默认、可单独开关。

数据（存 `contract.Store`，bucket `agent_templates` / `agent_schedule`）：

```ts
interface AgentTemplate {
  id: 'premarket'|'intraday'|'postmarket'|'quant'|'chat'
  title: string; description: string
  prompt: string; defaultPrompt: string; edited: boolean
  enabled: boolean
  schedule: PhaseSchedule
}
interface PhaseSchedule {
  phase: 'premarket'|'intraday'|'postmarket'
  enabled: boolean
  at?: string          // "09:15" / "15:10"
  everyMinutes?: number // 盘中 30
  window?: string       // "09:30-11:30,13:00-15:00"
  nextRunAt?: string; lastRunAt?: string; lastStatus?: string; lastSessionId?: string
}
```

方法：
- `templates` → `{templates:AgentTemplate[]}`
- `saveTemplate` `{id,prompt,enabled?}` → 列表（prompt trim 后 20..8000 字，空则报中文错误）
- `resetTemplate` `{id}` → 列表
- `schedule` → `{phases:PhaseSchedule[]}`
- `setSchedule` `{phase,enabled?,at?,everyMinutes?}` → `{phases}`
- `context` `{sessionId}` → `AgentContext`（见 3.5）
- `tree` `{sessionId}` → `{root:AgentNode,nodes:AgentNode[]}`（见 3.6）

调度：
- 插件 Activate 时启动 20s tick 的调度协程；Deactivate 停止。
- 触发条件（Asia/Shanghai，周一至周五；节假日以"数据源返回最新交易日"为准，暂用工作日近似并在 UI 注明）：
  - premarket：`at` 到达且当天未跑 → 用模板 prompt 作为 system 追加、input 为空，触发 worker。
  - intraday：交易时段内每 `everyMinutes` 分钟一次（去重窗口不足时跳过）。
  - postmarket：`at` 到达且当天未跑。
- 未配置可用模型时：不触发，`lastStatus="未配置模型"`，事件里说明，不刷错误。
- 触发前发 `agent.schedule` 事件 `{phase,nextRunAt,running,reason}`；跑完更新 lastRunAt/lastStatus。
- 同一时刻只允许一个自动任务；用户手动运行优先，自动任务顺延。
- 模板 prompt 拼接：`spec.System = template.prompt + "\n\n" + disclosureProtocol`；用户手动运行仍用当前模板。

前端（agentlab.vue）：
- 顶部三张「值班卡」：盘前 / 盘中 / 复盘。每张显示：开关、下次运行倒计时、上次结果（成功/失败/未配置模型）、「立即运行」、「编辑提示词」。
- 编辑提示词：模态 `.sheet`，textarea + 字数 + 「恢复默认」+「保存」；保存后立即生效。
- 「盘中」额外显示频率选择（15/30/60 分钟）。
- 下方保留：运行日志（流式）、报告、历史会话；新增「编队」标签（见 3.6）。
- 运行中显示上下文环（见 3.5），点击展开明细。
- 演示模式：mock 提供模板/调度/上下文/编队数据；定时器用固定 nextRunAt。

### 3.5 上下文可视化（backend-agent + frontend-agent）

`AgentContext`：

```ts
interface AgentContext {
  sessionId: string
  model: string
  contextWindow: number      // 来自模型预设；未知则 0
  usedTokens: number
  percent: number            // 0..100
  breakdown: { systemTokens: number; toolsTokens: number; messageTokens: number }
  messages: number
  steps: number
  toolCalls: number
  updatedAt: string
  note?: string              // "估算口径：中文≈1字/1token，英文≈4字符/1token"
}
```

- 运行中每步（模型调用前）更新并发 `agent.context` 事件；结束后 `context` 方法可回读（由 session events 重建 + 模型窗口）。
- 估算器：CJK 按 1 token/字，ASCII 按 4 字符/token，向上取整；system/tools/messages 分项。
- `toolsTokens`：L1 工具索引（名称+summary+tags）的估算；不把全部 schema 算进去（渐进披露）。
- 前端 `ContextMeter.vue`：参考 DSH——14px 圆环 + 百分比，点击弹层：
  - 顶部 `~已用 / 窗口`；
  - 分段条（system/tools/messages 三色）；
  - 三行图例 + token 数；
  - 底部一句口径说明。
  - 无 contextWindow 时只显示 `~已用`，不显示百分比环。

### 3.6 多智能体 / 子智能体可视化（backend-agent + frontend-agent）

数据：

```ts
interface AgentNode {
  sessionId: string; parentId?: string
  label: string; mode: 'main'|'subagent'
  worker: string; status: 'running'|'ok'|'failed'
  tokens: number; steps: number
  startedAt: string; endedAt?: string; elapsedMs: number
}
```

- `SessionMeta` 增 `parentId/mode/label`（omitempty，兼容旧数据）。
- 新工具 `agent.spawn` `{task,worker?,label?}`：
  - 仅主会话可调用；子会话内部调用直接报 "子智能体不能再次派生"。
  - 子会话用 `chat`（或指定 worker）worker + 任务文本跑一次完整 ReAct；完成后把**报告前 4000 字**作为工具结果返回。
  - 最多同时 2 个后代；父会话取消时级联取消子会话。
  - 失败不抛出到父流程，返回中文错误字符串供模型决定下一步。
- 事件：`agent.child` `{parentSessionId,childSessionId,label,status,tokens,elapsedMs,error?}`（started/running/finished 至少三种）。
- `tree` 返回当前会话的祖先链 + 后代树（从 sessions bucket 扫最近 100 条，按 parentId 组装）。

前端：
- agentlab 新增「编队」标签：左侧 worker 花名册（5 个角色 + 当前状态），中间谱系树（父→子，连线，状态点、token、耗时），右侧选中节点详情（worker、步骤、报告摘要、打开会话）。
- 运行中实时更新（`agent.child` 事件）；子节点点击可打开该子会话日志（复用 openSession）。
- 顶部显示「本次编排：1 个主会话 / N 个子智能体」。
- 演示模式：mock 返回一棵 1 主 + 2 子的示例树。

### 3.7 量化研究（frontend-market + backend-data 只读）

- 代码输入换成 `StockPicker`：输入即搜（自选优先 + market.search 防抖 250ms），↑↓ 选择、Enter 确认、Esc 关闭；显示代码/名称/市场；最近使用 5 个。
- 图表区改为上下两段：
  1. 标的 K 线（复用 CandleChart，带买入/卖出标记——若回测结果没有逐笔成交，则至少显示区间价格走势与均线，并在图例说明）。
  2. 净值曲线（现有逻辑修正：canvas 尺寸跟随容器、DPR、图例不再溢出、hover 读数、初始基准虚线、回撤阴影可选）。
- 修复当前绘制异常：容器高度/图例高度耦合、ResizeObserver 未覆盖、结果区 `v-else` 下 ref 晚挂载。
- 回测结果缺逐笔成交时：lead 会让 backend-data 在 `quant.backtest` 返回里补 `trades[]`（见 3.7.1）；前端有则画标记，无则降级。

#### 3.7.1 回测成交明细（backend-data）
- `backtest.Result.Trades`（json `trades`，平仓交易旧形状）**保持不变**；
- 新增 `backtest.Result.Fills`（json `fills`，逐笔 `{date,side:'buy'|'sell',price,qty,reason?}`，最多 500 条，超出截断）；
- quant run 响应**顶层**新增 `trades` = 同一份 fills，供前端画买卖标记；旧的 `result.trades` 不动。

### 3.8 模型设置：预设下拉 + 官方默认（backend-platform + frontend-agent）

`internal/llm` 新增：

```go
type ModelPreset struct {
    Model, Label string
    ContextWindow, MaxOutput, DefaultMaxTokens int
    Note string
}
type ProviderPreset struct {
    Provider, Label, BaseURL, KeyURL, DocsURL string
    Models []ModelPreset
    Note string
}
```

`modelsettings.presets` → `{providers: ProviderPreset[]}`。内置（以 2026-11 官方文档为准，UI 注明可覆盖）：

| provider | baseUrl | 模型（context / maxOutput / 默认maxTokens） |
|---|---|---|
| deepseek | https://api.deepseek.com | deepseek-flash (1M / 384K / 8192)、deepseek-v4-pro (1M / 384K / 8192)；旧名 deepseek-chat 保留但标注 legacy |
| openai | https://api.openai.com/v1 | gpt-5 (400K / 128K / 16384)、gpt-5-mini (400K / 128K / 16384) |
| qwen | https://dashscope.aliyuncs.com/compatible-mode/v1 | qwen3-max (256K / 32K / 8192)、qwen-plus (128K / 16K / 4096)、qwen-turbo (1M / 16K / 4096) |
| ark | https://ark.cn-beijing.volces.com/api/v3 | doubao-seed-2-1-pro (1M / 256K / 8192)、doubao-seed-2-1-turbo (256K / 256K / 8192) |
| ollama | http://127.0.0.1:11434/v1 | qwen3:8b (32K / 8K / 2048)、llama3.1:8b (128K / 8K / 2048) |
| openai-compat | 自定义 | 无预设，手填 |

`contract.ModelInfo` 增 `ContextWindow int \`json:"contextWindow,omitempty"\``；保存时若 MaxTokens 缺省则按模型预设填 `DefaultMaxTokens`。
`llm.defaultModelName` 更新：deepseek→`deepseek-flash`，ark→`doubao-seed-2-1-pro`，openai→`gpt-5-mini`。

前端 modelsettings.vue：
- 「新增配置」→ 先选 provider（卡片/下拉），再选模型（下拉，显示 context/maxOutput），baseUrl/maxTokens 自动填；
- maxTokens 输入旁显示「官方默认 X / 上限 Y」；高级折叠才允许改 baseUrl；
- 「去申请 API Key」链接（KeyURL）、「官方文档」链接（DocsURL）；
- 已有配置的编辑同上；legacy 模型名在列表里带「旧版」标签。
- 保存后列表卡片显示 context 窗口。

### 3.9 设置中心 / 插件管理（backend-platform + frontend-agent）

新插件 `plugins/console`（ID `console`，无依赖，UI slot：`{id:"console",title:"设置中心",group:"系统",component:"console",order:95}`）。

后端：
- bootstrap 注册服务 `kernel.inventory`，实现：
  ```go
  type Inventory interface {
      Plugins() []kernel.PluginView
      Status() kernel.StatusView
      Tools() []kernel.ToolView
      Disabled() []string
      SetEnabled(id string, enabled bool) error   // 持久化到 settings/plugins.disabled
      RestartRequired() bool
  }
  ```
- kernel 增 `SetDisabled([]string)`：Start 时对禁用插件 `markSkipped(id,"已停用（可在设置中心启用，重启生效）")`；PluginView 保持可见。
- 设置保存后返回 `{ok,restartRequired:true,message:"已保存，重启应用后生效"}`。
- console 方法：
  - `inventory` → `{plugins,status,tools,disabled,restartRequired}`
  - `setEnabled` `{id,enabled}`
  - `policy` → `contract.Policy`（脱敏后）
- console 前端：
  - 标签：插件 / 工具 / 审计 / 安全策略 / 数据源
  - 插件卡片：名称、版本、ID、状态（已激活/已停用/失败+原因）、依赖、标签、工具数/方法数/面板数、启用开关、失败原因展开。
  - 顶部统计：已安装 N / 已激活 M / 工具 X / 面板 Y / 服务 Z。
  - 工具页：搜索 + 风险标签 + 来源插件。
  - 审计页：最近 100 条（时间/插件/动作/耗时/结果），失败高亮。
  - 策略页：网络白名单、路径根、限流、工具调用上限、是否允许 exec（只读展示）。
  - 数据源页：健康状态（来自 Status.dataHealth）+ 同步入口跳转。
- 演示模式：mock 提供与真实一致形状的数据（含 console 自身）。

### 3.10 视觉刷新（lead + 全员）

设计方向：**深色研究终端 2.0**——更强的层次、克制的琥珀强调、等宽数字、左侧图标导航、卡片化信息块。

- 色板：背景 `#080b10`；面板 `#0e141d` / `#131b26` / `#18222f`；线 `rgba(148,163,184,.14)`；主文字 `#e8eef7`；次 `#a9b6c8`；弱 `#6f7d90`；涨 `#ff5a5f`；跌 `#22c08a`；强调 `#e9b44c`；信息 `#5b9dff`。
- 圆角 10/12；阴影只用于浮层；过渡 120–160ms `ease-out`。
- 导航：分组标题 + 图标 + 激活胶囊（左侧 2px 强调条），折叠态仅图标。
- 表格：表头 sticky、行 hover 高亮、数字 tabular-nums、涨跌色只用文字/小面积。
- 空态/加载：统一 `.skeleton` 呼吸、空态给主操作。
- 所有面板最小宽度 1024；<1200 时右栏折叠。
- 图表统一网格/坐标轴/读数条样式（chart.ts 内置常量）。

## 4. 验收

每个成员交付前必须：

```sh
go build ./... && go vet ./...
go test ./internal/<你的包>/... ./plugins/<你的插件>/... -count=1
cd frontend && npx vue-tsc --noEmit
```

（前端成员还要确认 `npm run dev` 下自己的面板无 console 报错；lead 统一跑 `scripts/verify.sh` 与 wails build。）

**完成定义**：
1. 本轮新增 API 全部有方法/工具/事件，形状与本文一致；
2. 前端无 TypeScript 错误，演示模式下面板可渲染；
3. 至少 1 条针对新逻辑的 Go 单测（调度去重、资金流解析、预设默认值、sync check 分支、上下文估算、子智能体边界）；
4. 旧测试全绿；
5. 回报：改的文件、跑的命令与真实输出、缺口、需要 lead 集成的事项。
