// 把后端返回的 unknown 归一到前端类型。字段名与 docs/API.md 一致；
// 这里只做防御性读取（缺字段给默认值），不做语义改写。

import type {
  AgentSession,
  CostBreakdown,
  EquityPoint,
  KlineAnalyzeResponse,
  ParamDef,
  QuantResult,
  QuantTrade,
  StrategySpec,
  WalkForwardFold,
  WalkForwardResult,
  Bar,
  DocSummary,
  FundFlow,
  KbHit,
  KnowledgeDoc,
  MaSeries,
  Market,
  ModelInfo,
  Quote,
  Sector,
  SkillInfo,
  StockRef,
  WatchItem,
  WatchSnapshot,
  WorkerInfo,
} from '../types'
import { inferMarket } from './format'

export function rec(v: unknown): Record<string, unknown> | null {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return null
  return v as Record<string, unknown>
}

/** 任意列表容器：数组本身，或 {items|list|data|rows|...}。 */
export function arr(v: unknown): unknown[] {
  if (Array.isArray(v)) return v
  const r = rec(v)
  if (!r) return []
  const keys = ['items', 'list', 'data', 'rows', 'bars', 'quotes', 'hits', 'sessions', 'sectors', 'docs', 'skills', 'results', 'providers', 'workers', 'events']
  for (const k of keys) {
    const c = r[k]
    if (Array.isArray(c)) return c
  }
  return []
}

export function pickStr(r: Record<string, unknown>, ...keys: string[]): string | undefined {
  for (const k of keys) {
    const v = r[k]
    if (typeof v === 'string' && v !== '') return v
    if (typeof v === 'number' && Number.isFinite(v)) return String(v)
  }
  return undefined
}

export function pickNum(r: Record<string, unknown>, ...keys: string[]): number | undefined {
  for (const k of keys) {
    const v = r[k]
    if (typeof v === 'number' && Number.isFinite(v)) return v
    if (typeof v === 'string' && v.trim() !== '') {
      const n = Number(v)
      if (Number.isFinite(n)) return n
    }
  }
  return undefined
}

export function pickBool(r: Record<string, unknown>, ...keys: string[]): boolean | undefined {
  for (const k of keys) {
    const v = r[k]
    if (typeof v === 'boolean') return v
  }
  return undefined
}

export function pickArr(r: Record<string, unknown>, ...keys: string[]): unknown[] {
  for (const k of keys) {
    const v = r[k]
    if (Array.isArray(v)) return v
  }
  return []
}

export function strList(v: unknown): string[] {
  return arr(v).filter((x): x is string => typeof x === 'string')
}

export function toQuote(v: unknown): Quote {
  const r = rec(v) ?? {}
  const code = pickStr(r, 'code', 'symbol') ?? ''
  return {
    code,
    name: pickStr(r, 'name') ?? '',
    market: (pickStr(r, 'market') as Market | undefined) ?? inferMarket(code),
    price: pickNum(r, 'price') ?? 0,
    prevClose: pickNum(r, 'prevClose') ?? 0,
    open: pickNum(r, 'open') ?? 0,
    high: pickNum(r, 'high') ?? 0,
    low: pickNum(r, 'low') ?? 0,
    change: pickNum(r, 'change') ?? 0,
    changePct: pickNum(r, 'changePct') ?? 0,
    volume: pickNum(r, 'volume') ?? 0,
    amount: pickNum(r, 'amount') ?? 0,
    turnover: pickNum(r, 'turnover') ?? 0,
    amplitude: pickNum(r, 'amplitude') ?? 0,
    time: pickStr(r, 'time') ?? '',
    source: pickStr(r, 'source') ?? '',
    stale: pickBool(r, 'stale') ?? false,
  }
}

export function toWatchItem(v: unknown): WatchItem {
  const r = rec(v) ?? {}
  const code = pickStr(r, 'code') ?? ''
  return {
    code,
    name: pickStr(r, 'name') ?? '',
    market: (pickStr(r, 'market') as Market | undefined) ?? inferMarket(code),
    group: pickStr(r, 'group') ?? '自选股',
    order: pickNum(r, 'order') ?? 0,
    source: pickStr(r, 'source'),
    price: pickNum(r, 'price'),
    prevClose: pickNum(r, 'prevClose'),
    changePct: pickNum(r, 'changePct'),
    amount: pickNum(r, 'amount'),
    turnover: pickNum(r, 'turnover'),
    stale: pickBool(r, 'stale'),
  }
}

export function toBar(v: unknown): Bar {
  const r = rec(v) ?? {}
  return {
    date: pickStr(r, 'date') ?? '',
    open: pickNum(r, 'open') ?? 0,
    high: pickNum(r, 'high') ?? 0,
    low: pickNum(r, 'low') ?? 0,
    close: pickNum(r, 'close') ?? 0,
    volume: pickNum(r, 'volume') ?? 0,
    amount: pickNum(r, 'amount') ?? 0,
  }
}

export function toFundFlow(v: unknown): FundFlow {
  const r = rec(v) ?? {}
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    kind: pickStr(r, 'kind') ?? '',
    date: pickStr(r, 'date'),
    mainNet: pickNum(r, 'mainNet') ?? 0,
    superNet: pickNum(r, 'superNet'),
    largeNet: pickNum(r, 'largeNet'),
    mediumNet: pickNum(r, 'mediumNet'),
    smallNet: pickNum(r, 'smallNet'),
    mainPct: pickNum(r, 'mainPct'),
    changePct: pickNum(r, 'changePct'),
    rank: pickNum(r, 'rank'),
    source: pickStr(r, 'source'),
  }
}

export function toSector(v: unknown): Sector {
  const r = rec(v) ?? {}
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    kind: pickStr(r, 'kind') ?? '',
    changePct: pickNum(r, 'changePct') ?? 0,
    mainNet: pickNum(r, 'mainNet'),
    count: pickNum(r, 'count'),
  }
}

export function toStockRef(v: unknown): StockRef {
  const r = rec(v) ?? {}
  const code = pickStr(r, 'code') ?? ''
  return {
    code,
    name: pickStr(r, 'name') ?? '',
    market: (pickStr(r, 'market') as Market | undefined) ?? inferMarket(code),
    kind: pickStr(r, 'kind'),
  }
}

export function toModelInfo(v: unknown): ModelInfo {
  const r = rec(v) ?? {}
  return {
    provider: pickStr(r, 'provider') ?? 'unknown',
    model: pickStr(r, 'model') ?? '',
    displayName: pickStr(r, 'displayName', 'name'),
    baseUrl: pickStr(r, 'baseUrl'),
    hasKey: pickBool(r, 'hasKey') ?? false,
    isDefault: pickBool(r, 'isDefault') ?? false,
    maxTokens: pickNum(r, 'maxTokens'),
    name: pickStr(r, 'name'),
    id: pickStr(r, 'id'),
  }
}

export function toWorker(v: unknown): WorkerInfo {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id') ?? '',
    title: pickStr(r, 'title', 'name') ?? '',
    description: pickStr(r, 'description', 'summary') ?? '',
  }
}

export function toAgentSession(v: unknown): AgentSession {
  const r = rec(v) ?? {}
  const status = pickStr(r, 'status') ?? 'ok'
  const normalized = status === 'ok' || status === 'failed' || status === 'running' ? status : 'ok'
  return {
    id: pickStr(r, 'id', 'sessionId') ?? '',
    worker: pickStr(r, 'worker') ?? 'chat',
    title: pickStr(r, 'title') ?? '未命名会话',
    startedAt: pickStr(r, 'startedAt') ?? '',
    status: normalized,
    tokens: pickNum(r, 'tokens'),
  }
}

export function toDocSummary(v: unknown): DocSummary {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id') ?? '',
    title: pickStr(r, 'title') ?? '未命名文档',
    summary: pickStr(r, 'summary') ?? '',
    tags: strList(r.tags),
    updatedAt: pickStr(r, 'updatedAt') ?? '',
  }
}

export function toKbHit(v: unknown): KbHit {
  const r = rec(v) ?? {}
  return {
    id: pickStr(r, 'id') ?? '',
    docId: pickStr(r, 'docId') ?? '',
    title: pickStr(r, 'title') ?? '未命名片段',
    snippet: pickStr(r, 'snippet', 'content') ?? '',
    score: pickNum(r, 'score') ?? 0,
  }
}

export function toKnowledgeDoc(v: unknown): KnowledgeDoc {
  const r = rec(v) ?? {}
  const sections = pickArr(r, 'sections')
    .map((s) => rec(s))
    .filter((s): s is Record<string, unknown> => s !== null)
    .map((s) => ({ heading: pickStr(s, 'heading') ?? '', content: pickStr(s, 'content') ?? '' }))
  return {
    id: pickStr(r, 'id') ?? '',
    title: pickStr(r, 'title') ?? '未命名文档',
    content: pickStr(r, 'content') ?? '',
    tags: strList(r.tags),
    sections: sections.length > 0 ? sections : undefined,
  }
}

export function toSkill(v: unknown): SkillInfo {
  const r = rec(v) ?? {}
  return {
    name: pickStr(r, 'name') ?? '',
    description: pickStr(r, 'description') ?? '',
    tags: strList(r.tags),
  }
}

/** watchlist.list 归一化：兼容 {items,groups,updatedAt,source} 与纯数组。 */
export function toWatchSnapshot(v: unknown): WatchSnapshot {
  const r = rec(v) ?? {}
  const rawItems = Array.isArray(v) ? v : pickArr(r, 'items')
  const items = rawItems.map(toWatchItem).filter((i) => i.code !== '')
  const groups = strList(pickArr(r, 'groups'))
  const groupSet: string[] = []
  for (const g of groups) if (g && !groupSet.includes(g)) groupSet.push(g)
  if (groupSet.length === 0) {
    for (const it of items) if (it.group && !groupSet.includes(it.group)) groupSet.push(it.group)
  }
  return {
    items,
    groups: groupSet,
    updatedAt: pickStr(r, 'updatedAt') ?? '',
    source: pickStr(r, 'source') ?? '',
  }
}

/** 指标数组：null 缺口保留，长度对齐 bars。 */
export function toMaSeries(v: unknown, length: number): MaSeries {
  const raw = Array.isArray(v) ? v : []
  const out: MaSeries = new Array<number | null>(length).fill(null)
  for (let i = 0; i < length && i < raw.length; i++) {
    const x = raw[i]
    if (typeof x === 'number' && Number.isFinite(x)) out[i] = x
  }
  return out
}

export function toNumList(v: unknown): number[] {
  return (Array.isArray(v) ? v : []).filter((x): x is number => typeof x === 'number' && Number.isFinite(x))
}

export function asText(v: unknown): string {
  if (typeof v === 'string') return v
  if (typeof v === 'number' && Number.isFinite(v)) return String(v)
  return ''
}

function num(v: unknown, fallback = 0): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : fallback
}

export function toParamDef(v: unknown): ParamDef {
  const r = rec(v) ?? {}
  return {
    key: pickStr(r, 'key') ?? '',
    label: pickStr(r, 'label', 'key') ?? '',
    type: pickStr(r, 'type') ?? 'int',
    min: pickNum(r, 'min'),
    max: pickNum(r, 'max'),
    step: pickNum(r, 'step'),
  }
}

export function toStrategySpec(v: unknown): StrategySpec {
  const r = rec(v) ?? {}
  const params = pickArr(r, 'params').map(toParamDef).filter((p) => p.key !== '')
  const defs = rec(r.default) ?? {}
  const def: Record<string, number> = {}
  for (const k of Object.keys(defs)) {
    const n = pickNum(defs, k)
    if (n !== undefined) def[k] = n
  }
  return {
    id: pickStr(r, 'id') ?? '',
    name: pickStr(r, 'name', 'id') ?? '',
    desc: pickStr(r, 'desc', 'description') ?? '',
    params: params.length > 0 ? params : undefined,
    default: def,
  }
}

export function toEquityPoint(v: unknown): EquityPoint {
  const r = rec(v) ?? {}
  return {
    date: pickStr(r, 'date') ?? '',
    equity: num(r.equity),
    cash: num(r.cash),
    position: num(r.position),
    shares: pickNum(r, 'shares') ?? 0,
  }
}

export function toQuantTrade(v: unknown): QuantTrade {
  const r = rec(v) ?? {}
  return {
    entryDate: pickStr(r, 'entryDate') ?? '',
    exitDate: pickStr(r, 'exitDate') ?? '',
    entryPrice: num(r.entryPrice),
    exitPrice: num(r.exitPrice),
    shares: pickNum(r, 'shares') ?? 0,
    pnl: num(r.pnl),
    returnPct: num(r.returnPct),
    days: pickNum(r, 'days') ?? 0,
  }
}

export function toCosts(v: unknown): CostBreakdown {
  const r = rec(v) ?? {}
  return {
    commission: num(r.commission),
    stampTax: num(r.stampTax),
    transfer: num(r.transfer),
    slippage: num(r.slippage),
  }
}

export function toQuantResult(v: unknown): QuantResult {
  const r = rec(v) ?? {}
  return {
    strategy: pickStr(r, 'strategy') ?? '',
    code: pickStr(r, 'code') ?? '',
    start: pickStr(r, 'start') ?? '',
    end: pickStr(r, 'end') ?? '',
    bars: pickNum(r, 'bars') ?? 0,
    initialCash: num(r.initialCash),
    finalEquity: num(r.finalEquity),
    totalReturn: num(r.totalReturn),
    cagr: num(r.cagr),
    maxDrawdown: num(r.maxDrawdown),
    sharpe: num(r.sharpe),
    sortino: num(r.sortino),
    winRate: num(r.winRate),
    turnover: num(r.turnover),
    tradeCount: pickNum(r, 'tradeCount') ?? 0,
    trades: pickArr(r, 'trades').map(toQuantTrade),
    equity: pickArr(r, 'equity').map(toEquityPoint).filter((e) => e.date !== ''),
    costs: toCosts(r.costs),
    warnings: strList(pickArr(r, 'warnings')),
  }
}

export function toFold(v: unknown): WalkForwardFold {
  const r = rec(v) ?? {}
  const best = rec(r.best) ?? {}
  const bestOut: Record<string, number | string> = {}
  for (const k of Object.keys(best)) {
    const raw = best[k]
    if (typeof raw === 'number' && Number.isFinite(raw)) bestOut[k] = raw
    else if (typeof raw === 'string') bestOut[k] = raw
  }
  return {
    index: pickNum(r, 'index') ?? 0,
    trainStart: pickStr(r, 'trainStart') ?? '',
    trainEnd: pickStr(r, 'trainEnd') ?? '',
    testStart: pickStr(r, 'testStart') ?? '',
    testEnd: pickStr(r, 'testEnd') ?? '',
    best: bestOut,
    trainSharpe: num(r.trainSharpe),
    trainReturn: num(r.trainReturn),
    testSharpe: num(r.testSharpe),
    testReturn: num(r.testReturn),
  }
}

export function toWalkForward(v: unknown): WalkForwardResult {
  const r = rec(v) ?? {}
  return {
    code: pickStr(r, 'code') ?? '',
    strategy: pickStr(r, 'strategy') ?? '',
    folds: pickArr(r, 'folds').map(toFold),
    isReturn: num(r.isReturn),
    oosReturn: num(r.oosReturn),
    overfitGap: num(r.overfitGap),
    verdict: pickStr(r, 'verdict') ?? '',
    note: pickStr(r, 'note'),
  }
}

export function toKlineAnalyze(v: unknown): KlineAnalyzeResponse {
  const r = rec(v) ?? {}
  const s = rec(r.summary) ?? {}
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    period: pickStr(r, 'period') ?? 'day',
    last: toBar(r.last),
    summary: {
      trend: pickStr(s, 'trend') ?? '—',
      ma: pickStr(s, 'ma') ?? '—',
      rsi: pickStr(s, 'rsi') ?? '—',
      macd: pickStr(s, 'macd') ?? '—',
      atr: pickStr(s, 'atr') ?? '—',
    },
    patterns: pickArr(r, 'patterns')
      .map((x) => rec(x))
      .filter((x): x is Record<string, unknown> => x !== null)
      .map((x) => ({
        name: pickStr(x, 'name') ?? '',
        index: pickNum(x, 'index') ?? 0,
        confidence: pickNum(x, 'confidence') ?? 0,
        note: pickStr(x, 'note'),
      }))
      .filter((x) => x.name !== ''),
    warning: pickStr(r, 'warning', 'stale'),
    support: toNumList(pickArr(r, 'support')),
    resistance: toNumList(pickArr(r, 'resistance')),
  }
}