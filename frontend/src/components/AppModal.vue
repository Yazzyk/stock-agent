<!-- 通用居中模态：复用全局 .scrim/.sheet 类，Esc 或点遮罩关闭。
     面板里所有「编辑/确认」弹层都用它，避免各面板各写一套遮罩。 -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'

withDefaults(
  defineProps<{
    title: string
    sub?: string
    /** 宽版（编队、审计表用） */
    wide?: boolean
    /** 点遮罩是否关闭；半填表单建议关掉防误触 */
    closeOnScrim?: boolean
  }>(),
  { sub: '', wide: false, closeOnScrim: true },
)

const emit = defineEmits<{ (e: 'close'): void }>()

function onKey(e: KeyboardEvent): void {
  if (e.key !== 'Escape') return
  e.stopPropagation()
  emit('close')
}

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <teleport to="body">
    <div class="scrim" @click.self="closeOnScrim && emit('close')">
      <div class="sheet" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="title">
        <div class="sheet-hd">
          <span class="sheet-title">{{ title }}</span>
          <span v-if="sub" class="tiny muted ellipsis">{{ sub }}</span>
          <span class="spacer" />
          <slot name="head" />
          <button class="btn btn-sm btn-ghost" type="button" aria-label="关闭" @click="emit('close')">×</button>
        </div>
        <div class="sheet-bd"><slot /></div>
        <div v-if="$slots.footer" class="sheet-ft"><slot name="footer" /></div>
      </div>
    </div>
  </teleport>
</template>
