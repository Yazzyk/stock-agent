// Wails 桥封装：真实环境走 window.go.main.App，浏览器直开时切到演示数据桥。
// Go 侧方法签名见 app.go；返回值是 JSON 字符串，这里统一解析。

import { createMockBridge, subscribeMockKernel } from './api.mock'
import type { AuditRow, GoBridge, KernelEvent, PluginView, StatusView, ToolView } from './types'

/** 是否处于演示模式（没有 Wails 运行时注入）。 */
export function isDemoMode(): boolean {
  return !window.go?.main?.App
}

let mock: GoBridge | null = null

function bridge(): GoBridge {
  const real = window.go?.main?.App
  if (real) return real
  if (!mock) mock = createMockBridge()
  return mock
}

// ---------- 内核事件 ----------

type Listener = (e: KernelEvent) => void

const listeners = new Set<Listener>()
let inited = false

function emit(e: KernelEvent): void {
  for (const fn of listeners) fn(e)
}

function toKernelEvent(payload: unknown): KernelEvent | null {
  if (typeof payload !== 'object' || payload === null) return null
  const r = payload as Record<string, unknown>
  const topic = typeof r.topic === 'string' ? r.topic : ''
  if (!topic) return null
  return {
    topic,
    time: typeof r.time === 'string' ? r.time : new Date().toISOString(),
    payload: typeof r.payload === 'string' ? r.payload : JSON.stringify(r.payload ?? {}),
  }
}

/** 初始化桥：订阅 kernel:event（真实 Wails 或演示事件源）。重复调用无副作用。 */
export function initBridge(): void {
  if (inited) return
  inited = true
  const rt = window.runtime
  if (rt && typeof rt.EventsOn === 'function') {
    rt.EventsOn('kernel:event', (...args: unknown[]) => {
      const e = toKernelEvent(args[0])
      if (e) emit(e)
    })
  }
  if (isDemoMode()) {
    mock = createMockBridge()
    subscribeMockKernel(emit)
  }
}

/** 订阅内核事件，返回取消订阅函数。 */
export function onKernelEvent(cb: Listener): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

// ---------- 绑定方法 ----------

export const Plugins = (): Promise<PluginView[]> => bridge().Plugins()
export const Status = (): Promise<StatusView> => bridge().Status()
export const Tools = (): Promise<ToolView[]> => bridge().Tools()
export const Audit = (limit = 100): Promise<AuditRow[]> => bridge().Audit(limit)

export async function invoke<T = unknown>(plugin: string, method: string, payload: unknown = {}): Promise<T> {
  const raw = await bridge().Invoke(plugin, method, JSON.stringify(payload ?? {}))
  return (raw ? JSON.parse(raw) : null) as T
}

export async function callTool<T = unknown>(name: string, args: unknown = {}): Promise<T> {
  const raw = await bridge().CallTool(name, JSON.stringify(args ?? {}))
  return (raw ? JSON.parse(raw) : null) as T
}

// ---------- 错误文案 ----------

/** 原始错误文本（不加工，供诊断）。 */
export function rawErrorText(e: unknown): string {
  if (e instanceof Error) return e.message
  if (typeof e === 'string') return e
  try {
    return JSON.stringify(e)
  } catch {
    return String(e)
  }
}

/**
 * 把内核/网络错误翻成中文可操作提示。
 * 技术栈信息不外露，避免用户看到一屏内部路径。
 */
export function friendlyError(e: unknown, scene = '操作'): string {
  const raw = rawErrorText(e)
  const text = (raw.split('\n')[0] ?? raw).trim()
  if (/Wails 运行时未注入/.test(text)) return '当前是浏览器预览，未连接桌面内核；请用 wails dev 或在应用内打开。'
  if (/插件不存在/.test(text)) return '对应插件未注册或未激活：请在内核启动日志里确认该插件已激活。'
  if (/方法不存在/.test(text)) return '该插件尚未提供此方法，后端接口需补齐（已记入前端对接清单）。'
  if (/工具不存在/.test(text)) return '工具未注册：请确认对应插件已激活。'
  if (/风险等级被安全策略拒绝/.test(text)) return '该操作被安全策略拒绝：只读研究型产品不允许写操作或外部命令。'
  if (/context deadline|timeout|timed out|超时/i.test(text)) return scene + '超时：数据源或模型响应过慢，请稍后重试。'
  if (/dial tcp|no such host|network is unreachable|connection refused/i.test(text)) return '网络不可达：请检查网络或数据源白名单配置。'
  if (/401|403|unauthorized|invalid.*key|api key/i.test(text)) return '鉴权失败：请在模型设置里更新 API Key 后重试。'
  if (/Guard|CheckPath|拒绝访问|permission denied/i.test(text)) return '路径访问被沙箱拒绝：文件需位于 ~/.guanlan 内。'
  return scene + '失败：' + (text.length > 180 ? text.slice(0, 180) + '…' : text)
}
