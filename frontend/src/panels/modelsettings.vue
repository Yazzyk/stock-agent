<script setup lang="ts">
// 模型设置：预设驱动的新增/编辑向导。
// 顺序：provider 卡片 → 模型下拉（带 context / 最大输出，自动填 baseUrl 与 maxTokens）
// → API Key → 高级折叠里才允许改 baseUrl；密钥只写不读。
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { shortTokens } from '../lib/api.agent'
import { modelPresets, type ModelPreset, type ProviderPreset } from '../lib/api.settings'
import {
  messageOf,
  modelList,
  modelRemove,
  modelSave,
  modelSetDefault,
  modelTest,
  workbuddyImport,
  workbuddyLoginPoll,
  workbuddyLoginStart,
  workbuddyLogout,
  workbuddyModels,
  workbuddyStatus,
  type WorkBuddyModel,
  type WorkBuddyStatus,
} from '../lib/data'
import { refreshDefaultModel, toast } from '../stores/app'
import type { ModelDraft, ModelInfo, ModelTestResult } from '../types'

const providers = ref<ModelInfo[]>([])
const defaultId = ref('')
const presets = ref<ProviderPreset[]>([])
const presetsFallback = ref(false)
const loading = ref(true)
const error = ref('')
const saving = ref(false)
const testing = ref(false)
const removing = ref('')
/** 正在切换默认的模型 id（防止重复点击）。 */
const settingDefault = ref('')
const testResult = ref<ModelTestResult | null>(null)
const advanced = ref(false)
const customModel = ref(false)
/** WorkBuddy 本机登录态与模型目录（token 不在前端，只有账号名/网关/到期时间）。 */
const wbStatus = ref<WorkBuddyStatus | null>(null)
const wbCatalog = ref<WorkBuddyModel[]>([])
const wbMessage = ref('')
const wbBusy = ref('')
const wbLoginId = ref('')
const wbPolling = ref(false)
/** 当前表单的 maxTokens 是否来自已保存配置（仅用于 maxOutput 未知时的兜底保护） */
let maxTokensFromSaved = false

const FORM_DEFAULT_MAX_TOKENS = 8192
/** maxTokens 硬上限：官方预设里最大的单次输出是 384K（DeepSeek），留到 1M 兜住未来模型。 */
const MAX_TOKENS_CAP = 1000000

const form = reactive({
  id: '',
  provider: 'deepseek',
  name: '',
  baseUrl: '',
  model: '',
  apiKey: '',
  maxTokens: FORM_DEFAULT_MAX_TOKENS,
  contextWindow: 0,
})

/** 后端 presets 不可用时的兜底（与 §3.8 表格一致），只用于渲染，不写库。 */
const FALLBACK_PRESETS: ProviderPreset[] = [
  {
    provider: 'deepseek',
    label: 'DeepSeek',
    baseUrl: 'https://api.deepseek.com',
    keyURL: 'https://platform.deepseek.com/api_keys',
    docsURL: 'https://platform.deepseek.com/api-docs/',
    models: [
      { model: 'deepseek-flash', label: 'DeepSeek Flash', contextWindow: 1000000, maxOutput: 384000, defaultMaxTokens: 8192 },
      { model: 'deepseek-v4-pro', label: 'DeepSeek V4 Pro', contextWindow: 1000000, maxOutput: 384000, defaultMaxTokens: 8192 },
      { model: 'deepseek-chat', label: 'DeepSeek Chat', contextWindow: 128000, maxOutput: 8192, defaultMaxTokens: 4096, note: 'legacy 旧版名' },
    ],
  },
  {
    provider: 'openai',
    label: 'OpenAI',
    baseUrl: 'https://api.openai.com/v1',
    keyURL: 'https://platform.openai.com/api-keys',
    docsURL: 'https://platform.openai.com/docs/models',
    models: [
      { model: 'gpt-5', label: 'GPT-5', contextWindow: 400000, maxOutput: 128000, defaultMaxTokens: 16384 },
      { model: 'gpt-5-mini', label: 'GPT-5 Mini', contextWindow: 400000, maxOutput: 128000, defaultMaxTokens: 16384 },
    ],
  },
  {
    provider: 'qwen',
    label: '通义千问',
    baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
    keyURL: 'https://bailian.console.aliyun.com/?apiKey=1',
    docsURL: 'https://help.aliyun.com/zh/model-studio/models',
    models: [
      { model: 'qwen3-max', label: 'Qwen3 Max', contextWindow: 256000, maxOutput: 32000, defaultMaxTokens: 8192 },
      { model: 'qwen-plus', label: 'Qwen Plus', contextWindow: 128000, maxOutput: 16000, defaultMaxTokens: 4096 },
      { model: 'qwen-turbo', label: 'Qwen Turbo', contextWindow: 1000000, maxOutput: 16000, defaultMaxTokens: 4096 },
    ],
  },
  {
    provider: 'ark',
    label: '火山方舟',
    baseUrl: 'https://ark.cn-beijing.volces.com/api/v3',
    keyURL: 'https://console.volcengine.com/ark',
    docsURL: 'https://www.volcengine.com/docs/82379',
    models: [
      { model: 'doubao-seed-2-1-pro', label: 'Doubao Seed 2.1 Pro', contextWindow: 1000000, maxOutput: 256000, defaultMaxTokens: 8192 },
      { model: 'doubao-seed-2-1-turbo', label: 'Doubao Seed 2.1 Turbo', contextWindow: 256000, maxOutput: 256000, defaultMaxTokens: 8192 },
    ],
  },
  {
    provider: 'ollama',
    label: 'Ollama 本地',
    baseUrl: 'http://127.0.0.1:11434/v1',
    docsURL: 'https://ollama.com/library',
    models: [
      { model: 'qwen3:8b', label: 'Qwen3 8B', contextWindow: 32000, maxOutput: 8000, defaultMaxTokens: 2048 },
      { model: 'llama3.1:8b', label: 'Llama 3.1 8B', contextWindow: 128000, maxOutput: 8000, defaultMaxTokens: 2048 },
    ],
  },
  {
    provider: 'opencode',
    label: 'OpenCode 兼容网关',
    baseUrl: 'https://opencode.ai/inference/openai/v1',
    keyURL: 'https://opencode.ai/console',
    docsURL: 'https://opencode.ai/docs/',
    note: 'OpenAI Chat Completions 兼容网关，需要填 OpenCode 控制台签发的 API Key；模型 ID 按 OpenCode 目录（2026-11），具体以控制台为准',
    models: [
      { model: 'qwen3.8-max', label: 'Qwen3.8 Max', contextWindow: 204800, maxOutput: 32768, defaultMaxTokens: 8192 },
      { model: 'deepseek-v4.1-flash', label: 'DeepSeek V4.1 Flash', contextWindow: 204800, maxOutput: 32768, defaultMaxTokens: 8192 },
      { model: 'deepseek-v4-pro', label: 'DeepSeek V4 Pro', contextWindow: 204800, maxOutput: 32768, defaultMaxTokens: 8192 },
      { model: 'kimi-k2.5', label: 'Kimi K2.5', contextWindow: 204800, maxOutput: 32768, defaultMaxTokens: 8192 },
      { model: 'glm-5.3', label: 'GLM 5.3', contextWindow: 204800, maxOutput: 32768, defaultMaxTokens: 8192 },
      { model: 'minimax-m3', label: 'MiniMax M3', contextWindow: 204800, maxOutput: 32768, defaultMaxTokens: 8192 },
    ],
  },
  {
    provider: 'workbuddy',
    label: 'WorkBuddy（本机登录）',
    baseUrl: 'https://www.workbuddy.cn/v2',
    docsURL: 'https://www.workbuddy.cn',
    models: [],
    note: '无需 API Key：使用本机 WorkBuddy 登录态（国内 workbuddy.cn / 国际 workbuddy.ai），模型从账号的模型目录里选',
  },
  {
    provider: 'openai-compat',
    label: 'OpenAI 兼容',
    baseUrl: '',
    models: [],
    note: '无预设，手填 BaseURL 与模型名',
  },
]

const editing = computed(() => form.id !== '')
const activePreset = computed<ProviderPreset | null>(() => presetOf(form.provider))
const activeModelPreset = computed<ModelPreset | null>(() => presetModel(form.provider, form.model))
/**
 * Max Tokens 是否锁定为官方上限（只读）：
 * 命中预设、官方给出了单次输出上限（maxOutput > 0），且未切到「自定义模型」时为 true。
 * 作为 computed 派生，切换 provider/模型、加载已保存配置、presets 晚到时都会自动重算，不会残留旧状态。
 */
const maxTokensAuto = computed(() => !customModel.value && (activeModelPreset.value?.maxOutput ?? 0) > 0)
const isWorkBuddy = computed(() => form.provider === 'workbuddy')
const wbLoggedIn = computed(() => wbStatus.value?.loggedIn === true)

/** WorkBuddy 模型目录映射成预设形状，复用窗口/输出上限/默认 maxTokens 的既有逻辑。 */
const wbModelPresets = computed<ModelPreset[]>(() =>
  wbCatalog.value
    .filter((m) => !m.isImageModel)
    .map((m) => ({
      model: m.id,
      label: m.name || m.id,
      contextWindow: m.contextWindow,
      maxOutput: m.maxOutputTokens,
      defaultMaxTokens: Math.min(m.maxOutputTokens > 0 ? m.maxOutputTokens : 8192, 8192),
      note: [m.creditHint, m.description].filter((v): v is string => !!v).join(' · '),
    })),
)

/** 模型下拉的数据源：WorkBuddy 用账号目录，其他 provider 用内置预设。 */
const activeModels = computed<ModelPreset[]>(() =>
  isWorkBuddy.value ? wbModelPresets.value : (activePreset.value?.models ?? []),
)

function presetOf(provider: string): ProviderPreset | null {
  return presets.value.find((p) => p.provider === provider) ?? null
}

function presetModel(provider: string, model: string): ModelPreset | null {
  if (provider === 'workbuddy') return wbModelPresets.value.find((m) => m.model === model) ?? null
  return presetOf(provider)?.models.find((m) => m.model === model) ?? null
}

function isLegacy(m: ModelPreset | null): boolean {
  if (!m) return false
  const note = (m.note ?? '').toLowerCase()
  return note.includes('legacy') || note.includes('旧版')
}

/** 列表卡片里的上下文窗口：后端回填优先，否则按预设推断。 */
function contextOf(m: ModelInfo): number {
  if (typeof m.contextWindow === 'number' && m.contextWindow > 0) return m.contextWindow
  return presetModel(m.provider, m.model)?.contextWindow ?? 0
}

function maxOutputOf(m: ModelInfo): number {
  return presetModel(m.provider, m.model)?.maxOutput ?? 0
}

function modelLegacy(m: ModelInfo): boolean {
  return isLegacy(presetModel(m.provider, m.model))
}

/** 输入框上限：有预设就按该模型的单次输出上限，没有就放到硬上限。 */
const maxTokensCeil = computed(() => {
  const maxOutput = activeModelPreset.value?.maxOutput ?? 0
  if (maxOutput <= 0) return MAX_TOKENS_CAP
  return Math.min(Math.max(1, maxOutput), MAX_TOKENS_CAP)
})

const formError = computed(() => {
  if (isWorkBuddy.value && !wbLoggedIn.value) return '请先登录 WorkBuddy（或从 dsh/go-stock 导入登录态）'
  if (form.model.trim() === '') return '先选择或填写模型名'
  if (form.baseUrl.trim() === '') return 'BaseURL 不能为空（可在高级里填写你的自建端点）'
  // 官方模型只读：值由预设决定，不对用户输入做范围校验；自定义模型才校验 1..上限
  if (!maxTokensAuto.value) {
    if (!Number.isFinite(form.maxTokens) || form.maxTokens < 1) return 'maxTokens 至少为 1'
    if (form.maxTokens > maxTokensCeil.value) {
      return 'maxTokens 不能超过当前模型单次输出上限 ' + maxTokensCeil.value + '（可换模型或调小）'
    }
  }
  return ''
})

const maxTokensHint = computed(() => {
  if (maxTokensAuto.value) {
    const official = activeModelPreset.value?.maxOutput ?? 0
    const tail = editing.value ? '；保存后会按官方上限写入' : ''
    return '官方上限 ' + shortTokens(official) + '（只读）；需要自定义请选『自定义模型』' + tail
  }
  if (customModel.value) return '自定义模型：可填 1 ~ ' + maxTokensCeil.value
  const m = activeModelPreset.value
  if (!m || m.maxOutput <= 0) return '官方默认值未知；可用 1 ~ ' + MAX_TOKENS_CAP
  return '官方默认 ' + m.defaultMaxTokens + ' / 上限 ' + shortTokens(m.maxOutput)
})

const keyURL = computed(() => activePreset.value?.keyURL ?? '')
const docsURL = computed(() => activePreset.value?.docsURL ?? '')

function applyList(res: { providers: ModelInfo[]; defaultId: string }): void {
  providers.value = res.providers
  defaultId.value = res.defaultId || (res.providers.find((p) => p.isDefault)?.id ?? '')
  // 默认只能有一个：以 defaultId 为准重排，避免旧的 isDefault 残留
  for (const p of providers.value) p.isDefault = p.id !== '' && p.id === defaultId.value
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    applyList(await modelList())
    const def = providers.value.find((p) => p.isDefault) ?? providers.value[0]
    if (def && form.id === '') loadIntoForm(def)
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadPresets(): Promise<void> {
  let list: ProviderPreset[] = FALLBACK_PRESETS
  let fallback = true
  try {
    const remote = await modelPresets()
    if (remote.length > 0) {
      list = remote
      fallback = false
    }
  } catch {
    // 后端未实现 presets：用内置兜底，页脚会注明
  }
  presets.value = list
  presetsFallback.value = fallback
  if (form.model === '') {
    // 新配置：按 provider 预填首个模型
    fillFromProvider(form.provider)
    return
  }
  // 已有配置：presets 可能在 load() 之后才到，这里把下拉/窗口/BaseURL 补齐
  syncWithPresets()
}

/**
 * presets 晚到时回填已有配置（修 race：load() 先跑会让 presetModel 查空 → customModel 误判为 true）。
 * 官方模型（maxOutput > 0）的 maxTokens 一律以官方上限为准（只读，保存后按该值写入）；
 * 其余情况只补缺失值：用户已经填过的 maxTokens / BaseURL 不覆盖。
 */
function syncWithPresets(): void {
  if (form.model === '') return
  const preset = presetModel(form.provider, form.model)
  // 命中预设 → 切回下拉；未命中（自定义模型名）→ 保留手填
  customModel.value = preset === null
  if (!preset) return
  if (form.contextWindow <= 0) form.contextWindow = preset.contextWindow
  if (preset.maxOutput > 0) {
    form.maxTokens = preset.maxOutput
    maxTokensFromSaved = false
  } else {
    // 官方未给输出上限：只在「没有已保存值」且为空 / 仍是通用占位默认值时补官方默认
    const emptyOrDefault =
      !Number.isFinite(form.maxTokens) || form.maxTokens <= 0 || form.maxTokens === FORM_DEFAULT_MAX_TOKENS
    if (!maxTokensFromSaved && emptyOrDefault) form.maxTokens = preset.defaultMaxTokens
  }
  const provider = activePreset.value
  if (form.baseUrl.trim() === '' && provider && provider.baseUrl !== '') form.baseUrl = provider.baseUrl
}

/** 按 provider 预设回填 baseUrl + 首个模型；openai-compat 交给手填。 */
function fillFromProvider(provider: string): void {
  const p = presetOf(provider)
  form.provider = provider
  form.baseUrl = p?.baseUrl ?? ''
  if (provider === 'workbuddy') {
    // WorkBuddy 的模型来自账号目录：登录后自动选第一个，未登录时留空等用户登录。
    const first = wbModelPresets.value[0]
    if (first) {
      applyModelPreset(first)
      customModel.value = false
    } else {
      form.model = ''
      form.contextWindow = 0
      form.maxTokens = FORM_DEFAULT_MAX_TOKENS
      maxTokensFromSaved = false
      customModel.value = false
    }
    return
  }
  const preset = p?.models[0]
  if (preset) {
    applyModelPreset(preset)
    customModel.value = false
  } else {
    form.model = ''
    form.contextWindow = 0
    form.maxTokens = FORM_DEFAULT_MAX_TOKENS
    maxTokensFromSaved = false
    customModel.value = true
  }
}

/** 官方预设对应的 Max Tokens：有单次输出上限就用上限，否则回落到预设默认值。 */
function officialMaxTokens(m: ModelPreset): number {
  return m.maxOutput > 0 ? m.maxOutput : m.defaultMaxTokens
}

/** 选中预设模型：回填模型名、上下文窗口、Max Tokens 与 BaseURL，并退出自定义模型态。 */
function applyModelPreset(m: ModelPreset): void {
  form.model = m.model
  form.contextWindow = m.contextWindow
  form.maxTokens = officialMaxTokens(m)
  maxTokensFromSaved = false
  customModel.value = false
  const p = activePreset.value
  if (p && p.baseUrl !== '') form.baseUrl = p.baseUrl
}

function pickProvider(provider: string): void {
  fillFromProvider(provider)
  testResult.value = null
}

function onModelChange(event: Event): void {
  const model = (event.target as HTMLSelectElement).value
  if (model === '__custom__') {
    customModel.value = true
    form.model = ''
    form.contextWindow = 0
    form.maxTokens = FORM_DEFAULT_MAX_TOKENS
    maxTokensFromSaved = false
    return
  }
  const m = presetModel(form.provider, model)
  if (m) {
    applyModelPreset(m)
    return
  }
  // 预设里没有这个名字（旧模型被移除等）：转手填，别让下拉显示成空白
  form.model = model
  form.contextWindow = 0
  customModel.value = true
}

function loadIntoForm(m: ModelInfo): void {
  form.id = m.id ?? ''
  form.provider = m.provider
  form.name = m.displayName ?? m.name ?? ''
  form.baseUrl = m.baseUrl ?? presetOf(m.provider)?.baseUrl ?? ''
  form.model = m.model
  form.apiKey = ''
  const preset = presetModel(m.provider, m.model)
  form.contextWindow = m.contextWindow ?? preset?.contextWindow ?? 0
  maxTokensFromSaved = typeof m.maxTokens === 'number' && m.maxTokens > 0
  if (preset && preset.maxOutput > 0) {
    // 官方模型：以官方单次输出上限为准（只读），已保存的旧 maxTokens 不参与显示
    form.maxTokens = preset.maxOutput
    maxTokensFromSaved = false
  } else {
    form.maxTokens = maxTokensFromSaved ? (m.maxTokens as number) : (preset?.defaultMaxTokens ?? FORM_DEFAULT_MAX_TOKENS)
  }
  customModel.value = preset === null
  testResult.value = null
  advanced.value = false
}

function newModel(): void {
  form.id = ''
  form.provider = 'deepseek'
  form.name = ''
  form.apiKey = ''
  form.model = ''
  form.maxTokens = FORM_DEFAULT_MAX_TOKENS
  form.contextWindow = 0
  maxTokensFromSaved = false
  testResult.value = null
  advanced.value = false
  customModel.value = false
  fillFromProvider('deepseek')
}

function draft(setDefault = false): ModelDraft {
  const d: ModelDraft = {
    provider: form.provider.trim(),
    model: form.model.trim(),
    setDefault,
  }
  if (form.id) d.id = form.id
  if (form.name.trim()) d.name = form.name.trim()
  if (form.baseUrl.trim()) d.baseUrl = form.baseUrl.trim()
  if (form.apiKey.trim()) d.apiKey = form.apiKey.trim()
  if (typeof form.maxTokens === 'number' && form.maxTokens > 0) d.maxTokens = form.maxTokens
  if (form.provider === 'workbuddy') d.authMode = 'workbuddy'
  else if (form.provider === 'opencode') d.authMode = 'opencode'
  return d
}

async function save(setDefault = false): Promise<ModelInfo | null> {
  if (formError.value) {
    toast(formError.value, 'warn')
    return null
  }
  saving.value = true
  error.value = ''
  try {
    applyList(await modelSave(draft(setDefault)))
    form.apiKey = ''
    const saved =
      providers.value.find((p) => p.id === form.id) ??
      providers.value.find((p) => p.provider === form.provider && p.model === form.model) ??
      null
    if (saved) {
      form.id = saved.id ?? form.id
      form.name = saved.displayName ?? form.name
      form.contextWindow = contextOf(saved)
    }
    toast(setDefault ? '已保存并设为默认模型' : '模型配置已保存', 'ok')
    return saved
  } catch (e) {
    toast(messageOf(e), 'error')
    return null
  } finally {
    saving.value = false
  }
}

async function test(): Promise<void> {
  let id = form.id
  if (!id) {
    const saved = await save(false)
    id = saved?.id ?? ''
    if (!id) return
  }
  testing.value = true
  testResult.value = null
  try {
    const res = await modelTest(id)
    testResult.value = res
    toast(res.ok ? '连通正常（' + res.latencyMs + 'ms）' : '连通失败：' + res.message, res.ok ? 'ok' : 'error')
  } catch (e) {
    testResult.value = { ok: false, latencyMs: 0, message: messageOf(e) }
    toast(messageOf(e), 'error')
  } finally {
    testing.value = false
  }
}

/** 把已保存的模型直接设为全局默认：不重填 Key，也不改这条配置的其他字段。 */
async function setAsDefault(m: ModelInfo): Promise<void> {
  const id = m.id ?? ''
  if (!id || settingDefault.value !== '') return
  settingDefault.value = id
  try {
    applyList(await modelSetDefault(id))
    await refreshDefaultModel()
    toast('已将 ' + providerLabel(m.provider) + ' / ' + m.model + ' 设为默认模型', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    settingDefault.value = ''
  }
}

async function remove(m: ModelInfo): Promise<void> {
  const id = m.id ?? ''
  if (!id) return
  if (removing.value !== id) {
    removing.value = id
    window.setTimeout(() => {
      if (removing.value === id) removing.value = ''
    }, 4000)
    return
  }
  removing.value = ''
  try {
    applyList(await modelRemove(id))
    if (form.id === id) newModel()
    toast('已删除配置', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

function providerLabel(p: string): string {
  return presetOf(p)?.label ?? (p === 'openai-compat' ? 'OpenAI 兼容' : p)
}

/** 列表卡片上的鉴权提示：WorkBuddy 显示本机登录态，其余显示 API Key 配置情况。 */
function keyHint(m: ModelInfo): string {
  if (m.authMode === 'workbuddy') return m.hasKey ? '本机登录态可用' : '未登录 WorkBuddy'
  return m.hasKey ? '已配置 key' : '未配置 key'
}

function openLink(url: string, e: MouseEvent): void {
  if (url === '') return
  // 桌面端交给系统浏览器；浏览器预览时退化为新标签页
  const rt = window.runtime as { BrowserOpenURL?: (u: string) => void } | undefined
  if (rt && typeof rt.BrowserOpenURL === 'function') {
    e.preventDefault()
    rt.BrowserOpenURL(url)
  }
}

/** 用系统浏览器打开地址；浏览器预览时退化为新标签页。 */
function openExternal(url: string): void {
  if (url === '') return
  const rt = window.runtime as { BrowserOpenURL?: (u: string) => void } | undefined
  if (rt && typeof rt.BrowserOpenURL === 'function') {
    rt.BrowserOpenURL(url)
    return
  }
  window.open(url, '_blank', 'noreferrer')
}

/** 简易延时，用于登录轮询。 */
function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms))
}

/** 读取 WorkBuddy 登录态；已登录时顺带刷新模型目录。 */
async function loadWorkBuddy(): Promise<void> {
  try {
    wbStatus.value = await workbuddyStatus()
  } catch (e) {
    wbStatus.value = { loggedIn: false, message: messageOf(e) }
  }
  if (wbStatus.value.message && !wbStatus.value.loggedIn) wbMessage.value = wbStatus.value.message
  if (wbStatus.value.loggedIn) await loadWorkBuddyModels()
  else wbCatalog.value = []
  if (form.provider === 'workbuddy') syncWithPresets()
}

/** 拉取 WorkBuddy 账号的模型目录；首次登录后自动选中第一个可用模型。 */
async function loadWorkBuddyModels(): Promise<void> {
  try {
    wbCatalog.value = await workbuddyModels()
    if (wbCatalog.value.length === 0) wbMessage.value = '模型目录为空：请确认该账号已开通对话模型'
  } catch (e) {
    wbCatalog.value = []
    wbMessage.value = messageOf(e)
  }
  if (form.provider !== 'workbuddy') return
  if (form.model === '' && wbModelPresets.value.length > 0) {
    applyModelPreset(wbModelPresets.value[0])
    customModel.value = false
    return
  }
  syncWithPresets()
}

/** 登录/导入/退出后统一刷新：模型列表（卡片状态）+ WorkBuddy 登录态与目录。 */
async function refreshAfterWorkBuddy(): Promise<void> {
  try {
    applyList(await modelList())
  } catch {
    // 模型列表刷新失败不打断登录结果，卡片下次刷新会补上
  }
  await loadWorkBuddy()
}

/** 发起 WorkBuddy 浏览器登录，并在后台轮询登录结果。 */
async function wbLogin(region: 'cn' | 'global'): Promise<void> {
  if (wbBusy.value !== '') return
  wbBusy.value = 'login'
  wbMessage.value = ''
  try {
    const session = await workbuddyLoginStart(region)
    if (!session.loginId) throw new Error('登录服务未返回会话 ID，请稍后重试')
    wbLoginId.value = session.loginId
    openExternal(session.url)
    await pollWorkBuddyLogin()
  } catch (e) {
    wbMessage.value = messageOf(e)
    toast(wbMessage.value, 'error')
  } finally {
    wbBusy.value = ''
  }
}

/** 轮询登录结果：ok 表示凭证已落盘；message 非空且未成功表示登录失败，停止等待。 */
async function pollWorkBuddyLogin(): Promise<void> {
  wbPolling.value = true
  try {
    for (let i = 0; i < 150 && wbPolling.value; i++) {
      await sleep(2000)
      const r = await workbuddyLoginPoll(wbLoginId.value)
      if (r.ok) {
        wbLoginId.value = ''
        wbMessage.value = ''
        toast('WorkBuddy 登录成功' + (r.accountName ? '：' + r.accountName : ''), 'ok')
        await refreshAfterWorkBuddy()
        return
      }
      if (r.message) {
        wbLoginId.value = ''
        wbMessage.value = r.message
        toast(r.message, 'error')
        return
      }
    }
    if (wbPolling.value) {
      wbMessage.value = '登录等待超时，请重新发起'
      toast(wbMessage.value, 'warn')
    }
  } catch (e) {
    wbMessage.value = messageOf(e)
    toast(wbMessage.value, 'error')
  } finally {
    wbPolling.value = false
  }
}

/** 从 dsh / go-stock 的凭证库一次性导入 WorkBuddy 登录态。 */
async function importWorkBuddy(): Promise<void> {
  if (wbBusy.value !== '') return
  wbBusy.value = 'import'
  try {
    const r = await workbuddyImport()
    wbMessage.value = r.message
    toast(r.message || '已导入 WorkBuddy 登录态', 'ok')
    await refreshAfterWorkBuddy()
  } catch (e) {
    wbMessage.value = messageOf(e)
    toast(wbMessage.value, 'error')
  } finally {
    wbBusy.value = ''
  }
}

/** 退出 WorkBuddy 登录（只清本应用自己的凭证，不影响 dsh / go-stock）。 */
async function logoutWorkBuddy(): Promise<void> {
  if (wbBusy.value !== '') return
  wbBusy.value = 'logout'
  try {
    const r = await workbuddyLogout()
    wbCatalog.value = []
    if (form.provider === 'workbuddy') {
      form.model = ''
      form.contextWindow = 0
    }
    toast(r.message || '已退出 WorkBuddy', 'ok')
    await refreshAfterWorkBuddy()
  } catch (e) {
    wbMessage.value = messageOf(e)
    toast(wbMessage.value, 'error')
  } finally {
    wbBusy.value = ''
  }
}

/** 手动刷新 WorkBuddy 模型目录。 */
async function reloadWorkBuddyModels(): Promise<void> {
  if (wbBusy.value !== '') return
  wbBusy.value = 'models'
  try {
    await loadWorkBuddyModels()
    if (wbLoggedIn.value) toast('WorkBuddy 模型目录已刷新', 'ok')
  } finally {
    wbBusy.value = ''
  }
}

onMounted(async () => {
  await Promise.all([load(), loadPresets(), loadWorkBuddy()])
})

onUnmounted(() => {
  wbPolling.value = false
})
</script>

<template>
  <section class="card ms">
    <div class="card-hd">
      <span class="card-title">模型设置</span>
      <span class="card-sub">按官方预设一键填好窗口与输出上限；密钥只写不读</span>
      <span class="spacer" />
      <span class="card-tools">
        <button class="btn btn-sm" type="button" @click="load">刷新</button>
        <button class="btn btn-sm btn-accent" type="button" @click="newModel">新增配置</button>
      </span>
    </div>

    <div class="ms-body">
      <div class="ms-list">
        <StateBlock v-if="loading" kind="loading" title="读取模型配置…" />
        <StateBlock v-else-if="error" kind="error" title="读取失败" :message="error" action-text="重试" @action="load" />
        <StateBlock
          v-else-if="providers.length === 0"
          kind="empty"
          title="还没有配置模型"
          message="至少配置一个模型才能使用 Agent 值班与对话。右侧选一个 provider，填上 API Key 即可。"
        />
        <template v-else>
          <button
            v-for="m in providers"
            :key="m.id || m.provider + m.model"
            class="ms-item"
            :class="{ on: form.id === m.id }"
            type="button"
            @click="m.id && loadIntoForm(m)"
          >
            <div class="row" style="gap: 6px">
              <span class="ms-prov">{{ providerLabel(m.provider) }}</span>
              <span v-if="m.isDefault || m.id === defaultId" class="tag tag-accent">默认</span>
              <span v-if="modelLegacy(m)" class="tag">旧版</span>
              <span v-if="m.authMode === 'workbuddy'" class="tag">WorkBuddy 登录</span>
              <span v-else-if="m.authMode === 'opencode'" class="tag">OpenCode</span>
              <span class="spacer" />
              <span class="dot" :class="m.hasKey ? 'dot-ok' : 'dot-err'" :title="keyHint(m)" />
            </div>
            <div class="ms-model num">{{ m.model }}</div>
            <div class="tiny muted ellipsis">{{ m.baseUrl || '使用 Provider 默认端点' }}</div>
            <div v-if="m.accountName" class="tiny muted ellipsis">
              账号 {{ m.accountName }}<span v-if="m.expiresAt"> · 到期 {{ m.expiresAt }}</span>
            </div>
            <div class="row tiny muted" style="gap: 8px; margin-top: 4px">
              <span v-if="contextOf(m) > 0">上下文 {{ shortTokens(contextOf(m)) }}</span>
              <span v-else class="hint-warn">上下文未知</span>
              <span v-if="maxOutputOf(m) > 0">· 输出上限 {{ shortTokens(maxOutputOf(m)) }}</span>
            </div>
            <div class="row" style="gap: 6px; margin-top: 6px">
              <span class="tiny" :class="m.hasKey ? 'muted' : 'hint-warn'">{{ keyHint(m) }}</span>
              <span class="spacer" />
              <span
                v-if="m.id && m.id !== defaultId"
                class="btn btn-sm btn-ghost"
                role="button"
                tabindex="0"
                :aria-busy="settingDefault === m.id"
                @click.stop="setAsDefault(m)"
              >
                {{ settingDefault === m.id ? '设置中…' : '设为默认' }}
              </span>
              <span class="btn btn-sm btn-ghost" role="button" tabindex="0" @click.stop="m.id && loadIntoForm(m)">编辑</span>
              <span class="btn btn-sm btn-ghost" :class="{ 'btn-danger': removing === m.id }" role="button" tabindex="0" @click.stop="remove(m)">
                {{ removing === m.id ? '确认删除' : '删除' }}
              </span>
            </div>
          </button>
        </template>
        <div v-if="presetsFallback" class="tiny muted ms-fb">
          预设来源：前端内置兜底（后端 presets 未就绪）。数值仍可手工覆盖。
        </div>
      </div>

      <div class="ms-form">
        <div class="ms-form-hd">
          <span class="card-title">{{ editing ? '编辑配置' : '新增配置' }}</span>
          <span v-if="form.contextWindow > 0" class="tag tag-info">窗口 {{ shortTokens(form.contextWindow) }}</span>
          <span class="spacer" />
          <span v-if="testResult" class="tag" :class="testResult.ok ? 'tag-down' : 'tag-up'">
            {{ testResult.ok ? '连通 ' + testResult.latencyMs + 'ms' : '连通失败' }}
          </span>
        </div>

        <!-- 步骤 1：provider -->
        <section class="ms-step">
          <div class="ms-step-hd"><span class="ms-step-no">1</span>选择 Provider</div>
          <div class="ms-prov-grid">
            <button
              v-for="p in presets"
              :key="p.provider"
              class="ms-prov-card"
              :class="{ on: form.provider === p.provider }"
              type="button"
              @click="pickProvider(p.provider)"
            >
              <span class="ms-prov-name">{{ p.label }}</span>
              <span v-if="p.provider === 'workbuddy'" class="ms-prov-url ellipsis">
                {{ wbStatus?.loggedIn ? '已登录 · ' + (wbStatus.accountName || wbStatus.domain || 'WorkBuddy') : '未登录 · 需本机登录' }}
              </span>
              <span v-else class="ms-prov-url ellipsis">{{ p.baseUrl || '自定义端点' }}</span>
            </button>
          </div>
          <div v-if="activePreset?.note" class="tiny muted">{{ activePreset.note }}</div>
        </section>

        <!-- 步骤 2：模型 -->
        <section class="ms-step">
          <div class="ms-step-hd"><span class="ms-step-no">2</span>选择模型</div>
          <div class="ms-grid">
            <label class="field ms-span">
              <span class="label">
                模型
                <span v-if="isLegacy(activeModelPreset)" class="tag" style="margin-left: 6px">旧版</span>
              </span>
              <select v-if="!customModel && activeModels.length > 0" class="select" :value="form.model" @change="onModelChange">
                <option v-for="m in activeModels" :key="m.model" :value="m.model">
                  {{ m.model }} · 上下文 {{ shortTokens(m.contextWindow) }} / 输出 {{ shortTokens(m.maxOutput) }}{{ isLegacy(m) ? '（旧版）' : '' }}
                </option>
                <option value="__custom__">自定义模型名…</option>
              </select>
              <input v-else v-model="form.model" class="input mono" placeholder="模型名，例如 deepseek-flash" spellcheck="false" />
              <span v-if="isWorkBuddy && wbModelPresets.length === 0" class="tiny muted">
                模型来自 WorkBuddy 账号目录：登录或导入后会自动加载。
              </span>
              <span v-else-if="activeModels.length > 0" class="tiny muted">
                <button class="link-btn" type="button" @click="customModel = !customModel">
                  {{ customModel ? '从预设里选择' : '手填模型名' }}
                </button>
              </span>
            </label>

            <label class="field">
              <span class="label">Max Tokens（单次输出上限）</span>
              <input
                v-model.number="form.maxTokens"
                class="input num"
                :class="{ 'ms-ro': maxTokensAuto }"
                type="number"
                min="1"
                :max="maxTokensCeil"
                step="1"
                :readonly="maxTokensAuto"
                :title="maxTokensAuto ? '官方模型固定使用官方最大输出；需要自定义请选「自定义模型」' : ''"
              />
              <span class="tiny muted">{{ maxTokensHint }}</span>
            </label>

            <label class="field">
              <span class="label">显示名（可选）</span>
              <input v-model="form.name" class="input" placeholder="例如 主模型" spellcheck="false" />
            </label>
          </div>
        </section>

        <!-- 步骤 3：密钥（WorkBuddy 用本机登录态，不显示 Key 输入） -->
        <section v-if="!isWorkBuddy" class="ms-step">
          <div class="ms-step-hd"><span class="ms-step-no">3</span>API Key</div>
          <label class="field">
            <span class="label">
              Key
              <span class="tiny muted" style="margin-left: 6px">{{ editing ? '已保存则留空表示不修改' : '仅写入本地，不回显' }}</span>
            </span>
            <input
              v-model="form.apiKey"
              class="input mono"
              type="password"
              autocomplete="off"
              placeholder="sk-…（留空不修改）"
              spellcheck="false"
            />
          </label>
          <div class="row" style="gap: 12px">
            <a v-if="keyURL" class="link" :href="keyURL" target="_blank" rel="noreferrer" @click="openLink(keyURL, $event)">去申请 API Key</a>
            <a v-if="docsURL" class="link" :href="docsURL" target="_blank" rel="noreferrer" @click="openLink(docsURL, $event)">官方文档</a>
            <span v-if="!keyURL" class="tiny muted">该 provider 无需 Key（本地模型）</span>
          </div>
          <div v-if="form.provider === 'opencode'" class="tiny muted">
            OpenCode 是 OpenAI Chat Completions 兼容网关：Key 从 OpenCode 控制台签发，模型名按目录选择。
          </div>
        </section>

        <!-- 步骤 3（WorkBuddy）：本机登录态 -->
        <section v-if="isWorkBuddy" class="ms-step">
          <div class="ms-step-hd"><span class="ms-step-no">3</span>WorkBuddy 登录态</div>
          <div class="row tiny muted" style="gap: 10px; flex-wrap: wrap">
            <span class="dot" :class="wbLoggedIn ? 'dot-ok' : 'dot-err'" />
            <span>{{ wbLoggedIn ? '已登录' : '未登录' }}</span>
            <span v-if="wbStatus?.accountName">账号 {{ wbStatus.accountName }}</span>
            <span v-if="wbStatus?.domain">网关 {{ wbStatus.domain }}</span>
            <span v-if="wbStatus?.expiresAt">到期 {{ wbStatus.expiresAt }}</span>
          </div>
          <div class="tiny muted">
            凭证保存在本机 {{ wbStatus?.vaultPath || 'workbuddy-vault' }}（0600）；token 只在后端拼请求头，不回传页面、不填 API Key。
          </div>
          <div class="row" style="gap: 8px; flex-wrap: wrap">
            <button class="btn btn-sm btn-accent" type="button" :disabled="wbBusy !== '' || wbPolling" @click="wbLogin('cn')">
              登录 WorkBuddy（国内）
            </button>
            <button class="btn btn-sm" type="button" :disabled="wbBusy !== '' || wbPolling" @click="wbLogin('global')">国际版</button>
            <button class="btn btn-sm" type="button" :disabled="wbBusy !== '' || wbPolling" @click="importWorkBuddy">从 dsh/go-stock 导入</button>
            <button class="btn btn-sm" type="button" :disabled="wbBusy !== '' || !wbLoggedIn" @click="reloadWorkBuddyModels">刷新模型目录</button>
            <button class="btn btn-sm" type="button" :disabled="wbBusy !== '' || !wbLoggedIn" @click="logoutWorkBuddy">退出登录</button>
          </div>
          <div v-if="wbPolling" class="tiny muted">已打开浏览器授权页，等待你完成登录（成功后这里会自动刷新模型目录）。</div>
          <div v-if="wbMessage" class="tiny" :class="wbLoggedIn ? 'muted' : 'hint-warn'">{{ wbMessage }}</div>
          <div v-if="!wbLoggedIn" class="tiny muted">
            没有 WorkBuddy 账号？可以改用 OpenCode 兼容网关，填 API Key 即可。
          </div>
        </section>

        <!-- 步骤 4：高级 -->
        <section class="ms-step">
          <button class="ms-adv" type="button" @click="advanced = !advanced">
            {{ advanced ? '收起高级设置' : '高级设置（BaseURL 等）' }}
          </button>
          <div v-if="advanced" class="ms-grid" style="margin-top: 8px">
            <label class="field ms-span">
              <span class="label">BaseURL</span>
              <input v-model="form.baseUrl" class="input mono" placeholder="https://api.example.com/v1" spellcheck="false" />
              <span v-if="isWorkBuddy" class="tiny muted">
                留空即可：WorkBuddy 网关按登录凭证的 domain 自动选国内/国际；只有自建代理时才填。
              </span>
              <span v-else class="tiny muted">默认 {{ activePreset?.baseUrl || '自定义' }}；自建网关、代理时改这里。</span>
            </label>
          </div>
        </section>

        <div v-if="formError" class="hint-warn">{{ formError }}</div>
        <div v-if="testResult && !testResult.ok" class="err-box">{{ testResult.message }}</div>
        <div v-if="testResult && testResult.ok" class="ok-box">{{ testResult.message }}</div>

        <div class="ms-actions">
          <button class="btn btn-accent" type="button" :disabled="saving || formError !== ''" @click="save(false)">
            {{ saving ? '保存中…' : '保存' }}
          </button>
          <button class="btn" type="button" :disabled="testing || saving || formError !== ''" @click="test">
            {{ testing ? '测试中…' : '测试连通' }}
          </button>
          <button class="btn" type="button" :disabled="saving || formError !== ''" @click="save(true)">保存并设为默认</button>
          <span class="spacer" />
          <span class="tiny muted">{{ presetsFallback ? '预设：内置兜底' : '预设：后端官方数据' }}</span>
        </div>

        <div class="ms-note">
          <div class="tiny muted">安全约束</div>
          <ul>
            <li>密钥只写不读：页面永远拿不到明文，只能看到「已配置 / 未配置」。</li>
            <li v-if="isWorkBuddy">WorkBuddy 使用本机登录态：token 不落日志、不回传前端，凭证文件权限 0600。</li>
            <li>上下文窗口与输出上限来自官方预设；官方模型的 Max Tokens 固定为该模型的官方单次输出上限（只读），自定义模型可手填。</li>
            <li>模型调用经 Guard 白名单与限速；本产品只做研究，不提供任何下单或交易接口。</li>
          </ul>
        </div>
      </div>
    </div>
  </section>
</template>


<style scoped>
  .ms {
    min-height: 0;
  }
  .ms .card-hd {
    flex-wrap: wrap;
    row-gap: 4px;
    padding-top: 7px;
    padding-bottom: 7px;
  }
  .ms-body {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: 320px 1fr;
    gap: 0;
  }
  .ms-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px;
    border-right: 1px solid var(--line);
    overflow-y: auto;
  }
  .ms-fb {
    margin-top: auto;
    padding-top: 8px;
    border-top: 1px dashed var(--line);
    line-height: 1.6;
  }
  .ms-item {
    display: block;
    width: 100%;
    padding: 9px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
    color: var(--text-2);
    text-align: left;
  }
  .ms-item:hover {
    border-color: var(--line-3);
    background: var(--panel-3);
  }
  .ms-item.on {
    border-color: rgba(232, 179, 75, 0.5);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .ms-prov {
    color: var(--text);
    font-size: 12.5px;
    font-weight: 600;
  }
  .ms-model {
    margin-top: 2px;
    color: var(--text-2);
    font-size: 12px;
  }
  .ms-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px 14px;
    overflow-y: auto;
  }
  .ms-form-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ms-step {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background: rgba(255, 255, 255, 0.012);
  }
  .ms-step-hd {
    display: flex;
    align-items: center;
    gap: 7px;
    color: var(--text-2);
    font-size: 12px;
    font-weight: 600;
  }
  .ms-step-no {
    display: inline-grid;
    place-items: center;
    width: 17px;
    height: 17px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-2);
    font: 600 10.5px/1 var(--mono);
  }
  .ms-prov-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 7px;
  }
  .ms-prov-card {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 7px 9px;
    border: 1px solid var(--line-2);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
    text-align: left;
    min-width: 0;
  }
  .ms-prov-card:hover {
    border-color: var(--line-3);
    background: var(--panel-3);
  }
  .ms-prov-card.on {
    border-color: rgba(233, 180, 76, 0.5);
    background: var(--accent-soft);
  }
  .ms-prov-name {
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
  }
  .ms-prov-url {
    color: var(--muted);
    font: 10px/1.4 var(--mono);
  }
  .ms-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 14px;
  }
  .ms-span {
    grid-column: 1 / -1;
  }
  .ms-ro {
    color: var(--muted);
    background: rgba(255, 255, 255, 0.02);
    cursor: default;
  }
  .ms-adv {
    align-self: flex-start;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--muted);
    font-size: 11.5px;
  }
  .ms-adv:hover {
    color: var(--text-2);
  }
  .ms-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    padding-top: 4px;
  }
  .link-btn {
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--info);
    font-size: 11px;
  }
  .link-btn:hover {
    text-decoration: underline;
  }
  .err-box {
    padding: 7px 10px;
    border: 1px solid rgba(255, 93, 93, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(255, 93, 93, 0.08);
    color: #ffb4b4;
    font-size: 12px;
  }
  .ok-box {
    padding: 7px 10px;
    border: 1px solid rgba(47, 191, 135, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(47, 191, 135, 0.08);
    color: #8ae0bb;
    font-size: 12px;
  }
  .ms-note {
    margin-top: auto;
    padding-top: 10px;
    border-top: 1px solid var(--line);
  }
  .ms-note ul {
    margin: 4px 0 0;
    padding-left: 16px;
    color: var(--muted);
    font-size: 11px;
    line-height: 1.8;
  }
</style>
