// Agent 流式输出：订阅 kernel:event 的 agent.delta / agent.state / agent.report。
// 抽屉与 agentlab 面板共用这一份状态。
// 本轮扩展：上下文占用（agent.context）、编队树（agent.child / tree）、值班调度（agent.schedule）。

import { reactive } from 'vue'
import { onKernelEvent } from '../api'
import {
  toAgentContext,
  toSchedule,
  type AgentChildEvent,
  type AgentContext,
  type AgentNode,
  type AgentNodeStatus,
  type AgentTree,
  type PhaseSchedule,
  type ScheduleTick,
} from '../lib/api.agent'
import type { AgentDeltaPayload, AgentDiagnostic, AgentReportPayload, AgentSessionDetail, AgentStatePayload } from '../types'
import {
  childReportsFromEvents,
  reportsFromEvents,
  sessionFeed,
  stateLabel,
  toolText,
  type AgentRole,
  type ReportEntry,
  type ToolCall,
} from '../lib/agentview'

export type LogKind = 'delta' | 'state' | 'tool' | 'error' | 'ok' | 'user' | 'user-pending' | 'system'

/** 一次运行结束后的诊断事实：报告为空时由它决定告警卡的标题与正文。
    后端没给就留空，前端明确写「后端未返回诊断信息」而不是含糊地让用户重试。 */
export interface RunFacts {
  finishReason: string
  emptyReason: string
  diagnostics: AgentDiagnostic[]
  /** 本轮已用步数与 token：没有诊断信息时也要把这两个数讲清楚 */
  steps: number
  tokens: number
  model: string
  at: string
  /** 本轮是谁、跑的是什么：告警卡上的「重跑」要用它（空串表示无法重跑） */
  worker: string
  input: string
  /** report 其实是推理过程回退来的（后端 contentFromReasoning） */
  contentFromReasoning: boolean
}

/** 上下文趋势的一个采样点（每次模型调用推一条 agent.context）。 */
export interface ContextPoint {
  step: number
  used: number
  at: string
}

/** 趋势最多保留的点数：够看出爬升/压缩，又不至于把弹层撑成一堵墙。 */
const CONTEXT_HISTORY_MAX = 60

export interface LogLine {
  id: number
  time: string
  text: string
  kind: LogKind
  /** 行创建时间戳（毫秒）：心跳噪声合并时用它判断 60 秒窗口。 */
  ts?: number
  /** 相同内容被合并的次数（>1 时渲染成 ×N）。 */
  repeat?: number
  /** 该行仍在持续接收流式增量（delta 段未关闭）。 */
  streaming?: boolean
  /** 结构化角色：对话/轨迹视图按它分组；旧行没有时按 kind 推断。 */
  role?: AgentRole
  /** 工具调用（kind==='tool' 的行才有）。 */
  tool?: ToolCall
  /** 该行所属 worker（RouterAgent 或子智能体）。 */
  worker?: string
  /** 模型名（助手消息首行的元信息）。 */
  model?: string
  /** 模型调用步数。 */
  step?: number
  /** 该步的 token 增量（有才显示）。 */
  tokens?: number
}

/**
 * 跨面板的 UI 请求（不参与 Agent 运行状态）：
 * 抽屉里的小环点「查看完整洞察」时，把工作台切到「上下文」页。
 * 用自增序号而不是布尔值，避免「已经打开过一次」导致后续请求被吞掉。
 */
export const agentUi = reactive({ contextPageSeq: 0 })

/** 请求打开 Agent 工作台的「上下文」页（工作台监听序号变化）。 */
export function requestContextPage(): void {
  agentUi.contextPageSeq += 1
}

export const agentState = reactive({
  running: false,
  sessionId: '',
  worker: '',
  stateText: '',
  log: [] as LogLine[],
  report: '',
  reportWorker: '',
  /** 本会话的历史报告：新一轮运行不清空，切换会话时整份替换。
      只装**根会话的交付物**（子智能体报告不进这里，否则报告册会混进过程产出）。 */
  reports: [] as ReportEntry[],
  /** 子智能体的过程报告（不进报告册）：只在「编队」节点详情与「轨迹」里当证据看。 */
  childReports: [] as ReportEntry[],
  /** 当前高亮的报告（默认最新一份）；空串表示没有报告。 */
  activeReportId: '',
  updatedAt: '',
  /** 当前会话上下文占用；null 表示还没拿到（面板据此隐藏上下文环）。 */
  context: null as AgentContext | null,
  /** 本会话的上下文用量序列（后端只给当前快照，趋势由前端按事件累积）。 */
  contextHistory: [] as ContextPoint[],
  /** 最近一次运行的诊断事实；null 表示还没有运行结束过。 */
  lastRun: null as RunFacts | null,
  /** 编队：主会话 sessionId + 节点（含主会话自身）。 */
  rootId: '',
  nodes: [] as AgentNode[],
  /** tree 方法给的汇总（权威）：总节点数、运行中数量、快照时间。 */
  formationMeta: { total: 0, activeCount: 0, updatedAt: '' } as {
    total: number
    activeCount: number
    updatedAt: string
  },
  /** 值班调度三时段；空数组表示后端还没提供（面板降级隐藏值班卡）。 */
  schedule: [] as PhaseSchedule[],
  /** 调度器最近一次推送的说明文案（例如「未配置模型」）。 */
  scheduleReason: '',
  /** 调度器正在跑的时段（phase），空串表示没有自动任务在跑。 */
  autoRunning: '' as '' | 'premarket' | 'intraday' | 'postmarket',
  /** 本轮运行开始时刻（毫秒）；0 表示没有在跑。对话页用它显示「思考中 12s」。 */
  runStartedAt: 0,
  /** 最近一轮运行耗时（毫秒）：结束时由 endRun 写入，对话页显示「已完成 · 18s」。 */
  lastRunMs: 0,
})

let seq = 0
const MAX_LINES = 600
/** 心跳噪声合并窗口：同一条 thinking 在这段时间内重复出现只更新时间。 */
const HEARTBEAT_MERGE_MS = 60_000
/** 当前仍在增长的那一段流式输出（0 表示没有活动段落）。 */
let activeDeltaId = 0

/** 把时间格式化成 HH:MM:SS（日志行显示用）。 */
function clockText(d: Date): string {
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}

/** 裁剪超长日志，避免长时间运行把内存与渲染压垮。 */
function trimLog(): void {
  if (agentState.log.length <= MAX_LINES) return
  agentState.log.splice(0, agentState.log.length - MAX_LINES)
  if (activeDeltaId > 0 && !agentState.log.some((l) => l.id === activeDeltaId)) activeDeltaId = 0
}

/** 关闭当前流式段落：下一次 delta 会新建一行（工具/状态/用户消息前都要关闭）。 */
export function closeDelta(): void {
  if (activeDeltaId === 0) return
  const line = agentState.log.find((l) => l.id === activeDeltaId)
  if (line) line.streaming = false
  activeDeltaId = 0
}

/**
 * 追加一行日志，返回行 id（0 表示空文本未追加）；非流式行会顺便关闭当前 delta 段。
 * 第三个参数用于补结构化字段（role / tool / worker / step / 历史时间），旧的调用方式不受影响。
 */
export function appendLine(text: string, kind: LogKind = 'delta', patch: Partial<LogLine> = {}): number {
  closeDelta()
  const body = text.replace(/\n+$/, '')
  if (body === '') return 0
  seq += 1
  const id = seq
  const d = new Date()
  const line: LogLine = { id, time: clockText(d), text: body, kind, ts: d.getTime(), ...patch }
  // id / kind / text 以调用参数为准，patch 只补业务字段
  line.id = id
  line.kind = kind
  line.text = body
  if (patch.time === undefined || patch.time === '') line.time = clockText(d)
  agentState.log.push(line)
  trimLog()
  agentState.updatedAt = d.toISOString()
  return id
}

/**
 * 追加一段流式增量：同一轮模型的连续 delta 合并进同一行（保留换行），
 * 只有段落被关闭后才新起一行，避免每个碎 token 各占一行。
 */
export function appendDelta(text: string): number {
  if (text === '') return 0
  if (activeDeltaId > 0) {
    const last = agentState.log[agentState.log.length - 1]
    if (last && last.id === activeDeltaId && last.kind === 'delta') {
      last.text += text
      agentState.updatedAt = new Date().toISOString()
      return last.id
    }
    activeDeltaId = 0
  }
  if (text.trim() === '') return 0
  seq += 1
  const id = seq
  const d = new Date()
  agentState.log.push({
    id,
    time: clockText(d),
    text,
    kind: 'delta',
    ts: d.getTime(),
    streaming: true,
    role: 'assistant',
    worker: agentState.worker,
    model: agentState.context?.model ?? '',
    step: agentState.context?.steps ?? 0,
  })
  trimLog()
  activeDeltaId = id
  agentState.updatedAt = d.toISOString()
  return id
}

/**
 * 追加状态行；thinking 心跳 detail 与该行完全相同且 60 秒内重复时只更新时间与次数，
 * 不再刷出一堆「盯盘心跳：任务仍在执行」。
 */
function appendStateLine(text: string, kind: LogKind, detail: string, thinking: boolean): void {
  closeDelta()
  const now = Date.now()
  const last = agentState.log[agentState.log.length - 1]
  if (
    thinking &&
    detail !== '' &&
    last &&
    last.kind === 'state' &&
    last.text === text &&
    last.ts !== undefined &&
    now - last.ts < HEARTBEAT_MERGE_MS
  ) {
    last.time = clockText(new Date(now))
    last.ts = now
    last.repeat = (last.repeat ?? 1) + 1
    agentState.updatedAt = new Date(now).toISOString()
    return
  }
  appendLine(text, kind, { role: 'status', worker: agentState.worker })
}

// ---------- 工具调用事件 ----------

/** agent.tool 事件的归一载荷（后端契约；字段缺失时由这里兜底）。 */
export interface ToolEventInput {
  sessionId?: string
  worker?: string
  id?: string
  phase?: 'start' | 'end'
  tool?: string
  meta?: string
  args?: unknown
  result?: unknown
  ok?: boolean
  error?: string
  ms?: number
  step?: number
  at?: string
}

/** 参数/结果可能是对象：字符串原样保留，对象转成可读 JSON（inspector 里用 <pre> 展示）。 */
function textOf(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

/** 事件时间：ISO 转 HH:MM:SS；已经是 HH:MM:SS 就原样用；其它情况留空由 appendLine 取当前时间。 */
function eventClock(raw: string): string {
  if (!raw) return ''
  if (/^\d{1,2}:\d{2}(:\d{2})?$/.test(raw)) return raw
  const t = Date.parse(raw)
  return Number.isNaN(t) ? '' : clockText(new Date(t))
}

/**
 * agent.tool 事件 → 工具行。
 * start 建一条「运行中」的条目，end 按 id 合并 ok/ms/result/error；
 * start/end 乱序或缺失时也不丢行：end 找不到 start 就补一行（结果照常显示），
 * start 迟于 end 到达时只补空字段，不回退已经落定的结果。
 */
export function applyToolEvent(ev: ToolEventInput): void {
  const phase: 'start' | 'end' = ev.phase === 'start' ? 'start' : 'end'
  const id = (ev.id ?? '').trim() !== '' ? (ev.id as string) : 'tool-' + (seq + 1)
  const sessionId = ev.sessionId ?? agentState.sessionId
  const worker = ev.worker || agentState.worker
  const incoming: ToolCall = {
    id,
    sessionId,
    worker,
    name: (ev.tool ?? '').trim() || (ev.meta ?? '').trim() || 'tool',
    meta: (ev.meta ?? '').trim(),
    args: textOf(ev.args),
    result: textOf(ev.result),
    ok: ev.ok !== false,
    error: (ev.error ?? '').trim(),
    ms: typeof ev.ms === 'number' && Number.isFinite(ev.ms) ? ev.ms : 0,
    step: typeof ev.step === 'number' && Number.isFinite(ev.step) ? ev.step : 0,
    at: ev.at ?? '',
    phase,
  }

  const found = agentState.log.find((l) => l.tool !== undefined && l.tool.id === id)
  if (found && found.tool) {
    const cur = found.tool
    if (incoming.phase === 'start' && cur.phase === 'end') {
      // 迟到/重放的 start：只补缺，不把已完成的结果打回运行中
      if (cur.name === '') cur.name = incoming.name
      if (cur.meta === '') cur.meta = incoming.meta
      if (cur.args === '') cur.args = incoming.args
      found.tool = { ...cur }
      return
    }
    const next: ToolCall = {
      ...cur,
      name: incoming.name !== '' ? incoming.name : cur.name,
      meta: incoming.meta !== '' ? incoming.meta : cur.meta,
      args: incoming.args !== '' ? incoming.args : cur.args,
      result: incoming.result !== '' ? incoming.result : cur.result,
      error: incoming.error !== '' ? incoming.error : cur.error,
      ok: incoming.phase === 'end' ? incoming.ok : cur.ok,
      ms: incoming.ms > 0 ? incoming.ms : cur.ms,
      step: incoming.step > 0 ? incoming.step : cur.step,
      worker: incoming.worker !== '' ? incoming.worker : cur.worker,
      phase: incoming.phase,
    }
    found.tool = next
    found.text = toolText(next)
    found.role = 'tool'
    found.streaming = next.phase === 'start'
    found.ts = Date.now()
    if (next.step > 0) found.step = next.step
    return
  }

  const lineId = appendLine(toolText(incoming), 'tool', {
    role: 'tool',
    tool: incoming,
    worker,
    step: incoming.step > 0 ? incoming.step : agentState.context?.steps ?? 0,
    time: eventClock(incoming.at),
  })
  const line = agentState.log.find((l) => l.id === lineId)
  if (line) line.streaming = incoming.phase === 'start'
}

// ---------- 报告 ----------

/** 记录一份报告（agent.report 事件 / run 返回值 / 历史会话共用）。 */
export interface ReportInput {
  sessionId?: string
  worker?: string
  content: string
  steps?: number
  tokens?: number
  model?: string
  fromReasoning?: boolean
}

/**
 * 追加一份报告（交付物）：同一会话 + 同一内容去重（重连/重放不会出现重复卡片）。
 * **只给根会话用**：子智能体的报告走 recordChildReport，不混进报告册。
 * 同时把 agentState.report 指向最新一份——AgentDrawer / quant.vue 仍读这个字段。
 */
export function recordReport(input: ReportInput): ReportEntry | null {
  const content = input.content ?? ''
  if (content.trim() === '') return null
  const sessionId = input.sessionId ?? agentState.sessionId
  const worker = input.worker || agentState.reportWorker || agentState.worker
  const model = input.model || agentState.context?.model || ''
  const existing = agentState.reports.find((r) => r.sessionId === sessionId && r.content === content)
  const entry: ReportEntry = existing ?? {
    id: 'r-' + (sessionId || 'none') + '-' + Date.now().toString(36),
    sessionId,
    worker,
    at: new Date().toISOString(),
    content,
    steps: 0,
    tokens: 0,
    model: '',
    fromReasoning: false,
  }
  if (input.steps !== undefined && input.steps > 0) entry.steps = input.steps
  if (input.tokens !== undefined && input.tokens > 0) entry.tokens = input.tokens
  if (model !== '') entry.model = model
  if (input.fromReasoning === true) entry.fromReasoning = true
  if (!existing) agentState.reports.push(entry)
  agentState.activeReportId = entry.id
  agentState.report = content
  agentState.reportWorker = worker
  return entry
}

/**
 * 记录一份**子智能体**的过程报告：不进报告册（reports），只进 childReports。
 * 信息不丢失：编队节点详情与轨迹页从 childReports 里取；报告册只展示根会话交付物。
 * 去重规则同 recordReport（同一子会话 + 同一内容只留一份）。
 */
export function recordChildReport(input: ReportInput): ReportEntry | null {
  const content = input.content ?? ''
  if (content.trim() === '') return null
  const sessionId = input.sessionId ?? ''
  // 没有 sessionId 就无法判断归属：宁可不收，也不往报告册里塞（报告册只装交付物）。
  if (sessionId === '') return null
  const existing = agentState.childReports.find((r) => r.sessionId === sessionId && r.content === content)
  const entry: ReportEntry = existing ?? {
    id: 'cr-' + sessionId + '-' + Date.now().toString(36),
    sessionId,
    worker: input.worker || '',
    at: new Date().toISOString(),
    content,
    steps: 0,
    tokens: 0,
    model: input.model || agentState.context?.model || '',
    fromReasoning: false,
  }
  if (input.steps !== undefined && input.steps > 0) entry.steps = input.steps
  if (input.tokens !== undefined && input.tokens > 0) entry.tokens = input.tokens
  if (input.fromReasoning === true) entry.fromReasoning = true
  if (!existing) agentState.childReports.push(entry)
  return entry
}

/** 清空报告（「清空」按钮用；beginRun 不会调用它）。 */
export function clearReports(): void {
  agentState.reports = []
  agentState.childReports = []
  agentState.activeReportId = ''
}

/**
 * 打开历史会话：整份替换日志、报告与上下文（不把上一会话的内容混进来）。
 * 报告只取**根会话交付物**（reportsFromEvents），子智能体报告单独放进 childReports，
 * 避免历史会话一打开就看到子报告混在报告册里。
 */
export function loadSession(detail: AgentSessionDetail): void {
  activeDeltaId = 0
  pendingSteers.length = 0
  const session = detail.session
  agentState.log = []
  agentState.sessionId = session.id
  agentState.worker = session.worker
  agentState.reportWorker = session.worker
  agentState.running = session.status === 'running'
  agentState.stateText = stateLabel(session.status)
  // 历史会话不显示"本轮已用时间"：不知道它在什么时候跑的，runStartedAt 归零。
  agentState.runStartedAt = 0
  agentState.lastRunMs = 0
  const reports = reportsFromEvents(detail.events, session)
  agentState.reports = reports
  // 子智能体的过程报告：只给编队 / 轨迹看，不进报告册
  agentState.childReports = childReportsFromEvents(detail.events, session)
  const latest = reports[reports.length - 1]
  agentState.activeReportId = latest?.id ?? ''
  agentState.report = latest?.content ?? ''
  for (const spec of sessionFeed(detail.events, session)) {
    appendLine(spec.text, spec.kind, {
      role: spec.role,
      tool: spec.tool,
      worker: spec.worker,
      step: spec.step,
      time: spec.time || undefined,
    })
  }
  // 换会话：趋势、诊断、编队汇总都属于上一轮，一起清掉（编队由面板重新读 tree）
  resetContextHistory()
  clearRunFacts()
  agentState.nodes = []
  agentState.rootId = session.id
  agentState.formationMeta = { total: 0, activeCount: 0, updatedAt: '' }
}

// ---------- 上下文占用 ----------

/** 写入上下文快照；会话不一致时丢弃（避免旧事件覆盖新会话）。 */
export function setContext(c: AgentContext | null): void {
  if (!c) return
  if (c.sessionId !== '' && agentState.sessionId !== '' && c.sessionId !== agentState.sessionId) return
  // 会话换了：上一条曲线作废（趋势只对同一会话有意义）
  const prev = agentState.context
  if (prev && prev.sessionId !== '' && c.sessionId !== '' && prev.sessionId !== c.sessionId) resetContextHistory()
  agentState.context = c
  pushContextHistory(c)
}

/**
 * 累积趋势点：后端每次模型调用前推一条 agent.context，
 * 这里按「步数变化」去重（同一 step 的重复快照只保留最新），最多留 60 个点。
 * 会话切换时由 setContext / clearAgentFeed / beginRun 清空，避免上一轮的曲线混进新会话。
 */
export function pushContextHistory(c: AgentContext | null = agentState.context): void {
  if (!c) return
  const used = Math.max(0, c.usedTokens)
  const step = Math.max(0, c.steps)
  const at = c.updatedAt || new Date().toISOString()
  const list = agentState.contextHistory
  const last = list[list.length - 1]
  if (last) {
    // 同一步的快照：用量可能因为 re-count 微调，用最新值覆盖而不是新增点
    if (last.step === step) {
      if (last.used !== used) {
        last.used = used
        last.at = at
      }
      return
    }
    // 步数回退（换了 run 但 session 没变）：重新起一条曲线
    if (step < last.step) list.length = 0
  }
  list.push({ step, used, at })
  if (list.length > CONTEXT_HISTORY_MAX) list.splice(0, list.length - CONTEXT_HISTORY_MAX)
}

/** 会话切换：上一条曲线作废（点序列只对当前会话有意义）。 */
function resetContextHistory(): void {
  agentState.contextHistory.length = 0
}

// ---------- 运行诊断 ----------

/** 记录本轮结束事实：报告为空时告警卡据此写出真实原因。 */
export function setRunFacts(f: RunFacts): void {
  agentState.lastRun = f
}

/** 清空诊断事实（重新开始一轮时调用，避免上一轮的告警卡跟着走）。 */
export function clearRunFacts(): void {
  agentState.lastRun = null
}

/** 更新/补齐本轮诊断事实（run 返回后再补 steps/tokens 等，不覆盖已拿到的诊断）。 */
export function patchRunFacts(patch: Partial<RunFacts>): void {
  const base = agentState.lastRun
  if (!base) return
  agentState.lastRun = { ...base, ...patch }
}

// ---------- 事件流（从日志派生） ----------

export interface FeedEvent {
  id: number
  time: string
  text: string
  kind: LogKind
}

/** 事件流只收这几类：用户消息 / 工具调用 / 步骤与状态 / 结果与失败；模型正文（delta）不算「发生过什么」。 */
const FEED_KINDS: LogKind[] = ['user', 'user-pending', 'tool', 'state', 'error', 'ok']

/**
 * 从日志派生本轮事件流（上下文弹层的第 4 段）。
 * 只保留有信息量的行：流式增量是模型正文，不属于「发生过什么」，因此剔除。
 */
export function feedEvents(limit = 40): FeedEvent[] {
  const out: FeedEvent[] = []
  for (const l of agentState.log) {
    if (!FEED_KINDS.includes(l.kind)) continue
    out.push({ id: l.id, time: l.time, text: l.text, kind: l.kind })
  }
  return limit > 0 && out.length > limit ? out.slice(out.length - limit) : out
}

// ---------- 编队 ----------

function statusOf(raw: string): AgentNodeStatus {
  return raw === 'ok' || raw === 'failed' ? raw : 'running'
}

/** 根节点判定：parentId 为空的才是根（tree 方法给的就是这个口径，不再靠 mode==='main' 猜）。 */
export function rootNodeOf(nodes: AgentNode[]): AgentNode | null {
  return nodes.find((n) => (n.parentId ?? '') === '') ?? nodes[0] ?? null
}

/** 造一个占位节点（拿不到 label/状态时用，避免谱系因为缺一环而断开）。 */
function placeholderNode(sessionId: string, parentId: string, label: string): AgentNode {
  return {
    sessionId,
    parentId,
    depth: parentId === '' ? 0 : 1,
    childCount: 0,
    label,
    role: label,
    mode: parentId === '' ? 'main' : 'subagent',
    worker: parentId === '' ? agentState.worker : '',
    status: 'running',
    tokens: 0,
    steps: 0,
    startedAt: new Date().toISOString(),
    elapsedMs: 0,
  }
}

/** 主会话节点：sessionId 出现时补一个根，切换会话则清空上一轮编队。 */
export function ensureRootNode(sessionId: string): void {
  if (sessionId === '') return
  const has = agentState.nodes.some((n) => n.sessionId === sessionId)
  if (agentState.rootId === sessionId && has) return
  // 换会话：上一轮的谱系整份作废（节点里没有这个会话才清，避免事件乱序时误删）
  if (agentState.nodes.length > 0 && !has) agentState.nodes = []
  agentState.rootId = sessionId
  if (has) return
  agentState.nodes.unshift(placeholderNode(sessionId, '', '主会话'))
}

/**
 * agent.child 事件 → 节点 upsert。
 * 事件只做「实时状态刷新」：已有节点只更新状态/用量，绝不因为事件重复而多出一个节点；
 * 树方法回读（setFormation）是权威快照，会整份替换。
 *
 * 顺序保护：后端每条事件都带 atMs，只有比该节点已记录的 atMs 更新才应用，
 * 这样事件重放（打开历史会话、重连）不会把已完成的节点打回 running。
 */
export function applyChildEvent(ev: AgentChildEvent): void {
  if (ev.childSessionId === '') return
  if (agentState.rootId === '') agentState.rootId = ev.parentSessionId
  if (ev.parentSessionId !== '') {
    const hasParent = agentState.nodes.some((n) => n.sessionId === ev.parentSessionId)
    // 父节点未知：编队为空时它就是根（parentId 留空），否则让它当孤儿挂一层
    if (!hasParent && agentState.nodes.length === 0) {
      agentState.nodes.unshift(placeholderNode(ev.parentSessionId, '', '主会话'))
    }
  }
  const now = new Date().toISOString()
  const status = statusOf(ev.status)
  const atMs = typeof ev.atMs === 'number' && Number.isFinite(ev.atMs) ? ev.atMs : 0
  const existing = agentState.nodes.find((n) => n.sessionId === ev.childSessionId)
  if (existing) {
    // 旧事件（重放/乱序）：直接丢弃，不覆盖新状态
    if (atMs > 0 && typeof existing.atMs === 'number' && atMs < existing.atMs) return
    existing.status = status
    if (atMs > 0) existing.atMs = atMs
    if (typeof ev.tokens === 'number') existing.tokens = ev.tokens
    if (typeof ev.elapsedMs === 'number') existing.elapsedMs = ev.elapsedMs
    if (ev.label !== '') {
      existing.label = ev.label
      existing.role = ev.label
    }
    if (ev.worker) existing.worker = ev.worker
    if (ev.startedAt) existing.startedAt = ev.startedAt
    if (status === 'running') {
      existing.endedAt = undefined
    } else {
      existing.endedAt = ev.endedAt || existing.endedAt || now
    }
    return
  }
  const label = ev.label !== '' ? ev.label : '子智能体'
  const isRootChild = ev.parentSessionId === ''
  agentState.nodes.push({
    sessionId: ev.childSessionId,
    parentId: ev.parentSessionId,
    depth: isRootChild ? 0 : 1,
    childCount: 0,
    label,
    role: label,
    mode: isRootChild ? 'main' : 'subagent',
    worker: ev.worker ?? '',
    status,
    tokens: ev.tokens ?? 0,
    steps: 0,
    startedAt: ev.startedAt || now,
    endedAt: status === 'running' ? undefined : ev.endedAt || now,
    elapsedMs: ev.elapsedMs ?? 0,
    atMs: atMs > 0 ? atMs : undefined,
  })
}

/**
 * tree 方法回读结果写入编队状态（权威快照）。
 * 按 sessionId 去重：root 与 nodes 里重复时保留 nodes 里的那份（字段更全）。
 */
export function setFormation(tree: AgentTree): void {
  const byId = new Map<string, AgentNode>()
  for (const n of tree.nodes) {
    if (n.sessionId === '') continue
    // 后端已按 (depth, startedAt, sessionId) 稳定排序：Map 的插入顺序就是渲染顺序
    byId.set(n.sessionId, n)
  }
  if (tree.root && tree.root.sessionId !== '' && !byId.has(tree.root.sessionId)) {
    byId.set(tree.root.sessionId, tree.root)
  }
  const nodes = Array.from(byId.values())
  const ids = new Set(nodes.map((n) => n.sessionId))
  // 父节点不在这一份里的孤儿：parentId 清空成根，depth 一并归零，
  // 否则会按后端给的旧 depth 缩进，谱系里凭空多出一层空格。
  for (const n of nodes) {
    if ((n.parentId ?? '') !== '' && !ids.has(n.parentId ?? '')) {
      n.parentId = ''
      n.depth = 0
    }
  }
  agentState.nodes = nodes
  agentState.rootId = tree.rootSessionId || rootNodeOf(nodes)?.sessionId || ''
  // 汇总以后端为准（事件增量算出来的可能落后一步）
  agentState.formationMeta = {
    total: tree.total > 0 ? tree.total : nodes.length,
    activeCount: tree.activeCount,
    updatedAt: tree.updatedAt,
  }
}

/** 选中节点写回（详情面板用同一份状态，切换标签不丢选中）。 */
export function setFormationFocus(sessionId: string): void {
  agentState.rootId = agentState.rootId === '' ? sessionId : agentState.rootId
}

/** 当前编队的汇总（谱系面板头部用）：节点总数与仍在运行的数量。 */
export function formationStats(): { total: number; active: number; childCount: number } {
  const total = agentState.nodes.length
  const active = agentState.nodes.filter((n) => n.status === 'running').length
  return { total, active, childCount: Math.max(0, total - 1) }
}

// ---------- 值班调度 ----------

export function setSchedule(list: PhaseSchedule[]): void {
  if (list.length === 0) return
  agentState.schedule = list
}

/** agent.schedule 事件：更新某时段的 nextRunAt 与运行标记。 */
export function applyScheduleTick(tick: ScheduleTick): void {
  agentState.scheduleReason = tick.reason ?? ''
  agentState.autoRunning = tick.running ? tick.phase : ''
  const found = agentState.schedule.find((p) => p.phase === tick.phase)
  if (found && typeof tick.nextRunAt === 'string' && tick.nextRunAt !== '') found.nextRunAt = tick.nextRunAt
}

// ---------- 运行中插话（steering） ----------

interface PendingSteer {
  id: number
  text: string
}

/** 本地已发出、但后端 agent.steer 事件还没回来的插话。 */
const pendingSteers: PendingSteer[] = []

/** 发出插话后的本地反馈行；收到 agent.steer 事件后会被替换成权威的用户消息。 */
export function markSteerPending(text: string): void {
  closeDelta()
  const body = text.trim()
  if (body === '') return
  const id = appendLine('（插话已并入下一步）' + body, 'user-pending', { role: 'user', worker: agentState.worker })
  if (id > 0) pendingSteers.push({ id, text: body })
}

/**
 * 以 agent.steer 事件为准落一条用户消息：
 * 先移除对应的本地待确认行，再追加权威行（事件里的文本优先）。
 */
export function settleSteer(text: string): void {
  closeDelta()
  const body = text.trim()
  let pending: PendingSteer | undefined
  if (body !== '') pending = pendingSteers.find((p) => p.text === body)
  if (!pending) pending = pendingSteers[0]
  let content = body
  if (pending) {
    const idx = pendingSteers.findIndex((p) => p.id === pending?.id)
    if (idx >= 0) pendingSteers.splice(idx, 1)
    const lineIdx = agentState.log.findIndex((l) => l.id === pending?.id)
    if (lineIdx >= 0) agentState.log.splice(lineIdx, 1)
    if (content === '') content = pending.text
  }
  if (content === '') return
  appendLine(content, 'user', { role: 'user', worker: agentState.worker })
}

export function clearAgentFeed(): void {
  activeDeltaId = 0
  pendingSteers.length = 0
  agentState.log = []
  agentState.report = ''
  agentState.reportWorker = ''
  clearReports()
  agentState.stateText = ''
  // 换了会话：趋势、诊断、编队汇总都属于上一轮，一起清掉
  resetContextHistory()
  clearRunFacts()
  agentState.formationMeta = { total: 0, activeCount: 0, updatedAt: '' }
}

/**
 * 开始一轮运行。
 * freshChat=true 表示这是一次新会话：清空转写/插话队列/流式段落，但**保留历史报告**（报告册不覆盖）。
 */
/**
 * 新建会话：清空转写 / 报告册 / 上下文 / 编队 / 诊断，并把会话指针归零。
 * 与 clearAgentFeed 的差别是连 sessionId 一起清掉——下一次运行会开一段全新会话。
 */
export function resetConversation(): void {
  clearAgentFeed()
  agentState.sessionId = ''
  agentState.worker = ''
  agentState.running = false
  agentState.runStartedAt = 0
  agentState.lastRunMs = 0
  agentState.nodes = []
  agentState.rootId = ''
  agentState.formationMeta = { total: 0, activeCount: 0, updatedAt: '' }
}

export function beginRun(worker: string, sessionId = '', freshChat = false): void {
  if (freshChat) {
    activeDeltaId = 0
    pendingSteers.length = 0
    agentState.log = []
  }
  agentState.running = true
  agentState.worker = worker
  agentState.sessionId = sessionId
  // 报告不再因为「新一轮运行」被清空：历史报告保留，agentState.report 仍指向最新一份。
  agentState.reportWorker = worker
  agentState.stateText = '准备中'
  // 新一轮就是新的编排：上一轮的子智能体过程报告全部作废（报告册不受影响，保留历史交付物）。
  agentState.childReports = []
  agentState.runStartedAt = Date.now()
  agentState.lastRunMs = 0
  // 新一轮（新会话）：上下文、趋势、诊断与编队都是新的一份
  agentState.context = null
  resetContextHistory()
  clearRunFacts()
  agentState.nodes = []
  agentState.rootId = ''
  agentState.formationMeta = { total: 0, activeCount: 0, updatedAt: '' }
}

export function endRun(failed = false, message = '', stopped = false): void {
  agentState.running = false
  agentState.stateText = stopped ? '已停止' : failed ? '失败' : '已完成'
  if (agentState.runStartedAt > 0) {
    agentState.lastRunMs = Date.now() - agentState.runStartedAt
    agentState.runStartedAt = 0
  }
  const now = new Date().toISOString()
  // 本轮已经结束：还挂在 running 的节点一律收尾，避免树上留一个永远转圈的子智能体。
  // （tree 方法回读会覆盖成权威状态，这里只是兜底。）
  for (const n of agentState.nodes) {
    if (n.status === 'running') {
      n.status = failed ? 'failed' : 'ok'
      if (!n.endedAt) n.endedAt = now
    }
  }
  const root = agentState.nodes.find((n) => n.sessionId === agentState.rootId)
  if (root && !root.endedAt) root.endedAt = now
  if (message) appendLine(message, failed ? 'error' : 'ok')
}

function parsePayload(raw: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(raw)
    if (typeof v !== 'object' || v === null || Array.isArray(v)) return null
    return v as Record<string, unknown>
  } catch {
    return null
  }
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

/** 后端状态码 → 中文文案（对话页状态 chip 与顶部状态共用一套口径，映射在 lib/agentview）。 */
function stateTextOf(st: string): string {
  return stateLabel(st)
}

/** 毫秒 → 1.3s / 12s / 2m10s（进度行用，方案与 lib/agentview 保持一致）。 */
function formatElapsedMs(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0s'
  if (ms < 1000) return Math.round(ms) + 'ms'
  const sec = ms / 1000
  if (sec < 10) return sec.toFixed(1) + 's'
  if (sec < 60) return Math.round(sec) + 's'
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return m + 'm' + (s > 0 ? s + 's' : '')
}

function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

/** 诊断数组归一：后端可能给字符串数组或 {code,message} 对象数组。 */
function toDiagnostics(v: unknown): AgentDiagnostic[] {
  if (!Array.isArray(v)) return []
  const out: AgentDiagnostic[] = []
  for (const item of v) {
    if (typeof item === 'string') {
      if (item.trim() !== '') out.push({ code: '', message: item })
      continue
    }
    if (typeof item !== 'object' || item === null) continue
    const r = item as Record<string, unknown>
    const message = str(r.message) || str(r.detail) || str(r.reason)
    if (message === '') continue
    out.push({ code: str(r.code), message })
  }
  return out
}

/** 订阅内核事件；返回取消订阅函数。 */
/**
 * 事件是否属于「当前正在看的会话」。
 * 双工模式下后台子智能体会同时推 delta/state/report，它们不能改父会话的转写与状态，
 * 否则前台会被子智能体的输出串台（历史 bug：子智能体完成后父会话被提前标记为结束）。
 * 当前会话还没确定（空串）时一律接受，保证第一帧能把会话 id 定下来。
 */
function forCurrentSession(sid: string): boolean {
  if (sid === '') return true
  if (agentState.sessionId === '') return true
  return sid === agentState.sessionId
}

export function subscribeAgent(): () => void {
  return onKernelEvent((e) => {
    const data = parsePayload(e.payload)
    if (!data) return
    if (e.topic === 'agent.delta') {
      const d = data as AgentDeltaPayload
      if (!forCurrentSession(str(d.sessionId))) return
      agentState.running = true
      if (typeof d.sessionId === 'string' && d.sessionId !== '') {
        agentState.sessionId = d.sessionId
        ensureRootNode(d.sessionId)
      }
      if (typeof d.text === 'string') appendDelta(d.text)
      return
    }
    if (e.topic === 'agent.state') {
      const s = data as AgentStatePayload
      if (!forCurrentSession(str(s.sessionId))) return
      const st = typeof s.state === 'string' ? s.state : ''
      if (typeof s.sessionId === 'string' && s.sessionId !== '') {
        agentState.sessionId = s.sessionId
        ensureRootNode(s.sessionId)
      }
      agentState.stateText = stateTextOf(st)
      if (st === 'thinking' || st === 'tool' || st === 'waiting') agentState.running = true
      if (st === 'done' || st === 'error' || st === 'stopped') agentState.running = false
      const detail = typeof s.detail === 'string' && s.detail !== '' ? s.detail : ''
      const text = '[' + agentState.stateText + ']' + (detail ? ' ' + detail : '')
      appendStateLine(text, st === 'tool' ? 'tool' : st === 'error' ? 'error' : 'state', detail, st === 'thinking')
      return
    }
    if (e.topic === 'agent.steer') {
      if (typeof data.sessionId === 'string' && data.sessionId !== '') agentState.sessionId = data.sessionId
      const text = typeof data.text === 'string' ? data.text : typeof data.content === 'string' ? data.content : ''
      settleSteer(text)
      return
    }
    if (e.topic === 'agent.report') {
      const r = data as AgentReportPayload
      const content = typeof r.report === 'string' ? r.report : ''
      const worker = typeof r.worker === 'string' && r.worker !== '' ? r.worker : agentState.reportWorker || agentState.worker
      // 子智能体的报告只当过程证据（childReports），绝不进报告册：
      // 报告 = 交付物，只给最终结果；子产出在「编队」节点详情与「轨迹」里看。
      // 同时它也不能改父会话的「正在运行 / 当前状态」：否则子任务先结束会把前台判定为已完成。
      // 判定双重保险：会话 id 不是当前会话，或它已经是编队里的子节点，都算子报告。
      const sid = str(r.sessionId)
      const knownChild =
        sid !== '' && agentState.nodes.some((n) => n.sessionId === sid && (n.parentId ?? '') !== '')
      if (!forCurrentSession(sid) || knownChild) {
        recordChildReport({
          sessionId: str(r.sessionId),
          worker,
          content,
          steps: 0,
          tokens: r.usage?.totalTokens ?? 0,
          model: agentState.context?.model ?? '',
          fromReasoning: r.contentFromReasoning === true,
        })
        return
      }
      if (content.trim() !== '') {
        recordReport({
          sessionId: typeof r.sessionId === 'string' ? r.sessionId : agentState.sessionId,
          worker,
          content,
          steps: agentState.context?.steps ?? 0,
          tokens: r.usage?.totalTokens ?? 0,
          model: agentState.context?.model ?? '',
          fromReasoning: r.contentFromReasoning === true,
        })
      } else {
        agentState.reportWorker = worker
      }
      agentState.running = false
      agentState.stateText = '已完成'
      // 报告事件里可能带诊断：先记下来，报告为空时告警卡才有话可说
      const diagnostics = toDiagnostics(r.diagnostics)
      if (diagnostics.length > 0 || str(r.emptyReason) !== '' || str(r.finishReason) !== '') {
        setRunFacts({
          finishReason: str(r.finishReason),
          emptyReason: str(r.emptyReason),
          diagnostics,
          steps: agentState.context?.steps ?? 0,
          tokens: r.usage?.totalTokens ?? agentState.context?.usedTokens ?? 0,
          model: agentState.context?.model ?? '',
          at: new Date().toISOString(),
          // 事件里没有原始任务文本：重跑按钮据此禁用，只保留「换模型」这类本地动作
          worker: agentState.worker,
          input: '',
          contentFromReasoning: r.contentFromReasoning === true,
        })
      }
      appendLine(agentState.report.trim() === '' ? '本轮没有正文产出' : '报告已生成', agentState.report.trim() === '' ? 'error' : 'ok', {
        role: 'status',
        worker,
      })
      return
    }
    if (e.topic === 'agent.tool') {
      if (!forCurrentSession(str(data.sessionId))) return
      closeDelta()
      applyToolEvent({
        sessionId: str(data.sessionId),
        worker: str(data.worker),
        id: str(data.id),
        phase: data.phase === 'start' ? 'start' : 'end',
        tool: str(data.tool),
        meta: str(data.meta),
        args: data.args,
        result: data.result,
        ok: data.ok !== false,
        error: str(data.error),
        ms: num(data.ms),
        step: num(data.step),
        at: str(data.at),
      })
      return
    }
    if (e.topic === 'agent.context') {
      // 每次模型调用前都会推一条上下文快照：它是天然的轮次边界，顺手关闭上一段 delta。
      closeDelta()
      setContext(toAgentContext(data))
      return
    }
    if (e.topic === 'agent.child') {
      applyChildEvent({
        parentSessionId: str(data.parentSessionId),
        childSessionId: str(data.childSessionId),
        label: str(data.label),
        status: str(data.status) === 'ok' || str(data.status) === 'failed' ? (str(data.status) as AgentNodeStatus) : 'running',
        phase: str(data.phase),
        worker: str(data.worker),
        tokens: num(data.tokens),
        elapsedMs: num(data.elapsedMs),
        error: str(data.error),
        // 顺序保护：atMs 缺失时退回 0（不做新旧比较，照常应用）
        atMs: num(data.atMs),
        startedAt: str(data.startedAt),
        endedAt: str(data.endedAt),
      })
      return
    }
    if (e.topic === 'agent.progress') {
      // 后台子智能体的派发 / 完成播报：这是前台「汇报进度」的可见凭据。
      if (!forCurrentSession(str(data.sessionId))) return
      const label = str(data.label) || '子智能体'
      const worker = str(data.worker)
      const phase = str(data.phase)
      const status = str(data.status)
      const elapsed = num(data.elapsedMs) ?? 0
      closeDelta()
      if (phase === 'started') {
        appendLine('已派发「' + label + '」到后台（' + (worker || 'worker') + '），完成后自动汇报', 'state', {
          role: 'status',
          worker,
        })
      } else {
        const ok = status !== 'failed'
        appendLine(
          '「' + label + '」' + (ok ? '已完成' : '失败') + (elapsed > 0 ? ' · 用时 ' + formatElapsedMs(elapsed) : ''),
          ok ? 'ok' : 'error',
          { role: 'status', worker },
        )
      }
      return
    }
    if (e.topic === 'agent.schedule') {
      const rawPhase = str(data.phase)
      // 全局变更（setSchedule）不带 phase，只带 phases：整体刷新一次
      if (rawPhase === '' && Array.isArray(data.phases)) {
        setSchedule(data.phases.map(toSchedule))
        agentState.scheduleReason = str(data.reason)
        if (data.running !== true) agentState.autoRunning = ''
        return
      }
      const phase = rawPhase === 'intraday' || rawPhase === 'postmarket' ? rawPhase : 'premarket'
      applyScheduleTick({
        phase,
        nextRunAt: str(data.nextRunAt),
        running: data.running === true,
        reason: str(data.reason),
      })
    }
  })
}
