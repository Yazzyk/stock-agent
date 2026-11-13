// 板块标签：把 6 位 A 股代码归到「沪 / 深 / 创 / 科 / 北」五类。
// 只做展示用的小标签，不参与任何业务判断。

/** 标签 → 中文全称（给 title / 无障碍文案用）。 */
export const BOARD_TITLES: Record<string, string> = {
  沪: '沪市',
  深: '深市',
  创: '创业板',
  科: '科创板',
  北: '北交所',
}

/**
 * 按代码判定所属板块，返回展示用标签（顺序：科 → 创 → 北 → 沪 → 深）。
 *
 * 判定表（自上而下，命中即返回）：
 * - 688 / 689        → ['科']（科创板，属沪市，所以不再叠「沪」）
 * - 30 开头（300/301/302）→ ['创']（创业板，属深市，所以不再叠「深」）
 * - 8 / 4 / 920 开头，或 market === 'bj' → ['北']（北交所）
 * - market === 'sh' → ['沪']；market === 'sz' → ['深']
 * - market 缺失时兜底：6 开头 = 沪，0 / 3 开头 = 深
 * - 其它（B 股、基金、无法识别的脏数据）→ []：宁可不显示，也不给错标签
 *
 * 为什么用前缀而不是接口字段：
 * 1. 契约里的 market 只有 sh / sz / bj，表达不了「主板 / 创业板 / 科创板 / 北交所」
 *    这种展示粒度；科创板与沪市主板同为 market === 'sh'，只看字段必然分不开。
 * 2. market 在多条链路里是可缺省的（历史快照、缓存条目、前端本地兜底），
 *    只信字段会让同一只票在不同状态下标签跳变；代码前缀是交易所分配的稳定事实。
 * 3. 前缀判定是纯函数，不依赖网络与快照版本，表格里逐行调用也不会产生额外请求。
 * 注意：market 只作为补充信号（北交所与兜底），不推翻前缀结论。
 */
export function boardTags(code: string, market?: string): string[] {
  const c = (code ?? '').replace(/[^0-9]/g, '')
  if (c.length < 3) return []
  const m = (market ?? '').toLowerCase()

  if (c.startsWith('688') || c.startsWith('689')) return ['科']
  if (c.startsWith('30')) return ['创']
  if (m === 'bj' || c.startsWith('8') || c.startsWith('4') || c.startsWith('920')) return ['北']
  if (m === 'sh') return ['沪']
  if (m === 'sz') return ['深']
  if (c.startsWith('6')) return ['沪']
  if (c.startsWith('0') || c.startsWith('3')) return ['深']
  return []
}

/** 标签 → 中文全称；未知标签原样返回，避免出现空白 title。 */
export function boardTitle(tag: string): string {
  return BOARD_TITLES[tag] ?? tag
}
