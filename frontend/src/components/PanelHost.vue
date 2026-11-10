<script setup lang="ts">
// 面板宿主：按 UI slot 的 component 字段从 src/panels/*.vue 动态取组件。
// 构建期 glob 一次，运行期只查表，不新增打包依赖。
import { computed, defineAsyncComponent, type Component } from 'vue'
import type { UISlot } from '../types'
import StateBlock from './StateBlock.vue'

const props = defineProps<{ slot: UISlot }>()

const modules = import.meta.glob<{ default: Component }>('../panels/*.vue')
const registry = new Map<string, Component>()
for (const [path, loader] of Object.entries(modules)) {
  const file = path.split('/').pop() ?? path
  registry.set(file.replace(/\.vue$/, ''), defineAsyncComponent(loader))
}

const comp = computed<Component | null>(() => registry.get(props.slot.component) ?? null)
</script>

<template>
  <component :is="comp" v-if="comp" :key="slot.id" />
  <div v-else class="card">
    <StateBlock
      kind="error"
      title="面板组件缺失"
      :message="'未找到 src/panels/' + slot.component + '.vue。请确认 UI slot 的 component 与面板文件名一致。'"
    />
  </div>
</template>
