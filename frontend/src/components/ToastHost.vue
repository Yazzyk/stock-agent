<script setup lang="ts">
import { dismissToast, state } from '../stores/app'

function close(id: number): void {
  dismissToast(id)
}
</script>

<template>
  <div class="toasts" aria-live="polite">
    <div v-for="t in state.toasts" :key="t.id" class="toast" :class="t.kind">
      <span class="grow">{{ t.text }}</span>
      <button type="button" aria-label="关闭提示" @click="close(t.id)">×</button>
    </div>
  </div>
</template>
