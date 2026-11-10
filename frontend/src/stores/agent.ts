// Agent 流式输出：订阅 kernel:event 的 agent.delta / agent.state / agent.report。
// 抽屉与 agentlab 面板共用这一份状态。

import { reactive } from 'vue'
import { onKernelEvent } from '../api'
import type { AgentDeltaPayload, AgentReportPayload, AgentStatePayload } from '../types'

export type LogKind = 'delta' | 'state' | 'tool' | 'error' | 'ok' | 'user' | 'user-pending'

export interface LogLine {
  id: number
  time: string
  text: string
  kind: LogKind
}

export const agentState = reactive({
  running: false,
  sessionId: '',
  worker: '',
  stateText: '',
  log: [] as LogLine[],
  report: '',
  reportWorker: '',
  updatedAt: '',
})

let seq = 0
const MAX_LINES = 600

/** 追加一行日志，返回行 id（0 表示空文本未追加）。 */
export function appendLine(text: string, kind: LogKind = 'delta'): number {
  const body = text.replace(/\n+$/, '')
  if (body === '') return 0
  seq += 1
  const id = seq
  const d = new Date()
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  agentState.log.push({ id, time: p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds()), text: body, kind })
  if (agentState.log.length > MAX_LINES) agentState.log.splice(0, agentState.log.length - MAX_LINES)
  agentState.updatedAt = d.toISOString()
  return id
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
  const body = text.trim()
  if (body === '') return
  const id = appendLine('（插话已并入下一步）用户：' + body, 'user-pending')
  if (id > 0) pendingSteers.push({ id, text: body })
}

/**
 * 以 agent.steer 事件为准落一条用户消息：
 * 先移除对应的本地待确认行，再追加权威行（事件里的文本优先）。
 */
export function settleSteer(text: string): void {
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
  appendLine('用户：' + content, 'user')
}

export function clearAgentFeed(): void {
  agentState.log = []
  agentState.report = ''
  agentState.reportWorker = ''
  agentState.stateText = ''
}

export function beginRun(worker: string, sessionId = ''): void {
  agentState.running = true
  agentState.worker = worker
  agentState.sessionId = sessionId
  agentState.report = ''
  agentState.reportWorker = worker
  agentState.stateText = '准备中'
  appendLine('开始运行 ' + worker, 'state')
}

export function endRun(failed = false, message = ''): void {
  agentState.running = false
  agentState.stateText = failed ? '失败' : '完成'
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

/** 订阅内核事件；返回取消订阅函数。 */
export function subscribeAgent(): () => void {
  return onKernelEvent((e) => {
    const data = parsePayload(e.payload)
    if (!data) return
    if (e.topic === 'agent.delta') {
      const d = data as AgentDeltaPayload
      agentState.running = true
      if (typeof d.sessionId === 'string' && d.sessionId !== '') agentState.sessionId = d.sessionId
      if (typeof d.text === 'string') appendLine(d.text, 'delta')
      return
    }
    if (e.topic === 'agent.state') {
      const s = data as AgentStatePayload
      const st = typeof s.state === 'string' ? s.state : ''
      if (typeof s.sessionId === 'string' && s.sessionId !== '') agentState.sessionId = s.sessionId
      agentState.stateText = st === 'thinking' ? '思考中' : st === 'tool' ? '调用工具' : st === 'done' ? '完成' : st === 'error' ? '失败' : st
      if (st === 'thinking' || st === 'tool') agentState.running = true
      if (st === 'done' || st === 'error') agentState.running = false
      const detail = typeof s.detail === 'string' && s.detail !== '' ? s.detail : ''
      appendLine('[' + agentState.stateText + ']' + (detail ? ' ' + detail : ''), st === 'tool' ? 'tool' : st === 'error' ? 'error' : 'state')
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
      if (typeof r.report === 'string') agentState.report = r.report
      if (typeof r.worker === 'string') agentState.reportWorker = r.worker
      agentState.running = false
      agentState.stateText = '完成'
      appendLine('报告已生成', 'ok')
    }
  })
}