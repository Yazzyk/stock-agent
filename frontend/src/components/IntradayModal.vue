<!-- 分时大图：面积/价格线 + 均价 + 昨收虚线 + 成交量，悬停显示逐分钟读数。
     自包含：数据由面板传入，内部只负责绘制与交互；Esc / 点遮罩由 AppModal 统一处理。 -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppModal from './AppModal.vue'
import { drawIntradayChart, fitCanvas, type IntradayChartLayout } from '../lib/chart'
import { dir, fmtPct, fmtPrice, fmtVolume } from '../lib/format'
import type { MinuteSeries } from '../types'

const props = withDefaults(
  defineProps<{
    series: MinuteSeries | null
    loading: boolean
    error: string
    /** 序列未到时用于标题兜底的名称与代码 */
    name?: string
    code?: string
  }>(),
  { name: '', code: '' },
)

const emit = defineEmits<{ (e: 'close'): void; (e: 'retry'): void }>()

const chartWrap = ref<HTMLElement | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)
const size = ref({ w: 0, h: 0 })
const hoverIndex = ref<number | null>(null)
let layout: IntradayChartLayout | null = null
let ro: ResizeObserver | null = null
let raf = 0

const title = computed<string>(() => {
  const name = props.series?.name || props.name
  const code = props.series?.code || props.code
  const parts = [name, code].filter((v): v is string => Boolean(v))
  return parts.length > 0 ? parts.join(' · ') : '分时走势'
})

const sub = computed<string>(() => {
  const date = props.series?.date ?? ''
  return date === '' ? '' : date + ' 分时'
})

const hoverPoint = computed(() => {
  const i = hoverIndex.value
  if (i === null) return null
  return props.series?.points[i] ?? null
})

const hoverPct = computed<number | null>(() => {
  const point = hoverPoint.value
  const prevClose = props.series?.prevClose ?? 0
  if (!point || prevClose <= 0) return null
  return (point.price / prevClose - 1) * 100
})

/** 合并同一帧内的多次重绘请求。 */
function scheduleDraw(): void {
  if (raf !== 0) return
  raf = window.requestAnimationFrame(() => {
    raf = 0
    draw()
  })
}

/** 按容器尺寸重绘分时图；容器过小时不绘制。 */
function draw(): void {
  const canvas = canvasEl.value
  if (!canvas) return
  const ctx = fitCanvas(canvas, size.value.w, size.value.h)
  if (!ctx) {
    layout = null
    return
  }
  layout = drawIntradayChart(ctx, props.series, {
    width: size.value.w,
    height: size.value.h,
    hoverIndex: hoverIndex.value,
  })
}

/** 读取容器尺寸变化，必要时重绘。 */
function measure(): void {
  const el = chartWrap.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height))
  if (w !== size.value.w || h !== size.value.h) size.value = { w, h }
  scheduleDraw()
}

/** 指针移动：把横坐标翻译成最近的分时点下标。 */
function onMove(e: PointerEvent): void {
  const canvas = canvasEl.value
  if (!canvas || !layout) return
  const rect = canvas.getBoundingClientRect()
  const index = layout.indexAt(e.clientX - rect.left)
  if (index !== hoverIndex.value) {
    hoverIndex.value = index
    scheduleDraw()
  }
}

/** 指针离开：清除悬停读数。 */
function onLeave(): void {
  if (hoverIndex.value === null) return
  hoverIndex.value = null
  scheduleDraw()
}

watch(
  () => [props.series, props.loading, props.error, size.value.w, size.value.h],
  () => {
    if (!props.series || props.series.points.length === 0) hoverIndex.value = null
    scheduleDraw()
  },
)

onMounted(() => {
  const el = chartWrap.value
  if (el) {
    ro = new ResizeObserver(() => measure())
    ro.observe(el)
  }
  measure()
})

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  ro = null
  if (raf !== 0) window.cancelAnimationFrame(raf)
  layout = null
})
</script>

<template>
  <AppModal :title="title" :sub="sub" wide @close="emit('close')">
    <div class="intraday">
      <div class="id-readout">
        <template v-if="hoverPoint">
          <span class="ro-item">时间 <b class="num">{{ hoverPoint.time }}</b></span>
          <span class="ro-item">价格 <b class="num">{{ fmtPrice(hoverPoint.price) }}</b></span>
          <span class="ro-item">均价 <b class="num">{{ fmtPrice(hoverPoint.avg) }}</b></span>
          <span class="ro-item">
            涨跌幅 <b class="num" :class="dir(hoverPct)">{{ fmtPct(hoverPct) }}</b>
          </span>
          <span class="ro-item">成交量 <b class="num">{{ fmtVolume(hoverPoint.volume) }}</b></span>
        </template>
        <span v-else class="tiny muted">在图上移动查看逐分钟读数</span>
        <span class="spacer" />
        <span class="tiny muted">昨收 <b class="num">{{ fmtPrice(series?.prevClose) }}</b></span>
      </div>

      <div ref="chartWrap" class="id-chart">
        <canvas ref="canvasEl" @pointermove="onMove" @pointerleave="onLeave" />
        <div v-if="loading" class="id-overlay">正在加载分时…</div>
        <div v-else-if="error" class="id-overlay id-error">
          <span>{{ error }}</span>
          <button class="btn btn-sm" type="button" @click="emit('retry')">重试</button>
        </div>
      </div>

      <div class="id-legend tiny muted">
        <span class="id-legend-item"><i class="id-swatch swatch-price" />价格</span>
        <span class="id-legend-item"><i class="id-swatch swatch-avg" />均价</span>
        <span class="id-legend-item"><i class="id-swatch swatch-prev" />昨收</span>
        <span class="spacer" />
        <span v-if="series?.source">来源 {{ series.source }}</span>
        <span>红涨绿跌 · 下方为分钟成交量</span>
      </div>
    </div>
  </AppModal>
</template>

<style scoped>
  .intraday {
    display: flex;
    flex-direction: column;
    gap: 10px;
    height: min(64vh, 520px);
  }
  .id-readout {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
    min-height: 20px;
    font-size: 11px;
    color: var(--muted);
  }
  .ro-item b {
    margin-left: 2px;
    color: var(--text);
    font-weight: 600;
  }
  .id-chart {
    position: relative;
    flex: 1 1 auto;
    min-height: 220px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: #0b1119;
    overflow: hidden;
  }
  .id-chart canvas {
    display: block;
    width: 100%;
    height: 100%;
    cursor: crosshair;
  }
  .id-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 16px;
    text-align: center;
    background: rgba(11, 17, 25, 0.72);
    color: var(--text-2);
    font-size: 12px;
  }
  .id-error {
    color: var(--warn, #e9b44c);
  }
  .id-legend {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .id-legend-item {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .id-swatch {
    display: inline-block;
    width: 10px;
    height: 2px;
    border-radius: 2px;
  }
  .swatch-price {
    background: #5b9dff;
  }
  .swatch-avg {
    background: #e9b44c;
  }
  .swatch-prev {
    background: rgba(148, 163, 184, 0.5);
    border-top: 1px dashed rgba(148, 163, 184, 0.8);
  }
</style>
