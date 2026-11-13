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
| `check` | `{}` | `SyncCheck`：只读诊断本机同花顺 / 网页登录态 / 手工文件，返回 `{local,web,manual,recommended,ready,message,steps,checkedAt}`；**不发网络请求** |
| `saveCookie` | `{cookie: string}` | `{ok, message, check: SyncCheck}`：先用该 cookie 拉一次网页自选，验证通过才落盘 `<DataDir>/ths_cookie.txt`（0600） |
| `captureCookie` | `{}` | `{ok, source, message, check}`：尝试只读解析 Safari Cookies.binarycookies；Chrome/Edge 因钥匙串加密返回 `ok=false` + "请粘贴" |
| `privacy` | `{}` | `{settingsUrl, appPath, dataDir, hint}`：`settingsUrl` 为 macOS 完全磁盘访问深链 |

`SyncResult` 追加 `check?: SyncCheck`（同步完成后回填）。
`SyncStep` 固定 4 步：`local` / `web-login` / `web-capture` / `manual`；`action.kind` ∈ `open-url | open-settings | copy | capture-cookie | paste-cookie | retry-sync | none`。
工具：`watch.list`、`watch.sync`、`watch.check`、`market.quote {codes: string[]}` → `Quote[]`、`market.search {keyword, limit?}` → `StockRef[]`。

## 2. 插件 fundflow（面板组件 `fundflow`）

| method | 入参 | 返回 |
|---|---|---|
| `stock` | `{code: string}` | `FundFlow` |
| `rank` | `{kind: "sector"\|"concept", limit?: number, direction?: "in"\|"out"\|"all"}` | `FundFlow[]`；`in` 净流入榜（默认）、`out` 净流出榜、`all` 按净额降序。`mainNet` 是板块全部资金净额（新浪 `netamount`），不是主力口径 |
| `sectors` | `{kind: "sector"\|"concept"}` | `Sector[]` |
| `history` | `{code: string, days?: number}`（默认 20，上限 120） | `{code,name,days,source?,points:[{date,mainNet,superNet,largeNet,mediumNet,smallNet,mainPct?,close?,changePct?}]}`（个股） |
| `sectorHistory` | `{code: string, days?: number}`（新浪板块 category，如 `new_dzxx`；默认 20，上限 120） | 同 `history`；走新浪 `MoneyFlow.ssl_bkzj_zjlrqs`（`bankuai=<category>`），用于板块/概念多日资金流 |
| `sectorBatch` | `{codes: string[], days?: number}`（≤20；新浪 category 如 `new_dzxx`，与 `sectorHistory` 同一套归一规则：1~40 位小写字母/数字/下划线） | `{items: FundFlowHistory[]}`：并发 ≤4，部分失败只少返回对应项，全失败才报错 |
| `batch` | `{codes: string[]}`（≤50） | `{items: FundFlow[], date, source}`：命中快照缓存先返回，其余逐只并发取（新浪 MoneyFlow）；部分失败只少返回对应项，不整体报错 |

`history`（个股多日）走新浪 `ssl_qsfx_zjlrqs`：上游只给 `r0_net` / `netamount` / `turnover` / `changeratio`，所以 `mainNet` 是**超大单净额**、`mainPct` 用 `r0_ratio`，`largeNet` / `mediumNet` / `smallNet` 恒为 0；只有当日的 `stock` / `batch` 才是「超大 + 大」的主力口径。两者不要画在同一条曲线上比较。

工具：`flow.stock {code}`、`flow.rank {kind, limit, direction?}`、`sector.list {kind}`、`flow.history {code, days?}`、`sector.history {code, days?}`。

排名口径：新浪 `MoneyFlow.ssl_bkzj_bk`（`sort=netamount&asc=0`，行业 `fenlei=0` / 概念 `fenlei=1`）一次取回全量榜单，服务层按方向本地重排后编号 `Rank=1..N`。`mainNet` 是 `netamount`（板块**全部资金净额**，不是主力口径），`mainPct` 是 `ratioamount×100`，`changePct` 是 `avg_changeratio×100`（缺失时缺省 0）。金额单位元、百分数为百分号数值。榜单缓存 60s，key 含 `kind`；另存 `category→名称` 5 分钟，供多日接口回填图例。

链路：板块榜与板块多日资金流都只有新浪一条链路（`Source=sina`），没有第二家兜底。入参就是新浪 category（如 `new_dzxx`），非法字符直接报错而不是静默改写；`sectorHistory` 走 `ssl_bkzj_zjlrqs`，保证“点榜单行就能画线”，某些板块上游没有 `r0_net` 时用 `netamount` 兜底，返回项的 `source` 说明实际口径。新浪不可用时该块返回明确错误，前端保留上次数据并标注，涨跌幅/占比整列隐藏。

## 3. 插件 kline（面板组件 `kline`）

| method | 入参 | 返回 |
|---|---|---|
| `bars` | `{code: string, period?: "day"\|"week"\|"month"\|"min60", limit?: number（默认 120，上限 250）, adjust?: ""\|"qfq"\|"hfq"}` | `{code, name, period, adjust: "qfq"\|"none", warning?: string, bars: Bar[], indicators: {ma5: number[], ma10: number[], ma20: number[], ma60: number[], volume: number[]}}`（数组与 bars 等长，缺口为 null） |
| `analyze` | `{code: string, period?: string}` | `{code, name, period, adjust?: string, warning?: string, last: Bar, summary: {trend, ma, rsi, macd, atr}, patterns: [{name, index, confidence, note}], support: number[], resistance: number[], horizons: Horizon[]}` |
| `minute` | `{code: string}` | `{code, name, date, prevClose, points: [{time, price, avg?, volume?, amount?}], source}`：当日分时；腾讯 `appstock/app/minute/query` 主源（累计量额差分、截断 15:00 后盘后行），新浪 1 分钟 K 线降级（只保留最后一根所在日期），缓存 60s |

`Horizon` = `{key: "short"\|"long", title, window: number, bias: "偏多"\|"偏空"\|"中性", summary: {trend, ma, rsi, macd, atr}, patterns, support, resistance}`。短线取最近 60 根（MA5/10/20 + RSI + MACD + ATR），长线取最近 250 根（MA20/60/120 + 长周期 MACD + 窗口回撤）；不足 120 根会明确提示样本不足。顶层 `summary/patterns/support/resistance` 保留，兼容旧调用方。

工具：`market.bars {code, period, limit, adjust}` 返回 `Bar[]`；`kline.analyze {code, period}` 返回同上；`market.minute {code}` 返回当日分时序列。
复权口径：`qfq` 与 `hfq` 都由腾讯 `fqkline` 提供（`qfqday` / `hfqday`），取不到时返回 `ErrAdjustUnsupported` 并提示改用不复权；`none` 走腾讯原始 → 新浪日线 → 同花顺兜底（THS 的 v6 line 实际是前复权，仅最后兜底，走到它时带 `warning`）。绝不用前复权冒充后复权。

## 4. 插件 modelsettings（面板组件 `modelsettings`）

| method | 入参 | 返回 |
|---|---|---|
| `list` | `{}` | `{providers: ModelInfo[], defaultId: string}` |
| `save` | `{id?: string, provider: string, name?: string, baseUrl?: string, model: string, apiKey?: string, maxTokens?: number, setDefault?: boolean}` | `{providers: ModelInfo[], defaultId: string}` |
| `test` | `{id?: string}` | `{ok: boolean, latencyMs: number, message: string}` |
| `setDefault` | `{id: string}` | `{providers: ModelInfo[], defaultId: string}`：只切换默认指向，不改该配置其他字段 |
| `remove` | `{id: string}` | `{providers: ModelInfo[]}` |
| `presets` | `{}` | `{providers: [{provider,label,baseUrl,keyUrl?,docsUrl?,authMode?,note?,models:[...]}]}`（内置官方预设，无网络） |
| `workbuddyStatus` | `{}` | `{loggedIn, accountName?, domain?, expiresAt?, vaultPath?, message?}`（只读本地凭证库，不返回 token） |
| `workbuddyModels` | `{}` | `{models: [{id,name,contextWindow,maxOutputTokens,creditHint?,supportsImages?,description?,isImageModel?}]}` |
| `workbuddyLogin` | `{region?: "cn"\|"global"}` | `{loginId, url, expiresIn?}`（浏览器打开 `url`，随后轮询） |
| `workbuddyLoginPoll` | `{loginId}` | `{ok, message?, accountName?}`；`ok=false` 且 `message` 为空表示仍在等待 |
| `workbuddyImport` | `{}` | `{message}`（从 dsh / go-stock 凭证库导入一次） |
| `workbuddyLogout` | `{}` | `{message}`（只清本应用凭证） |

`apiKey` 只写不读；`ModelInfo.hasKey` 表示是否已配置。**任何返回都不得包含明文 key。**

Max Tokens 口径：命中官方预设且 `maxOutput>0` 的模型，前端直接显示并只读为官方最大输出；只有「自定义模型」才允许手填。后端 `DefaultMaxTokensFor` 对命中预设的模型返回 `maxOutput`，`save` 显式传入的 `maxTokens` 仍优先。
`ModelInfo` 追加 `contextWindow?: number`。保存时 `maxTokens` 缺省按该模型预设 `defaultMaxTokens` 填充。
预设（2026-11 官方文档）：DeepSeek `deepseek-flash`/`deepseek-v4-pro`（1M 上下文 / 384K 输出 / 默认 8192），旧名 `deepseek-chat` 标 legacy；OpenAI `gpt-5`/`gpt-5-mini`；通义 `qwen3-max`/`qwen-plus`/`qwen-turbo`；方舟 `doubao-seed-2-1-pro`/`-turbo`；Ollama；OpenAI 兼容自定义；**OpenCode 兼容网关**（`baseUrl=https://opencode.ai/inference/openai/v1`，authMode=opencode，需 API Key，模型 `qwen3.8-max`/`deepseek-v4.1-flash`/`kimi-k2.5`/`glm-5.3` 等）；**WorkBuddy**（authMode=workbuddy，无需 API Key，使用本机登录态）。

`ModelInfo` 追加 `authMode?: "apiKey"|"workbuddy"|"opencode"`、`accountName?`、`expiresAt?`。WorkBuddy 的 `hasKey` 以本机登录态是否有效为准；OpenCode 按普通 API Key 处理。
WorkBuddy 凭证落盘在 `<DataDir>/workbuddy-vault/<cn|global>/<accountId>.json`（目录 0700、文件 0600、原子写），读取优先级：本地库 → dsh（`$DSH_HOME/workbuddy-vault`、`~/.dsh/workbuddy-vault`）→ go-stock；token 只在内存拼请求头，不写日志、不回传前端。

## 5. 插件 agentlab（面板组件 `agentlab`）

| method | 入参 | 返回 |
|---|---|---|
| `workers` | `{}` | `[{id, title, description, custom?, model?}]`（内置：router/premarket/intraday/postmarket/quant/chat；其余为用户/RouterAgent 创建的自定义 worker） |
| `run` | `{worker: string, input?: string, sessionId?: string, model?: string}` | `{sessionId, worker, report, steps, steers?, usage, model?, error?, artifacts?}`；worker 为空/`router` 时走 RouterAgent，model 为空时依次回落到模板 model、LLM 默认模型 |
| `send` | `{worker, input, sessionId, model?}` | 会话运行中自动转插话（回执"已插话"），空闲时等价 `run` |
| `stop` | `{sessionId: string}` | `{ok: boolean}` |
| `steer` | `{sessionId: string, text: string}` | `{ok: boolean, queued: number, sessionId}`（运行中插话：文本在下一次模型调用前并入上下文；会话已结束/队列满/空文本返回中文错误） |
| `sessions` | `{limit?: number}` | `[{id, worker, title, startedAt, status: "ok"\|"failed"\|"running", tokens}]` |
| `session` | `{sessionId: string}` | `{session, events: [{seq, time, kind, role?, content, toolName?, ok?}]}`；`kind` ∈ `system`（会话第一条，完整系统提示词）/`user`/`assistant`/`tool`/`state`/`error`/`report` |
| `templates` | `{}` | `{templates: AgentTemplate[]}`：`{id,title,description,prompt,defaultPrompt,edited,enabled,schedule}` |
| `saveTemplate` | `{id, prompt, enabled?}` | `{templates: AgentTemplate[]}`（prompt 20~8000 字） |
| `resetTemplate` | `{id}` | `{templates: AgentTemplate[]}` |
| `schedule` | `{}` | `{phases: PhaseSchedule[]}`：`{phase,enabled,at?,everyMinutes?,window?,nextRunAt?,lastRunAt?,lastStatus?,lastSessionId?}` |
| `setSchedule` | `{phase, enabled?, at?, everyMinutes?, window?}` | `{phases: PhaseSchedule[]}`；`at` 仅盘前/复盘，`everyMinutes`/`window` 仅盘中（window 形如 `09:30-11:30,13:00-15:00`，最多两段） |
| `context` | `{sessionId}` | `{sessionId,model,contextWindow,usedTokens,percent,breakdown:{systemTokens,toolsTokens,messageTokens},messages,steps,toolCalls,updatedAt,note?,pressureTokens?,sample?,cache?,usage?,history?}`。`usedTokens` 仍是启发式（system+tools+messages）；`pressureTokens` 是**最近一次请求实际发送的 prompt**（提供方上报，`sample="provider"` 时占用率用它，`sample="estimated"` 时退回 `usedTokens`）；`cache` = `{uncachedInputTokens,cacheReadTokens,outputTokens,hitPercent}`（会话累计，未上报时全 0）；`usage` = `{promptTokens,cachedTokens,completionTokens,totalTokens}`；`history` = 逐次模型调用采样 `[{step,promptTokens,cachedTokens,completionTokens,used,at}]`，每会话最多 120 点、按 step 升序，**只保存在进程内**（重启后旧会话为空）。`sessionId` 允许为空：**回退到最近一次会话**的上下文（前端首次打开上下文页/上下文环时还没有选中会话，直接调用就该拿到可渲染结果；连一次会话都没有时返回中文错误「先发一条消息」）。与 `tree` 的兜底一致 |
| `tree` | `{sessionId}` | `{root: AgentNode, nodes: AgentNode[]}`：`{sessionId,parentId?,label,mode:"main"\|"subagent",worker,status,tokens,steps,startedAt,endedAt?,elapsedMs}` |
| `models` | `{}` | `{models: [{id, model, displayName?, isDefault, hasKey}], defaultId}`：id 为 `provider/model`，不含 key |
| `createWorker` | `{id, title, description, prompt, model?}` | `{workers: WorkerView[]}`：id `^[a-z][a-z0-9_-]{1,31}$`，最多 20 个，内置 id 不可占用 |
| `removeWorker` | `{id}` | `{workers: WorkerView[]}`：只删自定义 worker |
| `saveTemplate` | `{id, prompt, enabled?, model?}` | `{templates}`：内置与自定义 worker 都可保存提示词与默认模型 |
| `setSchedule` | `{phase, enabled?, at?, everyMinutes?, window?}` | `{phases}`：调度触发时使用模板 model；`window` 仅盘中 |

流式事件（Subscribe `kernel:event`）：
- `agent.delta` → `{sessionId, text}`
- `agent.state` → `{sessionId, state: "thinking"\|"tool"\|"waiting"\|"done"\|"stopped"\|"error", detail?}`：`waiting` 表示前台在等后台子智能体结果；`stopped` 表示用户主动停止（**不是失败**，前端按「已停止」展示）。
- `agent.report` → `{sessionId, worker, report, usage}`
- 停机收口：ReAct 因步数上限（`Policy.MaxToolCalls`）或显式设置的 token 预算停机、且最后一条 assistant 没有实质正文时，Runtime 会追加一次**不带工具**的收口调用，按「结论/依据/不确定性/风险提示」生成报告；收口 token 计入总用量但不增加 `steps`。报告尾部会区分「token 预算耗尽」与「步数上限」，并说明是否为收口结果。**Token 默认不设预算上限**（`Policy.MaxTokensPerRun=0`，见 §6.5），所以默认只会因步数或模型单次输出上限停机。
- 空回复诊断：`run` 返回与 `agent.report` 在正文为空时带 `emptyReason`（`no_response` / `reasoning_only` / `truncated` / `content_filter` / `steps` / `budget` / `tool_only` / `error`）与 `diagnostics`（逐条排查线索），另带 `finishReason` 与 `contentFromReasoning`。前端按 `emptyReason` 给不同修复入口，**不解析文案**。
- `agent.steer` → `{sessionId, text, queued}`（插话已入队；前端据此渲染成用户消息）
- `agent.context` → `AgentContext`（每次模型调用前更新；运行结束再补一帧收尾快照，让 `pressureTokens` 反映最后一次请求）
- `agent.tool` → 工具调用轨迹：`{sessionId, worker, id, phase:"start"\|"end", tool, meta, args, result?, ok?, error?, ms?, step, at}`。`tool` 是展示用工具名（`tool.call` 时已解析出内层工具名，如 `market.quote`），`meta` 是元工具名（`tool.search`/`tool.describe`/`tool.call`）；`args`（≤800 字）与 `result`（≤1200 字）已脱敏截断，**不要 `JSON.parse`**；`id` 为 `<sessionId>-<step>`，同一次调用的 start/end 相同，但事件总线**不保证 start 先到**，前端按 `id` 合并；`step` 是本次运行内第几次元工具执行（从 1 起），**不是模型步数**。
- `agent.child` → `{parentSessionId,childSessionId,label,worker,status:"running"\|"ok"\|"failed",phase:"started"\|"finished",tokens,elapsedMs,error?,at,atMs,startedAt,endedAt?}`
- `agent.progress` → `{sessionId, childSessionId, label, worker, status, phase:"started"\|"finished", tokens?, elapsedMs?, at}`：**前台播报**事件（双工模式），前端据此在对话里显示「已派发… / 已完成 · 用时 x」。子智能体的 `agent.delta`/`agent.state`/`agent.report` 不进入父会话转写，只按 `sessionId` 落到它自己的会话。
- `agent.schedule` → 阶段级 `{phase,nextRunAt,running,reason}` 或全局 `{running,reason,phases}`
自动值班：盘前/盘中/复盘按模板提示词自动触发（20s tick、Asia/Shanghai、工作日近似、当天去重、单飞；无可用模型不触发）；自动任务跳过 Router 的 60s 去重，手动运行仍去重。
工具：`agent.run {worker, input, model?}`、`agent.status {}`、`agent.spawn {task, worker, label?, model?, background?}`（仅主会话；子会话不可再派生；最多 2 个后代；**默认 `background:true` 后台派发**：立即返回「已派发」，子结果在父会话的 ReAct 步骤边界以 `【子智能体完成】` 消息回传；`background:false` 时同步等待、返回报告前 4000 字；模型默认继承父会话）、`worker.create {id,title,description,prompt,model?}`、`worker.list {}`，
渐进式披露元工具（agentlab 注册，供 ReAct 使用）：`tool.search {query, limit?}`、`tool.describe {name}`、`tool.call {name, args}`。
控制观澜自身（同一插件注册，Risk=write/read）：`app.method {plugin, method, payload?}` 调**已激活插件已注册**的方法（改值班调度、改模型设置、改盯盘规则、启停插件、导入知识库、看审计），`app.catalog {plugin?}` 列出可用方法。plugin 大小写不敏感、method 必须精确命中，不在目录里的一律拒绝并列出可用方法；受 `Policy.AllowMethodControl`（默认开）约束，`exec` 风险永不放开。

## 5.5 插件 quant（面板组件 `quant`）

| method | 入参 | 返回 |
|---|---|---|
| `strategies` | `{}` | `{strategies: [{id, name, desc, formula?, rules?: string[], params: [{key,label,type,min,max,step}], default}], rules: string[], costs: Cost}`；`rules` 是全局成交/成本/复权/统计口径 |
| `run` | `{code, period?, strategy?, params?, limit?, initialCash?, adjust?}` | `{code, period, adjust: "qfq"\|"none", warning?, result: Result, report: string}` |
| `walkforward` | `{code, strategy?, folds?, limit?}` | `{result: {folds: [...], isReturn, oosReturn, overfitGap, verdict}, report: string}` |
| `optimize` | `{code, strategy?, folds?, limit?}` | `{result: {strategy, code, start, end, candidates: OptimizeCandidate[], best: OptimizeCandidate, walkforward?: WalkForwardResult, report, note?}}` |

`OptimizeCandidate` = `{params, totalReturn, cagr, maxDrawdown, sharpe, tradeCount, stability}`。`optimize` 在 `DefaultGrid` 上全样本评估，`stability` 是参数邻域（每个参数最多变化一格）的夏普均值/本点夏普，推荐参数只在稳健性不低于中位数的候选里按夏普选，并再跑一次 walk-forward。它用于优化参数，不用于选股。

`Result` 字段：`{strategy, code, start, end, bars, initialCash, finalEquity, totalReturn, cagr, maxDrawdown, sharpe, sortino, winRate, turnover, tradeCount, trades: Trade[], fills?: Fill[], equity: [{date,equity,cash,position,shares}], costs: {commission,stampTax,transfer,slippage}, warnings}`。
`trades` 是旧的**平仓交易**（entryDate/exitDate/…）；`fills` 是逐笔成交 `{date, side:"buy"\|"sell", price, qty, reason?}`（≤500，超出截断）。`run` 响应**顶层**另带 `trades` = 同一份 fills，供 K 线画买卖标记。
工具：`quant.backtest {code, strategy?, params?}` → 摘要 + report（不含 equity 数组）；`quant.optimize {code, strategy?, folds?, limit?}` → 候选摘要 + report（不含净值）。
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

## 5.7 插件 marketcenter（面板组件 `marketcenter`，A 股市场中心）

| method | 入参 | 返回 |
|---|---|---|
| `indices` | `{}` | `MarketIndex[]`：上证/深证/科创50/创业板/北证50 |
| `breadth` | `{}` | `{date, up, down, flat, limitUp, limitDown, broken, brokenRate, amount?}` |
| `ladder` | `{limit?: number}`（默认 50，上限 200） | `LimitUpStock[]`：`{code,name,price,changePct,boards,firstTime?,lastTime?,reason?,amount?,turnover?}` |
| `longhu` | `{limit?: number}`（默认 50，上限 200） | `LongHuRow[]`：`{code,name,price,changePct,reason,netBuy,buy,sell,date}` |
| `hot` | `{type?: "hour"\|"day", limit?: number}`（默认 hour/30，上限 100） | `HotStock[]`：`{code,name,market?,price?,changePct,heat,rank,rankChange?,conceptTags?,popularityTag?,reason?}` |
| `snapshot` | `{ladderLimit?, longhuLimit?}` | `{indices, breadth, ladder, longhu, hot, updatedAt, warnings?}`：五块并行取数，单块失败只降级该块（hot 默认小时榜 30 条） |

数据口径：
- 指数：腾讯 `qt.gtimg.cn`，新浪兜底；成交额来自上证+深证。
- 宽度：涨跌家数用新浪全市场快照并发扫描（`source=sina`），整轮失败时降级同花顺指数 realhead 的涨/平/跌三个字段（`source=ths-index`，只覆盖单市场、家数明显偏少，`warnings` 里会说清）；涨停/跌停/炸板用同花顺 `limit_up_pool` 汇总（`limit_up_count.open_num` 即炸板数），炸板率 = 炸板 /（涨停 + 炸板）。
- 涨停梯队：同花顺 `limit_up_pool`（连板数来自 `high_days_value >> 16`）。这一块**没有第二家涨停池**：拿别的源冒充会让涨停数/炸板数/连板梯队三块口径互相打架，所以该块失败时返回空数据 + `warnings`。
- 龙虎榜：同花顺 `ifmarket/lhbtable/report/<date>`（GBK HTML 表格解析），按代码合并上榜原因、按净买入绝对值降序；当日无数据回退最近交易日并标注。
- 热榜：同花顺 `dq.10jqka.com.cn/fuyao/hot_list_data` 人气榜（hour/day），热度/排名变化/题材/人气标签/上榜原因；现价用现有 Quote 批量补，失败只少价格列。
- 缓存：指数 15s，其余 60s；所有块失败只返回该块空数据 + `warnings`。
工具：`market.indices {}`、`market.breadth {}`、`market.ladder {limit?}`、`market.longhu {limit?}`、`market.hot {type?, limit?}`（均 RiskNetwork、只读）。
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


## 6.5 插件 console（面板组件 `console`，设置中心）

| method | 入参 | 返回 |
|---|---|---|
| `inventory` | `{}` | `{plugins: PluginView[], status: StatusView, tools: ToolView[], disabled: string[], restartRequired: boolean, audit: AuditEvent[], protected: string[]}` |
| `setEnabled` | `{id: string, enabled: boolean}` | `{ok, restartRequired, message, disabled: string[]}`（写盘 + 重启生效，不做运行时热停用） |
| `policy` | `{}` | `contract.Policy`（pathRoots 已脱敏为 `~`） |

- `PluginView` 沿用内核视图：`id/name/version/description/author?/dependsOn?/tags?/active/skipped?/disabled?/error?/tools/ui/methods`；`tools/ui/methods` 恒为数组（空为 `[]`）。
- **`emCookieStatus` / `emCookieCapture` / `emCookieSave` 已下线**（随东方财富数据源一起移除，2026-11-12）：三个方法不再注册，前端调用会得到「方法不存在」。不要再接线，也不要为它们补 UI——数据层没有 eastmoney 请求，手动配置 Cookie 只会让人误以为还有东财通道。
- `protected` 目前是 `["console"]`：设置中心不能停用自己，前端把开关置灰。
- 停用配置持久化在 `settings/plugins.disabled`（JSON 数组），启动时由 bootstrap 读入 kernel；被停用插件仍出现在清单里（`skipped=true, disabled=true`），依赖它的插件按「依赖已停用」跳过。
- UI slot：`{id:"console", title:"设置中心", group:"系统", component:"console", order:95}`。
- 服务：`kernel.inventory`（bootstrap 注册，供 console 读取内核清单）。
- 官方金融数据 API（同花顺 fuyao）：`https://fuyao.aicubes.cn`，Key 存 `~/.guanlan/fuyao.key`（0600，或环境变量 `GUANLAN_FUYAO_API_KEY` 覆盖），也可在设置中心「数据源」页配置/测试。**官方优先 + 现有链路兜底**：行情快照（批量，volume 股→手）、日/周/月 K 线（none/forward/backward 三档复权）、涨停池（连板数/封单额/涨停原因）优先走官方；无 Key、限流、上游错误或分钟线时自动降级到腾讯/新浪/网页同花顺。健康信息里来源名为 `fuyao`，设置中心方法为 `fuyao`/`fuyaoSave`/`fuyaoTest`（Key 只写不读）。
- 数据源网络策略：境内行情源（同花顺/腾讯/新浪/官方 fuyao）**直连**，不继承 `HTTP_PROXY`（系统代理常把境内请求转到海外节点，服务端直接 EOF）；IPv4 优先，失败回退系统解析；保留 HTTP/2。**只连白名单主机**：`10jqka.com.cn`、`gtimg.cn`、`sinajs.cn`、`sina.com.cn`、`sina.cn`、`hexun.com`、`aicubes.cn`（官方金融数据 API）、`deepseek.com`、`openai.com`。白名单在读取旧策略时会把已下线数据源的主机（如 `eastmoney.com`）静默剔除，避免历史配置把接口接回来。
- 数据源健康与限流：统一缓存（快照 3s / K 线 60s / 资金流 30s / 板块榜 60s）、默认 4 QPS、超时 8s、重试 2 次指数退避；单块失败返回部分结果 + `warnings`，不整体失败。东方财富登录态（`EastMoneyCookieStatus` 及其 Cookie 抓取链路）已随数据源移除，**不再有东财 Cookie 概念**。

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
| `settings` | lead / modelsettings（键：`llm`、`policy`、`ui`）；bootstrap 另用 `plugins.disabled`（插件启用清单） |
| `plugin_settings` | kernel |
| `watchlist` | watchlist 插件 + datasource（键布局两边共用：`item/<市场+代码>` 存条目 JSON，`meta` 存 updatedAt/source/groups；改布局必须两边同步） |
| `market_cache` | datasource |
| `kb_docs` `kb_chunks` `kb_meta` | knowledge / progressive |
| `sessions` `session_events` | agent（会话元信息与只追加事件；`sessions` 元信息含 `parentId/mode/label`） |
| `agent_templates` | agent（值班提示词：key = worker id） |
| `agent_schedule` | agent（自动值班计划：key = phase） |
| `agent_workers` | agent（自定义 worker 定义：key = worker id） |
| `watch_rules` `watch_runs` | watch（规则与触发日志） |
| `audit` | security |

## 10. 面板 / 导航

UI slot 的 `component` 与前端 `src/panels/<name>.vue` 文件名一致；
group 取值：`行情`（watchlist/marketcenter/fundflow/kline）、`研究`（agentlab/watch/quant/knowledge）、`设置`（modelsettings）、`系统`（console）。
order：**agentlab 1**（Agent 中心化：默认落地页与 RouterAgent 入口）、watchlist 10、marketcenter 15、fundflow 20、kline 30、watch 35、quant 45、knowledge 50、modelsettings 90、console 95。

## 11. 插件 fuyao（同花顺官方数据源，无独立面板）

插件 ID `fuyao`，Manifest 名「数据源 · 同花顺」。数据层实现 `contract.FuyaoService`（可选接口，不扩大 `DataService`）。端点/字段/权限/单位的权威清单见 **`docs/FUYAO-API.md`**；本文只登记方法名与 schema 契约。

### 11.1 方法（`Invoke("fuyao", "<method>", payload)`）

| method | 入参 | 返回 |
|---|---|---|
| `catalog` | `{group?: string, endpoint?: string}` | `{total,count,groups:string[],endpoints:FuyaoEndpoint[],asOf}`（离线，不发请求） |
| `query` | `{endpoint: string, params?: Record<string,string>, limit?: 1..200}` | `FuyaoData`：`{endpoint,path,title,group,timestamp?,count,items?,raw?,truncated?,params?,permission?,note?}` |
| `quote` | `{codes: string[1..50], limit?}` | `{count,items:FuyaoQuoteRow[],asOf,note}`；`volume` 单位股、`turnover` 元 |
| `kline` | `{codes: string[1..10], interval?: "1d"\|"1w"\|"1mo", adjust?: "forward"\|"none"\|"backward", asset?: "stock"\|"index"\|"fund", start?: "YYYY-MM-DD", end?: "YYYY-MM-DD", limit?: 1..2000}` | `{count,series:FuyaoBarsSeries[],asOf,note}` |
| `valuation` | `{codes: string[1..100]}` | `{count,items:{thscode,ticker,name,pe_ttm,pe_mrq,pb_mrq,ps_ttm,pcf_ttm}[],asOf,note}` |
| `limitPool` | `{pool?: "up"\|"down"\|"break", limit?: 1..200}` | `{pool,count,items:FuyaoLimitRow[],asOf,note}` |
| `limitStats` | `{date?: "YYYY-MM-DD"}` | `{data:{trade_date,seal_rate(0~1),open_stock_count,limit_up_stock_count,...},asOf}` |
| `hotRank` | `{kind?: "skyrocket"\|"hot"\|"history", period?: "day"\|"hour", limit?: 1..100}` | `{kind,period,count,items:FuyaoHotRow[],asOf,note}` |
| `special` | `{view:"anomaly"\|"anomaly-stock"\|"abnormal"\|"dragon-tiger"\|"dragon-tiger-institution"\|"department-record"\|"department-history"\|"chip-shape"\|"new-concepts"\|"turnover", codes?, code?, date?, tags?, shape?, departmentId?, limit?}` | `{view,count?,items?/data?,asOf}` |
| `capitalFlow` | `{view?: "snapshot"\|"history"\|"highfreq"\|"intraday", code: string, interval?: "1d"\|"1m", date?, start?, end?}` | 快照 `{view,item:FuyaoCapitalFlowRow}`；历史/高频 `{view,count,items[]}`；2004 返回 `permission=client` |
| `auction` | `{view?: "snapshot"\|"benchmark"\|"opportunities", codes?, stage?: "live"\|"final", date?}` | 快照 `{view,count,items:FuyaoAuctionRow[],asOf,note}` |
| `financials` | `{kind?: "income"\|"balance"\|"cash"\|"indicators", code?, codes?, period?: "annual"\|"quarterly", report?, limit?: 1..20}` | 报表 `{kind,count,items[],asOf,note}`；指标 `{kind:"indicators",data,asOf,note}` |
| `tickerSearch` | `{keyword: string, assetType?: string, limit?: 1..50}` | `{count,items:FuyaoTickerRef[],asOf}` |
| `calendar` | `{start?: "yyyyMMdd", end?: "yyyyMMdd"}` | `{count,items:FuyaoCalendarDay[],asOf,note}` |
| `index` | `{view?: "catalog"\|"constituents"\|"memberships"\|"snapshot"\|"kline", tag?, indexCode?, codes?, limit?}` | 对应 `{view,count,items/series,asOf}` |
| `fund` | `{view: string, thscode?, managerId?, companyId?, params?: Record<string,string>, limit?}` | `FuyaoData`；view 白名单见数据层（profile/snapshot/nav/holdings/manager/quota-summary/...） |
| `futures` | `{view: string, params?: Record<string,string>, limit?}` | `FuyaoData`；view：varieties/plates/contracts/contract/main/main-continuous/secondary/commodity-index/positions-*/companies/basis/basis-history/warehouse/kline/intraday/schedule/session/fundamentals |
| `options` | `{view: string, params?, limit?}` | `FuyaoData`；view：varieties/contracts/contract/kline/intraday/session |
| `news` | `{view?: "flash"\|"flash-refresh"\|"notice"\|"article"\|"topic"\|"stock-timeline"\|"anomaly"\|"events", tagId?, seq?, code?, date?, params?, limit?}` | `FuyaoData` |
| `ipo` | `{view?: "a-ipo"\|"hk-application"\|"hk-calendar"\|"hk-detail", market?, status?, issueCode?, limit?}` | `FuyaoData` |
| `marketDump` | `{kind?: "10y"\|"10d"\|"adjust"}` | `{kind,data:{presigned_url},asOf,note}` |

### 11.2 工具（20 个，均 `Risk=network`）

`fuyao.catalog`、`fuyao.api`、`fuyao.quote`、`fuyao.kline`、`fuyao.valuation`、`fuyao.limitPool`、`fuyao.hotRank`、`fuyao.capitalFlow`、`fuyao.auction`、`fuyao.special`、`fuyao.financials`、`fuyao.tickerSearch`、`fuyao.calendar`、`fuyao.index`、`fuyao.fund`、`fuyao.futures`、`fuyao.options`、`fuyao.news`、`fuyao.ipo`、`fuyao.marketDump`。

工具 schema 约定：`additionalProperties:false`；`codes` 有 minItems/maxItems；`limit` 有上下限；`fuyao.api.params` 是 `Record<string,string>`，键名由端点目录校验（未知参数直接报错）。

### 11.3 事件与降级语义

- 无新增事件 topic；官方降级通过 `slog` 结构化日志 `event=fuyao.fallback` 记录，并计入 `Health()["fuyao"]`。
- `permission=client`（官方 code=2004）不是错误：工具返回空 `items` + `note`，前端应展示「同花顺 AI 客户端专用」。
- `contract.ErrFuyaoPermission` 是数据层的可判定错误；插件工具层已转换为上述降级结果。
