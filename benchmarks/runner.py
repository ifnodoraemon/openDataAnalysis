#!/usr/bin/env python3
import sys
import json
from pathlib import Path

# A minimal benchmark runner that aggregates prompt metrics from llm-trace logs.

LLM_DEBUG_DIR = Path("data/llm-debug")

def parse_trace_metrics():
    """Parse trace files to collect prompt size metrics."""
    metrics = {
        "request_json_bytes": 0,
        "input_item_count": 0,
        "avg_tool_arg_bytes": 0,
        "tool_call_count": 0,
    }

    if not LLM_DEBUG_DIR.exists():
        print(f"警告：{LLM_DEBUG_DIR} 不存在，请先运行智能体。", file=sys.stderr)
        return metrics

    # Find the most recent date dir
    date_dirs = sorted([d for d in LLM_DEBUG_DIR.iterdir() if d.is_dir()], reverse=True)
    if not date_dirs:
        return metrics
    
    recent_dir = date_dirs[0]
    total_tool_arg_bytes = 0

    trace_dirs = [d for d in recent_dir.iterdir() if d.is_dir()]
    for trace_dir in trace_dirs:
        req_file = trace_dir / "request.json"
        
        if req_file.exists():
            size = req_file.stat().st_size
            metrics["request_json_bytes"] += size
            
            try:
                with open(req_file, 'r', encoding='utf-8') as f:
                    req_data = json.load(f)
                    
                    messages = req_data.get("messages", [])
                    metrics["input_item_count"] += len(messages)
                    
            except (OSError, json.JSONDecodeError) as exc:
                print(f"读取跟踪请求失败：{req_file}：{exc}", file=sys.stderr)

        # Parse tool arguments specifically if tracked in index.jsonl
        # The agent logs event types in index.jsonl inside the date dir
    
    index_file = recent_dir / "index.jsonl"
    if index_file.exists():
        try:
            with open(index_file, 'r', encoding='utf-8') as f:
                for line_number, line in enumerate(f, start=1):
                    try:
                        record = json.loads(line)
                        if record.get("event") == "tool.call":
                            payload = record.get("payload", {})
                            metrics["tool_call_count"] += 1
                            total_tool_arg_bytes += payload.get("arguments_bytes", 0)
                    except json.JSONDecodeError as exc:
                        print(f"解析跟踪索引失败：{index_file}:{line_number}：{exc}", file=sys.stderr)
        except OSError as exc:
            print(f"读取跟踪索引失败：{index_file}：{exc}", file=sys.stderr)

    if metrics["tool_call_count"] > 0:
        metrics["avg_tool_arg_bytes"] = total_tool_arg_bytes / metrics["tool_call_count"]

    return metrics

def run_benchmarks():
    print("=== OpenDataAnalysis 基准指标 ===")
    metrics = parse_trace_metrics()
    print(f"请求 JSON 总字节数：{metrics['request_json_bytes']}")
    print(f"输入项总数：{metrics['input_item_count']}")
    print(f"工具参数平均字节数：{metrics['avg_tool_arg_bytes']:.2f}")

if __name__ == "__main__":
    run_benchmarks()
