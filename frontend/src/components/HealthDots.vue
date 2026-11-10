<script setup lang="ts">
// 数据源健康点：ok/degraded/down → 绿/黄/红。鼠标悬停显示数据源名与原始状态。
import { computed } from 'vue'

const props = defineProps<{ health?: Record<string, string> }>()

interface Item {
  name: string
  raw: string
  cls: string
}

const items = computed<Item[]>(() => {
  const h = props.health ?? {}
  return Object.keys(h)
    .sort()
    .map((name) => {
      const raw = String(h[name] ?? '')
      const lower = raw.toLowerCase()
      const cls = lower === 'ok' || lower === 'healthy' || lower === 'up' ? 'dot-ok' : lower === '' ? 'dot-off' : lower === 'degraded' || lower === 'warn' || lower === 'slow' ? 'dot-warn' : 'dot-err'
      return { name, raw, cls }
    })
})
</script>

<template>
  <div v-if="items.length > 0" class="row health" style="gap: 9px" aria-label="数据源健康">
    <span v-for="it in items" :key="it.name" class="row" style="gap: 4px" :title="it.name + '：' + it.raw">
      <span class="dot" :class="it.cls" />
      <span class="tiny muted src-name">{{ it.name }}</span>
    </span>
  </div>
  <span v-else class="tiny muted">数据源未上报</span>
</template>

<style scoped>
  .health {
    flex: 0 0 auto;
    white-space: nowrap;
  }
  @media (max-width: 1420px) {
    .src-name {
      display: none;
    }
    .health {
      gap: 7px !important;
    }
    .health::before {
      content: '数据源';
      color: var(--muted);
      font-size: 10.5px;
    }
  }
</style>