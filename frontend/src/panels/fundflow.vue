<script setup lang="ts">
// 资金流面板：个股明细（主力/超大/大/中/小）与板块/概念排名（红绿条形 + 排序）。
import { computed, onMounted, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { flowRank, flowStock, messageOf } from '../lib/data'
import { dir, fmtAmount, fmtPct, fmtSignedAmount, normalizeCode } from '../lib/format'
import { state } from '../stores/app'
import type { FlowKind, FundFlow } from '../types'

type Tab = 'stock' | 'sector' | 'concept'

const tab = ref<Tab>('stock')
const inputCode = ref('600519')
const stockFlow = ref<FundFlow | null>(null)
const rows = ref<FundFlow[]>([])
const loading = ref(false)
const error = ref('')
const sortKey = ref<'mainNet' | 'changePct'>('mainNet')
const sortDesc = ref(true)

const rankKind = computed<FlowKind>(() => (tab.value === 'concept' ? 'concept' : 'sector'))

const maxAbs = computed(() => rows.value.reduce((m, r) => Math.max(m, Math.abs(r.mainNet)), 1))

const sorted = computed(() => {
  const list = rows.value.slice()
  list.sort((a, b) => {
    const av = sortKey.value === 'mainNet' ? a.mainNet : a.changePct ?? 0
    const bv = sortKey.value === 'mainNet' ? b.mainNet : b.changePct ?? 0
    return sortDesc.value ? bv - av : av - bv
  })
  return list
})

const flowParts = computed(() => {
  const f = stockFlow.value
  if (!f) return []
  return [
    { label: '主力净额', value: f.mainNet },
    { label: '超大单', value: f.superNet ?? 0 },
    { label: '大单', value: f.largeNet ?? 0 },
    { label: '中单', value: f.mediumNet ?? 0 },
    { label: '小单', value: f.smallNet ?? 0 },
  ]
})

const maxPart = computed(() => flowParts.value.reduce((m, p) => Math.max(m, Math.abs(p.value)), 1))

/** 资金与价格的方向组合，给出结构判断（研究提示，不是买卖建议）。 */
const reading = computed<{ title: string; text: string }>(() => {
  const f = stockFlow.value
  if (!f) return { title: '—', text: '' }
  const inflow = f.mainNet > 0
  const rise = (f.changePct ?? 0) > 0
  if (inflow && rise) return { title: '量价资金同向', text: '主力净流入且价格上行，短期结构偏强；仍需看 5 日累计是否持续。' }
  if (inflow && !rise) return { title: '资金逆势流入', text: '价格下跌但主力净流入，留意是否为大单承接；若次日不能收复则需警惕。' }
  if (!inflow && rise) return { title: '价涨资金流出', text: '价格上行而主力净流出，存在拉高派发风险，注意量能是否可持续。' }
  return { title: '同向走弱', text: '主力净流出且价格下跌，短期结构偏弱；避免在缩量反弹中加仓。' }
})

const superShare = computed(() => {
  const f = stockFlow.value
  if (!f || f.mainNet === 0) return null
  return ((f.superNet ?? 0) / f.mainNet) * 100
})

async function loadStock(): Promise<void> {
  const code = normalizeCode(inputCode.value)
  if (code.length !== 6) {
    error.value = '请输入 6 位股票代码，例如 600519'
    return
  }
  loading.value = true
  error.value = ''
  try {
    stockFlow.value = await flowStock(code)
  } catch (e) {
    stockFlow.value = null
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadRank(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    rows.value = await flowRank(rankKind.value, 40)
  } catch (e) {
    rows.value = []
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

function switchTab(next: Tab): void {
  tab.value = next
  error.value = ''
  if (next === 'stock') {
    if (!stockFlow.value) void loadStock()
  } else {
    void loadRank()
  }
}

function toggleSort(key: 'mainNet' | 'changePct'): void {
  if (sortKey.value === key) sortDesc.value = !sortDesc.value
  else {
    sortKey.value = key
    sortDesc.value = true
  }
}

function barWidth(v: number): string {
  return Math.min(100, (Math.abs(v) / maxAbs.value) * 100).toFixed(1) + '%'
}

watch(
  () => state.focus,
  (f) => {
    if (!f) return
    inputCode.value = f.code
    tab.value = 'stock'
    void loadStock()
  },
)

onMounted(() => {
  if (state.focus) inputCode.value = state.focus.code
  void loadStock()
})
</script>

<template>
  <section class="card ff">
    <div class="card-hd">
      <span class="card-title">资金流</span>
      <span class="card-sub">单位：元（自动换算 万/亿）</span>
      <span class="card-tools">
        <span v-if="tab === 'stock'" class="row" style="gap: 6px">
          <input
            v-model="inputCode"
            class="input"
            style="width: 110px"
            placeholder="6 位代码"
            aria-label="股票代码"
            @keydown.enter="loadStock"
          />
          <button class="btn btn-sm btn-accent" type="button" :disabled="loading" @click="loadStock">查询</button>
        </span>
        <button v-else class="btn btn-sm" type="button" :disabled="loading" @click="loadRank">刷新排名</button>
      </span>
    </div>

    <div class="tabs">
      <button class="tab" :class="{ on: tab === 'stock' }" type="button" @click="switchTab('stock')">个股</button>
      <button class="tab" :class="{ on: tab === 'sector' }" type="button" @click="switchTab('sector')">板块</button>
      <button class="tab" :class="{ on: tab === 'concept' }" type="button" @click="switchTab('concept')">概念</button>
    </div>

    <div class="card-bd">
      <StateBlock v-if="loading" kind="loading" title="读取资金流…" />
      <StateBlock
        v-else-if="error"
        kind="error"
        title="资金流读取失败"
        :message="error"
        action-text="重试"
        @action="tab === 'stock' ? loadStock() : loadRank()"
      />

      <!-- 个股 -->
      <div v-else-if="tab === 'stock'" class="stock-flow">
        <StateBlock v-if="!stockFlow" kind="empty" title="输入代码查询个股资金流" message="主力净额 = 超大单 + 大单；单日净额需要结合价格位置与 5 日累计判断。" />
        <template v-else>
          <div class="sf-head">
            <div class="col" style="gap: 2px">
              <span class="sf-name">{{ stockFlow.name || '—' }} <span class="num muted">{{ stockFlow.code }}</span></span>
              <span class="tiny muted">
                {{ stockFlow.date ? stockFlow.date : '' }}
                <template v-if="stockFlow.source"> · {{ stockFlow.source }}</template>
              </span>
            </div>
            <div class="col" style="gap: 2px; align-items: flex-end">
              <span class="sf-main num" :class="dir(stockFlow.mainNet)">{{ fmtSignedAmount(stockFlow.mainNet) }}</span>
              <span class="tiny muted">主力净额</span>
            </div>
          </div>

          <div class="sf-grid">
            <div class="sf-parts">
              <div v-for="p in flowParts" :key="p.label" class="sf-row">
                <span class="sf-label">{{ p.label }}</span>
                <span class="sf-track">
                  <span class="sf-zero" />
                  <span
                    class="sf-bar"
                    :class="dir(p.value)"
                    :style="{
                      width: barWidth(p.value),
                      left: p.value >= 0 ? '50%' : 'calc(50% - ' + barWidth(p.value) + ')',
                    }"
                  />
                </span>
                <span class="sf-val num" :class="dir(p.value)">{{ fmtSignedAmount(p.value) }}</span>
              </div>
              <div class="sf-meta">
                <span>涨跌幅 <b class="num" :class="dir(stockFlow.changePct)">{{ fmtPct(stockFlow.changePct) }}</b></span>
                <span>主力占比 <b class="num" :class="dir(stockFlow.mainPct)">{{ fmtPct(stockFlow.mainPct) }}</b></span>
                <span v-if="superShare !== null">超大单占主力 <b class="num">{{ superShare.toFixed(0) }}%</b></span>
              </div>
            </div>

            <aside class="sf-note">
              <div class="sf-note-hd">
                <span class="tag" :class="stockFlow.mainNet >= 0 ? 'tag-up' : 'tag-down'">{{ reading.title }}</span>
              </div>
              <p class="sf-note-text">{{ reading.text }}</p>
              <div class="sf-note-meta">
                <div><span class="muted">数据源</span> {{ stockFlow.source || '—' }}</div>
                <div><span class="muted">日期</span> {{ stockFlow.date || '—' }}</div>
                <div><span class="muted">口径</span> 主力 = 超大单 + 大单</div>
              </div>
            </aside>
          </div>
        </template>
      </div>

      <!-- 板块 / 概念排名 -->
      <div v-else class="rank">
        <StateBlock v-if="sorted.length === 0" kind="empty" title="暂无排名数据" message="数据源未返回板块资金流，稍后点「刷新排名」重试。" />
        <table v-else class="tbl">
          <thead>
            <tr>
              <th class="l" style="width: 46px">排名</th>
              <th class="l">名称</th>
              <th class="l" style="width: 42%">主力净额分布</th>
              <th class="sortable" @click="toggleSort('mainNet')">
                主力净额 {{ sortKey === 'mainNet' ? (sortDesc ? '↓' : '↑') : '' }}
              </th>
              <th class="sortable" @click="toggleSort('changePct')">
                涨跌幅 {{ sortKey === 'changePct' ? (sortDesc ? '↓' : '↑') : '' }}
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in sorted" :key="row.code">
              <td class="l num muted">{{ i + 1 }}</td>
              <td class="l name">{{ row.name }}</td>
              <td class="l">
                <span class="rk-track">
                  <span class="rk-zero" />
                  <span
                    class="rk-bar"
                    :class="dir(row.mainNet)"
                    :style="{
                      width: barWidth(row.mainNet),
                      left: row.mainNet >= 0 ? '50%' : 'calc(50% - ' + barWidth(row.mainNet) + ')',
                    }"
                  />
                </span>
              </td>
              <td class="num" :class="dir(row.mainNet)">{{ fmtSignedAmount(row.mainNet) }}</td>
              <td class="num" :class="dir(row.changePct)">{{ fmtPct(row.changePct) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="ff-foot">
      <span class="tiny muted">共 {{ tab === 'stock' ? (stockFlow ? 1 : 0) : sorted.length }} 条</span>
      <span class="spacer" />
      <span class="tiny muted">条形向右为净流入（红），向左为净流出（绿）</span>
    </div>

  </section>
</template>


<style scoped>
  .ff {
    min-height: 0;
  }
  .ff .card-bd {
    display: flex;
    flex-direction: column;
    min-height: 0;
    overflow: hidden;
  }
  .stock-flow {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
  }
  .stock-flow {
    max-width: 940px;
  }
  .sf-grid {
    display: grid;
    grid-template-columns: minmax(320px, 1fr) 300px;
    gap: 18px;
    align-items: start;
  }
  .sf-note {
    padding: 9px 11px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .sf-note-hd {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
  }
  .sf-note-text {
    margin: 0 0 8px;
    color: var(--text-2);
    font-size: 11.5px;
    line-height: 1.7;
  }
  .sf-note-meta {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-top: 7px;
    border-top: 1px solid var(--line);
    color: var(--muted);
    font-size: 11px;
  }
  .sf-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    padding-bottom: 10px;
    border-bottom: 1px solid var(--line);
  }
  .sf-name {
    font-size: 14px;
    font-weight: 600;
  }
  .sf-main {
    font-size: 20px;
    font-weight: 600;
    letter-spacing: 0.4px;
  }
  .sf-parts {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 0 10px;
  }
  .sf-row {
    display: grid;
    grid-template-columns: 64px 1fr 110px;
    align-items: center;
    gap: 10px;
  }
  .sf-label {
    color: var(--muted);
    font-size: 11.5px;
  }
  .sf-track,
  .rk-track {
    position: relative;
    display: block;
    height: 10px;
    border-radius: 2px;
    background: var(--bg);
    border: 1px solid var(--line);
    overflow: hidden;
  }
  .sf-zero,
  .rk-zero {
    position: absolute;
    left: 50%;
    top: -1px;
    bottom: -1px;
    width: 1px;
    background: var(--line-2);
  }
  .sf-bar,
  .rk-bar {
    position: absolute;
    top: 0;
    bottom: 0;
    border-radius: 1px;
  }
  .sf-bar.up,
  .rk-bar.up {
    background: linear-gradient(90deg, rgba(255, 93, 93, 0.55), var(--up));
  }
  .sf-bar.down,
  .rk-bar.down {
    background: linear-gradient(90deg, var(--down), rgba(47, 191, 135, 0.55));
  }
  .sf-bar.flat,
  .rk-bar.flat {
    background: var(--flat);
  }
  .sf-val {
    text-align: right;
    font-size: 12px;
  }
  .sf-meta {
    display: flex;
    gap: 22px;
    padding-top: 10px;
    border-top: 1px solid var(--line);
    color: var(--muted);
    font-size: 11.5px;
  }
  .rank {
    flex: 1 1 auto;
    min-height: 0;
    margin: 0 -12px;
    overflow-y: auto;
  }
  .rk-track {
    height: 8px;
    margin: 0 auto;
    width: 100%;
    max-width: 260px;
  }
  .ff-foot {
    display: flex;
    align-items: center;
    height: 26px;
    padding: 0 12px;
    border-top: 1px solid var(--line);
  }
</style>