#!/usr/bin/env bash
# smoke_test.sh - 最小场景冒烟测试，用于 CI 或发布前验证
# 用法: ./scripts/smoke_test.sh [--base-url http://127.0.0.1] [--timeout 120]
#
# 需要: node >= 18, 服务器已启动, server/.env 配置完整
# 环境变量:
#   SMOKE_SCENARIOS - 自定义场景 ID 列表（空格分隔），默认使用最小集合
#   SKIP_INFRA_FAILURES - 设置为 1 则基础设施故障不算失败

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认最小 smoke 场景：覆盖基础分析、歧义提问、delegate 恢复
DEFAULT_SCENARIOS=(
    "01_sales_complete"           # 基础完整分析
    "12_ambiguous_metrics"        # 歧义场景必须提问
    "16_delegate_failure_recovery" # delegate 失败恢复
)

BASE_URL="${1:-http://127.0.0.1}"
TIMEOUT="${2:-120}"
SCENARIOS=(${SMOKE_SCENARIOS:-${DEFAULT_SCENARIOS[@]}})
SKIP_INFRA="${SKIP_INFRA_FAILURES:-0}"

echo "========================================="
echo "  Scenario Smoke Test"
echo "========================================="
echo "Base URL:   $BASE_URL"
echo "Timeout:    ${TIMEOUT}s"
echo "Scenarios:  ${SCENARIOS[*]}"
echo ""

total=0
passed=0
failed=0
infra_blocked=0
results=()

print_failure_output() {
    local raw="$1"
    if [ -z "$raw" ]; then
        return
    fi
    echo "  --- output tail ---"
    printf '%s\n' "$raw" | tail -n 100 | sed 's/^/  /'
    echo "  --- end output ---"
}

classify_infra_blocker() {
    local raw="$1"
    local category=""
    category=$(printf '%s\n' "$raw" | grep -oE '"error_category"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 | sed -E 's/.*"error_category"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
    if [ -n "$category" ] && [ "$category" != "runtime_error" ]; then
        echo "$category"
        return
    fi
    if printf '%s\n' "$raw" | grep -Eiq 'too many requests|status[=:]429|\b429\b|llm api request failed'; then
        echo "llm_request_failed"
        return
    fi
    if printf '%s\n' "$raw" | grep -Eiq 'tls handshake timeout|context deadline exceeded|connection refused'; then
        echo "network_or_service_unavailable"
        return
    fi
    if printf '%s\n' "$raw" | grep -Eiq 'scenario timeout after'; then
        echo "scenario_timeout"
        return
    fi
}

record_infra_blocked() {
    local scenario_id="$1"
    local category="$2"
    echo "  ⚠️  INFRA BLOCKED ($category)"
    infra_blocked=$((infra_blocked + 1))
    results+=("{\"id\":\"$scenario_id\",\"status\":\"infra_blocked\",\"category\":\"$category\"}")
}

for scenario_id in "${SCENARIOS[@]}"; do
    total=$((total + 1))
    echo "--- [$total] Running: $scenario_id ---"

    set +e
    output=$(node "$SCRIPT_DIR/run_scenario.js" \
        --id "$scenario_id" \
        --base-url "$BASE_URL" \
        --timeout "$TIMEOUT" 2>&1)
    exit_code=$?
    set -e

    if [ $exit_code -eq 0 ]; then
        echo "  ✅ PASS"
        passed=$((passed + 1))
        results+=("{\"id\":\"$scenario_id\",\"status\":\"pass\"}")
    elif [ $exit_code -eq 2 ]; then
        error_cat=$(classify_infra_blocker "$output")
        if [ -n "$error_cat" ] && [ "$SKIP_INFRA" = "1" ]; then
            record_infra_blocked "$scenario_id" "$error_cat"
            print_failure_output "$output"
        else
            echo "  ❌ FAIL"
            print_failure_output "$output"
            failed=$((failed + 1))
            results+=("{\"id\":\"$scenario_id\",\"status\":\"fail\"}")
        fi
    else
        error_cat=$(classify_infra_blocker "$output")
        if [ -n "$error_cat" ] && [ "$SKIP_INFRA" = "1" ]; then
            record_infra_blocked "$scenario_id" "$error_cat"
            print_failure_output "$output"
        else
            echo "  ❌ ERROR (exit=$exit_code)"
            print_failure_output "$output"
            failed=$((failed + 1))
            results+=("{\"id\":\"$scenario_id\",\"status\":\"error\",\"exit_code\":$exit_code}")
        fi
    fi
    echo ""
done

# 输出汇总 JSON
summary_json=$(cat <<EOF
{
    "total": $total,
    "passed": $passed,
    "failed": $failed,
    "infra_blocked": $infra_blocked,
    "pass_rate": "$(echo "scale=1; $passed * 100 / $total" | bc)%",
    "scenarios": [$(IFS=,; echo "${results[*]}")]
}
EOF
)

echo "========================================="
echo "  Summary"
echo "========================================="
echo "$summary_json" | python3 -m json.tool 2>/dev/null || echo "$summary_json"
echo ""

# 输出到 CI 可读的文件
mkdir -p "$ROOT_DIR/tmp"
echo "$summary_json" > "$ROOT_DIR/tmp/smoke-test-summary.json"

if [ $failed -gt 0 ]; then
    echo "❌ $failed scenario(s) failed"
    exit 1
fi

if [ $infra_blocked -gt 0 ]; then
    echo "⚠️  No actionable scenario failures ($passed/$total passed, $infra_blocked infra-blocked)"
else
    echo "✅ All scenarios passed ($passed/$total)"
fi
exit 0
