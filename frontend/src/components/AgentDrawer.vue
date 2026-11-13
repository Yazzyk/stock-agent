<script setup lang="ts">
// 右侧 Agent 抽屉：任意面板可唤起，展示 agentlab 的流式日志与报告。
import { computed, nextTick, ref, watch } from 'vue'
import { mdToHtml } from '../lib/markdown'
import { activate, state } from '../stores/app'
import { agentContext, shortTokens } from '../lib/api.agent'
import { stateTone, toneDot } from '../lib/agentview'
import { agentState, requestContextPage, setContext } from '../stores/agent'
import ContextMeter from './ContextMeter.vue'

const emit = defineEmits<{ (e: 'close'): void }>()

const tab = ref<'log' | 'report'>('log')
const logEl = ref<HTMLElement | null>(null)

const reportHtml = computed(() => mdToHtml(agentState.report))
/** 抽屉只展示对话过程：系统提示词上万字，塞进流式日志会把真实输出挤没（轨迹页可看全文）。 */
const visibleLog = computed(() => agentState.log.filter((l) => l.kind !== 'system'))
/** 日志滚动触发器：流式段落只增长 length 不变，所以要同时看最后一行文本长度。 */
const logTick = computed(() => {
  const last = visibleLog.value[visibleLog.value.length - 1]
  return visibleLog.value.length + ':' + (last ? last.text.length : 0)
})

watch(
  () => logTick.value,
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

/** 抽屉里的上下文环也能刷新：弹层里的「刷新」按钮不该点了没反应。 */
async function refreshContext(): Promise<void> {
  try {
    const c = await agentContext(agentState.sessionId)
    if (c) setContext(c)
  } catch {
    // 没有会话时后端会给出可操作错误：这里不打断抽屉的其它内容
  }
}

/** 抽屉里的小环点「查看完整洞察」：先给工作台发一次切页请求，再激活面板。 */
function openContextPage(): void {
  requestContextPage()
  goAgentlab()
}

function goAgentlab(): void {
  const slot = state.slots.find((s) => s.component === 'agentlab')
  if (slot) {
    activate(slot.id)
    emit('close')
  }
}

/** 空回复时的一句话原因：优先用后端给的 emptyReason，其次按结束原因描述。 */
const diagText = computed(() => {
  const run = agentState.lastRun
  if (run?.emptyReason) return run.emptyReason
  if (run?.finishReason === 'length') return '模型返回空正文（推理占满输出预算）'
  if (run?.finishReason) return '本轮没有正文产出（结束原因：' + run.finishReason + '）'
  return '本轮没有正文产出'
})

/** 事实行：步数与 token 是没拿到诊断时唯一能讲清楚的东西。 */
const diagFacts = computed(() => {
  const run = agentState.lastRun
  const steps = run?.steps ?? agentState.context?.steps ?? 0
  const tokens = run?.tokens ?? agentState.context?.usedTokens ?? 0
  const has = (run?.diagnostics.length ?? 0) > 0
  return (
    (has ? '' : '后端未返回诊断信息 · ') +
    '已跑 ' + steps + ' 步 · ~' + shortTokens(tokens) + ' token' +
    (run?.model ? ' · ' + run.model : '') +
    '（明细在 Agent 工作台 → 报告）'
  )
})

/** 抽屉头部状态点：运行中一律琥珀，其余复用工作台统一的状态→色调映射（已完成=绿）。 */
const stateClass = computed(() => (agentState.running ? 'dot-warn' : toneDot(stateTone(agentState.stateText))))
</script>

<template>
  <aside class="drawer" aria-label="Agent 输出">
    <div class="drawer-hd">
      <span class="dot" :class="stateClass" />
      <span class="drawer-title">Agent 输出</span>
      <span class="tiny muted">{{ agentState.worker || '未运行' }} · {{ agentState.stateText || '空闲' }}</span>
      <!-- 上下文环：运行中随时可点开看 system/tools/messages 的分项占用 -->
      <ContextMeter :context="agentState.context" compact @refresh="refreshContext" @open="openContextPage" />
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
          <div v-if="visibleLog.length === 0" class="tiny muted" style="padding: 10px">
            暂无输出。到 Agent 实验室启动一个 worker，或按 ⌘J 随时查看。
          </div>
          <div v-for="line in visibleLog" :key="line.id" class="log-line" :class="[line.kind, line.streaming ? 'streaming' : '']">
            <span class="log-time">{{ line.time }}</span>
            <span class="log-text">{{ line.text }}<span v-if="line.repeat && line.repeat > 1" class="log-repeat">×{{ line.repeat }}</span></span>
          </div>
        </div>
      </template>
      <template v-else>
        <div class="drawer-report">
          <div v-if="agentState.report !== ''" class="md" v-html="reportHtml" />
          <div v-else-if="agentState.running" class="tiny muted" style="padding: 10px">运行中，报告会在结束后显示。</div>
          <!-- 空回复也要说清原因：抽屉里同样不显示「请重试」这种没信息量的一句话 -->
          <section v-else-if="agentState.lastRun !== null" class="drawer-diag">
            <div class="drawer-diag-title">{{ diagText }}</div>
            <div class="tiny muted drawer-diag-facts">{{ diagFacts }}</div>
          </section>
          <div v-else class="tiny muted" style="padding: 10px 0">
            还没有报告。到 Agent 工作台跑一次 RouterAgent，这里会同步显示。
          </div>
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
    align-items: flex-start;
    gap: 8px;
    padding: 2px 12px;
    color: var(--text-2);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .log-line.delta {
    padding-top: 4px;
  }
  .log-line.delta .log-text {
    color: var(--text);
  }
  .log-line.streaming .log-time {
    color: var(--accent-2);
  }
  .log-line.streaming .log-text::after {
    content: '▍';
    margin-left: 2px;
    color: var(--accent-2);
    animation: log-caret 1.1s steps(1) infinite;
  }
  @keyframes log-caret {
    50% {
      opacity: 0;
    }
  }
  .log-repeat {
    margin-left: 6px;
    font-size: 10.5px;
    color: var(--muted);
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
  .drawer-diag {
    display: flex;
    flex-direction: column;
    gap: 5px;
    margin-bottom: 10px;
    padding: 8px 10px;
    border: 1px solid rgba(255, 90, 95, 0.32);
    border-radius: var(--radius-sm);
    background: rgba(255, 90, 95, 0.07);
  }
  .drawer-diag-title {
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
    line-height: 1.5;
  }
  .drawer-diag-facts {
    line-height: 1.6;
  }
</style>
