// Agent 工作台的派生层：对话转写、轨迹账本、密度条、历史事件配对。
// 这里全是纯函数（不依赖 Vue 响应式），组件只负责渲染；
// 数据来源统一是 stores/agent 的日志行（log）与 reports，历史会话则先由 sessionFeed 还原成日志行。
//
// 约定：日志行的 kind 语义保持不变（其它面板/抽屉依赖），新增的 role / tool / step 等字段
// 只用于「对话 / 轨迹」这两个结构化视图，旧行缺字段时按 kind 兜底推断。

import type { AgentSession, AgentSessionEvent } from '../types'
import { mdToHtml } from './markdown'

/** 结构化角色：用户 / 助手 / 工具 / 状态 / 系统。 */
export type AgentRole = 'user' | 'assistant' | 'tool' | 'status' | 'system'

/** 一次工具调用（agent.tool 事件合并后的结果，或历史会话配对出来的结果）。 */
export interface ToolCall {
  /** 后端给的调用 id：start/end 用它合并；历史会话用事件序号兜底 */
  id: string
  sessionId: string
  worker: string
  /** 内层工具名，例如 market.quote */
  name: string
  /** 元工具名：tool.call / tool.search / tool.describe（inspector 里展示） */
  meta: string
  args: string
  result: string
  ok: boolean
  error: string
  /** 耗时（毫秒）；0 表示后端没给，不编造 */
  ms: number
  step: number
  at: string
  phase: 'start' | 'end'
}

/** 一份报告（交付物）：只装**根会话的最终产出**，子智能体的过程报告不在这个容器里（详见 ChildReport 注释）。
    同一根会话多次运行会有多份，按时间顺序追加，不互相覆盖。 */
export interface ReportEntry {
  id: string
  /** 空串表示「未归属」：事件里没带 sessionId，单独成区 */
  sessionId: string
  worker: string
  /** ISO 时间 */
  at: string
  content: string
  steps: number
  tokens: number
  model: string
  /** 正文是推理过程回退来的 */
  fromReasoning: boolean
}

/** 历史会话事件还原成日志行时的中间结构（appendLine 直接消费）。 */
export interface FeedSpec {
  text: string
  kind: 'delta' | 'state' | 'tool' | 'error' | 'ok' | 'user' | 'system'
  role: AgentRole
  tool?: ToolCall
  /** 事件原始时间（HH:MM:SS）；缺省由 appendLine 取当前时间 */
  time: string
  worker: string
  /** 模型调用步数：历史事件没有这个字段时按工具调用顺序补一个 */
  step: number
}

// ---------- 通用小工具 ----------

/** 压成单行并截断：行内预览必须单行，完整文本走 title / inspector。 */
export function oneLine(text: string, max = 160): string {
  const body = String(text ?? '')
    .replace(/\s+/g, ' ')
    .trim()
  if (body.length <= max) return body
  return body.slice(0, max) + '…'
}

/** 毫秒 → 420ms / 1.3s / 2m10s；0 或负数返回空串（没有就不显示）。 */
export function formatMs(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  if (ms < 1000) return Math.round(ms) + 'ms'
  const sec = ms / 1000
  if (sec < 10) return sec.toFixed(1) + 's'
  if (sec < 60) return Math.round(sec) + 's'
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return m + 'm' + (s > 0 ? s + 's' : '')
}

/** 角色中文标签（轨迹表格左侧的角色块）。 */
export function roleLabel(role: AgentRole): string {
  if (role === 'user') return '用户'
  if (role === 'assistant') return '助手'
  if (role === 'tool') return '工具'
  if (role === 'status') return '状态'
  return '系统'
}

/** 日志行 → 结构化角色：优先用行上的 role，旧行按 kind 推断。 */
export function roleOf(line: { kind: string; role?: AgentRole }): AgentRole {
  if (line.role) return line.role
  if (line.kind === 'user' || line.kind === 'user-pending') return 'user'
  if (line.kind === 'delta') return 'assistant'
  if (line.kind === 'tool') return 'tool'
  if (line.kind === 'state' || line.kind === 'ok' || line.kind === 'error') return 'status'
  return 'system'
}

// ---------- 状态色调（唯一一套映射） ----------

/** 状态色调：ok=绿（已完成/成功）· warn=琥珀（进行中 / 已停止）· err=红（失败）· dim=灰（空闲）。 */
export type StateTone = 'ok' | 'warn' | 'err' | 'dim'

/**
 * 状态 → 色调。**全站唯一一套映射**：工作台头部状态点、对话页 live 状态行、
 * 轨迹页状态行 / chip、会话列表徽标、编队节点都只调这里，
 * 避免同一个状态在不同面板里显示成两种颜色（历史问题：已完成有时是琥珀/白色）。
 *
 * 入参既接受后端状态码（done / ok / thinking / tool / waiting / stopped / error…），
 * 也接受中文文案（已完成 / 成功 / 运行中 / 已停止 / 失败…）；未识别一律按空闲（灰）处理。
 * 语义约定：「已停止」是中性偏琥珀，**不是绿色**——停止不等于完成。
 */
export function stateTone(state: string): StateTone {
  const s = (state ?? '').trim().toLowerCase()
  if (s === '') return 'dim'
  // 完成 / 成功 → 绿
  if (
    s === 'done' ||
    s === 'ok' ||
    s === 'success' ||
    s === 'succeeded' ||
    s === '完成' ||
    s === '已完成' ||
    s === '成功' ||
    s === '已成功'
  ) {
    return 'ok'
  }
  // 失败 → 红
  if (s === 'error' || s === 'failed' || s === 'fail' || s === '失败' || s === '出错') return 'err'
  // 进行中 / 已停止 → 琥珀
  if (
    s === 'thinking' ||
    s === 'tool' ||
    s === 'waiting' ||
    s === 'running' ||
    s === 'preparing' ||
    s === '准备中' ||
    s === '思考中' ||
    s === '调用工具' ||
    s === '等待子智能体' ||
    s === '运行中' ||
    s === 'stopped' ||
    s === '已停止' ||
    s === '停止'
  ) {
    return 'warn'
  }
  // 空闲 / 未知 → 灰
  return 'dim'
}

/** 色调 → 圆点 class：全局 style.css 的 dot-* 与这套色板一一对应（ok 绿 / warn 琥珀 / err 红 / off 灰）。 */
export function toneDot(tone: StateTone): 'dot-ok' | 'dot-warn' | 'dot-err' | 'dot-off' {
  if (tone === 'ok') return 'dot-ok'
  if (tone === 'warn') return 'dot-warn'
  if (tone === 'err') return 'dot-err'
  return 'dot-off'
}

/** 色调 → 文字色 class（各组件 scoped 样式里定义 .tone-ok / .tone-warn / .tone-err / .tone-dim）。 */
export function toneClass(tone: StateTone): string {
  return 'tone-' + tone
}

/** 色调 → 徽标 class（会话列表状态、编队节点状态标签用；同样由各组件 scoped 定义）。 */
export function toneTag(tone: StateTone): string {
  return 'tone-tag-' + tone
}

/**
 * 状态 → 中文文案。工作台、对话页、轨迹页、会话列表共用这一份口径，
 * 未识别的状态码原样返回（宁可显示后端枚举，也不编造）。
 */
export function stateLabel(state: string): string {
  const s = (state ?? '').trim()
  if (s === '') return '空闲'
  switch (s.toLowerCase()) {
    case 'thinking':
      return '思考中'
    case 'tool':
      return '调用工具'
    case 'waiting':
      return '等待子智能体'
    case 'preparing':
      return '准备中'
    case 'running':
      return '运行中'
    case 'done':
    case 'ok':
    case 'success':
      return '已完成'
    case 'stopped':
      return '已停止'
    case 'error':
    case 'failed':
      return '失败'
    default:
      return s
  }
}

/** 状态行文案拆成「标签 + 明细」：日志里是 `[思考中] 准备上下文`，chip 只显示标签。 */
export function splitStatus(text: string): { label: string; detail: string } {
  const m = /^\[([^\]]+)\]\s*(.*)$/.exec(text ?? '')
  if (!m) return { label: (text ?? '').trim(), detail: '' }
  return { label: (m[1] ?? '').trim(), detail: (m[2] ?? '').trim() }
}

/** 工具行单行预览：`market.quote · {"code":"600519"} → 1523.40 (+1.24%)`。 */
export function toolText(tool: ToolCall): string {
  const args = oneLine(tool.args, 80)
  const raw = tool.ok ? tool.result : tool.error || tool.result
  const result = oneLine(raw, 90)
  const head = tool.name + (args !== '' ? ' · ' + args : '')
  if (tool.phase === 'start') return head + ' → 运行中…'
  if (result !== '') return head + ' → ' + result
  return head + ' → ' + (tool.ok ? '无输出' : '失败')
}

/** 工具耗时/token 这类小数字的空值：0 显示「—」，避免编造。 */
export function dashNum(v: number): string {
  return v > 0 ? String(v) : '—'
}

// ---------- Markdown（报告正文 / 助手正文共用） ----------

/** 从源码里按顺序取出代码块语言（mdToHtml 会丢掉 fence 信息，这里补回来做头部标签）。 */
function fenceLanguages(src: string): string[] {
  const out: string[] = []
  let open = false
  for (const line of src.replace(/\r\n/g, '\n').split('\n')) {
    const m = /^\s*```(.*)$/.exec(line)
    if (!m) continue
    if (!open) {
      out.push((m[1] ?? '').trim() || 'text')
      open = true
    } else {
      open = false
    }
  }
  return out
}

/**
 * 渲染报告/正文 Markdown。
 * 在 mdToHtml 的基础上把 `<pre>` 包成「玻璃面板 + 语言标签 + 复制按钮」，
 * 复制行为由组件里的点击委托处理（data-copy-code 标记），避免在 v-html 里挂事件。
 */
export function markdownHtml(src: string): string {
  if (!src) return ''
  const langs = fenceLanguages(src)
  let index = 0
  return mdToHtml(src).replace(/<pre class="md-pre">([\s\S]*?)<\/pre>/g, (_m, body: string) => {
    const lang = langs[index] ?? 'text'
    index += 1
    return (
      '<div class="codeblk">' +
      '<div class="codeblk-hd"><span class="codeblk-lang">' + escapeHtml(lang) + '</span>' +
      '<button class="codeblk-copy" type="button" data-copy-code="1">复制</button></div>' +
      '<pre class="md-pre">' + body + '</pre>' +
      '</div>'
    )
  })
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

// ---------- 对话转写 ----------

export interface ChatTextPart {
  type: 'text'
  key: string
  text: string
  streaming: boolean
}
export interface ChatToolPart {
  type: 'tool'
  key: string
  tool: ToolCall
}
export interface ChatStatusPart {
  type: 'status'
  key: string
  label: string
  detail: string
  state: 'run' | 'ok' | 'error'
  repeat: number
}
export type ChatPart = ChatTextPart | ChatToolPart | ChatStatusPart

export interface ChatAssistantItem {
  kind: 'assistant'
  key: string
  /** HH:MM:SS */
  at: string
  worker: string
  model: string
  steps: number
  tokens: number
  streaming: boolean
  parts: ChatPart[]
}
export interface ChatUserItem {
  kind: 'user'
  key: string
  at: string
  text: string
}
export type ChatItem = ChatAssistantItem | ChatUserItem

/** 由日志行切出对话条目：用户消息独立成条，其余（助手正文/工具/状态）并进同一条助手消息。 */
export function buildChat(
  log: Array<{
    id: number
    time: string
    text: string
    kind: string
    role?: AgentRole
    streaming?: boolean
    repeat?: number
    worker?: string
    model?: string
    step?: number
    tokens?: number
    tool?: ToolCall
  }>,
  fallback: { worker: string; model: string },
): ChatItem[] {
  const out: ChatItem[] = []
  for (const line of log) {
    const role = roleOf(line)
    // 系统提示词不参与对话流：它在轨迹页单独展示（完整口径可能上万字，塞进聊天气泡会淹没对话）。
    if (role === 'system') continue
    if (role === 'user') {
      out.push({ kind: 'user', key: 'u' + line.id, at: line.time, text: line.text })
      continue
    }
    let block = out[out.length - 1]
    if (!block || block.kind !== 'assistant') {
      const next: ChatAssistantItem = {
        kind: 'assistant',
        key: 'a' + line.id,
        at: line.time,
        worker: line.worker || fallback.worker || 'RouterAgent',
        model: line.model || fallback.model,
        steps: 0,
        tokens: 0,
        streaming: false,
        parts: [],
      }
      out.push(next)
      block = next
    }
    const assistant = block as ChatAssistantItem
    if (role === 'tool') {
      const tool = line.tool ?? fallbackTool(line.id, line.text)
      assistant.parts.push({ type: 'tool', key: 't' + line.id, tool })
      continue
    }
    if (role === 'status') {
      const s = splitStatus(line.text)
      const state = line.kind === 'error' ? 'error' : line.kind === 'ok' ? 'ok' : 'run'
      assistant.parts.push({ type: 'status', key: 's' + line.id, label: s.label, detail: s.detail, state, repeat: line.repeat ?? 1 })
      continue
    }
    // 助手正文：kind==='delta'
    assistant.parts.push({ type: 'text', key: 'm' + line.id, text: line.text, streaming: line.streaming === true })
    if (line.streaming) assistant.streaming = true
    if (line.step && line.step > assistant.steps) assistant.steps = line.step
    if (line.tokens && line.tokens > 0) assistant.tokens += line.tokens
    if (line.model && line.model !== '') assistant.model = line.model
    if (line.worker && line.worker !== '') assistant.worker = line.worker
  }
  return out
}

/** 旧日志里没有结构化 tool 字段时的兜底：把一行文本当成工具调用展示。 */
function fallbackTool(id: number, text: string): ToolCall {
  const body = text.replace(/^\[[^\]]+\]\s*/, '')
  const arrow = body.indexOf('→')
  const head = arrow >= 0 ? body.slice(0, arrow) : body
  const rest = arrow >= 0 ? body.slice(arrow + 1) : ''
  const sep = head.indexOf(' · ')
  return {
    id: 'line-' + id,
    sessionId: '',
    worker: '',
    name: sep >= 0 ? head.slice(0, sep) : head.trim(),
    meta: '',
    args: sep >= 0 ? head.slice(sep + 3).trim() : '',
    result: rest.trim(),
    ok: true,
    error: '',
    ms: 0,
    step: 0,
    at: '',
    phase: 'end',
  }
}

/** 助手条目的纯文本（复制按钮用）：正文段落拼接，工具行用单行预览。 */
export function chatTextOf(item: ChatItem): string {
  if (item.kind === 'user') return item.text
  return item.parts
    .map((p) => (p.type === 'text' ? p.text : p.type === 'tool' ? toolText(p.tool) : '[' + p.label + '] ' + p.detail))
    .join('\n\n')
}

// ---------- 轨迹账本 ----------

export interface TrajRow {
  key: string
  lineId: number
  turn: number
  role: AgentRole
  text: string
  time: string
  worker: string
  step: number
  tool?: ToolCall
  error: boolean
  running: boolean
}

export interface TrajTurn {
  turn: number
  rows: TrajRow[]
  /** 步数：有 step 字段时按去重计，否则按助手消息条数 */
  steps: number
  tools: number
}

/** 日志行 → 轨迹行；用户消息开新一轮（第一条用户消息是第 1 轮，之前的行也算第 1 轮）。 */
export function trajRows(
  log: Array<{
    id: number
    time: string
    text: string
    kind: string
    role?: AgentRole
    streaming?: boolean
    worker?: string
    step?: number
    tool?: ToolCall
  }>,
): TrajRow[] {
  const rows: TrajRow[] = []
  let turn = 0
  for (const line of log) {
    const role = roleOf(line)
    // 系统提示词单独展示（systemPromptOf），不混进轮次账本与密度条。
    if (role === 'system') continue
    if (role === 'user') turn += 1
    else if (turn === 0) turn = 1
    const tool = role === 'tool' ? (line.tool ?? fallbackTool(line.id, line.text)) : undefined
    rows.push({
      key: 'r' + line.id,
      lineId: line.id,
      turn,
      role,
      text: line.text,
      time: line.time,
      worker: line.worker ?? '',
      step: line.step ?? 0,
      tool,
      error: line.kind === 'error' || (tool !== undefined && tool.ok === false),
      running: tool !== undefined ? tool.phase === 'start' : line.streaming === true,
    })
  }
  return rows
}

/** 按轮次分组，附上折叠摘要需要的步数/工具调用数。 */
export function groupTurns(rows: TrajRow[]): TrajTurn[] {
  const out: TrajTurn[] = []
  for (const row of rows) {
    let last = out[out.length - 1]
    if (!last || last.turn !== row.turn) {
      last = { turn: row.turn, rows: [], steps: 0, tools: 0 }
      out.push(last)
    }
    last.rows.push(row)
  }
  for (const t of out) {
    const steps = new Set(t.rows.filter((r) => r.step > 0).map((r) => r.step)).size
    const assistants = t.rows.filter((r) => r.role === 'assistant' && !r.running).length
    t.steps = steps > 0 ? steps : assistants
    t.tools = t.rows.filter((r) => r.role === 'tool').length
  }
  return out
}

/** 全局统计（顶部细文本：N 步 · M 次工具调用 · K 份报告由组件补上）。 */
export function trajStats(rows: TrajRow[]): { steps: number; tools: number } {
  const steps = new Set(rows.filter((r) => r.step > 0).map((r) => r.step)).size
  const assistants = rows.filter((r) => r.role === 'assistant' && !r.running).length
  return { steps: steps > 0 ? steps : assistants, tools: rows.filter((r) => r.role === 'tool').length }
}

/** 搜索：多个关键词 AND，命中范围覆盖正文、工具名/参数/结果与角色。 */
export function filterTrajRows(rows: TrajRow[], query: string): TrajRow[] {
  const terms = query
    .trim()
    .toLocaleLowerCase()
    .split(/\s+/)
    .filter(Boolean)
  if (terms.length === 0) return rows
  return rows.filter((row) => {
    const hay = [
      row.text,
      row.worker,
      row.role,
      row.tool?.name ?? '',
      row.tool?.meta ?? '',
      row.tool?.args ?? '',
      row.tool?.result ?? '',
      row.tool?.error ?? '',
    ]
      .join(' ')
      .toLocaleLowerCase()
    return terms.every((t) => hay.includes(t))
  })
}

/** 密度条一个色块：按「操作序列」等宽投影（不做缩放/拖拽，保持 DSH 的读法）。 */
export interface TrajSpan {
  key: string
  lineId: number
  /** 行序号（从 0 开始，用于提示文本） */
  index: number
  lane: number
  left: number
  width: number
  role: AgentRole
  error: boolean
}

/** 三泳道：输入（用户）/ 模型（助手）/ 工具；与 DSH 的 laneFor 一致。 */
export function laneOf(role: AgentRole): number {
  if (role === 'user') return 0
  if (role === 'tool') return 2
  return 1
}

/** 生成密度条色块（百分比坐标，窗口宽度由 CSS 决定）。 */
export function trajectorySpans(rows: TrajRow[]): TrajSpan[] {
  const n = rows.length
  if (n === 0) return []
  const width = 100 / n
  return rows.map((row, i) => ({
    key: row.key,
    lineId: row.lineId,
    index: i,
    lane: laneOf(row.role),
    left: i * width,
    width,
    role: row.role,
    error: row.error,
  }))
}

// ---------- 历史会话 ----------

/** 历史事件时间（HH:MM:SS）+ 会话开始日期 → ISO。 */
function atOf(sessionStart: string, time: string): string {
  const base = new Date(sessionStart)
  const m = /^(\d{1,2}):(\d{2})(?::(\d{2}))?$/.exec((time ?? '').trim())
  if (Number.isNaN(base.getTime()) || !m) return sessionStart || new Date().toISOString()
  const d = new Date(base.getFullYear(), base.getMonth(), base.getDate(), Number(m[1]), Number(m[2]), Number(m[3] ?? 0))
  return d.toISOString()
}

/**
 * 历史会话事件还原成日志行。
 * tool 事件按「assistant（参数）→ tool（结果）」相邻配对成同一条工具条目；
 * 只有参数没有结果时也保留一行（结果标「未记录结果」，不伪造成功）。
 */
/** 从日志行里取系统提示词（后出现的覆盖先出现的：同一会话只有一条）。 */
export function systemPromptOf(lines: Array<{ kind: string; text: string; role?: AgentRole }>): string {
  let out = ''
  for (const l of lines) {
    if (roleOf(l) === 'system' && l.text.trim() !== '') out = l.text
  }
  return out
}

export function sessionFeed(events: AgentSessionEvent[], session: AgentSession): FeedSpec[] {
  const out: FeedSpec[] = []
  let pending: { name: string; args: string; time: string; seq: number } | null = null
  /** 历史事件没有 step 字段：按工具调用顺序补一个，否则轨迹顶部的「N 步」会一直是 0。 */
  let stepSeq = 0

  const flushPending = (): void => {
    if (!pending) return
    stepSeq += 1
    out.push({
      text: pending.name + (pending.args !== '' ? ' · ' + pending.args : '') + ' → 未记录结果',
      kind: 'tool',
      role: 'tool',
      time: pending.time,
      worker: session.worker,
      step: stepSeq,
      tool: {
        id: 'sess-' + session.id + '-' + pending.seq,
        sessionId: session.id,
        worker: session.worker,
        name: pending.name,
        meta: '',
        args: pending.args,
        result: '',
        ok: true,
        error: '',
        ms: 0,
        step: stepSeq,
        at: '',
        phase: 'end',
      },
    })
    pending = null
  }

  for (const e of events) {
    const kind = (e.kind || '').toLowerCase()
    if (kind === 'report') continue
    // 系统提示词：会话第一条事件，落成 role=system 的日志行（只有轨迹页会展示它）。
    if (kind === 'system') {
      out.push({ text: e.content, kind: 'system', role: 'system', time: e.time, worker: session.worker, step: 0 })
      continue
    }
    if (kind === 'tool' && e.role === 'assistant') {
      flushPending()
      pending = { name: e.toolName || oneLine(e.content, 40), args: e.content, time: e.time, seq: e.seq }
      continue
    }
    if (kind === 'tool' && e.role === 'tool') {
      const name = pending?.name || e.toolName || 'tool'
      const args = pending?.args ?? ''
      const ok = e.ok !== false
      stepSeq += 1
      const tool: ToolCall = {
        id: 'sess-' + session.id + '-' + (e.seq || 0),
        sessionId: session.id,
        worker: session.worker,
        name,
        meta: '',
        args,
        result: ok ? e.content : '',
        ok,
        error: ok ? '' : e.content,
        ms: 0,
        step: stepSeq,
        at: '',
        phase: 'end',
      }
      out.push({ text: toolText(tool), kind: 'tool', role: 'tool', tool, time: e.time, worker: session.worker, step: stepSeq })
      pending = null
      continue
    }
    flushPending()
    if (kind === 'user' || kind === 'steer') {
      out.push({ text: e.content, kind: 'user', role: 'user', time: e.time, worker: session.worker, step: 0 })
      continue
    }
    if (kind === 'tool') {
      // 旧格式：整段 content 已经是「名字 → 结果」
      const body = e.content ?? ''
      const arrow = body.indexOf('→')
      stepSeq += 1
      const tool: ToolCall = {
        id: 'sess-' + session.id + '-' + (e.seq || 0),
        sessionId: session.id,
        worker: session.worker,
        name: e.toolName || (arrow >= 0 ? body.slice(0, arrow).trim() : 'tool'),
        meta: '',
        args: '',
        result: arrow >= 0 ? body.slice(arrow + 1).trim() : body,
        ok: e.ok !== false,
        error: e.ok === false ? body : '',
        ms: 0,
        step: stepSeq,
        at: '',
        phase: 'end',
      }
      out.push({ text: body, kind: 'tool', role: 'tool', tool, time: e.time, worker: session.worker, step: stepSeq })
      continue
    }
    if (kind === 'assistant' || kind === 'delta' || kind === 'message') {
      out.push({ text: e.content, kind: 'delta', role: 'assistant', time: e.time, worker: session.worker, step: 0 })
      continue
    }
    out.push({
      text: e.content,
      kind: kind === 'error' ? 'error' : kind === 'ok' || kind === 'done' ? 'ok' : 'state',
      role: 'status',
      time: e.time,
      worker: session.worker,
      step: 0,
    })
  }
  flushPending()
  return out
}

/** 历史会话里的报告：**只取根会话的最终交付物**，子智能体报告单独收（见 childReportsFromEvents）。
 *
 * 归属判定（旧数据没有 sessionId，所以必须两条路径都支持）：
 *   - 事件带 sessionId：只有等于本会话 id 才算交付物，否则是子智能体报告；
 *   - 事件不带 sessionId（老数据）：用 role 标的 worker 对比会话 worker；
 *     标的是别的 worker 时，只有「该会话最后一条 report 事件」才可能是本会话的交付物
 *     （一个会话一次运行只会落一份自己的 report，子报告是中途插进来的）。
 */
export function reportsFromEvents(events: AgentSessionEvent[], session: AgentSession): ReportEntry[] {
  return splitReports(events, session).root
}

/** 历史会话里的子智能体报告：不进报告册，只给「编队 / 轨迹」当过程证据。 */
export function childReportsFromEvents(events: AgentSessionEvent[], session: AgentSession): ReportEntry[] {
  return splitReports(events, session).child
}

/** 把历史事件里的 report 事件按「根会话交付物 / 子智能体产出」分流（两条导出函数共用）。 */
function splitReports(
  events: AgentSessionEvent[],
  session: AgentSession,
): { root: ReportEntry[]; child: ReportEntry[] } {
  const reports = events.filter((e) => (e.kind || '').toLowerCase() === 'report' && (e.content ?? '').trim() !== '')
  const lastSeq = reports.length > 0 ? (reports[reports.length - 1]?.seq ?? 0) : 0
  const root: ReportEntry[] = []
  const child: ReportEntry[] = []
  reports.forEach((e, index) => {
    const sid = (e.sessionId ?? '').trim()
    const isLast = index === reports.length - 1 || (e.seq ?? 0) === lastSeq
    const belongsToRoot =
      sid !== ''
        ? sid === session.id
        : e.role === undefined || e.role === '' || session.worker === '' || e.role === session.worker || isLast
    const entry: ReportEntry = {
      id: 'r-' + (session.id || 'x') + '-' + (e.seq || index + 1),
      // 老数据没有 sessionId：子报告用一个本会话内的稳定伪 id（不冒充根会话，避免去重/展示串在一起）
      sessionId: sid !== '' ? sid : belongsToRoot ? session.id : session.id + '#sub-' + (e.seq || index + 1),
      // 历史事件的 role 用来标报告作者（子报告是 worker，汇总可以写 RouterAgent）
      worker: e.role && e.role !== '' ? e.role : session.worker,
      at: atOf(session.startedAt, e.time),
      content: e.content ?? '',
      steps: 0,
      tokens: 0,
      model: '',
      fromReasoning: false,
    }
    if (belongsToRoot) root.push(entry)
    else child.push(entry)
  })
  // 历史事件没有逐条用量：把会话总 token 记在最新一份交付物上（其余保持 0，就不显示）
  const last = root[root.length - 1]
  if (last && (session.tokens ?? 0) > 0) last.tokens = session.tokens ?? 0
  return { root, child }
}
