# 交付里程（Roadmap）

> 详细设计见 [ARCHITECTURE.md](ARCHITECTURE.md)，协作约定见 [PLAN.md](PLAN.md)，业务口径见 [DOMAIN.md](DOMAIN.md)。

| 阶段 | 内容 | 验收 | 状态 |
|---|---|---|---|
| M0 骨架 | 契约（pkg/contract, pkg/plugin）、kernel、Wails 入口、headless、前端构建链路 | `go build ./...`、`go test ./internal/kernel`、`wails build` | ✅ |
| M1 数据 | sqlite 存储、同花顺/腾讯/新浪数据源（东方财富已下线）、指标库、自选同步三级降级 | `go test ./internal/{store,market,datasource}`、`--task=bars -- 600000` | ✅ |
| M2 Harness | Eino ReAct、Router/Worker/Session/Watchdog、模型适配与密钥加密 | `go test ./internal/agent ./internal/llm`、fake LLM 循环测试 | ✅ |
| M3 渐进式披露 | tool/skill/rag 三级、预算裁剪、披露审计 | `go test ./internal/progressive`、`--task=tools` | ✅ |
| M4 插件与界面 | 七个插件与面板：watchlist / fundflow / kline / agentlab / quant / knowledge / modelsettings | `npm run build`、七个面板可用、`--task=selftest` 全部激活 | ✅ |
| M5 量化与知识 | 回测引擎（成本/T+1/无未来函数/walk-forward）、知识库与技能内容 | `go test ./internal/backtest`、`--task=backtest -- 600000` | ✅ |
| M6 验收 | verify.sh 一键验收、QA 矩阵、桌面应用打包 | `bash scripts/verify.sh --with-network --with-race` 全绿、`wails build` 产出 app | ✅ |

## 后续（P1，未做）

- 情绪温度工具（涨停家数/炸板率/连板高度）与对应面板；
- 复权序列的长期可复现（日/周/月已由腾讯 `qfqday`/`hfqday` 覆盖；回测仍要在开始时把复权口径快照落盘，否则今天的年化明天就变）；
- 浅色主题与多主题；Windows/Linux 打包验证。
