# 交付计划与协作约定（v1，Lead 维护）

## 0. 现状（已交付，只读）

- `pkg/contract`：DTO 与 DataService/Store/LLMService/Guard/Audit 接口
- `pkg/plugin`：Manifest/Plugin/Context/Tool/UI/Method/Service/Bus
- `internal/kernel`：注册、拓扑激活、工具聚合、Invoke/CallTool 路由、审计、panic 兜底
- `internal/bootstrap`：配置/日志/组装（当前 Store/Guard/Audit/Data/LLM 是过渡 stub）
- `internal/headless`：无界面任务入口（selftest/plugins/tools/quote/bars/watchlist/premarket/postmarket/monitor/quant）
- `app.go` `main.go` `wails.json`：Wails 绑定与桌面入口（`go:embed frontend/dist`）
- `frontend/`：Vue3+TS+Vite 骨架，`api.ts` 已封装 Wails 桥
- 工具链已验证：`go build ./...` `go vet ./...` `node/npm(via npmmirror)` `vue-tsc+vite build` `wails v2.15.0`

## 1. 所有权（写作用域，互不越界）

| 成员 | 可写 | 只读 |
|---|---|---|
| lead | `pkg/**` `internal/kernel/**` `internal/bootstrap/**` `internal/headless/**` `internal/pluginsbundle?` `app.go` `main.go` `wails.json` `plugins/all` `plugins/builtin` `docs/ARCHITECTURE.md` `README.md` `go.mod` `go.sum` | 其它全部 |
| backend-core | `internal/agent/**` `internal/progressive/**` `internal/llm/**` `internal/security/**` `plugins/modelsettings/**` `plugins/agentlab/**` `plugins/knowledge/**` | 其余只读 |
| backend-data | `internal/datasource/**` `internal/market/**` `internal/store/**` `internal/backtest/**` `plugins/watchlist/**` `plugins/fundflow/**` `plugins/kline/**` | 其余只读 |
| frontend | `frontend/**` | 其余只读 |
| product | `docs/PRD.md` `docs/UX.md` `docs/ACCEPTANCE.md` `docs/DOMAIN.md` `content/**` | 其余只读 |
| qa | `test/**` `scripts/**` `docs/QA.md` | 其余只读 |

**红线**：不改别人的包；不改 `go.mod/go.sum`（依赖已备：eino v0.9.21、eino-ext deepseek v0.1.7 / openai v0.1.13、modernc.org/sqlite v1.52.0、golang.org/x/text、gopkg.in/yaml.v3）；需要新依赖先消息 lead。

## 2. 命名规范（前端与模型都依赖它，必须一致）

- **插件 ID**：`watchlist` `fundflow` `kline` `modelsettings` `agentlab` `knowledge`
- **工具名**：`<域>.<动作>`，例：`market.quote` `market.bars` `market.search` `watch.list` `watch.sync` `flow.stock` `flow.rank` `sector.list` `kline.analyze` `quant.backtest` `agent.run` `kb.search` `kb.open` `skill.list` `skill.view`
- **前端方法名**（Invoke 的 method）：动词短名 `list` `detail` `sync` `add` `remove` `save` `test` `run` `stop` `sessions` `search` `import`
- **面板组件键**（UI slot 的 Component，前端组件注册表键同名）：`watchlist` `fundflow` `kline` `modelsettings` `agentlab` `knowledge`
- **服务名**（Services.Register）：`agent.runtime` `kb.store` `kline.engine` `quant.engine`
- **事件主题**（Events）：`kernel.*` `agent.delta` `agent.report` `agent.state` `watch.updated` `market.tick`

## 3. 渐进式披露约定（backend-core 实现，其它插件配合）

工具作者的义务：
1. `Summary` ≤ 40 字，写"什么场景用"，不写实现；
2. `Tags` 至少 1 个域标签（market/flow/watch/agent/kb/skill/quant/model）；
3. `Schema` 必须是合法 JSON Schema（`type/object/properties/required`），LLM 只会看到 L2 展开后的 schema；
4. `Risk` 如实标注；`write` 只用于本地配置写入，`exec` 永不允许；
5. 单次返回尽量 ≤ 8KB，列表必须有 `limit`。

## 4. 数据源事实（已实测，2026-11-10）

| 用途 | 可用端点 | 备注 |
|---|---|---|
| 个股/指数 K 线 | `https://push2.eastmoney.com/api/qt/stock/kline/get`（https 可用，http 502） | `secid=1.600000/0.000001`，`klt=101/102/103`，`fqt=0/1/2` |
| 个股资金流 | `https://push2.eastmoney.com/api/qt/stock/fflow/kline/get` | f51 日期、f52 主力、f53 小单、f54 中单、f55 大单、f56 超大单 |
| 板块/概念资金流排名 | `https://data.eastmoney.com/dataapi/bkzj/getbkzj?key=f62&code=m:90+t:2`（行业 t:2 / 概念 t:3） | 返回 diff[].f12/f14/f62 |
| 快照（JSONP） | `https://d.10jqka.com.cn/v6/realhead/hs_<code>/defer/last.js` | items.10=现价；其余字段需和腾讯/新浪交叉校验后再用 |
| K 线（THS，含板块 bk_） | `https://d.10jqka.com.cn/v6/line/hs_<code>/01/last.js` | 全量历史 |
| 快照兜底 | `https://qt.gtimg.cn/q=sh600000`、`https://hq.sinajs.cn/list=sh600000` | GBK，需 x/text 转码 |
| 个股资金流兜底 | `https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssi_ssfx_flzjtj?daima=sh600000` | r0 超大 / r1 大 / r2 中 / r3 小（净额） |
| 同花顺自选（本机） | `~/Library/Containers/cn.com.10jqka.macstock/Data/Documents/cloud_store/blockstock_<uid>/public/*.dat` | 已确认存在；NSKeyedArchiver + protobuf；只保留 userid，凭证打码 |
| 同花顺自选（网页） | `https://t.10jqka.com.cn/newcircle/group/getSelfStockWithMarket/` | 需登录态 cookie；无则引导用户登录 |

## 5. 验收命令（每个人交付前自我验收）

```sh
go build ./... && go vet ./...
go test ./internal/<你的包>/ -count=1
go run . --headless --task=selftest          # 插件全部激活才 OK
go run . --headless --task=tools             # 工具索引（L1）
cd frontend && npm run build                 # vue-tsc 必须过
wails build -clean                           # 最终集成由 lead 跑
```

## 6. 安全红线（写死）

1. 不实现下单/交易接口，不引入 shell 执行工具；
2. 密钥只存本地，AES-GCM，密钥文件 0600，日志与审计全量脱敏（`sk-`、`Bearer `、`cookie`、`token=`）；
3. 网络只经 Guard 白名单，超时 8s，限速令牌桶，重试 ≤2；
4. 抓取内容标记 `untrusted`，模型输出不得直接驱动写文件/发请求；
5. 落盘路径必须在 `~/.guanlan` 内（Guard.CheckPath）；
6. 工具单次结果 ≤ 256KB（kernel 已截断），任务工具调用 ≤ policy.MaxToolCalls。

## 7. 协作方式

- 每完成一个可验收的里程碑，用 `send_message` 报 lead：**文件、命令、真实输出、缺口、需要 lead 集成的事项**。
- 遇到接口不匹配：先看 `pkg/contract` / `pkg/plugin`，仍然不够就把需求发给 lead，不要自行改契约文件。
- 不要动 git（不 commit / 不 stash / 不切分支），由 lead 统一提交。
- 不要并行跑 `go mod tidy`、`wails build`（会抢锁/删 dist），集成阶段由 lead 跑。
