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
}

// ---------- 模型设置（API.md 第 4 节） ----------

export interface ModelInfo {
  provider: string
  model: string
  displayName?: string
  baseUrl?: string
  hasKey: boolean
  isDefault: boolean
  maxTokens?: number
  /** 契约里 save 用 name 作为展示名，list 回的是 displayName，这里两者都收。 */
  name?: string
  id?: string
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
}

export interface AgentArtifact {
  name: string
  path: string
}

export interface AgentRunResult {
  sessionId: string
  worker: string
  report: string
  steps?: number
  usage?: Usage
  error?: string
  artifacts?: AgentArtifact[]
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

export interface QuantRunResponse {
  code: string
  period: string
  /** "none" 表示数据源不支持复权，结论需谨慎 */
  adjust: 'qfq' | 'none'
  warning?: string
  result: QuantResult
  report: string
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