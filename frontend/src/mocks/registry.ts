// 演示数据扩展点：各面板把自己的 mock 方法注册进来，api.mock 统一分发。
// 目的：避免所有人改同一个 api.mock.ts（写冲突），演示模式依然能覆盖新面板。

export interface MockContext {
  /** 模拟网络/模型延迟。 */
  sleep(ms: number): Promise<void>
  /** 向演示内核发一条 kernel:event（topic + JSON payload）。 */
  emit(topic: string, payload: Record<string, unknown>): void
}

export type MockHandler = (
  payload: Record<string, unknown>,
  ctx: MockContext,
) => unknown | Promise<unknown>

interface Entry {
  plugin: string
  method: string
  fn: MockHandler
}

const entries = new Map<string, Entry>()
let emitter: ((topic: string, payload: Record<string, unknown>) => void) | null = null

/** 由 api.mock 在模块加载时注入，用于把 mock 事件送进 kernel:event 订阅者。 */
export function setMockEmitter(fn: (topic: string, payload: Record<string, unknown>) => void): void {
  emitter = fn
}

export function registerMock(plugin: string, method: string, fn: MockHandler): void {
  entries.set(plugin + '/' + method, { plugin, method, fn })
}

const ctx: MockContext = {
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  emit: (topic, payload) => {
    if (emitter) emitter(topic, payload)
  },
}

/** 命中返回 {hit:true,value}；未注册返回 {hit:false}，由 api.mock 走原有 switch。 */
export async function dispatchMock(
  plugin: string,
  method: string,
  payload: Record<string, unknown>,
): Promise<{ hit: boolean; value?: unknown }> {
  const entry = entries.get(plugin + '/' + method)
  if (!entry) return { hit: false }
  return { hit: true, value: await entry.fn(payload, ctx) }
}
