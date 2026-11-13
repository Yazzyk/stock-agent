// 市场中心数据访问层：一次取齐指数、市场宽度、涨停梯队与龙虎榜。
// 后端是 plugins/marketcenter 的 snapshot 方法；这里只做形状归一，缺字段给安全默认。

import { friendlyError, invoke } from '../api'
import { pickArr, pickNum, pickStr, rec, strList } from './parse'
import type { HotStock, LimitUpStock, LongHuRow, MarketBreadth, MarketIndex } from '../types'

/** 涨停梯队的金额口径：由后端 amountKind 标注（同花顺涨停池给的是封单额 order_amount）。 */
export interface MarketLadderStock extends LimitUpStock {
  amountKind?: string
}

/** 市场中心快照（四块数据 + 更新时间 + 降级提示）。 */
export interface MarketSnapshot {
  indices: MarketIndex[]
  breadth: MarketBreadth
  ladder: MarketLadderStock[]
  longhu: LongHuRow[]
  hot: HotStock[]
  updatedAt: string
  warnings: string[]
}

/** 把任意对象归一为指数；缺失字段用空串/0 兜底。 */
function toIndex(v: unknown): MarketIndex {
  const r = rec(v) ?? {}
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    price: pickNum(r, 'price') ?? 0,
    change: pickNum(r, 'change') ?? 0,
    changePct: pickNum(r, 'changePct') ?? 0,
    amount: pickNum(r, 'amount'),
  }
}

/** 把任意对象归一为市场宽度；所有计数缺省为 0。 */
function toBreadth(v: unknown): MarketBreadth {
  const r = rec(v) ?? {}
  return {
    date: pickStr(r, 'date') ?? '',
    up: pickNum(r, 'up') ?? 0,
    down: pickNum(r, 'down') ?? 0,
    flat: pickNum(r, 'flat') ?? 0,
    limitUp: pickNum(r, 'limitUp') ?? 0,
    limitDown: pickNum(r, 'limitDown') ?? 0,
    broken: pickNum(r, 'broken') ?? 0,
    brokenRate: pickNum(r, 'brokenRate') ?? 0,
    amount: pickNum(r, 'amount'),
  }
}

/** 把任意对象归一为涨停梯队的一行。 */
function toLimitUp(v: unknown): MarketLadderStock {
  const r = rec(v) ?? {}
  const boards = pickNum(r, 'boards') ?? 1
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    price: pickNum(r, 'price') ?? 0,
    changePct: pickNum(r, 'changePct') ?? 0,
    boards: boards < 1 ? 1 : Math.round(boards),
    firstTime: pickStr(r, 'firstTime'),
    lastTime: pickStr(r, 'lastTime'),
    reason: pickStr(r, 'reason'),
    amount: pickNum(r, 'amount'),
    amountKind: pickStr(r, 'amountKind'),
    turnover: pickNum(r, 'turnover'),
  }
}

/** 把任意对象归一为龙虎榜的一行。 */
function toLongHu(v: unknown): LongHuRow {
  const r = rec(v) ?? {}
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    price: pickNum(r, 'price') ?? 0,
    changePct: pickNum(r, 'changePct') ?? 0,
    reason: pickStr(r, 'reason') ?? '',
    netBuy: pickNum(r, 'netBuy') ?? 0,
    buy: pickNum(r, 'buy') ?? 0,
    sell: pickNum(r, 'sell') ?? 0,
    date: pickStr(r, 'date') ?? '',
  }
}

/** 把任意对象归一为热榜条目。 */
function toHot(v: unknown): HotStock {
  const r = rec(v) ?? {}
  const tags = strList(pickArr(r, 'conceptTags')).filter((t) => t !== '')
  return {
    code: pickStr(r, 'code') ?? '',
    name: pickStr(r, 'name') ?? '',
    market: pickStr(r, 'market'),
    price: pickNum(r, 'price'),
    changePct: pickNum(r, 'changePct') ?? 0,
    heat: pickNum(r, 'heat') ?? 0,
    rank: pickNum(r, 'rank') ?? 0,
    rankChange: pickNum(r, 'rankChange') ?? 0,
    conceptTags: tags.length > 0 ? tags : undefined,
    popularityTag: pickStr(r, 'popularityTag'),
    reason: pickStr(r, 'reason'),
  }
}

export async function marketSnapshot(ladderLimit = 200, longhuLimit = 100): Promise<MarketSnapshot> {
  try {
    const r = rec(await invoke('marketcenter', 'snapshot', { ladderLimit, longhuLimit })) ?? {}
    return {
      indices: pickArr(r, 'indices').map(toIndex).filter((item) => item.code !== '' || item.name !== ''),
      breadth: toBreadth(r.breadth),
      ladder: pickArr(r, 'ladder').map(toLimitUp).filter((item) => item.code !== ''),
      longhu: pickArr(r, 'longhu').map(toLongHu).filter((item) => item.code !== ''),
      hot: pickArr(r, 'hot').map(toHot).filter((item) => item.code !== ''),
      updatedAt: pickStr(r, 'updatedAt') ?? '',
      warnings: strList(r.warnings).filter((text) => text !== ''),
    }
  } catch (e) {
    throw new Error(friendlyError(e, '读取市场中心'))
  }
}

/** 单独刷新同花顺热门榜（小时/日榜）；面板切换榜单时用。 */
export async function marketHot(type: 'hour' | 'day', limit = 30): Promise<HotStock[]> {
  try {
    const v = await invoke('marketcenter', 'hot', { type, limit })
    // 方法是直接返回数组；工具才包 {items}。两种形状都兼容。
    const raw = Array.isArray(v) ? v : pickArr(rec(v) ?? {}, 'items')
    return raw.map(toHot).filter((item) => item.code !== '')
  } catch (e) {
    throw new Error(friendlyError(e, '读取同花顺热门榜'))
  }
}
