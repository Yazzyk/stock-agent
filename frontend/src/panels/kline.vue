<script setup lang="ts">
// K 线面板：零依赖 canvas 绘制（蜡烛 + 影线 + MA5/10/20/60 + 成交量副图），
// 支持十字光标、滚轮缩放、拖拽平移、周期切换。数据来自 kline.bars。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { CHART_COLORS, CHART_MONO, prepareCanvas } from '../lib/canvas'
import { klineAnalyze, klineBars, messageOf } from '../lib/data'
import { dir, fmtPct, fmtPrice, fmtVolume, normalizeCode } from '../lib/format'
import { state } from '../stores/app'
import type { Bar, KlineAnalyzeResponse, KlineIndicators, MaSeries, Period } from '../types'

interface Geom {
  w: number
  h: number
  left: number
  plotW: number
  priceTop: number
  priceH: number
  volTop: number
  volH: number
}

interface Hover {
  index: number
  x: number
  y: number
}

// 颜色与 DPR 处理走共享 canvas 工具，量化面板的净值曲线也用同一套。
const C = CHART_COLORS
const MONO = CHART_MONO

const PERIODS: Array<{ key: Period; label: string }> = [
  { key: 'day', label: '日' },
  { key: 'week', label: '周' },
  { key: 'month', label: '月' },
  { key: 'min60', label: '60分' },
]

const EMPTY_SERIES: MaSeries = []
const emptyIndicators = (): KlineIndicators => ({
  ma5: EMPTY_SERIES,
  ma10: EMPTY_SERIES,
  ma20: EMPTY_SERIES,
  ma60: EMPTY_SERIES,
  volume: EMPTY_SERIES,
})

const code = ref('600519')
const codeInput = ref('600519')
const name = ref('')
const period = ref<Period>('day')
const bars = ref<Bar[]>([])
const inds = ref<KlineIndicators>(emptyIndicators())
const loading = ref(true)
const error = ref('')
const analyze = ref<KlineAnalyzeResponse | null>(null)
const analyzeError = ref('')
const showAnalyze = ref(true)
const visibleCount = ref(90)
const offset = ref(0)
const hover = ref<Hover | null>(null)
const dragging = ref(false)

const wrapEl = ref<HTMLElement | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)
const size = ref({ w: 0, h: 0 })

let ro: ResizeObserver | null = null
let raf = 0
let dragStartX = 0
let dragStartOffset = 0

const geom = computed<Geom>(() => {
  const w = size.value.w
  const h = size.value.h
  const left = 6
  const right = 58
  const top = 6
  const bottom = 16
  const gap = 10
  const plotW = Math.max(10, w - left - right)
  const avail = Math.max(40, h - top - bottom - gap)
  const priceH = Math.round(avail * 0.74)
  const volH = Math.max(24, avail - priceH)
  return { w, h, left, plotW, priceTop: top, priceH, volTop: top + priceH + gap, volH }
})

const view = computed(() => {
  const n = bars.value.length
  if (n === 0) return { start: 0, end: 0, count: 0 }
  const end = Math.max(1, n - offset.value)
  const count = Math.max(1, Math.min(visibleCount.value, end))
  return { start: end - count, end, count }
})

const activeIndex = computed(() => {
  const v = view.value
  if (v.count === 0) return -1
  if (hover.value) return Math.min(Math.max(hover.value.index, v.start), v.end - 1)
  return v.end - 1
})

const activeBar = computed<Bar | null>(() => {
  const i = activeIndex.value
  if (i < 0) return null
  return bars.value[i] ?? null
})

const activePct = computed<number | null>(() => {
  const i = activeIndex.value
  const b = bars.value[i]
  if (!b) return null
  const prev = bars.value[i - 1]
  const base = prev && prev.close > 0 ? prev.close : b.open
  if (!base) return null
  return ((b.close - base) / base) * 100
})

const maLegend = computed(() => {
  const i = activeIndex.value
  const src = inds.value
  const rows: Array<{ label: string; color: string; value: number | null }> = [
    { label: 'MA5', color: C.ma5, value: i >= 0 ? (src.ma5[i] ?? null) : null },
    { label: 'MA10', color: C.ma10, value: i >= 0 ? (src.ma10[i] ?? null) : null },
    { label: 'MA20', color: C.ma20, value: i >= 0 ? (src.ma20[i] ?? null) : null },
    { label: 'MA60', color: C.ma60, value: i >= 0 ? (src.ma60[i] ?? null) : null },
  ]
  return rows
})

function seriesAt(series: MaSeries, index: number): number | null {
  const v = series[index]
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  analyzeError.value = ''
  const analyzeTask = klineAnalyze(code.value, period.value).catch((e: unknown) => {
    analyzeError.value = messageOf(e)
    return null
  })
  try {
    const res = await klineBars(code.value, period.value, 320, 'qfq')
    bars.value = res.bars
    name.value = res.name
    const src = res.indicators
    inds.value = {
      ma5: src?.ma5 && src.ma5.length > 0 ? src.ma5 : emptyIndicators().ma5,
      ma10: src?.ma10 && src.ma10.length > 0 ? src.ma10 : emptyIndicators().ma10,
      ma20: src?.ma20 && src.ma20.length > 0 ? src.ma20 : emptyIndicators().ma20,
      ma60: src?.ma60 && src.ma60.length > 0 ? src.ma60 : emptyIndicators().ma60,
      volume: src?.volume && src.volume.length > 0 ? src.volume : emptyIndicators().volume,
    }
    offset.value = 0
    visibleCount.value = Math.min(90, Math.max(20, res.bars.length))
    scheduleDraw()
  } catch (e) {
    error.value = messageOf(e)
    bars.value = []
  } finally {
    const a = await analyzeTask
    analyze.value = a
    loading.value = false
  }
}

function submitCode(): void {
  const c = normalizeCode(codeInput.value)
  if (c.length !== 6) {
    error.value = '请输入 6 位股票代码，例如 600519'
    return
  }
  code.value = c
  void load()
}

function setPeriod(p: Period): void {
  if (period.value === p) return
  period.value = p
  void load()
}

// ---------- 绘制 ----------

function scheduleDraw(): void {
  if (raf !== 0) return
  raf = window.requestAnimationFrame(() => {
    raf = 0
    draw()
  })
}

function draw(): void {
  const canvas = canvasEl.value
  if (!canvas) return
  const g = geom.value
  const ctx = prepareCanvas(canvas, g.w, g.h)
  if (!ctx) return

  const list = bars.value
  const v = view.value
  if (list.length === 0 || v.count <= 0) return

  let min = Number.POSITIVE_INFINITY
  let max = Number.NEGATIVE_INFINITY
  for (let i = v.start; i < v.end; i++) {
    const b = list[i]
    if (!b) continue
    if (b.low < min) min = b.low
    if (b.high > max) max = b.high
  }
  const ind = inds.value
  const seriesKeys: Array<keyof KlineIndicators> = ['ma5', 'ma10', 'ma20', 'ma60']
  for (const key of seriesKeys) {
    const series = ind[key]
    for (let i = v.start; i < v.end; i++) {
      const val = seriesAt(series, i)
      if (val === null) continue
      if (val < min) min = val
      if (val > max) max = val
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

  const slotW = g.plotW / v.count
  const bodyW = Math.max(1, Math.min(16, slotW * 0.62))
  const yOf = (price: number): number => g.priceTop + g.priceH - ((price - min) / (max - min)) * g.priceH

  // 横向网格 + 右侧价格轴
  ctx.lineWidth = 1
  ctx.font = '10px ' + MONO
  ctx.textAlign = 'left'
  ctx.textBaseline = 'middle'
  for (let k = 0; k <= 4; k++) {
    const y = g.priceTop + (g.priceH * k) / 4
    ctx.strokeStyle = C.grid
    ctx.beginPath()
    ctx.moveTo(g.left, Math.round(y) + 0.5)
    ctx.lineTo(g.left + g.plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.fillStyle = C.text
    ctx.fillText((max - ((max - min) * k) / 4).toFixed(2), g.left + g.plotW + 6, y)
  }

  // 成交量区顶边
  ctx.strokeStyle = C.gridStrong
  ctx.beginPath()
  ctx.moveTo(g.left, Math.round(g.volTop) - 0.5)
  ctx.lineTo(g.left + g.plotW, Math.round(g.volTop) - 0.5)
  ctx.stroke()

  let volMax = 1
  for (let i = v.start; i < v.end; i++) {
    const b = list[i]
    if (b && b.volume > volMax) volMax = b.volume
  }

  // 蜡烛 + 成交量
  for (let i = 0; i < v.count; i++) {
    const b = list[v.start + i]
    if (!b) continue
    const up = b.close >= b.open
    const color = up ? C.up : C.down
    const x = g.left + (i + 0.5) * slotW
    ctx.strokeStyle = color
    ctx.fillStyle = color
    ctx.beginPath()
    ctx.moveTo(x, yOf(b.high))
    ctx.lineTo(x, yOf(b.low))
    ctx.stroke()
    const yOpen = yOf(b.open)
    const yClose = yOf(b.close)
    const top = Math.min(yOpen, yClose)
    const hgt = Math.max(1, Math.abs(yClose - yOpen))
    ctx.fillRect(x - bodyW / 2, top, bodyW, hgt)

    const vh = Math.max(1, (b.volume / volMax) * (g.volH - 4))
    ctx.globalAlpha = 0.6
    ctx.fillRect(x - bodyW / 2, g.volTop + g.volH - vh, bodyW, vh)
    ctx.globalAlpha = 1
  }

  // 均线
  const maColors: Array<[keyof KlineIndicators, string]> = [
    ['ma5', C.ma5],
    ['ma10', C.ma10],
    ['ma20', C.ma20],
    ['ma60', C.ma60],
  ]
  for (const [key, color] of maColors) {
    const series = ind[key]
    ctx.strokeStyle = color
    ctx.lineWidth = 1.2
    ctx.beginPath()
    let started = false
    for (let i = v.start; i < v.end; i++) {
      const val = seriesAt(series, i)
      if (val === null) {
        started = false
        continue
      }
      const x = g.left + (i - v.start + 0.5) * slotW
      const y = yOf(val)
      if (!started) {
        ctx.moveTo(x, y)
        started = true
      } else {
        ctx.lineTo(x, y)
      }
    }
    ctx.stroke()
  }
  ctx.lineWidth = 1

  // 时间轴
  ctx.fillStyle = C.text
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'
  const step = Math.max(1, Math.floor(v.count / 6))
  for (let i = 0; i < v.count; i += step) {
    const b = list[v.start + i]
    if (!b) continue
    ctx.fillText(b.date.slice(5), g.left + (i + 0.5) * slotW, g.volTop + g.volH + 3)
  }

  // 十字光标
  const hv = hover.value
  if (hv) {
    const idx = Math.min(Math.max(hv.index, v.start), v.end - 1)
    const x = g.left + (idx - v.start + 0.5) * slotW
    ctx.strokeStyle = C.cross
    ctx.setLineDash([3, 3])
    ctx.beginPath()
    ctx.moveTo(x, g.priceTop)
    ctx.lineTo(x, g.volTop + g.volH)
    ctx.stroke()
    const y = Math.min(Math.max(hv.y, g.priceTop), g.priceTop + g.priceH)
    ctx.beginPath()
    ctx.moveTo(g.left, y)
    ctx.lineTo(g.left + g.plotW, y)
    ctx.stroke()
    ctx.setLineDash([])

    ctx.fillStyle = C.gridStrong
    ctx.fillRect(g.left + g.plotW + 2, y - 8, 52, 16)
    ctx.fillStyle = C.text2
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    const priceAt = max - ((y - g.priceTop) / g.priceH) * (max - min)
    ctx.fillText(priceAt.toFixed(2), g.left + g.plotW + 6, y)

    const bar = list[idx]
    if (bar) {
      const label = bar.date
      const tw = ctx.measureText(label).width + 12
      const lx = Math.min(Math.max(x - tw / 2, g.left), g.left + g.plotW - tw)
      ctx.fillStyle = C.gridStrong
      ctx.fillRect(lx, g.volTop + g.volH + 1, tw, 14)
      ctx.fillStyle = C.text2
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      ctx.fillText(label, lx + tw / 2, g.volTop + g.volH + 2)
    }
  }
}

// ---------- 交互 ----------

function indexAt(clientX: number): number {
  const canvas = canvasEl.value
  if (!canvas) return -1
  const rect = canvas.getBoundingClientRect()
  const g = geom.value
  const v = view.value
  if (v.count <= 0) return -1
  const slotW = g.plotW / v.count
  const rel = clientX - rect.left - g.left
  const i = v.start + Math.floor(rel / slotW)
  return Math.min(Math.max(i, v.start), v.end - 1)
}

function onPointerMove(e: PointerEvent): void {
  const canvas = canvasEl.value
  if (!canvas) return
  const rect = canvas.getBoundingClientRect()
  const y = e.clientY - rect.top
  if (dragging.value) {
    const g = geom.value
    const v = view.value
    const slotW = v.count > 0 ? g.plotW / v.count : 1
    const shift = Math.round((e.clientX - dragStartX) / slotW)
    const maxOffset = Math.max(0, bars.value.length - 20)
    offset.value = Math.min(Math.max(dragStartOffset + shift, 0), maxOffset)
    return
  }
  const idx = indexAt(e.clientX)
  if (idx < 0) {
    hover.value = null
    return
  }
  hover.value = { index: idx, x: e.clientX - rect.left, y }
}

function onPointerLeave(): void {
  if (!dragging.value) hover.value = null
}

function onPointerDown(e: PointerEvent): void {
  const canvas = canvasEl.value
  if (!canvas || bars.value.length === 0) return
  dragging.value = true
  dragStartX = e.clientX
  dragStartOffset = offset.value
  canvas.setPointerCapture(e.pointerId)
}

function onPointerUp(e: PointerEvent): void {
  const canvas = canvasEl.value
  dragging.value = false
  if (canvas && canvas.hasPointerCapture(e.pointerId)) canvas.releasePointerCapture(e.pointerId)
}

function onWheel(e: WheelEvent): void {
  e.preventDefault()
  const n = bars.value.length
  if (n === 0) return
  const factor = e.deltaY > 0 ? 1.15 : 0.87
  const next = Math.round(visibleCount.value * factor)
  visibleCount.value = Math.min(Math.max(next, 20), n)
}

function resetView(): void {
  offset.value = 0
  visibleCount.value = Math.min(90, Math.max(20, bars.value.length))
}

watch(
  [bars, inds, hover, visibleCount, offset, size],
  () => {
    scheduleDraw()
  },
  { deep: false },
)

watch(
  () => state.focus,
  (f) => {
    if (!f) return
    code.value = f.code
    codeInput.value = f.code
    void load()
  },
)

function setupObserver(): void {
  ro = new ResizeObserver(() => {
    const el = wrapEl.value
    if (!el) return
    const rect = el.getBoundingClientRect()
    size.value = { w: Math.max(0, Math.floor(rect.width)), h: Math.max(0, Math.floor(rect.height)) }
    scheduleDraw()
  })
  if (wrapEl.value) ro.observe(wrapEl.value)
}

onMounted(async () => {
  if (state.focus) {
    code.value = state.focus.code
    codeInput.value = state.focus.code
  }
  setupObserver()
  await load()
})

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (raf !== 0) window.cancelAnimationFrame(raf)
})

const activeName = computed(() => name.value || code.value)

function maText(value: number | null): string {
  return value === null ? '—' : fmtPrice(value)
}
</script>

<template>
  <section class="card kl">
    <div class="card-hd">
      <span class="card-title">K 线</span>
      <span class="card-sub">{{ activeName }} <span class="num muted">{{ code }}</span></span>
      <span class="card-tools">
        <input
          v-model="codeInput"
          class="input"
          style="width: 96px"
          placeholder="6 位代码"
          aria-label="股票代码"
          @keydown.enter="submitCode"
        />
        <button class="btn btn-sm" type="button" @click="submitCode">查询</button>
        <span class="topbar-sep" />
        <button
          v-for="p in PERIODS"
          :key="p.key"
          class="btn btn-sm"
          :class="{ 'btn-accent': period === p.key }"
          type="button"
          @click="setPeriod(p.key)"
        >
          {{ p.label }}
        </button>
        <button class="btn btn-sm" :class="{ 'btn-accent': showAnalyze }" type="button" :aria-pressed="showAnalyze" @click="showAnalyze = !showAnalyze">判读</button>
        <button class="btn btn-sm" type="button" @click="load">刷新</button>
      </span>
    </div>

    <div class="kl-legend">
      <template v-if="activeBar">
        <span class="lg-date">{{ activeBar.date }}</span>
        <span>开 <b class="num">{{ fmtPrice(activeBar.open) }}</b></span>
        <span>高 <b class="num">{{ fmtPrice(activeBar.high) }}</b></span>
        <span>低 <b class="num">{{ fmtPrice(activeBar.low) }}</b></span>
        <span>收 <b class="num" :class="dir(activePct)">{{ fmtPrice(activeBar.close) }}</b></span>
        <span>涨跌 <b class="num" :class="dir(activePct)">{{ fmtPct(activePct) }}</b></span>
        <span>量 <b class="num">{{ fmtVolume(activeBar.volume) }}</b></span>
        <span class="topbar-sep" />
        <span v-for="m in maLegend" :key="m.label" class="lg-ma" :style="{ color: m.color }">
          {{ m.label }} <b class="num">{{ maText(m.value) }}</b>
        </span>
      </template>
      <span v-else class="tiny muted">暂无数据</span>
    </div>

    <div class="kl-body">
      <div ref="wrapEl" class="kl-wrap">
        <canvas
          ref="canvasEl"
          class="kl-canvas"
          :class="{ grabbing: dragging }"
          role="img"
          :aria-label="activeName + ' K 线图'"
          @pointermove="onPointerMove"
          @pointerdown="onPointerDown"
          @pointerup="onPointerUp"
          @pointercancel="onPointerUp"
          @pointerleave="onPointerLeave"
          @wheel.prevent="onWheel"
          @dblclick="resetView"
        />
        <div v-if="loading" class="kl-overlay"><StateBlock kind="loading" title="读取 K 线…" /></div>
        <div v-else-if="error" class="kl-overlay">
          <StateBlock kind="error" title="K 线读取失败" :message="error" action-text="重试" @action="load" />
        </div>
        <div v-else-if="bars.length === 0" class="kl-overlay">
          <StateBlock kind="empty" title="没有 K 线数据" message="确认代码是否正确；停牌或新股可能没有足够历史数据。" />
        </div>
      </div>

      <aside v-if="showAnalyze" class="kl-aside">
        <div class="ka-section">
          判读
          <span v-if="analyze?.warning" class="tag tag-accent" style="margin-left: 6px">{{ analyze.warning }}</span>
        </div>
        <template v-if="analyze">
          <div class="ka-trend">{{ analyze.summary.trend }}</div>
          <div class="ka-row"><span class="ka-k">均线</span><span class="ka-v">{{ analyze.summary.ma }}</span></div>
          <div class="ka-row"><span class="ka-k">RSI</span><span class="ka-v">{{ analyze.summary.rsi }}</span></div>
          <div class="ka-row"><span class="ka-k">MACD</span><span class="ka-v">{{ analyze.summary.macd }}</span></div>
          <div class="ka-row"><span class="ka-k">ATR</span><span class="ka-v">{{ analyze.summary.atr }}</span></div>

          <div class="ka-section">形态</div>
          <div v-if="analyze.patterns.length === 0" class="tiny muted">未识别到明确形态</div>
          <div v-else class="ka-patterns">
            <span v-for="pt in analyze.patterns" :key="pt.name + pt.index" class="tag" :title="pt.note || ''">
              {{ pt.name }} · {{ (pt.confidence * 100).toFixed(0) }}%
            </span>
          </div>

          <div class="ka-section">关键价位</div>
          <div class="ka-row">
            <span class="ka-k">支撑</span>
            <span class="ka-v num down">{{ analyze.support.slice(0, 2).map((p) => fmtPrice(p)).join(' / ') || '—' }}</span>
          </div>
          <div class="ka-row">
            <span class="ka-k">阻力</span>
            <span class="ka-v num up">{{ analyze.resistance.slice(0, 2).map((p) => fmtPrice(p)).join(' / ') || '—' }}</span>
          </div>
          <div class="ka-note">判读由 kline.analyze 生成，只描述结构，不构成买卖建议。</div>
        </template>
        <div v-else-if="analyzeError" class="tiny muted">{{ analyzeError }}</div>
        <div v-else class="tiny muted">判读读取中…</div>
      </aside>
    </div>

    <div class="kl-foot">
      <span class="tiny muted">滚轮缩放 · 拖拽平移 · 双击复位</span>
      <span class="spacer" />
      <span class="tiny muted">视窗 {{ view.count }} 根 / 共 {{ bars.length }} 根 · 前复权</span>
    </div>

  </section>
</template>


<style scoped>
  .kl {
    min-height: 0;
  }
  .kl-legend {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px 12px;
    padding: 5px 12px;
    border-bottom: 1px solid var(--line);
    color: var(--muted);
    font-size: 11.5px;
    min-height: 26px;
  }
  .kl-legend b {
    color: var(--text-2);
    font-weight: 500;
  }
  .lg-date {
    color: var(--text);
    font-family: var(--mono);
  }
  .lg-ma b {
    color: inherit;
  }
  .kl-body {
    display: flex;
    flex: 1 1 auto;
    min-height: 0;
  }
  .kl-wrap {
    position: relative;
    flex: 1 1 auto;
    min-width: 0;
    min-height: 260px;
    overflow: hidden;
  }
  .kl-aside {
    flex: 0 0 262px;
    width: 262px;
    overflow-y: auto;
    padding: 8px 10px 12px;
    border-left: 1px solid var(--line);
    background: var(--panel-2);
  }
  .ka-section {
    margin: 10px 0 5px;
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.8px;
  }
  .ka-section:first-child {
    margin-top: 0;
  }
  .ka-trend {
    padding: 6px 8px;
    border: 1px solid var(--line-2);
    border-left: 2px solid var(--accent);
    border-radius: var(--radius-sm);
    background: var(--panel);
    color: var(--text);
    font-size: 12px;
    line-height: 1.6;
  }
  .ka-row {
    display: grid;
    grid-template-columns: 42px 1fr;
    gap: 6px;
    padding: 3px 0;
    font-size: 11.5px;
  }
  .ka-k {
    color: var(--muted);
  }
  .ka-v {
    color: var(--text-2);
    line-height: 1.55;
  }
  .ka-patterns {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .ka-note {
    margin-top: 10px;
    padding-top: 8px;
    border-top: 1px solid var(--line);
    color: var(--muted);
    font-size: 10.5px;
    line-height: 1.6;
  }
  .kl-canvas {
    display: block;
    cursor: crosshair;
    touch-action: none;
  }
  .kl-canvas.grabbing {
    cursor: grabbing;
  }
  .kl-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(13, 17, 23, 0.86);
  }
  .kl-foot {
    display: flex;
    align-items: center;
    height: 24px;
    padding: 0 12px;
    border-top: 1px solid var(--line);
  }
</style>