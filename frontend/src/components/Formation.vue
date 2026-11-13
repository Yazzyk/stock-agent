<!-- 编队视图：
     · 只有根节点（没有派生子智能体）时，不再摆三栏骨架，改成一张居中的「RouterAgent」卡；
     · 有子智能体时是两栏：左侧谱系树、右侧节点详情；worker 花名册压成顶部一行 chips，
       点 chip 只看该 worker 的节点（含它们的祖先，保证谱系不断线）。 -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { agentSession } from '../lib/data'
import { markdownHtml, stateLabel, stateTone, toneDot } from '../lib/agentview'
import { shortElapsed, shortTokens, type AgentNode } from '../lib/api.agent'
import { fmtDateTime } from '../lib/format'
import type { WorkerInfo } from '../types'

const props = withDefaults(
  defineProps<{
    nodes: AgentNode[]
    /** 当前会话 id：树里没有它时退回 parentId 为空的根 */
    rootId?: string
    workers?: WorkerInfo[]
    /** 树数据读取中（显示骨架） */
    loading?: boolean
    /** 树读取失败的原因（空表示没出错） */
    error?: string
    /** 后端 tree 给的汇总：节点总数与运行中数量（权威，优先于本地推算） */
    total?: number
    activeCount?: number
  }>(),
  { rootId: '', workers: () => [], loading: false, error: '', total: 0, activeCount: 0 },
)

const emit = defineEmits<{ (e: 'open', sessionId: string): void; (e: 'refresh'): void }>()

const now = ref(Date.now())
let timer: number | null = null

/** 根：先按 parentId 为空找，再按 rootId 定位（事件先到时 rootId 可能还没落）。 */
const root = computed<AgentNode | null>(() => {
  const byParent = props.nodes.find((n) => (n.parentId ?? '') === '')
  if (byParent) return byParent
  return props.nodes.find((n) => n.sessionId === props.rootId) ?? props.nodes[0] ?? null
})

interface Row {
  node: AgentNode
  depth: number
  /** 是所在层级里的最后一个孩子（用于收尾竖线） */
  last: boolean
}

/** 谱系行：沿用后端 nodes 的顺序与 depth，depth 缺失时按父链兜底。 */
const rows = computed<Row[]>(() => {
  const list = props.nodes
  if (list.length === 0) return []
  const byId = new Map<string, AgentNode>()
  for (const n of list) byId.set(n.sessionId, n)

  /** 沿 parentId 往上数层数（防止数据回环，最多 32 层）。 */
  const chainDepth = (node: AgentNode): number => {
    let depth = 0
    let cur: AgentNode | undefined = node
    const guard = new Set<string>()
    while (cur && (cur.parentId ?? '') !== '' && depth < 32) {
      if (guard.has(cur.sessionId)) break
      guard.add(cur.sessionId)
      cur = byId.get(cur.parentId ?? '')
      if (!cur) break
      depth += 1
    }
    return depth
  }

  const depthOf = (node: AgentNode): number => {
    if ((node.parentId ?? '') === '') return 0
    if (typeof node.depth === 'number' && node.depth > 0) return node.depth
    return chainDepth(node)
  }

  // 每个父节点下最后一个孩子：CSS 用它收掉竖线尾巴
  const lastChild = new Map<string, string>()
  for (const n of list) lastChild.set(n.parentId ?? '', n.sessionId)

  return list.map((node) => ({
    node,
    depth: depthOf(node),
    last: node.sessionId === lastChild.get(node.parentId ?? '') && (node.parentId ?? '') !== '',
  }))
})

const childCount = computed(() => Math.max(0, rows.value.length - 1))
const runningCount = computed(() =>
  props.activeCount > 0 ? props.activeCount : props.nodes.filter((n) => n.status === 'running').length,
)
const totalCount = computed(() => (props.total > 0 ? props.total : rows.value.length))
/** 没有派生子智能体：只有一个节点（或连节点都还没读到）。 */
const soloMode = computed(() => rows.value.length <= 1)

/** worker 名 → 展示名 / 类别。 */
const workerTitle = computed(() => {
  const map = new Map<string, string>()
  for (const w of props.workers) map.set(w.id, w.title)
  return map
})
const workerKind = computed(() => {
  const map = new Map<string, 'main' | 'preset' | 'custom'>()
  for (const w of props.workers) map.set(w.id, w.kind ?? (w.custom ? 'custom' : w.id === 'router' ? 'main' : 'preset'))
  return map
})

function titleOf(id: string): string {
  if (id === '') return '未标注'
  return workerTitle.value.get(id) ?? id
}

function isRootNode(node: AgentNode): boolean {
  return (node.parentId ?? '') === ''
}

/** 节点展示名：label 优先，缺了用后端别名 role，再缺用 worker 标题。 */
function labelOf(node: AgentNode, rootNode: boolean): string {
  if (node.label) return node.label
  if (node.role) return node.role
  if (node.worker) return titleOf(node.worker)
  return rootNode ? '根会话' : '子智能体'
}

/** 根会话是不是「主控」本身：worker 为空或 router 时是 RouterAgent 会话。 */
const rootIsRouter = computed(() => {
  const r = root.value
  if (!r) return false
  const id = r.worker || 'router'
  return id === 'router' || workerKind.value.get(id) === 'main'
})

function roleOf(node: AgentNode): string {
  if (!isRootNode(node)) return '子智能体'
  if (rootIsRouter.value) return '主会话（RouterAgent）'
  const w = node.worker ? titleOf(node.worker) : ''
  return w === '' ? '根会话' : '根会话（' + w + '）'
}

interface Chip {
  id: string
  title: string
  kind: string
  count: number
  tokens: number
  status: string
}

/** 顶部 chips：按本次编队里真实出现过的 worker 分组。 */
const roster = computed<Chip[]>(() => {
  const map = new Map<string, Chip>()
  for (const n of props.nodes) {
    const id = n.worker || (isRootNode(n) ? 'router' : '')
    const key = id === '' ? '__none__' : id
    const found = map.get(key)
    if (found) {
      found.count += 1
      found.tokens += n.tokens
      if (n.status === 'running') found.status = 'running'
      else if (n.status === 'failed' && found.status !== 'running') found.status = 'failed'
      continue
    }
    map.set(key, {
      id: key === '__none__' ? '' : key,
      title: key === '__none__' ? '未标注 worker' : titleOf(id),
      kind: key === '__none__' ? '' : (workerKind.value.get(id) ?? ''),
      count: 1,
      tokens: n.tokens,
      status: n.status,
    })
  }
  return Array.from(map.values()).sort((a, b) => {
    if (a.kind === 'main' && b.kind !== 'main') return -1
    if (b.kind === 'main' && a.kind !== 'main') return 1
    return b.count - a.count
  })
})

/** 当前选中的 worker chip：空串表示不过滤。 */
const chipFilter = ref('')

/** 过滤后的谱系：保留命中节点 + 它们的祖先，保证树不断线。 */
const visibleRows = computed<Row[]>(() => {
  if (chipFilter.value === '') return rows.value
  const byId = new Map<string, AgentNode>()
  for (const n of props.nodes) byId.set(n.sessionId, n)
  const keep = new Set<string>()
  for (const n of props.nodes) {
    if ((n.worker || (isRootNode(n) ? 'router' : '')) !== chipFilter.value) continue
    let cur: AgentNode | undefined = n
    const guard = new Set<string>()
    while (cur && !guard.has(cur.sessionId)) {
      guard.add(cur.sessionId)
      keep.add(cur.sessionId)
      cur = (cur.parentId ?? '') === '' ? undefined : byId.get(cur.parentId ?? '')
    }
  }
  return rows.value.filter((r) => keep.has(r.node.sessionId))
})

function toggleChip(id: string): void {
  chipFilter.value = chipFilter.value === id ? '' : id
}

/** 节点状态点 / 文案：与工作台、对话页共用 lib/agentview 的唯一一套状态映射（已完成=绿、运行中=琥珀、失败=红）。 */
function statusDot(s: string): string {
  return toneDot(stateTone(s))
}

function statusText(s: string): string {
  return stateLabel(s)
}

/** 节点状态徽标：tag-down=绿（完成）、tag-up=红（失败）、tag-accent=琥珀（运行中）。
    这三个类名沿用行情的涨跌命名，但色相与 statusDot 完全一致。 */
function statusTag(s: string): string {
  const tone = stateTone(s)
  if (tone === 'ok') return 'tag-down'
  if (tone === 'err') return 'tag-up'
  return 'tag-accent'
}

function elapsedOf(node: AgentNode): number {
  if (node.elapsedMs > 0) return node.elapsedMs
  if (node.status !== 'running') return 0
  const start = Date.parse(node.startedAt)
  return Number.isNaN(start) ? 0 : Math.max(0, now.value - start)
}

// ---------- 选中节点 ----------

const selectedId = ref('')

watch(
  rows,
  (list) => {
    if (!list.some((r) => r.node.sessionId === selectedId.value)) selectedId.value = list[0]?.node.sessionId ?? ''
  },
  { immediate: true },
)

const selected = computed(() => props.nodes.find((n) => n.sessionId === selectedId.value) ?? null)

const summary = ref('')
const summaryBusy = ref(false)
const summaryDenied = ref('')

async function loadSummary(id: string): Promise<void> {
  summary.value = ''
  summaryDenied.value = ''
  if (id === '') return
  summaryBusy.value = true
  try {
    const detail = await agentSession(id)
    const hit = [...detail.events].reverse().find((e) => e.kind === 'report')
    summary.value = hit?.content ?? ''
  } catch {
    summaryDenied.value = '报告摘要读取失败（会话可能还没落盘）'
  } finally {
    summaryBusy.value = false
  }
}

watch(
  selectedId,
  (id) => {
    void loadSummary(id)
  },
  { immediate: true },
)

function pick(node: AgentNode): void {
  selectedId.value = node.sessionId
}

onMounted(() => {
  timer = window.setInterval(() => {
    now.value = Date.now()
  }, 1000)
})
onBeforeUnmount(() => {
  if (timer !== null) window.clearInterval(timer)
})
</script>

<template>
  <div class="fm">
    <div class="fm-hd">
      <span class="fm-title">本次编排</span>
      <span class="fm-count">{{ root ? labelOf(root, true) : '—' }}</span>
      <span class="fm-sep">/</span>
      <span class="fm-count num" :class="{ on: childCount > 0 }">{{ childCount }} 个子智能体</span>
      <span class="tiny muted">共 {{ totalCount }} 个节点</span>
      <span v-if="runningCount > 0" class="tag tag-accent">运行中 {{ runningCount }}</span>
      <span class="spacer" />
      <button class="btn btn-sm btn-ghost" type="button" :disabled="loading" @click="emit('refresh')">
        {{ loading ? '读取中…' : '刷新' }}
      </button>
    </div>

    <!-- 加载态 -->
    <div v-if="loading && rows.length === 0" class="fm-solo-wrap">
      <div class="skeleton" style="height: 148px; width: 420px" />
    </div>

    <!-- 读取失败且没有节点 -->
    <div v-else-if="error && rows.length === 0" class="fm-empty">
      <div class="fm-empty-title">编队树读取失败</div>
      <div class="tiny fm-empty-msg">{{ error }}</div>
      <button class="btn btn-sm" type="button" @click="emit('refresh')">重试</button>
    </div>

    <!-- 一个节点都没有 -->
    <div v-else-if="rows.length === 0" class="fm-empty">
      <div class="fm-empty-title">本次还没有编队记录</div>
      <div class="tiny fm-empty-msg">
        运行一次「盘前 / 盘中 / 复盘」，或让 RouterAgent 用 agent.spawn 派生子任务，这里会按父子关系画出谱系树。
      </div>
      <button class="btn btn-sm" type="button" :disabled="loading" @click="emit('refresh')">刷新</button>
    </div>

    <!-- 只有根节点：一张居中的「RouterAgent」卡，不摆三栏骨架 -->
    <div v-else-if="soloMode && root" class="fm-solo-wrap">
      <section class="fm-solo">
        <header class="fm-solo-hd">
          <span class="dot" :class="statusDot(root.status)" />
          <span class="fm-solo-title">{{ labelOf(root, true) }}</span>
          <span class="tag" :class="statusTag(root.status)">{{ statusText(root.status) }}</span>
          <span class="spacer" />
          <span class="tag">{{ titleOf(root.worker || 'router') }}</span>
        </header>
        <div class="fm-solo-id mono ellipsis" :title="root.sessionId">{{ root.sessionId || '会话还未创建' }}</div>
        <dl class="fm-solo-kv">
          <div><dt>步数</dt><dd class="num">{{ root.steps > 0 ? root.steps : '—' }}</dd></div>
          <div><dt>Token</dt><dd class="num">{{ root.tokens > 0 ? shortTokens(root.tokens) : '—' }}</dd></div>
          <div><dt>耗时</dt><dd class="num">{{ elapsedOf(root) > 0 ? shortElapsed(elapsedOf(root)) : '—' }}</dd></div>
          <div><dt>开始</dt><dd class="num">{{ root.startedAt ? fmtDateTime(root.startedAt) : '—' }}</dd></div>
        </dl>
        <p class="fm-solo-note">本次没有派生子智能体：{{ titleOf(root.worker || 'router') }} 直接回答或单线程完成。</p>
        <footer class="fm-solo-ft">
          <button class="btn btn-sm btn-accent" type="button" :disabled="root.sessionId === ''" @click="emit('open', root.sessionId)">
            打开会话日志
          </button>
        </footer>
      </section>
    </div>

    <!-- 有子智能体：顶部 chips + 两栏（谱系 / 详情） -->
    <template v-else>
      <div class="fm-chips" aria-label="worker 花名册">
        <button
          v-for="c in roster"
          :key="c.id || 'none'"
          class="fm-chip"
          :class="{ on: chipFilter === c.id }"
          type="button"
          :title="c.title + '：' + c.count + ' 个节点'"
          @click="toggleChip(c.id)"
        >
          <span class="dot" :class="statusDot(c.status)" />
          <span class="fm-chip-name">{{ c.title }}</span>
          <span class="fm-chip-meta num">{{ c.count }}</span>
          <span v-if="c.tokens > 0" class="fm-chip-meta">{{ shortTokens(c.tokens) }}</span>
        </button>
        <button v-if="chipFilter !== ''" class="fm-chip fm-chip-clear" type="button" @click="chipFilter = ''">显示全部</button>
      </div>

      <div class="fm-grid">
        <div class="fm-tree" aria-label="智能体谱系">
          <div
            v-for="r in visibleRows"
            :key="r.node.sessionId"
            class="fm-row"
            :class="{ last: r.last }"
            :data-depth="r.depth"
            :style="{ paddingLeft: 'calc(' + r.depth + ' * 18px + 8px)', '--rail': 'calc(17px + ' + (r.depth - 1) + ' * 18px)' }"
          >
            <button
              class="fm-node"
              :class="{ on: selectedId === r.node.sessionId, root: isRootNode(r.node) }"
              type="button"
              :aria-current="selectedId === r.node.sessionId ? 'true' : undefined"
              @click="pick(r.node)"
            >
              <span class="dot" :class="statusDot(r.node.status)" />
              <span class="fm-node-label ellipsis">{{ labelOf(r.node, isRootNode(r.node)) }}</span>
              <span class="spacer" />
              <span class="fm-node-meta tiny num">{{ r.node.tokens > 0 ? shortTokens(r.node.tokens) : '—' }}</span>
              <span class="fm-node-meta tiny num">{{ elapsedOf(r.node) > 0 ? shortElapsed(elapsedOf(r.node)) : '—' }}</span>
            </button>
          </div>
        </div>

        <aside class="fm-detail" aria-label="节点详情">
          <template v-if="selected">
            <div class="fm-detail-hd">
              <span class="dot" :class="statusDot(selected.status)" />
              <span class="fm-detail-title ellipsis">{{ labelOf(selected, isRootNode(selected)) }}</span>
              <span class="tag" :class="statusTag(selected.status)">{{ statusText(selected.status) }}</span>
            </div>
            <dl class="fm-kv">
              <dt>会话</dt>
              <dd class="mono ellipsis" :title="selected.sessionId">{{ selected.sessionId }}</dd>
              <dt>角色</dt>
              <dd>{{ roleOf(selected) }}</dd>
              <dt>Worker</dt>
              <dd class="mono">{{ selected.worker || (isRootNode(selected) ? 'router' : '—') }}</dd>
              <dt>状态</dt>
              <dd class="row" style="gap: 5px"><span class="dot" :class="statusDot(selected.status)" />{{ statusText(selected.status) }}</dd>
              <dt>步数</dt>
              <dd class="num">{{ selected.steps > 0 ? selected.steps : '—' }}</dd>
              <dt>Token</dt>
              <dd class="num">{{ selected.tokens > 0 ? shortTokens(selected.tokens) : '—' }}</dd>
              <dt>耗时</dt>
              <dd class="num">{{ elapsedOf(selected) > 0 ? shortElapsed(elapsedOf(selected)) : '—' }}</dd>
              <dt>开始</dt>
              <dd class="num">{{ selected.startedAt ? fmtDateTime(selected.startedAt) : '—' }}</dd>
            </dl>
            <div class="fm-report-hd">
              <span>报告摘要</span>
              <span v-if="summaryBusy" class="tiny muted">读取中…</span>
            </div>
            <div class="fm-report-wrap">
              <div v-if="summaryDenied" class="hint-warn">{{ summaryDenied }}</div>
              <div v-else-if="summaryBusy && summary === ''" class="skeleton" style="height: 56px" />
              <div v-else-if="summary === ''" class="tiny muted">这次会话还没有报告（运行中或没有正文产出）。</div>
              <div v-else class="fm-report md" v-html="markdownHtml(summary.slice(0, 800))" />
            </div>
            <button class="btn btn-sm btn-accent fm-open" type="button" @click="emit('open', selected.sessionId)">打开会话日志</button>
          </template>
          <div v-else class="tiny muted">选择左侧节点查看详情。</div>
        </aside>
      </div>
    </template>
  </div>
</template>

<style scoped>
.fm {
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  flex: 1 1 auto;
}
.fm-hd {
  display: flex;
  align-items: center;
  gap: 7px;
  flex: 0 0 auto;
  min-height: 36px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--glass-line);
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.025), transparent);
}
.fm-title {
  color: var(--muted);
  font-size: 10.5px;
  letter-spacing: 0.7px;
}
.fm-count {
  font-size: 12.5px;
  color: var(--text-2);
}
.fm-count.on {
  color: var(--accent-2);
}
.fm-sep {
  color: var(--faint);
}

/* ---------- 空态 / 单节点 ---------- */
.fm-empty,
.fm-solo-wrap {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 24px;
  overflow: auto;
}
.fm-empty-title {
  color: var(--text-2);
  font-size: 13px;
  font-weight: 600;
}
.fm-empty-msg {
  max-width: 460px;
  line-height: 1.7;
  text-align: center;
}
.fm-solo {
  width: 100%;
  max-width: 460px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px 18px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius);
  background: linear-gradient(180deg, rgba(233, 180, 76, 0.05), transparent 120px), var(--glass-2);
  backdrop-filter: blur(var(--blur-1)) saturate(125%);
  -webkit-backdrop-filter: blur(var(--blur-1)) saturate(125%);
  box-shadow: 0 18px 40px rgba(0, 0, 0, 0.28);
}
.fm-solo-hd {
  display: flex;
  align-items: center;
  gap: 8px;
}
.fm-solo-title {
  color: var(--text);
  font-size: 14px;
  font-weight: 600;
}
.fm-solo-id {
  color: var(--muted);
  font-size: 11px;
}
.fm-solo-kv {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
  margin: 2px 0 0;
  padding: 9px 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  background: rgba(255, 255, 255, 0.015);
}
.fm-solo-kv dt {
  color: var(--muted);
  font-size: 10px;
  letter-spacing: 0.4px;
}
.fm-solo-kv dd {
  margin: 2px 0 0;
  color: var(--text);
  font-size: 13px;
  font-weight: 600;
}
.fm-solo-note {
  margin: 0;
  color: var(--muted);
  font-size: 11.5px;
  line-height: 1.7;
}
.fm-solo-ft {
  display: flex;
  justify-content: flex-end;
}

/* ---------- chips ---------- */
.fm-chips {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 0 0 auto;
  flex-wrap: wrap;
  padding: 7px 12px;
  border-bottom: 1px solid var(--glass-line);
}
.fm-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 24px;
  padding: 0 9px;
  border: 1px solid var(--glass-line);
  border-radius: 999px;
  background: var(--glass-1);
  color: var(--text-2);
  font-size: 11.5px;
  transition: border-color 0.14s var(--ease), background 0.14s var(--ease), color 0.14s var(--ease);
}
.fm-chip:hover {
  border-color: var(--line-3);
  color: var(--text);
}
.fm-chip.on {
  border-color: rgba(233, 180, 76, 0.5);
  background: var(--accent-soft);
  color: var(--accent-2);
}
.fm-chip-name {
  font-weight: 500;
}
.fm-chip-meta {
  color: var(--muted);
  font-size: 10.5px;
}
.fm-chip.on .fm-chip-meta {
  color: var(--accent-2);
  opacity: 0.8;
}
.fm-chip-clear {
  color: var(--muted);
}

/* ---------- 两栏 ---------- */
.fm-grid {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 320px;
  /* 行高必须能被压缩：否则详情内容一长，底部的「打开会话日志」会被裁掉 */
  grid-template-rows: minmax(0, 1fr);
  overflow: hidden;
}
.fm-tree {
  display: flex;
  flex-direction: column;
  padding: 8px 10px 14px;
  overflow: auto;
  min-height: 0;
  min-width: 0;
}
.fm-row {
  position: relative;
  flex: 0 0 auto;
}
/* 父子连线：竖线贯穿整行（行高紧凑时不出现断点），最后一行收尾 */
.fm-row:not([data-depth='0'])::before {
  content: '';
  position: absolute;
  left: var(--rail, 0px);
  top: 0;
  bottom: 0;
  width: 1px;
  background: var(--line-2);
}
.fm-row:not([data-depth='0']).last::before {
  bottom: 50%;
}
.fm-row:not([data-depth='0'])::after {
  content: '';
  position: absolute;
  left: var(--rail, 0px);
  top: 50%;
  width: 9px;
  height: 1px;
  background: var(--line-2);
}
.fm-node {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  height: 30px;
  padding: 0 8px;
  border: 1px solid transparent;
  border-radius: var(--radius-xs);
  background: transparent;
  text-align: left;
  transition: background 0.14s var(--ease), border-color 0.14s var(--ease);
}
.fm-node:hover {
  background: var(--glass-2);
  border-color: var(--glass-line);
}
.fm-node:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
.fm-node.on {
  border-color: rgba(233, 180, 76, 0.4);
  background: var(--accent-soft);
  box-shadow: inset 2px 0 0 var(--accent);
}
.fm-node.root .fm-node-label {
  color: var(--text);
  font-weight: 600;
}
.fm-node-label {
  min-width: 0;
  color: var(--text-2);
  font-size: 12.5px;
}
.fm-node-meta {
  flex: 0 0 auto;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

/* ---------- 详情 ---------- */
.fm-detail {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
  min-width: 0;
  padding: 10px 12px;
  border-left: 1px solid var(--glass-line);
  background: var(--glass-1);
  overflow: hidden;
}
/* 除报告块外的子项都不压缩，否则面板变矮时标题会被压成半行 */
.fm-detail > * {
  flex: 0 0 auto;
}
.fm-detail > .fm-report-wrap {
  flex: 1 1 auto;
  min-height: 60px;
  overflow: auto;
}
.fm-detail-hd {
  display: flex;
  align-items: center;
  gap: 7px;
}
.fm-detail-title {
  min-width: 0;
  color: var(--text);
  font-size: 13px;
  font-weight: 600;
}
.fm-kv {
  display: grid;
  grid-template-columns: 52px minmax(0, 1fr);
  gap: 5px 8px;
  align-items: center;
  margin: 0;
  font-size: 11.5px;
}
.fm-kv dt {
  color: var(--muted);
}
.fm-kv dd {
  margin: 0;
  min-width: 0;
  color: var(--text-2);
}
.fm-report-hd {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
  padding-top: 8px;
  border-top: 1px solid var(--glass-line);
  color: var(--muted);
  font-size: 10.5px;
  letter-spacing: 0.7px;
}
.fm-report {
  padding: 8px 9px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--bg-2);
  color: var(--text-2);
  font-size: 11.5px;
  line-height: 1.7;
  word-break: break-word;
}
.fm-report :deep(.md-h) {
  margin: 8px 0 4px;
  font-size: 12px;
}
.fm-report :deep(.md-h:first-child) {
  margin-top: 0;
}
.fm-report :deep(.md-ul) {
  margin-bottom: 6px;
}
.fm-report :deep(.md-pre) {
  margin: 6px 0;
  font-size: 10.5px;
}
/* 320px 详情栏里 5 列表格放不下：缩字号并允许换行，避免右侧被裁 */
.fm-report :deep(table.md-table) {
  font-size: 10px;
  white-space: normal;
}
.fm-report :deep(table.md-table th),
.fm-report :deep(table.md-table td) {
  padding: 4px 6px;
}
.fm-open {
  justify-content: center;
}
</style>
