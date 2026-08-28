#!/usr/bin/env python3
import argparse
import os
from pathlib import Path
import sys
import yaml

ROOT = Path(__file__).resolve().parents[1]
SCENARIO_ROOT = ROOT / 'samples' / 'coverage_scenarios'

BASE_CHECKS = [
    '路径是否由当前目标和证据决定，而非固定步骤？',
    '事实、推断、假设和用户确认是否清楚区分？',
    '存在会实质改变结果的歧义时，是否询问用户或明确陈述获准的假设？',
    '最终交付是否回答用户请求，并由实际结果或制品支撑？',
    '是否避免了证据不足的归因和确定性表述？',
]


def load_yaml(path: Path):
    with path.open('r', encoding='utf-8') as f:
        return yaml.safe_load(f)


def iter_scenarios(selected_id=None):
    for path in sorted(SCENARIO_ROOT.glob('*/scenario.yaml')):
        data = load_yaml(path)
        if selected_id and data.get('id') != selected_id:
            continue
        yield path.parent, data


def render_one(folder: Path, data: dict) -> str:
    acceptance = data.get('acceptance', {}) or {}
    manual_review = data.get('manual_review', []) or []
    files = data.get('files', []) or []
    lines = []
    lines.append(f"# {data.get('id', folder.name)}")
    lines.append('')
    lines.append(f"- 目录: `{folder.relative_to(ROOT)}`")
    lines.append(f"- 行业: `{data.get('industry', 'unknown')}`")
    lines.append(f"- 任务跨度: `{data.get('task_length', 'unknown')}`")
    lines.append(f"- 提问: {data.get('prompt', '')}")
    lines.append('- 上传文件:')
    for file_name in files:
        lines.append(f"  - `{folder.relative_to(ROOT)}/{file_name}`")
    lines.append('')
    lines.append('## 结构化验收')
    lines.append(f"- 允许终态: `{acceptance.get('terminal_types', [])}`")
    if 'report_finalized' in acceptance:
        lines.append(f"- 报告已最终交付: `{acceptance.get('report_finalized')}`")
    req_tools = acceptance.get('required_tool_calls', []) or []
    req_codes = acceptance.get('required_tool_result_codes', []) or []
    if req_tools:
        lines.append('- 必须调用的工具:')
        for item in req_tools:
            lines.append(f"  - {item}")
    if req_codes:
        lines.append('- 必须出现的工具结果码:')
        for item in req_codes:
            lines.append(f"  - {item}")
    lines.append('')
    lines.append('## 人工验收 Checklist')
    for label in BASE_CHECKS:
        lines.append(f"- [ ] {label}")
    for item in manual_review:
        lines.append(f"- [ ] {item}")
    lines.append('')
    lines.append('## 记录')
    lines.append('- [ ] 最终报告是否回答了用户问题')
    lines.append('- [ ] 是否生成了合适的图表')
    lines.append('- [ ] 是否存在明显幻觉 / 错误归因 / 乱连表')
    lines.append('- [ ] 是否需要补充新的测试场景')
    return '\n'.join(lines)


def main():
    parser = argparse.ArgumentParser(description='Render scenario checklist for manual evaluation.')
    parser.add_argument('--id', help='Only render one scenario id')
    parser.add_argument('--output', help='Output markdown file path')
    args = parser.parse_args()

    rendered = []
    for folder, data in iter_scenarios(args.id):
        rendered.append(render_one(folder, data))

    if not rendered:
        print('未找到场景。', file=sys.stderr)
        sys.exit(1)

    doc = '\n\n---\n\n'.join(rendered) + '\n'
    if args.output:
        out_path = Path(args.output)
        if not out_path.is_absolute():
            out_path = ROOT / out_path
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(doc, encoding='utf-8')
    else:
        sys.stdout.write(doc)


if __name__ == '__main__':
    main()
