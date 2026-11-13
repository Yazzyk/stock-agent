<!-- 轨迹视图：照 DSH「轨迹」页复刻——固定工具栏 + 三泳道密度条 + 按轮次分组的账本 + 右侧 inspector。
     数据来自 stores/agent 的日志行（含 agent.tool 合并后的工具条目），派生逻辑全部在 lib/agentview.ts。 -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import {
  filterTrajRows,
  formatMs,
  groupTurns,
  markdownHtml,
  roleLabel,
  splitStatus,
  stateLabel,
  stateTone,
  systemPromptOf,
  toneClass,
  toolText,
  trajRows,
  trajStats,
  trajectorySpans,
  type ReportEntry,
  type TrajRow,
  type TrajTurn,
} from '../../lib/agentview'
import type { LogLine } from '../../stores/agent'
import { fmtDateTime } from '../../lib/format'

const props = withDefaults(
  defineProps<{
    lines: LogLine[]
    /** 根会话交付物份数（顶部细文本） */
    reportsCount?: number
    /** 子智能体的过程报告：只在本页当过程证据展示，不进报告册 */
    childReports?: ReportEntry[]
    /** 子会话 id → 展示名（编队节点的 label），让子报告能对到具体子智能体 */
    childLabels?: Record<string, string>
    /** worker id → 展示名：老数据里子报告没有子会话 id 时用它兜底 */
    childWorkerLabels?: Record<string, string>
    /** 运行中：行尾光标与跟随策略 */
    running?: boolean
    /** 滚动触发器（长度 + 末行文本长度） */
    tick?: string
  }>(),
  { reportsCount: 0, childReports: () => [], childLabels: () => ({}), childWorkerLabels: () => ({}), running: false, tick: '' },
)

/** 只渲染最后 300 行：再多的话滚动条与选中回跳都会变慢（DSH 的阈值是 100 + 虚拟化）。 */
const MAX_ROWS = 300
/** 距底 ≤2px 视为跟随尾部（与 DSH 一致）。 */
const TAIL_THRESHOLD = 2

const query = ref('')
const collapsedTurns = ref<number[]>([])
const callsCollapsed = ref(false)
const expandedCalls = ref<number[]>([])
const selectedKey = ref('')
const scroller = ref<HTMLElement | null>(null)
const followsTail = ref(true)

const rows = computed<TrajRow[]>(() => trajRows(props.lines))
/** 系统提示词：会话的第一条事件，默认收起（可能上万字，展开会挤掉轨迹本体）。 */
const systemPrompt = computed(() => systemPromptOf(props.lines))
const sysOpen = ref(false)
const filtered = computed<TrajRow[]>(() => filterTrajRows(rows.value, query.value))
const stats = computed(() => trajStats(rows.value))
const spans = computed(() => trajectorySpans(rows.value))
const matched = computed(() => new Set(filtered.value.map((r) => r.key)))

/** 三条泳道（输入 / 模型 / 工具）：标签与色块分组后一次算好，模板里不再反复 filter。 */
const laneSpans = computed(() =>
  [
    { lane: 0, label: '输入' },
    { lane: 1, label: '模型' },
    { lane: 2, label: '工具' },
  ].map((l) => ({ ...l, spans: spans.value.filter((s) => s.lane === l.lane) })),
)

/** 显示行：普通行 + 折叠摘要行（轮次折叠 / 调用折叠）。 */
type DisplayRow =
  | { kind: 'row'; key: string; row: TrajRow; turnStart: boolean }
  /** 轮次摘要行：默认展开（只作分组头），折叠时下面不再出行 */
  | { kind: 'turn'; key: string; turn: TrajTurn; collapsed: boolean }
  | { kind: 'calls'; key: string; turn: TrajTurn }

const displayRows = computed<DisplayRow[]>(() => {
  const out: DisplayRow[] = []
  for (const turn of groupTurns(filtered.value)) {
    const collapsed = collapsedTurns.value.includes(turn.turn)
    // 摘要行始终在轮次开头：展开时是分组头，折叠时是唯一一行
    out.push({ kind: 'turn', key: 'turn-' + turn.turn, turn, collapsed })
    if (collapsed) continue
    const hideCalls = callsCollapsed.value && !expandedCalls.value.includes(turn.turn) && turn.tools > 0
    let firstRow = true
    for (const row of turn.rows) {
      if (hideCalls && row.role === 'tool') continue
      out.push({ kind: 'row', key: row.key, row, turnStart: firstRow })
      firstRow = false
    }
    if (hideCalls) out.push({ kind: 'calls', key: 'calls-' + turn.turn, turn })
  }
  return out
})

/** 超量时只保留最后 MAX_ROWS 行，并提示更早的行被折叠。 */
const olderCount = computed(() => Math.max(0, displayRows.value.length - MAX_ROWS))
const shownRows = computed<DisplayRow[]>(() => (olderCount.value > 0 ? displayRows.value.slice(-MAX_ROWS) : displayRows.value))
const collapsibleTurns = computed(() => groupTurns(filtered.value).filter((t) => t.rows.length > 1).map((t) => t.turn))

const selected = computed<TrajRow | null>(() => {
  const hit = rows.value.find((r) => r.key === selectedKey.value)
  return hit ?? null
})

/** 轮次折叠开关的当前状态：全部可折叠轮都折叠时显示为按下。 */
const allTurnsCollapsed = computed(
  () => collapsibleTurns.value.length > 0 && collapsibleTurns.value.every((t) => collapsedTurns.value.includes(t)),
)

function toggleAllTurns(): void {
  collapsedTurns.value = allTurnsCollapsed.value ? [] : [...collapsibleTurns.value]
}

/** 单轮折叠开关（摘要行点击）。 */
function toggleTurn(turn: number): void {
  collapsedTurns.value = collapsedTurns.value.includes(turn)
    ? collapsedTurns.value.filter((t) => t !== turn)
    : [...collapsedTurns.value, turn]
}

function toggleCalls(): void {
  callsCollapsed.value = !callsCollapsed.value
  expandedCalls.value = []
}

function expandCalls(turn: number): void {
  expandedCalls.value = [...expandedCalls.value, turn]
}

/** 表格行选中：同时关闭 inspector 之外的残影（点击已选中行取消选中）。 */
function pick(row: TrajRow): void {
  selectedKey.value = selectedKey.value === row.key ? '' : row.key
}

/** 密度条点击：选中对应行并滚到可视区（被 300 行截断时静默忽略滚动）。 */
async function pickSpan(lineId: number): Promise<void> {
  const row = rows.value.find((r) => r.lineId === lineId)
  if (!row) return
  selectedKey.value = row.key
  await nextTick()
  const el = document.getElementById('tvrow-' + row.key)
  if (el) el.scrollIntoView({ block: 'nearest' })
}

function atBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= TAIL_THRESHOLD
}

function onScroll(): void {
  const el = scroller.value
  if (!el) return
  followsTail.value = atBottom(el)
}

async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = scroller.value
  if (!el) return
  el.scrollTop = el.scrollHeight
  followsTail.value = true
}

/** 流式内容更新：只在用户本来就贴底时跟随，不打断向上翻阅。 */
watch(
  () => props.tick,
  async () => {
    if (!followsTail.value) return
    await scrollToBottom()
  },
)

onMounted(async () => {
  await scrollToBottom()
})

/** 行内容：工具行三段式，其余按角色给单行摘要。 */
function rowText(row: TrajRow): string {
  if (row.tool) return toolText(row.tool)
  if (row.role === 'status') {
    const s = splitStatus(row.text)
    return s.detail !== '' ? s.label + ' · ' + s.detail : s.label
  }
  return row.text.replace(/\s+/g, ' ').trim()
}

/** 工具行的「名字 · 参数」与「→ 结果」两段（分别着色）。 */
function toolParts(row: TrajRow): { req: string; res: string; err: boolean } {
  if (!row.tool) return { req: rowText(row), res: '', err: false }
  const tool = row.tool
  const args = tool.args.replace(/\s+/g, ' ').trim()
  const req = tool.name + (args !== '' ? ' · ' + args : '')
  const res = tool.phase === 'start' ? '运行中…' : tool.ok ? tool.result : tool.error || tool.result
  return { req, res: res.replace(/\s+/g, ' ').trim() || (tool.ok ? '无输出' : '失败'), err: !tool.ok }
}

function statusTextOf(row: TrajRow): string {
  if (row.error) return stateLabel('error')
  if (row.running) return stateLabel('running')
  return stateLabel('done')
}

/** 状态行的色调（唯一一套映射）：失败红 / 运行中琥珀 / 完成绿；其它角色行返回空串（不抢正文色）。 */
function statusToneOf(row: TrajRow): string {
  if (row.role !== 'status') return ''
  return toneClass(stateTone(row.error ? 'error' : row.running ? 'running' : 'done'))
}

/** 子智能体报告的小标题：优先编队节点 label，其次子智能体的 worker 标题，最后会话 id 尾段。 */
function childTitle(r: ReportEntry): string {
  const label = props.childLabels[r.sessionId] ?? ''
  if (label !== '') return label
  const byWorker = props.childWorkerLabels[r.worker] ?? ''
  if (byWorker !== '') return byWorker
  if (r.worker !== '') return r.worker
  return r.sessionId.slice(-8)
}

const inspected = computed(() => {
  const row = selected.value
  if (!row) return null
  return {
    row,
    status: statusTextOf(row),
    role: roleLabel(row.role),
    tool: row.tool ?? null,
  }
})

function densityLeft(pct: number): string {
  return pct.toFixed(4) + '%'
}

/** 相邻色块之间留 1px 缝（DSH 的 span 间隙规则），密集时也不糊成一条。 */
function densityWidth(pct: number): string {
  return 'calc(' + pct.toFixed(4) + '% - 1px)'
}
</script>

<template>
  <div class="tv">
    <!-- 工具栏：32px sticky，两个折叠开关 + 过滤式搜索 + 右侧细统计 -->
    <div class="tv-toolbar" role="toolbar" aria-label="轨迹工具栏">
      <button class="tv-ctl" type="button" :aria-pressed="allTurnsCollapsed" @click="toggleAllTurns">
        <span class="tv-ctl-icon mono">{{ allTurnsCollapsed ? '⊞' : '⊟' }}</span>轮次
      </button>
      <button class="tv-ctl" type="button" :aria-pressed="callsCollapsed" @click="toggleCalls">
        <span class="tv-ctl-icon mono">{{ callsCollapsed ? '⊞' : '⊟' }}</span>调用
      </button>
      <label class="tv-search">
        <span class="tv-search-icon">⌕</span>
        <input v-model="query" type="search" placeholder="搜索" aria-label="搜索轨迹" />
        <button v-if="query !== ''" class="tv-search-clear" type="button" title="清空搜索" @click="query = ''">×</button>
      </label>
      <span class="spacer" />
      <span class="tv-stats">
        {{ stats.steps }} 步 · {{ stats.tools }} 次工具调用 · {{ reportsCount }} 份交付物
        <template v-if="childReports.length > 0"> · {{ childReports.length }} 份子智能体产出</template>
        <template v-if="query !== ''"> · 命中 {{ filtered.length }}</template>
      </span>
    </div>

    <!-- 系统提示词：带字数与展开开关。轨迹要能回答「模型到底收到了什么口径」 -->
    <section v-if="systemPrompt !== ''" class="tv-sys" :class="{ open: sysOpen }">
      <button class="tv-sys-hd" type="button" :aria-expanded="sysOpen" @click="sysOpen = !sysOpen">
        <span class="tv-sys-caret mono">{{ sysOpen ? '▾' : '▸' }}</span>
        <span class="tv-sys-title">系统提示词</span>
        <span class="tv-sys-meta">{{ systemPrompt.length }} 字 · 会话首条事件</span>
        <span class="spacer" />
        <span class="tv-sys-hint">{{ sysOpen ? '收起' : '展开' }}</span>
      </button>
      <pre v-if="sysOpen" class="tv-sys-body">{{ systemPrompt }}</pre>
    </section>

    <!-- 子智能体产出：过程证据（报告册只放交付物，这里补上「信息不丢」的入口），默认收起 -->
    <details v-if="childReports.length > 0" class="tv-kids">
      <summary class="tv-kids-hd">
        <span class="tv-kids-title">子智能体产出</span>
        <span class="tv-kids-meta">{{ childReports.length }} 份 · 过程证据，不进交付物</span>
      </summary>
      <div class="tv-kids-body">
        <details v-for="r in childReports" :key="r.id" class="tv-kid">
          <summary class="tv-kid-hd">
            <span class="tv-kid-label ellipsis">{{ childTitle(r) }}</span>
            <span class="tv-kid-worker mono tiny">{{ r.worker || '—' }}</span>
            <span class="tv-kid-time tiny">{{ fmtDateTime(r.at) }}</span>
          </summary>
          <div class="tv-kid-content md" v-html="markdownHtml(r.content)" />
        </details>
      </div>
    </details>

    <!-- 密度条：50px，三泳道（输入 / 模型 / 工具），点击色块选中对应行 -->
    <div class="tv-overview" :class="{ empty: spans.length === 0 }">
      <template v-for="lane in laneSpans" :key="'lane-' + lane.lane">
        <div class="tv-lane-label">{{ lane.label }}</div>
        <div class="tv-plot">
          <span
            v-for="s in lane.spans"
            :key="s.key"
            class="tv-span"
            :class="['lane-' + s.lane, { err: s.error, dim: query !== '' && !matched.has(s.key) }]"
            :style="{ left: densityLeft(s.left), width: densityWidth(s.width) }"
            :title="'第 ' + (s.index + 1) + ' 行'"
            @click="pickSpan(s.lineId)"
          />
        </div>
      </template>
    </div>

    <div class="tv-main">
      <div ref="scroller" class="tv-ledger" @scroll.passive="onScroll">
        <div v-if="spans.length === 0" class="tv-empty">
          <div class="tv-empty-title">这条会话还没有轨迹</div>
          <p class="tv-empty-text">运行一次之后，用户消息、模型输出与每一次工具调用都会按轮次记在这里，工具行可以展开参数与结果。</p>
        </div>
        <template v-else>
          <div v-if="olderCount > 0" class="tv-older">已折叠更早 {{ olderCount }} 行</div>
          <table class="tv-table">
            <colgroup>
              <col style="width: 84px" />
              <col />
            </colgroup>
            <tbody>
              <template v-for="item in shownRows" :key="item.key">
                <!-- 轮次摘要行：20px 高，点击折叠/展开 -->
                <tr v-if="item.kind === 'turn'" class="tv-summary" :class="{ collapsed: item.collapsed }" @click="toggleTurn(item.turn.turn)">
                  <td class="c-ev">
                    <span class="turn-rail" />
                    <span class="turn-label mono">第{{ item.turn.turn }}轮</span>
                  </td>
                  <td class="c-content">
                    <span v-if="item.collapsed" class="summary-ellipsis">…</span>
                    第 {{ item.turn.turn }} 轮 · {{ item.turn.steps }} 步 · {{ item.turn.tools }} 次工具调用
                    <span class="summary-hint">{{ item.collapsed ? '点击展开' : '点击折叠' }}</span>
                  </td>
                </tr>
                <!-- 调用折叠摘要：工具行合并成一行 -->
                <tr v-else-if="item.kind === 'calls'" class="tv-summary" @click="expandCalls(item.turn.turn)">
                  <td class="c-ev">
                    <span class="turn-rail" />
                  </td>
                  <td class="c-content">
                    <span class="summary-ellipsis">…</span>
                    {{ item.turn.tools }} 次工具调用
                    <span class="summary-hint">点击展开</span>
                  </td>
                </tr>
                <tr
                  v-else
                  :id="'tvrow-' + item.row.key"
                  class="tv-row"
                  :class="[statusToneOf(item.row), { sel: selectedKey === item.row.key, err: item.row.error, running: item.row.running }]"
                  :data-kind="item.row.role"
                  tabindex="0"
                  @click="pick(item.row)"
                  @keydown.enter.prevent="pick(item.row)"
                >
                  <td class="c-ev">
                    <span class="turn-rail" />
                    <span class="kind-slot">
                      <span class="kind-chip" :class="item.row.role">{{ roleLabel(item.row.role) }}</span>
                    </span>
                  </td>
                  <td class="c-content">
                    <div v-if="item.row.tool" class="tool-grid">
                      <span class="tool-req" :title="toolParts(item.row).req">
                        <span class="tool-name">{{ item.row.tool.name }}</span>
                        <span v-if="item.row.tool.args" class="tool-args"> · {{ item.row.tool.args.replace(/\s+/g, ' ').trim() }}</span>
                      </span>
                      <span class="tool-res">
                        <span class="arrow">→</span>
                        <span class="tool-result-text" :title="toolParts(item.row).res">{{ toolParts(item.row).res }}</span>
                        <span v-if="item.row.tool.ms > 0" class="tool-ms">{{ formatMs(item.row.tool.ms) }}</span>
                      </span>
                    </div>
                    <span v-else class="plain-text" :title="rowText(item.row)">
                      {{ rowText(item.row) }}<span v-if="item.row.running && running" class="tv-caret">▍</span>
                    </span>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </template>
      </div>

      <!-- inspector：320px 右侧滑出，可关闭 -->
      <aside v-if="inspected" class="tv-inspector" aria-label="行详情">
        <header class="tv-insp-hd">
          <span class="kind-chip" :class="inspected.row.role">{{ inspected.role }}</span>
          <span class="tv-insp-title ellipsis">
            {{ inspected.tool ? inspected.tool.name : (inspected.row.role === 'status' ? splitStatus(inspected.row.text).label : '第 ' + inspected.row.turn + ' 轮') }}
          </span>
          <span class="spacer" />
          <button class="tv-insp-close" type="button" title="关闭" @click="selectedKey = ''">×</button>
        </header>
        <div class="tv-insp-body">
          <dl class="tv-kv">
            <dt>角色</dt>
            <dd>{{ inspected.role }}</dd>
            <dt>状态</dt>
            <dd :class="{ 'is-err': inspected.row.error }">{{ inspected.status }}</dd>
            <dt>时间</dt>
            <dd class="mono">{{ inspected.row.time }}</dd>
            <template v-if="inspected.tool">
              <dt>工具</dt>
              <dd class="mono">{{ inspected.tool.name }}</dd>
              <dt>元工具</dt>
              <dd class="mono">{{ inspected.tool.meta || '—' }}</dd>
              <dt>耗时</dt>
              <dd class="mono">{{ formatMs(inspected.tool.ms) || '—' }}</dd>
              <dt>步数</dt>
              <dd class="mono">{{ inspected.row.step > 0 ? inspected.row.step : '—' }}</dd>
            </template>
            <template v-else>
              <dt>轮次</dt>
              <dd class="mono">第 {{ inspected.row.turn }} 轮</dd>
              <template v-if="inspected.row.step > 0">
                <dt>步数</dt>
                <dd class="mono">{{ inspected.row.step }}</dd>
              </template>
            </template>
            <dt>Worker</dt>
            <dd class="mono">{{ inspected.row.worker || '—' }}</dd>
          </dl>

          <template v-if="inspected.tool">
            <div class="tv-insp-label">参数</div>
            <pre class="tv-pre">{{ inspected.tool.args || '（无参数）' }}</pre>
            <div class="tv-insp-label">{{ inspected.tool.ok ? '结果' : '错误' }}</div>
            <pre class="tv-pre" :class="{ 'is-err': !inspected.tool.ok }">{{ (inspected.tool.ok ? inspected.tool.result : inspected.tool.error || inspected.tool.result) || '（无输出）' }}</pre>
          </template>
          <template v-else>
            <div class="tv-insp-label">内容</div>
            <pre class="tv-pre">{{ inspected.row.text }}</pre>
          </template>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.tv {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}

/* ---------- 工具栏 ---------- */
.tv-sys {
  flex: 0 0 auto;
  border-bottom: 0.5px solid var(--line-2);
  background: var(--glass-1);
}
.tv-sys-hd {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  height: 28px;
  padding: 0 10px;
  border: 0;
  background: transparent;
  color: var(--text-2);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.tv-sys-hd:hover {
  background: var(--glass-2);
}
.tv-sys-caret {
  font-size: 10px;
  color: var(--text-3);
}
.tv-sys-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-1);
}
.tv-sys-meta {
  font-size: 11px;
  color: var(--text-3);
}
.tv-sys-hint {
  font-size: 11px;
  color: var(--accent);
}
.tv-sys-body {
  max-height: 240px;
  margin: 0;
  padding: 8px 12px 12px;
  overflow: auto;
  border-top: 0.5px solid var(--line-2);
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  font-size: 11px;
  line-height: 1.55;
  color: var(--text-2);
  white-space: pre-wrap;
  word-break: break-word;
}

/* ---------- 子智能体产出（过程证据，不是交付物） ---------- */
.tv-kids {
  flex: 0 0 auto;
  border-bottom: 0.5px solid var(--line-2);
  background: var(--glass-1);
}
.tv-kids-hd,
.tv-kid-hd {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 10px;
  height: 26px;
  color: var(--text-2);
  font-size: 11.5px;
  cursor: pointer;
  list-style: none;
}
.tv-kids-hd::-webkit-details-marker,
.tv-kid-hd::-webkit-details-marker {
  display: none;
}
.tv-kids-hd::before,
.tv-kid-hd::before {
  content: '▸';
  color: var(--text-3);
  font-size: 9px;
}
.tv-kids[open] > .tv-kids-hd::before,
.tv-kid[open] > .tv-kid-hd::before {
  content: '▾';
}
.tv-kids-title {
  font-weight: 600;
  color: var(--text-1, var(--text));
}
.tv-kids-meta,
.tv-kid-time {
  color: var(--muted);
  font-size: 10.5px;
}
.tv-kids-body {
  max-height: 300px;
  overflow: auto;
  padding: 0 10px 8px;
}
.tv-kid {
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-2);
  margin-bottom: 6px;
}
.tv-kid-hd {
  height: 24px;
}
.tv-kid-label {
  max-width: 220px;
  color: var(--text-2);
  font-weight: 600;
}
.tv-kid-content {
  max-height: 220px;
  overflow: auto;
  padding: 6px 10px 10px;
  border-top: 1px solid var(--glass-line);
  font-size: 11.5px;
  color: var(--text-2);
}
.tv-toolbar {
  position: sticky;
  top: 0;
  z-index: 4;
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 0 0 auto;
  height: 32px;
  padding: 0 8px;
  border-bottom: 0.5px solid var(--line-2);
  background: var(--glass-2);
  backdrop-filter: blur(var(--blur-1));
  -webkit-backdrop-filter: blur(var(--blur-1));
}
.tv-ctl {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  height: 20px;
  padding: 0 5px;
  border: 1px solid transparent;
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  font-size: 12px;
  line-height: 18px;
}
.tv-ctl:hover {
  color: var(--text-2);
  background: var(--panel-2);
}
.tv-ctl[aria-pressed='true'] {
  color: var(--text);
  background: var(--panel-3);
}
.tv-ctl-icon {
  font-size: 13px;
  line-height: 1;
}
.tv-search {
  display: flex;
  align-items: center;
  gap: 4px;
  flex: 0 1 164px;
  min-width: 84px;
  margin-left: 6px;
  height: 22px;
  padding: 0 6px;
  border: 0.5px solid var(--line-3);
  border-radius: 4px;
  background: transparent;
  transition: border-color 0.14s var(--ease);
}
.tv-search:focus-within {
  border-color: var(--accent);
}
.tv-search-icon {
  color: var(--muted);
  font-size: 11px;
}
.tv-search input {
  flex: 1 1 auto;
  min-width: 0;
  height: 20px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text);
  font-size: 11px;
  outline: none;
}
.tv-search input::-webkit-search-cancel-button {
  display: none;
}
.tv-search-clear {
  flex: 0 0 auto;
  width: 14px;
  height: 14px;
  padding: 0;
  border: 0;
  border-radius: 3px;
  background: transparent;
  color: var(--muted);
  font-size: 12px;
  line-height: 14px;
}
.tv-search-clear:hover {
  color: var(--text);
  background: var(--panel-3);
}
.tv-stats {
  flex: 0 0 auto;
  padding-right: 2px;
  color: var(--muted);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* ---------- 密度条 ---------- */
.tv-overview {
  flex: 0 0 auto;
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr);
  grid-auto-rows: 14px;
  padding: 4px 0;
  height: 50px;
  border-bottom: 0.5px solid var(--line);
  background: var(--bg-2);
}
.tv-overview.empty {
  opacity: 0.4;
}
.tv-lane-label {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  padding-right: 6px;
  color: var(--muted);
  font-size: 9px;
  letter-spacing: 0.06em;
}
.tv-plot {
  position: relative;
  height: 14px;
}
.tv-span {
  position: absolute;
  top: 3px;
  height: 8px;
  min-width: 2px;
  border-radius: 1px;
  opacity: 0.78;
  cursor: pointer;
  transition: opacity 0.12s var(--ease);
}
.tv-span:hover {
  opacity: 1;
}
.tv-span.lane-0 {
  background: var(--info);
}
.tv-span.lane-1 {
  background: #a08cff;
}
.tv-span.lane-2 {
  background: var(--accent);
  opacity: 1;
}
.tv-span.err {
  background: var(--err);
  opacity: 1;
}
.tv-span.dim {
  opacity: 0.14;
}

/* ---------- 账本 ---------- */
.tv-main {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  overflow: hidden;
}
.tv-ledger {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  overflow: auto;
  container-type: inline-size;
}
.tv-table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
  font-size: 12px;
}
.tv-row {
  height: 30px;
  border-bottom: 0.5px solid var(--line);
  cursor: default;
  transition: background 0.12s var(--ease);
}
.tv-row:hover {
  background: rgba(38, 49, 72, 0.22);
}
.tv-row.sel {
  background: rgba(38, 49, 72, 0.32);
}
.tv-row:focus-visible {
  outline: 1px solid var(--accent);
  outline-offset: -1px;
}
.tv-row td {
  padding: 0;
  vertical-align: middle;
}
.c-ev {
  position: relative;
  padding-left: 16px !important;
  white-space: nowrap;
}
.c-content {
  min-width: 0;
  padding-right: 10px !important;
  overflow: hidden;
}
/* 轮次竖线：2px，失败行变红 */
.turn-rail {
  position: absolute;
  left: 7px;
  top: 0;
  bottom: 0;
  width: 2px;
  background: rgba(91, 157, 255, 0.22);
}
.tv-row.err .turn-rail {
  background: rgba(255, 90, 95, 0.55);
}
.tv-row.sel .turn-rail {
  left: 5px;
  width: 3px;
  background: var(--accent);
}
.turn-label {
  position: absolute;
  left: 13px;
  top: 3px;
  color: var(--muted);
  font-size: 8px;
  line-height: 10px;
  letter-spacing: 0.02em;
  font-variant-numeric: tabular-nums;
  opacity: 0.85;
}
.kind-slot {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  padding-right: 6px;
  height: 30px;
}
.kind-chip {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 19px;
  padding: 0 5px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 650;
  letter-spacing: 0.035em;
  line-height: 16px;
  white-space: nowrap;
}
/* 角色配色：低饱和背景 + 高对比文字（与 DSH 的四种语义色一致） */
.kind-chip.user {
  color: #8db4ff;
  background: rgba(91, 157, 255, 0.16);
}
.kind-chip.assistant {
  color: #c0b2ff;
  background: rgba(160, 140, 255, 0.16);
}
.kind-chip.tool {
  color: var(--accent-2);
  background: rgba(233, 180, 76, 0.15);
}
.kind-chip.status,
.kind-chip.system {
  color: var(--text-2);
  background: rgba(148, 163, 184, 0.14);
}
.plain-text {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-2);
  font-size: 12px;
  line-height: 29px;
}
.tv-row[data-kind='user'] .plain-text {
  color: var(--text);
}
.tv-row[data-kind='assistant'] .plain-text {
  color: var(--text-2);
}
.tv-row[data-kind='status'] .plain-text {
  color: var(--muted);
  font-size: 11.5px;
}
/* 状态行按唯一一套色调上色：完成绿 / 运行中琥珀 / 失败红（与工作台、对话页一致） */
.tv-row.tone-ok .plain-text {
  color: #4ce0ab;
}
.tv-row.tone-warn .plain-text {
  color: var(--accent-2);
}
.tv-row.tone-dim .plain-text {
  color: var(--muted);
}
.tv-row.err .plain-text {
  color: var(--err);
}
.tv-caret {
  margin-left: 2px;
  color: var(--accent-2);
  animation: tv-blink 1.1s steps(1) infinite;
}
@keyframes tv-blink {
  50% {
    opacity: 0;
  }
}
.tool-grid {
  display: grid;
  align-items: center;
  gap: 8px;
  min-width: 0;
  grid-template-columns: clamp(180px, 36cqw - 56px, 480px) minmax(0, 1fr);
  font: 12px/29px var(--mono);
}
.tool-req {
  display: flex;
  align-items: baseline;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}
.tool-name {
  flex: 0 0 auto;
  color: var(--text);
}
.tool-args {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--muted);
}
.tool-res {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
}
.arrow {
  flex: 0 0 auto;
  color: var(--faint);
}
.tool-result-text {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text-2);
}
.tv-row.err .tool-result-text {
  color: var(--err);
}
.tool-ms {
  flex: 0 0 auto;
  color: var(--muted);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}
.tv-row.running .tool-result-text {
  color: var(--accent-2);
}

/* 折叠摘要行：20px */
.tv-summary {
  height: 20px;
  border-bottom: 0.5px solid var(--line);
  cursor: pointer;
}
.tv-summary:hover {
  background: var(--panel-2);
}
.tv-summary .c-content {
  color: var(--muted);
  font-size: 11px;
  line-height: 19px;
}
.tv-summary:not(.collapsed) .c-content {
  color: var(--faint);
}
.tv-summary:not(.collapsed) .turn-label {
  opacity: 0.6;
}
.summary-ellipsis {
  margin-right: 6px;
  color: var(--faint);
}
.summary-hint {
  margin-left: 8px;
  color: var(--faint);
  font-size: 10px;
}
.tv-older {
  padding: 5px 10px 5px 16px;
  border-bottom: 0.5px solid var(--line);
  color: var(--muted);
  font-size: 10.5px;
}

/* ---------- 空态 ---------- */
.tv-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 48px 24px;
  text-align: center;
}
.tv-empty-title {
  color: var(--text-2);
  font-size: 13px;
  font-weight: 600;
}
.tv-empty-text {
  max-width: 400px;
  margin: 0;
  color: var(--muted);
  font-size: 11.5px;
  line-height: 1.75;
}

/* ---------- inspector ---------- */
.tv-inspector {
  flex: 0 0 320px;
  width: 320px;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-left: 0.5px solid var(--line-2);
  background: var(--glass-2);
  animation: tv-slide 0.16s var(--ease);
}
@keyframes tv-slide {
  from {
    transform: translateX(12px);
    opacity: 0;
  }
}
.tv-insp-hd {
  display: flex;
  align-items: center;
  gap: 7px;
  flex: 0 0 auto;
  min-height: 32px;
  padding: 6px 8px 6px 10px;
  border-bottom: 0.5px solid var(--line);
}
.tv-insp-title {
  min-width: 0;
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
}
.tv-insp-close {
  flex: 0 0 auto;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 1px solid transparent;
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  font-size: 14px;
  line-height: 18px;
}
.tv-insp-close:hover {
  color: var(--text);
  border-color: var(--line-2);
  background: var(--panel-2);
}
.tv-insp-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.tv-kv {
  display: grid;
  grid-template-columns: 58px minmax(0, 1fr);
  gap: 4px 8px;
  margin: 0 0 4px;
  font-size: 11px;
}
.tv-kv dt {
  color: var(--muted);
}
.tv-kv dd {
  margin: 0;
  min-width: 0;
  color: var(--text-2);
  overflow-wrap: anywhere;
}
.tv-kv dd.is-err {
  color: var(--err);
}
.tv-insp-label {
  margin-top: 4px;
  color: var(--muted);
  font-size: 10px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.tv-pre {
  max-height: 220px;
  margin: 0;
  padding: 8px 9px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  color: var(--text-2);
  font: 11px/1.6 var(--mono);
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
.tv-pre.is-err {
  color: var(--err);
  border-color: rgba(255, 90, 95, 0.3);
}

@media (prefers-reduced-motion: reduce) {
  .tv-inspector {
    animation: none;
  }
  .tv-caret {
    animation: none;
  }
}
</style>
