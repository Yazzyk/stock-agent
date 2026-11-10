<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import AgentDrawer from './components/AgentDrawer.vue'
import HealthDots from './components/HealthDots.vue'
import PanelHost from './components/PanelHost.vue'
import SearchBox from './components/SearchBox.vue'
import StateBlock from './components/StateBlock.vue'
import ToastHost from './components/ToastHost.vue'
import { fmtClock } from './lib/format'
import { activate, activateByIndex, boot, navGroups, overall, overallText, refreshStatus, state } from './stores/app'
import { subscribeAgent } from './stores/agent'

const searchRef = ref<InstanceType<typeof SearchBox> | null>(null)

let unsubAgent: (() => void) | null = null
let statusTimer: number | null = null

/** 扁平索引：与 ⌘1..⌘9 对应。 */
const flat = computed(() => state.slots)
function indexOf(slotId: string): number {
  return flat.value.findIndex((s) => s.id === slotId) + 1
}

function onKeydown(e: KeyboardEvent): void {
  const meta = e.metaKey || e.ctrlKey
  if (meta && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    searchRef.value?.focusInput()
    return
  }
  if (meta && /^[1-9]$/.test(e.key)) {
    e.preventDefault()
    activateByIndex(Number(e.key) - 1)
    return
  }
  if (meta && e.key.toLowerCase() === 'j') {
    e.preventDefault()
    state.drawer = !state.drawer
    return
  }
  if (e.key === 'Escape' && state.drawer) state.drawer = false
}

async function retry(): Promise<void> {
  state.error = ''
  await boot()
}

onMounted(async () => {
  window.addEventListener('keydown', onKeydown)
  unsubAgent = subscribeAgent()
  await boot()
  statusTimer = window.setInterval(() => {
    void refreshStatus()
  }, 30000)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  if (unsubAgent) unsubAgent()
  if (statusTimer !== null) window.clearInterval(statusTimer)
})
</script>

<template>
  <div class="app">
    <nav class="nav" aria-label="主导航">
      <div class="nav-brand">
        <span class="brand-mark">观</span>
        <span class="col" style="gap: 0">
          <span class="brand-name">观澜</span>
          <span class="brand-sub">A 股研究终端</span>
        </span>
      </div>
      <div class="nav-scroll">
        <template v-for="g in navGroups" :key="g.name">
          <div class="nav-group">{{ g.name }}</div>
          <button
            v-for="slot in g.slots"
            :key="slot.id"
            class="nav-item"
            :class="{ on: slot.id === state.activeId }"
            type="button"
            :aria-current="slot.id === state.activeId ? 'page' : undefined"
            @click="activate(slot.id)"
          >
            <span class="grow ellipsis">{{ slot.title }}</span>
            <span class="idx">{{ indexOf(slot.id) <= 9 ? '⌘' + indexOf(slot.id) : '' }}</span>
          </button>
        </template>
        <div v-if="!state.ready" class="nav-group">读取插件中…</div>
        <div v-else-if="state.slots.length === 0" class="nav-group">无可用面板</div>
      </div>
      <div class="nav-foot">
        <div>只读研究 · 无交易接口</div>
        <div v-if="state.demo" class="hint-warn">演示数据模式</div>
      </div>
    </nav>

    <div class="main">
      <header class="topbar">
        <span class="topbar-title">{{ state.ready ? (state.slots.find((s) => s.id === state.activeId)?.title ?? '工作台') : '启动中' }}</span>
        <span class="topbar-sep" />
        <SearchBox ref="searchRef" />
        <span class="spacer" />
        <HealthDots :health="state.status?.dataHealth" />
        <span class="topbar-sep" />
        <span class="row overall" style="gap: 6px" :title="overallText">
          <span class="dot" :class="'dot-' + (overall === 'ok' ? 'ok' : overall === 'warn' ? 'warn' : overall === 'error' ? 'err' : 'off')" />
          <span class="tiny muted overall-text">{{ overallText }}</span>
        </span>
        <button class="btn btn-sm drawer-btn" type="button" :aria-pressed="state.drawer" @click="state.drawer = !state.drawer">
          <span class="drawer-lbl">Agent 输出</span> <span class="kbd">⌘J</span>
        </button>
      </header>

      <section class="content">
        <StateBlock v-if="!state.ready" kind="loading" title="正在连接内核…" />
        <StateBlock
          v-else-if="state.error"
          kind="error"
          title="内核不可用"
          :message="state.error"
          action-text="重试"
          @action="retry"
        />
        <StateBlock
          v-else-if="state.slots.length === 0"
          kind="empty"
          title="没有可用面板"
          message="所有插件都未激活。请查看内核日志中插件激活失败的原因（依赖缺失或注册冲突）。"
        />
        <PanelHost v-else-if="state.slots.find((s) => s.id === state.activeId)" :slot="state.slots.find((s) => s.id === state.activeId)!" />
      </section>

      <footer class="statusbar">
        <span>插件 <b class="dim">{{ state.status?.active ?? 0 }}/{{ state.status?.plugins ?? 0 }}</b></span>
        <span class="sep">|</span>
        <span>工具 <b class="dim">{{ state.status?.tools ?? 0 }}</b></span>
        <span class="sep">|</span>
        <span>面板 <b class="dim">{{ state.status?.ui ?? 0 }}</b></span>
        <span class="sep">|</span>
        <span>模型 <b class="dim">{{ state.defaultModel || '未配置' }}</b></span>
        <span class="sep">|</span>
        <span>服务 <b class="dim">{{ (state.status?.services ?? []).length }}</b></span>
        <span class="spacer" />
        <span v-if="state.demo" class="tag tag-accent">演示数据</span>
        <span>更新 {{ fmtClock(state.updatedAt) }}</span>
      </footer>
    </div>

    <AgentDrawer v-if="state.drawer" @close="state.drawer = false" />
    <ToastHost />
  </div>
</template>