// Agent 域演示数据扩展（frontend-agent 维护）。
// 覆盖 §3.4 / §3.5 / §3.6 的新方法：值班模板、调度、上下文占用、编队树。
// 时间用固定规则推算（工作日 + at），不依赖定时器，刷新后仍然稳定。

import { registerMock } from './registry'

interface DemoPhase {
  phase: 'premarket' | 'intraday' | 'postmarket'
  enabled: boolean
  at?: string
  everyMinutes?: number
  window?: string
  nextRunAt?: string
  lastRunAt?: string
  lastStatus?: string
  lastSessionId?: string
}

interface DemoTemplate {
  id: string
  title: string
  description: string
  prompt: string
  defaultPrompt: string
  edited: boolean
  enabled: boolean
  /** 该 worker 的默认模型（空表示跟随全局默认） */
  model?: string
  schedule: DemoPhase
}

/** 演示模式的自定义 worker（与后端 CustomWorker 同形）。 */
interface DemoWorker {
  id: string
  title: string
  description: string
  prompt: string
  model?: string
  custom: true
}

/** 演示模式的内置 worker：顺序与后端一致（RouterAgent 第一）。 */
const BUILTIN_WORKERS = [
  { id: 'router', title: 'RouterAgent', description: '理解目标后拆解任务：分派给现有 worker，或按需创建新 worker，最后汇总结论' },
  { id: 'premarket', title: '盘前计划', description: '盘前梳理消息面、资金面与情绪周期，产出当日交易计划与观察清单' },
  { id: 'intraday', title: '盘中盯盘', description: '盯住自选与主线板块的异动、资金流向与情绪拐点，及时给出提醒' },
  { id: 'postmarket', title: '收盘复盘', description: '复盘当日涨停/炸板/连板梯队、资金主线与情绪周期位置，沉淀到知识库' },
  { id: 'quant', title: '量化研究', description: '做因子与规则的实证研究：定义、样本、统计口径与可复现结论' },
  { id: 'chat', title: '普通问答', description: '回答 A 股概念、规则、口径类问题，需要数据时先检索再回答' },
]

/** 演示模型列表：只演示 hasKey=true 的项，不含 key。 */
const DEMO_MODELS = [
  { id: 'deepseek/deepseek-chat', model: 'deepseek-chat', displayName: 'DeepSeek Chat', isDefault: true, hasKey: true },
  { id: 'qwen/qwen-max', model: 'qwen-max', displayName: '通义千问 Max', isDefault: false, hasKey: true },
]

let customWorkers: DemoWorker[] = []

function nextAt(at: string, from = Date.now()): string {
  const parts = at.split(':')
  const hh = Number(parts[0] ?? '9')
  const mm = Number(parts[1] ?? '15')
  const base = new Date(from)
  for (let i = 0; i < 9; i++) {
    const cand = new Date(base.getFullYear(), base.getMonth(), base.getDate() + i, hh, mm, 0, 0)
    const day = cand.getDay()
    if (cand.getTime() > from && day !== 0 && day !== 6) return cand.toISOString()
  }
  return ''
}

function nextIntraday(everyMinutes: number): string {
  const now = new Date()
  const next = new Date(now.getTime() + everyMinutes * 60000)
  // 收盘后顺延到下一个交易日 09:30（演示用近似，不含节假日）
  if (next.getHours() >= 15 || next.getHours() < 9) {
    const tomorrow = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 9, 30, 0, 0)
    while (tomorrow.getDay() === 0 || tomorrow.getDay() === 6) tomorrow.setDate(tomorrow.getDate() + 1)
    return tomorrow.toISOString()
  }
  return next.toISOString()
}

const DEFAULT_PROMPTS: Array<{
  id: string
  title: string
  description: string
  prompt: string
}> = [
  {
    id: 'router',
    title: 'RouterAgent',
    description: '理解目标后拆解任务：分派给现有 worker，或按需创建新 worker，最后汇总结论',
    prompt:
      '你是 RouterAgent（调度智能体），是用户对话的默认入口。\n' +
      '1) 简单概念问题直接回答；\n' +
      '2) 需要行情/复盘/盘前/量化时，用 agent.spawn 派给合适的 worker；\n' +
      '3) 没有合适的 worker 时，先 worker.create 创建再 spawn；\n' +
      '4) 最后把子报告汇总成一份结论：谁做了什么、依据是什么、还有哪些不确定。',
  },
  {
    id: 'premarket',
    title: '盘前计划',
    description: '开盘前把自选与板块过一遍，给今天的观察清单',
    prompt:
      '你是盘前研究助理。开盘前完成以下工作：\n' +
      '1) 读取自选列表，按涨跌幅与量能排序，标出昨日放量与缩量的标的；\n' +
      '2) 查一查自选所属板块昨日资金净流入排名（flow.rank）；\n' +
      '3) 给出一份「今日观察清单」：每只标的写关注理由、关键价位（前高/前低/MA20）、需要回避的情况；\n' +
      '4) 最后用 3 条写清今天的风险点。\n' +
      '只做研究描述，不给买卖建议，不预测点位。',
  },
  {
    id: 'intraday',
    title: '盘中',
    description: '交易时段定时巡检，发现异动再展开',
    prompt:
      '你是盘中跟踪助理。这次巡检请：\n' +
      '1) 拉取自选最新行情，找出涨跌幅超过 3% 或量比超过 2 的标的；\n' +
      '2) 对异动标的查一次资金流，说明是主力净流入还是流出；\n' +
      '3) 若没有异动，用一段话说明「无异动，量能平稳」即可，不要凑字数；\n' +
      '4) 有异动才展开分析，最多 3 只。\n' +
      '输出用短句，先结论后依据。',
  },
  {
    id: 'postmarket',
    title: '复盘',
    description: '收盘后对照盘前计划，记录偏差',
    prompt:
      '你是复盘助理。收盘后请：\n' +
      '1) 对照今早的观察清单，逐条写「计划 / 实际 / 偏差原因」；\n' +
      '2) 统计自选涨跌分布、领涨领跌板块与主力资金方向；\n' +
      '3) 指出今天判断错在哪里（不要只写对的）；\n' +
      '4) 给出明天要重点跟踪的 2~3 个变量。\n' +
      '复盘只做归因，不做荐股。',
  },
  {
    id: 'quant',
    title: '量化',
    description: 'K 线形态与参数研究',
    prompt:
      '你是量化研究助理。针对给定标的与策略：\n' +
      '1) 说明回测区间、样本量与复权口径；\n' +
      '2) 报告收益、回撤、夏普与换手，并指出是否存在过拟合迹象；\n' +
      '3) 若结果不稳健，明确说明「结论不可用」，不要包装。',
  },
  {
    id: 'chat',
    title: '对话',
    description: '自由研究，用户提问后运行',
    prompt: '你是 A 股研究助理。回答用户问题时先用工具取数，再给结论；只做研究描述，不给买卖建议。',
  },
]

/** 演示用的三个值班时段（只有这三个 id 参与调度）。 */
const DUTY_IDS = ['premarket', 'intraday', 'postmarket']

/** 构造内置模板；router/quant/chat 没有调度（schedule.enabled=false）。 */
function buildTemplates(): DemoTemplate[] {
  return DEFAULT_PROMPTS.map((d) => {
    const duty = DUTY_IDS.includes(d.id)
    const phase = duty ? (d.id as DemoPhase['phase']) : null
    const at = phase === 'premarket' ? '09:15' : phase === 'postmarket' ? '15:10' : ''
    const schedule: DemoPhase = {
      phase: phase ?? 'premarket',
      enabled: phase !== null,
      at: at !== '' ? at : undefined,
      everyMinutes: phase === 'intraday' ? 30 : undefined,
      window: phase === 'intraday' ? '09:30-11:30,13:00-15:00' : undefined,
      nextRunAt: phase === 'premarket' ? nextAt('09:15') : phase === 'intraday' ? nextIntraday(30) : phase === 'postmarket' ? nextAt('15:10') : undefined,
      lastRunAt: phase === 'premarket' ? new Date(Date.now() - 3600 * 1000).toISOString() : phase === 'postmarket' ? new Date(Date.now() - 20 * 3600 * 1000).toISOString() : undefined,
      lastStatus: phase === 'premarket' ? 'ok' : phase === 'intraday' ? 'ok' : phase === 'postmarket' ? '未配置模型' : undefined,
      lastSessionId: phase === 'premarket' ? 's-20261110-0930' : undefined,
    }
    return {
      id: d.id,
      title: d.title,
      description: d.description,
      prompt: d.prompt,
      defaultPrompt: d.prompt,
      edited: false,
      enabled: phase !== null,
      schedule,
    }
  })
}

let templates = buildTemplates()

/** 演示调度只包含三个值班时段。 */
function phases(): DemoPhase[] {
  return templates.filter((t) => DUTY_IDS.includes(t.id)).map((t) => t.schedule)
}

function refreshNextRun(phase: DemoPhase): void {
  if (phase.phase === 'premarket') phase.nextRunAt = nextAt(phase.at || '09:15')
  else if (phase.phase === 'postmarket') phase.nextRunAt = nextAt(phase.at || '15:10')
  else phase.nextRunAt = nextIntraday(phase.everyMinutes || 30)
}

function findTemplate(id: unknown): DemoTemplate | null {
  return templates.find((t) => t.id === id) ?? null
}

// ---------- 上下文 / 编队演示 ----------

const FENCE = String.fromCharCode(96).repeat(3)

const DEMO_WINDOW = 64000
let ctxTick = 0

/**
 * 演示用子智能体：主控一轮任命两个后台 worker，各自产出自己的过程报告。
 * report 是「子智能体产出」的正文：真实后端会把它们作为 agent.report 发出来，
 * 但 sessionId 是子会话（≠ 根会话），所以前端只把它们当过程证据，不进报告册（交付物）。
 */
const DEMO_CHILDREN = [
  {
    id: 's-demo-child-1',
    label: '半导体资金筛选',
    worker: 'quant',
    report:
      '# 半导体资金筛选 · 子智能体产出\n\n' +
      '- 半导体主力净流入 28.4 亿，连续 3 个交易日为正\n' +
      '- 设备（688012）量比 2.4，站上 MA20\n' +
      '- 存储（603986）昨日冲高回落，换手 18%，高位分歧明显\n\n' +
      '> 这是子智能体的过程报告：只作证据，不当作交付物本身。\n',
  },
  {
    id: 's-demo-child-2',
    label: '白酒板块核对',
    worker: 'chat',
    report:
      '# 白酒板块核对 · 子智能体产出\n\n' +
      '- 白酒主力净流出 6.3 亿，近 5 日累计流出 21 亿\n' +
      '- 600519 昨日缩量，量能只有 5 日均量的 0.72\n' +
      '- 结论：白酒暂时不是资金主线，只做观察\n\n' +
      '> 这是子智能体的过程报告：只作证据，不当作交付物本身。\n',
  },
]

/** 每两次 context 轮询推一条 agent.child，演示编队实时更新（无后台定时器）。 */
const CHILD_SCRIPT: Array<{ at: number; index: number; status: 'running' | 'ok'; tokens: number }> = [
  { at: 2, index: 0, status: 'running', tokens: 1200 },
  { at: 3, index: 0, status: 'ok', tokens: 8600 },
  { at: 4, index: 1, status: 'running', tokens: 900 },
  { at: 5, index: 1, status: 'ok', tokens: 5400 },
]

// ---------- 流式演示：agent.tool 事件 ----------

/** 演示用的工具脚本：7 次调用（含 1 次失败），覆盖 tool.call / tool.search 两种元工具。 */
interface DemoTool {
  tool: string
  meta: string
  args: string
  result: string
  ok: boolean
  error: string
  /** 真实耗时（毫秒），用于 start/end 之间的「运行中」窗口 */
  ms: number
}

const DEMO_TOOLS: DemoTool[] = [
  {
    tool: 'market.quote',
    meta: 'tool.call',
    args: '{"codes":["600519","300750","601318"]}',
    result: '600519 1504.70 (+0.82%)\n300750 246.18 (+3.47%)\n601318 48.62 (-1.06%)',
    ok: true,
    error: '',
    ms: 214,
  },
  {
    tool: 'flow.rank',
    meta: 'tool.call',
    args: '{"kind":"sector","days":1}',
    result: '半导体 +28.4 亿\n电池 +12.1 亿\n白酒 -6.3 亿',
    ok: true,
    error: '',
    ms: 412,
  },
  {
    tool: 'flow.stock',
    meta: 'tool.call',
    args: '{"code":"300750"}',
    result: '主力净流入 1.42 亿（超大单 0.86 亿）',
    ok: true,
    error: '',
    ms: 186,
  },
  {
    tool: 'kline.analyze',
    meta: 'tool.call',
    args: '{"code":"300750","period":"day"}',
    result: '放量突破前高，收于 MA20 上方',
    ok: true,
    error: '',
    ms: 498,
  },
  {
    tool: 'news.search',
    meta: 'tool.search',
    args: '{"keyword":"半导体 减持","limit":5}',
    result: '',
    ok: false,
    error: 'news.search: upstream timeout（>12s）',
    ms: 12012,
  },
  {
    tool: 'watchdog.list',
    meta: 'tool.call',
    args: '{}',
    result: '3 条规则，1 条今日触发',
    ok: true,
    error: '',
    ms: 96,
  },
  {
    tool: 'kline.analyze',
    meta: 'tool.call',
    args: '{"code":"600519","period":"day"}',
    result: 'MA20 1490.2 支撑有效',
    ok: true,
    error: '',
    ms: 342,
  },
]

/** 已经跑过脚本的 sessionId（每次新运行只跑一次）。 */
let toolScriptSession = ''
let toolTimers: number[] = []

function clearToolScript(): void {
  for (const t of toolTimers) window.clearTimeout(t)
  toolTimers = []
  toolScriptSession = ''
}

/**
 * 按时间顺序抛出 start/end 成对的 agent.tool 事件：
 * start 立刻建行（运行中），end 在真实耗时后合并结果；失败的那条会带 error 与 ok=false。
 */
function startToolScript(sessionId: string, ctx: { emit(topic: string, payload: Record<string, unknown>): void }): void {
  if (toolScriptSession === sessionId) return
  clearToolScript()
  toolScriptSession = sessionId
  DEMO_TOOLS.forEach((t, i) => {
    const id = 'demo-tool-' + i
    const step = i + 1
    const startAt = 320 + i * 620
    const endAt = startAt + Math.min(t.ms, 720)
    toolTimers.push(
      window.setTimeout(() => {
        ctx.emit('agent.tool', {
          sessionId,
          worker: 'router',
          id,
          phase: 'start',
          tool: t.tool,
          meta: t.meta,
          args: t.args,
          step,
          at: new Date().toISOString(),
        })
      }, startAt),
    )
    toolTimers.push(
      window.setTimeout(() => {
        ctx.emit('agent.tool', {
          sessionId,
          worker: 'router',
          id,
          phase: 'end',
          tool: t.tool,
          meta: t.meta,
          result: t.result,
          ok: t.ok,
          error: t.error,
          ms: t.ms,
          step,
          at: new Date().toISOString(),
        })
      }, endAt),
    )
  })
}

// ---------- 历史会话：多份报告 + 工具配对 ----------

interface DemoEvent {
  seq: number
  time: string
  kind: string
  role?: string
  content: string
  toolName?: string
  ok?: boolean
}

interface DemoDetail {
  session: {
    id: string
    worker: string
    title: string
    startedAt: string
    status: 'ok' | 'failed' | 'running'
    tokens?: number
  }
  events: DemoEvent[]
}

/** 演示会话 1：盘前。3 份报告（2 份子报告 + 1 份 RouterAgent 汇总），7 次工具调用里有 1 次失败。 */
const SESSION_PREMARKET: DemoDetail = {
  session: {
    id: 's-20261110-0930',
    worker: 'premarket',
    title: '盘前计划 · 2026-11-10',
    startedAt: '2026-11-10T09:30:12+08:00',
    status: 'ok',
    tokens: 18240,
  },
  events: [
    { seq: 1, time: '09:30:03', kind: 'user', role: 'user', content: '今天盘前给我一份观察清单，重点看半导体和白酒，把资金与量能一起过一遍。' },
    { seq: 2, time: '09:30:05', kind: 'state', content: '开始读取自选与板块资金' },
    { seq: 30, time: '09:30:06', kind: 'assistant', content: '先过一遍自选和板块资金，再决定今天的观察清单。' },
    { seq: 3, time: '09:30:06', kind: 'tool', role: 'assistant', toolName: 'market.quote', content: '{"codes":["600519","300750","601318"]}' },
    { seq: 4, time: '09:30:07', kind: 'tool', role: 'tool', toolName: 'market.quote', ok: true, content: '600519 1504.70 (+1.24%)\n300750 246.18 (+3.47%)\n601318 48.62 (-1.06%)' },
    { seq: 5, time: '09:30:12', kind: 'tool', role: 'assistant', toolName: 'flow.rank', content: '{"kind":"sector","days":1}' },
    { seq: 6, time: '09:30:13', kind: 'tool', role: 'tool', toolName: 'flow.rank', ok: true, content: '半导体 +28.4 亿\n电池 +12.1 亿\n白酒 -6.3 亿' },
    {
      seq: 31,
      time: '09:30:15',
      kind: 'assistant',
      content:
        '半导体资金连续三日净流入，设备端最强；白酒净流出，暂时不作为进攻方向。\n\n先用代码核对关键位：\n\n' +
        FENCE +
        'text\n600519  MA20 1490.2  MA60 1436.8\n300750  MA20 232.6   MA60 218.4\n' +
        FENCE +
        '\n\n接下来补一条新闻面，看有没有减持或业绩预告。',
    },
    { seq: 7, time: '09:30:19', kind: 'tool', role: 'assistant', toolName: 'news.search', content: '{"keyword":"半导体 减持","limit":5}' },
    { seq: 8, time: '09:30:31', kind: 'tool', role: 'tool', toolName: 'news.search', ok: false, content: 'news.search: upstream timeout（>12s）' },
    { seq: 9, time: '09:30:34', kind: 'tool', role: 'assistant', toolName: 'kline.analyze', content: '{"code":"300750","period":"day"}' },
    { seq: 10, time: '09:30:35', kind: 'tool', role: 'tool', toolName: 'kline.analyze', ok: true, content: '放量突破前高，收于 MA20 上方' },
    { seq: 11, time: '09:30:40', kind: 'state', content: '分派子任务：半导体资金筛选 / 白酒板块核对' },
    {
      seq: 12,
      time: '09:30:52',
      kind: 'report',
      role: 'quant',
      content:
        '# 半导体资金筛选 · 子报告\n\n' +
        '- 半导体主力净流入 28.4 亿，连续 3 个交易日为正\n' +
        '- 半导体设备（688012）量比 2.4，收盘价站上 MA20\n' +
        '- 存储（603986）昨日冲高回落，换手 18%，高位分歧明显\n\n' +
        '## 观察名单\n\n| 标的 | 逻辑 | 关键位 |\n|---|---|---|\n| 688012 | 设备主线放量 | 前高 62.4 |\n| 603986 | 存储涨价预期 | MA20 88.6 |\n',
    },
    {
      seq: 13,
      time: '09:31:04',
      kind: 'report',
      role: 'chat',
      content:
        '# 白酒板块核对 · 子报告\n\n- 白酒板块主力净流出 6.3 亿，近 5 日累计流出 21 亿\n- 600519 昨日缩量，量能只有 5 日均量的 0.72\n- 结论：白酒暂时不是资金主线，只做观察不做重点\n',
    },
    {
      seq: 32,
      time: '09:31:16',
      kind: 'assistant',
      content: '两份子报告都回来了：半导体是今天的主线，白酒只做观察。我把结论汇总成一份盘前计划。',
    },
    {
      seq: 14,
      time: '09:31:18',
      kind: 'report',
      role: 'router',
      content:
        '# 盘前计划 2026-11-10\n\n## 一、今日主线\n\n1. **半导体**：资金连续 3 日流入，设备与存储是两条腿\n2. **电池**：300750 放量突破，量比 2.13\n3. 白酒弱，资金流出，不做进攻方向\n\n## 二、观察清单\n\n| 标的 | 方向 | 触发 | 止损 | 仓位 |\n|---|---|---|---|---|\n| 600519 | 观察 | 回踩 1490 不破 | 1472 | 0 |\n| 300750 | 持有 | — | 238.5 | 15% |\n| 688012 | 观察 | 站稳 62.4 | 59.8 | 0 |\n\n## 三、风险\n\n- 指数在 3400 上方震荡，量能未有效放大\n- 半导体高位分歧，注意冲高回落\n\n' +
        FENCE +
        'text\n600519  MA20 1490.2  MA60 1436.8\n300750  MA20 232.6   MA60 218.4\n' +
        FENCE +
        '\n\n> 只做研究描述，不构成投资建议。\n',
    },
  ],
}

/** 演示会话 2：复盘。工具配对中故意缺一次结果（验证「没有配对也要显示」）。 */
const SESSION_POSTMARKET: DemoDetail = {
  session: {
    id: 's-20261107-1505',
    worker: 'postmarket',
    title: '盘后复盘 · 2026-11-07',
    startedAt: '2026-11-07T15:05:40+08:00',
    status: 'ok',
    tokens: 12060,
  },
  events: [
    { seq: 1, time: '15:05:41', kind: 'user', role: 'user', content: '复盘今天：对照早上的计划写偏差归因，明天要跟踪什么。' },
    { seq: 2, time: '15:05:43', kind: 'tool', role: 'assistant', toolName: 'watch.list', content: '{}' },
    { seq: 3, time: '15:05:44', kind: 'tool', role: 'tool', toolName: 'watch.list', ok: true, content: '3 条规则，2 条触发' },
    { seq: 4, time: '15:05:50', kind: 'tool', role: 'assistant', toolName: 'flow.stock', content: '{"code":"300750"}' },
    { seq: 5, time: '15:06:02', kind: 'tool', role: 'tool', toolName: 'flow.stock', ok: true, content: '主力净流入 1.42 亿' },
    { seq: 20, time: '15:06:05', kind: 'assistant', content: '300750 的计划与实际一致；601318 仍是弱震荡。先把偏差写清楚，再说明天的变量。' },
    { seq: 6, time: '15:06:09', kind: 'tool', role: 'assistant', toolName: 'kline.analyze', content: '{"code":"601318","period":"day"}' },
    {
      seq: 7,
      time: '15:06:30',
      kind: 'report',
      role: 'postmarket',
      content:
        '# 复盘 2026-11-07\n\n- 计划中的 300750 突破成立，实际最高 247.2\n- 601318 未触发止损，继续观察\n- 偏差原因：半导体板块资金流入超预期\n\n## 明日关注\n\n1. 指数能否放量站上 3420\n2. 白酒板块资金是否回流\n3. 半导体的高位分歧是否扩散\n',
    },
  ],
}

/** 演示会话 3：量化，失败态（报告仍保留，告警卡在报告页顶部）。 */
const SESSION_QUANT: DemoDetail = {
  session: {
    id: 's-20261107-1012',
    worker: 'quant',
    title: '量化 · MA 参数网格',
    startedAt: '2026-11-07T10:12:03+08:00',
    status: 'failed',
    tokens: 8410,
  },
  events: [
    { seq: 1, time: '10:12:04', kind: 'user', role: 'user', content: '跑一下 MA 参数网格，样本内 70% / 样本外 30%。' },
    { seq: 2, time: '10:12:06', kind: 'tool', role: 'assistant', toolName: 'quant.optimize', content: '{"folds":3,"strategy":"ma_cross"}' },
    { seq: 3, time: '10:12:09', kind: 'tool', role: 'tool', toolName: 'quant.optimize', ok: true, content: '3 折完成，样本外衰减 61%' },
    { seq: 20, time: '10:12:12', kind: 'assistant', content: '样本外衰减 61%，先不急着下结论。把三组参数的对齐方式核对完再写报告。' },
    {
      seq: 4,
      time: '10:12:30',
      kind: 'report',
      role: 'quant',
      content:
        '# 量化验证 · MA 参数网格\n\n' +
        FENCE +
        'text\n参数       样本内收益   样本外收益\nMA 5/20    +34.2%      +10.8%\nMA 10/30   +28.6%      +12.4%\nMA 20/60   +19.1%      +14.2%\n' +
        FENCE +
        '\n\n样本外衰减 61%，判定过拟合，参数不可用。\n',
    },
  ],
}

/** 编队树里的根会话复用了盘前那一份事件流（演示用：点开根节点能看到真实的两份子报告）。 */
const SESSION_ROOT: DemoDetail = {
  ...SESSION_PREMARKET,
  session: { ...SESSION_PREMARKET.session, id: 's-demo-root', title: '盘前值班 · 演示编排' },
}

const DEMO_SESSIONS: Record<string, DemoDetail> = {
  's-20261110-0930': SESSION_PREMARKET,
  's-20261107-1505': SESSION_POSTMARKET,
  's-20261107-1012': SESSION_QUANT,
  's-demo-root': SESSION_ROOT,
}

/**
 * 未知会话（流式演示现场产生、没落盘）：返回明确的说明而不是空事件流，
 * 免得点开历史会话后对话/轨迹突然变空白。
 */
function genericDetail(id: string): DemoDetail {
  return {
    session: { id, worker: 'router', title: id, startedAt: new Date().toISOString(), status: 'ok', tokens: 0 },
    events: [
      { seq: 1, time: '', kind: 'state', content: '演示模式：这次运行的事件流没有落盘' },
      {
        seq: 2,
        time: '',
        kind: 'report',
        role: 'router',
        content:
          '# 会话 ' + id + '\n\n> 演示模式只为三条历史会话保留了完整事件流与多份报告；\n> 这次运行的内容已在运行过程中展示过。\n',
      },
    ],
  }
}

// 覆盖 api.mock 里的单报告 session 实现：历史会话要能返回多份报告与成对的工具事件
/**
 * 子智能体会话详情：编队节点详情要能看到它自己的过程报告（信息不丢失的入口）。
 * 与真实后端一致：report 事件落在子会话自己的事件流里，不会出现在父会话的交付物里。
 */
function childDetail(child: (typeof DEMO_CHILDREN)[number]): DemoDetail {
  return {
    session: {
      id: child.id,
      worker: child.worker,
      title: child.label,
      startedAt: new Date(Date.now() - 60000).toISOString(),
      status: 'ok',
      tokens: 0,
    },
    events: [
      { seq: 1, time: '', kind: 'user', role: 'user', content: '（主控派发的子任务）' + child.label },
      { seq: 2, time: '', kind: 'assistant', content: '收到子任务，先取数据再下结论。' },
      { seq: 3, time: '', kind: 'report', role: child.worker, content: child.report },
    ],
  }
}

registerMock('agentlab', 'session', (p) => {
  const id = String(p.sessionId ?? '')
  // 子智能体：返回它自己的事件流（编队节点详情 / 子会话入口用）
  const child = DEMO_CHILDREN.find((c) => c.id === id)
  if (child) return childDetail(child)
  return DEMO_SESSIONS[id] ?? genericDetail(id || 's-demo-root')
})

registerMock('agentlab', 'templates', () => ({ templates }))

registerMock('agentlab', 'saveTemplate', (p) => {
  const t = findTemplate(p.id)
  if (!t) throw new Error('模板不存在：' + String(p.id ?? ''))
  const prompt = String(p.prompt ?? '').trim()
  if (prompt.length < 20) throw new Error('提示词至少 20 字（当前 ' + prompt.length + ' 字）')
  if (prompt.length > 8000) throw new Error('提示词最多 8000 字（当前 ' + prompt.length + ' 字）')
  t.prompt = prompt
  t.edited = prompt !== t.defaultPrompt
  if (typeof p.model === 'string') t.model = p.model.trim() === '' ? undefined : p.model.trim()
  if (typeof p.enabled === 'boolean') {
    t.enabled = p.enabled
    t.schedule.enabled = p.enabled
    refreshNextRun(t.schedule)
  }
  return { templates }
})

registerMock('agentlab', 'resetTemplate', (p) => {
  const t = findTemplate(p.id)
  if (!t) throw new Error('模板不存在：' + String(p.id ?? ''))
  t.prompt = t.defaultPrompt
  t.edited = false
  return { templates }
})

registerMock('agentlab', 'schedule', () => ({ phases: phases() }))

registerMock('agentlab', 'setSchedule', (p, ctx) => {
  const phase = phases().find((s) => s.phase === p.phase)
  if (!phase) throw new Error('未知时段：' + String(p.phase ?? ''))
  if (typeof p.enabled === 'boolean') phase.enabled = p.enabled
  if (typeof p.at === 'string' && p.at !== '') phase.at = p.at
  if (typeof p.everyMinutes === 'number' && p.everyMinutes > 0) phase.everyMinutes = p.everyMinutes
  if (typeof p.window === 'string' && p.window !== '') phase.window = p.window
  refreshNextRun(phase)
  // 与真实后端一致：改完调度发一条全局 agent.schedule 事件（前端整体刷新）
  ctx.emit('agent.schedule', { running: false, reason: '调度已保存（演示）', phases: phases() })
  return { phases: phases() }
})

registerMock('agentlab', 'context', (p, ctx) => {
  ctxTick += 1
  const sessionId = String(p.sessionId ?? 's-demo-root')
  // 演示用：一次新的流式运行（sessionId 是新生成的）会按时间顺序补 7 次工具调用事件，
  // 其中 1 次失败；轨迹/对话因此能在无后端时看到完整的工具行与合并逻辑。
  const rawSid = String(p.sessionId ?? '')
  if (rawSid.startsWith('s-demo-') && rawSid !== 's-demo-root') startToolScript(rawSid, ctx)
  // 演示用：缓慢爬升并封顶在 4.6 万，避免长时间挂机把占比刷到 100%
  const used = Math.min(46000, 24800 + ctxTick * 400)
  const steps = Math.min(8, 3 + ctxTick)
  // 提供方精确口径：最近一次请求的 prompt 比启发式估算略大（CJK/JSON schema 被低估），
  // 两者的差就是面板上「精确占用」与「估算分项」不对账的原因。
  const pressure = Math.min(58000, Math.round(used * 1.08) + 1200)
  // 逐轮台账：按 step 生成一串递增采样（最后一条 ≈ 当前 prompt），
  // 会话累计 usage / cache 由这些采样累加，保证「台账合计 = 会话累计」。
  const startedAt = Date.now() - steps * 24000
  const history: Array<{
    step: number
    promptTokens: number
    cachedTokens: number
    completionTokens: number
    used: number
    at: string
  }> = []
  let promptSum = 0
  let cachedSum = 0
  let completionSum = 0
  for (let i = 1; i <= steps; i++) {
    const prompt = Math.round(pressure * (0.45 + 0.55 * (i / steps)))
    const cached = Math.round(prompt * 0.72)
    const completion = 420 + i * 90
    promptSum += prompt
    cachedSum += cached
    completionSum += completion
    history.push({
      step: i,
      promptTokens: prompt,
      cachedTokens: cached,
      completionTokens: completion,
      used: prompt,
      at: new Date(startedAt + i * 24000).toISOString(),
    })
  }
  // 子智能体脚本只在「流式演示会话」里跑：浏览历史会话时不能凭空长出子节点或子报告。
  const live = sessionId.startsWith('s-demo-') && sessionId !== 's-demo-root'
  const script = live ? CHILD_SCRIPT.find((s) => s.at === ctxTick) : undefined
  if (script) {
    const child = DEMO_CHILDREN[script.index]
    if (child) {
      const at = new Date().toISOString()
      ctx.emit('agent.child', {
        parentSessionId: sessionId,
        childSessionId: child.id,
        label: child.label,
        worker: child.worker,
        status: script.status,
        phase: script.status === 'ok' ? 'finished' : 'started',
        tokens: script.tokens,
        elapsedMs: script.status === 'ok' ? 42000 + script.tokens : 12000,
        // 与后端一致：带毫秒时间戳，前端据此丢弃重放的旧事件
        at,
        atMs: Date.now(),
        startedAt: at,
      })
      // 子智能体完成：像真实后端一样发一条带**子会话 sessionId** 的 agent.report。
      // 验证点：它只能进 childReports（编队 / 轨迹），绝不能让报告册多出一份。
      if (script.status === 'ok') {
        ctx.emit('agent.report', {
          sessionId: child.id,
          worker: child.worker,
          report: child.report,
          usage: { promptTokens: 0, completionTokens: 0, totalTokens: script.tokens },
        })
      }
    }
  }
  return {
    sessionId,
    model: 'deepseek-flash',
    contextWindow: DEMO_WINDOW,
    usedTokens: used,
    // 与后端一致：百分比按「有 pressure 用 pressure」的同一口径取整
    percent: Math.round((pressure / DEMO_WINDOW) * 100),
    pressureTokens: pressure,
    sample: 'provider',
    breakdown: {
      systemTokens: 3200,
      toolsTokens: 9800,
      messageTokens: used - 13000 > 0 ? used - 13000 : 1200,
    },
    cache: {
      uncachedInputTokens: promptSum - cachedSum,
      cacheReadTokens: cachedSum,
      outputTokens: completionSum,
      hitPercent: Math.round((cachedSum / promptSum) * 100),
    },
    usage: {
      promptTokens: promptSum,
      cachedTokens: cachedSum,
      completionTokens: completionSum,
      totalTokens: promptSum + completionSum,
    },
    history,
    // 演示用：这几个计数也封顶，避免空闲轮询把「消息/工具/步数」刷成三位数
    messages: Math.min(12, 6 + ctxTick),
    steps,
    toolCalls: Math.min(6, 2 + ctxTick),
    updatedAt: new Date().toISOString(),
    note: '估算口径：中文≈1 字/1 token，英文≈4 字符/1 token；工具只计 L1 索引（名称+摘要+标签），不含 schema。',
  }
})

registerMock('agentlab', 'tree', (p) => {
  const sessionId = String(p.sessionId ?? '') || 's-demo-root'
  const startedAt = new Date(Date.now() - 96000).toISOString()

  /** 单根节点（没有派生子智能体）：编队页会渲染成一张 RouterAgent 卡。 */
  const singleRoot = (worker: string, label: string, status: 'ok' | 'failed' | 'running', tokens: number, steps: number) => {
    const root = {
      sessionId,
      parentId: '',
      depth: 0,
      childCount: 0,
      label,
      role: label,
      mode: 'main' as const,
      worker,
      status,
      tokens,
      steps,
      startedAt,
      endedAt: status === 'running' ? undefined : new Date().toISOString(),
      elapsedMs: 96000,
    }
    return {
      root,
      nodes: [root],
      rootSessionId: sessionId,
      sessionId,
      total: 1,
      activeCount: status === 'running' ? 1 : 0,
      updatedAt: new Date().toISOString(),
    }
  }

  // 历史会话：量化那一条是失败的单线程运行；复盘也是单线程。
  if (sessionId === 's-20261107-1012') return singleRoot('quant', '量化 · MA 参数网格', 'failed', 8410, 3)
  if (sessionId === 's-20261107-1505') return singleRoot('postmarket', '盘后复盘', 'ok', 12060, 4)
  // 流式演示现场产生的会话（点「运行」新建的）：一次双工编排——RouterAgent 前台 + 1 个后台 worker。
  if (sessionId.startsWith('s-demo-') && sessionId !== 's-demo-root') {
    const root = {
      sessionId,
      parentId: '',
      depth: 0,
      childCount: DEMO_CHILDREN.length,
      label: 'RouterAgent',
      role: 'RouterAgent',
      mode: 'main' as const,
      worker: 'router',
      status: 'ok' as const,
      tokens: 27200,
      steps: 8,
      startedAt,
      endedAt: new Date().toISOString(),
      elapsedMs: 96000,
    }
    // 与 CHILD_SCRIPT 用同一批子会话 id：流式演示里主控派发 2 个子智能体，各自有过程报告
    const children = DEMO_CHILDREN.map((child, i) => ({
      sessionId: child.id,
      parentId: sessionId,
      depth: 1,
      childCount: 0,
      label: child.label,
      role: child.label,
      mode: 'subagent' as const,
      worker: child.worker,
      status: 'ok' as const,
      tokens: i === 0 ? 6120 : 5400,
      steps: 3,
      startedAt: new Date(Date.now() - (80000 - i * 20000)).toISOString(),
      endedAt: new Date().toISOString(),
      elapsedMs: i === 0 ? 42000 : 26000,
    }))
    return {
      root,
      nodes: [root, ...children],
      rootSessionId: sessionId,
      sessionId,
      total: children.length + 1,
      activeCount: 0,
      updatedAt: new Date().toISOString(),
    }
  }

  // 盘前值班与 s-demo-root：一次真实的父子编排（2 个子智能体，全部完成）。
  const rootStatus: 'ok' = 'ok'
  const root = {
    sessionId,
    parentId: '',
    depth: 0,
    childCount: 2,
    label: '盘前值班',
    role: '盘前值班',
    mode: 'main' as const,
    worker: 'premarket',
    status: rootStatus,
    tokens: 18240,
    steps: 7,
    startedAt,
    endedAt: new Date(Date.now() - 20000).toISOString(),
    elapsedMs: 96000,
  }
  const nodes = [
    root,
    {
      sessionId: 's-demo-child-1',
      parentId: sessionId,
      depth: 1,
      childCount: 0,
      label: '半导体资金筛选',
      role: '半导体资金筛选',
      mode: 'subagent' as const,
      worker: 'quant',
      status: 'ok' as const,
      tokens: 8600,
      steps: 4,
      startedAt: new Date(Date.now() - 72000).toISOString(),
      endedAt: new Date(Date.now() - 30000).toISOString(),
      elapsedMs: 42000,
    },
    {
      sessionId: 's-demo-child-2',
      parentId: sessionId,
      depth: 1,
      childCount: 0,
      label: '白酒板块核对',
      role: '白酒板块核对',
      mode: 'subagent' as const,
      worker: 'chat',
      status: 'ok' as const,
      tokens: 5400,
      steps: 3,
      startedAt: new Date(Date.now() - 24000).toISOString(),
      endedAt: new Date(Date.now() - 12000).toISOString(),
      elapsedMs: 24000,
    },
  ]
  return {
    root,
    nodes,
    rootSessionId: sessionId,
    sessionId,
    total: nodes.length,
    activeCount: 0,
    updatedAt: new Date().toISOString(),
  }
})

/** 合并内置与自定义 worker（RouterAgent 第一），并带上模板里保存的模型。 */
/**
 * 演示 worker 列表：与真实后端同形状（对象 + kind），
 * router 是 main，BUILTIN_WORKERS 里其余预设计 preset，用户新建的算 custom。
 */
function workerViews(): {
  workers: Array<{
    id: string
    title: string
    description: string
    kind: 'main' | 'preset' | 'custom'
    main: boolean
    preset: boolean
    custom: boolean
    model?: string
  }>
  mainId: string
  total: number
  presets: number
  customs: number
} {
  const modelOf = (id: string): string | undefined => templates.find((t) => t.id === id)?.model
  const builtin = BUILTIN_WORKERS.map((w) => {
    const isMain = w.id === 'router'
    return {
      ...w,
      kind: (isMain ? 'main' : 'preset') as 'main' | 'preset',
      main: isMain,
      preset: !isMain,
      custom: false,
      model: modelOf(w.id),
    }
  })
  const custom = customWorkers.map((w) => ({
    id: w.id,
    title: w.title,
    description: w.description,
    kind: 'custom' as const,
    main: false,
    preset: false,
    custom: true,
    model: w.model ?? modelOf(w.id),
  }))
  const workers = [...builtin, ...custom]
  return {
    workers,
    mainId: 'router',
    total: workers.length,
    presets: builtin.length - 1,
    customs: custom.length,
  }
}

registerMock('agentlab', 'workers', () => workerViews())

registerMock('agentlab', 'models', () => ({
  models: DEMO_MODELS,
  defaultId: DEMO_MODELS.find((m) => m.isDefault)?.id ?? DEMO_MODELS[0]?.id ?? '',
}))

registerMock('agentlab', 'createWorker', (p) => {
  const id = String(p.id ?? '').trim()
  if (!/^[a-z][a-z0-9_-]{1,31}$/.test(id)) throw new Error('worker id 不合法：需小写字母开头，后续仅小写字母/数字/下划线/中划线，长度 2~32')
  if (BUILTIN_WORKERS.some((w) => w.id === id)) throw new Error('worker id 与内置 worker 冲突，请换一个')
  if (customWorkers.some((w) => w.id === id)) throw new Error('worker id ' + id + ' 已存在，请换一个')
  if (customWorkers.length >= 20) throw new Error('自定义 worker 已达上限 20 个，请先删除不用的再创建')
  const title = String(p.title ?? '').trim()
  if (title.length === 0 || [...title].length > 40) throw new Error('worker 标题需 1~40 字')
  const description = String(p.description ?? '').trim()
  if ([...description].length > 200) throw new Error('worker 描述最多 200 字')
  const prompt = String(p.prompt ?? '').trim()
  const n = [...prompt].length
  if (n < 20 || n > 8000) throw new Error('worker 提示词需 20~8000 字（当前 ' + n + ' 字）')
  const model = String(p.model ?? '').trim() || undefined
  customWorkers.push({ id, title, description, prompt, model, custom: true })
  templates.push({
    id,
    title,
    description,
    prompt,
    defaultPrompt: prompt,
    edited: false,
    enabled: true,
    model,
    schedule: { phase: 'premarket', enabled: false },
  })
  return { workers: workerViews() }
})

registerMock('agentlab', 'removeWorker', (p) => {
  const id = String(p.id ?? '').trim()
  if (BUILTIN_WORKERS.some((w) => w.id === id)) throw new Error('内置 worker ' + id + ' 不能删除')
  if (!customWorkers.some((w) => w.id === id)) throw new Error('自定义 worker 不存在：' + id)
  customWorkers = customWorkers.filter((w) => w.id !== id)
  templates = templates.filter((t) => t.id !== id)
  return { workers: workerViews() }
})

export function installAgentMocks(): void {
  templates = buildTemplates()
  customWorkers = []
  clearToolScript()
}
