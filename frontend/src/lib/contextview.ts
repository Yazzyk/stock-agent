// 上下文洞察的纯计算层：口径（精确/估算）/ 占比 / 色调 / 组成弧长 / 堆叠条 / 趋势折线。
//
// 抽出来是为了让「标题栏小环 + 弹层」和「工作台的上下文页」共用同一套口径——
// 两处各算一遍最容易出现「环里 42%、页面里 43%」这种对不上的数字。
//
// 口径（与后端 internal/agent/context.go 一致，也和 DSH 的 contextPressure 对齐）：
//   - 有 pressureTokens（提供方上报的最近一次请求 prompt）时，已用与占比都用它，标「精确」；
//   - 没有样本时退回启发式的 usedTokens（system+tools+messages），标「估算」并加 `~` 前缀；
//   - 组成分项永远是启发式估算，只切相对比例，**不**按比例缩放到精确占用（不做对账）。
import type { AgentContext, AgentContextPoint } from './api.agent'
import type { ContextPoint } from '../stores/agent'

/** 组成环几何：viewBox 80×80，半径 30，线宽 11。 */
export const DONUT_R = 30
export const DONUT_C = 2 * Math.PI * DONUT_R
/** 趋势折线画布（viewBox 单位，实际由 CSS 拉伸到容器宽度）。 */
export const TREND_W = 348
export const TREND_H = 64

/** 组成分项配色（环形图 / 堆叠条 / 图例共用一处，避免三处各写一个色值）。 */
export const CTX_COLORS = {
  system: '#5b9dff',
  tools: '#e9b44c',
  messages: '#22c08a',
} as const

/** 模型上下文窗口（未知时 0）。 */
export function ctxWindow(c: AgentContext | null): number {
  return c ? Math.max(0, c.contextWindow) : 0
}

/** 是否有提供方精确样本（pressureTokens>0 或后端标了 provider）。 */
export function ctxExact(c: AgentContext | null): boolean {
  if (!c) return false
  return (c.pressureTokens ?? 0) > 0 || c.sample === 'provider'
}

/** 口径徽标文案：精确（提供方上报）/ 估算（启发式）。 */
export function ctxSampleLabel(c: AgentContext | null): '精确' | '估算' {
  return ctxExact(c) ? '精确' : '估算'
}

/** 已用上下文（token）：有提供方 prompt 样本就用它，否则用启发式 usedTokens。 */
export function ctxUsed(c: AgentContext | null): number {
  if (!c) return 0
  const pressure = Math.max(0, c.pressureTokens ?? 0)
  if (pressure > 0) return pressure
  return Math.max(0, c.usedTokens)
}

/**
 * 占比（0..100 的整数）。
 * 后端 percent 已按同一口径（pressure ?? used）算好，这里直接沿用；
 * 只有后端没给（旧版本/窗口未知）时才用 ctxUsed 现算一次，且同样取整——
 * 全站只有一个精度，环与页面不会差 1%。
 */
export function ctxPercent(c: AgentContext | null): number {
  if (!c) return 0
  const win = ctxWindow(c)
  if (win <= 0) return 0
  const p = Number.isFinite(c.percent) ? c.percent : 0
  if (p > 0) return Math.min(100, p)
  return Math.min(100, Math.round((ctxUsed(c) / win) * 100))
}

/** 占比文案：一律整数百分比（与后端 percent 同精度）。 */
export function percentText(p: number): string {
  if (!Number.isFinite(p) || p <= 0) return '0%'
  return Math.round(Math.min(100, p)) + '%'
}

export type CtxTone = 'ok' | 'warn' | 'err'

/** 占用色调：85% 以上告警（红），60% 以上提醒（琥珀）。 */
export function ctxTone(p: number): CtxTone {
  if (p >= 85) return 'err'
  if (p >= 60) return 'warn'
  return 'ok'
}

export interface CtxCacheView {
  /** 整数命中率；没有缓存数据时为 0 */
  hitPercent: number
  uncached: number
  cacheRead: number
  output: number
  /** prompt 侧总量（uncached + cacheRead），缓存命中率的分母 */
  prompt: number
  /** 提供方有没有上报过缓存/用量数据（全 0 且无样本 → false，前端显示「未上报」） */
  hasData: boolean
}

/**
 * 会话累计缓存命中。
 * 分母用互斥桶 uncached + cacheRead（与后端 prompt 口径一致），优先自己算，
 * 后端 hitPercent 只在没有分桶数时兜底——避免两套精度。
 */
export function ctxCache(c: AgentContext | null): CtxCacheView {
  const raw = c?.cache
  const uncached = Math.max(0, raw?.uncachedInputTokens ?? 0)
  const cacheRead = Math.max(0, raw?.cacheReadTokens ?? 0)
  const output = Math.max(0, raw?.outputTokens ?? 0)
  const prompt = uncached + cacheRead
  const reported = Math.max(0, raw?.hitPercent ?? 0)
  const hit = prompt > 0 ? Math.round((cacheRead / prompt) * 100) : Math.min(100, reported)
  // 有缓存分桶 / 输出 / 提供方 prompt 样本就说明提供方报过 usage，0 也是「真实的 0」；
  // 全 0（sample 只有 estimated）就是「提供方未上报」，不能显示成 0% 命中。
  const hasData = prompt > 0 || output > 0 || (c?.pressureTokens ?? 0) > 0
  return {
    hitPercent: Math.min(100, Math.max(0, hit)),
    uncached,
    cacheRead,
    output,
    prompt,
    hasData,
  }
}

/** 逐轮台账：后端 history（本进程内权威）；没有就返回空数组，由组件回退到本地采样。 */
export function ctxHistory(c: AgentContext | null): AgentContextPoint[] {
  const list = c?.history ?? []
  return list.length > 0 ? list : []
}

export interface CtxLegendRow {
  key: string
  label: string
  color: string
  tokens: number
  /** 占「估算分项合计」的比例（0~100），只用于切比例，不代表窗口占比 */
  pct: number
  /** 环形图上的弧长（viewBox 单位） */
  arc: number
  /** 环形图上的起始偏移（累积角度，负值表示顺时针推进） */
  offset: number
}

export function ctxBreakdownTotal(c: AgentContext | null): number {
  const b = c?.breakdown
  if (!b) return 0
  return Math.max(0, b.systemTokens) + Math.max(0, b.toolsTokens) + Math.max(0, b.messageTokens)
}

/** 组成分项 → 环形图弧段 + 图例。没有分项数据时返回全 0 的三行（画一圈灰环）。 */
export function ctxRows(c: AgentContext | null): CtxLegendRow[] {
  const b = c?.breakdown
  const base = [
    { key: 'system', label: '系统提示', color: CTX_COLORS.system, tokens: Math.max(0, b?.systemTokens ?? 0) },
    { key: 'tools', label: '工具索引', color: CTX_COLORS.tools, tokens: Math.max(0, b?.toolsTokens ?? 0) },
    { key: 'messages', label: '对话消息', color: CTX_COLORS.messages, tokens: Math.max(0, b?.messageTokens ?? 0) },
  ]
  const total = ctxBreakdownTotal(c)
  let acc = 0
  return base.map((r) => {
    const frac = total > 0 ? r.tokens / total : 0
    const arc = DONUT_C * frac
    const row: CtxLegendRow = { ...r, pct: frac * 100, arc, offset: -acc }
    acc += arc
    return row
  })
}

export interface CtxStackRow {
  key: string
  color: string
  /** 用 flex-grow 切比例：宽度为 0 的段在组件里直接不渲染 */
  tokens: number
}

/**
 * 堆叠条的一段：bar 总长取占比（精确口径），三段只切相对比例。
 * 没有分项时退化成一段纯色（长度仍是占比）。
 */
export function ctxStack(c: AgentContext | null): CtxStackRow[] {
  if (ctxBreakdownTotal(c) <= 0) return [{ key: 'total', color: CTX_COLORS.system, tokens: 1 }]
  return ctxRows(c)
    .filter((r) => r.tokens > 0)
    .map((r) => ({ key: r.key, color: r.color, tokens: r.tokens }))
}

export interface TrendPoint {
  x: number
  y: number
  used: number
  step: number
}

/** 趋势的输入：只要 step + used（后端 history 与本地采样都能直接喂进来）。 */
export interface TrendInput {
  step: number
  used: number
}

export interface TrendShape {
  poly: string
  area: string
  max: number
  min: number
  pts: TrendPoint[]
  first: TrendInput
  last: TrendInput
}

/**
 * 趋势折线：两点以上才画得出来，否则返回 null（页面给空态）。
 * 纵轴取 [min, max] 并留 12% 余量——用量常在 40K~60K 之间抖动，
 * 从 0 起画会压成一条直线，看不出爬升与压缩。
 */
export function ctxTrendPoints(points: TrendInput[]): TrendShape | null {
  const list = points.filter((p) => Number.isFinite(p.used))
  if (list.length < 2) return null
  const values = list.map((p) => Math.max(0, p.used))
  const rawMax = Math.max(...values, 1)
  const rawMin = Math.min(...values)
  const span = Math.max(rawMax - rawMin, rawMax * 0.12, 1)
  const lo = Math.max(0, rawMin - span * 0.12)
  const hi = rawMax + span * 0.12
  const pad = 4
  const xAt = (i: number) => pad + (i / (list.length - 1)) * (TREND_W - pad * 2)
  const yAt = (v: number) => TREND_H - pad - ((v - lo) / (hi - lo)) * (TREND_H - pad * 2)
  const pts: TrendPoint[] = list.map((p, i) => ({
    x: xAt(i),
    y: yAt(Math.max(0, p.used)),
    used: Math.max(0, p.used),
    step: p.step,
  }))
  const poly = pts.map((p) => p.x.toFixed(1) + ',' + p.y.toFixed(1)).join(' ')
  const floor = (TREND_H - pad).toFixed(1)
  const area = poly + ' ' + (TREND_W - pad).toFixed(1) + ',' + floor + ' ' + pad + ',' + floor
  const first = list[0]
  const last = list[list.length - 1]
  return { poly, area, max: rawMax, min: rawMin, pts, first, last }
}

/** 兼容旧名：store 里的 ContextPoint 也能直接传（多出的字段被忽略）。 */
export function ctxTrend(history: ContextPoint[]): TrendShape | null {
  return ctxTrendPoints(history)
}

/**
 * token 数缩写：517 / 12.2K / 517K / 1.2M。
 * 规则：≥1000 缩量；缩量后 ≥100 取整，<100 留一位小数（与 DSH formatTokens 逐字一致）。
 */
export function formatTok(v: number): string {
  if (!Number.isFinite(v) || v <= 0) return '0'
  const scaled = (candidate: number): string =>
    candidate >= 100 ? String(Math.round(candidate)) : String(Math.round(candidate * 10) / 10)
  if (v < 1000) return String(Math.round(v))
  if (v < 1_000_000) return scaled(v / 1000) + 'K'
  return scaled(v / 1_000_000) + 'M'
}

/** 台账格子：缺失（0 / 非法）显示「—」，不要显示成 0。 */
export function tokCell(v: number | undefined): string {
  if (typeof v !== 'number' || !Number.isFinite(v) || v <= 0) return '—'
  return formatTok(v)
}

/** 只取时分秒：台账的时间列（HH:MM:SS），解析失败时退回原串前 8 位。 */
export function clockText(iso: string): string {
  if (!iso) return ''
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso.length > 8 ? iso.slice(-8) : iso
  return stampText(iso).slice(6)
}

/** MM-DD HH:MM:SS：页头的更新时间（本地时区）。 */
export function stampText(iso: string): string {
  if (!iso) return ''
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  const d = new Date(ts)
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}
