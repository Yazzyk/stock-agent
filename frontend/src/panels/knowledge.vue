<script setup lang="ts">
// 知识库：检索 / 文档列表 / 技能目录 + 阅读器 + 导入。
import { computed, onMounted, ref } from 'vue'
import StateBlock from '../components/StateBlock.vue'
import {
  kbImport,
  kbList,
  kbOpen,
  kbSearch,
  messageOf,
  skill as skillDoc,
  skills as skillList,
} from '../lib/data'
import { fmtDateTime } from '../lib/format'
import { mdToHtml } from '../lib/markdown'
import { toast } from '../stores/app'
import type { DocSummary, KbHit, KnowledgeDoc, SkillContent, SkillInfo } from '../types'

type Mode = 'search' | 'docs' | 'skills'

const mode = ref<Mode>('search')
const query = ref('')
const hits = ref<KbHit[]>([])
const docs = ref<DocSummary[]>([])
const total = ref(0)
const skillItems = ref<SkillInfo[]>([])
const doc = ref<KnowledgeDoc | null>(null)
const skillContent = ref<SkillContent | null>(null)
const loading = ref(false)
const error = ref('')
const importing = ref(false)
const showImport = ref(false)
const importTitle = ref('')
const importContent = ref('')
const importTags = ref('')

const viewerHtml = computed(() => mdToHtml(doc.value?.content ?? ''))

const title = computed(() => (mode.value === 'skills' && skillContent.value ? skillContent.value.name : (doc.value?.title ?? '阅读器')))

async function runSearch(): Promise<void> {
  const q = query.value.trim()
  if (q === '') return
  loading.value = true
  error.value = ''
  try {
    hits.value = await kbSearch(q, 20)
    if (hits.value.length === 0) error.value = ''
  } catch (e) {
    hits.value = []
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadDocs(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await kbList(50)
    docs.value = res.docs
    total.value = res.total
  } catch (e) {
    docs.value = []
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadSkills(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    skillItems.value = await skillList()
  } catch (e) {
    skillItems.value = []
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function switchMode(next: Mode): Promise<void> {
  mode.value = next
  error.value = ''
  if (next === 'docs' && docs.value.length === 0) await loadDocs()
  if (next === 'skills' && skillItems.value.length === 0) await loadSkills()
}

async function openDoc(id: string): Promise<void> {
  if (id === '') return
  loading.value = true
  error.value = ''
  try {
    doc.value = await kbOpen(id)
    skillContent.value = null
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function openSkill(name: string): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    skillContent.value = await skillDoc(name)
    doc.value = null
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

async function doImport(): Promise<void> {
  if (importContent.value.trim() === '') {
    toast('正文不能为空', 'warn')
    return
  }
  importing.value = true
  try {
    const tags = importTags.value
      .split(/[,，\s]+/)
      .map((t) => t.trim())
      .filter((t) => t !== '')
    const res = await kbImport(importTitle.value.trim() || '未命名文档', importContent.value, tags)
    toast('已导入《' + res.title + '》，' + res.chars + ' 字', 'ok')
    showImport.value = false
    importTitle.value = ''
    importContent.value = ''
    importTags.value = ''
    if (mode.value === 'docs') await loadDocs()
    if (res.id) await openDoc(res.id)
  } catch (e) {
    toast(messageOf(e), 'error')
  } finally {
    importing.value = false
  }
}

function scoreText(score: number): string {
  return score > 0 ? score.toFixed(2) : '—'
}

onMounted(async () => {
  await Promise.all([loadDocs(), loadSkills()])
  await runSearchOrFirst()
})

async function runSearchOrFirst(): Promise<void> {
  if (query.value.trim() !== '') {
    await runSearch()
    return
  }
  const first = docs.value[0]
  if (first) await openDoc(first.id)
}
</script>

<template>
  <section class="card kb">
    <div class="card-hd">
      <span class="card-title">知识库</span>
      <span class="card-sub">{{ total }} 篇文档 · {{ skillItems.length }} 个技能</span>
      <span class="card-tools">
        <button class="btn btn-sm" type="button" :disabled="loading" @click="switchMode('docs'); loadDocs()">刷新</button>
        <button class="btn btn-sm btn-accent" type="button" @click="showImport = !showImport">导入</button>
      </span>
    </div>

    <div v-if="showImport" class="kb-import">
      <div class="row" style="gap: 8px">
        <input v-model="importTitle" class="input grow" placeholder="标题（可选）" aria-label="导入标题" />
        <input v-model="importTags" class="input" style="width: 180px" placeholder="标签，逗号分隔" aria-label="导入标签" />
      </div>
      <textarea v-model="importContent" class="textarea mono" rows="4" placeholder="粘贴 Markdown 正文…" aria-label="导入正文" />
      <div class="row" style="gap: 8px">
        <span class="tiny muted">正文只写本地 ~/.guanlan，不联网</span>
        <span class="spacer" />
        <button class="btn btn-sm" type="button" @click="showImport = false">取消</button>
        <button class="btn btn-sm btn-accent" type="button" :disabled="importing" @click="doImport">
          {{ importing ? '导入中…' : '确认导入' }}
        </button>
      </div>
    </div>

    <div class="kb-body">
      <div class="kb-left">
        <div class="kb-modes">
          <button class="tab" :class="{ on: mode === 'search' }" type="button" @click="switchMode('search')">检索</button>
          <button class="tab" :class="{ on: mode === 'docs' }" type="button" @click="switchMode('docs')">文档</button>
          <button class="tab" :class="{ on: mode === 'skills' }" type="button" @click="switchMode('skills')">技能</button>
        </div>

        <div v-if="mode === 'search'" class="kb-search">
          <input
            v-model="query"
            class="input"
            placeholder="搜索知识库，回车检索"
            aria-label="知识库搜索"
            @keydown.enter="runSearch"
          />
          <button class="btn btn-sm btn-accent" type="button" :disabled="loading" @click="runSearch">检索</button>
        </div>

        <div class="kb-list">
          <StateBlock v-if="loading" kind="loading" title="读取中…" />
          <StateBlock v-else-if="error" kind="error" title="操作失败" :message="error" action-text="重试" @action="runSearchOrFirst" />
          <template v-else>
            <template v-if="mode === 'search'">
              <div v-if="hits.length === 0" class="kb-hint">
                <div class="tiny muted">输入关键词后回车。示例：均线、资金流、复盘、过拟合。</div>
              </div>
              <button
                v-for="h in hits"
                :key="h.id"
                class="kb-hit"
                :class="{ on: doc?.id === h.docId }"
                type="button"
                @click="openDoc(h.docId || h.id)"
              >
                <div class="row" style="gap: 6px">
                  <span class="tag">{{ scoreText(h.score) }}</span>
                  <span class="grow ellipsis kb-hit-title">{{ h.title }}</span>
                </div>
                <div class="kb-snippet">{{ h.snippet }}</div>
              </button>
            </template>

            <template v-else-if="mode === 'docs'">
              <div v-if="docs.length === 0" class="kb-hint"><div class="tiny muted">知识库还没有文档，点右上角「导入」添加。</div></div>
              <button
                v-for="d in docs"
                :key="d.id"
                class="kb-hit"
                :class="{ on: doc?.id === d.id }"
                type="button"
                @click="openDoc(d.id)"
              >
                <div class="row" style="gap: 6px">
                  <span class="grow ellipsis kb-hit-title">{{ d.title }}</span>
                  <span class="tiny muted">{{ fmtDateTime(d.updatedAt) }}</span>
                </div>
                <div class="kb-snippet">{{ d.summary }}</div>
                <div class="row" style="gap: 4px; margin-top: 4px">
                  <span v-for="t in d.tags" :key="t" class="tag">{{ t }}</span>
                </div>
              </button>
            </template>

            <template v-else>
              <div v-if="skillItems.length === 0" class="kb-hint"><div class="tiny muted">没有可用技能。</div></div>
              <button
                v-for="s in skillItems"
                :key="s.name"
                class="kb-hit"
                :class="{ on: skillContent?.name === s.name }"
                type="button"
                @click="openSkill(s.name)"
              >
                <div class="row" style="gap: 6px">
                  <span class="tag tag-accent mono">{{ s.name }}</span>
                  <span class="spacer" />
                  <span v-for="t in s.tags" :key="t" class="tiny muted">{{ t }}</span>
                </div>
                <div class="kb-snippet">{{ s.description }}</div>
              </button>
            </template>
          </template>
        </div>
      </div>

      <div class="kb-viewer">
        <div class="kb-viewer-hd">
          <span class="card-title ellipsis">{{ title }}</span>
          <span v-if="doc" class="row" style="gap: 4px">
            <span v-for="t in doc.tags" :key="t" class="tag">{{ t }}</span>
          </span>
          <span v-if="skillContent" class="tag tag-accent">技能</span>
          <span class="spacer" />
          <span v-if="doc?.id" class="tiny muted mono">{{ doc.id }}</span>
        </div>
        <div class="kb-viewer-bd">
          <div v-if="doc" class="md" v-html="viewerHtml" />
          <div v-else-if="skillContent" class="md" v-html="mdToHtml(skillContent.content)" />
          <div v-else class="tiny muted">从左侧选择一篇文档或一个技能。</div>
          <div v-if="skillContent && skillContent.resources.length > 0" class="kb-res">
            <div class="tiny muted">资源</div>
            <div v-for="r in skillContent.resources" :key="r" class="mono tiny">{{ r }}</div>
          </div>
        </div>
      </div>
    </div>

  </section>
</template>


<style scoped>
  .kb {
    min-height: 0;
  }
  .kb-import {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--line);
    background: var(--panel-2);
  }
  .kb-body {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: 340px 1fr;
  }
  .kb-left {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border-right: 1px solid var(--line);
  }
  .kb-modes {
    display: flex;
    gap: 2px;
    padding: 0 6px;
    border-bottom: 1px solid var(--line);
  }
  .kb-search {
    display: flex;
    gap: 6px;
    padding: 8px;
    border-bottom: 1px solid var(--line);
  }
  .kb-list {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 6px;
  }
  .kb-hint {
    padding: 10px 6px;
    line-height: 1.7;
  }
  .kb-hit {
    display: block;
    width: 100%;
    padding: 7px 8px;
    margin-bottom: 4px;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    text-align: left;
  }
  .kb-hit:hover {
    background: var(--panel-2);
    border-color: var(--line);
  }
  .kb-hit.on {
    background: var(--panel-2);
    border-color: rgba(232, 179, 75, 0.35);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .kb-hit-title {
    color: var(--text);
    font-size: 12.5px;
  }
  .kb-snippet {
    margin-top: 3px;
    color: var(--muted);
    font-size: 11.5px;
    line-height: 1.6;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .kb-viewer {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
  }
  .kb-viewer-hd {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 34px;
    flex: 0 0 34px;
    padding: 0 12px;
    border-bottom: 1px solid var(--line);
    overflow: hidden;
  }
  .kb-viewer-bd {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 12px 16px;
  }
  .kb-res {
    margin-top: 14px;
    padding-top: 10px;
    border-top: 1px solid var(--line);
  }
</style>
