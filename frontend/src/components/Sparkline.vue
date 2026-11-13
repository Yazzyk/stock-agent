<script setup lang="ts">
// 迷你走势：表格里的 20 日收盘价折线，零依赖 canvas，跟随 DPR。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { drawSparkline } from '../lib/chart'

const props = withDefaults(
  defineProps<{
    values: Array<number | null>
    width?: number
    height?: number
    baseline?: number | null
  }>(),
  { width: 76, height: 22, baseline: null },
)

const canvasEl = ref<HTMLCanvasElement | null>(null)
let raf = 0

function draw(): void {
  const canvas = canvasEl.value
  if (!canvas) return
  const dpr = window.devicePixelRatio || 1
  const w = props.width
  const h = props.height
  const pxW = Math.max(1, Math.round(w * dpr))
  const pxH = Math.max(1, Math.round(h * dpr))
  if (canvas.width !== pxW || canvas.height !== pxH) {
    canvas.width = pxW
    canvas.height = pxH
  }
  canvas.style.width = w + 'px'
  canvas.style.height = h + 'px'
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, w, h)
  drawSparkline(ctx, props.values, { width: w, height: h, baseline: props.baseline })
}

function schedule(): void {
  if (raf !== 0) return
  raf = window.requestAnimationFrame(() => {
    raf = 0
    draw()
  })
}

watch([() => props.values, () => props.width, () => props.height, () => props.baseline], schedule)
onMounted(schedule)
onBeforeUnmount(() => {
  if (raf !== 0) window.cancelAnimationFrame(raf)
})
</script>

<template>
  <canvas
    ref="canvasEl"
    class="spark"
    role="img"
    :aria-label="'近 ' + values.length + ' 日走势'"
    :style="{ width: width + 'px', height: height + 'px' }"
  />
</template>

<style scoped>
  .spark {
    display: block;
    vertical-align: middle;
  }
</style>
