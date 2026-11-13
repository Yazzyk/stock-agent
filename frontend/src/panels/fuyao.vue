<script setup lang="ts">
// 「数据源 · 同花顺官方」面板：把 124 个官方端点里可用的能力全部可视化。
//
// 信息架构（单页 + 二级分域，不新增一级导航）：
//   顶部工具条（标的联想/多选、刷新节律、口径与时间戳、端点路径）
//   → 分域页签（行情/涨跌停/热榜/竞价/特色数据/资金/财务/指数/基金/期货期权/资讯/打新/工具）
//   → 分域内二级视图（seg 切换）。
//
// 原则：
//   - 权限受限（官方 2004）与上游不可用（5003）都渲染成说明卡，不白屏、不弹错误框；
//   - 单位与口径写在每张表/卡的脚注里：成交量是「股」（同时给「手」）、金额是「元」、
//     涨跌幅是百分数原值、seal_rate 是 0~1 比例；
//   - 刷新节律按 docs/FUYAO-API.md §7；页面隐藏或非交易时段自动降频；
//   - 演示模式（浏览器直开）由 mocks/fuyao 提供全部 21 个方法的样例数据。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import Sparkline from '../components/Sparkline.vue'
import { invoke } from '../api'
import { copyText, openExternal, watchList } from '../lib/api.market'
import {
  cellDirection,
  describeFuyaoError,
  fmtLots,
  fmtMultiple,
  fmtPctValue,
  fmtPriceValue,
  fmtRatio,
  fmtShares,
  fmtSignedYuan,
  fmtStamp,
  fmtTradeDate,
  fmtYuan,
  formatCell,
  fuyaoAuction,
  fuyaoCalendar,
  fuyaoCapitalFlow,
  fuyaoCatalog,
  fuyaoFinancials,
  fuyaoFund,
  fuyaoFutures,
  fuyaoHotRank,
  fuyaoIndex,
  fuyaoIpo,
  fuyaoKline,
  fuyaoLimitPool,
  fuyaoLimitStats,
  fuyaoMarketDump,
  fuyaoNews,
  fuyaoOptions,
  fuyaoQuote,
  fuyaoSpecial,
  fuyaoTickerSearch,
  fuyaoValuation,
  inferColumns,
  isAuctionWindow,
  isTradingSession,
  labelOf,
  toAbilities,
  toEnvelope,
  type FuyaoAbility,
  type FuyaoAuctionRow,
  type FuyaoCalendarDay,
  type FuyaoCapitalFlow,
  type FuyaoCatalog,
  type FuyaoColumn,
  type FuyaoEndpoint,
  type FuyaoErrorView,
  type FuyaoHotRow,
  type FuyaoLimitRow,
  type FuyaoLimitStats,
  type FuyaoQuoteRow,
  type FuyaoRecord,
  type FuyaoSeries,
  type FuyaoTickerRef,
  type FuyaoValuationRow,
} from '../lib/api.fuyao'
import { fmtClock, fmtDateTime, fmtPrice, fmtPct, dir } from '../lib/format'
import { installFuyaoMocks } from '../mocks/fuyao'
import { toast } from '../stores/app'

installFuyaoMocks()

// ---------- 分域与视图定义 ----------

type DomainKey = 'market' | 'limit' | 'hot' | 'auction' | 'special' | 'flow' | 'financial' | 'index' | 'fund' | 'derivative' | 'news' | 'ipo' | 'tools'

interface ViewDef {
  key: string
  label: string
}

interface DomainDef {
  key: DomainKey
  title: string
  hint: string
  /** 二级视图；特色数据用 SPECIAL_VIEWS，故允许缺省。 */
  views?: ViewDef[]
}

/** 13 个分域（每域至少一个可用视图）。 */
const DOMAINS: DomainDef[] = [
  { key: 'market', title: '行情', hint: '快照 + 估值 + K 线', views: [{ key: 'snapshot', label: '快照/估值' }, { key: 'kline', label: 'K 线' }] },
  { key: 'limit', title: '涨跌停', hint: '涨停/跌停/炸板池 + 情绪', views: [{ key: 'pool', label: '涨停池/炸板池' }, { key: 'stats', label: '情绪统计' }] },
  { key: 'hot', title: '热榜', hint: '同花顺热股/飙升/历史', views: [{ key: 'hot', label: '榜单一览' }] },
  { key: 'auction', title: '竞价', hint: '快照/风向标/机会', views: [{ key: 'snapshot', label: '竞价快照' }, { key: 'benchmark', label: '风向标' }, { key: 'opportunities', label: '竞价机会' }] },
  { key: 'special', title: '特色数据', hint: '异动/龙虎榜/筹码/概念' },
  { key: 'flow', title: '资金', hint: '实时/历史/高频（客户端专用）', views: [{ key: 'snapshot', label: '实时' }, { key: 'history', label: '历史' }, { key: 'highfreq', label: '高频' }, { key: 'intraday', label: '分时' }] },
  { key: 'financial', title: '财务', hint: '三大报表 + 五类指标', views: [{ key: 'income', label: '利润表' }, { key: 'balance', label: '资产负债表' }, { key: 'cash', label: '现金流量表' }, { key: 'indicators', label: '五类指标' }] },
  { key: 'index', title: '指数/板块', hint: '目录/成分股/反查/快照/K线', views: [{ key: 'catalog', label: '目录' }, { key: 'constituents', label: '成分股' }, { key: 'memberships', label: '个股反查' }, { key: 'snapshot', label: '指数快照' }, { key: 'kline', label: '指数 K 线' }] },
  { key: 'fund', title: '基金', hint: '资料/净值/持仓/业绩/经理', views: [{ key: 'profile', label: '资料' }, { key: 'snapshot', label: '行情' }, { key: 'nav', label: '净值' }, { key: 'returns', label: '收益' }, { key: 'holdings', label: '持仓' }, { key: 'asset-allocation', label: '资产配置' }, { key: 'manager', label: '经理' }, { key: 'performance', label: '业绩' }, { key: 'etf-flow', label: 'ETF 申赎' }, { key: 'drawdown', label: '回撤' }, { key: 'holders', label: '持有人' }, { key: 'dividends', label: '分红' }, { key: 'industry-allocation', label: '行业配置' }, { key: 'financial-indicators', label: '基金指标' }, { key: 'news', label: '资讯' }] },
  { key: 'derivative', title: '期货/期权', hint: '品种/合约/持仓/基差/时间轴', views: [{ key: 'futures-varieties', label: '期货品种' }, { key: 'futures-main', label: '期货主力' }, { key: 'futures-positions', label: '期货持仓' }, { key: 'futures-basis', label: '基差' }, { key: 'futures-session', label: '期货时间轴' }, { key: 'futures-intraday', label: '期货分时' }, { key: 'futures-kline', label: '期货日 K' }, { key: 'futures-fundamentals', label: '期货 F10' }, { key: 'options-varieties', label: '期权品种' }, { key: 'options-contracts', label: '期权合约' }, { key: 'options-session', label: '期权时间轴' }, { key: 'options-intraday', label: '期权分时' }, { key: 'options-kline', label: '期权日 K' }] },
  { key: 'news', title: '资讯', hint: '快讯/公告/专题/个股/异动', views: [{ key: 'flash', label: '快讯' }, { key: 'notice', label: '公告' }, { key: 'topic', label: '专题' }, { key: 'stock-timeline', label: '个股新闻' }, { key: 'anomaly', label: '异动解读' }, { key: 'events', label: '事件库' }] },
  { key: 'ipo', title: '打新', hint: 'A 股/港股', views: [{ key: 'a-ipo', label: 'A 股打新' }, { key: 'hk-calendar', label: '港股日历' }, { key: 'hk-application', label: '港股认购' }, { key: 'hk-detail', label: '港股详情' }] },
  { key: 'tools', title: '工具/长尾', hint: '目录/通用查询/日历/导出', views: [{ key: 'catalog', label: '端点目录' }, { key: 'query', label: '通用查询' }, { key: 'calendar', label: '交易日历' }, { key: 'dump', label: '全市场导出' }] },
]

/** 特色数据的 10 个子视图（docs §3.6）。 */
const SPECIAL_VIEWS: ViewDef[] = [
  { key: 'anomaly', label: '个股异动' },
  { key: 'anomaly-stock', label: '异动原因' },
  { key: 'abnormal', label: '异动监控' },
  { key: 'dragon-tiger', label: '龙虎榜' },
  { key: 'dragon-tiger-institution', label: '机构交易' },
  { key: 'department-record', label: '营业部战绩' },
  { key: 'department-history', label: '营业部历史' },
  { key: 'chip-shape', label: '筹码形态' },
  { key: 'new-concepts', label: '新增概念' },
  { key: 'turnover', label: '成交额预测' },
]

/** 把 DOMAINS 展开成带默认视图的映射（特色数据用 SPECIAL_VIEWS）。 */
const DEFAULT_VIEW: Record<DomainKey, string> = {
  market: 'snapshot',
  limit: 'pool',
  hot: 'hot',
  auction: 'snapshot',
  special: 'anomaly',
  flow: 'snapshot',
  financial: 'income',
  index: 'catalog',
  fund: 'profile',
  derivative: 'futures-varieties',
  news: 'flash',
  ipo: 'a-ipo',
  tools: 'catalog',
}

const domain = ref<DomainKey>('market')
const viewByDomain = ref<Record<DomainKey, string>>({ ...DEFAULT_VIEW })

/** 当前分域定义。 */
const domainDef = computed<DomainDef>(() => DOMAINS.find((d) => d.key === domain.value) ?? DOMAINS[0])
/** 当前分域的视图列表（特色数据用独立清单）。 */
const viewList = computed<ViewDef[]>(() => (domain.value === 'special' ? SPECIAL_VIEWS : domainDef.value.views ?? []))
/** 当前视图 key。 */
const view = computed<string>(() => viewByDomain.value[domain.value])
/** 当前视图的中文名（找不到时回落 key）。 */
const viewLabel = computed<string>(() => viewList.value.find((v) => v.key === view.value)?.label ?? view.value)

// ---------- 通用状态 ----------

type Mode = 'quotes' | 'series' | 'limits' | 'stats' | 'hot' | 'auction' | 'flow' | 'generic' | 'record' | 'abilities' | 'news' | 'catalog' | 'query' | 'calendar' | 'dump' | 'permission' | 'empty'

const mode = ref<Mode>('quotes')
const loading = ref(true)
const refreshing = ref(false)
const error = ref<FuyaoErrorView | null>(null)
const note = ref('')
const asOf = ref('')
const dataTimestamp = ref('')
const endpointPath = ref('')
const updatedAt = ref('')
const hidden = ref(typeof document !== 'undefined' ? document.hidden : false)

// 各视图的数据容器（只保留当前视图需要的那一份，切视图即重置）
const rows = ref<FuyaoRecord[]>([])
const series = ref<FuyaoSeries[]>([])
const dataObj = ref<FuyaoRecord | null>(null)
const abilities = ref<FuyaoAbility[]>([])
const quoteRows = ref<FuyaoQuoteRow[]>([])
const valuationRows = ref<FuyaoValuationRow[]>([])
const limitRows = ref<FuyaoLimitRow[]>([])
const limitStats = ref<FuyaoLimitStats | null>(null)
const hotRows = ref<FuyaoHotRow[]>([])
/** 热榜排名走势的中文（官方 rank_trend=up/down/flat）。 */
function trendText(raw: string): string {
  if (raw === 'up') return '上升'
  if (raw === 'down') return '下降'
  if (raw === 'flat') return '持平'
  return raw || '—'
}

/** 是否需要显示统一的尾部口径提示（避免与各视图自带脚注重复）。 */
const showTailNote = computed<boolean>(() => {
  if (note.value === '') return false
  return !['hot', 'news', 'limits', 'auction', 'permission', 'empty', 'query', 'generic'].includes(mode.value)
})

/** 热榜标题随类型切换（热股/飙升/历史）。 */
const hotTitle = computed<string>(() => (hotKind.value === 'skyrocket' ? '同花顺飙升榜' : hotKind.value === 'history' ? '同花顺历史热股榜' : '同花顺热股榜'))
const auctionRows = ref<FuyaoAuctionRow[]>([])
const auctionStatus = ref('')
const flowRow = ref<FuyaoCapitalFlow | null>(null)
const calendarDays = ref<FuyaoCalendarDay[]>([])
const catalog = ref<FuyaoCatalog | null>(null)
const rawJson = ref('')
const queryEndpoint = ref('a-share.prices.snapshot')
const queryParams = ref('{}')
const queryParamsError = ref('')
const queryRawMode = ref(false)

// ---------- 工具条：标的与参数 ----------

/** 默认标的：股票 + 指数，覆盖快照/竞价/反查等视图。 */
const codes = ref<string[]>(['600519.SH', '300750.SZ', '688981.SH', '000001.SH'])
const codeInput = ref('')
const suggestions = ref<FuyaoTickerRef[]>([])
const suggestOpen = ref(false)
const searching = ref(false)
const catalogState = ref<'idle' | 'loading' | 'ready' | 'failed'>('idle')
const catalogQuery = ref('')
const catalogGroup = ref('')
const indexCatalogRows = ref<FuyaoRecord[]>([])
const indexTag = ref('cn_concept')
const indexCode = ref('885431.TI')
const specialCode = ref('600519.SH')
const shape = ref('单峰密集')
const departmentId = ref('D0001')
const reportPeriod = ref('2025-4')
const flowCode = ref('600519.SH')
const flowInterval = ref('1d')
/** 涨跌停池类型：up=涨停 / down=跌停 / break=炸板（官方三个端点）。 */
const poolType = ref<'up' | 'down' | 'break'>('up')
/** 热榜类型：hot=热股 / skyrocket=飙升 / history=历史。 */
const hotKind = ref<'hot' | 'skyrocket' | 'history'>('hot')
const hotPeriod = ref<'day' | 'hour'>('day')
const fundCode = ref('110011.OF')
/** 全市场导出类型：10y=十年日K / 10d=十日K / adjust=复权因子。 */
const dumpKind = ref<'10y' | '10d' | 'adjust'>('10y')
/** 基金二级视图直接由分域视图驱动（下拉框只是同一状态的镜像，避免双份真相）。 */
const fundView = computed<string>({
  get: () => view.value,
  set: (v: string) => {
    if (viewList.value.some((item) => item.key === v)) setView(v)
  },
})
const futCode = ref('RB2605.SHF')
const futDate = ref('2026-11-13')
/** 期货视图 key（futures-xxx）→ 数据层 view 名（positions → positions-variety）。 */
const FUTURES_VIEW_MAP: Record<string, string> = {
  varieties: 'varieties',
  contracts: 'contracts',
  main: 'main',
  positions: 'positions-variety',
  basis: 'basis',
  session: 'session',
  intraday: 'intraday',
  kline: 'kline',
  fundamentals: 'fundamentals',
}
/** 期货下拉框：value 是去掉 futures- 前缀的短名。 */
const futView = computed<string>({
  get: () => view.value.replace('futures-', ''),
  set: (v: string) => {
    if (viewList.value.some((item) => item.key === 'futures-' + v)) setView('futures-' + v)
  },
})
const optCode = ref('IO2612-C-4000.CFX')
/** 期权下拉框：value 是去掉 options- 前缀的短名。 */
const optView = computed<string>({
  get: () => view.value.replace('options-', ''),
  set: (v: string) => {
    if (viewList.value.some((item) => item.key === 'options-' + v)) setView('options-' + v)
  },
})
const newsTagId = ref('1')
const newsSeq = ref('0')
const newsCode = ref('688981.SH')
const ipoMarket = ref('sh_sz')
const ipoStatus = ref('subscribable')
const klineInterval = ref('1d')
const klineAdjust = ref('forward')
const today = new Date()
const dateValue = ref(today.toISOString().slice(0, 10))

// 刷新节律：自动（按域+时段）/ 固定间隔 / 关闭
type RefreshMode = 'auto' | '3s' | '30s' | '60s' | 'off'
const refreshMode = ref<RefreshMode>('auto')

/** 当前自动节律（毫秒；0 表示仅手动刷新）。 */
function cadenceMs(): number {
  if (refreshMode.value !== 'auto') {
    return refreshMode.value === '3s' ? 3000 : refreshMode.value === '30s' ? 30000 : refreshMode.value === '60s' ? 60000 : 0
  }
  const trading = isTradingSession()
  switch (domain.value) {
    case 'market':
      return view.value === 'snapshot' ? (trading ? 3000 : 60000) : 60000
    case 'limit':
      return 30000
    case 'hot':
      return 60000
    // 竞价只有 9:15–9:25 需要高频，其余时段静态
    case 'auction':
      return isAuctionWindow() ? 5000 : 0
    case 'special':
      return 60000
    case 'flow':
      return 30000
    case 'news':
      return view.value === 'flash' ? (trading ? 10000 : 60000) : 0
    case 'index':
      return view.value === 'snapshot' ? (trading ? 3000 : 60000) : 0
    default:
      return 0
  }
}

/** 当前节律的中文说明（工具条展示）。 */
const cadenceText = computed<string>(() => {
  const ms = cadenceMs()
  if (ms === 0) return hidden.value ? '页面隐藏 · 已暂停' : '仅手动刷新'
  return (ms >= 1000 ? ms / 1000 + 's' : ms + 'ms') + (refreshMode.value === 'auto' ? ' · 自动' : '')
})

// ---------- 分域切换 ----------

/** 切换分域：重置错误、切到该域上次的视图并重新加载。 */
function setDomain(key: DomainKey): void {
  if (domain.value === key) return
  domain.value = key
  resetData()
  void load(true)
}

/** 切换二级视图。 */
function setView(key: string): void {
  if (viewByDomain.value[domain.value] === key) return
  viewByDomain.value = { ...viewByDomain.value, [domain.value]: key }
  resetData()
  void load(true)
}

/** 清空上一次视图的数据与状态（避免 A 视图的表格闪到 B 视图）。 */
function resetData(): void {
  error.value = null
  note.value = ''
  asOf.value = ''
  dataTimestamp.value = ''
  endpointPath.value = ''
  rows.value = []
  series.value = []
  dataObj.value = null
  abilities.value = []
  quoteRows.value = []
  valuationRows.value = []
  limitRows.value = []
  limitStats.value = null
  hotRows.value = []
  auctionRows.value = []
  auctionStatus.value = ''
  flowRow.value = null
  calendarDays.value = []
  rawJson.value = ''
}

// ---------- 标的推理（tickerSearch 联想 + 自选填充） ----------

let suggestTimer: number | null = null

/** 输入防抖 300ms 调 tickerSearch；空输入直接关闭联想。 */
function onCodeInput(): void {
  suggestOpen.value = true
  if (suggestTimer !== null) window.clearTimeout(suggestTimer)
  const q = codeInput.value.trim()
  if (q === '') {
    suggestions.value = []
    return
  }
  suggestTimer = window.setTimeout(() => {
    void runSuggest(q)
  }, 300)
}

/** 调官方标的检索（失败静默：联想不是主路径）。 */
async function runSuggest(q: string): Promise<void> {
  searching.value = true
  try {
    suggestions.value = await fuyaoTickerSearch(q, '', 10)
  } catch {
    suggestions.value = []
  } finally {
    searching.value = false
  }
}

/** 手工输入归一：thscode 原样、6 位数字按交易所补后缀。 */
function normalizeInput(raw: string): string {
  const text = raw.trim().toUpperCase()
  if (text === '') return ''
  if (/\.[A-Z]{2}$/.test(text) || /^(SH|SZ|BJ)/.test(text)) return text
  const digits = text.replace(/[^0-9]/g, '').slice(0, 6)
  if (digits === '') return ''
  if (/^(6|5|9)/.test(digits)) return digits + '.SH'
  if (/^(4|8)/.test(digits)) return digits + '.BJ'
  return digits + '.SZ'
}

/** 加入一个标的（去重、上限 50，与官方快照批量一致）。 */
function addCode(raw: string): void {
  const code = normalizeInput(raw)
  if (code === '') return
  if (codes.value.includes(code)) {
    codeInput.value = ''
    suggestOpen.value = false
    return
  }
  if (codes.value.length >= 50) {
    toast('一次最多 50 个标的（官方快照批量上限）', 'warn')
    return
  }
  codes.value = [...codes.value, code]
  codeInput.value = ''
  suggestions.value = []
  suggestOpen.value = false
}

/** 从联想里选一项：优先用官方 thscode（带 .OF/.TI 等后缀）。 */
function pickSuggestion(item: FuyaoTickerRef): void {
  addCode(item.thscode || item.ticker)
}

/** 回车：输入框有内容就当标的加入。 */
function onCodeEnter(): void {
  if (suggestions.value.length > 0 && codeInput.value.trim() !== '') {
    const first = suggestions.value[0]
    if (first) {
      pickSuggestion(first)
      return
    }
  }
  addCode(codeInput.value)
}

/** 移除标的。 */
function removeCode(code: string): void {
  codes.value = codes.value.filter((c) => c !== code)
}

/** 用自选股填充标的（走现有 watchlist.list，不额外增加后端方法）。 */
async function fillFromWatchlist(): Promise<void> {
  try {
    const snap = await watchList()
    const list = snap.items.map((i) => (i.market + i.code).toUpperCase())
    if (list.length === 0) {
      toast('自选股为空：请先在自选股面板同步', 'warn')
      return
    }
    codes.value = Array.from(new Set(list)).slice(0, 50)
    toast('已用自选股填充 ' + codes.value.length + ' 个标的', 'ok')
  } catch (e) {
    toast(e instanceof Error ? e.message : '读取自选失败', 'warn')
  }
}

// ---------- 数据加载 ----------

let seq = 0

/** 当前请求的端点路径（工具条展示与复制；方法没有 endpoint 时按视图推断）。 */
function guessEndpoint(): string {
  const map: Record<string, string> = {
    'market/snapshot': 'a-share.prices.snapshot',
    'market/kline': 'a-share.prices.historical-batch',
    'limit/pool': 'a-share.special-data.limit-up-pool',
    'limit/stats': 'a-share.special-data.limit-up-statistics',
    'hot/hot': 'a-share.special-data.hot-stock-list',
    'auction/snapshot': 'a-share.auction.snapshot',
    'auction/benchmark': 'a-share.auction.short-term-benchmark',
    'auction/opportunities': 'a-share.special-data.auction-stock-opportunities',
    'flow/snapshot': 'a-share.capital-flow.snapshot',
    'flow/history': 'a-share.capital-flow.historical',
    'flow/highfreq': 'a-share.high-frequency.historical',
    'flow/intraday': 'a-share.high-frequency.intraday',
    'financial/income': 'a-share.financials.income-statements',
    'financial/balance': 'a-share.financials.balance-sheets',
    'financial/cash': 'a-share.financials.cash-flow-statements',
    'financial/indicators': 'a-share.financials.indicators',
    'index/catalog': 'a-share-index.catalog.ths-index-list',
    'index/constituents': 'a-share-index.constituents.ths-stock-list',
    'index/memberships': 'a-share-index.constituents.ths-index-list-by-stock',
    'index/snapshot': 'a-share-index.prices.snapshot',
    'index/kline': 'a-share-index.prices.historical',
    'tools/calendar': 'a-share.calendar.trading-days',
    'tools/catalog': '(离线目录：124 个端点)',
    'tools/query': queryEndpoint.value,
    'tools/dump':
      dumpKind.value === 'adjust'
        ? 'dump.market-dumps.adjustment-factors.download-url'
        : dumpKind.value === '10d'
          ? 'dump.market-dumps.daily-k-10d.download-url'
          : 'dump.market-dumps.daily-k.download-url',
    'special/anomaly': 'a-share.special-data.anomaly-analysis-list',
    'special/anomaly-stock': 'a-share.special-data.anomaly-analysis-stock',
    'special/abnormal': 'a-share.special-data.abnormal-monitor-list',
    'special/dragon-tiger': 'a-share.special-data.dragon-tiger-list',
    'special/dragon-tiger-institution': 'a-share.special-data.dragon-tiger-stock-institution',
    'special/department-record': 'a-share.special-data.dragon-tiger-department-classic-record',
    'special/department-history': 'a-share.special-data.dragon-tiger-department-history',
    'special/chip-shape': 'a-share.special-data.chip-shape-stocks',
    'special/new-concepts': 'a-share-index.baseinfo.new-concepts',
    'special/turnover': 'a-share.special-data.turnover-prediction-snapshot',
    'fund/profile': 'fund.profile.detail',
    'fund/snapshot': 'fund.market.snapshot',
    'fund/nav': 'fund.performance.nav',
    'fund/returns': 'fund.performance.returns',
    'fund/holdings': 'fund.portfolio.holdings',
    'fund/asset-allocation': 'fund.portfolio.asset-allocation',
    'fund/manager': 'fund.managers.detail',
    'fund/performance': 'fund.managers.performance',
    'fund/etf-flow': 'fund.etf-flow.net-inflow-historical',
    'fund/drawdown': 'fund.performance.drawdowns',
    'fund/holders': 'fund.holders.detail',
    'fund/dividends': 'fund.corporate-actions.dividends',
    'fund/industry-allocation': 'fund.portfolio.industry-allocation',
    'fund/financial-indicators': 'fund.financials.indicators',
    'fund/news': 'fund.news.article-list',
    'derivative/futures-varieties': 'futures.varieties.list',
    'derivative/futures-main': 'futures.contracts.main-list',
    'derivative/futures-positions': 'futures.positions.variety-daily',
    'derivative/futures-basis': 'futures.basis.main-continuous-latest',
    'derivative/futures-session': 'futures.calendar.session-timeline',
    'derivative/futures-fundamentals': 'futures.fundamentals.indicators-historical',
    'derivative/options-varieties': 'options.varieties.list',
    'derivative/options-contracts': 'options.contracts.list',
    'derivative/options-session': 'options.calendar.session-timeline',
    'news/flash': 'news.flash.list',
    'news/notice': 'news.notice.detail',
    'news/topic': 'news.topic.hot-list',
    'news/stock-timeline': 'news.stock.timeline-list',
    'news/anomaly': 'news.anomaly.analysis',
    'news/events': 'news.events.search',
    'ipo/a-ipo': 'a-share.ipo.calendar-list',
    'ipo/hk-calendar': 'hk-share.ipo.calendar-list',
    'ipo/hk-application': 'hk-share.ipo.application-list',
    'ipo/hk-detail': 'hk-share.ipo.issue-detail',
  }
  return map[domain.value + '/' + view.value] ?? 'fuyao.' + view.value
}

/** 加载当前分域/视图的数据；initial=true 时显示骨架屏。 */
async function load(initial = false): Promise<void> {
  const current = ++seq
  if (initial) loading.value = true
  else refreshing.value = true
  error.value = null
  const apply = (fn: () => void): void => {
    if (current === seq) fn()
  }
  try {
    await loadCurrent(apply)
    apply(() => {
      updatedAt.value = new Date().toISOString()
      if (endpointPath.value === '') endpointPath.value = guessEndpoint()
      if (note.value === '' && mode.value === 'generic' && rows.value.length === 0) {
        note.value = '官方返回成功但没有记录（合法空结果）：请核对日期/代码是否在可用范围内。'
      }
    })
  } catch (e) {
    apply(() => {
      const view = describeFuyaoError(e)
      // 5003 是上游状态而不是失败：用说明卡，不给红色错误块
      if (view.kind === 'upstream') {
        mode.value = 'empty'
        error.value = view
      } else {
        error.value = view
        mode.value = 'empty'
      }
    })
  } finally {
    apply(() => {
      loading.value = false
      refreshing.value = false
    })
  }
  schedule()
}

/** 按分域/视图分派到具体方法（集中在这里，便于核对方法 → 视图对照表）。 */
async function loadCurrent(apply: (fn: () => void) => void): Promise<void> {
  if (domain.value === 'market') {
    if (view.value === 'kline') {
      const res = await fuyaoKline({
        codes: codes.value,
        interval: klineInterval.value,
        adjust: klineAdjust.value,
        limit: 120,
      })
      apply(() => {
        series.value = res.series
        note.value = res.note
        asOf.value = res.asOf
        mode.value = res.series.some((s) => s.bars.length > 0) ? 'series' : 'empty'
      })
      return
    }
    const [quotes, valuation] = await Promise.all([fuyaoQuote(codes.value), fuyaoValuation(codes.value)])
    apply(() => {
      quoteRows.value = quotes.rows
      valuationRows.value = valuation.rows
      note.value = quotes.note
      asOf.value = quotes.asOf
      mode.value = quotes.rows.length === 0 && valuation.rows.length === 0 ? 'empty' : 'quotes'
    })
    return
  }

  if (domain.value === 'limit') {
    if (view.value === 'stats') {
      const res = await fuyaoLimitStats(dateValue.value)
      apply(() => {
        limitStats.value = res.stats
        asOf.value = res.asOf
        mode.value = res.stats.tradeDate === '' ? 'empty' : 'stats'
        note.value = 'seal_rate 是 0~1 比例（不是百分数）；家数为整数。'
      })
      return
    }
    const pool = await fuyaoLimitPool(poolType.value, 60)
    const stats = poolType.value === 'up' ? await fuyaoLimitStats(dateValue.value).catch(() => null) : null
    apply(() => {
      limitRows.value = pool.rows
      // 跌停/炸板池没有连板字段：直接用通用表渲染（字段随端点变化）
      rows.value = pool.items
      limitStats.value = stats?.stats ?? null
      note.value = pool.note
      asOf.value = pool.asOf
      mode.value = pool.rows.length === 0 ? 'empty' : poolType.value === 'up' ? 'limits' : 'generic'
    })
    return
  }

  if (domain.value === 'hot') {
    const res = await fuyaoHotRank(hotKind.value, hotPeriod.value, 30, hotKind.value === 'history' ? dateValue.value : '')
    apply(() => {
      hotRows.value = res.rows
      note.value = res.note
      asOf.value = res.asOf
      mode.value = res.rows.length === 0 ? 'empty' : 'hot'
    })
    return
  }

  if (domain.value === 'auction') {
    if (view.value === 'snapshot') {
      const res = await fuyaoAuction({ view: 'snapshot', codes: codes.value, stage: 'live' })
      // data_status 在信封顶层（ready/not_ready）；真实链路未透传时留空，交给行数判断
      const status = res.dataStatus === 'ready' || res.dataStatus === 'not_ready' ? res.dataStatus : ''
      apply(() => {
        auctionRows.value = res.rows
        auctionStatus.value = status
        note.value = res.note
        asOf.value = res.asOf
        if (res.permission === 'client') {
          mode.value = 'permission'
        } else if (status === 'not_ready') {
          mode.value = 'empty'
          error.value = { kind: 'other', title: '竞价未开始', message: '官方返回 data_status=not_ready：集合竞价 9:15 开始，9:25 结束后进入连续竞价。' }
        } else {
          mode.value = res.rows.length === 0 ? 'empty' : 'auction'
          if (res.rows.length === 0) error.value = { kind: 'other', title: '暂无竞价数据', message: '当前标的没有竞价快照，换个标的或稍后再试。' }
        }
      })
      return
    }
    const res = await fuyaoAuction({ view: view.value === 'benchmark' ? 'benchmark' : 'opportunities', date: dateValue.value })
    apply(() => {
      rows.value = res.items
      note.value = res.note
      asOf.value = res.asOf
      mode.value = res.items.length === 0 ? 'empty' : 'generic'
    })
    return
  }

  if (domain.value === 'special') {
    const res = await fuyaoSpecial({
      view: view.value,
      codes: view.value === 'anomaly-stock' ? codes.value : undefined,
      code: specialCode.value,
      date: dateValue.value,
      shape: shape.value,
      departmentId: departmentId.value,
      limit: 30,
    })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      dataTimestamp.value = res.dataTimestamp
      if (res.permission === 'client') {
        mode.value = 'permission'
        return
      }
      if (view.value === 'dragon-tiger') {
        const stockItems = res.data && Array.isArray(res.data.stock_items) ? res.data.stock_items : []
        rows.value = stockItems.map((x) => (typeof x === 'object' && x !== null ? (x as FuyaoRecord) : {})).filter((x) => Object.keys(x).length > 0)
        dataObj.value = res.data
        mode.value = rows.value.length === 0 ? 'empty' : 'generic'
        return
      }
      if (res.items.length > 0) {
        rows.value = res.items
        mode.value = 'generic'
        return
      }
      if (res.data) {
        dataObj.value = res.data
        rows.value = []
        mode.value = 'record'
        return
      }
      mode.value = 'empty'
    })
    return
  }

  if (domain.value === 'flow') {
    const res = await fuyaoCapitalFlow({
      view: view.value === 'snapshot' ? 'snapshot' : view.value === 'history' ? 'history' : view.value === 'intraday' ? 'intraday' : 'highfreq',
      code: flowCode.value,
      interval: flowInterval.value,
      date: dateValue.value,
    })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      if (res.permission === 'client') {
        mode.value = 'permission'
        return
      }
      flowRow.value = res.flow
      rows.value = res.items
      mode.value = res.flow ? 'flow' : res.items.length > 0 ? 'generic' : 'empty'
    })
    return
  }

  if (domain.value === 'financial') {
    const kind = view.value === 'indicators' ? 'indicators' : view.value === 'balance' ? 'balance' : view.value === 'cash' ? 'cash' : 'income'
    const res = await fuyaoFinancials({
      kind,
      code: codes.value[0] ?? '600519.SH',
      period: 'annual',
      report: reportPeriod.value,
      limit: 8,
    })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      if (kind === 'indicators') {
        abilities.value = toAbilities(res.data)
        rows.value = []
        mode.value = abilities.value.length === 0 ? 'empty' : 'abilities'
        return
      }
      rows.value = res.items
      mode.value = res.items.length === 0 ? 'empty' : 'generic'
    })
    return
  }

  if (domain.value === 'index') {
    if (view.value === 'catalog') {
      const tag = indexTag.value
      const res = await fuyaoIndex({ view: 'catalog', tag })
      apply(() => {
        indexCatalogRows.value = res.items
        rows.value = res.items
        note.value = res.note
        asOf.value = res.asOf
        mode.value = res.items.length === 0 ? 'empty' : 'generic'
      })
      return
    }
    if (view.value === 'kline') {
      const res = await fuyaoIndex({ view: 'kline', codes: [indexCode.value], limit: 120 })
      apply(() => {
        series.value = res.series
        note.value = res.note
        asOf.value = res.asOf
        mode.value = res.series.length === 0 ? 'empty' : 'series'
      })
      return
    }
    if (view.value === 'constituents') {
      const res = await fuyaoIndex({ view: 'constituents', indexCode: indexCode.value })
      apply(() => {
        rows.value = res.items
        note.value = res.note
        asOf.value = res.asOf
        mode.value = res.items.length === 0 ? 'empty' : 'generic'
      })
      return
    }
    if (view.value === 'memberships') {
      const res = await fuyaoIndex({ view: 'memberships', codes: codes.value })
      apply(() => {
        rows.value = res.items.map((r) => {
          const list = Array.isArray(r.index_list) ? r.index_list : []
          const names = list
            .map((x) => (typeof x === 'object' && x !== null ? (x as FuyaoRecord) : null))
            .map((x) => (x && typeof x.name === 'string' ? x.name : ''))
            .filter((x) => x !== '')
          return { thscode: r.thscode, ticker: r.ticker, name: r.name, index_list: names }
        })
        note.value = res.note
        asOf.value = res.asOf
        mode.value = res.items.length === 0 ? 'empty' : 'generic'
      })
      return
    }
    const res = await fuyaoIndex({ view: 'snapshot', codes: [indexCode.value] })
    apply(() => {
      rows.value = res.items
      note.value = res.note
      asOf.value = res.asOf
      mode.value = res.items.length === 0 ? 'empty' : 'generic'
    })
    return
  }

  if (domain.value === 'fund') {
    const res = await fuyaoFund({ view: view.value, thscode: fundCode.value, limit: 30 })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      if (res.permission === 'client') {
        mode.value = 'permission'
        return
      }
      if (res.items.length > 0) {
        rows.value = res.items
        dataObj.value = res.data
        mode.value = 'generic'
        return
      }
      if (res.data) {
        dataObj.value = res.data
        mode.value = 'record'
        return
      }
      mode.value = 'empty'
    })
    return
  }

  if (domain.value === 'derivative') {
    if (view.value.startsWith('futures')) {
      const futKey = view.value.replace('futures-', '')
      const res = await fuyaoFutures(FUTURES_VIEW_MAP[futKey] ?? 'varieties', { thscode: futCode.value, date: futDate.value, start_date: futDate.value, end_date: futDate.value })
      apply(() => {
        note.value = res.note
        asOf.value = res.asOf
        if (res.permission === 'client') {
          mode.value = 'permission'
          return
        }
        rows.value = res.items
        dataObj.value = res.data
        mode.value = res.items.length > 0 ? 'generic' : res.data ? 'record' : 'empty'
      })
      return
    }
    const res = await fuyaoOptions(view.value.replace('options-', ''), { thscode: optCode.value })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      if (res.permission === 'client') {
        mode.value = 'permission'
        return
      }
      rows.value = res.items
      dataObj.value = res.data
      mode.value = res.items.length > 0 ? 'generic' : res.data ? 'record' : 'empty'
    })
    return
  }

  if (domain.value === 'news') {
    const res = await fuyaoNews({
      view: view.value,
      tagId: Number(newsTagId.value) || 1,
      seq: newsSeq.value,
      code: newsCode.value,
      date: dateValue.value,
    })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      if (res.permission === 'client') {
        mode.value = 'permission'
        return
      }
      if (res.items.length > 0) {
        rows.value = res.items
        mode.value = view.value === 'anomaly' ? 'generic' : 'news'
        return
      }
      if (res.data) {
        dataObj.value = res.data
        mode.value = 'record'
        return
      }
      mode.value = 'empty'
    })
    return
  }

  if (domain.value === 'ipo') {
    const res = await fuyaoIpo({
      view: view.value,
      market: ipoMarket.value,
      status: ipoStatus.value,
      issueCode: 'HK0988',
      limit: 30,
    })
    apply(() => {
      note.value = res.note
      asOf.value = res.asOf
      if (res.permission === 'client') {
        mode.value = 'permission'
        return
      }
      if (res.items.length > 0) {
        rows.value = res.items
        dataObj.value = res.data
        mode.value = 'generic'
        return
      }
      if (res.data) {
        dataObj.value = res.data
        mode.value = 'record'
        return
      }
      mode.value = 'empty'
    })
    return
  }

  // tools
  if (view.value === 'calendar') {
    const days = await fuyaoCalendar('', '')
    apply(() => {
      calendarDays.value = days
      note.value = 'date 为 yyyyMMdd；date_ms 为当日零点毫秒戳。'
      mode.value = days.length === 0 ? 'empty' : 'calendar'
    })
    return
  }
  if (view.value === 'catalog') {
    await ensureCatalog()
    apply(() => {
      note.value = '目录是离线常量：124 个端点，分组与 ID 与 docs/FUYAO-API.md 一致；点击端点可跳到通用查询。'
      mode.value = (catalog.value?.endpoints.length ?? 0) === 0 ? 'empty' : 'catalog'
    })
    return
  }
  if (view.value === 'dump') {
    const res = await fuyaoMarketDump(dumpKind.value)
    apply(() => {
      dataObj.value = res.data
      note.value = res.note
      asOf.value = res.asOf
      mode.value = res.data ? 'dump' : 'empty'
    })
    return
  }
  // query
  await loadQuery(apply)
}

/** 目录懒加载一次：切换分域不重复请求（§7 要求）。 */
async function ensureCatalog(): Promise<void> {
  if (catalog.value !== null || catalogState.value === 'loading') return
  catalogState.value = 'loading'
  try {
    catalog.value = await fuyaoCatalog()
    catalogState.value = 'ready'
  } catch (e) {
    catalogState.value = 'failed'
    error.value = describeFuyaoError(e)
  }
}

/** 通用查询：保留原始 JSON 供「原始 JSON」切换（表格用归一后的记录）。 */
async function loadQuery(apply: (fn: () => void) => void): Promise<void> {
  await ensureCatalog()
  let params: Record<string, string> = {}
  queryParamsError.value = ''
  const text = queryParams.value.trim()
  if (text !== '' && text !== '{}') {
    try {
      const parsed: unknown = JSON.parse(text)
      const r = typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? (parsed as Record<string, unknown>) : null
      if (!r) throw new Error('params 必须是 JSON 对象')
      for (const [k, v] of Object.entries(r)) {
        if (typeof v === 'string' || typeof v === 'number') params[k] = String(v)
      }
    } catch (e) {
      queryParamsError.value = e instanceof Error ? e.message : 'params JSON 解析失败'
      throw new Error(queryParamsError.value)
    }
  }
  const raw = await invoke<unknown>('fuyao', 'query', { endpoint: queryEndpoint.value, params, limit: 30 })
  const env = toEnvelope(raw)
  apply(() => {
    rawJson.value = JSON.stringify(raw, null, 2)
    rows.value = env.items
    dataObj.value = env.data
    note.value = env.note
    asOf.value = env.asOf
    dataTimestamp.value = env.dataTimestamp
    endpointPath.value = queryEndpoint.value
    mode.value = env.permission === 'client' ? 'permission' : env.items.length > 0 ? 'query' : env.data ? 'record' : 'empty'
  })
}

// ---------- 目录浏览器 ----------

/** 目录按搜索词与分组过滤。 */
const catalogList = computed<FuyaoEndpoint[]>(() => {
  const list = catalog.value?.endpoints ?? []
  const q = catalogQuery.value.trim().toLowerCase()
  return list.filter((e) => {
    if (catalogGroup.value !== '' && e.group !== catalogGroup.value) return false
    if (q === '') return true
    return e.id.toLowerCase().includes(q) || e.path.toLowerCase().includes(q) || e.title.includes(q)
  })
})

/** 目录里出现过的分组（稳定顺序）。 */
const catalogGroups = computed<string[]>(() => catalog.value?.groups ?? [])

/** 点击目录行：切到通用查询并带上端点。 */
function useEndpoint(ep: FuyaoEndpoint): void {
  queryEndpoint.value = ep.id
  const required = ep.params.filter((p) => p.required).map((p) => p.name)
  queryParams.value = required.length > 0 ? JSON.stringify({ [required[0] ?? '']: '600519.SH' }, null, 2) : '{}'
  viewByDomain.value = { ...viewByDomain.value, tools: 'query' }
  domain.value = 'tools'
  resetData()
  void load(true)
}

/** 目录行里的端点 ID 快捷展示。 */
function endpointLabel(ep: FuyaoEndpoint): string {
  return ep.permission === 'client' ? '客户端专用' : ep.limit > 0 ? '≤' + ep.limit : '公开'
}

// ---------- 复制 / 打开 ----------

/** 复制端点路径（工具条与导出链接共用）。 */
async function copyPath(text: string): Promise<void> {
  if (text === '') return
  const ok = await copyText(text)
  toast(ok ? '已复制：' + text : '剪贴板不可用，请手动复制：' + text, ok ? 'ok' : 'warn')
}

/** 打开导出链接（浏览器完成下载，应用不代下）。 */
function openDumpUrl(): void {
  const url = typeof dataObj.value?.presigned_url === 'string' ? dataObj.value.presigned_url : ''
  if (url === '') return
  if (!openExternal(url)) void copyPath(url)
}

// ---------- 通用表格列 ----------

/** 当前视图优先展示的字段（其余字段自动补齐，最多 12 列）。 */
function preferredColumns(): string[] {
  const key = domain.value + '/' + view.value
  // 跌停/炸板池复用同一张表，但字段是涨停池的子集（没有连板/封单额）
  if (key === 'limit/pool' && poolType.value !== 'up') {
    return ['thscode', 'name', 'last_price', 'price_change_ratio_pct', 'first_limit_time', 'last_limit_time', 'open_times', 'turnover_ratio_pct', 'turnover']
  }
  const map: Record<string, string[]> = {
    'limit/pool': ['thscode', 'name', 'last_price', 'price_change_ratio_pct', 'continue_day_text', 'seal_money', 'open_times', 'limit_up_reason'],
    'auction/benchmark': ['date', 'limit_up_count', 'limit_down_count', 'avg_auction_pct', 'yesterday_limit_up_premium_pct'],
    'auction/opportunities': ['thscode', 'name', 'trade_date', 'auction_pct', 'auction_amount', 'auction_volume_ratio', 'warn_type', 'result_pct'],
    'special/anomaly': ['thscode', 'name', 'trade_date', 'tag_name', 'description', 'timestamp'],
    'special/anomaly-stock': ['thscode', 'name', 'trade_date', 'tag_name', 'reason'],
    'special/abnormal': ['thscode', 'name', 'trade_date', 'monitor_type', 'description', 'monitor_start', 'monitor_end'],
    'special/dragon-tiger': ['thscode', 'name', 'buy_value', 'sell_value', 'net_value', 'org_net_value', 'hot_money_net_value', 'net_rate', 'limit_reason', 'concept_list'],
    'special/dragon-tiger-institution': ['thscode', 'trade_date', 'institution_name', 'buy_value', 'sell_value', 'net_value', 'range_days'],
    'special/department-history': ['thscode', 'name', 'trade_date', 'buy_value', 'sell_value', 'net_value', 'next_day_pct'],
    'special/chip-shape': ['thscode', 'name', 'date', 'shape', 'concentration', 'profit_ratio_pct', 'avg_cost'],
    'special/new-concepts': ['name', 'thscode', 'date', 'stock_count', 'description'],
    'financial/income': ['period', 'operating_income', 'operating_costs', 'operating_profit', 'profit_total', 'net_profit', 'parent_holder_net_profit'],
    'financial/balance': ['period', 'assets_total', 'total_current_assets', 'cash', 'total_debt', 'holder_equity_total'],
    'financial/cash': ['period', 'act_cash_flow_net', 'invest_cash_flow_net', 'financing_cash_flow_net', 'cash_equivalents_net_addition'],
    'index/catalog': ['name', 'thscode'],
    'index/constituents': ['thscode', 'name', 'weight'],
    'index/memberships': ['thscode', 'name', 'index_list'],
    'index/snapshot': ['thscode', 'name', 'last_price', 'price_change_ratio_pct', 'volume', 'turnover'],
    'fund/snapshot': ['thscode', 'last_price', 'price_change_ratio_pct', 'volume', 'turnover'],
    'fund/nav': ['nav_date', 'unit_nav', 'adj_nav'],
    'fund/returns': ['return_week', 'return_month', 'return_tyear', 'return_year', 'return_tmonth', 'peer_average_month'],
    'fund/holdings': ['ticker', 'stock_name', 'hold_ratio', 'position_capital', 'position_count', 'period_increase_rate_pct', 'investment_rank'],
    'fund/asset-allocation': ['report_date_ms', 'stock_ratio_pct', 'bond_ratio_pct', 'deposit_ratio_pct', 'other_ratio_pct'],
    'fund/performance': ['date_ms', 'manager_return_pct', 'peer_return_pct', 'benchmark_return_pct'],
    'derivative/futures-varieties': ['variety_code', 'name', 'exchange_code', 'contract_multiplier', 'price_unit', 'tick_size', 'margin_rate'],
    'derivative/futures-main': ['thscode', 'name', 'variety_name', 'exchange_code', 'statistics_date', 'minute_visit_count', 'kline_visit_count'],
    'derivative/futures-positions': ['variety_code', 'variety_name', 'date', 'volume', 'long_position', 'short_position', 'net_position'],
    'derivative/futures-basis': ['thscode', 'name', 'reference_site', 'spot_price', 'converted_spot_price', 'basis', 'basis_rate_pct'],
    'derivative/futures-session': ['thscode', 'trade_date', 'timezone', 'trade_stages'],
    'derivative/options-varieties': ['variety_code', 'name', 'exchange_code', 'contract_multiplier', 'price_unit', 'tick_size'],
    'derivative/options-contracts': ['thscode', 'name', 'variety_code', 'list_date', 'last_trade_date', 'margin_rate'],
    'news/flash': ['title', 'summary', 'source', 'publish_time'],
    'news/topic': ['title', 'summary', 'source', 'publish_time'],
    'news/stock-timeline': ['title', 'summary', 'source', 'publish_time'],
    'news/anomaly': ['thscode', 'name', 'trade_date', 'reason'],
    'ipo/a-ipo': ['thscode', 'name', 'market', 'status', 'issue_price', 'subscription_start_ms', 'subscription_end_ms', 'listing_date'],
    'ipo/hk-calendar': ['issue_code', 'name', 'status', 'issue_price_range', 'lot_size', 'entry_fee', 'listing_date'],
    'ipo/hk-application': ['issue_code', 'name', 'apply_status', 'apply_amount', 'margin_ratio', 'sponsor'],
    'tools/query': ['thscode', 'name', 'last_price', 'price_change_ratio_pct', 'volume', 'turnover', 'date', 'date_ms'],
  }
  if (domain.value === 'special' && view.value === 'turnover') return []
  return map[key] ?? []
}

/** 通用表格列（当前 rows 推断）；期货成交/持仓单位是手，不按股展示。 */
const columns = computed<FuyaoColumn[]>(() => {
  const list = inferColumns(rows.value, preferredColumns(), 12)
  if (domain.value === 'derivative') {
    return list.map((col) => (col.key === 'volume' ? { ...col, label: '成交量(手)', format: 'number' as const } : col))
  }
  return list
})

/** 记录型视图（基金资料/公告/成交额预测）的键值对。 */
const recordEntries = computed<Array<{ key: string; label: string; value: string }>>(() => {
  const r = dataObj.value
  if (!r) return []
  return Object.keys(r)
    .filter((k) => r[k] !== undefined && r[k] !== null)
    .map((k) => ({ key: k, label: labelOf(k), value: formatCell(r, { key: k, label: labelOf(k), format: inferColumns([r], [k], 1)[0]?.format ?? 'text' }) }))
})

/** 龙虎榜游资席位（data.hot_money_items）。 */
const hotMoneyRows = computed<FuyaoRecord[]>(() => {
  const r = dataObj.value
  if (!r || !Array.isArray(r.hot_money_items)) return []
  return r.hot_money_items.map((x) => (typeof x === 'object' && x !== null ? (x as FuyaoRecord) : {})).filter((x) => Object.keys(x).length > 0)
})

/** 当前表格是否应显示（generic/query）。 */
const tableRows = computed<FuyaoRecord[]>(() => (mode.value === 'catalog' ? catalogList.value.map(endpointToRow) : rows.value))

/** 目录列表行 → 表格记录（与通用表格共用渲染）。 */
function endpointToRow(ep: FuyaoEndpoint): FuyaoRecord {
  return {
    id: ep.id,
    title: ep.title,
    group: ep.group,
    permission: ep.permission === 'client' ? '客户端专用' : '公开',
    limit: ep.limit,
    path: ep.path,
  }
}

/** 指数目录行点击：把该指数设为成分股/K线的标的。 */
function useIndexCode(row: FuyaoRecord): void {
  const code = typeof row.thscode === 'string' ? row.thscode : ''
  if (code === '') return
  indexCode.value = code
  if (view.value === 'catalog') {
    setView('constituents')
    return
  }
  void load(false)
}

/** K 线序列的收盘价（Sparkline 入参）。 */
function closesOf(s: FuyaoSeries): Array<number | null> {
  return s.bars.map((b) => b.close)
}

/** 序列最新一根的涨跌幅（相对前一根收盘）。 */
function seriesChangePct(s: FuyaoSeries): number {
  const last = s.bars[s.bars.length - 1]
  const prev = s.bars[s.bars.length - 2]
  if (!last || !prev || prev.close <= 0) return 0
  return ((last.close - prev.close) / prev.close) * 100
}

/** 表格列方向 class（红涨绿跌，只对涨跌幅/金额列生效）。 */
function dirOf(row: FuyaoRecord, col: FuyaoColumn): 'up' | 'down' | 'flat' {
  return cellDirection(row, col)
}

/** 新闻列表的时间戳（毫秒）。 */
function newsTime(row: FuyaoRecord): string {
  const ms = typeof row.publish_time === 'number' ? row.publish_time : 0
  return ms > 0 ? fmtStamp(ms) : '—'
}

/** 新闻正文：优先 summary，其次 content（通用层已截断到 600 字）。 */
function newsText(row: FuyaoRecord): string {
  const s = typeof row.summary === 'string' ? row.summary : ''
  if (s !== '') return s
  return typeof row.content === 'string' ? row.content.slice(0, 220) : ''
}

/** 涨跌停梯队按连板数分组。 */
const limitGroups = computed<Array<{ boards: number; text: string; items: FuyaoLimitRow[] }>>(() => {
  const map = new Map<number, FuyaoLimitRow[]>()
  for (const row of limitRows.value) {
    const boards = row.boards > 0 ? row.boards : 1
    const list = map.get(boards) ?? []
    list.push(row)
    map.set(boards, list)
  }
  return Array.from(map.entries())
    .sort((a, b) => b[0] - a[0])
    .map(([boards, items]) => ({ boards, text: items[0]?.boardsText || (boards === 1 ? '首板' : boards + '连板'), items }))
})

/** 涨停池情绪条：封板率、开板家数、涨停家数。 */
const limitMood = computed<Array<{ label: string; value: string; foot: string; tone: string }>>(() => {
  const s = limitStats.value
  if (!s) return []
  return [
    { label: '封板率', value: fmtRatio(s.sealRate), foot: '0~1 比例 × 100', tone: 'accent' },
    { label: '涨停家数', value: s.limitUpStockCount === null ? '—' : String(s.limitUpStockCount), foot: '收盘口径', tone: 'up' },
    { label: '触板家数', value: s.touchedLimitUpStockCount === null ? '—' : String(s.touchedLimitUpStockCount), foot: '盘中最高', tone: 'flat' },
    { label: '开板家数', value: s.openStockCount === null ? '—' : String(s.openStockCount), foot: '炸板次数合计', tone: 'down' },
    { label: '精确榜单', value: s.exactBoards === null ? '—' : String(s.exactBoards), foot: 'trade_date ' + fmtTradeDate(s.tradeDate), tone: 'flat' },
  ]
})

/** 资金流四档（元）：渲染用。 */
const flowSides = computed<Array<{ label: string; inflow: number; outflow: number; net: number }>>(() => {
  const f = flowRow.value
  if (!f) return []
  return [
    { label: '超大单', inflow: f.superLarge.inflowYuan, outflow: f.superLarge.outflowYuan, net: f.superLarge.netYuan },
    { label: '大单', inflow: f.large.inflowYuan, outflow: f.large.outflowYuan, net: f.large.netYuan },
    { label: '中单', inflow: f.medium.inflowYuan, outflow: f.medium.outflowYuan, net: f.medium.netYuan },
    { label: '小单', inflow: f.small.inflowYuan, outflow: f.small.outflowYuan, net: f.small.netYuan },
  ]
})

/** 交易日历按月份分组（展示用）。 */
const calendarMonths = computed<Array<{ month: string; days: FuyaoCalendarDay[] }>>(() => {
  const groups: Array<{ month: string; days: FuyaoCalendarDay[] }> = []
  for (const day of calendarDays.value) {
    const month = day.date.length === 8 ? day.date.slice(0, 4) + '-' + day.date.slice(4, 6) : '未知'
    const found = groups.find((g) => g.month === month)
    if (found) found.days.push(day)
    else groups.push({ month, days: [day] })
  }
  return groups
})

// ---------- 刷新调度 ----------

let timer: number | null = null

/** 重新排下一拍刷新：节律为 0 或页面隐藏时不排。 */
function schedule(): void {
  if (timer !== null) window.clearTimeout(timer)
  timer = null
  if (hidden.value) return
  const ms = cadenceMs()
  if (ms <= 0) return
  timer = window.setTimeout(() => {
    if (hidden.value) {
      schedule()
      return
    }
    void load(false)
  }, ms)
}

/** 手动刷新（保留旧数据，失败时展示上次成功结果 + 错误提示）。 */
function manualRefresh(): void {
  if (loading.value || refreshing.value) return
  void load(false)
}

/** 页面可见性变化：隐藏时暂停，恢复时重新按节律排。 */
function onVisibility(): void {
  hidden.value = document.hidden
  if (hidden.value) {
    // 页面隐藏：停表，不再向后端发请求（恢复可见时重新按节律排）
    if (timer !== null) window.clearTimeout(timer)
    timer = null
    return
  }
  schedule()
}

onMounted(() => {
  document.addEventListener('visibilitychange', onVisibility)
  void load(true)
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibility)
  if (timer !== null) window.clearTimeout(timer)
  if (suggestTimer !== null) window.clearTimeout(suggestTimer)
})

// 切 K 线参数 / 指数 / 基金等参数时重新取数（避免刷新按钮才生效）
watch([klineInterval, klineAdjust], () => {
  if (domain.value === 'market' && view.value === 'kline') void load(false)
})
watch(indexTag, () => {
  if (domain.value === 'index' && view.value === 'catalog') void load(false)
})
watch([poolType, hotKind, hotPeriod], () => {
  if (domain.value === 'limit' || domain.value === 'hot') void load(false)
})
watch(dumpKind, () => {
  if (domain.value === 'tools' && view.value === 'dump') void load(false)
})
watch(refreshMode, () => schedule())
</script>

<template>
  <section class="card fy">
    <div class="card-hd">
      <span class="card-title">数据源 · 同花顺官方</span>
      <span class="card-sub">{{ domainDef.title }} / {{ viewLabel }} · {{ codes.length }} 个标的</span>
      <span class="tag tag-info">124 端点目录</span>
      <span class="spacer" />
      <span class="tiny muted">{{ cadenceText }}</span>
      <button class="btn btn-sm" type="button" :disabled="loading || refreshing" @click="manualRefresh">
        {{ refreshing ? '刷新中…' : '刷新' }}
      </button>
    </div>

    <!-- 顶部工具条：标的输入 + 多选 + 刷新节律 + 口径 -->
    <div class="fy-bar">
      <div class="fy-picker">
        <div class="fy-picker-row">
          <input
            v-model="codeInput"
            class="input fy-code-input"
            type="text"
            placeholder="代码/名称，如 600519 / 茅台 / 000001.SH"
            aria-label="标的输入"
            @input="onCodeInput"
            @focus="suggestOpen = true"
            @keydown.enter.prevent="onCodeEnter"
          />
          <button class="btn btn-sm" type="button" @click="fillFromWatchlist">用自选股填充</button>
        </div>
        <div v-if="suggestOpen && (searching || suggestions.length > 0)" class="fy-suggest">
          <div v-if="searching" class="tiny muted fy-suggest-hint">检索中…</div>
          <button
            v-for="s in suggestions"
            :key="s.thscode"
            class="fy-suggest-item"
            type="button"
            @click="pickSuggestion(s)"
          >
            <span class="mono">{{ s.ticker }}</span>
            <span>{{ s.name }}</span>
            <span class="tiny muted">{{ s.assetType || s.exchange }}</span>
          </button>
        </div>
      </div>
      <div class="fy-chips">
        <button v-for="c in codes" :key="c" class="chip fy-chip" type="button" :title="'移除 ' + c" @click="removeCode(c)">
          <span class="mono">{{ c }}</span>
          <span class="fy-chip-x">×</span>
        </button>
        <span v-if="codes.length === 0" class="tiny muted">未选标的：请在上方加入</span>
      </div>
      <div class="fy-bar-right">
        <label class="tiny muted" for="fy-refresh">刷新</label>
        <select id="fy-refresh" v-model="refreshMode" class="select fy-refresh">
          <option value="auto">自动（按域）</option>
          <option value="3s">3 秒</option>
          <option value="30s">30 秒</option>
          <option value="60s">60 秒</option>
          <option value="off">仅手动</option>
        </select>
      </div>
    </div>

    <!-- 口径与端点路径 -->
    <div class="fy-meta">
      <span class="tiny muted">口径</span>
      <span class="tiny">成交量=股（另给手）· 金额=元 · 涨跌幅=百分数原值 · seal_rate=0~1</span>
      <span class="fy-meta-sep" />
      <span class="tiny muted">数据时间</span>
      <span class="tiny mono">{{ asOf ? fmtDateTime(asOf) : '—' }}</span>
      <template v-if="dataTimestamp">
        <span class="fy-meta-sep" />
        <span class="tiny muted">就绪</span>
        <span class="tiny mono">{{ dataTimestamp }}</span>
      </template>
      <span class="fy-meta-sep" />
      <span class="tiny muted">端点</span>
      <button class="fy-path mono" type="button" :title="'复制 ' + endpointPath" @click="copyPath(endpointPath)">
        {{ endpointPath || '—' }}
      </button>
      <span v-if="updatedAt" class="tiny muted fy-updated">本地刷新 {{ fmtClock(updatedAt) }}</span>
    </div>

    <!-- 分域页签 -->
    <div class="tabs fy-tabs">
      <button
        v-for="d in DOMAINS"
        :key="d.key"
        class="tab"
        :class="{ on: domain === d.key }"
        type="button"
        :title="d.hint"
        @click="setDomain(d.key)"
      >
        {{ d.title }}
      </button>
    </div>

    <!-- 二级视图 + 视图级参数 -->
    <div class="fy-sub">
      <span class="seg fy-seg">
        <button
          v-for="v in viewList"
          :key="v.key"
          class="seg-item"
          :class="{ on: view === v.key }"
          type="button"
          @click="setView(v.key)"
        >
          {{ v.label }}
        </button>
      </span>

      <template v-if="domain === 'market'">
        <template v-if="view === 'kline'">
          <span class="seg fy-seg">
            <button class="seg-item" :class="{ on: klineInterval === '1d' }" type="button" @click="klineInterval = '1d'">日</button>
            <button class="seg-item" :class="{ on: klineInterval === '1w' }" type="button" @click="klineInterval = '1w'">周</button>
            <button class="seg-item" :class="{ on: klineInterval === '1M' }" type="button" @click="klineInterval = '1M'">月</button>
          </span>
          <span class="seg fy-seg">
            <button class="seg-item" :class="{ on: klineAdjust === 'forward' }" type="button" @click="klineAdjust = 'forward'">前复权</button>
            <button class="seg-item" :class="{ on: klineAdjust === 'none' }" type="button" @click="klineAdjust = 'none'">不复权</button>
          </span>
        </template>
      </template>

      <template v-if="domain === 'limit'">
        <span class="seg fy-seg">
          <button class="seg-item" :class="{ on: poolType === 'up' }" type="button" @click="poolType = 'up'">涨停池</button>
          <button class="seg-item" :class="{ on: poolType === 'down' }" type="button" @click="poolType = 'down'">跌停池</button>
          <button class="seg-item" :class="{ on: poolType === 'break' }" type="button" @click="poolType = 'break'">炸板池</button>
        </span>
        <input v-model="dateValue" class="input fy-date" type="date" aria-label="交易日" />
      </template>

      <template v-if="domain === 'hot'">
        <span class="seg fy-seg">
          <button class="seg-item" :class="{ on: hotKind === 'hot' }" type="button" @click="hotKind = 'hot'">热股榜</button>
          <button class="seg-item" :class="{ on: hotKind === 'skyrocket' }" type="button" @click="hotKind = 'skyrocket'">飙升榜</button>
          <button class="seg-item" :class="{ on: hotKind === 'history' }" type="button" @click="hotKind = 'history'">历史榜</button>
        </span>
        <select v-if="hotKind !== 'history'" v-model="hotPeriod" class="select fy-refresh">
          <option value="day">日榜</option>
          <option value="hour">小时榜</option>
        </select>
        <input v-if="hotKind === 'history'" v-model="dateValue" class="input fy-date" type="date" aria-label="历史日期" />
      </template>

      <template v-if="domain === 'auction'">
        <input v-model="dateValue" class="input fy-date" type="date" aria-label="竞价日期" />
      </template>

      <template v-if="domain === 'special'">
        <input v-if="view === 'chip-shape'" v-model="shape" class="input fy-short" type="text" placeholder="形态，如 单峰密集" />
        <input v-if="view === 'department-record' || view === 'department-history'" v-model="departmentId" class="input fy-short" type="text" placeholder="营业部 ID" />
        <input v-model="dateValue" class="input fy-date" type="date" aria-label="日期" />
      </template>

      <template v-if="domain === 'flow'">
        <input v-model="flowCode" class="input fy-short" type="text" placeholder="个股 thscode" />
        <select v-if="view === 'history'" v-model="flowInterval" class="select fy-refresh">
          <option value="1d">日线</option>
          <option value="1m">分钟</option>
        </select>
      </template>

      <template v-if="domain === 'financial'">
        <span class="tiny muted">标的 {{ codes[0] || '—' }}</span>
        <input v-if="view === 'indicators'" v-model="reportPeriod" class="input fy-short" type="text" placeholder="报告期 yyyy-1..4" />
      </template>

      <template v-if="domain === 'index'">
        <select v-model="indexTag" class="select fy-refresh">
          <option value="cn_concept">概念</option>
          <option value="industry">行业</option>
          <option value="region">地域</option>
          <option value="tszs">特色</option>
        </select>
        <input v-model="indexCode" class="input fy-short" type="text" placeholder="指数 thscode，如 885431.TI" />
      </template>

      <template v-if="domain === 'fund'">
        <input v-model="fundCode" class="input fy-short" type="text" placeholder="基金代码，如 110011.OF" />
        <select v-model="fundView" class="select fy-refresh" aria-label="基金视图">
          <option v-for="v in viewList" :key="v.key" :value="v.key">{{ v.label }}</option>
        </select>
      </template>

      <template v-if="domain === 'derivative'">
        <template v-if="view.startsWith('futures')">
          <select v-model="futView" class="select fy-refresh">
            <option value="varieties">品种</option>
            <option value="contracts">合约</option>
            <option value="main">主力</option>
            <option value="positions-variety">持仓</option>
            <option value="basis">基差</option>
            <option value="session">时间轴</option>
            <option value="intraday">分时</option>
            <option value="kline">日 K</option>
            <option value="fundamentals">F10 指标</option>
          </select>
          <input v-model="futCode" class="input fy-short" type="text" placeholder="合约 thscode" />
          <input v-model="futDate" class="input fy-date" type="date" aria-label="持仓日期" />
        </template>
        <template v-else>
          <select v-model="optView" class="select fy-refresh">
            <option value="varieties">品种</option>
            <option value="contracts">合约</option>
            <option value="session">时间轴</option>
            <option value="intraday">分时</option>
            <option value="kline">日 K</option>
          </select>
          <input v-model="optCode" class="input fy-short" type="text" placeholder="期权 thscode" />
        </template>
      </template>

      <template v-if="domain === 'news'">
        <input v-model="newsTagId" class="input fy-tiny" type="text" placeholder="tag_id" />
        <input v-model="newsSeq" class="input fy-tiny" type="text" placeholder="seq" />
        <input v-if="view === 'stock-timeline'" v-model="newsCode" class="input fy-short" type="text" placeholder="个股 thscode" />
        <input v-if="view === 'anomaly'" v-model="dateValue" class="input fy-date" type="date" aria-label="日期" />
      </template>

      <template v-if="domain === 'tools' && view === 'dump'">
        <span class="seg fy-seg">
          <button class="seg-item" :class="{ on: dumpKind === '10y' }" type="button" @click="dumpKind = '10y'">十年日 K</button>
          <button class="seg-item" :class="{ on: dumpKind === '10d' }" type="button" @click="dumpKind = '10d'">十日 K</button>
          <button class="seg-item" :class="{ on: dumpKind === 'adjust' }" type="button" @click="dumpKind = 'adjust'">复权因子</button>
        </span>
      </template>

      <template v-if="domain === 'ipo'">
        <template v-if="view === 'a-ipo'">
          <select v-model="ipoMarket" class="select fy-refresh">
            <option value="sh_sz">沪深</option>
            <option value="bj">京市</option>
          </select>
          <select v-model="ipoStatus" class="select fy-refresh">
            <option value="subscribable">可申购</option>
            <option value="upcoming">待申购</option>
            <option value="awaiting_listing">待上市</option>
          </select>
        </template>
      </template>

      <span class="spacer" />
      <span v-if="mode === 'generic' || mode === 'query'" class="tiny muted">{{ tableRows.length }} 行</span>
    </div>

    <!-- 内容区 -->
    <div class="fy-body">
      <div v-if="loading" class="fy-skel">
        <div class="skeleton fy-skel-row" />
        <div class="skeleton fy-skel-row" />
        <div class="skeleton fy-skel-block" />
      </div>

      <StateBlock
        v-else-if="error && error.kind !== 'upstream' && error.kind !== 'permission'"
        kind="error"
        :title="error.title"
        :message="error.message"
        action-text="重试"
        @action="manualRefresh"
      />

      <!-- 客户端专用（2004）：说明 + 替代口径入口 -->
      <div v-else-if="mode === 'permission'" class="fy-notice">
        <div class="fy-notice-hd">
          <span class="tag tag-accent">客户端专用</span>
          <span class="fy-notice-title">该能力需要同花顺 AI 客户端</span>
        </div>
        <p class="fy-notice-text">
          官方返回 code=2004：外部 API Key 无权访问本接口，数据层已优雅降级（不计为错误、不重试）。
          <template v-if="note"> {{ note }}</template>
        </p>
        <div class="fy-notice-actions">
          <span class="tiny muted">替代口径：</span>
          <span class="tag">资金流 → 资金流面板（新浪）</span>
          <span class="tag">分时/高频 → K 线面板（腾讯/新浪分钟线）</span>
          <span class="tag">筹码/新增概念/成交额预测 → 暂无等价替代</span>
        </div>
      </div>

      <!-- 上游暂不可用（5003）：不是权限问题 -->
      <div v-else-if="error && error.kind === 'upstream'" class="fy-notice">
        <div class="fy-notice-hd">
          <span class="tag tag-warn">上游暂不可用</span>
          <span class="fy-notice-title">{{ error.title }}</span>
        </div>
        <p class="fy-notice-text">{{ error.message }}</p>
        <p class="fy-notice-text tiny muted">期货/期权合约、分时与时间轴仍可用；日 K 等上游恢复后会自动显示。</p>
        <button class="btn btn-sm" type="button" @click="manualRefresh">重试</button>
      </div>

      <!-- 空态 -->
      <StateBlock v-else-if="mode === 'empty'" kind="empty" :title="error?.title || '暂无数据'" :message="error?.message || note || '当前筛选条件下官方没有返回记录。'" />

      <!-- 行情：快照 + 估值 -->
      <div v-else-if="mode === 'quotes'" class="fy-stack">
        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">A 股行情快照</span>
            <span class="tiny muted">{{ quoteRows.length }} 只 · 官方 prices/snapshot</span>
          </div>
          <div v-if="quoteRows.length === 0" class="empty">官方快照没有返回记录</div>
          <div v-else class="table-wrap fy-table">
            <table class="tbl">
              <thead>
                <tr>
                  <th class="l">代码</th>
                  <th class="l">名称</th>
                  <th>现价</th>
                  <th>涨跌幅</th>
                  <th>成交量(股)</th>
                  <th>成交量(手)</th>
                  <th>成交额(元)</th>
                  <th>振幅</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in quoteRows" :key="row.thscode">
                  <td class="l mono tiny">{{ row.thscode }}</td>
                  <td class="l name">{{ row.name || row.ticker }}</td>
                  <td class="mono">{{ fmtPriceValue(row.lastPrice) }}</td>
                  <td class="mono" :class="dir(row.changePct)">{{ fmtPctValue(row.changePct) }}</td>
                  <td class="mono">{{ fmtShares(row.volumeShares) }}</td>
                  <td class="mono muted">{{ fmtLots(row.volumeShares) }}</td>
                  <td class="mono">{{ fmtYuan(row.turnoverYuan) }}</td>
                  <td class="mono muted">{{ row.prevClose > 0 ? fmtPctValue(((row.high - row.low) / row.prevClose) * 100) : '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="fy-foot tiny muted">口径：volume 单位股（官方原值），括号列按 1 手 = 100 股折算；turnover 单位元；涨跌幅为百分数原值。</div>
        </div>

        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">估值快照</span>
            <span class="tiny muted">{{ valuationRows.length }} 只 · PE/PB/PS/PCF 为倍数</span>
          </div>
          <div v-if="valuationRows.length === 0" class="empty">估值暂不可用</div>
          <div v-else class="table-wrap fy-table">
            <table class="tbl">
              <thead>
                <tr>
                  <th class="l">代码</th>
                  <th class="l">名称</th>
                  <th>PE(TTM)</th>
                  <th>PE(MRQ)</th>
                  <th>PB(MRQ)</th>
                  <th>PS(TTM)</th>
                  <th>PCF(TTM)</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in valuationRows" :key="row.thscode">
                  <td class="l mono tiny">{{ row.thscode }}</td>
                  <td class="l name">{{ row.name || row.ticker }}</td>
                  <td class="mono">{{ fmtMultiple(row.peTtm) }}</td>
                  <td class="mono muted">{{ fmtMultiple(row.peMrq) }}</td>
                  <td class="mono">{{ fmtMultiple(row.pbMrq) }}</td>
                  <td class="mono muted">{{ fmtMultiple(row.psTtm) }}</td>
                  <td class="mono muted">{{ fmtMultiple(row.pcfTtm) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="fy-foot tiny muted">口径：比率为「倍」，不是百分数；负值表示亏损或净资产为负。</div>
        </div>
      </div>

      <!-- K 线：轻量 SVG/Sparkline，不引入图表库 -->
      <div v-else-if="mode === 'series'" class="fy-stack">
        <div v-if="series.length === 0" class="empty">官方没有返回 K 线（可能停牌/代码不在支持范围内）</div>
        <div v-for="s in series" :key="s.thscode" class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title mono">{{ s.thscode }}</span>
            <span class="tag">{{ s.interval || klineInterval }}</span>
            <span class="tag">{{ s.adjust === 'forward' ? '前复权' : '不复权' }}</span>
            <span class="tiny muted">{{ s.bars.length }} 根</span>
            <span class="spacer" />
            <template v-if="s.bars.length > 1">
              <span class="mono" :class="dir(seriesChangePct(s))">{{ fmtPctValue(seriesChangePct(s)) }}</span>
              <span class="tiny muted">最新 {{ s.bars[s.bars.length - 1]?.date }}</span>
            </template>
          </div>
          <div v-if="s.bars.length === 0" class="empty">序列为空（官方合法空组）</div>
          <template v-else>
            <div class="fy-series">
              <Sparkline :values="closesOf(s)" :width="880" :height="120" />
              <div class="fy-series-stats">
                <span class="tiny muted">收盘</span>
                <span class="mono">{{ fmtPrice(s.bars[s.bars.length - 1]?.close) }}</span>
                <span class="tiny muted">量(股)</span>
                <span class="mono">{{ fmtShares(s.bars[s.bars.length - 1]?.volumeShares) }}</span>
                <span class="tiny muted">额(元)</span>
                <span class="mono">{{ fmtYuan(s.bars[s.bars.length - 1]?.turnoverYuan) }}</span>
              </div>
            </div>
            <div class="table-wrap fy-table fy-table-sm">
              <table class="tbl">
                <thead>
                  <tr>
                    <th class="l">日期</th>
                    <th>开盘</th>
                    <th>最高</th>
                    <th>最低</th>
                    <th>收盘</th>
                    <th>成交量(股)</th>
                    <th>成交额(元)</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="bar in s.bars.slice(-8)" :key="bar.date">
                    <td class="l mono tiny">{{ bar.date }}</td>
                    <td class="mono">{{ fmtPriceValue(bar.open) }}</td>
                    <td class="mono">{{ fmtPriceValue(bar.high) }}</td>
                    <td class="mono">{{ fmtPriceValue(bar.low) }}</td>
                    <td class="mono" :class="dir(bar.close - bar.open)">{{ fmtPriceValue(bar.close) }}</td>
                    <td class="mono">{{ fmtShares(bar.volumeShares) }}</td>
                    <td class="mono muted">{{ fmtYuan(bar.turnoverYuan) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </template>
        </div>
        <div class="fy-foot tiny muted">口径：date_ms 为毫秒戳；volume 单位股、turnover 单位元；官方指数/基金仅支持日线（1d）。</div>
      </div>

      <!-- 涨跌停：情绪条 + 梯队表 -->
      <div v-else-if="mode === 'limits'" class="fy-stack">
        <div class="fy-metrics">
          <div v-for="m in limitMood" :key="m.label" class="metric fy-metric">
            <span class="metric-label">{{ m.label }}</span>
            <span class="metric-value" :class="m.tone">{{ m.value }}</span>
            <span class="metric-foot">{{ m.foot }}</span>
          </div>
        </div>
        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">涨停梯队</span>
            <span class="tiny muted">{{ limitRows.length }} 只 · 按连板数分组（官方 continue_day_cnt）</span>
          </div>
          <div v-if="limitGroups.length === 0" class="empty">今日无涨停（非交易日请核对日期）</div>
          <div v-for="g in limitGroups" :key="g.boards" class="fy-ladder">
            <div class="fy-ladder-hd">
              <span class="tag tag-up">{{ g.text }}</span>
              <span class="tiny muted">{{ g.items.length }} 只</span>
            </div>
            <div class="table-wrap fy-table">
              <table class="tbl">
                <thead>
                  <tr>
                    <th class="l">代码</th>
                    <th class="l">名称</th>
                    <th>现价</th>
                    <th>涨跌幅</th>
                    <th>封板时间</th>
                    <th>开板次数</th>
                    <th>封单额(元)</th>
                    <th class="l">涨停原因</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in g.items" :key="row.thscode">
                    <td class="l mono tiny">{{ row.thscode }}</td>
                    <td class="l name">{{ row.name }}</td>
                    <td class="mono">{{ fmtPriceValue(row.lastPrice) }}</td>
                    <td class="mono" :class="dir(row.changePct)">{{ fmtPctValue(row.changePct) }}</td>
                    <td class="mono tiny">{{ row.limitTime || row.firstTime || '—' }}</td>
                    <td class="mono">{{ row.openTimes === null ? '—' : row.openTimes }}</td>
                    <td class="mono">{{ fmtYuan(row.sealMoneyYuan) }}</td>
                    <td class="l fy-wide">{{ row.reason || '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
        <div class="fy-foot tiny muted">口径：封单额/峰值封单额单位元；open_times 为开板次数（可能为 null）；seal_rate 是 0~1 比例，不按百分数格式化。</div>
      </div>

      <!-- 涨跌停：情绪统计 -->
      <div v-else-if="mode === 'stats'" class="fy-stack">
        <div class="fy-metrics">
          <div v-for="m in limitMood" :key="m.label" class="metric fy-metric">
            <span class="metric-label">{{ m.label }}</span>
            <span class="metric-value" :class="m.tone">{{ m.value }}</span>
            <span class="metric-foot">{{ m.foot }}</span>
          </div>
        </div>
        <div class="fy-block" v-if="limitStats">
          <div class="fy-block-hd">
            <span class="fy-block-title">原始字段（官方 limit-up-statistics）</span>
            <span class="tiny muted">交易日 {{ fmtTradeDate(limitStats.tradeDate) }}</span>
          </div>
          <div class="fy-kv">
            <div class="fy-kv-row"><span class="fy-kv-k">seal_rate</span><span class="fy-kv-v mono">{{ limitStats.sealRate === null ? '—' : limitStats.sealRate }}</span><span class="tiny muted">0~1 比例</span></div>
            <div class="fy-kv-row"><span class="fy-kv-k">open_stock_count</span><span class="fy-kv-v mono">{{ limitStats.openStockCount ?? '—' }}</span><span class="tiny muted">开板家数</span></div>
            <div class="fy-kv-row"><span class="fy-kv-k">limit_up_stock_count</span><span class="fy-kv-v mono">{{ limitStats.limitUpStockCount ?? '—' }}</span><span class="tiny muted">涨停家数</span></div>
            <div class="fy-kv-row"><span class="fy-kv-k">touched_limit_up_stock_count</span><span class="fy-kv-v mono">{{ limitStats.touchedLimitUpStockCount ?? '—' }}</span><span class="tiny muted">触板家数</span></div>
            <div class="fy-kv-row"><span class="fy-kv-k">page_boards / exact_boards</span><span class="fy-kv-v mono">{{ limitStats.pageBoards ?? '—' }} / {{ limitStats.exactBoards ?? '—' }}</span><span class="tiny muted">榜单口径差异</span></div>
          </div>
        </div>
      </div>

      <!-- 热榜 -->
      <div v-else-if="mode === 'hot'" class="fy-stack">
        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">{{ hotTitle }}</span>
            <span class="tiny muted">{{ hotRows.length }} 只 · heat 为上游热度字符串</span>
          </div>
          <div v-if="hotRows.length === 0" class="empty">暂无热榜数据</div>
          <div v-else class="table-wrap fy-table">
            <table class="tbl">
              <thead>
                <tr>
                  <th>排名</th>
                  <th class="l">代码</th>
                  <th class="l">名称</th>
                  <th>热度</th>
                  <th>排名变化</th>
                  <th class="l">趋势</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in hotRows" :key="row.thscode">
                  <td class="mono muted">{{ row.rank }}</td>
                  <td class="l mono tiny">{{ row.thscode }}</td>
                  <td class="l name">{{ row.name }}</td>
                  <td class="mono">{{ row.heat || '—' }}</td>
                  <td class="mono" :class="dir(row.rankChange)">{{ row.rankChange === null ? '—' : row.rankChange > 0 ? '+' + row.rankChange : String(row.rankChange) }}</td>
                  <td class="l tiny">{{ trendText(row.rankTrend) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="fy-foot tiny muted">口径：heat 是上游热度字符串（不是金额，不做亿/万格式化）；rank_change 正数表示名次上升。</div>
        </div>
      </div>

      <!-- 竞价快照 -->
      <div v-else-if="mode === 'auction'" class="fy-stack">
        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">集合竞价快照</span>
            <span class="tag tag-info">{{ auctionStatus === 'ready' ? '已就绪' : auctionStatus === 'not_ready' ? '未开始' : '官方快照' }}</span>
            <span class="tiny muted">{{ auctionRows.length }} 只 · 9:15–9:25 每 5s 刷新</span>
          </div>
          <div v-if="auctionRows.length === 0" class="empty">竞价未开始（data_status=not_ready）</div>
          <div v-else class="table-wrap fy-table">
            <table class="tbl">
              <thead>
                <tr>
                  <th class="l">代码</th>
                  <th class="l">名称</th>
                  <th>竞价</th>
                  <th>竞价涨幅</th>
                  <th>竞价量(股)</th>
                  <th>竞价额(元)</th>
                  <th>量比</th>
                  <th>昨日量比</th>
                  <th>未匹配量</th>
                  <th>流通市值(元)</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in auctionRows" :key="row.thscode">
                  <td class="l mono tiny">{{ row.thscode }}</td>
                  <td class="l name">{{ row.name }}</td>
                  <td class="mono">{{ fmtPriceValue(row.auctionPrice) }}</td>
                  <td class="mono" :class="dir(row.auctionPct)">{{ fmtPctValue(row.auctionPct) }}</td>
                  <td class="mono">{{ fmtShares(row.auctionVolumeShares) }}</td>
                  <td class="mono">{{ fmtYuan(row.auctionAmountYuan) }}</td>
                  <td class="mono muted">{{ row.volumeRatio.toFixed(2) }}</td>
                  <td class="mono muted">{{ fmtPctValue(row.yesterdayRatioPct) }}</td>
                  <td class="mono muted">{{ row.auctionUnmatched.toFixed(0) }}</td>
                  <td class="mono muted">{{ fmtYuan(row.floatMarketCapYuan) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="fy-foot tiny muted">口径：auction_pct / auction_turnover_pct / auction_yesterday_ratio_pct 为百分数原值；auction_volume 单位股；auction_amount / float_market_cap 单位元。</div>
        </div>
      </div>

      <!-- 资金流快照 -->
      <div v-else-if="mode === 'flow'" class="fy-stack">
        <div class="fy-block" v-if="flowRow">
          <div class="fy-block-hd">
            <span class="fy-block-title mono">{{ flowRow.thscode }}</span>
            <span class="tiny muted">主力净额 = 超大单 + 大单</span>
            <span class="spacer" />
            <span class="mono" :class="dir(flowRow.mainNetYuan)">{{ fmtSignedYuan(flowRow.mainNetYuan) }}</span>
          </div>
          <div class="table-wrap fy-table">
            <table class="tbl">
              <thead>
                <tr>
                  <th class="l">档位</th>
                  <th>流入(元)</th>
                  <th>流出(元)</th>
                  <th>净额(元)</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="side in flowSides" :key="side.label">
                  <td class="l">{{ side.label }}</td>
                  <td class="mono">{{ fmtYuan(side.inflow) }}</td>
                  <td class="mono">{{ fmtYuan(side.outflow) }}</td>
                  <td class="mono" :class="dir(side.net)">{{ fmtSignedYuan(side.net) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="fy-foot tiny muted">口径：金额单位元；数据来自官方 capital-flow/snapshot（若外部 Key 无权限会显示客户端专用说明）。</div>
        </div>
      </div>

      <!-- 财务指标 -->
      <div v-else-if="mode === 'abilities'" class="fy-stack">
        <div v-for="group in abilities" :key="group.ability" class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">{{ group.ability }}</span>
            <span class="tiny muted">{{ group.indicators.length }} 个指标 · value 为上游原始字符串</span>
          </div>
          <div class="fy-abilities">
            <div v-for="ind in group.indicators" :key="ind.indexId" class="fy-ability">
              <span class="fy-ability-name">{{ ind.indexId }}</span>
              <span class="mono fy-ability-value">{{ ind.value === '' ? '--' : ind.value }}</span>
            </div>
          </div>
        </div>
        <div class="fy-foot tiny muted">口径：百分比类指标按百分数表达（89.12 = 89.12%）；value 缺失（官方 null）显示 --。</div>
      </div>

      <!-- 资讯 -->
      <div v-else-if="mode === 'news'" class="fy-stack">
        <div v-for="(row, i) in rows" :key="i" class="fy-news">
          <div class="fy-news-hd">
            <span class="fy-news-title">{{ String(row.title ?? '（无标题）') }}</span>
            <span class="spacer" />
            <span class="tiny muted">{{ newsTime(row) }}</span>
            <span v-if="typeof row.source === 'string'" class="tag">{{ row.source }}</span>
          </div>
          <p class="fy-news-text">{{ newsText(row) }}</p>
          <div class="fy-news-foot tiny muted">
            <span v-if="typeof row.thscode === 'string'" class="mono">{{ row.thscode }}</span>
            <span v-if="typeof row.is_kuaixun === 'boolean'" class="tag">{{ row.is_kuaixun ? '快讯' : '资讯' }}</span>
          </div>
        </div>
        <div class="fy-foot tiny muted">口径：publish_time 为毫秒时间戳；正文在通用层截断到 600 字；快讯首屏 seq=0。</div>
      </div>

      <!-- 记录型（基金资料/公告/成交额预测/营业部战绩） -->
      <div v-else-if="mode === 'record'" class="fy-stack">
        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">{{ domainDef.title }} / {{ viewLabel }}</span>
            <span class="tiny muted">官方返回对象型 data（字段名与文档逐字一致）</span>
          </div>
          <div v-if="recordEntries.length === 0" class="empty">没有可展示字段</div>
          <div v-else class="fy-kv">
            <div v-for="row in recordEntries" :key="row.key" class="fy-kv-row">
              <span class="fy-kv-k mono">{{ row.key }}</span>
              <span class="fy-kv-v">{{ row.value }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 导出链接 -->
      <div v-else-if="mode === 'dump'" class="fy-stack">
        <div class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">全市场 Parquet 导出</span>
            <span class="tag">presigned_url 有时效</span>
          </div>
          <div class="fy-kv">
            <div class="fy-kv-row">
              <span class="fy-kv-k">presigned_url</span>
              <span class="fy-kv-v mono fy-break">{{ typeof dataObj?.presigned_url === 'string' ? dataObj.presigned_url : '—' }}</span>
            </div>
          </div>
          <div class="fy-notice-actions">
            <button class="btn btn-sm" type="button" @click="copyPath(typeof dataObj?.presigned_url === 'string' ? dataObj.presigned_url : '')">复制链接</button>
            <button class="btn btn-sm" type="button" @click="openDumpUrl">浏览器打开</button>
          </div>
          <div class="fy-foot tiny muted">下载域名 o.thsi.cn 不在应用网络白名单内，只展示链接、不代下载。</div>
        </div>
      </div>

      <!-- 交易日历 -->
      <div v-else-if="mode === 'calendar'" class="fy-stack">
        <div v-for="g in calendarMonths" :key="g.month" class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">{{ g.month }}</span>
            <span class="tiny muted">{{ g.days.length }} 个交易日</span>
          </div>
          <div class="fy-cal">
            <div v-for="d in g.days" :key="d.date" class="fy-cal-day">
              <span class="mono">{{ fmtTradeDate(d.date) }}</span>
              <span class="tiny muted">{{ fmtStamp(d.dateMs) }}</span>
            </div>
          </div>
        </div>
        <div class="fy-foot tiny muted">口径：date 为 yyyyMMdd；date_ms 为当日零点毫秒戳（Asia/Shanghai 展示）。</div>
      </div>

      <!-- 目录浏览器 -->
      <div v-else-if="mode === 'catalog'" class="fy-stack">
        <div class="fy-catalog-bar">
          <input v-model="catalogQuery" class="input fy-catalog-search" type="search" placeholder="搜索端点 ID / 路径 / 标题" />
          <select v-model="catalogGroup" class="select fy-refresh">
            <option value="">全部分组</option>
            <option v-for="g in catalogGroups" :key="g" :value="g">{{ g }}</option>
          </select>
          <span class="tiny muted">官方 {{ catalog?.total ?? 124 }} 个端点 · 演示目录 {{ catalog?.count ?? 0 }} 个</span>
          <span class="spacer" />
          <span class="tiny muted">点击行 → 通用查询</span>
        </div>
        <div class="table-wrap fy-table fy-table-tall">
          <table class="tbl">
            <thead>
              <tr>
                <th class="l">端点 ID / 路径</th>
                <th class="l">标题</th>
                <th class="l">分组</th>
                <th>权限</th>
                <th>上限</th>
                <th class="l">口径备注</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="ep in catalogList"
                :key="ep.id"
                class="fy-row"
                role="button"
                tabindex="0"
                @click="useEndpoint(ep)"
                @keydown.enter.prevent="useEndpoint(ep)"
              >
                <td class="l fy-endpoint-cell">
                  <div class="mono tiny">{{ ep.id }}</div>
                  <div class="mono fy-endpoint-path">{{ ep.path }}</div>
                </td>
                <td class="l name">{{ ep.title }}</td>
                <td class="l"><span class="tag">{{ ep.group }}</span></td>
                <td class="tiny">{{ endpointLabel(ep) }}</td>
                <td class="mono tiny">{{ ep.limit > 0 ? ep.limit : '—' }}</td>
                <td class="l tiny muted fy-note-cell">{{ ep.notes || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="catalogList.length === 0" class="empty">没有匹配的端点：清空搜索或换分组</div>
        <div class="fy-foot tiny muted">目录是离线常量，切换分域不会重复请求；真实环境返回 124 个端点，演示模式每域只列常用端点。</div>
      </div>

      <!-- 通用查询 -->
      <div v-else-if="mode === 'query' || mode === 'generic'" class="fy-stack">
        <div v-if="domain === 'tools'" class="fy-query">
          <div class="fy-query-row">
            <label class="tiny muted" for="fy-endpoint">端点</label>
            <select id="fy-endpoint" v-model="queryEndpoint" class="select fy-query-endpoint">
              <option v-for="ep in (catalog?.endpoints ?? [])" :key="ep.id" :value="ep.id">{{ ep.group }} · {{ ep.id }}</option>
            </select>
            <button class="btn btn-sm btn-accent" type="button" :disabled="refreshing" @click="manualRefresh">执行</button>
            <span class="spacer" />
            <button class="btn btn-sm" type="button" :aria-pressed="queryRawMode" @click="queryRawMode = !queryRawMode">
              {{ queryRawMode ? '表格视图' : '原始 JSON' }}
            </button>
          </div>
          <div class="fy-query-row">
            <label class="tiny muted" for="fy-params">params</label>
            <textarea id="fy-params" v-model="queryParams" class="textarea fy-query-params" rows="2" spellcheck="false" />
          </div>
          <div v-if="queryParamsError" class="hint-warn">params 解析失败：{{ queryParamsError }}</div>
        </div>

        <template v-if="domain === 'tools' && queryRawMode">
          <div class="fy-block">
            <div class="fy-block-hd">
              <span class="fy-block-title">原始 JSON</span>
              <span class="tiny muted">{{ rawJson.length }} 字符 · endpoint={{ queryEndpoint }}</span>
            </div>
            <pre class="fy-raw mono">{{ rawJson || '（无返回）' }}</pre>
          </div>
        </template>
        <template v-else>
          <div v-if="tableRows.length === 0" class="empty">没有记录（合法空结果）：请核对参数是否在该端点可用范围内</div>
          <div v-else class="table-wrap fy-table fy-table-tall">
            <table class="tbl">
              <thead>
                <tr>
                  <th v-for="col in columns" :key="col.key" :class="{ l: col.format === 'text' || col.format === 'wide' || col.format === 'date' }">
                    {{ col.label }}
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="(row, i) in tableRows"
                  :key="i"
                  :class="{ 'fy-row': domain === 'index' && view === 'catalog' }"
                  @click="domain === 'index' && view === 'catalog' ? useIndexCode(row) : undefined"
                >
                  <td
                    v-for="col in columns"
                    :key="col.key"
                    class="mono"
                    :class="[col.format === 'text' || col.format === 'wide' || col.format === 'date' ? 'l' : '', dirOf(row, col)]"
                  >
                    {{ formatCell(row, col) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>

        <!-- 龙虎榜游资席位（对象型 data 里的第二个数组） -->
        <div v-if="domain === 'special' && view === 'dragon-tiger' && hotMoneyRows.length > 0" class="fy-block">
          <div class="fy-block-hd">
            <span class="fy-block-title">游资/机构席位</span>
            <span class="tiny muted">{{ hotMoneyRows.length }} 条 · data.hot_money_items</span>
          </div>
          <div class="table-wrap fy-table">
            <table class="tbl">
              <thead>
                <tr>
                  <th class="l">营业部</th>
                  <th>买入(元)</th>
                  <th>卖出(元)</th>
                  <th>净额(元)</th>
                  <th>涉及个股</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, i) in hotMoneyRows" :key="i">
                  <td class="l name">{{ String(row.department_name ?? '—') }}</td>
                  <td class="mono">{{ fmtYuan(typeof row.buy_value === 'number' ? row.buy_value : undefined) }}</td>
                  <td class="mono">{{ fmtYuan(typeof row.sell_value === 'number' ? row.sell_value : undefined) }}</td>
                  <td class="mono" :class="dir(typeof row.net_value === 'number' ? row.net_value : 0)">{{ fmtSignedYuan(typeof row.net_value === 'number' ? row.net_value : undefined) }}</td>
                  <td class="mono tiny">{{ row.stock_count ?? '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="fy-foot tiny muted">
          口径：表格按官方字段名映射中文表头；成交量列单位股、金额列单位元、`_pct` 列为百分数原值、`seal_rate` 为 0~1 比例；缺失字段显示 —。
          <template v-if="note"> {{ note }}</template>
        </div>
      </div>

      <div v-if="showTailNote" class="fy-foot tiny muted">{{ note }}</div>
    </div>
  </section>
</template>

<style scoped>
  /* 面板局部样式：只作用于「数据源」面板，避免改动全局 token 与既有选择器。 */
  .fy {
    min-height: 0;
  }
  .fy-bar {
    display: flex;
    align-items: flex-start;
    flex-wrap: wrap;
    gap: 10px;
    padding: 10px 14px 8px;
    border-bottom: 1px solid var(--line);
  }
  .fy-picker {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 320px;
  }
  .fy-picker-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .fy-code-input {
    width: 300px;
  }
  .fy-suggest {
    position: absolute;
    top: 100%;
    left: 0;
    z-index: 5;
    width: 320px;
    margin-top: 4px;
    padding: 4px;
    border: 1px solid var(--line-2);
    border-radius: var(--radius-sm);
    background: var(--glass-3);
    box-shadow: var(--shadow-2);
    backdrop-filter: blur(var(--blur-2));
  }
  .fy-suggest-hint {
    padding: 6px 8px;
  }
  .fy-suggest-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    border: 0;
    border-radius: var(--radius-xs);
    background: transparent;
    color: var(--text-2);
    font-size: 12px;
    text-align: left;
  }
  .fy-suggest-item:hover {
    background: var(--panel-2);
    color: var(--text);
  }
  .fy-chips {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    max-width: 640px;
  }
  .fy-chip {
    height: 24px;
    font-size: 11px;
  }
  .fy-chip-x {
    color: var(--faint);
  }
  .fy-chip:hover .fy-chip-x {
    color: var(--up);
  }
  .fy-bar-right {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-left: auto;
  }
  .fy-refresh {
    height: 26px;
    font-size: 11.5px;
  }
  .fy-meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    padding: 6px 14px;
    border-bottom: 1px solid var(--line);
    background: rgba(255, 255, 255, 0.012);
  }
  .fy-meta-sep {
    width: 1px;
    height: 12px;
    background: var(--line-2);
    margin: 0 2px;
  }
  .fy-path {
    max-width: 420px;
    padding: 2px 6px;
    border: 1px dashed var(--line-2);
    border-radius: var(--radius-xs);
    background: transparent;
    color: var(--text-2);
    font-size: 11px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .fy-path:hover {
    border-color: var(--accent);
    color: var(--accent-2);
  }
  .fy-updated {
    margin-left: auto;
  }
  .fy-tabs {
    height: 34px;
    overflow-x: auto;
    flex-wrap: nowrap;
  }
  .fy-tabs .tab {
    height: 27px;
    white-space: nowrap;
    flex: 0 0 auto;
  }
  .fy-sub {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px 14px;
    border-bottom: 1px solid var(--line);
  }
  .fy-seg {
    flex-wrap: wrap;
  }
  .fy-date,
  .fy-short,
  .fy-tiny {
    height: 26px;
    font-size: 11.5px;
  }
  .fy-short {
    width: 160px;
  }
  .fy-tiny {
    width: 76px;
  }
  .fy-body {
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
    padding: 12px 14px 16px;
  }
  .fy-stack {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .fy-block {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .fy-block-hd {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .fy-block-title {
    font-size: 12px;
    font-weight: 600;
    color: var(--text);
  }
  .fy-metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: 8px;
  }
  .fy-metric {
    padding: 8px 10px;
  }
  .fy-table {
    max-height: 420px;
  }
  .fy-table-sm {
    max-height: 260px;
  }
  .fy-table-tall {
    max-height: 60vh;
  }
  .fy-wide {
    max-width: 360px;
    white-space: normal;
    line-height: 1.45;
  }
  .fy-note-cell {
    max-width: 420px;
    white-space: normal;
    line-height: 1.45;
  }
  .fy-endpoint-cell {
    max-width: 300px;
  }
  .fy-endpoint-path {
    color: var(--faint);
    font-size: 10.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .fy-row {
    cursor: pointer;
  }
  .fy-foot {
    padding: 2px 0;
    line-height: 1.6;
  }
  .fy-series {
    display: flex;
    align-items: flex-end;
    flex-wrap: wrap;
    gap: 16px;
  }
  .fy-series-stats {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    font-size: 12px;
  }
  .fy-ladder {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .fy-ladder-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .fy-notice {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px;
    border: 1px solid rgba(233, 180, 76, 0.28);
    border-radius: var(--radius-sm);
    background: rgba(233, 180, 76, 0.06);
  }
  .fy-notice-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .fy-notice-title {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text);
  }
  .fy-notice-text {
    margin: 0;
    color: var(--text-2);
    font-size: 12px;
    line-height: 1.7;
  }
  .fy-notice-actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .fy-kv {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .fy-kv-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 7px 10px;
    border-bottom: 1px solid rgba(148, 163, 184, 0.07);
    font-size: 12px;
  }
  .fy-kv-row:last-child {
    border-bottom: 0;
  }
  .fy-kv-k {
    flex: 0 0 220px;
    color: var(--muted);
    word-break: break-all;
  }
  .fy-kv-v {
    color: var(--text);
    word-break: break-all;
  }
  .fy-break {
    word-break: break-all;
  }
  .fy-abilities {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
    gap: 8px;
  }
  .fy-ability {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 10px;
    padding: 7px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .fy-ability-name {
    color: var(--text-2);
    font-size: 11.5px;
  }
  .fy-ability-value {
    color: var(--text);
    font-size: 13px;
  }
  .fy-news {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .fy-news-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .fy-news-title {
    color: var(--text);
    font-size: 12.5px;
    font-weight: 600;
  }
  .fy-news-text {
    margin: 0;
    color: var(--text-2);
    font-size: 12px;
    line-height: 1.65;
  }
  .fy-news-foot {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .fy-cal {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    gap: 6px;
  }
  .fy-cal-day {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 9px;
    border: 1px solid var(--line);
    border-radius: var(--radius-xs);
    background: var(--panel-2);
    font-size: 12px;
  }
  .fy-catalog-bar,
  .fy-query-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .fy-catalog-search {
    width: 300px;
  }
  .fy-query {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .fy-query-endpoint {
    width: 420px;
    max-width: 100%;
  }
  .fy-query-params {
    flex: 1 1 520px;
    min-height: 46px;
    font-family: var(--mono);
    font-size: 11.5px;
  }
  .fy-raw {
    max-height: 52vh;
    margin: 0;
    padding: 12px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg-2);
    color: var(--text-2);
    font-size: 11.5px;
    line-height: 1.6;
    overflow: auto;
    white-space: pre;
  }
  .fy-skel {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .fy-skel-row {
    height: 28px;
  }
  .fy-skel-block {
    height: 220px;
  }
</style>
