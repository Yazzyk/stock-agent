// 行情域演示数据（frontend-market 维护）：自选同步诊断、资金流历史/批量、回测逐笔成交。
// 只在浏览器直开（npm run dev，无 Wails 注入）时生效，形状对齐 docs/ITERATION-2026-11.md。
// 说明：这里的走势用确定性伪随机生成，同一代码每次得到同一条曲线，便于对照截图。

import { PRIVACY_SETTINGS_URL, THS_LOGIN_URL } from '../lib/api.market'
import type { FundFlowPoint } from '../lib/api.market'
import type { Bar, MinutePoint, MinuteSeries, Period } from '../types'
import { registerMock } from './registry'

// ---------- 确定性伪随机 ----------

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

const END_DATE = new Date(2026, 10, 10)

function tradingDays(count: number, end: Date): string[] {
  const out: string[] = []
  const d = new Date(end.getTime())
  while (out.length < count) {
    const day = d.getDay()
    if (day !== 0 && day !== 6) {
      const p = (n: number) => (n < 10 ? '0' + n : String(n))
      out.unshift(d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()))
    }
    d.setDate(d.getDate() - 1)
  }
  return out
}

// ---------- 演示标的 ----------

interface DemoStock {
  code: string
  name: string
  market: 'sh' | 'sz'
  group: string
  price: number
  pct: number
}

const STOCKS: DemoStock[] = [
  { code: '600519', name: '贵州茅台', market: 'sh', group: '核心持仓', price: 1523.4, pct: 1.24 },
  { code: '600036', name: '招商银行', market: 'sh', group: '核心持仓', price: 38.62, pct: -0.58 },
  { code: '000858', name: '五粮液', market: 'sz', group: '核心持仓', price: 142.7, pct: 2.06 },
  { code: '300750', name: '宁德时代', market: 'sz', group: '核心持仓', price: 246.18, pct: 3.47 },
  { code: '601318', name: '中国平安', market: 'sh', group: '观察池', price: 54.31, pct: -1.12 },
  { code: '002594', name: '比亚迪', market: 'sz', group: '观察池', price: 268.9, pct: 0.86 },
  { code: '688981', name: '中芯国际', market: 'sh', group: '观察池', price: 92.44, pct: 5.62 },
  { code: '601899', name: '紫金矿业', market: 'sh', group: '观察池', price: 19.87, pct: 2.31 },
  { code: '600900', name: '长江电力', market: 'sh', group: '观察池', price: 28.16, pct: 0.42 },
  { code: '002415', name: '海康威视', market: 'sz', group: '观察池', price: 31.72, pct: -0.94 },
  { code: '601012', name: '隆基绿能', market: 'sh', group: '观察池', price: 17.43, pct: -2.18 },
  { code: '000001', name: '上证指数', market: 'sh', group: '指数', price: 3418.62, pct: 0.72 },
]

function stockName(code: string): string {
  return STOCKS.find((s) => s.code === code)?.name ?? '演示标的'
}

// ---------- K 线（量化演示用；K 线面板的 bars 由 api.mock 提供） ----------

function makeBars(code: string, period: Period): Bar[] {
  const factor = period === 'week' ? 5 : period === 'month' ? 21 : 1
  const count = period === 'min60' ? 160 : 280
  const dates = tradingDays(count, END_DATE)
  const rand = rng(seedOf(code + '|' + period))
  let close = 8 + (seedOf(code) % 40)
  const bars: Bar[] = []
  for (let i = 0; i < count; i++) {
    const drift = (rand() - 0.47) * (factor > 1 ? 0.05 : 0.026)
    const open = close * (1 + (rand() - 0.5) * 0.01)
    close = Math.max(1.2, close * (1 + drift))
    const high = Math.max(open, close) * (1 + rand() * 0.012)
    const low = Math.min(open, close) * (1 - rand() * 0.012)
    const volume = Math.round((2 + rand() * 9) * 1e4 * factor)
    bars.push({ date: dates[i] ?? '', open: round2(open), high: round2(high), low: round2(low), close: round2(close), volume, amount: Math.round(volume * close) })
  }
  return bars
}

function ma(bars: Bar[], n: number): Array<number | null> {
  const out: Array<number | null> = []
  let sum = 0
  for (let i = 0; i < bars.length; i++) {
    sum += bars[i]?.close ?? 0
    if (i >= n) sum -= bars[i - n]?.close ?? 0
    out.push(i >= n - 1 ? round2(sum / n) : null)
  }
  return out
}

// ---------- 资金流历史（自选列与资金面板共用同一份序列） ----------

interface DemoFlow {
  dates: string[]
  points: FundFlowPoint[]
}

const flowCache = new Map<string, DemoFlow>()

function flowSeries(code: string): DemoFlow {
  const cached = flowCache.get(code)
  if (cached) return cached
  const stock = STOCKS.find((s) => s.code === code)
  const rand = rng(seedOf('flow|' + code))
  const days = 60
  const dates = tradingDays(days, END_DATE)
  const points: FundFlowPoint[] = []
  // 收盘价从当前价往回推，保证最后一天与行情一致
  let close = stock?.price ?? 20 + (seedOf(code) % 60)
  const closes: number[] = new Array(days).fill(0)
  for (let i = days - 1; i >= 0; i--) {
    closes[i] = round2(close)
    const step = (rand() - 0.48) * 0.024
    close = Math.max(1.2, close / (1 + step))
  }
  // 主力净额：带一段趋势，避免每天纯随机
  const base = (0.4 + rand() * 1.6) * 1e8
  let bias = 0
  for (let i = 0; i < days; i++) {
    if (rand() > 0.86) bias = (rand() - 0.5) * 2.4
    const mainNet = Math.round(base * (rand() - 0.5 + bias * 0.55))
    const superNet = Math.round(mainNet * (0.55 + rand() * 0.25))
    const largeNet = mainNet - superNet
    const mediumNet = Math.round(-mainNet * (0.3 + rand() * 0.3))
    const smallNet = -mainNet - mediumNet
    const prev = closes[i - 1] ?? closes[i] ?? 0
    const cur = closes[i] ?? 0
    points.push({
      date: dates[i] ?? '',
      mainNet,
      superNet,
      largeNet,
      mediumNet,
      smallNet,
      mainPct: round2((mainNet / Math.max(1, base * 12)) * 100),
      close: cur,
      changePct: prev > 0 ? round2((cur / prev - 1) * 100) : 0,
    })
  }
  const out: DemoFlow = { dates, points }
  flowCache.set(code, out)
  return out
}

// ---------- 同步诊断：按「未授权 → 已读取登录态 → 已授权本机客户端」三步演进 ----------

let demoCookie = false
let demoChecks = 0

interface DemoAction {
  kind: string
  label: string
  value?: string
}

function action(kind: string, label: string, value?: string): DemoAction {
  return value === undefined ? { kind, label } : { kind, label, value }
}

function checkMock(): unknown {
  demoChecks += 1
  const localOk = demoCookie && demoChecks >= 3
  const dataDir = '~/.guanlan'
  if (!demoCookie) {
    return {
      local: {
        id: 'ths-local',
        label: '本机同花顺客户端',
        available: false,
        state: 'denied',
        detail: '读取同花顺本地数据目录需要「完全磁盘访问权限」',
      },
      web: { id: 'ths-web', label: '网页登录态', available: false, state: 'no-login', detail: '未找到同花顺登录 cookie' },
      manual: { id: 'manual', label: '手工导入', available: true, state: 'empty', detail: '数据目录里没有手工导入的自选文件', items: 0 },
      recommended: 'ths-web',
      ready: false,
      message: '首次使用需要授权一个数据源：登录同花顺网页，或给应用授予完全磁盘访问权限。',
      steps: [
        {
          id: 'local',
          title: '读取本机同花顺数据',
          detail: '授予「完全磁盘访问权限」后，可直接读取客户端自选分组。',
          done: false,
          action: action('open-settings', '打开系统设置', PRIVACY_SETTINGS_URL),
        },
        {
          id: 'web-login',
          title: '登录同花顺网页',
          detail: '在浏览器完成登录，登录态保存在本机浏览器里。',
          done: false,
          action: action('open-url', '打开登录页', THS_LOGIN_URL),
        },
        {
          id: 'web-capture',
          title: '读取浏览器登录态',
          detail: '从本机浏览器读取同花顺 cookie（Safari 优先，Chrome 尽力）。',
          done: false,
          action: action('capture-cookie', '自动读取'),
        },
        {
          id: 'manual',
          title: '手工导入（兜底）',
          detail: '把同花顺导出的自选 CSV 放进数据目录，或手动粘贴登录态。',
          done: false,
          action: action('copy', '复制数据目录', dataDir),
        },
      ],
      checkedAt: new Date().toISOString(),
    }
  }
  return {
    local: localOk
      ? { id: 'ths-local', label: '本机同花顺客户端', available: true, state: 'ok', detail: '已读取客户端自选', groups: 4, items: STOCKS.length }
      : { id: 'ths-local', label: '本机同花顺客户端', available: false, state: 'denied', detail: '未授权完全磁盘访问权限，本次使用网页登录态' },
    web: { id: 'ths-web', label: '网页登录态', available: true, state: 'ok', detail: '已保存登录 cookie（演示）', groups: 3, items: 18 },
    manual: { id: 'manual', label: '手工导入', available: true, state: 'empty', detail: '未导入手工文件', items: 0 },
    recommended: localOk ? 'ths-local' : 'ths-web',
    ready: true,
    message: '数据源可用，可自动同步同花顺自选。',
    steps: [
      { id: 'local', title: '读取本机同花顺数据', detail: localOk ? '已授权并读取成功。' : '未授权，可在系统设置里开启后重试。', done: localOk, action: action('open-settings', '打开系统设置', PRIVACY_SETTINGS_URL) },
      { id: 'web-login', title: '登录同花顺网页', detail: '已检测到登录态。', done: true, action: action('open-url', '重新登录', THS_LOGIN_URL) },
      { id: 'web-capture', title: '读取浏览器登录态', detail: '已从浏览器读取并保存。', done: true, action: action('capture-cookie', '重新读取') },
      { id: 'manual', title: '手工导入（兜底）', detail: '可选：把自选 CSV 放进数据目录。', done: false, action: action('copy', '复制数据目录', dataDir) },
    ],
    checkedAt: new Date().toISOString(),
  }
}

// ---------- 回测（演示版：顶带 trades 逐笔成交，形状对齐 §3.7.1） ----------

interface Fill {
  date: string
  side: 'buy' | 'sell'
  price: number
  qty: number
  reason?: string
}

interface SimPoint {
  date: string
  equity: number
  cash: number
  position: number
  shares: number
}

function maAt(bars: Bar[], i: number, n: number): number | null {
  if (i < n - 1) return null
  let sum = 0
  for (let k = i - n + 1; k <= i; k++) sum += bars[k]?.close ?? 0
  return sum / n
}

function simulate(bars: Bar[], strategy: string, params: Record<string, unknown>, initialCash: number): {
  equity: SimPoint[]
  fills: Fill[]
  costs: { commission: number; stampTax: number; transfer: number; slippage: number }
  notional: number
} {
  let cash = initialCash
  let shares = 0
  const equity: SimPoint[] = []
  const fills: Fill[] = []
  const costs = { commission: 0, stampTax: 0, transfer: 0, slippage: 0 }
  let notional = 0
  const fast = Math.max(2, Math.round(typeof params.fast === 'number' ? params.fast : 5))
  const slow = Math.max(fast + 1, Math.round(typeof params.slow === 'number' ? params.slow : 20))
  const lookback = Math.max(2, Math.round(typeof params.lookback === 'number' ? params.lookback : 20))
  const threshold = typeof params.threshold === 'number' ? params.threshold : 0.05
  const wantFor = (i: number, bar: Bar): boolean => {
    if (strategy === 'buyhold') return true
    if (strategy === 'momentum') {
      const prev = bars[i - lookback]?.close
      return typeof prev === 'number' && prev > 0 && bar.close / prev - 1 > threshold
    }
    // ma / rsi 在演示里都用均线交叉（真实引擎里 RSI 有自己的实现）
    const f = maAt(bars, i - 1, fast)
    const s = maAt(bars, i - 1, slow)
    return f !== null && s !== null && f > s
  }
  for (let i = 1; i < bars.length; i++) {
    const bar = bars[i]
    if (!bar) continue
    const px = bar.open
    const wantUp = wantFor(i, bar)
    const equityAtOpen = cash + shares * px
    let want = wantUp ? Math.floor(equityAtOpen / px / 100) * 100 : 0
    if (want < 0) want = 0
    if (want > shares) {
      const qty = want - shares
      const amount = px * qty
      const commission = Math.max(amount * 0.0003, 5)
      const transfer = amount * 0.00001
      if (amount + commission + transfer <= cash) {
        cash -= amount + commission + transfer
        costs.commission += commission
        costs.transfer += transfer
        costs.slippage += amount * 0.001
        notional += amount
        shares = want
        fills.push({ date: bar.date, side: 'buy', price: round2(px), qty, reason: strategy === 'momentum' ? '动量触发' : 'MA' + fast + ' 上穿 MA' + slow })
      }
    } else if (want < shares) {
      const qty = shares - want
      const amount = px * qty
      const commission = Math.max(amount * 0.0003, 5)
      const stamp = amount * 0.0005
      const transfer = amount * 0.00001
      cash += amount - commission - stamp - transfer
      costs.commission += commission
      costs.stampTax += stamp
      costs.transfer += transfer
      costs.slippage += amount * 0.001
      notional += amount
      shares = want
      fills.push({ date: bar.date, side: 'sell', price: round2(px), qty, reason: strategy === 'momentum' ? '动量转弱' : 'MA' + fast + ' 下穿 MA' + slow })
    }
    equity.push({ date: bar.date, equity: round2(cash + shares * bar.close), cash: round2(cash), position: round2(shares * bar.close), shares })
  }
  return { equity, fills, costs, notional }
}

function dailyReturns(equity: SimPoint[]): number[] {
  const out: number[] = []
  for (let i = 1; i < equity.length; i++) {
    const prev = equity[i - 1]?.equity ?? 0
    const cur = equity[i]?.equity ?? 0
    if (prev > 0) out.push(cur / prev - 1)
  }
  return out
}

function sharpeOf(equity: SimPoint[]): number {
  const rs = dailyReturns(equity)
  if (rs.length < 2) return 0
  const mean = rs.reduce((a, b) => a + b, 0) / rs.length
  const variance = rs.reduce((a, b) => a + (b - mean) * (b - mean), 0) / (rs.length - 1)
  const sd = Math.sqrt(variance)
  if (sd === 0) return 0
  return round2((mean / sd) * Math.sqrt(252))
}

function drawdownOf(equity: SimPoint[]): number {
  let peak = 0
  let mdd = 0
  for (const p of equity) {
    peak = Math.max(peak, p.equity)
    if (peak > 0) mdd = Math.max(mdd, 1 - p.equity / peak)
  }
  return Math.round(mdd * 10000) / 10000
}

function winRateOf(fills: Fill[]): number {
  let open: Fill | null = null
  let wins = 0
  let closed = 0
  for (const f of fills) {
    if (f.side === 'buy') {
      open = f
      continue
    }
    if (open) {
      closed += 1
      if (f.price > open.price) wins += 1
      open = null
    }
  }
  return closed === 0 ? 0 : Math.round((wins / closed) * 10000) / 10000
}

function quantRunMock(p: Record<string, unknown>): unknown {
  const code = String(p.code ?? '600519')
  const period = (String(p.period ?? 'day') as Period) ?? 'day'
  const strategy = String(p.strategy ?? 'ma')
  const params = (typeof p.params === 'object' && p.params !== null ? p.params : {}) as Record<string, unknown>
  const initialCash = typeof p.initialCash === 'number' && p.initialCash > 0 ? p.initialCash : 100000
  const bars = makeBars(code, period)
  const sim = simulate(bars, strategy, params, initialCash)
  const finalEquity = sim.equity.length > 0 ? (sim.equity[sim.equity.length - 1]?.equity ?? initialCash) : initialCash
  const totalReturn = initialCash > 0 ? finalEquity / initialCash - 1 : 0
  const years = Math.max(0.08, bars.length / 252)
  const cagr = Math.pow(1 + totalReturn, 1 / years) - 1
  const costTotal = sim.costs.commission + sim.costs.stampTax + sim.costs.transfer + sim.costs.slippage

  const STRATEGY_LABEL: Record<string, string> = { ma: '均线交叉', momentum: '动量突破', rsi: 'RSI 反转', buyhold: '买入持有' }
  const result = {
    strategy: STRATEGY_LABEL[strategy] ?? strategy,
    code,
    start: bars[0]?.date ?? '',
    end: bars[bars.length - 1]?.date ?? '',
    bars: bars.length,
    initialCash,
    finalEquity: round2(finalEquity),
    totalReturn: Math.round(totalReturn * 10000) / 10000,
    cagr: Math.round(cagr * 10000) / 10000,
    maxDrawdown: drawdownOf(sim.equity),
    sharpe: sharpeOf(sim.equity),
    sortino: round2(sharpeOf(sim.equity) * 1.28),
    winRate: winRateOf(sim.fills),
    turnover: Math.round((sim.notional / initialCash) * 100) / 100,
    tradeCount: sim.fills.length,
    trades: [],
    equity: sim.equity,
    costs: {
      commission: round2(sim.costs.commission),
      stampTax: round2(sim.costs.stampTax),
      transfer: round2(sim.costs.transfer),
      slippage: round2(sim.costs.slippage),
    },
    warnings: ['演示数据：随机游走序列，结论仅用于验证面板'],
  }

  const report =
    '# 回测报告 · ' + result.strategy + '（' + code + '）\n\n' +
    '- 区间：' + result.start + ' ~ ' + result.end + '（' + result.bars + ' 根）\n' +
    '- 总收益：' + (result.totalReturn * 100).toFixed(2) + '%，年化：' + (result.cagr * 100).toFixed(2) +
    '%，最大回撤：' + (result.maxDrawdown * 100).toFixed(2) + '%\n' +
    '- 夏普：' + result.sharpe.toFixed(2) + '，胜率：' + (result.winRate * 100).toFixed(1) +
    '%，成交 ' + result.tradeCount + ' 笔，换手 ' + result.turnover.toFixed(2) + 'x\n' +
    '- 成本合计：' + costTotal.toFixed(2) + ' 元\n\n' +
    '## 逐笔成交\n\n' +
    (sim.fills.length === 0
      ? '（无成交）\n'
      : sim.fills.slice(0, 20).map((f) => '- ' + f.date + ' ' + (f.side === 'buy' ? '买入' : '卖出') + ' ' + f.qty + ' 股 @ ' + f.price.toFixed(2)).join('\n') + '\n')

  return {
    code,
    period,
    adjust: 'qfq',
    warning: '',
    result,
    report,
    trades: sim.fills,
  }
}

// ---------- 板块/概念演示数据（资金流线 + 双榜共用） ----------

/** 演示行业板块：[代码, 名称, 涨跌幅%, 主力净额(元)]，与 api.mock 的演示榜一致。 */
const DEMO_SECTORS: Array<[string, string, number, number]> = [
  ['BK0475', '半导体', 3.62, 4.82e9],
  ['BK0447', '证券Ⅱ', 2.14, 3.11e9],
  ['BK0727', '光伏设备', 1.88, 2.63e9],
  ['BK0478', '白酒Ⅱ', 1.42, 1.94e9],
  ['BK1036', '电池', 1.16, 1.72e9],
  ['BK0420', '国有大型银行Ⅱ', 0.64, 1.05e9],
  ['BK0451', '房地产开发', -0.42, -8.3e8],
  ['BK0737', '游戏Ⅱ', -0.86, -1.12e9],
  ['BK0456', '医药商业', -1.14, -1.51e9],
  ['BK0546', '煤炭开采', -1.62, -2.34e9],
  ['BK0481', '旅游及景区', -2.08, -2.96e9],
  ['BK0538', '能源金属', -2.44, -3.42e9],
]

/** 演示概念板块：[代码, 名称, 涨跌幅%, 主力净额(元)]。 */
const DEMO_CONCEPTS: Array<[string, string, number, number]> = [
  ['BK1101', 'AI 算力', 4.12, 6.42e9],
  ['BK1127', '国产替代', 3.38, 5.18e9],
  ['BK1098', '机器人概念', 2.86, 4.07e9],
  ['BK1052', '华为概念', 2.12, 3.26e9],
  ['BK1145', '低空经济', 1.54, 2.44e9],
  ['BK0987', '卫星互联网', 1.02, 1.63e9],
  ['BK0876', '白酒概念', -0.36, -6.2e8],
  ['BK0892', '猪肉概念', -0.94, -1.28e9],
  ['BK0903', '医美概念', -1.38, -1.86e9],
  ['BK0911', '油气设服', -1.86, -2.12e9],
  ['BK0924', '盐湖提锂', -2.26, -2.88e9],
  ['BK0955', '氢能源', -2.72, -3.16e9],
]

const sectorFlowCache = new Map<string, DemoFlow>()

/** 按代码查演示板块行。 */
function sectorRow(code: string): [string, string, number, number] | undefined {
  return [...DEMO_SECTORS, ...DEMO_CONCEPTS].find((row) => row[0] === code)
}

/** 板块/概念的多日资金流：同一代码每次生成同一条序列，方向跟随当日净额。 */
function sectorFlowSeries(code: string): DemoFlow {
  const cached = sectorFlowCache.get(code)
  if (cached) return cached
  const row = sectorRow(code)
  const rand = rng(seedOf('sector-flow|' + code))
  const days = 60
  const dates = tradingDays(days, END_DATE)
  const scale = Math.abs(row?.[3] ?? 1e8)
  const bias = (row?.[3] ?? 1) >= 0 ? 0.22 : -0.22
  let close = 10 + (seedOf(code) % 40)
  const points: FundFlowPoint[] = []
  for (let i = 0; i < days; i++) {
    const wobble = (rand() - 0.5) * 1.1
    const mainNet = Math.round(scale * (bias + wobble) * 0.16)
    const superNet = Math.round(mainNet * (0.55 + rand() * 0.2))
    const largeNet = mainNet - superNet
    const mediumNet = Math.round(-mainNet * (0.3 + rand() * 0.25))
    const smallNet = -mainNet - mediumNet
    const prev = close
    close = Math.max(1.2, close * (1 + (rand() - 0.49) * 0.03))
    points.push({
      date: dates[i] ?? '',
      mainNet,
      superNet,
      largeNet,
      mediumNet,
      smallNet,
      mainPct: round2((mainNet / Math.max(1, scale * 0.5)) * 100),
      close: round2(close),
      changePct: round2((close / Math.max(0.01, prev) - 1) * 100),
    })
  }
  const out: DemoFlow = { dates, points }
  sectorFlowCache.set(code, out)
  return out
}

/** 取板块多日资金流的最近 days 根，形状与后端 fundflow.sectorHistory 一致。 */
function sectorHistoryPayload(code: string, days: number): unknown {
  const series = sectorFlowSeries(code)
  const points = series.points.slice(Math.max(0, series.points.length - days))
  return {
    code,
    name: sectorRow(code)?.[1] ?? code,
    days: points.length,
    source: 'sina（演示）',
    points,
  }
}

// ---------- 分时（走势列小图 / 大图演示） ----------

const minuteCache = new Map<string, MinuteSeries>()

/** 生成确定性的当日分时：09:30-11:30 + 13:01-15:00，共 241 点，末点对齐演示现价。 */
function minuteSeriesMock(code: string): MinuteSeries {
  const cached = minuteCache.get(code)
  if (cached) return cached
  const stock = STOCKS.find((s) => s.code === code)
  const lastPrice = stock?.price ?? 20 + (seedOf(code) % 60)
  const pct = stock?.pct ?? 0
  const prevClose = round2(lastPrice / (1 + pct / 100))
  const rand = rng(seedOf('minute|' + code))
  const pad = (n: number): string => (n < 10 ? '0' + n : String(n))
  const stamp = (total: number): string => pad(Math.floor(total / 60)) + ':' + pad(total % 60)
  const times: string[] = []
  for (let m = 9 * 60 + 30; m <= 11 * 60 + 30; m++) times.push(stamp(m))
  for (let m = 13 * 60 + 1; m <= 15 * 60; m++) times.push(stamp(m))

  const points: MinutePoint[] = []
  let price = prevClose
  let cumVolume = 0
  let cumAmount = 0
  const drift = (lastPrice - prevClose) / times.length
  for (let i = 0; i < times.length; i++) {
    const last = i === times.length - 1
    price = last ? lastPrice : Math.max(0.5, round2(price + drift + (rand() - 0.48) * prevClose * 0.0018))
    const volume = Math.round(30 + rand() * 380)
    const amount = Math.round(volume * 100 * price)
    cumVolume += volume
    cumAmount += amount
    points.push({
      time: times[i] ?? '',
      price,
      avg: cumVolume > 0 ? round2(cumAmount / (cumVolume * 100)) : undefined,
      volume,
      amount,
    })
  }
  const out: MinuteSeries = {
    code,
    name: stockName(code),
    date: '2026-11-10',
    prevClose,
    points,
    source: 'sina（演示）',
  }
  minuteCache.set(code, out)
  return out
}

// ---------- 注册 ----------

export function installMarketMocks(): void {
  // 同步诊断
  registerMock('watchlist', 'check', async (p, ctx) => {
    await ctx.sleep(160)
    return checkMock()
  })
  registerMock('watchlist', 'saveCookie', async (p, ctx) => {
    const cookie = typeof p.cookie === 'string' ? p.cookie.trim() : ''
    await ctx.sleep(220)
    if (cookie === '') {
      return { ok: false, source: '', message: '登录态为空：请从浏览器复制同花顺 cookie 后重试（形如 v=xxx; ck=yyy）。', check: checkMock() }
    }
    demoCookie = true
    demoChecks = 0
    return { ok: true, source: 'manual', message: '登录态已保存（演示数据）', check: checkMock() }
  })
  registerMock('watchlist', 'captureCookie', async (p, ctx) => {
    await ctx.sleep(320)
    demoCookie = true
    demoChecks = 0
    return { ok: true, source: 'safari', message: '已从 Safari 读取同花顺登录态（演示数据）', check: checkMock() }
  })
  registerMock('watchlist', 'privacy', async (p, ctx) => {
    await ctx.sleep(60)
    return {
      settingsUrl: PRIVACY_SETTINGS_URL,
      appPath: '/Applications/观澜.app',
      dataDir: '~/.guanlan',
      hint: '系统设置 → 隐私与安全性 → 完全磁盘访问权限，勾选「观澜」后重启应用。',
    }
  })

  // 资金流：排名（带 BK 代码，供面板点行加线）
  registerMock('fundflow', 'rank', async (p, ctx) => {
    await ctx.sleep(120)
    const kind = p.kind === 'concept' ? 'concept' : 'sector'
    const direction = p.direction === 'out' ? 'out' : p.direction === 'all' ? 'all' : 'in'
    const limit = typeof p.limit === 'number' ? Math.max(1, Math.min(100, p.limit)) : 20
    const rows = (kind === 'concept' ? DEMO_CONCEPTS : DEMO_SECTORS).map(([code, name, pct, main]) => ({
      code,
      name,
      kind,
      mainNet: Math.round(main),
      changePct: pct,
      mainPct: round2((main / Math.max(1, Math.abs(main) * 12)) * 100),
      source: 'sina（演示）',
    }))
    const filtered =
      direction === 'in'
        ? rows.filter((r) => r.mainNet > 0).sort((a, b) => b.mainNet - a.mainNet)
        : direction === 'out'
          ? rows.filter((r) => r.mainNet < 0).sort((a, b) => a.mainNet - b.mainNet)
          : rows.slice().sort((a, b) => b.mainNet - a.mainNet)
    return filtered.slice(0, limit).map((row, i) => ({ ...row, rank: i + 1 }))
  })
  registerMock('fundflow', 'sectorHistory', async (p, ctx) => {
    await ctx.sleep(140)
    const code = String(p.code ?? 'BK1201')
    const raw = typeof p.days === 'number' ? p.days : 20
    const days = Math.min(120, Math.max(1, raw))
    return sectorHistoryPayload(code, days)
  })
  registerMock('fundflow', 'sectorBatch', async (p, ctx) => {
    await ctx.sleep(180)
    const raw = Array.isArray(p.codes) ? p.codes.filter((c): c is string => typeof c === 'string') : []
    const codes = raw.filter((c) => c.trim() !== '').slice(0, 20)
    const d = typeof p.days === 'number' ? p.days : 20
    const days = Math.min(120, Math.max(1, d))
    return { items: codes.map((code) => sectorHistoryPayload(code, days)) }
  })

  // 资金流
  registerMock('fundflow', 'history', async (p, ctx) => {
    await ctx.sleep(140)
    const code = String(p.code ?? '600519')
    const days = Math.min(120, Math.max(5, typeof p.days === 'number' ? p.days : 20))
    const series = flowSeries(code)
    const points = series.points.slice(Math.max(0, series.points.length - days))
    return { code, name: stockName(code), days: points.length, source: 'sina（演示）', points }
  })
  registerMock('fundflow', 'batch', async (p, ctx) => {
    await ctx.sleep(180)
    const raw = Array.isArray(p.codes) ? p.codes.filter((c): c is string => typeof c === 'string') : []
    const codes = (raw.length > 0 ? raw : STOCKS.map((s) => s.code)).slice(0, 50)
    const items = codes.map((code) => {
      const series = flowSeries(code)
      const last = series.points[series.points.length - 1]
      const stock = STOCKS.find((s) => s.code === code)
      return {
        code,
        name: stockName(code),
        kind: 'stock',
        date: last?.date ?? '',
        mainNet: last?.mainNet ?? 0,
        superNet: last?.superNet ?? 0,
        largeNet: last?.largeNet ?? 0,
        mediumNet: last?.mediumNet ?? 0,
        smallNet: last?.smallNet ?? 0,
        mainPct: last?.mainPct ?? 0,
        changePct: stock?.pct ?? last?.changePct ?? 0,
        source: 'sina（演示）',
      }
    })
    return { items, date: items[0]?.date ?? '', source: 'sina（演示）' }
  })

  // 分时：走势列小图与大图共用同一份确定性序列
  registerMock('kline', 'minute', async (p, ctx) => {
    await ctx.sleep(60)
    return minuteSeriesMock(String(p.code ?? '600519'))
  })

  // K 线：补复权口径（真实后端会返回 adjust=qfq/none + warning 降级说明），演示固定前复权且不降级
  registerMock('kline', 'bars', async (p, ctx) => {
    await ctx.sleep(80)
    const code = String(p.code ?? '600519')
    const period = (String(p.period ?? 'day') as Period) ?? 'day'
    const bars = makeBars(code, period)
    return {
      code,
      name: stockName(code),
      period,
      bars,
      indicators: {
        ma5: ma(bars, 5),
        ma10: ma(bars, 10),
        ma20: ma(bars, 20),
        ma60: ma(bars, 60),
        volume: bars.map((b) => b.volume),
      },
      adjust: 'qfq',
    }
  })

  // 回测：补顶带 trades（真实后端由 backend-data 提供）
  registerMock('quant', 'run', async (p, ctx) => {
    await ctx.sleep(420)
    return quantRunMock(p)
  })

  // 策略清单 + 全局口径：演示模式也能展示「算法说明」与参数提示
  registerMock('quant', 'strategies', async (_p, ctx) => {
    await ctx.sleep(60)
    return { strategies: DEMO_QUANT_STRATEGIES, rules: DEMO_QUANT_RULES, costs: DEMO_QUANT_COSTS }
  })

  // 参数优化：演示候选表 + 推荐参数 + 样本外结论（不跑真实网格）
  registerMock('quant', 'optimize', async (p, ctx) => {
    await ctx.sleep(520)
    return quantOptimizeMock(p)
  })
}

// ---------- 量化演示数据 ----------

/** 演示用的策略清单（与后端 backtest.Specs 的字段形状一致，含 formula/rules）。 */
const DEMO_QUANT_STRATEGIES = [
  {
    id: 'ma',
    name: '均线交叉',
    desc: '快线上穿慢线满仓、下穿空仓',
    formula: 'MA(fast) > MA(slow) → 满仓；否则空仓',
    rules: ['收盘后比较快慢均线，次日开盘按信号调仓。', '参数 fast 越小、slow 越大，信号越少但更稳。', '震荡市容易出现来回打脸，需结合成本与滑点看。'],
    params: [
      { key: 'fast', label: '快线', type: 'int', min: 2, max: 60, step: 1 },
      { key: 'slow', label: '慢线', type: 'int', min: 5, max: 250, step: 1 },
    ],
    default: { fast: 5, slow: 20 },
  },
  {
    id: 'rsi',
    name: 'RSI 反转',
    desc: 'RSI 低于下限买入、高于上限卖出',
    formula: 'RSI(period) < lower → 满仓；RSI > upper → 空仓',
    rules: ['用 Wilder RSI，收盘后判断阈值。', '下限/上限是滞回区间，避免同一区间反复触发。', '强趋势里超买可以持续很久，反转策略会踏空。'],
    params: [
      { key: 'period', label: '周期', type: 'int', min: 2, max: 60, step: 1 },
      { key: 'lower', label: '下限', type: 'float', min: 5, max: 45, step: 1 },
      { key: 'upper', label: '上限', type: 'float', min: 55, max: 95, step: 1 },
    ],
    default: { period: 14, lower: 30, upper: 70 },
  },
  {
    id: 'momentum',
    name: '动量',
    desc: 'N 日涨幅超过阈值满仓，否则空仓',
    formula: 'Close / Close[lookback] - 1 > threshold → 满仓',
    rules: ['回看 N 日涨幅，超过阈值持有。', '阈值越低越容易满仓，交易更频繁。', '动量在单边市有效，震荡市容易高买低卖。'],
    params: [
      { key: 'lookback', label: '回看天数', type: 'int', min: 3, max: 120, step: 1 },
      { key: 'threshold', label: '阈值', type: 'float', min: 0, max: 0.5, step: 0.01 },
    ],
    default: { lookback: 20, threshold: 0.05 },
  },
  { id: 'buyhold', name: '买入持有', desc: '基线：首日满仓不动', formula: '第 2 根开盘买入并一直持有', rules: ['用于和策略对比的基线。'], params: [], default: {} },
]

/** 演示用的全局口径（与 backtest.Rules 的关键条目一致）。 */
const DEMO_QUANT_RULES = [
  '信号在收盘后计算，只允许次日开盘成交；引擎强制，策略看不到未来数据。',
  'T+1：买入当日不可卖出；涨跌停开盘不成交，停牌不交易。',
  '成本：佣金万三（最低 5 元）、印花税卖出万五、过户费双边 0.001%、滑点单边 0.1%。',
  '仓位只有满仓/空仓，按 100 股整手取整；优先前复权，不可用时会明确降级提示。',
  '年化按 252 日、夏普为日收益均值/标准差×√252、胜率按建仓→清仓往返统计。',
]

/** 演示用的成本参数，与面板成本行一致。 */
const DEMO_QUANT_COSTS = { commission: 0.0003, stampTax: 0.0005, transfer: 0.00001, slippage: 0.001, minCommission: 5 }

/** 生成演示优化结果：候选按夏普降序，best 取稳健性达标且夏普最高的点。 */
function quantOptimizeMock(p: Record<string, unknown>): unknown {
  const code = String(p.code ?? '600519')
  const strategy = String(p.strategy ?? 'ma')
  const r = rng(seedOf(code + strategy))
  const candidates = Array.from({ length: 6 }, (_, i) => {
    const fast = 3 + i * 2
    const slow = 10 + i * 10
    const sharpe = round2(1.6 - i * 0.18 + r() * 0.12)
    const stability = round2(0.55 + r() * 0.4 - i * 0.03)
    const params: Record<string, number> =
      strategy === 'ma'
        ? { fast, slow }
        : strategy === 'rsi'
          ? { period: 6 + i * 4, lower: 20 + i * 5, upper: 100 - (20 + i * 5) }
          : { lookback: 5 + i * 10, threshold: round2(0.02 + i * 0.02) }
    return {
      params,
      totalReturn: round2(0.18 + r() * 0.5 - i * 0.03),
      cagr: round2(0.08 + r() * 0.2),
      maxDrawdown: round2(-0.12 - r() * 0.2),
      sharpe,
      tradeCount: 8 + i * 3,
      stability,
    }
  })
  const best = candidates[0]
  const folds = [0, 1, 2].map((i) => ({
    index: i,
    trainStart: '2024-0' + (i + 1) + '-01',
    trainEnd: '2024-0' + (i + 3) + '-01',
    testStart: '2024-0' + (i + 3) + '-01',
    testEnd: '2024-0' + (i + 4) + '-01',
    best: best.params,
    trainSharpe: round2(best.sharpe + 0.2),
    trainReturn: round2(best.totalReturn / 3),
    testSharpe: round2(best.sharpe - 0.15),
    testReturn: round2(best.totalReturn / 4),
  }))
  const report = [
    '# 参数优化 · ' + strategy + '（' + code + '）',
    '',
    '- 推荐参数：' + Object.keys(best.params).map((k) => k + '=' + best.params[k]).join('，'),
    '- 样本内：夏普 ' + best.sharpe.toFixed(2) + '，稳健性 ' + (best.stability ?? 0).toFixed(2),
    '- walk-forward：样本外收益 ' + (best.totalReturn / 4 * 100).toFixed(2) + '%',
    '',
    '> 推荐参数只是值得继续验证的假设，不要用单次回测最优参数直接上线。',
  ].join('\n')
  return {
    result: {
      strategy,
      code,
      start: '2023-01-04',
      end: '2026-11-10',
      candidates,
      best,
      walkforward: { code, strategy, folds, isReturn: round2(best.totalReturn), oosReturn: round2(best.totalReturn / 4), overfitGap: round2(best.totalReturn * 0.75), verdict: '过拟合风险中等', note: '演示数据' },
      report,
    },
  }
}
