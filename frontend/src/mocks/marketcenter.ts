// 市场中心演示数据：指数、涨跌家数、涨停梯队、龙虎榜。
// 只在浏览器直开时由 panels/marketcenter.vue 首次加载注册（幂等），不改 api.mock.ts。
// 数据是确定性的，便于对照截图；红涨绿跌由面板按数值方向着色。

import { registerMock } from './registry'
import type { HotStock, LimitUpStock, LongHuRow, MarketBreadth, MarketIndex } from '../types'

const DEMO_DATE = '2026-11-10'
const DEMO_UPDATED_AT = DEMO_DATE + 'T15:05:00+08:00'

const INDICES: MarketIndex[] = [
  { code: '000001', name: '上证指数', price: 3663.72, change: 52.31, changePct: 1.45, amount: 7.42e11 },
  { code: '399001', name: '深证成指', price: 11241.54, change: 113.32, changePct: 1.02, amount: 9.31e11 },
  { code: '000688', name: '科创50', price: 1265.02, change: 10.78, changePct: 0.86, amount: 7.35e10 },
  { code: '399006', name: '创业板指', price: 2494.4, change: 43.62, changePct: 1.78, amount: 4.86e11 },
  { code: '899050', name: '北证50', price: 902.51, change: 5.66, changePct: 0.63, amount: 1.62e10 },
]

const BREADTH: MarketBreadth = {
  date: DEMO_DATE,
  up: 3128,
  down: 1892,
  flat: 321,
  limitUp: 84,
  limitDown: 3,
  broken: 19,
  brokenRate: 18.4,
  amount: 1.673e12,
}

const LADDER: LimitUpStock[] = [
  { code: '002310', name: '东方新能', price: 28.1, changePct: 10.2, boards: 6, firstTime: '09:25:00', lastTime: '09:25:00', reason: '电力+储能+国企改革', amount: 3.24e8, turnover: 1.37 },
  { code: '601188', name: '龙江交通', price: 4.61, changePct: 10.02, boards: 5, firstTime: '09:25:02', lastTime: '14:48:56', reason: '铁路公路+东北振兴', amount: 1.4e7, turnover: 32.44 },
  { code: '603366', name: '日出东方', price: 10.88, changePct: 10.01, boards: 4, firstTime: '09:25:01', lastTime: '09:25:01', reason: '厨卫电器+光伏', amount: 7.8e8, turnover: 0.71 },
  { code: '002708', name: '光洋股份', price: 24.17, changePct: 10.01, boards: 3, firstTime: '09:30:12', lastTime: '09:30:12', reason: '汽车零部件+机器人', amount: 2.65e8, turnover: 3.14 },
  { code: '600327', name: '大东方', price: 5.94, changePct: 10.0, boards: 3, firstTime: '09:30:20', lastTime: '09:30:20', reason: '一般零售+医疗健康', amount: 2.17e8, turnover: 7.17 },
  { code: '002623', name: '亚玛顿', price: 21.21, changePct: 10.01, boards: 2, firstTime: '09:25:00', lastTime: '09:25:00', reason: '光伏设备+超薄玻璃', amount: 2.37e8, turnover: 0.69 },
  { code: '000530', name: '冰山冷热', price: 10.53, changePct: 10.03, boards: 2, firstTime: '09:30:15', lastTime: '09:45:12', reason: '制冷设备+冷链物流', amount: 1.26e8, turnover: 24.45 },
  { code: '600776', name: '东方通信', price: 13.65, changePct: 9.99, boards: 2, firstTime: '14:35:26', lastTime: '14:56:29', reason: '专网通信+卫星互联网', amount: 3.3e7, turnover: 8.75 },
  { code: '002919', name: '名臣健康', price: 18.2, changePct: 10.0, boards: 2, firstTime: '10:12:08', lastTime: '13:22:41', reason: '日化+医美', amount: 6.2e7, turnover: 9.84 },
  { code: '301052', name: '果麦文化', price: 48.96, changePct: 20.0, boards: 1, firstTime: '14:16:16', lastTime: '14:56:03', reason: 'AI短剧+AI视频+图书出版', amount: 2.2e7, turnover: 52.48 },
  { code: '002886', name: '沃特股份', price: 22.3, changePct: 10.01, boards: 1, firstTime: '14:35:28', lastTime: '14:35:28', reason: 'PTFE薄膜+PCB材料+机器人', amount: 4.88e7, turnover: 10.95 },
  { code: '600261', name: '阳光照明', price: 5.85, changePct: 9.96, boards: 1, firstTime: '09:30:16', lastTime: '09:30:16', reason: '照明设备+出口', amount: 2.25e8, turnover: 7.89 },
]

const LONGHU: LongHuRow[] = [
  { code: '002384', name: '东山精密', price: 92.06, changePct: 10.0, reason: '日涨幅偏离值达到7%的前5只证券', netBuy: 7.22e8, buy: 2.38e9, sell: 1.66e9, date: DEMO_DATE },
  { code: '002463', name: '沪电股份', price: 89.43, changePct: 10.0, reason: '日涨幅偏离值达到7%的前5只证券', netBuy: 3.86e8, buy: 1.4e9, sell: 1.01e9, date: DEMO_DATE },
  { code: '603920', name: '世运电路', price: 39.42, changePct: 8.39, reason: '连续三个交易日涨幅偏离值累计达到20%', netBuy: 3.73e8, buy: 9.36e8, sell: 5.63e8, date: DEMO_DATE },
  { code: '688801', name: '燧原科技', price: 494.0, changePct: 11.27, reason: '连续3个交易日收盘涨幅偏离值累计达到30%', netBuy: 2.56e8, buy: 1.22e9, sell: 9.61e8, date: DEMO_DATE },
  { code: '002281', name: '光迅科技', price: 103.93, changePct: 10.0, reason: '日涨幅偏离值达到7%的前5只证券', netBuy: 2.46e8, buy: 8.47e8, sell: 6.02e8, date: DEMO_DATE },
  { code: '300825', name: '阿尔特', price: 14.92, changePct: 6.88, reason: '连续三个交易日涨幅偏离值累计达到30%', netBuy: 2.27e8, buy: 5.92e8, sell: 3.65e8, date: DEMO_DATE },
  { code: '301052', name: '果麦文化', price: 48.96, changePct: 20.0, reason: '日换手率达到30%的前5只证券；日涨幅达到15%的前5只证券', netBuy: 2.04e8, buy: 2.95e8, sell: 9.1e7, date: DEMO_DATE },
  { code: '000678', name: '襄阳轴承', price: 21.19, changePct: 10.02, reason: '日换手率达到20%的前5只证券', netBuy: -8.6e7, buy: 1.42e8, sell: 2.28e8, date: DEMO_DATE },
]

const HOT_HOUR: HotStock[] = [
  { code: '600715', name: '文投控股', market: 'SH', price: 8.42, changePct: 10.18, heat: 529823, rank: 1, rankChange: 2, conceptTags: ['短剧游戏', '手机游戏'], popularityTag: '12天8板', reason: 'AI漫剧+剧集发行+北京国资' },
  { code: '300308', name: '中际旭创', market: 'SZ', price: 178.5, changePct: 15.07, heat: 486210, rank: 2, rankChange: 1, conceptTags: ['CPO', 'F5G概念'], popularityTag: '持续上榜', reason: '光模块+AI算力' },
  { code: '300502', name: '新易盛', market: 'SZ', price: 146.2, changePct: 16.98, heat: 471543, rank: 3, rankChange: -1, conceptTags: ['CPO', '5G'], popularityTag: '持续上榜', reason: '高速光模块+海外需求' },
  { code: '002463', name: '沪电股份', market: 'SZ', price: 89.43, changePct: 10.0, heat: 402118, rank: 4, rankChange: 3, conceptTags: ['PCB', 'AI服务器'], popularityTag: '机构关注', reason: 'AI服务器PCB+业绩预增' },
  { code: '688801', name: '燧原科技', market: 'SH', price: 494.0, changePct: 11.27, heat: 388901, rank: 5, rankChange: 0, conceptTags: ['AI芯片', '国产算力'], popularityTag: '新股', reason: '国产GPU+算力租赁' },
  { code: '002384', name: '东山精密', market: 'SZ', price: 92.06, changePct: 10.0, heat: 355420, rank: 6, rankChange: -2, conceptTags: ['PCB', '苹果概念'], popularityTag: '持续上榜', reason: '消费电子+AI硬件' },
  { code: '601127', name: '赛力斯', market: 'SH', price: 142.8, changePct: 6.32, heat: 341207, rank: 7, rankChange: 4, conceptTags: ['华为汽车', '智能驾驶'], popularityTag: '人气回升', reason: '问界销量+智驾' },
  { code: '002230', name: '科大讯飞', market: 'SZ', price: 58.7, changePct: 4.11, heat: 329874, rank: 8, rankChange: 0, conceptTags: ['AI应用', '大模型'], popularityTag: '持续上榜', reason: '星火大模型+教育AI' },
]

const HOT_DAY: HotStock[] = [
  { ...HOT_HOUR[1], rank: 1, rankChange: 1, heat: 5862511 },
  { ...HOT_HOUR[2], rank: 2, rankChange: -1, heat: 4715432 },
  { ...HOT_HOUR[0], rank: 3, rankChange: 2, heat: 4210284 },
  { ...HOT_HOUR[3], rank: 4, rankChange: 0, heat: 3982450 },
  { ...HOT_HOUR[4], rank: 5, rankChange: 3, heat: 3761220 },
]

/** 涨停梯队的金额口径与后端一致：演示数据按同花顺的封单额展示。 */
function ladderData(): Array<LimitUpStock & { amountKind: string }> {
  return LADDER.map((item) => ({ ...item, amountKind: '封单额' }))
}

let installed = false

/** 复制数组，避免调用方修改演示数据。 */
function copy<T>(items: T[]): T[] {
  return items.map((item) => ({ ...item }))
}

/** 按 limit 截断（默认 50，上限 200），与后端参数口径一致。 */
function clip<T>(items: T[], limit: unknown): T[] {
  const raw = typeof limit === 'number' && Number.isFinite(limit) ? Math.floor(limit) : 50
  const size = raw <= 0 ? 50 : Math.min(raw, 200)
  return copy(items.slice(0, size))
}

/** 组装完整快照。 */
function snapshot(): Record<string, unknown> {
  return {
    indices: copy(INDICES),
    breadth: { ...BREADTH },
    ladder: ladderData(),
    longhu: copy(LONGHU),
    hot: copy(HOT_HOUR),
    updatedAt: DEMO_UPDATED_AT,
    warnings: [],
  }
}

/** 注册市场中心演示方法；重复调用无副作用。 */
export function installMarketCenterMocks(): void {
  if (installed) return
  installed = true

  registerMock('marketcenter', 'snapshot', async (_p, ctx) => {
    await ctx.sleep(160)
    return snapshot()
  })
  registerMock('marketcenter', 'indices', async (_p, ctx) => {
    await ctx.sleep(80)
    return copy(INDICES)
  })
  registerMock('marketcenter', 'breadth', async (_p, ctx) => {
    await ctx.sleep(90)
    return { ...BREADTH }
  })
  registerMock('marketcenter', 'ladder', async (p, ctx) => {
    await ctx.sleep(110)
    return clip(ladderData(), p.limit)
  })
  registerMock('marketcenter', 'longhu', async (p, ctx) => {
    await ctx.sleep(120)
    return clip(LONGHU, p.limit)
  })
  registerMock('marketcenter', 'hot', async (p, ctx) => {
    await ctx.sleep(100)
    const list = p.type === 'day' ? HOT_DAY : HOT_HOUR
    return clip(list, p.limit)
  })
}
