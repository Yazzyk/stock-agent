<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import AgentDrawer from './components/AgentDrawer.vue'
import HealthDots from './components/HealthDots.vue'
import NavIcon from './components/NavIcon.vue'
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

/** 折叠导航：省空间，只留图标。持久化到 localStorage。 */
const collapsed = ref(localStorage.getItem('guanlan.nav.collapsed') === '1')

function toggleNav(): void {
  collapsed.value = !collapsed.value
  localStorage.setItem('guanlan.nav.collapsed', collapsed.value ? '1' : '0')
}

/** 扁平索引：与 ⌘1..⌘9 对应。 */
const flat = computed(() => state.slots)
function indexOf(slotId: string): number {
  return flat.value.findIndex((s) => s.id === slotId) + 1
}

const activeSlot = computed(() => state.slots.find((s) => s.id === state.activeId) ?? null)
const activeGroup = computed(() => navGroups.value.find((g) => g.slots.some((s) => s.id === state.activeId))?.name ?? '')

function onKeydown(e: KeyboardEvent): void {
  const meta = e.metaKey || e.ctrlKey
  if (meta && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    searchRef.value?.focusInput()
    return
  }
  if (meta && e.key.toLowerCase() === 'b') {
    e.preventDefault()
    toggleNav()
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
  <div class="app" :class="{ collapsed }">
    <!-- 左导航：顶部 nav-top 是 macOS 红绿灯所在区域（整块用于拖拽窗口），品牌放在它下面，
         这样系统按钮与本应用内容永远不会重叠。 -->
    <nav class="nav" aria-label="主导航">
      <div class="nav-top" aria-hidden="true"></div>
      <div class="nav-brand">
        <span class="brand-mark">观</span>
        <span class="col brand-text" style="gap: 0">
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
            :title="collapsed ? slot.title : (slot.id === state.activeId ? slot.title : undefined)"
            :aria-current="slot.id === state.activeId ? 'page' : undefined"
            @click="activate(slot.id)"
          >
            <NavIcon :name="slot.icon" />
            <span class="label">{{ slot.title }}</span>
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
      <!-- 自绘标题栏（主区顶部）：整条可拖拽移动窗口，按钮/搜索标 no-drag -->
      <header class="titlebar">
        <!-- 折叠/展开：独立的方形图标控件，位置固定在标题栏左端。
             标题栏左内边距 14px、且导航右边界线从 y=44（标题栏下方）才开始画，
             所以本按钮的可点区域 / hover 底永远不会与导航线相交。 -->
        <button
          class="icon-btn tb-toggle"
          type="button"
          :aria-pressed="collapsed"
          :aria-label="collapsed ? '展开导航' : '折叠导航'"
          :title="collapsed ? '展开导航 ⌘B' : '折叠导航 ⌘B'"
          @click="toggleNav"
        >
          <!-- 展开态：三横线（可折叠）；折叠态：右向箭头（可展开） -->
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <template v-if="collapsed">
              <path d="M3 2.5h10M3 13.5h10M9.5 5.5L12.5 8l-3 2.5" />
            </template>
            <template v-else>
              <path d="M2.5 4h11M2.5 8h7M2.5 12h11" />
            </template>
          </svg>
        </button>
        <span class="tb-title">{{ state.ready ? (activeSlot?.title ?? '工作台') : '启动中' }}</span>
        <span v-if="activeGroup" class="tb-group">{{ activeGroup }}</span>
        <SearchBox ref="searchRef" />
        <span class="spacer" />
        <div class="topbar-hud" :title="overallText">
          <HealthDots :health="state.status?.dataHealth" />
          <span class="topbar-divider" />
          <span class="row overall" style="gap: 6px">
            <span class="dot" :class="'dot-' + (overall === 'ok' ? 'ok' : overall === 'warn' ? 'warn' : overall === 'error' ? 'err' : 'off')" />
            <span class="tiny overall-text">{{ overallText }}</span>
          </span>
        </div>
        <button class="drawer-btn" :class="{ 'is-on': state.drawer }" type="button" :aria-pressed="state.drawer" @click="state.drawer = !state.drawer">
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M8 2.2l1.5 3.6 3.6 1.5-3.6 1.5L8 12.4 6.5 8.8 2.9 7.3l3.6-1.5z" />
          </svg>
          <span class="drawer-lbl">Agent 输出</span>
          <span class="kbd">⌘J</span>
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
        <PanelHost v-else-if="activeSlot" :slot="activeSlot" />
      </section>

      <footer class="statusbar">
        <span>插件 <b>{{ state.status?.active ?? 0 }}/{{ state.status?.plugins ?? 0 }}</b></span>
        <span class="sep">|</span>
        <span>工具 <b>{{ state.status?.tools ?? 0 }}</b></span>
        <span class="sep">|</span>
        <span>面板 <b>{{ state.status?.ui ?? 0 }}</b></span>
        <span class="sep">|</span>
        <span>模型 <b>{{ state.defaultModel || '未配置' }}</b></span>
        <span class="sep">|</span>
        <span>服务 <b>{{ (state.status?.services ?? []).length }}</b></span>
        <span class="spacer" />
        <span v-if="state.demo" class="tag tag-accent">演示数据</span>
        <span>更新 {{ fmtClock(state.updatedAt) }}</span>
      </footer>
    </div>

    <AgentDrawer v-if="state.drawer" @close="state.drawer = false" />
    <ToastHost />
  </div>
</template>
