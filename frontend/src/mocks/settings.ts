// 设置域演示数据扩展（frontend-agent 维护）。
// 覆盖 §3.8 的 modelsettings.presets 与 §3.9 的 console.inventory/setEnabled/policy。
// 插件清单直接借用演示桥的 Plugins()/Status()/Tools()，保证与左侧导航一致。

import { Plugins, Status, Tools } from '../api'
import { registerMock } from './registry'
import type { PluginView, StatusView } from '../types'

const PRESETS = [
  {
    provider: 'deepseek',
    label: 'DeepSeek',
    baseUrl: 'https://api.deepseek.com',
    keyUrl: 'https://platform.deepseek.com/api_keys',
    docsUrl: 'https://platform.deepseek.com/api-docs/',
    models: [
      { model: 'deepseek-flash', label: 'DeepSeek Flash', contextWindow: 1000000, maxOutput: 384000, defaultMaxTokens: 8192 },
      { model: 'deepseek-v4-pro', label: 'DeepSeek V4 Pro', contextWindow: 1000000, maxOutput: 384000, defaultMaxTokens: 8192 },
      { model: 'deepseek-chat', label: 'DeepSeek Chat', contextWindow: 128000, maxOutput: 8192, defaultMaxTokens: 4096, note: 'legacy：旧模型名，保留兼容' },
    ],
  },
  {
    provider: 'openai',
    label: 'OpenAI',
    baseUrl: 'https://api.openai.com/v1',
    keyUrl: 'https://platform.openai.com/api-keys',
    docsUrl: 'https://platform.openai.com/docs/models',
    models: [
      { model: 'gpt-5', label: 'GPT-5', contextWindow: 400000, maxOutput: 128000, defaultMaxTokens: 16384 },
      { model: 'gpt-5-mini', label: 'GPT-5 Mini', contextWindow: 400000, maxOutput: 128000, defaultMaxTokens: 16384 },
    ],
  },
  {
    provider: 'qwen',
    label: '通义千问',
    baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
    keyUrl: 'https://bailian.console.aliyun.com/?apiKey=1',
    docsUrl: 'https://help.aliyun.com/zh/model-studio/models',
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
    keyUrl: 'https://console.volcengine.com/ark',
    docsUrl: 'https://www.volcengine.com/docs/82379',
    models: [
      { model: 'doubao-seed-2-1-pro', label: 'Doubao Seed 2.1 Pro', contextWindow: 1000000, maxOutput: 256000, defaultMaxTokens: 8192 },
      { model: 'doubao-seed-2-1-turbo', label: 'Doubao Seed 2.1 Turbo', contextWindow: 256000, maxOutput: 256000, defaultMaxTokens: 8192 },
    ],
  },
  {
    provider: 'ollama',
    label: 'Ollama 本地',
    baseUrl: 'http://127.0.0.1:11434/v1',
    docsUrl: 'https://ollama.com/library',
    models: [
      { model: 'qwen3:8b', label: 'Qwen3 8B', contextWindow: 32000, maxOutput: 8000, defaultMaxTokens: 2048 },
      { model: 'llama3.1:8b', label: 'Llama 3.1 8B', contextWindow: 128000, maxOutput: 8000, defaultMaxTokens: 2048 },
    ],
  },
  {
    provider: 'opencode',
    label: 'OpenCode 兼容网关',
    baseUrl: 'https://opencode.ai/inference/openai/v1',
    keyUrl: 'https://opencode.ai/console',
    docsUrl: 'https://opencode.ai/docs/',
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
    docsUrl: 'https://www.workbuddy.cn',
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

/** 受保护插件：设置中心自己不能停用（与后端一致）。 */
const PROTECTED = ['console']

/** 演示审计：时间/插件/动作/耗时/结果，失败行前端会高亮。 */
const AUDIT = [
  { time: new Date(Date.now() - 60 * 1000).toISOString(), plugin: 'agentlab', kind: 'tool', action: 'market.quote', ok: true, risk: 'network', elapsedMs: 214, detail: '12 只标的' },
  { time: new Date(Date.now() - 95 * 1000).toISOString(), plugin: 'watchlist', kind: 'method', action: 'sync', ok: true, elapsedMs: 88, detail: '同步 12 条' },
  { time: new Date(Date.now() - 140 * 1000).toISOString(), plugin: 'knowledge', kind: 'tool', action: 'kb.search', ok: true, risk: 'read', elapsedMs: 12, detail: 'q=均线 命中 5' },
  { time: new Date(Date.now() - 260 * 1000).toISOString(), plugin: 'modelsettings', kind: 'method', action: 'test', ok: false, elapsedMs: 8021, detail: 'openai-compat 未配置密钥' },
  { time: new Date(Date.now() - 320 * 1000).toISOString(), plugin: 'console', kind: 'method', action: 'setEnabled', ok: true, risk: 'write', elapsedMs: 3, detail: 'watch 停用' },
  { time: new Date(Date.now() - 400 * 1000).toISOString(), plugin: 'fundflow', kind: 'tool', action: 'flow.rank', ok: true, risk: 'network', elapsedMs: 176, detail: 'sector 50 行' },
]

/** 演示中「已停用」的插件 id（setEnabled 改写它，inventory 读它）。 */
const disabled = new Set<string>()
let restartRequired = false

function decorate(list: PluginView[]): PluginView[] {
  return list.map((p) => {
    if (!disabled.has(p.id)) return p
    return {
      ...p,
      active: false,
      skipped: true,
      error: '已停用（可在设置中心启用，重启生效）',
    }
  })
}

async function snapshot(): Promise<{ plugins: PluginView[]; status: StatusView | null; tools: ReturnType<typeof flattenTools> }> {
  const [list, st, tools] = await Promise.all([Plugins(), Status(), Tools()])
  return { plugins: decorate(Array.isArray(list) ? list : []), status: st ?? null, tools: flattenTools(tools) }
}

function flattenTools(list: unknown): Array<Record<string, unknown>> {
  if (!Array.isArray(list)) return []
  return list.filter((t): t is Record<string, unknown> => typeof t === 'object' && t !== null)
}

registerMock('modelsettings', 'presets', () => ({ providers: PRESETS }))

/** 演示模式没有真实登录态：只展示未登录状态，避免面板空白。 */
registerMock('modelsettings', 'workbuddyStatus', () => ({
  loggedIn: false,
  vaultPath: '~/Library/Application Support/guanlan/workbuddy-vault',
  message: '演示模式：未连接本机 WorkBuddy 登录态',
}))

/** 演示模式模型目录为空，面板会提示先登录或导入。 */
registerMock('modelsettings', 'workbuddyModels', () => ({ models: [] }))

/** 演示模式不发起真实浏览器登录。 */
registerMock('modelsettings', 'workbuddyLogin', () => {
  throw new Error('演示模式不支持真实 WorkBuddy 登录')
})

/** 演示模式登录轮询直接返回可操作提示。 */
registerMock('modelsettings', 'workbuddyLoginPoll', () => ({ ok: false, message: '演示模式不支持真实 WorkBuddy 登录' }))

/** 演示模式不读取本机 dsh / go-stock 凭证。 */
registerMock('modelsettings', 'workbuddyImport', () => ({ message: '演示模式：不执行真实导入' }))

/** 演示模式退出登录无副作用。 */
registerMock('modelsettings', 'workbuddyLogout', () => ({ message: '演示模式：已模拟退出（未改动真实凭证）' }))

registerMock('console', 'inventory', async () => {
  const { plugins, status, tools } = await snapshot()
  const active = plugins.filter((p) => p.active).length
  const nextStatus: StatusView | null = status
    ? {
        ...status,
        plugins: plugins.length,
        active,
        tools: tools.length,
        ui: plugins.reduce((n, p) => n + (p.ui?.length ?? 0), 0),
      }
    : null
  return {
    plugins,
    status: nextStatus,
    tools,
    disabled: Array.from(disabled),
    restartRequired,
    audit: AUDIT,
    protected: PROTECTED,
  }
})

registerMock('console', 'setEnabled', (p) => {
  const id = String(p.id ?? '')
  const enabled = p.enabled === true
  if (id === '') throw new Error('缺少插件 ID')
  if (enabled) disabled.delete(id)
  else disabled.add(id)
  restartRequired = true
  return {
    ok: true,
    restartRequired: true,
    message: enabled ? '已启用，重启应用后生效' : '已停用，重启应用后生效',
    disabled: Array.from(disabled),
  }
})

registerMock('console', 'policy', async () => {
  const st = await Status()
  return st?.policy ?? {
    networkAllowlist: [],
    pathRoots: ['~/.guanlan'],
    allowExec: false,
    maxReqPerMin: 60,
    maxToolCalls: 24,
    maxTokensPerRun: 0,
  }
})

export function installSettingsMocks(): void {
  // 注册在模块加载时完成；这里只做一次幂等的演示状态复位。
  disabled.clear()
  restartRequired = false
}