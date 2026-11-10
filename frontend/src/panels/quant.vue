<script setup lang="ts">
// 量化研究：参数表单 → 回测 → 指标卡 + 净值曲线 + 报告；另有 walk-forward 过拟合检验。
// 只读研究：输出的是可复现的回测结论，不含任何交易指令。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { CHART_COLORS, CHART_MONO, prepareCanvas } from '../lib/canvas'
import { messageOf, quantRun, quantStrategies, quantWalkForward } from '../lib/data'
import { dir, fmtAmount, fmtNum, fmtPct, fmtPrice, normalizeCode } from '../lib/format'
import { mdToHtml } from '../lib/markdown'
import { state } from '../stores/app'
import type { EquityPoint, QuantRunResponse, StrategySpec, WalkForwardResponse } from '../types'

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
const period = ref('day')
const initialCash = ref(100000)
const folds = ref(3)
const loading = ref(false)
const wfLoading = ref(false)
const error = ref('')
const wfError = ref('')
const run = ref<QuantRunResponse | null>(null)
const wf = ref<WalkForwardResponse | null>(null)
const tab = ref<'report' | 'walkforward'>('report')
const ready = ref(false)

const canvasEl = ref<HTMLCanvasElement | null>(null)
const chartEl = ref<HTMLElement | null>(null)
const chartSize = ref({ w: 0, h: 0 })
const hover = ref<{ index: number; x: number } | null>(null)

let ro: ResizeObserver | null = null
let raf = 0

const spec = computed<StrategySpec | null>(() => strategies.value.find((s) => s.id === strategyId.value) ?? null)
const paramDefs = computed(() => spec.value?.params ?? [])
const equity = computed<EquityPoint[]>(() => run.value?.result.equity ?? [])
const result = computed(() => run.value?.result ?? null)
const noAdjust = computed(() => run.value?.adjust === 'none')
const reportHtml = computed(() => mdToHtml(run.value?.report ?? ''))
const wfReportHtml = computed(() => mdToHtml(wf.value?.report ?? ''))

const metrics = computed(() => {
  const r = result.value
  if (!r) return []
  return [
    { key: 'totalReturn', label: '总收益', value: fmtPct(r.totalReturn * 100), cls: dir(r.totalReturn) },
    { key: 'cagr', label: '年化', value: fmtPct(r.cagr * 100), cls: dir(r.cagr) },
    { key: 'mdd', label: '最大回撤', value: fmtPct(-Math.abs(r.maxDrawdown) * 100), cls: r.maxDrawdown > 0.0001 ? 'risk' : 'flat' },
    { key: 'sharpe', label: '夏普', value: fmtNum(r.sharpe, 2), cls: dir(r.sharpe) },
    { key: 'winRate', label: '胜率', value: fmtNum(r.winRate * 100, 1) + '%', cls: 'flat' },
    { key: 'trades', label: '交易笔数', value: String(r.tradeCount), cls: 'flat' },
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
  const idx = hover.value ? Math.min(Math.max(hover.value.index, 0), list.length - 1) : list.length - 1
  return list[idx] ?? null
})

const activeReturn = computed(() => {
  const p = activeEquity.value
  const base = result.value?.initialCash ?? 0
  if (!p || base <= 0) return 0
  return p.equity / base - 1
})

function seedParams(): void {
  for (const k of Object.keys(params)) delete params[k]
  const def = spec.value?.default ?? {}
  for (const d of paramDefs.value) {
    const v = def[d.key]
    params[d.key] = typeof v === 'number' ? v : d.min ?? 0
  }
}

async function loadStrategies(): Promise<void> {
  try {
    strategies.value = await quantStrategies()
    if (!strategies.value.some((s) => s.id === strategyId.value)) {
      strategyId.value = strategies.value[0]?.id ?? 'ma'
    }
    seedParams()
    ready.value = true
  } catch (e) {
    error.value = messageOf(e)
    ready.value = true
  }
}

async function submitRun(): Promise<void> {
  const c = normalizeCode(codeInput.value)
  if (c.length !== 6) {
    error.value = '请输入 6 位股票代码，例如 600519'
    return
  }
  code.value = c
  loading.value = true
  error.value = ''
  try {
    const res = await quantRun({
      code: c,
      period: period.value,
      strategy: strategyId.value,
      params: { ...params },
      limit: 500,
      initialCash: Math.max(1000, Math.round(initialCash.value || 100000)),
    })
    run.value = res
    wf.value = null
    tab.value = 'report'
    await nextTick()
    attachChart()
  } catch (e) {
    error.value = messageOf(e)
    run.value = null
  } finally {
    loading.value = false
  }
}

async function submitWalkForward(): Promise<void> {
  const c = normalizeCode(codeInput.value)
  if (c.length !== 6) {
    error.value = '请输入 6 位股票代码，例如 600519'
    return
  }
  wfLoading.value = true
  wfError.value = ''
  try {
    wf.value = await quantWalkForward(c, strategyId.value, Math.max(2, Math.min(6, folds.value)))
    tab.value = 'walkforward'
  } catch (e) {
    wfError.value = messageOf(e)
  } finally {
    wfLoading.value = false
  }
}

watch(strategyId, () => {
  seedParams()
})

// ---------- 净值曲线 ----------

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
  const w = chartSize.value.w
  const h = chartSize.value.h
  const ctx = prepareCanvas(canvas, w, h)
  if (!ctx) return
  const list = equity.value
  if (list.length < 2) {
    ctx.fillStyle = CHART_COLORS.text
    ctx.font = '11px ' + CHART_MONO
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText('运行回测后显示净值曲线', w / 2, h / 2)
    return
  }

  const left = 6
  const right = 62
  const top = 10
  const bottom = 18
  const plotW = Math.max(10, w - left - right)
  const plotH = Math.max(20, h - top - bottom)

  const init = result.value?.initialCash ?? list[0]?.equity ?? 0
  let min = init > 0 ? init : Number.POSITIVE_INFINITY
  let max = init > 0 ? init : Number.NEGATIVE_INFINITY
  for (const p of list) {
    if (p.equity < min) min = p.equity
    if (p.equity > max) max = p.equity
  }
  if (!Number.isFinite(min) || !Number.isFinite(max)) return
  if (max - min < 1) {
    max += 1
    min -= 1
  }
  const pad = (max - min) * 0.08
  min -= pad
  max += pad
  const yOf = (v: number): number => top + plotH - ((v - min) / (max - min)) * plotH
  const xOf = (i: number): number => left + (i / (list.length - 1)) * plotW

  ctx.font = '10px ' + CHART_MONO
  ctx.lineWidth = 1
  ctx.textAlign = 'left'
  ctx.textBaseline = 'middle'
  for (let k = 0; k <= 4; k++) {
    const y = top + (plotH * k) / 4
    ctx.strokeStyle = CHART_COLORS.grid
    ctx.beginPath()
    ctx.moveTo(left, Math.round(y) + 0.5)
    ctx.lineTo(left + plotW, Math.round(y) + 0.5)
    ctx.stroke()
    ctx.fillStyle = CHART_COLORS.text
    ctx.fillText(fmtAmount(max - ((max - min) * k) / 4), left + plotW + 6, y)
  }

  // 初始资金基准线
  if (init > 0) {
    const y = yOf(init)
    ctx.strokeStyle = CHART_COLORS.gridStrong
    ctx.setLineDash([4, 4])
    ctx.beginPath()
    ctx.moveTo(left, y)
    ctx.lineTo(left + plotW, y)
    ctx.stroke()
    ctx.setLineDash([])
  }

  // 面积 + 折线
  ctx.beginPath()
  ctx.moveTo(xOf(0), yOf(list[0]?.equity ?? init))
  for (let i = 1; i < list.length; i++) ctx.lineTo(xOf(i), yOf(list[i]?.equity ?? init))
  ctx.strokeStyle = CHART_COLORS.accent
  ctx.lineWidth = 1.4
  ctx.stroke()

  ctx.lineTo(xOf(list.length - 1), top + plotH)
  ctx.lineTo(xOf(0), top + plotH)
  ctx.closePath()
  const grad = ctx.createLinearGradient(0, top, 0, top + plotH)
  grad.addColorStop(0, 'rgba(232,179,75,0.22)')
  grad.addColorStop(1, 'rgba(232,179,75,0.02)')
  ctx.fillStyle = grad
  ctx.fill()

  // 时间轴
  ctx.fillStyle = CHART_COLORS.text
  ctx.textBaseline = 'top'
  const marks = [0, Math.floor(list.length / 2), list.length - 1]
  for (const i of marks) {
    const p = list[i]
    if (!p) continue
    ctx.textAlign = i === 0 ? 'left' : i === list.length - 1 ? 'right' : 'center'
    const x = Math.min(Math.max(xOf(i), left), left + plotW)
    ctx.fillText(p.date, x, top + plotH + 4)
  }

  // 十字光标
  const hv = hover.value
  if (hv) {
    const idx = Math.min(Math.max(hv.index, 0), list.length - 1)
    const p = list[idx]
    if (p) {
      const x = xOf(idx)
      ctx.strokeStyle = CHART_COLORS.cross
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
      ctx.fillStyle = CHART_COLORS.gridStrong
      ctx.fillRect(left + plotW + 2, y - 8, 58, 16)
      ctx.fillStyle = CHART_COLORS.text2
      ctx.textAlign = 'left'
      ctx.textBaseline = 'middle'
      ctx.fillText(fmtAmount(p.equity), left + plotW + 6, y)

      const label = p.date + '  ' + fmtAmount(p.equity)
      const tw = ctx.measureText(label).width + 12
      const lx = Math.min(Math.max(x - tw / 2, left), left + plotW - tw)
      ctx.fillStyle = CHART_COLORS.gridStrong
      ctx.fillRect(lx, top + 2, tw, 15)
      ctx.fillStyle = CHART_COLORS.text2
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      ctx.fillText(label, lx + tw / 2, top + 3)
    }
  }
}

function onPointerMove(e: PointerEvent): void {
  const canvas = canvasEl.value
  const list = equity.value
  if (!canvas || list.length < 2) return
  const rect = canvas.getBoundingClientRect()
  const left = 6
  const right = 62
  const plotW = Math.max(10, rect.width - left - right)
  const ratio = (e.clientX - rect.left - left) / plotW
  const idx = Math.round(ratio * (list.length - 1))
  hover.value = { index: Math.min(Math.max(idx, 0), list.length - 1), x: e.clientX - rect.left }
  scheduleDraw()
}

function onPointerLeave(): void {
  hover.value = null
  scheduleDraw()
}

/** 图例占 22px，画布高度要扣掉，否则 canvas 溢出到图例下面。 */
const LEGEND_H = 22

function measureChart(): void {
  const el = chartEl.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height) - LEGEND_H)
  if (w !== chartSize.value.w || h !== chartSize.value.h) {
    chartSize.value = { w, h }
  }
  scheduleDraw()
}

/** 结果区是 v-else 渲染的，ref 可能晚于 onMounted 出现，所以按 ref 变化重新挂观察器。 */
function attachChart(): void {
  const el = chartEl.value
  if (!el) return
  if (ro) ro.disconnect()
  ro = new ResizeObserver(() => {
    measureChart()
  })
  ro.observe(el)
  measureChart()
}

watch(chartEl, () => {
  attachChart()
})

watch(equity, () => scheduleDraw())

onMounted(async () => {
  await loadStrategies()
  if (state.focus) {
    codeInput.value = state.focus.code
    code.value = state.focus.code
  }
  await submitRun()
  await nextTick()
  attachChart()
})

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (raf !== 0) window.cancelAnimationFrame(raf)
})

function paramStep(key: string): number {
  const d = paramDefs.value.find((p) => p.key === key)
  if (!d) return 1
  if (d.step) return d.step
  return d.type === 'int' ? 1 : 0.01
}
</script>

<template>
  <section class="card q">
    <div class="card-hd">
      <span class="card-title">量化研究</span>
      <span class="card-sub">{{ spec ? spec.name + ' · ' + spec.desc : '只读研究：回测结论不等于未来收益' }}</span>
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
        <label class="field">
          <span class="label">代码</span>
          <input
            v-model="codeInput"
            class="input num"
            style="width: 108px"
            placeholder="600519"
            aria-label="股票代码"
            @keydown.enter="submitRun"
          />
        </label>

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

      <div v-if="noAdjust && run?.warning" class="q-warn">{{ run.warning }}</div>

      <StateBlock v-if="!ready && loading" kind="loading" title="准备回测…" />
      <StateBlock v-else-if="error" kind="error" title="回测失败" :message="error" action-text="重试" @action="submitRun" />
      <StateBlock
        v-else-if="!result"
        kind="empty"
        title="还没有回测结果"
        message="填好代码与参数后点「运行回测」。成本口径：佣金万三（最低 5 元）、印花税卖出万五、过户费双边 0.001%、滑点单边千分之一。"
      />

      <template v-else>
        <div class="q-main">
          <div class="q-metrics">
            <div v-for="mt in metrics" :key="mt.key" class="q-metric">
              <span class="q-metric-label">{{ mt.label }}</span>
              <span class="q-metric-value num" :class="mt.cls">{{ mt.value }}</span>
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

          <div ref="chartEl" class="q-chart">
            <canvas
              ref="canvasEl"
              class="q-canvas"
              role="img"
              aria-label="净值曲线"
              @pointermove="onPointerMove"
              @pointerleave="onPointerLeave"
            />
            <div class="q-legend">
              <span>净值 <b class="num">{{ fmtAmount(activeEquity?.equity) }}</b></span>
              <span>相对初始 <b class="num" :class="dir(activeReturn)">{{ fmtPct(activeReturn * 100) }}</b></span>
              <span class="muted tiny">{{ activeEquity?.date }}</span>
              <span class="spacer" />
              <span class="tiny muted">初始 {{ fmtPrice(result.initialCash) }} 元 · 虚线为基准</span>
            </div>
          </div>
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
                <tr v-for="f in wf.result.folds" :key="f.index" :class="{ sel: false }">
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
    border: 1px solid rgba(232, 179, 75, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(232, 179, 75, 0.08);
    color: var(--accent-2);
    font-size: 11.5px;
  }
  .q-main {
    display: grid;
    grid-template-columns: 360px 1fr;
    gap: 12px;
    align-items: stretch;
  }
  .q-metrics {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
    align-content: start;
  }
  .q-metric {
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 6px 9px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .q-metric-label {
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.3px;
  }
  .q-metric-value {
    font-size: 14px;
    font-weight: 600;
    letter-spacing: 0.2px;
  }
  .q-metric-value.risk {
    color: var(--accent);
  }
  .q-costs {
    grid-column: 1 / -1;
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
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
  .q-chart {
    position: relative;
    min-height: 240px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg);
    padding-bottom: 22px;
    overflow: hidden;
  }
  .q-canvas {
    display: block;
    cursor: crosshair;
  }
  .q-legend {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    align-items: center;
    gap: 12px;
    height: 22px;
    padding: 0 8px;
    border-top: 1px solid var(--line);
    background: var(--panel);
    color: var(--muted);
    font-size: 11px;
  }
  .q-legend b {
    color: var(--text-2);
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
  .wf-head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 8px;
  }
</style>