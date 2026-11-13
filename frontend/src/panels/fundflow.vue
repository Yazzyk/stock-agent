<script setup lang="ts">
// 板块/概念资金流线 + 双榜：多板块区间累计净额折线（流入红、流出绿），下方是净流入/净流出排行。
// 图用 lib/chart.ts 的零依赖 canvas 实现；数据走 fundflow.sectorBatch / sectorHistory / rank。
// 只读研究：点击榜单行只影响图线，不涉及交易，也不切到个股视角。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { messageOf, sectorFlowBatch, sectorFlowHistory, type FundFlowHistory } from '../lib/api.market'
import { flowRank } from '../lib/data'
import { drawSectorFlowLines, fitCanvas, type SectorFlowLinesLayout } from '../lib/chart'
import { dir, fmtPct, fmtSignedAmount } from '../lib/format'
import type { FlowKind, FundFlow } from '../types'

const RANGES = [10, 20, 60]
const DEFAULT_TOP = 8
const RANK_LIMIT = 20
const MAX_READOUT = 6
/** 与后端 sectorBatch 的 codes 上限保持一致：超过就无法一次拉齐，图线会缺。 */
const MAX_CHART_LINES = 20

/** 流入用红色系、流出用绿色系（与 A 股涨红跌绿一致）。 */
const INFLOW_COLORS = ['#ff5a5f', '#e8534e', '#f4755e', '#ef6c4a', '#d9534f', '#fc8452', '#e74c3c', '#eb4d4b', '#f19066', '#c0392b']
const OUTFLOW_COLORS = ['#22c08a', '#3ba272', '#2ecc71', '#27ae60', '#1abc9c', '#16a085', '#45b39d', '#52be80', '#58d68d', '#0abde3']

interface FlowLine {
  code: string
  name: string
  color: string
  /** 区间累计净额（与 dates 等长；缺失为 null，不补 0） */
  values: Array<number | null>
  /** 最新一天的累计值 */
  latest: number | null
  /** 当日涨跌幅（来自榜单；缺失为 undefined） */
  changePct?: number
}

interface ReadoutRow {
  code: string
  name: string
  color: string
  value: number
  changePct?: number
}

const kind = ref<FlowKind>('sector')
const range = ref(20)
const source = ref('')

const inRows = ref<FundFlow[]>([])
const outRows = ref<FundFlow[]>([])
const inLoading = ref(false)
const outLoading = ref(false)
const inError = ref('')
const outError = ref('')

const selectedCodes = ref<string[]>([])
const hiddenCodes = ref<string[]>([])
const histories = ref<Record<string, FundFlowHistory>>({})
const chartLoading = ref(false)
const chartError = ref('')
const pickError = ref('')
const hoverIndex = ref<number | null>(null)

const canvasEl = ref<HTMLCanvasElement | null>(null)
const chartEl = ref<HTMLElement | null>(null)
const chartSize = ref({ w: 0, h: 0 })
let layout: SectorFlowLinesLayout | null = null
let ro: ResizeObserver | null = null
let raf = 0

const dates = computed<string[]>(() => {
  const set = new Set<string>()
  for (const history of Object.values(histories.value)) {
    for (const point of history.points) if (point.date) set.add(point.date)
  }
  return [...set].sort()
})

const rowByCode = computed<Map<string, FundFlow>>(() => {
  const map = new Map<string, FundFlow>()
  for (const row of inRows.value) map.set(row.code, row)
  for (const row of outRows.value) if (!map.has(row.code)) map.set(row.code, row)
  return map
})

const inCodes = computed(() => new Set(inRows.value.map((row) => row.code)))
const outCodes = computed(() => new Set(outRows.value.map((row) => row.code)))

const lines = computed<FlowLine[]>(() => {
  const axis = dates.value
  let inflowIdx = 0
  let outflowIdx = 0
  const out: FlowLine[] = []
  for (const code of selectedCodes.value) {
    const history = histories.value[code]
    if (!history) continue
    const byDate = new Map<string, number>()
    for (const point of history.points) byDate.set(point.date, point.mainNet)
    const values: Array<number | null> = []
    let acc = 0
    let started = false
    for (const date of axis) {
      const day = byDate.get(date)
      if (typeof day === 'number') {
        acc += day
        started = true
        values.push(acc)
      } else {
        // 缺失日期沿用前值：停牌/缺行不该让累计曲线回落
        values.push(started ? acc : null)
      }
    }
    let latest: number | null = null
    for (let i = values.length - 1; i >= 0; i--) {
      const value = values[i]
      if (typeof value === 'number') {
        latest = value
        break
      }
    }
    const row = rowByCode.value.get(code)
    // 后端降级时 history.name 可能是代码（如 new_dzxx）；榜单名称优先，其次才用历史名称。
    const rowName = (row?.name ?? '').trim()
    const histName = (history.name ?? '').trim()
    const name = rowName || (histName !== '' && histName !== code ? histName : '') || code
    const inflow = inCodes.value.has(code) || (!outCodes.value.has(code) && (latest ?? 0) >= 0)
    const color = inflow
      ? INFLOW_COLORS[inflowIdx++ % INFLOW_COLORS.length]
      : OUTFLOW_COLORS[outflowIdx++ % OUTFLOW_COLORS.length]
    out.push({ code, name, color, values, latest, changePct: row?.changePct })
  }
  return out
})

const visibleLines = computed(() => lines.value.filter((line) => !hiddenCodes.value.includes(line.code)))

const covered = computed(() => lines.value.length)
const inflowTotal = computed(() => lines.value.reduce((sum, line) => sum + Math.max(0, line.latest ?? 0), 0))
const outflowTotal = computed(() => lines.value.reduce((sum, line) => sum + Math.min(0, line.latest ?? 0), 0))
/** 区间累计净流入/净流出最大的两条线，用于顶部 TOP 摘要（带当日涨跌幅）。 */
const topInflow = computed<FlowLine | null>(() => {
  const list = lines.value.filter((line) => (line.latest ?? 0) > 0)
  return list.length === 0 ? null : list.reduce((best, line) => ((line.latest ?? 0) > (best.latest ?? 0) ? line : best))
})
const topOutflow = computed<FlowLine | null>(() => {
  const list = lines.value.filter((line) => (line.latest ?? 0) < 0)
  return list.length === 0 ? null : list.reduce((best, line) => ((line.latest ?? 0) < (best.latest ?? 0) ? line : best))
})

const hoverDate = computed(() => {
  const i = hoverIndex.value
  if (i === null) return ''
  return dates.value[i] ?? ''
})

const readoutRows = computed<ReadoutRow[]>(() => {
  const i = hoverIndex.value
  if (i === null) return []
  const rows: ReadoutRow[] = []
  for (const line of visibleLines.value) {
    const value = line.values[i]
    if (typeof value !== 'number') continue
    rows.push({ code: line.code, name: line.name, color: line.color, value, changePct: line.changePct })
  }
  return rows.sort((a, b) => b.value - a.value)
})

const readoutMore = computed(() => Math.max(0, readoutRows.value.length - MAX_READOUT))

const hasRankPct = computed(() => inRows.value.some(hasMainPct) || outRows.value.some(hasMainPct))

/** 数据源口径提示：新浪榜单的净额是「全部资金净流入」，与主力净额口径不同。 */
const sourceNote = computed(() => {
  const rows = [...inRows.value, ...outRows.value]
  if (rows.some((row) => (row.source ?? '').includes('sina'))) {
    return '新浪口径：净额 = 全部资金净流入（非主力），涨跌幅与占比可用'
  }
  return ''
})

/** 后端没给涨跌幅的行（降级源）不能把 0 当成真实值展示。 */
function isDegradedRow(row: FundFlow): boolean {
  const v = (row as { changePct?: number }).changePct
  return typeof v !== 'number' || !Number.isFinite(v)
}

/** 判断某行是否带净额占比（后端未提供占比字段时省略该列）。 */
function hasMainPct(row: FundFlow): boolean {
  if (isDegradedRow(row)) return false
  return typeof row.mainPct === 'number' && Number.isFinite(row.mainPct)
}

/** 涨跌幅文案：降级源没有该字段时显示 —。 */
function changeText(row: FundFlow): string {
  return isDegradedRow(row) ? '—' : fmtPct(row.changePct)
}

/** 默认图线：净流入 TOP8 + 净流出 TOP8，按代码去重并保持榜单顺序。 */
function defaultCodes(): string[] {
  const ins = inRows.value.filter((row) => row.code !== '').slice(0, DEFAULT_TOP)
  const outs = outRows.value.filter((row) => row.code !== '').slice(0, DEFAULT_TOP)
  return [...new Set([...ins, ...outs].map((row) => row.code))]
}

// ---------- 榜单 ----------

/** 按方向加载单侧资金榜：两榜各自维护 loading/error，一侧失败不影响另一侧。 */
async function loadRankSide(direction: 'in' | 'out'): Promise<void> {
  const rows = direction === 'in' ? inRows : outRows
  const busy = direction === 'in' ? inLoading : outLoading
  const failure = direction === 'in' ? inError : outError
  busy.value = true
  failure.value = ''
  try {
    rows.value = await flowRank(kind.value, RANK_LIMIT, direction)
  } catch (e) {
    rows.value = []
    failure.value = messageOf(e)
  } finally {
    busy.value = false
  }
}

/** 并行刷新净流入与净流出两榜。 */
async function loadRank(): Promise<void> {
  await Promise.all([loadRankSide('in'), loadRankSide('out')])
}

/** 只刷新某一侧榜单。 */
function refreshRank(direction: 'in' | 'out'): void {
  void loadRankSide(direction)
}

// ---------- 图线 ----------

/** 批量拉取当前选中板块的多日序列；缺项时保留空位，由空态提示。 */
async function loadChart(): Promise<void> {
  const codes = selectedCodes.value
  pickError.value = ''
  if (codes.length === 0) {
    histories.value = {}
    chartError.value = ''
    chartLoading.value = false
    scheduleDraw()
    return
  }
  chartLoading.value = true
  chartError.value = ''
  try {
    const items = await sectorFlowBatch(codes, range.value)
    const next: Record<string, FundFlowHistory> = {}
    for (const item of items) next[item.code] = item
    histories.value = next
    if (items[0]?.source) source.value = items[0].source
  } catch (e) {
    histories.value = {}
    chartError.value = messageOf(e)
  } finally {
    chartLoading.value = false
    await nextTick()
    scheduleDraw()
  }
}

/** 首次加载与顶部刷新：先刷新双榜，再用默认 TOP8+8 拉图线。 */
async function loadAll(): Promise<void> {
  await loadRank()
  if (selectedCodes.value.length === 0) selectedCodes.value = defaultCodes()
  await loadChart()
}

/** 榜单行点击：把该板块加入/移出图线；加入时按需拉取它的多日序列。 */
async function toggleSector(row: FundFlow): Promise<void> {
  const code = row.code
  if (!code) return
  pickError.value = ''
  if (selectedCodes.value.includes(code)) {
    selectedCodes.value = selectedCodes.value.filter((item) => item !== code)
    hiddenCodes.value = hiddenCodes.value.filter((item) => item !== code)
    scheduleDraw()
    return
  }
  if (selectedCodes.value.length >= MAX_CHART_LINES) {
    pickError.value = '最多同时对比 ' + MAX_CHART_LINES + ' 个板块，请先点已选行移出或重置图表'
    return
  }
  selectedCodes.value = [...selectedCodes.value, code]
  if (histories.value[code]) {
    scheduleDraw()
    return
  }
  chartLoading.value = true
  try {
    const history = await sectorFlowHistory(code, range.value)
    histories.value = { ...histories.value, [code]: history }
  } catch (e) {
    selectedCodes.value = selectedCodes.value.filter((item) => item !== code)
    pickError.value = '加入「' + (row.name || code) + '」失败：' + messageOf(e)
  } finally {
    chartLoading.value = false
    await nextTick()
    scheduleDraw()
  }
}

/** 图例点击：隐藏/显示单条图线，颜色与其余图线都不变。 */
function toggleLine(code: string): void {
  hiddenCodes.value = hiddenCodes.value.includes(code)
    ? hiddenCodes.value.filter((item) => item !== code)
    : [...hiddenCodes.value, code]
}

/** 重置图表：恢复默认 TOP8+8、显示全部图例并重新拉数。 */
async function resetChart(): Promise<void> {
  hiddenCodes.value = []
  hoverIndex.value = null
  selectedCodes.value = defaultCodes()
  histories.value = {}
  await loadChart()
}

/** 切换板块/概念：清空图线选择与缓存，重新加载双榜和默认图线。 */
function setKind(next: FlowKind): void {
  if (kind.value === next) return
  kind.value = next
  selectedCodes.value = []
  hiddenCodes.value = []
  histories.value = {}
  hoverIndex.value = null
  void loadAll()
}

/** 切换区间：按新天数重新拉取已选板块的序列。 */
function setRange(next: number): void {
  if (range.value === next) return
  range.value = next
  hoverIndex.value = null
  void loadChart()
}

// ---------- 绘制 ----------

/** 合并同一帧内的重绘请求，避免悬停时反复打满主线程。 */
function scheduleDraw(): void {
  if (raf !== 0) return
  raf = window.requestAnimationFrame(() => {
    raf = 0
    draw()
  })
}

/** 用当前日期轴与可见图线重绘 canvas，并保留用于指针映射的布局。 */
function draw(): void {
  const canvas = canvasEl.value
  if (!canvas) return
  const w = chartSize.value.w
  const h = chartSize.value.h
  const ctx = fitCanvas(canvas, w, h)
  if (!ctx) {
    layout = null
    return
  }
  layout = drawSectorFlowLines(
    ctx,
    dates.value,
    visibleLines.value.map((line) => ({ name: line.name, color: line.color, values: line.values, label: line.name })),
    { width: w, height: h, hoverIndex: hoverIndex.value },
  )
}

/** 量一次容器尺寸；尺寸变化时更新并触发重绘。 */
function measure(): void {
  const el = chartEl.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height))
  if (w !== chartSize.value.w || h !== chartSize.value.h) {
    chartSize.value = { w, h }
  }
  scheduleDraw()
}

/** 挂上 ResizeObserver，容器宽度变化时自适应。 */
function attachChart(): void {
  const el = chartEl.value
  if (!el) return
  if (ro) ro.disconnect()
  ro = new ResizeObserver(() => measure())
  ro.observe(el)
  measure()
}

/** 指针移动：按 x 坐标映射到最近的交易日，画出十字线与读数。 */
function onPointerMove(e: PointerEvent): void {
  const canvas = canvasEl.value
  if (!canvas || !layout || layout.count === 0) return
  const rect = canvas.getBoundingClientRect()
  hoverIndex.value = layout.indexAt(e.clientX - rect.left)
  scheduleDraw()
}

/** 指针离开：清空悬停状态。 */
function onPointerLeave(): void {
  hoverIndex.value = null
  scheduleDraw()
}

watch([lines, hiddenCodes, hoverIndex], scheduleDraw)

onMounted(async () => {
  await nextTick()
  attachChart()
  void loadAll()
})

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (raf !== 0) window.cancelAnimationFrame(raf)
})
</script>

<template>
  <section class="card ff">
    <div class="card-hd">
      <span class="card-title">资金流向</span>
      <span class="card-sub">板块/概念 · 净额 = 全部资金净流入（非主力）· 单位自动换算 万/亿</span>
      <span class="card-tools">
        <span v-if="source" class="tag">{{ source }}</span>
        <button class="btn btn-sm" type="button" :disabled="chartLoading || inLoading || outLoading" @click="loadAll">刷新</button>
      </span>
    </div>

    <div class="ff-body">
      <!-- 1. 板块/概念资金流线 -->
      <section class="panel ff-chart-panel">
        <div class="panel-hd">
          <span class="panel-title">资金流线</span>
          <span class="card-sub">区间累计净额 · 缺失日沿用前值</span>
          <span class="panel-tools">
            <span class="seg">
              <button class="seg-item" :class="{ on: kind === 'sector' }" type="button" @click="setKind('sector')">板块</button>
              <button class="seg-item" :class="{ on: kind === 'concept' }" type="button" @click="setKind('concept')">概念</button>
            </span>
            <span class="seg">
              <button
                v-for="r in RANGES"
                :key="r"
                class="seg-item"
                :class="{ on: range === r }"
                type="button"
                @click="setRange(r)"
              >
                {{ r }}日
              </button>
            </span>
            <button class="btn btn-sm" type="button" :disabled="chartLoading" @click="resetChart">重置图表</button>
          </span>
        </div>

        <div class="panel-bd ff-chart-bd">
          <div class="ff-meta">
            <span>覆盖板块 <b class="num">{{ covered }}</b></span>
            <span>区间净流入合计 <b class="num" :class="dir(inflowTotal)">{{ fmtSignedAmount(inflowTotal) }}</b></span>
            <span>区间净流出合计 <b class="num" :class="dir(outflowTotal)">{{ fmtSignedAmount(outflowTotal) }}</b></span>
            <span v-if="topInflow">流入 TOP <b>{{ topInflow.name }}</b> <b class="num" :class="dir(topInflow.changePct)">{{ fmtPct(topInflow.changePct) }}</b></span>
            <span v-if="topOutflow">流出 TOP <b>{{ topOutflow.name }}</b> <b class="num" :class="dir(topOutflow.changePct)">{{ fmtPct(topOutflow.changePct) }}</b></span>
            <span class="spacer" />
            <span v-if="sourceNote" class="hint-warn">{{ sourceNote }}</span>
            <span class="tiny muted">{{ range }} 日 · {{ dates.length }} 个交易日</span>
          </div>

          <div class="ff-chart-wrap">
            <div ref="chartEl" class="ff-chart">
              <canvas
                ref="canvasEl"
                class="ff-canvas"
                role="img"
                :aria-label="'板块资金流区间累计曲线，共 ' + visibleLines.length + ' 条'"
                @pointermove="onPointerMove"
                @pointerleave="onPointerLeave"
              />
              <div v-if="chartLoading && lines.length === 0" class="ff-chart-state">
                <span class="skeleton ff-sk" />
              </div>
              <div v-else-if="chartError" class="ff-chart-state">
                <StateBlock kind="error" title="板块资金流读取失败" :message="chartError" action-text="重试" @action="loadChart" />
              </div>
              <div v-else-if="lines.length === 0" class="ff-chart-state">
                <StateBlock
                  kind="empty"
                  title="暂无板块资金流"
                  message="数据源未返回所选板块的多日资金流；点「刷新」重试，或先看下方榜单。"
                />
              </div>
            </div>

            <div class="ff-legend">
              <button
                v-for="line in lines"
                :key="line.code"
                class="lg-item"
                type="button"
                :aria-pressed="!hiddenCodes.includes(line.code)"
                :title="hiddenCodes.includes(line.code) ? '点击显示该线' : '点击隐藏该线'"
                @click="toggleLine(line.code)"
              >
                <span
                  class="lg-bar"
                  :style="{ background: line.color, opacity: hiddenCodes.includes(line.code) ? 0.3 : 1 }"
                  aria-hidden="true"
                />
                <span class="lg-name">{{ line.name }}</span>
                <span v-if="line.changePct !== undefined" class="num lg-chg" :class="dir(line.changePct)">{{ fmtPct(line.changePct) }}</span>
                <b class="num" :class="hiddenCodes.includes(line.code) ? 'muted' : dir(line.latest)">{{ fmtSignedAmount(line.latest) }}</b>
              </button>
              <span v-if="lines.length === 0" class="tiny muted">暂无图线</span>
              <span v-if="pickError" class="ro-err">{{ pickError }}</span>
            </div>

            <div class="ff-readout">
              <template v-if="hoverDate">
                <span class="ro-date">{{ hoverDate }}</span>
                <span v-for="row in readoutRows.slice(0, MAX_READOUT)" :key="row.code" class="ro-item">
                  <span class="lg-bar" :style="{ background: row.color }" aria-hidden="true" />
                  {{ row.name }}
                  <span v-if="row.changePct !== undefined" class="num" :class="dir(row.changePct)">{{ fmtPct(row.changePct) }}</span>
                  <b class="num" :class="dir(row.value)">{{ fmtSignedAmount(row.value) }}</b>
                </span>
                <span v-if="readoutRows.length === 0" class="tiny muted">当日无数据</span>
                <span v-if="readoutMore > 0" class="tiny muted">其他 {{ readoutMore }} 条</span>
              </template>
              <span v-else class="tiny muted">悬停查看当日读数 · 点图例隐藏/显示 · 点榜单行加入或移出图线</span>
            </div>
          </div>
        </div>
      </section>

      <!-- 2. 板块 / 概念排名：净流入榜与净流出榜并排 -->
      <section class="panel ff-rank">
        <div class="panel-hd">
          <span class="panel-title">资金排名</span>
          <span class="card-sub">净额口径（全部资金）· 元 · 点击行加入/移出图线</span>
          <span class="panel-tools">
            <button class="btn btn-sm" type="button" :disabled="inLoading && outLoading" @click="loadRank">全部刷新</button>
          </span>
        </div>
        <div class="panel-bd flush ff-rank-bd">
          <div class="ff-rank-grid">
            <div class="ff-rank-col">
              <div class="ff-rank-head">
                <span class="ff-rank-name"><span class="ff-dot up" aria-hidden="true" />净流入 TOP</span>
                <button class="btn btn-sm" type="button" :disabled="inLoading" @click="refreshRank('in')">刷新</button>
              </div>
              <StateBlock v-if="inLoading && inRows.length === 0" kind="loading" title="读取净流入榜…" />
              <StateBlock
                v-else-if="inError"
                kind="error"
                title="净流入榜读取失败"
                :message="inError"
                action-text="重试"
                @action="refreshRank('in')"
              />
              <StateBlock
                v-else-if="inRows.length === 0"
                kind="empty"
                title="暂无净流入数据"
                message="数据源未返回板块资金流，稍后点「刷新」重试。"
                action-text="刷新"
                @action="refreshRank('in')"
              />
              <div v-else class="ff-rank-scroll scroll-x">
                <table class="tbl">
                  <thead>
                    <tr>
                      <th class="l" style="width: 44px">排名</th>
                      <th class="l">名称</th>
                      <th>涨跌幅</th>
                      <th>净额</th>
                      <th v-if="hasRankPct">占比</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr
                      v-for="(row, i) in inRows"
                      :key="row.code + row.name"
                      :class="{ sel: selectedCodes.includes(row.code) }"
                      :title="selectedCodes.includes(row.code) ? '点击移出图线' : '点击加入图线'"
                      @click="toggleSector(row)"
                    >
                      <td class="l num muted">{{ row.rank ?? i + 1 }}</td>
                      <td class="l name">{{ row.name }}</td>
                      <td class="num" :class="isDegradedRow(row) ? 'muted' : dir(row.changePct)">{{ changeText(row) }}</td>
                      <td class="num" :class="dir(row.mainNet)">{{ fmtSignedAmount(row.mainNet) }}</td>
                      <td v-if="hasRankPct" class="num" :class="dir(row.mainPct)">{{ fmtPct(row.mainPct) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>

            <div class="ff-rank-col">
              <div class="ff-rank-head">
                <span class="ff-rank-name"><span class="ff-dot down" aria-hidden="true" />净流出 TOP</span>
                <button class="btn btn-sm" type="button" :disabled="outLoading" @click="refreshRank('out')">刷新</button>
              </div>
              <StateBlock v-if="outLoading && outRows.length === 0" kind="loading" title="读取净流出榜…" />
              <StateBlock
                v-else-if="outError"
                kind="error"
                title="净流出榜读取失败"
                :message="outError"
                action-text="重试"
                @action="refreshRank('out')"
              />
              <StateBlock
                v-else-if="outRows.length === 0"
                kind="empty"
                title="暂无净流出数据"
                message="数据源未返回板块资金流，稍后点「刷新」重试。"
                action-text="刷新"
                @action="refreshRank('out')"
              />
              <div v-else class="ff-rank-scroll scroll-x">
                <table class="tbl">
                  <thead>
                    <tr>
                      <th class="l" style="width: 44px">排名</th>
                      <th class="l">名称</th>
                      <th>涨跌幅</th>
                      <th>净额</th>
                      <th v-if="hasRankPct">占比</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr
                      v-for="(row, i) in outRows"
                      :key="row.code + row.name"
                      :class="{ sel: selectedCodes.includes(row.code) }"
                      :title="selectedCodes.includes(row.code) ? '点击移出图线' : '点击加入图线'"
                      @click="toggleSector(row)"
                    >
                      <td class="l num muted">{{ row.rank ?? i + 1 }}</td>
                      <td class="l name">{{ row.name }}</td>
                      <td class="num" :class="isDegradedRow(row) ? 'muted' : dir(row.changePct)">{{ changeText(row) }}</td>
                      <td class="num" :class="dir(row.mainNet)">{{ fmtSignedAmount(row.mainNet) }}</td>
                      <td v-if="hasRankPct" class="num" :class="dir(row.mainPct)">{{ fmtPct(row.mainPct) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>

    <div class="ff-foot">
      <span class="tiny muted">图线 {{ lines.length }} 条 · 可见 {{ visibleLines.length }} 条</span>
      <span class="spacer" />
      <span class="tiny muted">净流入榜按净额降序，净流出榜按净额升序；重置图表恢复 TOP8+8</span>
    </div>
  </section>
</template>

<style scoped>
  .ff {
    min-height: 0;
  }
  .ff-body {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 8px 12px 10px;
  }
  /* 1280×800 首屏要放得下图 + 至少 5 行排名：卡片头部收窄 */
  .ff-chart-panel > .panel-hd,
  .ff-rank > .panel-hd {
    min-height: 38px;
    flex: 0 0 auto;
  }
  /* 关键：面板纵向堆叠时不许被压扁，滚动交给 .ff-body */
  .ff-body > * {
    flex: 0 0 auto;
  }
  .ff-chart-bd {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px 12px;
  }
  .ff-chart-bd > * {
    flex: 0 0 auto;
  }
  .ff-meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 2px 14px;
    color: var(--muted);
    font-size: 11.5px;
  }
  .ff-meta b {
    color: var(--text-2);
    font-weight: 500;
    margin-left: 2px;
  }
  .ff-chart-wrap {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 0 0 auto;
  }
  .ff-chart {
    position: relative;
    flex: 0 0 auto;
    height: 240px;
    min-height: 240px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg);
    overflow: hidden;
  }
  .ff-canvas {
    display: block;
    cursor: crosshair;
  }
  .ff-chart-state {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(11, 17, 25, 0.9);
  }
  .ff-sk {
    width: 80%;
    height: 70%;
  }
  .ff-legend {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 2px 14px;
    min-height: 20px;
    color: var(--muted);
    font-size: 11px;
  }
  .lg-item {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-2);
    font-size: 11px;
  }
  .lg-item:hover {
    color: var(--text);
  }
  .lg-bar {
    width: 9px;
    height: 9px;
    border-radius: 2px;
    flex: 0 0 auto;
  }
  .lg-chg {
    font-size: 10.5px;
  }
  .lg-name {
    max-width: 96px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .ff-readout {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 2px 12px;
    min-height: 18px;
    color: var(--muted);
    font-size: 11px;
  }
  .ro-date {
    color: var(--text-2);
    font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  }
  .ro-item {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .ro-item b {
    color: var(--text);
    font-weight: 500;
  }
  .ro-err {
    color: var(--down);
  }
  .ff-rank-bd {
    max-height: 320px;
    display: flex;
    min-height: 0;
    overflow: hidden;
  }
  .ff-rank-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    width: 100%;
    min-height: 0;
  }
  .ff-rank-col {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .ff-rank-col + .ff-rank-col {
    border-left: 1px solid var(--line);
  }
  .ff-rank-head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 32px;
    padding: 4px 12px;
    border-bottom: 1px solid var(--line);
    background: var(--bg-2);
  }
  .ff-rank-name {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    font-size: 11.5px;
    font-weight: 500;
  }
  .ff-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--flat);
  }
  .ff-dot.up {
    background: var(--up);
  }
  .ff-dot.down {
    background: var(--down);
  }
  /* 排名行更紧凑，两榜并排时首屏至少露出 5 行 */
  .ff-rank-scroll {
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
  }
  .ff-rank-scroll .tbl {
    min-width: 360px;
  }
  .ff-rank-scroll .tbl thead th {
    padding-top: 6px;
    padding-bottom: 6px;
  }
  .ff-rank-scroll .tbl tbody td {
    padding-top: 5px;
    padding-bottom: 5px;
  }
  .ff-rank-scroll .tbl tbody tr {
    cursor: pointer;
  }
  .ff-rank-scroll .tbl tbody tr:hover td.name {
    text-decoration: underline;
  }
  .ff-foot {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 26px;
    padding: 0 12px;
    border-top: 1px solid var(--line);
  }
  @media (max-width: 1180px) {
    /* 窄屏改为上下两榜，表格宽度不够时横向滚动 */
    .ff-rank-grid {
      grid-template-columns: minmax(0, 1fr);
    }
    .ff-rank-col + .ff-rank-col {
      border-left: 0;
      border-top: 1px solid var(--line);
    }
    .ff-rank-bd {
      max-height: none;
    }
  }
  .card-tools {
    flex-wrap: wrap;
    justify-content: flex-end;
    row-gap: 4px;
  }
  .panel-tools {
    flex-wrap: wrap;
    justify-content: flex-end;
    row-gap: 4px;
  }
</style>
