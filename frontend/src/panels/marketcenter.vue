<script setup lang="ts">
// A 股市场中心：指数、市场宽度（涨跌家数/涨跌停/炸板）、涨停梯队与龙虎榜。
// 一次走 marketcenter.snapshot；单块失败由后端降级并在 warnings 里提示，不整页失败。
// 演示模式：首次加载本面板时注册 mocks/marketcenter（幂等），不改 api.mock.ts。
import { computed, onMounted, ref } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { marketHot, marketSnapshot, type MarketLadderStock, type MarketSnapshot } from '../lib/api.marketcenter'
import { messageOf } from '../lib/api.market'
import { dir, fmtAmount, fmtClock, fmtPct, fmtPrice, fmtSignedAmount } from '../lib/format'
import { installMarketCenterMocks } from '../mocks/marketcenter'
import type { HotStock } from '../types'

installMarketCenterMocks()

interface Metric {
  label: string
  value: string
  tone?: 'up' | 'down' | 'flat' | 'accent'
  foot?: string
}

interface LadderGroup {
  key: number
  label: string
  items: MarketLadderStock[]
}

const snapshot = ref<MarketSnapshot | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
let seq = 0

/** 拉取快照；initial=true 时展示骨架，否则保留旧数据并只置刷新态。 */
async function load(initial = false): Promise<void> {
  const current = ++seq
  if (initial) loading.value = true
  else refreshing.value = true
  error.value = ''
  try {
    const next = await marketSnapshot()
    if (current !== seq) return
    snapshot.value = next
  } catch (e) {
    if (current !== seq) return
    error.value = messageOf(e)
  } finally {
    if (current === seq) {
      loading.value = false
      refreshing.value = false
    }
  }
}

/** 手动刷新（保留当前内容，失败时展示上次成功数据）。 */
function refresh(): void {
  void load(false)
}

/** 挂载后首次加载快照。 */
onMounted(() => {
  void load(true)
})

// ---------- 同花顺热榜 ----------

const hotType = ref<'hour' | 'day'>('hour')
const dayHot = ref<HotStock[]>([])
const hotLoading = ref(false)
const hotError = ref('')

/** 当前榜单：小时榜跟随 snapshot（后端默认返回），日榜单独拉取并缓存。 */
const hotRows = computed<HotStock[]>(() =>
  hotType.value === 'day' ? dayHot.value : (snapshot.value?.hot ?? []),
)

/** 切换小时榜/日榜；日榜首次切换时才请求。 */
async function setHotType(next: 'hour' | 'day'): Promise<void> {
  if (hotType.value === next) return
  hotType.value = next
  if (next === 'hour' || dayHot.value.length > 0) return
  await loadDayHot()
}

/** 拉取日榜；失败保留旧数据并给出可重试错误。 */
async function loadDayHot(): Promise<void> {
  hotLoading.value = true
  hotError.value = ''
  try {
    dayHot.value = await marketHot('day', 50)
  } catch (e) {
    hotError.value = messageOf(e)
  } finally {
    hotLoading.value = false
  }
}

/** 热榜刷新：小时榜随快照刷新，日榜单独重拉。 */
function refreshHot(): void {
  if (hotType.value === 'day') void loadDayHot()
  else refresh()
}

/** 排名变化的展示：正数=上升、负数=下降、0=持平。 */
function rankChangeText(v: number | undefined): string {
  if (!v) return '—'
  return v > 0 ? '+' + v : String(v)
}

/** 主要指数（顺序由后端固定）。 */
const indices = computed(() => snapshot.value?.indices ?? [])
/** 市场宽度（涨跌家数、涨跌停、炸板、成交额）。 */
const breadth = computed(() => snapshot.value?.breadth)
/** 涨停梯队（后端已按连板数降序）。 */
const ladder = computed(() => snapshot.value?.ladder ?? [])
/** 后端返回的降级提示（单块失败时显示黄条）。 */
const warnings = computed(() => snapshot.value?.warnings ?? [])

/** 龙虎榜按净买入绝对值降序（后端已排序，这里防御性再排一次）。 */
const longhu = computed(() =>
  [...(snapshot.value?.longhu ?? [])].sort((a, b) => Math.abs(b.netBuy) - Math.abs(a.netBuy)),
)

/** 当日最高连板数，用于突出最高板。 */
const maxBoards = computed(() => ladder.value.reduce((max, item) => Math.max(max, item.boards), 0))

/** 按连板数分组：首板 / 2板 / 3板 / 4板及以上。 */
const ladderGroups = computed<LadderGroup[]>(() => {
  const buckets = new Map<number, MarketLadderStock[]>()
  for (const item of ladder.value) {
    const key = item.boards >= 4 ? 4 : Math.max(1, item.boards)
    const list = buckets.get(key)
    if (list) list.push(item)
    else buckets.set(key, [item])
  }
  return [...buckets.entries()]
    .sort((a, b) => b[0] - a[0])
    .map(([key, items]) => ({
      key,
      label: key >= 4 ? '4板及以上' : key === 1 ? '首板' : key + '板',
      items,
    }))
})

/** 宽度指标卡（红涨绿跌，炸板率用强调色）。 */
const metrics = computed<Metric[]>(() => {
  const b = breadth.value
  if (!b) return []
  return [
    { label: '上涨', value: String(b.up), tone: 'up', foot: '家' },
    { label: '下跌', value: String(b.down), tone: 'down', foot: '家' },
    { label: '平盘', value: String(b.flat), tone: 'flat', foot: '家' },
    { label: '涨停', value: String(b.limitUp), tone: 'up', foot: '家' },
    { label: '跌停', value: String(b.limitDown), tone: 'down', foot: '家' },
    { label: '炸板', value: String(b.broken), tone: 'accent', foot: '家' },
    { label: '炸板率', value: b.brokenRate.toFixed(1) + '%', tone: 'accent', foot: '炸板/(涨停+炸板)' },
    { label: '两市成交额', value: fmtAmount(b.amount), foot: '沪 + 深' },
  ]
})

/** 判断某只涨停股是否为当日最高板。 */
function isTopBoard(item: MarketLadderStock): boolean {
  return maxBoards.value > 0 && item.boards === maxBoards.value
}

/** 涨停股金额：按后端给的 amountKind 标注封单/成交，避免不同数据源口径混淆。 */
function fmtStockAmount(item: MarketLadderStock): string {
  if (!item.amount) return '—'
  const prefix = item.amountKind === '成交额' ? '成交 ' : item.amountKind === '封单额' ? '封单 ' : ''
  return prefix + fmtAmount(item.amount)
}
</script>

<template>
  <section class="card mc">
    <div class="card-hd">
      <span class="card-title">市场中心</span>
      <span class="card-sub">
        <template v-if="snapshot">
          {{ snapshot.breadth.date || '—' }} · {{ snapshot.breadth.limitUp || ladder.length }} 只涨停 · 更新 {{ fmtClock(snapshot.updatedAt) }}
        </template>
        <template v-else>指数 · 涨跌家数 · 涨停梯队 · 龙虎榜</template>
      </span>
      <span class="card-tools">
        <span v-if="refreshing" class="tiny muted">刷新中…</span>
        <button class="btn btn-sm" type="button" :disabled="loading || refreshing" @click="refresh">刷新</button>
      </span>
    </div>

    <div v-if="loading && !snapshot" class="mc-body">
      <div class="mc-skel-row">
        <div v-for="i in 5" :key="'i' + i" class="skeleton mc-skel-index" />
      </div>
      <div class="mc-skel-row">
        <div v-for="i in 8" :key="'m' + i" class="skeleton mc-skel-metric" />
      </div>
      <div class="skeleton mc-skel-block" />
      <div class="skeleton mc-skel-block" />
    </div>

    <StateBlock
      v-else-if="error && !snapshot"
      kind="error"
      title="市场中心加载失败"
      :message="error"
      action-text="重试"
      @action="refresh"
    />

    <div v-else-if="snapshot" class="mc-body">
      <div v-if="error" class="mc-warn">
        <span class="dot dot-warn" aria-hidden="true" />
        <span class="mc-warn-text">刷新失败：{{ error }}（展示上次成功数据）</span>
      </div>
      <div v-if="warnings.length > 0" class="mc-warn">
        <span class="dot dot-warn" aria-hidden="true" />
        <div class="mc-warn-list">
          <span v-for="text in warnings" :key="text" class="mc-warn-text">{{ text }}</span>
        </div>
      </div>

      <!-- 指数 -->
      <div class="mc-section">
        <div class="mc-sec-hd">
          <span class="mc-sec-title">主要指数</span>
          <span class="tiny muted">{{ indices.length }} 个 · 红涨绿跌</span>
        </div>
        <div v-if="indices.length === 0" class="mc-empty">指数数据暂不可用</div>
        <div v-else class="mc-indices">
          <div v-for="idx in indices" :key="idx.code" class="mc-index">
            <div class="mc-index-hd">
              <span class="mc-index-name">{{ idx.name }}</span>
              <span class="mono tiny muted">{{ idx.code }}</span>
            </div>
            <div class="mc-index-price mono" :class="dir(idx.changePct)">{{ fmtPrice(idx.price) }}</div>
            <div class="mc-index-foot">
              <span class="mono" :class="dir(idx.changePct)">{{ fmtPct(idx.changePct) }}</span>
              <span class="tiny muted">{{ idx.amount ? fmtAmount(idx.amount) : '—' }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 市场宽度 -->
      <div class="mc-section">
        <div class="mc-sec-hd">
          <span class="mc-sec-title">市场宽度</span>
          <span class="tiny muted">涨跌家数 · 涨跌停 · 炸板 · 成交额</span>
        </div>
        <div v-if="metrics.length === 0" class="mc-empty">宽度数据暂不可用</div>
        <div v-else class="mc-metrics">
          <div v-for="item in metrics" :key="item.label" class="metric mc-metric">
            <span class="metric-label">{{ item.label }}</span>
            <span class="metric-value" :class="item.tone">{{ item.value }}</span>
            <span v-if="item.foot" class="metric-foot">{{ item.foot }}</span>
          </div>
        </div>
      </div>

      <!-- 同花顺热榜 -->
      <div class="mc-section">
        <div class="mc-sec-hd">
          <span class="mc-sec-title">同花顺热榜</span>
          <span class="seg">
            <button class="seg-item" :class="{ on: hotType === 'hour' }" type="button" @click="setHotType('hour')">小时榜</button>
            <button class="seg-item" :class="{ on: hotType === 'day' }" type="button" @click="setHotType('day')">日榜</button>
          </span>
          <span class="tiny muted">{{ hotRows.length }} 只 · 热度/排名变化/题材</span>
          <span class="spacer" />
          <button class="btn btn-sm" type="button" :disabled="hotLoading || refreshing" @click="refreshHot">刷新热榜</button>
        </div>
        <div v-if="hotLoading && hotRows.length === 0" class="mc-empty">读取热门榜…</div>
        <div v-else-if="hotError && hotRows.length === 0" class="mc-empty">
          {{ hotError }}
          <button class="btn btn-sm" type="button" @click="loadDayHot">重试</button>
        </div>
        <div v-else-if="hotRows.length === 0" class="mc-empty">暂无热门榜数据</div>
        <div v-else class="table-wrap mc-lh">
          <table class="tbl mc-hot-tbl">
            <thead>
              <tr>
                <th>排名</th>
                <th class="l">名称</th>
                <th>现价</th>
                <th>涨跌幅</th>
                <th>热度</th>
                <th>排名变化</th>
                <th class="l">题材 / 人气</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in hotRows" :key="row.code">
                <td class="mono muted">{{ row.rank }}</td>
                <td class="l name">
                  {{ row.name }}
                  <span class="mono tiny muted" style="margin-left: 6px">{{ row.market }}{{ row.code }}</span>
                </td>
                <td class="mono">{{ row.price ? fmtPrice(row.price) : '—' }}</td>
                <td class="mono" :class="dir(row.changePct)">{{ fmtPct(row.changePct) }}</td>
                <td class="mono">{{ fmtAmount(row.heat, 0) }}</td>
                <td class="mono" :class="dir(row.rankChange)">{{ rankChangeText(row.rankChange) }}</td>
                <td class="l mc-reason-cell">
                  <span v-if="row.popularityTag" class="tag tag-accent">{{ row.popularityTag }}</span>
                  <span v-for="tag in (row.conceptTags ?? []).slice(0, 3)" :key="tag" class="tag" style="margin-left: 4px">{{ tag }}</span>
                  <span v-if="row.reason" class="tiny muted" :title="row.reason" style="margin-left: 6px">{{ row.reason }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 涨停梯队 -->
      <div class="mc-section">
        <div class="mc-sec-hd">
          <span class="mc-sec-title">涨停梯队</span>
          <span class="tiny muted">{{ ladder.length }} 只 · 按连板数分组</span>
          <span v-if="maxBoards > 0" class="tag tag-up">最高 {{ maxBoards }} 板</span>
        </div>
        <div v-if="ladderGroups.length === 0" class="mc-empty">暂无涨停梯队数据</div>
        <div v-else class="mc-groups">
          <div
            v-for="group in ladderGroups"
            :key="group.key"
            class="mc-group"
            :class="{ highest: group.items.some(isTopBoard) }"
          >
            <div class="mc-group-hd">
              <span class="mc-group-label">{{ group.label }}</span>
              <span class="tag">{{ group.items.length }} 只</span>
              <span v-if="group.items.some(isTopBoard)" class="tag tag-up">最高板</span>
            </div>
            <div class="mc-ladder-scroll">
              <div class="mc-ladder">
                <div class="mc-ladder-row mc-ladder-head">
                  <span>代码</span>
                  <span>名称</span>
                  <span>涨幅</span>
                  <span>封板时间</span>
                  <span>涨停原因</span>
                  <span>金额</span>
                </div>
                <div
                  v-for="item in group.items"
                  :key="item.code"
                  class="mc-ladder-row"
                  :class="{ top: isTopBoard(item) }"
                >
                  <span class="mono tiny">{{ item.code }}</span>
                  <span class="mc-name">{{ item.name }}</span>
                  <span class="mono" :class="dir(item.changePct)">{{ fmtPct(item.changePct) }}</span>
                  <span class="mono tiny muted">
                    {{ item.firstTime || '—' }}
                    <template v-if="item.lastTime && item.lastTime !== item.firstTime"> / {{ item.lastTime }}</template>
                  </span>
                  <span class="mc-reason" :title="item.reason || ''">{{ item.reason || '—' }}</span>
                  <span class="mono tiny muted" :title="item.amountKind || ''">{{ fmtStockAmount(item) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 龙虎榜 -->
      <div class="mc-section">
        <div class="mc-sec-hd">
          <span class="mc-sec-title">龙虎榜</span>
          <span class="tiny muted">{{ longhu.length }} 只 · 按净买入绝对值降序</span>
        </div>
        <div v-if="longhu.length === 0" class="mc-empty">暂无龙虎榜数据</div>
        <div v-else class="table-wrap mc-lh">
          <table class="tbl mc-lh-tbl">
            <thead>
              <tr>
                <th class="l">代码</th>
                <th class="l">名称</th>
                <th>涨跌幅</th>
                <th class="l">上榜原因</th>
                <th>净买入</th>
                <th>买入</th>
                <th>卖出</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in longhu" :key="row.code">
                <td class="l mono tiny">{{ row.code }}</td>
                <td class="l name">{{ row.name }}</td>
                <td class="mono" :class="dir(row.changePct)">{{ fmtPct(row.changePct) }}</td>
                <td class="l mc-reason-cell">{{ row.reason || '—' }}</td>
                <td class="mono" :class="dir(row.netBuy)">{{ fmtSignedAmount(row.netBuy) }}</td>
                <td class="mono">{{ fmtAmount(row.buy) }}</td>
                <td class="mono">{{ fmtAmount(row.sell) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="mc-foot tiny muted">
        口径：涨停/炸板来自同花顺涨停池，热榜来自同花顺人气榜（小时/日榜），涨跌家数来自新浪全市场涨跌分布（降级用同花顺单市场），龙虎榜来自同花顺龙虎榜；成交额为沪深两市合计。
      </div>
    </div>
  </section>
</template>

<style scoped>
  .mc {
    min-height: 0;
  }
  .mc-body {
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .mc-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .mc-sec-hd {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .mc-sec-title {
    font-size: 12px;
    font-weight: 600;
    color: var(--text);
  }
  .mc-warn {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 7px 10px;
    border: 1px solid rgba(233, 180, 76, 0.35);
    border-radius: var(--radius-sm);
    background: rgba(233, 180, 76, 0.07);
    color: var(--accent-2);
    font-size: 11.5px;
    line-height: 1.6;
  }
  .mc-warn .dot {
    margin-top: 4px;
  }
  .mc-warn-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .mc-warn-text {
    min-width: 0;
  }
  .mc-empty {
    padding: 12px;
    border: 1px dashed var(--line-2);
    border-radius: var(--radius-sm);
    color: var(--muted);
    font-size: 11.5px;
    text-align: center;
  }

  .mc-indices {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(146px, 1fr));
    gap: 8px;
  }
  .mc-index {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 9px 11px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: rgba(255, 255, 255, 0.015);
    min-width: 0;
  }
  .mc-index-hd {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 6px;
  }
  .mc-index-name {
    font-size: 11.5px;
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .mc-index-price {
    font-size: 19px;
    font-weight: 600;
    letter-spacing: -0.3px;
    font-variant-numeric: tabular-nums;
  }
  .mc-index-foot {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 6px;
    font-size: 11.5px;
  }

  .mc-metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(108px, 1fr));
    gap: 8px;
  }
  .mc-metric {
    padding: 7px 10px;
  }
  .mc-metric .metric-value {
    font-size: 17px;
  }

  .mc-groups {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .mc-group {
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .mc-group.highest {
    border-color: rgba(255, 90, 95, 0.35);
    box-shadow: inset 2px 0 0 rgba(255, 90, 95, 0.65);
  }
  .mc-group-hd {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 6px 10px;
    background: rgba(255, 255, 255, 0.02);
    border-bottom: 1px solid var(--line);
  }
  .mc-group-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text);
  }
  .mc-ladder-scroll {
    overflow-x: auto;
  }
  .mc-ladder {
    min-width: 760px;
  }
  .mc-ladder-row {
    display: grid;
    grid-template-columns: 64px minmax(90px, 1fr) 70px 126px minmax(180px, 2.2fr) 96px;
    align-items: center;
    gap: 8px;
    padding: 5px 10px;
    border-bottom: 1px solid rgba(148, 163, 184, 0.07);
    font-size: 12px;
  }
  .mc-ladder-row:last-child {
    border-bottom: 0;
  }
  .mc-ladder-row.top {
    background: rgba(255, 90, 95, 0.06);
  }
  .mc-ladder-row.top .mc-name {
    color: var(--up);
    font-weight: 600;
  }
  .mc-ladder-head {
    position: sticky;
    top: 0;
    z-index: 1;
    background: rgba(15, 21, 32, 0.97);
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.4px;
  }
  .mc-name {
    color: var(--text);
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .mc-reason {
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .mc-lh {
    max-height: 360px;
  }
  .mc-lh-tbl .mc-reason-cell {
    min-width: 240px;
    max-width: 460px;
    white-space: normal;
    line-height: 1.45;
  }
  .mc-foot {
    padding: 2px 0 4px;
    line-height: 1.6;
  }

  .mc-skel-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(146px, 1fr));
    gap: 8px;
  }
  .mc-skel-index {
    height: 82px;
  }
  .mc-skel-metric {
    height: 62px;
  }
  .mc-skel-block {
    height: 120px;
  }
</style>
