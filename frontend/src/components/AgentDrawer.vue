<script setup lang="ts">
// 右侧 Agent 抽屉：任意面板可唤起，展示 agentlab 的流式日志与报告。
import { computed, nextTick, ref, watch } from 'vue'
import { mdToHtml } from '../lib/markdown'
import { activate, state } from '../stores/app'
import { agentState } from '../stores/agent'

const emit = defineEmits<{ (e: 'close'): void }>()

const tab = ref<'log' | 'report'>('log')
const logEl = ref<HTMLElement | null>(null)

const reportHtml = computed(() => mdToHtml(agentState.report))

watch(
  () => agentState.log.length,
  async () => {
    if (tab.value !== 'log') return
    await nextTick()
    if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
  },
)

watch(
  () => agentState.report,
  (val) => {
    if (val !== '') tab.value = 'report'
  },
)

function goAgentlab(): void {
  const slot = state.slots.find((s) => s.component === 'agentlab')
  if (slot) {
    activate(slot.id)
    emit('close')
  }
}

const stateClass = computed(() => {
  if (agentState.running) return 'dot-warn'
  if (agentState.stateText === '失败') return 'dot-err'
  return agentState.stateText === '完成' ? 'dot-ok' : 'dot-off'
})
</script>

<template>
  <aside class="drawer" aria-label="Agent 输出">
    <div class="drawer-hd">
      <span class="dot" :class="stateClass" />
      <span class="drawer-title">Agent 输出</span>
      <span class="tiny muted">{{ agentState.worker || '未运行' }} · {{ agentState.stateText || '空闲' }}</span>
      <span class="spacer" />
      <button class="btn btn-sm btn-ghost" type="button" @click="goAgentlab">打开面板</button>
      <button class="btn btn-sm btn-ghost" type="button" aria-label="关闭抽屉" @click="emit('close')">×</button>
    </div>
    <div class="tabs" style="flex: 0 0 auto">
      <button class="tab" :class="{ on: tab === 'log' }" type="button" @click="tab = 'log'">流式日志</button>
      <button class="tab" :class="{ on: tab === 'report' }" type="button" @click="tab = 'report'">报告</button>
    </div>
    <div class="drawer-bd">
      <template v-if="tab === 'log'">
        <div ref="logEl" class="drawer-log">
          <div v-if="agentState.log.length === 0" class="tiny muted" style="padding: 10px">
            暂无输出。到 Agent 实验室启动一个 worker，或按 ⌘J 随时查看。
          </div>
          <div v-for="line in agentState.log" :key="line.id" class="log-line" :class="line.kind">
            <span class="log-time">{{ line.time }}</span>
            <span class="log-text">{{ line.text }}</span>
          </div>
        </div>
      </template>
      <template v-else>
        <div class="drawer-report">
          <div v-if="agentState.report === ''" class="tiny muted" style="padding: 10px">还没有报告。</div>
          <div v-else class="md" v-html="reportHtml" />
        </div>
      </template>
    </div>

  </aside>
</template>


<style scoped>
  .drawer-log {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 6px 0;
    font: 11.5px/1.65 var(--mono);
  }
  .log-line {
    display: flex;
    gap: 8px;
    padding: 2px 12px;
    color: var(--text-2);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .log-line:hover {
    background: var(--panel);
  }
  .log-time {
    flex: 0 0 auto;
    color: var(--muted);
  }
  .log-text {
    flex: 1 1 auto;
    min-width: 0;
  }
  .log-line.state .log-text {
    color: var(--accent-2);
  }
  .log-line.tool .log-text {
    color: var(--info);
  }
  .log-line.error .log-text {
    color: var(--err);
  }
  .log-line.ok .log-text {
    color: var(--ok);
  }
  .log-line.user {
    border-left: 2px solid var(--info);
    background: rgba(76, 157, 255, 0.07);
  }
  .log-line.user .log-time {
    color: var(--info);
  }
  .log-line.user .log-text {
    color: #bcd8ff;
  }
  .log-line.user-pending .log-text {
    color: var(--muted);
    font-style: italic;
  }
  .drawer-report {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 10px 12px;
  }
</style>
