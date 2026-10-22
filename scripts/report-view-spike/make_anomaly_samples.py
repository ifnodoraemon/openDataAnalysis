#!/usr/bin/env python3
"""Generate broken report variants from a real report.html.

The point is to answer a product question the happy path cannot: when the report
renders wrong, does a screenshot actually reveal it? Each variant models a real
failure mode seen in generated reports.

Usage:
    python3 make_anomaly_samples.py <report.html> <out_dir>

| variant                   | what breaks                                     |
| ------------------------- | ----------------------------------------------- |
| v1_empty_series           | chart with empty data arrays -> blank plot      |
| v2_no_series              | chart option without a series key               |
| v3_overlap_css            | blocks absolutely positioned on top of each other |
| v4_overflow_css           | table wider than the viewport, content clipped  |
| v5_injected_script        | script + missing image inside report content    |
"""
import html
import json
import re
import sys
from pathlib import Path

CHART_ATTR = re.compile(r'data-chart-option="([^"]*)"')
SCRIPT_MARK = "window.__SPIKE_PWNED__"


def mutate_chart(source: str, mutate) -> str:
    done = {"first": False}

    def repl(match: "re.Match[str]") -> str:
        if done["first"]:
            return match.group(0)
        raw = html.unescape(match.group(1))
        try:
            option = json.loads(raw)
        except json.JSONDecodeError:
            return match.group(0)
        option = mutate(option)
        encoded = html.escape(json.dumps(option, ensure_ascii=False), quote=True)
        done["first"] = True
        return f'data-chart-option="{encoded}"'

    return CHART_ATTR.sub(repl, source, count=1)


def empty_series(option: dict) -> dict:
    for series in option.get("series", []):
        if "data" in series:
            series["data"] = []
    return option


def drop_series(option: dict) -> dict:
    option.pop("series", None)
    return option


def inject_head(source: str, css: str) -> str:
    return source.replace("</head>", f"<style>{css}</style>\n</head>", 1)


def inject_body(source: str, snippet: str) -> str:
    return source.replace("</body>", f"{snippet}\n</body>", 1)


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    src = Path(sys.argv[1])
    out = Path(sys.argv[2])
    out.mkdir(parents=True, exist_ok=True)
    base = src.read_text()
    (out / "v0_original.html").write_text(base)

    (out / "v1_empty_series.html").write_text(mutate_chart(base, empty_series))
    (out / "v2_no_series.html").write_text(mutate_chart(base, drop_series))

    overlap = (
        "body{position:relative}"
        ".report-block-wrapper{position:absolute;left:16px;top:0;width:1168px;background:#fff;}"
    )
    (out / "v3_overlap_css.html").write_text(inject_head(base, overlap))

    overflow = "table{width:3000px !important}"
    (out / "v4_overflow_css.html").write_text(inject_head(base, overflow))

    injected = (
        f'<script>{SCRIPT_MARK}=true;document.title="pwned";</script>'
        '<img src="/assets/missing-asset.png" alt="broken">'
    )
    (out / "v5_injected_script.html").write_text(inject_body(base, injected))

    # An option the runtime rejects with "Chart data is empty": no canvas is ever
    # created for that chart, which is the case that breaks naive readiness checks.
    (out / "v6_empty_option.html").write_text(CHART_ATTR.sub('data-chart-option="{}"', base, count=1))

    # A perfectly valid chart-free report. Readiness must not require a canvas.
    (out / "v7_chartless.html").write_text(
        re.sub(r'<div id="[^"]*-ref-\d+"[^>]*class="chart-box">.*?</div>\s*</div>', "", base, flags=re.S)
    )

    print(json.dumps({"out": str(out), "variants": 8}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
