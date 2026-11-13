// 设置域新增接口：模型官方预设（§3.8）与设置中心 inventory/policy（§3.9）。
// 后端 Go 结构体可能沿用默认 json 名（KeyURL/DocsURL/ContextWindow…），
// 解析统一用多候选键名，避免大小写差异把面板打成空白。

import { friendlyError, invoke } from '../api'
import type { AuditRow, MethodView, PluginView, Policy, StatusView, ToolView, UISlot } from '../types'
import { arr, pickArr, pickBool, pickNum, pickStr, rec, strList } from './parse'

async function guard<T>(scene: string, fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (e) {
    throw new Error(friendlyError(e, scene))
  }
}

// ---------- 模型预设 ----------

export interface ModelPreset {
  model: string
  label: string
  /** 上下文窗口（token） */
  contextWindow: number
  /** 单次最大输出（token） */
  maxOutput: number
  /** 官方建议的 maxTokens 默认值 */
  defaultMaxTokens: number
  note?: string
}

export interface ProviderPreset {
  provider: string
  label: string
  baseUrl: string
  keyURL?: string
  docsURL?: string
  models: ModelPreset[]
  note?: string
}

function toModelPreset(v: unknown): ModelPreset {
  const r = rec(v) ?? {}
  const model = pickStr(r, 'model', 'Model', 'name', 'id') ?? ''
  const maxOutput = pickNum(r, 'maxOutput', 'MaxOutput', 'maxOutputTokens') ?? 0
  return {
    model,
    label: pickStr(r, 'label', 'Label', 'displayName') ?? model,
    contextWindow: pickNum(r, 'contextWindow', 'ContextWindow', 'window') ?? 0,
    maxOutput,
    defaultMaxTokens: pickNum(r, 'defaultMaxTokens', 'DefaultMaxTokens', 'maxTokens') ?? Math.min(maxOutput || 8192, 8192),
    note: pickStr(r, 'note', 'Note'),
  }
}

function toProviderPreset(v: unknown): ProviderPreset {
  const r = rec(v) ?? {}
  const provider = pickStr(r, 'provider', 'Provider', 'id') ?? ''
  const models = pickArr(r, 'models', 'Models').map(toModelPreset).filter((m) => m.model !== '')
  return {
    provider,
    label: pickStr(r, 'label', 'Label', 'name') ?? provider,
    baseUrl: pickStr(r, 'baseUrl', 'BaseURL', 'baseURL', 'BaseUrl') ?? '',
    keyURL: pickStr(r, 'keyURL', 'KeyURL', 'keyUrl', 'key_url'),
    docsURL: pickStr(r, 'docsURL', 'DocsURL', 'docsUrl', 'docs_url'),
    models,
    note: pickStr(r, 'note', 'Note'),
  }
}

export function modelPresets(): Promise<ProviderPreset[]> {
  return guard('读取模型预设', async () => {
    const v = await invoke('modelsettings', 'presets', {})
    const r = rec(v)
    const raw = r ? pickArr(r, 'providers', 'Providers') : arr(v)
    return raw.map(toProviderPreset).filter((p) => p.provider !== '')
  })
}

// ---------- 设置中心 ----------

export interface ConsoleInventory {
  plugins: PluginView[]
  status: StatusView | null
  tools: ToolView[]
  disabled: string[]
  restartRequired: boolean
  /** 最近审计事件（inventory 自带，不需要额外的 console.audit 方法） */
  audit: AuditRow[]
  /** 受保护插件：开关置灰（目前只有 console 自己） */
  protected: string[]
}

export interface SetEnabledResult {
  ok: boolean
  restartRequired: boolean
  message: string
  /** 保存后的完整停用清单，直接覆盖本地状态 */
  disabled: string[]
}

function toAuditRow(v: unknown): AuditRow {
  const r = rec(v) ?? {}
  return {
    time: pickStr(r, 'time') ?? '',
    plugin: pickStr(r, 'plugin') ?? '',
    kind: pickStr(r, 'kind') ?? '',
    action: pickStr(r, 'action') ?? '',
    ok: pickBool(r, 'ok') === true,
    risk: pickStr(r, 'risk'),
    elapsedMs: pickNum(r, 'elapsedMs'),
    detail: pickStr(r, 'detail'),
  }
}

function toToolView(v: unknown): ToolView {
  const r = rec(v) ?? {}
  return {
    name: pickStr(r, 'name') ?? '',
    title: pickStr(r, 'title') ?? '',
    summary: pickStr(r, 'summary') ?? '',
    tags: strList(pickArr(r, 'tags')),
    risk: pickStr(r, 'risk') ?? 'read',
  }
}

function toMethodView(v: unknown): MethodView {
  const r = rec(v) ?? {}
  return { name: pickStr(r, 'name') ?? '', description: pickStr(r, 'description') }
}

function toUISlot(v: unknown): UISlot {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id') ?? '',
    title: pickStr(r, 'title') ?? '',
    icon: pickStr(r, 'icon'),
    group: pickStr(r, 'group'),
    component: pickStr(r, 'component') ?? '',
    order: pickNum(r, 'order') ?? 100,
    owner: pickStr(r, 'owner'),
  }
}

function toPluginView(v: unknown): PluginView {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id', 'ID') ?? '',
    name: pickStr(r, 'name', 'Name') ?? '',
    version: pickStr(r, 'version', 'Version') ?? '',
    description: pickStr(r, 'description', 'Description') ?? '',
    author: pickStr(r, 'author', 'Author'),
    dependsOn: strList(pickArr(r, 'dependsOn', 'DependsOn')),
    tags: strList(pickArr(r, 'tags', 'Tags')),
    active: pickBool(r, 'active', 'Active') === true,
    skipped: pickBool(r, 'skipped', 'Skipped') === true,
    error: pickStr(r, 'error', 'Error'),
    tools: pickArr(r, 'tools', 'Tools').map(toToolView),
    ui: pickArr(r, 'ui', 'UI').map(toUISlot),
    methods: pickArr(r, 'methods', 'Methods').map(toMethodView),
  }
}

export function toPolicy(v: unknown): Policy {
  const r = rec(v) ?? {}
  return {
    networkAllowlist: strList(pickArr(r, 'networkAllowlist', 'NetworkAllowlist')),
    pathRoots: strList(pickArr(r, 'pathRoots', 'PathRoots')),
    allowExec: pickBool(r, 'allowExec', 'AllowExec') === true,
    maxReqPerMin: pickNum(r, 'maxReqPerMin', 'MaxReqPerMin') ?? 0,
    maxToolCalls: pickNum(r, 'maxToolCalls', 'MaxToolCalls') ?? 0,
    maxTokensPerRun: pickNum(r, 'maxTokensPerRun', 'MaxTokensPerRun') ?? 0,
  }
}

function toStatusView(v: unknown): StatusView | null {
  const r = rec(v)
  if (!r) return null
  const health = rec(r.dataHealth)
  const dataHealth: Record<string, string> = {}
  if (health) {
    for (const k of Object.keys(health)) {
      const val = health[k]
      if (typeof val === 'string') dataHealth[k] = val
    }
  }
  return {
    started: pickBool(r, 'started') === true,
    plugins: pickNum(r, 'plugins') ?? 0,
    active: pickNum(r, 'active') ?? 0,
    tools: pickNum(r, 'tools') ?? 0,
    ui: pickNum(r, 'ui') ?? 0,
    services: strList(pickArr(r, 'services')),
    policy: toPolicy(r.policy),
    dataHealth,
    time: pickStr(r, 'time') ?? '',
  }
}

export function consoleInventory(): Promise<ConsoleInventory> {
  return guard('读取设置中心清单', async () => {
    const r = rec(await invoke('console', 'inventory', {})) ?? {}
    const plugins = pickArr(r, 'plugins', 'Plugins').map(toPluginView).filter((p) => p.id !== '')
    const tools = pickArr(r, 'tools', 'Tools').map(toToolView).filter((t) => t.name !== '')
    return {
      plugins,
      status: toStatusView(r.status ?? r.Status),
      // 工具清单缺失时用插件内嵌工具兜底，面板照样能搜索
      tools: tools.length > 0 ? tools : plugins.flatMap((p) => p.tools),
      disabled: strList(pickArr(r, 'disabled', 'Disabled')),
      restartRequired: pickBool(r, 'restartRequired', 'RestartRequired') === true,
      audit: pickArr(r, 'audit', 'Audit').map(toAuditRow).filter((a) => a.plugin !== '' || a.action !== ''),
      protected: strList(pickArr(r, 'protected', 'Protected')),
    }
  })
}

export function consoleSetEnabled(id: string, enabled: boolean): Promise<SetEnabledResult> {
  return guard('保存插件启用状态', async () => {
    const r = rec(await invoke('console', 'setEnabled', { id, enabled })) ?? {}
    return {
      ok: r.ok !== false,
      restartRequired: pickBool(r, 'restartRequired', 'RestartRequired') !== false,
      message: pickStr(r, 'message') ?? '已保存，重启应用后生效',
      disabled: strList(pickArr(r, 'disabled', 'Disabled')),
    }
  })
}

export function consolePolicy(): Promise<Policy> {
  return guard('读取安全策略', async () => toPolicy(await invoke('console', 'policy', {})))
}

/** 官方金融数据 API 的配置状态（不含明文 Key）。 */
export interface FuyaoStatus {
  enabled: boolean
  hasKey: boolean
  keyMasked: string
  keyOrigin: string
  baseUrl: string
  health: string
}

/** 官方 API 保存入参：apiKey 空串表示保持原 Key 不变。 */
export interface FuyaoDraft {
  apiKey: string
  baseUrl: string
  enabled: boolean
  clearKey?: boolean
}

/** 读取官方金融数据 API 配置状态。 */
export function consoleFuyao(): Promise<FuyaoStatus> {
  return guard('读取官方数据源配置', async () => {
    const r = rec(await invoke('console', 'fuyao', {})) ?? {}
    return {
      enabled: r.enabled === true,
      hasKey: r.hasKey === true,
      keyMasked: pickStr(r, 'keyMasked') ?? '',
      keyOrigin: pickStr(r, 'keyOrigin') ?? '',
      baseUrl: pickStr(r, 'baseUrl') ?? '',
      health: pickStr(r, 'health') ?? '',
    }
  })
}

/** 保存官方金融数据 API 配置（Key 只写不读）。 */
export function consoleFuyaoSave(draft: FuyaoDraft): Promise<FuyaoStatus> {
  return guard('保存官方数据源配置', async () => {
    const r = rec(await invoke('console', 'fuyaoSave', draft)) ?? {}
    return {
      enabled: r.enabled === true,
      hasKey: r.hasKey === true,
      keyMasked: pickStr(r, 'keyMasked') ?? '',
      keyOrigin: pickStr(r, 'keyOrigin') ?? '',
      baseUrl: pickStr(r, 'baseUrl') ?? '',
      health: pickStr(r, 'health') ?? '',
    }
  })
}

/** 用真实快照请求验证官方 API Key。 */
export function consoleFuyaoTest(): Promise<{ ok: boolean; message: string }> {
  return guard('测试官方数据源', async () => {
    const r = rec(await invoke('console', 'fuyaoTest', {})) ?? {}
    return { ok: r.ok === true, message: pickStr(r, 'message') ?? '' }
  })
}
