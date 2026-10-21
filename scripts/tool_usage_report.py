#!/usr/bin/env python3
"""Report which tools the agent actually uses, across recorded scenario runs.

This exists to make tool-surface convergence data-driven instead of a guess:
before deleting a tool, look at whether any scenario ever calls it, and whether
the calls that do happen succeed.

Usage:
    python3 scripts/tool_usage_report.py [--runs-dir tmp/scenario-runs] [--json]

Reads the per-scenario artifacts written by scripts/run_scenario.js
(events.ndjson) and prints a markdown table sorted by call count, plus the set
of registered tools that were never called.
"""
import argparse
import json
import pathlib
import re
import sys
from collections import defaultdict

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
SERVER_ROOT = REPO_ROOT / "server"


def registered_tool_names():
    """Collect tool names from the Go sources' Name() implementations."""
    names = set()
    pattern = re.compile(
        r'func \(t \*(\w+)\) Name\(\) string \{\s*return "([^"]+)"', re.S
    )
    for path in SERVER_ROOT.rglob("*.go"):
        if "_test.go" in path.name:
            continue
        for match in pattern.finditer(path.read_text(encoding="utf-8")):
            names.add(match.group(2))
    return names


def collect(runs_dir):
    calls = defaultdict(int)
    failures = defaultdict(int)
    error_codes = defaultdict(lambda: defaultdict(int))
    scenarios = defaultdict(set)
    run_count = 0
    completed = 0

    for events_path in sorted(runs_dir.glob("*/events.ndjson")):
        run_count += 1
        scenario_id = events_path.parent.name.split("-", 2)[-1]
        saw_completed = False
        for line in events_path.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            event = record.get("event") or {}
            kind = event.get("type")
            data = event.get("data") or {}

            if kind == "tool_call":
                name = data.get("name")
                if name:
                    calls[name] += 1
                    scenarios[name].add(scenario_id)
            elif kind == "tool_result":
                name = data.get("name")
                if not name:
                    continue
                if data.get("success") is False:
                    failures[name] += 1
                    # Structured failures carry a machine code; the report keeps
                    # them so a tool that "works" but is constantly mistyped
                    # stands out.
                    raw = data.get("result")
                    if isinstance(raw, str):
                        try:
                            code = json.loads(raw).get("error_code")
                        except (json.JSONDecodeError, AttributeError):
                            code = None
                        if code:
                            error_codes[name][code] += 1
            elif kind == "run_completed":
                saw_completed = True
        if saw_completed:
            completed += 1

    return {
        "runs": run_count,
        "completed": completed,
        "calls": dict(calls),
        "failures": dict(failures),
        "error_codes": {k: dict(v) for k, v in error_codes.items()},
        "scenarios": {k: sorted(v) for k, v in scenarios.items()},
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runs-dir", default=str(REPO_ROOT / "tmp" / "scenario-runs"))
    parser.add_argument("--json", action="store_true", help="emit raw json")
    args = parser.parse_args()

    runs_dir = pathlib.Path(args.runs_dir)
    if not runs_dir.is_dir():
        print(f"no scenario runs at {runs_dir}", file=sys.stderr)
        return 1

    data = collect(runs_dir)
    registered = registered_tool_names()
    called = set(data["calls"])

    if args.json:
        print(json.dumps({**data, "registered": sorted(registered),
                          "never_called": sorted(registered - called)},
                         ensure_ascii=False, indent=2))
        return 0

    print(f"# 工具使用报告\n")
    print(f"运行场景：{data['runs']} 个（完成 {data['completed']} 个）")
    print(f"注册工具：{len(registered)} 个，被调用：{len(called)} 个\n")

    print("| 工具 | 调用次数 | 涉及场景 | 失败次数 | 失败率 | 常见错误码 |")
    print("|---|---|---|---|---|---|")
    for name, count in sorted(data["calls"].items(), key=lambda kv: (-kv[1], kv[0])):
        fail = data["failures"].get(name, 0)
        rate = f"{fail / count * 100:.0f}%" if count else "-"
        codes = data["error_codes"].get(name, {})
        code_text = ", ".join(f"{k}×{v}" for k, v in sorted(codes.items(), key=lambda kv: -kv[1])[:3])
        print(f"| `{name}` | {count} | {len(data['scenarios'].get(name, []))} | {fail} | {rate} | {code_text or '-'} |")

    never = sorted(registered - called)
    print(f"\n## 从未被调用的工具（{len(never)} 个）\n")
    for name in never:
        print(f"- `{name}`")
    return 0


if __name__ == "__main__":
    sys.exit(main())
