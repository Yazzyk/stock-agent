<script setup lang="ts">
// Watch 哨兵：用户自定义盯盘规则。左侧规则列表 + 右侧编辑器 + 底部触发日志。
// 规则只做「声明式条件求值 → 派一次 Agent 任务」，不产生任何交易指令。
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import {
  agentRun,
  messageOf,
  watchdogList,
  watchdogLogs,
  watchdogRemove,
  watchdogSave,
  watchdogTemplates,
  watchdogTest,
  watchdogToggle,
} from '../lib/data'
import { clip, fmtDateTime } from '../lib/format'
import { toast } from '../stores/app'
import type {
  WatchMetricSpec,
  WatchRule,
  WatchRunLog,
  WatchScenario,
  WatchSource,
  WatchSourceType,
  WatchTestResult,
} from '../types'

const SOURCE_TYPES: Array<{ value: WatchSourceType; text: string }> = [
  { value: 'quote', text: '个股行情' },
  { value: 'sector', text: '板块/概念' },
  { value: 'kline', text: 'K 线指标' },
  { value: 'news', text: '新闻/公告' },
  { value: 'schedule', text: '定时' },
]

const WORKERS = [
  { value: 'chat', text: '对话 chat' },
  { value: 'intraday', text: '盯盘 intraday' },
  { value: 'premarket', text: '盘前 premarket' },
  { value: 'postmarket', text: '复盘 postmarket' },
  { value: 'quant', text: '量化 quant' },
]

const rules = ref<WatchRule[]>([])
const metrics = ref<WatchMetricSpec[]>([])
const logs = ref<WatchRunLog[]>([])
const running = ref(false)
const loading = ref(true)
const error = ref('')
const selectedId = ref('')

const draft = reactive<WatchRule>(blankRule())
const saving = ref(false)
const testing = ref(false)
const removing = ref(false)
const testResult = ref<WatchTestResult | null>(null)
const testError = ref('')
const notice = ref('')

const templates = ref<WatchRule[]>([])
const showTemplates = ref(false)
const templatesLoaded = ref(false)

const wish = ref('')
const generating = ref(false)
const agentReply = ref('')

const expandedLog = ref('')
const condInput = ref<HTMLTextAreaElement | null>(null)

function blankSource(type: WatchSourceType): WatchSource {
  switch (type) {
    case 'sector':
      return { type, sector: '', kind: 'sector' }
    case 'news':
      return { type, keyword: '', kind: 'notice' }
    case 'schedule':
      return { type, times: ['09:35'] }
    case 'kline':
      return { type, codes: [], period: 'day' }
    default:
      return { type: 'quote', codes: [] }
  }
}

function blankRule(): WatchRule {
  return {
    name: '',
    description: '',
    enabled: true,
    sources: [blankSource('quote')],
    condition: 'quote.changePct > 3',
    action: { worker: 'intraday', prompt: '' },
    cooldownSeconds: 600,
    quietWindows: [],
  }
}

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

const selected = computed(() => rules.value.find((r) => r.id === selectedId.value) ?? null)
const enabledCount = computed(() => rules.value.filter((r) => r.enabled).length)
const isNew = computed(() => !draft.id)
const validation = computed<string>(() => {
  if (draft.name.trim() === '') return '规则名称不能为空'
  if (draft.sources.length === 0) return '至少需要一个触发源'
  for (const s of draft.sources) {
    if ((s.type === 'quote' || s.type === 'kline') && (s.codes ?? []).length === 0) return '行情/K 线触发源需要填股票代码'
    if (s.type === 'sector' && (s.sector ?? '').trim() === '') return '板块触发源需要填板块名'
    if (s.type === 'news' && (s.keyword ?? '').trim() === '') return '新闻触发源需要填关键词'
    if (s.type === 'schedule' && (s.times ?? []).length === 0) return '定时触发源需要填时间（HH:MM）'
  }
  if (draft.condition.trim() === '') return '条件表达式不能为空'
  return ''
})

const condHint = computed(() => {
  if (draft.condition.trim() === '') return '例如 quote.changePct > 3 and quote.volumeRatio > 2'
  return ''
})

const quietText = computed({
  get: () => (draft.quietWindows ?? []).join(','),
  set: (v: string) => {
    draft.quietWindows = v
      .split(/[,，\s]+/)
      .map((s) => s.trim())
      .filter((s) => s !== '')
  },
})

function metricLabel(name: string): string {
  const m = metrics.value.find((x) => x.name === name)
  if (!m) return name
  return m.label + (m.unit ? ' / ' + m.unit : '')
}

function metricUnit(name: string): string {
  return metrics.value.find((x) => x.name === name)?.unit ?? ''
}

/** 表格里用：只要指标中文名，单位由数值后缀体现。 */
function metricName(name: string): string {
  return metrics.value.find((x) => x.name === name)?.label ?? name
}

function fmtMetric(name: string, v: number | undefined): string {
  if (v === undefined) return '—'
  const unit = metricUnit(name)
  const num = Number.isInteger(v) ? String(v) : v.toFixed(2)
  return unit ? num + unit : num
}

function scenarioTitle(s: WatchScenario): string {
  if (s.code) return s.code + ' ' + (s.name ?? '')
  if (s.sector) return '板块 ' + s.sector
  return s.name ?? '场景'
}

function scenarioKeys(s: WatchScenario): string[] {
  return Object.keys(s.metrics).slice(0, 4)
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await watchdogList()
    rules.value = res.rules
    metrics.value = res.metrics
    logs.value = res.logs
    running.value = res.running
    if (res.rules.length > 0) {
      const keep = res.rules.find((r) => r.id === selectedId.value)
      select(keep ?? res.rules[0] ?? null)
    }
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

function select(rule: WatchRule | null): void {
  testResult.value = null
  testError.value = ''
  notice.value = ''
  if (!rule) {
    selectedId.value = ''
    Object.assign(draft, blankRule())
    return
  }
  selectedId.value = rule.id ?? ''
  Object.assign(draft, clone(rule))
}

function newRule(): void {
  selectedId.value = ''
  testResult.value = null
  testError.value = ''
  notice.value = ''
  Object.assign(draft, blankRule())
}

async function toggleRule(rule: WatchRule, enabled: boolean): Promise<void> {
  if (!rule.id) return
  try {
    const updated = await watchdogToggle(rule.id, enabled)
    const idx = rules.value.findIndex((r) => r.id === rule.id)
    if (idx >= 0) rules.value[idx] = updated
    if (draft.id === rule.id) draft.enabled = updated.enabled
    toast(enabled ? '已启用「' + rule.name + '」' : '已停用「' + rule.name + '」', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

async function save(): Promise<WatchRule | null> {
  if (validation.value) {
    toast(validation.value, 'warn')
    return null
  }
  saving.value = true
  notice.value = ''
  try {
    const res = await watchdogSave({ ...draft, sources: clone(draft.sources) })
    rules.value = res.rules
    select(res.rule)
    toast('已保存「' + res.rule.name + '」', 'ok')
    return res.rule
  } catch (e) {
    notice.value = messageOf(e)
    return null
  } finally {
    saving.value = false
  }
}

async function test(): Promise<void> {
  let id = draft.id ?? ''
  if (!id) {
    const saved = await save()
    id = saved?.id ?? ''
    if (!id) return
  }
  testing.value = true
  testError.value = ''
  testResult.value = null
  try {
    testResult.value = await watchdogTest(id)
  } catch (e) {
    testError.value = messageOf(e)
  } finally {
    testing.value = false
  }
}

async function remove(): Promise<void> {
  const id = draft.id ?? ''
  if (!id) return
  if (!removing.value) {
    removing.value = true
    window.setTimeout(() => {
      removing.value = false
    }, 4000)
    return
  }
  removing.value = false
  try {
    rules.value = await watchdogRemove(id)
    toast('规则已删除', 'ok')
    if (rules.value.length > 0) select(rules.value[0] ?? null)
    else newRule()
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

async function loadTemplates(): Promise<void> {
  if (templatesLoaded.value) {
    showTemplates.value = !showTemplates.value
    return
  }
  try {
    const res = await watchdogTemplates()
    templates.value = res.templates
    if (res.metrics.length > 0) metrics.value = res.metrics
    templatesLoaded.value = true
    showTemplates.value = true
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

function useTemplate(tpl: WatchRule): void {
  const next = clone(tpl)
  delete next.id
  select(null)
  Object.assign(draft, next)
  showTemplates.value = false
  notice.value = '已填入模板「' + tpl.name + '」，可改参数后保存'
}

async function askAgent(): Promise<void> {
  const text = wish.value.trim()
  if (text === '') {
    toast('先用一句话描述你要盯什么', 'warn')
    return
  }
  generating.value = true
  agentReply.value = ''
  notice.value = ''
  try {
    const res = await agentRun('chat', '请调用 watchdog.create 工具创建规则：' + text)
    if (res.error) throw new Error(res.error)
    agentReply.value = clip(res.report.replace(/\n+/g, ' '), 260)
    await load()
    const created = rules.value.find((r) => r.createdBy === 'agent')
    if (created) select(created)
    toast('规则已刷新（Agent 创建）', 'ok')
  } catch (e) {
    notice.value = messageOf(e)
  } finally {
    generating.value = false
  }
}

function insertMetric(name: string): void {
  const el = condInput.value
  const text = draft.condition
  if (!el) {
    draft.condition = text + ' ' + name
    return
  }
  const start = el.selectionStart ?? text.length
  const end = el.selectionEnd ?? text.length
  const next = text.slice(0, start) + name + text.slice(end)
  draft.condition = next
  void nextTick(() => {
    el.focus()
    const pos = start + name.length
    el.setSelectionRange(pos, pos)
  })
}

function addSource(): void {
  if (draft.sources.length >= 6) {
    toast('触发源最多 6 个', 'warn')
    return
  }
  draft.sources.push(blankSource('quote'))
}

function setSourceType(i: number, type: WatchSourceType): void {
  const prev = draft.sources[i]
  if (!prev) return
  const next = blankSource(type)
  if (type === 'quote' || type === 'kline') next.codes = prev.codes ?? []
  if (type === 'kline') next.period = prev.period ?? 'day'
  draft.sources.splice(i, 1, next)
}

function onCodesInput(i: number, e: Event): void {
  const el = e.target as HTMLInputElement | null
  const src = draft.sources[i]
  if (!el || !src) return
  src.codes = el.value
    .split(/[,，\s]+/)
    .map((c) => c.trim())
    .filter((c) => c !== '')
}

function onTimesInput(i: number, e: Event): void {
  const el = e.target as HTMLInputElement | null
  const src = draft.sources[i]
  if (!el || !src) return
  src.times = el.value
    .split(/[,，\s]+/)
    .map((c) => c.trim())
    .filter((c) => c !== '')
}

function logState(log: WatchRunLog): { text: string; cls: string } {
  if (log.error) return { text: '错误', cls: 'tag-up' }
  if (log.triggered) return { text: '已派发', cls: 'tag-down' }
  if (log.matched) return { text: '仅命中', cls: 'tag-accent' }
  return { text: '未命中', cls: '' }
}

function toggleLog(key: string): void {
  expandedLog.value = expandedLog.value === key ? '' : key
}

onMounted(async () => {
  await load()
})

// 供模板里直接使用
const refreshLogs = async (): Promise<void> => {
  try {
    logs.value = await watchdogLogs(50)
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}
</script>

<template>
  <section class="card wd">
    <div class="card-hd">
      <span class="card-title">Watch 哨兵</span>
      <span class="row" style="gap: 6px">
        <span class="dot" :class="running ? 'dot-ok' : 'dot-err'" />
        <span class="tiny muted">{{ running ? '引擎运行中' : '引擎未运行' }}</span>
      </span>
      <span class="card-sub">{{ rules.length }} 条规则 · {{ enabledCount }} 条启用</span>
      <span class="card-tools">
        <button class="btn btn-sm" type="button" @click="loadTemplates">从模板新建</button>
        <button class="btn btn-sm" type="button" @click="newRule">新建规则</button>
        <button class="btn btn-sm" type="button" :disabled="loading" @click="load">刷新</button>
      </span>
    </div>

    <div class="wd-agent">
      <span class="tag tag-accent">一句话创建</span>
      <input
        v-model="wish"
        class="input grow"
        placeholder="例如：帮我盯着半导体板块，主力净流入超过 20 亿就提醒我判断持续性"
        aria-label="用一句话描述你要盯什么"
        @keydown.enter="askAgent"
      />
      <button class="btn btn-sm btn-accent" type="button" :disabled="generating" @click="askAgent">
        {{ generating ? 'Agent 生成中…' : '让 Agent 写规则' }}
      </button>
      <button class="btn btn-sm btn-ghost" type="button" title="刷新触发日志" @click="refreshLogs">刷新日志</button>
    </div>
    <div v-if="agentReply" class="wd-agent-reply">
      <span class="muted tiny">Agent：</span>{{ agentReply }}
    </div>

    <div v-if="showTemplates" class="wd-templates">
      <div class="row" style="margin-bottom: 4px">
        <span class="tiny muted">内置模板（点击填入编辑器）</span>
        <span class="spacer" />
        <button class="btn btn-sm btn-ghost" type="button" @click="showTemplates = false">×</button>
      </div>
      <button v-for="(tpl, i) in templates" :key="i" class="wd-tpl" type="button" @click="useTemplate(tpl)">
        <span class="grow ellipsis">{{ tpl.name }}</span>
        <span class="tiny muted mono">{{ tpl.condition }}</span>
      </button>
      <div v-if="templates.length === 0" class="tiny muted">没有可用模板</div>
    </div>

    <div v-if="notice" class="wd-notice">{{ notice }}</div>

    <div class="wd-body">
      <aside class="wd-list">
        <StateBlock v-if="loading" kind="loading" title="读取规则…" />
        <StateBlock v-else-if="error" kind="error" title="读取失败" :message="error" action-text="重试" @action="load" />
        <StateBlock
          v-else-if="rules.length === 0"
          kind="empty"
          title="还没有 Watch 规则"
          message="用顶部一句话让 Agent 生成，或从模板新建一条。规则命中后只会派一次 Agent 任务。"
        />
        <template v-else>
          <div
            v-for="r in rules"
            :key="r.id"
            class="wd-item"
            :class="{ on: r.id === selectedId }"
            role="button"
            tabindex="0"
            @click="select(r)"
            @keydown.enter="select(r)"
          >
            <div class="row" style="gap: 6px">
              <span class="grow ellipsis wd-item-name">{{ r.name }}</span>
              <input
                class="wd-switch"
                type="checkbox"
                :checked="r.enabled"
                :aria-label="'启用 ' + r.name"
                @click.stop
                @change="toggleRule(r, ($event.target as HTMLInputElement).checked)"
              />
            </div>
            <div class="wd-item-cond mono ellipsis">{{ r.condition }}</div>
            <div class="row" style="gap: 6px">
              <span v-if="r.triggerCount" class="tiny muted">触发 {{ r.triggerCount }} 次</span>
              <span v-else class="tiny muted">未触发</span>
              <span class="spacer" />
              <span v-if="r.lastError" class="tag tag-up" :title="r.lastError">错误</span>
              <span v-else-if="r.lastTriggeredAt" class="tiny muted">{{ fmtDateTime(r.lastTriggeredAt) }}</span>
            </div>
          </div>
        </template>
      </aside>

      <div class="wd-editor">
        <div class="wd-editor-hd">
          <span class="card-title">{{ isNew ? '新建规则' : '编辑规则' }}</span>
          <span v-if="selected?.createdBy" class="tag">{{ selected.createdBy === 'agent' ? 'Agent 创建' : selected.createdBy === 'template' ? '来自模板' : '手动创建' }}</span>
          <span class="spacer" />
          <span v-if="validation" class="hint-warn">{{ validation }}</span>
        </div>

        <div class="wd-grid">
          <label class="field wd-span2">
            <span class="label">名称</span>
            <input v-model="draft.name" class="input" placeholder="例如 自选放量突破" maxlength="60" />
          </label>
          <label class="field wd-span2">
            <span class="label">描述（可选）</span>
            <input v-model="draft.description" class="input" placeholder="给这条规则一句说明" maxlength="120" />
          </label>
        </div>

        <div class="wd-section">
          触发源
          <span class="tiny muted" style="margin-left: 6px">最多 6 个，任一来源提供指标即可求值</span>
          <span class="spacer" />
          <button class="btn btn-sm btn-ghost" type="button" @click="addSource">+ 添加</button>
        </div>

        <div v-for="(s, i) in draft.sources" :key="i" class="wd-source">
          <select class="select" :value="s.type" aria-label="触发源类型" @change="setSourceType(i, ($event.target as HTMLSelectElement).value as WatchSourceType)">
            <option v-for="t in SOURCE_TYPES" :key="t.value" :value="t.value">{{ t.text }}</option>
          </select>

          <template v-if="s.type === 'quote' || s.type === 'kline'">
            <input
              class="input grow mono"
              :value="(s.codes ?? []).join(',')"
              placeholder="代码，逗号分隔：600519,300750"
              aria-label="股票代码"
              @input="onCodesInput(i, $event)"
            />
            <select v-if="s.type === 'kline'" class="select" v-model="s.period" aria-label="K 线周期">
              <option value="day">日线</option>
              <option value="week">周线</option>
              <option value="month">月线</option>
            </select>
          </template>

          <template v-else-if="s.type === 'sector'">
            <input v-model="s.sector" class="input grow" placeholder="板块/概念名：半导体" aria-label="板块名" />
            <select v-model="s.kind" class="select" aria-label="板块类型">
              <option value="sector">行业板块</option>
              <option value="concept">概念板块</option>
            </select>
          </template>

          <template v-else-if="s.type === 'news'">
            <input v-model="s.keyword" class="input grow" placeholder="关键词：并购重组" aria-label="关键词" />
            <select v-model="s.kind" class="select" aria-label="新闻类型">
              <option value="notice">公告</option>
              <option value="roll">新闻</option>
            </select>
          </template>

          <template v-else>
            <input
              class="input grow mono"
              :value="(s.times ?? []).join(',')"
              placeholder="HH:MM，逗号分隔：09:20,14:50"
              aria-label="触发时间"
              @input="onTimesInput(i, $event)"
            />
          </template>

          <button class="btn btn-sm btn-ghost" type="button" aria-label="删除触发源" @click="draft.sources.splice(i, 1)">×</button>
        </div>

        <div class="wd-section">
          条件表达式
          <span class="tiny muted" style="margin-left: 6px">只支持白名单指标与 and/or/比较运算</span>
        </div>
        <textarea
          ref="condInput"
          v-model="draft.condition"
          class="textarea mono"
          rows="2"
          placeholder="quote.changePct > 3 and quote.volumeRatio > 2"
          aria-label="条件表达式"
          spellcheck="false"
        />
        <div v-if="condHint" class="hint">{{ condHint }}</div>
        <div class="wd-chips">
          <button
            v-for="m in metrics"
            :key="m.name"
            class="wd-chip"
            type="button"
            :title="m.name + (m.unit ? '（单位 ' + m.unit + '）' : '')"
            @click="insertMetric(m.name)"
          >
            {{ m.label }}<span class="wd-chip-name">{{ m.name }}</span><span v-if="m.unit" class="wd-chip-unit">{{ m.unit }}</span>
          </button>
        </div>

        <div class="wd-section">命中后动作</div>
        <div class="wd-grid">
          <label class="field">
            <span class="label">Worker</span>
            <select v-model="draft.action.worker" class="select">
              <option v-for="w in WORKERS" :key="w.value" :value="w.value">{{ w.text }}</option>
            </select>
          </label>
          <label class="field">
            <span class="label">冷却秒数（≥30）</span>
            <input v-model.number="draft.cooldownSeconds" class="input num" type="number" min="30" step="30" />
          </label>
          <label class="field wd-span2">
            <span class="label">Prompt 模板</span>
            <textarea
              v-model="draft.action.prompt"
              class="textarea"
              rows="3"
              placeholder="可用占位：{summary} {code} {sector} {指标名}，例如 {quote.changePct}"
            />
          </label>
          <label class="field wd-span2">
            <span class="label">静默时段（逗号分隔，HH:MM-HH:MM）</span>
            <input v-model="quietText" class="input mono" placeholder="11:30-13:00" />
          </label>
        </div>

        <div class="wd-actions">
          <button class="btn btn-accent" type="button" :disabled="saving || validation !== ''" @click="save">
            {{ saving ? '保存中…' : '保存' }}
          </button>
          <button class="btn" type="button" :disabled="testing || validation !== ''" @click="test">
            {{ testing ? '试运行中…' : '试运行' }}
          </button>
          <button class="btn" :class="{ 'btn-danger': removing }" type="button" :disabled="isNew" @click="remove">
            {{ removing ? '确认删除' : '删除' }}
          </button>
          <span class="spacer" />
          <span class="tiny muted">试运行只求值、不触发 Agent</span>
        </div>

        <div v-if="testError" class="wd-test-err">{{ testError }}</div>

        <div v-if="testResult" class="wd-test">
          <div class="row" style="gap: 8px">
            <span class="tag" :class="testResult.matched ? 'tag-down' : 'tag-up'">{{ testResult.matched ? '命中' : '未命中' }}</span>
            <span class="tiny muted">{{ testResult.summary }}</span>
            <span class="spacer" />
            <span v-if="testResult.elapsedMs" class="tiny muted">{{ testResult.elapsedMs }}ms</span>
          </div>
          <div v-if="testResult.error" class="hint-warn">{{ testResult.error }}</div>

          <table v-if="testResult.scenarios.length > 0" class="tbl" style="margin-top: 8px">
            <thead>
              <tr>
                <th class="l">场景</th>
                <th class="l">命中</th>
                <th class="l">关键指标</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(s, i) in testResult.scenarios" :key="i">
                <td class="l">{{ scenarioTitle(s) }}</td>
                <td class="l">
                  <span class="tag" :class="s.matched ? 'tag-down' : ''">{{ s.matched ? '命中' : '未命中' }}</span>
                </td>
                <td class="l">
                  <span v-for="k in scenarioKeys(s)" :key="k" class="wd-kv" :title="k">
                    {{ metricName(k) }} <b class="num">{{ fmtMetric(k, s.metrics[k]) }}</b>
                  </span>
                </td>
              </tr>
            </tbody>
          </table>

          <div class="wd-section" style="margin-top: 10px">渲染后的 prompt（Agent 实际收到的内容）</div>
          <pre class="wd-prompt">{{ testResult.prompt || '（prompt 为空）' }}</pre>
        </div>
      </div>
    </div>

    <div class="wd-logs">
      <div class="wd-section" style="margin: 0; padding: 0 12px">
        触发日志
        <span class="tiny muted" style="margin-left: 6px">最近 {{ logs.length }} 条</span>
        <span class="spacer" />
        <span class="tiny muted">点击行可展开报告</span>
      </div>
      <div class="wd-log-scroll">
        <table v-if="logs.length > 0" class="tbl">
          <thead>
            <tr>
              <th class="l" style="width: 130px">时间</th>
              <th class="l">规则</th>
              <th class="l" style="width: 76px">状态</th>
              <th class="l">摘要 / 报告</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="(log, i) in logs" :key="log.time + log.ruleId + i">
              <tr :class="{ sel: expandedLog === log.time + i }" @click="toggleLog(log.time + i)">
                <td class="l num tiny">{{ fmtDateTime(log.time) }}</td>
                <td class="l">{{ log.ruleName }} <span class="tiny muted mono">{{ log.worker }}</span></td>
                <td class="l"><span class="tag" :class="logState(log).cls">{{ logState(log).text }}</span></td>
                <td class="l ellipsis">{{ log.error ? log.error : log.summary || clip(log.report ?? '', 60) || '—' }}</td>
              </tr>
              <tr v-if="expandedLog === log.time + i">
                <td colspan="4" class="l">
                  <pre class="wd-report">{{ log.report || log.error || '（无报告）' }}</pre>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <div v-else class="tiny muted" style="padding: 8px 12px">还没有触发记录。规则命中后这里会出现 Agent 报告。</div>
      </div>
    </div>
  </section>
</template>

<style scoped>
  .wd {
    min-height: 0;
  }
  .wd-agent {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--line);
    background: var(--panel-2);
  }
  .wd-agent-reply {
    padding: 6px 12px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
    color: var(--text-2);
    font-size: 11.5px;
    line-height: 1.7;
  }
  .wd-templates {
    padding: 8px 12px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
  }
  .wd-tpl {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 5px 8px;
    margin-bottom: 3px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-size: 12px;
    text-align: left;
  }
  .wd-tpl:hover {
    background: var(--panel-2);
    border-color: var(--line-3);
  }
  .wd-notice {
    padding: 5px 12px;
    border-bottom: 1px solid var(--line);
    background: rgba(232, 179, 75, 0.08);
    color: var(--accent-2);
    font-size: 11.5px;
  }
  .wd-body {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: 300px 1fr;
  }
  .wd-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 8px;
    border-right: 1px solid var(--line);
    overflow-y: auto;
    min-height: 0;
  }
  .wd-item {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 7px 9px;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    cursor: pointer;
  }
  .wd-item:hover {
    background: var(--panel-2);
    border-color: var(--line);
  }
  .wd-item.on {
    background: var(--panel-2);
    border-color: rgba(232, 179, 75, 0.35);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .wd-item-name {
    color: var(--text);
    font-size: 12.5px;
    font-weight: 600;
  }
  .wd-item-cond {
    color: var(--muted);
    font-size: 11px;
  }
  .wd-switch {
    appearance: none;
    width: 26px;
    height: 14px;
    border-radius: 8px;
    background: var(--line-3);
    position: relative;
    cursor: pointer;
    transition: background 0.12s;
    flex: 0 0 auto;
  }
  .wd-switch::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--text-2);
    transition: transform 0.12s;
  }
  .wd-switch:checked {
    background: rgba(47, 191, 135, 0.55);
  }
  .wd-switch:checked::after {
    transform: translateX(12px);
    background: #eafff6;
  }
  .wd-editor {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px;
    overflow-y: auto;
    min-height: 0;
  }
  /* 编辑区内容多，禁止 flex 压缩子项（否则 textarea/chips 会被压成 0 高） */
  .wd-editor > * {
    flex: 0 0 auto;
  }
  .wd-editor textarea[aria-label='条件表达式'] {
    min-height: 46px;
  }
  .wd-editor-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .wd-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px 12px;
  }
  .wd-span2 {
    grid-column: 1 / -1;
  }
  .wd-section {
    display: flex;
    align-items: center;
    margin-top: 4px;
    color: var(--muted);
    font-size: 10.5px;
    letter-spacing: 0.6px;
  }
  .wd-source {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .wd-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    max-height: 78px;
    overflow-y: auto;
    padding: 2px 0;
  }
  .wd-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 6px;
    border: 1px solid var(--line-2);
    border-radius: 4px;
    background: var(--panel-2);
    color: var(--text-2);
    font-size: 11px;
    cursor: pointer;
  }
  .wd-chip:hover {
    border-color: rgba(232, 179, 75, 0.5);
    color: var(--accent-2);
    background: var(--accent-soft);
  }
  .wd-chip-name {
    color: var(--muted);
    font: 10px var(--mono);
  }
  .wd-chip-unit {
    color: var(--accent);
    font-size: 10px;
  }
  .wd-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    padding-top: 4px;
    border-top: 1px solid var(--line);
  }
  .wd-test {
    padding: 8px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .wd-test-err {
    padding: 6px 10px;
    border: 1px solid rgba(255, 93, 93, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(255, 93, 93, 0.08);
    color: #ffb4b4;
    font-size: 11.5px;
  }
  .wd-kv {
    display: inline-block;
    margin-right: 10px;
    color: var(--muted);
    font-size: 11px;
  }
  .wd-kv b {
    color: var(--text-2);
  }
  .wd-prompt {
    margin: 0;
    padding: 8px 10px;
    max-height: 140px;
    overflow: auto;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg);
    color: var(--text-2);
    font: 11.5px/1.7 var(--mono);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .wd-logs {
    flex: 0 0 auto;
    max-height: 190px;
    display: flex;
    flex-direction: column;
    border-top: 1px solid var(--line);
  }
  .wd-log-scroll {
    flex: 1 1 auto;
    overflow-y: auto;
    min-height: 0;
  }
  .wd-report {
    margin: 4px 0;
    padding: 8px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--bg);
    color: var(--text-2);
    font: 11.5px/1.7 var(--mono);
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
