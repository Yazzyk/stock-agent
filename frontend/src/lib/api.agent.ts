// Agent 域新增接口：值班模板/调度、上下文占用、编队树。
// 形状逐字对齐 docs/ITERATION-2026-11.md §3.4 / §3.5 / §3.6。
// 解析层做缺省兜底：后端字段缺失时面板仍可渲染（各降级分支见注释），
// 也因此不会把「后端还没实现」变成白屏。

import { friendlyError, invoke } from '../api'
import type { WorkerInfo } from '../types'
import { arr, pickArr, pickBool, pickNum, pickStr, rec } from './parse'

async function guard<T>(scene: string, fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (e) {
    throw new Error(friendlyError(e, scene))
  }
}

/** 模板/worker id：内置 id 或自定义 worker id（如 etf_flow）。 */
export type TemplateId = string
export type DutyPhase = 'premarket' | 'intraday' | 'postmarket'
export type AgentNodeStatus = 'running' | 'ok' | 'failed'

/** 时段调度：盘前/复盘用 at，盘中用 everyMinutes。 */
export interface PhaseSchedule {
  phase: DutyPhase
  enabled: boolean
  /** "09:15" 形式；盘中一般没有 */
  at?: string
  /** 盘中频率：15/30/60 分钟 */
  everyMinutes?: number
  /** 交易时段，例如 "09:30-11:30,13:00-15:00" */
  window?: string
  nextRunAt?: string
  lastRunAt?: string
  lastStatus?: string
  lastSessionId?: string
}

export interface AgentTemplate {
  id: TemplateId
  title: string
  description: string
  prompt: string
  defaultPrompt: string
  /** 用户改过提示词（可恢复默认） */
  edited: boolean
  enabled: boolean
  /** 该 worker 的默认模型（空表示跟随全局默认） */
  model?: string
  schedule: PhaseSchedule
}

/** agentlab.models 返回的模型选项（不含 key）。 */
export interface AgentModelOption {
  id: string
  model: string
  displayName?: string
  isDefault: boolean
  hasKey: boolean
}

export interface AgentModelList {
  models: AgentModelOption[]
  defaultId: string
}

/** 新建自定义 worker 的入参。 */
export interface AgentWorkerDraft {
  id: string
  title: string
  description: string
  prompt: string
  model?: string
}

export interface AgentContextBreakdown {
  systemTokens: number
  toolsTokens: number
  messageTokens: number
}

/** 会话累计的提示词缓存命中（提供方上报；全 0 时字段仍然存在，前端靠 sample/pressureTokens 判空）。 */
export interface AgentContextCache {
  /** 未命中缓存的提示词 token（prompt - cached） */
  uncachedInputTokens: number
  /** 命中缓存、按缓存价计费的 token */
  cacheReadTokens: number
  outputTokens: number
  /** cached/prompt 的整数百分比；prompt=0 时为 0 */
  hitPercent: number
}

/** 会话累计用量（提供方 usage 的累计值），用于「会话累计」小网格。 */
export interface AgentContextUsage {
  promptTokens: number
  cachedTokens: number
  completionTokens: number
  totalTokens: number
}

/** 一次模型调用的上下文采样（后端权威 history 的一行，按 step 升序）。 */
export interface AgentContextPoint {
  /** 第几次模型调用（从 1 起；含运行结束的收口调用） */
  step: number
  /** 该请求的 prompt（提供方上报；没有样本时为 0） */
  promptTokens: number
  cachedTokens: number
  completionTokens: number
  /** 该请求的上下文规模：prompt>0 时等于 prompt，否则是启发式估算 */
  used: number
  at: string
}

/** agent.context 事件 / context 方法的载荷。contextWindow=0 表示模型窗口未知。
 *  新增字段全部可选：旧后端/旧事件没有它们时，面板自动降级为「估算」口径。 */
export interface AgentContext {
  sessionId: string
  model: string
  contextWindow: number
  /** 启发式估算（system+tools+messages）；有 pressureTokens 时展示不优先用它 */
  usedTokens: number
  /** 0..100；窗口未知时后端给 0，前端据此降级为只显示已用 */
  percent: number
  breakdown: AgentContextBreakdown
  messages: number
  steps: number
  toolCalls: number
  updatedAt: string
  note?: string
  /** 最近一次请求实际发送的 prompt（提供方精确值）；0/缺失表示还没有样本 */
  pressureTokens?: number
  /** 占用数字的来源：provider（有提供方样本）/ estimated（纯估算） */
  sample?: 'provider' | 'estimated' | ''
  /** 会话累计缓存命中 */
  cache?: AgentContextCache
  /** 会话累计用量 */
  usage?: AgentContextUsage
  /** 逐轮采样（仅本进程内有效，重启后旧会话为空） */
  history?: AgentContextPoint[]
}

/**
 * 编队节点（tree 方法的权威形状）。
 * 根节点的判据是 parentId === ''（不是 mode），旧会话的 mode 可能为空。
 */
export interface AgentNode {
  sessionId: string
  /** 根节点为空串 */
  parentId?: string
  /** 根 = 0；nodes 已按 (depth, startedAt, sessionId) 稳定排序，前端不要再排序 */
  depth: number
  /** 直接子节点数 */
  childCount: number
  label: string
  /** 后端给的别名（恒等于 label），取名字时两者都可以用 */
  role: string
  mode: 'main' | 'subagent'
  worker: string
  status: AgentNodeStatus
  tokens: number
  steps: number
  startedAt: string
  endedAt?: string
  elapsedMs: number
  /** 事件合并用：agent.child 带 atMs，只接受更新的那条（旧事件丢弃） */
  atMs?: number
}

export interface AgentTree {
  root: AgentNode | null
  nodes: AgentNode[]
  /** 整棵树的根会话 id */
  rootSessionId: string
  /** 本次查询的会话 id（可能就是某个子节点） */
  sessionId: string
  total: number
  /** 仍在运行的节点数 */
  activeCount: number
  updatedAt: string
}

/** agent.schedule 事件载荷。 */
export interface ScheduleTick {
  phase: DutyPhase
  nextRunAt?: string
  running: boolean
  reason?: string
}

/**
 * agent.child 事件载荷（started / finished 两条）。
 * 后端带 atMs（毫秒时间戳）：同一节点只接受比本地已记录更新的那条，旧事件直接丢弃——
 * 这是「事件重放」与「tree 快照回读」能安全混用的前提。
 */
export interface AgentChildEvent {
  parentSessionId: string
  childSessionId: string
  label: string
  status: AgentNodeStatus
  /** started / finished */
  phase?: string
  worker?: string
  tokens?: number
  elapsedMs?: number
  error?: string
  atMs?: number
  startedAt?: string
  endedAt?: string
}

export interface SchedulePatch {
  enabled?: boolean
  at?: string
  everyMinutes?: number
  /** 盘中交易时段，例如 "09:30-11:30,13:00-15:00" */
  window?: string
}

// ---------- 解析 ----------

function toPhase(raw: unknown): DutyPhase {
  return raw === 'premarket' || raw === 'intraday' || raw === 'postmarket' ? raw : 'premarket'
}

function toTemplateId(raw: unknown): TemplateId {
  return typeof raw === 'string' && raw !== '' ? raw : 'chat'
}

export function toSchedule(v: unknown): PhaseSchedule {
  const r = rec(v) ?? {}
  const phase = toPhase(pickStr(r, 'phase'))
  return {
    phase,
    enabled: pickBool(r, 'enabled') === true,
    at: pickStr(r, 'at'),
    everyMinutes: pickNum(r, 'everyMinutes'),
    window: pickStr(r, 'window'),
    nextRunAt: pickStr(r, 'nextRunAt'),
    lastRunAt: pickStr(r, 'lastRunAt'),
    lastStatus: pickStr(r, 'lastStatus'),
    lastSessionId: pickStr(r, 'lastSessionId'),
  }
}

export function toAgentTemplate(v: unknown): AgentTemplate {
  const r = rec(v) ?? {}
  const id = toTemplateId(pickStr(r, 'id'))
  const prompt = pickStr(r, 'prompt') ?? ''
  const defaultPrompt = pickStr(r, 'defaultPrompt') ?? ''
  return {
    id,
    title: pickStr(r, 'title') ?? '',
    description: pickStr(r, 'description') ?? '',
    prompt,
    defaultPrompt,
    edited: pickBool(r, 'edited') === true || (defaultPrompt !== '' && prompt !== defaultPrompt),
    enabled: pickBool(r, 'enabled') !== false,
    model: pickStr(r, 'model') || undefined,
    schedule: toSchedule(r.schedule ?? { phase: id }),
  }
}

/** 统一 list 形状：兼容 {templates:[...]} 与裸数组，按 id 去重并丢掉空条目。 */
function templateList(v: unknown): AgentTemplate[] {
  const r = rec(v)
  const raw = r ? pickArr(r, 'templates') : arr(v)
  const seen = new Set<string>()
  const list: AgentTemplate[] = []
  for (const t of raw.map(toAgentTemplate)) {
    if (t.title === '' && t.prompt === '') continue
    if (seen.has(t.id)) continue
    seen.add(t.id)
    list.push(t)
  }
  return list
}

/** 非负数字兜底：缺字段、负数、NaN 一律归 0（解析层不抛错、不做语义改写）。 */
function nonNeg(v: number | undefined): number {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : 0
}

/** 解析一行逐轮采样；step/at 都不合法的条目直接丢掉（宁可少一行，不要一行空行）。 */
function toContextPoint(v: unknown): AgentContextPoint | null {
  const r = rec(v)
  if (!r) return null
  const step = pickNum(r, 'step') ?? 0
  const at = pickStr(r, 'at') ?? ''
  if (step <= 0 && at === '') return null
  return {
    step,
    promptTokens: nonNeg(pickNum(r, 'promptTokens')),
    cachedTokens: nonNeg(pickNum(r, 'cachedTokens')),
    completionTokens: nonNeg(pickNum(r, 'completionTokens')),
    used: nonNeg(pickNum(r, 'used')),
    at,
  }
}

/** 解析缓存桶：缺字段按 0，hitPercent 越界时按 0（口径由 contextview 重算）。 */
function toContextCache(v: unknown): AgentContextCache | undefined {
  const r = rec(v)
  if (!r) return undefined
  const hit = pickNum(r, 'hitPercent') ?? 0
  return {
    uncachedInputTokens: nonNeg(pickNum(r, 'uncachedInputTokens')),
    cacheReadTokens: nonNeg(pickNum(r, 'cacheReadTokens')),
    outputTokens: nonNeg(pickNum(r, 'outputTokens')),
    hitPercent: hit > 0 && hit <= 100 ? hit : 0,
  }
}

function toContextUsage(v: unknown): AgentContextUsage | undefined {
  const r = rec(v)
  if (!r) return undefined
  return {
    promptTokens: nonNeg(pickNum(r, 'promptTokens')),
    cachedTokens: nonNeg(pickNum(r, 'cachedTokens')),
    completionTokens: nonNeg(pickNum(r, 'completionTokens')),
    totalTokens: nonNeg(pickNum(r, 'totalTokens')),
  }
}

export function toAgentContext(v: unknown): AgentContext | null {
  const r = rec(v)
  if (!r) return null
  const sessionId = pickStr(r, 'sessionId') ?? ''
  const window_ = nonNeg(pickNum(r, 'contextWindow', 'ContextWindow', 'window'))
  const used = nonNeg(pickNum(r, 'usedTokens', 'UsedTokens'))
  const b = rec(r.breakdown) ?? {}
  const pressure = nonNeg(pickNum(r, 'pressureTokens'))
  const sampleRaw = pickStr(r, 'sample')
  const sample: AgentContext['sample'] =
    sampleRaw === 'provider' || sampleRaw === 'estimated' ? sampleRaw : pressure > 0 ? 'provider' : 'estimated'
  // percent：后端已按「有 pressure 用 pressure」的同一口径算好，直接沿用；
  // 只有旧后端完全没给 percent 时才在前端按同一口径补一次（整数，不引入第二套精度）。
  const accounted = pressure > 0 ? pressure : used
  const percent = pickNum(r, 'percent') ?? (window_ > 0 ? Math.min(100, Math.round((accounted / window_) * 100)) : 0)
  const history = pickArr(r, 'history')
    .map(toContextPoint)
    .filter((p): p is AgentContextPoint => p !== null)
  const out: AgentContext = {
    sessionId,
    model: pickStr(r, 'model') ?? '',
    contextWindow: window_,
    usedTokens: used,
    percent,
    breakdown: {
      systemTokens: nonNeg(pickNum(b, 'systemTokens')),
      toolsTokens: nonNeg(pickNum(b, 'toolsTokens')),
      messageTokens: nonNeg(pickNum(b, 'messageTokens')),
    },
    messages: nonNeg(pickNum(r, 'messages')),
    steps: nonNeg(pickNum(r, 'steps')),
    toolCalls: nonNeg(pickNum(r, 'toolCalls')),
    updatedAt: pickStr(r, 'updatedAt') ?? '',
    note: pickStr(r, 'note'),
    pressureTokens: pressure,
    sample,
    cache: toContextCache(r.cache),
    usage: toContextUsage(r.usage),
    history,
  }
  return out
}

export function toAgentNode(v: unknown): AgentNode {
  const r = rec(v) ?? {}
  const status = pickStr(r, 'status')
  const label = pickStr(r, 'label') ?? ''
  return {
    sessionId: pickStr(r, 'sessionId') ?? '',
    // parentId 缺失等同空串（根），不要用 undefined 让下游到处判空
    parentId: pickStr(r, 'parentId') ?? '',
    depth: pickNum(r, 'depth') ?? 0,
    childCount: pickNum(r, 'childCount') ?? 0,
    label,
    role: pickStr(r, 'role') ?? label,
    mode: pickStr(r, 'mode') === 'main' ? 'main' : 'subagent',
    worker: pickStr(r, 'worker') ?? '',
    status: status === 'ok' || status === 'failed' ? status : 'running',
    tokens: pickNum(r, 'tokens') ?? 0,
    steps: pickNum(r, 'steps') ?? 0,
    startedAt: pickStr(r, 'startedAt') ?? '',
    endedAt: pickStr(r, 'endedAt'),
    elapsedMs: pickNum(r, 'elapsedMs') ?? 0,
  }
}

export function toAgentTree(v: unknown): AgentTree {
  const r = rec(v) ?? {}
  const rootRaw = rec(r.root)
  const root = rootRaw ? toAgentNode(rootRaw) : null
  // nodes 已由后端按 (depth, startedAt, sessionId) 稳定排序，这里保持原顺序
  const nodes = pickArr(r, 'nodes').map(toAgentNode).filter((n) => n.sessionId !== '')
  if (root && !nodes.some((n) => n.sessionId === root.sessionId)) nodes.unshift(root)
  const rootId = pickStr(r, 'rootSessionId') ?? root?.sessionId ?? ''
  return {
    root,
    nodes,
    rootSessionId: rootId,
    sessionId: pickStr(r, 'sessionId') ?? '',
    total: pickNum(r, 'total') ?? nodes.length,
    activeCount: pickNum(r, 'activeCount') ?? nodes.filter((n) => n.status === 'running').length,
    updatedAt: pickStr(r, 'updatedAt') ?? '',
  }
}

// ---------- 方法 ----------

export function agentTemplates(): Promise<AgentTemplate[]> {
  return guard('读取值班模板', async () => templateList(await invoke('agentlab', 'templates', {})))
}

/** 保存提示词（可同时改开关与模型）；model 传空串表示清空该 worker 的默认模型。 */
export function agentSaveTemplate(id: TemplateId, prompt: string, enabled?: boolean, model?: string): Promise<AgentTemplate[]> {
  const payload: Record<string, unknown> = { id, prompt }
  if (typeof enabled === 'boolean') payload.enabled = enabled
  if (typeof model === 'string') payload.model = model
  return guard('保存提示词', async () => templateList(await invoke('agentlab', 'saveTemplate', payload)))
}

/** worker 的类别：主控 / 预设 / 用户自建（workers 方法用 kind 区分）。 */
export type WorkerKind = 'main' | 'preset' | 'custom'

/**
 * 解析 worker 列表项。
 * 后端现在的判据是 kind（main/preset/custom），同时给 main/preset/builtin/custom 布尔；
 * 旧的裸数组响应没有 kind，此时按 id==='router' 与 custom 布尔回落。
 */
function toWorkerInfo(v: unknown): WorkerInfo {
  const r = rec(v) ?? {}
  const id = pickStr(r, 'id') ?? ''
  const rawKind = pickStr(r, 'kind')
  const kind: WorkerKind =
    rawKind === 'main' || rawKind === 'preset' || rawKind === 'custom'
      ? rawKind
      : pickBool(r, 'main') === true || id === 'router'
        ? 'main'
        : pickBool(r, 'custom') === true
          ? 'custom'
          : 'preset'
  return {
    id,
    title: pickStr(r, 'title') ?? '',
    description: pickStr(r, 'description') ?? '',
    kind,
    custom: kind === 'custom',
    model: pickStr(r, 'model') || undefined,
  }
}

/**
 * worker 列表归一：主路径是 {workers:[...], mainId, total}。
 * 裸数组只是对旧响应的兼容分支，不参与任何主控判定。
 */
function workerList(v: unknown): WorkerInfo[] {
  const r = rec(v)
  const raw = r ? pickArr(r, 'workers') : arr(v)
  return raw.map(toWorkerInfo).filter((w) => w.id !== '')
}

/** 解析模型列表；hasKey 决定前端是否展示该选项。 */
function toModelOption(v: unknown): AgentModelOption {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id') ?? '',
    model: pickStr(r, 'model') ?? '',
    displayName: pickStr(r, 'displayName'),
    isDefault: pickBool(r, 'isDefault') === true,
    hasKey: pickBool(r, 'hasKey') === true,
  }
}

export function agentModels(): Promise<AgentModelList> {
  return guard('读取 Agent 可用模型', async () => {
    const v = await invoke('agentlab', 'models', {})
    const r = rec(v) ?? {}
    return {
      models: pickArr(r, 'models').map(toModelOption).filter((m) => m.id !== ''),
      defaultId: pickStr(r, 'defaultId') ?? '',
    }
  })
}

/** 创建自定义 worker，返回合并后的 worker 列表。 */
export function agentWorkerCreate(draft: AgentWorkerDraft): Promise<WorkerInfo[]> {
  const payload: Record<string, unknown> = {
    id: draft.id,
    title: draft.title,
    description: draft.description,
    prompt: draft.prompt,
  }
  if (draft.model) payload.model = draft.model
  return guard('创建 worker', async () => workerList(await invoke('agentlab', 'createWorker', payload)))
}

/** 删除自定义 worker，返回合并后的 worker 列表。 */
export function agentWorkerRemove(id: string): Promise<WorkerInfo[]> {
  return guard('删除 worker', async () => workerList(await invoke('agentlab', 'removeWorker', { id })))
}

export function agentResetTemplate(id: TemplateId): Promise<AgentTemplate[]> {
  return guard('恢复默认提示词', async () => templateList(await invoke('agentlab', 'resetTemplate', { id })))
}

export function agentSchedule(): Promise<PhaseSchedule[]> {
  return guard('读取调度配置', async () => {
    const v = await invoke('agentlab', 'schedule', {})
    const r = rec(v)
    const raw = r ? pickArr(r, 'phases') : arr(v)
    return raw.map(toSchedule)
  })
}

export function agentSetSchedule(phase: DutyPhase, patch: SchedulePatch): Promise<PhaseSchedule[]> {
  const payload: Record<string, unknown> = { phase }
  if (typeof patch.enabled === 'boolean') payload.enabled = patch.enabled
  if (typeof patch.at === 'string' && patch.at !== '') payload.at = patch.at
  if (typeof patch.everyMinutes === 'number' && patch.everyMinutes > 0) payload.everyMinutes = patch.everyMinutes
  if (typeof patch.window === 'string' && patch.window.trim() !== '') payload.window = patch.window.trim()
  return guard('保存调度配置', async () => {
    const v = await invoke('agentlab', 'setSchedule', payload)
    const r = rec(v)
    return (r ? pickArr(r, 'phases') : arr(v)).map(toSchedule)
  })
}

/** workers 方法的响应：workers + 主控 id（mainId 恒为 router）。 */
export interface AgentWorkerList {
  workers: WorkerInfo[]
  mainId: string
  total: number
  presets: number
  customs: number
}

export function agentWorkerList(): Promise<AgentWorkerList> {
  return guard('读取 worker 列表', async () => {
    const v = await invoke('agentlab', 'workers', {})
    const r = rec(v)
    const workers = workerList(v)
    // mainId 是后端的权威主控指针（恒为 "router"）；只有旧响应缺它时才看 kind，
    // 绝不拿「列表第一个」当主控——列表顺序不保证，误判会让面板失去对话框。
    const mainId =
      (r ? pickStr(r, 'mainId') : undefined) || workers.find((w) => w.kind === 'main')?.id || ''
    return {
      workers,
      mainId,
      total: (r ? pickNum(r, 'total') : undefined) ?? workers.length,
      presets: (r ? pickNum(r, 'presets') : undefined) ?? workers.filter((w) => w.kind === 'preset').length,
      customs: (r ? pickNum(r, 'customs') : undefined) ?? workers.filter((w) => w.kind === 'custom').length,
    }
  })
}

export function agentContext(sessionId: string): Promise<AgentContext | null> {
  // sessionId 为空时后端会回退到「最近一次会话」：空闲状态下也要能看到上下文，
  // 所以这里不再提前返回 null（那是「只有运行中才有上下文」的旧限制）。
  return guard('读取上下文占用', async () => {
    const payload: Record<string, unknown> = {}
    if (sessionId !== '') payload.sessionId = sessionId
    return toAgentContext(await invoke('agentlab', 'context', payload))
  })
}

export function agentTree(sessionId: string): Promise<AgentTree> {
  return guard('读取编队树', async () => {
    const payload: Record<string, unknown> = {}
    if (sessionId !== '') payload.sessionId = sessionId
    return toAgentTree(await invoke('agentlab', 'tree', payload))
  })
}

/** 空回复原因代码 → 人话标题（后端 emptyReason 是稳定枚举）。 */
export function emptyReasonTitle(code: string): string {
  switch (code) {
    case 'no_response':
      return '模型没有返回任何内容'
    case 'reasoning_only':
      return '只有推理过程，没有正文'
    case 'truncated':
      return '输出被 max_tokens 截断'
    case 'content_filter':
      return '被内容过滤拦截'
    case 'steps':
      return '步数上限用完，还没写出结论'
    case 'budget':
      return 'token 预算用完，没产出正文'
    case 'tool_only':
      return '只调用了工具，没有写出结论'
    case 'error':
      return '运行报错，没有产出正文'
    default:
      return ''
  }
}

/** 空回复原因代码 → 可执行的下一步建议（客户端动作，不涉及后端接口）。 */
export function emptyReasonHint(code: string): string {
  switch (code) {
    case 'reasoning_only':
    case 'truncated':
      return '把输出上限调大（例如 8192）后重试，或换一个非推理模型。'
    case 'no_response':
      return '稍后重试一次；连续出现就换模型或检查服务商状态。'
    case 'content_filter':
      return '改写触发词后重试（去掉敏感表述或个股明确买卖建议）。'
    case 'steps':
      return '把任务拆小（例如先只要结论、再要明细）后重跑。'
    case 'budget':
      return '缩小输入范围（更少的标的/更短的上下文）后重跑。'
    case 'tool_only':
      return '在提示词里明确要求最后输出结论段落，再重跑。'
    default:
      return ''
  }
}

/** 秒级倒计时文案：给「下次运行」用。 */
export function countdownText(iso: string | undefined, now: number): string {
  if (!iso) return ''
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return ''
  const diff = ts - now
  if (diff <= 0) return '即将运行'
  const min = Math.floor(diff / 60000)
  if (min < 1) return '不到 1 分钟'
  if (min < 60) return min + ' 分钟'
  const h = Math.floor(min / 60)
  const m = min % 60
  if (h < 24) return m > 0 ? h + ' 小时 ' + m + ' 分' : h + ' 小时'
  return Math.floor(h / 24) + ' 天'
}

/** token 数缩写：18240 → 18.2K。 */
export function shortTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0'
  if (n < 1000) return String(Math.round(n))
  if (n < 1000000) return (Math.round(n / 100) / 10).toFixed(1).replace(/\.0$/, '') + 'K'
  return (Math.round(n / 100000) / 10).toFixed(1).replace(/\.0$/, '') + 'M'
}

/** 耗时缩写：4200 → 4.2s，195000 → 3分15秒。 */
export function shortElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  if (ms < 1000) return Math.round(ms) + 'ms'
  if (ms < 60000) return (Math.round(ms / 100) / 10).toFixed(1).replace(/\.0$/, '') + 's'
  const total = Math.round(ms / 1000)
  const m = Math.floor(total / 60)
  const s = total % 60
  return s > 0 ? m + '分' + s + '秒' : m + '分'
}
