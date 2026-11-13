# 同花顺官方数据 API 接入清单（fuyao.aicubes.cn）

> 版本：2026-11-13（依据 https://fuyao.aicubes.cn/llms-full.txt 全量文档 + 真实 Key 采样）
> 这是数据层与前端接力的**唯一端点依据**：端点、参数、上限、权限、单位都以本文为准。
> 机器可读目录在 `internal/datasource/fuyao_catalog.go`（125 个端点，ID 与本文一致），
> 任何一侧新增/下线端点都必须同步本文件 + 目录 + 单测。

## 1. 基本约定

- Base URL：`https://fuyao.aicubes.cn`
- 鉴权：请求头 `X-api-key`（Key 在 `~/.guanlan/fuyao.key`，0600；env `GUANLAN_FUYAO_API_KEY` 优先）。
- 响应信封：`{code, message, request_id, data}`；HTTP 200 也可能是业务错误。
- 时间：毫秒级 Unix 时间戳，展示时区 `Asia/Shanghai`；日期参数用 `YYYY-MM-DD` 或 `yyyyMMdd`（交易日历）。
- 单位：价格/金额=元；成交量=**股**（数据层对外 `contract.Quote.Volume` 才折成手）；涨跌幅/占比/封板率=百分数原值（`1.74` = +1.74%）；估值倍数=倍；`heat` 是热度字符串。
- 批量：`thscodes` 英文逗号分隔；**A 股 K 线/指数 K 线/ETF K 线/三大报表批量均为 10 个/次**；A 股快照官方未写死上限，本层按 50/批 + 并发 ≤4；估值 100；集合竞价 100；ETF 快照 100；反查 100。
- 限流：HTTP 429 或 `code=4001` 都表示限流；数据层统一「退避一次后重试」，仍失败才降级；分批并发上限 4。

## 2. 错误码与处理策略

| code | 含义 | 数据层/工具行为 |
|---|---|---|
| 0 | 成功 | 正常解析 |
| 1001/1002/1003/1004 | 参数缺失/格式/越界/冲突 | 本地先校验；透传中文说明 |
| 2001 | 未认证 | 提示设置中心填写 Key |
| 2003 | 权限不足 | `contract.ErrFuyaoPermission`，工具降级说明 |
| **2004** | **同花顺 AI 客户端专用** | **返回 `permission=client` + `note`，不报错、不重试** |
| 3001/3002/3004 | 标的不存在/未就绪/类型不支持 | 透传；空结果要区分「合法空」 |
| 4001 | 限流 | 退避 300ms 重试一次；仍失败降级并打 `event=fuyao.fallback` |
| 5001/5002/5003 | 内部错误/上游超时/上游不可用 | 透传，前端显示「暂时不可用」而不是 Key 错误 |

**客户端专用清单（真实 Key 实测 2004）**：`capital-flow/*`、`high-frequency/*`、`chip-shape-*`、`fund/etf-flow/*`、`a-share-index/baseinfo/new-concepts`、`news/events/search`、`turnover-prediction-snapshot`、`futures/fundamentals/*`。这些能力在本项目里一律优雅降级，替代口径为现有新浪资金流 / 本地方案。

**上游暂不可用（实测 5003）**：`futures/prices/daily`、`options/prices/daily`（合约、分时、时间轴仍可用）。

## 3. 端点总表（125）

### 3.1 行情（3）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.prices.historical` | `/api/a-share/prices/historical` | thscode, interval, start, end | — | timestamp(毫秒戳) | 公开 |
| `a-share.prices.historical-batch` | `/api/a-share/prices/historical-batch` | thscodes, interval, start, end | ≤10；最多 10 个 | interval, adjust | 公开 |
| `a-share.prices.snapshot` | `/api/a-share/prices/snapshot` | 无 | ≤50 | timestamp(毫秒戳), total | 公开 |

### 3.2 估值（1）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.valuations.snapshot` | `/api/a-share/valuations/snapshot` | thscodes | ≤100；最多 100 个 | 见官方字段表 | 公开 |
> 字段：`pe_ttm/pe_mrq/pb_mrq/ps_ttm/pcf_ttm` 均为倍；`name` 由官方直接返回，可用于补名称。

### 3.3 基础（4）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.calendar.trading-days` | `/api/a-share/calendar/trading-days` | 无 | — | timestamp(毫秒戳) | 公开 |
| `a-share.corporate-actions.adjustment-factors` | `/api/a-share/corporate-actions/adjustment-factors` | thscode | — | thscode, ticker | 公开 |
| `meta.tickers.list` | `/api/meta/tickers/list` | 无 | ≤10000 | timestamp(毫秒戳) | 公开 |
| `meta.tickers.search` | `/api/meta/tickers/search` | q | ≤50 | timestamp(毫秒戳) | 公开 |
> 交易日历 `date` 为 `yyyyMMdd`；`date_ms` 是当日零点毫秒戳。标的检索 `q` 支持代码/名称子串，`limit ≤50`；标的列表 `asset_type` 枚举：`a-share`、`a-share-index`、`fund-otc`、`fund-etf`、`fund-lof`、`fund-reits`、`forex`、`futures`、`futures-commodity-index`、`options`。

### 3.4 财务（7）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.financials.balance-sheets` | `/api/a-share/financials/balance-sheets` | thscode, period | — | assets_total, total_current_assets, non_current_nets_total, cash, accounts_receivable, total_debt, holder_equity_total | 公开 |
| `a-share.financials.balance-sheets-batch` | `/api/a-share/financials/balance-sheets-batch` | thscodes, period | ≤10；最多 10 个 | 见官方字段表 | 公开 |
| `a-share.financials.cash-flow-statements` | `/api/a-share/financials/cash-flow-statements` | thscode, period | — | act_cash_flow_net, invest_cash_flow_net, financing_cash_flow_net, pay_fixed_assets_etc_cash, pay_dividends_profits_interest_cash, cash_equivalents_net_addition | 公开 |
| `a-share.financials.cash-flow-statements-batch` | `/api/a-share/financials/cash-flow-statements-batch` | thscodes, period | ≤10；最多 10 个 | 见官方字段表 | 公开 |
| `a-share.financials.indicators` | `/api/a-share/financials/indicators` | thscode, report | — | `report`、`abilities[]:{ability,indicators[]:{index_id,value}}`；`value` 为原始数值字符串，百分比指标按百分数，缺失 `null` | 公开 |
| `a-share.financials.income-statements` | `/api/a-share/financials/income-statements` | thscode, period | — | operating_income, operating_costs, operating_expenses, sales_fee, manage_fee, research_and_development_expenses, operating_profit(元), interest_expenses, profit_total, income_tax_expense(元), net_profit(元), parent_holder_net_profit | 公开 |
| `a-share.financials.income-statements-batch` | `/api/a-share/financials/income-statements-batch` | thscodes, period | ≤10；最多 10 个 | 见官方字段表 | 公开 |
> 单标的报表 `period=annual|quarterly`，`limit 1..20`，与 `start/end` 互斥；批量报表每组结构 `{thscode,timestamp,item:[]}`，合法空组保留 `item:[]`。
> 财务指标 `report` 格式 **`yyyy-1..4`**（1 一季报、2 中报、3 三季报、4 年报），`value` 是原始数值字符串（不要转 float），百分比类按百分数表达。

### 3.5 指数（7）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share-index.baseinfo.new-concepts` | `/api/a-share-index/baseinfo/new-concepts` | 无 | — | page, size, total, has_next | 客户端专用（2004） |
| `a-share-index.catalog.ths-index-list` | `/api/a-share-index/catalog/ths-index-list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `a-share-index.constituents.ths-index-list-by-stock` | `/api/a-share-index/constituents/ths-index-list-by-stock` | thscode | ≤100 | 见官方字段表 | 公开 |
| `a-share-index.constituents.ths-stock-list` | `/api/a-share-index/constituents/ths-stock-list` | thscode | — | timestamp(毫秒戳) | 公开 |
| `a-share-index.prices.historical` | `/api/a-share-index/prices/historical` | thscode, interval, start, end | 近 5 年 | 见官方字段表 | 公开 |
| `a-share-index.prices.historical-batch` | `/api/a-share-index/prices/historical-batch` | thscodes, interval, start, end | ≤10；最多 10 个 | interval, adjust | 公开 |
| `a-share-index.prices.snapshot` | `/api/a-share-index/prices/snapshot` | thscodes | ≤100 | 见官方字段表 | 公开 |
> 指数代码后缀 `.SH/.SZ`（标准指数）或 `.TI`（同花顺概念/行业）。指数历史单标的只支持 `1d`、近 5 年；批量 ≤10 也只支持 `1d`。个股反查单次 ≤100 个代码，`type=cn_concept|industry|region|tszs`（大小写敏感）。

### 3.6 特色数据（21）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.special-data.abnormal-monitor-list` | `/api/a-share/special-data/abnormal-monitor-list` | 无 | — | page, size, total, has_next | 公开 |
| `a-share.special-data.anomaly-analysis-list` | `/api/a-share/special-data/anomaly-analysis-list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `a-share.special-data.anomaly-analysis-stock` | `/api/a-share/special-data/anomaly-analysis-stock` | thscodes | ≤50；最多 50 个 | 见官方字段表 | 公开 |
| `a-share.special-data.auction-opportunity-dates` | `/api/a-share/special-data/auction-opportunity-dates` | 无 | — | timestamp(毫秒戳), source_updated_at, dates, latest_date | 公开 |
| `a-share.special-data.auction-stock-opportunities` | `/api/a-share/special-data/auction-stock-opportunities` | 无 | ≤50；最多50个 | timestamp(毫秒戳), trade_date, mode, page, size, total, has_next | 公开 |
| `a-share.special-data.chip-shape-dates` | `/api/a-share/special-data/chip-shape-dates` | 无 | — | latest_date, dates | 客户端专用（2004） |
| `a-share.special-data.chip-shape-stocks` | `/api/a-share/special-data/chip-shape-stocks` | shape | — | date, shape, page, size, total, has_next | 客户端专用（2004） |
| `a-share.special-data.dragon-tiger-department-classic-record` | `/api/a-share/special-data/dragon-tiger-department-classic-record` | department_id | — | department_id, department_name, organization_id, classic_record | 公开 |
| `a-share.special-data.dragon-tiger-department-history` | `/api/a-share/special-data/dragon-tiger-department-history` | department_id | — | page, size, total, has_next | 公开 |
| `a-share.special-data.dragon-tiger-list` | `/api/a-share/special-data/dragon-tiger-list` | 无 | — | timestamp(毫秒戳), board_type, trade_date, count, stock_count, stock_items, hot_money_items | 公开 |
| `a-share.special-data.dragon-tiger-stock-institution` | `/api/a-share/special-data/dragon-tiger-stock-institution` | thscode | — | thscode, trade_date | 公开 |
| `a-share.special-data.hot-stock-list` | `/api/a-share/special-data/hot-stock-list` | 无 | — | 见官方字段表 | 公开 |
| `a-share.special-data.hot-stock-list-history` | `/api/a-share/special-data/hot-stock-list-history` | date | — | date, date_ms(毫秒戳) | 公开 |
| `a-share.special-data.hot-stock-rank-trend` | `/api/a-share/special-data/hot-stock-rank-trend` | thscode, start_date, end_date | — | timestamp(毫秒戳) | 公开 |
| `a-share.special-data.limit-break-pool` | `/api/a-share/special-data/limit-break-pool` | 无 | — | thscode, ticker, name, last_price(元), price_change_ratio_pct(%), open_times(次), first_seal_time, last_seal_time, turnover_ratio_pct(%), turnover(元) | 公开 |
| `a-share.special-data.limit-down-pool` | `/api/a-share/special-data/limit-down-pool` | 无 | — | thscode, ticker, name, last_price(元), price_change_ratio_pct(%), first_limit_time, last_limit_time, turnover_ratio_pct(%) | 公开 |
| `a-share.special-data.limit-up-ladder` | `/api/a-share/special-data/limit-up-ladder` | 无 | — | timestamp(毫秒戳), window | 公开 |
| `a-share.special-data.limit-up-pool` | `/api/a-share/special-data/limit-up-pool` | 无 | — | timestamp(毫秒戳) | 公开 |
| `a-share.special-data.limit-up-statistics` | `/api/a-share/special-data/limit-up-statistics` | 无 | — | timestamp(毫秒戳), trade_date, seal_rate(比例(0-1)), open_stock_count, limit_up_stock_count, touched_limit_up_stock_count, page_boards, exact_boards | 公开 |
| `a-share.special-data.skyrocket-list` | `/api/a-share/special-data/skyrocket-list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `a-share.special-data.turnover-prediction-snapshot` | `/api/a-share/special-data/turnover-prediction-snapshot` | 无 | — | timestamp(毫秒戳), total | 客户端专用（2004） |
> 涨停池字段最全：`continue_day_cnt`（连板数）、`continue_day_text`（首板/5天4板）、`seal_money`（封单额，元）、`max_seal_money`（峰值封单额，元）、`open_times`（开板次数，可 null）、`limit_up_reason`（涨停原因）。跌停/炸板池复用同表，缺失字段为空。
> 热榜 `heat` 是上游热度字符串；`rank_change` 正数表示名次上升。龙虎榜 `data.stock_items[]` 含 `buy_value/sell_value/net_value/org_net_value/hot_money_net_value`（元）与 `concept_list`。
> 涨停统计 `seal_rate` 是 0~1 的比例（不是百分数）。
> 其它上限：热榜 `limit 1..100`；异动监控 `size 1..30`；竞价机会 `size 1..20`；龙虎榜/历史热榜只支持一年内数据；涨停统计查询范围一年。

### 3.7 竞价（2）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.auction.short-term-benchmark` | `/api/a-share/auction/short-term-benchmark` | 无 | — | timestamp(毫秒戳), date, date_ms(毫秒戳) | 公开 |
| `a-share.auction.snapshot` | `/api/a-share/auction/snapshot` | thscodes | ≤100；最多 100 个 | timestamp(毫秒戳), auction_phase, data_status, total | 公开 |
> 竞价字段：`auction_pct`、`auction_turnover_pct`、`auction_yesterday_ratio_pct` 百分数原值；`auction_volume` 股；`auction_amount`/`float_market_cap` 元。

### 3.8 资金（2）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.capital-flow.historical` | `/api/a-share/capital-flow/historical` | thscode, interval, start, end | ≤7；窗口最多 7 个自然日 | timestamp(毫秒戳), thscode, interval | 客户端专用（2004） |
| `a-share.capital-flow.snapshot` | `/api/a-share/capital-flow/snapshot` | thscode | — | timestamp(毫秒戳), super_large, large, medium, small | 客户端专用（2004） |
> **外部 Key 实测 2004（客户端专用）**。实时快照四类资金对象 `inflow_amount/outflow_amount/net_amount`（元）；历史只给流入/流出，数据层补 `net_amount=inflow-outflow`。1m 窗口 ≤7 天，1d 窗口 ≤1 年。

### 3.9 高频（2）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.high-frequency.historical` | `/api/a-share/high-frequency/historical` | thscode, start, end | — | timestamp(毫秒戳), thscode, interval | 客户端专用（2004） |
| `a-share.high-frequency.intraday` | `/api/a-share/high-frequency/intraday` | thscode | — | timestamp(毫秒戳), thscode, interval | 客户端专用（2004） |
> **外部 Key 实测 2004（客户端专用）**。日线必须东八区交易日零点；分钟线须与 end 同一交易日；只能查最近 30 个 A 股交易日。

### 3.10 基金（34）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `fund.backtest.indicators` | `/api/fund/backtest/indicators` | 无 | — | id, indicator_name, indicator_code, type, value_kind, support_operation, unit, description, state_rules, create_time(毫秒戳), update_time | 公开 |
| `fund.backtest.result` | `/api/fund/backtest/result` | thscode, buy_conditions, sell_conditions, buy_frequency_type, max_buy_times, per_buy_amount | — | start_date, end_date, metrics, trade_count, trades, curve_points | 公开 |
| `fund.companies.detail` | `/api/fund/companies/detail` | company_id | — | company_id, company_name, company_type, established_date_ms, fund_count, scale | 公开 |
| `fund.corporate-actions.dividends` | `/api/fund/corporate-actions/dividends` | thscode | — | timestamp(毫秒戳), dividend_count, dividend_total | 公开 |
| `fund.diagnostics.detail` | `/api/fund/diagnostics/detail` | thscode | — | thscode, ticker, fund_type, peer_code, dimensions, peer_dimensions, probabilities, ranges, resilience, peer_resilience | 公开 |
| `fund.etf-flow.net-inflow-historical` | `/api/fund/etf-flow/net-inflow-historical` | 无 | — | thscode, report | 客户端专用（2004） |
| `fund.financials.balance-sheets` | `/api/fund/financials/balance-sheets` | thscode | — | start_date_ms, end_date_ms, publish_date_ms, total_assets(元), bank_deposit, fund_investment, stock_investment, bond_investment, transactional_financial_assets, other_assets, total_liability, other_liability | 公开 |
| `fund.financials.income-statements` | `/api/fund/financials/income-statements` | thscode | — | start_date_ms, end_date_ms, publish_date_ms, income, total_income, investment_income, stock_investment_income, bond_investment_income, fund_investment_income, dividend_income, interest_income, fair_value_income | 公开 |
| `fund.financials.indicators` | `/api/fund/financials/indicators` | thscode | — | start_date_ms, end_date_ms, publish_date_ms, distribution_profit, current_profit, current_income, distribution_share_profit, average_nav_profit_margin, average_share_current_profit, share_nav, sum_share_nav, asset_nav | 公开 |
| `fund.holders.detail` | `/api/fund/holders/detail` | thscode | — | merge_scope, report_date_ms, ins_position, holder_amount, avg_holder_share, psnl_rate, mgmt_staff_hold_rate | 公开 |
| `fund.holders.top` | `/api/fund/holders/top` | thscode | ≤10 | timestamp(毫秒戳), limit | 公开 |
| `fund.managers.detail` | `/api/fund/managers/detail` | manager_id | — | manager_id, manager_name, sex, degree, company_id, company_name, resume, photo_url, annual_return_pct, maximum_return_pct, radar_comparison | 公开 |
| `fund.managers.experience` | `/api/fund/managers/experience` | manager_id | — | awards, heavy_assets, investment_history | 公开 |
| `fund.managers.investment-style` | `/api/fund/managers/investment-style` | manager_id | — | representative_fund_thscode, representative_fund_ticker, representative_fund_name, investment_idea, total_fund_scale, industry_preferences | 公开 |
| `fund.managers.performance` | `/api/fund/managers/performance` | manager_id, range | — | date_ms(毫秒戳), manager_return_pct, peer_return_pct, benchmark_return_pct | 公开 |
| `fund.market.historical` | `/api/fund/market/historical` | thscode, start, end | 窗口最长 5 个自然年 | timestamp(毫秒戳), thscode, interval, adjust | 公开 |
| `fund.market.historical-batch` | `/api/fund/market/historical-batch` | thscodes, start, end | ≤10；最多 10 个 | interval, adjust | 公开 |
| `fund.market.snapshot` | `/api/fund/market/snapshot` | thscode | ≤100 | thscode, ticker, last_price(元), open_price, high_price, low_price, prev_price, price_change_ratio_pct(%), price_change, price_amplitude_ratio_pct, volume(股), turnover(元) | 公开 |
| `fund.news.article-list` | `/api/fund/news/article-list` | thscode | — | timestamp(毫秒戳), limit, offset, has_more | 公开 |
| `fund.offerings.list` | `/api/fund/offerings/list` | subscribe | — | thscode, ticker, subscription_start_ms, subscription_end_ms | 公开 |
| `fund.performance.drawdowns` | `/api/fund/performance/drawdowns` | thscode | — | thscode, ticker, week, month, tmonth, hyear, year, twoyear, tyear, fyear, nowyear, now | 公开 |
| `fund.performance.indicators-historical` | `/api/fund/performance/indicators-historical` | thscode, start, end | — | date_ms(毫秒戳), rsi_pct, donchian_channel, track_index_pe_ttm_five_year_percentile | 公开 |
| `fund.performance.nav` | `/api/fund/performance/nav` | thscode | — | nav_date, unit_nav(元), adj_nav(元) | 公开 |
| `fund.performance.returns` | `/api/fund/performance/returns` | thscode | — | return_month, return_week, return_tmonth, return_hyear, return_year, return_twoyear, return_tyear, return_fyear, return_nowyear, return_now, peer_average_week, peer_average_month | 公开 |
| `fund.portfolio.asset-allocation` | `/api/fund/portfolio/asset-allocation` | thscode | — | report_date_ms, stock_ratio_pct, bond_ratio_pct, deposit_ratio_pct, other_ratio_pct | 公开 |
| `fund.portfolio.bond-history` | `/api/fund/portfolio/bond-history` | thscode, report_type, end_date | — | 见官方字段表 | 公开 |
| `fund.portfolio.bond-report-dates` | `/api/fund/portfolio/bond-report-dates` | thscode | — | 见官方字段表 | 公开 |
| `fund.portfolio.holdings` | `/api/fund/portfolio/holdings` | thscode | — | thscode, ticker, stock_name, hold_ratio, asset_type, position_capital, position_count, security_market_value_rate_pct, period_increase_rate_pct, investment_rank, start_date_ms, end_date_ms | 公开 |
| `fund.portfolio.industry-allocation` | `/api/fund/portfolio/industry-allocation` | thscode | — | report_period, industry_name, ratio_pct | 公开 |
| `fund.portfolio.stock-history` | `/api/fund/portfolio/stock-history` | thscode, report_type, end_date | — | thscode, ticker, name, asset_type, hold_ratio, market_value, period_increase_pct, rank(名次), report_type, end_date_ms | 公开 |
| `fund.portfolio.stock-report-dates` | `/api/fund/portfolio/stock-report-dates` | thscode | — | report_type, report_type_name, start_date_ms, end_date_ms | 公开 |
| `fund.profile.detail` | `/api/fund/profile/detail` | thscode | — | thscode, ticker, fund_name, estab_date, company_id, mgmt_name, manager_name, fund_scale, unit_nav(元), manager_info, trade_rule, rate_info | 公开 |
| `fund.quota.list` | `/api/fund/quota/list` | tab | — | name, sub_tab | 公开 |
| `fund.quota.summary` | `/api/fund/quota/summary` | tab | — | name, unlimited, total_limit, total, buy | 公开 |
> 基金代码用完整后缀（`.OF` 场外、`.SH/.SZ` 场内）。净值 `unit_nav/adj_nav` 元；收益率/占比百分数原值；规模元。`quota/summary`、`quota/list` 的 `tab` 是 **JSON 数组字符串**（如 `["nazhi100"]`）。`backtest/result` 参数复杂，工具只做透传，前端不要直接暴露给普通用户。

### 3.11 期货（21）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `futures.basis.historical` | `/api/futures/basis/historical` | thscode | — | timestamp(毫秒戳) | 公开 |
| `futures.basis.main-continuous-latest` | `/api/futures/basis/main-continuous-latest` | 无 | — | timestamp(毫秒戳), thscode, ticker, name, variety_name, reference_site, updated_at, spot_publish_date, spot_indicator_id, default_value, spot_price, converted_spot_price | 公开 |
| `futures.calendar.session-timeline` | `/api/futures/calendar/session-timeline` | thscode | — | timestamp(毫秒戳), thscode, trade_date, timezone, trade_stages, exchange_sessions | 公开 |
| `futures.calendar.trading-schedule` | `/api/futures/calendar/trading-schedule` | thscode, start_date, end_date | — | timestamp(毫秒戳), thscode, timezone, trade_dates, regular_schedules, special_schedules, daylight_saving_periods | 公开 |
| `futures.contracts.commodity-index-list` | `/api/futures/contracts/commodity-index-list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `futures.contracts.detail` | `/api/futures/contracts/detail` | thscode | — | timestamp(毫秒戳), thscode, ticker, name, variety_code, variety_name, exchange_code, list_date, end_date, last_trade_date, last_delivery_date | 公开 |
| `futures.contracts.list` | `/api/futures/contracts/list` | 无 | — | timestamp(毫秒戳), total | 公开 |
| `futures.contracts.main-continuous-list` | `/api/futures/contracts/main-continuous-list` | 无 | — | timestamp(毫秒戳), thscode, ticker, name, variety_code, variety_name, exchange_code, minute_visit_count, kline_visit_count, statistics_date | 公开 |
| `futures.contracts.main-list` | `/api/futures/contracts/main-list` | 无 | — | timestamp(毫秒戳), thscode, ticker, name, variety_code, variety_name, exchange_code, list_date, end_date, last_trade_date, last_delivery_date, margin_rate | 公开 |
| `futures.contracts.secondary-main-list` | `/api/futures/contracts/secondary-main-list` | 无 | — | 见官方字段表 | 公开 |
| `futures.fundamentals.indicators-historical` | `/api/futures/fundamentals/indicators-historical` | indicator_ids, start_date, end_date | — | timestamp(毫秒戳) | 客户端专用（2004） |
| `futures.positions.company-list` | `/api/futures/positions/company-list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `futures.positions.company-variety-daily` | `/api/futures/positions/company-variety-daily` | date, varieties | — | timestamp(毫秒戳), date, variety_code, company_name, volume(股), volume_change, long_position, long_position_change, short_position, short_position_change, net_position, net_position_change | 公开 |
| `futures.positions.contract-daily` | `/api/futures/positions/contract-daily` | thscode, variety, date | — | timestamp(毫秒戳), date, position_item, average_item | 公开 |
| `futures.positions.contract-historical` | `/api/futures/positions/contract-historical` | thscode, variety, company, start_date | — | timestamp(毫秒戳), start_date, end_date, position_item, average_item | 公开 |
| `futures.positions.variety-daily` | `/api/futures/positions/variety-daily` | date | — | timestamp(毫秒戳), date, variety_code, variety_name, volume(股), volume_change, long_position, long_position_change, short_position, short_position_change, net_position, net_position_change | 公开 |
| `futures.prices.daily` | `/api/futures/prices/daily` | thscode | — | timestamp(毫秒戳), thscode, interval | 公开；上游暂不可用 |
| `futures.prices.intraday` | `/api/futures/prices/intraday` | thscode | — | timestamp(毫秒戳), thscode, date, session | 公开 |
| `futures.varieties.list` | `/api/futures/varieties/list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `futures.variety-plates.list` | `/api/futures/variety-plates/list` | 无 | — | timestamp(毫秒戳) | 公开 |
| `futures.warehouse-receipts.historical` | `/api/futures/warehouse-receipts/historical` | thscode, start_date, end_date | — | timestamp(毫秒戳) | 公开 |
> 价格单位随合约；持仓/成交单位手；基差字段见官方。`positions` 类参数必须成对（thscode+variety，日期格式 `YYYY-MM-DD`）。`fundamentals/*` 客户端专用。

### 3.12 期权（6）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `options.calendar.session-timeline` | `/api/options/calendar/session-timeline` | thscode | — | 见官方字段表 | 公开 |
| `options.contracts.detail` | `/api/options/contracts/detail` | thscode | — | timestamp(毫秒戳), thscode, ticker, display_code, name, variety_code, pinyin, list_date, end_date, last_trade_date, last_delivery_date, margin_rate | 公开 |
| `options.contracts.list` | `/api/options/contracts/list` | 无 | — | timestamp(毫秒戳), total | 公开 |
| `options.prices.daily` | `/api/options/prices/daily` | thscode | — | timestamp(毫秒戳), thscode, interval | 公开；上游暂不可用 |
| `options.prices.intraday` | `/api/options/prices/intraday` | thscode | — | timestamp(毫秒戳), thscode, date, session | 公开 |
| `options.varieties.list` | `/api/options/varieties/list` | 无 | — | timestamp(毫秒戳) | 公开 |
> 与期货同构；`prices/daily` 上游实测 5003，`prices/intraday`、合约与时间轴可用。

### 3.13 资讯（8）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `news.anomaly.analysis` | `/api/news/anomaly/analysis` | begin_date, end_date | — | was_truncated | 公开 |
| `news.article.detail` | `/api/news/article/detail` | client_type, encoded_seq | — | encoded_seq, title, author, summary, content, source, publish_time_ms, is_kuaixun | 公开 |
| `news.events.search` | `/api/news/events/search` | 无 | ≤200；最多 200 个 | total | 客户端专用（2004） |
| `news.flash.list` | `/api/news/flash/list` | tag_id, seq | — | 见官方字段表 | 公开 |
| `news.flash.refresh` | `/api/news/flash/refresh` | tag_id, seq, create_time_ms | — | 见官方字段表 | 公开 |
| `news.notice.detail` | `/api/news/notice/detail` | seq | — | seq, guid, title, report_period, summary, notice_type, monthly_report_interpretation, has_findings, findings, summary_html | 公开 |
| `news.stock.timeline-list` | `/api/news/stock/timeline-list` | thscode, offset | — | has_more, offset | 公开 |
| `news.topic.hot-list` | `/api/news/topic/hot-list` | tab_id | — | next_cursor, has_more | 公开 |
> 快讯首屏 `seq="0"`；刷新 `create_time_ms` 必须是毫秒且能整除 1000；个股分时新闻首屏 `offset="first"`；`news/events/search` 客户端专用。长文本在通用层截断到 600 字。

### 3.14 新股（1）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `a-share.ipo.calendar-list` | `/api/a-share/ipo/calendar-list` | market, status | — | market, status | 公开 |
> `market=sh_sz|bj`；`status=upcoming|subscribable|awaiting_listing`。

### 3.15 港股打新（3）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `hk-share.ipo.application-list` | `/api/hk-share/ipo/application-list` | 无 | — | page, size, total, has_next | 公开 |
| `hk-share.ipo.calendar-list` | `/api/hk-share/ipo/calendar-list` | status | — | status | 公开 |
| `hk-share.ipo.issue-detail` | `/api/hk-share/ipo/issue-detail` | issue_code | — | issue_code, currency, issue_price_range, lower, upper, lot_size, entry_fee | 公开 |

### 3.16 全市场导出（3）
| 端点 ID | 路径 | 必填参数 | 上限/窗口 | 关键字段（单位） | 权限/状态 |
|---|---|---|---|---|---|
| `dump.market-dumps.adjustment-factors.download-url` | `/api/dump/market-dumps/adjustment-factors/download-url` | 无 | — | presigned_url | 公开 |
| `dump.market-dumps.daily-k-10d.download-url` | `/api/dump/market-dumps/daily-k-10d/download-url` | 无 | — | presigned_url | 公开 |
| `dump.market-dumps.daily-k.download-url` | `/api/dump/market-dumps/daily-k/download-url` | 无 | — | presigned_url | 公开 |
> 返回 `presigned_url`，有时效；下载域名 `o.thsi.cn` 不在应用网络白名单内，应由浏览器完成。

### 3.17 关键字段速查（前端按此渲染）

| 视图/端点 | 字段 | 单位/口径 |
|---|---|---|
| A 股快照 `prices/snapshot` | `thscode,ticker,last_price,price_change,price_change_ratio_pct,open_price,high_price,low_price,prev_price,volume,turnover` | 价格元；`volume` **股**；`turnover` 元；`price_change_ratio_pct` 百分数（1.74=+1.74%）；**无 name** |
| 估值 `valuations/snapshot` | `thscode,ticker,name,pe_ttm,pe_mrq,pb_mrq,ps_ttm,pcf_ttm` | 倍数（不是百分数） |
| 历史 K 线 `prices/historical(-batch)` | `date_ms,open_price,high_price,low_price,close_price,volume,turnover`；批量外层 `thscode,timestamp,item[]` | `date_ms` 毫秒戳；`volume` 股；`turnover` 元；批量合法空组 `timestamp:null,item:[]` |
| 涨停池 `special-data/limit-up-pool` | `thscode,ticker,name,is_st,is_new,last_price,price_change_ratio_pct,limit_up_time,open_times,limit_up_reason,continue_day_text,continue_day_cnt,seal_money,max_seal_money` | 价格/封单额元；涨跌幅百分数；`open_times` 可 null |
| 跌停/炸板池 | `thscode,ticker,name,last_price,price_change_ratio_pct,first_limit_time,last_limit_time,first_seal_time,last_seal_time,turnover_ratio_pct,turnover,open_times` | 时间 `HH:MM`（上海）；金额元；换手/涨跌幅百分数 |
| 涨停统计 `limit-up-statistics` | `trade_date,seal_rate,open_stock_count,limit_up_stock_count,touched_limit_up_stock_count,page_boards,exact_boards` | `seal_rate` **0~1 比例**；家数为整数 |
| 热榜 `hot-stock-list`/`skyrocket-list` | `thscode,ticker,name,rank,heat,rank_change,rank_trend` | `heat` 热度字符串；`rank_change` 正数=名次上升 |
| 历史热榜/排名走势 | `date,date_ms,rank`（走势还含 `thscode,ticker`） | 日期 `YYYY-MM-DD`；`date_ms` 毫秒戳 |
| 龙虎榜 `dragon-tiger-list` | `stock_items[]`：`thscode,ticker,name,buy_value,sell_value,net_value,org_net_value,hot_money_net_value,net_rate,change,range_days,limit_reason,concept_list`；`hot_money_items[]` | 金额元；比例/涨跌幅百分数；`concept_list` 字符串数组 |
| 集合竞价 `auction/snapshot` | `auction_phase,data_status,total,item[]:{thscode,ticker,name,auction_price,auction_pct,auction_volume,auction_amount,auction_unmatched,auction_turnover_pct,auction_yesterday_ratio_pct,auction_volume_ratio,pre_close_price,open_price,last_price,float_market_cap}` | `auction_volume` 股；金额元；三个 pct 为百分数 |
| 资金流快照 `capital-flow/snapshot` | `timestamp,super_large{inflow_amount,outflow_amount,net_amount},large,medium,small` | 元；客户端专用 2004 |
| 资金流历史 | `date_ms,super_large{inflow_amount,outflow_amount},large,medium,small` | 元；量层补 `net_amount` |
| 三大报表 | `period,fiscal_year,fiscal_period,report_date_ms,operating_income,operating_costs,operating_profit,profit_total,net_profit,parent_holder_net_profit,assets_total,total_debt,holder_equity_total,act_cash_flow_net,...` | 金额元；`report_date_ms` 毫秒戳 |
| 财务指标 `financials/indicators` | `report,abilities[]:{ability,indicators[]:{index_id,value}}` | `value` 原始字符串；百分比指标百分数；缺失 null |
| 交易日历 | `date_ms,date` | `date` yyyyMMdd；`date_ms` 当日零点 |
| 标的检索/列表 | `thscode,ticker,name,exchange,asset_type,...` | `asset_type` 见 §3.3 枚举 |
| 指数目录/成分 | 目录 `thscode,name`；成分 `thscode,name`（部分含 `weight`） | 指数 thscode 后缀 `.TI`/`.SH`/`.SZ` |
| 指数快照 | 同 A 股快照字段 | 成交量按指数口径（股），不要与个股直接比较 |
| 基金资料 `fund/profile/detail` | `thscode,ticker,fund_name,fund_type,company_id,manager_id,manager_name,estab_date,fund_scale,unit_nav,rate_info,trade_rule` | 规模/净值元；客户端专用字段可能缺 |
| 基金净值/收益 | `nav_date,unit_nav,adj_nav`；收益区间字段见端点表 | 净值元；收益百分数 |
| 基金持仓 | `total_stock_ratio_pct,stock_ratio_pct,main_industry,concentration_ratio,item[]` | 占比百分数 |
| 期货品种/合约 | 品种 `variety_code,quote_code,name,exchange_code,contract_multiplier,price_unit,tick_size,trade_sessions,margin_rate,...`；合约 `thscode,ticker,name,list_date,last_trade_date,last_delivery_date,variety_code` | 乘数/最小变动价位随品种；日期 `YYYY-MM-DD` |
| 期货持仓/基差/仓单 | 见端点表字段 | 持仓手；基差随现货口径；仓单手/吨以官方为准 |
| 期权合约 | `thscode,ticker,name,variety_code,exchange_code,list_date,last_trade_date,last_delivery_date,end_date` | 日期 `YYYY-MM-DD` |
| 快讯/资讯 | `id,title,summary,content,source,publish_time,url`（字段随端点变化） | `publish_time` 毫秒戳；正文截断 600 字 |
| 打新 | `thscode,ticker,name,subscription_start_ms,subscription_end_ms,issue_price,listing_date,...` | 金额元；日期毫秒戳或 `YYYY-MM-DD`（以端点为准） |

## 4. 实现状态与权限受限清单

| 状态 | 数量 | 说明 |
|---|---|---|
| 已接入（目录 + 通用查询 + 必要强类型方法） | 125 | `fuyao.api` 可查任意端点；常用域另有 `fuyao.quote/kline/limitPool/hotRank/auction/financials/...` 强类型工具 |
| 客户端专用（2004，优雅降级） | 11 | `capital-flow/snapshot`、`capital-flow/historical`、`high-frequency/historical`、`high-frequency/intraday`、`chip-shape-dates`、`chip-shape-stocks`、`fund/etf-flow/net-inflow-historical`、`a-share-index/baseinfo/new-concepts`、`news/events/search`、`turnover-prediction-snapshot`、`futures/fundamentals/indicators-historical` |
| 上游暂不可用（5003，非权限） | 2 | `futures/prices/daily`、`options/prices/daily` |
| 无权限（2003） | 0 | 当前 Key 未观察到 2003；如后续出现按 2004 同一路径降级 |

替代口径：资金流用现有新浪 `MoneyFlow`；高频分时用腾讯/新浪分钟线；筹码/新增概念/成交额预测/ETF 申赎没有等价替代，前端应展示「客户端专用」说明；期货/期权日 K 暂时保留「上游不可用」空态。

## 5. 自选股提速：瓶颈与实测

### 5.1 瓶颈定位（代码级）

| 位置 | 旧行为 | 代价 |
|---|---|---|
| `Service.Quote` | 官方快照成功也**固定再打一次**腾讯批量（补名称/交叉校验） | 2 个串行 RTT |
| `fuyaoQuotes` | 官方结果不进缓存（缓存只覆盖降级路径 `quoteOne`） | 每次刷新都回源 |
| `saveCachedQuote` | 每只、每次刷新都写 sqlite（快照 + 名称两行） | 50 只 ≈100 次写/轮 |
| `CachedName` | 每只都读一次 store | 50 次读/轮 |
| `plugins/watchlist.list` | 本身只读 store + 缺名时一次批量补名，没有 N+1；慢的是上面的行情链路 | — |

### 5.2 方案

1. 官方批量快照：50/批 + 并发 ≤4（`fuyaoSnapshotRows`），按 `thscodes` 切分，尊重 429/4001（退避 300ms 重试一次）。
2. 两级缓存：批量键 `fuyao:snap:v2:<排序后代码集合>` + 单代码键 `fuyao:q:v2:<市场+代码>`；TTL 盘中 3s、午休 15s、非交易时段 60s；键含字段/单位版本 `v2`。
3. 名称优先读进程内缓存 → 本地 `market_cache` → 仅在仍缺名时打一次腾讯/新浪批量；官方命中时不再无条件请求腾讯。
4. 落盘节流：同一代码 30 秒内最多写一次；价格/量额未变不写。
5. 降级可观测：任一批失败打 `Warn("官方快照部分批次失败…", event=fuyao.fallback)`；`Health` 继续记录 `fuyao` 最近错误。

### 5.3 前后耗时表（httptest 注入固定 25ms 延迟，相对比较）

| 只数 | 旧链路 | 旧请求数 | 新-冷缓存 | 冷请求数 | 新-热缓存 | 热请求数 |
|---|---|---|---|---|---|---|
| 10 | 56ms | 2 | 28ms | 1 | 0ms | 0 |
| 30 | 54ms | 2 | 28ms | 1 | 0ms | 0 |
| 50 | 54ms | 2 | 29ms | 1 | 0ms | 0 |

真实网络（2026-11-13，本机，`GUANLAN_FUYAO_LIVE=1`）：50 只冷启动 **248ms**、热缓存 **0ms（0 请求）**。旧链路在真实 sqlite 下还会叠加约 100 次写盘，差距会更大。

复现：`go test ./internal/datasource -run TestWatchlistQuoteLatency -v -count=1`。

## 6. Agent 工具清单

| 工具 | 入参（必填加 *） | 返回要点 | Risk | 典型问题 |
|---|---|---|---|---|
| `fuyao.catalog` | group? / endpoint? | 125 个端点的 ID/路径/参数/权限 | network | 「官方有哪些数据？」「这个接口要什么参数？」 |
| `fuyao.api` | endpoint*, params?, limit? | 任意端点原始记录（裁剪 + 口径） | network | 长尾能力兜底 |
| `fuyao.quote` | codes* ≤50 | last_price/涨跌幅/volume(股)/turnover(元) | network | 「这几只现在什么价？」 |
| `fuyao.kline` | codes* ≤10, interval, adjust, asset, start/end, limit | 每组 thscode + bars[] | network | 「近一年走势/前复权」 |
| `fuyao.valuation` | codes* ≤100 | pe_ttm/pe_mrq/pb_mrq/ps_ttm/pcf_ttm | network | 「贵不贵/同行估值」 |
| `fuyao.limitPool` | pool, limit | 连板数、封单额、涨停原因 | network | 「今天涨停梯队」 |
| `fuyao.limitStats` | date? | 封板率、开板家数、连板家数（raw） | network | 「情绪强弱/晋级率」 |
| `fuyao.hotRank` | kind, period, limit | rank/heat/rank_change | network | 「市场在关注什么」 |
| `fuyao.special` | view*, codes/code/date/tags/shape/departmentId/limit | 异动/龙虎榜/营业部/筹码/概念/成交额 | network | 「为什么涨」「谁在买」 |
| `fuyao.capitalFlow` | code*, view, interval, start/end | 资金流四档/历史（2004 降级） | network | 「主力流入」 |
| `fuyao.auction` | view, codes/stage/date | 竞价价格/涨跌幅/量额 | network | 「开盘抢筹」 |
| `fuyao.financials` | kind, code/codes, period/report, limit | 三大报表/五类指标 | network | 「业绩如何/财务质量」 |
| `fuyao.tickerSearch` | keyword*, assetType, limit | thscode/名称/类型 | network | 「茅台代码多少」 |
| `fuyao.calendar` | start/end | 交易日 date(yyyyMMdd)+date_ms | network | 「明天开市吗」 |
| `fuyao.index` | view, tag/indexCode/codes, limit | 目录/成分股/反查/快照/K线 | network | 「这个概念有哪些票」 |
| `fuyao.fund` | view*, thscode/managerId/companyId/params | 净值/持仓/经理/诊断 | network | 「基金怎么样」 |
| `fuyao.futures` | view*, params | 品种/合约/持仓/基差/时间轴 | network | 「期货主力合约」 |
| `fuyao.options` | view*, params | 期权合约/分时/时间轴 | network | 「期权链」 |
| `fuyao.news` | view, tagId/seq/code/params | 快讯/公告/专题/个股新闻 | network | 「有什么消息」 |
| `fuyao.ipo` | view, market/status/issueCode | 打新日历/发行详情 | network | 「本周新股」 |
| `fuyao.marketDump` | kind | presigned_url | network | 「全市场历史数据下载」 |

工具统一输出：`{endpoint|view, title, count, items|series|data, dataTimestamp?, permission?, note, asOf}`。`permission=client` 表示官方 2004 降级；`note` 写明单位与时间口径。

## 7. 给前端 Agent 的接入清单

调用方式：`Invoke("fuyao", "<method>", payloadJSON)`；Wails 方法名与下表一致。目录/通用方法不发网络请求或只发一次。

| 可展示视图 | 方法 / 工具 | 入参 | 返回字段 | 建议刷新 | 空态 / 错误态语义 |
|---|---|---|---|---|---|
| 自选行情列 | `quote` / `fuyao.quote` | `{codes: string[≤50]}` | `items[].thscode/ticker/last_price/price_change_ratio_pct/volume(股)/turnover(元)` | 盘中 3s、盘后 60s（后端同 TTL） | `count=0` → 显示「—」；后端 `market.quote` 已含腾讯/新浪降级，工具失败才提示 |
| 估值列/估值卡 | `valuation` / `fuyao.valuation` | `{codes}` | `pe_ttm/pe_mrq/pb_mrq/ps_ttm/pcf_ttm/name` | 60s | 无数据 → 「—」；2004 不走这里（公开） |
| K 线/迷你走势 | `kline` / `fuyao.kline` | `{codes,interval,adjust,asset,start,end,limit}` | `series[].thscode/bars[].date/open/high/low/close/volume(股)/turnover(元)` | 日线 60s；分时另用 `capitalFlow.intraday`（2004）或现有 minute | `bars=[]` → 「无数据」；指数/基金仅 `1d` |
| 涨跌停/连板梯队 | `limitPool` / `fuyao.limitPool` | `{pool,limit}` | `continue_day_cnt/continue_day_text/seal_money(元)/max_seal_money(元)/limit_up_time/limit_up_reason/open_times` | 30s | `count=0` → 「今日无涨停」；非交易日明确标注日期 |
| 情绪/封板率 | `limitStats` / `fuyao.limitStats` | `{date?}` | `seal_rate(0~1)/open_stock_count/limit_up_stock_count/...` | 30s | 无数据 → 显示「待收盘」 |
| 热榜卡片 | `hotRank` / `fuyao.hotRank` | `{kind,period,limit}` | `rank/heat/rank_change/rank_trend` | 60s | `count=0` → 空态；`heat` 不作金额格式化 |
| 龙虎榜/异动/监控 | `special` / `fuyao.special` | `{view,codes?,date?,departmentId?}` | 见 §3.6 | 60s | 非交易日 → 最近交易日；2004 视图显示「客户端专用」 |
| 集合竞价 | `auction` / `fuyao.auction` | `{view,codes,stage,date}` | `auction_price/auction_pct/auction_volume(股)/auction_amount(元)` | 9:15–9:25 每 5s；其余静态 | `data_status=not_ready` → 「竞价未开始」 |
| 资金流卡/历史 | `capitalFlow` / `fuyao.capitalFlow` | `{code,view,interval,start,end}` | `super_large/large/medium/small` + `main_net`（元） | 30s | 2004 → 显示说明并回退到现有新浪 `flow.*` |
| 财务报表卡 | `financials` / `fuyao.financials` | `{kind,code|codes,period,report,limit}` | 报表字段（元）/指标 `abilities[].indicators[].value` | 手动/日更 | 无数据 → 「未披露」；指标 `value=null` → `--` |
| 标的搜索 | `tickerSearch` / `fuyao.tickerSearch` | `{keyword,assetType,limit}` | `thscode/ticker/name/exchange/asset_type` | 输入防抖 300ms | 无命中 → 「没有找到」 |
| 交易日历 | `calendar` / `fuyao.calendar` | `{start,end}` | `date(yyyyMMdd)/date_ms` | 1 天 | 空 → 按「非交易日」处理 |
| 指数/板块页 | `index` / `fuyao.index` | `{view,tag,indexCode,codes,limit}` | 目录 `thscode/name`；成分股 `thscode/name`；反查 `index_list` | 目录 1 天；快照 3s | 成分股空 → 「暂无成分」 |
| 基金页 | `fund` / `fuyao.fund` | `{view,thscode,managerId,companyId,params}` | 见 §3.10 | 净值日更；快照 30s | `view` 白名单见数据层；2004 展示说明 |
| 期货/期权页 | `futures`/`options` | `{view,params}` | 见 §3.11/3.12 | 合约日更；分时 30s | 日 K 5003 → 「上游暂不可用」 |
| 资讯流 | `news` / `fuyao.news` | `{view,tagId,seq,code,params}` | `items[]`（title/summary/publish_time…） | 快讯 10s；其它手动 | 空 → 「暂无资讯」；`events` 2004 |
| 打新日历 | `ipo` / `fuyao.ipo` | `{view,market,status,issueCode}` | 发行记录 | 1 天 | 空 → 「暂无新股」 |
| 长尾/调试 | `catalog`/`api` | `{group|endpoint}` / `{endpoint,params,limit}` | 目录 / 原始记录 | 手动 | 参数未知会明确报「不支持参数」 |

前端注意：
1. 不要把 `volume` 当「手」展示；官方列是股。若要沿用现有列（手），前端需 `/100` 或改调 `market.quote`（数据层已折算）。
2. `auction_pct`、`seal_rate` 口径不同（前者百分数、后者 0~1），不要共用格式化函数。
3. `fuyao.api` 的 `params` 键名大小写敏感，必须与目录一致；不要在 URL 里拼中文（数据层已 url.Encode）。
4. 自选行情刷新不要逐只调用；一次 `quote` 传全部代码（≤50），后端会走批量缓存。

## 8. 测试与验证

- 离线单测：`go test ./internal/datasource/... -count=1`（固件在 `internal/datasource/testdata/fuyao/`）。
- 竞态：`go test ./internal/datasource/... -race`。
- 计时表：`go test ./internal/datasource -run TestWatchlistQuoteLatency -v`。
- 真实采样（默认跳过）：`GUANLAN_FUYAO_LIVE=1 GUANLAN_FUYAO_API_KEY=... go test ./internal/datasource -run TestFuyaoLivedDomains -v`。
- 2026-11-13 实测返回形状：行情 11 字段、估值 8 字段、涨停池 14 字段、热榜 7 字段、竞价 15 字段、利润表 21 字段、交易日历 2 字段、指数目录 2 字段、基金资料 12 字段、期货品种 20 字段、期权合约 9 字段、龙虎榜 14 字段；主力资金返回 2004 已降级。

## 9. 风险与未覆盖项

1. **客户端专用能力（2004）**：资金流、高频、筹码、新增概念、成交额预测、ETF 申赎、资讯事件库、期货 F10 无法用外部 Key；工具已降级为说明，但「官方全量」在这些域并不成立。
2. **上游波动**：期货/期权日 K 当前 5003；`news/anomaly` 采样时也遇到 5003。前端空态必须允许「暂时取不到」。
3. **契约稳定性**：官方批量接口上限/窗口可能调整（例如单标的 K 线文档只写 1d），本层按目录限制并在超限时本地切分；若官方改上限，需同步 `fuyao_catalog.go` 与本文。
4. **名称补全**：快照不返回名称；离线首次加载且腾讯/新浪不可用时，名称可能为空（保留上次缓存）。
5. **Parquet 下载**：预签名链接有时效，且域名不在网络白名单；只返回链接，不代下载。
6. **前端尚未接入**：本轮只做数据层 + 工具 + 文档，`frontend/**` 由接力 Agent 按 §7 接入；`style.css`/`panels/**` 未改。
