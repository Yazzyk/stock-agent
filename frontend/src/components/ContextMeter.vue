<!-- 上下文环：标题栏一枚小环 + 点击弹出一个紧凑洞察面板（≤420px 高）。
     环与面板的数字都来自 lib/contextview.ts（ctxUsed/ctxPercent），与工作台的「上下文」页共用一套口径。
     紧凑面板只放占比句 / 数字+口径 / 堆叠条 / 三行图例 / 缓存命中，底部 emit open 让宿主切到「上下文」标签。
     降级：没有快照/窗口未知时环仍显示（「—」或 `~已用`），弹层给中文空态，不会白屏。 -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { AgentContext } from '../lib/api.agent'
import { ctxExact, ctxPercent, ctxSampleLabel, ctxTone, ctxUsed, ctxWindow, formatTok, percentText } from '../lib/contextview'
import ContextInsight from './ContextInsight.vue'

const props = withDefaults(defineProps<{ context: AgentContext | null; compact?: boolean }>(), { compact: false })
const emit = defineEmits<{ (e: 'refresh'): void; (e: 'open'): void }>()

const open = ref(false)
const rootEl = ref<HTMLElement | null>(null)
const pos = ref({ left: 0, top: 0, width: 300 })

// 同一页面可能同时存在两个环（工作台 + 抽屉），弹层 id 必须唯一
const panelId = 'ctx-meter-' + Math.random().toString(36).slice(2, 9)

/** 弹层宽度：300~320px（紧凑版只放占比/图例，300px 正好）。 */
const PANEL_W = 300
/** 估算高度：紧凑面板约 260px，用 300 做上下翻转判断（真实高度由 CSS max-height 兜底）。 */
const PANEL_H = 300

const RADIUS = 5.5
const CIRC = 2 * Math.PI * RADIUS

const win = computed(() => ctxWindow(props.context))
const hasWindow = computed(() => win.value > 0)
const used = computed(() => ctxUsed(props.context))
const exact = computed(() => ctxExact(props.context))
const sampleLabel = computed(() => ctxSampleLabel(props.context))
const percent = computed(() => ctxPercent(props.context))
const pctText = computed(() => percentText(percent.value))
const tone = computed(() => ctxTone(percent.value))
const dash = computed(() => (CIRC * percent.value) / 100 + ' ' + CIRC)

/** 环内文字：有窗口显示占比，没有窗口显示 `已用`（估算才带 ~），连快照都没有就是「—」。 */
const ringText = computed(() => {
  if (!props.context) return '—'
  if (hasWindow.value) return pctText.value
  return (exact.value ? '' : '~') + formatTok(used.value)
})

const hintText = computed(() => {
  if (!props.context) return '还没有上下文快照（点击查看）'
  if (!hasWindow.value) return '上下文约 ' + used.value + ' token（窗口未知，点击看洞察）'
  return '上下文已用 ' + pctText.value + '（' + sampleLabel.value + '口径，点击看洞察）'
})

/** 弹层定位：默认右上对齐向下展开；下方放不下就向上；再放不下就贴边并压缩高度（内部滚动）。 */
function place(): void {
  const el = rootEl.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const vw = window.innerWidth
  const vh = window.innerHeight
  const width = Math.min(PANEL_W, Math.max(264, vw - 16))
  const left = Math.min(Math.max(8, r.right - width), Math.max(8, vw - width - 8))
  const below = r.bottom + 8
  const above = r.top - PANEL_H - 8
  // 下方放不下就向上翻；高度由 CSS max-height: min(60vh, 420px) 兜底（面板本身不会超出视口）
  const top = below + PANEL_H > vh && above > 8 ? above : Math.max(8, below)
  pos.value = { left, top, width }
}

function toggle(): void {
  open.value = !open.value
  if (open.value) place()
}

function close(): void {
  open.value = false
}

/** 打开完整洞察：先收起弹层，再把 open 交给宿主（agentlab 用它切 tab='context'）。 */
function openFull(): void {
  open.value = false
  emit('open')
}

function refresh(): void {
  emit('refresh')
}

function onDocPointer(e: MouseEvent): void {
  const el = rootEl.value
  if (!el) return
  if (el.contains(e.target as Node)) return
  const panel = document.getElementById(panelId)
  if (panel && panel.contains(e.target as Node)) return
  open.value = false
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') open.value = false
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointer, true)
  window.addEventListener('keydown', onKey)
  window.addEventListener('resize', place)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocPointer, true)
  window.removeEventListener('keydown', onKey)
  window.removeEventListener('resize', place)
})

// 会话切换导致上下文消失时收起弹层，避免留下空壳
watch(
  () => props.context?.sessionId ?? '',
  () => {
    if (!props.context) open.value = false
  },
)
</script>

<template>
  <span ref="rootEl" class="ctx-meter">
    <button
      class="ctx-btn"
      type="button"
      :aria-expanded="open"
      :aria-controls="panelId"
      :title="hintText"
      @click.stop="toggle"
    >
      <svg viewBox="0 0 14 14" width="14" height="14" aria-hidden="true">
        <circle cx="7" cy="7" :r="RADIUS" fill="none" stroke="rgba(148,163,184,.25)" stroke-width="2" />
        <circle
          v-if="hasWindow && percent > 0"
          cx="7"
          cy="7"
          :r="RADIUS"
          fill="none"
          :stroke="tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--accent)' : 'var(--info)'"
          stroke-width="2"
          stroke-linecap="round"
          :stroke-dasharray="dash"
          transform="rotate(-90 7 7)"
        />
      </svg>
      <span class="ctx-pct num" :class="hasWindow ? tone : ''">{{ ringText }}</span>
      <span v-if="!compact && hasWindow" class="ctx-label tiny muted">上下文</span>
    </button>

    <teleport to="body">
      <div
        v-if="open"
        :id="panelId"
        class="ctx-panel"
        :style="{ left: pos.left + 'px', top: pos.top + 'px', width: pos.width + 'px' }"
        role="dialog"
        aria-modal="false"
        aria-label="上下文洞察"
      >
        <div class="ctx-hd">
          <span class="ctx-hd-title">上下文洞察</span>
          <span class="ctx-hd-model mono ellipsis">{{ context?.model || '模型未知' }}</span>
          <span class="spacer" />
          <button class="ctx-refresh" type="button" title="重新读取上下文快照" @click="refresh">刷新</button>
          <button class="ctx-close" type="button" aria-label="关闭上下文洞察" @click="close">×</button>
        </div>
        <ContextInsight :context="context" variant="popover" @refresh="refresh" @open="openFull" />
      </div>
    </teleport>
  </span>
</template>

<style scoped>
.ctx-meter {
  display: inline-flex;
  align-items: center;
  position: relative;
}
.ctx-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 24px;
  padding: 0 7px;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--text-2);
  transition: border-color 0.14s var(--ease), background 0.14s var(--ease);
}
.ctx-btn:hover {
  border-color: var(--glass-line);
  background: var(--glass-1);
}
.ctx-pct {
  font: 11.5px/1 var(--mono);
  font-variant-numeric: tabular-nums;
  color: var(--text-2);
}
.ctx-pct.warn {
  color: var(--accent);
}
.ctx-pct.err {
  color: var(--err);
}
.ctx-label {
  letter-spacing: 0.3px;
}

/* ---------- 弹层 ---------- */
.ctx-panel {
  position: fixed;
  z-index: 90;
  max-width: calc(100vw - 16px);
  /* 紧凑弹层：最高 420px / 60vh，内部滚动；不会盖住底部输入区 */
  max-height: min(60vh, 420px);
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 11px 11px;
  border: 1px solid var(--glass-line);
  border-radius: 14px;
  background: var(--glass-2);
  backdrop-filter: blur(var(--blur-1)) saturate(130%);
  -webkit-backdrop-filter: blur(var(--blur-1)) saturate(130%);
  box-shadow: var(--shadow-2);
  animation: ctx-in 0.14s var(--ease);
}
@keyframes ctx-in {
  from {
    opacity: 0;
    transform: translateY(-3px);
  }
}
.ctx-hd {
  display: flex;
  align-items: center;
  gap: 6px;
  padding-bottom: 7px;
  border-bottom: 1px solid var(--glass-line);
  position: sticky;
  top: -10px;
  margin-top: -10px;
  padding-top: 10px;
  background: var(--glass-2);
  z-index: 1;
}
.ctx-hd-title {
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
  white-space: nowrap;
}
.ctx-hd-model {
  color: var(--muted);
  font-size: 10.5px;
  min-width: 0;
}
.ctx-refresh {
  height: 20px;
  padding: 0 6px;
  border: 1px solid transparent;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--muted);
  font-size: 10.5px;
  white-space: nowrap;
}
.ctx-refresh:hover {
  border-color: var(--glass-line);
  color: var(--text);
}
.ctx-close {
  width: 20px;
  height: 20px;
  border: 1px solid transparent;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--muted);
  font-size: 14px;
  line-height: 1;
}
.ctx-close:hover {
  border-color: var(--glass-line);
  color: var(--text);
}
</style>
