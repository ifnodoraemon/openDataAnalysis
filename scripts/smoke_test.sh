#!/usr/bin/env bash
# smoke_test.sh - 最小场景冒烟测试，用于 CI 或发布前验证
# 用法: ./scripts/smoke_test.sh [--base-url http://127.0.0.1] [--timeout 120]
#
# 需要: node >= 18, 服务器已启动, server/.env 配置完整
# 环境变量:
#   SMOKE_SCENARIOS - 自定义场景 ID 列表（空格分隔）；未设置时发现并运行全部场景

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BASE_URL="http://127.0.0.1"
TIMEOUT="120"
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

for scenario_id in "${SCENARIOS[@]}"; do
    total=$((total + 1))
    echo "--- [$total] 正在运行：$scenario_id ---"

    set +e
    output=$(node "$SCRIPT_DIR/run_scenario.js" \
        --id "$scenario_id" \
        --base-url "$BASE_URL" \
        --timeout "$TIMEOUT" 2>&1)
    exit_code=$?
    set -e

    if [ $exit_code -eq 0 ]; then
        echo "  ✅ 通过"
        passed=$((passed + 1))
        results+=("{\"id\":\"$scenario_id\",\"status\":\"pass\"}")
    else
        echo "  ❌ 失败（退出码=$exit_code）"
        print_failure_output "$output"
        failed=$((failed + 1))
        results+=("{\"id\":\"$scenario_id\",\"status\":\"fail\",\"exit_code\":$exit_code}")
    fi
    echo ""
done

# 输出汇总 JSON
pass_rate=$(python3 -c 'import sys; p=int(sys.argv[1]); t=int(sys.argv[2]); print(f"{(p * 100 / t if t else 0):.1f}%")' "$passed" "$total")
summary_json=$(cat <<EOF
{
    "total": $total,
    "passed": $passed,
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
    echo "❌ 共有 $failed 个场景失败"
    exit 1
fi

echo "✅ 全部场景通过（$passed/$total）"
exit 0
