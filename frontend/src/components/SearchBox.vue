<script setup lang="ts">
// 全局搜索：⌘K 聚焦，走 market.search 工具；选中后定位到 K 线面板。
import { ref } from 'vue'
import { messageOf, searchStocks } from '../lib/data'
import { focusStock } from '../stores/app'
import type { StockRef } from '../types'

const q = ref('')
const open = ref(false)
const loading = ref(false)
const rows = ref<StockRef[]>([])
const cursor = ref(0)
const err = ref('')
const inputEl = ref<HTMLInputElement | null>(null)

let timer: number | null = null

function focusInput(): void {
  inputEl.value?.focus()
  inputEl.value?.select()
}

defineExpose({ focusInput })

function schedule(): void {
  if (timer !== null) window.clearTimeout(timer)
  const keyword = q.value.trim()
  err.value = ''
  if (keyword === '') {
    rows.value = []
    open.value = false
    return
  }
  timer = window.setTimeout(() => {
    void run(keyword)
  }, 200)
}

async function run(keyword: string): Promise<void> {
  loading.value = true
  try {
    rows.value = await searchStocks(keyword, 8)
    cursor.value = 0
    open.value = true
    err.value = rows.value.length === 0 ? '未找到匹配的代码或名称' : ''
  } catch (e) {
    rows.value = []
    open.value = true
    err.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

function choose(row: StockRef): void {
  focusStock(row.code, row.name)
  q.value = ''
  rows.value = []
  open.value = false
  inputEl.value?.blur()
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    open.value = false
    inputEl.value?.blur()
    return
  }
  if (!open.value || rows.value.length === 0) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    cursor.value = (cursor.value + 1) % rows.value.length
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    cursor.value = (cursor.value - 1 + rows.value.length) % rows.value.length
  } else if (e.key === 'Enter') {
    e.preventDefault()
    const row = rows.value[cursor.value]
    if (row) choose(row)
  }
}

function onBlur(): void {
  window.setTimeout(() => {
    open.value = false
  }, 130)
}
</script>

<template>
  <div class="search">
    <div class="search-input">
      <span class="tiny muted" aria-hidden="true">⌕</span>
      <input
        ref="inputEl"
        v-model="q"
        type="text"
        placeholder="搜索代码 / 名称"
        aria-label="全局搜索"
        spellcheck="false"
        @input="schedule"
        @keydown="onKey"
        @blur="onBlur"
      />
      <span class="kbd">⌘K</span>
    </div>
    <div v-if="open" class="search-pop" role="listbox">
      <div v-if="loading" class="search-empty">搜索中…</div>
      <div v-else-if="err && rows.length === 0" class="search-empty">{{ err }}</div>
      <button
        v-for="(row, i) in rows"
        :key="row.code + row.market"
        class="search-row"
        :class="{ on: i === cursor }"
        type="button"
        role="option"
        :aria-selected="i === cursor"
        @mousedown.prevent="choose(row)"
        @mouseenter="cursor = i"
      >
        <span class="code">{{ row.code }}</span>
        <span class="grow">{{ row.name }}</span>
        <span class="tiny muted">{{ row.kind }}</span>
      </button>
    </div>
  </div>
</template>
