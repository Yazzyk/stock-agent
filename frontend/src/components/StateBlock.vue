<script setup lang="ts">
// 通用状态块：loading(加载中) / empty(空态) / error(错误) / stale(数据过期)。
// 错误只展示可操作的中文提示，不展示堆栈。
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    kind: 'loading' | 'empty' | 'error' | 'stale'
    title?: string
    message?: string
    actionText?: string
  }>(),
  { title: '', message: '', actionText: '' },
)

const emit = defineEmits<{ (e: 'action'): void }>()

const fallback = computed(() => {
  switch (props.kind) {
    case 'loading':
      return '加载中…'
    case 'empty':
      return '暂无数据'
    case 'error':
      return '加载失败'
    default:
      return '数据可能过期'
  }
})
</script>

<template>
  <div class="state-block" :class="{ 'state-block-stale': kind === 'stale' }" role="status" aria-live="polite">
    <span v-if="kind === 'loading'" class="spinner" aria-hidden="true" />
    <div class="state-block-title">{{ title || fallback }}</div>
    <div v-if="message" class="state-block-msg">{{ message }}</div>
    <button v-if="actionText" class="btn btn-sm" type="button" @click="emit('action')">{{ actionText }}</button>
  </div>
</template>