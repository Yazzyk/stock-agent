<script setup lang="ts">
// Agent 实验室：五个 worker + 输入 + 流式日志 + 报告 + 历史会话。
// 流式数据来自 kernel:event（agent.delta / agent.state / agent.report）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { agentRun, agentSessions, agentSession, agentSteer, agentStop, agentWorkers, messageOf } from '../lib/data'
import { fmtDateTime } from '../lib/format'
import { mdToHtml } from '../lib/markdown'
import { toast } from '../stores/app'
import { agentState, appendLine, beginRun, clearAgentFeed, endRun, markSteerPending } from '../stores/agent'
import type { AgentSession, WorkerInfo } from '../types'

const workers = ref<WorkerInfo[]>([])
const sessions = ref<AgentSession[]>([])
const activeWorker = ref('chat')
/** 底部输入框：新任务模式下是任务描述，运行中切成插话内容。 */
const composer = ref('')
const steering = ref(false)
const tab = ref<'log' | 'report'>('log')
const loading = ref(true)
const error = ref('')
const sessionError = ref('')
const sessionBusy = ref('')

const logEl = ref<HTMLElement | null>(null)
let alive = true

const selectedWorker = computed(() => workers.value.find((w) => w.id === activeWorker.value) ?? null)
const reportHtml = computed(() => mdToHtml(agentState.report))
const canRun = computed(() => !agentState.running && activeWorker.value !== '' && selectedWorker.value !== null)
/** 运行中且已拿到 sessionId 才允许插话（sessionId 来自 agent.state/delta 事件）。 */
const isSteer = computed(() => agentState.running && agentState.sessionId !== '')
const steerBlocked = computed(() => agentState.running && agentState.sessionId === '')
// 插话必须非空；新任务允许留空（worker 有自己的默认提示），与头部「运行」保持一致
const canSend = computed(() => (isSteer.value ? composer.value.trim() !== '' : canRun.value) && !steering.value)

const emptyWorkers: WorkerInfo[] = []

async function loadWorkers(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const list = await agentWorkers()
    workers.value = list.length > 0 ? list : emptyWorkers
    if (list.length > 0 && !list.some((w) => w.id === activeWorker.value)) {
      activeWorker.value = list[0]?.id ?? 'chat'
    }
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadSessions(): Promise<void> {
  try {
    sessions.value = await agentSessions(15)
  } catch {
    sessions.value = []
  }
}

async function run(): Promise<void> {
  if (!canRun.value) return
  tab.value = 'log'
  const worker = activeWorker.value
  const task = composer.value.trim()
  composer.value = ''
  beginRun(worker)
  try {
    const res = await agentRun(worker, task)
    if (!alive) return
    if (res.error) {
      endRun(true, res.error)
      toast('运行失败：' + res.error, 'error')
    } else {
      if (res.report) agentState.report = res.report
      agentState.reportWorker = res.worker
      endRun(false, '完成，用时 ' + (res.steps ?? 0) + ' 步')
      if (res.usage) appendLine('token 用量：' + res.usage.totalTokens, 'ok')
      tab.value = 'report'
    }
    await loadSessions()
  } catch (e) {
    if (!alive) return
    endRun(true, messageOf(e))
    toast(messageOf(e), 'error')
  }
}

/** 运行中插话：不等待 run 返回，直接把文本并入同一 session 的下一步。 */
async function steer(): Promise<void> {
  const text = composer.value.trim()
  if (text === '') {
    toast('插话内容不能为空', 'warn')
    return
  }
  const sid = agentState.sessionId
  if (sid === '') {
    toast('会话还没就绪或已结束，请重新运行后再插话', 'warn')
    return
  }
  steering.value = true
  try {
    const res = await agentSteer(sid, text)
    if (!res.ok) throw new Error('插话未入队，请稍后重试')
    markSteerPending(text)
    composer.value = ''
    toast(res.queued > 0 ? '插话已入队（排队 ' + res.queued + ' 条）' : '插话已发送', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    steering.value = false
  }
}

async function submitComposer(): Promise<void> {
  if (isSteer.value) {
    await steer()
    return
  }
  await run()
}

async function stop(): Promise<void> {
  const id = agentState.sessionId
  if (!id) {
    endRun(false, '已本地标记停止')
    return
  }
  try {
    await agentStop(id)
    endRun(false, '已请求停止')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

async function openSession(s: AgentSession): Promise<void> {
  if (sessionBusy.value !== '') return
  sessionBusy.value = s.id
  sessionError.value = ''
  try {
    const detail = await agentSession(s.id)
    clearAgentFeed()
    agentState.reportWorker = detail.session.worker
    agentState.sessionId = detail.session.id
    const lines = detail.events.filter((e) => e.kind !== 'report')
    for (const e of lines) appendLine(e.content, e.kind === 'tool' ? 'tool' : e.ok === false ? 'error' : 'state')
    const reportEvent = [...detail.events].reverse().find((e) => e.kind === 'report')
    if (reportEvent) agentState.report = reportEvent.content
    tab.value = reportEvent ? 'report' : 'log'
  } catch (e) {
    sessionError.value = messageOf(e)
  } finally {
    sessionBusy.value = ''
  }
}

function sessionState(s: AgentSession): string {
  if (s.status === 'ok') return 'tag-down'
  if (s.status === 'failed') return 'tag-up'
  return 'tag-accent'
}

function sessionStateText(s: AgentSession): string {
  if (s.status === 'ok') return '成功'
  if (s.status === 'failed') return '失败'
  return '运行中'
}

watch(
  () => agentState.log.length,
  async () => {
    if (tab.value !== 'log') return
    await nextTick()
    if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
  },
)

onMounted(async () => {
  await Promise.all([loadWorkers(), loadSessions()])
})

onBeforeUnmount(() => {
  alive = false
})
</script>

<template>
  <section class="card al">
    <div class="card-hd">
      <span class="card-title">Agent 实验室</span>
      <span class="row" style="gap: 6px">
        <span class="dot" :class="agentState.running ? 'dot-warn' : agentState.stateText === '失败' ? 'dot-err' : agentState.report ? 'dot-ok' : 'dot-off'" />
        <span class="tiny muted">{{ agentState.running ? '运行中' : agentState.stateText || '空闲' }}</span>
        <span v-if="agentState.sessionId" class="tiny muted mono">{{ agentState.sessionId }}</span>
      </span>
      <span class="card-tools">
        <button class="btn btn-sm" type="button" :disabled="agentState.log.length === 0 && !agentState.report" @click="clearAgentFeed">清空</button>
        <button class="btn btn-sm" type="button" :disabled="!agentState.running" @click="stop">停止</button>
        <button class="btn btn-sm btn-accent" type="button" :disabled="!canRun" @click="run">
          {{ agentState.running ? '运行中…' : '运行' }}
        </button>
      </span>
    </div>

    <div class="al-body">
      <div class="al-left">
        <StateBlock v-if="loading" kind="loading" title="读取 worker…" />
        <StateBlock v-else-if="error" kind="error" title="读取失败" :message="error" action-text="重试" @action="loadWorkers" />
        <template v-else>
          <div class="al-section">Worker</div>
          <div class="al-workers">
            <button
              v-for="w in workers"
              :key="w.id"
              class="al-worker"
              :class="{ on: activeWorker === w.id }"
              type="button"
              @click="activeWorker = w.id"
            >
              <span class="al-worker-title">{{ w.title }}</span>
              <span class="al-worker-desc">{{ w.description }}</span>
            </button>
          </div>

          <div class="al-hint">
            <div class="al-section" style="margin-top: 0">任务 / 插话</div>
            <div class="tiny muted">
              输入框在右下角：未运行时是「新任务」，运行中自动切「插话」，内容会并入同一步。
            </div>
          </div>

          <div class="al-section">
            历史会话
            <span class="spacer" />
            <button class="btn btn-sm btn-ghost" type="button" @click="loadSessions">刷新</button>
          </div>
          <div v-if="sessionError" class="hint-warn">{{ sessionError }}</div>
          <div v-if="sessions.length === 0" class="tiny muted">暂无历史会话</div>
          <div v-else class="al-sessions">
            <button
              v-for="s in sessions"
              :key="s.id"
              class="al-session"
              :class="{ on: agentState.sessionId === s.id }"
              type="button"
              @click="openSession(s)"
            >
              <span class="row" style="gap: 6px">
                <span class="grow ellipsis">{{ s.title }}</span>
                <span class="tag" :class="sessionState(s)">{{ sessionStateText(s) }}</span>
              </span>
              <span class="row tiny muted" style="gap: 8px">
                <span class="mono">{{ s.worker }}</span>
                <span>{{ fmtDateTime(s.startedAt) }}</span>
                <span v-if="s.tokens">· {{ s.tokens }} tok</span>
                <span v-if="sessionBusy === s.id">· 读取中…</span>
              </span>
            </button>
          </div>
        </template>
      </div>

      <div class="al-right">
        <div class="tabs" style="flex: 0 0 auto">
          <button class="tab" :class="{ on: tab === 'log' }" type="button" @click="tab = 'log'">
            流式日志 <span class="tiny muted">{{ agentState.log.length }}</span>
          </button>
          <button class="tab" :class="{ on: tab === 'report' }" type="button" @click="tab = 'report'">报告</button>
          <span class="spacer" />
          <span class="tiny muted" style="padding-right: 10px">
            {{ agentState.worker || agentState.reportWorker || '—' }} · {{ agentState.stateText || '空闲' }}
          </span>
        </div>

        <div v-show="tab === 'log'" ref="logEl" class="al-log">
          <div v-if="agentState.log.length === 0" class="tiny muted" style="padding: 10px">
            选择 worker 后点「运行」，这里会实时显示工具调用与模型增量输出（⌘J 可在任意面板查看）。
          </div>
          <div v-for="line in agentState.log" :key="line.id" class="log-line" :class="line.kind">
            <span class="log-time">{{ line.time }}</span>
            <span class="log-text">{{ line.text }}</span>
          </div>
        </div>

        <div v-show="tab === 'report'" class="al-report">
          <div v-if="agentState.report === ''" class="tiny muted" style="padding: 10px">还没有报告。运行结束后会自动切到这一页。</div>
          <div v-else class="md" v-html="reportHtml" />
        </div>

        <form class="al-composer" @submit.prevent="submitComposer">
          <div class="al-composer-hd">
            <span class="tag" :class="isSteer ? 'tag-accent' : ''">{{ isSteer ? '插话' : '新任务' }}</span>
            <span class="tiny muted">
              <template v-if="isSteer">并入同一会话的下一步（{{ agentState.sessionId }}），不影响正在进行的 run</template>
              <template v-else-if="steerBlocked">运行中，等待 session 就绪…</template>
              <template v-else-if="agentState.running">运行中…</template>
              <template v-else>选好 worker 后发送，会开启一次新会话</template>
            </span>
            <span class="spacer" />
            <span class="kbd">Enter 发送</span>
          </div>
          <div class="al-composer-row">
            <textarea
              v-model="composer"
              class="textarea"
              rows="2"
              :placeholder="isSteer
                ? '插话：例如「只看半导体和白酒」「先别管指数」'
                : (selectedWorker ? selectedWorker.title + '：可写关注点，例如「只看半导体与白酒」' : '选择 worker 后填写任务')"
              :aria-label="isSteer ? '插话内容' : '任务输入'"
              @keydown.enter.exact.prevent="submitComposer"
            />
            <button class="btn btn-accent" type="submit" :disabled="!canSend">
              {{ isSteer ? (steering ? '发送中…' : '发送插话') : agentState.running ? '运行中…' : '运行' }}
            </button>
          </div>
        </form>
      </div>
    </div>

  </section>
</template>


<style scoped>
  .al {
    min-height: 0;
  }
  .al-body {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: 300px 1fr;
  }
  .al-left {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px;
    border-right: 1px solid var(--line);
    overflow-y: auto;
    min-height: 0;
  }
  .al-section {
    display: flex;
    align-items: center;
    margin-top: 6px;
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.8px;
  }
  .al-workers {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  .al-worker {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 7px 9px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
    text-align: left;
  }
  .al-worker:hover {
    border-color: var(--line-3);
    background: var(--panel-3);
  }
  .al-worker.on {
    border-color: rgba(232, 179, 75, 0.5);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .al-worker-title {
    color: var(--text);
    font-size: 12.5px;
    font-weight: 600;
  }
  .al-worker-desc {
    color: var(--muted);
    font-size: 11px;
    line-height: 1.5;
  }
  .al-sessions {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .al-session {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 6px 8px;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-size: 12px;
    text-align: left;
  }
  .al-session:hover {
    background: var(--panel-2);
    border-color: var(--line);
  }
  .al-session.on {
    border-color: rgba(232, 179, 75, 0.35);
    background: var(--panel-2);
  }
  .al-right {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
  }
  .al-hint {
    padding: 6px 8px;
    border: 1px dashed var(--line-2);
    border-radius: var(--radius-sm);
    line-height: 1.6;
  }
  .al-composer {
    flex: 0 0 auto;
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 8px 10px;
    border-top: 1px solid var(--line);
    background: var(--panel-2);
  }
  .al-composer-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .al-composer-row {
    display: flex;
    align-items: flex-end;
    gap: 8px;
  }
  .al-composer-row textarea {
    flex: 1 1 auto;
    min-width: 0;
  }
  .al-log {
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
    background: var(--panel-2);
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
  .al-report {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 10px 14px;
  }
</style>