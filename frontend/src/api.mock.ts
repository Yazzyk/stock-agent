// 演示数据桥：浏览器直开（npm run dev，无 Wails 注入）时替代 window.go。
// 形状严格对齐 docs/API.md，真实 Wails 环境不会加载本文件。

import type {
  AgentRunResult,
  AgentSession,
  Bar,
  DocSummary,
  FundFlow,
  GoBridge,
  KbHit,
  KernelEvent,
  KnowledgeDoc,
  ModelInfo,
  Period,
  PluginView,
  Quote,
  Sector,
  SkillInfo,
  StatusView,
  StockRef,
  SyncResult,
  ToolView,
  WatchItem,
  WatchMetricSpec,
  WatchRule,
  WatchRunLog,
  WatchScenario,
  WatchSource,
  WatchTestResult,
  WorkerInfo,
} from './types'

const FENCE = String.fromCharCode(96).repeat(3)

// ---------- 确定性伪随机（同一代码每次同一条曲线） ----------

function seedOf(text: string): number {
  let h = 2166136261
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

function rng(seed: number): () => number {
  let a = seed || 1
  return () => {
    a += 0x6d2b79f5
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function round2(v: number): number {
  return Math.round(v * 100) / 100
}

function tradingDays(count: number, end: Date): string[] {
  const out: string[] = []
  const d = new Date(end.getTime())
  while (out.length < count) {
    const day = d.getDay()
    if (day !== 0 && day !== 6) {
      const p = (n: number) => (n < 10 ? '0' + n : String(n))
      out.unshift(d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()))
    }
    d.setDate(d.getDate() - 1)
  }
  return out
}

const END_DATE = new Date(2026, 10, 10)

function makeBars(code: string, period: Period): Bar[] {
  const factor = period === 'week' ? 5 : period === 'month' ? 21 : 1
  const count = period === 'min60' ? 160 : 280
  const dates = tradingDays(count, END_DATE)
  const rand = rng(seedOf(code + '|' + period))
  let close = 8 + (seedOf(code) % 40)
  const bars: Bar[] = []
  for (let i = 0; i < count; i++) {
    const drift = (rand() - 0.47) * (factor > 1 ? 0.05 : 0.026)
    const open = close * (1 + (rand() - 0.5) * 0.01)
    close = Math.max(1.2, close * (1 + drift))
    const high = Math.max(open, close) * (1 + rand() * 0.012)
    const low = Math.min(open, close) * (1 - rand() * 0.012)
    const volume = Math.round((2 + rand() * 9) * 1e4 * factor) // 单位：手
    bars.push({
      date: dates[i] ?? '',
      open: round2(open),
      high: round2(high),
      low: round2(low),
      close: round2(close),
      volume,
      amount: Math.round(volume * close),
    })
  }
  return bars
}

function ma(bars: Bar[], n: number): Array<number | null> {
  const out: Array<number | null> = []
  let sum = 0
  for (let i = 0; i < bars.length; i++) {
    sum += bars[i]?.close ?? 0
    if (i >= n) sum -= bars[i - n]?.close ?? 0
    out.push(i >= n - 1 ? round2(sum / n) : null)
  }
  return out
}

// ---------- 静态目录 ----------

interface DemoStock {
  code: string
  name: string
  market: 'sh' | 'sz'
  group: string
  price: number
  pct: number
  amount: number
  turnover: number
}

const DEMO_STOCKS: DemoStock[] = [
  { code: '600519', name: '贵州茅台', market: 'sh', group: '核心持仓', price: 1523.4, pct: 1.24, amount: 4.82e9, turnover: 0.31 },
  { code: '600036', name: '招商银行', market: 'sh', group: '核心持仓', price: 38.62, pct: -0.58, amount: 2.15e9, turnover: 0.54 },
  { code: '000858', name: '五粮液', market: 'sz', group: '核心持仓', price: 142.7, pct: 2.06, amount: 3.04e9, turnover: 1.12 },
  { code: '300750', name: '宁德时代', market: 'sz', group: '核心持仓', price: 246.18, pct: 3.47, amount: 6.71e9, turnover: 1.86 },
  { code: '601318', name: '中国平安', market: 'sh', group: '观察池', price: 54.31, pct: -1.12, amount: 2.88e9, turnover: 0.42 },
  { code: '002594', name: '比亚迪', market: 'sz', group: '观察池', price: 268.9, pct: 0.86, amount: 5.12e9, turnover: 1.03 },
  { code: '688981', name: '中芯国际', market: 'sh', group: '观察池', price: 92.44, pct: 5.62, amount: 7.93e9, turnover: 3.24 },
  { code: '601899', name: '紫金矿业', market: 'sh', group: '观察池', price: 19.87, pct: 2.31, amount: 3.66e9, turnover: 0.98 },
  { code: '000001', name: '上证指数', market: 'sh', group: '指数', price: 3418.62, pct: 0.72, amount: 5.16e11, turnover: 0 },
  { code: '399001', name: '深证成指', market: 'sz', group: '指数', price: 11026.4, pct: 1.08, amount: 6.82e11, turnover: 0 },
  { code: '399006', name: '创业板指', market: 'sz', group: '指数', price: 2287.15, pct: 1.63, amount: 3.24e11, turnover: 0 },
  { code: '000688', name: '科创50', market: 'sh', group: '指数', price: 1042.77, pct: -0.44, amount: 8.9e10, turnover: 0 },
]

const INDUSTRIES: Array<[string, string, number, number]> = [
  ['BK0475', '半导体', 3.62, 4.82e9],
  ['BK0447', '证券Ⅱ', 2.14, 3.11e9],
  ['BK0727', '光伏设备', 1.88, 2.63e9],
  ['BK0478', '白酒Ⅱ', 1.42, 1.94e9],
  ['BK1036', '电池', 1.16, 1.72e9],
  ['BK0420', '国有大型银行Ⅱ', 0.64, 1.05e9],
  ['BK0451', '房地产开发', -0.42, -8.3e8],
  ['BK0737', '游戏Ⅱ', -0.86, -1.12e9],
  ['BK0456', '医药商业', -1.14, -1.51e9],
  ['BK0546', '煤炭开采', -1.62, -2.34e9],
  ['BK0481', '旅游及景区', -2.08, -2.96e9],
  ['BK0538', '能源金属', -2.44, -3.42e9],
]

const CONCEPTS: Array<[string, string, number, number]> = [
  ['BK1101', 'AI 算力', 4.12, 6.42e9],
  ['BK1127', '国产替代', 3.38, 5.18e9],
  ['BK1098', '机器人概念', 2.86, 4.07e9],
  ['BK1052', '华为概念', 2.12, 3.26e9],
  ['BK1145', '低空经济', 1.54, 2.44e9],
  ['BK0987', '卫星互联网', 1.02, 1.63e9],
  ['BK0876', '白酒概念', -0.36, -6.2e8],
  ['BK0892', '猪肉概念', -0.94, -1.28e9],
  ['BK0903', '医美概念', -1.38, -1.86e9],
  ['BK0911', '油气设服', -1.86, -2.12e9],
  ['BK0924', '盐湖提锂', -2.26, -2.88e9],
  ['BK0955', '氢能源', -2.72, -3.16e9],
]

const WORKERS: WorkerInfo[] = [
  { id: 'premarket', title: '盘前', description: '读持仓与昨日行情，产出当日计划：标的/方向/触发/止损/仓位' },
  { id: 'intraday', title: '盯盘', description: '放量突破、跌破均线、资金异常流入等信号巡检' },
  { id: 'postmarket', title: '复盘', description: '计划 vs 实际逐条对照，偏差归因并写入记忆' },
  { id: 'quant', title: '量化', description: '参数网格 + 样本内外 + walk-forward，识别过拟合' },
  { id: 'chat', title: '对话', description: '自由研究对话，可调用已注册工具' },
]

const SKILLS: Array<{ name: string; description: string; content: string }> = [
  {
    name: 'premarket',
    description: '盘前计划：读持仓与昨日行情，产出可执行计划',
    content: '# 盘前计划技能\n\n## 步骤\n1. 读取自选与持仓，确认可用资金\n2. 拉昨收、涨跌幅与成交额，标记异常放量\n3. 用 market.bars 计算 MA5/10/20/60 判断位置\n4. 输出计划：标的、方向、触发条件、止损位、仓位\n\n> 只做研究记录，不构成投资建议。\n',
  },
  {
    name: 'intraday',
    description: '盯盘信号：放量突破、跌破均线、资金异常',
    content: '# 盯盘技能\n\n## 触发条件\n- 放量突破：成交量 > 5 日均量 2 倍且站上 MA20\n- 跌破均线：收盘跌破 MA10 且主力净流出\n- 资金异常：主力净额 > 5 日均值 3 倍\n',
  },
  {
    name: 'postmarket',
    description: '盘后复盘：计划 vs 实际，偏差归因并写入记忆',
    content: '# 复盘技能\n\n1. 读当日盘前计划\n2. 对照实际开高低收与资金流\n3. 逐条判定"符合 / 偏离 / 反向"及原因\n4. 结论写入 memory/ 供次日盘前读取\n',
  },
  {
    name: 'quant',
    description: '量化验证：参数网格与样本外检验',
    content: '# 量化技能\n\n- 样本内 70% 调参，样本外 30% 只验一次\n- 参数网格至少 2×3\n- 输出：总收益、年化、最大回撤、夏普、交易数、胜率\n- 样本外衰减超过 50% 即过拟合信号\n',
  },
]

function tool(name: string, title: string, summary: string, tags: string[], risk: string): ToolView {
  return { name, title, summary, tags, risk }
}

const PLUGINS: PluginView[] = [
  {
    id: 'watch',
    name: 'Watch 哨兵',
    version: '0.1.0',
    description: '自定义盯盘规则：条件命中后自动派一次 Agent 任务，规则文件即插件',
    active: true,
    dependsOn: ['agentlab'],
    tools: [
      tool('watchdog.list', '列出 Watch 规则', '查看所有盯盘规则与开关状态', ['watch'], 'read'),
      tool('watchdog.create', '创建 Watch 规则', '按描述创建盯盘规则（Agent 用）', ['watch'], 'write'),
      tool('watchdog.toggle', '开关 Watch 规则', '启用或停用一条规则', ['watch'], 'write'),
      tool('watchdog.test', '试运行 Watch 规则', '只求值不触发，返回场景与渲染后的 prompt', ['watch'], 'read'),
      tool('watchdog.logs', 'Watch 触发日志', '最近的命中与派发记录', ['watch'], 'read'),
    ],
    ui: [{ id: 'watch', title: 'Watch 哨兵', group: '研究', component: 'watch', order: 35, owner: 'watch' }],
    methods: [
      { name: 'list', description: '规则/状态/指标/日志' },
      { name: 'save', description: '新增或更新规则' },
      { name: 'toggle', description: '开关规则' },
      { name: 'remove', description: '删除规则' },
      { name: 'test', description: '试运行' },
      { name: 'logs', description: '触发日志' },
      { name: 'templates', description: '规则模板' },
      { name: 'metrics', description: '指标白名单' },
      { name: 'state', description: '引擎状态' },
    ],
  },
  {
    id: 'watchlist',
    name: '自选股',
    version: '0.1.0',
    description: '同花顺自选同步与实时行情',
    active: true,
    tools: [
      tool('watch.list', '自选列表', '读取本地自选分组与条目', ['watch', 'market'], 'read'),
      tool('watch.sync', '同步同花顺', '从本机同花顺客户端或网页同步自选', ['watch'], 'read'),
      tool('market.quote', '行情快照', '按代码批量取现价涨跌幅成交额', ['market'], 'network'),
      tool('market.search', '股票搜索', '按代码或名称搜索 A 股标的', ['market'], 'read'),
    ],
    ui: [{ id: 'ui.watchlist', title: '自选股', group: '行情', component: 'watchlist', order: 10, owner: 'watchlist' }],
    methods: [
      { name: 'list', description: '自选列表' },
      { name: 'sync', description: '同步同花顺自选' },
      { name: 'add', description: '加入自选' },
      { name: 'remove', description: '移出自选' },
      { name: 'groups', description: '分组列表' },
    ],
  },
  {
    id: 'fundflow',
    name: '资金流',
    version: '0.1.0',
    description: '个股与板块资金净额',
    active: true,
    tools: [
      tool('flow.stock', '个股资金流', '主力/超大/大/中/小单净额', ['flow', 'market'], 'network'),
      tool('flow.rank', '资金流排名', '板块或概念按主力净额排名', ['flow'], 'network'),
      tool('sector.list', '板块列表', '行业与概念板块涨跌幅', ['flow', 'market'], 'read'),
    ],
    ui: [{ id: 'ui.fundflow', title: '资金流', group: '行情', component: 'fundflow', order: 20, owner: 'fundflow' }],
    methods: [
      { name: 'stock', description: '个股资金流' },
      { name: 'rank', description: '板块/概念排名' },
      { name: 'sectors', description: '板块列表' },
    ],
  },
  {
    id: 'kline',
    name: 'K 线',
    version: '0.1.0',
    description: '日/周/月/60 分 K 线与均线',
    active: true,
    tools: [
      tool('market.bars', 'K 线序列', '按代码与周期取前复权 K 线', ['market'], 'network'),
      tool('kline.analyze', 'K 线分析', '均线与形态摘要', ['market', 'quant'], 'read'),
    ],
    ui: [{ id: 'ui.kline', title: 'K 线', group: '行情', component: 'kline', order: 30, owner: 'kline' }],
    methods: [
      { name: 'bars', description: 'K 线数据' },
      { name: 'analyze', description: 'K 线分析' },
    ],
  },
  {
    id: 'agentlab',
    name: 'Agent 实验室',
    version: '0.1.0',
    description: '盘前/盯盘/复盘/量化/对话五个 worker',
    active: true,
    tools: [
      tool('agent.run', '启动 Agent', '按 worker 运行一次研究任务', ['agent'], 'write'),
      tool('agent.status', 'Agent 状态', '当前运行状态', ['agent'], 'read'),
    ],
    ui: [{ id: 'ui.agentlab', title: 'Agent 实验室', group: '研究', component: 'agentlab', order: 40, owner: 'agentlab' }],
    methods: [
      { name: 'workers', description: 'worker 列表' },
      { name: 'run', description: '启动 worker' },
      { name: 'stop', description: '停止运行' },
      { name: 'sessions', description: '历史会话' },
      { name: 'session', description: '会话详情' },
    ],
  },
  {
    id: 'quant',
    name: '量化研究',
    version: '0.1.0',
    description: '日线回测与 walk-forward：T+1、涨跌停不可成交、四项成本',
    active: true,
    tools: [tool('quant.backtest', '回测策略', '对单只 A 股跑日线回测，返回收益/回撤/成本明细与报告', ['quant', 'research'], 'network')],
    ui: [{ id: 'quant', title: '量化研究', group: '研究', component: 'quant', order: 45, owner: 'quant' }],
    methods: [
      { name: 'strategies', description: '策略与参数定义' },
      { name: 'run', description: '单次回测' },
      { name: 'walkforward', description: 'walk-forward 过拟合检验' },
    ],
  },
  {
    id: 'knowledge',
    name: '知识库',
    version: '0.1.0',
    description: '本地知识检索与技能目录',
    active: true,
    tools: [
      tool('kb.search', '知识检索', '全文检索本地知识库', ['kb'], 'read'),
      tool('kb.open', '打开文档', '读取命中文档全文', ['kb'], 'read'),
      tool('kb.list', '文档列表', '列出知识库文档', ['kb'], 'read'),
      tool('skill.list', '技能列表', '列出可用技能', ['skill'], 'read'),
      tool('skill.view', '技能正文', '读取技能 Markdown', ['skill'], 'read'),
    ],
    ui: [{ id: 'ui.knowledge', title: '知识库', group: '研究', component: 'knowledge', order: 50, owner: 'knowledge' }],
    methods: [
      { name: 'list', description: '文档列表' },
      { name: 'search', description: '知识检索' },
      { name: 'open', description: '打开文档' },
      { name: 'import', description: '导入文档' },
      { name: 'skills', description: '技能列表' },
      { name: 'skill', description: '技能正文' },
    ],
  },
  {
    id: 'modelsettings',
    name: '模型设置',
    version: '0.1.0',
    description: 'Provider、BaseURL、模型与密钥',
    active: true,
    tools: [],
    ui: [{ id: 'ui.modelsettings', title: '模型设置', group: '设置', component: 'modelsettings', order: 90, owner: 'modelsettings' }],
    methods: [
      { name: 'list', description: '模型列表' },
      { name: 'save', description: '保存配置' },
      { name: 'test', description: '连通性测试' },
      { name: 'remove', description: '删除配置' },
    ],
  },
]

// ---------- 可变演示状态 ----------

let tick = 0

const models: ModelInfo[] = [
  {
    id: 'deepseek:deepseek-chat',
    provider: 'deepseek',
    model: 'deepseek-chat',
    displayName: 'DeepSeek Chat',
    baseUrl: 'https://api.deepseek.com',
    hasKey: true,
    isDefault: true,
    maxTokens: 8192,
  },
  {
    id: 'openai-compat:gpt-4o-mini',
    provider: 'openai-compat',
    model: 'gpt-4o-mini',
    displayName: 'OpenAI 兼容 · gpt-4o-mini',
    baseUrl: 'https://api.openai.com/v1',
    hasKey: false,
    isDefault: false,
    maxTokens: 4096,
  },
]

const sessions: AgentSession[] = [
  { id: 's-20261110-0930', worker: 'premarket', title: '盘前计划 · 2026-11-10', startedAt: '2026-11-10T09:30:12+08:00', status: 'ok', tokens: 18240 },
  { id: 's-20261107-1505', worker: 'postmarket', title: '盘后复盘 · 2026-11-07', startedAt: '2026-11-07T15:05:40+08:00', status: 'ok', tokens: 12060 },
  { id: 's-20261107-1012', worker: 'quant', title: '量化 · MA 参数网格', startedAt: '2026-11-07T10:12:03+08:00', status: 'failed', tokens: 8410 },
]

const REPORTS: Record<string, string> = {
  's-20261110-0930':
    '# 盘前计划 2026-11-10\n\n## 一、持仓检查\n- 600519 贵州茅台：昨收 1504.7，位于 MA20 上方，主力净流入 1.24 亿\n- 300750 宁德时代：放量突破前高，量能 2.13 倍\n\n## 二、今日计划\n| 标的 | 方向 | 触发 | 止损 | 仓位 |\n|---|---|---|---|---|\n| 600519 | 观察 | 回踩 1490 不破 | 1472 | 0 |\n| 300750 | 持有 | — | 238.5 | 15% |\n\n## 三、风险\n- 指数在 3400 上方震荡，量能未有效放大\n- 半导体连续 3 日流入，注意高位分歧\n',
  's-20261107-1505':
    '# 复盘 2026-11-07\n\n- 计划中的 300750 突破成立，实际最高 247.2\n- 601318 未触发止损，继续观察\n- 偏差原因：半导体板块资金流入超预期\n\n## 明日关注\n1. 指数能否放量站上 3420\n2. 白酒板块资金是否回流\n',
  's-20261107-1012':
    '# 量化验证 · MA 参数网格\n\n' +
    FENCE +
    'text\n参数       样本内收益   样本外收益\nMA 5/20    +34.2%      +10.8%\nMA 10/30   +28.6%      +12.4%\nMA 20/60   +19.1%      +14.2%\n' +
    FENCE +
    '\n\n样本外衰减 61%，判定过拟合，参数不可用。\n',
}

const DOC_SUMMARIES: DocSummary[] = [
  { id: 'doc-ma', title: '均线系统与趋势判定', summary: 'MA5 上穿 MA20 视为短期转强；跌破 MA60 需降低仓位。', tags: ['技术面', '均线'], updatedAt: '2026-11-06T18:20:00+08:00' },
  { id: 'doc-flow', title: '资金流读法', summary: '主力净额 = 超大单 + 大单；单日流入不构成信号。', tags: ['资金面'], updatedAt: '2026-11-05T10:02:00+08:00' },
  { id: 'doc-review', title: '复盘模板', summary: '计划 vs 实际逐条对照，偏差原因写入记忆。', tags: ['流程'], updatedAt: '2026-11-04T21:40:00+08:00' },
  { id: 'doc-overfit', title: '过拟合识别', summary: '样本外衰减超过 50% 视为过拟合。', tags: ['量化'], updatedAt: '2026-11-03T14:12:00+08:00' },
]

const DOC_CONTENT: Record<string, KnowledgeDoc> = {
  'doc-ma': {
    id: 'doc-ma',
    title: '均线系统与趋势判定',
    tags: ['技术面', '均线'],
    content:
      '# 均线系统与趋势判定\n\n## 核心口径\n- MA5 上穿 MA20：短期转强，但需要量能配合\n- 价格在 MA60 上方：中期趋势健康\n- 均线发散：趋势延续；均线收敛：方向待定\n\n## 判定代码\n' +
      FENCE +
      'text\nif MA5 > MA20 && close > MA60: 趋势 = 多头\n' +
      FENCE +
      '\n\n> 均线是滞后指标，只能确认不能预测。\n',
    sections: [
      { heading: '核心口径', content: 'MA5 与 MA20 的交叉需要量能确认；MA60 代表中期成本线。' },
      { heading: '常见误用', content: '把均线交叉当作买卖信号，忽略成交结构与资金方向。' },
    ],
  },
  'doc-flow': {
    id: 'doc-flow',
    title: '资金流读法',
    tags: ['资金面'],
    content: '# 资金流读法\n\n## 定义\n- 主力净额 = 超大单 + 大单\n- 单日净流入不构成信号，需看 5 日累计\n\n## 用法\n1. 与价格位置结合：高位大额流入需警惕\n2. 与板块共振：个股流入 + 板块流入更可信\n',
  },
  'doc-review': {
    id: 'doc-review',
    title: '复盘模板',
    tags: ['流程'],
    content: '# 复盘模板\n\n## 一、计划回顾\n逐条列出盘前计划\n\n## 二、实际走势\n开高低收、量能、资金流\n\n## 三、偏差归因\n- 符合 / 偏离 / 反向\n- 原因：消息面、资金面、情绪面\n\n## 四、明日准备\n',
  },
  'doc-overfit': {
    id: 'doc-overfit',
    title: '过拟合识别',
    tags: ['量化'],
    content: '# 过拟合识别\n\n- 样本内 70% 调参，样本外 30% 只验一次\n- 样本外衰减超过 50% 视为过拟合\n- 参数越多，需要越长的样本外区间\n',
  },
}

function nowISO(): string {
  return new Date().toISOString()
}

function quoteOf(s: DemoStock): Quote {
  const wobble = Math.sin(tick / 3 + s.code.length) * 0.004
  const price = round2(s.price * (1 + wobble))
  const prevClose = round2(s.price / (1 + s.pct / 100))
  return {
    code: s.code,
    name: s.name,
    market: s.market,
    price,
    prevClose,
    open: round2(prevClose * 1.002),
    high: round2(Math.max(price, prevClose) * 1.006),
    low: round2(Math.min(price, prevClose) * 0.994),
    change: round2(price - prevClose),
    changePct: round2(((price - prevClose) / prevClose) * 100),
    volume: Math.round(s.amount / Math.max(price, 1) / 100), // 单位：手
    amount: s.amount,
    turnover: s.turnover,
    amplitude: round2(Math.abs(s.pct) * 1.4 + 0.5),
    time: nowISO(),
    source: 'eastmoney',
    stale: false,
  }
}

function watchItems(): WatchItem[] {
  return DEMO_STOCKS.map((s, i) => ({ code: s.code, name: s.name, market: s.market, group: s.group, order: i, source: 'local-ths' }))
}

function watchPayload(): { items: WatchItem[]; groups: string[]; updatedAt: string; source: string } {
  tick += 1
  return { items: watchItems(), groups: ['核心持仓', '观察池', '指数'], updatedAt: nowISO(), source: '本地同花顺 · 演示数据' }
}

function rankData(kind: 'sector' | 'concept'): FundFlow[] {
  const rows = kind === 'sector' ? INDUSTRIES : CONCEPTS
  return rows.map(([code, name, pct, main], i) => ({
    code,
    name,
    kind,
    mainNet: Math.round(main),
    changePct: pct,
    rank: i + 1,
    source: 'eastmoney',
  }))
}

function sectorData(kind: 'sector' | 'concept'): Sector[] {
  const rows = kind === 'sector' ? INDUSTRIES : CONCEPTS
  return rows.map(([code, name, pct, main]) => ({
    code,
    name,
    kind,
    changePct: pct,
    mainNet: Math.round(main),
    count: 40 + (seedOf(code) % 120),
  }))
}

function flowOf(code: string): FundFlow {
  const rand = rng(seedOf(code))
  const main = (rand() - 0.4) * 3.6e8
  return {
    code,
    name: DEMO_STOCKS.find((s) => s.code === code)?.name ?? '未知标的',
    kind: 'stock',
    date: '2026-11-10',
    mainNet: Math.round(main),
    superNet: Math.round(main * 0.52),
    largeNet: Math.round(main * 0.48),
    mediumNet: Math.round(-main * 0.18),
    smallNet: Math.round(-main * 0.22),
    mainPct: round2((rand() - 0.4) * 8),
    changePct: round2((rand() - 0.45) * 6),
    source: 'eastmoney',
  }
}

function searchStocks(keyword: string): StockRef[] {
  const k = keyword.trim().toLowerCase()
  if (!k) return []
  const pool: StockRef[] = [
    ...DEMO_STOCKS.map((s) => ({ code: s.code, name: s.name, market: s.market, kind: s.group })),
    ...INDUSTRIES.map(([code, name]) => ({ code, name, market: 'sh' as const, kind: '行业板块' })),
    ...CONCEPTS.map(([code, name]) => ({ code, name, market: 'sh' as const, kind: '概念板块' })),
  ]
  return pool.filter((s) => s.code.toLowerCase().includes(k) || s.name.includes(keyword.trim())).slice(0, 10)
}

function syncResult(): SyncResult {
  return {
    success: true,
    message: '已从本机同花顺同步 3 个分组、12 只标的',
    groups: ['核心持仓', '观察池', '指数'],
    added: 0,
    removed: 0,
    needWebLogin: false,
    needFullDiskAccess: false,
    warnings: ['演示数据：真实环境读取 ~/Library/Containers/cn.com.10jqka.macstock 下的分组文件'],
    source: 'local-ths',
  }
}

// ---------- 内核事件（模拟流式） ----------

let kernelListener: ((e: KernelEvent) => void) | null = null
let stopped = false

export function subscribeMockKernel(cb: (e: KernelEvent) => void): void {
  kernelListener = cb
}

function emit(topic: string, payload: Record<string, unknown>): void {
  if (!kernelListener) return
  kernelListener({ topic, time: nowISO(), payload: JSON.stringify(payload) })
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms)
  })
}

const DELTA_STEPS = [
  '读取自选列表…\n',
  '命中 12 只标的，拉取最新行情\n',
  'market.quote → 600519 1523.40 (+1.24%)\n',
  'market.quote → 300750 246.18 (+3.47%)\n',
  '计算 MA5/MA10/MA20/MA60 位置关系…\n',
  'flow.stock → 300750 主力净流入 1.42 亿\n',
  '识别到 1 个放量突破信号（300750，量比 2.13）\n',
  '生成研究草稿…\n',
]

function reportOf(worker: string): string {
  const title = WORKERS.find((w) => w.id === worker)?.title ?? worker
  return (
    '# ' + title + ' 演示报告\n\n## 结论\n- 自选 12 只中 8 只上涨、4 只下跌\n- 300750 宁德时代放量突破，量比 2.13，主力净流入 1.42 亿\n- 601318 中国平安跌破 MA10，注意回撤\n\n## 计划\n1. 观察 600519 回踩 1490 是否获得支撑\n2. 300750 持仓不动，止损上移至 238.50\n3. 半导体板块资金连续 3 日流入，加入观察\n\n' +
    FENCE +
    'text\n600519  MA20 1490.2  MA60 1436.8\n300750  MA20 232.6   MA60 218.4\n' +
    FENCE +
    '\n\n> 本报告由演示数据生成，不构成投资建议。\n'
  )
}

async function mockRun(worker: string, input: string): Promise<AgentRunResult> {
  const id = 's-demo-' + Date.now().toString(36)
  stopped = false
  const wantsWatchRule = /watchdog\.create/.test(input)
  const createdRule = wantsWatchRule ? watchCreateFromDescription(input) : null
  emit('agent.state', { sessionId: id, state: 'thinking', detail: '准备上下文' })
  for (let i = 0; i < DELTA_STEPS.length; i++) {
    if (stopped) break
    await sleep(260)
    emit('agent.delta', { sessionId: id, text: DELTA_STEPS[i] ?? '' })
    if (i === 5) emit('agent.state', { sessionId: id, state: 'tool', detail: 'flow.stock' })
  }
  const report = createdRule
    ? '# 已创建 Watch 规则\n\n- 规则：' + createdRule.name + '\n- 触发源：' +
      createdRule.sources.map((s) => (s.type === 'quote' || s.type === 'kline' ? s.type + ':' + (s.codes ?? []).join(',') : s.type === 'sector' ? '板块:' + (s.sector ?? '') : s.type === 'news' ? '新闻:' + (s.keyword ?? '') : '定时:' + (s.times ?? []).join(','))).join(' | ') +
      '\n- 条件：`' + createdRule.condition + '`\n- 动作：' + createdRule.action.worker + '\n- 冷却：' + createdRule.cooldownSeconds + ' 秒\n\n> 规则已启用，可在面板里试运行或调参。\n'
    : reportOf(worker)
  const usage = { promptTokens: 12480, completionTokens: 1860, totalTokens: 14340 }
  const session: AgentSession = {
    id,
    worker,
    title: (input || WORKERS.find((w) => w.id === worker)?.title || worker) + ' · 演示',
    startedAt: nowISO(),
    status: stopped ? 'failed' : 'ok',
    tokens: usage.totalTokens,
  }
  sessions.unshift(session)
  if (!stopped) emit('agent.report', { sessionId: id, worker, report, usage })
  emit('agent.state', { sessionId: id, state: stopped ? 'error' : 'done', detail: stopped ? '已手动停止' : '' })
  return { sessionId: id, worker, report, steps: 6, usage }
}


// ---------- Watch 哨兵演示数据 ----------

const WATCH_METRICS: WatchMetricSpec[] = [
  { name: 'flow.mainNet', label: '主力净额', unit: '元' },
  { name: 'flow.mainNetPct', label: '主力净占比（净额/成交额）', unit: '%' },
  { name: 'flow.mainNetYi', label: '主力净额', unit: '亿元' },
  { name: 'kline.aboveMA20', label: '站上 MA20', unit: '1/0' },
  { name: 'kline.atrPct', label: 'ATR 占收盘', unit: '%' },
  { name: 'kline.ma20dev', label: '距 MA20', unit: '%' },
  { name: 'kline.macd', label: 'MACD 柱', unit: '' },
  { name: 'kline.rsi', label: 'RSI(14)', unit: '' },
  { name: 'kline.volumeRatio', label: '量比(5日)', unit: '' },
  { name: 'news.hits', label: '命中新闻/公告数', unit: '条' },
  { name: 'news.negative', label: '负面词命中', unit: '1/0' },
  { name: 'quote.amount', label: '成交额', unit: '元' },
  { name: 'quote.changePct', label: '涨跌幅', unit: '%' },
  { name: 'quote.price', label: '现价', unit: '元' },
  { name: 'quote.turnover', label: '换手率', unit: '%' },
  { name: 'quote.volumeRatio', label: '量比', unit: '' },
  { name: 'sector.changePct', label: '板块涨跌幅', unit: '%' },
  { name: 'sector.mainNet', label: '板块主力净额', unit: '元' },
  { name: 'sector.mainNetYi', label: '板块主力净额', unit: '亿元' },
  { name: 'time.hour', label: '小时', unit: '' },
  { name: 'time.minute', label: '分钟', unit: '' },
  { name: 'time.trading', label: '是否交易时段', unit: '1/0' },
]

const WATCH_TEMPLATES: WatchRule[] = [
  {
    name: '半导体板块资金异动',
    description: '板块主力净流入占比超过 2% 时让 Agent 判断持续性',
    enabled: true,
    sources: [{ type: 'sector', sector: '半导体', kind: 'sector' }],
    condition: 'sector.mainNetYi > 20',
    action: {
      worker: 'intraday',
      prompt: '板块 {sector} 主力净流入 {sector.mainNetYi} 亿元，涨跌 {sector.changePct}%。请判断资金持续性，并给出 3 条观察要点（不构成投资建议）。',
    },
    cooldownSeconds: 600,
    createdBy: 'template',
  },
  {
    name: '自选股放量突破',
    description: '量比 > 2 且站上 MA20 时触发',
    enabled: false,
    sources: [{ type: 'kline', codes: ['600519'], period: 'day' }],
    condition: 'kline.volumeRatio > 2 and kline.aboveMA20 > 0',
    action: {
      worker: 'intraday',
      prompt: '标的出现放量突破（量比 {kline.volumeRatio}，距 MA20 {kline.ma20dev}%），请结合形态判断是真突破还是诱多。',
    },
    cooldownSeconds: 900,
    createdBy: 'template',
  },
  {
    name: '公告关键词监控',
    description: '公告出现关键词时让 Agent 做方向研究',
    enabled: false,
    sources: [{ type: 'news', keyword: '并购重组', kind: 'notice' }],
    condition: 'news.hits > 0',
    action: {
      worker: 'chat',
      prompt: '出现了 {news.hits} 条与「{keyword}」相关的公告/新闻（示例：{summary}）。请评估可能受益的方向与需要验证的数据。',
    },
    cooldownSeconds: 1800,
    createdBy: 'template',
  },
]

let watchRules: WatchRule[] = [
  {
    id: 'w7f3a1b2c4d5',
    name: '自选放量突破',
    description: '自选股量比放大且涨跌幅超过阈值时派给盯盘 worker',
    enabled: true,
    sources: [{ type: 'quote', codes: ['300750', '688981'] }],
    condition: 'quote.changePct > 3 and quote.volumeRatio > 2',
    action: {
      worker: 'intraday',
      prompt: '标的 {code} 出现放量拉升：涨跌幅 {quote.changePct}%，量比 {quote.volumeRatio}。请给出当日节奏判断与需要继续观察的信号（不构成投资建议）。',
    },
    cooldownSeconds: 600,
    quietWindows: ['11:30-13:00'],
    createdBy: 'user',
    createdAt: '2026-11-08T09:12:00+08:00',
    updatedAt: '2026-11-10T09:20:00+08:00',
    lastTriggeredAt: '2026-11-10T10:32:00+08:00',
    triggerCount: 2,
  },
  {
    id: 'w2c9e8d7a6b5',
    name: '半导体板块资金异动',
    description: '板块主力净流入超过 20 亿时让 Agent 判断持续性',
    enabled: false,
    sources: [{ type: 'sector', sector: '半导体', kind: 'sector' }],
    condition: 'sector.mainNetYi > 20 and sector.changePct > 1',
    action: {
      worker: 'chat',
      prompt: '板块 {sector} 主力净流入 {sector.mainNetYi} 亿元，涨跌 {sector.changePct}%。请判断资金是否具有持续性，并列出 3 条验证要点。',
    },
    cooldownSeconds: 900,
    quietWindows: [],
    createdBy: 'user',
    createdAt: '2026-11-06T15:40:00+08:00',
    updatedAt: '2026-11-09T21:03:00+08:00',
    triggerCount: 0,
    lastError: '未配置模型：请在「模型设置」里填写 API Key 后重试',
  },
  {
    id: 'w9d1f0a2b3c4',
    name: '盘中定时巡检',
    description: '交易时段的固定时点让 Agent 做一次盘面巡检',
    enabled: true,
    sources: [{ type: 'schedule', times: ['09:35', '13:05'] }],
    condition: 'time.trading == 1',
    action: {
      worker: 'intraday',
      prompt: '到了巡检时点：请汇总自选涨跌分布、板块资金前五与异动标的，给出 5 条观察要点。',
    },
    cooldownSeconds: 300,
    quietWindows: ['11:30-13:00'],
    createdBy: 'user',
    createdAt: '2026-11-05T08:30:00+08:00',
    updatedAt: '2026-11-10T09:00:00+08:00',
    lastTriggeredAt: '2026-11-10T13:05:12+08:00',
    triggerCount: 5,
  },
]

const WATCH_LOGS: WatchRunLog[] = [
  {
    time: '2026-11-10T10:32:00+08:00',
    ruleId: 'w7f3a1b2c4d5',
    ruleName: '自选放量突破',
    worker: 'intraday',
    triggered: true,
    matched: true,
    summary: '300750 涨 3.47%，量比 2.13',
    metrics: { 'quote.changePct': 3.47, 'quote.volumeRatio': 2.13, 'quote.price': 246.18 },
    report: '300750 宁德时代放量突破前高，量比 2.13，主力净流入 1.42 亿。\n\n观察要点：\n1. 回踩 240 是否缩量\n2. 板块资金是否延续\n3. 若跌破 MA10（232.6）需降低预期',
    elapsedMs: 3120,
  },
  {
    time: '2026-11-10T09:35:04+08:00',
    ruleId: 'w9d1f0a2b3c4',
    ruleName: '盘中定时巡检',
    worker: 'intraday',
    triggered: true,
    matched: true,
    summary: '定时巡检：自选 12 只，8 涨 4 跌',
    metrics: { 'time.trading': 1 },
    report: '巡检结果：自选 12 只中 8 涨 4 跌，半导体与电池资金流入居前；601318 跌破 MA10。',
    elapsedMs: 6420,
  },
  {
    time: '2026-11-10T10:20:31+08:00',
    ruleId: 'w2c9e8d7a6b5',
    ruleName: '半导体板块资金异动',
    worker: 'chat',
    triggered: false,
    matched: true,
    error: '触发未派发：冷却中（600 秒内只派发一次）',
  },
]

function renderWatchPrompt(tpl: string, metrics: Record<string, number>, ctx: Record<string, string>): string {
  return tpl.replace(/\{([a-zA-Z0-9_.]+)\}/g, (whole, key: string) => {
    if (ctx[key] !== undefined) return ctx[key]
    const v = metrics[key]
    return v === undefined ? whole : String(v)
  })
}

function watchScenarioMetrics(code: string, name: string): Record<string, number> {
  const rand = rng(seedOf(code))
  return {
    'quote.price': round2(12 + rand() * 260),
    'quote.changePct': round2((rand() - 0.3) * 6),
    'quote.volumeRatio': round2(0.6 + rand() * 2.4),
    'quote.turnover': round2(rand() * 4),
    'kline.rsi': round2(38 + rand() * 32),
    'kline.ma20dev': round2((rand() - 0.4) * 8),
    'kline.volumeRatio': round2(0.7 + rand() * 2.2),
    'kline.aboveMA20': rand() > 0.35 ? 1 : 0,
    'flow.mainNetYi': round2((rand() - 0.4) * 8),
    'flow.mainNetPct': round2((rand() - 0.4) * 12),
    'time.trading': 1,
  }
}

function watchTestMock(id: string): WatchTestResult {
  const rule = watchRules.find((r) => r.id === id) ?? watchRules[0]
  const metrics: Record<string, number> = {}
  const scenarios: WatchScenario[] = []
  const codes: string[] = []
  for (const s of rule?.sources ?? []) {
    if (s.type === 'quote' || s.type === 'kline') for (const c of s.codes ?? []) codes.push(c)
  }
  if (codes.length === 0) {
    const sector = (rule?.sources ?? []).find((s) => s.type === 'sector')?.sector
    if (sector) {
      metrics['sector.changePct'] = 3.62
      metrics['sector.mainNetYi'] = 48.2
      metrics['sector.mainNet'] = 4.82e9
      metrics['news.hits'] = 0
      metrics['time.trading'] = 1
      scenarios.push({ sector, name: sector, metrics: { ...metrics }, matched: true })
    } else {
      metrics['time.trading'] = 1
      metrics['news.hits'] = 2
      scenarios.push({ name: '定时/新闻场景', metrics: { ...metrics }, matched: true })
    }
  } else {
    codes.forEach((code, i) => {
      const name = DEMO_STOCKS.find((s) => s.code === code)?.name ?? code
      const m = watchScenarioMetrics(code, name)
      // 首条场景刻意构造成"放量突破"形态，避免出现「命中但数值不满足阈值」的演示矛盾
      if (i === 0) {
        m['quote.changePct'] = 3.47
        m['quote.volumeRatio'] = 2.13
        m['kline.volumeRatio'] = 2.13
        m['kline.aboveMA20'] = 1
      }
      for (const k of Object.keys(m)) metrics[k] = m[k] ?? 0
      scenarios.push({
        code,
        name,
        metrics: m,
        matched: i === 0,
      })
    })
  }
  const first = scenarios[0]
  if (first) for (const k of Object.keys(first.metrics)) metrics[k] = first.metrics[k] ?? 0
  const summary = scenarios.length > 0
    ? scenarios
        .map((s) => (s.code ? s.code + ' ' + s.name : s.sector ?? s.name ?? '') + '：' + (s.matched ? '命中' : '未命中'))
        .join('；')
    : '没有可求值的场景'
  const prompt = renderWatchPrompt(rule?.action.prompt ?? '', metrics, {
    code: first?.code ?? '',
    name: first?.name ?? '',
    sector: first?.sector ?? '',
    summary,
    keyword: (rule?.sources ?? []).find((s) => s.type === 'news')?.keyword ?? '',
  })
  return {
    ruleId: rule?.id ?? id,
    matched: scenarios.some((s) => s.matched),
    scenarios,
    summary,
    prompt,
    elapsedMs: 780,
  }
}

/** Agent 一句话创建规则：从描述里抽代码/关键词，拼一条能直接跑的规则。 */
function watchCreateFromDescription(text: string): WatchRule {
  const body = text.replace(/^.*watchdog\.create[^：:]*[：:]?/, '').trim() || text
  const codes = (body.match(/\b\d{6}\b/g) ?? []).slice(0, 3)
  const wantsSector = /板块|概念|行业/.test(body)
  const wantsNews = /公告|新闻|消息/.test(body)
  const wantsTime = /每天|盘中|定时|开盘|收盘/.test(body)
  const rule: WatchRule = {
    id: 'w' + Date.now().toString(36),
    name: body.slice(0, 28) || 'Agent 生成的规则',
    description: '由 Agent 根据一句话描述创建',
    enabled: true,
    sources: wantsSector
      ? [{ type: 'sector', sector: /半导体/.test(body) ? '半导体' : 'AI 算力', kind: 'sector' }]
      : wantsNews
        ? [{ type: 'news', keyword: /并购/.test(body) ? '并购重组' : '回购', kind: 'notice' }]
        : wantsTime
          ? [{ type: 'schedule', times: ['09:35', '13:05'] }]
          : [{ type: 'quote', codes: codes.length > 0 ? codes : ['600519'] }],
    condition: wantsSector
      ? 'sector.changePct > 2 and sector.mainNetYi > 10'
      : wantsNews
        ? 'news.hits > 0'
        : wantsTime
          ? 'time.trading == 1'
          : 'quote.changePct > 3 and quote.volumeRatio > 2',
    action: {
      worker: 'intraday',
      prompt: '触发信号：{summary}。请结合行情与资金面给出简短判断和观察要点（不构成投资建议）。',
    },
    cooldownSeconds: 600,
    quietWindows: ['11:30-13:00'],
    createdBy: 'agent',
    createdAt: nowISO(),
    updatedAt: nowISO(),
    triggerCount: 0,
  }
  watchRules = [rule, ...watchRules]
  return rule
}

// ---------- 量化研究演示数据 ----------

const QUANT_STRATEGIES = [
  {
    id: 'ma',
    name: '均线交叉',
    desc: '快线上穿慢线满仓、下穿空仓',
    params: [
      { key: 'fast', label: '快线', type: 'int', min: 2, max: 60, step: 1 },
      { key: 'slow', label: '慢线', type: 'int', min: 5, max: 250, step: 1 },
    ],
    default: { fast: 5, slow: 20 },
  },
  {
    id: 'rsi',
    name: 'RSI 反转',
    desc: 'RSI 低于下限买入、高于上限卖出',
    params: [
      { key: 'period', label: '周期', type: 'int', min: 2, max: 60, step: 1 },
      { key: 'lower', label: '下限', type: 'float', min: 5, max: 45, step: 1 },
      { key: 'upper', label: '上限', type: 'float', min: 55, max: 95, step: 1 },
    ],
    default: { period: 14, lower: 30, upper: 70 },
  },
  {
    id: 'momentum',
    name: '动量',
    desc: 'N 日涨幅超过阈值满仓，否则空仓',
    params: [
      { key: 'lookback', label: '回看天数', type: 'int', min: 3, max: 120, step: 1 },
      { key: 'threshold', label: '阈值', type: 'float', min: 0, max: 0.5, step: 0.01 },
    ],
    default: { lookback: 20, threshold: 0.05 },
  },
  { id: 'buyhold', name: '买入持有', desc: '基线：首日满仓不动', params: [], default: {} },
]

interface SimTrade {
  entryDate: string
  exitDate: string
  entryPrice: number
  exitPrice: number
  shares: number
  pnl: number
  returnPct: number
  days: number
}

interface SimResult {
  equity: Array<{ date: string; equity: number; cash: number; position: number; shares: number }>
  trades: SimTrade[]
  costs: { commission: number; stampTax: number; transfer: number; slippage: number }
  /** 双边成交额（买+卖），换手 = 成交额 / 初始资金 */
  notional: number
}

function maAt(bars: Bar[], i: number, n: number): number | null {
  if (i < n - 1) return null
  let sum = 0
  for (let k = i - n + 1; k <= i; k++) sum += bars[k]?.close ?? 0
  return sum / n
}

function simulateRun(bars: Bar[], strategy: string, params: Record<string, number>, initialCash: number): SimResult {
  let cash = initialCash
  let shares = 0
  let entryDate = ''
  let entryPrice = 0
  const equity: SimResult['equity'] = []
  const trades: SimTrade[] = []
  const costs = { commission: 0, stampTax: 0, transfer: 0, slippage: 0 }
  let notional = 0
  const fast = Math.max(2, Math.round(params.fast ?? 5))
  const slow = Math.max(fast + 1, Math.round(params.slow ?? 20))
  const lookback = Math.max(1, Math.round(params.lookback ?? 20))
  const threshold = params.threshold ?? 0.05

  for (let i = 1; i < bars.length; i++) {
    const bar = bars[i]
    if (!bar) continue
    const px = bar.open
    let target = 1
    if (strategy === 'ma') {
      const f = maAt(bars, i - 1, fast)
      const s = maAt(bars, i - 1, slow)
      target = f !== null && s !== null && f > s ? 1 : 0
    } else if (strategy === 'momentum') {
      const prev = bars[i - lookback]?.close
      target = prev && prev > 0 && bar.close / prev - 1 > threshold ? 1 : 0
    } else if (strategy === 'rsi') {
      const f = maAt(bars, i - 1, 5)
      const s = maAt(bars, i - 1, 20)
      target = f !== null && s !== null && f > s ? 1 : 0
    }
    const equityAtOpen = cash + shares * px
    let want = Math.floor((equityAtOpen * target) / px / 100) * 100
    if (want < 0) want = 0
    if (want > shares) {
      const buy = want - shares
      const amount = px * buy
      const commission = Math.max(amount * 0.0003, 5)
      const transfer = amount * 0.00001
      const slip = amount * 0.001
      if (amount + commission + transfer <= cash) {
        cash -= amount + commission + transfer
        costs.commission += commission
        costs.transfer += transfer
        costs.slippage += slip
        notional += amount
        if (entryDate === '') {
          entryDate = bar.date
          entryPrice = px
        }
        shares = want
      }
    } else if (want < shares) {
      const sell = shares - want
      const amount = px * sell
      const commission = Math.max(amount * 0.0003, 5)
      const stamp = amount * 0.0005
      const transfer = amount * 0.00001
      const slip = amount * 0.001
      cash += amount - commission - stamp - transfer
      costs.commission += commission
      costs.stampTax += stamp
      costs.transfer += transfer
      costs.slippage += slip
      notional += amount
      if (entryPrice > 0) {
        trades.push({
          entryDate,
          exitDate: bar.date,
          entryPrice: round2(entryPrice),
          exitPrice: round2(px),
          shares: sell,
          pnl: round2((px - entryPrice) * sell),
          returnPct: round2((px / entryPrice - 1) * 10000) / 10000,
          days: Math.max(1, Math.round((Date.parse(bar.date) - Date.parse(entryDate)) / 86400000)),
        })
      }
      entryDate = ''
      entryPrice = 0
      shares = want
      if (shares > 0) {
        entryDate = bar.date
        entryPrice = px
      }
    }
    equity.push({
      date: bar.date,
      equity: round2(cash + shares * bar.close),
      cash: round2(cash),
      position: round2(shares * bar.close),
      shares,
    })
  }
  return { equity, trades, costs, notional }
}

function dailyReturns(equity: SimResult['equity']): number[] {
  const out: number[] = []
  for (let i = 1; i < equity.length; i++) {
    const prev = equity[i - 1]?.equity ?? 0
    const cur = equity[i]?.equity ?? 0
    if (prev > 0) out.push(cur / prev - 1)
  }
  return out
}

function sharpeOf(equity: SimResult['equity']): number {
  const rs = dailyReturns(equity)
  if (rs.length < 2) return 0
  const mean = rs.reduce((a, b) => a + b, 0) / rs.length
  const variance = rs.reduce((a, b) => a + (b - mean) * (b - mean), 0) / (rs.length - 1)
  const sd = Math.sqrt(variance)
  if (sd === 0) return 0
  return round2((mean / sd) * Math.sqrt(252))
}

function drawdownOf(equity: SimResult['equity']): number {
  let peak = 0
  let mdd = 0
  for (const p of equity) {
    peak = Math.max(peak, p.equity)
    if (peak > 0) mdd = Math.max(mdd, 1 - p.equity / peak)
  }
  return round2(mdd * 10000) / 10000
}

function quantReport(code: string, strategyName: string, result: {
  start: string
  end: string
  bars: number
  initialCash: number
  finalEquity: number
  totalReturn: number
  cagr: number
  maxDrawdown: number
  sharpe: number
  sortino: number
  winRate: number
  tradeCount: number
  turnover: number
  trades: SimTrade[]
  costs: { commission: number; stampTax: number; transfer: number; slippage: number }
}): string {
  const total =
    result.costs.commission + result.costs.stampTax + result.costs.transfer + result.costs.slippage
  let text = '# 回测报告 · ' + strategyName + '（' + code + '）\n\n'
  text += '- 区间：' + result.start + ' ~ ' + result.end + '（' + result.bars + ' 根日线）\n'
  text += '- 初始资金：' + result.initialCash.toFixed(2) + ' 元，期末权益：' + result.finalEquity.toFixed(2) + ' 元\n'
  text +=
    '- 总收益：' + (result.totalReturn * 100).toFixed(2) + '%，年化：' + (result.cagr * 100).toFixed(2) +
    '%，最大回撤：' + (result.maxDrawdown * 100).toFixed(2) + '%\n'
  text +=
    '- 夏普：' + result.sharpe.toFixed(2) + '，索提诺：' + result.sortino.toFixed(2) +
    '，胜率：' + (result.winRate * 100).toFixed(2) + '%，交易 ' + result.tradeCount +
    ' 笔，换手 ' + result.turnover.toFixed(2) + 'x\n\n'
  text += '## 成本明细\n\n'
  text += '- 佣金：' + result.costs.commission.toFixed(2) + ' 元\n'
  text += '- 印花税：' + result.costs.stampTax.toFixed(2) + ' 元\n'
  text += '- 过户费：' + result.costs.transfer.toFixed(2) + ' 元\n'
  text += '- 滑点：' + result.costs.slippage.toFixed(2) + ' 元\n'
  text += '- 合计：' + total.toFixed(2) + ' 元（占初始资金 ' + ((total / result.initialCash) * 100).toFixed(2) + '%）\n\n'
  text += '## 交易明细\n\n'
  if (result.trades.length === 0) {
    text += '（无平仓交易）\n'
  } else {
    text += '| 建仓 | 平仓 | 买入价 | 卖出价 | 股数 | 盈亏 | 收益 |\n|---|---|---|---|---|---|---|\n'
    for (const t of result.trades.slice(0, 20)) {
      text +=
        '| ' + t.entryDate + ' | ' + t.exitDate + ' | ' + t.entryPrice.toFixed(2) + ' | ' +
        t.exitPrice.toFixed(2) + ' | ' + t.shares + ' | ' + t.pnl.toFixed(2) + ' | ' +
        (t.returnPct * 100).toFixed(2) + '% |\n'
    }
  }
  return text
}

function quantRunMock(code: string, period: string, strategy: string, params: Record<string, number>, initialCash: number): unknown {
  const bars = makeBars(code, (period as Period) || 'day')
  const sim = simulateRun(bars, strategy, params, initialCash)
  const spec = QUANT_STRATEGIES.find((s) => s.id === strategy) ?? QUANT_STRATEGIES[0]
  const finalEquity = sim.equity.length > 0 ? (sim.equity[sim.equity.length - 1]?.equity ?? initialCash) : initialCash
  const totalReturn = initialCash > 0 ? finalEquity / initialCash - 1 : 0
  const years = Math.max(0.08, bars.length / 252)
  const cagr = Math.pow(1 + totalReturn, 1 / years) - 1
  const wins = sim.trades.filter((t) => t.pnl > 0).length
  const result = {
    strategy: spec?.name ?? strategy,
    code,
    start: bars[0]?.date ?? '',
    end: bars[bars.length - 1]?.date ?? '',
    bars: bars.length,
    initialCash,
    finalEquity: round2(finalEquity),
    totalReturn: Math.round(totalReturn * 10000) / 10000,
    cagr: Math.round(cagr * 10000) / 10000,
    maxDrawdown: drawdownOf(sim.equity),
    sharpe: sharpeOf(sim.equity),
    sortino: round2(sharpeOf(sim.equity) * 1.28),
    winRate: sim.trades.length > 0 ? Math.round((wins / sim.trades.length) * 10000) / 10000 : 0,
    turnover: Math.round((sim.notional / initialCash) * 100) / 100,
    tradeCount: sim.trades.length,
    trades: sim.trades,
    equity: sim.equity,
    costs: {
      commission: round2(sim.costs.commission),
      stampTax: round2(sim.costs.stampTax),
      transfer: round2(sim.costs.transfer),
      slippage: round2(sim.costs.slippage),
    },
    warnings: ['演示数据：随机游走序列，结论仅用于验证面板'],
  }
  return {
    code,
    period,
    adjust: 'qfq',
    result,
    report: quantReport(code, result.strategy, result),
  }
}

function walkForwardMock(code: string, strategy: string, folds: number): unknown {
  const bars = makeBars(code, 'day')
  const n = Math.max(2, Math.min(folds, 5))
  const size = Math.floor(bars.length / (n + 1))
  const spec = QUANT_STRATEGIES.find((s) => s.id === strategy) ?? QUANT_STRATEGIES[0]
  const params = (spec?.default ?? {}) as Record<string, number>
  const rows = []
  let isComp = 1
  let oosComp = 1
  for (let k = 0; k < n; k++) {
    const train = bars.slice(k * size, (k + 1) * size)
    const test = bars.slice((k + 1) * size, (k + 2) * size)
    const tr = simulateRun(train, strategy, params, 100000)
    const te = simulateRun(test, strategy, params, 100000)
    const trRet = tr.equity.length > 0 ? (tr.equity[tr.equity.length - 1]?.equity ?? 100000) / 100000 - 1 : 0
    const teRet = te.equity.length > 0 ? (te.equity[te.equity.length - 1]?.equity ?? 100000) / 100000 - 1 : 0
    isComp *= 1 + trRet
    oosComp *= 1 + teRet
    rows.push({
      index: k,
      trainStart: train[0]?.date ?? '',
      trainEnd: train[train.length - 1]?.date ?? '',
      testStart: test[0]?.date ?? '',
      testEnd: test[test.length - 1]?.date ?? '',
      best: params,
      trainSharpe: sharpeOf(tr.equity),
      trainReturn: Math.round(trRet * 10000) / 10000,
      testSharpe: sharpeOf(te.equity),
      testReturn: Math.round(teRet * 10000) / 10000,
    })
  }
  const isReturn = isComp - 1
  const oosReturn = oosComp - 1
  const gap = isReturn - oosReturn
  let verdict = '样本内外接近，策略相对稳健'
  if (isReturn > 0 && gap > isReturn * 0.5) verdict = '过拟合风险高：样本外收益不足样本内的一半'
  else if (isReturn > 0 && gap > isReturn * 0.25) verdict = '过拟合风险中等：样本外明显衰减'
  else if (oosReturn < 0) verdict = '策略不成立：样本外为负'
  const result = {
    code,
    strategy: spec?.name ?? strategy,
    folds: rows,
    isReturn: Math.round(isReturn * 10000) / 10000,
    oosReturn: Math.round(oosReturn * 10000) / 10000,
    overfitGap: Math.round(gap * 10000) / 10000,
    verdict,
    note: '演示数据：随机游走序列',
  }
  const head =
    '# Walk-forward 报告 · ' + result.strategy + '（' + code + '）\n\n' +
    '- 样本内复利：' + (isReturn * 100).toFixed(2) + '%，样本外复利：' + (oosReturn * 100).toFixed(2) +
    '%，差距：' + (gap * 100).toFixed(2) + '%\n- 结论：' + verdict + '\n\n'
  return { result, report: head }
}

function analyzeMock(code: string, period: string): unknown {
  const bars = makeBars(code, (period as Period) || 'day')
  const last = bars[bars.length - 1]
  const ma5 = maAt(bars, bars.length - 1, 5) ?? 0
  const ma20 = maAt(bars, bars.length - 1, 20) ?? 0
  const ma60 = maAt(bars, bars.length - 1, 60) ?? 0
  const close = last?.close ?? 0
  const trend = close > ma20 && ma20 > ma60 ? '多头排列：价格在 MA20/MA60 上方' : close < ma20 ? '转弱：价格跌破 MA20' : '震荡：均线纠缠'
  return {
    code,
    name: DEMO_STOCKS.find((s) => s.code === code)?.name ?? '演示标的',
    period,
    last,
    summary: {
      trend,
      ma: 'MA5 ' + ma5.toFixed(2) + ' / MA20 ' + ma20.toFixed(2) + ' / MA60 ' + ma60.toFixed(2),
      rsi: 'RSI14 ' + (52 + (seedOf(code) % 18)).toFixed(1) + '，中性偏强',
      macd: 'DIF 在 DEA 上方，红柱缩短',
      atr: 'ATR14 ' + (close * 0.021).toFixed(2) + '（约 2.1% 波动）',
    },
    patterns: [
      { name: '放量突破', index: bars.length - 8, confidence: 0.72, note: '量能 2.1 倍，收盘站上 MA20' },
      { name: '均线多头排列', index: bars.length - 3, confidence: 0.64, note: 'MA5 > MA20 > MA60' },
    ],
    support: [round2(ma20 * 0.995), round2(ma60)],
    resistance: [round2(close * 1.03), round2(close * 1.06)],
  }
}

// ---------- 桥实现 ----------

export function createMockBridge(): GoBridge {
  const status: StatusView = {
    started: true,
    plugins: PLUGINS.length,
    active: PLUGINS.length,
    // 与真实后端一致：8 个插件共 27 个工具（演示桥只登记参与 UI 的一部分）
    tools: 27,
    ui: PLUGINS.length,
    services: ['agent.runtime', 'kb.store', 'kline.engine', 'llm', 'quant.engine', 'watch.engine'],
    policy: {
      networkAllowlist: ['push2.eastmoney.com', 'd.10jqka.com.cn', 'qt.gtimg.cn', 'hq.sinajs.cn'],
      pathRoots: ['~/.guanlan'],
      allowExec: false,
      maxReqPerMin: 60,
      maxToolCalls: 24,
      maxTokensPerRun: 120000,
    },
    dataHealth: { eastmoney: 'ok', ths: 'ok', sina: 'degraded', store: 'ok' },
    time: nowISO(),
  }

  return {
    Plugins: async () => PLUGINS,
    Status: async () => ({ ...status, time: nowISO() }),
    Tools: async () => PLUGINS.flatMap((p) => p.tools),
    Audit: async (limit: number) =>
      [
        { time: '2026-11-10 09:30:12', plugin: 'agentlab', kind: 'tool', action: 'market.quote', ok: true, risk: 'network', elapsedMs: 214, detail: '12 只标的' },
        { time: '2026-11-10 09:30:13', plugin: 'watchlist', kind: 'method', action: 'watch.sync', ok: true, elapsedMs: 88, detail: '同步 12 条' },
        { time: '2026-11-10 09:30:20', plugin: 'knowledge', kind: 'tool', action: 'kb.search', ok: true, risk: 'read', elapsedMs: 12, detail: 'q=均线 命中 5' },
        { time: '2026-11-10 09:31:02', plugin: 'modelsettings', kind: 'method', action: 'test', ok: false, elapsedMs: 8021, detail: 'openai-compat 未配置密钥' },
      ].slice(0, limit),
    CallTool: async (name: string, args: string) => {
      const a = JSON.parse(args || '{}') as Record<string, unknown>
      let out: unknown
      switch (name) {
        case 'watch.list':
          out = watchPayload()
          break
        case 'watch.sync':
          out = syncResult()
          break
        case 'market.quote': {
          const codes = Array.isArray(a.codes) ? a.codes.filter((c): c is string => typeof c === 'string') : []
          tick += 1
          out = codes
            .map((c) => DEMO_STOCKS.find((s) => s.code === c))
            .filter((s): s is DemoStock => s !== undefined)
            .map(quoteOf)
          break
        }
        case 'market.search':
          out = searchStocks(String(a.keyword ?? ''))
          break
        case 'market.bars': {
          const period = (String(a.period ?? 'day') as Period) ?? 'day'
          out = makeBars(String(a.code ?? '600000'), period)
          break
        }
        case 'flow.stock':
          out = flowOf(String(a.code ?? '600000'))
          break
        case 'flow.rank':
          out = rankData(a.kind === 'concept' ? 'concept' : 'sector')
          break
        case 'sector.list':
          out = sectorData(a.kind === 'concept' ? 'concept' : 'sector')
          break
        case 'kline.analyze':
          out = analyzeMock(String(a.code ?? '600519'), String(a.period ?? 'day'))
          break
        case 'quant.backtest': {
          const params: Record<string, number> = {}
          const rawParams = a.params
          if (rawParams && typeof rawParams === 'object' && !Array.isArray(rawParams)) {
            for (const k of Object.keys(rawParams as Record<string, unknown>)) {
              const v = (rawParams as Record<string, unknown>)[k]
              if (typeof v === 'number' && Number.isFinite(v)) params[k] = v
            }
          }
          const run = quantRunMock(String(a.code ?? '600519'), String(a.period ?? 'day'), String(a.strategy ?? 'ma'), params, 100000)
          const rec2 = run as { result: Record<string, unknown>; report: string }
          out = {
            code: a.code,
            strategy: rec2.result.strategy,
            start: rec2.result.start,
            end: rec2.result.end,
            totalReturn: rec2.result.totalReturn,
            cagr: rec2.result.cagr,
            maxDrawdown: rec2.result.maxDrawdown,
            sharpe: rec2.result.sharpe,
            sortino: rec2.result.sortino,
            winRate: rec2.result.winRate,
            tradeCount: rec2.result.tradeCount,
            turnover: rec2.result.turnover,
            costs: rec2.result.costs,
            report: rec2.report,
          }
          break
        }
        case 'kb.search': {
          const q = String(a.query ?? '')
          const hits: KbHit[] = DOC_SUMMARIES.filter((d) => d.title.includes(q) || d.summary.includes(q) || q === '')
            .map((d, i) => ({ id: d.id + ':1', docId: d.id, title: d.title, snippet: d.summary, score: round2(0.95 - i * 0.07) }))
          out = { hits: hits.length > 0 ? hits : DOC_SUMMARIES.slice(0, 3).map((d, i) => ({ id: d.id + ':1', docId: d.id, title: d.title, snippet: d.summary, score: round2(0.88 - i * 0.06) })) }
          break
        }
        case 'kb.open':
          out = DOC_CONTENT[String(a.id ?? 'doc-ma')] ?? DOC_CONTENT['doc-ma']
          break
        case 'kb.list':
          out = { docs: DOC_SUMMARIES, total: DOC_SUMMARIES.length }
          break
        case 'skill.list':
          out = { skills: SKILLS.map((s): SkillInfo => ({ name: s.name, description: s.description, tags: [] })) }
          break
        case 'skill.view': {
          const found = SKILLS.find((s) => s.name === String(a.name ?? '')) ?? SKILLS[0]
          out = { name: found.name, description: found.description, content: found.content, resources: [] }
          break
        }
        case 'skill.read':
          out = { name: String(a.name ?? ''), description: '', content: '# 技能资源\n\n演示环境未提供资源文件。', resources: [] }
          break
        default:
          throw new Error('kernel: 工具不存在 ' + name)
      }
      return JSON.stringify(out)
    },
    Invoke: async (plugin: string, method: string, payload: string) => {
      const p = JSON.parse(payload || '{}') as Record<string, unknown>
      let out: unknown
      if (plugin === 'watchlist') {
        if (method === 'list') out = watchPayload()
        else if (method === 'sync') out = syncResult()
        else if (method === 'groups') out = { groups: ['核心持仓', '观察池', '指数'] }
        else if (method === 'add' || method === 'remove') out = { items: watchItems() }
        else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'fundflow') {
        if (method === 'stock') out = flowOf(String(p.code ?? '600000'))
        else if (method === 'rank') out = rankData(p.kind === 'concept' ? 'concept' : 'sector')
        else if (method === 'sectors') out = sectorData(p.kind === 'concept' ? 'concept' : 'sector')
        else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'kline') {
        if (method === 'analyze') out = analyzeMock(String(p.code ?? '600519'), String(p.period ?? 'day'))
        else if (method === 'bars') {
          const code = String(p.code ?? '600519')
          const period = (String(p.period ?? 'day') as Period) ?? 'day'
          const bars = makeBars(code, period)
          out = {
            code,
            name: DEMO_STOCKS.find((s) => s.code === code)?.name ?? '演示标的',
            period,
            bars,
            indicators: {
              ma5: ma(bars, 5),
              ma10: ma(bars, 10),
              ma20: ma(bars, 20),
              ma60: ma(bars, 60),
              volume: bars.map((b) => b.volume),
            },
          }
        } else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'modelsettings') {
        if (method === 'list') {
          out = { providers: models, defaultId: models.find((m) => m.isDefault)?.id ?? '' }
        } else if (method === 'save') {
          const id = String(p.id ?? '') || String(p.provider ?? 'deepseek') + ':' + String(p.model ?? '')
          const setDefault = p.setDefault === true
          if (setDefault) models.forEach((m) => (m.isDefault = false))
          const idx = models.findIndex((m) => m.id === id)
          const next: ModelInfo = {
            id,
            provider: String(p.provider ?? 'deepseek'),
            model: String(p.model ?? ''),
            displayName: String(p.name ?? '') || String(p.provider ?? '') + ' · ' + String(p.model ?? ''),
            baseUrl: p.baseUrl ? String(p.baseUrl) : undefined,
            hasKey: p.apiKey ? true : idx >= 0 ? models[idx]?.hasKey === true : false,
            isDefault: setDefault || (idx >= 0 ? models[idx]?.isDefault === true : false),
            maxTokens: typeof p.maxTokens === 'number' ? p.maxTokens : undefined,
          }
          if (idx >= 0) models[idx] = next
          else models.push(next)
          out = { providers: models, defaultId: models.find((m) => m.isDefault)?.id ?? '' }
        } else if (method === 'test') {
          await sleep(640)
          out = { ok: true, latencyMs: 612, message: '连通正常，模型返回 1 个 token' }
        } else if (method === 'remove') {
          const id = String(p.id ?? '')
          const idx = models.findIndex((m) => m.id === id)
          if (idx >= 0) models.splice(idx, 1)
          out = { providers: models, defaultId: models.find((m) => m.isDefault)?.id ?? '' }
        } else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'watch') {
        if (method === 'list') out = { rules: watchRules, running: true, metrics: WATCH_METRICS, logs: WATCH_LOGS }
        else if (method === 'metrics') out = { metrics: WATCH_METRICS }
        else if (method === 'state') out = { running: true, rules: watchRules.length }
        else if (method === 'templates') out = { templates: WATCH_TEMPLATES, metrics: WATCH_METRICS }
        else if (method === 'logs') out = { logs: WATCH_LOGS.slice(0, typeof p.limit === 'number' ? p.limit : 50) }
        else if (method === 'save') {
          const raw = p as Record<string, unknown>
          const rule: WatchRule = {
            id: typeof raw.id === 'string' && raw.id !== '' ? raw.id : 'w' + Date.now().toString(36),
            name: String(raw.name ?? '未命名规则'),
            description: raw.description ? String(raw.description) : undefined,
            enabled: raw.enabled === true,
            sources: Array.isArray(raw.sources) ? (raw.sources as WatchSource[]) : [],
            condition: String(raw.condition ?? 'true'),
            action: {
              worker: String((raw.action as Record<string, unknown> | undefined)?.worker ?? 'intraday'),
              prompt: String((raw.action as Record<string, unknown> | undefined)?.prompt ?? ''),
            },
            cooldownSeconds: typeof raw.cooldownSeconds === 'number' ? raw.cooldownSeconds : 300,
            quietWindows: Array.isArray(raw.quietWindows) ? (raw.quietWindows as string[]) : [],
            createdBy: 'user',
            createdAt: nowISO(),
            updatedAt: nowISO(),
          }
          const idx = watchRules.findIndex((r) => r.id === rule.id)
          if (idx >= 0) watchRules[idx] = { ...watchRules[idx], ...rule }
          else watchRules = [rule, ...watchRules]
          out = { rule, rules: watchRules }
        } else if (method === 'toggle') {
          const id = String(p.id ?? '')
          const enabled = p.enabled === true
          watchRules = watchRules.map((r) => (r.id === id ? { ...r, enabled } : r))
          out = { rule: watchRules.find((r) => r.id === id) ?? watchRules[0] }
        } else if (method === 'remove') {
          const id = String(p.id ?? '')
          watchRules = watchRules.filter((r) => r.id !== id)
          out = { rules: watchRules }
        } else if (method === 'test') {
          out = watchTestMock(String(p.id ?? watchRules[0]?.id ?? ''))
        } else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'quant') {
        const code = String(p.code ?? '600519')
        const strategy = String(p.strategy ?? 'ma')
        if (method === 'strategies') out = { strategies: QUANT_STRATEGIES }
        else if (method === 'run') {
          const rawParams = p.params
          const params: Record<string, number> = {}
          if (rawParams && typeof rawParams === 'object' && !Array.isArray(rawParams)) {
            for (const k of Object.keys(rawParams as Record<string, unknown>)) {
              const v = (rawParams as Record<string, unknown>)[k]
              if (typeof v === 'number' && Number.isFinite(v)) params[k] = v
            }
          }
          const cash = typeof p.initialCash === 'number' && p.initialCash > 0 ? p.initialCash : 100000
          out = quantRunMock(code, String(p.period ?? 'day'), strategy, params, cash)
        } else if (method === 'walkforward') {
          out = walkForwardMock(code, strategy, typeof p.folds === 'number' ? p.folds : 3)
        } else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'agentlab') {
        if (method === 'workers') out = WORKERS
        else if (method === 'run') out = await mockRun(String(p.worker ?? 'chat'), String(p.input ?? ''))
        else if (method === 'stop') {
          stopped = true
          out = { ok: true }
        } else if (method === 'sessions') out = sessions
        else if (method === 'session') {
          const id = String(p.sessionId ?? sessions[0]?.id ?? '')
          const session = sessions.find((s) => s.id === id) ?? sessions[0]
          out = {
            session,
            events: [
              { seq: 1, time: '09:30:12', kind: 'state', content: '开始读取自选', ok: true },
              { seq: 2, time: '09:30:13', kind: 'tool', content: 'market.quote → 12 只标的', toolName: 'market.quote', ok: true },
              { seq: 3, time: '09:30:18', kind: 'report', content: REPORTS[id] ?? REPORTS['s-20261110-0930'] ?? '', ok: true },
            ],
          }
        } else throw new Error('kernel: 方法不存在 ' + method)
      } else if (plugin === 'knowledge') {
        if (method === 'list') out = { docs: DOC_SUMMARIES, total: DOC_SUMMARIES.length }
        else if (method === 'search') {
          const q = String(p.query ?? '')
          const hits = DOC_SUMMARIES.filter((d) => q === '' || d.title.includes(q) || d.summary.includes(q)).map((d, i) => ({
            id: d.id + ':1',
            docId: d.id,
            title: d.title,
            snippet: d.summary,
            score: round2(0.95 - i * 0.07),
          }))
          out = { hits: hits.length > 0 ? hits : DOC_SUMMARIES.map((d, i) => ({ id: d.id + ':1', docId: d.id, title: d.title, snippet: d.summary, score: round2(0.86 - i * 0.06) })) }
        } else if (method === 'open') out = DOC_CONTENT[String(p.id ?? 'doc-ma')] ?? DOC_CONTENT['doc-ma']
        else if (method === 'import') out = { id: 'doc-demo', title: String(p.title ?? '演示导入'), chars: String(p.content ?? '').length }
        else if (method === 'skills') out = { skills: SKILLS.map((s) => ({ name: s.name, description: s.description, tags: [] })) }
        else if (method === 'skill') {
          const found = SKILLS.find((s) => s.name === String(p.name ?? '')) ?? SKILLS[0]
          out = { name: found.name, description: found.description, content: found.content, resources: [] }
        } else throw new Error('kernel: 方法不存在 ' + method)
      } else {
        throw new Error('kernel: 插件不存在 ' + plugin)
      }
      return JSON.stringify(out)
    },
  }
}