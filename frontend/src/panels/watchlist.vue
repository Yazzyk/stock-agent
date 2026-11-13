<script setup lang="ts">
// 自选股面板：数据状态条（本机同花顺 / 网页登录 / 手工文件）+ 授权向导 + 行情表 + 资金列 + 资金抽屉。
// 打开面板自动 check；ready 且自选为空时自动 sync；执行向导动作后每 2 秒轮询 check，授权成功即自动同步。
// 手动「同步同花顺」按钮保留为兜底。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Directive } from 'vue'
import IntradayModal from '../components/IntradayModal.vue'
import StateBlock from '../components/StateBlock.vue'
import {
  copyText,
  fiveDayMain,
  flowBatch,
  flowHistory,
  flowNarrative,
  flowStats,
  messageOf,
  minute as fetchMinute,
  openExternal,
  quotes,
  watchCaptureCookie,
  watchCheck,
  watchList,
  watchPrivacy,
  watchSaveCookie,
  watchSync,
  type FundFlowPoint,
  type PrivacyInfo,
  type SyncCheck,
  type SyncSource,
  type SyncStep,
} from '../lib/api.market'
import { boardTags, boardTitle } from '../lib/board'
import { CHART, drawFlowChart, drawSparkline, fitCanvas, type FlowChartLayout, type FlowVisible } from '../lib/chart'
import { dir, fmtAmount, fmtClock, fmtPct, fmtPrice, fmtSignedAmount } from '../lib/format'
import { focusStock, state, toast } from '../stores/app'
import type { FundFlow, MinuteSeries, Quote, SyncResult, WatchItem, WatchSnapshot } from '../types'

type ColKey = 'amount' | 'turnover' | 'main' | 'mainPct' | 'five' | 'spark'

const snapshot = ref<WatchSnapshot | null>(null)
const quoteMap = ref<Record<string, Quote>>({})
const flowMap = ref<Record<string, FundFlow>>({})
const detailMap = ref<Record<string, { spark: Array<number | null>; five: number }>>({})
const activeGroup = ref('全部')
const loading = ref(true)
const refreshing = ref(false)
const syncing = ref(false)
const error = ref('')
const syncResult = ref<SyncResult | null>(null)
const auto = ref(true)

// 数据源诊断
const check = ref<SyncCheck | null>(null)
const checkError = ref('')
const privacy = ref<PrivacyInfo | null>(null)
const wizardOpen = ref(true)
const polling = ref(false)
const pollCount = ref(0)
const pasteOpen = ref(false)
const cookieText = ref('')
const savingCookie = ref(false)

// 列设置
// 默认显示主力净额/主力占比（§3.2），5 日主力与迷你走势默认关/开按需求：5日默认关，走势默认开
const cols = ref<Record<ColKey, boolean>>({ amount: true, turnover: true, main: true, mainPct: true, five: false, spark: true })
const colMenu = ref(false)

// 资金抽屉
const drawer = ref<{ code: string; name: string } | null>(null)
const drawerPoints = ref<FundFlowPoint[]>([])
const drawerLoading = ref(false)
const drawerError = ref('')
const drawerRange = ref(20)
const drawerVisible = ref<FlowVisible>({ main: true, cumulative: true, close: true })
const drawerHover = ref<{ index: number | null; y: number | null }>({ index: null, y: null })
const dwCanvas = ref<HTMLCanvasElement | null>(null)
const dwWrap = ref<HTMLElement | null>(null)
const dwSize = ref({ w: 0, h: 0 })
let dwLayout: FlowChartLayout | null = null
let dwRo: ResizeObserver | null = null

let timer: number | null = null
let pollTimer: number | null = null
let quoteSeq = 0

interface DetailCacheEntry {
  at: number
  points: FundFlowPoint[]
}

const detailCache = new Map<string, DetailCacheEntry>()

// ---------- 分时（走势列 + 大图） ----------

const MINUTE_TTL = 60000
const minuteMap = ref<Record<string, { at: number; series: MinuteSeries | null }>>({})
let minuteInflight = false
let minuteRunAt = 0

const minuteModal = ref<{ code: string; name: string } | null>(null)
const modalSeries = ref<MinuteSeries | null>(null)
const modalLoading = ref(false)
const modalError = ref('')

interface SparkBinding {
  prices: Array<number | null>
  baseline: number | null
  up: boolean
  width: number
  height: number
}

/** 行内分时小图：按 DPR 画价格线，颜色按现价相对昨收（红涨绿跌）。 */
function drawMinuteSpark(el: HTMLCanvasElement, opt: SparkBinding): void {
  const dpr = window.devicePixelRatio || 1
  const w = opt.width
  const h = opt.height
  const pxW = Math.max(1, Math.round(w * dpr))
  const pxH = Math.max(1, Math.round(h * dpr))
  if (el.width !== pxW) el.width = pxW
  if (el.height !== pxH) el.height = pxH
  el.style.width = w + 'px'
  el.style.height = h + 'px'
  const ctx = el.getContext('2d')
  if (!ctx) return
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, w, h)
  drawSparkline(ctx, opt.prices, {
    width: w,
    height: h,
    baseline: opt.baseline,
    color: opt.up ? CHART.up : CHART.down,
  })
}

/** 分时迷你图的局部指令：不新增公共组件，避免改动 Sparkline 的既有签名。 */
const vSpark: Directive<HTMLCanvasElement, SparkBinding> = {
  mounted(el, binding) {
    drawMinuteSpark(el, binding.value)
  },
  updated(el, binding) {
    drawMinuteSpark(el, binding.value)
  },
}

/** 取缓存里的分时序列（展示用；过期也先展示，下一轮刷新会替换）。 */
function minuteSeriesOf(code: string): MinuteSeries | null {
  return minuteMap.value[code]?.series ?? null
}

/** 走势列用的价格序列（空数组表示暂无分时，渲染占位符）。 */
function minutePricesOf(code: string): Array<number | null> {
  const series = minuteSeriesOf(code)
  return series ? series.points.map((p) => p.price) : []
}

/** 现价相对昨收的方向，用于小图着色。 */
function minuteUp(code: string): boolean {
  const series = minuteSeriesOf(code)
  if (!series || series.points.length === 0) return true
  const last = series.points[series.points.length - 1]?.price ?? 0
  return series.prevClose <= 0 || last >= series.prevClose
}

/**
 * 拉取分时并写入缓存：单只失败静默（列里显示「—」），并发 ≤4，绝不阻塞行情表。
 * force=true 时忽略 TTL（大图重试）。
 */
async function loadMinutes(codes: string[], force = false): Promise<void> {
  if (codes.length === 0 || minuteInflight) return
  const now = Date.now()
  const need = codes.filter((code) => {
    if (force) return true
    const hit = minuteMap.value[code]
    return !hit || now - hit.at > MINUTE_TTL
  })
  if (need.length === 0) return
  minuteInflight = true
  const results: Record<string, { at: number; series: MinuteSeries | null }> = {}
  const queue = need.slice()
  try {
    const workers = Math.min(4, queue.length)
    await Promise.all(
      Array.from({ length: workers }, async () => {
        for (;;) {
          const code = queue.shift()
          if (!code) return
          try {
            const series = await fetchMinute(code)
            results[code] = { at: Date.now(), series }
          } catch {
            // 分时取不到不影响行情与资金列，保留旧图，60 秒后再试
            results[code] = { at: Date.now(), series: minuteMap.value[code]?.series ?? null }
          }
        }
      }),
    )
  } finally {
    minuteInflight = false
  }
  minuteMap.value = { ...minuteMap.value, ...results }
}

/** 行情刷新时顺带刷新分时：整体 60s 节流，避免 5s 轮询每次打满上游。 */
function refreshMinutes(codes: string[]): void {
  if (!cols.value.spark || codes.length === 0) return
  const now = Date.now()
  if (now - minuteRunAt < MINUTE_TTL) return
  minuteRunAt = now
  void loadMinutes(codes)
}

/** 打开分时大图：先用缓存立即出图，只有缓存缺失或过期时才回源刷新。 */
function openMinute(row: Row): void {
  minuteModal.value = { code: row.code, name: row.name }
  const hit = minuteMap.value[row.code]
  modalSeries.value = hit?.series ?? null
  modalError.value = ''
  if (!hit || Date.now() - hit.at > MINUTE_TTL) void loadModalMinute()
}

/** 大图取数：只有完全没有数据时才显示 loading，已有旧图时静默更新。 */
async function loadModalMinute(): Promise<void> {
  const target = minuteModal.value
  if (!target) return
  modalLoading.value = modalSeries.value === null
  modalError.value = ''
  try {
    const series = await fetchMinute(target.code)
    if (minuteModal.value?.code !== target.code) return
    modalSeries.value = series
    minuteMap.value = { ...minuteMap.value, [target.code]: { at: Date.now(), series } }
  } catch (e) {
    if (minuteModal.value?.code !== target.code) return
    // 已有旧图时静默保留，避免一次网络抖动把可读的分时替换成错误页
    if (modalSeries.value === null) modalError.value = messageOf(e)
  } finally {
    if (minuteModal.value?.code === target.code) modalLoading.value = false
  }
}

/** 关闭分时大图并清理临时状态。 */
function closeMinuteModal(): void {
  minuteModal.value = null
  modalSeries.value = null
  modalError.value = ''
  modalLoading.value = false
}

const groups = computed<string[]>(() => ['全部', ...(snapshot.value?.groups ?? [])])

interface Row {
  code: string
  name: string
  market: string
  group: string
  /** 板块标签（沪/深/创/科/北），来自 boardTags 的纯函数判定 */
  boards: string[]
  price?: number
  changePct?: number
  amount?: number
  turnover?: number
  stale: boolean
}

/**
 * 数字着色类：值缺失（渲染成「—」）返回 na（muted 灰，别把空值染白），
 * 有值时按方向返回 up / down / flat。现价的方向取自涨跌幅，所以 by 可显式传入。
 */
function paint(v: number | null | undefined, by?: number | null): 'up' | 'down' | 'flat' | 'na' {
  if (typeof v !== 'number' || !Number.isFinite(v)) return 'na'
  return dir(by === undefined ? v : by)
}

const rows = computed<Row[]>(() => {
  const items: WatchItem[] = snapshot.value?.items ?? []
  const order = snapshot.value?.groups ?? []
  const groupRank = (g: string): number => {
    const i = order.indexOf(g)
    return i >= 0 ? i : order.length
  }
  const list = items
    .filter((it) => activeGroup.value === '全部' || it.group === activeGroup.value)
    .sort((a, b) => (groupRank(a.group) === groupRank(b.group) ? a.order - b.order : groupRank(a.group) - groupRank(b.group)))
  return list.map((it) => {
    const q = quoteMap.value[it.code]
    return {
      code: it.code,
      name: it.name || q?.name || '—',
      market: it.market,
      group: it.group,
      boards: boardTags(it.code, it.market),
      price: q?.price ?? it.price,
      changePct: q?.changePct ?? it.changePct,
      amount: q?.amount ?? it.amount,
      turnover: q?.turnover ?? it.turnover,
      stale: Boolean(q?.stale ?? it.stale),
    }
  })
})

const staleCount = computed(() => rows.value.filter((r) => r.stale).length)
const sourceList = computed<SyncSource[]>(() => {
  const c = check.value
  if (!c) return []
  return [c.local, c.web, c.manual]
})
const dataDir = computed(() => privacy.value?.dataDir || '~/.guanlan')
/** 本机同花顺那一步：action.kind = open-settings。macOS 不允许程序自动加入完全磁盘访问，所以需要「打开设置 + 复制路径」两步。 */
const localStep = computed<SyncStep | null>(
  () => check.value?.steps.find((s) => s.action.kind === 'open-settings') ?? null,
)

const drawerStats = computed(() => flowStats(drawerPoints.value))
const drawerNarrative = computed(() => flowNarrative(drawerPoints.value))
const drawerView = computed(() => {
  const list = drawerPoints.value
  return list.slice(Math.max(0, list.length - drawerRange.value))
})
const drawerHoverPoint = computed(() => {
  const i = drawerHover.value.index
  if (i === null) return null
  return drawerView.value[i] ?? null
})

function stateDot(s: SyncSource): string {
  if (s.state === 'ok') return 'dot-ok'
  if (s.state === 'empty') return 'dot-warn'
  if (s.state === 'denied' || s.state === 'no-login') return 'dot-warn'
  return 'dot-off'
}

function stateText(s: SyncSource): string {
  switch (s.state) {
    case 'ok':
      return s.items !== undefined ? '可用 · ' + (s.items ?? 0) + ' 只' : '可用'
    case 'empty':
      return '未导入'
    case 'denied':
      return '未授权'
    case 'no-login':
      return '未登录'
    default:
      return '未找到'
  }
}

// ---------- 载入 ----------

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const snap = await watchList()
    snapshot.value = snap
    const codes = snap.items.map((i) => i.code)
    if (codes.length > 0) {
      // 行情与资金各一次批量：market.quote / fundflow.batch 都按代码数组一次取（≤50）。
      await Promise.all([refreshQuotes(), loadFlow(codes)])
      // 5 日主力默认隐藏：没有打开该列就不逐只拉多日资金历史（避免 N 次串行请求）
      if (cols.value.five) void loadDetails(codes)
      if (cols.value.spark) void loadMinutes(codes)
    } else {
      quoteMap.value = {}
      flowMap.value = {}
      detailMap.value = {}
      minuteMap.value = {}
    }
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function refreshQuotes(): Promise<void> {
  const items = snapshot.value?.items ?? []
  if (items.length === 0 || refreshing.value) return
  const seq = ++quoteSeq
  refreshing.value = true
  try {
    // 一次批量取全部行情（≤50）：逐只调用会把 50 只变成 50 个 RTT。
    // 代码必须带市场前缀：裸 code 会被误判交易所（指数 sh000001 尤其明显）。
    const list = await quotes(items.map((i) => i.market + i.code))
    if (seq !== quoteSeq) return
    const map: Record<string, Quote> = { ...quoteMap.value }
    for (const q of list) map[q.code] = q
    quoteMap.value = map
    error.value = ''
    refreshMinutes(items.map((i) => i.code))
  } catch (e) {
    if (seq !== quoteSeq) return
    error.value = messageOf(e)
  } finally {
    if (seq === quoteSeq) refreshing.value = false
  }
}

async function loadFlow(codes: string[]): Promise<void> {
  try {
    const batch = await flowBatch(codes)
    const map: Record<string, FundFlow> = {}
    for (const item of batch.items) map[item.code] = item
    flowMap.value = map
  } catch {
    flowMap.value = {}
  }
}

/** 缓存多日历史：保留更长的那份（抽屉要 60 日，列表只要 20 日）。 */
function storeDetail(code: string, points: FundFlowPoint[]): void {
  const prev = detailCache.get(code)
  if (prev && prev.points.length >= points.length && Date.now() - prev.at < 60000) return
  detailCache.set(code, { at: Date.now(), points })
}

/** 5 日主力与迷你走势来自多日历史；单只失败不影响整张表。并发 4，缓存 60 秒。 */
async function loadDetails(codes: string[]): Promise<void> {
  const now = Date.now()
  const need = codes.filter((c) => {
    const hit = detailCache.get(c)
    return !hit || now - hit.at > 60000
  })
  const queue = need.slice()
  const workers = Math.min(4, queue.length)
  await Promise.all(
    Array.from({ length: workers }, async () => {
      for (;;) {
        const code = queue.shift()
        if (!code) return
        try {
          const history = await flowHistory(code, 20)
          storeDetail(code, history.points)
        } catch {
          // 单只标的取不到多日历史时，列里显示占位符即可
        }
      }
    }),
  )
  const next: Record<string, { spark: Array<number | null>; five: number }> = {}
  for (const code of codes) {
    const hit = detailCache.get(code)
    if (!hit) continue
    next[code] = { spark: hit.points.map((p) => p.close ?? null), five: fiveDayMain(hit.points) }
  }
  detailMap.value = next
}

// ---------- 同步诊断与向导 ----------

async function runCheck(): Promise<void> {
  checkError.value = ''
  try {
    const res = await watchCheck()
    check.value = res
    if (!res.ready) wizardOpen.value = true
  } catch (e) {
    checkError.value = messageOf(e)
  }
}

async function loadPrivacy(): Promise<void> {
  try {
    privacy.value = await watchPrivacy()
  } catch {
    privacy.value = null
  }
}

let autoSynced = false

async function autoSync(reason: string): Promise<void> {
  if (autoSynced || syncing.value) return
  autoSynced = true
  toast(reason, 'info')
  await doSync()
}

async function doSync(): Promise<void> {
  syncing.value = true
  syncResult.value = null
  try {
    const res = await watchSync()
    syncResult.value = res
    if (res.success) {
      toast('同步完成：' + res.message, 'ok')
      await load()
      await runCheck()
    } else {
      // 失败后放开自动同步锁：用户按向导补完授权还能再来一次
      autoSynced = false
      toast('同步未完成：' + res.message, 'warn')
      wizardOpen.value = true
    }
  } catch (e) {
    syncResult.value = {
      success: false,
      message: messageOf(e),
      groups: [],
      added: 0,
      removed: 0,
      needWebLogin: false,
      needFullDiskAccess: false,
      warnings: [],
    }
    autoSynced = false
    toast('同步失败：' + messageOf(e), 'error')
    // 失败后重新诊断，用户能看到下一步该做什么
    await runCheck()
  } finally {
    syncing.value = false
  }
}

async function runStep(step: SyncStep): Promise<void> {
  const act = step.action
  switch (act.kind) {
    case 'open-url':
    case 'open-settings': {
      const fallback = act.kind === 'open-url' ? 'https://t.10jqka.com.cn/' : privacy.value?.settingsUrl ?? ''
      const url = act.value || fallback
      if (!url) {
        toast('没有可打开的地址：请手动登录同花顺网页', 'warn')
        return
      }
      const ok = openExternal(url)
      if (!ok) {
        await copyText(url)
        toast('浏览器预览模式：链接已复制，请在系统浏览器打开', 'warn')
      }
      startPoll('完成授权后会自动检测数据源…')
      return
    }
    case 'copy': {
      const target = act.value || dataDir.value
      const ok = await copyText(target)
      toast(ok ? '已复制：' + target : '剪贴板不可用，请手动复制：' + target, ok ? 'ok' : 'warn')
      return
    }
    case 'capture-cookie': {
      try {
        const res = await watchCaptureCookie()
        toast(res.message, res.ok ? 'ok' : 'warn')
        if (res.check) check.value = res.check
        if (res.ok) {
          if (res.check?.ready) void autoSync('登录态可用，正在自动同步…')
          else startPoll('已读取登录态，正在检测数据源…')
        }
      } catch (e) {
        toast(messageOf(e), 'error')
      }
      return
    }
    case 'paste-cookie': {
      openPaste()
      return
    }
    case 'retry-sync': {
      await doSync()
      return
    }
    default:
      return
  }
}

/** 复制应用路径：用户在「完全磁盘访问权限」列表里点 + 号粘贴即可。 */
async function copyAppPath(): Promise<void> {
  const path = privacy.value?.appPath ?? ''
  if (path === '') {
    toast('未获取到应用路径：可在 Finder 里找到「观澜.app」后拖入列表', 'warn')
    return
  }
  const ok = await copyText(path)
  toast(
    ok ? '已复制应用路径，粘贴到完全磁盘访问列表即可' : '剪贴板不可用，请手动复制：' + path,
    ok ? 'ok' : 'warn',
  )
}

function startPoll(reason: string): void {
  if (polling.value) return
  polling.value = true
  pollCount.value = 0
  toast(reason, 'info')
  pollTimer = window.setInterval(() => {
    void pollTick()
  }, 2000)
}

function stopPoll(): void {
  if (pollTimer !== null) window.clearInterval(pollTimer)
  pollTimer = null
  polling.value = false
}

async function pollTick(): Promise<void> {
  pollCount.value += 1
  if (pollCount.value > 60) {
    stopPoll()
    toast('等待授权超时：可点「重新检测」，或手动粘贴登录态', 'warn')
    return
  }
  try {
    const res = await watchCheck()
    check.value = res
    if (res.ready) {
      stopPoll()
      await autoSync('检测到可用数据源，正在自动同步…')
    }
  } catch {
    // 轮询期间的单次失败不打断，下一拍再试
  }
}

function openPaste(): void {
  pasteOpen.value = true
  cookieText.value = ''
}

async function saveCookie(): Promise<void> {
  savingCookie.value = true
  try {
    const res = await watchSaveCookie(cookieText.value)
    toast(res.message, res.ok ? 'ok' : 'warn')
    if (res.check) check.value = res.check
    if (res.ok) {
      pasteOpen.value = false
      cookieText.value = ''
      if (res.check?.ready) void autoSync('登录态已保存，正在自动同步…')
      else startPoll('登录态已保存，正在检测数据源…')
    }
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    savingCookie.value = false
  }
}

// ---------- 行操作 ----------

/** 行点击与「K线」按钮：静默跳转到 K 线面板（不弹 toast），焦点供 kline.vue 的 watch 载入。 */
function focusRow(row: Row): void {
  focusStock(row.code, row.name, { silent: true })
}

function openDrawer(row: Row): void {
  drawer.value = { code: row.code, name: row.name }
  drawerPoints.value = []
  drawerError.value = ''
  drawerHover.value = { index: null, y: null }
  void loadDrawer()
}

function closeDrawer(): void {
  drawer.value = null
  drawerPoints.value = []
  if (dwRo) {
    dwRo.disconnect()
    dwRo = null
  }
  dwLayout = null
}

async function loadDrawer(): Promise<void> {
  const d = drawer.value
  if (!d) return
  drawerLoading.value = true
  drawerError.value = ''
  try {
    const cached = detailCache.get(d.code)
    if (cached && Date.now() - cached.at < 60000 && cached.points.length >= 60) {
      drawerPoints.value = cached.points
    } else {
      const history = await flowHistory(d.code, 60)
      storeDetail(d.code, history.points)
      drawerPoints.value = history.points
    }
    await nextTick()
    attachDrawerChart()
  } catch (e) {
    drawerError.value = messageOf(e)
  } finally {
    drawerLoading.value = false
  }
}

function toggleDrawerSeries(key: keyof FlowVisible): void {
  drawerVisible.value = { ...drawerVisible.value, [key]: !drawerVisible.value[key] }
  dwSchedule()
}

function setDrawerRange(n: number): void {
  drawerRange.value = n
  dwSchedule()
}

// ---------- 抽屉图 ----------

let dwRaf = 0

function dwSchedule(): void {
  if (dwRaf !== 0) return
  dwRaf = window.requestAnimationFrame(() => {
    dwRaf = 0
    dwDraw()
  })
}

function dwDraw(): void {
  const canvas = dwCanvas.value
  if (!canvas) return
  const ctx = fitCanvas(canvas, dwSize.value.w, dwSize.value.h)
  if (!ctx) {
    dwLayout = null
    return
  }
  dwLayout = drawFlowChart(ctx, drawerView.value, {
    width: dwSize.value.w,
    height: dwSize.value.h,
    visible: drawerVisible.value,
    hoverIndex: drawerHover.value.index,
    hoverY: drawerHover.value.y,
  })
}

function dwMeasure(): void {
  const el = dwWrap.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const w = Math.max(0, Math.floor(rect.width))
  const h = Math.max(0, Math.floor(rect.height))
  if (w !== dwSize.value.w || h !== dwSize.value.h) dwSize.value = { w, h }
  dwSchedule()
}

function attachDrawerChart(): void {
  const el = dwWrap.value
  if (!el) return
  if (dwRo) dwRo.disconnect()
  dwRo = new ResizeObserver(() => dwMeasure())
  dwRo.observe(el)
  dwMeasure()
}

function dwMove(e: PointerEvent): void {
  const canvas = dwCanvas.value
  if (!canvas || !dwLayout) return
  const rect = canvas.getBoundingClientRect()
  const x = e.clientX - rect.left
  const index = Math.max(0, Math.min(dwLayout.indexAt(x), drawerView.value.length - 1))
  drawerHover.value = { index, y: e.clientY - rect.top }
  dwSchedule()
}

function dwLeave(): void {
  drawerHover.value = { index: null, y: null }
  dwSchedule()
}

// ---------- 自动刷新 ----------

function toggleAuto(): void {
  auto.value = !auto.value
  if (auto.value) startTimer()
  else stopTimer()
}

function startTimer(): void {
  stopTimer()
  if (!auto.value || document.hidden) return
  timer = window.setTimeout(() => {
    void refreshQuotes().finally(() => startTimer())
  }, quoteCadenceMs())
}

function stopTimer(): void {
  if (timer !== null) window.clearTimeout(timer)
  timer = null
}

/** 行情刷新节律：盘中 5s（后端快照 TTL 3s），非交易时段 60s；页面隐藏时不排下一拍。 */
function quoteCadenceMs(): number {
  const now = new Date()
  const day = now.getDay()
  const minutes = now.getHours() * 60 + now.getMinutes()
  const trading = day !== 0 && day !== 6 && ((minutes >= 9 * 60 + 30 && minutes <= 11 * 60 + 30) || (minutes >= 13 * 60 && minutes <= 15 * 60))
  return trading ? 5000 : 60000
}

/** 页面可见性变化：隐藏时停表（不耗后端 TTL），恢复时重排。 */
function onVisibility(): void {
  if (document.hidden) stopTimer()
  else if (auto.value) startTimer()
}

function toggleCol(key: ColKey): void {
  cols.value = { ...cols.value, [key]: !cols.value[key] }
}

function onDocDown(e: MouseEvent): void {
  if (!colMenu.value) return
  const target = e.target
  if (target instanceof Element && target.closest('.wl-colmenu-wrap')) return
  colMenu.value = false
}

watch(auto, (on) => {
  if (on) startTimer()
  else stopTimer()
})

watch(
  () => cols.value.five,
  (on) => {
    if (!on) return
    // 打开「5 日主力」列时才补拉多日历史（并发 ≤4，带 60s 缓存）
    void loadDetails((snapshot.value?.items ?? []).map((i) => i.code))
  },
)

watch(
  () => cols.value.spark,
  (on) => {
    if (!on) return
    minuteRunAt = Date.now()
    void loadMinutes((snapshot.value?.items ?? []).map((i) => i.code))
  },
)

watch(
  () => state.focus,
  (f) => {
    if (!f) return
    const item = (snapshot.value?.items ?? []).find((i) => i.code === f.code)
    if (item && activeGroup.value !== '全部' && activeGroup.value !== item.group) activeGroup.value = item.group
  },
)

onMounted(async () => {
  document.addEventListener('mousedown', onDocDown)
  document.addEventListener('visibilitychange', onVisibility)
  await load()
  startTimer()
  await runCheck()
  void loadPrivacy()
  if ((snapshot.value?.items.length ?? 0) === 0 && check.value?.ready) {
    await autoSync('自选为空，正在按已授权的数据源自动同步…')
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocDown)
  document.removeEventListener('visibilitychange', onVisibility)
  stopTimer()
  stopPoll()
  if (dwRo) dwRo.disconnect()
  if (dwRaf !== 0) window.cancelAnimationFrame(dwRaf)
})
</script>

<template>
  <section class="card wl">
    <div class="card-hd">
      <span class="card-title">自选股</span>
      <span class="card-sub">
        {{ snapshot?.items.length ?? 0 }} 只 · {{ snapshot?.groups.length ?? 0 }} 组
        <template v-if="snapshot?.updatedAt"> · 更新 {{ fmtClock(snapshot.updatedAt) }}</template>
      </span>
      <span v-if="snapshot && snapshot.source" class="tag">{{ snapshot.source }}</span>
      <span class="card-tools">
        <button class="btn btn-sm" type="button" :aria-pressed="auto" @click="toggleAuto">
          {{ auto ? '自动刷新 5s' : '已暂停' }}
        </button>
        <button class="btn btn-sm" type="button" :disabled="loading || refreshing" @click="refreshQuotes">刷新</button>
        <span class="wl-colmenu-wrap">
          <button class="btn btn-sm btn-ghost" type="button" :aria-expanded="colMenu" @click="colMenu = !colMenu">列</button>
          <div v-if="colMenu" class="wl-colmenu">
            <div class="wl-colmenu-hd">显示列</div>
            <label class="check"><input type="checkbox" :checked="cols.amount" @change="toggleCol('amount')" />成交额</label>
            <label class="check"><input type="checkbox" :checked="cols.turnover" @change="toggleCol('turnover')" />换手</label>
            <label class="check"><input type="checkbox" :checked="cols.main" @change="toggleCol('main')" />主力净额</label>
            <label class="check"><input type="checkbox" :checked="cols.mainPct" @change="toggleCol('mainPct')" />主力占比</label>
            <label class="check"><input type="checkbox" :checked="cols.five" @change="toggleCol('five')" />5日主力</label>
            <label class="check"><input type="checkbox" :checked="cols.spark" @change="toggleCol('spark')" />迷你走势</label>
          </div>
        </span>
        <button class="btn btn-sm btn-accent" type="button" :disabled="syncing" @click="doSync">
          {{ syncing ? '同步中…' : '同步同花顺' }}
        </button>
      </span>
    </div>

    <!-- 数据状态条 -->
    <div class="wl-status">
      <span v-if="checkError" class="hint-warn">检测失败：{{ checkError }}</span>
      <template v-else-if="sourceList.length > 0">
        <span v-for="src in sourceList" :key="src.id" class="wl-src" :title="src.detail || ''">
          <span class="dot" :class="stateDot(src)" aria-hidden="true" />
          <span>{{ src.label }}</span>
          <span class="tiny muted">{{ stateText(src) }}</span>
        </span>
        <span v-if="check" class="tag" :class="check.ready ? 'tag-info' : 'tag-accent'">
          {{ check.ready ? '可同步' : '待授权' }}
        </span>
      </template>
      <span v-else class="tiny muted">正在检测数据源…</span>
      <span class="spacer" />
      <span v-if="polling" class="tiny muted">等待授权… {{ pollCount }}/60</span>
      <span v-if="check" class="tiny muted">检测于 {{ fmtClock(check.checkedAt) }}</span>
      <button class="btn btn-sm btn-ghost" type="button" :disabled="polling" @click="runCheck">重新检测</button>
    </div>

    <!-- 授权向导 -->
    <div v-if="check && !check.ready" class="wl-wizard">
      <div class="wz-hd">
        <span class="wz-title">启用数据源</span>
        <span class="tiny muted grow">{{ check.message }}</span>
        <button class="btn btn-sm btn-ghost" type="button" @click="wizardOpen = !wizardOpen">
          {{ wizardOpen ? '收起' : '展开' }}
        </button>
      </div>
      <ol v-if="wizardOpen" class="wz-steps">
        <li v-for="step in check.steps" :key="step.id" class="wz-step" :class="{ done: step.done }">
          <span class="wz-mark" aria-hidden="true">{{ step.done ? '✓' : '' }}</span>
          <div class="grow wz-text">
            <div class="wz-step-title">{{ step.title }}</div>
            <div class="tiny muted">{{ step.detail }}</div>
          </div>
          <span v-if="step.done" class="tag tag-info">已完成</span>
          <span v-else class="wz-actions">
            <button
              v-if="step.action.kind !== 'none'"
              class="btn btn-sm btn-accent"
              type="button"
              @click="runStep(step)"
            >
              {{ step.action.label }}
            </button>
            <button
              v-if="step.action.kind === 'open-settings'"
              class="btn btn-sm"
              type="button"
              :disabled="!privacy?.appPath"
              :title="privacy?.appPath ? '复制：' + privacy.appPath : '未获取到应用路径'"
              @click="copyAppPath"
            >
              复制应用路径
            </button>
          </span>
        </li>
      </ol>
      <div v-if="wizardOpen && localStep && !localStep.done" class="wz-hint">
        macOS 不允许程序自动把自己加入「完全磁盘访问权限」：先点「打开系统设置」，再点「复制应用路径」，
        在列表里按 <span class="kbd">+</span> 粘贴路径（或直接拖入「观澜.app」），回到这里会自动检测并同步。
      </div>
      <div v-if="wizardOpen" class="wz-ft">
        <span class="tiny muted">授权完成后自动重试（每 2 秒检测一次，最多 60 次）；也可以手动粘贴浏览器里的同花顺 cookie。</span>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" @click="openPaste">粘贴登录态</button>
      </div>
      <div v-if="pasteOpen" class="wl-paste">
        <textarea
          v-model="cookieText"
          class="textarea"
          rows="3"
          spellcheck="false"
          placeholder="粘贴同花顺 cookie，形如 v=xxxx; ck=yyyy; 只写本地文件，不会上传"
          aria-label="同花顺登录 cookie"
        />
        <div class="row">
          <span class="tiny muted">只写到 {{ dataDir }}/ths_cookie.txt（0600），不会上传。</span>
          <span class="spacer" />
          <button class="btn btn-sm" type="button" @click="pasteOpen = false">取消</button>
          <button class="btn btn-sm btn-accent" type="button" :disabled="savingCookie || cookieText.trim() === ''" @click="saveCookie">
            {{ savingCookie ? '验证中…' : '保存并验证' }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="syncResult" class="wl-sync" :class="syncResult.success ? 'ok' : 'warn'">
      <div class="row">
        <span class="grow">{{ syncResult.message }}</span>
        <span class="tiny muted">新增 {{ syncResult.added }} · 移除 {{ syncResult.removed }}</span>
        <button class="btn btn-sm btn-ghost" type="button" aria-label="关闭同步提示" @click="syncResult = null">×</button>
      </div>
      <div v-if="syncResult.needWebLogin" class="hint-warn">需要在浏览器登录同花顺后重试：点上方向导的「打开登录页」，登录完成后会自动同步。</div>
      <div v-if="syncResult.needFullDiskAccess" class="hint-warn">
        需要给本应用「完全磁盘访问权限」：系统设置 → 隐私与安全性 → 完全磁盘访问权限{{ privacy?.hint ? '（' + privacy.hint + '）' : '' }}。
      </div>
      <ul v-if="syncResult.warnings.length > 0" class="warn-list">
        <li v-for="(w, i) in syncResult.warnings" :key="i">{{ w }}</li>
      </ul>
    </div>

    <div class="tabs">
      <button
        v-for="g in groups"
        :key="g"
        class="tab"
        :class="{ on: activeGroup === g }"
        type="button"
        @click="activeGroup = g"
      >
        {{ g }}
      </button>
    </div>

    <div class="card-bd flush wl-table">
      <StateBlock v-if="loading && rows.length === 0" kind="loading" title="读取自选与行情…" />
      <StateBlock
        v-else-if="error && rows.length === 0"
        kind="error"
        title="自选读取失败"
        :message="error"
        action-text="重试"
        @action="load"
      />
      <div v-else-if="rows.length === 0" class="wl-empty">
        <StateBlock
          kind="empty"
          title="自选为空"
          message="按向导授权一个数据源后会自动同步；也可以在全局搜索里找到标的再加入自选。"
          action-text="同步同花顺"
          @action="doSync"
        />
        <div class="wl-empty-actions">
          <button class="btn btn-sm" type="button" @click="runCheck">重新检测数据源</button>
          <button class="btn btn-sm" type="button" @click="openPaste">粘贴登录态</button>
        </div>
      </div>
      <table v-else class="tbl wl-tbl">
        <thead>
          <tr>
            <th class="l">代码</th>
            <th class="l">名称</th>
            <th v-if="activeGroup === '全部'" class="l grp">分组</th>
            <th>现价</th>
            <th>涨跌幅</th>
            <th v-if="cols.amount">成交额</th>
            <th v-if="cols.turnover">换手</th>
            <th v-if="cols.main">主力净额</th>
            <th v-if="cols.mainPct">主力占比</th>
            <th v-if="cols.five">5日主力</th>
            <th v-if="cols.spark" class="l">走势</th>
            <th class="l wl-actions-hd">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in rows"
            :key="row.code"
            :class="{ sel: state.focus?.code === row.code, 'drawer-on': drawer?.code === row.code }"
            @click="focusRow(row)"
          >
            <td class="l num">
              <span class="wl-code">
                <span class="wl-code-num">{{ row.code }}</span>
                <span v-if="row.boards.length > 0" class="wl-boards">
                  <span
                    v-for="b in row.boards"
                    :key="b"
                    class="wl-board"
                    :class="'b-' + b"
                    :title="boardTitle(b)"
                    >{{ b }}</span
                  >
                </span>
              </span>
            </td>
            <td class="l name">
              {{ row.name }}
              <span v-if="row.stale" class="tag tag-accent" style="margin-left: 6px">缓存</span>
            </td>
            <td v-if="activeGroup === '全部'" class="l tiny muted grp">{{ row.group }}</td>
            <td class="num" :class="paint(row.price, row.changePct ?? 0)">{{ fmtPrice(row.price) }}</td>
            <td class="num" :class="paint(row.changePct)">{{ fmtPct(row.changePct) }}</td>
            <td v-if="cols.amount" class="num">{{ fmtAmount(row.amount) }}</td>
            <td v-if="cols.turnover" class="num">
              {{ row.turnover === undefined || row.turnover === 0 ? '—' : row.turnover.toFixed(2) + '%' }}
            </td>
            <td v-if="cols.main" class="num" :class="paint(flowMap[row.code]?.mainNet)">
              {{ fmtSignedAmount(flowMap[row.code]?.mainNet) }}
            </td>
            <td v-if="cols.mainPct" class="num" :class="paint(flowMap[row.code]?.mainPct)">
              {{ fmtPct(flowMap[row.code]?.mainPct) }}
            </td>
            <td v-if="cols.five" class="num" :class="paint(detailMap[row.code]?.five)">
              {{ fmtSignedAmount(detailMap[row.code]?.five) }}
            </td>
            <td v-if="cols.spark" class="l">
              <button
                v-if="minutePricesOf(row.code).length > 0"
                class="wl-spark-btn"
                type="button"
                title="查看分时大图"
                @click.stop="openMinute(row)"
              >
                <canvas
                  v-spark="{
                    prices: minutePricesOf(row.code),
                    baseline: minuteSeriesOf(row.code)?.prevClose ?? null,
                    up: minuteUp(row.code),
                    width: 76,
                    height: 22,
                  }"
                  class="wl-spark"
                  aria-hidden="true"
                />
              </button>
              <span v-else class="tiny muted">—</span>
            </td>
            <td class="l">
              <span class="wl-row-actions">
                <button class="btn btn-sm" type="button" @click.stop="focusRow(row)">K线</button>
                <button class="btn btn-sm" type="button" @click.stop="openDrawer(row)">资金</button>
                <button class="btn btn-sm" type="button" @click.stop="openMinute(row)">分时</button>
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="wl-foot">
      <span v-if="error && rows.length > 0" class="hint-warn">{{ error }}</span>
      <span v-else-if="staleCount > 0" class="hint-warn">{{ staleCount }} 条为缓存数据（数据源暂不可达）</span>
      <span v-else class="tiny muted">点行或「K线」跳转 K 线面板；「资金」开右侧抽屉，「分时」看当日走势大图</span>
      <span class="spacer" />
      <span class="tiny muted">红涨绿跌 · 金额自动换算</span>
    </div>

    <!-- 资金抽屉 -->
    <aside v-if="drawer" class="wl-drawer" aria-label="标的资金抽屉">
      <div class="dw-hd">
        <span class="dw-title">{{ drawer.name }} <span class="num muted">{{ drawer.code }}</span></span>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" @click="focusStock(drawer.code, drawer.name)">打开 K 线</button>
        <button class="btn btn-sm btn-ghost" type="button" aria-label="关闭资金抽屉" @click="closeDrawer">×</button>
      </div>
      <div class="dw-bd">
        <div class="dw-metrics">
          <div class="metric">
            <span class="metric-label">主力净额</span>
            <span class="metric-value" :class="paint(drawerStats.lastMain)">{{ fmtSignedAmount(drawerStats.lastMain) }}</span>
          </div>
          <div class="metric">
            <span class="metric-label">5 日主力</span>
            <span class="metric-value" :class="paint(drawerStats.sum5)">{{ fmtSignedAmount(drawerStats.sum5) }}</span>
          </div>
          <div class="metric">
            <span class="metric-label">20 日主力</span>
            <span class="metric-value" :class="paint(drawerStats.sum20)">{{ fmtSignedAmount(drawerStats.sum20) }}</span>
          </div>
          <div class="metric">
            <span class="metric-label">连续</span>
            <span class="metric-value" :class="drawerStats.streakInflow ? 'up' : 'down'">{{ drawerStats.streakDays }} 日</span>
          </div>
        </div>

        <div class="seg dw-seg">
          <button
            v-for="r in [5, 10, 20, 60]"
            :key="r"
            class="seg-item"
            :class="{ on: drawerRange === r }"
            type="button"
            @click="setDrawerRange(r)"
          >
            {{ r }}日
          </button>
        </div>

        <div class="dw-chart-wrap">
          <div ref="dwWrap" class="dw-chart">
            <canvas
              ref="dwCanvas"
              class="dw-canvas"
              role="img"
              :aria-label="drawer.name + ' 资金流'"
              @pointermove="dwMove"
              @pointerleave="dwLeave"
            />
            <div v-if="drawerLoading" class="dw-state"><span class="spinner" aria-hidden="true" /></div>
            <div v-else-if="drawerError" class="dw-state">
              <StateBlock kind="error" title="资金流读取失败" :message="drawerError" action-text="重试" @action="loadDrawer" />
            </div>
          </div>
          <div class="dw-legend">
            <button class="lg-item" type="button" @click="toggleDrawerSeries('main')">
              <span class="lg-bar" :class="{ off: !drawerVisible.main }" />当日
            </button>
            <button class="lg-item" type="button" @click="toggleDrawerSeries('cumulative')">
              <span class="lg-line accent" :class="{ off: !drawerVisible.cumulative }" />累计
            </button>
            <button class="lg-item" type="button" @click="toggleDrawerSeries('close')">
              <span class="lg-line info" :class="{ off: !drawerVisible.close }" />收盘价
            </button>
            <span class="spacer" />
            <span v-if="drawerHoverPoint" class="tiny muted">
              {{ drawerHoverPoint.date }} · 主力
              <b class="num" :class="dir(drawerHoverPoint.mainNet)">{{ fmtSignedAmount(drawerHoverPoint.mainNet) }}</b>
            </span>
          </div>
        </div>

        <div class="dw-note">
          <div class="dw-note-hd">
            <span class="tag" :class="drawerStats.streakInflow ? 'tag-up' : 'tag-down'">{{ drawerNarrative.title }}</span>
          </div>
          <ul>
            <li v-for="(line, i) in drawerNarrative.lines" :key="i">{{ line }}</li>
          </ul>
          <div class="tiny muted">纯描述，不构成买卖建议。</div>
        </div>
      </div>
    </aside>

    <IntradayModal
      v-if="minuteModal"
      :series="modalSeries"
      :loading="modalLoading"
      :error="modalError"
      :name="minuteModal.name"
      :code="minuteModal.code"
      @close="closeMinuteModal"
      @retry="loadModalMinute"
    />
  </section>
</template>

<style scoped>
  .wl {
    position: relative;
    min-height: 0;
  }
  .wl-status {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 14px;
    padding: 6px 12px;
    border: 1px solid var(--glass-line);
    border-radius: var(--radius-sm);
    background: var(--glass-1);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
    font-size: 11.5px;
    color: var(--text-2);
  }
  .wl-src {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .wl-wizard {
    margin: 8px 12px 0;
    padding: 9px 11px;
    border: 1px solid var(--glass-line);
    border-radius: var(--radius-sm);
    background: linear-gradient(rgba(233, 180, 76, 0.06), rgba(233, 180, 76, 0.06)), var(--glass-2);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
  }
  .wz-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .wz-title {
    color: var(--accent-2);
    font-size: 12px;
    font-weight: 600;
  }
  .wz-steps {
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .wz-step {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 9px;
    padding: 6px 8px;
    border: 1px solid var(--line);
    border-radius: var(--radius-xs);
    background: var(--panel);
  }
  .wz-step.done {
    opacity: 0.72;
  }
  .wz-actions {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex: 0 0 auto;
  }
  .wz-hint {
    margin-top: 8px;
    color: var(--muted);
    font-size: 11px;
    line-height: 1.65;
  }
  .wz-mark {
    display: grid;
    place-items: center;
    width: 16px;
    height: 16px;
    flex: 0 0 auto;
    border: 1px solid var(--line-3);
    border-radius: 50%;
    color: var(--ok);
    font-size: 10px;
  }
  .wz-step.done .wz-mark {
    border-color: var(--ok);
    background: rgba(34, 192, 138, 0.12);
  }
  .wz-text {
    min-width: 0;
  }
  .wz-step-title {
    color: var(--text);
    font-size: 12px;
  }
  .wz-ft {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
    flex-wrap: wrap;
  }
  .wl-paste {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 8px;
  }
  .wl-paste .textarea {
    width: 100%;
    font-family: var(--mono);
    font-size: 11.5px;
  }
  .wl-colmenu-wrap {
    position: relative;
    display: inline-flex;
  }
  .wl-colmenu {
    position: absolute;
    top: 28px;
    right: 0;
    z-index: 60;
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 148px;
    padding: 8px 10px;
    border: 1px solid var(--glass-line);
    border-radius: var(--radius-sm);
    background: var(--glass-3);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
    box-shadow: var(--shadow-3);
  }
  .wl-colmenu-hd {
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.6px;
  }
  .wl-colmenu .check {
    font-size: 11.5px;
  }
  .wl-sync {
    margin: 8px 12px 0;
    padding: 7px 10px;
    border: 1px solid var(--glass-line);
    border-left-width: 3px;
    border-radius: var(--radius-sm);
    background: var(--glass-2);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
    font-size: 12px;
    color: var(--text-2);
  }
  .wl-sync.ok {
    border-left-color: var(--ok);
    background: linear-gradient(rgba(34, 192, 138, 0.06), rgba(34, 192, 138, 0.06)), var(--glass-2);
  }
  .wl-sync.warn {
    border-left-color: var(--warn);
    background: linear-gradient(rgba(233, 180, 76, 0.06), rgba(233, 180, 76, 0.06)), var(--glass-2);
  }
  .warn-list {
    margin: 4px 0 0;
    padding-left: 16px;
    color: var(--muted);
    font-size: 11px;
  }
  .wl-table {
    overflow: auto;
    min-height: 0;
  }
  .wl-tbl thead th {
    position: sticky;
    top: 0;
  }
  /* thead 保留全局 8px blur，只把圆角对齐卡片 */
  .wl-tbl thead th:first-child {
    border-top-left-radius: var(--radius-sm);
  }
  /* 代码列：等宽数字在前、板块标签在后；标签定宽，避免刷新时抖动 */
  .wl-code {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    vertical-align: middle;
  }
  .wl-code-num {
    font-family: var(--mono);
    font-variant-numeric: tabular-nums;
  }
  .wl-boards {
    display: inline-flex;
    gap: 3px;
    flex: 0 0 auto;
  }
  .wl-board {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    box-sizing: border-box;
    min-width: 22px;
    height: 15px;
    padding: 0 5px;
    border: 1px solid transparent;
    border-radius: 6px;
    font-size: 10px;
    font-family: system-ui, -apple-system, 'PingFang SC', sans-serif;
    line-height: 1;
    letter-spacing: 0;
    font-weight: 500;
    opacity: 0.9;
    user-select: none;
  }
  /* 板块标签配色：低饱和，且与红涨绿跌语义无关（刻意不用 --up/--down） */
  .wl-board.b-沪 {
    color: #8fa6c4;
    border-color: rgba(143, 166, 196, 0.28);
    background: rgba(143, 166, 196, 0.12);
  }
  .wl-board.b-深 {
    color: #a89bc4;
    border-color: rgba(168, 155, 196, 0.28);
    background: rgba(168, 155, 196, 0.12);
  }
  .wl-board.b-创 {
    color: #d39a6a;
    border-color: rgba(211, 154, 106, 0.3);
    background: rgba(211, 154, 106, 0.12);
  }
  .wl-board.b-科 {
    color: #6fb9b3;
    border-color: rgba(111, 185, 179, 0.3);
    background: rgba(111, 185, 179, 0.12);
  }
  .wl-board.b-北 {
    color: #7fb08a;
    border-color: rgba(127, 176, 138, 0.3);
    background: rgba(127, 176, 138, 0.12);
  }
  /* 涨跌配色：scoped 里补足特异性，压过全局 .tbl tbody td 的默认灰；
     平盘是白色（--flat），缺数据的 na 保持 muted，不染白 */
  .wl-tbl tbody td.up {
    color: var(--up);
  }
  .wl-tbl tbody td.down {
    color: var(--down);
  }
  .wl-tbl tbody td.flat {
    color: var(--flat);
  }
  .wl-tbl tbody td.na {
    color: var(--muted);
  }
  .wl-row-actions {
    display: inline-flex;
    gap: 4px;
    opacity: 0.72;
    transition: opacity 0.12s var(--ease);
  }
  .wl-tbl tbody tr:hover .wl-row-actions,
  .wl-tbl tbody tr:focus-within .wl-row-actions,
  .wl-tbl tbody tr.drawer-on .wl-row-actions {
    opacity: 1;
  }
  .wl-spark-btn {
    display: inline-flex;
    padding: 0;
    border: 0;
    background: none;
    line-height: 0;
    cursor: zoom-in;
  }
  .wl-spark-btn:hover {
    filter: brightness(1.2);
  }
  .wl-spark {
    display: block;
  }
  /* 操作列钉在右侧：横向滚动时始终能看到「K线 / 资金」 */
  .wl-tbl thead th:last-child,
  .wl-tbl tbody td:last-child {
    position: sticky;
    right: 0;
    z-index: 2;
    background: var(--panel);
    box-shadow: -1px 0 0 var(--line);
  }
  .wl-tbl thead th:last-child {
    z-index: 4;
    border-top-right-radius: var(--radius-sm);
    background: rgba(15, 21, 32, 0.98);
    backdrop-filter: blur(8px);
  }
  .wl-tbl tbody tr:hover td:last-child {
    background: linear-gradient(rgba(233, 180, 76, 0.05), rgba(233, 180, 76, 0.05)), var(--panel);
  }
  .wl-tbl tbody tr.sel td:last-child {
    background: linear-gradient(rgba(233, 180, 76, 0.09), rgba(233, 180, 76, 0.09)), var(--panel);
  }
  .wl-actions-hd {
    width: 132px;
    padding-left: 4px;
    padding-right: 6px;
  }
  .wl-empty {
    padding: 8px 0 20px;
  }
  .wl-empty-actions {
    display: flex;
    justify-content: center;
    gap: 8px;
    margin-top: -4px;
  }
  .wl-foot {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 26px;
    padding: 0 12px;
    border-top: 1px solid var(--line);
    font-size: 11px;
    overflow: hidden;
  }
  .wl-drawer {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    z-index: 40;
    display: flex;
    flex-direction: column;
    width: 420px;
    border-left: 1px solid var(--line-2);
    background: linear-gradient(180deg, rgba(255, 255, 255, 0.02), transparent 120px), var(--bg-2);
    box-shadow: -18px 0 50px rgba(0, 0, 0, 0.4);
    animation: wl-drawer-in 0.2s var(--ease);
  }
  @keyframes wl-drawer-in {
    from {
      transform: translateX(20px);
      opacity: 0;
    }
  }
  .dw-hd {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 42px;
    flex: 0 0 42px;
    padding: 0 10px;
    border-bottom: 1px solid var(--line);
  }
  .dw-title {
    font-size: 12.5px;
    font-weight: 600;
  }
  .dw-bd {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .dw-metrics {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
  }
  .dw-metrics .metric-value {
    font-size: 14px;
  }
  /* 抽屉指标块：与面板其它毛玻璃卡片一致 */
  .dw-metrics .metric {
    border-color: var(--glass-line);
    background: var(--glass-2);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
  }
  .dw-metrics .metric-value.up {
    color: var(--up);
  }
  .dw-metrics .metric-value.down {
    color: var(--down);
  }
  .dw-metrics .metric-value.flat {
    color: var(--flat);
  }
  .dw-metrics .metric-value.na {
    color: var(--muted);
  }
  .dw-seg {
    align-self: flex-start;
  }
  .dw-chart-wrap {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .dw-chart {
    position: relative;
    height: 210px;
    border: 1px solid var(--glass-line);
    border-radius: var(--radius-sm);
    background: var(--glass-2);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
    overflow: hidden;
  }
  .dw-canvas {
    display: block;
    cursor: crosshair;
  }
  .dw-state {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    background: rgba(11, 17, 25, 0.85);
  }
  .dw-legend {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px 12px;
    min-height: 22px;
    color: var(--muted);
    font-size: 11px;
  }
  .lg-item {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-2);
    font-size: 11px;
  }
  .lg-bar {
    width: 9px;
    height: 9px;
    border-radius: 2px;
    background: var(--up);
  }
  .lg-bar.off {
    background: var(--faint);
    opacity: 0.5;
  }
  .lg-line {
    width: 14px;
    height: 2px;
    border-radius: 1px;
  }
  .lg-line.accent {
    background: var(--accent);
  }
  .lg-line.info {
    background: var(--info);
  }
  .lg-line.off {
    background: var(--faint);
    opacity: 0.5;
  }
  .dw-note {
    padding: 8px 10px;
    border: 1px solid var(--glass-line);
    border-radius: var(--radius-sm);
    background: var(--glass-2);
    backdrop-filter: blur(18px) saturate(140%);
    -webkit-backdrop-filter: blur(18px) saturate(140%);
  }
  .dw-note-hd {
    margin-bottom: 4px;
  }
  .dw-note ul {
    margin: 0 0 6px;
    padding-left: 16px;
    color: var(--text-2);
    font-size: 11.5px;
    line-height: 1.7;
  }
  @media (max-width: 1360px) {
    .wl-tbl tbody td,
    .wl-tbl thead th {
      padding-left: 8px;
      padding-right: 8px;
    }
  }
  @media (max-width: 1200px) {
    .wl-drawer {
      width: 360px;
    }
    /* 1024 宽下优先保行情与资金列，分组列让位 */
    .wl-tbl .grp {
      display: none;
    }
  }
  .card-tools {
    flex-wrap: wrap;
    justify-content: flex-end;
    row-gap: 4px;
  }
</style>