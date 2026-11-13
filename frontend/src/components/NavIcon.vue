<script setup lang="ts">
// 侧边栏图标：插件 slot.icon 是语义名（star/waves/candles…），
// 同时兼容旧的插件 id（watchlist/fundflow…），统一映射到 16px 线性图标。
import { computed } from 'vue'

const props = defineProps<{ name?: string; size?: number }>()

/** 各面板的图标路径（16×16 视框、线性描边）。 */
const PATHS: Record<string, string> = {
  // 自选股：五角星
  star: 'M8 2.2l1.76 3.56 3.94.57-2.85 2.78.67 3.92L8 11.18l-3.52 1.85.67-3.92L2.3 6.33l3.94-.57L8 2.2Z',
  // 资金流：两层波浪
  waves:
    'M1.5 6.1c1.3 0 1.3-1.2 2.6-1.2s1.3 1.2 2.6 1.2 1.3-1.2 2.6-1.2 1.3 1.2 2.6 1.2 1.3-1.2 2.6-1.2M1.5 10.7c1.3 0 1.3-1.2 2.6-1.2s1.3 1.2 2.6 1.2 1.3-1.2 2.6-1.2 1.3 1.2 2.6 1.2 1.3-1.2 2.6-1.2',
  // K 线：两根带影线的蜡烛
  candles:
    'M4.5 2v1.8M4.5 12.2V14M3.1 3.8h2.8v8.4H3.1V3.8ZM11.5 2v3.2M11.5 13V14M10.1 5.2h2.8v7.8h-2.8V5.2Z',
  // 量化研究：Σ
  sigma: 'M4 3.5h8l-4.5 4.5L12 12.5H4',
  // 市场中心：脉冲
  pulse: 'M1.5 8h3l1.5-4.5 3 9L11 8h3.5',
  // Watch 哨兵：雷达
  radar: 'M8 2.5a5.5 5.5 0 1 0 5.5 5.5M8 8l3.9-3.9M8 5.6A2.4 2.4 0 1 0 10.4 8M8 8h.01',
  // Agent 工作台：机器人
  robot:
    'M6 2.5v2M10 2.5v2M4.5 4.5h7a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2h-7a2 2 0 0 1-2-2v-5a2 2 0 0 1 2-2ZM6 7.2v1.6M10 7.2v1.6M6 11h4',
  // 知识库：翻开的书
  book: 'M3 3.2h3.2A1.8 1.8 0 0 1 8 5v8a1.6 1.6 0 0 0-1.6-1.6H3V3.2ZM13 3.2H9.8A1.8 1.8 0 0 0 8 5v8a1.6 1.6 0 0 1 1.6-1.6H13V3.2Z',
  // 模型设置：芯片
  cpu: 'M5.2 5.2h5.6v5.6H5.2V5.2ZM6.8 2v2.2M9.2 2v2.2M6.8 11.8V14M9.2 11.8V14M2 6.8h2.2M2 9.2h2.2M11.8 6.8H14M11.8 9.2H14',
  // 设置中心：齿轮
  settings:
    'M8 5.8a2.2 2.2 0 1 0 0 4.4 2.2 2.2 0 0 0 0-4.4ZM13.1 9.3l.7.6-1 1.7-.9-.3a5 5 0 0 1-1.2.7l-.2 1H8.5l-.2-1a5 5 0 0 1-1.2-.7l-.9.3-1-1.7.7-.6a4.8 4.8 0 0 1 0-1.4l-.7-.6 1-1.7.9.3c.4-.3.8-.5 1.2-.7l.2-1h2l.2 1c.4.2.8.4 1.2.7l.9-.3 1 1.7-.7.6a4.8 4.8 0 0 1 0 1.4Z',
  // 数据源：三层数据库盘片
  database: 'M8 1.8c2.9 0 5.2.9 5.2 2v8.4c0 1.1-2.3 2-5.2 2s-5.2-.9-5.2-2V3.8c0-1.1 2.3-2 5.2-2ZM2.8 3.8c0 1.1 2.3 2 5.2 2s5.2-.9 5.2-2M2.8 7.8c0 1.1 2.3 2 5.2 2s5.2-.9 5.2-2M2.8 11.6c0 1.1 2.3 2 5.2 2s5.2-.9 5.2-2',
}

/** 旧键名到语义名的别名：插件 id 与历史图标名都能落到同一套图形。 */
const ALIASES: Record<string, string> = {
  watchlist: 'star',
  fundflow: 'waves',
  kline: 'candles',
  chart: 'candles',
  marketcenter: 'pulse',
  watch: 'radar',
  agentlab: 'robot',
  quant: 'sigma',
  knowledge: 'book',
  modelsettings: 'cpu',
  console: 'settings',
  gear: 'settings',
  fuyao: 'database',
  datasource: 'database',
}

/** 解析图标名：先别名再直取，未命中时返回中性方块。 */
function pathOf(name: string | undefined): string {
  const key = name ?? ''
  const semantic = ALIASES[key] ?? key
  return PATHS[semantic] ?? 'M3 3h10v10H3zM6 3v10M3 6h10'
}

const path = computed(() => pathOf(props.name))
</script>

<template>
  <svg
    :width="size ?? 16"
    :height="size ?? 16"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    stroke-width="1.35"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    class="nav-icon"
  >
    <path :d="path" />
  </svg>
</template>

<style scoped>
  .nav-icon {
    flex: 0 0 auto;
    opacity: 0.9;
  }
</style>
