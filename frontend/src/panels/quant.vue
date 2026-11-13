<script setup lang="ts">
// 量化研究：标的 + 策略参数 → 回测 → 指标卡 + 标的 K 线（含买卖标记）+ 净值曲线 + 报告。
// 图表区固定上下两段：容器各自持有固定高度，图例放在 canvas 之外的正常流里，
// 避免「图例高度和画布高度互相耦合」；ResizeObserver 覆盖两个容器，结果区晚挂载也没问题。
// 只读研究：输出的是可复现的回测结论，不含任何交易指令。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import StockPicker from '../components/StockPicker.vue'
import StateBlock from '../components/StateBlock.vue'
import { klineBars, messageOf as marketMessage, quantRun, tradeMarkersOf, type TradeMarker } from '../lib/api.market'
import { CandleChart, drawEquityChart, fitCanvas, type CandleMarker } from '../lib/chart'
import { agentRun, quantOptimize, quantStrategiesPayload, quantWalkForward } from '../lib/data'
import { dir, fmtAmount, fmtNum, fmtPct, fmtPrice } from '../lib/format'
import { mdToHtml } from '../lib/markdown'
import { rec } from '../lib/parse'
import { friendlyError } from '../api'
import { state, toast } from '../stores/app'
import { agentState, beginRun, endRun } from '../stores/agent'
import type {
  Bar,
  EquityPoint,
  KlineIndicators,
  OptimizeResult,
  Period,
  QuantRunResponse,
  StrategySpec,
  WalkForwardResponse,
} from '../types'

const PERIODS = [
  { key: 'day', label: '日线' },
  { key: 'week', label: '周线' },
  { key: 'month', label: '月线' },
]

const strategies = ref<StrategySpec[]>([])
const strategyId = ref('ma')
const params = reactive<Record<string, number>>({})
const codeInput = ref('600519')
const code = ref('600519')
const stockName = ref('')
const period = ref('day')
const initialCash = ref(100000)
const folds = ref(3)
const loading = ref(false)
const wfLoading = ref(false)
const error = ref('')
const wfError = ref('')
const run = ref<QuantRunResponse | null>(null)
const wf = ref<WalkForwardResponse | null>(null)
const tab = ref<'report' | 'walkforward' | 'optimize'>('report')
const ready = ref(false)
// 算法说明：strategies 方法一并返回的全局口径
const engineRules = ref<string[]>([])
const algoOpen = ref(false)
// 参数优化
const optLoading = ref(false)
const optError = ref('')
const opt = ref<OptimizeResult | null>(null)
const agentBusy = ref(false)

// K 线区
const bars = ref<Bar[]>([])
const inds = ref<Partial<KlineIndicators>>({})
const markers = ref<CandleMarker[]>([])
const markerNote = ref('')
const klineCanvas = ref<HTMLCanvasElement | null>(null)
const klineWrap = ref<HTMLElement | null>(null)
const klineSize = ref({ w: 0, h: 0 })
const klineHover = ref<{ index: number; y: number } | null>(null)

// 净值区
const equityCanvas = ref<HTMLCanvasElement | null>(null)
const equityWrap = ref<HTMLElement | null>(null)
const equitySize = ref({ w: 0, h: 0 })
const equityHover = ref<number | null>(null)
const showDrawdown = ref(false)

let candle: CandleChart | null = null
let roK: ResizeObserver | null = null
let roE: ResizeObserver | null = null
let rafK = 0
let rafE = 0
let runSeq = 0

const spec = computed<StrategySpec | null>(() => strategies.value.find((s) => s.id === strategyId.value) ?? null)
const paramDefs = computed(() => spec.value?.params ?? [])
const equity = computed<EquityPoint[]>(() => run.value?.result.equity ?? [])
const result = computed(() => run.value?.result ?? null)
const noAdjust = computed(() => run.value?.adjust === 'none')
const reportHtml = computed(() => mdToHtml(run.value?.report ?? ''))
const wfReportHtml = computed(() => mdToHtml(wf.value?.report ?? ''))
const optReportHtml = computed(() => mdToHtml(opt.value?.report ?? ''))
const optRows = computed(() => opt.value?.candidates ?? [])
const bestSignature = computed(() => (opt.value?.best ? paramsText(opt.value.best.params) : ''))
const staleCode = computed(() => result.value !== null && result.value.code !== '' && result.value.code !== code.value)

const metrics = computed(() => {
  const r = result.value
  if (!r) return []
  return [
    { key: 'totalReturn', label: '总收益', value: fmtPct(r.totalReturn * 100), cls: dir(r.totalReturn) },
    { key: 'cagr', label: '年化', value: fmtPct(r.cagr * 100), cls: dir(r.cagr) },
    { key: 'mdd', label: '最大回撤', value: fmtPct(-Math.abs(r.maxDrawdown) * 100), cls: r.maxDrawdown > 0.0001 ? 'risk' : 'flat' },
    { key: 'sharpe', label: '夏普', value: fmtNum(r.sharpe, 2), cls: dir(r.sharpe) },
    { key: 'winRate', label: '胜率', value: fmtNum(r.winRate * 100, 1) + '%', cls: 'flat' },
    { key: 'trades', label: '成交笔数', value: String(markers.value.length || r.tradeCount), cls: 'flat' },
    { key: 'turnover', label: '换手', value: fmtNum(r.turnover, 2) + 'x', cls: 'flat' },
    { key: 'final', label: '期末权益', value: fmtAmount(r.finalEquity), cls: dir(r.finalEquity - r.initialCash) },
  ]
})

const costs = computed(() => {
  const c = result.value?.costs
  if (!c) return []
  return [
    { label: '佣金', value: c.commission },
    { label: '印花税', value: c.stampTax },
    { label: '过户费', value: c.transfer },
    { label: '滑点', value: c.slippage },
  ]
})

const costTotal = computed(() => costs.value.reduce((s, c) => s + c.value, 0))

const wfVerdictTone = computed(() => {
  const v = wf.value?.result.verdict ?? ''
  if (v.includes('高') || v.includes('不成立')) return 'tag-up'
  if (v.includes('中')) return 'tag-accent'
  if (v === '') return ''
  return 'tag-down'
})

const activeEquity = computed<EquityPoint | null>(() => {
  const list = equity.value
  if (list.length === 0) return null
  const idx = equityHover.value !== null ? Math.min(Math.max(equityHover.value, 0), list.length - 1) : list.length - 1
  return list[idx] ?? null
})

const activeReturn = computed(() => {
  const p = activeEquity.value
  const base = result.value?.initialCash ?? 0
  if (!p || base <= 0) return 0
  return p.equity / base - 1
})

const activeBar = computed<Bar | null>(() => {
  const i = klineHover.value?.index ?? -1
  if (i < 0) return null
  return bars.value[i] ?? null
})

const activeBarPct = computed<number | null>(() => {
  const i = klineHover.value?.index ?? -1
  const b = bars.value[i]
  if (!b) return null
  const prev = bars.value[i - 1]
  const base = prev && prev.close > 0 ? prev.close : b.open
  if (!base) return null
  return ((b.close - base) / base) * 100
})

function seedParams(): void {
  for (const k of Object.keys(params)) delete params[k]
  const def = spec.value?.default ?? {}
  for (const d of paramDefs.value) {
    const v = def[d.key]
    params[d.key] = typeof v === 'number' ? v : d.min ?? 0
  }
}

/** 读取策略清单与全局口径：quant.strategies 一次返回 strategies / rules / costs。 */
async function loadStrategies(): Promise<void> {
  try {
    const payload = await quantStrategiesPayload()
    strategies.value = payload.strategies.filter((s) => s.id !== '')
    engineRules.value = payload.rules
    if (!strategies.value.some((s) => s.id === strategyId.value)) {
      strategyId.value = strategies.value[0]?.id ?? 'ma'
    }
    seedParams()
    ready.value = true
  } catch (e) {
    error.value = friendlyError(e, '读取策略列表')
    ready.value = true
  }
}

async function submitRun(): Promise<void> {
  if (codeInput.value.length !== 6) {
    error.value = '请选择 6 位股票代码，例如 600519'
    return
  }
  code.value = codeInput.value
  const seq = ++runSeq
  loading.value = true
  error.value = ''
  try {
    const [res, barRes] = await Promise.all([
      quantRun({
        code: code.value,
        period: period.value,
        strategy: strategyId.value,
        params: { ...params },
        limit: 500,
        initialCash: Math.max(1000, Math.round(initialCash.value || 100000)),
      }),
      klineBars(code.value, period.value as Period, 320, 'qfq').catch(() => null),
    ])
    if (seq !== runSeq) return
    run.value = res
    wf.value = null
    tab.value = 'report'
    bars.value = barRes?.bars ?? []
    inds.value = barRes?.indicators ?? {}
    if (barRes?.name) stockName.value = barRes.name
    applyMarkers(tradeMarkersOf(res))
    await nextTick()
    attachCharts(true)
  } catch (e) {
    if (seq !== runSeq) return
    error.value = marketMessage(e)
    run.value = null
  } finally {
    if (seq === runSeq) loading.value = false
  }
}

/** 顶带 trades（或 result.fills）→ 蜡烛图上的买卖标记；日期对不上就不画，并说明原因。 */
function applyMarkers(fills: TradeMarker[]): void {
  if (fills.length === 0) {
    markers.value = []
    markerNote.value = '后端未返回逐笔成交，图上只显示价格与均线'
    return
  }
  const index = new Map<string, number>()
  bars.value.forEach((b, i) => index.set(b.date, i))
  const out: CandleMarker[] = []
  let missing = 0
  for (const f of fills) {
    const i = index.get(f.date)
    if (i === undefined) {
      missing += 1
      continue
    }
    out.push({ index: i, side: f.side, price: f.price })
  }
  markers.value = out
  markerNote.value =
    out.length === 0
      ? '成交日期与 K 线区间不重叠（' + fills.length + ' 笔未落图）'
      : missing > 0
        ? '已画 ' + out.length + ' 笔，另有 ' + missing + ' 笔超出 K 线区间'
        : '已画 ' + out.length + ' 笔买卖标记'
}

async function submitWalkForward(): Promise<void> {
  if (codeInput.value.length !== 6) {
    error.value = '请选择 6 位股票代码，例如 600519'
    return
  }
  wfLoading.value = true
  wfError.value = ''
  try {
    wf.value = await quantWalkForward(codeInput.value, strategyId.value, Math.max(2, Math.min(6, folds.value)))
    tab.value = 'walkforward'
  } catch (e) {
    wfError.value = marketMessage(e)
  } finally {
    wfLoading.value = false
  }
}

function onPick(pick: { code: string; name: string }): void {
  codeInput.value = pick.code
  stockName.value = pick.name
  void submitRun()
}

watch(strategyId, () => {
  seedParams()
  opt.value = null
  optError.value = ''
})

// ---------- K 线区 ----------

function kSchedule(): void {
  if (rafK !== 0) return
  rafK = window.requestAnimationFrame(() => {
    rafK = 0
    kDraw()
  })
}

function kDraw(): void {
  const canvas = klineCanvas.value
  if (!canvas) return
  if (!candle) {
    candle = new CandleChart(canvas, { overlays: { ma: true, boll: false }, sub: 'volume', markers: true })
  }
  candle.resize(klineSize.value.w, klineSize.value.h)
  candle.setData(bars.value, inds.value, markers.value)
  candle.draw()
}

function kMeasure(): void {
  const el = klineWrap.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height))
  if (w !== klineSize.value.w || h !== klineSize.value.h) klineSize.value = { w, h }
  if (candle) candle.resize(w, h)
  kSchedule()
}

function attachKline(): void {
  const el = klineWrap.value
  if (!el) return
  if (roK) roK.disconnect()
  roK = new ResizeObserver(() => kMeasure())
  roK.observe(el)
  kMeasure()
}

function onKlineMove(e: PointerEvent): void {
  const canvas = klineCanvas.value
  if (!canvas || !candle) return
  const rect = canvas.getBoundingClientRect()
  const x = e.clientX - rect.left
  const y = e.clientY - rect.top
  const idx = candle.indexAt(x)
  if (idx < 0) {
    klineHover.value = null
    candle.setHover(null)
  } else {
    klineHover.value = { index: idx, y }
    candle.setHover({ index: idx, y })
  }
  kSchedule()
}

function onKlineLeave(): void {
  klineHover.value = null
  candle?.setHover(null)
  kSchedule()
}

// ---------- 净值区 ----------

function eSchedule(): void {
  if (rafE !== 0) return
  rafE = window.requestAnimationFrame(() => {
    rafE = 0
    eDraw()
  })
}

function eDraw(): void {
  const canvas = equityCanvas.value
  if (!canvas) return
  const ctx = fitCanvas(canvas, equitySize.value.w, equitySize.value.h)
  if (!ctx) return
  drawEquityChart(ctx, equity.value, {
    width: equitySize.value.w,
    height: equitySize.value.h,
    initialCash: result.value?.initialCash ?? 0,
    hoverIndex: equityHover.value,
    drawdown: showDrawdown.value,
  })
}

function eMeasure(): void {
  const el = equityWrap.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height))
  if (w !== equitySize.value.w || h !== equitySize.value.h) equitySize.value = { w, h }
  eSchedule()
}

function attachEquity(): void {
  const el = equityWrap.value
  if (!el) return
  if (roE) roE.disconnect()
  roE = new ResizeObserver(() => eMeasure())
  roE.observe(el)
  eMeasure()
}

function onEquityMove(e: PointerEvent): void {
  const canvas = equityCanvas.value
  const list = equity.value
  if (!canvas || list.length < 2) return
  const rect = canvas.getBoundingClientRect()
  const left = 10
  const right = 64
  const plotW = Math.max(10, rect.width - left - right)
  const ratio = (e.clientX - rect.left - left) / plotW
  const idx = Math.round(ratio * (list.length - 1))
  equityHover.value = Math.min(Math.max(idx, 0), list.length - 1)
  eSchedule()
}

function onEquityLeave(): void {
  equityHover.value = null
  eSchedule()
}

/** 结果区是 v-else 渲染的，ref 晚于 onMounted 出现，所以每次结果更新后重新挂观察器。 */
function attachCharts(reset: boolean): void {
  if (reset && candle) {
    candle.destroy()
    candle = null
  }
  attachKline()
  attachEquity()
}

watch(klineWrap, () => attachKline(), { flush: 'post' })
watch(equityWrap, () => attachEquity(), { flush: 'post' })
watch(showDrawdown, () => eSchedule())
watch(equity, () => eSchedule())

watch(
  () => state.focus,
  (f) => {
    if (!f) return
    if (f.code === codeInput.value && run.value) return
    codeInput.value = f.code
    stockName.value = f.name
    void submitRun()
  },
)

onMounted(async () => {
  await loadStrategies()
  if (state.focus) {
    codeInput.value = state.focus.code
    stockName.value = state.focus.name
  }
  await submitRun()
})

onBeforeUnmount(() => {
  if (roK) roK.disconnect()
  if (roE) roE.disconnect()
  if (rafK !== 0) window.cancelAnimationFrame(rafK)
  if (rafE !== 0) window.cancelAnimationFrame(rafE)
  candle?.destroy()
  candle = null
})

function paramStep(key: string): number {
  const d = paramDefs.value.find((p) => p.key === key)
  if (!d) return 1
  if (d.step) return d.step
  return d.type === 'int' ? 1 : 0.01
}

/** 把参数对象按 key 排序拼成「fast=5, slow=20」，用于表格、说明与 Agent 提示词。 */
function paramsText(p: Record<string, number> | undefined): string {
  if (!p) return '-'
  const keys = Object.keys(p).sort()
  if (keys.length === 0) return '（无参数）'
  return keys.map((k) => k + '=' + p[k]).join(', ')
}

/** 运行参数优化：全样本网格评估 + 邻域稳健性 + walk-forward 样本外，结果写进「参数优化」tab。 */
async function submitOptimize(): Promise<void> {
  if (codeInput.value.length !== 6) {
    optError.value = '请选择 6 位股票代码，例如 600519'
    return
  }
  optLoading.value = true
  optError.value = ''
  tab.value = 'optimize'
  try {
    opt.value = await quantOptimize(codeInput.value, strategyId.value, Math.max(2, Math.min(6, folds.value)))
  } catch (e) {
    optError.value = marketMessage(e)
  } finally {
    optLoading.value = false
  }
}

/** 组「让 Agent 自主优化」的提示词：标的、策略、当前参数、优化 top3 与样本外结论一次交代清楚。 */
function buildOptimizePrompt(): string {
  const lines: string[] = []
  const name = stockName.value ? stockName.value + '（' + codeInput.value + '）' : codeInput.value
  lines.push('任务：复核并优化「' + (spec.value?.name ?? strategyId.value) + '」在 ' + name + ' 上的参数与规则，只做只读研究，不给交易指令。')
  lines.push('- 策略 ID：' + strategyId.value)
  lines.push('- 当前参数：' + paramsText(params))
  if (spec.value?.formula) lines.push('- 策略公式：' + spec.value.formula)
  const cands = opt.value?.candidates.slice(0, 3) ?? []
  if (cands.length > 0) {
    lines.push(
      '- 本地优化候选 top3：' +
        cands
          .map(
            (c, i) =>
              '#' +
              (i + 1) +
              ' ' +
              paramsText(c.params) +
              '（总收益 ' +
              fmtPct(c.totalReturn * 100) +
              '，夏普 ' +
              fmtNum(c.sharpe, 2) +
              '，稳健性 ' +
              (c.stability === undefined ? '—' : fmtNum(c.stability, 2)) +
              '）',
          )
          .join('；'),
    )
  } else {
    lines.push('- 本地还没有优化结果：请先用 quant.optimize 跑一次。')
  }
  const wfRes = opt.value?.walkforward ?? wf.value?.result
  if (wfRes && wfRes.folds.length > 0) {
    lines.push(
      '- walk-forward：样本内 ' +
        fmtPct(wfRes.isReturn * 100) +
        '，样本外 ' +
        fmtPct(wfRes.oosReturn * 100) +
        '，差距 ' +
        fmtPct(wfRes.overfitGap * 100) +
        '，结论：' +
        wfRes.verdict,
    )
  } else {
    lines.push('- walk-forward：本地暂无结论，请用 quant.optimize / quant.walkforward 补齐样本外验证。')
  }
  lines.push('')
  lines.push('步骤要求：')
  lines.push('1. 先读算法说明与工具 schema，说明信号规则、成交口径（次日开盘、T+1、涨跌停不可成交）和成本假设；')
  lines.push('2. 用 quant.backtest / quant.optimize 复核关键参数，参数优化必须同时报告样本外表现与邻域稳健性；')
  lines.push('3. 最后给出「下一步该改什么」（规则、参数或数据）与至少两条可证伪条件；禁止只报样本内最优参数，也不要把回测结论当成未来收益承诺。')
  return lines.join('\n')
}

/** 打开 Agent 抽屉并让 quant worker 自主复核/优化策略；不阻塞面板，错误用 toast 提示。 */
function runAgentOptimize(): void {
  state.drawer = true
  if (agentState.running) {
    toast('已有 Agent 任务在运行：可在抽屉里插话或先停止', 'warn')
    return
  }
  beginRun('quant')
  agentBusy.value = true
  agentRun('quant', buildOptimizePrompt())
    .then((res) => {
      if (res.error) {
        endRun(true, res.error)
        toast('Agent 运行失败：' + res.error, 'error')
        return
      }
      if (res.report) {
        agentState.report = res.report
        agentState.reportWorker = res.worker
      }
      endRun(false, '完成，用时 ' + (res.steps ?? 0) + ' 步')
    })
    .catch((e) => {
      endRun(true, marketMessage(e))
      toast(marketMessage(e), 'error')
    })
    .finally(() => {
      agentBusy.value = false
    })
}
</script>

<template>
  <section class="card q">
    <div class="card-hd">
      <span class="card-title">量化研究</span>
      <span class="card-sub ellipsis">{{ spec ? spec.name + ' · ' + spec.desc : '只读研究：回测结论不等于未来收益' }}</span>
      <span class="card-tools">
        <span v-if="noAdjust" class="tag tag-accent" title="数据源不支持复权，本次按不复权计算">不复权</span>
        <button class="btn btn-sm" type="button" :disabled="wfLoading" @click="submitWalkForward">
          {{ wfLoading ? '检验中…' : 'walk-forward' }}
        </button>
        <button class="btn btn-sm btn-accent" type="button" :disabled="loading" @click="submitRun">
          {{ loading ? '回测中…' : '运行回测' }}
        </button>
      </span>
    </div>

    <div class="q-body">
      <div class="q-form">
        <div class="field">
          <span class="label">标的</span>
          <StockPicker v-model="codeInput" :name="stockName" :width="190" @select="onPick" />
        </div>

        <label class="field">
          <span class="label">周期</span>
          <select v-model="period" class="select">
            <option v-for="p in PERIODS" :key="p.key" :value="p.key">{{ p.label }}</option>
          </select>
        </label>

        <label class="field">
          <span class="label">策略</span>
          <select v-model="strategyId" class="select">
            <option v-for="s in strategies" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
        </label>

        <label v-for="d in paramDefs" :key="d.key" class="field">
          <span class="label">{{ d.label }}</span>
          <input
            v-model.number="params[d.key]"
            class="input num"
            style="width: 96px"
            type="number"
            :min="d.min"
            :max="d.max"
            :step="paramStep(d.key)"
            :title="d.label + '（' + d.key + '）：' + (d.min ?? 0) + ' ~ ' + (d.max ?? 0)"
          />
        </label>

        <label class="field">
          <span class="label">初始资金</span>
          <input v-model.number="initialCash" class="input num" style="width: 118px" type="number" min="1000" step="10000" />
        </label>

        <label class="field">
          <span class="label">折数</span>
          <input v-model.number="folds" class="input num" style="width: 66px" type="number" min="2" max="6" step="1" />
        </label>
      </div>

      <div class="q-algo">
        <button class="q-algo-hd" type="button" :aria-expanded="algoOpen" @click="algoOpen = !algoOpen">
          <span class="q-algo-title">算法说明</span>
          <span class="tiny muted ellipsis">信号怎么算 · 成交与成本口径 · 参数范围</span>
          <span class="spacer" />
          <span class="q-algo-toggle">{{ algoOpen ? '收起' : '展开' }}</span>
        </button>
        <div v-show="algoOpen" class="q-algo-body">
          <section v-if="engineRules.length > 0" class="q-algo-sec">
            <h4 class="q-algo-h">全局口径（所有策略共用）</h4>
            <ul class="q-algo-list">
              <li v-for="(r, i) in engineRules" :key="i">{{ r }}</li>
            </ul>
          </section>
          <section class="q-algo-sec">
            <h4 class="q-algo-h">{{ spec?.name ?? '策略' }}</h4>
            <p v-if="spec?.formula" class="q-formula">{{ spec.formula }}</p>
            <ul v-if="(spec?.rules ?? []).length > 0" class="q-algo-list">
              <li v-for="(r, i) in spec?.rules ?? []" :key="i">{{ r }}</li>
            </ul>
            <p v-else class="tiny muted">该策略没有额外的规则说明。</p>
            <div v-if="paramDefs.length > 0" class="q-paramdefs">
              <span v-for="d in paramDefs" :key="d.key" class="q-paramdef">
                <b>{{ d.label }}</b>
                <code>{{ d.key }}</code>
                <span class="num">{{ d.min ?? '?' }} ~ {{ d.max ?? '?' }}</span>
                <span class="tiny muted">{{ d.type === 'int' ? '整数' : '小数' }}<template v-if="d.step"> · 步长 {{ d.step }}</template></span>
              </span>
            </div>
          </section>
        </div>
      </div>

      <div v-if="run?.warning" class="q-warn">{{ run.warning }}</div>
      <div v-if="staleCode" class="q-warn">下方结果属于 {{ result?.code }}，已切换标的；点「运行回测」重新计算。</div>

      <StateBlock v-if="!ready && loading" kind="loading" title="准备回测…" />
      <StateBlock v-else-if="error" kind="error" title="回测失败" :message="error" action-text="重试" @action="submitRun" />
      <StateBlock
        v-else-if="!result"
        kind="empty"
        title="还没有回测结果"
        message="选好标的与参数后点「运行回测」。成本口径：佣金万三（最低 5 元）、印花税卖出万五、过户费双边 0.001%、滑点单边千分之一。"
      />

      <template v-else>
        <div class="q-metrics">
          <div v-for="mt in metrics" :key="mt.key" class="metric">
            <span class="metric-label">{{ mt.label }}</span>
            <span class="metric-value" :class="mt.cls">{{ mt.value }}</span>
          </div>
          <div class="q-costs">
            <span v-for="c in costs" :key="c.label" class="q-cost-item">
              <span class="muted">{{ c.label }}</span>
              <b class="num">{{ fmtAmount(c.value) }}</b>
            </span>
            <span class="q-cost-item">
              <span class="muted">成本合计</span>
              <b class="num">{{ fmtAmount(costTotal) }}</b>
            </span>
            <span class="q-cost-item">
              <span class="muted">区间</span>
              <b class="num tiny">{{ result.start }} ~ {{ result.end }}（{{ result.bars }} 根）</b>
            </span>
          </div>
        </div>

        <div class="q-charts">
          <section class="q-pane">
            <div class="q-pane-hd">
              <span class="q-pane-title">标的 K 线</span>
              <span class="tag tag-accent" v-if="markers.length > 0">B/S 标记</span>
              <span class="tiny muted ellipsis">{{ markerNote }}</span>
              <span class="spacer" />
              <span class="tiny muted">{{ bars.length }} 根 · {{ result.code }}</span>
            </div>
            <div ref="klineWrap" class="q-pane-body">
              <canvas
                ref="klineCanvas"
                class="q-canvas"
                role="img"
                aria-label="标的 K 线与买卖标记"
                @pointermove="onKlineMove"
                @pointerleave="onKlineLeave"
              />
              <div v-if="bars.length === 0" class="q-pane-state tiny muted">K 线数据不可用（回测仍可用）</div>
            </div>
            <div class="q-legend">
              <template v-if="activeBar">
                <span class="num">{{ activeBar.date }}</span>
                <span>开 <b class="num">{{ fmtPrice(activeBar.open) }}</b></span>
                <span>高 <b class="num">{{ fmtPrice(activeBar.high) }}</b></span>
                <span>低 <b class="num">{{ fmtPrice(activeBar.low) }}</b></span>
                <span>收 <b class="num" :class="dir(activeBarPct)">{{ fmtPrice(activeBar.close) }}</b></span>
                <span>涨跌 <b class="num" :class="dir(activeBarPct)">{{ fmtPct(activeBarPct) }}</b></span>
              </template>
              <template v-else>
                <span>MA5 / MA10 / MA20 / MA60</span>
                <span class="muted">·</span>
                <span>成交量副图</span>
              </template>
              <span class="spacer" />
              <span class="tiny muted">▲ 买入（琥珀） · ▼ 卖出（蓝）</span>
            </div>
          </section>

          <section class="q-pane">
            <div class="q-pane-hd">
              <span class="q-pane-title">净值曲线</span>
              <span class="spacer" />
              <button class="chip" :class="{ on: showDrawdown }" type="button" :aria-pressed="showDrawdown" @click="showDrawdown = !showDrawdown">
                回撤
              </button>
            </div>
            <div ref="equityWrap" class="q-pane-body short">
              <canvas
                ref="equityCanvas"
                class="q-canvas"
                role="img"
                aria-label="净值曲线"
                @pointermove="onEquityMove"
                @pointerleave="onEquityLeave"
              />
            </div>
            <div class="q-legend">
              <span>净值 <b class="num">{{ fmtAmount(activeEquity?.equity) }}</b></span>
              <span>相对初始 <b class="num" :class="dir(activeReturn)">{{ fmtPct(activeReturn * 100) }}</b></span>
              <span class="muted tiny">{{ activeEquity?.date }}</span>
              <span class="spacer" />
              <span class="tiny muted">初始 {{ fmtPrice(result.initialCash) }} 元 · 虚线为基准</span>
            </div>
          </section>
        </div>

        <div v-if="result.warnings && result.warnings.length > 0" class="q-warnings">
          <span v-for="(w, i) in result.warnings" :key="i" class="tag">{{ w }}</span>
        </div>

        <div class="q-tabs">
          <button class="tab" :class="{ on: tab === 'report' }" type="button" @click="tab = 'report'">回测报告</button>
          <button class="tab" :class="{ on: tab === 'walkforward' }" type="button" @click="tab = 'walkforward'">
            walk-forward
            <span v-if="wf" class="tag" :class="wfVerdictTone" style="margin-left: 6px">{{ wf.result.verdict }}</span>
          </button>
          <button class="tab" :class="{ on: tab === 'optimize' }" type="button" @click="tab = 'optimize'">
            参数优化
            <span v-if="opt?.best" class="tag tag-accent" style="margin-left: 6px">有推荐</span>
          </button>
          <span class="spacer" />
          <span class="tiny muted" style="padding-right: 10px">{{ result.strategy }} · {{ result.code }}</span>
        </div>

        <div v-show="tab === 'report'" class="q-report">
          <div class="md" v-html="reportHtml" />
        </div>

        <div v-show="tab === 'walkforward'" class="q-report">
          <StateBlock v-if="wfError" kind="error" title="walk-forward 失败" :message="wfError" action-text="重试" @action="submitWalkForward" />
          <StateBlock
            v-else-if="!wf"
            kind="empty"
            title="还没有 walk-forward 结果"
            message="点右上角「walk-forward」：每折在样本内选参、紧邻样本外原样验证，再看样本外还剩多少。"
          />
          <template v-else>
            <div class="wf-head">
              <span class="tag" :class="wfVerdictTone">{{ wf.result.verdict }}</span>
              <span class="tiny muted">样本内 {{ fmtPct(wf.result.isReturn * 100) }}</span>
              <span class="tiny muted">样本外 {{ fmtPct(wf.result.oosReturn * 100) }}</span>
              <span class="tiny muted">差距 {{ fmtPct(wf.result.overfitGap * 100) }}</span>
              <span v-if="wf.result.note" class="tiny muted">· {{ wf.result.note }}</span>
            </div>
            <table v-if="wf.result.folds.length > 0" class="tbl">
              <thead>
                <tr>
                  <th class="l">折</th>
                  <th class="l">样本内</th>
                  <th class="l">样本外</th>
                  <th>最优参数</th>
                  <th>样本内夏普</th>
                  <th>样本外夏普</th>
                  <th>样本内收益</th>
                  <th>样本外收益</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="f in wf.result.folds" :key="f.index">
                  <td class="l num">{{ f.index + 1 }}</td>
                  <td class="l num tiny">{{ f.trainStart }} ~ {{ f.trainEnd }}</td>
                  <td class="l num tiny">{{ f.testStart }} ~ {{ f.testEnd }}</td>
                  <td class="num tiny">
                    <span v-for="(v, k) in f.best" :key="k" style="margin-left: 6px">{{ k }}={{ v }}</span>
                  </td>
                  <td class="num">{{ fmtNum(f.trainSharpe, 2) }}</td>
                  <td class="num">{{ fmtNum(f.testSharpe, 2) }}</td>
                  <td class="num" :class="dir(f.trainReturn)">{{ fmtPct(f.trainReturn * 100) }}</td>
                  <td class="num" :class="dir(f.testReturn)">{{ fmtPct(f.testReturn * 100) }}</td>
                </tr>
              </tbody>
            </table>
            <div class="md" style="margin-top: 10px" v-html="wfReportHtml" />
          </template>
        </div>

        <div v-show="tab === 'optimize'" class="q-report">
          <div class="opt-hd">
            <button class="btn btn-sm btn-accent" type="button" :disabled="optLoading" @click="submitOptimize">
              {{ optLoading ? '优化中…' : '运行参数优化' }}
            </button>
            <button class="btn btn-sm" type="button" :disabled="agentBusy || agentState.running" @click="runAgentOptimize">
              {{ agentBusy ? 'Agent 准备中…' : '让 Agent 自主优化' }}
            </button>
            <span class="tiny muted">先在全样本网格里筛选，再看邻域稳健性与 walk-forward 样本外；推荐参数不是上线建议。</span>
          </div>
          <StateBlock
            v-if="optError"
            kind="error"
            title="参数优化失败"
            :message="optError"
            action-text="重试"
            @action="submitOptimize"
          />
          <StateBlock v-else-if="optLoading && !opt" kind="loading" title="正在优化参数…" />
          <StateBlock
            v-else-if="!opt"
            kind="empty"
            title="还没有优化结果"
            message="点「运行参数优化」：在默认网格上评估每组参数，按夏普与邻域稳健性排序，再对推荐参数做 walk-forward 样本外验证。"
          />
          <template v-else>
            <div v-if="opt.note" class="q-warn">{{ opt.note }}</div>
            <div v-if="opt.best" class="opt-best">
              <span class="tiny muted">推荐参数</span>
              <b class="num">{{ paramsText(opt.best.params) }}</b>
              <span class="tiny muted">总收益 {{ fmtPct(opt.best.totalReturn * 100) }}</span>
              <span class="tiny" :class="dir(opt.best.sharpe)">夏普 {{ fmtNum(opt.best.sharpe, 2) }}</span>
              <span class="tiny muted">稳健性 {{ opt.best.stability === undefined ? '—' : fmtNum(opt.best.stability, 2) }}</span>
            </div>
            <table v-if="optRows.length > 0" class="tbl">
              <thead>
                <tr>
                  <th class="l">排名</th>
                  <th class="l">参数</th>
                  <th>总收益</th>
                  <th>年化</th>
                  <th>回撤</th>
                  <th>夏普</th>
                  <th>交易数</th>
                  <th>稳健性</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(c, i) in optRows" :key="i" :class="{ 'opt-row-best': paramsText(c.params) === bestSignature }">
                  <td class="l num">{{ i + 1 }}</td>
                  <td class="l num tiny">{{ paramsText(c.params) }}</td>
                  <td class="num" :class="dir(c.totalReturn)">{{ fmtPct(c.totalReturn * 100) }}</td>
                  <td class="num" :class="dir(c.cagr)">{{ fmtPct(c.cagr * 100) }}</td>
                  <td class="num risk">{{ fmtPct(-Math.abs(c.maxDrawdown) * 100) }}</td>
                  <td class="num">{{ fmtNum(c.sharpe, 2) }}</td>
                  <td class="num">{{ c.tradeCount }}</td>
                  <td class="num">{{ c.stability === undefined ? '—' : fmtNum(c.stability, 2) }}</td>
                </tr>
              </tbody>
            </table>
            <div class="md" style="margin-top: 10px" v-html="optReportHtml" />
          </template>
        </div>
      </template>
    </div>
  </section>
</template>

<style scoped>
  .q {
    min-height: 0;
  }
  .q-body {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow-y: auto;
    padding: 10px 12px 12px;
    gap: 10px;
  }
  .q-form {
    display: flex;
    align-items: flex-end;
    flex-wrap: wrap;
    gap: 10px 14px;
  }
  .q-warn {
    padding: 6px 10px;
    border: 1px solid rgba(233, 180, 76, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(233, 180, 76, 0.08);
    color: var(--accent-2);
    font-size: 11.5px;
  }
  .q-metrics {
    display: grid;
    grid-template-columns: repeat(8, minmax(0, 1fr));
    gap: 6px;
  }
  .q-metrics .metric-value {
    font-size: 14px;
  }
  .q-metrics .metric-value.risk {
    color: var(--accent);
  }
  .q-costs {
    grid-column: 1 / -1;
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    margin-top: 2px;
    padding-top: 6px;
    border-top: 1px solid var(--line);
  }
  .q-cost-item {
    display: flex;
    gap: 5px;
    font-size: 11px;
  }
  .q-cost-item b {
    color: var(--text-2);
    font-weight: 500;
  }
  .q-charts {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .q-pane {
    display: flex;
    flex-direction: column;
    min-width: 0;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg);
    overflow: hidden;
  }
  .q-pane-hd {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 26px;
    flex: 0 0 26px;
    padding: 0 8px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
  }
  .q-pane-title {
    color: var(--text-2);
    font-size: 11px;
    letter-spacing: 0.5px;
  }
  .q-pane-body {
    position: relative;
    height: 280px;
    min-height: 0;
  }
  .q-pane-body.short {
    height: 220px;
  }
  .q-pane-state {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
  }
  .q-canvas {
    display: block;
    cursor: crosshair;
  }
  .q-legend {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 22px;
    flex: 0 0 22px;
    padding: 0 8px;
    border-top: 1px solid var(--line);
    background: var(--panel);
    color: var(--muted);
    font-size: 11px;
    overflow: hidden;
    white-space: nowrap;
  }
  .q-legend b {
    color: var(--text-2);
    font-weight: 500;
  }
  .q-warnings {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .q-tabs {
    display: flex;
    align-items: center;
    gap: 2px;
    border-bottom: 1px solid var(--line);
  }
  .q-report {
    min-height: 140px;
  }
  .q-algo {
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg);
    overflow: hidden;
  }
  .q-algo-hd {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 10px;
    border: 0;
    background: var(--panel);
    color: var(--text-2);
    font: inherit;
    font-size: 11.5px;
    text-align: left;
    cursor: pointer;
  }
  .q-algo-hd:hover {
    background: var(--panel-2);
  }
  .q-algo-title {
    color: var(--text);
    font-weight: 600;
  }
  .q-algo-toggle {
    color: var(--accent-2);
    font-size: 11px;
  }
  .q-algo-body {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 10px 12px 12px;
    border-top: 1px solid var(--line);
  }
  .q-algo-sec {
    min-width: 0;
  }
  .q-algo-h {
    margin: 0 0 6px;
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
  }
  .q-formula {
    margin: 0 0 7px;
    padding: 5px 8px;
    border-left: 2px solid var(--accent);
    border-radius: 0 var(--radius-xs) var(--radius-xs) 0;
    background: var(--accent-soft);
    color: var(--accent-2);
    font: 11.5px/1.6 var(--mono);
  }
  .q-algo-list {
    margin: 0;
    padding-left: 18px;
    color: var(--text-2);
    font-size: 11.5px;
    line-height: 1.7;
  }
  .q-algo-list li {
    margin-bottom: 3px;
  }
  .q-paramdefs {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
  }
  .q-paramdef {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 8px;
    border: 1px solid var(--line);
    border-radius: var(--radius-xs);
    background: var(--panel);
    color: var(--text-2);
    font-size: 11px;
  }
  .q-paramdef code {
    color: var(--accent-2);
    font: 10.5px var(--mono);
  }
  .opt-hd {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 10px;
  }
  .opt-best {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 8px;
    padding: 7px 10px;
    border: 1px solid rgba(233, 180, 76, 0.35);
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    font-size: 11.5px;
  }
  .opt-row-best td {
    background: rgba(233, 180, 76, 0.1) !important;
    color: var(--text);
  }
  .wf-head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 8px;
  }
  @media (max-width: 1280px) {
    .q-metrics {
      grid-template-columns: repeat(4, minmax(0, 1fr));
    }
  }
  .card-tools {
    flex-wrap: wrap;
    justify-content: flex-end;
    row-gap: 4px;
  }
</style>