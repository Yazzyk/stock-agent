<!-- 报告册 = 交付物册：只放**根会话（主控）的最终交付物**。
     子智能体的过程报告不进这里（stores/agent 的 childReports），它们在「编队」节点详情与「轨迹」里看。
     版式：同一会话多次运行时，顶部保留一条 tab 带，按时间倒序标「第 N 轮」；
     只有一轮时不摆 tab 带，直接在正文头写「最终交付物」。报告多时 tab 带横向滚动，正文单独滚动。 -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { markdownHtml, type ReportEntry } from '../../lib/agentview'
import { fmtDateTime } from '../../lib/format'
import { shortTokens } from '../../lib/api.agent'

const props = withDefaults(
  defineProps<{
    reports: ReportEntry[]
    /** 外部指定高亮报告 id（为空时用最新一份） */
    activeId?: string
    /** 正在运行：空态文案要说清「还在生成」 */
    running?: boolean
    /** 子智能体过程报告的份数：只用来在页脚给一句极轻的入口提示，不进正文 */
    childCount?: number
  }>(),
  { activeId: '', running: false, childCount: 0 },
)

const emit = defineEmits<{
  (e: 'select', id: string): void
  /** 页脚提示：子智能体过程见编队 / 轨迹 */
  (e: 'open-formation'): void
  (e: 'open-trajectory'): void
}>()

const copied = ref('')
const pickedId = ref('')

/** 列表按时间升序存：最新的排最前（阅读习惯），序号仍按时间顺序。 */
const ordered = computed(() => [...props.reports].reverse())
const latestId = computed(() => props.reports[props.reports.length - 1]?.id ?? '')

/** 按时间顺序给一个稳定轮次号（第 1 轮最早，第 N 轮最新）：一次运行 = 一份交付物。 */
function roundOf(r: ReportEntry): number {
  return props.reports.findIndex((x) => x.id === r.id) + 1
}

/** 当前展示哪一份：外部 activeId 优先，其次用户点选，最后最新。 */
const selectedId = computed(() => {
  const list = props.reports
  // 用户点选优先（否则点旧报告会被外部 activeId 拉回最新），其次外部 activeId，最后最新。
  if (pickedId.value !== '' && list.some((r) => r.id === pickedId.value)) return pickedId.value
  if (props.activeId !== '' && list.some((r) => r.id === props.activeId)) return props.activeId
  return latestId.value
})

const selected = computed(() => props.reports.find((r) => r.id === selectedId.value) ?? null)

/** 列表变化（切换会话 / 新报告到达）：选中项失效时回到最新一份。 */
watch(
  () => props.reports.map((r) => r.id).join('|'),
  (key, prev) => {
    if (key === prev) return
    // 新报告到达 / 切换会话：回到最新一份
    pickedId.value = ''
  },
  { immediate: true },
)

/** 点 tab：更新本地选中，同时通知宿主（宿主可以顺手更新 activeReportId）。 */
function select(r: ReportEntry): void {
  pickedId.value = r.id
  emit('select', r.id)
}

/** 时间只取 HH:MM，tab 带里空间有限。 */
function clockOf(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const d = new Date(t)
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  return p(d.getHours()) + ':' + p(d.getMinutes())
}

/** 复制一份报告正文；1.4 秒后按钮恢复。 */
async function copy(r: ReportEntry | null): Promise<void> {
  if (!r) return
  try {
    await navigator.clipboard.writeText(r.content)
    copied.value = r.id
    window.setTimeout(() => {
      if (copied.value === r.id) copied.value = ''
    }, 1400)
  } catch {
    copied.value = ''
  }
}

/** 正文里的代码块复制按钮：事件委托（v-html 里不挂 Vue 事件）。 */
function onBodyClick(e: MouseEvent): void {
  const target = e.target as HTMLElement | null
  const btn = target?.closest('[data-copy-code]') as HTMLElement | null
  if (!btn) return
  const block = btn.closest('.codeblk')
  const pre = block?.querySelector('pre')
  const text = pre?.textContent ?? ''
  if (text === '') return
  void navigator.clipboard.writeText(text).then(() => {
    btn.textContent = '已复制'
    window.setTimeout(() => {
      btn.textContent = '复制'
    }, 1200)
  })
}
</script>

<template>
  <div class="rd">
    <div class="rd-hd">
      <span class="rd-count">最终交付物</span>
      <span v-if="reports.length > 1" class="tiny muted">共 {{ reports.length }} 轮</span>
      <span v-if="latestId" class="tiny muted">· 最新 {{ fmtDateTime(reports[reports.length - 1]?.at) }}</span>
      <span class="spacer" />
      <span v-if="reports.length > 1" class="tiny muted">按时间倒序 · 点标签切轮次</span>
      <button class="btn btn-sm" type="button" :disabled="selected === null" @click="copy(selected)">
        {{ copied !== '' ? '已复制' : '复制当前' }}
      </button>
    </div>

    <div v-if="reports.length === 0" class="rd-empty">
      <div class="rd-empty-title">还没有交付物</div>
      <p class="rd-empty-text">
        <template v-if="running">RouterAgent 正在生成，结束后交付物会出现在这里；不用刷新。</template>
        <template v-else>报告是交付物：一次运行结束后自动保存一份最终结论（结论 / 依据 / 口径与时间戳 / 风险与待验证点）。
          子智能体的过程产出不进这里，去「编队」节点详情或「轨迹」看。</template>
      </p>
    </div>

    <template v-else>
      <!-- 轮次标签带：同一会话多次运行才有意义；只有一轮时不摆，避免「1 份也要点一下」 -->
      <div v-if="reports.length > 1" class="rd-strip" role="tablist" aria-label="交付物轮次">
        <button
          v-for="r in ordered"
          :key="r.id"
          class="rd-tab"
          :class="{ on: r.id === selectedId }"
          type="button"
          role="tab"
          :aria-selected="r.id === selectedId"
          :title="'第 ' + roundOf(r) + ' 轮 · ' + (r.worker || 'RouterAgent') + ' · ' + fmtDateTime(r.at)"
          @click="select(r)"
        >
          <span class="rd-tab-no mono">第 {{ roundOf(r) }} 轮</span>
          <span class="rd-tab-worker ellipsis">{{ r.worker || 'RouterAgent' }}</span>
          <span class="rd-tab-time tiny">{{ clockOf(r.at) }}</span>
          <span v-if="r.id === latestId" class="rd-tab-latest">最新</span>
          <span v-if="r.tokens > 0" class="rd-tab-metric tiny">{{ shortTokens(r.tokens) }}</span>
        </button>
      </div>

      <!-- 正文：选中那份交付物 -->
      <div v-if="selected" class="rd-pane">
        <div class="rd-body-hd">
          <span v-if="reports.length > 1" class="rd-no mono">第 {{ roundOf(selected) }} 轮</span>
          <span class="rd-worker ellipsis">{{ selected.worker || 'RouterAgent' }}</span>
          <span v-if="selected.sessionId" class="rd-sid mono" :title="selected.sessionId">{{ selected.sessionId.slice(-8) }}</span>
          <span class="rd-time tiny muted">{{ fmtDateTime(selected.at) }}</span>
          <span v-if="selected.steps > 0" class="rd-metric tiny">· {{ selected.steps }} 步</span>
          <span v-if="selected.tokens > 0" class="rd-metric tiny">· {{ shortTokens(selected.tokens) }} tok</span>
          <span v-if="selected.id === latestId" class="tag tag-accent">最终</span>
          <span v-if="selected.fromReasoning" class="tag tag-accent">推理回退</span>
          <span class="spacer" />
          <button class="btn btn-sm btn-ghost" type="button" @click="copy(selected)">
            {{ copied === selected.id ? '已复制' : '复制' }}
          </button>
        </div>
        <div class="rd-content md" @click="onBodyClick">
          <div v-html="markdownHtml(selected.content)" />
        </div>
        <!-- 极轻的过程入口（不塞正文）：子智能体的产出不进交付物，但必须找得到 -->
        <div v-if="childCount > 0" class="rd-foot">
          <span class="tiny muted">子智能体过程产出 {{ childCount }} 份（不进交付物）</span>
          <button class="rd-foot-link" type="button" @click="emit('open-formation')">编队 →</button>
          <button class="rd-foot-link" type="button" @click="emit('open-trajectory')">轨迹 →</button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.rd {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}
.rd-hd {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
  min-height: 38px;
  padding: 6px 14px;
  border-bottom: 1px solid var(--glass-line);
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.03), transparent);
}
.rd-count {
  color: var(--text);
  font: 600 12px/1 var(--mono);
  letter-spacing: 0.2px;
  font-variant-numeric: tabular-nums;
}

/* 页脚：子智能体过程的极轻入口 */
.rd-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
  padding: 5px 16px;
  border-top: 1px solid var(--glass-line);
  background: rgba(255, 255, 255, 0.012);
}
.rd-foot-link {
  height: 19px;
  padding: 0 7px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--muted);
  font-size: 10.5px;
  transition: color 0.14s var(--ease), border-color 0.14s var(--ease);
}
.rd-foot-link:hover {
  color: var(--text);
  border-color: var(--line-3);
}

/* ---------- 报告标签带 ---------- */
.rd-strip {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 0 0 auto;
  padding: 8px 14px;
  border-bottom: 1px solid var(--glass-line);
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: thin;
}
.rd-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: 0 0 auto;
  height: 28px;
  max-width: 240px;
  padding: 0 9px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-pill);
  background: var(--glass-1);
  color: var(--text-2);
  font-size: 11.5px;
  white-space: nowrap;
  transition: border-color 0.14s var(--ease), background 0.14s var(--ease), color 0.14s var(--ease);
}
.rd-tab:hover {
  border-color: var(--line-3);
  background: var(--glass-2);
  color: var(--text);
}
.rd-tab.on {
  border-color: rgba(233, 180, 76, 0.5);
  background: var(--accent-soft);
  color: var(--accent-2);
}
.rd-tab-no {
  color: inherit;
  font-size: 10.5px;
  font-weight: 600;
}
.rd-tab-worker {
  min-width: 0;
  max-width: 96px;
}
.rd-tab-time,
.rd-tab-metric {
  color: var(--muted);
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}
.rd-tab.on .rd-tab-time,
.rd-tab.on .rd-tab-metric {
  color: rgba(245, 204, 124, 0.75);
}
.rd-tab-latest {
  padding: 0 5px;
  border-radius: 5px;
  background: rgba(233, 180, 76, 0.22);
  color: #ffe2a6;
  font-size: 9.5px;
  line-height: 15px;
}

/* ---------- 正文 ---------- */
.rd-pane {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.rd-body-hd {
  display: flex;
  align-items: center;
  gap: 7px;
  flex: 0 0 auto;
  min-height: 34px;
  padding: 4px 16px;
  border-bottom: 1px solid var(--glass-line);
  background: rgba(255, 255, 255, 0.012);
}
.rd-no {
  color: var(--accent);
  font-size: 11px;
  font-weight: 600;
}
.rd-worker {
  max-width: 220px;
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
}
.rd-sid {
  flex: 0 0 auto;
  color: var(--faint);
  font-size: 10px;
  letter-spacing: 0.2px;
}
.rd-time {
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
}
.rd-metric {
  flex: 0 0 auto;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.rd-content {
  flex: 1 1 auto;
  min-height: 0;
  width: 100%;
  max-width: 980px;
  margin: 0 auto;
  padding: 16px 18px 28px;
  overflow-y: auto;
}
.rd-empty {
  flex: 1 1 auto;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 30px 24px;
  text-align: center;
}
.rd-empty-title {
  color: var(--text-2);
  font-size: 13px;
  font-weight: 600;
}
.rd-empty-text {
  max-width: 420px;
  margin: 0;
  color: var(--muted);
  font-size: 11.5px;
  line-height: 1.75;
}

/* 报告正文的代码块：玻璃面板 + 头部语言标签 + 复制按钮 */
.rd :deep(.codeblk) {
  margin: 8px 0;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--bg-2);
  overflow: hidden;
}
.rd :deep(.codeblk-hd) {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 24px;
  padding: 0 8px;
  border-bottom: 1px solid var(--glass-line);
  background: var(--glass-2);
}
.rd :deep(.codeblk-lang) {
  color: var(--muted);
  font: 10px/1 var(--mono);
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.rd :deep(.codeblk-copy) {
  margin-left: auto;
  height: 18px;
  padding: 0 6px;
  border: 1px solid var(--line-2);
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  font-size: 10px;
}
.rd :deep(.codeblk-copy:hover) {
  color: var(--text);
  border-color: var(--line-3);
}
.rd :deep(.md-h) {
  margin: 12px 0 5px;
  font-size: 13px;
}
.rd :deep(.md-p) {
  margin: 0 0 7px;
}
.rd :deep(.md-pre) {
  margin: 0;
  padding: 8px 10px;
  overflow-x: auto;
  border: 0;
  border-radius: 0;
  background: transparent;
  font: 11.5px/1.65 var(--mono);
  color: var(--text-2);
}
</style>
