// 全局壳层状态：插件/面板、内核健康、当前面板、Toast、跨面板联动标的。
// 用模块级 reactive 单例，不引入额外状态库。

import { computed, reactive } from 'vue'
import { initBridge, isDemoMode, Plugins, Status, invoke, friendlyError } from '../api'
import type { PluginView, StatusView, UISlot } from '../types'

export type ToastKind = 'ok' | 'warn' | 'error' | 'info'

export interface ToastItem {
  id: number
  kind: ToastKind
  text: string
}

export interface NavGroup {
  name: string
  slots: UISlot[]
}

export interface FocusSymbol {
  code: string
  name: string
}

export const state = reactive({
  ready: false,
  demo: false,
  error: '',
  plugins: [] as PluginView[],
  status: null as StatusView | null,
  slots: [] as UISlot[],
  activeId: '',
  drawer: false,
  toasts: [] as ToastItem[],
  focus: null as FocusSymbol | null,
  updatedAt: '',
  defaultModel: '',
})

let toastSeq = 0

export function toast(text: string, kind: ToastKind = 'info'): void {
  const item: ToastItem = { id: ++toastSeq, kind, text }
  state.toasts.push(item)
  window.setTimeout(() => dismissToast(item.id), 5200)
}

export function dismissToast(id: number): void {
  const idx = state.toasts.findIndex((t) => t.id === id)
  if (idx >= 0) state.toasts.splice(idx, 1)
}

/** 按 Slot.order 排序后按 group 分组；组的顺序取组内最前面板的位置。 */
export const navGroups = computed<NavGroup[]>(() => {
  const sorted = state.slots.slice().sort((a, b) => (a.order === b.order ? a.id.localeCompare(b.id) : a.order - b.order))
  const groups: NavGroup[] = []
  for (const slot of sorted) {
    const name = slot.group && slot.group.trim() !== '' ? slot.group : '面板'
    const found = groups.find((g) => g.name === name)
    if (found) found.slots.push(slot)
    else groups.push({ name, slots: [slot] })
  }
  return groups
})

export const activeSlot = computed<UISlot | null>(() => state.slots.find((s) => s.id === state.activeId) ?? null)

export const activeTitle = computed<string>(() => activeSlot.value?.title ?? '工作台')

export type Overall = 'ok' | 'warn' | 'error' | 'off'

/** 顶部状态灯：全部激活=ok，部分失败=warn，未启动=error。 */
export const overall = computed<Overall>(() => {
  const st = state.status
  if (!st) return 'off'
  if (!st.started) return 'error'
  if (st.active < st.plugins) return 'warn'
  const failed = state.plugins.some((p) => !p.active)
  return failed ? 'warn' : 'ok'
})

export const overallText = computed<string>(() => {
  switch (overall.value) {
    case 'ok':
      return '运行正常'
    case 'warn':
      return '部分插件未激活'
    case 'error':
      return '内核未启动'
    default:
      return '读取中'
  }
})

export function activate(id: string): void {
  if (!state.slots.some((s) => s.id === id)) return
  state.activeId = id
}

/** ⌘1..⌘9：按扁平顺序切面板。 */
export function activateByIndex(index: number): void {
  const sorted = state.slots.slice().sort((a, b) => (a.order === b.order ? a.id.localeCompare(b.id) : a.order - b.order))
  const target = sorted[index]
  if (target) activate(target.id)
}

/** 全局搜索选中标的：记录焦点，并尽量切到 K 线面板。 */
export function focusStock(code: string, name: string): void {
  state.focus = { code, name }
  const kline = state.slots.find((s) => s.component === 'kline')
  if (kline) activate(kline.id)
  toast('已定位 ' + code + ' ' + name, 'ok')
}

async function loadDefaultModel(): Promise<void> {
  try {
    const res = await invoke<{ providers?: unknown[]; defaultId?: string }>('modelsettings', 'list', {})
    const list = Array.isArray(res?.providers) ? res.providers : []
    const recs = list.filter((m): m is Record<string, unknown> => typeof m === 'object' && m !== null)
    const def = recs.find((m) => m.id === res?.defaultId || m.isDefault === true) ?? recs[0]
    if (def) {
      const provider = typeof def.provider === 'string' ? def.provider : ''
      const model = typeof def.model === 'string' ? def.model : ''
      state.defaultModel = provider ? provider + ' / ' + model : model
    }
  } catch {
    state.defaultModel = ''
  }
}

export async function refreshStatus(): Promise<void> {
  try {
    state.status = await Status()
    state.updatedAt = state.status.time || new Date().toISOString()
  } catch (e) {
    state.error = friendlyError(e, '读取内核状态')
  }
}

export async function boot(): Promise<void> {
  initBridge()
  state.demo = isDemoMode()
  try {
    const [plugins, status] = await Promise.all([Plugins(), Status()])
    state.plugins = Array.isArray(plugins) ? plugins : []
    state.status = status
    state.updatedAt = status?.time ?? new Date().toISOString()
    const slots: UISlot[] = []
    for (const p of state.plugins) {
      if (!p.active) continue
      for (const s of p.ui ?? []) slots.push(s)
    }
    slots.sort((a, b) => (a.order === b.order ? a.id.localeCompare(b.id) : a.order - b.order))
    state.slots = slots
    if (!state.activeId) state.activeId = slots[0]?.id ?? ''
    state.ready = true
    state.error = ''
    await loadDefaultModel()
    if (state.demo) toast('演示数据模式：未检测到桌面内核，展示的是内置样例', 'warn')
  } catch (e) {
    state.error = friendlyError(e, '初始化界面')
    state.ready = true
  }
}
