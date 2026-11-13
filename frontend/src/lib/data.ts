// 数据访问层：只调用 docs/API.md 里冻结的插件方法/工具，并做形状归一。
// 面板不直接拼 Invoke 字符串，改契约只改这一处。

import { callTool, friendlyError, invoke } from '../api'
import type {
  AgentDiagnostic,
  AgentRunResult,
  AgentSession,
  AgentSessionDetail,
  AgentSessionEvent,
  AgentStatus,
  AgentSteerResult,
  DocListPayload,
  DocSummary,
  FlowKind,
  FundFlow,
  ImportResult,
  KlineAnalyzeResponse,
  OptimizeResponse,
  OptimizeResult,
  OptimizeCandidate,
  KbHit,
  KbSearchPayload,
  KlineResponse,
  KnowledgeDoc,
  ModelDraft,
  ModelInfo,
  ModelListPayload,
  ModelTestResult,
  Period,
  Quote,
  QuantRunResponse,
  Sector,
  SkillContent,
  StrategySpec,
  WalkForwardResponse,
  WatchListPayload,
  WatchRule,
  WatchRunLog,
  WatchTemplatesPayload,
  WatchTestResult,
  SkillInfo,
  StockRef,
  SyncResult,
  WorkerInfo,
} from '../types'
import {
  arr,
  pickArr,
  pickBool,
  pickNum,
  pickStr,
  rec,
  toAgentSession,
  toBar,
  toDocSummary,
  toFundFlow,
  toKbHit,
  toKlineAnalyze,
  toKnowledgeDoc,
  toMaSeries,
  toModelInfo,
  toNumList,
  toQuantFills,
  toQuantResult,
  toStrategySpec,
  toWalkForward,
  toWatchList,
  toWatchLog,
  toWatchMetric,
  toWatchRule,
  toWatchTest,
  toQuote,
  toSector,
  toSkill,
  toStockRef,
  toWatchSnapshot,
  toWatchItem,
  toWorker,
  strList,
} from './parse'

/** 统一把异常换成中文可操作提示；面板只展示 message。 */
async function guard<T>(scene: string, fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (e) {
    throw new Error(friendlyError(e, scene))
  }
}

export function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : friendlyError(e)
}

// ---------- 自选 ----------

export function watchList(): Promise<import('../types').WatchSnapshot> {
  return guard('读取自选', async () => toWatchSnapshot(await invoke('watchlist', 'list', {})))
}

export function watchGroups(): Promise<string[]> {
  return guard('读取分组', async () => {
    const r = rec(await invoke('watchlist', 'groups', {}))
    return r ? strList(pickArr(r, 'groups')) : []
  })
}

export function watchSync(): Promise<SyncResult> {
  return guard('同步同花顺自选', async () => {
    const r = rec(await invoke('watchlist', 'sync', {})) ?? {}
    const itemCount = pickNum(r, 'added') ?? 0
    return {
      success: r.success === true,
      message: pickStr(r, 'message') ?? (itemCount > 0 ? '同步完成' : '同步结束，无新增'),
      groups: strList(pickArr(r, 'groups')),
      added: pickNum(r, 'added') ?? 0,
      removed: pickNum(r, 'removed') ?? 0,
      needWebLogin: r.needWebLogin === true,
      needFullDiskAccess: r.needFullDiskAccess === true,
      warnings: strList(pickArr(r, 'warnings')),
      source: pickStr(r, 'source'),
    }
  })
}

export function watchAdd(code: string, name?: string, group?: string): Promise<import('../types').WatchSnapshot> {
  return guard('加入自选', async () => toWatchSnapshot(await invoke('watchlist', 'add', { code, name, group })))
}

export function watchRemove(code: string): Promise<import('../types').WatchSnapshot> {
  return guard('移出自选', async () => toWatchSnapshot(await invoke('watchlist', 'remove', { code })))
}

// ---------- 行情 / 搜索 ----------

export function quotes(codes: string[]): Promise<Quote[]> {
  if (codes.length === 0) return Promise.resolve([])
  return guard('读取行情', async () => arr(await callTool('market.quote', { codes })).map(toQuote))
}

export function searchStocks(keyword: string, limit = 10): Promise<StockRef[]> {
  const q = keyword.trim()
  if (!q) return Promise.resolve([])
  return guard('搜索标的', async () => arr(await callTool('market.search', { keyword: q, limit })).map(toStockRef))
}

// ---------- 资金流 ----------

export function flowStock(code: string): Promise<FundFlow> {
  return guard('查询个股资金流', async () => toFundFlow(await invoke('fundflow', 'stock', { code })))
}

/** 资金流排名方向：in=主力净流入榜，out=主力净流出榜，all=按净额降序（兼容旧行为）。 */
export type FlowRankDirection = 'in' | 'out' | 'all'

export function flowRank(kind: FlowKind, limit = 50, direction: FlowRankDirection = 'all'): Promise<FundFlow[]> {
  return guard('查询资金流排名', async () =>
    arr(await invoke('fundflow', 'rank', { kind, limit, direction })).map(toFundFlow),
  )
}

export function sectors(kind: FlowKind): Promise<Sector[]> {
  return guard('查询板块列表', async () => arr(await invoke('fundflow', 'sectors', { kind })).map(toSector))
}

// ---------- K 线 ----------

export async function klineBars(code: string, period: Period, limit = 320, adjust = 'qfq'): Promise<KlineResponse> {
  return guard('读取 K 线', async () => {
    const r = rec(await invoke('kline', 'bars', { code, period, limit, adjust })) ?? {}
    const bars = pickArr(r, 'bars').map(toBar).filter((b) => b.date !== '')
    if (bars.length === 0) throw new Error('该标的暂无 K 线数据（可能停牌或代码有误）')
    const ind = rec(r.indicators) ?? {}
    return {
      code: pickStr(r, 'code') ?? code,
      name: pickStr(r, 'name') ?? '',
      period: pickStr(r, 'period') ?? period,
      bars,
      indicators: {
        ma5: toMaSeries(ind.ma5, bars.length),
        ma10: toMaSeries(ind.ma10, bars.length),
        ma20: toMaSeries(ind.ma20, bars.length),
        ma60: toMaSeries(ind.ma60, bars.length),
        volume: toMaSeries(ind.volume, bars.length),
      },
    }
  })
}

/** K 线判读：趋势/均线/RSI/MACD/ATR + 形态 + 支撑阻力。 */
export function klineAnalyze(code: string, period: Period): Promise<KlineAnalyzeResponse> {
  return guard('读取 K 线判读', async () => {
    const parsed = toKlineAnalyze(await invoke('kline', 'analyze', { code, period }))
    if (parsed.code === '') parsed.code = code
    return parsed
  })
}

// ---------- 模型设置 ----------

function toModelList(v: unknown): ModelListPayload {
  const r = rec(v) ?? {}
  return {
    providers: pickArr(r, 'providers').map(toModelInfo),
    defaultId: pickStr(r, 'defaultId') ?? '',
  }
}

export function modelList(): Promise<ModelListPayload> {
  return guard('读取模型配置', async () => toModelList(await invoke('modelsettings', 'list', {})))
}

export function modelSave(draft: ModelDraft): Promise<ModelListPayload> {
  const payload: Record<string, unknown> = {
    provider: draft.provider,
    model: draft.model,
  }
  if (draft.id) payload.id = draft.id
  if (draft.name) payload.name = draft.name
  if (draft.baseUrl) payload.baseUrl = draft.baseUrl
  if (draft.apiKey) payload.apiKey = draft.apiKey
  if (typeof draft.maxTokens === 'number' && draft.maxTokens > 0) payload.maxTokens = draft.maxTokens
  if (draft.setDefault) payload.setDefault = true
  return guard('保存模型配置', async () => toModelList(await invoke('modelsettings', 'save', payload)))
}

/** 把某条已保存的模型配置设为全局默认（不需要重新填写 Key）。 */
export function modelSetDefault(id: string): Promise<ModelListPayload> {
  return guard('设置默认模型', async () => toModelList(await invoke('modelsettings', 'setDefault', { id })))
}

export function modelTest(id: string): Promise<ModelTestResult> {
  return guard('测试模型连通', async () => {
    const r = rec(await invoke('modelsettings', 'test', { id })) ?? {}
    return {
      ok: r.ok === true,
      latencyMs: pickNum(r, 'latencyMs') ?? 0,
      message: pickStr(r, 'message') ?? (r.ok === true ? '连通正常' : '连通失败'),
    }
  })
}

export function modelRemove(id: string): Promise<ModelListPayload> {
  return guard('删除模型配置', async () => toModelList(await invoke('modelsettings', 'remove', { id })))
}

// ---------- Agent ----------

export function agentWorkers(): Promise<WorkerInfo[]> {
  // 后端返回 {workers:[...], mainId, total}；arr() 会取 workers，同时兼容旧的裸数组。
  // kind（main/preset/custom）在 toWorker 里归一。
  return guard('读取 worker 列表', async () => arr(await invoke('agentlab', 'workers', {})).map(toWorker))
}

/**
 * 诊断条目归一。
 * 后端契约：diagnostics 是 **string[]**，每条形如
 * 「检测到 reasoning_content 非空而 content 为空：该模型是推理模型，正文被推理占满」。
 * 前端仍然用 {code,message} 承载（code 留空，只用于排版时占不占一列），
 * 同时保留对早期 {code,message} 对象数组的兼容，避免旧后端把告警卡变空白。
 */
function toAgentDiagnostic(v: unknown): AgentDiagnostic {
  if (typeof v === 'string') {
    const message = v.trim()
    return { code: '', message }
  }
  const r = rec(v) ?? {}
  return { code: pickStr(r, 'code') ?? '', message: pickStr(r, 'message', 'detail', 'reason') ?? '' }
}

export function agentRun(worker: string, input: string, sessionId?: string, model?: string): Promise<AgentRunResult> {
  return guard('运行 Agent', async () => {
    const payload: Record<string, unknown> = { worker, input, sessionId }
    if (model) payload.model = model
    const r = rec(await invoke('agentlab', 'run', payload)) ?? {}
    return {
      sessionId: pickStr(r, 'sessionId') ?? '',
      worker: pickStr(r, 'worker') ?? worker,
      report: pickStr(r, 'report') ?? '',
      steps: pickNum(r, 'steps'),
      usage: rec(r.usage) ? {
        promptTokens: pickNum(rec(r.usage) ?? {}, 'promptTokens') ?? 0,
        completionTokens: pickNum(rec(r.usage) ?? {}, 'completionTokens') ?? 0,
        totalTokens: pickNum(rec(r.usage) ?? {}, 'totalTokens') ?? 0,
      } : undefined,
      error: pickStr(r, 'error'),
      model: pickStr(r, 'model') || undefined,
      // 诊断字段由后端新增：缺失时留空，前端据此判定「无诊断信息」
      finishReason: pickStr(r, 'finishReason') ?? '',
      emptyReason: pickStr(r, 'emptyReason') ?? '',
      diagnostics: pickArr(r, 'diagnostics').map(toAgentDiagnostic).filter((d) => d.message !== ''),
      reasoning: pickStr(r, 'reasoning') ?? '',
      contentFromReasoning: pickBool(r, 'contentFromReasoning') === true,
      artifacts: pickArr(r, 'artifacts')
        .map((a) => rec(a))
        .filter((a): a is Record<string, unknown> => a !== null)
        .map((a) => ({ name: pickStr(a, 'name') ?? '', path: pickStr(a, 'path') ?? '' })),
    }
  })
}

/** 运行中插话：并入同一 session 的下一步。 */
export function agentSteer(sessionId: string, text: string): Promise<AgentSteerResult> {
  return guard('发送插话', async () => {
    const r = rec(await invoke('agentlab', 'steer', { sessionId, text })) ?? {}
    const raw = r.queued
    const queued = typeof raw === 'number' ? raw : raw === true ? 1 : 0
    return { ok: r.ok === true, queued }
  })
}

export function agentStop(sessionId: string): Promise<boolean> {
  return guard('停止 Agent', async () => {
    const r = rec(await invoke('agentlab', 'stop', { sessionId })) ?? {}
    return r.ok === true
  })
}

export function agentSessions(limit = 20): Promise<AgentSession[]> {
  return guard('读取历史会话', async () => arr(await invoke('agentlab', 'sessions', { limit })).map(toAgentSession))
}

export function agentSession(sessionId: string): Promise<AgentSessionDetail> {
  return guard('读取会话详情', async () => {
    const r = rec(await invoke('agentlab', 'session', { sessionId })) ?? {}
    const session = toAgentSession(r.session)
    if (session.id === '') session.id = sessionId
    const events: AgentSessionEvent[] = pickArr(r, 'events')
      .map((e) => rec(e))
      .filter((e): e is Record<string, unknown> => e !== null)
      .map((e) => ({
        seq: pickNum(e, 'seq') ?? 0,
        time: pickStr(e, 'time') ?? '',
        kind: pickStr(e, 'kind') ?? 'event',
        role: pickStr(e, 'role'),
        content: pickStr(e, 'content') ?? '',
        toolName: pickStr(e, 'toolName'),
        ok: typeof e.ok === 'boolean' ? e.ok : undefined,
      }))
    return { session, events }
  })
}

export function agentStatusOf(raw: string): AgentStatus {
  return raw === 'ok' || raw === 'failed' || raw === 'running' ? raw : 'ok'
}

// ---------- 知识库 ----------

export function kbList(limit = 50): Promise<DocListPayload> {
  return guard('读取知识库列表', async () => {
    const r = rec(await invoke('knowledge', 'list', { limit })) ?? {}
    return {
      docs: pickArr(r, 'docs').map(toDocSummary),
      total: pickNum(r, 'total') ?? 0,
    }
  })
}

export function kbSearch(query: string, limit = 20): Promise<KbHit[]> {
  const q = query.trim()
  if (!q) return Promise.resolve([])
  return guard('知识检索', async () => {
    const r = rec(await invoke('knowledge', 'search', { query: q, limit })) ?? {}
    return (pickArr(r, 'hits').length > 0 ? pickArr(r, 'hits') : arr(await Promise.resolve(r))).map(toKbHit)
  })
}

export function kbOpen(id: string): Promise<KnowledgeDoc> {
  return guard('打开文档', async () => toKnowledgeDoc(await invoke('knowledge', 'open', { id })))
}

export function kbImport(title: string, content: string, tags: string[]): Promise<ImportResult> {
  return guard('导入文档', async () => {
    const r = rec(await invoke('knowledge', 'import', { title, content, tags })) ?? {}
    return {
      id: pickStr(r, 'id') ?? '',
      title: pickStr(r, 'title') ?? title,
      chars: pickNum(r, 'chars') ?? content.length,
    }
  })
}

export function skills(): Promise<SkillInfo[]> {
  return guard('读取技能列表', async () => {
    const r = rec(await invoke('knowledge', 'skills', {})) ?? {}
    return (pickArr(r, 'skills').length > 0 ? pickArr(r, 'skills') : arr(r)).map(toSkill)
  })
}

export function skill(name: string): Promise<SkillContent> {
  return guard('读取技能正文', async () => {
    const r = rec(await invoke('knowledge', 'skill', { name })) ?? {}
    return {
      name: pickStr(r, 'name') ?? name,
      description: pickStr(r, 'description') ?? '',
      content: pickStr(r, 'content', 'body') ?? '',
      resources: strList(pickArr(r, 'resources')),
    }
  })
}

export type { DocSummary, KbSearchPayload, ModelInfo, Quote, Sector }

// ---------- 量化研究 ----------

export interface QuantRunInput {
  code: string
  period?: string
  strategy?: string
  params?: Record<string, number>
  limit?: number
  initialCash?: number
  adjust?: string
}

export interface QuantStrategiesPayload {
  strategies: StrategySpec[]
  /** 全局成交/成本/复权/统计口径（后端 backtest.Rules()） */
  rules: string[]
}

/** 策略清单 + 全局口径；算法说明区域用它渲染，不必让面板直接拼 invoke。 */
export function quantStrategiesPayload(): Promise<QuantStrategiesPayload> {
  return guard('读取策略列表', async () => {
    const r = rec(await invoke('quant', 'strategies', {})) ?? {}
    const raw = pickArr(r, 'strategies').length > 0 ? pickArr(r, 'strategies') : arr(r)
    return {
      strategies: raw.map(toStrategySpec),
      rules: strList(pickArr(r, 'rules')),
    }
  })
}

/** 只要策略清单的兼容入口。 */
export function quantStrategies(): Promise<StrategySpec[]> {
  return quantStrategiesPayload().then((r) => r.strategies)
}

export function quantRun(input: QuantRunInput): Promise<QuantRunResponse> {
  return guard('运行回测', async () => {
    const payload: Record<string, unknown> = { code: input.code }
    if (input.period) payload.period = input.period
    if (input.strategy) payload.strategy = input.strategy
    if (input.params && Object.keys(input.params).length > 0) payload.params = input.params
    if (typeof input.limit === 'number') payload.limit = input.limit
    if (typeof input.initialCash === 'number') payload.initialCash = input.initialCash
    const r = rec(await invoke('quant', 'run', payload)) ?? {}
    return {
      code: pickStr(r, 'code') ?? input.code,
      period: pickStr(r, 'period') ?? input.period ?? 'day',
      adjust: pickStr(r, 'adjust') === 'none' ? 'none' : 'qfq',
      warning: pickStr(r, 'warning'),
      result: toQuantResult(r.result),
      report: pickStr(r, 'report') ?? '',
      trades: toQuantFills(r.trades ?? (r.result as Record<string, unknown> | undefined)?.fills),
    }
  })
}

export function quantWalkForward(code: string, strategy: string, folds = 3, limit = 500): Promise<WalkForwardResponse> {
  return guard('运行 walk-forward', async () => {
    const r = rec(await invoke('quant', 'walkforward', { code, strategy, folds, limit })) ?? {}
    return {
      result: toWalkForward(r.result),
      report: pickStr(r, 'report') ?? '',
    }
  })
}

/**
 * 量化参数优化：后端在默认网格上做样本内筛选 + 邻域稳健性，再对推荐参数跑 walk-forward。
 * 返回可直接渲染的候选表与结论，不返回净值序列（省传输）。
 */
export function quantOptimize(code: string, strategy: string, folds = 3, limit = 500): Promise<OptimizeResult> {
  return guard('运行参数优化', async () => {
    const r = rec(await invoke('quant', 'optimize', { code, strategy, folds, limit })) ?? {}
    const result = rec(r.result) ?? r
    return toOptimizeResult(result)
  })
}

/** 把 optimize 的原始返回归一成 OptimizeResult。 */
function toOptimizeResult(v: Record<string, unknown>): OptimizeResult {
  const candidates = pickArr(v, 'candidates').map((x) => {
    const c = rec(x) ?? {}
    const rawParams = rec(c.params) ?? {}
    const params: Record<string, number> = {}
    for (const k of Object.keys(rawParams)) {
      const n = pickNum(rawParams, k)
      if (n !== undefined) params[k] = n
    }
    const out: OptimizeCandidate = {
      params,
      totalReturn: pickNum(c, 'totalReturn') ?? 0,
      cagr: pickNum(c, 'cagr') ?? 0,
      maxDrawdown: pickNum(c, 'maxDrawdown') ?? 0,
      sharpe: pickNum(c, 'sharpe') ?? 0,
      tradeCount: pickNum(c, 'tradeCount') ?? 0,
      stability: pickNum(c, 'stability'),
    }
    return out
  })
  const rawBest = rec(v.best)
  const best = rawBest
    ? (candidates.find((c) => JSON.stringify(c.params) === JSON.stringify(pickNumMap(rawBest.params))) ?? candidates[0] ?? null)
    : (candidates[0] ?? null)
  const wf = rec(v.walkforward)
  return {
    strategy: pickStr(v, 'strategy') ?? '',
    code: pickStr(v, 'code') ?? '',
    start: pickStr(v, 'start') ?? '',
    end: pickStr(v, 'end') ?? '',
    candidates,
    best: best ?? (rawBest ? {
      params: pickNumMap(rawBest.params),
      totalReturn: pickNum(rawBest, 'totalReturn') ?? 0,
      cagr: pickNum(rawBest, 'cagr') ?? 0,
      maxDrawdown: pickNum(rawBest, 'maxDrawdown') ?? 0,
      sharpe: pickNum(rawBest, 'sharpe') ?? 0,
      tradeCount: pickNum(rawBest, 'tradeCount') ?? 0,
      stability: pickNum(rawBest, 'stability'),
    } : null),
    walkforward: wf ? toWalkForward(wf) : undefined,
    report: pickStr(v, 'report') ?? '',
    note: pickStr(v, 'note'),
  }
}

/** 把任意参数对象里的数值取出来（忽略字符串/空值）。 */
function pickNumMap(v: unknown): Record<string, number> {
  const r = rec(v) ?? {}
  const out: Record<string, number> = {}
  for (const k of Object.keys(r)) {
    const n = pickNum(r, k)
    if (n !== undefined) out[k] = n
  }
  return out
}

// ---------- WorkBuddy / OpenCode 接入 ----------

/** WorkBuddy 本机登录态（不含 token，只给界面展示）。 */
export interface WorkBuddyStatus {
  loggedIn: boolean
  accountName?: string
  domain?: string
  expiresAt?: string
  vaultPath?: string
  message?: string
}

/** WorkBuddy 模型目录条目（来自 /v2/enterprises/personal/models）。 */
export interface WorkBuddyModel {
  id: string
  name: string
  contextWindow: number
  maxOutputTokens: number
  creditHint?: string
  supportsImages?: boolean
  description?: string
  isImageModel?: boolean
}

export function workbuddyStatus(): Promise<WorkBuddyStatus> {
  return guard('读取 WorkBuddy 登录态', async () => {
    const r = rec(await invoke('modelsettings', 'workbuddyStatus', {})) ?? {}
    return {
      loggedIn: r.loggedIn === true,
      accountName: pickStr(r, 'accountName'),
      domain: pickStr(r, 'domain'),
      expiresAt: pickStr(r, 'expiresAt'),
      vaultPath: pickStr(r, 'vaultPath'),
      message: pickStr(r, 'message'),
    }
  })
}

export function workbuddyModels(): Promise<WorkBuddyModel[]> {
  return guard('读取 WorkBuddy 模型目录', async () => {
    const r = rec(await invoke('modelsettings', 'workbuddyModels', {})) ?? {}
    return pickArr(r, 'models').map((x) => {
      const m = rec(x) ?? {}
      return {
        id: pickStr(m, 'id') ?? '',
        name: pickStr(m, 'name', 'id') ?? '',
        contextWindow: pickNum(m, 'contextWindow') ?? 0,
        maxOutputTokens: pickNum(m, 'maxOutputTokens') ?? 0,
        creditHint: pickStr(m, 'creditHint'),
        supportsImages: m.supportsImages === true,
        description: pickStr(m, 'description'),
        isImageModel: m.isImageModel === true,
      }
    }).filter((m) => m.id !== '')
  })
}

/** 发起 WorkBuddy 扫码/浏览器登录；返回登录会话 id 与授权地址。 */
export function workbuddyLoginStart(region: 'cn' | 'global'): Promise<{ loginId: string; url: string }> {
  return guard('发起 WorkBuddy 登录', async () => {
    const r = rec(await invoke('modelsettings', 'workbuddyLogin', { region })) ?? {}
    return { loginId: pickStr(r, 'loginId') ?? '', url: pickStr(r, 'url') ?? '' }
  })
}

/** 轮询登录结果；ok=true 表示凭证已落盘。 */
export function workbuddyLoginPoll(loginId: string): Promise<{ ok: boolean; message: string; accountName?: string }> {
  return guard('等待 WorkBuddy 登录', async () => {
    const r = rec(await invoke('modelsettings', 'workbuddyLoginPoll', { loginId })) ?? {}
    return { ok: r.ok === true, message: pickStr(r, 'message') ?? '', accountName: pickStr(r, 'accountName') }
  })
}

/** 从本机 dsh / go-stock 的 WorkBuddy 凭证库导入一次。 */
export function workbuddyImport(): Promise<{ message: string }> {
  return guard('导入 WorkBuddy 登录态', async () => {
    const r = rec(await invoke('modelsettings', 'workbuddyImport', {})) ?? {}
    return { message: pickStr(r, 'message') ?? '' }
  })
}

/** 退出 WorkBuddy 登录（删除本地凭证）。 */
export function workbuddyLogout(): Promise<{ message: string }> {
  return guard('退出 WorkBuddy', async () => {
    const r = rec(await invoke('modelsettings', 'workbuddyLogout', {})) ?? {}
    return { message: pickStr(r, 'message') ?? '' }
  })
}

// ---------- Watch 哨兵（插件 id=watch，工具名 watchdog.*） ----------

export function watchdogList(): Promise<WatchListPayload> {
  return guard('读取 Watch 规则', async () => toWatchList(await invoke('watch', 'list', {})))
}

export interface WatchSaveResult {
  rule: WatchRule
  rules: WatchRule[]
}

export function watchdogSave(rule: WatchRule): Promise<WatchSaveResult> {
  const payload: Record<string, unknown> = {
    name: rule.name,
    enabled: rule.enabled,
    sources: rule.sources,
    condition: rule.condition,
    action: rule.action,
    cooldownSeconds: rule.cooldownSeconds ?? 300,
    quietWindows: rule.quietWindows ?? [],
  }
  if (rule.id) payload.id = rule.id
  if (rule.description) payload.description = rule.description
  return guard('保存 Watch 规则', async () => {
    const r = rec(await invoke('watch', 'save', payload)) ?? {}
    return {
      rule: toWatchRule(r.rule),
      rules: arr(r.rules).map(toWatchRule),
    }
  })
}

export function watchdogToggle(id: string, enabled: boolean): Promise<WatchRule> {
  return guard('切换规则开关', async () => {
    const r = rec(await invoke('watch', 'toggle', { id, enabled })) ?? {}
    return toWatchRule(r.rule)
  })
}

export function watchdogRemove(id: string): Promise<WatchRule[]> {
  return guard('删除规则', async () => {
    const r = rec(await invoke('watch', 'remove', { id })) ?? {}
    return arr(r.rules).map(toWatchRule)
  })
}

export function watchdogTest(id: string): Promise<WatchTestResult> {
  return guard('试运行规则', async () => toWatchTest(await invoke('watch', 'test', { id })))
}

export function watchdogLogs(limit = 50): Promise<WatchRunLog[]> {
  return guard('读取触发日志', async () => {
    const r = rec(await invoke('watch', 'logs', { limit })) ?? {}
    return arr(r.logs).map(toWatchLog)
  })
}

export function watchdogTemplates(): Promise<WatchTemplatesPayload> {
  return guard('读取规则模板', async () => {
    const r = rec(await invoke('watch', 'templates', {})) ?? {}
    return {
      templates: arr(r.templates).map(toWatchRule),
      metrics: arr(r.metrics).map(toWatchMetric).filter((m) => m.name !== ''),
    }
  })
}