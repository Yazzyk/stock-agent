// 前端类型集中定义。字段名逐字对齐 docs/API.md（Go 侧 json tag 为 camelCase）。

// ---------- 内核视图 ----------

export interface ToolView {
  name: string
  title: string
  summary: string
  tags?: string[]
  risk: string
}

export interface UISlot {
  id: string
  title: string
  icon?: string
  group?: string
  component: string
  order: number
  owner?: string
}

export interface MethodView {
  name: string
  description?: string
}

export interface PluginView {
  id: string
  name: string
  version: string
  description: string
  author?: string
  dependsOn?: string[]
  tags?: string[]
  active: boolean
  skipped?: boolean
  error?: string
  tools: ToolView[]
  ui: UISlot[]
  methods: MethodView[]
}

export interface Policy {
  networkAllowlist: string[]
  pathRoots: string[]
  allowExec: boolean
  maxReqPerMin: number
  maxToolCalls: number
  maxTokensPerRun: number
}

export interface StatusView {
  started: boolean
  plugins: number
  active: number
  tools: number
  ui: number
  services: string[]
  policy: Policy
  dataHealth?: Record<string, string>
  time: string
}

export interface AuditRow {
  time: string
  plugin: string
  kind: string
  action: string
  ok: boolean
  risk?: string
  elapsedMs?: number
  detail?: string
}

// ---------- 市场 DTO（contract.go） ----------

export type Market = 'sh' | 'sz' | 'bj'
export type Period = 'day' | 'week' | 'month' | 'min60'
export type FlowKind = 'sector' | 'concept'

export interface Quote {
  code: string
  name: string
  market: Market
  price: number
  prevClose: number
  open: number
  high: number
  low: number
  change: number
  changePct: number
  volume: number
  amount: number
  turnover: number
  amplitude: number
  time: string
  source: string
  stale?: boolean
}

export interface Bar {
  date: string
  open: number
  high: number
  low: number
  close: number
  volume: number
  amount: number
}

export interface FundFlow {
  code: string
  name: string
  kind: string
  date?: string
  mainNet: number
  superNet?: number
  largeNet?: number
  mediumNet?: number
  smallNet?: number
  mainPct?: number
  changePct?: number
  rank?: number
  source?: string
}

export interface Sector {
  code: string
  name: string
  kind: string
  changePct: number
  mainNet?: number
  count?: number
}

export interface WatchItem {
  code: string
  name: string
  market: Market
  group: string
  order: number
  source?: string
  // 契约外兜底：万一后端在条目里直带行情，前端也能用。
  price?: number
  prevClose?: number
  changePct?: number
  amount?: number
  turnover?: number
  stale?: boolean
}

export interface StockRef {
  code: string
  name: string
  market: Market
  kind?: string
}

export interface SyncResult {
  success: boolean
  message: string
  groups: string[]
  added: number
  removed: number
  needWebLogin: boolean
  needFullDiskAccess: boolean
  warnings: string[]
  source?: string
}

/** watchlist.list 返回（API.md 第 1 节）。 */
export interface WatchSnapshot {
  items: WatchItem[]
  groups: string[]
  updatedAt: string
  source: string
}

// ---------- K 线（API.md 第 3 节） ----------

export type MaSeries = Array<number | null>

export interface KlineIndicators {
  ma5: MaSeries
  ma10: MaSeries
  ma20: MaSeries
  ma60: MaSeries
  volume: MaSeries
}

export interface KlineResponse {
  code: string
  name: string
  period: string
  bars: Bar[]
  indicators?: Partial<KlineIndicators>
}

export interface KlinePattern {
  name: string
  index: number
  confidence: number
  note?: string
}

export interface KlineSummary {
  trend: string
  ma: string
  rsi: string
  macd: string
  atr: string
}

/** 一个时间尺度的研判：短线看 5/10/20 日动量，长线看 20/60/120 日结构。 */
export interface KlineHorizon {
  /** short = 短线，long = 长线 */
  key: 'short' | 'long'
  /** 展示标题，例如「短线（5~20 日）」 */
  title: string
  /** 参与计算的 K 线根数 */
  window: number
  /** 偏多 / 偏空 / 中性（结构结论，不是买卖建议） */
  bias: string
  summary: KlineSummary
  patterns: KlinePattern[]
  support: number[]
  resistance: number[]
}

export interface KlineAnalyzeResponse {
  code: string
  name: string
  period: string
  last: Bar
  summary: KlineSummary
  patterns: KlinePattern[]
  /** 后端降级/数据不足时的提示，可选 */
  warning?: string
  support: number[]
  resistance: number[]
  /** 短线/长线双尺度研判；后端旧版本可能不返回 */
  horizons?: KlineHorizon[]
}

// ---------- 模型设置（API.md 第 4 节） ----------

/** 模型接入方式：API Key / WorkBuddy 本机登录态 / OpenCode 兼容网关。 */
export type ModelAuthMode = 'apiKey' | 'workbuddy' | 'opencode'

export interface ModelInfo {
  provider: string
  model: string
  displayName?: string
  baseUrl?: string
  hasKey: boolean
  isDefault: boolean
  maxTokens?: number
  /** 模型上下文窗口（token），来自 provider 预设；未知为 0。 */
  contextWindow?: number
  /** 契约里 save 用 name 作为展示名，list 回的是 displayName，这里两者都收。 */
  name?: string
  id?: string
  /** 接入方式；缺省按 apiKey 处理（兼容旧后端） */
  authMode?: ModelAuthMode
  /** WorkBuddy 登录账号（仅展示，不含 token） */
  accountName?: string
  /** 登录态到期时间（ISO） */
  expiresAt?: string
}

export interface ModelListPayload {
  providers: ModelInfo[]
  defaultId: string
}

export interface ModelDraft {
  id?: string
  provider: string
  name?: string
  baseUrl?: string
  model: string
  apiKey?: string
  maxTokens?: number
  setDefault?: boolean
  authMode?: ModelAuthMode
}

export interface ModelTestResult {
  ok: boolean
  latencyMs: number
  message: string
}

// ---------- Agent（API.md 第 5 节） ----------

export interface Usage {
  promptTokens: number
  completionTokens: number
  totalTokens: number
}

export interface WorkerInfo {
  id: string
  title: string
  description: string
  /** main = 主控（router）；preset = 预设任务 worker；custom = 用户自建 */
  kind?: 'main' | 'preset' | 'custom'
  /** 用户通过「创建 worker」新增的 worker（等价于 kind === 'custom'） */
  custom?: boolean
  /** 该 worker 的默认模型（空表示跟随全局默认） */
  model?: string
}

export interface AgentArtifact {
  name: string
  path: string
}

/** 后端诊断条目：空回复时用它说明「为什么没产出」。code 可能为空（后端只给一句人话）。 */
export interface AgentDiagnostic {
  code: string
  message: string
}

export interface AgentRunResult {
  sessionId: string
  worker: string
  report: string
  steps?: number
  usage?: Usage
  error?: string
  artifacts?: AgentArtifact[]
  /** 本次实际使用的模型（空表示默认模型） */
  model?: string
  /** 结束原因：'stop' | 'length' | 'tool_calls' | 'content_filter' | '' */
  finishReason?: string
  /**
   * 空回复的原因代码（稳定枚举，有正文时缺省）：
   * no_response | reasoning_only | truncated | content_filter | steps | budget | tool_only | error
   */
  emptyReason?: string
  /**
   * 诊断明细：后端返回 **string[]**（每条形如「检测到 reasoning_content 非空而 content 为空…」）。
   * 前端在 lib/data.ts 归一成 {code,message} 便于排版；这里保留两种形态以兼容旧后端。
   */
  diagnostics?: Array<string | AgentDiagnostic>
  /** 推理模型的 reasoning_content 原文（可能为空） */
  reasoning?: string
  /** true 表示 report 其实是推理过程回退来的，不是真正的结论正文 */
  contentFromReasoning?: boolean
}

export type AgentStatus = 'ok' | 'failed' | 'running'

export interface AgentSession {
  id: string
  worker: string
  title: string
  startedAt: string
  status: AgentStatus
  tokens?: number
}

export interface AgentSessionEvent {
  seq: number
  time: string
  kind: string
  role?: string
  content: string
  toolName?: string
  ok?: boolean
  /** 产生这条事件的会话 id（后端新契约可选字段）：
      根会话的事件里混进子智能体 report 时，靠它区分「交付物」与「子过程产出」。
      旧数据没有这个字段，前端按 role/顺序兜底。 */
  sessionId?: string
}

/** agentlab.steer 返回：queued 是当前排队条数。 */
export interface AgentSteerResult {
  ok: boolean
  queued: number
}

export interface AgentSessionDetail {
  session: AgentSession
  events: AgentSessionEvent[]
}

/** agent.delta / agent.state / agent.report 的载荷。 */
export interface AgentDeltaPayload {
  sessionId?: string
  text?: string
}

export interface AgentStatePayload {
  sessionId?: string
  state?: string
  detail?: string
}

export interface AgentReportPayload {
  sessionId?: string
  worker?: string
  report?: string
  usage?: Usage
  /** 空回复归因与诊断（缺失表示后端没给） */
  finishReason?: string
  emptyReason?: string
  /** 后端是 string[]；这里放开成 unknown，由 store 归一 */
  diagnostics?: unknown
  reasoning?: string
  contentFromReasoning?: boolean
}

// ---------- 知识库（API.md 第 6 节） ----------

export interface DocSummary {
  id: string
  title: string
  summary: string
  tags: string[]
  updatedAt: string
}

export interface DocListPayload {
  docs: DocSummary[]
  total: number
}

export interface KbHit {
  id: string
  docId: string
  title: string
  snippet: string
  score: number
}

export interface KbSearchPayload {
  hits: KbHit[]
}

export interface DocSection {
  heading: string
  content: string
}

export interface KnowledgeDoc {
  id: string
  title: string
  content: string
  tags: string[]
  sections?: DocSection[]
}

export interface ImportResult {
  id: string
  title: string
  chars: number
}

export interface SkillInfo {
  name: string
  description: string
  tags: string[]
}

export interface SkillListPayload {
  skills: SkillInfo[]
}

export interface SkillContent {
  name: string
  description: string
  content: string
  resources: string[]
}

// ---------- 量化研究（API.md §5.5） ----------

export interface ParamDef {
  key: string
  label: string
  /** int | float */
  type: string
  min?: number
  max?: number
  step?: number
}

export interface StrategySpec {
  id: string
  name: string
  desc: string
  params?: ParamDef[]
  default?: Record<string, number>
  /** 策略规则（逐条中文说明，UI 直接展示，解决「不知道算法是什么」） */
  rules?: string[]
  /** 一句话信号公式，例如「MA(fast) > MA(slow) → 满仓」 */
  formula?: string
}

export interface QuantTrade {
  entryDate: string
  exitDate: string
  entryPrice: number
  exitPrice: number
  shares: number
  pnl: number
  returnPct: number
  days: number
}

export interface EquityPoint {
  date: string
  equity: number
  cash: number
  position: number
  shares: number
}

export interface CostBreakdown {
  commission: number
  stampTax: number
  transfer: number
  slippage: number
}

export interface QuantResult {
  strategy: string
  code: string
  start: string
  end: string
  bars: number
  initialCash: number
  finalEquity: number
  totalReturn: number
  cagr: number
  maxDrawdown: number
  sharpe: number
  sortino: number
  winRate: number
  turnover: number
  tradeCount: number
  trades: QuantTrade[]
  equity: EquityPoint[]
  costs: CostBreakdown
  warnings?: string[]
}

/** 逐笔成交（回测结果顶层 trades / result.fills），用于在 K 线上画买卖标记。 */
export interface QuantFill {
  date: string
  side: 'buy' | 'sell'
  price: number
  qty: number
  reason?: string
}

export interface QuantRunResponse {
  code: string
  period: string
  /** "none" 表示数据源不支持复权，结论需谨慎 */
  adjust: 'qfq' | 'none'
  warning?: string
  result: QuantResult
  report: string
  /** 逐笔成交（≤500）；result.trades 仍是旧的平仓交易数组，两者语义不同。 */
  trades?: QuantFill[]
}

export interface WalkForwardFold {
  index: number
  trainStart: string
  trainEnd: string
  testStart: string
  testEnd: string
  best: Record<string, number | string>
  trainSharpe: number
  trainReturn: number
  testSharpe: number
  testReturn: number
}

export interface WalkForwardResult {
  code: string
  strategy: string
  folds: WalkForwardFold[]
  isReturn: number
  oosReturn: number
  overfitGap: number
  verdict: string
  note?: string
}

export interface WalkForwardResponse {
  result: WalkForwardResult
  report: string
}

/** 参数优化候选：样本内指标 + 稳健性（邻域参数表现）。 */
export interface OptimizeCandidate {
  params: Record<string, number>
  totalReturn: number
  cagr: number
  maxDrawdown: number
  sharpe: number
  tradeCount: number
  /** 邻域稳健性 0~1：参数轻微扰动后夏普的平均衰减，越高越稳 */
  stability?: number
}

export interface OptimizeResult {
  strategy: string
  code: string
  start: string
  end: string
  /** 网格中按夏普排序的前 N 个候选 */
  candidates: OptimizeCandidate[]
  /** 综合样本内表现与稳健性选出的推荐参数 */
  best: OptimizeCandidate | null
  /** 推荐参数在 walk-forward 上的样本外表现 */
  walkforward?: WalkForwardResult
  report: string
  note?: string
}

export interface OptimizeResponse {
  result: OptimizeResult
}

// ---------- Watch 哨兵（API.md §5.6，插件 id=watch） ----------

export type WatchSourceType = 'quote' | 'sector' | 'kline' | 'news' | 'schedule'

export interface WatchSource {
  type: WatchSourceType
  /** quote / kline */
  codes?: string[]
  /** sector：板块或概念名 */
  sector?: string
  /** sector: sector|concept；news: roll|notice */
  kind?: string
  /** news 关键词 */
  keyword?: string
  /** kline 周期，默认 day */
  period?: string
  /** schedule：["09:20","14:50"] */
  times?: string[]
}

export interface WatchAction {
  /** agentlab worker：chat/intraday/premarket/postmarket/quant */
  worker: string
  /** 模板，可用 {指标名}、{code}、{sector}、{summary} 占位 */
  prompt: string
}

export interface WatchRule {
  id?: string
  name: string
  description?: string
  enabled: boolean
  sources: WatchSource[]
  condition: string
  action: WatchAction
  cooldownSeconds?: number
  quietWindows?: string[]
  createdBy?: string
  createdAt?: string
  updatedAt?: string
  /** 运行时状态：不入库 */
  lastTriggeredAt?: string
  triggerCount?: number
  lastError?: string
}

export interface WatchMetricSpec {
  name: string
  label: string
  unit?: string
}

export interface WatchRunLog {
  time: string
  ruleId: string
  ruleName: string
  worker: string
  triggered: boolean
  matched: boolean
  summary?: string
  metrics?: Record<string, number>
  report?: string
  error?: string
  elapsedMs?: number
}

export interface WatchScenario {
  code?: string
  name?: string
  sector?: string
  metrics: Record<string, number>
  matched: boolean
}

export interface WatchTestResult {
  ruleId: string
  matched: boolean
  scenarios: WatchScenario[]
  summary: string
  prompt: string
  error?: string
  elapsedMs?: number
}

export interface WatchListPayload {
  rules: WatchRule[]
  running: boolean
  metrics: WatchMetricSpec[]
  logs: WatchRunLog[]
}

export interface WatchTemplatesPayload {
  templates: WatchRule[]
  metrics: WatchMetricSpec[]
}

// ---------- 分时（market.minute / kline.minute） ----------

export interface MinutePoint {
  /** HH:MM */
  time: string
  price: number
  /** 均价，数据源未提供时为 undefined */
  avg?: number
  /** 分钟成交量（手） */
  volume?: number
  /** 分钟成交额（元） */
  amount?: number
}

export interface MinuteSeries {
  code: string
  name: string
  date: string
  prevClose: number
  points: MinutePoint[]
  source?: string
}

// ---------- 市场中心（plugins/marketcenter） ----------

export interface MarketIndex {
  code: string
  name: string
  price: number
  change: number
  changePct: number
  amount?: number
}

export interface MarketBreadth {
  date: string
  up: number
  down: number
  flat: number
  limitUp: number
  limitDown: number
  /** 炸板数（触及涨停后打开） */
  broken: number
  /** 炸板率 0~100 */
  brokenRate: number
  amount?: number
}

export interface LimitUpStock {
  code: string
  name: string
  price: number
  changePct: number
  /** 连续涨停板数（首板=1） */
  boards: number
  firstTime?: string
  lastTime?: string
  /** 涨停原因/所属题材 */
  reason?: string
  amount?: number
  turnover?: number
}

export interface HotStock {
  code: string
  name: string
  /** SH / SZ；未知为空 */
  market?: string
  price?: number
  changePct: number
  /** 热度分（同花顺 rate） */
  heat: number
  rank: number
  /** 排名变化：正数表示上升 */
  rankChange?: number
  conceptTags?: string[]
  popularityTag?: string
  /** 上榜原因（小时榜通常有） */
  reason?: string
}

export interface HotStock {
  code: string
  name: string
  /** SH / SZ；未知为空 */
  market?: string
  price?: number
  changePct: number
  /** 热度分（同花顺 rate） */
  heat: number
  rank: number
  /** 排名变化：正数表示上升 */
  rankChange?: number
  conceptTags?: string[]
  popularityTag?: string
  /** 上榜原因（小时榜通常有） */
  reason?: string
}

export interface LongHuRow {
  code: string
  name: string
  price: number
  changePct: number
  reason: string
  /** 龙虎榜净买入额（元） */
  netBuy: number
  buy: number
  sell: number
  date: string
}

// ---------- 内核事件 ----------

export interface KernelEvent {
  topic: string
  time: string
  payload: string
}

// ---------- Wails 桥 ----------

export interface GoBridge {
  Plugins: () => Promise<PluginView[]>
  Status: () => Promise<StatusView>
  Invoke: (plugin: string, method: string, payload: string) => Promise<string>
  CallTool: (name: string, args: string) => Promise<string>
  Audit: (limit: number) => Promise<AuditRow[]>
  Tools: () => Promise<ToolView[]>
}

export interface WailsRuntime {
  EventsOn?: (name: string, cb: (...args: unknown[]) => void) => void
  EventsOff?: (name: string) => void
  EventsEmit?: (name: string, ...args: unknown[]) => void
}

declare global {
  interface Window {
    go?: { main?: { App?: GoBridge } }
    runtime?: WailsRuntime
  }
}