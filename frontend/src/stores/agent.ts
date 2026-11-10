// Agent 流式输出：订阅 kernel:event 的 agent.delta / agent.state / agent.report。
// 抽屉与 agentlab 面板共用这一份状态。

import { reactive } from 'vue'
import { onKernelEvent } from '../api'
import type { AgentDeltaPayload, AgentReportPayload, AgentStatePayload } from '../types'

export type LogKind = 'delta' | 'state' | 'tool' | 'error' | 'ok'

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

export function appendLine(text: string, kind: LogKind = 'delta'): void {
  const body = text.replace(/\n+$/, '')
  if (body === '') return
  seq += 1
  const d = new Date()
  const p = (n: number) => (n < 10 ? '0' + n : String(n))
  agentState.log.push({ id: seq, time: p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds()), text: body, kind })
  if (agentState.log.length > MAX_LINES) agentState.log.splice(0, agentState.log.length - MAX_LINES)
  agentState.updatedAt = d.toISOString()
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
