<script setup lang="ts">
// 模型设置：provider 列表 + 编辑表单 + 连通测试 + 设为默认。
// 密钥只写不读：输入框永远为空，只显示 hasKey。
import { computed, onMounted, reactive, ref } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import { messageOf, modelList, modelRemove, modelSave, modelTest } from '../lib/data'
import { toast } from '../stores/app'
import type { ModelDraft, ModelInfo, ModelTestResult } from '../types'

const providers = ref<ModelInfo[]>([])
const defaultId = ref('')
const loading = ref(true)
const error = ref('')
const saving = ref(false)
const testing = ref(false)
const removing = ref('')
const testResult = ref<ModelTestResult | null>(null)

const form = reactive({ id: '', provider: 'deepseek', name: '', baseUrl: '', model: '', apiKey: '', maxTokens: 8192 })

const editing = computed(() => form.id !== '')

const formError = computed(() => {
  if (form.model.trim() === '') return '模型名不能为空，例如 deepseek-chat'
  if (form.provider === 'openai-compat' && form.baseUrl.trim() === '') return 'OpenAI 兼容通道必须填 BaseURL'
  if (form.maxTokens !== undefined && form.maxTokens !== null && (form.maxTokens < 128 || form.maxTokens > 200000)) return 'maxTokens 需在 128 ~ 200000 之间'
  return ''
})

function applyList(res: { providers: ModelInfo[]; defaultId: string }): void {
  providers.value = res.providers
  defaultId.value = res.defaultId || (res.providers.find((p) => p.isDefault)?.id ?? '')
  const def = providers.value.find((p) => p.id === defaultId.value)
  if (def) {
    for (const p of providers.value) p.isDefault = p.id === defaultId.value
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    applyList(await modelList())
    const def = providers.value.find((p) => p.isDefault) ?? providers.value[0]
    if (def && form.id === '') loadIntoForm(def)
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

function loadIntoForm(m: ModelInfo): void {
  form.id = m.id ?? ''
  form.provider = m.provider
  form.name = m.displayName ?? m.name ?? ''
  form.baseUrl = m.baseUrl ?? ''
  form.model = m.model
  form.apiKey = ''
  form.maxTokens = m.maxTokens ?? 8192
  testResult.value = null
}

function newModel(): void {
  form.id = ''
  form.provider = 'deepseek'
  form.name = ''
  form.baseUrl = 'https://api.deepseek.com'
  form.model = 'deepseek-chat'
  form.apiKey = ''
  form.maxTokens = 8192
  testResult.value = null
}

function draft(setDefault = false): ModelDraft {
  const d: ModelDraft = {
    provider: form.provider.trim(),
    model: form.model.trim(),
    setDefault,
  }
  if (form.id) d.id = form.id
  if (form.name.trim()) d.name = form.name.trim()
  if (form.baseUrl.trim()) d.baseUrl = form.baseUrl.trim()
  if (form.apiKey.trim()) d.apiKey = form.apiKey.trim()
  if (typeof form.maxTokens === 'number' && form.maxTokens > 0) d.maxTokens = form.maxTokens
  return d
}

async function save(setDefault = false): Promise<ModelInfo | null> {
  if (formError.value) {
    toast(formError.value, 'warn')
    return null
  }
  saving.value = true
  error.value = ''
  try {
    applyList(await modelSave(draft(setDefault)))
    form.apiKey = ''
    const saved =
      providers.value.find((p) => p.provider === form.provider && p.model === form.model) ??
      providers.value.find((p) => p.isDefault) ??
      null
    if (saved) {
      form.id = saved.id ?? form.id
      form.name = saved.displayName ?? form.name
      form.baseUrl = saved.baseUrl ?? form.baseUrl
    }
    toast(setDefault ? '已保存并设为默认模型' : '模型配置已保存', 'ok')
    return saved
  } catch (e) {
    toast(messageOf(e), 'error')
    return null
  } finally {
    saving.value = false
  }
}

async function test(): Promise<void> {
  let id = form.id
  if (!id) {
    const saved = await save(false)
    id = saved?.id ?? ''
    if (!id) return
  }
  testing.value = true
  testResult.value = null
  try {
    const res = await modelTest(id)
    testResult.value = res
    toast(res.ok ? '连通正常（' + res.latencyMs + 'ms）' : '连通失败：' + res.message, res.ok ? 'ok' : 'error')
  } catch (e) {
    testResult.value = { ok: false, latencyMs: 0, message: messageOf(e) }
    toast(messageOf(e), 'error')
  } finally {
    testing.value = false
  }
}

async function remove(m: ModelInfo): Promise<void> {
  const id = m.id ?? ''
  if (!id) return
  if (removing.value !== id) {
    removing.value = id
    window.setTimeout(() => {
      if (removing.value === id) removing.value = ''
    }, 4000)
    return
  }
  removing.value = ''
  try {
    applyList(await modelRemove(id))
    if (form.id === id) newModel()
    toast('已删除配置', 'ok')
  } catch (e) {
    toast(messageOf(e), 'error')
  }
}

function providerLabel(p: string): string {
  if (p === 'deepseek') return 'DeepSeek'
  if (p === 'openai-compat') return 'OpenAI 兼容'
  return p
}

onMounted(load)
</script>

<template>
  <section class="card ms">
    <div class="card-hd">
      <span class="card-title">模型设置</span>
      <span class="card-sub">密钥本地加密存储，任何接口都不回显明文</span>
      <span class="card-tools">
        <button class="btn btn-sm" type="button" @click="load">刷新</button>
        <button class="btn btn-sm btn-accent" type="button" @click="newModel">新增 Provider</button>
      </span>
    </div>

    <div class="ms-body">
      <div class="ms-list">
        <StateBlock v-if="loading" kind="loading" title="读取模型配置…" />
        <StateBlock
          v-else-if="error"
          kind="error"
          title="读取失败"
          :message="error"
          action-text="重试"
          @action="load"
        />
        <StateBlock
          v-else-if="providers.length === 0"
          kind="empty"
          title="还没有配置模型"
          message="至少配置一个模型才能使用 Agent 面板。填 DeepSeek 或任意 OpenAI 兼容端点。"
        />
        <template v-else>
          <button
            v-for="m in providers"
            :key="m.id || m.provider + m.model"
            class="ms-item"
            :class="{ on: form.id === m.id }"
            type="button"
            @click="m.id && loadIntoForm(m)"
          >
            <div class="row" style="gap: 6px">
              <span class="ms-prov">{{ providerLabel(m.provider) }}</span>
              <span v-if="m.isDefault || m.id === defaultId" class="tag tag-accent">默认</span>
              <span class="spacer" />
              <span class="dot" :class="m.hasKey ? 'dot-ok' : 'dot-err'" :title="m.hasKey ? '已配置密钥' : '未配置密钥'" />
            </div>
            <div class="ms-model num">{{ m.model }}</div>
            <div class="tiny muted ellipsis">{{ m.baseUrl || '使用 Provider 默认端点' }}</div>
            <div class="row" style="gap: 6px; margin-top: 6px">
              <span class="tiny" :class="m.hasKey ? 'muted' : 'hint-warn'">{{ m.hasKey ? '已配置 key' : '未配置 key' }}</span>
              <span class="spacer" />
              <span class="btn btn-sm btn-ghost" role="button" tabindex="0" @click.stop="m.id && loadIntoForm(m)">编辑</span>
              <span class="btn btn-sm btn-ghost" :class="{ 'btn-danger': removing === m.id }" role="button" tabindex="0" @click.stop="remove(m)">
                {{ removing === m.id ? '确认删除' : '删除' }}
              </span>
            </div>
          </button>
        </template>
      </div>

      <div class="ms-form">
        <div class="ms-form-hd">
          <span class="card-title">{{ editing ? '编辑配置' : '新增配置' }}</span>
          <span class="spacer" />
          <span v-if="testResult" class="tag" :class="testResult.ok ? 'tag-down' : 'tag-up'">
            {{ testResult.ok ? '连通 ' + testResult.latencyMs + 'ms' : '连通失败' }}
          </span>
        </div>

        <div class="ms-grid">
          <label class="field">
            <span class="label">Provider</span>
            <select v-model="form.provider" class="select">
              <option value="deepseek">DeepSeek</option>
              <option value="openai-compat">OpenAI 兼容</option>
            </select>
          </label>

          <label class="field">
            <span class="label">显示名（可选）</span>
            <input v-model="form.name" class="input" placeholder="例如 主模型" spellcheck="false" />
          </label>

          <label class="field">
            <span class="label">Model</span>
            <input v-model="form.model" class="input" placeholder="deepseek-chat" spellcheck="false" />
          </label>

          <label class="field">
            <span class="label">Max Tokens</span>
            <input v-model.number="form.maxTokens" class="input num" type="number" min="128" max="200000" step="128" />
          </label>

          <label class="field ms-span">
            <span class="label">BaseURL</span>
            <input
              v-model="form.baseUrl"
              class="input mono"
              :placeholder="form.provider === 'deepseek' ? 'https://api.deepseek.com' : 'https://api.openai.com/v1'"
              spellcheck="false"
            />
          </label>

          <label class="field ms-span">
            <span class="label">
              API Key
              <span class="tiny muted" style="margin-left: 6px">
                {{ editing ? '已保存则留空表示不修改' : '仅写入本地，不回显' }}
              </span>
            </span>
            <input
              v-model="form.apiKey"
              class="input mono"
              type="password"
              autocomplete="off"
              placeholder="sk-…（留空不修改）"
              spellcheck="false"
            />
          </label>
        </div>

        <div v-if="formError" class="hint-warn">{{ formError }}</div>
        <div v-if="testResult && !testResult.ok" class="err-box">{{ testResult.message }}</div>
        <div v-if="testResult && testResult.ok" class="ok-box">{{ testResult.message }}</div>

        <div class="ms-actions">
          <button class="btn btn-accent" type="button" :disabled="saving || formError !== ''" @click="save(false)">
            {{ saving ? '保存中…' : '保存' }}
          </button>
          <button class="btn" type="button" :disabled="testing || saving || formError !== ''" @click="test">
            {{ testing ? '测试中…' : '测试连通' }}
          </button>
          <button class="btn" type="button" :disabled="saving || formError !== ''" @click="save(true)">保存并设为默认</button>
          <span class="spacer" />
          <span class="tiny muted">默认值来自 list 的 defaultId</span>
        </div>

        <div class="ms-note">
          <div class="tiny muted">安全约束</div>
          <ul>
            <li>密钥只写不读：页面永远拿不到明文，只能看到「已配置 / 未配置」。</li>
            <li>模型调用经 Guard 白名单与限速；超时 8 秒。</li>
            <li>本产品只做研究，不提供任何下单或交易接口。</li>
          </ul>
        </div>
      </div>
    </div>

  </section>
</template>


<style scoped>
  .ms {
    min-height: 0;
  }
  .ms-body {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: 320px 1fr;
    gap: 0;
  }
  .ms-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px;
    border-right: 1px solid var(--line);
    overflow-y: auto;
  }
  .ms-item {
    display: block;
    width: 100%;
    padding: 9px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    background: var(--panel-2);
    color: var(--text-2);
    text-align: left;
  }
  .ms-item:hover {
    border-color: var(--line-3);
    background: var(--panel-3);
  }
  .ms-item.on {
    border-color: rgba(232, 179, 75, 0.5);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .ms-prov {
    color: var(--text);
    font-size: 12.5px;
    font-weight: 600;
  }
  .ms-model {
    margin-top: 2px;
    color: var(--text-2);
    font-size: 12px;
  }
  .ms-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px 14px;
    overflow-y: auto;
  }
  .ms-form-hd {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ms-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 14px;
  }
  .ms-span {
    grid-column: 1 / -1;
  }
  .ms-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    padding-top: 4px;
  }
  .err-box {
    padding: 7px 10px;
    border: 1px solid rgba(255, 93, 93, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(255, 93, 93, 0.08);
    color: #ffb4b4;
    font-size: 12px;
  }
  .ok-box {
    padding: 7px 10px;
    border: 1px solid rgba(47, 191, 135, 0.4);
    border-radius: var(--radius-sm);
    background: rgba(47, 191, 135, 0.08);
    color: #8ae0bb;
    font-size: 12px;
  }
  .ms-note {
    margin-top: auto;
    padding-top: 10px;
    border-top: 1px solid var(--line);
  }
  .ms-note ul {
    margin: 4px 0 0;
    padding-left: 16px;
    color: var(--muted);
    font-size: 11px;
    line-height: 1.8;
  }
</style>
