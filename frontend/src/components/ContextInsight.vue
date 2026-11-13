<script setup lang="ts">
// 上下文洞察：工作台「上下文」页 + 标题栏小环的弹层共用同一套口径与计算。
//
// 数据分两层，**永不互相对账**：
//   - 精确层：提供方上报的最近一次请求 prompt（pressureTokens）、缓存桶、会话累计 usage、逐轮 history；
//   - 估算层：system / tools / messages 的启发式分项（只用来切相对比例，数字一律加 `~`）。
// 有样本时「已用 / 占比」走精确口径，没有样本退回 usedTokens 并标「估算」。
//
// 两个变体只差布局密度（计算完全共用）：
//   - page：完整版——KPI / 逐轮台账 / 组成 / 趋势 / 会话累计 / 事件流 / 口径脚注；
//   - popover：紧凑版——只留占比句 + 数字/徽标 + 堆叠条 + 三行图例 + 缓存命中，
//     底部一个「查看完整洞察 →」按钮（emit open，由宿主切到「上下文」标签）。
import { computed, nextTick, ref, watch } from 'vue'
import type { AgentContext, AgentContextPoint } from '../lib/api.agent'
import {
  DONUT_C,
  DONUT_R,
  TREND_H,
  TREND_W,
  clockText,
  ctxBreakdownTotal,
  ctxCache,
  ctxExact,
  ctxHistory,
  ctxPercent,
  ctxRows,
  ctxSampleLabel,
  ctxStack,
  ctxTone,
  ctxTrendPoints,
  ctxUsed,
  ctxWindow,
  formatTok,
  percentText,
  stampText,
  tokCell,
} from '../lib/contextview'
import { agentState, feedEvents, type FeedEvent } from '../stores/agent'

const props = withDefaults(defineProps<{ context: AgentContext | null; variant?: 'page' | 'popover' }>(), {
  variant: 'page',
})
const emit = defineEmits<{ (e: 'refresh'): void; (e: 'open'): void }>()

const page = computed(() => props.variant === 'page')

const win = computed(() => ctxWindow(props.context))
const hasWindow = computed(() => win.value > 0)
const used = computed(() => ctxUsed(props.context))
const exact = computed(() => ctxExact(props.context))
const sampleLabel = computed(() => ctxSampleLabel(props.context))
const percent = computed(() => ctxPercent(props.context))
const pctText = computed(() => percentText(percent.value))
const tone = computed(() => ctxTone(percent.value))
/** 精确口径不带 `~`，估算口径一定带（这是两种来源唯一可靠的视觉区分）。 */
const usedText = computed(() => (exact.value ? '' : '~') + formatTok(used.value))
/** 趋势/台账里的 token 前缀，与 KPI 同一个口径。 */
const tokPrefix = computed(() => (exact.value ? '' : '~'))

const rows = computed(() => ctxRows(props.context))
const hasBreakdown = computed(() => ctxBreakdownTotal(props.context) > 0)
const stack = computed(() => ctxStack(props.context))

const cache = computed(() => ctxCache(props.context))

const usage = computed(() => props.context?.usage)
const usageHasData = computed(() => {
  const u = usage.value
  return !!u && (u.promptTokens > 0 || u.completionTokens > 0 || u.totalTokens > 0)
})

const messages = computed(() => props.context?.messages ?? 0)
const steps = computed(() => props.context?.steps ?? 0)
const toolCalls = computed(() => props.context?.toolCalls ?? 0)
/** 会话数：本次编队里的会话总数（主会话 + 子智能体），只作规模参照。 */
const sessionCount = computed(() => Math.max(1, agentState.nodes.length))

const note = computed(
  () =>
    props.context?.note ||
    '估算口径：中文≈1 字/1 token，英文≈4 字符/1 token；工具只算 L1 索引（名称+摘要+标签），不含 schema。',
)

// ---------- 逐轮台账（本次重点） ----------
/** 后端权威 history（本进程内，按 step 升序）。 */
const ledger = computed<AgentContextPoint[]>(() => ctxHistory(props.context))
const lastStep = computed(() => (ledger.value.length > 0 ? ledger.value[ledger.value.length - 1].step : 0))

/** 台账容器：新采样进来后滚到最新一行（行按 step 升序，最新在最下面）。 */
const ledgerEl = ref<HTMLElement | null>(null)
watch(
  () => [ledger.value.length, lastStep.value],
  async () => {
    await nextTick()
    const el = ledgerEl.value
    if (el) el.scrollTop = el.scrollHeight
  },
  { immediate: true, flush: 'post' },
)

/** 单行缓存命中：分母是该请求的 prompt；没有 prompt 样本时给「—」而不是 0%。 */
function rowHit(p: AgentContextPoint): string {
  if (p.promptTokens <= 0) return '—'
  const hit = Math.round((p.cachedTokens / p.promptTokens) * 100)
  return Math.min(100, Math.max(0, hit)) + '%'
}

// ---------- 趋势 ----------
/** 本地采样：store 里按会话累积，最多 60 个（后端没 history 时的回退来源）。 */
const localHistory = computed(() => agentState.contextHistory)
const trendSource = computed<'backend' | 'local' | ''>(() => {
  if (ledger.value.length >= 2) return 'backend'
  if (localHistory.value.length >= 2) return 'local'
  if (ledger.value.length > 0) return 'backend'
  if (localHistory.value.length > 0) return 'local'
  return ''
})
const trendPoints = computed(() => (trendSource.value === 'backend' ? ledger.value : localHistory.value))
const trend = computed(() => ctxTrendPoints(trendPoints.value))
const trendFallback = computed(() => trendSource.value === 'local')

/** 事件流（用户消息 / 工具调用 / 步骤 / 状态），最多 40 条；跟随当前会话。 */
const events = computed<FeedEvent[]>(() => feedEvents(40))

function eventTone(kind: string): string {
  if (kind === 'user' || kind === 'user-pending') return 'user'
  if (kind === 'tool') return 'tool'
  if (kind === 'error') return 'err'
  if (kind === 'ok') return 'ok'
  return 'state'
}
</script>

<template>
  <div class="ci" :class="page ? 'ci-page' : 'ci-pop'">
    <!-- 页头（只在大页出现）：标题 + 口径徽标 + 模型 + 会话 + 更新时间 + 刷新 -->
    <header v-if="page" class="ci-hd">
      <span class="ci-hd-title">上下文洞察</span>
      <span v-if="context" class="ci-badge" :class="exact ? 'is-exact' : 'is-est'">{{ sampleLabel }}</span>
      <span class="tag">{{ context?.model || '模型未知' }}</span>
      <span
        v-if="context?.sessionId"
        class="ci-hd-sid mono ellipsis"
        :title="context.sessionId"
      >{{ context.sessionId }}</span>
      <span class="spacer" />
      <span v-if="context?.updatedAt" class="tiny muted">更新于 {{ stampText(context.updatedAt) }}</span>
      <button class="btn btn-sm btn-ghost" type="button" @click="emit('refresh')">刷新</button>
    </header>

    <div v-if="!context" class="ci-empty">
      <div>还没有上下文快照</div>
      <div class="tiny muted">
        运行一次 RouterAgent（或在历史会话里点一条），这里会显示精确占用、估算分项、缓存命中、逐轮台账与趋势。
      </div>
      <button class="btn btn-sm" type="button" @click="emit('refresh')">重新读取</button>
    </div>

    <template v-else>
      <!-- 0) 弹层紧凑版：只回答「现在占了多少、由什么构成、缓存命中多少」 -->
      <section v-if="!page" class="ci-hero">
        <div class="ci-hero-hd">
          <span class="ci-hero-head">上下文已用</span>
          <span class="ci-hero-pct num" :class="tone">{{ hasWindow ? pctText : '—' }}</span>
        </div>
        <div class="ci-hero-figs">
          <span class="num">{{ usedText }}<span class="ci-hero-sep">/</span>{{ hasWindow ? formatTok(win) : '窗口未知' }}</span>
          <span class="ci-badge" :class="exact ? 'is-exact' : 'is-est'">{{ sampleLabel }}</span>
        </div>
        <div class="ci-bar-track" aria-hidden="true">
          <div v-if="percent > 0" class="ci-bar" :style="{ width: percent + '%' }">
            <span
              v-for="s in stack"
              :key="s.key"
              class="ci-bar-seg"
              :style="{ flexGrow: s.tokens, background: s.color }"
            />
          </div>
        </div>
        <dl class="ci-legend">
          <div v-for="r in rows" :key="r.key" class="ci-row">
            <dt><span class="ci-dot" :style="{ background: r.color }" />{{ r.label }}</dt>
            <dd class="num ci-row-tok">~{{ formatTok(r.tokens) }}</dd>
          </div>
        </dl>
        <div class="ci-hero-cache">
          <span>缓存命中</span>
          <span class="num">{{ cache.hasData ? cache.hitPercent + '%' : '未上报' }}</span>
        </div>
      </section>

      <!-- 紧凑版底部入口：完整洞察（台账/趋势/累计/事件）在「上下文」标签页 -->
      <button v-if="!page" class="ci-more" type="button" @click="emit('open')">
        查看完整洞察 <span class="ci-more-arrow" aria-hidden="true">→</span>
      </button>

      <!-- 页面版内容（弹层里不渲染，避免把整页塞进 420px 的弹层） -->
      <template v-if="page">
      <!-- 1) KPI 六格：已用 / 窗口 / 占比 / 缓存命中 / 步数 / 工具调用 -->
      <dl class="ci-kpi">
        <div class="ci-kpi-cell">
          <dt>已用<span class="ci-dt-src">{{ sampleLabel }}</span></dt>
          <dd class="num" :title="used + ' token（' + sampleLabel + '口径）'">{{ usedText }}</dd>
        </div>
        <div class="ci-kpi-cell">
          <dt>窗口</dt>
          <dd class="num">{{ hasWindow ? formatTok(win) : '未知' }}</dd>
        </div>
        <div class="ci-kpi-cell">
          <dt>占比</dt>
          <dd class="num" :class="tone">{{ hasWindow ? pctText : '—' }}</dd>
        </div>
        <div class="ci-kpi-cell">
          <dt>缓存命中</dt>
          <dd class="num" :class="{ muted: !cache.hasData }">{{ cache.hasData ? cache.hitPercent + '%' : '未上报' }}</dd>
        </div>
        <div class="ci-kpi-cell">
          <dt>步数</dt>
          <dd class="num">{{ steps }}</dd>
        </div>
        <div class="ci-kpi-cell">
          <dt>工具调用</dt>
          <dd class="num">{{ toolCalls }}</dd>
        </div>
      </dl>

      <!-- 2) 逐轮台账（重点）：一行一次模型调用，最新一行高亮 -->
      <section class="ci-card ci-card-ledger">
        <div class="ci-sect-hd">
          <span>逐轮台账</span>
          <span class="spacer" />
          <span class="tiny muted">共 {{ ledger.length }} 次请求（含收口调用）</span>
        </div>
        <div v-if="ledger.length > 0" class="ci-led">
          <div class="ci-led-hd">
            <span>步</span>
            <span class="r ci-hd-2"><span>请求</span><span>prompt</span></span>
            <span class="r ci-hd-2"><span>缓存</span><span>命中</span></span>
            <span class="r">输出</span>
            <span class="r ci-hd-2"><span>上下文</span><span>规模</span></span>
            <span class="r">时间</span>
          </div>
          <div ref="ledgerEl" class="ci-led-body">
            <div
              v-for="p in ledger"
              :key="p.step"
              class="ci-led-row"
              :class="{ on: p.step === lastStep }"
              :title="'第 ' + p.step + ' 次请求 · ' + (p.at || '时间未知')"
            >
              <span class="num">{{ p.step || '—' }}</span>
              <span class="num r">{{ tokCell(p.promptTokens) }}</span>
              <span class="num r">{{ rowHit(p) }}</span>
              <span class="num r">{{ tokCell(p.completionTokens) }}</span>
              <span class="num r">{{ tokCell(p.used) }}</span>
              <span class="num r ci-led-time">{{ clockText(p.at) || '—' }}</span>
            </div>
          </div>
        </div>
        <div v-else class="ci-empty-line tiny muted">
          本次运行还没有逐轮采样。（后端只保留本进程内的会话记录，重启前的旧会话读不到历史。）
        </div>
      </section>

      <div class="ci-grid">
        <!-- 3) 组成：环形图 + 4px 堆叠条 + 三行图例（估算分项，全部带 ~） -->
        <section v-if="page" class="ci-card">
          <div class="ci-sect-hd">
            <span>上下文组成</span>
            <span class="spacer" />
            <span class="tiny muted">{{ sessionCount }} 个会话 · {{ messages }} 条消息</span>
          </div>
          <div class="ci-compose">
            <svg
              class="ci-donut"
              viewBox="0 0 80 80"
              :width="100"
              :height="100"
              role="img"
              :aria-label="'上下文组成：已用 ' + used + ' token，占 ' + pctText"
            >
              <circle cx="40" cy="40" :r="DONUT_R" fill="none" stroke="var(--panel-3)" stroke-width="11" />
              <template v-if="hasBreakdown">
                <circle
                  v-for="r in rows"
                  :key="r.key"
                  cx="40"
                  cy="40"
                  :r="DONUT_R"
                  fill="none"
                  :stroke="r.color"
                  stroke-width="11"
                  :stroke-dasharray="r.arc + ' ' + (DONUT_C - r.arc)"
                  :stroke-dashoffset="r.offset"
                  transform="rotate(-90 40 40)"
                />
              </template>
              <text x="40" y="37" class="ci-donut-num" text-anchor="middle">{{ formatTok(used) }}</text>
              <text x="40" y="49" class="ci-donut-sub" text-anchor="middle">{{ hasWindow ? pctText : 'tok' }}</text>
            </svg>
            <div class="ci-compose-side">
              <dl class="ci-legend">
                <div v-for="r in rows" :key="r.key" class="ci-row">
                  <dt><span class="ci-dot" :style="{ background: r.color }" />{{ r.label }}</dt>
                  <dd class="num ci-row-tok">~{{ formatTok(r.tokens) }}</dd>
                  <dd class="num ci-row-pct">{{ hasBreakdown ? Math.round(r.pct) + '%' : '—' }}</dd>
                </div>
              </dl>
              <div class="ci-bar-track ci-bar-track-lg" aria-hidden="true">
                <div v-if="percent > 0" class="ci-bar" :style="{ width: percent + '%' }">
                  <span
                    v-for="s in stack"
                    :key="s.key"
                    class="ci-bar-seg"
                    :style="{ flexGrow: s.tokens, background: s.color }"
                  />
                </div>
              </div>
              <div class="ci-bar-ft tiny muted">
                <span>条长 = 占用 {{ hasWindow ? pctText : '—' }}</span>
                <span class="spacer" />
                <span>{{ sampleLabel }}</span>
              </div>
            </div>
          </div>
          <div class="ci-hint">
            估算分项只切相对比例，不与精确占用对账；工具只计 L1 索引（名称+摘要+标签），不含 schema。
          </div>
        </section>

        <!-- 4) 趋势：优先后端 history.used，没有就回退本地采样 -->
        <section class="ci-card">
          <div class="ci-sect-hd">
            <span>上下文趋势</span>
            <span v-if="trendFallback" class="ci-badge is-est ci-badge-sm">本地采样（后端未提供历史）</span>
            <span class="spacer" />
            <span v-if="trend" class="tiny muted">{{ trendPoints.length }} 个采样点</span>
          </div>
          <div v-if="trend" class="ci-trend">
            <svg
              :viewBox="'0 0 ' + TREND_W + ' ' + TREND_H"
              class="ci-trend-svg"
              :style="{ height: page ? '80px' : '58px' }"
              preserveAspectRatio="none"
              role="img"
              :aria-label="'上下文用量趋势，最高 ' + trend.max + ' token'"
            >
              <polyline :points="trend.area" class="ci-trend-area" />
              <polyline :points="trend.poly" class="ci-trend-line" />
              <circle
                :cx="trend.pts[trend.pts.length - 1].x"
                :cy="trend.pts[trend.pts.length - 1].y"
                r="2.4"
                class="ci-trend-dot"
              />
            </svg>
            <div class="ci-trend-ft">
              <span class="tiny muted">第 {{ trend.first.step }} 步 · {{ tokPrefix }}{{ formatTok(trend.first.used) }}</span>
              <span class="spacer" />
              <span class="tiny muted">峰值 {{ tokPrefix }}{{ formatTok(trend.max) }}</span>
              <span class="spacer" />
              <span class="tiny muted">第 {{ trend.last.step }} 步 · {{ tokPrefix }}{{ formatTok(trend.last.used) }}</span>
            </div>
          </div>
          <div v-else class="ci-empty-line tiny muted">
            至少两次模型调用才画得出趋势（当前 {{ trendPoints.length }} 个采样点）。
          </div>
        </section>
      </div>

      <div class="ci-grid ci-grid-bottom">
        <!-- 5) 会话累计：提供方 usage 的四格小台账 -->
        <section class="ci-card">
          <div class="ci-sect-hd">
            <span>会话累计</span>
            <span class="spacer" />
            <span class="tiny muted">{{ sampleLabel }}</span>
          </div>
          <dl v-if="usageHasData" class="ci-cum">
            <div class="ci-cum-cell">
              <dt>提示词</dt>
              <dd class="num">{{ formatTok(usage?.promptTokens ?? 0) }}</dd>
            </div>
            <div class="ci-cum-cell">
              <dt>缓存命中</dt>
              <dd class="num">{{ cache.hasData ? cache.hitPercent + '%' : '—' }}</dd>
            </div>
            <div class="ci-cum-cell">
              <dt>输出</dt>
              <dd class="num">{{ formatTok(usage?.completionTokens ?? 0) }}</dd>
            </div>
            <div class="ci-cum-cell">
              <dt>总计</dt>
              <dd class="num">{{ formatTok(usage?.totalTokens ?? 0) }}</dd>
            </div>
          </dl>
          <div v-else class="ci-empty-line tiny muted">提供方未上报用量（缓存 / 提示词 / 输出）。</div>
        </section>

        <!-- 6) 事件流：本轮发生过什么 -->
        <section class="ci-card">
          <div class="ci-sect-hd">
            <span>事件流</span>
            <span class="spacer" />
            <span v-if="events.length > 0" class="tiny muted">最近 {{ events.length }} 条</span>
          </div>
          <ul v-if="events.length > 0" class="ci-feed" :class="{ 'ci-feed-page': page }">
            <li v-for="e in events" :key="e.id" class="ci-feed-row" :class="'k-' + eventTone(e.kind)">
              <span class="ci-feed-time mono">{{ e.time }}</span>
              <span class="ci-feed-dot" aria-hidden="true" />
              <span class="ci-feed-text ellipsis">{{ e.text }}</span>
            </li>
          </ul>
          <div v-else class="ci-empty-line tiny muted">还没有事件。运行后这里会按时间排出用户消息、工具调用与状态变化。</div>
        </section>
      </div>

      <!-- 7) 口径脚注 -->
      <footer class="ci-foot">
        <span class="ci-note">
          占用数字来自提供方上报的 prompt；组成分项是启发式估算，用 ~ 标出；Token 不设预算上限。
        </span>
        <span class="ci-note">{{ note }}</span>
      </footer>
      </template>
    </template>
  </div>
</template>

<style scoped>
.ci {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
}
.ci-page {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 10px 14px 12px;
}

/* ---------- 页头 ---------- */
.ci-hd {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-bottom: 9px;
  border-bottom: 1px solid var(--glass-line);
}
.ci-hd-title {
  color: var(--text);
  font-size: 13px;
  font-weight: 600;
}
.ci-hd-sid {
  color: var(--muted);
  font-size: 10.5px;
  max-width: 220px;
}
.ci-badge {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  border: 1px solid var(--line-2);
  border-radius: var(--radius-pill);
  font-size: 10.5px;
  line-height: 1;
  white-space: nowrap;
  flex: 0 0 auto;
}
.ci-badge-sm {
  height: 16px;
  font-size: 9.5px;
  padding: 0 5px;
}
.ci-badge.is-exact {
  color: var(--down);
  border-color: rgba(34, 192, 138, 0.4);
  background: rgba(34, 192, 138, 0.1);
}
.ci-badge.is-est {
  color: var(--accent);
  border-color: rgba(233, 180, 76, 0.42);
  background: var(--accent-soft);
}

.ci-empty {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: flex-start;
  padding: 22px 18px;
  border: 1px dashed var(--line-2);
  border-radius: var(--radius-sm);
  color: var(--text-2);
  font-size: 12px;
}
.ci-empty-line {
  padding: 9px 10px;
  border: 1px dashed var(--line-2);
  border-radius: var(--radius-xs);
  line-height: 1.6;
}

/* ---------- 弹层 hero ---------- */
.ci-hero {
  display: flex;
  flex-direction: column;
  gap: 7px;
  padding: 10px 11px;
  background: var(--glass-2);
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-sm);
  backdrop-filter: blur(var(--blur-1)) saturate(130%);
  -webkit-backdrop-filter: blur(var(--blur-1)) saturate(130%);
}
.ci-hero-hd {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ci-hero-head {
  color: var(--text-2);
  font-size: 11.5px;
  white-space: nowrap;
}
.ci-hero-pct {
  font: 600 15px/1 var(--mono);
  color: var(--text);
  letter-spacing: -0.3px;
}
.ci-hero-pct.warn {
  color: var(--accent);
}
.ci-hero-pct.err {
  color: var(--err);
}
/* 紧凑版底部入口：宿主（agentlab / 抽屉）监听 open 后切到「上下文」标签 */
.ci-more {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  height: 26px;
  padding: 0 10px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  color: var(--accent-2);
  font-size: 11px;
  letter-spacing: 0.2px;
  transition: background 0.14s var(--ease), border-color 0.14s var(--ease);
}
.ci-more:hover {
  border-color: var(--line-2);
  background: var(--glass-2);
}
.ci-more-arrow {
  color: var(--accent);
}
.ci-hero-figs {
  display: flex;
  align-items: center;
  gap: 7px;
  font: 12px/1 var(--mono);
  font-variant-numeric: tabular-nums;
  color: var(--text);
}
.ci-hero-sep {
  margin: 0 4px;
  color: var(--faint);
}
.ci-hero-cache .num {
  color: var(--text-2);
}
.ci-hero-cache {
  display: flex;
  align-items: center;
  gap: 5px;
  color: var(--muted);
  font-size: 11px;
}

/* ---------- KPI ---------- */
.ci-kpi {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 7px;
  margin: 0;
}
.ci-pop .ci-kpi {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.ci-kpi-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 6px 8px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  min-width: 0;
}
.ci-kpi-cell dt {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--muted);
  font-size: 9.5px;
  letter-spacing: 0.3px;
  white-space: nowrap;
}
.ci-dt-src {
  color: var(--faint);
  font-size: 9px;
}
.ci-kpi-cell dd {
  margin: 0;
  color: var(--text);
  font: 600 14px/1.2 var(--mono);
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.2px;
  white-space: nowrap;
}
.ci-kpi-cell dd.warn {
  color: var(--accent);
}
.ci-kpi-cell dd.err {
  color: var(--err);
}
.ci-kpi-cell dd.muted {
  color: var(--muted);
  font-size: 11px;
  font-weight: 500;
}

/* ---------- 布局 ---------- */
.ci-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 10px;
}
.ci-grid-bottom {
  align-items: start;
}
.ci-pop .ci-grid {
  grid-template-columns: minmax(0, 1fr);
}
.ci-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 9px 10px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-sm);
  background: var(--glass-2);
  backdrop-filter: blur(var(--blur-1)) saturate(130%);
  -webkit-backdrop-filter: blur(var(--blur-1)) saturate(130%);
  min-width: 0;
}
.ci-sect-hd {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--muted);
  font-size: 10.5px;
  letter-spacing: 0.7px;
  min-width: 0;
}

/* ---------- 堆叠条 ---------- */
.ci-bar-track {
  height: 4px;
  border-radius: var(--radius-pill);
  background: var(--panel-3);
  overflow: hidden;
}
.ci-bar-track-lg {
  margin-top: 2px;
}
.ci-bar {
  display: flex;
  gap: 1px;
  height: 100%;
  min-width: 2px;
  border-radius: var(--radius-pill);
  overflow: hidden;
}
.ci-bar-seg {
  flex-basis: 0;
  min-width: 0;
}
.ci-bar-ft {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 10px;
}

/* ---------- 图例 ---------- */
.ci-legend {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  min-width: 0;
}
.ci-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.ci-row dt {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1 1 auto;
  color: var(--text-2);
  font-size: 11.5px;
  min-width: 0;
}
.ci-row dd {
  margin: 0;
  color: var(--text);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.ci-row-tok {
  font-family: var(--mono);
}
.ci-row-pct {
  width: 36px;
  text-align: right;
  color: var(--muted);
}
.ci-dot {
  width: 8px;
  height: 8px;
  border-radius: 2px;
  flex: 0 0 auto;
}
.ci-compose {
  display: flex;
  align-items: center;
  gap: 14px;
}
.ci-donut {
  flex: 0 0 auto;
}
.ci-donut-num {
  fill: var(--text);
  font: 600 12px var(--mono);
  font-variant-numeric: tabular-nums;
}
.ci-donut-sub {
  fill: var(--muted);
  font: 9.5px var(--mono);
}
.ci-compose-side {
  display: flex;
  flex-direction: column;
  gap: 7px;
  flex: 1 1 auto;
  min-width: 0;
}
.ci-hint {
  color: var(--faint);
  font-size: 10px;
  line-height: 1.5;
}

/* ---------- 逐轮台账 ---------- */
.ci-card-ledger {
  gap: 7px;
}
.ci-led {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  overflow: hidden;
}
.ci-led-hd,
.ci-led-row {
  display: grid;
  grid-template-columns: 32px minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1fr) 60px;
  gap: 6px;
  align-items: center;
  padding: 0 8px;
}
.ci-led-hd {
  min-height: 24px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--glass-line);
  color: var(--muted);
  font-size: 10px;
  line-height: 1.15;
  letter-spacing: 0.2px;
}
.ci-led-hd span,
.ci-led-row span {
  min-width: 0;
}
.ci-led-hd span {
  /* 表头：page 一行；popover 用 .ci-hd-2 的两个 span 强制成两行，
     否则 CJK 默认「任意字间可断」，会出现「缓存命/中」这种断法。 */
  white-space: nowrap;
}
.ci-hd-2 span {
  display: inline;
}
/* page 版两个词之间留一点气口，避免出现「请求prompt」粘连 */
.ci-page .ci-hd-2 span + span {
  margin-left: 3px;
}
.ci-pop .ci-hd-2 {
  display: inline-flex;
  flex-direction: column;
  align-items: flex-end;
  line-height: 1.2;
  white-space: normal;
}
.ci-pop .ci-hd-2 span {
  display: block;
}
.ci-led-row span {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ci-led-hd .r,
.ci-led-row .r {
  text-align: right;
}
.ci-led-body {
  max-height: 240px;
  overflow-y: auto;
}
.ci-led-row {
  height: 26px;
  color: var(--text-2);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  border-bottom: 1px solid rgba(148, 163, 184, 0.07);
}
.ci-led-row:last-child {
  border-bottom: none;
}
.ci-led-row .num {
  font-size: 12px;
}
.ci-led-row.on {
  background: var(--glass-2);
  color: var(--text);
  box-shadow: inset 2px 0 0 var(--accent);
}
.ci-led-time {
  color: var(--muted);
}
.ci-pop .ci-led-hd,
.ci-pop .ci-led-row {
  grid-template-columns: 22px minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1fr) 56px;
  gap: 5px;
  padding: 0 6px;
}
.ci-pop .ci-led-hd {
  font-size: 9px;
  padding: 4px 6px;
  min-height: 24px;
}
.ci-pop .ci-led-time {
  font-size: 10px;
}
.ci-pop .ci-led-row,
.ci-pop .ci-led-row .num {
  font-size: 11px;
}

/* ---------- 趋势 ---------- */
.ci-trend {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.ci-trend-svg {
  width: 100%;
  display: block;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
}
.ci-trend-area {
  fill: rgba(91, 157, 255, 0.14);
  stroke: none;
}
.ci-trend-line {
  fill: none;
  stroke: var(--info);
  stroke-width: 1.5;
  stroke-linejoin: round;
  stroke-linecap: round;
  vector-effect: non-scaling-stroke;
}
.ci-trend-dot {
  fill: var(--accent-2);
}
.ci-trend-ft {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ci-trend-ft .tiny {
  white-space: nowrap;
}

/* ---------- 会话累计 ---------- */
.ci-cum {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 6px;
  margin: 0;
}
.ci-pop .ci-cum {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.ci-cum-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 5px 7px;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
  min-width: 0;
}
.ci-cum-cell dt {
  color: var(--muted);
  font-size: 9.5px;
  white-space: nowrap;
}
.ci-cum-cell dd {
  margin: 0;
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* ---------- 事件流 ---------- */
.ci-feed {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  max-height: 148px;
  overflow-y: auto;
  border: 1px solid var(--glass-line);
  border-radius: var(--radius-xs);
  background: var(--glass-1);
}
.ci-feed-page {
  max-height: 240px;
}
.ci-feed-row {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 24px;
  padding: 0 8px;
  font-size: 11px;
  color: var(--text-2);
}
.ci-feed-row:hover {
  background: var(--glass-2);
}
.ci-feed-time {
  flex: 0 0 auto;
  color: var(--faint);
  font-size: 10px;
}
.ci-feed-dot {
  flex: 0 0 auto;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--faint);
}
.ci-feed-text {
  flex: 1 1 auto;
  min-width: 0;
}
.ci-feed-row.k-user .ci-feed-dot {
  background: var(--info);
}
.ci-feed-row.k-user .ci-feed-text {
  color: var(--info);
}
.ci-feed-row.k-tool .ci-feed-dot {
  background: var(--info);
}
.ci-feed-row.k-tool .ci-feed-text {
  color: var(--text-2);
}
.ci-feed-row.k-state .ci-feed-dot {
  background: var(--accent);
}
.ci-feed-row.k-state .ci-feed-text {
  color: var(--accent-2);
}
.ci-feed-row.k-ok .ci-feed-dot {
  background: var(--ok);
}
.ci-feed-row.k-err .ci-feed-dot {
  background: var(--err);
}
.ci-feed-row.k-err .ci-feed-text {
  color: var(--err);
}

/* ---------- 口径 ---------- */
.ci-foot {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding-top: 6px;
  border-top: 1px solid var(--glass-line);
  color: var(--muted);
  font-size: 10.5px;
  line-height: 1.5;
}
.ci-note {
  color: var(--faint);
}
</style>
