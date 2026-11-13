#!/usr/bin/env bash
# 观澜 · 数据源网络冒烟（scripts/smoke.sh）
#
# 作用：对 docs/PLAN.md §4 的公开行情端点做「形状断言」，
#       验证端点还活着、返回结构没变。任何失败只打印 WARN，绝不让脚本失败。
#
# 用法: scripts/smoke.sh [--timeout=8] [--only=关键字]
# 退出码: 0 = 脚本执行完成（不代表所有端点都通，看 PASS/WARN 统计）
#         2 = 参数错误或缺少 curl
#
# 注意：这是网络冒烟，不是验收门禁。数据层真伪由 test/ 的 fixture 测试负责。
# 网络/风控/端点变更都会导致 WARN，复跑确认后再判断是不是产品问题。
set -uo pipefail

TIMEOUT=8
ONLY=""
UA="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

usage() {
  cat <<'USAGE'
观澜 · 数据源网络冒烟

对 docs/PLAN.md §4 的公开行情端点做形状断言，验证端点还活着、返回结构没变。
任何失败只打印 WARN，绝不让脚本失败（网络/风控/端点变更都会导致 WARN，复跑确认）。

用法: scripts/smoke.sh [选项]

选项:
  --timeout=<秒>   单请求超时，默认 8（与 Guard 的 8s 网络超时一致）
  --only=<关键字>  只跑名称含该关键字的检查，例如 --only=腾讯
  -h, --help       显示本帮助

退出码: 0 = 脚本执行完成（不代表所有端点都通，看 PASS/WARN 统计）；2 = 参数错误或缺少 curl
USAGE
}

for arg in "$@"; do
  case "$arg" in
    --timeout=*) TIMEOUT="${arg#*=}" ;;
    --only=*) ONLY="${arg#*=}" ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

if ! command -v curl >/dev/null 2>&1; then
  echo "缺少 curl，无法做网络冒烟" >&2
  exit 2
fi

PASSED=0
WARNED=0

pass() { PASSED=$((PASSED + 1)); printf 'PASS  %-24s %s\n' "$1" "$2"; }
warn() { WARNED=$((WARNED + 1)); printf 'WARN  %-24s %s\n' "$1" "$2"; }

should_run() {
  [ -z "$ONLY" ] && return 0
  case "$1" in
    *"$ONLY"*) return 0 ;;
    *) return 1 ;;
  esac
}

# 统一请求：结果写进 RESP_BODY / RESP_CODE（RESP_CODE 为 curl 退出码）
RESP_BODY=""
RESP_CODE=0
request() {
  RESP_BODY="$(curl -sS --max-time "$TIMEOUT" --compressed \
    -A "$UA" \
    -H 'Referer: https://stockpage.10jqka.com.cn/' \
    -H 'Accept: */*' \
    "$1" 2>/dev/null)"
  RESP_CODE=$?
}

# 断言：响应体必须包含字面量（不用正则，避免转义踩坑）
contains() { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

bytes() { printf '%s' "$1" | wc -c | tr -d ' '; }

# ---------- 1. 同花顺 K 线（JSONP） ----------
check_ths_kline() {
  local name='THS K线 JSONP'
  local url='https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 ${RESP_CODE}，超时 ${TIMEOUT}s）"
    return 0
  fi
  size="$(bytes "$body")"
  if [ "$size" -lt 200 ]; then
    warn "$name" "响应过短（${size}B），可能被拦截"
    return 0
  fi
  if ! printf '%s' "$body" | grep -Eq '^[A-Za-z0-9_]+\(\{'; then
    warn "$name" "不是 JSONP 包裹（应为 函数名({...}），前 40 字节: ${body:0:40}"
    return 0
  fi
  if ! contains "$body" '"data"' || ! contains "$body" '"total"'; then
    warn "$name" "缺少 data/total 字段（${size}B）"
    return 0
  fi
  pass "$name" "${size}B，JSONP + data/total 正常"
}

# ---------- 2. 新浪板块资金流排名（ssl_bkzj_bk） ----------
check_sina_bkzj_rank() {
  local name='新浪板块资金榜'
  local url='https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_bkzj_bk?page=1&num=100&sort=netamount&asc=0&fenlei=0'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 $RESP_CODE，超时 $TIMEOUT 秒）"
    return 0
  fi
  size="$(bytes "$body")"
  if ! contains "$body" '"category"'; then
    warn "$name" "缺少 category（新浪板块代码，$size 字节）"
    return 0
  fi
  if ! contains "$body" '"netamount"'; then
    warn "$name" "缺少 netamount（资金净额）"
    return 0
  fi
  pass "$name" "$size 字节，category/netamount 正常"
}

# ---------- 3. 新浪个股多日资金流（ssl_qsfx_zjlrqs） ----------
check_sina_flow_history() {
  local name='新浪多日资金流'
  local url='https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_zjlrqs?page=1&num=20&sort=opendate&asc=0&daima=sh600000'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 $RESP_CODE，超时 $TIMEOUT 秒）"
    return 0
  fi
  size="$(bytes "$body")"
  if [ "$body" = "null" ] || [ -z "$body" ]; then
    warn "$name" "返回 null/空（代码或参数可能变了）"
    return 0
  fi
  if ! contains "$body" '"opendate"'; then
    warn "$name" "缺少 opendate（$size 字节）"
    return 0
  fi
  if ! contains "$body" '"r0_net"'; then
    warn "$name" "缺少 r0_net（超大单净额）"
    return 0
  fi
  pass "$name" "$size 字节，opendate/r0_net 正常"
}

# ---------- 4. 新浪个股资金流（兜底） ----------
check_sina_moneyflow() {
  local name='新浪 moneyflow'
  local url='https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssi_ssfx_flzjtj?daima=sh600000'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 ${RESP_CODE}，超时 ${TIMEOUT}s）"
    return 0
  fi
  size="$(bytes "$body")"
  if [ "$body" = "null" ] || [ -z "$body" ]; then
    warn "$name" "返回 null/空（可能需要登录态或代码参数变更）"
    return 0
  fi
  if ! contains "$body" 'r0'; then
    warn "$name" "缺少 r0 字段（超大单净额）"
    return 0
  fi
  pass "$name" "${size}B，含 r0 资金字段"
}

# ---------- 5. 腾讯快照（GBK 兜底） ----------
check_tencent_snapshot() {
  local name='腾讯快照 GBK'
  local url='https://qt.gtimg.cn/q=sh600000'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size tilde
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 ${RESP_CODE}，超时 ${TIMEOUT}s）"
    return 0
  fi
  size="$(bytes "$body")"
  if ! contains "$body" 'v_sh600000='; then
    warn "$name" "缺少 v_sh600000= 前缀（${size}B）"
    return 0
  fi
  # GBK 字节不是合法 UTF-8，必须用 LC_ALL=C 才不会被 tr 拒绝
  tilde="$(printf '%s' "$body" | LC_ALL=C tr -cd '~' | wc -c | tr -d ' ')"
  if [ "$tilde" -lt 30 ]; then
    warn "$name" "分隔符过少（${tilde} 个 ~，期望 ≥30），字段结构可能变了"
    return 0
  fi
  pass "$name" "${size}B，${tilde} 个字段分隔符"
}

# ---------- 附加：腾讯 K 线后复权（hfq，替换旧东财 K 线检查） ----------
check_tencent_hfq() {
  local name='腾讯 hfq 后复权'
  local url='https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=sh600000,day,,,30,hfq'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 $RESP_CODE，超时 $TIMEOUT 秒）"
    return 0
  fi
  size="$(bytes "$body")"
  if ! contains "$body" '"hfqday"'; then
    warn "$name" "缺少 hfqday 键（$size 字节，后复权可能已下线）"
    return 0
  fi
  pass "$name" "$size 字节，data 下 hfqday 正常"
}

# ---------- 附加：同花顺涨停池（limit_up_pool） ----------
check_ths_limit_up() {
  local name='同花顺涨停池'
  # data.10jqka.com.cn 必须带浏览器 UA + 同域 Referer，否则被 Nginx 403
  local url='https://data.10jqka.com.cn/dataapi/limit_up/limit_up_pool?page=1&limit=200&field=10,9001,9002,330323,330324,330325,330329,199112,133971,133970,1968584&filter=HS,GEM2STAR&order_field=330324&order_type=0'
  should_run "$name" || return 0
  request "$url" 'https://data.10jqka.com.cn/market/longhu/'
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 $RESP_CODE，超时 $TIMEOUT 秒）"
    return 0
  fi
  size="$(bytes "$body")"
  if ! contains "$body" '"limit_up_count"'; then
    warn "$name" "缺少 limit_up_count 汇总（$size 字节）"
    return 0
  fi
  if ! contains "$body" '"open_num"'; then
    warn "$name" "缺少 open_num（炸板数口径）"
    return 0
  fi
  pass "$name" "$size 字节，limit_up_count/open_num 正常"
}

# ---------- 附加：同花顺龙虎榜（GBK HTML 表格） ----------
# 龙虎榜只在收盘后才发布，直接查「今天」在盘中必然是空表（0 字节）。
# 产品代码 fetchMarketLongHu 会自己往前回退到最近有数据的交易日，
# 所以这里也必须**沿着同一条回退路径**验证，否则冒烟测的是「今天有没有收盘」
# 而不是「端点还能不能解析」。回退窗口与产品一致：最多 5 个自然日内的工作日。
check_ths_longhu() {
  local name='同花顺龙虎榜'
  local offset day url body size rows tried=0
  should_run "$name" || return 0
  for offset in 0 1 2 3 4 5 6; do
    if [ "$tried" -ge 5 ]; then
      break
    fi
    day="$(date -v-${offset}d '+%Y-%m-%d' 2>/dev/null || date -d "-${offset} day" '+%Y-%m-%d')"
    url="https://data.10jqka.com.cn/ifmarket/lhbtable/report/$day/1"
    request "$url" 'https://data.10jqka.com.cn/market/longhu/'
    if [ "$RESP_CODE" -ne 0 ]; then
      warn "$name" "请求失败（curl 退出码 $RESP_CODE，超时 $TIMEOUT 秒）"
      return 0
    fi
    body="$RESP_BODY"
    size="$(bytes "$body")"
    # 表格是 GBK，中文在 UTF-8 下不可读；只断言结构：m-table 与 tr 行数。
    # 空表（当日未发布/非交易日）继续往前找，不算失败。
    if contains "$body" 'm-table'; then
      rows="$(printf '%s' "$body" | LC_ALL=C grep -o '<tr' | wc -c | tr -d ' ')"
      if [ -n "$rows" ] && [ "$rows" -ge 1 ]; then
        pass "$name" "${day}：${size} 字节，${rows} 个 tr 起始标记"
        return 0
      fi
    fi
    tried=$((tried + 1))
  done
  warn "$name" "最近 ${tried} 个自然日都没有数据（最后试 ${day}，${size} 字节）：可能全程休市或页面改版"
}


printf '观澜 · 数据源冒烟 %s（超时 %ss）\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$TIMEOUT"
printf '%s\n' '────────────────────────────────────────────────────────────'

check_ths_kline
check_sina_bkzj_rank
check_sina_flow_history
check_sina_moneyflow
check_tencent_snapshot
check_tencent_hfq
check_ths_limit_up
check_ths_longhu

printf '%s\n' '────────────────────────────────────────────────────────────'
printf '统计: PASS %d，WARN %d\n' "$PASSED" "$WARNED"
if [ "$WARNED" -eq 0 ]; then
  printf '结论: 全部端点形状正常\n'
else
  printf '结论: 有 %d 个端点降级（WARN 不代表产品 bug：可能是网络/风控/端点变更，复跑确认）\n' "$WARNED"
fi
exit 0
