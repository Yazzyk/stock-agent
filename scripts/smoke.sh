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

# ---------- 2. 东财板块资金流排名（bkzj） ----------
check_eastmoney_bkzj() {
  local name='东财 bkzj 板块资金'
  local url='https://data.eastmoney.com/dataapi/bkzj/getbkzj?key=f62&code=m:90+t:2'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 ${RESP_CODE}，超时 ${TIMEOUT}s）"
    return 0
  fi
  size="$(bytes "$body")"
  if ! contains "$body" '"diff"'; then
    warn "$name" "缺少 diff 列表（${size}B）"
    return 0
  fi
  if ! contains "$body" '"f62"'; then
    warn "$name" "缺少字段 f62（主力净额）"
    return 0
  fi
  if ! contains "$body" '"f14"'; then
    warn "$name" "缺少字段 f14（板块名称）"
    return 0
  fi
  pass "$name" "${size}B，diff/f62/f14 正常"
}

# ---------- 3. 东财个股资金流（push2 fflow） ----------
check_push2_fflow() {
  local name='push2 fflow 个股资金'
  local url='https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?secid=1.600000&fields1=f1,f2,f3,f7&fields2=f51,f52,f53,f54,f55,f56&klt=101&lmt=10'
  should_run "$name" || return 0
  request "$url"
  local body="$RESP_BODY" size
  if [ "$RESP_CODE" -ne 0 ]; then
    warn "$name" "请求失败（curl 退出码 ${RESP_CODE}，超时 ${TIMEOUT}s）"
    return 0
  fi
  size="$(bytes "$body")"
  if contains "$body" '"data":null'; then
    warn "$name" "返回 data=null（secid 或参数可能变了）"
    return 0
  fi
  if ! contains "$body" '"klines"'; then
    warn "$name" "缺少 klines 字段（${size}B）"
    return 0
  fi
  pass "$name" "${size}B，data.klines 正常"
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

# ---------- 附加：东财 K 线（push2his / push2 两个主机都试） ----------
check_eastmoney_kline() {
  local name='东财 K线 https'
  should_run "$name" || return 0
  local query='secid=1.600000&fields1=f1,f2,f3,f4,f5,f6&fields2=f51,f52,f53,f54,f55,f56,f57&klt=101&fqt=1&lmt=5'
  local hosts='https://push2his.eastmoney.com/api/qt/stock/kline/get https://push2.eastmoney.com/api/qt/stock/kline/get'
  local tries="" host
  for host in $hosts; do
    request "${host}?${query}"
    if [ "$RESP_CODE" -ne 0 ]; then
      tries="${tries}${host} curl=${RESP_CODE}; "
      continue
    fi
    local body="$RESP_BODY" size
    size="$(bytes "$body")"
    if contains "$body" '"klines"'; then
      pass "$name" "${host} ${size}B，data.klines 正常"
      return 0
    fi
    tries="${tries}${host} 无 klines(${size}B); "
  done
  warn "$name" "两个主机均失败: ${tries}"
}

printf '观澜 · 数据源冒烟 %s（超时 %ss）\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$TIMEOUT"
printf '%s\n' '────────────────────────────────────────────────────────────'

check_ths_kline
check_eastmoney_bkzj
check_push2_fflow
check_sina_moneyflow
check_tencent_snapshot
check_eastmoney_kline

printf '%s\n' '────────────────────────────────────────────────────────────'
printf '统计: PASS %d，WARN %d\n' "$PASSED" "$WARNED"
if [ "$WARNED" -eq 0 ]; then
  printf '结论: 全部端点形状正常\n'
else
  printf '结论: 有 %d 个端点降级（WARN 不代表产品 bug：可能是网络/风控/端点变更，复跑确认）\n' "$WARNED"
fi
exit 0
