// 数字与时间的统一格式化。所有函数对 undefined / NaN 返回占位符 "—"，
// 且把 0 当作有效值（0.00% 而不是 "—"）。A 股语义：红涨绿跌。

const DASH = '—'

function valid(v: number | null | undefined): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

/** 固定小数位；非数字返回占位符。 */
export function fmtNum(v: number | null | undefined, digits = 2): string {
  if (!valid(v)) return DASH
  return v.toFixed(digits)
}

/** 价格：两位小数（大额也保持两位，和行情终端一致）。 */
export function fmtPrice(v: number | null | undefined): string {
  return fmtNum(v, 2)
}

/** 带符号数字：+1.23 / -1.23 / 0.00。 */
export function fmtSigned(v: number | null | undefined, digits = 2): string {
  if (!valid(v)) return DASH
  const s = v.toFixed(digits)
  return v > 0 ? '+' + s : s
}

/** 涨跌幅：+2.34% / -1.08% / 0.00%。 */
export function fmtPct(v: number | null | undefined, digits = 2): string {
  if (!valid(v)) return DASH
  const s = Math.abs(v).toFixed(digits)
  if (v > 0) return '+' + s + '%'
  if (v < 0) return '-' + s + '%'
  return s + '%'
}

/** 金额（元）→ 万/亿/万亿，小数位随量级收敛（12.3亿 / 5160亿 / 8600万）。 */
export function fmtAmount(v: number | null | undefined, digits?: number): string {
  if (!valid(v)) return DASH
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs >= 1e12) return sign + (abs / 1e12).toFixed(digits ?? 2) + '万亿'
  if (abs >= 1e8) {
    const d = digits ?? (abs >= 1e10 ? 0 : 1)
    return sign + (abs / 1e8).toFixed(d) + '亿'
  }
  if (abs >= 1e4) {
    const d = digits ?? (abs >= 1e6 ? 0 : 1)
    return sign + (abs / 1e4).toFixed(d) + '万'
  }
  return sign + abs.toFixed(digits ?? 0)
}

/** 带符号金额：+2.3亿 / -8600万 / 0。 */
export function fmtSignedAmount(v: number | null | undefined, digits?: number): string {
  if (!valid(v)) return DASH
  if (v === 0) return '0'
  const s = fmtAmount(Math.abs(v), digits)
  return (v > 0 ? '+' : '-') + s
}

/** 成交量（手，契约单位）→ 手 / 万手（1 位小数）/ 亿手。 */
export function fmtVolume(v: number | null | undefined, digits?: number): string {
  if (!valid(v)) return DASH
  const abs = Math.abs(v)
  if (abs >= 1e8) return (v / 1e8).toFixed(digits ?? 2) + '亿手'
  if (abs >= 1e4) return (v / 1e4).toFixed(digits ?? 1) + '万手'
  if (abs >= 1) return v.toFixed(digits ?? 0) + '手'
  return v.toFixed(2) + '手'
}

/** 涨跌方向 → CSS 类名（A 股：涨红跌绿）。 */
export function dir(v: number | null | undefined): 'up' | 'down' | 'flat' {
  if (!valid(v) || v === 0) return 'flat'
  return v > 0 ? 'up' : 'down'
}

/** 时间戳/ISO → HH:MM:SS。无法解析时原样返回短串。 */
export function fmtClock(v: string | null | undefined): string {
  if (!v) return DASH
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v.length > 19 ? v.slice(11, 19) : v
  const p = (n: number) => String(n).padStart(2, '0')
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}

/** 时间戳/ISO → MM-DD HH:mm。 */
export function fmtDateTime(v: string | null | undefined): string {
  if (!v) return DASH
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  const p = (n: number) => String(n).padStart(2, '0')
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes())
}

/** 代码 → 交易所中文名（展示用）。 */
export function marketName(market: string | undefined): string {
  switch (market) {
    case 'sh':
      return '沪'
    case 'sz':
      return '深'
    case 'bj':
      return '京'
    default:
      return ''
  }
}

/** 依据代码前缀推断交易所，后端未给 market 时兜底。 */
export function inferMarket(code: string): 'sh' | 'sz' | 'bj' {
  if (code.startsWith('6') || code.startsWith('5') || code.startsWith('9')) return 'sh'
  if (code.startsWith('4') || code.startsWith('8')) return 'bj'
  return 'sz'
}

/** 把用户输入规范成 6 位代码（容忍 sh600000 / 600000.SH 等写法）。 */
export function normalizeCode(raw: string): string {
  const digits = raw.replace(/[^0-9]/g, '')
  return digits.slice(0, 6)
}

/** 截断长文本（日志、片段）。 */
export function clip(text: string, max: number): string {
  if (text.length <= max) return text
  return text.slice(0, max) + '…'
}