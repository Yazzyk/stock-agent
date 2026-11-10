# 验收矩阵（ACCEPTANCE）

版本：v1.0 · 2026-11-10 · 维护：product · 执行：qa
对应任务台账 R1–R9。每条给出 Given / When / Then、可执行命令、期望输出。

## 0. 怎么用

1. 在仓库根目录执行（除标注 `cd frontend` 的命令）。
2. 每条先跑命令，再对照"期望输出"判断。
3. 结果只有三种，**不许写"部分通过"**：

| 判定 | 含义 | 记录方式 |
|---|---|---|
| PASS | 命令成功且输出符合期望 | 记 exit code 与关键行 |
| FAIL | 命令成功但输出不符合期望，或出现明确错误 | 记实际输出前 10 行 |
| BLOCKED | 依赖项未交付（例如工具尚未注册），本次无法判定 | 记缺失的依赖与归属人 |

4. 发现 FAIL 或 BLOCKED，写清：条目号、命令原文、实际输出、期望输出、归属人。
5. 全部条目跑完，把结果贴给 lead，不要只写"验收通过"。

## 通用前置

```sh
go version                 # 期望 go1.25.x
node -v && npm -v          # 期望 node ≥ 20
wails version              # 期望 v2.15.0（仅 R9 需要）
echo $HOME/.guanlan        # 数据目录，首次运行会自动创建
```

任何一条前置不满足，先修环境，不要把环境问题记成 FAIL。

---

## R1 删除教学代码与引用

| 项 | 内容 |
|---|---|
| 归属 | lead |
| 状态 | 已完成（2026-11-10 复核） |
| Given | 仓库根目录，无 `cmd/mini` 教学代码 |
| When | 检查目录、全仓库引用、构建 |
| Then | 目录不存在、`*.go` 中无 `cmd/mini` 引用、`go build ./...` 成功 |

```sh
test ! -d cmd/mini && echo "PASS: 无 cmd/mini" || echo "FAIL: 存在 cmd/mini"
grep -rn "cmd/mini" --include=*.go . | wc -l
go build ./...
echo "build exit=$?"
```

期望输出：

```
PASS: 无 cmd/mini
0
build exit=0
```

判 FAIL 的条件：目录存在；引用计数 > 0；`go build` 非 0 退出。

---

## R2 插件契约与 kernel 运行时

| 项 | 内容 |
|---|---|
| 归属 | lead |
| 状态 | 已完成 |
| Given | `pkg/plugin` 与 `internal/kernel` 已交付 |
| When | 跑 kernel 单测与构建 |
| Then | 全部用例 `ok`；覆盖拓扑激活、工具重名回滚、Invoke 路由、CallTool 风险拒绝、审计记录、panic 兜底 |

```sh
go build ./...
go test ./internal/kernel/ -count=1 -v 2>&1 | tail -20
```

期望输出（关键行）：

```
ok  	github.com/yazzyk/stock-agent/internal/kernel	0.1xx s
```

补充检查（插件接线后才有意义，未接线记 BLOCKED）：

```sh
go run . --headless --task=selftest 2>/dev/null | tail -3
```

期望：`结果: OK`，且第一行 `插件: N 个，已激活 N 个`（N ≥ 6）。

---

## R3 数据层与存储

| 项 | 内容 |
|---|---|
| 归属 | backend-data |
| 状态 | 交付中 |
| Given | 网络可达白名单域名；sqlite 可写 `~/.guanlan` |
| When | 取快照、取 K 线、跑数据层单测 |
| Then | 快照含来源与时间戳；K 线条数与排序正确；单测无 FAIL |

```sh
go run . --headless --task=quote -- 600000
go run . --headless --task=bars -- 600000 day 60
go test ./... -count=1 2>&1 | grep -E "FAIL|ok " | head -20
```

期望输出（`quote`，字段名以 `contract.Quote` 为准）：

```json
[
  {
    "code": "600000",
    "name": "浦发银行",
    "market": "sh",
    "price": 10.12,
    "prevClose": 10.03,
    "changePct": 0.9,
    "amount": 623100000,
    "time": "2026-11-10 15:00:00",
    "source": "eastmoney",
    "stale": false
  }
]
```

数值仅示意：不同交易日的价格、成交额必然不同，核对的是**字段名与不变量**（字段齐全、类型正确、单位是元、`time` 为今日、`source` 非空）。

`bars` 期望：60 条，`date` 严格递增，无重复；`close > 0`。

判 FAIL 的条件：

- `price` 为 0 或负数；
- `source` 为空（无法追溯口径）；
- `bars` 条数不等于请求的 `limit`（数据不足时要显式报错，不能静默少返回）；
- 单测输出含 `FAIL`。

判 BLOCKED 的条件：输出为 `工具不存在: market.quote`（插件未接线）。

---

## R4 回测引擎与策略

| 项 | 内容 |
|---|---|
| 归属 | backend-data |
| 状态 | 交付中（依赖 R3 数据、R5 的 `quant` worker） |
| Given | 后复权日线 ≥ 2 年；历史成分股含退市标的 |
| When | 跑量化 worker，检查报告结构 |
| Then | 生成 `runs/<日期>/report.md`，首节为五关自检表，成本明细四项齐全 |

```sh
go run . --headless --task=quant
ls ~/.guanlan/runs/$(date +%F)/
sed -n '1,25p' ~/.guanlan/runs/$(date +%F)/report.md
```

期望：报告包含下列关键字（缺一记 FAIL）：

```
未来函数
样本内外
walk-forward
幸存者偏差
参数敏感性
佣金
印花税
过户费
滑点
```

成本口径必须与 `docs/DOMAIN.md` 第 4.3 节一致：佣金双边 0.03% 最低 5 元、印花税卖出 0.05%（2023-08-28 起）、过户费双边 0.001%、滑点单边 0.1%。

判 FAIL 的条件：

- 报告无自检表，或自检结论与绩效数字矛盾（例如样本外衰减 62% 却写"通过"）；
- 成本行只写佣金、缺印花税或滑点；
- 撮合出现"T 日收盘信号 T 日成交"（查代码或用构造数据验证）。

---

## R5 Agent harness

| 项 | 内容 |
|---|---|
| 归属 | backend-core |
| 状态 | 交付中 |
| Given | 模型已配置（`Models()` 中存在 `hasKey=true`）；插件已接线 |
| When | 跑盘前 worker |
| Then | 工具调用 ≥ 3 次；落盘 `plan.md` 且四字段齐全；退出码 0 |

```sh
go run . --headless --task=premarket; echo "exit=$?"
ls ~/.guanlan/runs/$(date +%F)/
grep -cE "^\| .*\| .*\| .*\| .*\| .*\|" ~/.guanlan/runs/$(date +%F)/plan.md
```

期望：

- `exit=0`；
- 目录中存在 `plan.md`；
- 计划正文含 `触发条件`、`失效条件`、`止损位`、`计划仓位` 四个词；
- 标的数量 ≤ 5。

补充检查（盘后能读到当天计划）：

```sh
go run . --headless --task=postmarket; echo "exit=$?"
ls ~/.guanlan/runs/$(date +%F)/
```

期望：目录中出现 `review.md`，且正文含"对账"或"计划与实际"。

判 FAIL 的条件：无落盘文件；四字段缺失；标的 > 5；模型被调用但审计无记录。

判 BLOCKED 的条件：`插件不存在: agentlab` 或 `Models()` 无可用 key。

---

## R6 渐进式披露（工具 / 技能 / 知识库）

| 项 | 内容 |
|---|---|
| 归属 | backend-core |
| 状态 | 交付中 |
| Given | `content/skills/` 6 个技能、`content/kb/` 10 篇已就位 |
| When | 拉工具索引、技能列表、知识库检索 |
| Then | 工具 ≥ 12 个且 summary ≤ 40 字；技能 6 个且 description ≤ 60 字；检索命中带片段 |

```sh
go run . --headless --task=tools
go run . --headless --task=skill.list
go run . --headless --task=kb.search
```

期望（`tools` 格式由 `internal/headless` 固定）：

```
market.quote                 [network] 取个股与指数实时快照，批量 ≤ 50 只
market.bars                  [network] 取日/周/月/分钟 K 线，需显式指定复权
...
```

说明：headless 的兜底分支会把未知 task 当成工具名调用，参数为空对象 `{}`；因此这些工具必须对缺省参数给出合理默认值。若工具强制要求参数，返回参数校验错误也算“命令可跑通”，记录里写明所需参数。

检查点：

- 工具总数 ≥ 12；
- 每行含 `[risk]`，risk 只出现 `read`/`network`/`write`（**出现 `exec` 直接 FAIL**，违反红线）；
- `skill.list` 返回 6 个技能，name 为 `premarket-plan / postmarket-review / intraday-monitor / kline-reading / fundflow-reading / quant-honesty`；
- `kb.search` 返回结果含标题与片段，且引用文件路径在 `content/kb/` 下。

frontmatter 解析自检（product 侧已跑通，qa 可复核）：

```sh
grep -c "^name:" content/skills/*/SKILL.md
grep -c "^description:" content/skills/*/SKILL.md
grep -L "^risk:" content/skills/*/SKILL.md   # 期望无输出
```

判 FAIL 的条件：工具 summary 超 40 字；出现 `exec` 风险工具；技能少于 6 个；kb 检索 0 结果。

---

## R7 自选股同步

| 项 | 内容 |
|---|---|
| 归属 | backend-data + watchlist 插件 |
| 状态 | 交付中 |
| Given | 本机可能安装也可能未安装同花顺 |
| When | 调用自选列表；无数据时触发同步 |
| Then | 有数据返回条目；无数据返回结构化失败（含下一步指引），不 panic、不空白 |

```sh
go run . --headless --task=watchlist; echo "exit=$?"
```

期望（成功）：

```json
[
  {"code":"600000","name":"浦发银行","market":"sh","group":"默认","order":0,"source":"ths-local"}
]
```

期望（失败，且必须带指引）：

```json
{
  "success": false,
  "message": "本机未找到同花顺数据，可通过网页登录或手工导入",
  "needFullDiskAccess": true,
  "needWebLogin": true
}
```

判 FAIL 的条件：`exit` 非 0 且无结构化错误；输出为空；返回条目缺 `code` 或 `group`；凭证出现在输出里（**出现 userid 之外的任何凭证信息直接 FAIL**）。

---

## R8 资金流与 K 线面板

| 项 | 内容 |
|---|---|
| 归属 | backend-data + frontend |
| 状态 | 交付中 |
| Given | 数据源可用；前端依赖已安装 |
| When | 拉板块资金流、板块列表；构建前端 |
| Then | 资金流字段单位正确；前端 `vue-tsc` 与打包通过 |

```sh
go run . --headless --task=flow.rank
go run . --headless --task=sector.list
cd frontend && npm run build; echo "build exit=$?"
```

期望（`flow.rank`）：返回数组，每项含 `code`/`name`/`mainNet`/`changePct`，`mainNet` 量级在 1e7–1e11 之间（单位元）。若工具要求必填参数，返回参数校验错误也算**命令可跑通**，但要在记录里写明参数需求。

前端期望：

```
vite v6.x building for production...
✓ built in xxx ms
build exit=0
```

判 FAIL 的条件：`mainNet` 以"万/亿"为单位（口径错）；`vue-tsc` 报类型错误；构建产物缺失。

---

## R9 安全与一键验收

| 项 | 内容 |
|---|---|
| 归属 | lead + qa |
| 状态 | 交付中 |
| Given | 全量插件已接线；`~/.guanlan` 可写 |
| When | 检查脱敏、白名单、路径、风险等级、审计 |
| Then | 无明文密钥；无 exec 工具；落盘只在数据目录；审计条数等于调用次数 |

```sh
# 1) 脱敏：输出与日志里不得出现密钥特征
go run . --headless --task=selftest 2>&1 | grep -E "sk-[A-Za-z0-9]|Bearer [A-Za-z0-9]|token=" | wc -l

# 2) 无 exec 风险工具
go run . --headless --task=tools | grep -c "\[exec\]"

# 3) 落盘路径
ls -la ~/.guanlan/runs/

# 4) 白名单内可正常取数（非白名单域名被拒由 internal/security 单测覆盖）
go run . --headless --task=quote -- 600000

# 5) 集成构建
wails build -clean; echo "build exit=$?"
```

期望输出：

```
0        # 无密钥特征
0        # 无 exec 工具
drwx------ ... runs
build exit=0
```

判 FAIL 的条件：第 1 项 > 0；第 2 项 > 0；`~/.guanlan` 之外出现运行产物；`wails build` 非 0。

---

## 附录 A 当前基线（接线完成后实测）

采集于集成末期。总览与面板清单为本机实跑输出，R3–R9 的结论取自 lead 的集成实测记录。

### A.1 总览

```
插件: 7 个，已激活 7 个；工具 22 个；面板 7 个
服务: agent.runtime, kb.store, kline.engine, llm, quant.engine
结果: OK
```

> 工具数以 `--task=tools` 实测为准，22 个 = 上表合计（knowledge 含 `kb.import`，早期台账的 21 是加它之前的数，已作废）。

| 插件 | 版本 | 工具 | 方法 | 面板 |
|---|---|---|---|---|
| agentlab | 1.0.0 | 5 | 5 | 1 |
| fundflow | 1.0.0 | 3 | 3 | 1 |
| kline | 1.0.0 | 2 | 2 | 1 |
| knowledge | 1.0.0 | 7 | 6 | 1 |
| modelsettings | 0.1.0 | 0 | 4 | 1 |
| quant | 0.1.0 | 1 | 3 | 1 |
| watchlist | 1.0.0 | 4 | 5 | 1 |
| **合计** | — | **22** | **28** | **7** |

### A.2 面板清单（与 `docs/API.md` §10 一致）

| id | title | component | group | order |
|---|---|---|---|---|
| watchlist | 自选股 | watchlist | 行情 | 10 |
| fundflow | 资金流向 | fundflow | 行情 | 20 |
| kline | K线分析 | kline | 行情 | 30 |
| agentlab | Agent 实验室 | agentlab | 研究 | 40 |
| quant | 量化研究 | quant | 研究 | 45 |
| knowledge | 知识库 | knowledge | 研究 | 50 |
| modelsettings | 模型设置 | modelsettings | 设置 | 90 |

### A.3 R1–R9 结论

| 条目 | 结论 | 证据 |
|---|---|---|
| R1 删除教学代码 | 通过 | `cmd/mini` 不存在 |
| R2 插件架构 | 通过 | 7/7 激活，工具 22 |
| R3 数据层 | 通过 | 实测 5/5 live |
| R4 回测 | 通过 | 11 项测试；报告含成本四行（佣金/印花税/过户费/滑点） |
| R5 Agent harness | 通过 | 盘前任务用真实模型跑 8 步并产出计划 |
| R6 渐进式披露 | 通过 | `tool.*` / `skill.*` / `kb.*` 齐备，预算裁剪有测试覆盖 |
| R7 自选同步 | 通过 | 20 组 163 只，`source=ths-local` |
| R8 面板 | 通过 | 7 个面板已在桌面端验证 |
| R9 安全 | 通过 | 脱敏、白名单、路径、审计均通过；工具索引无 `exec` |

### A.4 交付时不计为失败的已知限制

| 限制 | 表现 | 处理 |
|---|---|---|
| 复权 | `qfq`/`hfq` 当前没有真实序列 | 接口显式降级为 `adjust=none`，界面显示角标或警告条，不静默 |
| 周期 | 仅 `day`/`week`/`month`/`min60` | `min15`/`min5`/`min1` 不支持，请求时需明确报错 |
| K 线主源 | 东财 K 线端点不可达 | K 线以同花顺为主源；东财仍提供资金流与板块数据 |
| 情绪指标 | 涨停家数 / 炸板率 / 连板高度无工具 | 知识库 `theme-sentiment-cycle.md` 有口径，工具列 P1 |

命中以上四条时不记 FAIL；但"静默降级"（没有角标、没有错误）仍记 FAIL。

## 附录 B 交叉复核清单（product ↔ 其它成员）

| 检查项 | 期望 | 归属 |
|---|---|---|
| `content/skills/*/SKILL.md` frontmatter 可被 yaml.v3 解析 | 6/6 通过 | product |
| 技能正文 80–200 行 | 6/6 在区间内 | product |
| skill description ≤ 60 字 | 6/6 通过 | product |
| `content/kb/*.md` 每篇有且仅有一个 H1 | 10/10 | product |
| kb 每篇正文 300–800 汉字 | 10/10 | product |
| 成本口径（DOMAIN 4.3）与回测实现一致 | 四项参数逐字一致 | backend-data |
| 资金流分档（DOMAIN 2.1）与数据层实现一致 | f52 = f55 + f56 | backend-data |
| 面板槽位与 `frontend/src/panels` 组件键一致 | 7 个键同名（watchlist/fundflow/kline/agentlab/quant/knowledge/modelsettings） | frontend |
| 红涨绿跌变量 | `--up: #ef4b4b`、`--down: #2fbf71` | frontend |

## 附录 C 记录模板

```
验收时间：2026-11-11 10:20
执行人：qa
环境：go1.25.x / node 20.x / wails v2.15.0 / macOS 15

| 条目 | 结果 | 命令 | 证据 |
|---|---|---|---|
| R1 | PASS | test ! -d cmd/mini | PASS: 无 cmd/mini |
| R2 | PASS | go test ./internal/kernel/ | ok 0.124s |
| R3 | BLOCKED | go run . --headless --task=quote | 工具不存在: market.quote（backend-data 未接线） |
...
汇总：PASS 2 / FAIL 0 / BLOCKED 7
```
