// 行情域数据访问层（frontend-market 维护）。
// 覆盖：自选同步诊断（iteration §3.1）、资金流历史/批量（§3.2）、K 线（§3.3）、量化成交明细（§3.7.1）。
// 类型集中在本文件，不往公共 types.ts 里塞；形状与 docs/ITERATION-2026-11.md 逐字对齐。

import { callTool, friendlyError, invoke } from '../api'
import {
  arr,
  pickArr,
  pickBool,
  pickNum,
  pickStr,
  rec,
  strList,
  toBar,
  toFundFlow,
  toKlineAnalyze,
  toMaSeries,
  toQuantResult,
  toQuote,
  toStockRef,
  toWatchSnapshot,
} from './parse'
import type {
  FundFlow,
  QuantRunResponse,
  KlineAnalyzeResponse,
  KlineIndicators,
  KlineResponse as KlinePayload,
  MinutePoint,
  MinuteSeries,
  Period,
  Quote,
  StockRef,
  SyncResult,
  WailsRuntime,
  WatchSnapshot,
} from '../types'

/** 面板只展示 message；原始错误已在内核侧翻成中文。 */
export function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : friendlyError(e)
}

async function guard<T>(scene: string, fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (e) {
    throw new Error(friendlyError(e, scene))
  }
}

// ---------- 自选：同步诊断（§3.1） ----------

export type SyncActionKind =
  | 'open-url'
  | 'open-settings'
  | 'copy'
  | 'capture-cookie'
  | 'paste-cookie'
  | 'retry-sync'
  | 'none'

export type SyncSourceState = 'ok' | 'denied' | 'no-login' | 'missing' | 'empty'

export interface SyncAction {
  kind: SyncActionKind
  label: string
  value?: string
}

export interface SyncSource {
  id: string
  label: string
  available: boolean
  state: SyncSourceState
  detail?: string
  groups?: number
  items?: number
}

export interface SyncStep {
  id: string
  title: string
  detail: string
  done: boolean
  action: SyncAction
}

export interface SyncCheck {
  local: SyncSource
  web: SyncSource
  manual: SyncSource
  recommended: string
  ready: boolean
  message: string
  steps: SyncStep[]
  checkedAt: string
}

export interface CookieResult {
  ok: boolean
  source: string
  message: string
  check?: SyncCheck
}

export interface PrivacyInfo {
  settingsUrl: string
  appPath: string
  dataDir: string
  hint: string
}

/** 同花顺网页登录入口（同花顺账号密码登录）。 */
export const THS_LOGIN_URL = 'https://t.10jqka.com.cn/'
/** macOS 完全磁盘访问权限面板深链（后端 privacy 也会给，这里只是兜底）。 */
export const PRIVACY_SETTINGS_URL = 'x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles'

const ACTION_KINDS: SyncActionKind[] = ['open-url', 'open-settings', 'copy', 'capture-cookie', 'paste-cookie', 'retry-sync', 'none']
const SOURCE_STATES: SyncSourceState[] = ['ok', 'denied', 'no-login', 'missing', 'empty']

function toAction(v: unknown): SyncAction {
  const r = rec(v) ?? {}
  const raw = pickStr(r, 'kind') ?? 'none'
  const kind = (ACTION_KINDS as string[]).includes(raw) ? (raw as SyncActionKind) : 'none'
  return { kind, label: pickStr(r, 'label') ?? '继续', value: pickStr(r, 'value') }
}

function toSource(v: unknown, fallbackId: string): SyncSource {
  const r = rec(v) ?? {}
  const raw = pickStr(r, 'state') ?? 'missing'
  const state = (SOURCE_STATES as string[]).includes(raw) ? (raw as SyncSourceState) : 'missing'
  return {
    id: pickStr(r, 'id') ?? fallbackId,
    label: pickStr(r, 'label') ?? fallbackId,
    available: pickBool(r, 'available') ?? false,
    state,
    detail: pickStr(r, 'detail'),
    groups: pickNum(r, 'groups'),
    items: pickNum(r, 'items'),
  }
}

export function toSyncCheck(v: unknown): SyncCheck {
  const r = rec(v) ?? {}
  return {
    local: toSource(r.local, 'ths-local'),
    web: toSource(r.web, 'ths-web'),
    manual: toSource(r.manual, 'manual'),
    recommended: pickStr(r, 'recommended') ?? 'none',
    ready: pickBool(r, 'ready') ?? false,
    message: pickStr(r, 'message') ?? '',
    steps: pickArr(r, 'steps')
      .map((s) => rec(s))
      .filter((s): s is Record<string, unknown> => s !== null)
      .map((s) => ({
        id: pickStr(s, 'id') ?? '',
        title: pickStr(s, 'title') ?? '',
        detail: pickStr(s, 'detail') ?? '',
        done: pickBool(s, 'done') ?? false,
        action: toAction(s.action),
      }))
      .filter((s) => s.id !== ''),
    checkedAt: pickStr(r, 'checkedAt') ?? '',
  }
}

function toCookieResult(v: unknown): CookieResult {
  const r = rec(v) ?? {}
  return {
    ok: pickBool(r, 'ok') ?? false,
    source: pickStr(r, 'source') ?? '',
    message: pickStr(r, 'message') ?? (pickBool(r, 'ok') ? '已保存' : '未取到登录态'),
    check: rec(r.check) ? toSyncCheck(r.check) : undefined,
  }
}

/** 只读诊断：探测三路数据源，绝不触发同步。 */
export function watchCheck(): Promise<SyncCheck> {
  return guard('检测数据源', async () => toSyncCheck(await invoke('watchlist', 'check', {})))
}

/** 手动粘贴 cookie 保存（先验证后落盘；后端 0600）。 */
export function watchSaveCookie(cookie: string): Promise<CookieResult> {
  return guard('保存登录态', async () => toCookieResult(await invoke('watchlist', 'saveCookie', { cookie })))
}

/** 尝试从本机浏览器读取同花顺登录态。 */
export function watchCaptureCookie(): Promise<CookieResult> {
  return guard('读取浏览器登录态', async () => toCookieResult(await invoke('watchlist', 'captureCookie', {})))
}

/** 完全磁盘访问权限相关的系统信息。 */
export function watchPrivacy(): Promise<PrivacyInfo> {
  return guard('读取权限信息', async () => {
    const r = rec(await invoke('watchlist', 'privacy', {})) ?? {}
    return {
      settingsUrl: pickStr(r, 'settingsUrl') ?? PRIVACY_SETTINGS_URL,
      appPath: pickStr(r, 'appPath') ?? '',
      dataDir: pickStr(r, 'dataDir') ?? '',
      hint: pickStr(r, 'hint') ?? '',
    }
  })
}

// ---------- 打开外部链接 / 复制 ----------

interface RuntimeWithOpen extends WailsRuntime {
  BrowserOpenURL?: (url: string) => void
}

/** 用系统浏览器打开链接或系统设置深链；浏览器预览下返回 false，由面板降级提示。 */
export function openExternal(url: string): boolean {
  const rt = window.runtime as RuntimeWithOpen | undefined
  if (url && rt && typeof rt.BrowserOpenURL === 'function') {
    rt.BrowserOpenURL(url)
    return true
  }
  return false
}

/** 写剪贴板；不可用时返回 false（面板提示手动复制）。 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    return false
  }
  return false
}

// ---------- 自选（沿用既有方法，归一化后复用） ----------

export function watchList(): Promise<WatchSnapshot> {
  return guard('读取自选', async () => toWatchSnapshot(await invoke('watchlist', 'list', {})))
}

/** 同步同花顺自选（向导自动触发，或用户手动兜底）。 */
export function watchSync(): Promise<SyncResult> {
  return guard('同步同花顺自选', async () => {
    const r = rec(await invoke('watchlist', 'sync', {})) ?? {}
    return {
      success: pickBool(r, 'success') ?? false,
      message: pickStr(r, 'message') ?? '同步结束',
      groups: strList(pickArr(r, 'groups')),
      added: pickNum(r, 'added') ?? 0,
      removed: pickNum(r, 'removed') ?? 0,
      needWebLogin: pickBool(r, 'needWebLogin') ?? false,
      needFullDiskAccess: pickBool(r, 'needFullDiskAccess') ?? false,
      warnings: strList(pickArr(r, 'warnings')),
      source: pickStr(r, 'source'),
    }
  })
}

export function quotes(codes: string[]): Promise<Quote[]> {
  if (codes.length === 0) return Promise.resolve([])
  return guard('读取行情', async () => arr(await callTool('market.quote', { codes })).map(toQuote))
}

export function searchStocks(keyword: string, limit = 10): Promise<StockRef[]> {
  const q = keyword.trim()
  if (!q) return Promise.resolve([])
  return guard('搜索标的', async () => arr(await callTool('market.search', { keyword: q, limit })).map(toStockRef))
}

// ---------- 资金流（§3.2） ----------

export interface FundFlowPoint {
  date: string
  mainNet: number
  superNet: number
  largeNet: number
  mediumNet: number
  smallNet: number
  mainPct?: number
  close?: number
  changePct?: number
}

export interface FundFlowHistory {
  code: string
  name: string
  days: number
  source: string
  points: FundFlowPoint[]
}

export interface FundFlowBatch {
  items: FundFlow[]
  date: string
  source: string
}

export function toFlowPoint(v: unknown): FundFlowPoint {
  const r = rec(v) ?? {}
  return {
    date: pickStr(r, 'date') ?? '',
    mainNet: pickNum(r, 'mainNet') ?? 0,
    superNet: pickNum(r, 'superNet') ?? 0,
    largeNet: pickNum(r, 'largeNet') ?? 0,
    mediumNet: pickNum(r, 'mediumNet') ?? 0,
    smallNet: pickNum(r, 'smallNet') ?? 0,
    mainPct: pickNum(r, 'mainPct'),
    close: pickNum(r, 'close'),
    changePct: pickNum(r, 'changePct'),
  }
}

/** 解析后端返回的多日资金流对象：缺字段时用入参兜底，坏点直接丢弃。 */
function toFlowHistory(v: unknown, fallbackCode: string, fallbackDays: number): FundFlowHistory {
  const r = rec(v) ?? {}
  return {
    code: pickStr(r, 'code') ?? fallbackCode,
    name: pickStr(r, 'name') ?? '',
    days: pickNum(r, 'days') ?? fallbackDays,
    source: pickStr(r, 'source') ?? '',
    points: pickArr(r, 'points').map(toFlowPoint).filter((p) => p.date !== ''),
  }
}

/** 个股多日资金流；days 默认 20，后端上限 120。 */
export function flowHistory(code: string, days = 20): Promise<FundFlowHistory> {
  return guard('读取资金流历史', async () =>
    toFlowHistory(await invoke('fundflow', 'history', { code, days }), code, days),
  )
}

/** 板块/概念多日资金流（BK 代码）；days 默认 20，后端上限 120。 */
export function sectorFlowHistory(code: string, days = 20): Promise<FundFlowHistory> {
  return guard('读取板块资金流历史', async () =>
    toFlowHistory(await invoke('fundflow', 'sectorHistory', { code, days }), code, days),
  )
}

/** 批量板块/概念多日资金流；codes 上限 20，后端缺 items 时返回空数组而不是抛错。 */
export function sectorFlowBatch(codes: string[], days = 20): Promise<FundFlowHistory[]> {
  const list = codes.filter((c) => c !== '').slice(0, 20)
  if (list.length === 0) return Promise.resolve([])
  return guard('读取板块资金流历史', async () => {
    const r = rec(await invoke('fundflow', 'sectorBatch', { codes: list, days })) ?? {}
    return pickArr(r, 'items')
      .map((v) => toFlowHistory(v, '', days))
      .filter((h) => h.code !== '')
  })
}

/** 批量当日快照（自选列表用）；codes 上限 50。 */
export function flowBatch(codes: string[]): Promise<FundFlowBatch> {
  const list = codes.filter((c) => c !== '').slice(0, 50)
  if (list.length === 0) return Promise.resolve({ items: [], date: '', source: '' })
  return guard('读取资金流快照', async () => {
    const r = rec(await invoke('fundflow', 'batch', { codes: list })) ?? {}
    return {
      items: pickArr(r, 'items').map(toFundFlow),
      date: pickStr(r, 'date') ?? '',
      source: pickStr(r, 'source') ?? '',
    }
  })
}

// ---------- 资金流统计（纯函数，两个面板共用） ----------

export interface FlowStats {
  /** 最近 N 日主力净额合计（默认 span=5）。 */
  sum5: number
  sum10: number
  sum20: number
  /** 连续净流入/流出天数（从最新一天往回数）。 */
  streakDays: number
  streakInflow: boolean
  /** 最新一天的净额与占比。 */
  lastMain: number
  lastPct: number
  /** 区间内净流入天数 / 总天数。 */
  inflowDays: number
  totalDays: number
}

function sumTail(points: FundFlowPoint[], n: number): number {
  let sum = 0
  for (let i = Math.max(0, points.length - n); i < points.length; i++) sum += points[i]?.mainNet ?? 0
  return sum
}

export function flowStats(points: FundFlowPoint[]): FlowStats {
  const last = points[points.length - 1]
  const lastMain = last?.mainNet ?? 0
  let streakDays = 0
  const streakInflow = lastMain >= 0
  for (let i = points.length - 1; i >= 0; i--) {
    const v = points[i]?.mainNet ?? 0
    if (streakInflow ? v >= 0 : v <= 0) streakDays += 1
    else break
  }
  return {
    sum5: sumTail(points, 5),
    sum10: sumTail(points, 10),
    sum20: sumTail(points, 20),
    streakDays,
    streakInflow,
    lastMain,
    lastPct: last?.mainPct ?? 0,
    inflowDays: points.filter((p) => p.mainNet > 0).length,
    totalDays: points.length,
  }
}

/** 5 日主力净额合计（自选列）。 */
export function fiveDayMain(points: FundFlowPoint[]): number {
  return sumTail(points, 5)
}

export interface FlowNarrative {
  title: string
  lines: string[]
}

/**
 * 资金动向纯描述：连续净流入/流出、区间累计、主力与股价是否背离。
 * 只描述已发生的数据，不给买卖建议（产品红线）。
 */
export function flowNarrative(points: FundFlowPoint[]): FlowNarrative {
  if (points.length === 0) return { title: '暂无数据', lines: [] }
  const st = flowStats(points)
  const lines: string[] = []
  if (st.streakDays >= 2) {
    lines.push(
      '连续 ' + st.streakDays + ' 日主力' + (st.streakInflow ? '净流入' : '净流出') +
        '，区间合计 ' + (st.streakInflow ? '流入' : '流出') + ' ' + fmtYi(sumTail(points, st.streakDays)) + '。',
    )
  } else {
    lines.push('最新一日主力' + (st.lastMain >= 0 ? '净流入 ' : '净流出 ') + fmtYi(Math.abs(st.lastMain)) + '，方向尚未连续。')
  }
  lines.push('近 5 日累计 ' + fmtYi(st.sum5) + '，近 20 日累计 ' + fmtYi(st.sum20) + '。')
  lines.push('区间内 ' + st.totalDays + ' 个交易日中有 ' + st.inflowDays + ' 日净流入。')

  const first = points[0]
  const last = points[points.length - 1]
  const priceUp = first?.close !== undefined && last?.close !== undefined && first.close > 0 && last.close > 0
    ? last.close - first.close
    : 0
  const flowUp = st.sum20
  if (priceUp !== 0 && flowUp !== 0) {
    if (priceUp > 0 && flowUp < 0) {
      lines.push('价格区间上行而主力资金净流出，属于价量背离，留意是否为散户承接。')
    } else if (priceUp < 0 && flowUp > 0) {
      lines.push('价格区间回落而主力资金净流入，属于逆势承接，需观察能否守住支撑。')
    } else {
      lines.push('价格与主力资金同向，区间结构一致。')
    }
  }
  return { title: st.streakInflow ? '主力净流入' : '主力净流出', lines }
}

/** 金额（元）→ 带符号的「亿/万」短文案。 */
export function fmtYi(v: number): string {
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs >= 1e8) return sign + (abs / 1e8).toFixed(2) + ' 亿'
  if (abs >= 1e4) return sign + (abs / 1e4).toFixed(1) + ' 万'
  return sign + abs.toFixed(0) + ' 元'
}

// ---------- K 线（§3.3） ----------

/**
 * K 线响应 + 复权口径。
 * 后端用腾讯 fqkline 提供前/后复权，失败时降级为同花顺/新浪原始序列，此时 adjust='none'、warning 写降级原因；
 * 前端按 adjust 标注口径，并把 warning 原样展示（自己编文案会和真实口径打架）。
 */
export interface KlineResponse extends KlinePayload {
  adjust?: 'qfq' | 'none'
  warning?: string
}

function toAdjust(v: string | undefined): 'qfq' | 'none' | undefined {
  if (v === 'qfq') return 'qfq'
  if (v === 'none') return 'none'
  return undefined
}

export function klineBars(code: string, period: Period, limit = 320, adjust = 'qfq'): Promise<KlineResponse> {
  return guard('读取 K 线', async () => {
    const r = rec(await invoke('kline', 'bars', { code, period, limit, adjust })) ?? {}
    const bars = pickArr(r, 'bars').map(toBar).filter((b) => b.date !== '')
    if (bars.length === 0) throw new Error('该标的暂无 K 线数据（可能停牌或代码有误）')
    const ind = rec(r.indicators) ?? {}
    const indicators: KlineIndicators = {
      ma5: toMaSeries(ind.ma5, bars.length),
      ma10: toMaSeries(ind.ma10, bars.length),
      ma20: toMaSeries(ind.ma20, bars.length),
      ma60: toMaSeries(ind.ma60, bars.length),
      volume: toMaSeries(ind.volume, bars.length),
    }
    return {
      code: pickStr(r, 'code') ?? code,
      name: pickStr(r, 'name') ?? '',
      period: pickStr(r, 'period') ?? period,
      bars,
      indicators,
      adjust: toAdjust(pickStr(r, 'adjust')),
      warning: pickStr(r, 'warning'),
    }
  })
}

export function klineAnalyze(code: string, period: Period): Promise<KlineAnalyzeResponse> {
  return guard('读取 K 线判读', async () => {
    const parsed = toKlineAnalyze(await invoke('kline', 'analyze', { code, period }))
    if (parsed.code === '') parsed.code = code
    return parsed
  })
}

// ---------- 分时走势 ----------

/** 解析单个分时点：时间与价格缺失时返回 null（调用方丢弃该点）。 */
function toMinutePoint(v: unknown): MinutePoint | null {
  const r = rec(v) ?? {}
  const time = pickStr(r, 'time') ?? ''
  const price = pickNum(r, 'price')
  if (time === '' || price === undefined) return null
  return {
    time,
    price,
    avg: pickNum(r, 'avg'),
    volume: pickNum(r, 'volume'),
    amount: pickNum(r, 'amount'),
  }
}

/**
 * 个股/指数当日分时（09:30-15:00，价格不做复权）。
 * 缺省字段按类型声明补齐；坏点直接丢弃，空序列交给面板显示「暂无分时数据」。
 */
export function minute(code: string): Promise<MinuteSeries> {
  return guard('读取分时走势', async () => {
    const r = rec(await invoke('kline', 'minute', { code })) ?? {}
    const points = pickArr(r, 'points')
      .map(toMinutePoint)
      .filter((p): p is MinutePoint => p !== null)
    return {
      code: pickStr(r, 'code') ?? code,
      name: pickStr(r, 'name') ?? '',
      date: pickStr(r, 'date') ?? '',
      prevClose: pickNum(r, 'prevClose') ?? 0,
      points,
      source: pickStr(r, 'source'),
    }
  })
}

// ---------- 量化成交明细（§3.7.1） ----------

export interface TradeMarker {
  date: string
  side: 'buy' | 'sell'
  price: number
  qty: number
  reason?: string
}

export interface QuantRunInput {
  code: string
  period?: string
  strategy?: string
  params?: Record<string, number>
  limit?: number
  initialCash?: number
  adjust?: string
}

/** 回测响应 + 顶层逐笔成交（types.ts 的 QuantRunResponse 里没有 trades，这里扩展）。 */
export interface QuantRunFull extends QuantRunResponse {
  trades?: TradeMarker[]
}

/**
 * 运行回测。直接用 invoke 而不是 lib/data 的封装，因为顶层 trades 会被它的归一化丢掉，
 * 而那正是画买卖标记的数据源。
 */
export function quantRun(input: QuantRunInput): Promise<QuantRunFull> {
  return guard('运行回测', async () => {
    const payload: Record<string, unknown> = { code: input.code }
    if (input.period) payload.period = input.period
    if (input.strategy) payload.strategy = input.strategy
    if (input.params && Object.keys(input.params).length > 0) payload.params = input.params
    if (typeof input.limit === 'number') payload.limit = input.limit
    if (typeof input.initialCash === 'number') payload.initialCash = input.initialCash
    if (input.adjust) payload.adjust = input.adjust
    const r = rec(await invoke('quant', 'run', payload)) ?? {}
    const trades = normalizeMarkers(pickArr(r, 'trades'))
    return {
      code: pickStr(r, 'code') ?? input.code,
      period: pickStr(r, 'period') ?? input.period ?? 'day',
      adjust: pickStr(r, 'adjust') === 'none' ? 'none' : 'qfq',
      warning: pickStr(r, 'warning'),
      result: toQuantResult(r.result),
      report: pickStr(r, 'report') ?? '',
      trades,
    }
  })
}

/**
 * 从回测响应里取逐笔成交（§3.7.1）。
 * 契约：响应顶层 trades: [{date, side, price, qty, reason?}]，result.fills 是同一份数据。
 * result.trades 是旧的「平仓交易」数组，形状不同，这里不读、也不假设它。
 */
export function tradeMarkersOf(run: QuantRunResponse | null): TradeMarker[] {
  if (!run) return []
  const top = rec(run) ?? {}
  const r = rec(top.result) ?? {}
  const rows = pickArr(top, 'trades')
  const source = rows.length > 0 ? rows : pickArr(r, 'fills')
  return normalizeMarkers(source)
}

function normalizeMarkers(rows: unknown[]): TradeMarker[] {
  const out: TradeMarker[] = []
  for (const row of rows) {
    const x = rec(row)
    if (!x) continue
    const side = pickStr(x, 'side')
    if (side !== 'buy' && side !== 'sell') continue
    const date = pickStr(x, 'date', 'time') ?? ''
    const price = pickNum(x, 'price') ?? 0
    if (date === '' || price <= 0) continue
    out.push({ date, side, price, qty: pickNum(x, 'qty', 'shares') ?? 0, reason: pickStr(x, 'reason') })
  }
  out.sort((a, b) => (a.date < b.date ? -1 : a.date > b.date ? 1 : 0))
  return out.slice(0, 500)
}
