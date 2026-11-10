<script setup lang="ts">
// 自选股面板：分组 tabs + 行情表格 + 同花顺同步 + 5 秒自动刷新。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { messageOf, quotes, watchList, watchSync } from '../lib/data'
import { dir, fmtAmount, fmtClock, fmtPct, fmtPrice } from '../lib/format'
import { state, toast } from '../stores/app'
import type { Quote, SyncResult, WatchItem, WatchSnapshot } from '../types'

interface Row {
  code: string
  name: string
  market: string
  group: string
  price?: number
  changePct?: number
  amount?: number
  turnover?: number
  stale: boolean
}

const snapshot = ref<WatchSnapshot | null>(null)
const quoteMap = ref<Record<string, Quote>>({})
const activeGroup = ref('全部')
const loading = ref(true)
const refreshing = ref(false)
const syncing = ref(false)
const error = ref('')
const syncResult = ref<SyncResult | null>(null)
const auto = ref(true)

let timer: number | null = null

const groups = computed<string[]>(() => ['全部', ...(snapshot.value?.groups ?? [])])

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
      price: q?.price ?? it.price,
      changePct: q?.changePct ?? it.changePct,
      amount: q?.amount ?? it.amount,
      turnover: q?.turnover ?? it.turnover,
      stale: Boolean(q?.stale ?? it.stale),
    }
  })
})

const staleCount = computed(() => rows.value.filter((r) => r.stale).length)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const snap = await watchList()
    snapshot.value = snap
    const codes = snap.items.map((i) => i.code)
    if (codes.length > 0) {
      const list = await quotes(codes)
      const map: Record<string, Quote> = {}
      for (const q of list) map[q.code] = q
      quoteMap.value = map
    } else {
      quoteMap.value = {}
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
  refreshing.value = true
  try {
    const list = await quotes(items.map((i) => i.code))
    const map: Record<string, Quote> = { ...quoteMap.value }
    for (const q of list) map[q.code] = q
    quoteMap.value = map
    error.value = ''
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    refreshing.value = false
  }
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
    } else {
      toast('同步未完成：' + res.message, 'warn')
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
  } finally {
    syncing.value = false
  }
}

function toggleAuto(): void {
  auto.value = !auto.value
  if (auto.value) startTimer()
  else stopTimer()
}

function startTimer(): void {
  stopTimer()
  timer = window.setInterval(() => {
    void refreshQuotes()
  }, 5000)
}

function stopTimer(): void {
  if (timer !== null) window.clearInterval(timer)
  timer = null
}

watch(auto, (on) => {
  if (on) startTimer()
  else stopTimer()
})

watch(
  () => state.focus,
  (f) => {
    if (!f) return
    const item = (snapshot.value?.items ?? []).find((i) => i.code === f.code)
    if (item && activeGroup.value !== '全部' && activeGroup.value !== item.group) activeGroup.value = item.group
  },
)

onMounted(async () => {
  await load()
  startTimer()
})

onBeforeUnmount(stopTimer)
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
        <button class="btn btn-sm btn-accent" type="button" :disabled="syncing" @click="doSync">
          {{ syncing ? '同步中…' : '同步同花顺' }}
        </button>
      </span>
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

    <div v-if="syncResult" class="sync-note" :class="syncResult.success ? 'ok' : 'warn'">
      <div class="row">
        <span class="grow">{{ syncResult.message }}</span>
        <span class="tiny muted">新增 {{ syncResult.added }} · 移除 {{ syncResult.removed }}</span>
        <button class="btn btn-sm btn-ghost" type="button" aria-label="关闭同步提示" @click="syncResult = null">×</button>
      </div>
      <div v-if="syncResult.needWebLogin" class="hint-warn">
        需要在浏览器登录同花顺后重试：打开 t.10jqka.com.cn 登录，再点同步。
      </div>
      <div v-if="syncResult.needFullDiskAccess" class="hint-warn">
        需要给本应用「完全磁盘访问权限」：系统设置 → 隐私与安全性 → 完全磁盘访问权限，添加本应用后重启。
      </div>
      <ul v-if="syncResult.warnings.length > 0" class="warn-list">
        <li v-for="(w, i) in syncResult.warnings" :key="i">{{ w }}</li>
      </ul>
    </div>

    <div class="card-bd flush scroll">
      <StateBlock v-if="loading" kind="loading" title="读取自选与行情…" />
      <StateBlock
        v-else-if="error && rows.length === 0"
        kind="error"
        title="自选读取失败"
        :message="error"
        action-text="重试"
        @action="load"
      />
      <StateBlock
        v-else-if="rows.length === 0"
        kind="empty"
        title="自选为空"
        message="点右上角「同步同花顺」从本机客户端导入分组；也可以在全局搜索里找到标的再加入自选。"
        action-text="同步同花顺"
        @action="doSync"
      />
      <table v-else class="tbl">
        <thead>
          <tr>
            <th class="l">代码</th>
            <th class="l">名称</th>
            <th v-if="activeGroup === '全部'" class="l">分组</th>
            <th>现价</th>
            <th>涨跌幅</th>
            <th>成交额</th>
            <th>换手</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in rows"
            :key="row.code"
            :class="{ sel: state.focus?.code === row.code }"
            @click="state.focus = { code: row.code, name: row.name }"
          >
            <td class="l num">{{ row.code }}</td>
            <td class="l name">
              {{ row.name }}
              <span v-if="row.stale" class="tag tag-accent" style="margin-left: 6px">缓存</span>
            </td>
            <td v-if="activeGroup === '全部'" class="l tiny muted">{{ row.group }}</td>
            <td class="num" :class="dir(row.changePct)">{{ fmtPrice(row.price) }}</td>
            <td class="num" :class="dir(row.changePct)">{{ fmtPct(row.changePct) }}</td>
            <td class="num">{{ fmtAmount(row.amount) }}</td>
            <td class="num">{{ row.turnover === undefined || row.turnover === 0 ? '—' : row.turnover.toFixed(2) + '%' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="wl-foot">
      <span v-if="error && rows.length > 0" class="hint-warn">{{ error }}</span>
      <span v-else-if="staleCount > 0" class="hint-warn">{{ staleCount }} 条为缓存数据（数据源暂不可达）</span>
      <span v-else class="tiny muted">点击任意行可把该标的同步到 K 线面板</span>
      <span class="spacer" />
      <span class="tiny muted">红涨绿跌 · 成交额单位自动换算</span>
    </div>

  </section>
</template>


<style scoped>
  .wl {
    min-height: 0;
  }
  .scroll {
    overflow: auto;
  }
  .sync-note {
    margin: 8px 12px 0;
    padding: 7px 10px;
    border: 1px solid var(--line-2);
    border-left-width: 3px;
    border-radius: var(--radius-sm);
    font-size: 12px;
    color: var(--text-2);
  }
  .sync-note.ok {
    border-left-color: var(--ok);
    background: rgba(47, 191, 135, 0.06);
  }
  .sync-note.warn {
    border-left-color: var(--warn);
    background: rgba(232, 179, 75, 0.06);
  }
  .warn-list {
    margin: 4px 0 0;
    padding-left: 16px;
    color: var(--muted);
    font-size: 11px;
  }
  .wl-foot {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 26px;
    padding: 0 12px;
    border-top: 1px solid var(--line);
    font-size: 11px;
  }
</style>