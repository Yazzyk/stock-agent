<script setup lang="ts">
// 可搜索选股器：输入即搜（自选优先 + 全市场防抖 250ms），↑↓ 选择、Enter 确认、Esc 关闭。
// 下拉展示代码/名称/市场；最近使用的 5 个存 localStorage，和全局搜索互不影响。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { messageOf, searchStocks, watchList } from '../lib/api.market'
import { marketName } from '../lib/format'
import type { StockRef } from '../types'

const props = withDefaults(
  defineProps<{
    /** 当前代码 */
    modelValue: string
    /** 当前名称（用于关闭态显示） */
    name?: string
    placeholder?: string
    /** 容器宽度，0 表示自适应 */
    width?: number
    disabled?: boolean
  }>(),
  { name: '', placeholder: '代码 / 名称', width: 0, disabled: false },
)

const emit = defineEmits<{
  (e: 'update:modelValue', code: string): void
  (e: 'select', pick: { code: string; name: string; market: string }): void
}>()

interface Row {
  code: string
  name: string
  market: string
  hint: string
}

const RECENT_KEY = 'guanlan.market.recent'
const RECENT_MAX = 5

// 自选缓存：多个 picker 共用一次请求，避免每个面板各拉一遍。
let watchCache: StockRef[] = []
let watchLoading: Promise<void> | null = null

function ensureWatch(): Promise<void> {
  if (watchCache.length > 0) return Promise.resolve()
  if (!watchLoading) {
    watchLoading = watchList()
      .then((snap) => {
        watchCache = snap.items.map((i) => ({ code: i.code, name: i.name, market: i.market, kind: i.group }))
      })
      .catch(() => {
        watchCache = []
      })
      .finally(() => {
        watchLoading = null
      })
  }
  return watchLoading
}

const rootEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
const open = ref(false)
const q = ref('')
const loading = ref(false)
const err = ref('')
const cursor = ref(0)
const hits = ref<StockRef[]>([])
const recents = ref<StockRef[]>(loadRecents())

let timer: number | null = null

function loadRecents(): StockRef[] {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw) as unknown
    if (!Array.isArray(parsed)) return []
    const out: StockRef[] = []
    for (const item of parsed.slice(0, RECENT_MAX)) {
      if (typeof item !== 'object' || item === null) continue
      const r = item as Record<string, unknown>
      if (typeof r.code !== 'string' || r.code === '') continue
      out.push({ code: r.code, name: typeof r.name === 'string' ? r.name : '', market: (r.market === 'sh' || r.market === 'bj' ? r.market : 'sz') })
    }
    return out
  } catch {
    return []
  }
}

function saveRecent(pick: { code: string; name: string; market: string }): void {
  const rest = recents.value.filter((r) => r.code !== pick.code)
  recents.value = [{ code: pick.code, name: pick.name, market: pick.market as StockRef['market'] }, ...rest].slice(0, RECENT_MAX)
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(recents.value))
  } catch {
    // localStorage 不可用时只影响「最近使用」，不打断主流程
  }
}

const currentLabel = computed(() => {
  if (props.name !== '') return props.name
  const hit = recents.value.find((r) => r.code === props.modelValue)
  return hit?.name ?? ''
})

function localRows(keyword: string): Row[] {
  const kw = keyword.trim().toLowerCase()
  const pool: StockRef[] = []
  for (const r of recents.value) pool.push(r)
  for (const r of watchCache) if (!pool.some((p) => p.code === r.code)) pool.push(r)
  const matched = kw === '' ? pool : pool.filter((r) => r.code.includes(kw) || r.name.toLowerCase().includes(kw))
  return matched.slice(0, 8).map((r) => ({
    code: r.code,
    name: r.name,
    market: r.market,
    hint: recents.value.some((x) => x.code === r.code) ? '最近' : '自选',
  }))
}

const rows = computed<Row[]>(() => {
  const local = localRows(q.value)
  const seen = new Set(local.map((r) => r.code))
  const extra: Row[] = []
  for (const hit of hits.value) {
    if (seen.has(hit.code)) continue
    seen.add(hit.code)
    extra.push({ code: hit.code, name: hit.name, market: hit.market, hint: hit.kind ?? '全市场' })
  }
  return [...local, ...extra].slice(0, 12)
})

function schedule(keyword: string): void {
  if (timer !== null) window.clearTimeout(timer)
  err.value = ''
  if (keyword.trim() === '') {
    hits.value = []
    loading.value = false
    return
  }
  timer = window.setTimeout(() => {
    void run(keyword)
  }, 250)
}

async function run(keyword: string): Promise<void> {
  loading.value = true
  try {
    hits.value = await searchStocks(keyword, 10)
    if (hits.value.length === 0 && localRows(keyword).length === 0) err.value = '未找到匹配的代码或名称'
    cursor.value = 0
  } catch (e) {
    hits.value = []
    err.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function openPicker(): Promise<void> {
  if (props.disabled) return
  open.value = true
  q.value = ''
  hits.value = []
  err.value = ''
  cursor.value = 0
  await ensureWatch()
  window.setTimeout(() => {
    inputEl.value?.focus()
    inputEl.value?.select()
  }, 0)
}

function closePicker(): void {
  open.value = false
  q.value = ''
  hits.value = []
  err.value = ''
}

function choose(row: Row): void {
  emit('update:modelValue', row.code)
  emit('select', { code: row.code, name: row.name, market: row.market })
  saveRecent({ code: row.code, name: row.name, market: row.market })
  closePicker()
  inputEl.value?.blur()
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    e.preventDefault()
    closePicker()
    inputEl.value?.blur()
    return
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    const n = rows.value.length
    if (n > 0) cursor.value = (cursor.value + 1) % n
    return
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    const n = rows.value.length
    if (n > 0) cursor.value = (cursor.value - 1 + n) % n
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    const row = rows.value[cursor.value]
    if (row) choose(row)
    else if (q.value.trim() !== '') void run(q.value)
  }
}

function onInput(): void {
  open.value = true
  cursor.value = 0
  schedule(q.value)
}

function onBlur(): void {
  window.setTimeout(() => {
    const root = rootEl.value
    if (root && root.contains(document.activeElement)) return
    closePicker()
  }, 130)
}

function onDocDown(e: MouseEvent): void {
  if (!open.value) return
  const el = rootEl.value
  if (el && e.target instanceof Node && el.contains(e.target)) return
  closePicker()
}

watch(
  () => props.modelValue,
  () => {
    cursor.value = 0
  },
)

onMounted(() => {
  document.addEventListener('mousedown', onDocDown)
  void ensureWatch()
})

onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocDown)
  if (timer !== null) window.clearTimeout(timer)
})

defineExpose({ open: openPicker, close: closePicker })
</script>

<template>
  <div ref="rootEl" class="picker" :class="{ disabled }" :style="width > 0 ? { width: width + 'px' } : undefined">
    <button
      v-if="!open"
      class="picker-btn"
      type="button"
      :disabled="disabled"
      :aria-label="'选择标的，当前 ' + (modelValue || '无')"
      @click="openPicker"
    >
      <span class="num picker-code">{{ modelValue || '—' }}</span>
      <span class="picker-name ellipsis">{{ currentLabel || '选择标的' }}</span>
      <span class="picker-caret" aria-hidden="true">▾</span>
    </button>
    <div v-else class="picker-input">
      <span class="tiny muted" aria-hidden="true">⌕</span>
      <input
        ref="inputEl"
        v-model="q"
        type="text"
        spellcheck="false"
        :placeholder="placeholder"
        aria-label="搜索标的"
        @input="onInput"
        @keydown="onKey"
        @blur="onBlur"
      />
      <span v-if="loading" class="tiny muted">搜索中</span>
    </div>
    <div v-if="open" class="picker-pop" role="listbox">
      <div v-if="rows.length === 0" class="picker-empty">{{ err || '输入代码或名称开始搜索' }}</div>
      <button
        v-for="(row, i) in rows"
        :key="row.code"
        class="picker-row"
        :class="{ on: i === cursor }"
        type="button"
        role="option"
        :aria-selected="i === cursor"
        @mousedown.prevent="choose(row)"
        @mouseenter="cursor = i"
      >
        <span class="num picker-row-code">{{ row.code }}</span>
        <span class="grow ellipsis">{{ row.name }}</span>
        <span class="picker-row-mkt">{{ marketName(row.market) }}</span>
        <span class="tiny muted">{{ row.hint }}</span>
      </button>
      <div v-if="err && rows.length > 0" class="picker-empty">{{ err }}</div>
    </div>
  </div>
</template>

<style scoped>
  .picker {
    position: relative;
    display: inline-flex;
    min-width: 128px;
    max-width: 260px;
  }
  .picker.disabled {
    opacity: 0.6;
  }
  .picker-btn {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    height: 28px;
    padding: 0 8px;
    border: 1px solid var(--line-2);
    border-radius: var(--radius-sm);
    background: var(--bg-2);
    color: var(--text);
    font-size: 12px;
    text-align: left;
    transition: border-color 0.14s var(--ease), background 0.14s var(--ease);
  }
  .picker-btn:hover {
    border-color: var(--line-3);
    background: var(--panel-2);
  }
  .picker-code {
    color: var(--text-2);
    font-size: 11.5px;
  }
  .picker-name {
    flex: 1 1 auto;
    min-width: 0;
  }
  .picker-caret {
    color: var(--muted);
    font-size: 10px;
  }
  .picker-input {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    height: 28px;
    padding: 0 8px;
    border: 1px solid var(--accent);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
    box-shadow: 0 0 0 3px rgba(233, 180, 76, 0.13);
  }
  .picker-input input {
    flex: 1 1 auto;
    min-width: 0;
    border: 0;
    outline: none;
    background: transparent;
    color: var(--text);
    font-size: 12px;
  }
  .picker-input input::placeholder {
    color: var(--muted);
  }
  .picker-pop {
    position: absolute;
    top: 32px;
    left: 0;
    z-index: 60;
    width: max(100%, 300px);
    max-height: 320px;
    overflow-y: auto;
    border: 1px solid var(--line-2);
    border-radius: var(--radius-sm);
    background: var(--panel);
    box-shadow: var(--shadow-3);
  }
  .picker-row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 10px;
    border: 0;
    background: transparent;
    color: var(--text-2);
    font-size: 12px;
    text-align: left;
    white-space: nowrap;
  }
  .picker-row:hover,
  .picker-row.on {
    background: var(--panel-3);
    color: var(--text);
  }
  .picker-row-code {
    color: var(--muted);
    font-size: 11.5px;
  }
  .picker-row.on .picker-row-code {
    color: var(--accent-2);
  }
  .picker-row-mkt {
    display: inline-flex;
    align-items: center;
    height: 16px;
    padding: 0 4px;
    border: 1px solid var(--line-2);
    border-radius: 4px;
    color: var(--muted);
    font-size: 10px;
  }
  .picker-empty {
    padding: 9px 10px;
    color: var(--muted);
    font-size: 11.5px;
  }
</style>
