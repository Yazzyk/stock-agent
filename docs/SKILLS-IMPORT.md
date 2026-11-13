# 导入技能清单（dsh / openclaw）

本轮从本机 dsh（DeepSeek Harness）与 openclaw 的金融相关技能中筛选并导入 4 个，落在 `content/skills/`。
原则：**只导入对 A 股研究有方法论价值、且不依赖本应用无法执行的运行时**；纯 CLI / Python / 浏览器自动化技能不导入。

| 技能 | 来源 | 用途 | 本应用适配 |
|---|---|---|---|
| `champion-limitup-playbook` | `~/.dsh/skills/champion-limitup-playbook` | 实盘冠军短线战法：反指抱团、一字首开、双叠加、逻辑挖掘四步法 | 保留方法论；数据获取改用 `market.*` / `flow.*` / `kline.*` 工具 |
| `ultra-short-trading` | `~/.openclaw/workspace-finance-expert/skills/ultra-short-trading` | 超短线七位一体：周期择时、选股审美、一进二、量价打板、见顶离场 | 保留方法论；`one_to_two.md` 仅作聚宽策略参考，不执行 |
| `youzi-tracker` | `~/.openclaw/workspace-finance-expert/skills/youzi-tracker` | 游资席位库、龙虎榜分析、资金博弈与情绪判断 | 龙虎榜→`market.longhu`、热榜→`market.hot`、涨停梯队→`market.ladder`；席位明细无工具，禁止编造 |
| `humanizer-zh` | `~/.dsh/skills/humanizer-zh` | 中文报告润色：去模板化、去空话、保留事实与作者声音 | 只用于表达润色，不改事实/数字/结论/不确定性 |

## 统一适配说明

每个导入技能的 `SKILL.md` 顶部都加了「观澜适配」提示：

- 本应用只读研究，**不能执行 shell / Python / 浏览器自动化**；
- 数据必须走现有工具，不要声称执行了外部命令；
- 技能里的示例数字不是真实行情；
- 结论必须写清依据、不确定性与风险提示，不构成交易建议。

## 未导入及原因

| 来源技能 | 原因 |
|---|---|
| dsh `short-drama-*` / `toonflow` / `openbiliclaw-adapter` / `comment-analysis` / `browse` | 短剧、B 站内容运营，与 A 股研究无关 |
| dsh `dsh-*`（文档/合并 PR/性能/CI/代码审查等）、`goframe-v2`、`ub-mode`、`xterminal-mcp` | 开发流程类，面向编码助手而非投研 Agent |
| openclaw `weather` / `notion` / `openai-image-gen` / `video-frames` / `gifgrep` / `xiaohongshu-mcp` 等 | 与本产品定位无关 |
| openclaw `akshare-stock` / `aakshare` / `tushare-finance` / `tusharefree` / `tdx-stock` / `a-stock-market` | 依赖 Python / CLI / 外部账号，本应用禁止执行命令；行情与财务数据已有内置数据源与工具 |
| openclaw `hithink-finance-*`（9 个） | 依赖 `hithink-finance` CLI；其覆盖的行情/涨停池/龙虎榜/热榜已由 `market.*` 工具体系提供 |
| openclaw `Stock-Analysis` | 面向美股/加密与 Yahoo Finance，不属于 A 股终端范围 |
| openclaw `agent-browser` / `Playwright-Scraper` / `multi-search-engine` / `brave-search` | 依赖浏览器自动化或搜索 API，本应用不开放 exec / 浏览器 |

如果后续要接入其中某类能力，优先做成**受控工具**（白名单域名 + 结构化返回），而不是把 CLI 指令直接交给模型。
