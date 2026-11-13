<script setup lang="ts">
// Agent 工作台：左侧 worker / 历史会话，右侧「对话 / 轨迹 / 报告 / 上下文 / 编队」。
// 流式数据来自 kernel:event（agent.delta / state / tool / report），
// 上下文占用来自 agent.context（每步更新）+ context 方法回读（运行中轮询）。
// 报告 = 交付物：agentState.reports 只装根会话的最终产出（子智能体报告在 childReports，
// 由「编队」节点详情与「轨迹」页当过程证据展示）。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import AppModal from '../components/AppModal.vue'
import ContextInsight from '../components/ContextInsight.vue'
import ContextMeter from '../components/ContextMeter.vue'
import Formation from '../components/Formation.vue'
import StateBlock from '../components/StateBlock.vue'
import ChatTranscript from '../components/agent/ChatTranscript.vue'
import ReportDeck from '../components/agent/ReportDeck.vue'
import TrajectoryView from '../components/agent/TrajectoryView.vue'
import {
  agentContext,
  agentModels,
  agentResetTemplate,
  agentSaveTemplate,
  agentSchedule,
  agentSetSchedule,
  agentTemplates,
  agentTree,
  agentWorkerCreate,
  agentWorkerList,
  agentWorkerRemove,
  countdownText,
  emptyReasonHint,
  emptyReasonTitle,
  shortTokens,
  type AgentModelOption,
  type AgentTemplate,
  type DutyPhase,
  type PhaseSchedule,
} from '../lib/api.agent'
import { agentRun, agentSessions, agentSession, agentSteer, agentStop, messageOf } from '../lib/data'
import { fmtDateTime } from '../lib/format'
import { stateLabel, stateTone, toneClass, toneDot, toneTag } from '../lib/agentview'
import { toast } from '../stores/app'
import {
  agentState,
  agentUi,
  appendLine,
  beginRun,
  ensureRootNode,
  resetConversation,
  endRun,
  loadSession,
  markSteerPending,
  recordReport,
  requestContextPage,
  setContext,
  setFormation,
  setRunFacts,
  setSchedule,
} from '../stores/agent'
import type { AgentSession, AgentDiagnostic, WorkerInfo } from '../types'

/** 值班卡展示用：三个时段固定顺序。 */
const PHASES: Array<{ phase: DutyPhase; title: string; sub: string }> = [
  { phase: 'premarket', title: '盘前', sub: '开盘前计划' },
  { phase: 'intraday', title: '盘中', sub: '交易时段跟踪' },
  { phase: 'postmarket', title: '复盘', sub: '收盘后复盘' },
]

const FREQ = [15, 30, 60]

/** 主标签：对话是默认页，轨迹/报告/上下文/编队并列。 */
type TabKey = 'chat' | 'trajectory' | 'report' | 'context' | 'formation'

const workers = ref<WorkerInfo[]>([])
/** 后端给的主控 id（mainId 恒为 router），缺省时回落到 'router'。 */
const mainId = ref('router')
const templates = ref<AgentTemplate[]>([])
const sessions = ref<AgentSession[]>([])
const activeWorker = ref('router')
/** 顶部模型选择：空串表示跟随默认模型；只影响新运行，不改模板。 */
const runModel = ref('')
const models = ref<AgentModelOption[]>([])
const composer = ref('')
const composerFocused = ref(false)
const steering = ref(false)
const tab = ref<TabKey>('chat')
const loading = ref(true)
const error = ref('')
const sessionError = ref('')
const sessionBusy = ref('')
const dutyLoading = ref(true)
const dutyError = ref('')
/** 值班倒计时每秒刷新 */
const now = ref(Date.now())
const formationLoading = ref(false)
const formationError = ref('')

const composerEl = ref<HTMLTextAreaElement | null>(null)
let alive = true
let clockTimer: number | null = null
let ctxTimer: number | null = null
/** 轮询节拍：跑两拍做一次编队树对齐（6 秒）。 */
let tick = 0

const selectedWorker = computed(() => workers.value.find((w) => w.id === activeWorker.value) ?? null)
/** 当前打开的会话（用于 worker 条展示会话级元信息，而不是重复一遍 worker 描述）。 */
const currentSession = computed(() => sessions.value.find((s) => s.id === agentState.sessionId) ?? null)
/** 主控：用后端 mainId 定位（权威指针，恒为 router）；缺它才退回 kind==='main'。 */
const masterWorker = computed(() => {
  const byMainId = workers.value.find((w) => w.id === mainId.value)
  if (byMainId) return byMainId
  return workers.value.find((w) => w.kind === 'main') ?? null
})
/** 任务 Worker：主控 + 由主控按需生成的 worker（内置预设不再占左侧列表）。 */
const customWorkers = computed(() => workers.value.filter((w) => w.kind === 'custom' || w.custom === true))
/** 模型下拉只展示 hasKey=true 的项。 */
const keyModels = computed(() => models.value.filter((m) => m.hasKey))
/** 当前 worker 最近 5 个会话（列表已按开始时间倒序）。 */
const workerSessions = computed(() => sessions.value.filter((s) => s.worker === activeWorker.value).slice(0, 5))
/** 日志滚动触发器：流式段落只增长 length 不变，所以要同时看最后一行文本长度。 */
const logTick = computed(() => {
  const last = agentState.log[agentState.log.length - 1]
  return agentState.log.length + ':' + (last ? last.text.length : 0)
})
/** 对话页的条目数不含系统提示词（它在轨迹页单独展示，不算一轮对话）。 */
const chatLineCount = computed(() => agentState.log.filter((l) => l.kind !== 'system').length)
/** 只有主控能直接对话；其它 worker 只读（由主控分派运行）。 */
const isRouter = computed(() => selectedWorker.value !== null && selectedWorker.value.id === masterWorker.value?.id)
/** 运行按钮可用：RouterAgent + 没在跑 + 列表已读到。 */
const canRun = computed(() => isRouter.value && !agentState.running && selectedWorker.value !== null)
const isSteer = computed(() => agentState.running && agentState.sessionId !== '')
const canSend = computed(() => (isSteer.value ? composer.value.trim() !== '' : canRun.value) && !steering.value)
const childCount = computed(() => Math.max(0, agentState.nodes.length - 1))
/** 子会话 id → 编队节点 label：轨迹页的「子智能体产出」用它标注作者。 */
const childLabels = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {}
  for (const n of agentState.nodes) {
    if ((n.parentId ?? '') === '') continue
    if (n.label !== '') out[n.sessionId] = n.label
  }
  return out
})
/** worker id → 子节点 label：历史数据里子报告没有子会话 id，用 worker 兜底对应到子智能体名字。 */
const childWorkerLabels = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {}
  for (const n of agentState.nodes) {
    if ((n.parentId ?? '') === '') continue
    if (n.worker !== '' && n.label !== '' && out[n.worker] === undefined) out[n.worker] = n.label
  }
  return out
})
/** 子智能体过程报告份数：只用于报告页页脚的极轻入口提示（不进报告册）。 */
const childReportCount = computed(() => agentState.childReports.length)
/**
 * 状态点与文案：唯一一套映射在 lib/agentview 的 stateTone / stateLabel，
 * 工作台头部、对话页 live 行、轨迹页、会话列表、编队节点全部消费它，
 * 避免同一个「已完成」在不同地方显示成两种颜色。
 */
const statusText = computed(() => (agentState.running ? '运行中' : agentState.stateText || '空闲'))
const statusTone = computed(() => (agentState.running ? 'warn' : stateTone(agentState.stateText)))
const statusDot = computed(() => toneDot(statusTone.value))
/** 本轮已用时间（毫秒）：对话页的运行中状态行显示「思考中 12s」。 */
const runElapsedMs = computed(() => {
  if (!agentState.running || agentState.runStartedAt === 0) return 0
  return Math.max(0, now.value - agentState.runStartedAt)
})
/** 会话 id 在头部只留尾段，完整值放 title。 */
const shortSid = computed(() => {
  const id = agentState.sessionId
  if (id === '') return ''
  return id.length > 22 ? '…' + id.slice(-20) : id
})
const modelText = computed(() => {
  const hit = keyModels.value.find((m) => m.id === runModel.value)
  return hit ? modelLabel(hit) : '默认模型'
})
/** composer 下方元信息：步数 / token / 上下文占比。 */
const composerMeta = computed(() => {
  const ctx = agentState.context
  const steps = ctx?.steps ?? 0
  const tokens = ctx?.usedTokens ?? 0
  const percent = ctx?.percent ?? 0
  return { steps, tokens: shortTokens(tokens), percent }
})

const emptyWorkers: WorkerInfo[] = []

// ---------- 模板 / 调度 ----------

/** 按 id 找模板（内置 worker 与自定义 worker 都在 templates 里）。 */
function templateOfId(id: string): AgentTemplate | null {
  return templates.value.find((t) => t.id === id) ?? null
}

function templateOf(phase: DutyPhase): AgentTemplate | null {
  return templateOfId(phase)
}

/** 模型下拉文案：provider/model（有展示名时附加）。 */
function modelLabel(m: AgentModelOption): string {
  return m.displayName ? m.id + ' · ' + m.displayName : m.id
}

/** 调度信息优先取 schedule 方法的结果，缺的时候退回模板内嵌的 schedule。 */
function sched(phase: DutyPhase): PhaseSchedule {
  const fromStore = agentState.schedule.find((p) => p.phase === phase)
  if (fromStore) return fromStore
  const t = templateOf(phase)
  return t ? t.schedule : { phase, enabled: false }
}

/** 值班条空间有限：倒计时用 13h27m 这类紧凑写法，完整中文放 title。 */
function nextRunText(phase: DutyPhase): string {
  const s = sched(phase)
  if (!s.enabled) return '未开启'
  if (!s.nextRunAt) return '待排期'
  const ms = Date.parse(s.nextRunAt) - now.value
  if (Number.isNaN(ms)) return '待排期'
  if (ms <= 0) return '即将'
  const min = Math.floor(ms / 60000)
  const day = Math.floor(min / 1440)
  const hour = Math.floor((min % 1440) / 60)
  const minute = min % 60
  if (day > 0) return day + '天' + hour + 'h 后'
  if (hour > 0) return hour + 'h' + minute + 'm 后'
  return Math.max(1, minute) + 'm 后'
}

/** 倒计时的完整中文（放 title，不占行宽）。 */
function nextRunFull(phase: DutyPhase): string {
  const s = sched(phase)
  if (!s.enabled) return '自动运行已关闭'
  if (!s.nextRunAt) return '还没有排期'
  return '下次 ' + (countdownText(s.nextRunAt, now.value) || '待排期')
}

/** worker id → 展示名（历史会话的助手消息元信息用）。 */
const workerNames = computed(() => {
  const out: Record<string, string> = {}
  for (const w of workers.value) out[w.id] = w.title
  return out
})

function nextRunAtText(phase: DutyPhase): string {
  const iso = sched(phase).nextRunAt
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  return p(d.getHours()) + ':' + p(d.getMinutes())
}

/** 后端 lastStatus 是中文短语（完成 / 失败：原因 / 未配置模型 / 运行中），这里归并成短标签。 */
function lastText(phase: DutyPhase): string {
  const raw = (sched(phase).lastStatus || templateOf(phase)?.schedule.lastStatus || '').trim()
  if (raw === '') return '还没有跑过'
  if (raw === 'ok' || raw === '成功' || raw === '完成') return '成功'
  if (raw === 'failed' || raw.startsWith('失败')) return '失败'
  if (raw === '运行中') return '运行中'
  if (raw.includes('模型')) return '未配置模型'
  return raw.length > 12 ? raw.slice(0, 12) + '…' : raw
}

function lastAtText(phase: DutyPhase): string {
  const iso = sched(phase).lastRunAt || templateOf(phase)?.schedule.lastRunAt || ''
  if (iso === '') return ''
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  return fmtDateTime(iso)
}

function lastDot(phase: DutyPhase): string {
  const text = lastText(phase)
  if (text === '还没有跑过') return 'dot-off'
  if (text === '未配置模型') return 'dot-warn'
  return toneDot(stateTone(text))
}

/** 失败原因太长时放进 title，值班条上只留短标签。 */
function lastTitle(phase: DutyPhase): string {
  return sched(phase).lastStatus || templateOf(phase)?.schedule.lastStatus || '还没有跑过'
}

async function loadDuty(): Promise<void> {
  dutyLoading.value = true
  dutyError.value = ''
  try {
    const [list, phases] = await Promise.all([agentTemplates(), agentSchedule()])
    templates.value = list
    setSchedule(phases)
    if (list.length === 0) dutyError.value = '后端没有返回值班模板：请在 设置中心 检查 agentlab 插件是否已激活。'
  } catch (e) {
    dutyError.value = messageOf(e)
  } finally {
    dutyLoading.value = false
  }
}

/** 静默刷新调度（自动值班跑完后回填 lastStatus/lastRunAt，不闪骨架）。 */
async function refreshScheduleQuiet(): Promise<void> {
  try {
    const phases = await agentSchedule()
    setSchedule(phases)
    for (const t of templates.value) {
      if (t.id !== 'premarket' && t.id !== 'intraday' && t.id !== 'postmarket') continue
      const found = phases.find((p) => p.phase === t.schedule.phase)
      if (found) t.schedule = found
    }
  } catch {
    // 读不到就沿用现有数据，值班条回落到模板内嵌的 schedule
  }
}

async function togglePhase(phase: DutyPhase, enabled: boolean): Promise<void> {
  try {
    setSchedule(await agentSetSchedule(phase, { enabled }))
    const t = templateOf(phase)
    if (t) t.enabled = enabled
    toast(enabled ? '已开启自动运行（工作日近似，不含节假日）' : '已关闭自动运行', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

async function setFrequency(phase: DutyPhase, minutes: number): Promise<void> {
  try {
    setSchedule(await agentSetSchedule(phase, { everyMinutes: minutes }))
    toast('盘中频率已改为每 ' + minutes + ' 分钟', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

/** 时间编辑下拉选项：小时 00–23，分钟按 5 分钟步长。 */
const HOURS = Array.from({ length: 24 }, (_, i) => String(i).padStart(2, '0'))
const MINUTES = Array.from({ length: 12 }, (_, i) => String(i * 5).padStart(2, '0'))
/** 盘中默认交易时段（与后端 DefaultIntradayWindow 一致）。 */
const DEFAULT_WINDOW = '09:30-11:30,13:00-15:00'

/** 拆分 "HH:MM"，非法值回落默认值。 */
function timeParts(at: string | undefined): { h: string; m: string } {
  const [h, m] = (at || '').trim().split(':')
  const pad = (v: string | undefined, fallback: string) => (/^\d{1,2}$/.test(v ?? '') ? (v as string).padStart(2, '0') : fallback)
  return { h: pad(h, '09'), m: pad(m, '15') }
}

/** 分钟下拉：5 分钟步长；当前值不是 5 的倍数时也保留，避免选择框显示空白。 */
function minuteOptions(at: string | undefined): string[] {
  const cur = timeParts(at).m
  return MINUTES.includes(cur) ? MINUTES : [...MINUTES, cur].sort()
}

/** 保存某个阶段的运行时间（HH:MM）。 */
async function saveAt(phase: DutyPhase, at: string): Promise<void> {
  if (!/^\d{2}:\d{2}$/.test(at)) return
  try {
    setSchedule(await agentSetSchedule(phase, { at }))
    toast('运行时间已改为 ' + at, 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

/** 时/分下拉的 change：拼回 HH:MM 后保存。 */
async function setTimePart(phase: DutyPhase, part: 'hour' | 'minute', event: Event): Promise<void> {
  const cur = timeParts(sched(phase).at)
  const value = (event.target as HTMLSelectElement).value
  await saveAt(phase, part === 'hour' ? value + ':' + cur.m : cur.h + ':' + value)
}

/** 排期弹窗：盘前 / 复盘改运行时间，盘中改频率与交易时段（替代以前占半行的大卡控件）。 */
const timeEditor = reactive({ open: false, phase: 'premarket' as DutyPhase })

function openTimeEditor(phase: DutyPhase): void {
  timeEditor.phase = phase
  if (phase === 'intraday') {
    openWindow(phase)
    return
  }
  timeEditor.open = true
}

type WindowKey = 'amStart' | 'amEnd' | 'pmStart' | 'pmEnd'
/** 交易时段编辑器的四个时间字段。 */
const WINDOW_FIELDS: Array<{ key: WindowKey; label: string }> = [
  { key: 'amStart', label: '上午开始' },
  { key: 'amEnd', label: '上午结束' },
  { key: 'pmStart', label: '下午开始' },
  { key: 'pmEnd', label: '下午结束' },
]
/** 交易时段编辑器状态（上午/下午各一段）。 */
const windowEditor = reactive({
  open: false,
  phase: 'intraday' as DutyPhase,
  amStart: '09:30',
  amEnd: '11:30',
  pmStart: '13:00',
  pmEnd: '15:00',
  saving: false,
})

/** 把 "09:30-11:30,13:00-15:00" 拆成上下午四个时间，缺失段用默认值。 */
function splitWindow(raw: string | undefined): Pick<typeof windowEditor, WindowKey> {
  const parts = (raw || DEFAULT_WINDOW).split(',').map((x) => x.trim())
  const segment = (part: string | undefined, fallback: string): [string, string] => {
    const [start, end] = (part || '').split('-')
    const ok = (v: string | undefined) => /^\d{2}:\d{2}$/.test(v ?? '')
    const [fbStart, fbEnd] = fallback.split('-')
    return [ok(start) ? start : fbStart, ok(end) ? end : fbEnd]
  }
  const [amStart, amEnd] = segment(parts[0], '09:30-11:30')
  const [pmStart, pmEnd] = segment(parts[1], '13:00-15:00')
  return { amStart, amEnd, pmStart, pmEnd }
}

/** 打开盘中交易时段编辑器。 */
function openWindow(phase: DutyPhase): void {
  Object.assign(windowEditor, splitWindow(sched(phase).window), { open: true, phase })
}

/** 交易时段校验：每段开始早于结束，上午结束不晚于下午开始。 */
const windowError = computed(() => {
  const toMin = (s: string) => {
    const [h, m] = s.split(':').map(Number)
    return h * 60 + m
  }
  if (toMin(windowEditor.amStart) >= toMin(windowEditor.amEnd)) return '上午的开始时间需早于结束时间'
  if (toMin(windowEditor.pmStart) >= toMin(windowEditor.pmEnd)) return '下午的开始时间需早于结束时间'
  if (toMin(windowEditor.amEnd) > toMin(windowEditor.pmStart)) return '上午结束不能晚于下午开始'
  return ''
})

/** 保存交易时段（开始/结束时间拼回 window 字符串）。 */
async function saveWindow(): Promise<void> {
  if (windowError.value !== '' || windowEditor.saving) return
  windowEditor.saving = true
  try {
    const win = [windowEditor.amStart + '-' + windowEditor.amEnd, windowEditor.pmStart + '-' + windowEditor.pmEnd].join(',')
    setSchedule(await agentSetSchedule(windowEditor.phase, { window: win }))
    windowEditor.open = false
    toast('交易时段已改为 ' + win, 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    windowEditor.saving = false
  }
}

/** 交易时段编辑器里某个字段的时/分 change。 */
function setWindowPart(key: WindowKey, part: 'hour' | 'minute', event: Event): void {
  const cur = timeParts(windowEditor[key])
  const value = (event.target as HTMLSelectElement).value
  windowEditor[key] = part === 'hour' ? value + ':' + cur.m : cur.h + ':' + value
}

// ---------- 提示词编辑 ----------

const editor = reactive({ open: false, id: 'premarket', prompt: '', model: '', saving: false })

const editorTemplate = computed<AgentTemplate | null>(() => templateOfId(editor.id))
const editorTitle = computed(() => {
  const meta = PHASES.find((p) => p.phase === editor.id)
  const title = meta ? meta.title : templateOfId(editor.id)?.title || editor.id
  return '编辑提示词 · ' + title
})
const editorLen = computed(() => editor.prompt.trim().length)
const editorError = computed(() => {
  if (editorLen.value === 0) return '提示词不能为空'
  if (editorLen.value < 20) return '提示词至少 20 字（当前 ' + editorLen.value + ' 字）'
  if (editorLen.value > 8000) return '提示词最多 8000 字（当前 ' + editorLen.value + ' 字）'
  return ''
})
const editorPlaceholder = computed(() => editorTemplate.value?.description || '写下这个 worker 要关注什么、按什么格式输出。')

/** 打开某个 worker 的提示词编辑器（内置 worker 或自定义 worker）。 */
function openEditor(id: string): void {
  const t = templateOfId(id)
  if (!t) return
  editor.open = true
  editor.id = id
  editor.prompt = t.prompt
  editor.model = t.model ?? ''
}

async function savePrompt(): Promise<void> {
  if (editorError.value !== '') {
    toast(editorError.value, 'warn')
    return
  }
  editor.saving = true
  try {
    templates.value = await agentSaveTemplate(editor.id, editor.prompt, undefined, editor.model)
    editor.open = false
    toast('提示词已保存，下一次运行按新提示词执行', 'ok')
    await loadWorkers()
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    editor.saving = false
  }
}

async function restoreDefault(): Promise<void> {
  if (editor.saving) return
  editor.saving = true
  try {
    templates.value = await agentResetTemplate(editor.id)
    editor.prompt = templateOfId(editor.id)?.prompt ?? editor.prompt
    toast('已恢复默认提示词（记得保存）', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    editor.saving = false
  }
}

// ---------- worker / 会话 ----------

async function loadWorkers(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const resp = await agentWorkerList()
    const list = resp.workers
    mainId.value = resp.mainId || 'router'
    workers.value = list.length > 0 ? list : emptyWorkers
    if (list.length > 0 && !list.some((w) => w.id === activeWorker.value)) {
      activeWorker.value = list.some((w) => w.id === mainId.value) ? mainId.value : list[0]?.id ?? mainId.value
    }
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

/** 读取可用模型（只用于下拉展示，不影响运行）。 */
async function loadModels(): Promise<void> {
  try {
    models.value = (await agentModels()).models
  } catch {
    models.value = []
  }
}

async function loadSessions(): Promise<void> {
  try {
    sessions.value = await agentSessions(30)
  } catch {
    sessions.value = []
  }
}

/** 点击 worker：切换详情并打开该 worker 最近一次会话（对话页）。 */
async function selectWorker(w: WorkerInfo): Promise<void> {
  const same = activeWorker.value === w.id
  activeWorker.value = w.id
  tab.value = 'chat'
  if (same) return
  if (agentState.running) {
    toast('运行中：先停止再切换 worker 查看历史', 'warn')
    return
  }
  const latest = sessions.value.find((s) => s.worker === w.id)
  if (!latest) {
    toast(
      w.id === 'router'
        ? '「' + w.title + '」还没有历史会话，可以直接运行一次'
        : '「' + w.title + '」还没有历史会话：在主控里描述目标，主控会分派它运行',
      'warn',
    )
    return
  }
  await openSession(latest)
}

// ---------- 自定义 worker ----------

const creator = reactive({ open: false, id: '', title: '', description: '', prompt: '', model: '', saving: false })

/** 新建 worker 的即时校验（中文、与后端规则一致）。 */
const creatorError = computed(() => {
  const id = creator.id.trim()
  if (id === '') return 'worker id 不能为空'
  if (!/^[a-z][a-z0-9_-]{1,31}$/.test(id)) return 'id 需小写字母开头，后续仅小写字母/数字/下划线/中划线，长度 2~32'
  if (workers.value.some((w) => w.id === id)) return 'id 已被占用，请换一个'
  const title = creator.title.trim()
  const titleLen = [...title].length
  if (titleLen === 0 || titleLen > 40) return '标题需 1~40 字（当前 ' + titleLen + ' 字）'
  if ([...creator.description.trim()].length > 200) return '描述最多 200 字'
  const promptLen = [...creator.prompt.trim()].length
  if (promptLen < 20) return '提示词至少 20 字（当前 ' + promptLen + ' 字）'
  if (promptLen > 8000) return '提示词最多 8000 字（当前 ' + promptLen + ' 字）'
  return ''
})

/** 打开发起自定义 worker 的模态框（默认带一段可改的提示词骨架）。 */
function openCreator(): void {
  creator.open = true
  creator.id = ''
  creator.title = ''
  creator.description = ''
  creator.prompt = '你是专用研究助手。先说明要查的数据与口径，再给结论，并写清依据、不确定性与风险提示。'
  creator.model = runModel.value
  creator.saving = false
}

/** 提交新建 worker：成功后刷新列表并选中新 worker。 */
async function createWorker(): Promise<void> {
  if (creatorError.value !== '') {
    toast(creatorError.value, 'warn')
    return
  }
  creator.saving = true
  try {
    const id = creator.id.trim()
    workers.value = await agentWorkerCreate({
      id,
      title: creator.title.trim(),
      description: creator.description.trim(),
      prompt: creator.prompt.trim(),
      model: creator.model || undefined,
    })
    templates.value = await agentTemplates()
    activeWorker.value = id
    creator.open = false
    toast('已创建 worker「' + creator.title.trim() + '」', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    creator.saving = false
  }
}

/** 待删除的自定义 worker（确认框用）。 */
const remover = ref<WorkerInfo | null>(null)

/** 二次确认后删除自定义 worker；删除当前选中项时回落到 RouterAgent。 */
async function confirmRemoveWorker(): Promise<void> {
  const target = remover.value
  if (!target) return
  try {
    workers.value = await agentWorkerRemove(target.id)
    templates.value = await agentTemplates()
    if (activeWorker.value === target.id) activeWorker.value = 'router'
    toast('已删除 worker「' + target.title + '」', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    remover.value = null
  }
}

// ---------- 运行 / 插话 ----------

/** 运行一次任务：显式 model 优先，否则用顶部下拉（空串=跟随默认模型）。 */
async function runWorker(worker: string, task: string, model = runModel.value, direct = false): Promise<void> {
  if (agentState.running) return
  // 只有主控能直接对话；值班卡（direct=true）是对应时段的手动触发，不算对话
  if (!direct && worker !== 'router') {
    toast('任务 Worker 只读：请切到「RouterAgent」描述目标，由主控分派它运行', 'warn')
    return
  }
  tab.value = 'chat'
  // 新一轮 = 新会话：转写从空白开始（历史报告保留，报告册不做覆盖），
  // 然后把用户这句话落进日志，对话与轨迹才看得出“谁提了什么”。
  beginRun(worker, '', true)
  if (task.trim() !== '') appendLine(task.trim(), 'user', { role: 'user', worker })
  appendLine('开始运行 ' + worker, 'state', { role: 'status', worker })
  lastRunInput.value = task
  autoGrowComposer()
  try {
    const res = await agentRun(worker, task, undefined, model || undefined)
    if (!alive) return
    // 运行返回里带着权威 sessionId：事件偶尔缺失（纯工具步/极短会话）时用它兜底，
    // 否则编队与上下文会退回「最近一个会话」，看起来像没连上。
    if (res.sessionId && agentState.sessionId === '') {
      agentState.sessionId = res.sessionId
      ensureRootNode(res.sessionId)
    }
    // 诊断事实先落库：即使这次是失败、或者报告为空，报告页也有话可说
    setRunFacts({
      finishReason: res.finishReason ?? '',
      emptyReason: res.emptyReason ?? '',
      diagnostics: (res.diagnostics ?? []).map((d) =>
        typeof d === 'string' ? { code: '', message: d } : { code: d.code, message: d.message },
      ),
      steps: res.steps ?? 0,
      tokens: res.usage?.totalTokens ?? 0,
      model: res.model ?? model,
      at: new Date().toISOString(),
      worker: res.worker,
      input: task,
      contentFromReasoning: res.contentFromReasoning === true,
    })
    if (res.error) {
      endRun(true, res.error)
      toast('运行失败：' + res.error, 'error')
    } else {
      // 事件里通常已经记过这份报告（按内容去重）；这里兜底 run 返回值
      if (res.report) {
        recordReport({
          sessionId: res.sessionId,
          worker: res.worker,
          content: res.report,
          steps: res.steps ?? 0,
          tokens: res.usage?.totalTokens ?? 0,
          model: res.model ?? '',
          fromReasoning: res.contentFromReasoning === true,
        })
      } else {
        agentState.reportWorker = res.worker
      }
      endRun(false, '完成，用时 ' + (res.steps ?? 0) + ' 步')
      if (res.model) appendLine('模型：' + res.model, 'ok', { role: 'status', worker })
      if (res.usage) appendLine('token 用量：' + res.usage.totalTokens, 'ok', { role: 'status', worker })
      if (res.finishReason) appendLine('结束原因：' + res.finishReason, 'ok', { role: 'status', worker })
      for (const d of res.diagnostics ?? []) appendLine('[诊断] ' + (typeof d === 'string' ? d : d.message), 'error', { role: 'status', worker })
      tab.value = 'report'
    }
    await loadSessions()
    await refreshContext()
    await loadTree()
  } catch (e) {
    if (!alive) return
    const text = messageOf(e)
    setRunFacts({
      finishReason: '',
      emptyReason: '',
      diagnostics: [{ code: '', message: text }],
      steps: 0,
      tokens: 0,
      model,
      at: new Date().toISOString(),
      worker,
      input: task,
      contentFromReasoning: false,
    })
    endRun(true, text)
    toast(text, 'error')
  }
}

async function run(): Promise<void> {
  if (!canRun.value) return
  const worker = activeWorker.value
  const task = composer.value.trim()
  composer.value = ''
  await runWorker(worker, task)
}

/** 值班条「立即运行」：用该时段的 worker 跑一次（提示词由后端模板注入）。 */
async function runPhase(phase: DutyPhase): Promise<void> {
  if (agentState.running) {
    toast('已有任务在运行，等它结束后再试', 'warn')
    return
  }
  activeWorker.value = phase
  await runWorker(phase, '', runModel.value, true)
}

async function steer(): Promise<void> {
  const text = composer.value.trim()
  if (text === '') {
    toast('插话内容不能为空', 'warn')
    return
  }
  const sid = agentState.sessionId
  if (sid === '') {
    toast('会话还没就绪或已结束，请重新运行后再插话', 'warn')
    return
  }
  steering.value = true
  try {
    const res = await agentSteer(sid, text)
    if (!res.ok) throw new Error('插话未入队，请稍后重试')
    markSteerPending(text)
    composer.value = ''
    autoGrowComposer()
    toast(res.queued > 0 ? '插话已入队（排队 ' + res.queued + ' 条）' : '插话已发送', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    steering.value = false
  }
}

async function submitComposer(): Promise<void> {
  if (isSteer.value) {
    await steer()
    return
  }
  await run()
}

/**
 * 新建会话：清空当前转写与报告册，回到 RouterAgent 的空白对话。
 * 运行中不允许：先停止再开新会话，否则会把正在写入的会话弄混。
 */
function newChat(): void {
  if (agentState.running) {
    toast('运行中：先停止，再新建会话', 'warn')
    return
  }
  resetConversation()
  activeWorker.value = mainId.value || 'router'
  tab.value = 'chat'
  sessionError.value = ''
  composer.value = ''
  void nextTick(() => {
    composerEl.value?.focus()
    autoGrowComposer()
  })
  toast('已新建会话', 'ok')
}

async function stop(): Promise<void> {
  const id = agentState.sessionId
  if (!id) {
    // 会话 id 还没落下来（极早期停止）：只做本地收尾，不猜后端状态。
    endRun(false, '已本地标记停止', true)
    return
  }
  try {
    await agentStop(id)
    endRun(false, '已请求停止', true)
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

// ---------- composer ----------

/** 输入框自动增高：1~6 行，超过 6 行内部滚动。 */
function autoGrowComposer(): void {
  void nextTick(() => {
    const el = composerEl.value
    if (!el) return
    el.style.height = 'auto'
    const max = 6 * 21 + 18
    const next = Math.min(el.scrollHeight, max)
    el.style.height = Math.max(next, 34) + 'px'
    el.style.overflowY = el.scrollHeight > max ? 'auto' : 'hidden'
  })
}

/** 空态建议 chip：填入输入框并聚焦（不直接发送，让用户还能改）。 */
function fillComposer(text: string): void {
  composer.value = text
  void nextTick(() => {
    composerEl.value?.focus()
    autoGrowComposer()
  })
}

// ---------- 历史会话 ----------

async function openSession(s: AgentSession): Promise<void> {
  // 运行中切会话会把「正在追加的日志」与「历史会话」混在一起：先拦住，让用户停止或插话。
  if (agentState.running && s.id !== agentState.sessionId) {
    toast('运行中：先停止再切换会话（或在输入框里插话）', 'warn')
    return
  }
  if (sessionBusy.value !== '') return
  sessionBusy.value = s.id
  sessionError.value = ''
  try {
    const detail = await agentSession(s.id)
    // 日志、报告、上下文整份替换（切换会话不混入上一份）
    loadSession(detail)
    // 会话属于哪个 worker，就把选中项切到哪个 worker：
    // 这样顶部 worker 条、只读约束与输入区都能与当前会话对得上（非 RouterAgent 会话没有输入框）。
    if (s.worker !== '' && workers.value.some((w) => w.id === s.worker)) activeWorker.value = s.worker
    tab.value = agentState.reports.length > 0 ? 'report' : 'chat'
    await refreshContext()
    await loadTree()
  } catch (e) {
    sessionError.value = messageOf(e)
  } finally {
    sessionBusy.value = ''
  }
}

function sessionState(s: AgentSession): string {
  // 会话列表的状态徽标跟同一套色调：已完成绿 / 失败红 / 运行中琥珀
  return toneTag(stateTone(s.status))
}

function sessionStateText(s: AgentSession): string {
  // 历史会话的「成功」也统一叫已完成，与顶部状态、轨迹页同一口径
  return stateLabel(s.status)
}

// ---------- 上下文 / 编队 ----------

/**
 * 回读上下文快照。
 * sessionId 为空时后端会回退到「最近一次会话」——这样空闲状态下也能看到上下文。
 */
async function refreshContext(): Promise<void> {
  try {
    const c = await agentContext(agentState.sessionId)
    if (c) setContext(c)
  } catch {
    // 还没有任何会话 / 后端未实现 context 时不打扰用户
  }
}

/** 打开上下文页：先把快照读新，再切标签（避免看到上一轮的旧数字）。 */
function openContext(): void {
  tab.value = 'context'
  void refreshContext()
}

// 抽屉里的小环点「查看完整洞察」：切到工作台的上下文页（无论当前在哪一页）
watch(
  () => agentUi.contextPageSeq,
  () => openContext(),
)

async function loadTree(): Promise<void> {
  formationLoading.value = true
  formationError.value = ''
  try {
    const tree = await agentTree(agentState.sessionId)
    if (tree.nodes.length > 0) setFormation(tree)
  } catch (e) {
    if (agentState.nodes.length === 0 && agentState.sessionId !== '') formationError.value = messageOf(e)
  } finally {
    formationLoading.value = false
  }
}

/** 编队标签打开：每次都回读一次树（子智能体可能刚被派发，缓存的一层节点会看不见新人）。 */
function openFormation(): void {
  tab.value = 'formation'
  void loadTree()
}

// ---------- 运行诊断（空回复告警卡） ----------

/** 本轮原始任务文本：RunFacts 里没有它时用它兜底重跑。 */
const lastRunInput = ref('')
/** 本地动作后的反馈文案（重跑 / 换模型）。 */
const diagHint = ref('')

/** 告警卡是否出现：运行结束且（报告为空 或 后端返回了诊断）。 */
const showDiag = computed(() => {
  if (agentState.running) return false
  const run = agentState.lastRun
  if (run !== null && run.diagnostics.length > 0) return true
  if (agentState.reports.length > 0) return false
  if (run !== null) return true
  return agentState.stateText === '失败'
})

/** 标题用真实原因：emptyReason 是后端给的稳定枚举，翻成人话；没有就按 finishReason / 事实描述。 */
const diagTitle = computed(() => {
  const run = agentState.lastRun
  if (run?.emptyReason) return emptyReasonTitle(run.emptyReason) || '本轮没有正文产出（' + run.emptyReason + '）'
  if (run?.finishReason === 'length') return '模型返回空正文（推理占满输出预算）'
  if (run?.finishReason === 'stop') return '模型正常结束，但没有产出正文'
  if (run?.finishReason === 'content_filter') return '内容被安全策略拦截，没有正文'
  if (run?.finishReason) return '本轮没有正文产出（结束原因：' + run.finishReason + '）'
  if (agentState.stateText === '失败') return '本轮运行失败，没有产出报告'
  return '本轮没有正文产出'
})

/** 原因代码对应的下一步建议（没有枚举时为空，不凭空给建议）。 */
const diagHintText = computed(() => emptyReasonHint(agentState.lastRun?.emptyReason ?? ''))

/** 诊断明细：后端给的每条一行；没有就在正文里明确说没有。 */
const diagLines = computed<AgentDiagnostic[]>(() => agentState.lastRun?.diagnostics ?? [])

/** 已用步数与 token：没有诊断信息时这两个数是唯一的事实。 */
const diagFacts = computed(() => {
  const run = agentState.lastRun
  const steps = run?.steps ?? agentState.context?.steps ?? 0
  const tokens = run?.tokens ?? agentState.context?.usedTokens ?? 0
  return { steps, tokens: shortTokens(tokens), model: run?.model ?? agentState.context?.model ?? '' }
})

/** 可重跑：有原始任务文本（或上一轮确实是 RouterAgent 发的）且没在运行。 */
const canRetry = computed(() => !agentState.running && (lastRunInput.value !== '' || (agentState.lastRun?.input ?? '') !== ''))

/** 「把输出上限调到 8192 后重试」：本地提示 + 带上这句口径重跑（不臆造后端接口）。 */
async function retryWithBudget(): Promise<void> {
  if (agentState.running) return
  const task = lastRunInput.value || agentState.lastRun?.input || ''
  if (task === '') {
    diagHint.value = '已记住口径：把输出上限调到 8192，再运行一次即可。'
    toast('请把输出上限调到 8192 后再运行', 'warn')
    return
  }
  diagHint.value = '按「输出上限 8192」重跑一次…'
  await runWorker('router', task + '\n\n（重跑口径：若正文为空，先确认输出上限已调到 8192）')
}

/** 「换一个模型」：切到下拉里的下一个可用模型（不改后端配置，只影响下一次运行）。 */
function switchModel(): void {
  const list = keyModels.value
  if (list.length < 2) {
    diagHint.value = '当前只有一个可用模型：到设置中心再加一个，才能换模型重跑。'
    toast('当前只有一个可用模型，先到设置中心添加', 'warn')
    return
  }
  const idx = list.findIndex((m) => m.id === runModel.value)
  const next = list[(idx + 1) % list.length]
  if (!next) return
  runModel.value = next.id
  diagHint.value = '已切到 ' + modelLabel(next) + '：再点「运行」就用新模型重跑。'
  toast('已切换到 ' + modelLabel(next), 'ok')
}

/** 点编队节点「打开会话日志」：会话列表里有就用它的元数据，没有就按 sessionId 直读。 */
async function openChildSession(sessionId: string): Promise<void> {
  const found = sessions.value.find((s) => s.id === sessionId)
  if (found) {
    await openSession(found)
    return
  }
  const tree = agentState.nodes.find((n) => n.sessionId === sessionId)
  await openSession({
    id: sessionId,
    worker: tree?.worker ?? '',
    title: tree?.label || sessionId,
    startedAt: tree?.startedAt ?? '',
    status: tree?.status === 'failed' ? 'failed' : tree?.status === 'running' ? 'running' : 'ok',
  })
}

// ---------- 生命周期 ----------

watch(
  () => agentState.sessionId,
  (id) => {
    if (id !== '') void refreshContext()
  },
)

watch(
  () => agentState.running,
  (running) => {
    if (!running) void loadTree()
  },
)

// 自动值班跑完（autoRunning 归零）后回读一次调度：值班条上的「上次结果」要立刻更新
watch(
  () => agentState.autoRunning,
  (current, previous) => {
    if (previous !== '' && current === '') {
      void refreshScheduleQuiet()
      void loadSessions()
    }
  },
)

onMounted(async () => {
  clockTimer = window.setInterval(() => {
    now.value = Date.now()
  }, 1000)
  // 运行中每 3 秒回读一次上下文：以 context 方法为准，事件只做增量
  ctxTimer = window.setInterval(() => {
    void refreshContext()
    if (!agentState.running) return
    tick = (tick + 1) % 2
    if (tick === 0) void loadTree()
  }, 3000)
  await Promise.all([loadWorkers(), loadSessions(), loadDuty(), loadModels()])
  await Promise.all([loadTree(), refreshContext()])
  autoGrowComposer()
})

onBeforeUnmount(() => {
  alive = false
  if (clockTimer !== null) window.clearInterval(clockTimer)
  if (ctxTimer !== null) window.clearInterval(ctxTimer)
})
</script>

<template>
  <section class="card al">
    <!-- 顶部：标题 + 状态 + 会话 id + 模型 + 停止/运行 -->
    <div class="card-hd al-hd">
      <span class="card-title">Agent 工作台</span>
      <span class="al-status">
        <span class="dot" :class="statusDot" />
        <span class="tiny" :class="toneClass(statusTone)">{{ statusText }}</span>
      </span>
      <span v-if="agentState.sessionId" class="al-sid mono ellipsis" :title="agentState.sessionId">{{ shortSid }}</span>
      <span class="spacer" />
      <span class="card-tools">
        <button class="btn btn-sm btn-ghost" type="button" title="开启一段新对话（清空当前转写与报告册）" @click="newChat">＋ 新会话</button>
        <button class="btn btn-sm" type="button" :disabled="!agentState.running" @click="stop">停止</button>
        <button v-if="isRouter" class="btn btn-sm btn-accent" type="button" :disabled="!canRun" title="和 RouterAgent 对话" @click="run">
          {{ agentState.running ? '运行中…' : '运行' }}
        </button>
      </span>
    </div>

    <!-- 值班：压成一条紧凑横排；时间/频率走弹窗编辑 -->
    <div v-if="dutyLoading" class="al-duty">
      <div v-for="p in PHASES" :key="p.phase" class="skeleton" style="height: 30px; flex: 1 1 0" />
    </div>
    <div v-else-if="dutyError" class="al-banner">
      <span class="hint-warn">值班调度不可用：{{ dutyError }}</span>
      <span class="spacer" />
      <button class="btn btn-sm btn-ghost" type="button" @click="loadDuty">重试</button>
    </div>
    <div v-else class="al-duty">
      <div
        v-for="p in PHASES"
        :key="p.phase"
        class="al-duty-item"
        :class="{ on: sched(p.phase).enabled, live: agentState.autoRunning === p.phase }"
      >
        <span class="dot" :class="lastDot(p.phase)" :title="lastTitle(p.phase) + (lastAtText(p.phase) ? ' · ' + lastAtText(p.phase) : '')" />
        <span class="al-duty-title">{{ p.title }}</span>
        <span class="al-duty-num num" :title="nextRunFull(p.phase)">{{ nextRunText(p.phase) }}</span>
        <button class="al-duty-when tiny" type="button" :title="'排期 / 交易时段（' + p.sub + '）'" @click="openTimeEditor(p.phase)">
          <template v-if="p.phase === 'intraday'">每 {{ sched(p.phase).everyMinutes || 30 }} 分</template>
          <template v-else>{{ nextRunAtText(p.phase) || sched(p.phase).at || '--:--' }}</template>
        </button>
        <span class="spacer" />
        <label class="check al-duty-auto" :title="sched(p.phase).enabled ? '关闭自动运行' : '开启自动运行'">
          <input type="checkbox" :checked="sched(p.phase).enabled" @change="togglePhase(p.phase, ($event.target as HTMLInputElement).checked)" />
          <span class="tiny">自动</span>
        </label>
        <button class="btn btn-sm btn-ghost" type="button" @click="openEditor(p.phase)">
          提示词<span v-if="templateOf(p.phase)?.edited" class="al-edited" title="已改过">·</span>
        </button>
        <button class="btn btn-sm" type="button" :disabled="agentState.running" @click="runPhase(p.phase)">立即运行</button>
      </div>
    </div>

    <div class="al-body">
      <!-- 左栏：主控 / 任务 worker / 历史会话（切换标签页时不收起） -->
      <aside class="al-left">
        <button class="al-newchat" type="button" title="开启一段新对话（清空当前转写与报告册）" @click="newChat">
          <span class="al-newchat-plus" aria-hidden="true">＋</span>
          <span>新建会话</span>
        </button>
        <StateBlock v-if="loading" kind="loading" title="读取 worker…" />
        <StateBlock v-else-if="error" kind="error" title="读取失败" :message="error" action-text="重试" @action="loadWorkers" />
        <template v-else>
          <div class="al-section">RouterAgent</div>
          <div v-if="masterWorker" class="al-workers">
            <div
              class="al-worker"
              :class="{ on: activeWorker === masterWorker.id }"
              role="button"
              tabindex="0"
              :aria-current="activeWorker === masterWorker.id ? 'true' : undefined"
              @click="selectWorker(masterWorker)"
              @keydown.enter.prevent="selectWorker(masterWorker)"
              @keydown.space.prevent="selectWorker(masterWorker)"
            >
              <span class="al-worker-row">
                <span class="al-worker-title">{{ masterWorker.title }}</span>
                <span class="spacer" />
                <span v-if="masterWorker.model" class="tiny muted ellipsis">{{ masterWorker.model }}</span>
              </span>
              <span class="al-worker-desc">{{ masterWorker.description }}</span>
            </div>
          </div>
          <div v-else class="tiny muted">没读到 RouterAgent：请检查 agentlab 插件是否已激活。</div>

          <div class="al-section">
            任务 Worker
            <span class="spacer" />
            <button class="btn btn-sm btn-ghost" type="button" title="高级入口：手动写一条 worker 定义" @click="openCreator">新建</button>
          </div>
          <div v-if="customWorkers.length === 0" class="al-hint tiny muted">
            还没有任务 worker：直接和主控说目标，它会按需创建（worker.create）。
          </div>
          <div v-else class="al-workers">
            <div
              v-for="w in customWorkers"
              :key="w.id"
              class="al-worker"
              :class="{ on: activeWorker === w.id }"
              role="button"
              tabindex="0"
              :aria-current="activeWorker === w.id ? 'true' : undefined"
              @click="selectWorker(w)"
              @keydown.enter.prevent="selectWorker(w)"
              @keydown.space.prevent="selectWorker(w)"
            >
              <span class="al-worker-row">
                <span class="al-worker-title">{{ w.title }}</span>
                <span class="tag">自定义</span>
                <span class="spacer" />
                <button class="al-del" type="button" title="删除该 worker" @click.stop="remover = w">×</button>
              </span>
              <span class="al-worker-desc">{{ w.description }}</span>
            </div>
          </div>

          <div class="al-section">
            历史会话
            <span class="spacer" />
            <button class="btn btn-sm btn-ghost" type="button" @click="loadSessions">刷新</button>
          </div>
          <div v-if="sessionError" class="hint-warn">{{ sessionError }}</div>
          <div v-if="sessions.length === 0" class="tiny muted">还没有历史会话。</div>
          <div v-else class="al-sessions">
            <button
              v-for="s in sessions"
              :key="s.id"
              class="al-session"
              :class="{ on: agentState.sessionId === s.id }"
              type="button"
              @click="openSession(s)"
            >
              <span class="al-session-row">
                <span class="grow ellipsis">{{ s.title }}</span>
                <span class="tag" :class="sessionState(s)">{{ sessionStateText(s) }}</span>
              </span>
              <span class="al-session-meta tiny muted">
                <span class="mono">{{ s.worker }}</span>
                <span>{{ fmtDateTime(s.startedAt) }}</span>
                <span v-if="s.tokens">· {{ s.tokens }} tok</span>
                <span v-if="sessionBusy === s.id">· 读取中…</span>
              </span>
            </button>
          </div>
        </template>
      </aside>

      <div class="al-main">
        <!-- worker 条：所有标签页都保留（上下文/编队页顶部也要能看出当前是谁的会话） -->
        <div v-if="selectedWorker" class="al-wbar">
          <span class="al-wbar-title">{{ selectedWorker.title }}</span>
          <span v-if="selectedWorker.custom" class="tag tag-accent">自定义</span>
          <span class="tag">模型 {{ selectedWorker.model || runModel || '默认' }}</span>
          <span v-if="currentSession" class="al-wbar-desc tiny muted ellipsis" :title="currentSession.title">
            {{ currentSession.title }} · {{ fmtDateTime(currentSession.startedAt) }}
            <template v-if="currentSession.tokens"> · {{ currentSession.tokens }} tok</template>
          </span>
          <span v-else class="al-wbar-desc tiny muted ellipsis">{{ selectedWorker.description }}</span>
          <span class="spacer" />
          <button v-if="workerSessions.length > 0" class="btn btn-sm btn-ghost" type="button" @click="openSession(workerSessions[0] as AgentSession)">
            最近一次会话
          </button>
          <button class="btn btn-sm btn-ghost" type="button" @click="openEditor(selectedWorker.id)">编辑提示词</button>
        </div>

        <div class="tabs al-tabs">
          <button class="tab" :class="{ on: tab === 'chat' }" type="button" @click="tab = 'chat'">
            对话 <span class="tiny muted">{{ chatLineCount }}</span>
          </button>
          <button class="tab" :class="{ on: tab === 'trajectory' }" type="button" @click="tab = 'trajectory'">轨迹</button>
          <button class="tab" :class="{ on: tab === 'report' }" type="button" @click="tab = 'report'">
            报告 <span v-if="agentState.reports.length > 0" class="tiny muted">{{ agentState.reports.length }}</span>
          </button>
          <button class="tab" :class="{ on: tab === 'context' }" type="button" @click="openContext">上下文</button>
          <button class="tab" :class="{ on: tab === 'formation' }" type="button" @click="openFormation">
            编队 <span v-if="childCount > 0" class="tiny muted">{{ childCount }}</span>
          </button>
          <span class="spacer" />
          <ContextMeter :context="agentState.context" @refresh="refreshContext" @open="openContext" />
          <span class="al-tabs-meta tiny muted">
            {{ agentState.worker || agentState.reportWorker || '—' }} ·
            <span :class="toneClass(statusTone)">{{ agentState.running ? '运行中' : agentState.stateText || '空闲' }}</span>
          </span>
        </div>

        <!-- 对话 -->
        <div v-show="tab === 'chat'" class="al-page">
          <ChatTranscript
            :lines="agentState.log"
            :worker="selectedWorker?.title || agentState.worker || 'RouterAgent'"
            :model="agentState.context?.model || ''"
            :running="agentState.running"
            :tick="logTick"
            :worker-names="workerNames"
            :state="agentState.stateText"
            :elapsed-ms="runElapsedMs"
            @suggest="fillComposer"
          />
        </div>

        <!-- 轨迹 -->
        <div v-show="tab === 'trajectory'" class="al-page">
          <TrajectoryView
            :lines="agentState.log"
            :reports-count="agentState.reports.length"
            :child-reports="agentState.childReports"
            :child-labels="childLabels"
            :child-worker-labels="childWorkerLabels"
            :running="agentState.running"
            :tick="logTick"
          />
        </div>

        <!-- 报告：告警卡 + 最终交付物（多轮时给轮次标签带） -->
        <div v-show="tab === 'report'" class="al-page al-report-page">
          <section v-if="showDiag" class="al-diag" :class="{ 'al-diag-soft': agentState.reports.length > 0 }" role="alert" aria-live="polite">
            <div class="al-diag-hd">
              <span class="dot dot-err" />
              <span class="al-diag-title">{{ diagTitle }}</span>
              <span class="spacer" />
              <span class="tag">{{ agentState.reportWorker || agentState.worker || '—' }}</span>
            </div>
            <ul v-if="diagLines.length > 0" class="al-diag-list">
              <li v-for="(d, i) in diagLines" :key="i" class="al-diag-item">
                <span v-if="d.code" class="al-diag-code mono">{{ d.code }}</span>
                <span class="al-diag-msg">{{ d.message }}</span>
              </li>
            </ul>
            <div v-else class="al-diag-item">
              <span class="al-diag-msg">
                后端未返回诊断信息。本轮已跑 {{ diagFacts.steps }} 步、累计 ~{{ diagFacts.tokens }} token<template
                  v-if="diagFacts.model"
                >，模型 {{ diagFacts.model }}</template>。
              </span>
            </div>
            <div v-if="diagHintText" class="al-diag-item">
              <span class="al-diag-code mono">下一步</span>
              <span class="al-diag-msg">{{ diagHintText }}</span>
            </div>
            <div class="al-diag-ft">
              <button class="btn btn-sm btn-accent" type="button" :disabled="!canRetry" @click="retryWithBudget">
                把输出上限调到 8192 后重试
              </button>
              <button class="btn btn-sm" type="button" :disabled="agentState.running" @click="switchModel">换一个模型</button>
              <span class="spacer" />
              <button class="btn btn-sm btn-ghost" type="button" @click="tab = 'chat'">看对话</button>
            </div>
            <div v-if="diagHint" class="al-diag-hint tiny muted">{{ diagHint }}</div>
          </section>
          <ReportDeck
            :reports="agentState.reports"
            :active-id="agentState.activeReportId"
            :running="agentState.running"
            :child-count="childReportCount"
            @open-formation="openFormation"
            @open-trajectory="tab = 'trajectory'"
          />
        </div>

        <!-- 上下文 -->
        <div v-show="tab === 'context'" class="al-page al-context">
          <ContextInsight :context="agentState.context" variant="page" @refresh="refreshContext" />
        </div>

        <!-- 编队 -->
        <div v-show="tab === 'formation'" class="al-page">
          <Formation
            v-if="tab === 'formation'"
            :nodes="agentState.nodes"
            :root-id="agentState.rootId"
            :workers="workers"
            :loading="formationLoading"
            :error="formationError"
            :total="agentState.formationMeta.total"
            :active-count="agentState.formationMeta.activeCount"
            @refresh="loadTree"
            @open="openChildSession"
          />
        </div>

        <!-- 输入区：浮动玻璃卡片（只读 worker 不渲染；上下文/编队页也保留，随时能插话） -->
        <form v-if="isRouter" class="al-composer" @submit.prevent="submitComposer">
          <div class="al-composer-box" :class="{ focus: composerFocused }">
            <textarea
              ref="composerEl"
              v-model="composer"
              class="al-composer-input"
              rows="1"
              :placeholder="isSteer
                ? '插话：例如「只看半导体和白酒」「先别管指数」'
                : '和 RouterAgent 说一句话：例如「复盘今天的涨停梯队，再给我一份明日预案」'"
              :aria-label="isSteer ? '插话内容' : '任务输入'"
              @input="autoGrowComposer"
              @focus="composerFocused = true"
              @blur="composerFocused = false"
              @keydown.enter.exact.prevent="submitComposer"
            />
            <div class="al-composer-ft">
              <span class="al-mode" :class="{ steer: isSteer }">{{ isSteer ? '插话' : '新任务' }}</span>
              <select v-model="runModel" class="al-model-inline" aria-label="本次运行的模型" :title="'本次运行模型：' + modelText">
                <option value="">跟随默认模型</option>
                <option v-for="m in keyModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
              </select>
              <span class="spacer" />
              <span class="al-composer-kbd tiny">Enter 发送 · Shift+Enter 换行</span>
              <button class="al-send" type="submit" :disabled="!canSend" :title="isSteer ? '发送插话' : '运行 RouterAgent'" :aria-label="isSteer ? '发送插话' : '运行 RouterAgent'">
                <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                  <path d="M12 19V5M6 11l6-6 6 6" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
              </button>
            </div>
          </div>
          <div class="al-composer-meta tiny muted">
            {{ composerMeta.steps }} 步 · ~{{ composerMeta.tokens }} tok · 上下文 {{ composerMeta.percent.toFixed(1) }}%
            <template v-if="agentState.running"> · 运行中可插话</template>
          </div>
        </form>

        <!-- 只读 worker：说明为什么没有输入框（给一个回到 RouterAgent 的入口） -->
        <div v-if="!isRouter" class="al-readonly tiny muted">
          <span class="tag">只读</span>
          <span class="grow">只读会话（值班 / 子任务）：要追问或接着做，回 RouterAgent 描述目标，它会挑合适的 worker 或新建一个。</span>
          <button v-if="masterWorker" class="btn btn-sm btn-ghost" type="button" @click="selectWorker(masterWorker)">回到 RouterAgent</button>
        </div>
      </div>
    </div>

    <!-- 排期弹窗（盘前 / 复盘） -->
    <AppModal
      v-if="timeEditor.open"
      :title="'排期 · ' + (PHASES.find((p) => p.phase === timeEditor.phase)?.title || '')"
      sub="只在工作日近似执行，不含节假日；开关在值班条上"
      @close="timeEditor.open = false"
    >
      <div class="al-sched">
        <div class="tiny muted">每个交易日 {{ sched(timeEditor.phase).at || '09:15' }} 自动运行一次。</div>
        <div class="al-win-row">
          <select
            class="select al-time-part"
            :value="timeParts(sched(timeEditor.phase).at).h"
            aria-label="运行时间（时）"
            @change="setTimePart(timeEditor.phase, 'hour', $event)"
          >
            <option v-for="h in HOURS" :key="h" :value="h">{{ h }}</option>
          </select>
          <span class="al-time-sep">:</span>
          <select
            class="select al-time-part"
            :value="timeParts(sched(timeEditor.phase).at).m"
            aria-label="运行时间（分）"
            @change="setTimePart(timeEditor.phase, 'minute', $event)"
          >
            <option v-for="m in minuteOptions(sched(timeEditor.phase).at)" :key="m" :value="m">{{ m }}</option>
          </select>
          <span class="spacer" />
          <span class="tiny muted">上次：{{ lastAtText(timeEditor.phase) || '—' }} · {{ lastText(timeEditor.phase) }}</span>
        </div>
      </div>
      <template #footer>
        <span class="spacer" />
        <button class="btn btn-sm btn-accent" type="button" @click="timeEditor.open = false">完成</button>
      </template>
    </AppModal>

    <!-- 盘中频率 + 交易时段 -->
    <AppModal
      v-if="windowEditor.open"
      title="盘中排期"
      sub="只在时段内按频率巡检；工作日近似，不含节假日"
      @close="windowEditor.open = false"
    >
      <div class="al-win-grid">
        <label v-for="f in WINDOW_FIELDS" :key="f.key" class="al-win-field">
          <span class="tiny muted">{{ f.label }}</span>
          <span class="al-win-row">
            <select
              class="select al-time-part"
              :value="timeParts(windowEditor[f.key]).h"
              :aria-label="f.label + '（时）'"
              @change="setWindowPart(f.key, 'hour', $event)"
            >
              <option v-for="h in HOURS" :key="h" :value="h">{{ h }}</option>
            </select>
            <span class="al-time-sep">:</span>
            <select
              class="select al-time-part"
              :value="timeParts(windowEditor[f.key]).m"
              :aria-label="f.label + '（分）'"
              @change="setWindowPart(f.key, 'minute', $event)"
            >
              <option v-for="m in minuteOptions(windowEditor[f.key])" :key="m" :value="m">{{ m }}</option>
            </select>
          </span>
        </label>
        <label class="al-win-field">
          <span class="tiny muted">巡检频率</span>
          <select
            class="select al-freq"
            :value="sched(windowEditor.phase).everyMinutes || 30"
            aria-label="盘中频率"
            @change="setFrequency(windowEditor.phase, Number(($event.target as HTMLSelectElement).value))"
          >
            <option v-for="m in FREQ" :key="m" :value="m">每 {{ m }} 分钟</option>
          </select>
        </label>
      </div>
      <template #footer>
        <span v-if="windowError" class="tiny hint-warn">{{ windowError }}</span>
        <span v-else class="tiny muted">当前 {{ sched(windowEditor.phase).window || DEFAULT_WINDOW }}</span>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" :disabled="windowEditor.saving" @click="windowEditor.open = false">取消</button>
        <button class="btn btn-sm btn-accent" type="button" :disabled="windowEditor.saving || windowError !== ''" @click="saveWindow">
          {{ windowEditor.saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </AppModal>

    <AppModal v-if="editor.open" :title="editorTitle" :sub="editorTemplate?.title || ''" @close="editor.open = false">
      <div class="al-editor">
        <div class="tiny muted">{{ editorPlaceholder }}</div>
        <textarea v-model="editor.prompt" class="textarea al-editor-area" rows="14" spellcheck="false" aria-label="提示词" />
        <div class="row" style="gap: 8px">
          <span class="tiny muted">模型</span>
          <select v-model="editor.model" class="select al-model" aria-label="该 worker 的默认模型">
            <option value="">跟随默认模型</option>
            <option v-for="m in keyModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
          </select>
          <span class="spacer" />
          <span class="tiny" :class="editorError ? 'hint-warn' : 'muted'">
            {{ editorLen }} 字（20 ~ 8000）<template v-if="editorError"> · {{ editorError }}</template>
          </span>
          <span v-if="editorTemplate?.edited" class="tag tag-accent">已修改</span>
        </div>
      </div>
      <template #footer>
        <button class="btn btn-sm btn-ghost" type="button" :disabled="editor.saving" @click="restoreDefault">恢复默认</button>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" :disabled="editor.saving" @click="editor.open = false">取消</button>
        <button class="btn btn-sm btn-accent" type="button" :disabled="editor.saving || editorError !== ''" @click="savePrompt">
          {{ editor.saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </AppModal>

    <AppModal v-if="creator.open" title="手动新建 Worker" sub="高级入口：平时直接在主控对话框里说需求，主控会按需创建 worker" @close="creator.open = false">
      <div class="al-editor">
        <label class="al-field">
          <span class="tiny muted">ID（小写字母开头，2~32 位）</span>
          <input v-model="creator.id" class="input" placeholder="etf_flow" spellcheck="false" />
        </label>
        <label class="al-field">
          <span class="tiny muted">标题（1~40 字）</span>
          <input v-model="creator.title" class="input" placeholder="ETF 资金" />
        </label>
        <label class="al-field">
          <span class="tiny muted">描述（最多 200 字）</span>
          <input v-model="creator.description" class="input" placeholder="跟踪 ETF 份额与资金流" />
        </label>
        <label class="al-field">
          <span class="tiny muted">提示词（20~8000 字）</span>
          <textarea v-model="creator.prompt" class="textarea al-editor-area" rows="6" spellcheck="false" aria-label="worker 提示词" />
        </label>
        <div class="row" style="gap: 8px">
          <span class="tiny muted">默认模型</span>
          <select v-model="creator.model" class="select al-model" aria-label="worker 默认模型">
            <option value="">跟随默认模型</option>
            <option v-for="m in keyModels" :key="m.id" :value="m.id">{{ modelLabel(m) }}</option>
          </select>
          <span class="spacer" />
          <span v-if="creatorError" class="tiny hint-warn">{{ creatorError }}</span>
        </div>
      </div>
      <template #footer>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" :disabled="creator.saving" @click="creator.open = false">取消</button>
        <button class="btn btn-sm btn-accent" type="button" :disabled="creator.saving || creatorError !== ''" @click="createWorker">
          {{ creator.saving ? '创建中…' : '创建' }}
        </button>
      </template>
    </AppModal>

    <AppModal v-if="remover" title="删除 Worker" :sub="remover.title" @close="remover = null">
      <div class="tiny muted">删除后不可恢复：该 worker 的提示词与模型覆盖会一并清理；已产生的历史会话仍可查看。</div>
      <template #footer>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" @click="remover = null">取消</button>
        <button class="btn btn-sm btn-accent" type="button" @click="confirmRemoveWorker">确认删除</button>
      </template>
    </AppModal>
  </section>
</template>

<style scoped>
.al {
  min-height: 0;
}
.al-hd {
  flex-wrap: wrap;
  row-gap: 4px;
  padding-top: 7px;
  padding-bottom: 7px;
}
.al-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--text-2);
}

/* ---------- 状态色调（全站唯一一套，来自 lib/agentview 的 stateTone） ----------
   已完成 / 成功 = 绿（#4ce0ab 在 --panel 上对比度 ≈ 11:1，深色底可读）；
   进行中 / 已停止 = 琥珀；失败 = 红；空闲 = 灰。文字与圆点同色系。 */
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
/* 会话列表的状态徽标：与点/文字同一套色板 */
.tag.tone-tag-ok {
  border-color: rgba(34, 192, 138, 0.42);
  background: rgba(34, 192, 138, 0.12);
  color: #4ce0ab;
}
.tag.tone-tag-warn {
  border-color: rgba(233, 180, 76, 0.42);
  background: var(--accent-soft);
  color: var(--accent-2);
}
.tag.tone-tag-err {
  border-color: rgba(255, 90, 95, 0.42);
  background: rgba(255, 90, 95, 0.12);
  color: var(--err);
}
.tag.tone-tag-dim {
  border-color: var(--line-2);
  background: var(--glass-1);
  color: var(--muted);
}
.al-sid {
  max-width: 200px;
  padding: 0 6px;
  border: 1px solid var(--glass-line);
  border-radius: 5px;
  background: var(--glass-1);
  color: var(--muted);
  font-size: 10.5px;
  line-height: 18px;
}
.al-model {
  height: 24px;
  max-width: 220px;
  padding: 0 6px;
  font-size: 11.5px;
}

/* ---------- 值班条 ---------- */
.al-duty {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--glass-line);
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.02), transparent);
}
.al-duty-item {
  display: flex;
  align-items: center;
  gap: 5px;
  flex: 1 1 0;
  min-width: 0;
  height: 32px;
  padding: 0 7px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-sm);
  background: var(--glass-1);
  transition: border-color 0.16s var(--ease), background 0.16s var(--ease);
}
.al-duty-item.on {
  border-color: rgba(233, 180, 76, 0.3);
  background: linear-gradient(180deg, rgba(233, 180, 76, 0.055), transparent 80%), var(--glass-1);
}
.al-duty-item.live {
  box-shadow: inset 2px 0 0 var(--accent);
}
.al-duty-title {
  flex: 0 0 auto;
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
}
.al-duty-num {
  flex: 0 0 auto;
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
  letter-spacing: -0.1px;
}
.al-duty-item.on .al-duty-num {
  color: var(--accent-2);
}
.al-duty-when {
  flex: 0 0 auto;
  height: 19px;
  padding: 0 6px;
  border: 1px solid var(--glass-line);
  border-radius: 5px;
  background: transparent;
  color: var(--muted);
  font-family: var(--mono);
  font-size: 10.5px;
  transition: border-color 0.14s var(--ease), color 0.14s var(--ease);
}
.al-duty-when:hover {
  border-color: var(--line-3);
  color: var(--text);
}
.al-duty-last {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--muted);
}
.al-duty-auto {
  flex: 0 0 auto;
  gap: 3px;
}
.al-duty .btn-sm {
  height: 22px;
  padding: 0 6px;
}

/* 1280 宽下值班条最挤：隐藏「自动」文字与输入区快捷键提示，保留控件本身 */
@media (max-width: 1360px) {
  .al-duty-auto .tiny {
    display: none;
  }
  .al-composer-kbd {
    display: none;
  }
}
.al-banner {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--glass-line);
  background: rgba(233, 180, 76, 0.06);
}
.al-edited {
  color: var(--accent);
  margin-left: 2px;
}

/* ---------- 主体两栏 ---------- */
.al-body {
  flex: 1 1 auto;
  min-height: 0;
  display: grid;
  grid-template-columns: 260px minmax(0, 1fr);
  /* 行高必须可压缩：否则编队/上下文这类高内容页会把面板撑出可视区 */
  grid-template-rows: minmax(0, 1fr);
}
.al-left {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-height: 0;
  padding: 9px 9px 12px;
  border-right: 1px solid var(--glass-line);
  background: var(--glass-1);
  overflow-y: auto;
}
/* 新建会话：左栏最上方的主操作，玻璃胶囊 */
.al-newchat {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 7px;
  flex: 0 0 auto;
  height: 32px;
  padding: 0 11px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-sm);
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.035), transparent), var(--glass-2);
  color: var(--text-2);
  font-size: 12px;
  font-weight: 500;
  transition: border-color 0.14s var(--ease), background 0.14s var(--ease), color 0.14s var(--ease);
}
.al-newchat:hover {
  border-color: rgba(233, 180, 76, 0.45);
  background: var(--accent-soft);
  color: var(--accent-2);
}
.al-newchat-plus {
  color: var(--accent);
  font-size: 13px;
  line-height: 1;
}
.al-section {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  color: var(--muted);
  font-size: 10px;
  letter-spacing: 0.8px;
}
.al-workers {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.al-worker {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 8px;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  cursor: pointer;
  transition: background 0.14s var(--ease), border-color 0.14s var(--ease);
}
.al-worker:hover {
  border-color: var(--glass-line);
  background: var(--glass-2);
}
.al-worker.on {
  border-color: rgba(233, 180, 76, 0.42);
  background: var(--accent-soft);
  box-shadow: inset 2px 0 0 var(--accent);
}
.al-worker-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.al-worker-title {
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
}
.al-worker-desc {
  color: var(--muted);
  font-size: 10.5px;
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.al-del {
  flex: 0 0 auto;
  width: 18px;
  height: 18px;
  padding: 0;
  border: 1px solid transparent;
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  font-size: 13px;
  line-height: 16px;
}
.al-del:hover {
  color: var(--err);
  border-color: rgba(255, 90, 95, 0.32);
}
.al-hint {
  padding: 6px 8px;
  border: 1px dashed var(--line-2);
  border-radius: var(--radius-sm);
  line-height: 1.6;
}
.al-sessions {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.al-session {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 5px 7px;
  border: 1px solid transparent;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--text-2);
  font-size: 11.5px;
  text-align: left;
  transition: background 0.14s var(--ease), border-color 0.14s var(--ease);
}
.al-session:hover {
  background: var(--glass-2);
  border-color: var(--glass-line);
}
.al-session.on {
  border-color: rgba(233, 180, 76, 0.35);
  background: var(--glass-2);
}
.al-session-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.al-session-meta {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.al-main {
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
}
.al-wbar {
  display: flex;
  align-items: center;
  gap: 7px;
  flex: 0 0 auto;
  min-height: 36px;
  padding: 4px 12px;
  border-bottom: 1px solid var(--glass-line);
  background: var(--glass-1);
}
.al-wbar-title {
  flex: 0 0 auto;
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
}
.al-wbar-desc {
  min-width: 0;
  max-width: 42%;
}
.al-tabs {
  flex: 0 0 auto;
}
.al-tabs-meta {
  padding-right: 10px;
  font-variant-numeric: tabular-nums;
}
.al-page {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.al-page > * {
  min-height: 0;
}
.al-report-page {
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.al-report-page > :deep(.rd) {
  flex: 1 1 auto;
  min-height: 0;
}
.al-context {
  display: flex;
  flex-direction: column;
  overflow-y: auto;
}

/* ---------- 输入区 ---------- */
.al-composer {
  flex: 0 0 auto;
  padding: 8px 12px 9px;
}
.al-composer-box {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--glass-line);
  border-radius: 18px;
  background: var(--glass-2);
  backdrop-filter: blur(24px) saturate(150%);
  -webkit-backdrop-filter: blur(24px) saturate(150%);
  box-shadow: 0 10px 26px rgba(0, 0, 0, 0.24);
  transition: border-color 0.16s var(--ease), box-shadow 0.16s var(--ease), background 0.16s var(--ease);
}
.al-composer-box:focus-within {
  border-color: rgba(233, 180, 76, 0.55);
  background: var(--glass-3);
  box-shadow: 0 0 0 1px rgba(233, 180, 76, 0.55), 0 18px 40px rgba(0, 0, 0, 0.35);
}
.al-composer-input {
  width: 100%;
  min-height: 34px;
  max-height: 144px;
  padding: 9px 13px 4px;
  border: 0;
  background: transparent;
  color: var(--text);
  font: 12.5px/21px var(--font);
  resize: none;
  outline: none;
  overflow-y: hidden;
}
.al-composer-input::placeholder {
  color: var(--muted);
}
.al-composer-ft {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 3px 7px 7px 10px;
}
.al-mode {
  flex: 0 0 auto;
  height: 20px;
  padding: 0 8px;
  border: 1px solid var(--glass-line);
  border-radius: 999px;
  background: var(--glass-1);
  color: var(--text-2);
  font-size: 10.5px;
  line-height: 18px;
}
.al-mode.steer {
  border-color: rgba(233, 180, 76, 0.42);
  background: var(--accent-soft);
  color: var(--accent-2);
}
.al-composer-model {
  max-width: 190px;
  font-size: 10.5px;
}
/* 模型选择器（输入区左下）：无边框幽灵样式，focus 才出现琥珀描边 */
.al-model-inline {
  flex: 0 1 auto;
  max-width: 190px;
  height: 20px;
  padding: 0 18px 0 4px;
  border: 1px solid transparent;
  border-radius: 6px;
  background-color: transparent;
  color: var(--muted);
  font-size: 10.5px;
  appearance: none;
  cursor: pointer;
  background-image: linear-gradient(45deg, transparent 50%, var(--muted) 50%),
    linear-gradient(135deg, var(--muted) 50%, transparent 50%);
  background-position: calc(100% - 9px) 50%, calc(100% - 5px) 50%;
  background-size: 4px 4px, 4px 4px;
  background-repeat: no-repeat;
  transition: border-color 0.14s var(--ease), color 0.14s var(--ease), background-color 0.14s var(--ease);
}
.al-model-inline:hover {
  border-color: var(--glass-line);
  color: var(--text-2);
  background-color: rgba(255, 255, 255, 0.03);
}
.al-model-inline:focus {
  outline: none;
  border-color: rgba(233, 180, 76, 0.5);
  color: var(--text);
}
.al-composer-kbd {
  flex: 0 0 auto;
  color: var(--faint);
  font-size: 10px;
}
.al-send {
  flex: 0 0 auto;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border: 1px solid rgba(233, 180, 76, 0.5);
  border-radius: 50%;
  background: linear-gradient(180deg, #f2c66d, #cf9430);
  color: #23180a;
  transition: transform 0.14s var(--ease), filter 0.14s var(--ease), opacity 0.14s var(--ease);
}
.al-send:hover:not(:disabled) {
  filter: brightness(1.08);
  transform: translateY(-1px);
}
.al-send:disabled {
  opacity: 0.34;
  cursor: not-allowed;
}
.al-composer-meta {
  padding: 5px 4px 0;
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}
.al-readonly {
  flex: 0 0 auto;
  display: flex;
  align-items: flex-start;
  gap: 7px;
  margin: 0 12px 10px;
  padding: 7px 9px;
  border: 1px dashed var(--line-2);
  border-radius: var(--radius-sm);
  line-height: 1.6;
}

/* ---------- 告警卡 ---------- */
.al-diag {
  flex: 0 0 auto;
  display: flex;
  flex-direction: column;
  gap: 7px;
  margin: 10px 14px 0;
  padding: 9px 11px;
  border: 1px solid rgba(255, 90, 95, 0.32);
  border-radius: var(--radius-sm);
  background: linear-gradient(180deg, rgba(255, 90, 95, 0.07), transparent 70%), var(--glass-1);
}
.al-diag-soft {
  border-color: rgba(233, 180, 76, 0.3);
  background: linear-gradient(180deg, rgba(233, 180, 76, 0.06), transparent 70%), var(--glass-1);
}
.al-diag-soft .al-diag-hd .dot {
  background: var(--accent);
}
.al-diag-hd {
  display: flex;
  align-items: center;
  gap: 7px;
  flex-wrap: wrap;
}
.al-diag-title {
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
  line-height: 1.45;
}
.al-diag-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.al-diag-item {
  display: flex;
  align-items: baseline;
  gap: 7px;
  color: var(--text-2);
  font-size: 11.5px;
  line-height: 1.6;
}
.al-diag-code {
  flex: 0 0 auto;
  padding: 0 5px;
  border: 1px solid var(--line-2);
  border-radius: 4px;
  background: var(--panel-2);
  color: var(--muted);
  font-size: 10.5px;
  line-height: 16px;
}
.al-diag-msg {
  flex: 1 1 auto;
  min-width: 0;
  word-break: break-word;
}
.al-diag-ft {
  display: flex;
  align-items: center;
  gap: 7px;
  flex-wrap: wrap;
  padding-top: 7px;
  border-top: 1px solid rgba(255, 90, 95, 0.18);
}
.al-diag-hint {
  line-height: 1.55;
}

/* ---------- 弹窗表单 ---------- */
.al-win-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.al-win-field {
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.al-win-row {
  display: flex;
  align-items: center;
  gap: 4px;
}
.al-time-part {
  width: 54px;
  height: 24px;
  padding: 0 4px;
  font-size: 11.5px;
}
.al-time-sep {
  color: var(--muted);
  font-size: 11px;
}
.al-freq {
  width: 110px;
  height: 24px;
  padding: 0 6px;
  font-size: 11.5px;
}
.al-sched {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.al-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.al-field {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.al-editor-area {
  font: 12px/1.7 var(--mono);
  resize: vertical;
  min-height: 240px;
}

@media (prefers-reduced-motion: reduce) {
  .al-composer-box,
  .al-send,
  .al-duty-item,
  .al-worker {
    transition: none;
  }
}
</style>
