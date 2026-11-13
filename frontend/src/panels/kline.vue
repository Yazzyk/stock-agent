<script setup lang="ts">
// K 线面板：零依赖 canvas（lib/chart.ts 的 CandleChart）+ 自选快捷条 + 十字光标读数条 + 右侧研判。
// 交互：滚轮以鼠标为中心缩放、拖拽平移、双击复位、←/→ 平移、+/- 缩放、Esc 退出最大化。
// 数据只走 lib/api.market.ts（kline.bars / kline.analyze）；从自选或全局搜索跳转时按 state.focus 载入。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import StockPicker from '../components/StockPicker.vue'
import StateBlock from '../components/StateBlock.vue'
import { klineAnalyze, klineBars, messageOf, watchList } from '../lib/api.market'
import { CandleChart, CHART, computeMacd, shortDate, type CandleOptions } from '../lib/chart'
import { dir, fmtAmount, fmtPct, fmtPrice, fmtVolume } from '../lib/format'
import { state } from '../stores/app'
import type { Bar, KlineAnalyzeResponse, KlineHorizon, KlineIndicators, Period, WatchItem } from '../types'

const PERIODS: Array<{ key: Period; label: string }> = [
  { key: 'day', label: '日' },
  { key: 'week', label: '周' },
  { key: 'month', label: '月' },
  { key: 'min60', label: '60分' },
]

const emptyIndicators = (): Partial<KlineIndicators> => ({})

const code = ref('600519')
const name = ref('')
const period = ref<Period>('day')
const bars = ref<Bar[]>([])
const inds = ref<Partial<KlineIndicators>>(emptyIndicators())
const loading = ref(false)
const error = ref('')
const analyze = ref<KlineAnalyzeResponse | null>(null)
const analyzeError = ref('')

// 研判尺度：短线 / 长线，默认短线；旧后端没有 horizons 时自动回退到单份摘要。
const horizonKey = ref<'short' | 'long'>('short')
const horizonList = computed<KlineHorizon[]>(() => analyze.value?.horizons ?? [])
const activeHorizon = computed<KlineHorizon | null>(() => {
  const list = horizonList.value
  if (list.length === 0) return null
  return list.find((h) => h.key === horizonKey.value) ?? list[0]
})

// 指标开关
const showMa = ref(true)
const showBoll = ref(false)
const showVolume = ref(true)
const showMacd = ref(false)

// 研判栏：用户意愿 + 窄屏自动收起
const asideWanted = ref(true)
const wide = ref(true)
const maximized = ref(false)
const asideVisible = computed(() => asideWanted.value && wide.value && !maximized.value)

const chips = ref<WatchItem[]>([])
const hover = ref<{ index: number; y: number } | null>(null)
const dragging = ref(false)
const size = ref({ w: 0, h: 0 })
/** 视窗版本号：chart 是普通对象，缩放/平移后用它通知读数条重算。 */
const viewTick = ref(0)

const wrapEl = ref<HTMLElement | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)
const rootEl = ref<HTMLElement | null>(null)

let chart: CandleChart | null = null
let ro: ResizeObserver | null = null
let raf = 0
let loadSeq = 0
let dragStartX = 0
let dragStartOffset = 0
let dragMoved = false

const MACD = computed(() => (bars.value.length > 0 ? computeMacd(bars.value) : null))

const activeIndex = computed(() => {
  const n = bars.value.length
  void viewTick.value
  if (n === 0) return -1
  const view = chart?.range() ?? { start: 0, end: n, count: n }
  if (hover.value) {
    return Math.min(Math.max(hover.value.index, view.start), Math.max(view.start, view.end - 1))
  }
  return Math.max(0, view.end - 1)
})

const activeBar = computed<Bar | null>(() => bars.value[activeIndex.value] ?? null)

const activePct = computed<number | null>(() => {
  const i = activeIndex.value
  const b = bars.value[i]
  if (!b) return null
  const prev = bars.value[i - 1]
  const base = prev && prev.close > 0 ? prev.close : b.open
  if (!base) return null
  return ((b.close - base) / base) * 100
})

const activeAmplitude = computed<number | null>(() => {
  const i = activeIndex.value
  const b = bars.value[i]
  if (!b) return null
  const prev = bars.value[i - 1]
  const base = prev && prev.close > 0 ? prev.close : b.open
  if (!base) return null
  return ((b.high - b.low) / base) * 100
})

const maReadout = computed(() => {
  const i = activeIndex.value
  const src = inds.value
  const rows: Array<{ label: string; color: string; value: number | null }> = [
    { label: 'MA5', color: CHART.ma5, value: i >= 0 ? numAt(src.ma5, i) : null },
    { label: 'MA10', color: CHART.ma10, value: i >= 0 ? numAt(src.ma10, i) : null },
    { label: 'MA20', color: CHART.ma20, value: i >= 0 ? numAt(src.ma20, i) : null },
    { label: 'MA60', color: CHART.ma60, value: i >= 0 ? numAt(src.ma60, i) : null },
  ]
  return rows
})

const macdReadout = computed(() => {
  const m = MACD.value
  const i = activeIndex.value
  if (!m || i < 0) return null
  return { dif: numAt(m.dif, i), dea: numAt(m.dea, i), hist: numAt(m.hist, i) }
})

function numAt(series: Array<number | null> | undefined, i: number): number | null {
  const v = series?.[i]
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}

/** 尺度切换按钮文案：短线/长线由 key 决定，不依赖后端标题长度。 */
function horizonLabel(h: KlineHorizon): string {
  return h.key === 'long' ? '长线' : '短线'
}

/** 研判倾向对应的样式类：偏多=红（up）、偏空=绿（down）、其余中性。 */
function biasClass(bias: string): string {
  if (bias.startsWith('偏多')) return 'bull'
  if (bias.startsWith('偏空')) return 'bear'
  return 'flat'
}

const viewText = computed(() => {
  void viewTick.value
  const view = chart?.range()
  const total = bars.value.length
  if (!view || total === 0) return '共 0 根'
  return '视窗 ' + view.count + ' 根 / 共 ' + total + ' 根'
})

const activeName = computed(() => name.value || code.value)

const periodLabel = computed(() => PERIODS.find((p) => p.key === period.value)?.label ?? '日')

/** 复权口径与降级提示：都来自 kline.bars 的响应，前端不自己编。 */
const adjust = ref<'qfq' | 'none' | ''>('')
const dataWarning = ref('')
const warningClosed = ref(false)

const adjustLabel = computed(() =>
  adjust.value === 'qfq' ? '前复权' : adjust.value === 'none' ? '不复权' : '复权口径未知',
)

function chartOptions(): Partial<CandleOptions> {
  return {
    overlays: { ma: showMa.value, boll: showBoll.value },
    sub: showMacd.value ? (showVolume.value ? 'both' : 'macd') : showVolume.value ? 'volume' : 'none',
    markers: true,
  }
}

function ensureChart(): CandleChart | null {
  const canvas = canvasEl.value
  if (!canvas) return null
  if (!chart) chart = new CandleChart(canvas, chartOptions())
  chart.resize(size.value.w, size.value.h)
  return chart
}

function scheduleDraw(): void {
  if (raf !== 0) return
  raf = window.requestAnimationFrame(() => {
    raf = 0
    chart?.draw()
  })
}

function syncOptions(): void {
  chart?.setOptions(chartOptions())
  scheduleDraw()
}

async function load(keepView = false): Promise<void> {
  const seq = ++loadSeq
  loading.value = true
  error.value = ''
  analyzeError.value = ''
  const target = code.value
  const periodNow = period.value
  const analyzeTask = klineAnalyze(target, periodNow)
    .then((res) => {
      if (seq !== loadSeq) return
      analyze.value = res
    })
    .catch((e: unknown) => {
      if (seq !== loadSeq) return
      analyze.value = null
      analyzeError.value = messageOf(e)
    })
  try {
    const res = await klineBars(target, periodNow, 320, 'qfq')
    if (seq !== loadSeq) return
    bars.value = res.bars
    if (res.name) name.value = res.name
    inds.value = res.indicators ?? emptyIndicators()
    adjust.value = res.adjust ?? ''
    dataWarning.value = res.warning ?? ''
    warningClosed.value = false
    await nextTick()
    const c = ensureChart()
    if (c) {
      c.setData(bars.value, inds.value)
      if (!keepView) c.reset()
      viewTick.value += 1
      scheduleDraw()
    }
  } catch (e) {
    if (seq !== loadSeq) return
    bars.value = []
    adjust.value = ''
    dataWarning.value = ''
    error.value = messageOf(e)
    chart?.setData([], emptyIndicators())
    scheduleDraw()
  } finally {
    await analyzeTask
    if (seq === loadSeq) loading.value = false
  }
}

function pick(next: string, nextName: string): void {
  if (next === code.value) return
  code.value = next
  name.value = nextName
  hover.value = null
  void load()
}

function onPickerSelect(pickRow: { code: string; name: string }): void {
  pick(pickRow.code, pickRow.name)
}

function setPeriod(p: Period): void {
  if (period.value === p) return
  period.value = p
  hover.value = null
  void load()
}

function toggleMax(): void {
  maximized.value = !maximized.value
  window.setTimeout(measure, 60)
}

function toggleAside(): void {
  asideWanted.value = !asideWanted.value
  window.setTimeout(measure, 60)
}

// ---------- 尺寸 ----------

function measure(): void {
  const el = wrapEl.value
  const root = rootEl.value
  if (root) wide.value = root.getBoundingClientRect().width >= 1040
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height))
  if (w !== size.value.w || h !== size.value.h) {
    size.value = { w, h }
    chart?.resize(w, h)
  }
  scheduleDraw()
}

function setupObserver(): void {
  if (ro) ro.disconnect()
  ro = new ResizeObserver(() => measure())
  if (wrapEl.value) ro.observe(wrapEl.value)
  if (rootEl.value) ro.observe(rootEl.value)
}

// ---------- 指针交互 ----------

function localPoint(e: PointerEvent | WheelEvent): { x: number; y: number } | null {
  const canvas = canvasEl.value
  if (!canvas) return null
  const rect = canvas.getBoundingClientRect()
  return { x: e.clientX - rect.left, y: e.clientY - rect.top }
}

function onPointerDown(e: PointerEvent): void {
  const canvas = canvasEl.value
  if (!canvas || bars.value.length === 0) return
  dragging.value = true
  dragMoved = false
  dragStartX = e.clientX
  dragStartOffset = chart?.range().start ?? 0
  canvas.setPointerCapture(e.pointerId)
}

function onPointerMove(e: PointerEvent): void {
  const p = localPoint(e)
  if (!p) return
  if (dragging.value) {
    const c = chart
    if (!c) return
    const layout = c.layout()
    const slotW = layout && layout.slotW > 0 ? layout.slotW : 8
    const dx = e.clientX - dragStartX
    if (Math.abs(dx) > 3) dragMoved = true
    if (dragMoved) {
      const total = c.total
      const count = c.visibleCount
      const start = dragStartOffset - Math.round(dx / slotW)
      const offset = Math.max(0, total - count - start)
      c.setVisible(count, offset)
      viewTick.value += 1
      scheduleDraw()
    }
    return
  }
  const c = chart
  if (!c) return
  const idx = c.indexAt(p.x)
  if (idx < 0) {
    if (hover.value) {
      hover.value = null
      c.setHover(null)
      scheduleDraw()
    }
    return
  }
  hover.value = { index: idx, y: p.y }
  c.setHover({ index: idx, y: p.y })
  scheduleDraw()
}

function onPointerUp(e: PointerEvent): void {
  const canvas = canvasEl.value
  dragging.value = false
  if (canvas && canvas.hasPointerCapture(e.pointerId)) canvas.releasePointerCapture(e.pointerId)
}

function onPointerLeave(): void {
  if (dragging.value) return
  hover.value = null
  chart?.setHover(null)
  scheduleDraw()
}

function onWheel(e: WheelEvent): void {
  const c = chart
  if (!c) return
  const layout = c.layout()
  const p = localPoint(e)
  if (!layout || !p) return
  const ratio = (p.x - layout.left) / layout.plotW
  const factor = e.deltaY > 0 ? 1.16 : 0.86
  c.zoom(factor, ratio)
  viewTick.value += 1
  scheduleDraw()
}

function onDoubleClick(): void {
  chart?.reset()
  hover.value = null
  chart?.setHover(null)
  viewTick.value += 1
  scheduleDraw()
}

function onKey(e: KeyboardEvent): void {
  const c = chart
  if (!c) return
  const step = Math.max(3, Math.round(c.visibleCount * 0.15))
  switch (e.key) {
    case 'ArrowLeft':
      e.preventDefault()
      c.pan(step)
      break
    case 'ArrowRight':
      e.preventDefault()
      c.pan(-step)
      break
    case '+':
    case '=':
      e.preventDefault()
      c.zoom(0.86)
      break
    case '-':
    case '_':
      e.preventDefault()
      c.zoom(1.18)
      break
    case 'Home':
    case '0':
      e.preventDefault()
      c.reset()
      break
    case 'Escape':
      maximized.value = false
      break
    default:
      return
  }
  viewTick.value += 1
  scheduleDraw()
}

function onDocKey(e: KeyboardEvent): void {
  if (e.key === 'Escape' && maximized.value) maximized.value = false
}

// ---------- 生命周期 ----------

watch([showMa, showBoll, showVolume, showMacd], syncOptions)

watch(
  () => state.focus,
  (f) => {
    if (!f) return
    if (f.code === code.value) return
    code.value = f.code
    name.value = f.name
    hover.value = null
    void load()
  },
)

watch(maximized, () => {
  window.setTimeout(() => {
    measure()
  }, 80)
})

onMounted(async () => {
  if (state.focus) {
    code.value = state.focus.code
    name.value = state.focus.name
  }
  document.addEventListener('keydown', onDocKey)
  await nextTick()
  setupObserver()
  await load()
  try {
    const snap = await watchList()
    chips.value = snap.items.slice(0, 20)
  } catch {
    chips.value = []
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onDocKey)
  if (ro) ro.disconnect()
  if (raf !== 0) window.cancelAnimationFrame(raf)
  chart?.destroy()
  chart = null
})
</script>

<template>
  <section ref="rootEl" class="card kl" :class="{ max: maximized }">
    <div class="card-hd">
      <span class="card-title">K 线</span>
      <span class="card-sub ellipsis">{{ activeName }} <span class="num muted">{{ code }}</span> · {{ periodLabel }}</span>
      <span class="card-tools">
        <StockPicker :model-value="code" :name="name" :width="186" @select="onPickerSelect" />
        <span class="seg">
          <button
            v-for="p in PERIODS"
            :key="p.key"
            class="seg-item"
            :class="{ on: period === p.key }"
            type="button"
            @click="setPeriod(p.key)"
          >
            {{ p.label }}
          </button>
        </span>
        <span class="topbar-sep" />
        <button class="chip" :class="{ on: showMa }" type="button" :aria-pressed="showMa" @click="showMa = !showMa">MA</button>
        <button class="chip" :class="{ on: showBoll }" type="button" :aria-pressed="showBoll" @click="showBoll = !showBoll">BOLL</button>
        <button class="chip" :class="{ on: showVolume }" type="button" :aria-pressed="showVolume" @click="showVolume = !showVolume">量</button>
        <button class="chip" :class="{ on: showMacd }" type="button" :aria-pressed="showMacd" @click="showMacd = !showMacd">MACD</button>
        <span class="topbar-sep" />
        <button class="btn btn-sm" type="button" title="回到最新" @click="onDoubleClick">最新</button>
        <button
          class="btn btn-sm"
          type="button"
          :aria-pressed="asideWanted"
          @click="toggleAside"
        >
          {{ asideVisible ? '收起研判' : '研判' }}
        </button>
        <button class="btn btn-sm" type="button" :aria-pressed="maximized" @click="toggleMax">
          {{ maximized ? '还原' : '最大化' }}
        </button>
      </span>
    </div>

    <div v-if="chips.length > 0" class="kl-chips">
      <span class="tiny muted kl-chips-label">自选</span>
      <button
        v-for="c in chips"
        :key="c.code"
        class="chip"
        :class="{ on: c.code === code }"
        type="button"
        :title="c.code + ' ' + c.name"
        @click="pick(c.code, c.name)"
      >
        {{ c.name }}
      </button>
    </div>

    <div class="kl-readout">
      <template v-if="activeBar">
        <span class="ro-date num">{{ shortDate(activeBar.date) }}</span>
        <span class="ro-item">开 <b class="num">{{ fmtPrice(activeBar.open) }}</b></span>
        <span class="ro-item">高 <b class="num">{{ fmtPrice(activeBar.high) }}</b></span>
        <span class="ro-item">低 <b class="num">{{ fmtPrice(activeBar.low) }}</b></span>
        <span class="ro-item">收 <b class="num" :class="dir(activePct)">{{ fmtPrice(activeBar.close) }}</b></span>
        <span class="ro-item">涨跌 <b class="num" :class="dir(activePct)">{{ fmtPct(activePct) }}</b></span>
        <span class="ro-item">振幅 <b class="num">{{ fmtPct(activeAmplitude) }}</b></span>
        <span class="ro-item">量 <b class="num">{{ fmtVolume(activeBar.volume) }}</b></span>
        <span class="ro-item">额 <b class="num">{{ fmtAmount(activeBar.amount) }}</b></span>
        <span v-if="showMa" class="ro-sep" />
        <span v-for="m in maReadout" v-show="showMa" :key="m.label" class="ro-ma" :style="{ color: m.color }">
          {{ m.label }} <b class="num">{{ m.value === null ? '—' : fmtPrice(m.value) }}</b>
        </span>
        <span v-if="showMacd && macdReadout" class="ro-sep" />
        <span v-if="showMacd && macdReadout" class="ro-ma" :style="{ color: CHART.ma5 }">
          DIF <b class="num">{{ fmtPrice(macdReadout.dif) }}</b>
        </span>
        <span v-if="showMacd && macdReadout" class="ro-ma" :style="{ color: CHART.info }">
          DEA <b class="num">{{ fmtPrice(macdReadout.dea) }}</b>
        </span>
        <span v-if="showMacd && macdReadout" class="ro-ma" :style="{ color: CHART.text2 }">
          MACD <b class="num">{{ fmtPrice(macdReadout.hist) }}</b>
        </span>
      </template>
      <span v-else class="tiny muted">暂无数据</span>
    </div>

    <div v-if="dataWarning && !warningClosed" class="kl-warn">
      <span class="hint-warn grow">{{ dataWarning }}</span>
      <button class="btn btn-sm btn-ghost" type="button" aria-label="关闭复权口径提示" @click="warningClosed = true">×</button>
    </div>

    <div class="kl-body">
      <div ref="wrapEl" class="kl-wrap" tabindex="0" role="application" aria-label="K 线图，可用方向键平移、加减号缩放" @keydown="onKey">
        <canvas
          ref="canvasEl"
          class="kl-canvas"
          :class="{ grabbing: dragging }"
          role="img"
          :aria-label="activeName + ' ' + periodLabel + ' K 线'"
          @pointerdown="onPointerDown"
          @pointermove="onPointerMove"
          @pointerup="onPointerUp"
          @pointercancel="onPointerUp"
          @pointerleave="onPointerLeave"
          @wheel.prevent="onWheel"
          @dblclick="onDoubleClick"
        />
        <div v-if="loading && bars.length === 0" class="kl-overlay">
          <div class="kl-skeleton">
            <span class="skeleton sk-line" />
            <span class="skeleton sk-block" />
            <span class="skeleton sk-line short" />
          </div>
        </div>
        <div v-else-if="error" class="kl-overlay">
          <StateBlock kind="error" title="K 线读取失败" :message="error" action-text="重试" @action="load()" />
        </div>
        <div v-else-if="bars.length === 0" class="kl-overlay">
          <StateBlock
            kind="empty"
            title="没有 K 线数据"
            message="确认代码是否正确；停牌或新股可能没有足够历史数据。也可以点上方自选快捷条切换标的。"
            action-text="重新读取"
            @action="load()"
          />
        </div>
        <div v-if="loading && bars.length > 0" class="kl-busy">
          <span class="spinner" aria-hidden="true" />
          <span class="tiny">载入 {{ activeName }}…</span>
        </div>
      </div>

      <aside v-if="asideVisible" class="kl-aside">
        <div class="ka-hd">
          <span class="ka-title">研判</span>
          <span v-if="analyze?.warning" class="tag tag-accent">{{ analyze.warning }}</span>
          <span class="spacer" />
          <button class="btn btn-sm btn-ghost" type="button" aria-label="收起研判" @click="toggleAside">×</button>
        </div>
        <template v-if="analyze">
          <div v-if="horizonList.length > 0" class="seg ka-seg">
            <button
              v-for="h in horizonList"
              :key="h.key"
              class="seg-item"
              :class="{ on: activeHorizon?.key === h.key }"
              type="button"
              @click="horizonKey = h.key"
            >
              {{ horizonLabel(h) }}
            </button>
          </div>

          <template v-if="activeHorizon">
            <div class="ka-hd2">
              <span class="ka-bias" :class="biasClass(activeHorizon.bias)">{{ activeHorizon.bias }}</span>
              <span class="tiny muted">{{ activeHorizon.title }} · 参与计算 {{ activeHorizon.window }} 根</span>
            </div>
            <div class="ka-trend">{{ activeHorizon.summary.trend }}</div>
            <div class="ka-row"><span class="ka-k">均线</span><span class="ka-v">{{ activeHorizon.summary.ma }}</span></div>
            <div class="ka-row"><span class="ka-k">RSI</span><span class="ka-v">{{ activeHorizon.summary.rsi }}</span></div>
            <div class="ka-row"><span class="ka-k">MACD</span><span class="ka-v">{{ activeHorizon.summary.macd }}</span></div>
            <div class="ka-row"><span class="ka-k">ATR</span><span class="ka-v">{{ activeHorizon.summary.atr }}</span></div>

            <div class="ka-section">形态</div>
            <div v-if="activeHorizon.patterns.length === 0" class="tiny muted">未识别到明确形态</div>
            <div v-else class="ka-patterns">
              <span v-for="pt in activeHorizon.patterns" :key="pt.name + pt.index" class="tag" :title="pt.note || ''">
                {{ pt.name }} · {{ (pt.confidence * 100).toFixed(0) }}%
              </span>
            </div>

            <div class="ka-section">关键价位</div>
            <div class="ka-row">
              <span class="ka-k">支撑</span>
              <span class="ka-v num down">{{ activeHorizon.support.slice(0, 2).map((p) => fmtPrice(p)).join(' / ') || '—' }}</span>
            </div>
            <div class="ka-row">
              <span class="ka-k">阻力</span>
              <span class="ka-v num up">{{ activeHorizon.resistance.slice(0, 2).map((p) => fmtPrice(p)).join(' / ') || '—' }}</span>
            </div>
            <div class="ka-note">判读由 kline.analyze 生成，只描述结构，不构成买卖建议。</div>
          </template>

          <!-- 旧版本后端没有 horizons：回退到原来的单一摘要展示 -->
          <template v-else>
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
        </template>
        <div v-else-if="analyzeError" class="ka-note">{{ analyzeError }}</div>
        <div v-else class="tiny muted">判读读取中…</div>
      </aside>
    </div>

    <div class="kl-foot">
      <span class="tiny muted">
        {{ viewText }}<template v-if="bars.length > 0"> · {{ adjustLabel }}</template>
      </span>
      <span class="spacer" />
      <span class="tiny muted">滚轮缩放 · 拖拽平移 · 双击复位 · <span class="kbd">←</span><span class="kbd">→</span> 平移 · <span class="kbd">+</span><span class="kbd">-</span> 缩放</span>
    </div>
  </section>
</template>

<style scoped>
  .kl {
    min-height: 0;
  }
  .kl.max {
    position: fixed;
    inset: 10px;
    z-index: 80;
    box-shadow: var(--shadow-3);
  }
  .kl-chips {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    border-bottom: 1px solid var(--line);
    overflow-x: auto;
    scrollbar-width: none;
  }
  .kl-chips::-webkit-scrollbar {
    display: none;
  }
  .kl-chips-label {
    flex: 0 0 auto;
    margin-right: 2px;
  }
  .kl-readout {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px 11px;
    min-height: 27px;
    padding: 4px 12px;
    border-bottom: 1px solid var(--line);
    color: var(--muted);
    font-size: 11.5px;
  }
  .ro-date {
    color: var(--text);
    font-size: 11.5px;
  }
  .ro-item b {
    color: var(--text-2);
    font-weight: 500;
  }
  .ro-sep {
    width: 1px;
    height: 12px;
    background: var(--line-2);
  }
  .ro-ma {
    font-size: 11px;
  }
  .ro-ma b {
    color: inherit;
    font-weight: 600;
  }
  .kl-warn {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 0 0 auto;
    padding: 4px 12px;
    border-bottom: 1px solid rgba(233, 180, 76, 0.28);
    background: rgba(233, 180, 76, 0.07);
  }
  .kl-warn .hint-warn {
    line-height: 1.5;
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
    min-height: 300px;
    overflow: hidden;
    outline: none;
  }
  .kl-wrap:focus-visible {
    box-shadow: inset 0 0 0 1px rgba(233, 180, 76, 0.4);
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
    background: rgba(11, 17, 25, 0.9);
  }
  .kl-skeleton {
    display: flex;
    flex-direction: column;
    gap: 10px;
    width: min(560px, 70%);
  }
  .sk-line {
    height: 10px;
    width: 40%;
  }
  .sk-line.short {
    width: 26%;
  }
  .sk-block {
    height: 160px;
    border-radius: var(--radius-sm);
  }
  .kl-busy {
    position: absolute;
    top: 8px;
    left: 10px;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 3px 9px;
    border: 1px solid var(--line-2);
    border-radius: 999px;
    background: rgba(11, 17, 25, 0.86);
    color: var(--text-2);
  }
  .kl-aside {
    flex: 0 0 300px;
    width: 300px;
    overflow-y: auto;
    padding: 8px 12px 12px;
    border-left: 1px solid var(--line);
    background: linear-gradient(180deg, rgba(255, 255, 255, 0.015), transparent 140px), var(--panel-2);
  }
  .ka-hd {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 8px;
  }
  .ka-title {
    font-size: 11px;
    letter-spacing: 1px;
    color: var(--muted);
  }
  .ka-seg {
    margin-bottom: 8px;
  }
  .ka-hd2 {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 8px;
  }
  .ka-bias {
    display: inline-flex;
    align-items: center;
    padding: 2px 9px;
    border: 1px solid var(--line-2);
    border-radius: 999px;
    background: var(--panel);
    color: var(--text-2);
    font-size: 11.5px;
    font-weight: 600;
  }
  .ka-bias.bull {
    border-color: rgba(255, 90, 95, 0.4);
    background: rgba(255, 90, 95, 0.1);
    color: var(--up);
  }
  .ka-bias.bear {
    border-color: rgba(34, 192, 138, 0.4);
    background: rgba(34, 192, 138, 0.1);
    color: var(--down);
  }
  .ka-section {
    margin: 12px 0 5px;
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.8px;
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
  .kl-foot {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 26px;
    padding: 0 12px;
    border-top: 1px solid var(--line);
    overflow: hidden;
  }
  @media (max-width: 1100px) {
    .kl-aside {
      flex-basis: 250px;
      width: 250px;
    }
  }
  .card-tools {
    flex-wrap: wrap;
    justify-content: flex-end;
    row-gap: 4px;
  }
</style>