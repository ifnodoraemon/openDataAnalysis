# Report view capture spike

Harness for the design in `docs/report-view-capture.zh-CN.md`. It answers four
questions with measurements instead of assumptions:

1. Does headless Chromium render a real report faithfully (ECharts, KaTeX, tables)?
2. Is capture pixel-deterministic across runs?
3. Is slicing better than sending one full-page image, and at what token cost?
4. Does a screenshot actually expose broken renders?

This is a spike, not production code. The production shape is a
`render-executor` service returning one full-page PNG plus layout rects, with
slicing done server-side in Go.

## Prerequisites

- Docker, and the Chromium image:
  `docker pull zenika/alpine-chrome:with-puppeteer`
- Python 3 with `PIL` and `numpy` (only for `pixelcmp.py` and the sample maker)

## Assemble a site directory

The capture target must load its assets same-origin, exactly like the app does:

```bash
SITE=/tmp/rv-spike/site
mkdir -p "$SITE"
cp -r client/public/assets "$SITE/assets"
cp client/public/oda-chart-runtime.js client/public/oda-math-runtime.js "$SITE/"
cp data/storage/workspaces/<ws>/runs/<run>/report/report.html "$SITE/report.html"
```

## Run

```bash
# layout snapshot + per-block slices, two runs for the determinism check
scripts/report-view-spike/run.sh /tmp/rv-spike/site capture.js out1
SPIKE_RUN=2 scripts/report-view-spike/run.sh /tmp/rv-spike/site capture.js out1

# facts only (no screenshots) - cheap, used by the anomaly matrix
SPIKE_MODE=facts scripts/report-view-spike/run.sh /tmp/rv-spike/site capture.js out_facts

# warm-instance latency: full-page shot vs per-slice clips
scripts/report-view-spike/run.sh /tmp/rv-spike/site latency.js out_latency

# prove full-page crop == per-slice clip
scripts/report-view-spike/run.sh /tmp/rv-spike/site slice_compare.js out_cmp
python3 scripts/report-view-spike/pixelcmp.py /tmp/rv-spike/out_cmp

# broken-report variants
python3 scripts/report-view-spike/make_anomaly_samples.py \
  "$SITE/report.html" "$SITE"
SPIKE_PAGE=v1_empty_series.html SPIKE_MODE=facts \
  scripts/report-view-spike/run.sh /tmp/rv-spike/site capture.js out_anomaly
```

Environment knobs: `SPIKE_RUN`, `SPIKE_MODE` (`full`/`facts`), `SPIKE_PAGE`,
`SPIKE_SLICE_HEIGHT` (default 960), `SPIKE_VIEWPORT_WIDTH` (default 1200),
`SPIKE_OUT`, `SPIKE_PORT`, `SPIKE_IMAGE`.

## Results

Measured on a real 8-block report with 6 ECharts charts, 1200x23731 px:

| question | answer |
| --- | --- |
| render fidelity | charts, dual axes, rotated labels, tables, inline math all correct |
| determinism | 19/19 slice sha256 identical across runs; full page identical |
| crop == clip | 19/19 pixel-identical (`maxdiff=0`) |
| slice height | 960 keeps 1200x960 under the per-image pixel budget; 1500 gets downscaled 20% |
| token cost | ~36.4k tokens for 29 slices vs 166 tokens for the whole page at 0.066 scale |
| latency (warm) | load 0.65-2.0s + settle 0.8-1.4s + full-page shot 3.4-5.8s ≈ 5.4s |
| clip vs shot | 19 clips take 24-25s; one full-page shot takes 3.4s |

Anomaly matrix (`make_anomaly_samples.py`, 8 variants) - which channel catches what:

| variant | structured facts | screenshot |
| --- | --- | --- |
| v1 empty series | canvas pixel count drops (no baseline -> not actionable) | blank chart, obvious |
| v2 no series | same | partial chart |
| v3 blocks absolutely positioned | scroll height 23731 -> 3941, all 8 blocks at y=0 | misleading: only the last-painted block is visible and looks normal |
| v4 table 3000px wide | `overflow_x=true`, scroll w=3239 | right side clipped |
| v5 injected script + broken img | `script_ran=true`, `imgs_failed=[...]` | script execution invisible |
| v6 chart rejected by runtime | `charts_missing=["cht_demand_capacity"]` | blank |
| v7 chart-free report | must still reach ready (it does) | normal |

Facts and pixels are complementary: use facts for anything assertable, pixels for
"does this look right".

Four gotchas worth keeping:

- `echarts.getInstanceByDom(el).isFinished()` does not exist on the instance.
  Any readiness check built on it never fires. Use canvas `toDataURL` stability.
- Do not capture per block. One full-page screenshot plus a server-side crop is
  pixel-identical and ~7x faster.
- Readiness must not require a canvas or a minimum pixel count. A chart-free
  report and a chart the renderer rejected both have no canvas, and both would
  wait out the whole timeout (settle dropped to 125-871ms after the fix).
- A capture page without the report CSP executes model-authored `<script>`
  (v5 proves it). The production capture page must carry `reportHTMLCSP` and the
  same sanitizer as the in-app preview.


## Limitations

- The static server is `python3 -m http.server` (single-threaded), so page load
  numbers are pessimistic versus nginx.
- The container runs as uid 1000; the output directory is chmod'ed for it.
- These variants exercise the raw server-rendered HTML. The client sanitizer is
  not in this path - that gap is deliberate and documented in the design doc.
