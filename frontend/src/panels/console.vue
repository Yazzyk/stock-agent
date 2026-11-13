<script setup lang="ts">
// 设置中心：插件 / 工具 / 审计 / 安全策略 / 数据源。
// 数据来自 console.inventory（含 plugins/status/tools/audit/disabled/protected）与 console.policy；
// 停用插件写回 settings，重启生效 —— 这里只负责说清「什么时候生效」。
import { computed, onMounted, ref } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import {
  consoleFuyao,
  consoleFuyaoSave,
  consoleFuyaoTest,
  consoleInventory,
  consolePolicy,
  consoleSetEnabled,
  type ConsoleInventory,
  type FuyaoStatus,
} from '../lib/api.settings'
import { messageOf } from '../lib/data'
import { fmtDateTime } from '../lib/format'
import { activate, state, toast } from '../stores/app'
import type { AuditRow, PluginView, Policy } from '../types'

type TabKey = 'plugins' | 'tools' | 'audit' | 'policy' | 'data'

const TABS: Array<{ key: TabKey; label: string }> = [
  { key: 'plugins', label: '插件' },
  { key: 'tools', label: '工具' },
  { key: 'audit', label: '审计' },
  { key: 'policy', label: '安全策略' },
  { key: 'data', label: '数据源' },
]

const tab = ref<TabKey>('plugins')
const inv = ref<ConsoleInventory | null>(null)
const loading = ref(true)
const invError = ref('')
const policy = ref<Policy | null>(null)
const policyError = ref('')
const busyId = ref('')
const query = ref('')
const expanded = ref('')
/** setEnabled 成功后置位：顶部常驻「重启生效」提示 */
const restartPending = ref(false)

// ---------- 官方金融数据 API（同花顺 fuyao） ----------
/** 当前配置状态（Key 只有掩码，不回显明文）。 */
const fy = ref<FuyaoStatus | null>(null)
const fyError = ref('')
const fyBusy = ref('')
/** 输入框里的新 Key（留空 = 保持原 Key 不变）。 */
const fyKey = ref('')
const fyBase = ref('')
const fyEnabled = ref(true)
const fyTest = ref<{ ok: boolean; message: string } | null>(null)

/** 读取官方 API 配置状态；失败不打断数据源页的其它内容。 */
async function loadFuyao(): Promise<void> {
  fyError.value = ''
  try {
    const st = await consoleFuyao()
    fy.value = st
    fyBase.value = st.baseUrl
    fyEnabled.value = st.enabled
  } catch (e) {
    fyError.value = messageOf(e)
  }
}

/** 保存官方 API 配置（Key 留空即保持原值；重新保存后立即生效）。 */
async function saveFuyao(clearKey = false): Promise<void> {
  if (fyBusy.value !== '') return
  fyBusy.value = clearKey ? 'clear' : 'save'
  fyTest.value = null
  try {
    const st = await consoleFuyaoSave({ apiKey: clearKey ? '' : fyKey.value.trim(), baseUrl: fyBase.value.trim(), enabled: fyEnabled.value, clearKey })
    fy.value = st
    fyKey.value = ''
    fyEnabled.value = st.enabled
    fyBase.value = st.baseUrl
    // 配置缓存 30 秒失效；这里提示一下，避免用户以为没生效。
    toast(clearKey ? '已清除官方 API Key' : '已保存：官方数据源优先生效（现有链路继续兜底）', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    fyBusy.value = ''
  }
}

/** 用真实快照验证 Key 是否可用。 */
async function testFuyao(): Promise<void> {
  if (fyBusy.value !== '') return
  fyBusy.value = 'test'
  fyTest.value = null
  try {
    fyTest.value = await consoleFuyaoTest()
  } catch (e) {
    fyTest.value = { ok: false, message: messageOf(e) }
  } finally {
    fyBusy.value = ''
  }
}

const plugins = computed<PluginView[]>(() => inv.value?.plugins ?? [])
const tools = computed(() => inv.value?.tools ?? [])
const status = computed(() => inv.value?.status ?? null)
/** 审计事件由 inventory 自带（不再单独调 console.audit） */
const auditRows = computed<AuditRow[]>(() => inv.value?.audit ?? [])
/** 受保护插件：开关置灰，避免把设置中心自己停掉 */
const protectedIds = computed(() => new Set(inv.value?.protected ?? []))

function isProtected(p: PluginView): boolean {
  return protectedIds.value.has(p.id)
}

function switchTitle(p: PluginView): string {
  if (isProtected(p)) return '受保护插件：设置中心不能停用自己'
  return p.active ? '停用后重启生效' : '启用后重启生效'
}

const stats = computed(() => {
  const total = plugins.value.length
  const active = plugins.value.filter((p) => p.active).length
  const toolCount = tools.value.length
  const panelCount = plugins.value.reduce((n, p) => n + p.ui.length, 0)
  const serviceCount = status.value?.services.length ?? 0
  return { total, active, toolCount, panelCount, serviceCount }
})

/** 工具 → 来源插件（inventory 的 ToolView 没有插件字段，用插件内嵌工具索引反查）。 */
const toolOwner = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {}
  for (const p of plugins.value) {
    for (const t of p.tools) if (!map[t.name]) map[t.name] = p.name || p.id
  }
  return map
})

const filteredTools = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (q === '') return tools.value
  return tools.value.filter((t) => {
    const hay = [t.name, t.title, t.summary, (t.tags ?? []).join(' '), toolOwner.value[t.name] ?? ''].join(' ').toLowerCase()
    return hay.includes(q)
  })
})

const failedAudit = computed(() => auditRows.value.filter((r) => !r.ok).length)

const dataSources = computed(() => {
  const health = status.value?.dataHealth ?? {}
  return Object.keys(health).map((name) => ({ name, value: health[name] ?? '' }))
})

function pluginStatusText(p: PluginView): string {
  if (p.active) return '已激活'
  if (p.skipped) return '已停用'
  return '失败'
}

function pluginStatusClass(p: PluginView): string {
  if (p.active) return 'tag-down'
  if (p.skipped) return 'tag'
  return 'tag-up'
}

function pluginStatusDot(p: PluginView): string {
  if (p.active) return 'dot-ok'
  if (p.skipped) return 'dot-off'
  return 'dot-err'
}

function riskClass(risk: string): string {
  if (risk === 'exec') return 'tag-up'
  if (risk === 'write') return 'tag-accent'
  if (risk === 'network') return 'tag-info'
  return 'tag'
}

function riskText(risk: string): string {
  if (risk === 'exec') return '执行'
  if (risk === 'write') return '写'
  if (risk === 'network') return '网络'
  return '只读'
}

function healthDot(v: string): string {
  const s = v.toLowerCase()
  if (s === 'ok' || s === 'up' || s === '健康') return 'dot-ok'
  if (s === 'down' || s === 'error' || s === 'failed') return 'dot-err'
  return 'dot-warn'
}

async function loadInventory(): Promise<void> {
  loading.value = true
  invError.value = ''
  try {
    const data = await consoleInventory()
    inv.value = data
    restartPending.value = data.restartRequired
  } catch (e) {
    invError.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadPolicy(): Promise<void> {
  policyError.value = ''
  try {
    policy.value = await consolePolicy()
  } catch (e) {
    policyError.value = messageOf(e)
  }
}

async function refreshAll(): Promise<void> {
  await Promise.all([loadInventory(), loadPolicy()])
}

/** 用后端返回的停用清单重排插件状态（重启前内核仍在跑，但 UI 要一致）。 */
function applyDisabled(ids: string[]): void {
  if (!inv.value) return
  inv.value.disabled = ids.slice()
  const off = new Set(ids)
  for (const p of inv.value.plugins) {
    if (protectedIds.value.has(p.id)) continue
    const disabled = off.has(p.id)
    p.active = !disabled
    p.skipped = disabled
    if (disabled) p.error = '已停用（可在设置中心启用，重启生效）'
    else if ((p.error ?? '').startsWith('已停用')) p.error = ''
  }
}

async function togglePlugin(p: PluginView, next: boolean): Promise<void> {
  if (busyId.value !== '') return
  busyId.value = p.id
  try {
    const res = await consoleSetEnabled(p.id, next)
    if (res.restartRequired) restartPending.value = true
    if (res.disabled.length > 0 || next) applyDisabled(res.disabled)
    // 兜底乐观更新：后端没回 disabled 时也要让用户立刻看到结果
    p.active = next
    p.skipped = !next
    p.error = next ? '' : '已停用（可在设置中心启用，重启生效）'
    if (inv.value && !next && !inv.value.disabled.includes(p.id)) inv.value.disabled.push(p.id)
    if (inv.value && next) inv.value.disabled = inv.value.disabled.filter((id) => id !== p.id)
    toast(res.message, res.ok ? 'ok' : 'error')
  } catch (e) {
    toast(messageOf(e), 'error')
    await loadInventory()
  } finally {
    busyId.value = ''
  }
}

function goSync(): void {
  const slot = state.slots.find((s) => s.component === 'watchlist')
  if (slot) {
    activate(slot.id)
    return
  }
  toast('自选面板未启用：请先在「插件」里激活 watchlist', 'warn')
}

onMounted(() => {
  void refreshAll()
  void loadFuyao()
})
</script>

<template>
  <section class="card cs">
    <div class="card-hd">
      <span class="card-title">设置中心</span>
      <span class="card-sub">插件安装与激活、工具索引、审计、安全策略、数据源健康</span>
      <span class="spacer" />
      <span class="card-tools">
        <button class="btn btn-sm" type="button" @click="refreshAll">刷新</button>
      </span>
    </div>

    <div class="cs-stats">
      <div class="metric"><span class="metric-label">已安装</span><span class="metric-value num">{{ stats.total }}</span><span class="metric-foot">个插件</span></div>
      <div class="metric"><span class="metric-label">已激活</span><span class="metric-value num accent">{{ stats.active }}</span><span class="metric-foot">个插件</span></div>
      <div class="metric"><span class="metric-label">工具</span><span class="metric-value num">{{ stats.toolCount }}</span><span class="metric-foot">可调用</span></div>
      <div class="metric"><span class="metric-label">面板</span><span class="metric-value num">{{ stats.panelCount }}</span><span class="metric-foot">个 UI slot</span></div>
      <div class="metric"><span class="metric-label">服务</span><span class="metric-value num">{{ stats.serviceCount }}</span><span class="metric-foot">已注册</span></div>
    </div>

    <div v-if="restartPending" class="cs-restart">
      <span class="dot dot-warn" />
      <span>插件启用状态已保存，<b>重启应用后生效</b>（当前会话里停用的插件仍在内存中运行）。</span>
    </div>

    <div class="tabs">
      <button v-for="t in TABS" :key="t.key" class="tab" :class="{ on: tab === t.key }" type="button" @click="tab = t.key">
        {{ t.label }}
        <span v-if="t.key === 'plugins'" class="tiny muted">{{ plugins.length }}</span>
        <span v-if="t.key === 'tools'" class="tiny muted">{{ tools.length }}</span>
        <span v-if="t.key === 'audit' && failedAudit > 0" class="tag tag-up">{{ failedAudit }}</span>
      </button>
    </div>

    <div class="cs-body">
      <!-- 插件 -->
      <template v-if="tab === 'plugins'">
        <StateBlock v-if="loading" kind="loading" title="读取插件清单…" />
        <StateBlock v-else-if="invError" kind="error" title="读取失败" :message="invError" action-text="重试" @action="loadInventory" />
        <div v-else-if="plugins.length === 0" class="empty">
          <div>没有读到任何插件</div>
          <div class="tiny">内核可能还没启动，或 console 插件未被注册。</div>
        </div>
        <div v-else class="cs-plugins">
          <article v-for="p in plugins" :key="p.id" class="cs-plugin" :class="{ off: !p.active }">
            <header class="cs-plugin-hd">
              <span class="dot" :class="pluginStatusDot(p)" />
              <span class="cs-plugin-name ellipsis">{{ p.name || p.id }}</span>
              <span class="tiny muted mono">v{{ p.version }}</span>
              <span class="tag" :class="pluginStatusClass(p)">{{ pluginStatusText(p) }}</span>
              <span v-if="isProtected(p)" class="tag cs-lock" title="受保护插件：设置中心不能停用自己">受保护</span>
              <span class="spacer" />
              <label class="check cs-switch" :class="{ locked: isProtected(p) }" :title="switchTitle(p)">
                <input
                  type="checkbox"
                  :checked="p.active"
                  :disabled="busyId === p.id || isProtected(p)"
                  :aria-disabled="isProtected(p)"
                  @change="togglePlugin(p, ($event.target as HTMLInputElement).checked)"
                />
                <span v-if="isProtected(p)" class="tiny muted">锁定</span>
              </label>
            </header>
            <div class="cs-plugin-desc">{{ p.description }}</div>
            <div class="cs-plugin-meta">
              <span class="mono ellipsis">{{ p.id }}</span>
              <span v-if="(p.dependsOn ?? []).length > 0" class="tiny muted">依赖 {{ (p.dependsOn ?? []).join(' / ') }}</span>
            </div>
            <div v-if="(p.tags ?? []).length > 0" class="cs-plugin-tags">
              <span v-for="tg in p.tags" :key="tg" class="chip cs-tag">{{ tg }}</span>
            </div>
            <div class="cs-plugin-counts">
              <span class="tiny">工具 <b class="num">{{ p.tools.length }}</b></span>
              <span class="tiny">方法 <b class="num">{{ p.methods.length }}</b></span>
              <span class="tiny">面板 <b class="num">{{ p.ui.length }}</b></span>
              <span class="spacer" />
              <button
                v-if="p.error"
                class="link-btn"
                type="button"
                @click="expanded = expanded === p.id ? '' : p.id"
              >
                {{ expanded === p.id ? '收起失败原因' : '查看失败原因' }}
              </button>
            </div>
            <div v-if="p.error && expanded === p.id" class="cs-plugin-err">{{ p.error }}</div>
          </article>
        </div>
      </template>

      <!-- 工具 -->
      <template v-else-if="tab === 'tools'">
        <div class="toolbar cs-toolbar">
          <input v-model="query" class="input cs-search" placeholder="搜索工具名 / 说明 / 插件 / 标签" spellcheck="false" />
          <span class="tiny muted">{{ filteredTools.length }} / {{ tools.length }}</span>
        </div>
        <div v-if="filteredTools.length === 0" class="empty"><div>没有匹配的工具</div><div class="tiny">换个关键词，或先在「插件」里激活对应插件。</div></div>
        <div v-else class="table-wrap">
          <table class="tbl">
            <thead>
              <tr>
                <th class="l">工具</th>
                <th class="l">说明</th>
                <th class="l">来源</th>
                <th class="l">风险</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="t in filteredTools" :key="t.name">
                <td class="l mono">{{ t.name }}</td>
                <td class="l"><span class="ellipsis">{{ t.summary || t.title }}</span></td>
                <td class="l tiny muted">{{ toolOwner[t.name] ?? '—' }}</td>
                <td class="l"><span class="tag" :class="riskClass(t.risk)">{{ riskText(t.risk) }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 审计（来自 inventory.audit，最近 100 条） -->
      <template v-else-if="tab === 'audit'">
        <div v-if="auditRows.length === 0" class="empty"><div>还没有审计记录</div><div class="tiny">插件方法调用与工具调用都会留痕。</div></div>
        <div v-else class="table-wrap">
          <table class="tbl">
            <thead>
              <tr>
                <th class="l">时间</th>
                <th class="l">插件</th>
                <th class="l">动作</th>
                <th class="l">耗时</th>
                <th class="l">结果</th>
                <th class="l">详情</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(r, i) in auditRows" :key="i" :class="{ fail: !r.ok }">
                <td class="l mono tiny">{{ fmtDateTime(r.time) }}</td>
                <td class="l tiny">{{ r.plugin }}</td>
                <td class="l mono">{{ r.action }}</td>
                <td class="l num">{{ r.elapsedMs ?? 0 }}ms</td>
                <td class="l">
                  <span class="tag" :class="r.ok ? 'tag-down' : 'tag-up'">{{ r.ok ? '成功' : '失败' }}</span>
                </td>
                <td class="l tiny muted ellipsis">{{ r.detail || r.kind }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 安全策略 -->
      <template v-else-if="tab === 'policy'">
        <StateBlock v-if="policyError" kind="error" title="读取策略失败" :message="policyError" action-text="重试" @action="loadPolicy" />
        <div v-else-if="!policy" class="empty"><div>策略读取中…</div></div>
        <div v-else class="cs-policy">
          <div class="cs-policy-row">
            <span class="cs-policy-k">网络白名单</span>
            <span class="cs-policy-v">
              <span v-for="h in policy.networkAllowlist" :key="h" class="chip cs-tag">{{ h }}</span>
              <span v-if="policy.networkAllowlist.length === 0" class="tiny muted">未配置（所有外网请求都会失败）</span>
            </span>
          </div>
          <div class="cs-policy-row">
            <span class="cs-policy-k">路径根</span>
            <span class="cs-policy-v">
              <span v-for="p in policy.pathRoots" :key="p" class="chip cs-tag mono">{{ p }}</span>
            </span>
          </div>
          <div class="cs-policy-row">
            <span class="cs-policy-k">允许外部命令</span>
            <span class="cs-policy-v" :class="policy.allowExec ? 'hint-warn' : ''">{{ policy.allowExec ? '允许（不推荐）' : '禁止（只读研究）' }}</span>
          </div>
          <div class="cs-policy-row">
            <span class="cs-policy-k">请求限速</span>
            <span class="cs-policy-v num">{{ policy.maxReqPerMin }} 次 / 分钟</span>
          </div>
          <div class="cs-policy-row">
            <span class="cs-policy-k">单次运行工具上限</span>
            <span class="cs-policy-v num">{{ policy.maxToolCalls }} 次</span>
          </div>
          <div class="cs-policy-row">
            <span class="cs-policy-k">单次运行 token 上限</span>
            <span class="cs-policy-v num">{{ policy.maxTokensPerRun > 0 ? policy.maxTokensPerRun : '不设上限' }}</span>
          </div>
          <div class="tiny muted cs-policy-note">策略只读展示：修改需要在内核配置文件里改，重启生效。</div>
        </div>
      </template>

      <!-- 数据源 -->
      <template v-else>
        <!-- 官方金融数据 API：Key 只写不读，保存后优先生效，失败自动降级 -->
        <section class="cs-fy">
          <div class="cs-fy-hd">
            <span class="cs-fy-title">同花顺官方金融数据 API</span>
            <span v-if="fy?.health" class="tag">{{ fy.health }}</span>
            <span class="spacer" />
            <span class="tiny muted">官方优先 · 现有链路兜底</span>
          </div>
          <div v-if="fyError" class="hint-warn">读取失败：{{ fyError }}</div>
          <template v-else>
            <div class="cs-fy-row">
              <span class="cs-fy-k">API Key</span>
              <input
                v-model="fyKey"
                class="cs-fy-input mono"
                type="password"
                autocomplete="off"
                spellcheck="false"
                :placeholder="fy?.hasKey ? '已保存：' + fy.keyMasked + '（留空表示不修改）' : '粘贴 X-api-key（fuyao.aicubes.cn）'"
              />
            </div>
            <div class="cs-fy-row">
              <span class="cs-fy-k">Base URL</span>
              <input v-model="fyBase" class="cs-fy-input mono" type="text" spellcheck="false" placeholder="https://fuyao.aicubes.cn" />
            </div>
            <div class="cs-fy-row">
              <span class="cs-fy-k">启用</span>
              <label class="check">
                <input v-model="fyEnabled" type="checkbox" />
                <span class="tiny">官方 API 优先（关闭后只用腾讯/新浪/网页同花顺）</span>
              </label>
            </div>
            <div class="cs-fy-ft">
              <button class="btn btn-sm btn-accent" type="button" :disabled="fyBusy !== ''" @click="saveFuyao(false)">
                {{ fyBusy === 'save' ? '保存中…' : '保存' }}
              </button>
              <button class="btn btn-sm" type="button" :disabled="fyBusy !== ''" @click="testFuyao">测试连通</button>
              <button v-if="fy?.hasKey" class="btn btn-sm btn-ghost" type="button" :disabled="fyBusy !== ''" @click="saveFuyao(true)">
                清除 Key
              </button>
              <span v-if="fyTest" class="tiny" :class="fyTest.ok ? 'hint-ok' : 'hint-warn'">{{ fyTest.message }}</span>
              <span class="spacer" />
              <span class="tiny muted">Key 存在本机数据目录（0600），不回显、不进日志</span>
            </div>
          </template>
        </section>

        <div v-if="dataSources.length === 0" class="empty"><div>没有数据源健康信息</div><div class="tiny">内核 Status.dataHealth 为空。</div></div>
        <div v-else class="cs-data">
          <div v-for="s in dataSources" :key="s.name" class="cs-data-row">
            <span class="dot" :class="healthDot(s.value)" />
            <span class="cs-data-name">{{ s.name }}</span>
            <span class="tiny muted mono">{{ s.value }}</span>
            <span class="spacer" />
            <button class="btn btn-sm btn-ghost" type="button" @click="goSync">去同步</button>
          </div>
          <div class="tiny muted cs-policy-note">同花顺本地数据缺失时，自选面板会自动弹出授权向导；这里只显示最近一次探测结果。</div>
        </div>
      </template>
    </div>
  </section>
</template>


<style scoped>
  .cs {
    min-height: 0;
  }
  /* ---------- 官方金融数据 API ---------- */
  .cs-fy {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px;
    margin-bottom: 10px;
    border: 0.5px solid var(--line-2);
    border-radius: 10px;
    background: var(--glass-1);
  }
  .cs-fy-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .cs-fy-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-1);
  }
  .cs-fy-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .cs-fy-k {
    flex: 0 0 70px;
    font-size: 12px;
    color: var(--text-3);
  }
  .cs-fy-input {
    flex: 1 1 auto;
    min-width: 0;
    height: 28px;
    padding: 0 8px;
    border: 0.5px solid var(--line-2);
    border-radius: 6px;
    background: var(--glass-2);
    color: var(--text-1);
    font-size: 12px;
  }
  .cs-fy-input:focus {
    outline: none;
    border-color: var(--accent);
  }
  .cs-fy-ft {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .cs .card-hd {
    flex-wrap: wrap;
    row-gap: 4px;
    padding-top: 7px;
    padding-bottom: 7px;
  }
  .cs-stats {
    flex: 0 0 auto;
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 8px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--line);
  }
  .cs-restart {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 7px 12px;
    border-bottom: 1px solid var(--line);
    background: rgba(233, 180, 76, 0.07);
    color: var(--accent-2);
    font-size: 11.5px;
  }
  .cs-body {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    padding: 10px 12px;
  }
  .cs-plugins {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 9px;
  }
  .cs-plugin {
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 10px 11px;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background: var(--panel-2);
    min-width: 0;
  }
  .cs-plugin.off {
    opacity: 0.72;
    border-style: dashed;
  }
  .cs-plugin-hd {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .cs-plugin-name {
    color: var(--text);
    font-size: 12.5px;
    font-weight: 600;
  }
  .cs-switch.locked {
    cursor: not-allowed;
    opacity: 0.55;
  }
  .cs-switch.locked input {
    cursor: not-allowed;
  }
  .cs-switch.locked:hover {
    opacity: 0.55;
  }
  .cs-lock {
    border-color: rgba(148, 163, 184, 0.3);
    color: var(--muted);
    cursor: help;
  }
  .cs-plugin-desc {
    color: var(--text-2);
    font-size: 11.5px;
    line-height: 1.55;
  }
  .cs-plugin-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    color: var(--muted);
    font-size: 10.5px;
  }
  .cs-plugin-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .cs-tag {
    height: 20px;
    padding: 0 7px;
    font-size: 10.5px;
  }
  .cs-plugin-counts {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-top: 5px;
    border-top: 1px solid var(--line);
    color: var(--muted);
  }
  .cs-plugin-counts b {
    color: var(--text-2);
  }
  .cs-plugin-err {
    padding: 6px 8px;
    border: 1px solid rgba(255, 93, 93, 0.32);
    border-radius: var(--radius-sm);
    background: rgba(255, 93, 93, 0.07);
    color: #ffb4b4;
    font: 11px/1.6 var(--mono);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .cs-toolbar {
    margin-bottom: 8px;
  }
  .cs-search {
    flex: 0 1 320px;
  }
  .cs-policy {
    display: flex;
    flex-direction: column;
    gap: 7px;
    margin-bottom: 10px;
    padding: 10px;
    max-width: 760px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .cs-data {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 760px;
  }
  .cs-policy-row {
    display: grid;
    grid-template-columns: 150px minmax(0, 1fr);
    gap: 10px;
    align-items: start;
    padding: 7px 0;
    border-bottom: 1px solid var(--line);
    font-size: 12px;
  }
  .cs-policy-k {
    color: var(--muted);
  }
  .cs-policy-v {
    color: var(--text-2);
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .cs-policy-note {
    margin-top: 4px;
    line-height: 1.7;
  }
  .cs-data-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
  }
  .cs-data-name {
    color: var(--text);
    font-size: 12.5px;
  }
  .link-btn {
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--info);
    font-size: 11px;
  }
  .link-btn:hover {
    text-decoration: underline;
  }
  .tbl tr.fail td {
    background: rgba(255, 93, 93, 0.05);
    color: #ffb4b4;
  }
</style>
