# 观澜 · Stock Agent 架构

> 定位：面向 A 股的**研究型 harness agent** 桌面应用。盘前出计划、盘中盯盘、盘后复盘、量化验证，
> 全过程可复现、可审计、**只读不下单**。

## 1. 一句话架构

**插件优先的 kernel + 双层 Router/Worker agent + 三级渐进式披露 + 声明式 Watchdog + Wails 桌面端。**

\`\`\`
┌──────────────────────────── frontend (Vue3 + TS, Wails) ────────────────────────────┐
│ 插件导航(动态) │ 面板区(由 UI slot 渲染) │ Agent 工作台(流式事件) │ 设置/审计          │
└───────────────────────────────▲────────────────────────────────────────────────────┘
                                │ Bind: Plugins() / Invoke(plugin, method, json)
┌───────────────────────────────┴─────────────── kernel ──────────────────────────────┐
│ Registry · 拓扑激活 · Service Bus · Tool Registry · UI Slots · Method Router         │
│ Event Bus · Audit · Guard(网络白名单/路径/脱敏)                                       │
└───▲──────────────▲───────────────▲──────────────────▲──────────────────▲────────────┘
    │              │               │                  │                  │
 plugins/watchlist plugins/fundflow plugins/kline  plugins/watch     plugins/agentlab
 (同花顺自选)      (个股/板块/概念)  (K线+指标)       (Watchdog)        (盘前/复盘/量化)
    │              │               │                  │                  │
    └──────────────┴───── internal/datasource ─────────┴──────────────────┘
                    同花顺(10jqka) · 腾讯(gtimg) · 新浪(sina) · 缓存+限速
\`\`\`

## 2. 分层与依赖方向（只允许向下）

| 层 | 包 | 职责 | 禁止 |
|---|---|---|---|
| 契约 | \`pkg/contract\` | 跨层 DTO 与 Service 接口（Data/Store/LLM/Guard/Audit） | 依赖任何上层 |
| 契约 | \`pkg/plugin\` | Manifest / Plugin / Context / Tool / UI / Method / Bus | 依赖 kernel |
| 运行时 | \`internal/kernel\` | 注册、拓扑激活、聚合、调用路由、审计 | 依赖具体插件 |
| 能力 | \`internal/agent\` \`internal/progressive\` \`internal/llm\` \`internal/security\` \`internal/watch\` | harness、披露、模型、安全、Watchdog | 依赖 frontend |
| 数据 | \`internal/datasource\` \`internal/market\` \`internal/store\` \`internal/backtest\` | 行情/资金流/指标/持久化/回测 | 依赖 agent |
| 插件 | \`plugins/*\` | 具体功能（工具 + 面板 + 方法 + 服务） | 互相 import（只经 Service） |

## 3. 插件模型（对齐 dsh「插件 = 工具 + UI slot + 服务」，对齐 superagent「能力进 Worker」）

\`\`\`go
type Plugin interface {
    Manifest() Manifest              // id/name/version/dependsOn/tags
    Activate(*plugin.Context) error  // 注册 tools / ui / methods / services
    Deactivate(context.Context) error
}
\`\`\`

一个插件能做四件事，全部通过 \`Context\`：

| 能力 | API | 消费者 |
|---|---|---|
| 工具 | \`ctx.Tools.Add(Tool{Name,Summary,Schema,Risk,Run})\` | LLM（经渐进式网关） |
| 面板 | \`ctx.UI.Add(UISlot{ID,Title,Component,Group,Order})\` | 前端动态路由 |
| 方法 | \`ctx.Methods.Add(name, fn)\` | 前端 \`Invoke(pluginID,method,payload)\` |
| 服务 | \`ctx.Services.Register(name, impl)\` | 其它插件（无 import） |

**新增功能 = 新增 \`plugins/<x>\` 包（init 里 \`all.Register(New)\`）+ 在 \`plugins/builtin\` 加一行 blank import。**
kernel 与前端侧边栏都不用改。详细步骤见 [PLUGIN-DEV.md](PLUGIN-DEV.md)。

## 4. Agent harness：双层 Router / Worker（superagent 借鉴）

\`\`\`
Trigger(用户输入 / 定时 / 市场事件)
        │
   ┌────▼─────┐   Fast Path: 规则过滤(交易时段、重复、限频)
   │  Router  │   Slow Path: 轻量模型判意图 → 选 Worker / 合并批量
   └────┬─────┘
        │ Task{worker, goal, constraints}
   ┌────▼─────────────────────────────────────────┐
   │ Worker(独立 Session)                          │
   │  premarket / intraday / postmarket / quant    │
   │  ReAct(Eino Graph): model → tool gateway → …  │
   │  记忆: 工作态 / 事件态 / 长期知识(RAG)          │
   └────┬─────────────────────────────────────────┘
        │ Report + 记忆回写 + 审计
   ┌────▼─────┐
   │  Router  │ 对外统一回复 / 推送到前端事件流
   └──────────┘
\`\`\`

- Router 不直接读行情、不挂业务工具（只挂 \`tool.*\`、\`kb.*\`、\`skill.*\`）。
- Worker 上下文隔离，工具由渐进式网关按需展开；报告经 Router 汇总。
- Session 是**只追加事件日志**（sqlite \`sessions\`/\`session_events\`），可重放、可审计。
- Watchdog：定时心跳 + 任务超时（5 分钟）+ 失败重试一次；恢复后从日志重建状态。

### 4.1 会话插话（steering：step-boundary 注入）

运行中的会话收到用户新消息时，**不另起会话、不丢弃**，而是入队并在**下一次模型调用前**并入上下文：

\`\`\`
用户消息(运行中) ──► Runtime.Steer(sessionId,text) ──► 队列(每会话 ≤5 条)
                                                      │
ReAct loop:  model 调用前 drain ──► append user "[用户插话] …" ──► 继续原任务
                                                      │
                                   session 事件(kind=user) + 前端 agent.steer
\`\`\`

同一 sessionId 在运行时再调 Run 会返回可操作错误（提示改用 steer 或先 stop），避免并发写同一会话。
插话只追加约束，不重置任务目标，也不增加步数预算。


## 4.2 自动值班（盘前 / 盘中 / 复盘）

Agent 工作台不再要求用户先发消息：每个 worker 有一份可编辑的**值班模板**（system prompt 的
行为描述部分；三级披露协议仍由代码统一追加，用户改不掉）。调度器随插件 Activate 启动、
20s tick，按 Asia/Shanghai 的工作日近似时间窗触发：

| 阶段 | 默认 | 语义 |
|---|---|---|
| premarket | 09:15 一次 | 开盘前出当日计划；at 型有 90 分钟补跑宽限 |
| intraday | 交易时段每 30 分钟 | 09:30–11:30、13:00–15:00，频率可调 15/30/60 |
| postmarket | 15:10 一次 | 收盘复盘，沉淀知识库 |

约束：当天去重、全局单飞、用户手动运行优先、无可用模型只记 `lastStatus` 不触发；
自动任务走 `execute` 路径，绕开 Router 的 60s 去重（手动运行仍去重）。节假日暂按工作日近似，
UI 明确标注可能误触发。模板与计划分别落在 `agent_templates` / `agent_schedule`。

## 4.3 上下文用量（对齐 dsh ContextMeter）

每次模型调用前按同一口径估算并广播 `agent.context`：

- 非 ASCII 1 token/字，ASCII 4 字符/token，向上取整；
- 分 `systemTokens / toolsTokens / messageTokens`，tools 只算 L1 索引（渐进披露的常驻部分）；
- `contextWindow` 来自模型预设（`contract.ModelInfo.ContextWindow`）；未知为 0 时前端只显示
  「~已用 N」而不画百分比环。

前端「上下文」标签页对齐 dsh-context 的上下文洞察页：KPI 行 + system/tools/messages 组成环形图
+ 逐轮趋势 + 事件流 + 口径说明；标题栏另有一枚可点开的小环（`ContextMeter`）。运行中由事件
驱动，结束后可用 `agentlab.context {sessionId}` 回读；`sessionId` 为空时后端**回退最近一次会话**，
所以空闲状态（还没选中会话）也能渲染快照，而不是报错。

## 4.4 双工与子智能体编队（对齐 superagent 双工 / dsh subagent）

主控（RouterAgent）是**前台**：拆解、派发、持续播报进度、最后汇总；重活交给后台 worker。
默认走双工（`agent.spawn` 的 `background:true`）：

```
RouterAgent(前台 ReAct)                     Worker(后台子会话)
  │ tool.call agent.spawn ──► 立即返回「已派发」
  │ 模型继续输出「派了什么/等什么」─► agent.delta / agent.progress（前端播报）
  │ …                                        │ 完整 ReAct：model → tools → …
  │ 步骤边界 drain（已完成子结果）◄──────────┘ 终态进 childHub
  │ 想结束时仍有 pending → 图进 wait 节点等结果
  │ 收到【子智能体完成】→ 汇报要点 → 汇总
```

- 子会话是一等会话（`sessions` 元信息带 `parentId/mode/label`），有独立事件流与 token 统计；
- 只允许主会话派生，子会话再派生直接返回中文错误；单会话最多 2 个并发的后台后代；
- 子 ctx 派生自父 ctx，父取消/停止级联取消；同步模式（`background:false`）仍保留，报告截断 4000 字；
- `agent.child` 驱动「编队」树，`agent.progress` 驱动前台播报；**子智能体的 delta/state/report
  不进入父会话转写**（前端按 sessionId 过滤），否则前台会被后台输出串台、`running` 被提前置为完成；
- 会话第一条事件固定是 `system`（完整系统提示词），轨迹页可展开审计「模型到底收到了什么口径」。

树由 `agentlab.tree {sessionId}` 按最近 100 个会话的 `parentId` 组装，兼容没有这些字段的旧会话；
上下文/编队标签页不再收起左侧会话列表（切换页不丢会话上下文）。

## 7.6 设置中心（插件管理）

`plugins/console` 只做展示与开关，不持有内核状态：bootstrap 把 `kernel.Inventory` 注册为服务
`kernel.inventory`，console 类型断言取用。停用插件写入 `settings/plugins.disabled`，重启后
kernel 对停用项 `markSkipped`（清单仍可见），依赖它的插件显示「依赖已停用」。不做运行时热停用，
避免留下悬空的工具/面板贡献。设置中心不能停用自己（`protected`）。

## 5. 渐进式披露（三级，本项目的核心机制）

同一套三段式用在 **tool / skill / rag** 三类资源上：**索引 → 展开 → 取全量**。

| 资源 | L1 常驻（便宜） | L2 按需展开 | L3 执行/取全量 |
|---|---|---|---|
| Tool | 名称 + 一句话 + 标签（受 token 预算裁剪） | \`tool.describe\` 返回完整 JSON Schema + 示例 | \`tool.call\` 执行（带 Guard/审计） |
| Skill | frontmatter 目录（name + description） | \`skill.view\` 返回 SKILL.md 正文 | \`skill.read\` 读引用文件 |
| RAG | 文档标题 + 摘要 + 标签 | \`kb.search\` 命中片段 + id + 分数 | \`kb.open\` 取全文/邻接块 |

设计约束：
- L1 总预算 \`<= 1800 tokens\`，超预算按「域相关性 + 风险等级」裁剪并留 \`tool.search\` 兜底；
- 每次展开都写审计（谁在什么时候看了什么），可回答「模型为什么调这个工具」；
- 展开结果进上下文前做脱敏与截断（单条 \`<= 8KB\`），避免上下文爆掉。

## 6. 数据层（参考 go-stock，按实测可达性取舍）

| 数据 | 来源 | 说明 |
|---|---|---|
| 实时快照 | \`d.10jqka.com.cn/v6/realhead/hs_<code>/defer/last.js\` | JSONP，字段 10=现价（已实测）；并发 ≤4 |
| 日/周/月 K | \`d.10jqka.com.cn/v6/line/hs_<code>/01/last.js\` | 全量历史，一次拉取本地切片；**实际是前复权**，只作不复权链路的最后兜底 |
| 板块/概念 K | \`d.10jqka.com.cn/v6/line/bk_<code>/01/last.js\` | THS 板块码 881xxx |
| 前/后复权 K | 腾讯 \`web.ifzq.gtimg.cn/appstock/app/fqkline/get\`（param 末位 \`qfq\` / \`hfq\`） | 响应键 \`qfqday\`/\`hfqday\`（周/月同构）；取不到即 \`ErrAdjustUnsupported\`，绝不拿前复权冒充后复权 |
| 不复权 K | 腾讯原始序列 → 新浪日线 → 同花顺 | 三级兜底，走到 THS 那一步带 \`warning\` |
| 当日分时 | 腾讯 \`appstock/app/minute/query\` → 新浪 1 分钟 K 线 | 腾讯行是「时间 价格 累计量 累计额」，必须差分还原分钟量；新浪只保留最后一根所在日期 |
| 个股资金流 | 新浪 \`MoneyFlow.ssi_ssfx_flzjtj\`（当日）/ \`ssl_qsfx_zjlrqs\`（多日） | 当日：\`r0~r3\` 是成交额，净额 = \`rN_in − rN_out\`，主力 = \`r0_net + r1_net\`；多日：上游只给 \`r0_net\`，所以多日的「主力」是超大单净额（更窄，r1~r3 恒 0） |
| 板块/概念资金榜 | 新浪 \`MoneyFlow.ssl_bkzj_bk\`（行业 \`fenlei=0\` / 概念 \`fenlei=1\`，\`sort=netamount\`） | \`mainNet\` = \`netamount\`，是板块**全部资金净额**而非主力口径；板块代码为新浪 category（如 \`new_dzxx\`） |
| 板块/概念多日资金流 | 新浪 \`MoneyFlow.ssl_bkzj_zjlrqs\`（\`bankuai=<category>\`） | 无 \`r0_net\` 的板块用 \`netamount\` 兜底，返回项带 \`source\` |
| 涨跌家数 | 新浪全市场快照并发扫描 → 同花顺 \`ths-index\` | 降级口径只覆盖单市场、家数偏少，\`source\` 与 \`warnings\` 会标出来 |
| 涨停/跌停/炸板、连板梯队 | 同花顺 \`dataapi/limit_up/limit_up_pool\` | 只有这一家；不拿别的源冒充，避免涨停数/炸板数/梯队三块口径打架 |
| 龙虎榜 | 同花顺 \`ifmarket/lhbtable/report/<date>\` | GBK HTML 表格解析，不是结构化 JSON；当日无数据回退最近交易日 |
| 人气热榜 | 同花顺 \`dq.10jqka.com.cn/fuyao/hot_list_data\` | 小时榜/日榜；现价用 Quote 批量补，失败只少价格列 |
| 兜底快照 | \`qt.gtimg.cn\` / \`hq.sinajs.cn\` | GBK，需转码（手动设 Accept-Encoding 会拿到压缩字节） |
| 新闻/公告 | 新浪滚动 \`feed.mix.sina.com.cn/api/roll/get\`（\`lid=2516\` 要闻 / \`lid=2517\` 公告位） | Watchdog 的 \`news\` 触发源；\`notice\` 已降级为「滚动新闻 + 关键词/个股过滤」，不是交易所公告全文 |
| 同花顺自选 | ① 本机 macOS 容器 \`blockstock_*.dat\` ② 网页端 cookie ③ 手工导入 | 三级降级，任一可用即可 |

约束：全部经 \`internal/datasource\`，统一缓存（快照 3s / K线 60s / 资金流 30s）、
限速（默认 4 QPS）、超时（8s）、重试（2 次指数退避）、失败返回部分结果而不是整体失败。
**复权诚实**：拿不到真复权序列时返回 \`ErrAdjustUnsupported\`，插件降级为 \`adjust=none\` + 角标，绝不冒充。

## 7. 安全边界（硬约束）

1. **只读研究**：不实现任何券商/交易下单接口；所有输出带「研究用途，不构成投资建议」。
2. **密钥**：仅存本地（AES-GCM，密钥文件 \`0600\`，绑定机器 id）；日志/审计/前端展示全量脱敏。
3. **网络**：域名白名单（`10jqka.com.cn`、`gtimg.cn`、`sinajs.cn`、`sina.com.cn`、`sina.cn`、`hexun.com`、`deepseek.com`、`openai.com`），禁止任意 URL；已下线数据源（东方财富）的主机在读取旧策略时会被自动剔除，避免历史配置把接口接回来。无 shell、无任意文件读写工具。
4. **注入**：抓取内容一律标记 \`untrusted\`，模型输出不得直接驱动文件写入或网络请求。
5. **路径**：所有落盘限制在 \`~/.guanlan/\`；路径穿越校验。
6. **审计**：工具调用、模型决策、披露展开、配置变更全部 append-only 记录，可导出。
7. **限流**：外部接口令牌桶（默认 4 QPS）+ 每分钟请求上限（`MaxReqPerMin`=120）+ 单次任务工具调用上限（`MaxToolCalls`=30）。**Token 默认不设预算上限**：`Policy.MaxTokensPerRun=0` 表示不限制，停机只由工具调用步数与模型单次输出上限决定；需要成本闸门时才在策略里显式写 >0 的值。
8. **不执行任意代码**：Watchdog 规则是声明式 DSL（见 7.5），Agent 只能"生成规则"，不能"生成脚本"。

## 7.5 Watchdog：用户自定义盯盘（声明式，不执行任意脚本）

superagent 的 Watchdog 是「主动推进」，我们的落地方式是**声明式规则**：

\`\`\`
用户/Agent 写规则(JSON)  →  文件即插件(~/.guanlan/watches/*.json, 热加载)
        │
   每 30s tick：静默时段/冷却/限频/交易时段过滤
        │
   取数 → 条件求值(安全 DSL，只读白名单指标) → 命中
        │
   派一次 Agent 任务(worker + 渲染后的 prompt) → 报告落日志 + 事件推送前端
\`\`\`

设计取舍（为什么不是「Agent 写脚本执行」）：

| 方案 | 风险 | 结论 |
|---|---|---|
| Agent 生成 Go/shell 脚本执行 | 任意代码执行、越权访问密钥/网络、无法审计 | **禁止** |
| 声明式条件 + 指标白名单 | 只能读数值，无副作用，规则可静态校验 | **采用** |
| 文件即插件 | 用户可手写规则、可版本管理、可热加载、可单独开关 | **采用** |

触发源：行情快照、板块/概念资金流、K 线指标、新闻/公告关键词、定时。
条件 DSL 只允许数字/布尔/比较/逻辑/括号与白名单指标；未知指标在保存时拒绝；
动作只会「给 Agent 派一次带模板的任务」，不直接下单、不写任意文件。
限流：单规则冷却 ≥30s、每规则 ≤6 次/小时、全局并发 ≤1、仅交易时段触发（市场类源）。

## 8. 目录

\`\`\`
pkg/contract/       跨层契约       pkg/plugin/     插件契约
internal/kernel/    插件运行时     internal/bootstrap/  组装与装配
internal/agent/     harness        internal/progressive/ 三级披露
internal/llm/       Eino 模型适配   internal/security/    Guard/脱敏/审计
internal/watch/     Watchdog 规则/DSL/调度
internal/datasource/ 行情数据源     internal/market/     指标与领域
internal/store/     sqlite         internal/backtest/   回测与策略
plugins/            功能插件       frontend/            Wails 前端
content/            技能与知识库    docs/                设计文档
test/ scripts/      集成测试/验收脚本
\`\`\`