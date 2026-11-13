<!-- 对话转写：照 Pi Agent 桌面端的版式——
     用户消息是右对齐气泡，助手消息不是气泡（首行元信息 + 正文），工具调用是助手块里的紧凑行，
     状态行是极小 chip。滚动只在「用户本来就在底部」时跟随。 -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { buildChat, chatTextOf, formatMs, markdownHtml, oneLine, stateTone, toneClass, toneDot, type ChatItem, type ChatToolPart } from '../../lib/agentview'
import type { LogLine } from '../../stores/agent'

const props = withDefaults(
  defineProps<{
    lines: LogLine[]
    /** 当前 worker（助手消息里标「RouterAgent / worker 名」） */
    worker?: string
    /** 当前模型（消息元信息） */
    model?: string
    /** 运行中：空态文案不同 */
    running?: boolean
    /** 滚动触发器：日志长度 + 末行长度（流式段落长度不变但文本在涨） */
    tick?: string
    /** worker id → 展示名（历史会话里落盘的是 id，这里换成中文标题） */
    workerNames?: Record<string, string>
    /** 运行状态文案：思考中 / 调用工具 / 等待子智能体…（DSH 式状态行） */
    state?: string
    /** 本轮已经跑了多久（毫秒）；0 表示不显示计时 */
    elapsedMs?: number
  }>(),
  { worker: '', model: '', running: false, tick: '', workerNames: () => ({}), state: '', elapsedMs: 0 },
)

const emit = defineEmits<{ (e: 'suggest', text: string): void }>()

/** 距底 ≤ 40px 视为「用户在看最新一条」，与 Pi 的滚动策略一致。 */
const BOTTOM_GAP = 40
const scroller = ref<HTMLElement | null>(null)
const followsTail = ref(true)
const copied = ref('')
const expanded = ref<string[]>([])

const items = computed<ChatItem[]>(() =>
  buildChat(props.lines, { worker: props.worker || 'RouterAgent', model: props.model }),
)

const suggestions = [
  { label: '盘前计划', text: '帮我做今天的盘前计划：梳理自选与主线板块的消息面、资金面，给一份观察清单。' },
  { label: '盘中异动', text: '盯一下自选里的盘中异动：涨跌幅或量比异常的标的，说明资金方向。' },
  { label: '收盘复盘', text: '做今天的收盘复盘：对照盘前计划写偏差归因，给出明天要跟踪的变量。' },
  { label: 'K 线结构', text: '看一下 600519 的日线结构：均线排列、量能、关键支撑与压力位。' },
]

/** 判断是否贴底：scrollTop + clientHeight 与 scrollHeight 的差值。 */
function atBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_GAP
}

function onScroll(): void {
  const el = scroller.value
  if (!el) return
  followsTail.value = atBottom(el)
}

function scrollToBottom(smooth = false): void {
  const el = scroller.value
  if (!el) return
  el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
  followsTail.value = true
}

/** 流式追加：只有用户原本就在底部才跟随，否则不动正在阅读的位置。 */
watch(
  () => props.tick,
  async () => {
    if (!followsTail.value) return
    await nextTick()
    scrollToBottom(false)
  },
)

onMounted(async () => {
  await nextTick()
  scrollToBottom(false)
})

function expand(key: string): void {
  expanded.value = expanded.value.includes(key) ? expanded.value.filter((k) => k !== key) : [...expanded.value, key]
}

function isExpanded(key: string): boolean {
  return expanded.value.includes(key)
}

/** 工具行状态标记：失败红 / 运行中脉冲 / 完成勾。 */
function toolState(t: ChatToolPart['tool']): string {
  if (!t.ok) return 'err'
  if (t.phase === 'start') return 'run'
  return 'ok'
}

async function copyItem(item: ChatItem): Promise<void> {
  const text = chatTextOf(item)
  if (text.trim() === '') return
  try {
    await navigator.clipboard.writeText(text)
    copied.value = item.key
    window.setTimeout(() => {
      if (copied.value === item.key) copied.value = ''
    }, 1400)
  } catch {
    copied.value = ''
  }
}

/** 正文代码块的复制（v-html 里用事件委托）。 */
function onBodyClick(e: MouseEvent): void {
  const btn = (e.target as HTMLElement | null)?.closest('[data-copy-code]') as HTMLElement | null
  if (!btn) return
  const text = btn.closest('.codeblk')?.querySelector('pre')?.textContent ?? ''
  if (text === '') return
  void navigator.clipboard.writeText(text).then(() => {
    btn.textContent = '已复制'
    window.setTimeout(() => {
      btn.textContent = '复制'
    }, 1200)
  })
}

function pickSuggest(text: string): void {
  emit('suggest', text)
}

/** 运行中状态行的计时文案：0s / 12s / 1m20s。 */
const elapsedLabel = computed(() => {
  const ms = props.elapsedMs
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const sec = Math.floor(ms / 1000)
  if (sec < 60) return sec + 's'
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return m + 'm' + (s > 0 ? s + 's' : '')
})

/** 助手消息首行的作者：优先翻译成 worker 标题，未知 id 原样显示。 */
function workerLabel(id: string): string {
  if (id === '') return props.worker || 'RouterAgent'
  return props.workerNames[id] || id
}

/** live 状态行的色调：只走 lib/agentview 的唯一一套映射（思考中=琥珀、已完成=绿……）。 */
const liveTone = computed(() => stateTone(props.state || 'thinking'))
/** 状态 chip 的色调：运行中=琥珀、完成=绿、失败=红（不再把「已完成」写成中性文字色）。 */
function statusTone(state: 'run' | 'ok' | 'error'): string {
  return toneClass(stateTone(state === 'run' ? 'running' : state === 'ok' ? 'done' : 'error'))
}
</script>

<template>
  <div class="ct">
    <div ref="scroller" class="ct-scroll" @scroll.passive="onScroll">
      <div v-if="items.length === 0" class="ct-empty">
        <div class="ct-brand">
          <span class="ct-brand-mark">观</span>
        </div>
        <div class="ct-empty-title">和 RouterAgent 说一句话</div>
        <p class="ct-empty-text">RouterAgent 会拆解目标、分派给合适的 worker 或按需新建，最后把结论汇总回来。</p>
        <div class="ct-suggest">
          <button v-for="s in suggestions" :key="s.label" class="ct-suggest-chip" type="button" @click="pickSuggest(s.text)">
            {{ s.label }}
          </button>
        </div>
      </div>

      <div v-else class="ct-flow">
        <template v-for="item in items" :key="item.key">
          <!-- 用户消息：右对齐气泡 + 右下角元信息 -->
          <div v-if="item.kind === 'user'" class="ct-user">
            <div class="ct-bubble">{{ item.text }}</div>
            <div class="ct-user-meta">{{ item.at }} · 你</div>
          </div>

          <!-- 助手消息：不是气泡；首行元信息 + 正文 + 工具行 + 状态 chip -->
          <div v-else class="ct-assistant">
            <div class="ct-meta">
              <span class="ct-who">{{ workerLabel(item.worker) }}</span>
              <span v-if="item.model" class="ct-meta-item mono">{{ item.model }}</span>
              <span class="ct-meta-item">{{ item.at }}</span>
              <span v-if="item.steps > 0" class="ct-meta-item">{{ item.steps }} 步</span>
              <span v-if="item.tokens > 0" class="ct-meta-item">~{{ item.tokens }} tok</span>
              <span class="spacer" />
              <button class="ct-copy" type="button" :title="'复制这条消息'" @click="copyItem(item)">
                {{ copied === item.key ? '已复制' : '复制' }}
              </button>
            </div>

            <div class="ct-parts" @click="onBodyClick">
              <template v-for="part in item.parts" :key="part.key">
                <div v-if="part.type === 'text'" class="ct-text md" :class="{ streaming: part.streaming }">
                  <div v-html="markdownHtml(part.text)" />
                </div>

                <div v-else-if="part.type === 'tool'" class="ct-tool" :class="[toolState(part.tool), { open: isExpanded(part.key) }]">
                  <button class="ct-tool-row" type="button" :title="oneLine(part.tool.args + ' → ' + (part.tool.ok ? part.tool.result : part.tool.error), 300)" @click="expand(part.key)">
                    <span class="ct-tool-caret">{{ isExpanded(part.key) ? '▾' : '▸' }}</span>
                    <span class="ct-tool-name">{{ part.tool.name }}</span>
                    <span v-if="part.tool.args" class="ct-tool-args">{{ oneLine(part.tool.args, 120) }}</span>
                    <span class="ct-tool-arrow">→</span>
                    <span class="ct-tool-result">
                      <template v-if="part.tool.phase === 'start'">运行中…</template>
                      <template v-else-if="part.tool.ok">{{ oneLine(part.tool.result, 140) || '无输出' }}</template>
                      <template v-else>{{ oneLine(part.tool.error || part.tool.result, 140) || '失败' }}</template>
                    </span>
                    <span v-if="part.tool.ms > 0" class="ct-tool-ms">{{ formatMs(part.tool.ms) }}</span>
                  </button>
                  <div v-if="isExpanded(part.key)" class="ct-tool-body">
                    <div class="ct-tool-section">
                      <span class="ct-tool-label">参数</span>
                      <pre class="ct-pre">{{ part.tool.args || '（无参数）' }}</pre>
                    </div>
                    <div class="ct-tool-section">
                      <span class="ct-tool-label">{{ part.tool.ok ? '结果' : '错误' }}</span>
                      <pre class="ct-pre">{{ (part.tool.ok ? part.tool.result : part.tool.error || part.tool.result) || '（无输出）' }}</pre>
                    </div>
                  </div>
                </div>

                <div v-else class="ct-status" :class="[part.state, statusTone(part.state)]">
                  <span v-if="part.state === 'run'" class="ct-status-dot" />
                  <span v-else class="ct-status-icon">{{ part.state === 'ok' ? '✓' : '×' }}</span>
                  <span class="ct-status-label">{{ part.label }}</span>
                  <span v-if="part.detail" class="ct-status-detail">{{ part.detail }}</span>
                  <span v-if="part.repeat > 1" class="ct-status-repeat">×{{ part.repeat }}</span>
                </div>
              </template>
            </div>
          </div>
        </template>

        <!-- 运行中状态行：色点 + 状态文案 + 已用时间（色调跟唯一一套状态映射） -->
        <div v-if="running" class="ct-live" :class="toneClass(liveTone)" role="status" aria-live="polite">
          <span class="ct-live-dot" :class="toneDot(liveTone)" aria-hidden="true" />
          <span class="ct-live-text">{{ state || '思考中' }}…</span>
          <span v-if="elapsedLabel" class="ct-live-time mono">{{ elapsedLabel }}</span>
          <span class="ct-live-hint">可继续输入插话</span>
        </div>
      </div>
    </div>

    <button v-if="!followsTail && items.length > 0" class="ct-bottom" type="button" @click="scrollToBottom(true)">
      回到底部 ↓
    </button>
  </div>
</template>

<style scoped>
.ct {
  position: relative;
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}
.ct-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.ct-flow,
.ct-empty {
  max-width: 860px;
  margin: 0 auto;
  padding: 18px 22px 28px;
}
.ct-flow {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

/* ---------- 运行中状态行 ---------- */
.ct-live {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 2px 2px 0;
  font-size: 12px;
  color: var(--text-3);
}
.ct-live-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent) 55%, transparent);
  animation: ct-pulse 1.6s ease-out infinite;
}
.ct-live-text {
  color: var(--text-2);
}
/* 状态色调（唯一一套，来自 lib/agentview 的 stateTone）：已完成为绿、进行中为琥珀、失败为红、空闲为灰。
   文字用比圆点略亮的同色系，保证在深色底上的对比度 ≥ 4.5:1（#4ce0ab ≈ 11:1）。 */
.tone-ok {
  color: #4ce0ab;
}
.tone-warn {
  color: var(--accent-2);
}
.tone-err {
  color: var(--err);
}
.tone-dim {
  color: var(--muted);
}
.ct-live.tone-ok .ct-live-text {
  color: #4ce0ab;
}
.ct-live.tone-ok .ct-live-hint {
  color: rgba(76, 224, 171, 0.62);
}
.ct-live-time {
  color: var(--text-3);
}
.ct-live-hint {
  margin-left: auto;
  font-size: 11px;
  color: var(--text-3);
  opacity: 0.8;
}
@keyframes ct-pulse {
  0% {
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent) 55%, transparent);
  }
  70% {
    box-shadow: 0 0 0 6px transparent;
  }
  100% {
    box-shadow: 0 0 0 0 transparent;
  }
}

/* ---------- 用户消息 ---------- */
.ct-user {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
}
.ct-bubble {
  max-width: 76%;
  padding: 9px 13px;
  border: 1px solid rgba(233, 180, 76, 0.26);
  border-radius: 14px 14px 4px 14px;
  background: linear-gradient(180deg, rgba(233, 180, 76, 0.14), rgba(233, 180, 76, 0.07));
  color: var(--text);
  font-size: 13px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}
.ct-user-meta {
  color: var(--faint);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}

/* ---------- 助手消息 ---------- */
.ct-assistant {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.ct-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 18px;
  color: var(--muted);
  font-size: 10.5px;
}
.ct-who {
  color: var(--text-2);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.2px;
}
.ct-meta-item {
  font-variant-numeric: tabular-nums;
}
.ct-copy {
  flex: 0 0 auto;
  height: 18px;
  padding: 0 6px;
  border: 1px solid transparent;
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  font-size: 10px;
  opacity: 0;
  transition: opacity 0.14s var(--ease), color 0.14s var(--ease);
}
.ct-assistant:hover .ct-copy,
.ct-copy:focus-visible {
  opacity: 1;
}
.ct-copy:hover {
  color: var(--text);
  border-color: var(--line-2);
  background: var(--panel-2);
}
.ct-parts {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.ct-text {
  color: var(--text-2);
  font-size: 13px;
  line-height: 1.8;
  word-break: break-word;
}
/* 正文里的代码块：玻璃面板 + 头部语言标签 + 复制按钮（与报告册同一套观感） */
.ct-text :deep(.codeblk) {
  margin: 8px 0;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--bg-2);
  overflow: hidden;
}
.ct-text :deep(.codeblk-hd) {
  display: flex;
  align-items: center;
  height: 24px;
  padding: 0 8px;
  border-bottom: 1px solid var(--glass-line);
  background: var(--glass-2);
}
.ct-text :deep(.codeblk-lang) {
  color: var(--muted);
  font: 10px/1 var(--mono);
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.ct-text :deep(.codeblk-copy) {
  margin-left: auto;
  height: 18px;
  padding: 0 6px;
  border: 1px solid var(--line-2);
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  font-size: 10px;
}
.ct-text :deep(.codeblk-copy:hover) {
  color: var(--text);
  border-color: var(--line-3);
}
.ct-text :deep(.md-pre) {
  margin: 0;
  padding: 8px 10px;
  border: 0;
  border-radius: 0;
  background: transparent;
  overflow-x: auto;
  font: 11.5px/1.65 var(--mono);
  color: var(--text-2);
}
/* 流式光标挂在最后一个块的行尾，避免单独占一行 */
.ct-text.streaming :deep(.md > *:last-child)::after {
  content: '▍';
  margin-left: 1px;
  color: var(--accent-2);
  animation: ct-blink 1.1s steps(1) infinite;
}
@keyframes ct-blink {
  50% {
    opacity: 0;
  }
}

/* ---------- 工具调用行 ---------- */
.ct-tool {
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  overflow: hidden;
}
.ct-tool.err {
  border-color: rgba(255, 90, 95, 0.32);
  background: rgba(255, 90, 95, 0.05);
}
.ct-tool-row {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  height: 26px;
  padding: 0 9px;
  border: 0;
  background: transparent;
  color: var(--text-2);
  font: 11.5px/26px var(--mono);
  text-align: left;
  white-space: nowrap;
  overflow: hidden;
}
.ct-tool-row:hover {
  background: var(--glass-2);
}
.ct-tool-caret {
  flex: 0 0 auto;
  color: var(--muted);
  font-size: 10px;
}
.ct-tool-name {
  flex: 0 0 auto;
  color: var(--accent-2);
  font-weight: 500;
}
.ct-tool.err .ct-tool-name {
  color: var(--err);
}
.ct-tool-args {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--muted);
}
.ct-tool-arrow {
  flex: 0 0 auto;
  color: var(--faint);
}
.ct-tool-result {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text-2);
}
.ct-tool.err .ct-tool-result {
  color: var(--err);
}
.ct-tool.run .ct-tool-result {
  color: var(--accent-2);
}
.ct-tool-ms {
  flex: 0 0 auto;
  color: var(--muted);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}
.ct-tool-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 9px;
  border-top: 1px solid var(--glass-line);
  background: var(--bg-2);
}
.ct-tool-section {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.ct-tool-label {
  color: var(--muted);
  font-size: 10px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.ct-pre {
  max-height: 260px;
  margin: 0;
  padding: 7px 9px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  color: var(--text-2);
  font: 11px/1.6 var(--mono);
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
}

/* ---------- 状态 chip ---------- */
.ct-status {
  display: inline-flex;
  align-items: center;
  align-self: flex-start;
  gap: 5px;
  height: 20px;
  padding: 0 7px;
  border: 1px solid var(--glass-line);
  border-radius: 999px;
  background: var(--glass-1);
  color: var(--muted);
  font-size: 10.5px;
  white-space: nowrap;
  max-width: 100%;
  overflow: hidden;
}
.ct-status-dot {
  flex: 0 0 auto;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--muted);
}
.ct-status.run .ct-status-dot {
  background: var(--accent);
  box-shadow: 0 0 6px rgba(233, 180, 76, 0.6);
  animation: ct-pulse 1.4s ease-in-out infinite;
}
.ct-status-icon {
  flex: 0 0 auto;
  font-size: 10px;
  line-height: 1;
}
.ct-status.ok .ct-status-icon {
  color: var(--ok);
}
.ct-status.error .ct-status-icon {
  color: var(--err);
}
/* 状态 chip 的文字跟同一套色调（tone-* 由 lib/agentview 的 stateTone 给出）：完成绿 / 进行中琥珀 / 失败红 */
.ct-status.tone-ok .ct-status-label {
  color: #4ce0ab;
}
.ct-status.tone-warn .ct-status-label {
  color: var(--accent-2);
}
.ct-status.tone-err .ct-status-label {
  color: var(--err);
}
.ct-status-detail {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  font-family: var(--mono);
}
.ct-status-repeat {
  color: var(--faint);
  font-variant-numeric: tabular-nums;
}
@keyframes ct-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

/* ---------- 空态 ---------- */
.ct-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding-top: 64px;
  text-align: center;
}
.ct-brand {
  display: grid;
  place-items: center;
  width: 46px;
  height: 46px;
  border-radius: 16px;
  border: 1px solid rgba(233, 180, 76, 0.32);
  background: linear-gradient(150deg, rgba(233, 180, 76, 0.22), rgba(233, 180, 76, 0.05));
  box-shadow: 0 14px 34px rgba(233, 180, 76, 0.14), inset 0 1px 0 rgba(255, 255, 255, 0.09);
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
}
.ct-brand-mark {
  color: var(--accent-2);
  font-size: 20px;
  font-weight: 700;
  text-shadow: 0 0 18px rgba(233, 180, 76, 0.35);
}
.ct-empty-title {
  color: var(--text);
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.2px;
}
.ct-empty-text {
  max-width: 380px;
  margin: 0;
  color: var(--muted);
  font-size: 11.5px;
  line-height: 1.75;
}
.ct-suggest {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 7px;
  margin-top: 6px;
}
.ct-suggest-chip {
  height: 26px;
  padding: 0 11px;
  border: 1px solid var(--glass-line);
  border-radius: 999px;
  background: var(--glass-2);
  color: var(--text-2);
  font-size: 11.5px;
  transition: border-color 0.14s var(--ease), color 0.14s var(--ease), background 0.14s var(--ease);
}
.ct-suggest-chip:hover {
  border-color: rgba(233, 180, 76, 0.4);
  background: var(--accent-soft);
  color: var(--accent-2);
}

/* ---------- 回到底部 ---------- */
.ct-bottom {
  position: absolute;
  right: 18px;
  bottom: 12px;
  height: 27px;
  padding: 0 12px;
  border: 1px solid var(--glass-line);
  border-radius: 999px;
  background: var(--glass-3);
  backdrop-filter: blur(var(--blur-2)) saturate(140%);
  -webkit-backdrop-filter: blur(var(--blur-2)) saturate(140%);
  color: var(--text-2);
  font-size: 11px;
  box-shadow: 0 10px 24px rgba(0, 0, 0, 0.35);
}
.ct-bottom:hover {
  color: var(--text);
  border-color: var(--line-3);
}

@media (prefers-reduced-motion: reduce) {
  .ct-text.streaming :deep(.md > *:last-child)::after,
  .ct-status.run .ct-status-dot {
    animation: none;
  }
}
</style>
