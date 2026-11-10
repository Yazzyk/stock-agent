# 观澜 · Stock Agent

面向 A 股的**研究型 harness agent**桌面应用：盘前出计划、盘中盯盘、盘后复盘、量化验证，
全过程可复现、可审计。**只做研究，不接券商下单。**

> 研究用途，不构成投资建议。量化结论必须自带样本内外与成本假设，任何"稳赚"叙事都是错的。

## 它解决什么问题

一个人做 A 股研究，重复劳动集中在四件事：拉数据、算指标、守纪律、写复盘。
这个应用把四件事变成可复用的闭环：

```
数据(同花顺/东财) → 指标与资金流 → Agent 盘前计划 → 盘中盯盘触发 → 盘后复盘 → 记忆回写 → 下一次更准
```

和"AI 选股神器"的区别：**没有预测承诺，只有流程纪律和可检验的结论。**

## 核心能力

| 能力 | 说明 |
|---|---|
| 自选股 | 秒级同步本机同花顺自选（本地容器解析），网页端/手工导入三级降级 |
| 资金流向 | 个股主力/超大/大/中/小单净额；板块、概念资金流排名 |
| K 线分析 | 日/周/月/60 分，MA/量能，形态识别，指标库（RSI/MACD/BOLL/KDJ/ATR） |
| Agent 工作台 | 盘前计划 / 盘中盯盘 / 盘后复盘 / 量化研究 / 对话，流式输出 + 会话存档 |
| 量化回测 | T+1、无未来函数、真实成本（佣金/印花税/过户费/滑点）、样本内外与 walk-forward |
| 知识库 | 本地 RAG（中文 bigram 索引）+ 技能库（SKILL.md），Agent 按需检索 |
| 模型设置 | DeepSeek / OpenAI 兼容多 provider，密钥本地 AES-GCM 加密 |

## 架构（插件优先）

```
Wails 桌面端 (Vue3 + TS)
   │  Plugins() / Invoke(plugin, method, payload)
   ▼
kernel  注册 · 拓扑激活 · 工具/面板/方法/服务聚合 · 事件总线 · 审计 · 安全网关
   │
   ├── agent harness：Router(轻量判意图) → Worker(隔离上下文 ReAct) → 记忆回写
   ├── 渐进式披露：tool / skill / rag 三级（索引 → 展开 → 执行）
   ├── 数据层：同花顺 · 东财 · 新浪 · 腾讯（缓存 / 限速 / 降级）
   └── 插件：watchlist fundflow kline agentlab knowledge modelsettings
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
go run . --headless --task=premarket
go run . --headless --task=postmarket

# 桌面端开发（热重载）
wails dev
# 构建 macOS 应用
wails build
```

本地数据在 `~/.guanlan/`（sqlite、密钥、会话、日志），不会写入仓库。

## 数据来源与限制

| 数据 | 来源 | 备注 |
|---|---|---|
| 快照 / K 线 | 同花顺 `d.10jqka.com.cn` | JSONP；板块用 `bk_` 前缀 |
| 个股资金流 / 板块概念排名 | 东方财富 push2 / bkzj | 部分域名存在地区可达性差异，已做多源降级 |
| 兜底快照 | 腾讯 `qt.gtimg.cn`、新浪 `hq.sinajs.cn` | GBK 转码 |
| 自选股 | 本机同花顺容器 `blockstock_*.dat` → 网页登录态 → 手工导入 | 三级降级，凭证不落盘 |

行情数据受供应商限制，存在延迟与字段差异；**任何资金流/指标结论都应在界面上标注口径与时间戳**。

## 安全边界

1. **只读**：不实现任何交易/下单接口；输出固定带"研究用途"提示。
2. **密钥**：AES-GCM 本地加密（密钥文件 0600），界面与日志只显示 `hasKey`，全链路脱敏。
3. **网络**：域名白名单 + 超时 + 令牌桶限速 + 重试上限；抓取内容标记 `untrusted`。
4. **文件**：所有落盘限制在 `~/.guanlan/`，路径穿越校验。
5. **审计**：工具调用、模型决策、披露展开、配置变更 append-only 记录，可导出。
6. **预算**：单次工具结果 ≤ 256KB、工具调用次数与 token 预算双重上限。

## 目录

```
pkg/contract/        跨层契约（DTO + Data/Store/LLM/Guard/Audit）
pkg/plugin/          插件契约（Manifest/Context/Tool/UI/Method/Service/Bus）
internal/kernel/     插件运行时（注册、拓扑激活、路由、审计）
internal/agent/      harness：Router / Worker / Session / Watchdog
internal/progressive/ tool·skill·rag 三级披露
internal/datasource/ 同花顺 / 东财 / 新浪 / 腾讯
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
数据层、harness、渐进式披露、六个面板、知识库与验收脚本正在按 [docs/PLAN.md](docs/PLAN.md) 分线交付。


---

[![](https://img.shields.io/badge/powered_by-dsh-4D6BFE?style=flat-square&logo=deepseek&logoColor=white)](https://github.com/deepseek-ai/deepseek-harness)
