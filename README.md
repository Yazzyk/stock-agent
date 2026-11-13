# 观澜 · Stock Agent

![status](https://img.shields.io/badge/status-WIP-orange)
![go](https://img.shields.io/badge/Go-1.25-00ADD8)
![wails](https://img.shields.io/badge/Wails-v2-DF0000)

> 🚧 **开发中（Work in progress）**：早期版本，接口、数据口径与界面仍在快速变动，暂不保证可用性，也不适合直接用于实盘决策。

面向 A 股的**研究型 harness agent**桌面应用：盘前出计划、盘中盯盘、盘后复盘、量化验证，
全过程可复现、可审计。**只做研究，不接券商下单。**

> 研究用途，不构成投资建议。量化结论必须自带样本内外与成本假设，任何"稳赚"叙事都是错的。

## 它解决什么问题

一个人做 A 股研究，重复劳动集中在四件事：拉数据、算指标、守纪律、写复盘。
这个应用把四件事变成可复用的闭环：

```
数据(同花顺/腾讯/新浪) → 指标与资金流 → Agent 盘前计划 → 盘中盯盘触发 → 盘后复盘 → 记忆回写 → 下一次更准
```

和"AI 选股神器"的区别：**没有预测承诺，只有流程纪律和可检验的结论。**

## 核心能力

| 能力 | 说明 |
|---|---|
| 自选股 | 秒级同步本机同花顺自选（本地容器解析），网页端/手工导入三级降级；**同步向导自动诊断、打开登录页/系统设置、授权后自动续跑**；代码后标 `沪/深/创/科/北` 板块标签；现价与涨跌幅按红涨绿跌**平盘白色**；走势列为**当日分时小图**，可一键打开分时大图 |
| 资金流向 | 板块/概念**多线资金流**（净流入 TOP8 + 净流出 TOP8 的区间累计曲线，可点榜单行增删）；**净流入 / 净流出双榜**（新浪口径 = 板块全部资金净额，不是主力）；个股资金仍在自选行内与资金抽屉查看 |
| K 线分析 | 日/周/月/60 分，MA/BOLL/量能/MACD，形态识别与关键价位；**短线/长线双尺度研判**；自选一键跳 K 线 |
| 市场中心 | 上证/深证/科创50/创业板/北证50、涨跌停与炸板率、**同花顺小时/日热榜**、涨停梯队、龙虎榜；单块数据源失败只降级该块 |
| Agent 工作台 | **只有一个对话入口：RouterAgent**。RouterAgent 拆解任务、分派给任务 worker 或按需新建 worker，最后汇总；任务 worker 是**只读视图**（无输入框），由 RouterAgent 生成与驱动；盘前/盘中/复盘仍可自动值班；界面分**对话**（Pi Agent 风格转写）/ **轨迹**（DSH 风格工具调用账本 + 密度条）/ **报告**（多份不覆盖）/ **上下文** / **编队**；流式输出 + 会话存档 + 插话；**空回复给诊断卡**（内容过滤 / 仅推理 / 截断 / 预算）而不是一句「请重试」 |
| RouterAgent 可控制观澜 | RouterAgent 通过 `app.method` 直接调插件方法：改值班调度、改模型设置、改盯盘规则、启停插件、导入知识库、看审计——不用手点界面 |
| 上下文与编队 | 上下文洞察（精确占用 / 缓存命中 / 组成环 + 堆叠条 / **逐轮台账** / 趋势 / 事件流，参考 dsh-context；空闲也回读快照，后端在 `sessionId` 为空时回退最近会话）；编队**没有子智能体时只显示 RouterAgent 卡**，有子智能体时给花名册 chips + 谱系树 + 节点详情；**Token 不设预算上限**（停机只由工具调用步数与模型单次输出上限决定） |
| 量化回测 | T+1、无未来函数、真实成本、样本内外与 walk-forward；策略公式/规则/成本口径可查；参数优化（邻域稳健性）+ 让 Agent 自主设计下一步实验；K 线买卖标记 + 净值曲线 |
| 知识库 | 本地 RAG（中文 bigram 索引）+ 技能库（10 个 SKILL.md，含从 dsh / openclaw 导入的短线、游资、报告润色技能），Agent 按需检索 |
| 模型设置 | DeepSeek / OpenAI / 通义 / 方舟 / Ollama / OpenAI 兼容 / OpenCode 兼容网关 / WorkBuddy 本机登录态；官方模型自动使用官方最大输出（只读），自定义模型才手填 |
| 设置中心 | 插件安装/激活状态、工具索引、审计、安全策略、数据源健康；启停重启生效 |

## 架构（插件优先）

```
Wails 桌面端 (Vue3 + TS)
   │  Plugins() / Invoke(plugin, method, payload)
   ▼
kernel  注册 · 拓扑激活 · 工具/面板/方法/服务聚合 · 事件总线 · 审计 · 安全网关
   │
   ├── agent harness：Router(轻量判意图) → Worker(隔离上下文 ReAct) → 记忆回写
   │     ├── 自动值班调度（盘前/盘中/复盘，可编辑提示词）
   │     ├── 上下文计量（system/tools/messages）
   │     └── 子智能体派生（agent.spawn → 谱系树）
   ├── 渐进式披露：tool / skill / rag 三级（索引 → 展开 → 执行）
   ├── 数据层：同花顺 · 腾讯 · 新浪（直连 / 单块降级 / 缓存 / 限速）
   └── 插件（10）：watchlist fundflow kline marketcenter agentlab knowledge modelsettings quant watch console
```

**新增一个功能 = 新增一个 `plugins/<name>` 包**（实现 `plugin.Plugin`，在 `Activate` 里注册
工具/面板/方法/服务），再在 `plugins/builtin` 加一行 blank import。kernel 与前端侧边栏都不用改。

设计细节见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)，分工与约定见 [docs/PLAN.md](docs/PLAN.md)。

## 渐进式披露（核心机制）

工具、技能、知识库都按同一套三级展开，避免把几十个工具的完整 schema 一次性塞给模型：

| 资源 | L1 常驻 | L2 按需 | L3 执行 |
|---|---|---|---|
| 工具 | 名称 + 一句话 + 标签（有 token 预算） | `tool.describe` 展开 JSON Schema | `tool.call` 执行（过安全网关 + 审计） |
| 技能 | 目录（name/description） | `skill.view` 正文 | `skill.read` 引用文件 |
| 知识库 | 文档目录 | `kb.search` 命中片段 | `kb.open` 全文 + 邻接块 |

## 快速开始

```sh
# 依赖：Go 1.25+、Node 20+、wails v2.15
go build ./...

# 无界面自检（不需要 GUI，适合排障）
go run . --headless --task=selftest
go run . --headless --task=tools
go run . --headless --task=bars -- 600000 day 60
go run . --headless --task=minute -- 600000
go run . --headless --task=premarket
go run . --headless --task=postmarket

# 桌面端开发（热重载）
wails dev
# 构建 macOS 应用
wails build
```

本地数据在 `~/.guanlan/`（sqlite、密钥、会话、日志），不会写入仓库。

## 数据来源与限制

**全部数据只走同花顺 / 腾讯 / 新浪三家，不依赖东方财富任何接口。**
配置同花顺官方金融数据 API（`fuyao.aicubes.cn`）后，快照 / 日周月 K 线 / 涨停池**优先走官方**，
无 Key、限流或分钟线时自动降级到下面的现有链路（Key 存 `~/.guanlan/fuyao.key` 0600，
或环境变量 `GUANLAN_FUYAO_API_KEY`；设置中心「数据源」页可配置与测试，只写不读）。

| 数据 | 来源 | 备注 |
|---|---|---|
| 快照 | 官方 fuyao `/api/a-share/prices/snapshot`（优先）→ 同花顺 `d.10jqka.com.cn` | 官方批量取数、volume 由股换算成手；网页同花顺 JSONP 与腾讯/新浪交叉校验兜底 |
| 前复权 K 线 | 腾讯 `web.ifzq.gtimg.cn` `fqkline?...,qfq` | 除权日前后的价格与同花顺一致 |
| 后复权 K 线 | 腾讯 `fqkline?...,hfq` | 与前复权同源同口径，不再需要第二个供应商 |
| 不复权 K 线 | 腾讯原始 → 新浪日线 → 同花顺兜底 | 同花顺 line 实际是前复权，只作最后兜底并在注释标明 |
| 个股资金流 | 新浪 `MoneyFlow.ssi_ssfx_flzjtj`（当日）/ `ssl_qsfx_zjlrqs`（多日） | `r0~r3` 是成交额、净额 = in − out、主力净额 = `r0_net + r1_net`（与旧东财「超大单 + 大单」同名口径等价）；多日接口上游只给 `r0_net`，所以多日序列的主力是超大单净额（更窄），界面按 `source` 标注 |
| 板块/概念资金榜 | 新浪 `MoneyFlow.ssl_bkzj_bk`（行业/概念）/ `ssl_bkzj_zjlrqs`（多日） | 板块代码用新浪 category（如 `new_dzxx`），不再有 BK 代码 |
| 涨跌家数 | 新浪全市场快照并发扫描（56 页 × 100，约 1.3s） | 口径是真实逐股涨跌统计；失败时降级同花顺指数 realhead 的涨/平/跌字段（只覆盖单市场） |
| 涨停梯队 / 炸板 / 跌停 | 官方 fuyao `/api/a-share/special-data/limit-up-pool`（优先）→ 同花顺 `dataapi/limit_up/limit_up_pool` | 官方给连板数/封单额/涨停原因；网页池的 `limit_up_count.open_num` 提供炸板数 |
| 龙虎榜 | 同花顺 `ifmarket/lhbtable/report/<date>` | GBK HTML 表格解析；当日无数据回退最近交易日 |
| 兜底快照 | 腾讯 `qt.gtimg.cn`、新浪 `hq.sinajs.cn` | GBK 转码 |
| 自选股 | 本机同花顺容器 `blockstock_*.dat` → 网页登录态 → 手工导入 | 三级降级；cookie 仅本地 0600 落盘，任何返回值/日志不回显 |

行情数据受供应商限制，存在延迟与字段差异；**任何资金流/指标结论都应在界面上标注口径与时间戳**。
资金流口径切换后，历史东财数据的"主力净额"与本版"新浪主力（r0+r1 净额）"不可直接比较，跨版本对比请以界面上标注的 `source` 为准。

## 安全边界

1. **只读**：不实现任何交易/下单接口；输出固定带"研究用途"提示。
2. **密钥**：AES-GCM 本地加密（密钥文件 0600），界面与日志只显示 `hasKey`，全链路脱敏。
3. **网络**：域名白名单（同花顺 / 腾讯 / 新浪 / 模型服务）+ 超时 + 令牌桶限速 + 重试上限；抓取内容标记 `untrusted`。
   已下线数据源的主机会在白名单读取时被自动剔除，避免旧配置把接口"悄悄接回来"。
4. **文件**：所有落盘限制在 `~/.guanlan/`，路径穿越校验。
5. **审计**：工具调用、模型决策、披露展开、配置变更 append-only 记录，可导出。
6. **预算**：单次工具结果 ≤ 256KB、单次任务的工具调用次数有上限（30）；**token 预算默认不设上限**（`MaxTokensPerRun=0`），停机只由工具调用步数与模型单次输出上限决定，需要成本闸门时才在策略里显式写 >0 的值。

## 目录

```
pkg/contract/        跨层契约（DTO + Data/Store/LLM/Guard/Audit）
pkg/plugin/          插件契约（Manifest/Context/Tool/UI/Method/Service/Bus）
internal/kernel/     插件运行时（注册、拓扑激活、路由、审计）
internal/agent/      harness：Router / Worker / Session / Watchdog
internal/progressive/ tool·skill·rag 三级披露
internal/datasource/ 同花顺 / 腾讯 / 新浪
internal/market/     指标与形态（纯函数）
internal/backtest/   回测与策略
internal/store/      sqlite 持久化
internal/security/   Guard / 脱敏 / 审计
plugins/*            功能插件（工具 + 面板 + 方法）
content/skills/*     Agent 技能（SKILL.md）
content/kb/*         知识库文档（RAG 语料）
frontend/            Wails 前端（Vue3 + TS）
docs/                架构、计划、PRD、验收
```

## 状态

骨架（契约 / kernel / Wails / headless / 前端构建链路）已完成并通过单测；
数据层、harness、渐进式披露、十个面板、知识库与验收脚本正在按 [docs/PLAN.md](docs/PLAN.md) 分线交付。


---

[![](https://img.shields.io/badge/powered_by-dsh-4D6BFE?style=flat-square&logo=deepseek&logoColor=white)](https://github.com/deepseek-ai/deepseek-harness)
