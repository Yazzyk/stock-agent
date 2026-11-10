#!/usr/bin/env bash
# 观澜 · 一键验收脚本（退出码即结论）
#
# 用法:
#   scripts/verify.sh                  默认 7 步：build / vet / test / 前端构建 / selftest / 工具索引
#   scripts/verify.sh --with-wails     追加 wails build -clean（会删 frontend/dist，仅集成阶段用）
#   scripts/verify.sh --skip-frontend  跳过前端构建（临时排障用，正式验收不要跳过）
#   scripts/verify.sh --verbose        每步都打印尾部输出（默认只打印失败摘要）
#   scripts/verify.sh --keep-logs      保留日志目录便于复盘
#
# 退出码: 0 = 全部 PASS；1 = 有步骤 FAIL；2 = 参数错误或缺少必要工具链
#
# 说明:
# - 只读验证：不修改产品代码，不跑 go mod tidy，不跑 git。
# - 前端构建会重写 frontend/dist（Wails 的 go:embed 依赖它），这是验收的一部分。
# - 日志写在临时目录，默认用 trash 清理（macOS 可恢复），没有 trash 才 rm 自己的临时目录。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 2

WITH_WAILS=0
SKIP_FRONTEND=0
WITH_RACE=0
WITH_NETWORK=0
VERBOSE=0
KEEP_LOGS=0

# EXPECT_PLUGINS 是"插件装配数下限"，默认按最终验收的 7 个内置插件
# （watchlist / fundflow / kline / agentlab / knowledge / modelsettings / quant）。
# 交付中期插件尚未全接线时必然不达标——这是"集成末期生效"的门禁，
# 需要中期跑绿可以显式传 --expect-plugins=0 关掉该断言。
EXPECT_PLUGINS=7

usage() {
  cat <<'USAGE'
观澜 · 一键验收

用法: scripts/verify.sh [选项]

选项:
  --with-wails        追加 wails build -clean（会删 frontend/dist，仅集成阶段用）
  --skip-frontend     跳过前端构建（记为 SKIP，不计入 PASS）
  --with-race         追加 go test -race ./... -count=1（集成阶段查数据竞争）
  --with-network      追加联网数据链路断言（bars 30 根）；离线环境不要开
  --expect-plugins=N  插件装配数下限，默认 7；传 0 关闭该断言（交付中期用）
  --verbose           每步都打印尾部输出
  --keep-logs         保留日志目录
  -h, --help          显示本帮助

退出码: 0 全 PASS / 1 有 FAIL / 2 参数或环境问题
USAGE
}

for arg in "$@"; do
  case "$arg" in
    --with-wails) WITH_WAILS=1 ;;
    --skip-frontend) SKIP_FRONTEND=1 ;;
    --with-race) WITH_RACE=1 ;;
    --with-network) WITH_NETWORK=1 ;;
    --expect-plugins=*)
      EXPECT_PLUGINS="${arg#*=}"
      case "$EXPECT_PLUGINS" in
        ''|*[!0-9]*) echo "--expect-plugins 需要非负整数，实际: $EXPECT_PLUGINS" >&2; exit 2 ;;
      esac
      ;;
    --verbose|-v) VERBOSE=1 ;;
    --keep-logs) KEEP_LOGS=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "缺少 go 工具链，无法验收" >&2
  exit 2
fi

LOG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/guanlan-verify.XXXXXX")" || { echo "无法创建日志目录" >&2; exit 2; }

cleanup() {
  if [ "$KEEP_LOGS" -eq 1 ]; then
    printf '日志保留在: %s\n' "$LOG_DIR"
    return 0
  fi
  case "$LOG_DIR" in
    */guanlan-verify.*)
      if command -v trash >/dev/null 2>&1; then
        trash "$LOG_DIR" >/dev/null 2>&1 || true
      else
        rm -rf -- "$LOG_DIR"
      fi
      ;;
    *) printf '未识别的临时目录，保留: %s\n' "$LOG_DIR" ;;
  esac
}
trap cleanup EXIT

# ---------- 计时 ----------

_now_ms() {
  if command -v perl >/dev/null 2>&1; then
    perl -MTime::HiRes=time -e 'printf("%d", time()*1000)'
  else
    printf '%s000' "$(date +%s)"
  fi
}

_fmt_ms() {
  awk -v ms="$1" 'BEGIN { printf "%.2fs", ms / 1000 }'
}

# ---------- 结果记录 ----------

STEP_NAMES=()
STEP_STATUS=()
STEP_MS=()
STEP_NOTE=()
STEP_LOG=()
PASSES=0
FAILS=0
SKIPS=0

record_skip() { # name start end log note
  local name="$1" start="$2" end="$3" log="$4" note="$5"
  SKIPS=$((SKIPS + 1))
  STEP_NAMES+=("$name")
  STEP_STATUS+=("SKIP")
  STEP_MS+=($((end - start)))
  STEP_NOTE+=("$note")
  STEP_LOG+=("$log")
  printf '%-4s %8s  %s\n' "SKIP" "$(_fmt_ms $((end - start)))" "$name"
}

record() { # name code start end log note
  local name="$1" code="$2" start="$3" end="$4" log="$5" note="$6"
  local ms=$((end - start))
  local status="PASS"
  if [ "$code" -ne 0 ]; then
    status="FAIL"
    FAILS=$((FAILS + 1))
  else
    PASSES=$((PASSES + 1))
  fi
  STEP_NAMES+=("$name")
  STEP_STATUS+=("$status")
  STEP_MS+=("$ms")
  STEP_NOTE+=("$note")
  STEP_LOG+=("$log")
  printf '%-4s %8s  %s\n' "$status" "$(_fmt_ms "$ms")" "$name"
  if [ "$code" -ne 0 ]; then
    printf '     ↳ 失败摘要（完整日志: %s）:\n' "$log"
    tail -n 20 "$log" 2>/dev/null | sed 's/^/       /'
    printf '     ↳ 退出码: %d\n' "$code"
  elif [ "$VERBOSE" -eq 1 ]; then
    tail -n 6 "$log" 2>/dev/null | sed 's/^/       /'
  fi
}

run_step() { # name logfile note cmd...
  local name="$1" logfile="$2" note="$3"
  shift 3
  local log="$LOG_DIR/$logfile"
  local start end code
  start=$(_now_ms)
  "$@" >"$log" 2>&1
  code=$?
  end=$(_now_ms)
  record "$name" "$code" "$start" "$end" "$log" "$note"
}

# ---------- 各步骤 ----------

step_frontend() {
  local log="$LOG_DIR/frontend-build.log"
  local start end code
  if [ "$SKIP_FRONTEND" -eq 1 ]; then
    echo "已按 --skip-frontend 跳过前端构建（结果不计入 PASS）" >"$log"
    record_skip "frontend: npm run build" "$(_now_ms)" "$(_now_ms)" "$log" "跳过（--skip-frontend）"
    return 0
  fi
  start=$(_now_ms)
  if [ ! -d "$ROOT/frontend/node_modules" ]; then
    {
      echo "前端依赖缺失: $ROOT/frontend/node_modules 不存在"
      echo "修复: cd frontend && npm install"
    } >"$log"
    code=1
  elif ! command -v npm >/dev/null 2>&1; then
    echo "缺少 npm，无法构建前端" >"$log"
    code=1
  else
    ( cd "$ROOT/frontend" && npm run build ) >"$log" 2>&1
    code=$?
  fi
  end=$(_now_ms)
  record "frontend: npm run build" "$code" "$start" "$end" "$log" "vue-tsc 类型检查 + vite 构建"
}

step_headless_selftest() {
  local log="$LOG_DIR/headless-selftest.log"
  local start end code
  start=$(_now_ms)
  go run . --headless --task=selftest >"$log" 2>&1
  code=$?
  end=$(_now_ms)
  record "headless: selftest" "$code" "$start" "$end" "$log" "插件必须全部激活"
}

step_headless_tools() {
  local log="$LOG_DIR/headless-tools.log"
  local start end code
  start=$(_now_ms)
  go run . --headless --task=tools >"$log" 2>&1
  code=$?
  if [ "$code" -eq 0 ] && ! grep -Eq '^[a-z0-9]+(\.[a-z0-9_]+)+ +\[(read|network|write|exec)\]' "$log"; then
    {
      echo ""
      echo "断言失败: 工具索引为空（没有一行形如 'market.quote  [network] ...'）"
      echo "含义: plugins/builtin 未装配具体插件，或插件没有注册任何工具。"
    } >>"$log"
    code=3
  fi
  end=$(_now_ms)
  record "headless: 工具索引" "$code" "$start" "$end" "$log" "断言：至少 1 个工具且带风险等级"
}

# step_network_chain 是联网数据链路断言（--with-network 才跑）：
# 默认不跑是为了让 verify.sh 在离线环境也能给出稳定结论。
step_network_chain() {
  local log="$LOG_DIR/network-chain.log"
  local start end code
  start=$(_now_ms)
  go run . --headless --task=bars -- 600000 day 30 >"$log" 2>&1
  code=$?
  if [ "$code" -eq 0 ]; then
    # 注意：headless 的 pretty() 会把输出截到 4000 字节，所以这里不能断言"恰好 30 根"。
    # 精确条数由 go test -tags live ./test/ -run TestLiveEndToEnd 用 Go API 断言。
    local count
    count="$(grep -cE '^    "date"' "$log" 2>/dev/null || true)"
    count="${count:-0}"
    if [ "$count" -lt 10 ]; then
      {
        echo ""
        echo "断言失败: bars 输出里只有 ${count} 条 date（期望 ≥10；数据不足必须显式报错，不能静默少返回）"
      } >>"$log"
      code=5
    fi
    if ! grep -qE '^    "close": ' "$log"; then
      {
        echo ""
        echo "断言失败: bars 输出缺少 close 字段"
      } >>"$log"
      code=6
    fi
  fi
  end=$(_now_ms)
  record "network: 数据链路（bars）" "$code" "$start" "$end" "$log" "联网；断言 ≥10 根 + close 字段"
}

step_plugins() {
  local log="$LOG_DIR/headless-plugins.log"
  local start end code
  start=$(_now_ms)
  go run . --headless --task=plugins >"$log" 2>&1
  code=$?
  # 只数"插件行"（形如 'watchlist  自选  v0.1.0  active=true ...'），
  # 不能数日志行：go run 的 slog 也写到同一个日志文件里，会把行数灌水。
  local count
  count="$(grep -cE 'active=(true|false)' "$log" 2>/dev/null || true)"
  count="${count:-0}"
  if [ "$EXPECT_PLUGINS" -gt 0 ] && [ "$count" -lt "$EXPECT_PLUGINS" ]; then
    {
      echo ""
      echo "断言失败: 插件装配数 ${count} < 期望 ${EXPECT_PLUGINS}"
      echo "含义: plugins/builtin 还没 import 全部内置插件（集成末期门禁；中期可传 --expect-plugins=0）"
    } >>"$log"
    code=4
  fi
  end=$(_now_ms)
  record "headless: 插件装配数" "$code" "$start" "$end" "$log" "断言 ≥ ${EXPECT_PLUGINS}（0=关闭）"
}

step_wails() {
  local log="$LOG_DIR/wails-build.log"
  local start end code
  start=$(_now_ms)
  if ! command -v wails >/dev/null 2>&1; then
    echo "缺少 wails CLI（go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0）" >"$log"
    code=1
  else
    wails build -clean >"$log" 2>&1
    code=$?
  fi
  end=$(_now_ms)
  record "wails build -clean" "$code" "$start" "$end" "$log" "会删除 frontend/dist 后重建"
}

# ---------- 开跑 ----------

MODE="标准（不含 wails build）"
if [ "$WITH_WAILS" -eq 1 ]; then
  MODE="完整（含 wails build，会删 dist）"
fi

printf '观澜 · 验收开始 %s\n' "$(date '+%Y-%m-%d %H:%M:%S')"
printf '  项目 : %s\n' "$ROOT"
printf '  Go   : %s\n' "$(go version)"
if command -v node >/dev/null 2>&1; then
  printf '  Node : %s\n' "$(node --version)"
fi
if command -v npm >/dev/null 2>&1; then
  printf '  npm  : %s\n' "$(npm --version 2>/dev/null)"
fi
printf '  模式 : %s\n' "$MODE"
printf '  日志 : %s\n\n' "$LOG_DIR"

run_step "go build ./..." "go-build.log" "全仓库编译" go build ./...
run_step "go vet ./..." "go-vet.log" "静态检查（含测试代码）" go vet ./...
# live 测试带 build tag，默认的 go vet ./... 不会看它们；单独 vet 一次防止位腐
run_step "go vet -tags live ./test/" "go-vet-live.log" "live 测试的静态检查" go vet -tags live ./test/
run_step "go test ./... -count=1" "go-test.log" "全部单元/契约测试" go test ./... -count=1
if [ "$WITH_RACE" -eq 1 ]; then
  run_step "go test -race ./... -count=1" "go-test-race.log" "数据竞争检测（较慢）" go test -race ./... -count=1
fi
step_frontend
step_headless_selftest
step_plugins
if [ "$WITH_NETWORK" -eq 1 ]; then
  step_network_chain
fi
step_headless_tools
if [ "$WITH_WAILS" -eq 1 ]; then
  step_wails
fi

# ---------- 总表 ----------

TOTAL=$((PASSES + FAILS + SKIPS))
TOTAL_MS=0
for ms in "${STEP_MS[@]}"; do
  TOTAL_MS=$((TOTAL_MS + ms))
done

printf '\n%s\n' '────────────────────────────────────────────────────────────'
printf '验收总表\n\n'
printf '%-6s %10s  %-32s %s\n' "结果" "耗时" "步骤" "备注"
for i in "${!STEP_NAMES[@]}"; do
  printf '%-6s %10s  %-32s %s\n' "${STEP_STATUS[$i]}" "$(_fmt_ms "${STEP_MS[$i]}")" "${STEP_NAMES[$i]}" "${STEP_NOTE[$i]}"
done
if [ "$SKIPS" -gt 0 ]; then
  printf '\n总计 %d 步：PASS %d，FAIL %d，SKIP %d；总耗时 %s\n' "$TOTAL" "$PASSES" "$FAILS" "$SKIPS" "$(_fmt_ms "$TOTAL_MS")"
else
  printf '\n总计 %d 步：PASS %d，FAIL %d；总耗时 %s\n' "$TOTAL" "$PASSES" "$FAILS" "$(_fmt_ms "$TOTAL_MS")"
fi

if [ "$FAILS" -eq 0 ]; then
  printf '结论: PASS\n'
  exit 0
fi
printf '结论: FAIL（%d 个步骤未通过，失败摘要见上）\n' "$FAILS"
exit 1
