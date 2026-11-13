// 同花顺官方数据（fuyao）前端访问层。
//
// 职责：
//   1) 把 `invoke('fuyao', method, payload)` 的 21 个方法收敛成有类型的函数；
//   2) 把统一信封 {endpoint,title,count,items|series|data,asOf,permission,note}
//      归一到前端类型，并把「客户端专用（2004）」「上游暂不可用（5003）」变成可展示的语义；
//   3) 提供**官方口径专用**的数字格式化：成交量是「股」（不是手）、金额是「元」、
//      涨跌幅/占比是百分数原值、seal_rate 是 0~1 比例——与 lib/format.ts 的契约口径
//      （成交量=手）刻意分开，避免两套单位互相污染。
//
// 依据：docs/FUYAO-API.md §1–§5、§7（前端注意 4 条）。
// 约束：不使用 any；所有归一都是防御式读取（缺字段给安全的默认值）。

import { friendlyError, invoke } from '../api'
import { pickArr, pickNum, pickStr, rec, strList } from './parse'

/** 官方记录：字段名与官方 JSON 逐字一致（只在展示层做标签映射）。 */
export type FuyaoRecord = Record<string, unknown>

/** 权限标记：client 表示官方 code=2004（同花顺 AI 客户端专用，外部 Key 无权）。 */
export type FuyaoPermission = '' | 'client'

/** 统一信封归一后的形状（items/series/data 三者按端点取其一或其二）。 */
export interface FuyaoEnvelope {
  endpoint: string
  title: string
  group: string
  view: string
  count: number
  items: FuyaoRecord[]
  series: FuyaoSeries[]
  data: FuyaoRecord | null
  asOf: string
  dataTimestamp: string
  /** 集合竞价类端点的就绪状态：ready / not_ready（缺失为空串）。 */
  dataStatus: string
  permission: FuyaoPermission
  note: string
  truncated: boolean
}

// ---------- 强类型视图 ----------

/** 官方 A 股/指数快照一行。成交量单位股、成交额单位元、涨跌幅百分数原值。 */
export interface FuyaoQuoteRow {
  thscode: string
  ticker: string
  name: string
  lastPrice: number
  change: number
  changePct: number
  open: number
  high: number
  low: number
  prevClose: number
  /** 成交量，单位：股（官方口径，展示时另给「手」）。 */
  volumeShares: number
  /** 成交额，单位：元。 */
  turnoverYuan: number
  timestamp: number
}

/** 估值快照一行：PE/PB/PS/PCF 均为倍数（不是百分数）。 */
export interface FuyaoValuationRow {
  thscode: string
  ticker: string
  name: string
  peTtm: number | null
  peMrq: number | null
  pbMrq: number | null
  psTtm: number | null
  pcfTtm: number | null
}

/** 一根官方 K 线：volume 单位股、turnover 单位元。 */
export interface FuyaoBar {
  date: string
  dateMs: number
  open: number
  high: number
  low: number
  close: number
  volumeShares: number
  turnoverYuan: number
}

/** 一只标的的 K 线序列（批量端点外层按 thscode 分组）。 */
export interface FuyaoSeries {
  thscode: string
  interval: string
  adjust: string
  bars: FuyaoBar[]
}

/** 涨跌停/炸板池一行：封单额单位元，seal 相关为百分数/次数。 */
export interface FuyaoLimitRow {
  thscode: string
  ticker: string
  name: string
  lastPrice: number
  changePct: number
  limitTime: string
  firstTime: string
  lastTime: string
  openTimes: number | null
  reason: string
  boardsText: string
  boards: number
  sealMoneyYuan: number
  maxSealYuan: number
  turnoverPct: number
}

/** 涨停强度统计：sealRate 是 0~1 比例（不是百分数）。 */
export interface FuyaoLimitStats {
  tradeDate: string
  sealRate: number | null
  openStockCount: number | null
  limitUpStockCount: number | null
  touchedLimitUpStockCount: number | null
  pageBoards: number | null
  exactBoards: number | null
}

/** 热榜一行：heat 是上游热度字符串，不要当金额格式化。 */
export interface FuyaoHotRow {
  thscode: string
  ticker: string
  name: string
  rank: number
  heat: string
  rankChange: number | null
  rankTrend: string
  date: string
}

/** 集合竞价一行：auction_volume 股、auction_amount 元、三个 pct 为百分数原值。 */
export interface FuyaoAuctionRow {
  thscode: string
  ticker: string
  name: string
  auctionPrice: number
  auctionPct: number
  auctionVolumeShares: number
  auctionAmountYuan: number
  auctionUnmatched: number
  turnoverPct: number
  yesterdayRatioPct: number
  volumeRatio: number
  prevClose: number
  open: number
  lastPrice: number
  floatMarketCapYuan: number
}

/** 资金流单侧（元）。 */
export interface FuyaoFlowSide {
  inflowYuan: number
  outflowYuan: number
  netYuan: number
}

/** 资金流快照一行（元）。 */
export interface FuyaoCapitalFlow {
  thscode: string
  timestamp: number
  superLarge: FuyaoFlowSide
  large: FuyaoFlowSide
  medium: FuyaoFlowSide
  small: FuyaoFlowSide
  mainNetYuan: number
}

/** 标的检索结果。 */
export interface FuyaoTickerRef {
  thscode: string
  ticker: string
  name: string
  exchange: string
  assetType: string
}

/** 交易日历一天：date 为 yyyyMMdd，dateMs 为当日零点毫秒戳。 */
export interface FuyaoCalendarDay {
  date: string
  dateMs: number
}

/** 财务指标：value 保留上游原始字符串（null 表示缺失）。 */
export interface FuyaoIndicator {
  indexId: string
  value: string
}

/** 财务指标能力分组（五类指标）。 */
export interface FuyaoAbility {
  ability: string
  indicators: FuyaoIndicator[]
}

/** 端点目录一项（与后端 contract.FuyaoEndpoint 对齐）。 */
export interface FuyaoEndpoint {
  id: string
  path: string
  title: string
  group: string
  limit: number
  permission: string
  notes: string
  params: FuyaoParam[]
}

/** 端点参数目录。 */
export interface FuyaoParam {
  name: string
  type: string
  required: boolean
  default: string
  desc: string
}

/** 目录查询结果。 */
export interface FuyaoCatalog {
  total: number
  count: number
  groups: string[]
  endpoints: FuyaoEndpoint[]
}

// ---------- 格式化（官方口径专用） ----------

const DASH = '—'

/** 合法数字判断（NaN/Infinity/undefined 都返回占位符）。 */
function valid(v: number | null | undefined): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

/**
 * 成交量（官方口径「股」）→ 亿股 / 万股 / 股。
 * 不要用 lib/format.ts 的 fmtVolume：那个入参是「手」。
 */
export function fmtShares(v: number | null | undefined): string {
  if (!valid(v)) return DASH
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs >= 1e8) return sign + (abs / 1e8).toFixed(2) + '亿股'
  if (abs >= 1e4) return sign + (abs / 1e4).toFixed(1) + '万股'
  return sign + abs.toFixed(0) + '股'
}

/** 成交量（股 → 手，1 手 = 100 股）→ 万手 / 手。 */
export function fmtLots(shares: number | null | undefined): string {
  if (!valid(shares)) return DASH
  const lots = shares / 100
  const abs = Math.abs(lots)
  const sign = lots < 0 ? '-' : ''
  if (abs >= 1e4) return sign + (abs / 1e4).toFixed(2) + '万手'
  if (abs >= 1) return sign + abs.toFixed(0) + '手'
  return sign + abs.toFixed(2) + '手'
}

/** 金额（官方口径「元」）→ 万亿 / 亿 / 万 / 元（不带货币符号）。 */
export function fmtYuan(v: number | null | undefined, digits?: number): string {
  if (!valid(v)) return DASH
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs >= 1e12) return sign + (abs / 1e12).toFixed(digits ?? 2) + '万亿'
  if (abs >= 1e8) return sign + (abs / 1e8).toFixed(digits ?? (abs >= 1e10 ? 0 : 2)) + '亿'
  if (abs >= 1e4) return sign + (abs / 1e4).toFixed(digits ?? 1) + '万'
  return sign + abs.toFixed(digits ?? 0) + '元'
}

/** 带符号金额（元）→ +18.60亿 / -8600万 / 0。 */
export function fmtSignedYuan(v: number | null | undefined, digits?: number): string {
  if (!valid(v)) return DASH
  if (v === 0) return '0'
  return (v > 0 ? '+' : '-') + fmtYuan(Math.abs(v), digits)
}

/** 百分数原值（1.74 = +1.74%）→ 带符号百分数。 */
export function fmtPctValue(v: number | null | undefined, digits = 2): string {
  if (!valid(v)) return DASH
  const s = Math.abs(v).toFixed(digits)
  if (v > 0) return '+' + s + '%'
  if (v < 0) return '-' + s + '%'
  return s + '%'
}

/** 比例（0~1，例如 seal_rate）→ 百分数。与 fmtPctValue 刻意分开，避免口径混用。 */
export function fmtRatio(v: number | null | undefined, digits = 1): string {
  if (!valid(v)) return DASH
  return (v * 100).toFixed(digits) + '%'
}

/** 已经是百分数原值、但语义不是涨跌的指标（换手率/占比/权重）→ 不带符号的百分数。 */
export function fmtPctPlain(v: number | null | undefined, digits = 2): string {
  if (!valid(v)) return DASH
  return v.toFixed(digits) + '%'
}

/** 净值类（基金单位净值/累计净值）→ 3 位小数（官方精度比较高）。 */
export function fmtNav(v: number | null | undefined): string {
  if (!valid(v)) return DASH
  return v.toFixed(3)
}

/** 估值倍数 → 12.34 倍；负值（亏损导致 PE 为负）原样展示并标注。 */
export function fmtMultiple(v: number | null | undefined): string {
  if (!valid(v)) return DASH
  return v.toFixed(2) + '倍'
}

/** 价格：两位小数；缺失给占位符。 */
export function fmtPriceValue(v: number | null | undefined): string {
  if (!valid(v)) return DASH
  return v.toFixed(2)
}

/** 毫秒时间戳 → MM-DD HH:mm（Asia/Shanghai 由浏览器本地时区决定，演示与截图同为东八区）。 */
export function fmtStamp(ms: number | null | undefined): string {
  if (!valid(ms) || ms <= 0) return DASH
  const d = new Date(ms)
  if (Number.isNaN(d.getTime())) return DASH
  const p = (n: number) => String(n).padStart(2, '0')
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes())
}

/** yyyyMMdd → yyyy-MM-dd；其它形状原样返回。 */
export function fmtTradeDate(raw: string): string {
  const s = raw.trim()
  if (/^\d{8}$/.test(s)) return s.slice(0, 4) + '-' + s.slice(4, 6) + '-' + s.slice(6, 8)
  return s || DASH
}

// ---------- 错误语义 ----------

export interface FuyaoErrorView {
  kind: 'permission' | 'upstream' | 'param' | 'unsupported' | 'network' | 'other'
  title: string
  message: string
}

/**
 * 把后端/网络错误翻成前端可展示的三类语义：
 *   permission → 客户端专用（2004/2003）；upstream → 上游暂不可用（5001/5002/5003）；
 *   param → 参数问题（1001~1004）；network → 网络/超时。
 * 其余交给 friendlyError 兜底。
 */
export function describeFuyaoError(e: unknown): FuyaoErrorView {
  const raw = e instanceof Error ? e.message : typeof e === 'string' ? e : ''
  if (/2004|客户端专用|无权访问/.test(raw)) {
    return {
      kind: 'permission',
      title: '客户端专用数据',
      message: '该能力为同花顺 AI 客户端专用（官方 2004），外部 API Key 无法访问；请改用现有替代口径（资金流用新浪、分时用腾讯/新浪）。',
    }
  }
  if (/5003|上游数据源不可用|上游暂不可用|暂时取不到/.test(raw)) {
    return {
      kind: 'upstream',
      title: '上游暂不可用',
      message: '同花顺上游当前返回不可用（5003，非权限问题）。这是数据源状态，稍后重试即可，不需要改 Key。',
    }
  }
  if (/不存在或不适用|未收录|不支持此数据能力/.test(raw)) {
    return {
      kind: 'unsupported',
      title: '端点未收录或标的不适用',
      message: '数据层目录没有收录该端点（或该端点不适用于当前标的类型）。这不是 Key 问题；请核对 docs/FUYAO-API.md 与 internal/datasource/fuyao_catalog.go 是否同步。',
    }
  }
  if (/不支持参数|缺少必填参数|参数格式错误|参数越界|参数冲突|未知视图|未知 view/.test(raw)) {
    return { kind: 'param', title: '参数不合法', message: friendlyError(e, '读取官方数据') }
  }
  if (/timeout|超时|dial tcp|no such host|connection refused|网络不可达/i.test(raw)) {
    return { kind: 'network', title: '网络不可达', message: friendlyError(e, '读取官方数据') }
  }
  return { kind: 'other', title: '读取失败', message: friendlyError(e, '读取官方数据') }
}

/** 统一调用封装：invoke 的 JSON 已被 api.ts 解析，这里只包一层错误文案。 */
async function call<T>(method: string, payload: Record<string, unknown> = {}): Promise<T> {
  try {
    return await invoke<T>('fuyao', method, payload)
  } catch (e) {
    const view = describeFuyaoError(e)
    throw new Error(view.message)
  }
}

// ---------- 归一：信封 ----------

/** 把官方/工具信封归一到 FuyaoEnvelope（items 与 series/data 可并存）。 */
export function toEnvelope(v: unknown): FuyaoEnvelope {
  const r = rec(v) ?? {}
  const permission = pickStr(r, 'permission') === 'client' ? 'client' : ''
  return {
    endpoint: pickStr(r, 'endpoint', 'view', 'pool', 'kind') ?? '',
    title: pickStr(r, 'title') ?? '',
    group: pickStr(r, 'group') ?? '',
    view: pickStr(r, 'view') ?? '',
    count: pickNum(r, 'count') ?? 0,
    items: pickArr(r, 'items')
      .map((x) => rec(x))
      .filter((x): x is FuyaoRecord => x !== null),
    series: pickArr(r, 'series').map(toSeries).filter((s) => s.thscode !== '' || s.bars.length > 0),
    data: rec(r.data),
    asOf: pickStr(r, 'asOf') ?? '',
    dataTimestamp: pickStr(r, 'dataTimestamp') ?? '',
    dataStatus: pickStr(r, 'data_status') ?? '',
    permission,
    note: pickStr(r, 'note') ?? '',
    truncated: r.truncated === true,
  }
}

/** K 线序列归一：兼容 {thscode,bars} 与批量外层 {thscode,timestamp,item[]}。 */
export function toSeries(v: unknown): FuyaoSeries {
  const r = rec(v) ?? {}
  const rawBars = pickArr(r, 'bars').length > 0 ? pickArr(r, 'bars') : pickArr(r, 'item')
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    interval: pickStr(r, 'interval') ?? '',
    adjust: pickStr(r, 'adjust') ?? '',
    bars: rawBars.map(toBar).filter((b) => b.close > 0),
  }
}

/** 一根 K 线归一：官方 date_ms 毫秒戳优先，缺失时退回 date 字符串。 */
export function toBar(v: unknown): FuyaoBar {
  const r = rec(v) ?? {}
  const dateMs = pickNum(r, 'date_ms', 'timestamp') ?? 0
  return {
    date:
      pickStr(r, 'date') ??
      (dateMs > 0 ? new Date(dateMs).toISOString().slice(0, 10) : ''),
    dateMs,
    open: pickNum(r, 'open_price', 'open') ?? 0,
    high: pickNum(r, 'high_price', 'high') ?? 0,
    low: pickNum(r, 'low_price', 'low') ?? 0,
    close: pickNum(r, 'close_price', 'close') ?? 0,
    volumeShares: pickNum(r, 'volume') ?? 0,
    turnoverYuan: pickNum(r, 'turnover') ?? 0,
  }
}

/** 行情快照归一（volume=股、turnover=元）。 */
export function toQuoteRow(v: unknown): FuyaoQuoteRow {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    ticker: pickStr(r, 'ticker') ?? '',
    name: pickStr(r, 'name') ?? '',
    lastPrice: pickNum(r, 'last_price') ?? 0,
    change: pickNum(r, 'price_change') ?? 0,
    changePct: pickNum(r, 'price_change_ratio_pct') ?? 0,
    open: pickNum(r, 'open_price') ?? 0,
    high: pickNum(r, 'high_price') ?? 0,
    low: pickNum(r, 'low_price') ?? 0,
    prevClose: pickNum(r, 'prev_price') ?? 0,
    volumeShares: pickNum(r, 'volume') ?? 0,
    turnoverYuan: pickNum(r, 'turnover') ?? 0,
    timestamp: pickNum(r, 'timestamp') ?? 0,
  }
}

/** 估值归一：缺失值保留 null（面板渲染「—」而不是 0 倍）。 */
export function toValuationRow(v: unknown): FuyaoValuationRow {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    ticker: pickStr(r, 'ticker') ?? '',
    name: pickStr(r, 'name') ?? '',
    peTtm: pickNum(r, 'pe_ttm') ?? null,
    peMrq: pickNum(r, 'pe_mrq') ?? null,
    pbMrq: pickNum(r, 'pb_mrq') ?? null,
    psTtm: pickNum(r, 'ps_ttm') ?? null,
    pcfTtm: pickNum(r, 'pcf_ttm') ?? null,
  }
}

/** 涨跌停池归一：open_times 允许 null（官方就是这么给的）。 */
export function toLimitRow(v: unknown): FuyaoLimitRow {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    ticker: pickStr(r, 'ticker') ?? '',
    name: pickStr(r, 'name') ?? '',
    lastPrice: pickNum(r, 'last_price') ?? 0,
    changePct: pickNum(r, 'price_change_ratio_pct') ?? 0,
    limitTime: pickStr(r, 'limit_up_time') ?? '',
    firstTime: pickStr(r, 'first_limit_time', 'first_seal_time') ?? '',
    lastTime: pickStr(r, 'last_limit_time', 'last_seal_time') ?? '',
    openTimes: pickNum(r, 'open_times') ?? null,
    reason: pickStr(r, 'limit_up_reason') ?? '',
    boardsText: pickStr(r, 'continue_day_text') ?? '',
    boards: pickNum(r, 'continue_day_cnt') ?? 0,
    sealMoneyYuan: pickNum(r, 'seal_money') ?? 0,
    maxSealYuan: pickNum(r, 'max_seal_money') ?? 0,
    turnoverPct: pickNum(r, 'turnover_ratio_pct') ?? 0,
  }
}

/** 涨停统计归一（seal_rate 为 0~1 比例）。 */
export function toLimitStats(v: unknown): FuyaoLimitStats {
  const r = rec(v) ?? {}
  return {
    tradeDate: pickStr(r, 'trade_date') ?? '',
    sealRate: pickNum(r, 'seal_rate') ?? null,
    openStockCount: pickNum(r, 'open_stock_count') ?? null,
    limitUpStockCount: pickNum(r, 'limit_up_stock_count') ?? null,
    touchedLimitUpStockCount: pickNum(r, 'touched_limit_up_stock_count') ?? null,
    pageBoards: pickNum(r, 'page_boards') ?? null,
    exactBoards: pickNum(r, 'exact_boards') ?? null,
  }
}

/** 热榜归一：heat 保留字符串（不是金额）。 */
export function toHotRow(v: unknown): FuyaoHotRow {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    ticker: pickStr(r, 'ticker') ?? '',
    name: pickStr(r, 'name') ?? '',
    rank: pickNum(r, 'rank') ?? 0,
    heat: pickStr(r, 'heat') ?? '',
    rankChange: pickNum(r, 'rank_change') ?? null,
    rankTrend: pickStr(r, 'rank_trend') ?? '',
    date: pickStr(r, 'date') ?? '',
  }
}

/** 竞价归一：三个 pct 都是百分数原值，auction_volume 是股。 */
export function toAuctionRow(v: unknown): FuyaoAuctionRow {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    ticker: pickStr(r, 'ticker') ?? '',
    name: pickStr(r, 'name') ?? '',
    auctionPrice: pickNum(r, 'auction_price') ?? 0,
    auctionPct: pickNum(r, 'auction_pct') ?? 0,
    auctionVolumeShares: pickNum(r, 'auction_volume') ?? 0,
    auctionAmountYuan: pickNum(r, 'auction_amount') ?? 0,
    auctionUnmatched: pickNum(r, 'auction_unmatched') ?? 0,
    turnoverPct: pickNum(r, 'auction_turnover_pct') ?? 0,
    yesterdayRatioPct: pickNum(r, 'auction_yesterday_ratio_pct') ?? 0,
    volumeRatio: pickNum(r, 'auction_volume_ratio') ?? 0,
    prevClose: pickNum(r, 'pre_close_price') ?? 0,
    open: pickNum(r, 'open_price') ?? 0,
    lastPrice: pickNum(r, 'last_price') ?? 0,
    floatMarketCapYuan: pickNum(r, 'float_market_cap') ?? 0,
  }
}

/** 资金流单侧归一（元）。 */
function toFlowSide(v: unknown): FuyaoFlowSide {
  const r = rec(v) ?? {}
  const inflow = pickNum(r, 'inflow_amount') ?? 0
  const outflow = pickNum(r, 'outflow_amount') ?? 0
  return { inflowYuan: inflow, outflowYuan: outflow, netYuan: pickNum(r, 'net_amount') ?? inflow - outflow }
}

/** 资金流快照归一（元；main_net 由数据层给出）。 */
export function toCapitalFlow(v: unknown): FuyaoCapitalFlow {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    timestamp: pickNum(r, 'timestamp') ?? 0,
    superLarge: toFlowSide(r.super_large),
    large: toFlowSide(r.large),
    medium: toFlowSide(r.medium),
    small: toFlowSide(r.small),
    mainNetYuan: pickNum(r, 'main_net') ?? 0,
  }
}

/** 标的检索归一。 */
export function toTickerRef(v: unknown): FuyaoTickerRef {
  const r = rec(v) ?? {}
  return {
    thscode: pickStr(r, 'thscode') ?? '',
    ticker: pickStr(r, 'ticker') ?? '',
    name: pickStr(r, 'name') ?? '',
    exchange: pickStr(r, 'exchange') ?? '',
    assetType: pickStr(r, 'asset_type') ?? '',
  }
}

/** 交易日历归一：date 保持 yyyyMMdd（展示时再格式化）。 */
export function toCalendarDay(v: unknown): FuyaoCalendarDay {
  const r = rec(v) ?? {}
  return { date: pickStr(r, 'date') ?? '', dateMs: pickNum(r, 'date_ms') ?? 0 }
}

/** 端点目录归一（含参数与权限说明）。 */
export function toEndpoint(v: unknown): FuyaoEndpoint {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id') ?? '',
    path: pickStr(r, 'path') ?? '',
    title: pickStr(r, 'title') ?? '',
    group: pickStr(r, 'group') ?? '',
    limit: pickNum(r, 'limit') ?? 0,
    permission: pickStr(r, 'permission') ?? '',
    notes: pickStr(r, 'notes') ?? '',
    params: pickArr(r, 'params')
      .map((x) => {
        const p = rec(x) ?? {}
        return {
          name: pickStr(p, 'name') ?? '',
          type: pickStr(p, 'type') ?? '',
          required: p.required === true,
          default: pickStr(p, 'default') ?? '',
          desc: pickStr(p, 'desc') ?? '',
        }
      })
      .filter((p) => p.name !== ''),
  }
}

/** 财务指标能力归一：abilities[].indicators[]:{index_id,value}。 */
export function toAbilities(v: unknown): FuyaoAbility[] {
  const data = rec(v) ?? {}
  return pickArr(data, 'abilities')
    .map((x) => {
      const a = rec(x) ?? {}
      return {
        ability: pickStr(a, 'ability') ?? '',
        indicators: pickArr(a, 'indicators')
          .map((y) => {
            const ind = rec(y) ?? {}
            return {
              indexId: pickStr(ind, 'index_id', 'index_name') ?? '',
              value: pickStr(ind, 'value') ?? '',
            }
          })
          .filter((i) => i.indexId !== ''),
      }
    })
    .filter((a) => a.indicators.length > 0)
}

// ---------- 方法封装（21 个） ----------

/** 端点目录（离线常量，不发网络请求；同一次会话只取一次）。 */
export async function fuyaoCatalog(payload: { group?: string; endpoint?: string } = {}): Promise<FuyaoCatalog> {
  const raw = await call<unknown>('catalog', payload)
  const r = rec(raw) ?? {}
  return {
    total: pickNum(r, 'total') ?? 0,
    count: pickNum(r, 'count') ?? 0,
    groups: strList(pickArr(r, 'groups')),
    endpoints: pickArr(r, 'endpoints').map(toEndpoint).filter((e) => e.id !== ''),
  }
}

/** 通用查询：endpoint 用目录 ID，params 键名必须与目录一致（大小写敏感）。 */
export async function fuyaoQuery(endpoint: string, params: Record<string, string> = {}, limit = 30): Promise<FuyaoEnvelope> {
  return toEnvelope(await call<unknown>('query', { endpoint, params, limit }))
}

/** 批量行情快照（一次 ≤50，后端自动分批；volume=股、turnover=元）。 */
export async function fuyaoQuote(codes: string[]): Promise<FuyaoEnvelope & { rows: FuyaoQuoteRow[] }> {
  if (codes.length === 0) return { ...toEnvelope(null), rows: [] }
  const env = toEnvelope(await call<unknown>('quote', { codes: codes.slice(0, 50) }))
  return { ...env, rows: env.items.map(toQuoteRow) }
}

/** 批量估值（PE/PB/PS/PCF 为倍数）。 */
export async function fuyaoValuation(codes: string[]): Promise<FuyaoEnvelope & { rows: FuyaoValuationRow[] }> {
  if (codes.length === 0) return { ...toEnvelope(null), rows: [] }
  const env = toEnvelope(await call<unknown>('valuation', { codes: codes.slice(0, 100) }))
  return { ...env, rows: env.items.map(toValuationRow) }
}

/** 批量历史 K 线（股票/指数/基金；10/批，超限由后端切分）。 */
export async function fuyaoKline(input: {
  codes: string[]
  interval?: string
  adjust?: string
  asset?: string
  start?: string
  end?: string
  limit?: number
}): Promise<{ series: FuyaoSeries[]; note: string; asOf: string }> {
  const payload: Record<string, unknown> = { codes: input.codes.slice(0, 50) }
  if (input.interval) payload.interval = input.interval
  if (input.adjust) payload.adjust = input.adjust
  if (input.asset) payload.asset = input.asset
  if (input.start) payload.start = input.start
  if (input.end) payload.end = input.end
  if (input.limit) payload.limit = input.limit
  const env = toEnvelope(await call<unknown>('kline', payload))
  return { series: env.series, note: env.note, asOf: env.asOf }
}

/** 涨跌停/炸板池（pool=up/down/break）。 */
export async function fuyaoLimitPool(pool: 'up' | 'down' | 'break', limit = 50): Promise<FuyaoEnvelope & { rows: FuyaoLimitRow[] }> {
  const env = toEnvelope(await call<unknown>('limitPool', { pool, limit }))
  return { ...env, rows: env.items.map(toLimitRow) }
}

/** 涨停强度统计（seal_rate 为 0~1 比例）。 */
export async function fuyaoLimitStats(date = ''): Promise<{ stats: FuyaoLimitStats; asOf: string }> {
  const raw = await call<unknown>('limitStats', date ? { date } : {})
  const r = rec(raw) ?? {}
  return { stats: toLimitStats(r.data), asOf: pickStr(r, 'asOf') ?? '' }
}

/** 热榜（kind=hot/skyrocket/history；period=day/hour）。 */
export async function fuyaoHotRank(kind: 'hot' | 'skyrocket' | 'history', period: 'day' | 'hour' = 'day', limit = 30, date = ''): Promise<FuyaoEnvelope & { rows: FuyaoHotRow[] }> {
  const payload: Record<string, unknown> = { kind, period, limit }
  if (date) payload.date = date
  const env = toEnvelope(await call<unknown>('hotRank', payload))
  return { ...env, rows: env.items.map(toHotRow) }
}

/** 特色数据（view 白名单由后端决定）。 */
export async function fuyaoSpecial(input: {
  view: string
  codes?: string[]
  code?: string
  date?: string
  shape?: string
  departmentId?: string
  limit?: number
}): Promise<FuyaoEnvelope> {
  const payload: Record<string, unknown> = { view: input.view }
  if (input.codes && input.codes.length > 0) payload.codes = input.codes
  if (input.code) payload.code = input.code
  if (input.date) payload.date = input.date
  if (input.shape) payload.shape = input.shape
  if (input.departmentId) payload.departmentId = input.departmentId
  if (input.limit) payload.limit = input.limit
  return toEnvelope(await call<unknown>('special', payload))
}

/** 集合竞价（view=snapshot/benchmark/opportunities）。 */
export async function fuyaoAuction(input: {
  view: 'snapshot' | 'benchmark' | 'opportunities'
  codes?: string[]
  stage?: string
  date?: string
}): Promise<FuyaoEnvelope & { rows: FuyaoAuctionRow[] }> {
  const payload: Record<string, unknown> = { view: input.view }
  if (input.codes && input.codes.length > 0) payload.codes = input.codes
  if (input.stage) payload.stage = input.stage
  if (input.date) payload.date = input.date
  const env = toEnvelope(await call<unknown>('auction', payload))
  return { ...env, rows: env.items.map(toAuctionRow) }
}

/** 资金流（view=snapshot/history/highfreq/intraday；2004 会降级成说明）。 */
export async function fuyaoCapitalFlow(input: {
  view: 'snapshot' | 'history' | 'highfreq' | 'intraday'
  code: string
  interval?: string
  date?: string
  start?: string
  end?: string
}): Promise<FuyaoEnvelope & { flow: FuyaoCapitalFlow | null }> {
  const payload: Record<string, unknown> = { view: input.view, code: input.code }
  if (input.interval) payload.interval = input.interval
  if (input.date) payload.date = input.date
  if (input.start) payload.start = input.start
  if (input.end) payload.end = input.end
  const raw = await call<unknown>('capitalFlow', payload)
  const env = toEnvelope(raw)
  const r = rec(raw) ?? {}
  const item = rec(r.item)
  return { ...env, flow: item ? toCapitalFlow(item) : null }
}

/** 财务报表 / 财务指标（kind=income/balance/cash/indicators）。 */
export async function fuyaoFinancials(input: {
  kind: 'income' | 'balance' | 'cash' | 'indicators'
  code?: string
  period?: string
  report?: string
  limit?: number
}): Promise<FuyaoEnvelope> {
  const payload: Record<string, unknown> = { kind: input.kind }
  if (input.code) payload.code = input.code
  if (input.period) payload.period = input.period
  if (input.report) payload.report = input.report
  if (input.limit) payload.limit = input.limit
  return toEnvelope(await call<unknown>('financials', payload))
}

/** 标的联想（代码/名称子串；≤50）。 */
export async function fuyaoTickerSearch(keyword: string, assetType = '', limit = 12): Promise<FuyaoTickerRef[]> {
  const q = keyword.trim()
  if (q === '') return []
  const env = toEnvelope(await call<unknown>('tickerSearch', { keyword: q, assetType, limit }))
  return env.items.map(toTickerRef)
}

/** 交易日历（date 为 yyyyMMdd）。 */
export async function fuyaoCalendar(start = '', end = ''): Promise<FuyaoCalendarDay[]> {
  const payload: Record<string, unknown> = {}
  if (start) payload.start = start
  if (end) payload.end = end
  const env = toEnvelope(await call<unknown>('calendar', payload))
  return env.items.map(toCalendarDay)
}

/** 指数目录/成分股/反查/快照/K 线。 */
export async function fuyaoIndex(input: {
  view: 'catalog' | 'constituents' | 'memberships' | 'snapshot' | 'kline'
  tag?: string
  indexCode?: string
  codes?: string[]
  limit?: number
}): Promise<FuyaoEnvelope> {
  const payload: Record<string, unknown> = { view: input.view }
  if (input.tag) payload.tag = input.tag
  if (input.indexCode) payload.indexCode = input.indexCode
  if (input.codes && input.codes.length > 0) payload.codes = input.codes
  if (input.limit) payload.limit = input.limit
  return toEnvelope(await call<unknown>('index', payload))
}

/** 基金视图（view 白名单见数据层；2004 视图会返回 permission=client）。 */
export async function fuyaoFund(input: {
  view: string
  thscode?: string
  managerId?: string
  companyId?: string
  params?: Record<string, string>
  limit?: number
}): Promise<FuyaoEnvelope> {
  const payload: Record<string, unknown> = { view: input.view }
  if (input.thscode) payload.thscode = input.thscode
  if (input.managerId) payload.managerId = input.managerId
  if (input.companyId) payload.companyId = input.companyId
  if (input.params && Object.keys(input.params).length > 0) payload.params = input.params
  if (input.limit) payload.limit = input.limit
  return toEnvelope(await call<unknown>('fund', payload))
}

/** 期货视图（view 白名单见数据层；日 K 上游 5003）。 */
export async function fuyaoFutures(view: string, params: Record<string, string> = {}, limit = 30): Promise<FuyaoEnvelope> {
  return toEnvelope(await call<unknown>('futures', { view, params, limit }))
}

/** 期权视图（日 K 上游 5003）。 */
export async function fuyaoOptions(view: string, params: Record<string, string> = {}, limit = 30): Promise<FuyaoEnvelope> {
  return toEnvelope(await call<unknown>('options', { view, params, limit }))
}

/** 资讯视图（flash/notice/topic/stock-timeline/anomaly/events）。 */
export async function fuyaoNews(input: {
  view: string
  tagId?: number
  seq?: string
  code?: string
  date?: string
  params?: Record<string, string>
  limit?: number
}): Promise<FuyaoEnvelope> {
  const payload: Record<string, unknown> = { view: input.view }
  if (typeof input.tagId === 'number') payload.tagId = input.tagId
  if (input.seq) payload.seq = input.seq
  if (input.code) payload.code = input.code
  if (input.date) payload.date = input.date
  if (input.params && Object.keys(input.params).length > 0) payload.params = input.params
  if (input.limit) payload.limit = input.limit
  return toEnvelope(await call<unknown>('news', payload))
}

/** 打新（view=a-ipo/hk-application/hk-calendar/hk-detail）。 */
export async function fuyaoIpo(input: {
  view: string
  market?: string
  status?: string
  issueCode?: string
  limit?: number
}): Promise<FuyaoEnvelope> {
  const payload: Record<string, unknown> = { view: input.view }
  if (input.market) payload.market = input.market
  if (input.status) payload.status = input.status
  if (input.issueCode) payload.issueCode = input.issueCode
  if (input.limit) payload.limit = input.limit
  return toEnvelope(await call<unknown>('ipo', payload))
}

/** 全市场 Parquet 下载链接（presigned_url 有时效，只展示不代下）。 */
export async function fuyaoMarketDump(kind: '10y' | '10d' | 'adjust'): Promise<{ data: FuyaoRecord | null; note: string; asOf: string }> {
  const raw = await call<unknown>('marketDump', { kind })
  const env = toEnvelope(raw)
  return { data: env.data, note: env.note, asOf: env.asOf }
}

// ---------- 通用表格列推断 ----------

/** 单元格格式（按官方字段口径选择，不跨口径复用）。 */
export type FuyaoColumnFormat =
  | 'text'
  | 'price'
  | 'pct'
  | 'pctPlain'
  | 'ratio'
  | 'yuan'
  | 'shares'
  | 'lots'
  | 'multiple'
  | 'nav'
  | 'number'
  | 'stamp'
  | 'date'
  | 'wide'

/** 一列：key 为官方字段名，label 为中文表头。 */
export interface FuyaoColumn {
  key: string
  label: string
  format: FuyaoColumnFormat
}

/** 官方字段中文标签（catalog/query 长尾表格共用；未知字段回落成原名）。 */
export const FIELD_LABELS: Record<string, string> = {
  thscode: '代码',
  ticker: '代码',
  name: '名称',
  stock_name: '名称',
  fund_name: '基金名称',
  last_price: '现价',
  price_change: '涨跌额',
  price_change_ratio_pct: '涨跌幅',
  open_price: '开盘',
  high_price: '最高',
  low_price: '最低',
  prev_price: '昨收',
  pre_close_price: '昨收',
  volume: '成交量(股)',
  turnover: '成交额(元)',
  turnover_ratio_pct: '换手率',
  amount: '金额(元)',
  rank: '排名',
  heat: '热度',
  rank_change: '排名变化',
  rank_trend: '排名走势',
  date: '日期',
  date_ms: '日期',
  timestamp: '时间',
  trade_date: '交易日',
  is_st: 'ST',
  is_new: '新股',
  limit_up_time: '涨停时间',
  first_limit_time: '首次跌停',
  last_limit_time: '最后跌停',
  first_seal_time: '首次封板',
  last_seal_time: '最后封板',
  open_times: '开板次数',
  limit_up_reason: '涨停原因',
  continue_day_text: '连板说明',
  continue_day_cnt: '连板数',
  seal_money: '封单额(元)',
  max_seal_money: '峰值封单(元)',
  seal_rate: '封板率(0~1)',
  open_stock_count: '开板家数',
  limit_up_stock_count: '涨停家数',
  touched_limit_up_stock_count: '触板家数',
  page_boards: '页面上榜数',
  exact_boards: '精确榜单数',
  auction_phase: '竞价阶段',
  data_status: '数据状态',
  total: '总数',
  auction_price: '竞价',
  auction_pct: '竞价涨幅',
  auction_volume: '竞价量(股)',
  auction_amount: '竞价额(元)',
  auction_unmatched: '未匹配量',
  auction_turnover_pct: '竞价换手',
  auction_yesterday_ratio_pct: '昨量比',
  auction_volume_ratio: '量比',
  float_market_cap: '流通市值(元)',
  period: '报告期',
  fiscal_year: '财年',
  fiscal_period: '财季',
  report_date_ms: '报告日',
  operating_income: '营业收入(元)',
  operating_costs: '营业成本(元)',
  operating_expenses: '营业费用(元)',
  sales_fee: '销售费用(元)',
  manage_fee: '管理费用(元)',
  research_and_development_expenses: '研发费用(元)',
  operating_profit: '营业利润(元)',
  interest_expenses: '利息费用(元)',
  profit_total: '利润总额(元)',
  income_tax_expense: '所得税(元)',
  net_profit: '净利润(元)',
  parent_holder_net_profit: '归母净利润(元)',
  assets_total: '总资产(元)',
  total_current_assets: '流动资产(元)',
  non_current_nets_total: '非流动资产(元)',
  cash: '现金(元)',
  accounts_receivable: '应收账款(元)',
  total_debt: '总负债(元)',
  holder_equity_total: '所有者权益(元)',
  act_cash_flow_net: '经营现金流净额(元)',
  invest_cash_flow_net: '投资现金流净额(元)',
  financing_cash_flow_net: '筹资现金流净额(元)',
  pay_fixed_assets_etc_cash: '购建资产支出(元)',
  pay_dividends_profits_interest_cash: '分红付息支出(元)',
  cash_equivalents_net_addition: '现金净增加额(元)',
  index_id: '指标',
  value: '数值',
  ability: '能力',
  tag: '标签',
  exchange: '交易所',
  asset_type: '资产类型',
  title: '标题',
  summary: '摘要',
  content: '正文',
  source: '来源',
  publish_time: '发布时间',
  url: '链接',
  seq: '序号',
  id: 'ID',
  variety_code: '品种代码',
  variety_name: '品种',
  exchange_code: '交易所',
  contract_multiplier: '合约乘数',
  price_unit: '报价单位',
  tick_size: '最小变动',
  margin_rate: '保证金率',
  list_date: '上市日',
  last_trade_date: '最后交易日',
  last_delivery_date: '最后交割日',
  report_type: '报告类型',
  end_date: '截止日',
  start_date: '起始日',
  hold_ratio: '持仓占比',
  position_capital: '持仓市值(元)',
  position_count: '持股数(股)',
  nav_date: '净值日期',
  unit_nav: '单位净值(元)',
  adj_nav: '累计净值(元)',
  return_week: '近1周',
  return_month: '近1月',
  return_tyear: '近3年',
  return_year: '近1年',
  manager_name: '基金经理',
  company_name: '基金公司',
  fund_type: '基金类型',
  fund_scale: '规模(元)',
  estab_date: '成立日',
  ratio_pct: '占比',
  industry_name: '行业',
  net_value: '净额(元)',
  net_amount: '净额(元)',
  main_net: '主力净额(元)',
  buy_value: '买入额(元)',
  sell_value: '卖出额(元)',
  org_net_value: '机构净额(元)',
  hot_money_net_value: '游资净额(元)',
  limit_reason: '上榜原因',
  concept_list: '概念',
  board_type: '榜单类型',
  stock_count: '个股数',
  subscription_start_ms: '申购开始',
  subscription_end_ms: '申购结束',
  issue_price: '发行价',
  listing_date: '上市日',
  presigned_url: '下载链接',
  long_position: '多单(手)',
  short_position: '空单(手)',
  net_position: '净持仓(手)',
  long_position_change: '多单变化',
  short_position_change: '空单变化',
  net_position_change: '净持仓变化',
  volume_change: '成交量变化',
  company: '公司',
  rank_value: '名次',
  limit_up_count: '涨停家数',
  limit_down_count: '跌停家数',
  auction_up_count: '竞价上涨家数',
  auction_down_count: '竞价下跌家数',
  auction_flat_count: '竞价平盘家数',
  avg_auction_pct: '平均竞价涨幅',
  yesterday_limit_up_premium_pct: '昨涨停溢价',
  stage: '阶段',
  warn_type: '提示类型',
  result_pct: '当日结果',
  mode: '模式',
  tag_name: '异动标签',
  description: '说明',
  monitor_type: '监控类型',
  monitor_start: '监控开始',
  monitor_end: '监控结束',
  concentration: '集中度(0~1)',
  profit_ratio_pct: '获利比例',
  avg_cost: '平均成本(元)',
  next_day_pct: '次日涨幅',
  weight: '权重(%)',
  predicted_turnover: '预测成交额(元)',
  current_turnover: '当前成交额(元)',
  confidence: '置信度',
  institution_name: '机构/席位',
  apply_status: '认购状态',
  apply_amount: '认购金额(元)',
  sponsor: '保荐人',
  lower: '区间下限',
  upper: '区间上限',
  lot_size: '每手股数',
  entry_fee: '入场费(元)',
  issue_price_range: '发行价区间',
  currency: '币种',
  is_kuaixun: '快讯',
  report_period: '报告期',
  notice_type: '公告类型',
  has_findings: '有异常发现',
  findings: '异常发现',
  report_type_name: '报告类型',
  quote_code: '行情代码',
  trade_sessions: '交易时段',
  open_interest: '持仓量(手)',
  price: '价格',
  basis: '基差',
  basis_rate_pct: '基差率',
  converted_spot_price: '折算现货价',
  spot_price: '现货价',
  spot_publish_date: '现货日期',
  reference_site: '现货参考地',
  trade_stages: '交易阶段',
  exchange_sessions: '交易所时段',
  timezone: '时区',
  short_name: '简称',
  return_tmonth: '近3月',
  return_hyear: '近半年',
  return_twoyear: '近2年',
  return_fyear: '近5年',
  return_nowyear: '今年以来',
  return_now: '成立以来',
  peer_average_week: '同类近1周',
  peer_average_month: '同类近1月',
  period_increase_rate_pct: '较上期变化',
  investment_rank: '持仓排名',
  stock_ratio_pct: '股票占比',
  bond_ratio_pct: '债券占比',
  deposit_ratio_pct: '现金占比',
  other_ratio_pct: '其它占比',
  manager_return_pct: '基金经理收益',
  peer_return_pct: '同类平均',
  benchmark_return_pct: '基准收益',
  price_amplitude_ratio_pct: '振幅',
  yesterday_ratio_pct: '昨日量比',
  annual_return_pct: '年化收益',
  maximum_return_pct: '最大收益',
  net_rate: '净买占比',
  change: '涨跌幅',
  status: '状态',
  market: '市场',
  note: '备注',
  statistics_date: '统计日期',
  minute_visit_count: '分时访问量',
  kline_visit_count: 'K 线访问量',
  display_code: '显示代码',
  merge_scope: '合并口径',
  ins_position: '机构持仓(元)',
  holder_amount: '持有人户数',
  avg_holder_share: '户均份额',
  psnl_rate: '个人持有比例',
  mgmt_staff_hold_rate: '员工持有比例',
  dividend_count: '分红次数',
  dividend_total: '分红总额(元)',
  share_nav: '单位净值(元)',
  asset_nav: '累计净值(元)',
  current_profit: '本期利润(元)',
  distribution_profit: '本期已分配利润(元)',
  publish_date_ms: '披露日',
  position_item: '持仓明细',
  average_item: '均价明细',
  warehouse_receipt: '仓单',
  week: '近1周回撤',
  month: '近1月回撤',
  tmonth: '近3月回撤',
  hyear: '近半年回撤',
  year: '近1年回撤',
  twoyear: '近2年回撤',
  tyear: '近3年回撤',
  fyear: '近5年回撤',
  nowyear: '今年以来回撤',
  now: '成立以来回撤',
  start_date_ms: '开始日期',
  end_date_ms: '结束日期',
  tab_id: '专题 ID',
}

/** 字段名 → 中文标签（未知字段保留原名，便于对文档）。 */
export function labelOf(key: string): string {
  return FIELD_LABELS[key] ?? key
}

/** 枚举值 → 中文（官方枚举只翻展示层，不改原始数据）。 */
const VALUE_LABELS: Record<string, Record<string, string>> = {
  status: { subscribable: '可申购', upcoming: '待申购', awaiting_listing: '待上市', open: '进行中', closed: '已结束' },
  market: { sh_sz: '沪深', bj: '京市' },
  mode: { strong: '强势', weak: '弱势', all: '全部' },
  rank_trend: { up: '上升', down: '下降', flat: '持平' },
  auction_phase: { pre_open: '集合竞价', continuous: '连续竞价', closed: '已收盘' },
  data_status: { ready: '就绪', not_ready: '未就绪' },
  board_type: { all: '全部', org: '机构', hot_money: '游资' },
  state: { current: '当前', history: '历史' },
  asset_type: {
    'a-share': 'A 股',
    'a-share-index': '指数',
    'fund-otc': '场外基金',
    'fund-etf': 'ETF',
    'fund-lof': 'LOF',
    'fund-reits': 'REITs',
    forex: '外汇',
    futures: '期货',
    options: '期权',
  },
  apply_status: { subscribing: '认购中', upcoming: '待申购', listed: '已上市' },
}

/** 枚举值翻译：命中字典返回中文，否则原样返回。 */
export function valueLabelOf(key: string, raw: string): string {
  return VALUE_LABELS[key]?.[raw] ?? raw
}

/** 按字段名推断格式：口径判断集中在这里，避免各视图各写一套。 */
export function formatOf(key: string): FuyaoColumnFormat {
  const k = key.toLowerCase()
  // 涨跌类：需要带符号
  if (k === 'price_change_ratio_pct' || k === 'change' || k === 'net_rate' || k.endsWith('_change_pct')) return 'pct'
  // 比例类：0~1 原值
  if (k === 'seal_rate' || k === 'margin_rate' || k === 'concentration' || k === 'volume_ratio') return 'ratio'
  // 百分数但不是涨跌：不带符号（收益率区间/占比/权重）
  if (k === 'weight' || k.includes('ratio_pct') || k.includes('turnover_pct') || k.includes('_rate_pct')) return 'pctPlain'
  if (k.startsWith('return_') || k.startsWith('peer_average') || k === 'hold_ratio') return 'pctPlain'
  if (k.endsWith('_pct')) return 'pct'
  if (k === 'volume' || k.endsWith('_volume') || k === 'position_count') return 'shares'
  if (k === 'unit_nav' || k === 'adj_nav' || k === 'nav') return 'nav'
  if (k === 'holder_amount') return 'number'
  if (k === 'psnl_rate' || k === 'mgmt_staff_hold_rate') return 'pctPlain'
  if (/^(week|month|tmonth|hyear|year|twoyear|tyear|fyear|nowyear|now)$/.test(k)) return 'pct'
  if (k === 'publish_time' || k === 'create_time' || k === 'update_time') return 'stamp'
  if (/^(issue_price|avg_cost|spot_price|converted_spot_price|basis)$/.test(k)) return 'price'
  if (
    k === 'turnover' ||
    k.endsWith('turnover') ||
    k.endsWith('_amount') ||
    k.endsWith('_value') ||
    k.endsWith('_money') ||
    k.endsWith('_net') ||
    k.endsWith('_income') ||
    k.endsWith('_costs') ||
    k.endsWith('_profit') ||
    k.endsWith('_expense') ||
    k.endsWith('_fee') ||
    k === 'cash' ||
    k.endsWith('_assets') ||
    k.endsWith('_debt') ||
    k.endsWith('_equity') ||
    k.endsWith('_cash') ||
    k.endsWith('_addition') ||
    k.endsWith('_total') ||
    k.endsWith('receivable') ||
    k === 'main_net' ||
    k === 'net_value' ||
    k === 'ins_position' ||
    k === 'position_capital' ||
    k === 'fund_scale' ||
    k === 'entry_fee'
  ) {
    return 'yuan'
  }
  if (k === 'pe_ttm' || k === 'pe_mrq' || k === 'pb_mrq' || k === 'ps_ttm' || k === 'pcf_ttm') return 'multiple'
  if (k.endsWith('_ms') || k === 'timestamp' || k === 'create_time' || k === 'update_time') return 'stamp'
  if (k.endsWith('_date') || k === 'date') return 'date'
  if (k === 'last_price' || k === 'price' || k === 'price_change' || k === 'auction_price' || k === 'open_price' || k === 'high_price' || k === 'low_price' || k === 'prev_price' || k === 'pre_close_price') return 'price'
  if (/^(rank|rank_change|size|page|limit|offset)$/.test(k) || /(_count|_cnt|_times|_size|_boards)$/.test(k)) return 'number'
  if (k === 'content' || k === 'summary' || k === 'title' || k === 'resume' || k === 'notes' || k === 'description' || k === 'concept_list') return 'wide'
  return 'text'
}

/** 从记录集合推断表格列：优先用给定 keys，否则取前 max 个非空字段。 */
export function inferColumns(rows: FuyaoRecord[], preferred: string[] = [], max = 12): FuyaoColumn[] {
  const keys: string[] = []
  for (const k of preferred) {
    if (rows.some((r) => r[k] !== undefined && r[k] !== null)) keys.push(k)
  }
  for (const row of rows) {
    for (const k of Object.keys(row)) {
      if (keys.length >= max) break
      if (keys.includes(k)) continue
      // thscode 已经展示时不再重复展示 ticker；以下划线开头的字段是面板临时字段。
      if (k === 'ticker' && keys.includes('thscode')) continue
      if (k.startsWith('_')) continue
      const v = row[k]
      if (v === undefined || v === null) continue
      keys.push(k)
    }
    if (keys.length >= max) break
  }
  return keys.map((k) => ({ key: k, label: labelOf(k), format: formatOf(k) }))
}

/** 按列格式把一条记录渲染成文本（缺失给占位符）。 */
export function formatCell(row: FuyaoRecord, col: FuyaoColumn): string {
  const raw = row[col.key]
  const num = pickNum(row, col.key)
  switch (col.format) {
    case 'price':
      return fmtPriceValue(num)
    case 'pct':
      return fmtPctValue(num)
    case 'pctPlain':
      return fmtPctPlain(num)
    case 'ratio':
      return num === undefined ? DASH : fmtRatio(num)
    case 'nav':
      return fmtNav(num)
    case 'yuan':
      return fmtYuan(num)
    case 'shares':
      return fmtShares(num)
    case 'lots':
      return fmtLots(num)
    case 'multiple':
      return fmtMultiple(num)
    case 'number':
      return num === undefined ? DASH : String(num)
    case 'stamp':
      return fmtStamp(num)
    case 'date': {
      if (typeof raw === 'string') return fmtTradeDate(raw)
      return fmtStamp(num)
    }
    case 'wide': {
      if (Array.isArray(raw)) return raw.filter((x): x is string => typeof x === 'string').join('、') || DASH
      const text = typeof raw === 'string' ? raw : raw === undefined || raw === null ? '' : String(raw)
      return text === '' ? DASH : text
    }
    default: {
      if (raw === undefined || raw === null || raw === '') return DASH
      if (typeof raw === 'boolean') return raw ? '是' : '否'
      if (typeof raw === 'number') return Number.isFinite(raw) ? String(raw) : DASH
      if (typeof raw === 'string') return valueLabelOf(col.key, raw)
      if (Array.isArray(raw)) return raw.map((x) => (typeof x === 'string' ? x : JSON.stringify(x))).join('、') || DASH
      return JSON.stringify(raw)
    }
  }
}

/** 涨跌方向（用于表格着色）：只对涨跌幅/涨跌额/净额类列生效。 */
export function cellDirection(row: FuyaoRecord, col: FuyaoColumn): 'up' | 'down' | 'flat' {
  // 只有「涨跌/净额」类列上色：持仓市值、发行价、总资产这类金额红绿会误导。
  const signed = col.format === 'pct' || (col.format === 'price' && col.key === 'price_change') || (col.format === 'yuan' && /net|change|main/.test(col.key))
  if (!signed) return 'flat'
  const v = pickNum(row, col.key)
  if (v === undefined || v === 0) return 'flat'
  return v > 0 ? 'up' : 'down'
}

// ---------- 交易时段（刷新节律用） ----------

/** 当前是否 A 股连续竞价时段（09:30–11:30 / 13:00–15:00，周一到周五）。 */
export function isTradingSession(now = new Date()): boolean {
  const day = now.getDay()
  if (day === 0 || day === 6) return false
  const minutes = now.getHours() * 60 + now.getMinutes()
  return (minutes >= 9 * 60 + 30 && minutes <= 11 * 60 + 30) || (minutes >= 13 * 60 && minutes <= 15 * 60)
}

/** 当前是否集合竞价时段（09:15–09:25，周一到周五）。 */
export function isAuctionWindow(now = new Date()): boolean {
  const day = now.getDay()
  if (day === 0 || day === 6) return false
  const minutes = now.getHours() * 60 + now.getMinutes()
  return minutes >= 9 * 60 + 15 && minutes <= 9 * 60 + 25
}
