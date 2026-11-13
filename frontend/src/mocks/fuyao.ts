// 同花顺官方数据（fuyao）演示数据：浏览器直开（无 Wails 注入）时让「数据源」面板的
// 全部页签都能渲染出真实形状的内容。
//
// 约定：
//   - 字段名与 docs/FUYAO-API.md §3 逐字一致（thscode/last_price/continue_day_cnt…），
//     单位也一致：volume=股、金额=元、涨跌幅=百分数原值、seal_rate=0~1 比例；
//   - 目录（catalog）的分组名与端点 ID 全部取自 docs/FUYAO-API.md，
//     只精简到每域 2–5 个常用端点，total 仍显示官方 125；
//   - 刻意演两个降级样例：资金流 = 客户端专用（2004）、期货期权日 K = 上游暂不可用（5003）；
//   - 所有数据确定性生成，截图可复现；不引入随机时间。
//
// 只在演示模式被 panels/fuyao.vue 与 api.mock 调用，真实 Wails 环境不会加载。

import { registerMock } from './registry'

/** 演示数据基准时刻（与其它面板的演示日期一致，便于对照截图）。 */
const AS_OF = '2026-11-13T15:05:00+08:00'
const TRADE_DATE = '2026-11-13'
const PREV_TRADE_DATE = '2026-11-12'

type Row = Record<string, unknown>

interface DemoStock {
  ths: string
  ticker: string
  name: string
  price: number
  pct: number
  volume: number
  turnover: number
  turnoverPct: number
  pe: number
  pb: number
  ps: number
  pcf: number
  industry: string
}

/** 演示标的：字段值与自选/市场中心演示数据保持同一套口径。 */
const STOCKS: DemoStock[] = [
  { ths: '600519.SH', ticker: '600519', name: '贵州茅台', price: 1523.4, pct: 1.24, volume: 3950084, turnover: 5.96e9, turnoverPct: 0.31, pe: 24.86, pb: 8.24, ps: 12.4, pcf: 26.1, industry: '白酒Ⅱ' },
  { ths: '600036.SH', ticker: '600036', name: '招商银行', price: 38.62, pct: -0.58, volume: 55640300, turnover: 2.15e9, turnoverPct: 0.54, pe: 6.82, pb: 0.96, ps: 2.41, pcf: 5.12, industry: '股份制银行Ⅱ' },
  { ths: '000858.SZ', ticker: '000858', name: '五粮液', price: 142.7, pct: 2.06, volume: 21430000, turnover: 3.04e9, turnoverPct: 1.12, pe: 18.34, pb: 4.12, ps: 6.02, pcf: 17.8, industry: '白酒Ⅱ' },
  { ths: '300750.SZ', ticker: '300750', name: '宁德时代', price: 246.18, pct: 3.47, volume: 27260000, turnover: 6.71e9, turnoverPct: 1.86, pe: 32.14, pb: 5.42, ps: 3.86, pcf: 21.4, industry: '电池' },
  { ths: '601318.SH', ticker: '601318', name: '中国平安', price: 54.31, pct: -1.12, volume: 53120000, turnover: 2.88e9, turnoverPct: 0.42, pe: 7.94, pb: 1.02, ps: 1.12, pcf: 6.34, industry: '保险Ⅱ' },
  { ths: '002594.SZ', ticker: '002594', name: '比亚迪', price: 268.9, pct: 0.86, volume: 19030000, turnover: 5.12e9, turnoverPct: 1.03, pe: 26.42, pb: 4.86, ps: 1.34, pcf: 18.2, industry: '乘用车' },
  { ths: '688981.SH', ticker: '688981', name: '中芯国际', price: 92.44, pct: 5.62, volume: 85720000, turnover: 7.93e9, turnoverPct: 3.24, pe: 88.62, pb: 3.24, ps: 8.62, pcf: 41.2, industry: '半导体' },
  { ths: '601899.SH', ticker: '601899', name: '紫金矿业', price: 19.87, pct: 2.31, volume: 184200000, turnover: 3.66e9, turnoverPct: 0.98, pe: 15.42, pb: 4.18, ps: 1.86, pcf: 12.4, industry: '贵金属' },
]

/** 指数：后缀 .SH/.SZ，成交量按指数口径（股），不要与个股直接比较。 */
const INDICES: Array<{ ths: string; ticker: string; name: string; price: number; pct: number; volume: number; turnover: number }> = [
  { ths: '000001.SH', ticker: '000001', name: '上证指数', price: 3418.62, pct: 0.72, volume: 4.62e10, turnover: 5.16e11 },
  { ths: '399001.SZ', ticker: '399001', name: '深证成指', price: 11026.4, pct: 1.08, volume: 5.18e10, turnover: 6.82e11 },
  { ths: '399006.SZ', ticker: '399006', name: '创业板指', price: 2287.15, pct: 1.63, volume: 1.92e10, turnover: 3.24e11 },
  { ths: '000688.SH', ticker: '000688', name: '科创50', price: 1042.77, pct: -0.44, volume: 6.42e9, turnover: 8.9e10 },
]

/** 涨停池（字段取自 docs 的涨停池字段表）。 */
const LIMIT_UP: Row[] = [
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', is_st: false, is_new: false, last_price: 28.1, price_change_ratio_pct: 10.2, limit_up_time: '09:25:00', open_times: 0, limit_up_reason: '电力+储能+国企改革', continue_day_text: '6天6板', continue_day_cnt: 6, seal_money: 3.24e8, max_seal_money: 3.86e8, turnover_ratio_pct: 1.37 },
  { thscode: '601188.SH', ticker: '601188', name: '龙江交通', is_st: false, is_new: false, last_price: 4.61, price_change_ratio_pct: 10.02, limit_up_time: '09:25:02', open_times: 3, limit_up_reason: '铁路公路+东北振兴', continue_day_text: '5天5板', continue_day_cnt: 5, seal_money: 1.4e7, max_seal_money: 2.6e7, turnover_ratio_pct: 32.44 },
  { thscode: '603366.SH', ticker: '603366', name: '日出东方', is_st: false, is_new: false, last_price: 10.88, price_change_ratio_pct: 10.01, limit_up_time: '09:25:01', open_times: 0, limit_up_reason: '厨卫电器+光伏', continue_day_text: '4天4板', continue_day_cnt: 4, seal_money: 7.8e8, max_seal_money: 8.2e8, turnover_ratio_pct: 0.71 },
  { thscode: '002708.SZ', ticker: '002708', name: '光洋股份', is_st: false, is_new: false, last_price: 24.17, price_change_ratio_pct: 10.01, limit_up_time: '09:30:12', open_times: 1, limit_up_reason: '汽车零部件+机器人', continue_day_text: '3天3板', continue_day_cnt: 3, seal_money: 2.65e8, max_seal_money: 3.1e8, turnover_ratio_pct: 3.14 },
  { thscode: '600327.SH', ticker: '600327', name: '大东方', is_st: false, is_new: false, last_price: 5.94, price_change_ratio_pct: 10, limit_up_time: '09:30:20', open_times: 2, limit_up_reason: '一般零售+医疗健康', continue_day_text: '3天3板', continue_day_cnt: 3, seal_money: 2.17e8, max_seal_money: 2.6e8, turnover_ratio_pct: 7.17 },
  { thscode: '301052.SZ', ticker: '301052', name: '果麦文化', is_st: false, is_new: false, last_price: 48.96, price_change_ratio_pct: 20, limit_up_time: '14:16:16', open_times: 6, limit_up_reason: 'AI短剧+AI视频+图书出版', continue_day_text: '首板', continue_day_cnt: 1, seal_money: 2.2e7, max_seal_money: 4.1e7, turnover_ratio_pct: 52.48 },
  { thscode: '002886.SZ', ticker: '002886', name: '沃特股份', is_st: false, is_new: false, last_price: 22.3, price_change_ratio_pct: 10.01, limit_up_time: '14:35:28', open_times: 2, limit_up_reason: 'PTFE薄膜+PCB材料+机器人', continue_day_text: '首板', continue_day_cnt: 1, seal_money: 4.88e7, max_seal_money: 6.2e7, turnover_ratio_pct: 10.95 },
  { thscode: '600261.SH', ticker: '600261', name: '阳光照明', is_st: false, is_new: false, last_price: 5.85, price_change_ratio_pct: 9.96, limit_up_time: '09:30:16', open_times: null, limit_up_reason: '照明设备+出口', continue_day_text: '首板', continue_day_cnt: 1, seal_money: 2.25e8, max_seal_money: 2.25e8, turnover_ratio_pct: 7.89 },
]

/** 跌停池（字段是涨停池的子集，缺失字段官方就是空）。 */
const LIMIT_DOWN: Row[] = [
  { thscode: '002762.SZ', ticker: '002762', name: '金发拉比', last_price: 6.12, price_change_ratio_pct: -10.0, first_limit_time: '09:31:02', last_limit_time: '14:56:11', turnover_ratio_pct: 6.84 },
  { thscode: '600225.SH', ticker: '600225', name: '卓朗科技', is_st: true, last_price: 2.34, price_change_ratio_pct: -9.86, first_limit_time: '10:12:44', last_limit_time: '13:22:08', turnover_ratio_pct: 4.12 },
  { thscode: '000982.SZ', ticker: '000982', name: '中银绒业', last_price: 1.18, price_change_ratio_pct: -10.0, first_limit_time: '13:40:20', last_limit_time: '13:40:20', turnover_ratio_pct: 9.36 },
]

/** 炸板池：含 open_times/turnover（元）。 */
const LIMIT_BREAK: Row[] = [
  { thscode: '002463.SZ', ticker: '002463', name: '沪电股份', last_price: 89.43, price_change_ratio_pct: 7.86, first_seal_time: '09:38:12', last_seal_time: '14:02:41', open_times: 4, turnover_ratio_pct: 5.62, turnover: 4.18e9 },
  { thscode: '300502.SZ', ticker: '300502', name: '新易盛', last_price: 146.2, price_change_ratio_pct: 6.42, first_seal_time: '10:12:08', last_seal_time: '13:46:20', open_times: 2, turnover_ratio_pct: 8.14, turnover: 6.24e9 },
  { thscode: '603920.SH', ticker: '603920', name: '世运电路', last_price: 39.42, price_change_ratio_pct: 5.18, first_seal_time: '09:56:30', last_seal_time: '11:02:12', open_times: 3, turnover_ratio_pct: 4.86, turnover: 1.86e9 },
  { thscode: '002384.SZ', ticker: '002384', name: '东山精密', last_price: 92.06, price_change_ratio_pct: 4.62, first_seal_time: '13:12:04', last_seal_time: '14:48:56', open_times: 1, turnover_ratio_pct: 3.42, turnover: 2.62e9 },
]

/** 热股榜（heat 是热度字符串，rank_change 正数=名次上升）。 */
const HOT_HOT: Row[] = [
  { thscode: '600715.SH', ticker: '600715', name: '文投控股', rank: 1, heat: '529823', rank_change: 2, rank_trend: 'up' },
  { thscode: '300308.SZ', ticker: '300308', name: '中际旭创', rank: 2, heat: '486210', rank_change: 1, rank_trend: 'up' },
  { thscode: '300502.SZ', ticker: '300502', name: '新易盛', rank: 3, heat: '471543', rank_change: -1, rank_trend: 'down' },
  { thscode: '002463.SZ', ticker: '002463', name: '沪电股份', rank: 4, heat: '402118', rank_change: 3, rank_trend: 'up' },
  { thscode: '688981.SH', ticker: '688981', name: '中芯国际', rank: 5, heat: '388901', rank_change: 0, rank_trend: 'flat' },
  { thscode: '002384.SZ', ticker: '002384', name: '东山精密', rank: 6, heat: '355420', rank_change: -2, rank_trend: 'down' },
  { thscode: '601127.SH', ticker: '601127', name: '赛力斯', rank: 7, heat: '341207', rank_change: 4, rank_trend: 'up' },
  { thscode: '002230.SZ', ticker: '002230', name: '科大讯飞', rank: 8, heat: '329874', rank_change: 0, rank_trend: 'flat' },
]

/** 飙升榜：排名变化更剧烈。 */
const HOT_SKYROCKET: Row[] = [
  { thscode: '301052.SZ', ticker: '301052', name: '果麦文化', rank: 1, heat: '186420', rank_change: 42, rank_trend: 'up' },
  { thscode: '600327.SH', ticker: '600327', name: '大东方', rank: 2, heat: '162318', rank_change: 36, rank_trend: 'up' },
  { thscode: '002886.SZ', ticker: '002886', name: '沃特股份', rank: 3, heat: '151204', rank_change: 28, rank_trend: 'up' },
  { thscode: '600261.SH', ticker: '600261', name: '阳光照明', rank: 4, heat: '139861', rank_change: 21, rank_trend: 'up' },
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', rank: 5, heat: '128436', rank_change: -3, rank_trend: 'down' },
]

/** 历史热股榜（date/date_ms）。 */
const HOT_HISTORY: Row[] = HOT_HOT.slice(0, 6).map((row, i) => ({
  ...row,
  date: PREV_TRADE_DATE,
  date_ms: Date.parse(PREV_TRADE_DATE + 'T00:00:00+08:00'),
  rank: i + 1,
  heat: String(320000 - i * 18000),
}))

/** 龙虎榜（stock_items 为官方字段；hot_money_items 是游资席位）。 */
const DRAGON_TIGER: Row = {
  trade_date: TRADE_DATE,
  board_type: 'all',
  count: 5,
  stock_count: 5,
  timestamp: Date.parse(AS_OF),
  stock_items: [
    { thscode: '002384.SZ', ticker: '002384', name: '东山精密', buy_value: 2.38e9, sell_value: 1.66e9, net_value: 7.22e8, org_net_value: 3.12e8, hot_money_net_value: 4.1e8, net_rate: 12.4, change: 10.0, range_days: 1, limit_reason: '日涨幅偏离值达到7%的前5只证券', concept_list: ['PCB', '苹果概念', 'AI服务器'] },
    { thscode: '002463.SZ', ticker: '002463', name: '沪电股份', buy_value: 1.4e9, sell_value: 1.01e9, net_value: 3.86e8, org_net_value: 1.86e8, hot_money_net_value: 2.0e8, net_rate: 8.6, change: 10.0, range_days: 1, limit_reason: '日涨幅偏离值达到7%的前5只证券', concept_list: ['PCB', 'AI服务器'] },
    { thscode: '603920.SH', ticker: '603920', name: '世运电路', buy_value: 9.36e8, sell_value: 5.63e8, net_value: 3.73e8, org_net_value: 1.24e8, hot_money_net_value: 2.49e8, net_rate: 6.4, change: 8.39, range_days: 3, limit_reason: '连续三个交易日涨幅偏离值累计达到20%', concept_list: ['PCB', '新能源汽车'] },
    { thscode: '688801.SH', ticker: '688801', name: '燧原科技', buy_value: 1.22e9, sell_value: 9.61e8, net_value: 2.56e8, org_net_value: 2.02e8, hot_money_net_value: 5.4e7, net_rate: 5.2, change: 11.27, range_days: 3, limit_reason: '连续3个交易日收盘涨幅偏离值累计达到30%', concept_list: ['AI芯片', '国产算力'] },
    { thscode: '000678.SZ', ticker: '000678', name: '襄阳轴承', buy_value: 1.42e8, sell_value: 2.28e8, net_value: -8.6e7, org_net_value: -2.1e7, hot_money_net_value: -6.5e7, net_rate: -4.2, change: 10.02, range_days: 1, limit_reason: '日换手率达到20%的前5只证券', concept_list: ['汽车零部件', '机器人'] },
  ],
  hot_money_items: [
    { department_id: 'D0001', department_name: '华鑫证券上海分公司', buy_value: 6.42e8, sell_value: 1.28e8, net_value: 5.14e8, stock_count: 3 },
    { department_id: 'D0002', department_name: '中国银河证券绍兴路', buy_value: 4.18e8, sell_value: 9.6e7, net_value: 3.22e8, stock_count: 2 },
    { department_id: 'D0003', department_name: '机构专用', buy_value: 3.86e8, sell_value: 1.74e8, net_value: 2.12e8, stock_count: 4 },
  ],
}

/** 个股异动（含 tag_code 与中文解释）。 */
const ANOMALY: Row[] = [
  { thscode: '300308.SZ', ticker: '300308', name: '中际旭创', trade_date: TRADE_DATE, tag_code: 'rocket', tag_name: '火箭发射', description: '5 分钟内涨幅超过 2%，成交额同步放大', timestamp: Date.parse(TRADE_DATE + 'T10:12:00+08:00') },
  { thscode: '688981.SH', ticker: '688981', name: '中芯国际', trade_date: TRADE_DATE, tag_code: 'big_buy', tag_name: '大笔买入', description: '单笔成交额超过 1000 万元', timestamp: Date.parse(TRADE_DATE + 'T10:32:00+08:00') },
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', trade_date: TRADE_DATE, tag_code: 'limit_up', tag_name: '封涨停板', description: '9:25 一字板，封单额 3.24 亿', timestamp: Date.parse(TRADE_DATE + 'T09:25:00+08:00') },
  { thscode: '002463.SZ', ticker: '002463', name: '沪电股份', trade_date: TRADE_DATE, tag_code: 'open_limit', tag_name: '打开涨停', description: '14:02 开板，开板 4 次', timestamp: Date.parse(TRADE_DATE + 'T14:02:41+08:00') },
  { thscode: '601318.SH', ticker: '601318', name: '中国平安', trade_date: TRADE_DATE, tag_code: 'big_sell', tag_name: '大笔卖出', description: '单笔成交额超过 1000 万元', timestamp: Date.parse(TRADE_DATE + 'T13:46:00+08:00') },
]

/** 异动监控（严重异动/重点监控）。 */
const ABNORMAL: Row[] = [
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', state: 'current', trade_date: TRADE_DATE, monitor_type: '严重异常波动', description: '连续 10 个交易日涨幅偏离值累计超过 100%', monitor_start: '2026-11-04', monitor_end: '2026-11-17' },
  { thscode: '603366.SH', ticker: '603366', name: '日出东方', state: 'current', trade_date: TRADE_DATE, monitor_type: '重点监控', description: '连续 4 个交易日涨停，换手率异常', monitor_start: '2026-11-10', monitor_end: '2026-11-14' },
  { thscode: '301052.SZ', ticker: '301052', name: '果麦文化', state: 'current', trade_date: TRADE_DATE, monitor_type: '重点监控', description: '日换手率达到 52.48%，触发交易所关注', monitor_start: '2026-11-13', monitor_end: '2026-11-18' },
]

/** 竞价快照（data_status=ready；panel 按 status 决定是否显示「竞价未开始」）。 */
const AUCTION: Row[] = [
  { thscode: '600519.SH', ticker: '600519', name: '贵州茅台', auction_price: 1530.0, auction_pct: 0.43, auction_volume: 126400, auction_amount: 1.94e8, auction_unmatched: -3200, auction_turnover_pct: 0.01, auction_yesterday_ratio_pct: 1.82, auction_volume_ratio: 1.64, pre_close_price: 1523.4, open_price: 1530.0, last_price: 1523.4, float_market_cap: 1.92e12 },
  { thscode: '300750.SZ', ticker: '300750', name: '宁德时代', auction_price: 250.5, auction_pct: 1.76, auction_volume: 268400, auction_amount: 6.72e7, auction_unmatched: 8600, auction_turnover_pct: 0.02, auction_yesterday_ratio_pct: 2.41, auction_volume_ratio: 2.18, pre_close_price: 246.18, open_price: 250.5, last_price: 246.18, float_market_cap: 1.08e12 },
  { thscode: '688981.SH', ticker: '688981', name: '中芯国际', auction_price: 94.8, auction_pct: 2.55, auction_volume: 512000, auction_amount: 4.85e7, auction_unmatched: 12400, auction_turnover_pct: 0.06, auction_yesterday_ratio_pct: 3.12, auction_volume_ratio: 2.86, pre_close_price: 92.44, open_price: 94.8, last_price: 92.44, float_market_cap: 3.84e11 },
  { thscode: '601318.SH', ticker: '601318', name: '中国平安', auction_price: 53.9, auction_pct: -0.75, auction_volume: 318000, auction_amount: 1.71e7, auction_unmatched: -18600, auction_turnover_pct: 0.01, auction_yesterday_ratio_pct: 0.86, auction_volume_ratio: 0.74, pre_close_price: 54.31, open_price: 53.9, last_price: 54.31, float_market_cap: 9.86e11 },
]

/** 竞价风向标（短线情绪基准）。 */
const AUCTION_BENCHMARK: Row[] = [
  { date: TRADE_DATE, date_ms: Date.parse(TRADE_DATE + 'T00:00:00+08:00'), stage: 'final', limit_up_count: 42, limit_down_count: 3, auction_up_count: 2186, auction_down_count: 1642, auction_flat_count: 312, avg_auction_pct: 0.36, yesterday_limit_up_premium_pct: 3.42, note: '昨日涨停股今日竞价溢价 3.42%' },
]

/** 竞价机会（历史筛选）。 */
const AUCTION_OPPS: Row[] = [
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', trade_date: PREV_TRADE_DATE, mode: 'strong', auction_pct: 6.42, auction_amount: 2.86e8, auction_volume_ratio: 4.12, yesterday_ratio_pct: 3.24, warn_type: '高开抢筹', result_pct: 10.2 },
  { thscode: '603366.SH', ticker: '603366', name: '日出东方', trade_date: PREV_TRADE_DATE, mode: 'strong', auction_pct: 4.18, auction_amount: 1.92e8, auction_volume_ratio: 3.26, yesterday_ratio_pct: 2.84, warn_type: '高开抢筹', result_pct: 10.01 },
  { thscode: '301052.SZ', ticker: '301052', name: '果麦文化', trade_date: PREV_TRADE_DATE, mode: 'strong', auction_pct: 3.26, auction_amount: 6.4e7, auction_volume_ratio: 2.42, yesterday_ratio_pct: 1.96, warn_type: '量比异动', result_pct: 20.0 },
]

/** 个股异动原因（按股票批量查）。 */
const ANOMALY_BY_STOCK: Row[] = [
  { thscode: '688981.SH', ticker: '688981', name: '中芯国际', trade_date: TRADE_DATE, reason: '国产替代+先进制程扩产，半导体板块资金净流入 42.6 亿', tag_name: '板块联动' },
  { thscode: '600519.SH', ticker: '600519', name: '贵州茅台', trade_date: TRADE_DATE, reason: '白酒板块反弹，机构调研密集', tag_name: '机构关注' },
  { thscode: '000858.SZ', ticker: '000858', name: '五粮液', trade_date: TRADE_DATE, reason: '白酒板块反弹，北向资金净买入 2.1 亿', tag_name: '资金流入' },
]

/** 营业部经典战绩 / 历史上榜。 */
const DEPARTMENT_RECORD: Row = {
  department_id: 'D0001',
  department_name: '华鑫证券上海分公司',
  organization_id: 'ORG-8821',
  classic_record: '近一年上榜 268 次，次日平均溢价 4.82%，成功率 62.4%',
  rank: 3,
  stock_count: 128,
  avg_premium_pct: 4.82,
  success_rate_pct: 62.4,
}
const DEPARTMENT_HISTORY: Row[] = [
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', trade_date: '2026-11-13', buy_value: 3.62e8, sell_value: 1.24e8, net_value: 2.38e8, next_day_pct: 10.02 },
  { thscode: '300308.SZ', ticker: '300308', name: '中际旭创', trade_date: '2026-11-12', buy_value: 2.18e8, sell_value: 8.6e7, net_value: 1.32e8, next_day_pct: 5.18 },
  { thscode: '002463.SZ', ticker: '002463', name: '沪电股份', trade_date: '2026-11-11', buy_value: 1.86e8, sell_value: 1.42e8, net_value: 4.4e7, next_day_pct: -1.24 },
]

/** 财务三大报表（金额单位元；report_date_ms 为毫秒戳）。 */
const INCOME: Row[] = [
  { period: '2025-4', fiscal_year: 2025, fiscal_period: 4, report_date_ms: Date.parse('2025-12-31T00:00:00+08:00'), operating_income: 1.786e11, operating_costs: 1.42e10, sales_fee: 5.12e9, manage_fee: 9.86e9, research_and_development_expenses: 1.24e9, operating_profit: 1.186e11, profit_total: 1.192e11, income_tax_expense: 2.98e10, net_profit: 8.94e10, parent_holder_net_profit: 8.62e10 },
  { period: '2025-2', fiscal_year: 2025, fiscal_period: 2, report_date_ms: Date.parse('2025-06-30T00:00:00+08:00'), operating_income: 9.12e10, operating_costs: 7.4e9, sales_fee: 2.68e9, manage_fee: 4.92e9, research_and_development_expenses: 6.2e8, operating_profit: 6.02e10, profit_total: 6.06e10, income_tax_expense: 1.52e10, net_profit: 4.54e10, parent_holder_net_profit: 4.38e10 },
  { period: '2024-4', fiscal_year: 2024, fiscal_period: 4, report_date_ms: Date.parse('2024-12-31T00:00:00+08:00'), operating_income: 1.742e11, operating_costs: 1.48e10, sales_fee: 5.46e9, manage_fee: 1.02e10, research_and_development_expenses: 1.18e9, operating_profit: 1.152e11, profit_total: 1.158e11, income_tax_expense: 2.89e10, net_profit: 8.69e10, parent_holder_net_profit: 8.31e10 },
]

const BALANCE: Row[] = [
  { period: '2025-4', report_date_ms: Date.parse('2025-12-31T00:00:00+08:00'), assets_total: 3.142e11, total_current_assets: 2.486e11, non_current_nets_total: 6.56e10, cash: 1.824e11, accounts_receivable: 1.24e9, total_debt: 5.42e10, holder_equity_total: 2.6e11 },
  { period: '2025-2', report_date_ms: Date.parse('2025-06-30T00:00:00+08:00'), assets_total: 3.02e11, total_current_assets: 2.38e11, non_current_nets_total: 6.4e10, cash: 1.74e11, accounts_receivable: 1.18e9, total_debt: 5.16e10, holder_equity_total: 2.504e11 },
]

const CASH: Row[] = [
  { period: '2025-4', report_date_ms: Date.parse('2025-12-31T00:00:00+08:00'), act_cash_flow_net: 8.62e10, invest_cash_flow_net: -1.24e10, financing_cash_flow_net: -4.86e10, pay_fixed_assets_etc_cash: 2.18e9, pay_dividends_profits_interest_cash: 4.42e10, cash_equivalents_net_addition: 2.52e10 },
  { period: '2025-2', report_date_ms: Date.parse('2025-06-30T00:00:00+08:00'), act_cash_flow_net: 4.18e10, invest_cash_flow_net: -6.2e9, financing_cash_flow_net: -2.4e10, pay_fixed_assets_etc_cash: 1.12e9, pay_dividends_profits_interest_cash: 2.18e10, cash_equivalents_net_addition: 1.16e10 },
]

/** 五类财务指标（value 为上游原始字符串，null 表示缺失）。 */
const ABILITIES: Row[] = [
  { ability: '盈利能力', indicators: [{ index_id: '净资产收益率ROE', value: '32.86' }, { index_id: '总资产报酬率ROA', value: '28.42' }, { index_id: '销售毛利率', value: '91.86' }, { index_id: '销售净利率', value: '50.06' }] },
  { ability: '成长能力', indicators: [{ index_id: '营业收入同比增长率', value: '2.53' }, { index_id: '净利润同比增长率', value: '2.88' }, { index_id: '净资产同比增长率', value: '3.82' }] },
  { ability: '偿债能力', indicators: [{ index_id: '资产负债率', value: '17.25' }, { index_id: '流动比率', value: '4.58' }, { index_id: '速动比率', value: '3.86' }] },
  { ability: '营运能力', indicators: [{ index_id: '存货周转率', value: '0.32' }, { index_id: '应收账款周转率', value: '146.2' }, { index_id: '总资产周转率', value: '0.58' }] },
  { ability: '现金能力', indicators: [{ index_id: '每股经营现金流', value: '68.42' }, { index_id: '经营现金流/净利润', value: '0.96' }, { index_id: '自由现金流', value: null }] },
]

/** 指数目录（同花顺概念/行业/地域/特色）。 */
const INDEX_CATALOG: Record<string, Array<{ thscode: string; name: string; ticker?: string }>> = {
  cn_concept: [
    { thscode: '885431.TI', name: 'AI 算力', ticker: '885431' },
    { thscode: '885442.TI', name: '国产替代', ticker: '885442' },
    { thscode: '885486.TI', name: '机器人概念', ticker: '885486' },
    { thscode: '885520.TI', name: '华为概念', ticker: '885520' },
    { thscode: '885621.TI', name: '低空经济', ticker: '885621' },
    { thscode: '885702.TI', name: '卫星互联网', ticker: '885702' },
  ],
  industry: [
    { thscode: '881121.TI', name: '半导体', ticker: '881121' },
    { thscode: '881155.TI', name: '白酒Ⅱ', ticker: '881155' },
    { thscode: '881198.TI', name: '电池', ticker: '881198' },
    { thscode: '881256.TI', name: '证券Ⅱ', ticker: '881256' },
  ],
  region: [
    { thscode: '882001.TI', name: '北京板块', ticker: '882001' },
    { thscode: '882002.TI', name: '上海板块', ticker: '882002' },
    { thscode: '882012.TI', name: '广东板块', ticker: '882012' },
  ],
  tszs: [
    { thscode: '884001.TI', name: '同花顺特色指数', ticker: '884001' },
    { thscode: '884012.TI', name: '高股息精选', ticker: '884012' },
  ],
}

/** 基金演示：一只场外主动权益 + 一只 ETF。 */
const FUND = { ths: '110011.OF', ticker: '110011', name: '易方达中小盘混合' }

const FUND_PROFILE: Row = {
  thscode: FUND.ths,
  ticker: FUND.ticker,
  fund_name: FUND.name,
  fund_type: '混合型',
  company_id: 'C0001',
  company_name: '易方达基金',
  manager_id: 'M0001',
  manager_name: '张坤',
  estab_date: '2008-06-19',
  fund_scale: 3.86e10,
  unit_nav: 7.842,
  trade_rule: 'T+1 确认，持有 7 天内赎回费 1.5%',
  rate_info: '管理费 1.2%/年，托管费 0.25%/年',
}

const FUND_NAV: Row[] = [
  { nav_date: '2026-11-13', unit_nav: 7.842, adj_nav: 9.126 },
  { nav_date: '2026-11-12', unit_nav: 7.796, adj_nav: 9.08 },
  { nav_date: '2026-11-11', unit_nav: 7.812, adj_nav: 9.096 },
  { nav_date: '2026-11-10', unit_nav: 7.768, adj_nav: 9.052 },
  { nav_date: '2026-11-07', unit_nav: 7.724, adj_nav: 9.008 },
]

const FUND_RETURNS: Row[] = [
  { return_week: 0.86, return_month: 2.14, return_tmonth: 6.42, return_hyear: 12.84, return_year: 18.62, return_twoyear: 24.18, return_tyear: 36.42, return_fyear: 52.16, return_nowyear: 18.62, peer_average_week: 0.42, peer_average_month: 1.68 },
]

const FUND_HOLDINGS: Row[] = [
  { ticker: '600519', stock_name: '贵州茅台', hold_ratio: 9.86, asset_type: '股票', position_capital: 3.806e9, position_count: 2498000, period_increase_rate_pct: 1.24, investment_rank: 1 },
  { ticker: '000858', stock_name: '五粮液', hold_ratio: 8.42, asset_type: '股票', position_capital: 3.248e9, position_count: 22760000, period_increase_rate_pct: -0.86, investment_rank: 2 },
  { ticker: '600036', stock_name: '招商银行', hold_ratio: 7.18, asset_type: '股票', position_capital: 2.771e9, position_count: 71750000, period_increase_rate_pct: 2.42, investment_rank: 3 },
  { ticker: '300750', stock_name: '宁德时代', hold_ratio: 6.24, asset_type: '股票', position_capital: 2.408e9, position_count: 9780000, period_increase_rate_pct: 3.16, investment_rank: 4 },
  { ticker: '601318', stock_name: '中国平安', hold_ratio: 4.86, asset_type: '股票', position_capital: 1.876e9, position_count: 34540000, period_increase_rate_pct: -1.12, investment_rank: 5 },
]

const FUND_ALLOCATION: Row[] = [
  { report_date_ms: Date.parse('2026-09-30T00:00:00+08:00'), stock_ratio_pct: 88.6, bond_ratio_pct: 2.4, deposit_ratio_pct: 6.8, other_ratio_pct: 2.2 },
  { report_date_ms: Date.parse('2026-06-30T00:00:00+08:00'), stock_ratio_pct: 86.2, bond_ratio_pct: 3.1, deposit_ratio_pct: 8.4, other_ratio_pct: 2.3 },
]

const FUND_MANAGER: Row = {
  manager_id: 'M0001',
  manager_name: '张坤',
  company_name: '易方达基金',
  degree: '硕士',
  annual_return_pct: 16.42,
  maximum_return_pct: 128.6,
  investment_idea: '长期持有高质量公司，重视自由现金流与护城河',
  representative_fund_name: FUND.name,
  total_fund_scale: 8.62e10,
}

const FUND_PERFORMANCE: Row[] = [
  { date_ms: Date.parse('2026-09-30T00:00:00+08:00'), manager_return_pct: 12.42, peer_return_pct: 8.16, benchmark_return_pct: 6.42 },
  { date_ms: Date.parse('2026-06-30T00:00:00+08:00'), manager_return_pct: 8.16, peer_return_pct: 5.42, benchmark_return_pct: 4.18 },
  { date_ms: Date.parse('2026-03-31T00:00:00+08:00'), manager_return_pct: 4.86, peer_return_pct: 3.12, benchmark_return_pct: 2.64 },
]

/** 期货品种（乘数/最小变动随品种）。 */
const FUTURES_VARIETIES: Row[] = [
  { variety_code: 'RB', quote_code: 'RB', name: '螺纹钢', exchange_code: 'SHFE', contract_multiplier: 10, price_unit: '元/吨', tick_size: 1, trade_sessions: '21:00-23:00,09:00-15:00', margin_rate: 0.11 },
  { variety_code: 'CU', quote_code: 'CU', name: '沪铜', exchange_code: 'SHFE', contract_multiplier: 5, price_unit: '元/吨', tick_size: 10, trade_sessions: '21:00-01:00,09:00-15:00', margin_rate: 0.12 },
  { variety_code: 'IF', quote_code: 'IF', name: '沪深300股指', exchange_code: 'CFFEX', contract_multiplier: 300, price_unit: '点', tick_size: 0.2, trade_sessions: '09:30-11:30,13:00-15:00', margin_rate: 0.12 },
  { variety_code: 'M', quote_code: 'M', name: '豆粕', exchange_code: 'DCE', contract_multiplier: 10, price_unit: '元/吨', tick_size: 1, trade_sessions: '21:00-23:00,09:00-15:00', margin_rate: 0.09 },
]

const FUTURES_CONTRACTS: Row[] = [
  { thscode: 'RB2605.SHF', ticker: 'RB2605', name: '螺纹钢2605', variety_code: 'RB', variety_name: '螺纹钢', exchange_code: 'SHFE', list_date: '2025-05-16', end_date: '2026-05-15', last_trade_date: '2026-05-15', last_delivery_date: '2026-05-20', margin_rate: 0.11 },
  { thscode: 'CU2603.SHF', ticker: 'CU2603', name: '沪铜2603', variety_code: 'CU', variety_name: '沪铜', exchange_code: 'SHFE', list_date: '2025-03-17', end_date: '2026-03-16', last_trade_date: '2026-03-16', last_delivery_date: '2026-03-20', margin_rate: 0.12 },
  { thscode: 'IF2612.CFX', ticker: 'IF2612', name: '沪深300股指2612', variety_code: 'IF', variety_name: '沪深300股指', exchange_code: 'CFFEX', list_date: '2025-12-22', end_date: '2026-12-18', last_trade_date: '2026-12-18', last_delivery_date: '2026-12-18', margin_rate: 0.12 },
]

const FUTURES_MAIN: Row[] = [
  { thscode: 'RB2605.SHF', ticker: 'RB2605', name: '螺纹钢2605', variety_code: 'RB', variety_name: '螺纹钢', exchange_code: 'SHFE', statistics_date: TRADE_DATE, minute_visit_count: 128640, kline_visit_count: 86240 },
  { thscode: 'CU2603.SHF', ticker: 'CU2603', name: '沪铜2603', variety_code: 'CU', variety_name: '沪铜', exchange_code: 'SHFE', statistics_date: TRADE_DATE, minute_visit_count: 96420, kline_visit_count: 62180 },
  { thscode: 'IF2612.CFX', ticker: 'IF2612', name: '沪深300股指2612', variety_code: 'IF', variety_name: '沪深300股指', exchange_code: 'CFFEX', statistics_date: TRADE_DATE, minute_visit_count: 74200, kline_visit_count: 51840 },
]

const FUTURES_POSITIONS: Row[] = [
  { variety_code: 'RB', variety_name: '螺纹钢', date: TRADE_DATE, volume: 2864000, volume_change: 128400, long_position: 1642000, long_position_change: 86200, short_position: 1726000, short_position_change: 94200, net_position: -84000, net_position_change: -8000 },
  { variety_code: 'CU', variety_name: '沪铜', date: TRADE_DATE, volume: 486200, volume_change: -18200, long_position: 268400, long_position_change: 6400, short_position: 248600, short_position_change: -4200, net_position: 19800, net_position_change: 10600 },
  { variety_code: 'M', variety_name: '豆粕', date: TRADE_DATE, volume: 1864200, volume_change: 64200, long_position: 1024000, long_position_change: 28600, short_position: 984000, short_position_change: 12400, net_position: 40000, net_position_change: 16200 },
]

const FUTURES_BASIS: Row[] = [
  { thscode: 'RB2605.SHF', ticker: 'RB2605', name: '螺纹钢2605', variety_name: '螺纹钢', reference_site: '上海', spot_publish_date: TRADE_DATE, spot_price: 3286, converted_spot_price: 3286, basis: 42, basis_rate_pct: 1.28 },
  { thscode: 'CU2603.SHF', ticker: 'CU2603', name: '沪铜2603', variety_name: '沪铜', reference_site: '上海有色', spot_publish_date: TRADE_DATE, spot_price: 72840, converted_spot_price: 72840, basis: -186, basis_rate_pct: -0.25 },
]

const FUTURES_SESSION: Row[] = [
  { thscode: 'RB2605.SHF', trade_date: TRADE_DATE, timezone: 'Asia/Shanghai', trade_stages: '夜盘 21:00-23:00；日盘 09:00-10:15、10:30-11:30、13:30-15:00', exchange_sessions: 'SHFE' },
]

const FUTURES_INTRADAY: Row[] = [
  { thscode: 'RB2605.SHF', date: TRADE_DATE, session: 'day', timestamp: Date.parse(TRADE_DATE + 'T09:00:00+08:00'), price: 3284, volume: 12400, open_interest: 1642000 },
  { thscode: 'RB2605.SHF', date: TRADE_DATE, session: 'day', timestamp: Date.parse(TRADE_DATE + 'T10:30:00+08:00'), price: 3292, volume: 18600, open_interest: 1652000 },
  { thscode: 'RB2605.SHF', date: TRADE_DATE, session: 'day', timestamp: Date.parse(TRADE_DATE + 'T14:00:00+08:00'), price: 3328, volume: 24200, open_interest: 1642000 },
]

const OPTIONS_VARIETIES: Row[] = [
  { variety_code: 'IO', name: '沪深300股指期权', exchange_code: 'CFFEX', contract_multiplier: 100, price_unit: '点', tick_size: 0.2 },
  { variety_code: 'SR', name: '白糖期权', exchange_code: 'CZCE', contract_multiplier: 10, price_unit: '元/吨', tick_size: 0.5 },
]

const OPTIONS_CONTRACTS: Row[] = [
  { thscode: 'IO2612-C-4000.CFX', ticker: 'IO2612-C-4000', display_code: 'IO2612-C-4000', name: '沪深300购2612-4000', variety_code: 'IO', list_date: '2025-12-22', end_date: '2026-12-18', last_trade_date: '2026-12-18', last_delivery_date: '2026-12-18', margin_rate: 0.12 },
  { thscode: 'IO2612-P-3800.CFX', ticker: 'IO2612-P-3800', display_code: 'IO2612-P-3800', name: '沪深300沽2612-3800', variety_code: 'IO', list_date: '2025-12-22', end_date: '2026-12-18', last_trade_date: '2026-12-18', last_delivery_date: '2026-12-18', margin_rate: 0.12 },
]

const OPTIONS_SESSION: Row[] = [
  { thscode: 'IO2612-C-4000.CFX', trade_date: TRADE_DATE, timezone: 'Asia/Shanghai', trade_stages: '09:30-11:30；13:00-15:00', exchange_sessions: 'CFFEX' },
]

/** 快讯（publish_time 毫秒戳；正文在通用层会被截断）。 */
const NEWS_FLASH: Row[] = [
  { id: 'F1', seq: '8842', title: '央行公开市场净投放 2860 亿元', summary: '为维护银行体系流动性合理充裕，央行开展 7 天期逆回购操作 2860 亿元，当日无到期。', source: '同花顺快讯', publish_time: Date.parse('2026-11-13T14:32:00+08:00'), is_kuaixun: true },
  { id: 'F2', seq: '8841', title: '半导体设备进口额同比增长 18.6%', summary: '10 月半导体设备进口额 42.8 亿美元，同比 +18.6%，先进制程扩产带动设备需求。', source: '同花顺快讯', publish_time: Date.parse('2026-11-13T13:48:00+08:00'), is_kuaixun: true },
  { id: 'F3', seq: '8840', title: '沪深两市成交额突破 1.6 万亿', summary: '截至 14:00，沪深两市成交额 1.62 万亿元，较上一交易日同期放量 12.4%。', source: '同花顺快讯', publish_time: Date.parse('2026-11-13T14:02:00+08:00'), is_kuaixun: true },
  { id: 'F4', seq: '8839', title: '北向资金今日净买入 86.4 亿元', summary: '沪股通净买入 42.6 亿元，深股通净买入 43.8 亿元，连续 3 日净流入。', source: '同花顺快讯', publish_time: Date.parse('2026-11-13T11:36:00+08:00'), is_kuaixun: true },
]

const NEWS_NOTICE: Row = {
  seq: 'N2048',
  title: '贵州茅台：2025 年第三季度报告',
  report_period: '2025Q3',
  notice_type: '定期报告',
  summary: '前三季度实现营业收入 1268.6 亿元，同比 +6.2%；归母净利润 632.4 亿元，同比 +6.8%。',
  has_findings: false,
  publish_time: Date.parse('2026-10-28T16:42:00+08:00'),
}

const NEWS_TOPIC: Row[] = [
  { tab_id: 1, title: 'AI 算力产业链持续升温', summary: '光模块、PCB、服务器代工订单能见度提升，机构上调明年出货预期。', source: '同花顺专题', publish_time: Date.parse('2026-11-13T10:12:00+08:00') },
  { tab_id: 1, title: '国产替代主线扩散', summary: '半导体设备、材料、EDA 多点开花，政策与订单共振。', source: '同花顺专题', publish_time: Date.parse('2026-11-13T09:48:00+08:00') },
]

const NEWS_ANOMALY: Row[] = [
  { thscode: '688981.SH', ticker: '688981', name: '中芯国际', trade_date: TRADE_DATE, reason: '先进制程扩产叠加国产替代，盘中一度涨 7.2%', begin_date: TRADE_DATE, end_date: TRADE_DATE },
  { thscode: '002310.SZ', ticker: '002310', name: '东方新能', trade_date: TRADE_DATE, reason: '电力+储能+国企改革，6 连板', begin_date: TRADE_DATE, end_date: TRADE_DATE },
]

const NEWS_TIMELINE: Row[] = [
  { seq: 'S1', title: '中芯国际：先进制程产能利用率回升至 92%', summary: '公司在业绩说明会上表示，四季度产能利用率环比继续改善。', source: '同花顺', publish_time: Date.parse('2026-11-13T15:02:00+08:00') },
  { seq: 'S2', title: '中芯国际获机构调研', summary: '3 家公募与 2 家外资机构参与调研，关注扩产节奏与资本开支。', source: '同花顺', publish_time: Date.parse('2026-11-12T17:26:00+08:00') },
]

const IPO_A: Row[] = [
  { thscode: '301668.SZ', ticker: '301668', name: '宏远精密', market: 'sh_sz', status: 'subscribable', subscription_start_ms: Date.parse('2026-11-13T09:15:00+08:00'), subscription_end_ms: Date.parse('2026-11-13T15:00:00+08:00'), issue_price: 28.6, listing_date: '2026-11-21', issue_pe: 22.4 },
  { thscode: '688812.SH', ticker: '688812', name: '星澜微电', market: 'sh_sz', status: 'upcoming', subscription_start_ms: Date.parse('2026-11-17T09:15:00+08:00'), subscription_end_ms: Date.parse('2026-11-17T15:00:00+08:00'), issue_price: 42.18, listing_date: '2026-11-26', issue_pe: 31.6 },
  { thscode: '920118.BJ', ticker: '920118', name: '北科智造', market: 'bj', status: 'awaiting_listing', subscription_start_ms: Date.parse('2026-11-06T09:15:00+08:00'), subscription_end_ms: Date.parse('2026-11-06T15:00:00+08:00'), issue_price: 12.4, listing_date: '2026-11-18', issue_pe: 18.2 },
]

const IPO_HK_CALENDAR: Row[] = [
  { issue_code: 'HK0988', name: '云脉智能', status: 'subscribable', subscription_start_ms: Date.parse('2026-11-12T09:00:00+08:00'), subscription_end_ms: Date.parse('2026-11-17T12:00:00+08:00'), listing_date: '2026-11-24', currency: 'HKD', issue_price_range: '18.80-22.60', lot_size: 200, entry_fee: 4520 },
  { issue_code: 'HK0992', name: '澜海生物', status: 'upcoming', subscription_start_ms: Date.parse('2026-11-20T09:00:00+08:00'), subscription_end_ms: Date.parse('2026-11-25T12:00:00+08:00'), listing_date: '2026-12-02', currency: 'HKD', issue_price_range: '9.60-12.40', lot_size: 500, entry_fee: 6260 },
]

const IPO_HK_APPLICATION: Row[] = [
  { issue_code: 'HK0988', name: '云脉智能', apply_status: '认购中', apply_amount: 1.86e9, margin_ratio: 0.1, sponsor: '中金公司' },
  { issue_code: 'HK0992', name: '澜海生物', apply_status: '待申购', apply_amount: 0, margin_ratio: 0.1, sponsor: '摩根士丹利' },
]

// ---------- 工具函数 ----------

/** 确定性伪随机（同一代码每次同一条曲线）。 */
function seedOf(text: string): number {
  let h = 2166136261
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

function rng(seed: number): () => number {
  let a = seed || 1
  return () => {
    a += 0x6d2b79f5
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function round2(v: number): number {
  return Math.round(v * 100) / 100
}

/** 代码归一：sh600519 / 600519.SH / 600519 / 000001.SH → 6 位 ticker + 标准 thscode。 */
function normalizeCode(raw: string): { ticker: string; ths: string } {
  const text = raw.trim().toUpperCase()
  const digits = text.replace(/[^0-9]/g, '').slice(0, 6)
  if (text.includes('.TI')) return { ticker: digits, ths: digits + '.TI' }
  if (text.endsWith('.SH') || text.startsWith('SH') || /^(6|5|9)/.test(digits)) return { ticker: digits, ths: digits + '.SH' }
  if (text.endsWith('.BJ') || text.startsWith('BJ')) return { ticker: digits, ths: digits + '.BJ' }
  if (text.endsWith('.OF')) return { ticker: digits, ths: digits + '.OF' }
  return { ticker: digits, ths: digits + '.SZ' }
}

/** 按代码找演示标的；找不到就用确定性数据合成一只，保证任何输入都有行。 */
function stockOf(raw: string): DemoStock {
  const { ticker, ths } = normalizeCode(raw)
  const hit = STOCKS.find((s) => s.ths === ths || s.ticker === ticker)
  if (hit) return hit
  // 指数代码（.SH/.SZ）直接映射到指数演示数据，避免把 000001 当成个股。
  const idx = INDICES.find((i) => i.ths === ths)
  if (idx) {
    return {
      ths: idx.ths,
      ticker: idx.ticker,
      name: idx.name,
      price: idx.price,
      pct: idx.pct,
      volume: idx.volume,
      turnover: idx.turnover,
      turnoverPct: 0,
      pe: 0,
      pb: 0,
      ps: 0,
      pcf: 0,
      industry: '指数',
    }
  }
  const rand = rng(seedOf(ticker))
  const price = round2(6 + rand() * 80)
  const pct = round2((rand() - 0.45) * 7)
  return {
    ths,
    ticker,
    name: '演示标的 ' + ticker,
    price,
    pct,
    volume: Math.round((1 + rand() * 20) * 1e6),
    turnover: Math.round(price * (1 + rand() * 20) * 1e6),
    turnoverPct: round2(rand() * 6),
    pe: round2(8 + rand() * 60),
    pb: round2(0.8 + rand() * 8),
    ps: round2(0.6 + rand() * 12),
    pcf: round2(6 + rand() * 40),
    industry: '演示行业',
  }
}

/** 生成最近 n 个交易日（YYYY-MM-DD，跳过周末）。 */
function tradingDays(n: number, end: Date): string[] {
  const out: string[] = []
  const d = new Date(end.getTime())
  while (out.length < n) {
    const day = d.getDay()
    if (day !== 0 && day !== 6) {
      const p = (x: number) => (x < 10 ? '0' + x : String(x))
      out.unshift(d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()))
    }
    d.setDate(d.getDate() - 1)
  }
  return out
}

const BARS_END = new Date(2026, 10, 13)

/** 生成 K 线：日/周/月按因子放大波动；volume 单位股。 */
function barsOf(code: string, count: number, interval: string): Row[] {
  const factor = interval === '1w' || interval === 'week' ? 5 : interval === '1M' || interval === 'month' ? 21 : 1
  const dates = tradingDays(count, BARS_END)
  const rand = rng(seedOf(code + '|' + interval))
  const base = stockOf(code)
  let close = base.price * 0.86
  const out: Row[] = []
  for (let i = 0; i < count; i++) {
    const drift = (rand() - 0.46) * (factor > 1 ? 0.06 : 0.028)
    const open = close * (1 + (rand() - 0.5) * 0.012)
    close = Math.max(1.2, close * (1 + drift))
    const high = Math.max(open, close) * (1 + rand() * 0.014)
    const low = Math.min(open, close) * (1 - rand() * 0.014)
    const volume = Math.round((2 + rand() * 8) * 1e6 * factor)
    out.push({
      date_ms: Date.parse((dates[i] ?? '') + 'T00:00:00+08:00'),
      date: dates[i] ?? '',
      open_price: round2(open),
      high_price: round2(high),
      low_price: round2(low),
      close_price: round2(close),
      volume,
      turnover: Math.round(volume * close),
    })
  }
  return out
}

/** 统一信封。 */
function envelope(fields: Row): Row {
  return { asOf: AS_OF, ...fields }
}

/** 空结果信封（合法空，带 note 说明）。 */
function empty(title: string, note: string): Row {
  return envelope({ title, count: 0, items: [], note })
}

/** 客户端专用降级信封（官方 2004）。 */
function clientOnly(title: string): Row {
  return envelope({
    title,
    count: 0,
    items: [],
    permission: 'client',
    note: '该能力为同花顺 AI 客户端专用（官方 code=2004），外部 API Key 无权访问；请改用腾讯/新浪口径。',
  })
}

/** 上游不可用错误（5003：不是权限问题，面板按「上游暂不可用」展示）。 */
function upstreamError(): never {
  throw new Error('datasource: 官方 API /api/futures/prices/daily 返回 code=5003: 官方上游数据源不可用')
}

// ---------- 目录（分组名/端点 ID 与 docs/FUYAO-API.md 一致） ----------

interface DemoEndpoint {
  id: string
  path: string
  title: string
  group: string
  limit?: number
  permission?: string
  notes?: string
  params?: Array<{ name: string; type: string; required?: boolean; default?: string; desc?: string }>
}

function ep(id: string, title: string, group: string, params: DemoEndpoint['params'] = [], extra: Partial<DemoEndpoint> = {}): DemoEndpoint {
  return { id, path: '/api/' + id.replace(/\./g, '/'), title, group, params, ...extra }
}

/** 演示目录：每域 2–5 个真实端点（total 仍按官方 125）。 */
const ENDPOINTS: DemoEndpoint[] = [
  ep('a-share.prices.snapshot', 'A 股行情快照', '行情', [{ name: 'thscodes', type: 'string' }], { limit: 50, notes: 'volume=股、turnover=元；单批 50 个。' }),
  ep('a-share.prices.historical', 'A 股历史 K 线', '行情', [{ name: 'thscode', type: 'string', required: true }, { name: 'interval', type: 'string', required: true }, { name: 'start', type: 'string', required: true }, { name: 'end', type: 'string', required: true }], { notes: 'date_ms 毫秒戳；volume=股。' }),
  ep('a-share.prices.historical-batch', 'A 股历史 K 线（批量）', '行情', [{ name: 'thscodes', type: 'string', required: true }, { name: 'interval', type: 'string', required: true }], { limit: 10 }),
  ep('a-share.valuations.snapshot', 'A 股估值快照', '估值', [{ name: 'thscodes', type: 'string', required: true }], { limit: 100, notes: 'PE/PB/PS/PCF 为倍数。' }),
  ep('a-share.calendar.trading-days', '交易日历', '基础', [], { notes: 'date=yyyyMMdd；date_ms=当日零点。' }),
  ep('meta.tickers.search', '标的检索', '基础', [{ name: 'q', type: 'string', required: true }], { limit: 50 }),
  ep('meta.tickers.list', '标的列表', '基础', [], { limit: 10000 }),
  ep('a-share.financials.indicators', '财务指标', '财务', [{ name: 'thscode', type: 'string', required: true }, { name: 'report', type: 'string', required: true }], { notes: 'report=yyyy-1..4；value 为原始数值字符串。' }),
  ep('a-share.financials.income-statements', '利润表', '财务', [{ name: 'thscode', type: 'string', required: true }, { name: 'period', type: 'string' }], { notes: '金额单位元。' }),
  ep('a-share.financials.balance-sheets', '资产负债表', '财务', [{ name: 'thscode', type: 'string', required: true }]),
  ep('a-share.financials.cash-flow-statements', '现金流量表', '财务', [{ name: 'thscode', type: 'string', required: true }]),
  ep('a-share.financials.indicators', '财务指标', '财务', [{ name: 'thscode', type: 'string', required: true }, { name: 'report', type: 'string', required: true }], { notes: 'report=yyyy-1..4；value 为原始字符串。' }),
  ep('a-share-index.catalog.ths-index-list', '同花顺指数列表', '指数', [{ name: 'tag', type: 'string' }]),
  ep('a-share-index.constituents.ths-stock-list', '指数成分股', '指数', [{ name: 'thscode', type: 'string', required: true }]),
  ep('a-share-index.constituents.ths-index-list-by-stock', '个股反查板块', '指数', [{ name: 'thscodes', type: 'string', required: true }, { name: 'type', type: 'string' }]),
  ep('a-share-index.prices.snapshot', '指数行情快照', '指数', [{ name: 'thscodes', type: 'string', required: true }], { limit: 100 }),
  ep('a-share-index.baseinfo.new-concepts', '同花顺新增概念', '指数', [], { permission: 'client' }),
  ep('a-share.special-data.limit-up-pool', '涨停池', '特色数据', [], { notes: 'seal_money=元；open_times 可 null。' }),
  ep('a-share.special-data.limit-down-pool', '跌停池', '特色数据'),
  ep('a-share.special-data.limit-break-pool', '炸板池', '特色数据'),
  ep('a-share.special-data.limit-up-statistics', '涨停统计', '特色数据', [], { notes: 'seal_rate 为 0~1 比例。' }),
  ep('a-share.special-data.hot-stock-list', '热股榜', '特色数据', [{ name: 'limit', type: 'integer' }], { limit: 100 }),
  ep('a-share.special-data.skyrocket-list', '飙升榜', '特色数据'),
  ep('a-share.special-data.hot-stock-list-history', '历史热股榜', '特色数据', [{ name: 'date', type: 'string', required: true }]),
  ep('a-share.special-data.anomaly-analysis-list', '个股异动', '特色数据'),
  ep('a-share.special-data.anomaly-analysis-stock', '个股异动原因', '特色数据', [{ name: 'thscodes', type: 'string', required: true }], { limit: 50 }),
  ep('a-share.special-data.abnormal-monitor-list', '异动监控', '特色数据'),
  ep('a-share.special-data.dragon-tiger-list', '龙虎榜', '特色数据', [{ name: 'date', type: 'string' }]),
  ep('a-share.special-data.dragon-tiger-stock-institution', '龙虎榜机构', '特色数据', [{ name: 'thscode', type: 'string', required: true }]),
  ep('a-share.special-data.dragon-tiger-department-classic-record', '营业部经典战绩', '特色数据', [{ name: 'department_id', type: 'string', required: true }]),
  ep('a-share.special-data.dragon-tiger-department-history', '营业部历史', '特色数据', [{ name: 'department_id', type: 'string', required: true }]),
  ep('a-share.special-data.chip-shape-dates', '筹码形态日期', '特色数据', [], { permission: 'client' }),
  ep('a-share.special-data.chip-shape-stocks', '筹码形态股票', '特色数据', [{ name: 'shape', type: 'string', required: true }], { permission: 'client' }),
  ep('a-share.special-data.turnover-prediction-snapshot', '成交额预测', '特色数据', [], { permission: 'client' }),
  ep('a-share.auction.snapshot', '集合竞价快照', '竞价', [{ name: 'thscodes', type: 'string', required: true }], { limit: 100, notes: 'auction_volume=股；三个 pct 为百分数。' }),
  ep('a-share.auction.short-term-benchmark', '竞价风向标', '竞价'),
  ep('a-share.special-data.auction-stock-opportunities', '竞价机会', '特色数据'),
  ep('a-share.capital-flow.snapshot', '资金流快照', '资金', [{ name: 'thscode', type: 'string', required: true }], { permission: 'client', notes: '外部 Key 实测 2004。' }),
  ep('a-share.capital-flow.historical', '资金流历史', '资金', [{ name: 'thscode', type: 'string', required: true }], { permission: 'client' }),
  ep('a-share.high-frequency.historical', '高频历史', '高频', [{ name: 'thscode', type: 'string', required: true }], { permission: 'client' }),
  ep('a-share.high-frequency.intraday', '高频分时', '高频', [{ name: 'thscode', type: 'string', required: true }], { permission: 'client' }),
  ep('fund.profile.detail', '基金资料', '基金', [{ name: 'thscode', type: 'string', required: true }]),
  ep('fund.market.snapshot', '基金行情快照', '基金', [{ name: 'thscode', type: 'string', required: true }], { limit: 100 }),
  ep('fund.performance.nav', '基金净值', '基金', [{ name: 'thscode', type: 'string', required: true }]),
  ep('fund.performance.returns', '基金收益', '基金', [{ name: 'thscode', type: 'string', required: true }]),
  ep('fund.portfolio.holdings', '基金持仓', '基金', [{ name: 'thscode', type: 'string', required: true }]),
  ep('fund.portfolio.asset-allocation', '资产配置', '基金', [{ name: 'thscode', type: 'string', required: true }]),
  ep('fund.managers.detail', '基金经理', '基金', [{ name: 'manager_id', type: 'string', required: true }]),
  ep('fund.etf-flow.net-inflow-historical', 'ETF 申赎净流入', '基金', [], { permission: 'client' }),
  ep('futures.varieties.list', '期货品种', '期货'),
  ep('futures.contracts.list', '期货合约', '期货'),
  ep('futures.contracts.main-list', '期货主力合约', '期货'),
  ep('futures.positions.variety-daily', '期货品种持仓', '期货', [{ name: 'date', type: 'string', required: true }]),
  ep('futures.basis.main-continuous-latest', '主力基差', '期货'),
  ep('futures.prices.daily', '期货日 K', '期货', [], { notes: '上游实测 5003，暂时不可用。' }),
  ep('futures.calendar.session-timeline', '期货交易时间轴', '期货', [{ name: 'thscode', type: 'string', required: true }]),
  ep('options.varieties.list', '期权品种', '期权'),
  ep('options.contracts.list', '期权合约', '期权'),
  ep('options.prices.daily', '期权日 K', '期权', [], { notes: '上游实测 5003。' }),
  ep('options.prices.intraday', '期权分时', '期权', [{ name: 'thscode', type: 'string', required: true }]),
  ep('options.calendar.session-timeline', '期权交易时间轴', '期权', [{ name: 'thscode', type: 'string', required: true }]),
  ep('news.flash.list', '快讯列表', '资讯', [{ name: 'tag_id', type: 'integer' }, { name: 'seq', type: 'string' }], { notes: '首屏 seq=0；publish_time 毫秒戳。' }),
  ep('news.notice.detail', '公告详情', '资讯', [{ name: 'seq', type: 'string', required: true }]),
  ep('news.topic.hot-list', '热门专题', '资讯', [{ name: 'tab_id', type: 'integer' }]),
  ep('news.stock.timeline-list', '个股新闻', '资讯', [{ name: 'thscode', type: 'string', required: true }]),
  ep('news.anomaly.analysis', '异动解读', '资讯', [{ name: 'begin_date', type: 'string' }, { name: 'end_date', type: 'string' }]),
  ep('news.events.search', '资讯事件库', '资讯', [], { permission: 'client' }),
  ep('a-share.ipo.calendar-list', 'A 股打新日历', '新股', [{ name: 'market', type: 'string' }, { name: 'status', type: 'string' }]),
  ep('hk-share.ipo.calendar-list', '港股打新日历', '港股', [{ name: 'status', type: 'string' }]),
  ep('hk-share.ipo.application-list', '港股认购列表', '港股'),
  ep('hk-share.ipo.issue-detail', '港股发行详情', '港股', [{ name: 'issue_code', type: 'string', required: true }]),
  ep('dump.market-dumps.daily-k.download-url', '全市场日 K 导出', '全市场导出', [], { notes: 'presigned_url 有时效。' }),
  ep('dump.market-dumps.daily-k-10d.download-url', '全市场 10 日 K 导出', '全市场导出'),
  ep('dump.market-dumps.adjustment-factors.download-url', '复权因子导出', '全市场导出'),
]

const GROUP_ORDER = ['行情', '估值', '基础', '财务', '指数', '特色数据', '竞价', '资金', '高频', '基金', '期货', '期权', '资讯', '新股', '港股', '全市场导出']

/** 目录方法：返回 total=125（官方全量）+ 演示端点列表。 */
function catalog(payload: Row): Row {
  const group = typeof payload.group === 'string' ? payload.group.trim() : ''
  const endpoint = typeof payload.endpoint === 'string' ? payload.endpoint.trim() : ''
  const groups = GROUP_ORDER.filter((g) => ENDPOINTS.some((e) => e.group === g))
  const list = ENDPOINTS.filter((e) => {
    if (group && !e.group.includes(group)) return false
    if (endpoint && e.id !== endpoint && e.path !== endpoint) return false
    return true
  }).map((e) => ({
    id: e.id,
    path: e.path,
    title: e.title,
    group: e.group,
    limit: e.limit ?? 0,
    permission: e.permission ?? '',
    notes: e.notes ?? '',
    params: (e.params ?? []).map((p) => ({ name: p.name, type: p.type, required: p.required === true, default: p.default ?? '', desc: p.desc ?? '' })),
  }))
  return { total: 125, count: list.length, groups, endpoints: list, asOf: AS_OF, note: '演示模式只列每域常用端点（分组与 ID 与官方一致），真实环境返回全部 125 个。' }
}

/** 冲突检测：演示端点里没有这个 ID 时也要能给出可读结果。 */
function endpointById(id: string): DemoEndpoint | undefined {
  return ENDPOINTS.find((e) => e.id === id || e.path === id)
}

// ---------- 方法实现 ----------

/** quote：官方快照（name 为空时由前端退回代码）。 */
function quote(payload: Row): Row {
  const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
  const list = codes.slice(0, 50).map((raw) => {
    const { ths } = normalizeCode(raw)
    // 指数后缀 .SH/.SZ 且 ticker 在指数清单里时按指数口径出数（volume 仍为股）。
    const index = INDICES.find((i) => i.ths === ths)
    if (index) {
      const prev = round2(index.price / (1 + index.pct / 100))
      return {
        thscode: index.ths,
        ticker: index.ticker,
        name: index.name,
        last_price: index.price,
        price_change: round2(index.price - prev),
        price_change_ratio_pct: index.pct,
        open_price: round2(prev * 1.001),
        high_price: round2(index.price * 1.004),
        low_price: round2(prev * 0.996),
        prev_price: prev,
        volume: index.volume,
        turnover: index.turnover,
        timestamp: Date.parse(AS_OF),
      }
    }
    const s = stockOf(raw)
    const prevClose = round2(s.price / (1 + s.pct / 100))
    return {
      thscode: s.ths,
      ticker: s.ticker,
      name: s.name,
      last_price: s.price,
      price_change: round2(s.price - prevClose),
      price_change_ratio_pct: s.pct,
      open_price: round2(prevClose * 1.002),
      high_price: round2(Math.max(s.price, prevClose) * 1.006),
      low_price: round2(Math.min(s.price, prevClose) * 0.994),
      prev_price: prevClose,
      volume: s.volume,
      turnover: s.turnover,
      timestamp: Date.parse(AS_OF),
    }
  })
  return envelope({
    count: list.length,
    items: list,
    note: '官方快照：volume 单位股、turnover 单位元、price_change_ratio_pct 为百分数原值；未返回的代码由腾讯/新浪链路兜底。',
  })
}

/** valuation：PE/PB/PS/PCF 为倍数。 */
function valuation(payload: Row): Row {
  const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
  // 指数没有估值口径：估值快照只返回个股行。
  const items = codes
    .slice(0, 100)
    .filter((raw) => !INDICES.some((i) => i.ths === normalizeCode(raw).ths))
    .map((raw) => {
      const s = stockOf(raw)
      return { thscode: s.ths, ticker: s.ticker, name: s.name, pe_ttm: s.pe, pe_mrq: round2(s.pe * 1.02), pb_mrq: s.pb, ps_ttm: s.ps, pcf_ttm: s.pcf }
    })
  return envelope({ count: items.length, items, note: '估值倍数：PE/PB/PS/PCF 为倍，不是百分数；数据为最新快照。' })
}

/** kline：批量历史 K 线（volume=股）。 */
function kline(payload: Row): Row {
  const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
  const interval = typeof payload.interval === 'string' && payload.interval !== '' ? payload.interval : '1d'
  const adjust = typeof payload.adjust === 'string' && payload.adjust !== '' ? payload.adjust : 'forward'
  const limit = typeof payload.limit === 'number' && payload.limit > 0 ? payload.limit : 120
  const series = codes.slice(0, 10).map((raw) => {
    const { ths } = normalizeCode(raw)
    return { thscode: ths, interval, adjust, timestamp: Date.parse(AS_OF), bars: barsOf(ths, Math.min(limit, 240), interval) }
  })
  return envelope({ count: series.length, series, note: '官方 K 线：volume 单位股、turnover 单位元；adjust=forward 为前复权。' })
}

/** limitPool：涨停/跌停/炸板池。 */
function limitPool(payload: Row): Row {
  const pool = typeof payload.pool === 'string' ? payload.pool : 'up'
  const limit = typeof payload.limit === 'number' && payload.limit > 0 ? payload.limit : 50
  const rows = pool === 'down' ? LIMIT_DOWN : pool === 'break' ? LIMIT_BREAK : LIMIT_UP
  return envelope({
    pool,
    count: Math.min(rows.length, limit),
    items: rows.slice(0, limit),
    note: '涨跌幅/封板率为百分数原值；seal_money/max_seal_money 单位元；open_times 为开板次数（可能 null）。',
  })
}

/** limitStats：封板率 0~1。 */
function limitStats(): Row {
  const data = {
    trade_date: TRADE_DATE,
    seal_rate: 0.784,
    open_stock_count: 19,
    limit_up_stock_count: 84,
    touched_limit_up_stock_count: 107,
    page_boards: 84,
    exact_boards: 79,
    timestamp: Date.parse(AS_OF),
  }
  return envelope({ data, note: 'seal_rate 是 0~1 比例（不是百分数）；家数为整数。' })
}

/** hotRank：热榜（history 返回历史榜）。 */
function hotRank(payload: Row): Row {
  const kind = typeof payload.kind === 'string' ? payload.kind : 'hot'
  const limit = typeof payload.limit === 'number' && payload.limit > 0 ? payload.limit : 30
  const rows = kind === 'skyrocket' ? HOT_SKYROCKET : kind === 'history' ? HOT_HISTORY : HOT_HOT
  return envelope({ kind, count: Math.min(rows.length, limit), items: rows.slice(0, limit), note: 'heat 是上游热度字符串（不是金额）；rank_change 正数表示排名上升。' })
}

/** 个股龙虎榜机构明细。 */
const DRAGON_TIGER_INSTITUTION: Row[] = [
  { thscode: '002384.SZ', trade_date: TRADE_DATE, institution_name: '机构专用', buy_value: 3.12e8, sell_value: 1.24e8, net_value: 1.88e8, range_days: 1 },
  { thscode: '002384.SZ', trade_date: TRADE_DATE, institution_name: '深股通专用', buy_value: 2.86e8, sell_value: 1.42e8, net_value: 1.44e8, range_days: 1 },
  { thscode: '002384.SZ', trade_date: TRADE_DATE, institution_name: '华鑫证券上海分公司', buy_value: 2.18e8, sell_value: 0.62e8, net_value: 1.56e8, range_days: 1 },
]

const CHIP_SHAPE: Row[] = [
  { thscode: '600519.SH', ticker: '600519', name: '贵州茅台', date: TRADE_DATE, shape: '单峰密集', concentration: 0.86, profit_ratio_pct: 92.4, avg_cost: 1486 },
  { thscode: '300750.SZ', ticker: '300750', name: '宁德时代', date: TRADE_DATE, shape: '双峰密集', concentration: 0.72, profit_ratio_pct: 78.6, avg_cost: 238.4 },
]

const NEW_CONCEPTS: Row[] = [
  { thscode: '886012.TI', name: '固态电池', date: TRADE_DATE, stock_count: 28, description: '硫化物电解质中试线落地，多家电池厂送样' },
  { thscode: '886024.TI', name: 'AI 眼镜', date: TRADE_DATE, stock_count: 34, description: '多款 AI 眼镜新品发布，光学与声学供应链受益' },
]

/** special：按 view 分发（含 2004 视图）。 */
function special(payload: Row): Row {
  const view = typeof payload.view === 'string' ? payload.view : 'anomaly'
  const limit = typeof payload.limit === 'number' && payload.limit > 0 ? payload.limit : 20
  switch (view) {
    case 'anomaly-stock': {
      const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
      const rows = ANOMALY_BY_STOCK.filter((r) => codes.some((c) => {
        const { ths } = normalizeCode(c)
        return r.thscode === ths
      }))
      return envelope({ view, count: rows.length, items: rows, note: '按股票批量查异动原因（≤50 个代码）。' })
    }
    case 'abnormal':
      return envelope({ view, count: ABNORMAL.length, items: ABNORMAL })
    case 'dragon-tiger':
      return envelope({ view, data: DRAGON_TIGER, note: 'stock_items 金额单位元；concept_list 为字符串数组。' })
    case 'dragon-tiger-institution':
      return envelope({ view, count: DRAGON_TIGER_INSTITUTION.length, items: DRAGON_TIGER_INSTITUTION })
    case 'department-record':
      return envelope({ view, data: DEPARTMENT_RECORD })
    case 'department-history':
      return envelope({ view, count: DEPARTMENT_HISTORY.length, items: DEPARTMENT_HISTORY.slice(0, limit) })
    // 以下三个视图官方实测 2004（客户端专用）：演示也返回降级说明，避免前端误以为可用。
    case 'chip-shape':
      return { ...clientOnly('筹码形态'), view }
    case 'new-concepts':
      return { ...clientOnly('同花顺新增概念'), view }
    case 'turnover':
      return { ...clientOnly('市场成交额预测'), view }
    default:
      return envelope({ view: 'anomaly', count: ANOMALY.length, items: ANOMALY.slice(0, limit) })
  }
}

/** auction：快照 / 风向标 / 机会。 */
function auction(payload: Row): Row {
  const view = typeof payload.view === 'string' ? payload.view : 'snapshot'
  if (view === 'benchmark') return envelope({ view, count: AUCTION_BENCHMARK.length, items: AUCTION_BENCHMARK, note: '竞价风向标口径：昨日涨停股今日竞价溢价率为百分数。' })
  if (view === 'opportunities') return envelope({ view, count: AUCTION_OPPS.length, items: AUCTION_OPPS })
  const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
  const rows = AUCTION.filter((r) => codes.length === 0 || codes.some((c) => normalizeCode(c).ths === r.thscode))
  return envelope({
    view: 'snapshot',
    count: rows.length,
    items: rows,
    auction_phase: 'pre_open',
    data_status: 'ready',
    total: rows.length,
    note: 'auction_pct/auction_turnover_pct 为百分数原值；auction_volume 单位股；auction_amount 单位元。',
  })
}

/** capitalFlow：实时/历史/高频全部客户端专用（演示 2004 降级）。 */
function capitalFlow(payload: Row): Row {
  const view = typeof payload.view === 'string' ? payload.view : 'snapshot'
  if (view === 'snapshot') {
    return { ...clientOnly('主力资金快照'), view, item: null }
  }
  return { ...clientOnly('主力资金历史'), view, count: 0, items: [] }
}

/** financials：三大报表 + 五类指标。 */
function financials(payload: Row): Row {
  const kind = typeof payload.kind === 'string' ? payload.kind : 'income'
  if (kind === 'indicators') {
    return envelope({
      kind,
      data: { thscode: normalizeCode(typeof payload.code === 'string' ? payload.code : '600519').ths, report: typeof payload.report === 'string' ? payload.report : '2025-4', abilities: ABILITIES },
      note: 'value 为上游原始数值字符串；百分比类指标按百分数表达（89.12=89.12%）。',
    })
  }
  const rows = kind === 'balance' ? BALANCE : kind === 'cash' ? CASH : INCOME
  return envelope({
    kind,
    count: rows.length,
    items: rows,
    note: '报表金额单位元；period=annual 仅年报、quarterly 为季度末。',
  })
}

/** tickerSearch：从股票/指数/基金里做子串匹配。 */
function tickerSearch(payload: Row): Row {
  const keyword = typeof payload.keyword === 'string' ? payload.keyword.trim().toLowerCase() : ''
  const limit = typeof payload.limit === 'number' && payload.limit > 0 ? payload.limit : 12
  if (keyword === '') return envelope({ count: 0, items: [] })
  const pool: Row[] = [
    ...STOCKS.map((s) => ({ thscode: s.ths, ticker: s.ticker, name: s.name, exchange: s.ths.split('.')[1], asset_type: 'a-share' })),
    ...INDICES.map((s) => ({ thscode: s.ths, ticker: s.ticker, name: s.name, exchange: s.ths.split('.')[1], asset_type: 'a-share-index' })),
    { thscode: FUND.ths, ticker: FUND.ticker, name: FUND.name, exchange: 'OF', asset_type: 'fund-otc' },
    ...FUTURES_CONTRACTS.map((c) => ({ thscode: String(c.thscode), ticker: String(c.ticker), name: String(c.name), exchange: String(c.exchange_code), asset_type: 'futures' })),
  ]
  const items = pool.filter((r) => {
    const ticker = typeof r.ticker === 'string' ? r.ticker.toLowerCase() : ''
    const name = typeof r.name === 'string' ? r.name : ''
    return ticker.includes(keyword) || name.toLowerCase().includes(keyword) || String(r.thscode).toLowerCase().includes(keyword)
  })
  return envelope({ count: items.length, items: items.slice(0, limit) })
}

/** calendar：未来 10 个交易日（含演示交易日）。 */
function calendar(payload: Row): Row {
  const start = typeof payload.start === 'string' && payload.start !== '' ? payload.start : TRADE_DATE
  const startMs = Date.parse(start.length === 8 ? start.slice(0, 4) + '-' + start.slice(4, 6) + '-' + start.slice(6) + 'T00:00:00+08:00' : start + 'T00:00:00+08:00')
  const base = Number.isFinite(startMs) ? new Date(startMs) : new Date(2026, 10, 13)
  const dates = tradingDays(10, new Date(base.getTime() + 14 * 86400000))
  const items = dates.map((d) => ({
    date: d.replace(/-/g, ''),
    date_ms: Date.parse(d + 'T00:00:00+08:00'),
  }))
  return envelope({ count: items.length, items, note: 'date 为 yyyyMMdd；date_ms 为当日零点毫秒戳。' })
}

/** index：目录/成分股/反查/快照/K 线。 */
function index(payload: Row): Row {
  const view = typeof payload.view === 'string' ? payload.view : 'catalog'
  switch (view) {
    case 'constituents': {
      const code = typeof payload.indexCode === 'string' ? payload.indexCode.toUpperCase() : ''
      const rows = STOCKS.slice(0, 6).map((s, i) => ({ thscode: s.ths, ticker: s.ticker, name: s.name, weight: round2(12.4 - i * 1.6) }))
      return envelope({ view, indexCode: code, count: rows.length, items: rows, note: '成分股按权重降序；thscode 带交易所后缀。' })
    }
    case 'memberships': {
      const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
      const rows = codes.slice(0, 20).map((c) => {
        const s = stockOf(c)
        return { thscode: s.ths, ticker: s.ticker, name: s.name, index_list: [{ thscode: '881121.TI', name: s.industry, type: 'industry' }, { thscode: '885431.TI', name: 'AI 算力', type: 'cn_concept' }] }
      })
      return envelope({ view, count: rows.length, items: rows, note: 'type=cn_concept/industry/region/tszs（大小写敏感）。' })
    }
    case 'snapshot': {
      const codes = Array.isArray(payload.codes) ? payload.codes.filter((c): c is string => typeof c === 'string') : []
      const rows = codes.length > 0
        ? codes.map((raw) => {
            const { ths, ticker } = normalizeCode(raw)
            const hit = INDICES.find((i) => i.ths === ths || i.ticker === ticker)
            const catalogName = Object.values(INDEX_CATALOG)
              .flat()
              .find((i) => i.thscode === ths)?.name
            const base = hit ?? {
              ths,
              ticker,
              name: catalogName ?? '演示指数 ' + ticker,
              price: 2400 + (seedOf(ticker) % 900),
              pct: round2(((seedOf(ticker) % 400) - 180) / 100),
              volume: 1.2e10,
              turnover: 2.4e11,
            }
            const prev = round2(base.price / (1 + base.pct / 100))
            return {
              thscode: base.ths,
              ticker: base.ticker,
              name: base.name,
              last_price: base.price,
              price_change: round2(base.price - prev),
              price_change_ratio_pct: base.pct,
              open_price: round2(prev * 1.001),
              high_price: round2(base.price * 1.004),
              low_price: round2(prev * 0.996),
              prev_price: prev,
              volume: base.volume,
              turnover: base.turnover,
            }
          })
        : []
      return envelope({ view, count: rows.length, items: rows, note: 'volume 单位股、turnover 单位元（指数成交量口径与个股不同）。' })
    }
    case 'kline':
      return kline(payload)
    default: {
      const tag = typeof payload.tag === 'string' && payload.tag !== '' ? payload.tag : 'cn_concept'
      const rows = INDEX_CATALOG[tag] ?? INDEX_CATALOG.cn_concept ?? []
      return envelope({ view: 'catalog', tag, count: rows.length, items: rows, note: '指数 thscode 后缀：.TI（同花顺）/ .SH / .SZ（标准指数）。' })
    }
  }
}

/** 基金视图 → 演示数据。 */
const FUND_VIEWS: Record<string, () => Row> = {
  profile: () => envelope({ count: 1, data: FUND_PROFILE, note: '规模/净值单位元；trade_rule/rate_info 为文本。' }),
  snapshot: () => envelope({
    count: 1,
    items: [{
      thscode: FUND.ths, ticker: FUND.ticker, last_price: 7.842, open_price: 7.8, high_price: 7.86, low_price: 7.78, prev_price: 7.796,
      price_change: 0.046, price_change_ratio_pct: 0.59, price_amplitude_ratio_pct: 1.03, volume: 1286400, turnover: 1.008e7,
    }],
    note: 'ETF/LOF 场内有价格；场外基金只有净值。volume 单位股。',
  }),
  nav: () => envelope({ count: FUND_NAV.length, items: FUND_NAV, note: 'unit_nav/adj_nav 单位元。' }),
  returns: () => envelope({ count: FUND_RETURNS.length, items: FUND_RETURNS, note: '收益率均为百分数原值。' }),
  holdings: () => envelope({ count: FUND_HOLDINGS.length, items: FUND_HOLDINGS, note: 'hold_ratio 为百分数；position_capital 单位元。' }),
  'asset-allocation': () => envelope({ count: FUND_ALLOCATION.length, items: FUND_ALLOCATION, note: '各类资产占比为百分数。' }),
  manager: () => envelope({ count: 1, data: FUND_MANAGER }),
  performance: () => envelope({ count: FUND_PERFORMANCE.length, items: FUND_PERFORMANCE, note: '收益率为百分数；date_ms 为报告日。' }),
  drawdown: () => envelope({
    count: 1,
    items: [{ thscode: FUND.ths, week: -1.24, month: -3.86, tmonth: -6.42, hyear: -8.16, year: -12.4, twoyear: -18.6, tyear: -22.4, fyear: -26.8, nowyear: -12.4, now: -12.4 }],
    note: '回撤为百分数（负值表示回撤幅度）。',
  }),
  news: () => envelope({ count: 2, items: NEWS_TOPIC, note: '基金资讯复用资讯字段形状。' }),
  holders: () => envelope({
    count: 1,
    items: [{
      merge_scope: '合并',
      report_date_ms: Date.parse('2026-06-30T00:00:00+08:00'),
      ins_position: 2.86e9,
      holder_amount: 4862000,
      avg_holder_share: 7.94,
      psnl_rate: 32.6,
      mgmt_staff_hold_rate: 0.42,
    }],
    note: '持有人户数/机构持仓为官方口径；占比为百分数。',
  }),
  dividends: () => envelope({
    count: 3,
    items: [
      { timestamp: Date.parse('2026-01-16T00:00:00+08:00'), dividend_count: 1, dividend_total: 1.86e9 },
      { timestamp: Date.parse('2025-01-17T00:00:00+08:00'), dividend_count: 1, dividend_total: 1.62e9 },
      { timestamp: Date.parse('2024-01-19T00:00:00+08:00'), dividend_count: 1, dividend_total: 1.42e9 },
    ],
    note: '分红总额单位元；timestamp 为分红实施日。',
  }),
  'industry-allocation': () => envelope({
    count: 4,
    items: [
      { report_period: '2026Q3', industry_name: '制造业', ratio_pct: 42.6 },
      { report_period: '2026Q3', industry_name: '金融业', ratio_pct: 18.4 },
      { report_period: '2026Q3', industry_name: '信息传输/软件', ratio_pct: 12.8 },
      { report_period: '2026Q3', industry_name: '批发和零售业', ratio_pct: 6.2 },
    ],
    note: '行业占比为百分数原值。',
  }),
  'financial-indicators': () => envelope({
    count: 2,
    items: [
      { start_date_ms: Date.parse('2026-01-01T00:00:00+08:00'), end_date_ms: Date.parse('2026-06-30T00:00:00+08:00'), publish_date_ms: Date.parse('2026-08-28T00:00:00+08:00'), share_nav: 7.842, asset_nav: 8.126, current_profit: 6.42e8, distribution_profit: 0 },
      { start_date_ms: Date.parse('2025-01-01T00:00:00+08:00'), end_date_ms: Date.parse('2025-12-31T00:00:00+08:00'), publish_date_ms: Date.parse('2026-03-28T00:00:00+08:00'), share_nav: 7.468, asset_nav: 7.752, current_profit: 12.86e8, distribution_profit: 0 },
    ],
    note: 'share_nav/asset_nav 单位元；current_profit 单位元。',
  }),
}

/** fund：白名单外的 view 给可读错误。 */
function fund(payload: Row): Row {
  const view = typeof payload.view === 'string' && payload.view !== '' ? payload.view : 'profile'
  const fn = FUND_VIEWS[view]
  if (fn) return fn()
  if (view === 'etf-flow') return { ...clientOnly('ETF 申赎净流入'), view }
  return envelope({
    view,
    count: 0,
    items: [],
    note: '演示环境未内置该基金视图的样例数据，可用基金资料/净值/持仓/业绩/经理查看；真实环境由数据层返回。',
  })
}

/** futures/options：视图 → 演示数据；日 K 演 5003。 */
function futures(payload: Row): Row {
  const view = typeof payload.view === 'string' ? payload.view : 'varieties'
  switch (view) {
    case 'contracts':
      return envelope({ view, count: FUTURES_CONTRACTS.length, items: FUTURES_CONTRACTS })
    case 'contract':
      return envelope({ view, count: 1, items: FUTURES_CONTRACTS.slice(0, 1) })
    case 'main':
    case 'main-continuous':
      return envelope({ view, count: FUTURES_MAIN.length, items: FUTURES_MAIN })
    case 'positions-variety':
      return envelope({ view, count: FUTURES_POSITIONS.length, items: FUTURES_POSITIONS, note: '成交/持仓单位手；date 为 YYYY-MM-DD。' })
    case 'basis':
      return envelope({ view, count: FUTURES_BASIS.length, items: FUTURES_BASIS, note: '基差随现货口径（元/吨）。' })
    case 'session':
      return envelope({ view, count: FUTURES_SESSION.length, items: FUTURES_SESSION })
    case 'intraday':
      return envelope({ view, count: FUTURES_INTRADAY.length, items: FUTURES_INTRADAY, note: '价格单位随合约；成交单位手。' })
    case 'kline':
      return upstreamError()
    case 'fundamentals':
      return { ...clientOnly('期货 F10 指标'), view }
    default:
      return envelope({ view: 'varieties', count: FUTURES_VARIETIES.length, items: FUTURES_VARIETIES, note: '乘数/最小变动价位/保证金率随品种，交易时段为交易所口径。' })
  }
}

function options(payload: Row): Row {
  const view = typeof payload.view === 'string' ? payload.view : 'varieties'
  switch (view) {
    case 'contracts':
      return envelope({ view, count: OPTIONS_CONTRACTS.length, items: OPTIONS_CONTRACTS })
    case 'contract':
      return envelope({ view, count: 1, items: OPTIONS_CONTRACTS.slice(0, 1) })
    case 'session':
      return envelope({ view, count: OPTIONS_SESSION.length, items: OPTIONS_SESSION })
    case 'intraday':
      return envelope({ view, count: FUTURES_INTRADAY.length, items: FUTURES_INTRADAY, note: '期权分时字段与期货同构。' })
    case 'kline':
      return upstreamError()
    default:
      return envelope({ view: 'varieties', count: OPTIONS_VARIETIES.length, items: OPTIONS_VARIETIES })
  }
}

/** news：快讯/公告/专题/个股新闻/异动解读/事件库。 */
function news(payload: Row): Row {
  const view = typeof payload.view === 'string' && payload.view !== '' ? payload.view : 'flash'
  switch (view) {
    case 'notice':
      return envelope({ view, count: 1, data: NEWS_NOTICE, note: '公告正文在通用层截断到 600 字。' })
    case 'topic':
      return envelope({ view, count: NEWS_TOPIC.length, items: NEWS_TOPIC })
    case 'stock-timeline':
      return envelope({ view, count: NEWS_TIMELINE.length, items: NEWS_TIMELINE, note: '个股新闻首屏 offset="first"。' })
    case 'anomaly':
      return envelope({ view, count: NEWS_ANOMALY.length, items: NEWS_ANOMALY })
    case 'events':
      return { ...clientOnly('资讯事件库'), view }
    default:
      return envelope({ view: 'flash', count: NEWS_FLASH.length, items: NEWS_FLASH, note: '快讯首屏 seq="0"；publish_time 为毫秒戳。' })
  }
}

/** ipo：A 股打新 / 港股打新。 */
function ipo(payload: Row): Row {
  const view = typeof payload.view === 'string' && payload.view !== '' ? payload.view : 'a-ipo'
  if (view === 'hk-calendar') return envelope({ view, count: IPO_HK_CALENDAR.length, items: IPO_HK_CALENDAR, note: '金额按发行币种（HKD）；lot_size 为每手股数。' })
  if (view === 'hk-application') return envelope({ view, count: IPO_HK_APPLICATION.length, items: IPO_HK_APPLICATION })
  if (view === 'hk-detail') {
    return envelope({ view, count: 1, data: { issue_code: 'HK0988', name: '云脉智能', currency: 'HKD', issue_price_range: '18.80-22.60', lower: 18.8, upper: 22.6, lot_size: 200, entry_fee: 4520 } })
  }
  const market = typeof payload.market === 'string' && payload.market !== '' ? payload.market : ''
  const status = typeof payload.status === 'string' && payload.status !== '' ? payload.status : ''
  const rows = IPO_A.filter((r) => (market === '' || r.market === market) && (status === '' || r.status === status))
  return envelope({ view: 'a-ipo', count: rows.length, items: rows, note: 'market=sh_sz|bj；status=upcoming|subscribable|awaiting_listing。' })
}

/** marketDump：全市场导出链接（有时效，只展示）。 */
function marketDump(payload: Row): Row {
  const kind = typeof payload.kind === 'string' ? payload.kind : '10y'
  const name = kind === 'adjust' ? 'adjustment-factors' : kind === '10d' ? 'daily-k-10d' : 'daily-k'
  return envelope({
    kind,
    data: { presigned_url: 'https://o.thsi.cn/dump/demo/' + name + '.parquet?expires=202611131800&signature=demo-only' },
    note: 'presigned_url 有时效；下载域名 o.thsi.cn 不在应用网络白名单内，请交给浏览器完成。',
  })
}

/** query：通用查询。按端点 ID 复用强类型 mock，未知端点给一条可读的演示记录。 */
function query(payload: Row): Row {
  const endpoint = typeof payload.endpoint === 'string' ? payload.endpoint : ''
  const limit = typeof payload.limit === 'number' && payload.limit > 0 ? payload.limit : 30
  const ep = endpointById(endpoint)
  const params = payload.params && typeof payload.params === 'object' && !Array.isArray(payload.params)
    ? (payload.params as Record<string, unknown>)
    : {}
  const text = (key: string): string => (typeof params[key] === 'string' ? String(params[key]) : '')
  switch (endpoint) {
    case 'a-share.prices.snapshot':
      return { ...quote({ codes: ['600519.SH', '300750.SZ', '000001.SH'] }), endpoint }
    case 'a-share.valuations.snapshot':
      return { ...valuation({ codes: ['600519.SH', '300750.SZ'] }), endpoint }
    case 'a-share.calendar.trading-days':
      return { ...calendar({}), endpoint }
    case 'meta.tickers.search':
      return { ...tickerSearch({ keyword: text('q') || '茅台' }), endpoint }
    case 'a-share.financials.income-statements':
      return { ...financials({ kind: 'income' }), endpoint }
    case 'a-share.special-data.limit-up-pool':
      return { ...limitPool({ pool: 'up', limit }), endpoint }
    case 'a-share.special-data.limit-up-statistics':
      return { ...limitStats(), endpoint }
    case 'a-share.special-data.hot-stock-list':
      return { ...hotRank({ kind: 'hot', limit }), endpoint }
    case 'a-share.auction.snapshot':
      return { ...auction({ view: 'snapshot', codes: [] }), endpoint }
    case 'a-share-index.catalog.ths-index-list':
      return { ...index({ view: 'catalog', tag: text('tag') || 'cn_concept' }), endpoint }
    case 'fund.profile.detail':
      return { ...fund({ view: 'profile' }), endpoint }
    case 'news.flash.list':
      return { ...news({ view: 'flash' }), endpoint }
    case 'futures.varieties.list':
      return { ...futures({ view: 'varieties' }), endpoint }
    case 'options.varieties.list':
      return { ...options({ view: 'varieties' }), endpoint }
    case 'a-share.ipo.calendar-list':
      return { ...ipo({ view: 'a-ipo' }), endpoint }
    case 'dump.market-dumps.daily-k.download-url':
      return { ...marketDump({ kind: '10y' }), endpoint }
  }
  if (ep?.permission === 'client') {
    return { ...clientOnly(ep.title), endpoint }
  }
  if (endpoint === '') {
    return envelope({ endpoint, count: 0, items: [], note: '请先选择端点（目录 → 端点 → 通用查询）。' })
  }
  // 未内置样例的长尾端点：返回一条演示记录，形状足以验证「表格/原始 JSON 切换」。
  const detail = ep?.title ?? endpoint
  const items = [
    { thscode: '600519.SH', ticker: '600519', name: '贵州茅台', value: 1, date: TRADE_DATE, date_ms: Date.parse(TRADE_DATE + 'T00:00:00+08:00'), note: detail + ' · 演示记录 1' },
    { thscode: '300750.SZ', ticker: '300750', name: '宁德时代', value: 2, date: TRADE_DATE, date_ms: Date.parse(TRADE_DATE + 'T00:00:00+08:00'), note: detail + ' · 演示记录 2' },
  ]
  return envelope({
    endpoint,
    title: detail,
    count: items.length,
    items: items.slice(0, limit),
    note: '演示环境未内置该端点的真实样例，返回结构化占位记录；参数已按目录校验（键名大小写敏感）。',
  })
}

// ---------- 注册 ----------

/** 方法名 → 演示实现（与 plugins/fuyao 的 21 个前端方法一一对应）。 */
const HANDLERS: Record<string, (payload: Row) => unknown> = {
  catalog,
  query,
  quote,
  valuation,
  kline,
  limitPool,
  limitStats,
  hotRank,
  special,
  auction,
  capitalFlow,
  financials,
  tickerSearch,
  calendar,
  index,
  fund,
  futures,
  options,
  news,
  ipo,
  marketDump,
}

/** 供 api.mock 的 Invoke 分支兜底调用（registry 命中时不会走到这里）。 */
export function fuyaoMock(method: string, payload: Record<string, unknown>): unknown {
  const fn = HANDLERS[method]
  if (!fn) throw new Error('kernel: 方法不存在 ' + method)
  return fn(payload)
}

/** 清点有 mock 的方法（面板脚注与自检用）。 */
export function fuyaoMockMethods(): string[] {
  return Object.keys(HANDLERS)
}

let installed = false

/** 注册 21 个 fuyao 演示方法；重复调用无副作用。 */
export function installFuyaoMocks(): void {
  if (installed) return
  installed = true
  for (const [method, fn] of Object.entries(HANDLERS)) {
    registerMock('fuyao', method, async (payload, ctx) => {
      await ctx.sleep(method === 'catalog' ? 40 : 90)
      return fn(payload)
    })
  }
}
