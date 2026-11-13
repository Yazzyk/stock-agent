// 图表内核（frontend-market 维护）：零依赖 canvas 自绘。
// - CandleChart：K 线 + MA/BOLL + 成交量/MACD 副图 + 十字光标 + 买卖标记（K 线与量化共用）
// - drawFlowChart：资金流双轴图（柱=当日主力净额，线=累计净额 / 收盘价）
// - drawEquityChart：量化净值曲线（基准虚线 + 回撤阴影）
// - drawSparkline：自选列表的迷你走势
// - drawIntradayChart：当日分时（价格/均价/昨收/成交量 + 悬停读数）
// 约定：坐标一律用 CSS 像素，DPR 缩放由 fitCanvas 统一处理，调用方只负责量容器尺寸。

import { fmtAmount } from './format'
import type { Bar, EquityPoint, KlineIndicators, MaSeries, MinuteSeries } from '../types'
import type { FundFlowPoint } from './api.market'

/** 与 style.css 的色板同源（canvas 读不到 CSS 变量，改动时两边一起改）。 */
export const CHART = {
  bg: '#0b1119',
  grid: 'rgba(148, 163, 184, 0.10)',
  gridStrong: 'rgba(148, 163, 184, 0.22)',
  axis: 'rgba(148, 163, 184, 0.30)',
  text: '#6f7d92',
  text2: '#aebacb',
  text3: '#e9eff8',
  up: '#ff5a5f',
  down: '#22c08a',
  flat: '#8c98aa',
  accent: '#e9b44c',
  info: '#5b9dff',
  ma5: '#f5cc7c',
  ma10: '#5b9dff',
  ma20: '#c07bff',
  ma60: '#94a3b8',
  boll: 'rgba(233, 180, 76, 0.55)',
  bollFill: 'rgba(233, 180, 76, 0.06)',
  volUp: 'rgba(255, 90, 95, 0.55)',
  volDown: 'rgba(34, 192, 138, 0.50)',
  cross: 'rgba(174, 186, 203, 0.45)',
  tagBg: '#1b2634',
} as const

export const MONO = "ui-monospace, 'SF Mono', Menlo, Consolas, monospace"

function clamp(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v
}

/** 按 CSS 尺寸设置 backing store 并铺底色；容器过小时返回 null，调用方跳过绘制。 */
export function fitCanvas(canvas: HTMLCanvasElement, w: number, h: number): CanvasRenderingContext2D | null {
  if (w < 24 || h < 24) return null
  const dpr = window.devicePixelRatio || 1
  const pxW = Math.round(w * dpr)
  const pxH = Math.round(h * dpr)
  if (canvas.width !== pxW || canvas.height !== pxH) {
    canvas.width = pxW
    canvas.height = pxH
  }
  canvas.style.width = w + 'px'
  canvas.style.height = h + 'px'
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, w, h)
  ctx.fillStyle = CHART.bg
  ctx.fillRect(0, 0, w, h)
  return ctx
}

function seriesAt(series: MaSeries | undefined, i: number): number | null {
  const v = series?.[i]
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}

/** 日期短标签：2026-11-10 → 11-10，分钟线保留时分。 */
export function shortDate(date: string): string {
  if (date.length >= 16) return date.slice(5, 16)
  if (date.length >= 10) return date.slice(5, 10)
  return date
}

// ---------- 指标计算 ----------

export interface BollSeries {
  mid: MaSeries
  up: MaSeries
  low: MaSeries
}

/** BOLL(20,2)：中轨=MA20，上下轨=中轨 ± 2 倍标准差。 */
export function computeBoll(bars: Bar[], n = 20, k = 2): BollSeries {
  const mid: MaSeries = new Array<number | null>(bars.length).fill(null)
  const up: MaSeries = new Array<number | null>(bars.length).fill(null)
  const low: MaSeries = new Array<number | null>(bars.length).fill(null)
  let sum = 0
  let sq = 0
  for (let i = 0; i < bars.length; i++) {
    const close = bars[i]?.close ?? 0
    sum += close
    sq += close * close
    if (i >= n) {
      const out = bars[i - n]?.close ?? 0
      sum -= out
      sq -= out * out
    }
    if (i >= n - 1) {
      const mean = sum / n
      const variance = Math.max(0, sq / n - mean * mean)
      const sd = Math.sqrt(variance)
      mid[i] = mean
      up[i] = mean + k * sd
      low[i] = mean - k * sd
    }
  }
  return { mid, up, low }
}

export interface MacdSeries {
  dif: MaSeries
  dea: MaSeries
  hist: MaSeries
}

function ema(values: number[], n: number): number[] {
  const out: number[] = []
  const k = 2 / (n + 1)
  let prev = 0
  for (let i = 0; i < values.length; i++) {
    const v = values[i] ?? 0
    prev = i === 0 ? v : v * k + prev * (1 - k)
    out.push(prev)
  }
  return out
}

/** MACD(12,26,9)：hist 为 2*(DIF-DEA)，与国内行情软件口径一致。 */
export function computeMacd(bars: Bar[], fast = 12, slow = 26, signal = 9): MacdSeries {
  const closes = bars.map((b) => b.close)
  const fastEma = ema(closes, fast)
  const slowEma = ema(closes, slow)
  const difArr = closes.map((_, i) => (fastEma[i] ?? 0) - (slowEma[i] ?? 0))
  const deaArr = ema(difArr, signal)
  const dif: MaSeries = closes.map((_, i) => (i >= slow - 1 ? difArr[i] ?? 0 : null))
  const dea: MaSeries = closes.map((_, i) => (i >= slow - 1 ? deaArr[i] ?? 0 : null))
  const hist: MaSeries = closes.map((_, i) => (i >= slow - 1 ? 2 * ((difArr[i] ?? 0) - (deaArr[i] ?? 0)) : null))
  return { dif, dea, hist }
}

// ---------- K 线 ----------

export interface CandleOverlays {
  ma: boolean
  boll: boolean
}

export interface CandleOptions {
  overlays: CandleOverlays
  /** 副图：成交量 / MACD / 同时显示（成交量收窄） / 不显示 */
  sub: 'volume' | 'macd' | 'both' | 'none'
  /** 是否绘制买卖标记（量化回测用） */
  markers: boolean
}

export interface CandleMarker {
  index: number
  side: 'buy' | 'sell'
  price: number
  note?: string
}

export interface CandleHover {
  index: number
  /** 相对 canvas 的 y（CSS 像素），用于横向十字线 */
  y: number
}

export interface CandleLayout {
  left: number
  plotW: number
  right: number
  priceTop: number
  priceH: number
  /** 成交量副图 */
  volTop: number
  volH: number
  /** MACD 副图 */
  macdTop: number
  macdH: number
  /** 所有副图的下边界（日期轴/十字线终点） */
  subBottom: number
  bottom: number
  slotW: number
  start: number
  end: number
  count: number
  min: number
  max: number
}

const DEFAULT_OPTIONS: CandleOptions = {
  overlays: { ma: true, boll: false },
  sub: 'volume',
  markers: true,
}

const MIN_COUNT = 12
const DEFAULT_COUNT = 90

export class CandleChart {
  private canvas: HTMLCanvasElement
  private opts: CandleOptions
  private bars: Bar[] = []
  private inds: Partial<KlineIndicators> = {}
  private boll: BollSeries | null = null
  private macd: MacdSeries | null = null
  private markers: CandleMarker[] = []
  private w = 0
  private h = 0
  private count = DEFAULT_COUNT
  private offset = 0
  private hover: CandleHover | null = null
  private geo: CandleLayout | null = null

  constructor(canvas: HTMLCanvasElement, opts?: Partial<CandleOptions>) {
    this.canvas = canvas
    this.opts = {
      overlays: { ...DEFAULT_OPTIONS.overlays, ...(opts?.overlays ?? {}) },
      sub: opts?.sub ?? DEFAULT_OPTIONS.sub,
      markers: opts?.markers ?? DEFAULT_OPTIONS.markers,
    }
  }

  setData(bars: Bar[], inds?: Partial<KlineIndicators>, markers?: CandleMarker[]): void {
    this.bars = bars
    this.inds = inds ?? {}
    this.markers = markers ?? []
    this.boll = computeBoll(bars)
    this.macd = computeMacd(bars)
    this.clampView()
  }

  setOptions(patch: Partial<CandleOptions>): void {
    this.opts = {
      overlays: { ...this.opts.overlays, ...(patch.overlays ?? {}) },
      sub: patch.sub ?? this.opts.sub,
      markers: patch.markers ?? this.opts.markers,
    }
  }

  get options(): CandleOptions {
    return this.opts
  }

  resize(w: number, h: number): void {
    this.w = Math.max(0, w)
    this.h = Math.max(0, h)
  }

  get total(): number {
    return this.bars.length
  }

  get visibleCount(): number {
    return this.count
  }

  /** 首次载入时给一个合适的视窗：最近 90 根，数据不足时全显。 */
  reset(): void {
    const n = this.bars.length
    if (n === 0) {
      this.count = DEFAULT_COUNT
      this.offset = 0
      return
    }
    this.count = clamp(DEFAULT_COUNT, Math.min(MIN_COUNT, n), n)
    this.offset = 0
    this.clampView()
  }

  setVisible(count: number, offset = 0): void {
    this.count = Math.round(count)
    this.offset = Math.round(offset)
    this.clampView()
  }

  toLatest(): void {
    this.offset = 0
    this.clampView()
  }

  setHover(hover: CandleHover | null): void {
    this.hover = hover
  }

  range(): { start: number; end: number; count: number } {
    const n = this.bars.length
    if (n === 0) return { start: 0, end: 0, count: 0 }
    const end = Math.max(1, n - this.offset)
    const count = Math.min(this.count, end)
    return { start: Math.max(0, end - count), end, count }
  }

  private clampView(): void {
    const n = this.bars.length
    if (n === 0) {
      this.count = DEFAULT_COUNT
      this.offset = 0
      return
    }
    const minC = Math.min(MIN_COUNT, n)
    this.count = Math.round(clamp(this.count, minC, n))
    this.offset = Math.round(clamp(this.offset, 0, Math.max(0, n - this.count)))
  }

  /** 平移：delta 为正表示往前看（更早的 K 线）。 */
  pan(delta: number): void {
    this.offset = Math.round(this.offset + delta)
    this.clampView()
  }

  /** 缩放：anchor 是鼠标在绘图区内的横向比例（0..1），缩放时锚点位置不动。 */
  zoom(factor: number, anchor = 0.5): void {
    const n = this.bars.length
    if (n === 0) return
    const v = this.range()
    const minC = Math.min(MIN_COUNT, n)
    const next = Math.round(clamp(this.count * factor, minC, n))
    if (next === this.count) return
    const a = clamp(anchor, 0, 1)
    const anchorIndex = v.start + a * v.count
    const newStart = Math.round(anchorIndex - a * next)
    this.count = next
    this.offset = clamp(n - next - newStart, 0, Math.max(0, n - next))
    this.clampView()
  }

  layout(): CandleLayout | null {
    return this.geo
  }

  /** 画布相对 x → K 线下标；不在绘图区内返回 -1。 */
  indexAt(x: number): number {
    const g = this.geo
    if (!g || g.count <= 0) return -1
    const rel = x - g.left
    if (rel < 0 || rel > g.plotW) return -1
    const i = g.start + Math.floor(rel / g.slotW)
    return clamp(i, g.start, g.end - 1)
  }

  xOf(index: number): number {
    const g = this.geo
    if (!g) return 0
    return g.left + (index - g.start + 0.5) * g.slotW
  }

  priceAtY(y: number): number {
    const g = this.geo
    if (!g || g.priceH <= 0) return 0
    return g.max - ((y - g.priceTop) / g.priceH) * (g.max - g.min)
  }

  yOfPrice(price: number): number {
    const g = this.geo
    if (!g || g.max === g.min) return g?.priceTop ?? 0
    return g.priceTop + g.priceH - ((price - g.min) / (g.max - g.min)) * g.priceH
  }

  draw(): void {
    const ctx = fitCanvas(this.canvas, this.w, this.h)
    if (!ctx) return
    const w = this.w
    const h = this.h
    const bars = this.bars
    const v = this.range()
    if (bars.length === 0 || v.count <= 0) {
      this.geo = null
      this.drawEmpty(ctx, w, h)
      return
    }

    const left = 10
    const right = 64
    const top = 8
    const bottom = 18
    const plotW = Math.max(20, w - left - right)
    const availH = Math.max(40, h - top - bottom)
    // 副图：成交量与 MACD 可同时开启（同时开时成交量收窄，给 MACD 让位）
    const roomy = availH >= 120
    const showVol = roomy && (this.opts.sub === 'volume' || this.opts.sub === 'both')
    const showMacd = roomy && (this.opts.sub === 'macd' || this.opts.sub === 'both')
    const subCount = (showVol ? 1 : 0) + (showMacd ? 1 : 0)
    const gap = subCount > 0 ? 8 : 0
    let volH = 0
    let macdH = 0
    if (showVol && showMacd) {
      volH = Math.max(22, Math.round(availH * 0.13))
      macdH = Math.max(34, Math.round(availH * 0.2))
    } else if (showVol) {
      volH = Math.max(30, Math.round(availH * 0.24))
    } else if (showMacd) {
      macdH = Math.max(36, Math.round(availH * 0.26))
    }
    const subTotal = volH + macdH + (subCount > 1 ? gap : 0)
    const budget = Math.max(24, availH - 90)
    if (subTotal > budget) {
      const k = budget / Math.max(1, subTotal)
      volH = Math.round(volH * k)
      macdH = Math.round(macdH * k)
    }
    const priceH = Math.max(40, availH - subTotal - gap * subCount)
    const priceTop = top
    let cursor = priceTop + priceH
    const volTop = showVol ? cursor + gap : cursor
    if (showVol) cursor += gap + volH
    const macdTop = showMacd ? cursor + gap : cursor
    if (showMacd) cursor += gap + macdH
    const subBottom = cursor

    // ---- 价格范围：可见 K 线 + 已开启的叠加线 ----
    let min = Number.POSITIVE_INFINITY
    let max = Number.NEGATIVE_INFINITY
    const consider = (val: number | null): void => {
      if (val === null) return
      if (val < min) min = val
      if (val > max) max = val
    }
    for (let i = v.start; i < v.end; i++) {
      const b = bars[i]
      if (!b) continue
      consider(b.low)
      consider(b.high)
    }
    const keys: Array<keyof KlineIndicators> = []
    if (this.opts.overlays.ma) keys.push('ma5', 'ma10', 'ma20', 'ma60')
    for (const key of keys) {
      const series = this.inds[key]
      for (let i = v.start; i < v.end; i++) consider(seriesAt(series, i))
    }
    if (this.opts.overlays.boll && this.boll) {
      for (let i = v.start; i < v.end; i++) {
        consider(seriesAt(this.boll.up, i))
        consider(seriesAt(this.boll.low, i))
      }
    }
    if (!Number.isFinite(min) || !Number.isFinite(max)) return
    if (max - min < 0.01) {
      max += 0.5
      min -= 0.5
    }
    const pad = (max - min) * 0.06
    min -= pad
    max += pad

    const slotW = plotW / v.count
    const bodyW = Math.max(1, Math.min(16, slotW * 0.66))
    const yOf = (price: number): number => priceTop + priceH - ((price - min) / (max - min)) * priceH
    const xOf = (i: number): number => left + (i - v.start + 0.5) * slotW

    this.geo = { left, plotW, right, priceTop, priceH, volTop, volH, macdTop, macdH, subBottom, bottom, slotW, start: v.start, end: v.end, count: v.count, min, max }

    ctx.lineWidth = 1
    ctx.font = '10px ' + MONO

    // ---- 横向网格 + 右侧价格轴 ----
    for (let k = 0; k <= 4; k++) {
      const y = priceTop + (priceH * k) / 4
      ctx.strokeStyle = CHART.grid
      ctx.beginPath()
      ctx.moveTo(left, Math.round(y) + 0.5)
      ctx.lineTo(left + plotW, Math.round(y) + 0.5)
      ctx.stroke()
      ctx.fillStyle = CHART.text
      ctx.textAlign = 'left'
      ctx.textBaseline = 'middle'
      ctx.fillText((max - ((max - min) * k) / 4).toFixed(2), left + plotW + 6, y)
    }

    // ---- 副图分隔线 ----
    const separators: number[] = []
    if (showVol) separators.push(volTop)
    if (showMacd) separators.push(macdTop)
    for (const y of separators) {
      ctx.strokeStyle = CHART.gridStrong
      ctx.beginPath()
      ctx.moveTo(left, Math.round(y) - 0.5)
      ctx.lineTo(left + plotW, Math.round(y) - 0.5)
      ctx.stroke()
    }

    // ---- 悬停高亮 ----
    const hv = this.hover
    const hoverIndex = hv ? clamp(hv.index, v.start, v.end - 1) : -1
    if (hoverIndex >= 0) {
      ctx.fillStyle = 'rgba(233, 180, 76, 0.05)'
      ctx.fillRect(left + (hoverIndex - v.start) * slotW, priceTop, slotW, Math.max(1, subBottom - priceTop))
    }

    // ---- 蜡烛 + 影线 ----
    for (let i = v.start; i < v.end; i++) {
      const b = bars[i]
      if (!b) continue
      const up = b.close >= b.open
      const color = up ? CHART.up : CHART.down
      const x = xOf(i)
      ctx.strokeStyle = color
      ctx.fillStyle = color
      ctx.beginPath()
      ctx.moveTo(x, yOf(b.high))
      ctx.lineTo(x, yOf(b.low))
      ctx.stroke()
      const yOpen = yOf(b.open)
      const yClose = yOf(b.close)
      const topY = Math.min(yOpen, yClose)
      const bodyH = Math.max(1, Math.abs(yClose - yOpen))
      ctx.fillRect(x - bodyW / 2, topY, bodyW, bodyH)
    }

    // ---- BOLL 带 ----
    if (this.opts.overlays.boll && this.boll) {
      const up = this.boll.up
      const low = this.boll.low
      ctx.beginPath()
      let started = false
      for (let i = v.start; i < v.end; i++) {
        const val = seriesAt(up, i)
        if (val === null) continue
        if (!started) {
          ctx.moveTo(xOf(i), yOf(val))
          started = true
        } else ctx.lineTo(xOf(i), yOf(val))
      }
      for (let i = v.end - 1; i >= v.start; i--) {
        const val = seriesAt(low, i)
        if (val === null) continue
        ctx.lineTo(xOf(i), yOf(val))
      }
      ctx.closePath()
      ctx.fillStyle = CHART.bollFill
      ctx.fill()
      const drawBand = (series: MaSeries, color: string, dash: number[]): void => {
        ctx.strokeStyle = color
        ctx.lineWidth = 1
        ctx.setLineDash(dash)
        ctx.beginPath()
        let on = false
        for (let i = v.start; i < v.end; i++) {
          const val = seriesAt(series, i)
          if (val === null) {
            on = false
            continue
          }
          if (!on) {
            ctx.moveTo(xOf(i), yOf(val))
            on = true
          } else ctx.lineTo(xOf(i), yOf(val))
        }
        ctx.stroke()
        ctx.setLineDash([])
      }
      drawBand(up, CHART.boll, [4, 3])
      drawBand(low, CHART.boll, [4, 3])
      drawBand(this.boll.mid, CHART.accent, [])
    }

    // ---- 均线 ----
    if (this.opts.overlays.ma) {
      const mas: Array<[keyof KlineIndicators, string]> = [
        ['ma5', CHART.ma5],
        ['ma10', CHART.ma10],
        ['ma20', CHART.ma20],
        ['ma60', CHART.ma60],
      ]
      for (const [key, color] of mas) {
        const series = this.inds[key]
        if (!series) continue
        ctx.strokeStyle = color
        ctx.lineWidth = 1.2
        ctx.beginPath()
        let on = false
        for (let i = v.start; i < v.end; i++) {
          const val = seriesAt(series, i)
          if (val === null) {
            on = false
            continue
          }
          if (!on) {
            ctx.moveTo(xOf(i), yOf(val))
            on = true
          } else ctx.lineTo(xOf(i), yOf(val))
        }
        ctx.stroke()
      }
      ctx.lineWidth = 1
    }

    // ---- 副图 ----
    if (showVol) this.drawVolume(ctx, v, bars, left, plotW, volTop, volH, slotW, xOf, showMacd)
    if (showMacd && this.macd) this.drawMacd(ctx, v, left, plotW, macdTop, macdH, slotW, xOf)

    // ---- 时间轴 ----
    ctx.fillStyle = CHART.text
    ctx.textAlign = 'center'
    ctx.textBaseline = 'top'
    const axisBottom = subBottom + 4
    const labelStep = Math.max(1, Math.ceil(v.count / Math.max(2, Math.floor(plotW / 64))))
    for (let i = 0; i < v.count; i += labelStep) {
      const b = bars[v.start + i]
      if (!b) continue
      const x = left + (i + 0.5) * slotW
      const half = 26
      ctx.fillText(shortDate(b.date), clamp(x, left + half, left + plotW - half), axisBottom)
    }

    // ---- 买卖标记 ----
    if (this.opts.markers && this.markers.length > 0) {
      for (const m of this.markers) {
        if (m.index < v.start || m.index >= v.end) continue
        const x = xOf(m.index)
        const y = yOf(m.price)
        const buy = m.side === 'buy'
        const color = buy ? CHART.accent : CHART.info
        const dir = buy ? 1 : -1
        const base = y + dir * 9
        ctx.fillStyle = color
        ctx.beginPath()
        ctx.moveTo(x, base)
        ctx.lineTo(x - 4, base + dir * 7)
        ctx.lineTo(x + 4, base + dir * 7)
        ctx.closePath()
        ctx.fill()
        ctx.font = '9px ' + MONO
        ctx.fillStyle = color
        ctx.textAlign = 'center'
        ctx.textBaseline = buy ? 'top' : 'bottom'
        ctx.fillText(buy ? 'B' : 'S', x, base + dir * 8)
        ctx.font = '10px ' + MONO
      }
    }

    // ---- 最新价标签（右轴） ----
    const lastBar = bars[v.end - 1]
    if (lastBar) {
      const prev = bars[v.end - 2]
      const lastUp = lastBar.close >= (prev?.close ?? lastBar.open)
      const y = clamp(yOf(lastBar.close), priceTop + 6, priceTop + priceH - 6)
      ctx.fillStyle = lastUp ? 'rgba(255, 90, 95, 0.9)' : 'rgba(34, 192, 138, 0.9)'
      ctx.fillRect(left + plotW + 2, y - 8, right - 4, 16)
      ctx.fillStyle = '#0b1119'
      ctx.font = '10px ' + MONO
      ctx.textAlign = 'left'
      ctx.textBaseline = 'middle'
      ctx.fillText(lastBar.close.toFixed(2), left + plotW + 6, y)
    }

    // ---- 十字光标 ----
    if (hv && hoverIndex >= 0) {
      const x = xOf(hoverIndex)
      ctx.strokeStyle = CHART.cross
      ctx.setLineDash([3, 3])
      ctx.beginPath()
      ctx.moveTo(x, priceTop)
      ctx.lineTo(x, subBottom)
      ctx.stroke()
      if (hv.y >= priceTop && hv.y <= priceTop + priceH) {
        ctx.beginPath()
        ctx.moveTo(left, Math.round(hv.y) + 0.5)
        ctx.lineTo(left + plotW, Math.round(hv.y) + 0.5)
        ctx.stroke()
      }
      ctx.setLineDash([])

      // 右轴价格读数
      if (hv.y >= priceTop && hv.y <= priceTop + priceH) {
        const price = max - ((hv.y - priceTop) / priceH) * (max - min)
        ctx.fillStyle = CHART.tagBg
        ctx.fillRect(left + plotW + 2, hv.y - 8, right - 4, 16)
        ctx.fillStyle = CHART.text2
        ctx.textAlign = 'left'
        ctx.textBaseline = 'middle'
        ctx.fillText(price.toFixed(2), left + plotW + 6, hv.y)
      }

      // 底部日期读数
      const bar = bars[hoverIndex]
      if (bar) {
        const label = shortDate(bar.date)
        const tw = ctx.measureText(label).width + 12
        const lx = clamp(x - tw / 2, left, left + plotW - tw)
        ctx.fillStyle = CHART.tagBg
        ctx.fillRect(lx, axisBottom - 1, tw, 14)
        ctx.fillStyle = CHART.text2
        ctx.textAlign = 'center'
        ctx.textBaseline = 'top'
        ctx.fillText(label, lx + tw / 2, axisBottom)
      }
    }
  }

  private drawVolume(
    ctx: CanvasRenderingContext2D,
    v: { start: number; end: number; count: number },
    bars: Bar[],
    left: number,
    plotW: number,
    subTop: number,
    subH: number,
    slotW: number,
    xOf: (i: number) => number,
    narrow = false,
  ): void {
    let volMax = 1
    for (let i = v.start; i < v.end; i++) {
      const b = bars[i]
      if (b && b.volume > volMax) volMax = b.volume
    }
    const bodyW = Math.max(1, Math.min(16, slotW * 0.66))
    for (let i = v.start; i < v.end; i++) {
      const b = bars[i]
      if (!b) continue
      const up = b.close >= b.open
      const hgt = Math.max(1, (b.volume / volMax) * (subH - 6))
      ctx.fillStyle = up ? CHART.volUp : CHART.volDown
      ctx.fillRect(xOf(i) - bodyW / 2, subTop + subH - hgt, bodyW, hgt)
    }
    ctx.fillStyle = CHART.text
    ctx.font = '9px ' + MONO
    ctx.textAlign = 'left'
    ctx.textBaseline = 'top'
    ctx.fillText(narrow ? 'VOL' : 'VOL ' + fmtAmount(volMax), left + 3, subTop + 2)
    ctx.font = '10px ' + MONO
    void plotW
  }

  private drawMacd(
    ctx: CanvasRenderingContext2D,
    v: { start: number; end: number; count: number },
    left: number,
    plotW: number,
    subTop: number,
    subH: number,
    slotW: number,
    xOf: (i: number) => number,
  ): void {
    const macd = this.macd
    if (!macd) return
    let mMax = 0.0001
    for (let i = v.start; i < v.end; i++) {
      mMax = Math.max(mMax, Math.abs(seriesAt(macd.dif, i) ?? 0), Math.abs(seriesAt(macd.dea, i) ?? 0), Math.abs(seriesAt(macd.hist, i) ?? 0))
    }
    const zeroY = subTop + subH / 2
    const scale = (subH / 2 - 3) / mMax
    ctx.strokeStyle = CHART.gridStrong
    ctx.beginPath()
    ctx.moveTo(left, Math.round(zeroY) + 0.5)
    ctx.lineTo(left + plotW, Math.round(zeroY) + 0.5)
    ctx.stroke()

    const bodyW = Math.max(1, Math.min(8, slotW * 0.5))
    for (let i = v.start; i < v.end; i++) {
      const h = seriesAt(macd.hist, i)
      if (h === null) continue
      ctx.fillStyle = h >= 0 ? CHART.volUp : CHART.volDown
      const hgt = Math.abs(h) * scale
      ctx.fillRect(xOf(i) - bodyW / 2, h >= 0 ? zeroY - hgt : zeroY, bodyW, Math.max(1, hgt))
    }
    const line = (series: MaSeries, color: string): void => {
      ctx.strokeStyle = color
      ctx.lineWidth = 1.1
      ctx.beginPath()
      let on = false
      for (let i = v.start; i < v.end; i++) {
        const val = seriesAt(series, i)
        if (val === null) {
          on = false
          continue
        }
        const y = zeroY - val * scale
        if (!on) {
          ctx.moveTo(xOf(i), y)
          on = true
        } else ctx.lineTo(xOf(i), y)
      }
      ctx.stroke()
    }
    line(macd.dif, CHART.ma5)
    line(macd.dea, CHART.info)
    ctx.lineWidth = 1

    const lastDif = seriesAt(macd.dif, v.end - 1) ?? 0
    const lastDea = seriesAt(macd.dea, v.end - 1) ?? 0
    ctx.font = '9px ' + MONO
    ctx.textAlign = 'left'
    ctx.textBaseline = 'top'
    ctx.fillStyle = CHART.ma5
    ctx.fillText('DIF ' + lastDif.toFixed(2), left + 3, subTop + 2)
    ctx.fillStyle = CHART.info
    ctx.fillText('DEA ' + lastDea.toFixed(2), left + 62, subTop + 2)
    ctx.font = '10px ' + MONO
  }

  private drawEmpty(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    ctx.fillStyle = CHART.text
    ctx.font = '11px ' + MONO
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText('暂无 K 线数据', w / 2, h / 2)
  }

  /** 只释放引用，避免面板卸载后还被闭包持有。 */
  destroy(): void {
    this.bars = []
    this.inds = {}
    this.markers = []
    this.hover = null
    this.geo = null
  }
}

// ---------- 资金流双轴图 ----------

export interface FlowVisible {
  /** 当日主力净额（柱） */
  main: boolean
  /** 累计主力净额（线） */
  cumulative: boolean
  /** 收盘价（线，右轴） */
  close: boolean
}

export interface FlowChartOptions {
  width: number
  height: number
  visible: FlowVisible
  /** 悬停下标（相对 points 的下标） */
  hoverIndex?: number | null
  hoverY?: number | null
}

export interface FlowChartLayout {
  left: number
  plotW: number
  right: number
  top: number
  plotH: number
  slotW: number
  count: number
  zeroY: number
  mainMax: number
  mainMin: number
  closeMax: number
  closeMin: number
  xOf(i: number): number
  indexAt(x: number): number
  mainAt(y: number): number
  closeAt(y: number): number
}

/**
 * 资金流多日图：左轴金额（元），右轴股价。
 * 柱=当日主力净额，线=累计净额（同一金额轴，图例可关掉以免压扁柱子），线=收盘价。
 */
export function drawFlowChart(
  ctx: CanvasRenderingContext2D,
  points: FundFlowPoint[],
  opts: FlowChartOptions,
): FlowChartLayout | null {
  const w = opts.width
  const h = opts.height
  if (w < 80 || h < 60) return null
  if (points.length === 0) {
    ctx.fillStyle = CHART.text
    ctx.font = '11px ' + MONO
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText('暂无资金流数据', w / 2, h / 2)
    return null
  }

  const left = 48
  const right = 56
  const top = 12
  const bottom = 20
  const plotW = Math.max(40, w - left - right)
  const plotH = Math.max(30, h - top - bottom)
  const count = points.length
  const slotW = plotW / count

  // 累计净额
  const cumulative: number[] = []
  let acc = 0
  for (const p of points) {
    acc += p.mainNet
    cumulative.push(acc)
  }

  const showClose = opts.visible.close && points.some((p) => typeof p.close === 'number' && (p.close ?? 0) > 0)
  const showCum = opts.visible.cumulative

  let mainMax = 0
  let mainMin = 0
  if (opts.visible.main) {
    for (const p of points) {
      mainMax = Math.max(mainMax, p.mainNet)
      mainMin = Math.min(mainMin, p.mainNet)
    }
  }
  if (showCum) {
    for (const v of cumulative) {
      mainMax = Math.max(mainMax, v)
      mainMin = Math.min(mainMin, v)
    }
  }
  if (mainMax === 0 && mainMin === 0) mainMax = 1
  const mainPad = (mainMax - mainMin) * 0.12 || Math.abs(mainMax) * 0.2 || 1
  mainMax += mainPad
  mainMin -= mainPad

  let closeMax = 0
  let closeMin = 0
  if (showClose) {
    let lo = Number.POSITIVE_INFINITY
    let hi = Number.NEGATIVE_INFINITY
    for (const p of points) {
      const c = p.close ?? 0
      if (c <= 0) continue
      lo = Math.min(lo, c)
      hi = Math.max(hi, c)
    }
    if (Number.isFinite(lo) && Number.isFinite(hi)) {
      const pad = (hi - lo) * 0.1 || 0.5
      closeMin = lo - pad
      closeMax = hi + pad
    }
  }

  const yMain = (v: number): number => top + plotH - ((v - mainMin) / (mainMax - mainMin)) * plotH
  const yClose = (v: number): number =>
    closeMax === closeMin ? top + plotH / 2 : top + plotH - ((v - closeMin) / (closeMax - closeMin)) * plotH
  const xOf = (i: number): number => left + (i + 0.5) * slotW
  const zeroY = yMain(0)

  ctx.font = '10px ' + MONO
  ctx.lineWidth = 1

  // 网格 + 左轴金额标签
  for (let k = 0; k <= 4; k++) {
    const y = top + (plotH * k) / 4
    ctx.strokeStyle = CHART.grid
    ctx.beginPath()
    ctx.moveTo(left, Math.round(y) + 0.5)
    ctx.lineTo(left + plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.fillStyle = CHART.text
    ctx.textAlign = 'right'
    ctx.textBaseline = 'middle'
    ctx.fillText(fmtAmount(mainMax - ((mainMax - mainMin) * k) / 4), left - 6, y)
  }

  // 右轴股价标签
  if (showClose) {
    for (let k = 0; k <= 4; k++) {
      const y = top + (plotH * k) / 4
      ctx.fillStyle = 'rgba(91, 157, 255, 0.75)'
      ctx.textAlign = 'left'
      ctx.textBaseline = 'middle'
      ctx.fillText((closeMax - ((closeMax - closeMin) * k) / 4).toFixed(2), left + plotW + 6, y)
    }
  }

  // 0 轴
  ctx.strokeStyle = CHART.gridStrong
  ctx.beginPath()
  ctx.moveTo(left, Math.round(zeroY) + 0.5)
  ctx.lineTo(left + plotW, Math.round(zeroY) + 0.5)
  ctx.stroke()

  // 当日净额柱
  if (opts.visible.main) {
    const bodyW = Math.max(1, Math.min(18, slotW * 0.6))
    for (let i = 0; i < count; i++) {
      const p = points[i]
      if (!p) continue
      const y = yMain(p.mainNet)
      const hgt = Math.max(1, Math.abs(zeroY - y))
      ctx.fillStyle = p.mainNet >= 0 ? CHART.volUp : CHART.volDown
      ctx.fillRect(xOf(i) - bodyW / 2, p.mainNet >= 0 ? y : zeroY, bodyW, hgt)
    }
  }

  // 累计净额线
  if (showCum) {
    ctx.strokeStyle = CHART.accent
    ctx.lineWidth = 1.6
    ctx.beginPath()
    for (let i = 0; i < count; i++) {
      const y = yMain(cumulative[i] ?? 0)
      if (i === 0) ctx.moveTo(xOf(i), y)
      else ctx.lineTo(xOf(i), y)
    }
    ctx.stroke()
    ctx.lineWidth = 1
  }

  // 收盘价线
  if (showClose) {
    ctx.strokeStyle = CHART.info
    ctx.lineWidth = 1.4
    ctx.beginPath()
    let on = false
    for (let i = 0; i < count; i++) {
      const c = points[i]?.close ?? 0
      if (c <= 0) {
        on = false
        continue
      }
      const y = yClose(c)
      if (!on) {
        ctx.moveTo(xOf(i), y)
        on = true
      } else ctx.lineTo(xOf(i), y)
    }
    ctx.stroke()
    ctx.lineWidth = 1
  }

  // 日期轴
  ctx.fillStyle = CHART.text
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'
  const step = Math.max(1, Math.ceil(count / Math.max(2, Math.floor(plotW / 56))))
  for (let i = 0; i < count; i += step) {
    const p = points[i]
    if (!p) continue
    ctx.fillText(shortDate(p.date), clamp(xOf(i), left + 20, left + plotW - 20), top + plotH + 4)
  }

  // 悬停
  const hvIndex = typeof opts.hoverIndex === 'number' ? clamp(opts.hoverIndex, 0, count - 1) : -1
  if (hvIndex >= 0) {
    const x = xOf(hvIndex)
    ctx.strokeStyle = CHART.cross
    ctx.setLineDash([3, 3])
    ctx.beginPath()
    ctx.moveTo(x, top)
    ctx.lineTo(x, top + plotH)
    ctx.stroke()
    const hy = opts.hoverY
    if (typeof hy === 'number' && hy >= top && hy <= top + plotH) {
      ctx.beginPath()
      ctx.moveTo(left, Math.round(hy) + 0.5)
      ctx.lineTo(left + plotW, Math.round(hy) + 0.5)
      ctx.stroke()
    }
    ctx.setLineDash([])
    const p = points[hvIndex]
    if (p) {
      const label = shortDate(p.date)
      const tw = ctx.measureText(label).width + 12
      const lx = clamp(x - tw / 2, left, left + plotW - tw)
      ctx.fillStyle = CHART.tagBg
      ctx.fillRect(lx, top + plotH + 2, tw, 14)
      ctx.fillStyle = CHART.text2
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      ctx.fillText(label, lx + tw / 2, top + plotH + 3)
    }
  }

  return {
    left,
    plotW,
    right,
    top,
    plotH,
    slotW,
    count,
    zeroY,
    mainMax,
    mainMin,
    closeMax,
    closeMin,
    xOf,
    indexAt: (x: number): number => clamp(Math.floor((x - left) / slotW), 0, count - 1),
    mainAt: (y: number): number => mainMax - ((y - top) / plotH) * (mainMax - mainMin),
    closeAt: (y: number): number => closeMax - ((y - top) / plotH) * (closeMax - closeMin),
  }
}

// ---------- 板块/概念资金流多线图 ----------

export interface SectorFlowLine {
  name: string
  color: string
  /** 区间累计主力净额（与 dates 等长，缺失为 null） */
  values: Array<number | null>
  /** 右侧末端标签；为空时用 name */
  label?: string
}

export interface SectorFlowLinesOptions {
  width: number
  height: number
  /** 悬停的日期下标 */
  hoverIndex?: number | null
}

export interface SectorFlowLinesLayout {
  count: number
  xOf(i: number): number
  indexAt(x: number): number
}

/**
 * 板块/概念资金流多线图：Y 轴金额（元，自动换算万/亿），X 轴交易日。
 * 每条线由调用方给好累计值与颜色（流入红、流出绿），缺失值断线；
 * 返回布局供面板把指针位置映射到日期。
 */

/** 按画布宽度截断文本；放不下时以省略号结尾。 */
function truncateCanvasText(ctx: CanvasRenderingContext2D, text: string, maxWidth: number): string {
  if (ctx.measureText(text).width <= maxWidth) return text
  const chars = Array.from(text)
  while (chars.length > 1 && ctx.measureText(chars.join('') + '…').width > maxWidth) {
    chars.pop()
  }
  return chars.join('') + '…'
}

export function drawSectorFlowLines(
  ctx: CanvasRenderingContext2D,
  dates: string[],
  series: SectorFlowLine[],
  opts: SectorFlowLinesOptions,
): SectorFlowLinesLayout {
  const w = opts.width
  const h = opts.height
  const count = dates.length
  const left = 54
  const top = 12
  const bottom = 20
  // 右侧留出末端名称标签的宽度（最多 160px，超长名称会截断）。
  ctx.font = '10px ' + MONO
  let labelWidth = 0
  for (const line of series) {
    const label = (line.label || line.name || '').trim()
    if (label !== '') labelWidth = Math.max(labelWidth, ctx.measureText(label).width)
  }
  const right = Math.min(160, Math.max(14, Math.ceil(labelWidth) + 16))
  const plotW = Math.max(40, w - left - right)
  const plotH = Math.max(30, h - top - bottom)
  const xOf = (i: number): number => (count <= 1 ? left + plotW / 2 : left + (i / (count - 1)) * plotW)
  const indexAt = (x: number): number =>
    count <= 1 ? 0 : clamp(Math.round(((x - left) / plotW) * (count - 1)), 0, count - 1)

  if (w < 80 || h < 60 || count === 0 || series.length === 0) {
    if (w >= 80 && h >= 60) {
      ctx.fillStyle = CHART.text
      ctx.font = '11px ' + MONO
      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      ctx.fillText(series.length === 0 ? '暂无板块资金流数据' : '暂无日期数据', w / 2, h / 2)
    }
    return { count: 0, xOf, indexAt }
  }

  // 值域必须含 0，否则「流入/流出」的零轴会被藏起来；上下各留 8% 余量。
  let lo = 0
  let hi = 0
  for (const line of series) {
    for (const v of line.values) {
      if (typeof v !== 'number' || !Number.isFinite(v)) continue
      lo = Math.min(lo, v)
      hi = Math.max(hi, v)
    }
  }
  if (lo === 0 && hi === 0) hi = 1
  const pad = (hi - lo) * 0.08 || Math.abs(hi) * 0.2 || 1
  lo -= pad
  hi += pad
  const yOf = (v: number): number => top + plotH - ((v - lo) / (hi - lo)) * plotH

  ctx.font = '10px ' + MONO
  ctx.lineWidth = 1

  // 5 条水平网格 + 左侧金额刻度
  for (let k = 0; k <= 4; k++) {
    const y = top + (plotH * k) / 4
    ctx.strokeStyle = CHART.grid
    ctx.beginPath()
    ctx.moveTo(left, Math.round(y) + 0.5)
    ctx.lineTo(left + plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.fillStyle = CHART.text
    ctx.textAlign = 'right'
    ctx.textBaseline = 'middle'
    ctx.fillText(fmtAmount(hi - ((hi - lo) * k) / 4, 1), left - 6, y)
  }

  // 零轴加粗：一眼看出累计净流入 / 净流出
  const zeroY = yOf(0)
  ctx.strokeStyle = CHART.gridStrong
  ctx.beginPath()
  ctx.moveTo(left, Math.round(zeroY) + 0.5)
  ctx.lineTo(left + plotW, Math.round(zeroY) + 0.5)
  ctx.stroke()

  // 折线：缺失值断线，末端补圆点
  for (const line of series) {
    ctx.strokeStyle = line.color
    ctx.lineWidth = 1.4
    ctx.beginPath()
    let drawing = false
    let lastX = 0
    let lastY = 0
    for (let i = 0; i < count; i++) {
      const v = line.values[i]
      if (typeof v !== 'number' || !Number.isFinite(v)) {
        drawing = false
        continue
      }
      const x = xOf(i)
      const y = yOf(v)
      if (!drawing) {
        ctx.moveTo(x, y)
        drawing = true
      } else {
        ctx.lineTo(x, y)
      }
      lastX = x
      lastY = y
    }
    ctx.stroke()
    ctx.lineWidth = 1
    if (drawing) {
      ctx.fillStyle = line.color
      ctx.beginPath()
      ctx.arc(lastX, lastY, 2.6, 0, Math.PI * 2)
      ctx.fill()
    }
  }

  // 右侧末端名称：按末端 y 排序后做简单防重叠，名称过长时截断。
  type EndLabel = { name: string; color: string; x: number; y: number }
  const ends: EndLabel[] = []
  for (const line of series) {
    for (let i = count - 1; i >= 0; i--) {
      const v = line.values[i]
      if (typeof v !== 'number' || !Number.isFinite(v)) continue
      ends.push({ name: (line.label || line.name || '').trim(), color: line.color, x: xOf(i), y: yOf(v) })
      break
    }
  }
  ends.sort((a, b) => a.y - b.y)
  ctx.font = '10px ' + MONO
  ctx.textAlign = 'left'
  ctx.textBaseline = 'middle'
  let prevY = top - 12
  for (const end of ends) {
    if (end.name === '') continue
    const y = Math.max(end.y, prevY + 11)
    prevY = y
    const text = truncateCanvasText(ctx, end.name, Math.max(20, right - 12))
    ctx.fillStyle = end.color
    ctx.fillText(text, Math.min(end.x + 6, left + plotW + 6), clamp(y, top + 4, top + plotH - 4))
  }

  // 日期轴：约 6 个标签，首尾收进绘图区避免被裁掉
  ctx.fillStyle = CHART.text
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'
  const step = Math.max(1, Math.ceil(count / 6))
  for (let i = 0; i < count; i += step) {
    const date = dates[i]
    if (!date) continue
    ctx.fillText(shortDate(date), clamp(xOf(i), left + 18, left + plotW - 18), top + plotH + 4)
  }

  // 悬停：竖向十字线 + 每条线在当前日期的圆点 + 日期读数条
  const hvIndex = typeof opts.hoverIndex === 'number' ? clamp(opts.hoverIndex, 0, count - 1) : -1
  if (hvIndex >= 0) {
    const x = xOf(hvIndex)
    ctx.strokeStyle = CHART.cross
    ctx.setLineDash([3, 3])
    ctx.beginPath()
    ctx.moveTo(Math.round(x) + 0.5, top)
    ctx.lineTo(Math.round(x) + 0.5, top + plotH)
    ctx.stroke()
    ctx.setLineDash([])
    for (const line of series) {
      const v = line.values[hvIndex]
      if (typeof v !== 'number' || !Number.isFinite(v)) continue
      ctx.fillStyle = line.color
      ctx.beginPath()
      ctx.arc(x, yOf(v), 2.4, 0, Math.PI * 2)
      ctx.fill()
    }
    const date = dates[hvIndex]
    if (date) {
      const label = shortDate(date)
      const tw = ctx.measureText(label).width + 12
      const lx = clamp(x - tw / 2, left, left + plotW - tw)
      ctx.fillStyle = CHART.tagBg
      ctx.fillRect(lx, top + plotH + 2, tw, 14)
      ctx.fillStyle = CHART.text2
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      ctx.fillText(label, lx + tw / 2, top + plotH + 3)
    }
  }

  return { count, xOf, indexAt }
}

// ---------- 迷你走势 ----------

export interface SparklineOptions {
  width: number
  height: number
  color?: string
  fill?: boolean
  /** 参考线（成本/基准），落在区间内才画 */
  baseline?: number | null
}

/** 迷你走势：只画折线 + 面积，自动按首尾涨跌上色。 */
export function drawSparkline(ctx: CanvasRenderingContext2D, values: Array<number | null>, opts: SparklineOptions): void {
  const w = opts.width
  const h = opts.height
  const pts = values.filter((v): v is number => typeof v === 'number' && Number.isFinite(v))
  if (pts.length < 2 || w < 8 || h < 6) return
  const pad = 1.5
  let lo = Math.min(...pts)
  let hi = Math.max(...pts)
  if (hi - lo < 1e-9) {
    hi += 0.01
    lo -= 0.01
  }
  const xOf = (i: number): number => pad + (i / (pts.length - 1)) * (w - pad * 2)
  const yOf = (v: number): number => pad + (1 - (v - lo) / (hi - lo)) * (h - pad * 2)
  const color = opts.color ?? (pts[pts.length - 1] >= pts[0] ? CHART.up : CHART.down)

  ctx.beginPath()
  ctx.moveTo(xOf(0), yOf(pts[0] ?? 0))
  for (let i = 1; i < pts.length; i++) ctx.lineTo(xOf(i), yOf(pts[i] ?? 0))
  ctx.strokeStyle = color
  ctx.lineWidth = 1.2
  ctx.stroke()

  if (opts.fill !== false) {
    ctx.lineTo(xOf(pts.length - 1), h - pad)
    ctx.lineTo(xOf(0), h - pad)
    ctx.closePath()
    ctx.fillStyle = color === CHART.up ? 'rgba(255, 90, 95, 0.12)' : 'rgba(34, 192, 138, 0.12)'
    ctx.fill()
  }

  if (typeof opts.baseline === 'number' && opts.baseline > lo && opts.baseline < hi) {
    const y = yOf(opts.baseline)
    ctx.strokeStyle = CHART.gridStrong
    ctx.setLineDash([2, 2])
    ctx.beginPath()
    ctx.moveTo(pad, y)
    ctx.lineTo(w - pad, y)
    ctx.stroke()
    ctx.setLineDash([])
  }
}

// ---------- 分时图 ----------

export interface IntradayChartOptions {
  width: number
  height: number
  /** 悬停点下标（相对 series.points） */
  hoverIndex?: number | null
}

export interface IntradayChartLayout {
  count: number
  /** 横坐标 → 最近的分时点下标 */
  indexAt(x: number): number
  /** 纵坐标 → 价格（价格子图坐标系） */
  priceAt(y: number): number
}

/** A 股分时横轴槽位：09:30=0、11:30=120、13:01=121、15:00=240；不在交易时段返回 null。 */
function intradaySlot(time: string): number | null {
  const m = /^(\d{2}):(\d{2})$/.exec(time)
  if (!m) return null
  const total = Number(m[1]) * 60 + Number(m[2])
  const morningOpen = 9 * 60 + 30
  const morningClose = 11 * 60 + 30
  const afternoonOpen = 13 * 60
  const afternoonClose = 15 * 60
  if (total >= morningOpen && total <= morningClose) return total - morningOpen
  if (total >= afternoonOpen && total <= afternoonClose) {
    // 13:00 与 13:01 共用午后第一槽位（13:00 仅腾讯降级源会出现）
    return 121 + Math.max(0, total - (13 * 60 + 1))
  }
  return null
}

/**
 * 当日分时图：价格线（主色）+ 均价线（accent）+ 昨收虚线，下方成交量子图，悬停竖线。
 * 横轴按固定交易分钟槽位排布，因此午休会留出正确的间隔；空数据显示「暂无分时数据」。
 */
export function drawIntradayChart(
  ctx: CanvasRenderingContext2D,
  series: MinuteSeries | null,
  opts: IntradayChartOptions,
): IntradayChartLayout | null {
  const w = opts.width
  const h = opts.height
  if (w < 80 || h < 60) return null
  const points = series?.points ?? []
  if (points.length === 0) {
    ctx.fillStyle = CHART.text
    ctx.font = '11px ' + MONO
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText('暂无分时数据', w / 2, h / 2)
    return null
  }

  const left = 46
  const right = 14
  const top = 10
  const bottom = 18
  const plotW = Math.max(40, w - left - right)
  const plotH = Math.max(32, h - top - bottom)
  const volH = clamp(plotH * 0.22, 10, 48)
  const gap = 8
  const priceH = Math.max(12, plotH - volH - gap)
  const volTop = top + priceH + gap
  const slotMax = 240
  const entries = points.map((point, i) => ({ point, slot: intradaySlot(point.time) ?? Math.min(i, slotMax) }))
  const xOf = (slot: number): number => left + clamp(slot / slotMax, 0, 1) * plotW

  // 价格区间必须包含昨收，虚线才不会跑到图外
  let lo = Number.POSITIVE_INFINITY
  let hi = Number.NEGATIVE_INFINITY
  for (const { point } of entries) {
    if (point.price > 0) {
      lo = Math.min(lo, point.price)
      hi = Math.max(hi, point.price)
    }
    if (typeof point.avg === 'number' && point.avg > 0) {
      lo = Math.min(lo, point.avg)
      hi = Math.max(hi, point.avg)
    }
  }
  const prevClose = series?.prevClose ?? 0
  if (prevClose > 0) {
    lo = Math.min(lo, prevClose)
    hi = Math.max(hi, prevClose)
  }
  if (!Number.isFinite(lo) || !Number.isFinite(hi)) {
    lo = 0
    hi = 1
  }
  const pad = (hi - lo) * 0.08 || Math.abs(hi) * 0.005 || 0.01
  lo -= pad
  hi += pad
  const yPrice = (v: number): number => top + priceH - ((v - lo) / (hi - lo)) * priceH

  // 成交量上限
  let volMax = 0
  for (const { point } of entries) volMax = Math.max(volMax, point.volume ?? 0)
  if (volMax <= 0) volMax = 1
  const yVol = (v: number): number => volTop + volH - (clamp(v, 0, volMax) / volMax) * volH

  ctx.font = '10px ' + MONO
  ctx.lineWidth = 1

  // 价格网格 + 左轴标签
  for (let k = 0; k <= 4; k++) {
    const y = top + (priceH * k) / 4
    ctx.strokeStyle = CHART.grid
    ctx.beginPath()
    ctx.moveTo(left, Math.round(y) + 0.5)
    ctx.lineTo(left + plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.fillStyle = CHART.text
    ctx.textAlign = 'right'
    ctx.textBaseline = 'middle'
    ctx.fillText((hi - ((hi - lo) * k) / 4).toFixed(2), left - 6, y)
  }

  // 昨收虚线
  if (prevClose > 0) {
    const y = yPrice(prevClose)
    ctx.strokeStyle = CHART.gridStrong
    ctx.setLineDash([3, 3])
    ctx.beginPath()
    ctx.moveTo(left, Math.round(y) + 0.5)
    ctx.lineTo(left + plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.setLineDash([])
  }

  // 成交量网格（子图顶部一条分隔线）
  ctx.strokeStyle = CHART.grid
  ctx.beginPath()
  ctx.moveTo(left, Math.round(volTop) + 0.5)
  ctx.lineTo(left + plotW, Math.round(volTop) + 0.5)
  ctx.stroke()

  // 成交量子图：红涨绿跌
  const barW = clamp(plotW / 241, 0.8, 4)
  let prevPrice = prevClose
  for (const { point, slot } of entries) {
    const volume = point.volume ?? 0
    if (volume <= 0) continue
    const y = yVol(volume)
    ctx.fillStyle = point.price >= prevPrice ? CHART.volUp : CHART.volDown
    ctx.fillRect(xOf(slot) - barW / 2, y, barW, volTop + volH - y)
    prevPrice = point.price
  }

  // 均价线
  const avgReady = entries.some(({ point }) => typeof point.avg === 'number' && point.avg > 0)
  if (avgReady) {
    ctx.strokeStyle = CHART.accent
    ctx.lineWidth = 1.2
    ctx.beginPath()
    let on = false
    for (const { point, slot } of entries) {
      if (typeof point.avg !== 'number' || point.avg <= 0) {
        on = false
        continue
      }
      const y = yPrice(point.avg)
      if (!on) ctx.moveTo(xOf(slot), y)
      else ctx.lineTo(xOf(slot), y)
      on = true
    }
    ctx.stroke()
    ctx.lineWidth = 1
  }

  // 价格线（主色）
  ctx.strokeStyle = CHART.info
  ctx.lineWidth = 1.4
  ctx.beginPath()
  entries.forEach(({ point, slot }, i) => {
    const y = yPrice(point.price)
    if (i === 0) ctx.moveTo(xOf(slot), y)
    else ctx.lineTo(xOf(slot), y)
  })
  ctx.stroke()
  ctx.lineWidth = 1

  // X 轴时间标签：固定交易时点，午休不显示多余刻度
  ctx.fillStyle = CHART.text
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'
  const ticks: Array<[number, string]> = [
    [0, '09:30'],
    [60, '10:30'],
    [120, '11:30'],
    [180, '14:00'],
    [240, '15:00'],
  ]
  for (const [slot, label] of ticks) {
    const x = clamp(xOf(slot), left + 16, left + plotW - 16)
    ctx.fillText(label, x, top + plotH + 4)
  }

  // 悬停：竖线 + 价格/均价点 + 时间标签
  const hover = typeof opts.hoverIndex === 'number' ? clamp(opts.hoverIndex, 0, entries.length - 1) : -1
  if (hover >= 0) {
    const entry = entries[hover]
    if (entry) {
      const x = xOf(entry.slot)
      ctx.strokeStyle = CHART.cross
      ctx.setLineDash([3, 3])
      ctx.beginPath()
      ctx.moveTo(x, top)
      ctx.lineTo(x, volTop + volH)
      ctx.stroke()
      ctx.setLineDash([])
      const dot = (v: number, color: string): void => {
        ctx.fillStyle = color
        ctx.beginPath()
        ctx.arc(x, yPrice(v), 2.5, 0, Math.PI * 2)
        ctx.fill()
      }
      dot(entry.point.price, CHART.info)
      if (typeof entry.point.avg === 'number' && entry.point.avg > 0) dot(entry.point.avg, CHART.accent)
      const label = entry.point.time
      ctx.font = '10px ' + MONO
      const tw = ctx.measureText(label).width + 12
      const lx = clamp(x - tw / 2, left, left + plotW - tw)
      ctx.fillStyle = CHART.tagBg
      ctx.fillRect(lx, top + plotH + 2, tw, 14)
      ctx.fillStyle = CHART.text2
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      ctx.fillText(label, lx + tw / 2, top + plotH + 3)
    }
  }

  return {
    count: entries.length,
    indexAt: (x: number): number => {
      const target = ((x - left) / plotW) * slotMax
      let best = 0
      let bestGap = Number.POSITIVE_INFINITY
      for (let i = 0; i < entries.length; i++) {
        const entry = entries[i]
        if (!entry) continue
        const d = Math.abs(entry.slot - target)
        if (d < bestGap) {
          bestGap = d
          best = i
        }
      }
      return best
    },
    priceAt: (y: number): number => hi - ((clamp(y, top, top + priceH) - top) / priceH) * (hi - lo),
  }
}

// ---------- 量化净值曲线 ----------

export interface EquityChartOptions {
  width: number
  height: number
  initialCash: number
  hoverIndex?: number | null
  /** 回撤阴影 */
  drawdown?: boolean
}

export interface EquityChartLayout {
  left: number
  plotW: number
  top: number
  plotH: number
  count: number
  xOf(i: number): number
  yOf(v: number): number
  indexAt(x: number): number
}

/** 净值曲线：初始资金基准虚线 + 可选回撤阴影 + 十字光标。 */
export function drawEquityChart(
  ctx: CanvasRenderingContext2D,
  points: EquityPoint[],
  opts: EquityChartOptions,
): EquityChartLayout | null {
  const w = opts.width
  const h = opts.height
  if (w < 80 || h < 60) return null
  if (points.length < 2) {
    ctx.fillStyle = CHART.text
    ctx.font = '11px ' + MONO
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText('没有净值序列', w / 2, h / 2)
    return null
  }

  const left = 10
  const right = 64
  const top = 10
  const bottom = 18
  const plotW = Math.max(40, w - left - right)
  const plotH = Math.max(30, h - top - bottom)
  const init = opts.initialCash > 0 ? opts.initialCash : points[0]?.equity ?? 0
  let lo = init > 0 ? init : Number.POSITIVE_INFINITY
  let hi = init > 0 ? init : Number.NEGATIVE_INFINITY
  for (const p of points) {
    lo = Math.min(lo, p.equity)
    hi = Math.max(hi, p.equity)
  }
  if (!Number.isFinite(lo) || !Number.isFinite(hi)) return null
  if (hi - lo < 1) {
    hi += 1
    lo -= 1
  }
  const pad = (hi - lo) * 0.08
  lo -= pad
  hi += pad
  const xOf = (i: number): number => left + (i / (points.length - 1)) * plotW
  const yOf = (v: number): number => top + plotH - ((v - lo) / (hi - lo)) * plotH

  ctx.font = '10px ' + MONO
  ctx.lineWidth = 1
  for (let k = 0; k <= 4; k++) {
    const y = top + (plotH * k) / 4
    ctx.strokeStyle = CHART.grid
    ctx.beginPath()
    ctx.moveTo(left, Math.round(y) + 0.5)
    ctx.lineTo(left + plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.fillStyle = CHART.text
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    ctx.fillText(fmtAmount(hi - ((hi - lo) * k) / 4), left + plotW + 6, y)
  }

  // 回撤阴影：每个点相对此前峰值的差值区域
  if (opts.drawdown) {
    let peak = points[0]?.equity ?? init
    ctx.fillStyle = 'rgba(255, 90, 95, 0.10)'
    for (let i = 1; i < points.length; i++) {
      const p = points[i]
      if (!p) continue
      peak = Math.max(peak, p.equity)
      if (p.equity < peak) {
        const yTop = yOf(peak)
        const yBottom = yOf(p.equity)
        ctx.fillRect(xOf(i - 1), yTop, Math.max(1, xOf(i) - xOf(i - 1)), Math.max(0.5, yBottom - yTop))
      }
    }
  }

  // 基准虚线
  if (init > 0) {
    const y = yOf(init)
    ctx.strokeStyle = CHART.gridStrong
    ctx.setLineDash([4, 4])
    ctx.beginPath()
    ctx.moveTo(left, y)
    ctx.lineTo(left + plotW, y)
    ctx.stroke()
    ctx.setLineDash([])
  }

  // 面积 + 折线
  ctx.beginPath()
  ctx.moveTo(xOf(0), yOf(points[0]?.equity ?? init))
  for (let i = 1; i < points.length; i++) ctx.lineTo(xOf(i), yOf(points[i]?.equity ?? init))
  ctx.strokeStyle = CHART.accent
  ctx.lineWidth = 1.5
  ctx.stroke()
  ctx.lineTo(xOf(points.length - 1), top + plotH)
  ctx.lineTo(xOf(0), top + plotH)
  ctx.closePath()
  const grad = ctx.createLinearGradient(0, top, 0, top + plotH)
  grad.addColorStop(0, 'rgba(233, 180, 76, 0.20)')
  grad.addColorStop(1, 'rgba(233, 180, 76, 0.02)')
  ctx.fillStyle = grad
  ctx.fill()

  // 时间轴
  ctx.fillStyle = CHART.text
  ctx.textBaseline = 'top'
  const marks = [0, Math.floor(points.length / 2), points.length - 1]
  for (const i of marks) {
    const p = points[i]
    if (!p) continue
    ctx.textAlign = i === 0 ? 'left' : i === points.length - 1 ? 'right' : 'center'
    ctx.fillText(p.date, clamp(xOf(i), left, left + plotW), top + plotH + 4)
  }

  // 悬停
  const hv = typeof opts.hoverIndex === 'number' ? clamp(opts.hoverIndex, 0, points.length - 1) : -1
  if (hv >= 0) {
    const p = points[hv]
    if (p) {
      const x = xOf(hv)
      ctx.strokeStyle = CHART.cross
      ctx.setLineDash([3, 3])
      ctx.beginPath()
      ctx.moveTo(x, top)
      ctx.lineTo(x, top + plotH)
      ctx.stroke()
      const y = yOf(p.equity)
      ctx.beginPath()
      ctx.moveTo(left, y)
      ctx.lineTo(left + plotW, y)
      ctx.stroke()
      ctx.setLineDash([])
      ctx.fillStyle = CHART.tagBg
      ctx.fillRect(left + plotW + 2, y - 8, right - 4, 16)
      ctx.fillStyle = CHART.text2
      ctx.textAlign = 'left'
      ctx.textBaseline = 'middle'
      ctx.fillText(fmtAmount(p.equity), left + plotW + 6, y)
    }
  }

  return {
    left,
    plotW,
    top,
    plotH,
    count: points.length,
    xOf,
    yOf,
    indexAt: (x: number): number => clamp(Math.round(((x - left) / plotW) * (points.length - 1)), 0, points.length - 1),
  }
}
