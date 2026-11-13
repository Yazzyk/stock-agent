<script setup lang="ts">
// 数据源健康点：可用=绿、降级=黄、失败=红、未使用/未接入=灰。鼠标悬停显示数据源名与原始状态。
import { computed } from 'vue'

const props = defineProps<{ health?: Record<string, string> }>()

interface Item {
  name: string
  raw: string
  cls: string
  hint: string
}

/** 按后端中文/英文状态文案归类健康点颜色；未知文案归中性灰，避免把计量值误报成红点。 */
function classify(raw: string): string {
  const text = raw.trim().toLowerCase()
  if (text === '' || text.startsWith('未使用') || text.startsWith('未接入')) return 'dot-off'
  if (text.startsWith('可用') || text.startsWith('ok') || text.startsWith('healthy') || text.startsWith('up')) return 'dot-ok'
  if (
    text.startsWith('降级') ||
    text.startsWith('degraded') ||
    text.startsWith('warn') ||
    text.startsWith('slow') ||
    text.startsWith('慢') ||
    text.startsWith('超时')
  ) {
    return 'dot-warn'
  }
  if (text.startsWith('失败') || text.startsWith('error') || text.startsWith('down') || text.startsWith('fail')) return 'dot-err'
  return 'dot-off'
}

/** 灰点场景的简短文案：沿用后端的「未使用/未接入」前缀，空值显示未上报。 */
function offHint(raw: string): string {
  const text = raw.trim()
  if (text.startsWith('未使用')) return '未使用'
  if (text.startsWith('未接入')) return '未接入'
  if (text === '') return '未上报'
  return ''
}

/** 计量值（如 cache 的「12 项」）不是数据源状态，不参与顶部指示灯展示。 */
function isMetric(name: string, raw: string): boolean {
  return name === 'cache' || /^\d+\s*项$/.test(raw.trim())
}

const items = computed<Item[]>(() => {
  const h = props.health ?? {}
  return Object.keys(h)
    .sort()
    .filter((name) => !isMetric(name, String(h[name] ?? '')))
    .map((name) => {
      const raw = String(h[name] ?? '')
      const cls = classify(raw)
      return { name, raw, cls, hint: cls === 'dot-off' ? offHint(raw) : '' }
    })
})
</script>

<template>
  <div v-if="items.length > 0" class="row health" style="gap: 8px" aria-label="数据源健康">
    <span class="health-label">数据源</span>
    <span v-for="it in items" :key="it.name" class="row" style="gap: 4px" :title="it.name + '：' + (it.raw || '未上报')">
      <span class="dot" :class="it.cls" />
      <span v-if="it.hint" class="tiny muted src-state">{{ it.hint }}</span>
    </span>
  </div>
  <span v-else class="tiny muted">数据源未上报</span>
</template>

<style scoped>
  .health {
    flex: 0 0 auto;
    white-space: nowrap;
  }
  .health-label {
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.3px;
  }
  .src-state {
    font-size: 10.5px;
  }
  @media (max-width: 1180px) {
    .src-state {
      display: none;
    }
  }
</style>
