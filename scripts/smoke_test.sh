#!/usr/bin/env bash
# smoke_test.sh - 最小场景冒烟测试，用于 CI 或发布前验证
# 用法: ./scripts/smoke_test.sh [--base-url http://127.0.0.1] [--timeout 120]
#
# 需要: node >= 18, 服务器已启动, server/.env 配置完整
# 环境变量:
#   SMOKE_SCENARIOS - 自定义场景 ID 列表（空格分隔）；未设置时发现并运行全部场景
#
# 注意: 服务端必须 REPORT_VIEW_CAPTURE=off 才能用作基准基线。捕获会为每次报告
# 变更增加 4-5 秒并注入图片 token，静默改变超时预算与 token 账目。
# 启动日志里的 report_view_capture= 字段是唯一可靠的确认方式。

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BASE_URL="http://127.0.0.1"
TIMEOUT="120"
REPEAT="1"
JOBS="1"
POSITIONAL_ARGS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --base-url)
            if [ $# -lt 2 ]; then
                echo "错误：--base-url 缺少参数值" >&2
                exit 64
            fi
            BASE_URL="$2"
            shift 2
            ;;
        --timeout)
            if [ $# -lt 2 ]; then
                echo "错误：--timeout 缺少参数值" >&2
                exit 64
            fi
            TIMEOUT="$2"
            shift 2
            ;;
        --jobs)
            # Runs are independent: each scenario gets its own session, its own
            # analysis database and its own artifacts. The only shared resource
            # is the model gateway, so jobs is bounded by that.
            if [ $# -lt 2 ]; then
                echo "错误：--jobs 缺少参数值" >&2
                exit 64
            fi
            JOBS="$2"
            shift 2
            ;;
        --repeat)
            # A single run cannot tell a fixed defect from model variance: the
            # same scenario passes and fails across runs. Repeating turns the
            # result into a rate.
            if [ $# -lt 2 ]; then
                echo "错误：--repeat 缺少参数值" >&2
                exit 64
            fi
            REPEAT="$2"
            shift 2
            ;;
        --help|-h)
            echo "用法：$0 [--base-url http://127.0.0.1] [--timeout 120]"
            echo "       $0 [base-url] [timeout]"
            exit 0
            ;;
        --)
            shift
            break
            ;;
        -*)
            echo "错误：未知选项 $1" >&2
            exit 64
            ;;
        *)
            POSITIONAL_ARGS+=("$1")
            shift
            ;;
    esac
done
if [ ${#POSITIONAL_ARGS[@]} -ge 1 ]; then
    BASE_URL="${POSITIONAL_ARGS[0]}"
fi
if [ ${#POSITIONAL_ARGS[@]} -ge 2 ]; then
    TIMEOUT="${POSITIONAL_ARGS[1]}"
fi
SCENARIOS=()
if [ -n "${SMOKE_SCENARIOS:-}" ]; then
    read -r -a SCENARIOS <<< "$SMOKE_SCENARIOS"
else
    while IFS= read -r scenario_file; do
        SCENARIOS+=("$(basename "$(dirname "$scenario_file")")")
    done < <(find "$ROOT_DIR/samples/coverage_scenarios" -mindepth 2 -maxdepth 2 -name scenario.yaml -print | sort)
fi
if [ ${#SCENARIOS[@]} -eq 0 ]; then
    echo "错误：未发现测试场景" >&2
    exit 1
fi

echo "========================================="
echo "  场景冒烟测试"
echo "========================================="
echo "服务地址：  $BASE_URL"
echo "超时时间：  ${TIMEOUT} 秒"
echo "测试场景：  ${SCENARIOS[*]}"
echo ""

total=0
passed=0
failed=0
flaky=0
results=()

print_failure_output() {
    local raw="$1"
    if [ -z "$raw" ]; then
        return
    fi
    echo "  --- 末尾输出 ---"
    printf '%s\n' "$raw" | tail -n 100 | sed 's/^/  /'
    echo "  --- 输出结束 ---"
}

# scenario_timeout 按场景声明的长度给时间预算。
#
# 一个"长"场景在 300 秒里常常还在正常产出报告块，默认超时会把正常工作判成失败
# （实测 19/24 在 300s 被截断、600s 全部通过）。场景可用 timeout_seconds 显式
# 覆盖；--timeout 只在场景没有声明长度时作为兜底。
scenario_timeout() {
    local scenario_id="$1"
    local yaml="$ROOT_DIR/samples/coverage_scenarios/$scenario_id/scenario.yaml"
    if [ ! -f "$yaml" ]; then
        echo "$TIMEOUT"
        return
    fi
    local explicit
    explicit=$(sed -n 's/^timeout_seconds: *//p' "$yaml" | head -1)
    if [ -n "$explicit" ]; then
        echo "$explicit"
        return
    fi
    local declared
    declared=$(sed -n 's/^task_length: *//p' "$yaml" | head -1)
    case "$declared" in
        long) echo 600 ;;
        medium) echo 420 ;;
        short) echo 300 ;;
        *) echo "$TIMEOUT" ;;
    esac
}

RUNS_DIR="$ROOT_DIR/tmp/run-status"
rm -rf "$RUNS_DIR"
mkdir -p "$RUNS_DIR"

# One file per (scenario, attempt) keeps parallel workers from sharing state.
task_args=""
for scenario_id in "${SCENARIOS[@]}"; do
    for attempt in $(seq 1 "$REPEAT"); do
        task_args="$task_args$scenario_id $attempt\n"
    done
done

run_one() {
    local sid="$1"
    local attempt="$2"
    local budget
    budget=$(scenario_timeout "$sid")
    local log="$RUNS_DIR/$sid.$attempt.log"
    if node "$SCRIPT_DIR/run_scenario.js" \
        --id "$sid" \
        --base-url "$BASE_URL" \
        --timeout "$budget" > "$log" 2>&1; then
        echo ok > "$RUNS_DIR/$sid.$attempt.status"
    else
        echo fail > "$RUNS_DIR/$sid.$attempt.status"
    fi
}

export -f run_one scenario_timeout
export ROOT_DIR SCRIPT_DIR BASE_URL TIMEOUT RUNS_DIR

echo "并行度：$JOBS，每个场景重复 $REPEAT 次，共 $(( ${#SCENARIOS[@]} * REPEAT )) 次运行"
if [ "$JOBS" -gt 1 ]; then
    cat <<'WARN'
⚠️ 并行运行共享同一个 workspace：上传文件是 workspace 级状态，并行时每个场景都会
   看到其它场景刚上传的文件（实测出现过"450 个待导入的上传文件"）。这会让场景互相
   污染，不稳定率显著上升（串行 11/30 → 并行 19/30）。
   并行只适合做快速冒烟；**正式基线请用 --jobs 1**，除非已实现按 run 隔离工作区。
WARN
    echo ""
fi

if [ "$JOBS" -le 1 ]; then
    while read -r sid attempt; do run_one "$sid" "$attempt"; done < <(printf "$task_args")
else
    printf "$task_args" | xargs -P "$JOBS" -n 2 bash -c 'run_one "$0" "$1"'
fi

for scenario_id in "${SCENARIOS[@]}"; do
    total=$((total + 1))
    scenario_passes=$(grep -l '^ok$' "$RUNS_DIR/$scenario_id".*.status 2>/dev/null | wc -l)
    scenario_passes=${scenario_passes//[^0-9]/}
    if [ "$scenario_passes" -eq "$REPEAT" ]; then
        status="pass"
        passed=$((passed + 1))
    elif [ "$scenario_passes" -eq 0 ]; then
        status="fail"
        failed=$((failed + 1))
    else
        status="flaky"
        flaky=$((flaky + 1))
    fi
    echo "  ${status}（$scenario_passes/$REPEAT） $scenario_id"
    if [ "$status" != "pass" ]; then
        for log in "$RUNS_DIR/$scenario_id".*.log; do
            if ! grep -q '^ok$' "${log%.log}.status" 2>/dev/null; then
                print_failure_output "$(tail -n 40 "$log")"
            fi
        done
    fi
    results+=("{\"id\":\"$scenario_id\",\"status\":\"$status\",\"runs\":$REPEAT,\"passes\":$scenario_passes}")
done

# 输出汇总 JSON
pass_rate=$(python3 -c 'import sys; p=int(sys.argv[1]); t=int(sys.argv[2]); print(f"{(p * 100 / t if t else 0):.1f}%")' "$passed" "$total")
summary_json=$(cat <<EOF
{
    "total": $total,
    "repeat": $REPEAT,
    "passed": $passed,
    "flaky": $flaky,
    "failed": $failed,
    "pass_rate": "$pass_rate",
    "scenarios": [$(IFS=,; echo "${results[*]}")]
}
EOF
)

echo "========================================="
echo "  汇总"
echo "========================================="
echo "$summary_json" | python3 -m json.tool 2>/dev/null || echo "$summary_json"
echo ""

# 输出到 CI 可读的文件
mkdir -p "$ROOT_DIR/tmp"
echo "$summary_json" > "$ROOT_DIR/tmp/smoke-test-summary.json"

if [ $failed -gt 0 ]; then
    echo "❌ 共有 $failed 个场景每次运行都失败"
    exit 1
fi
if [ $flaky -gt 0 ]; then
    echo "⚠️ 共有 $flaky 个场景不稳定（有时通过、有时失败）——单次运行看不出这类缺陷"
    exit 1
fi

echo "✅ 全部场景通过（$passed/$total）"
exit 0
