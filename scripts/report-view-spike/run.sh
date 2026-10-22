#!/usr/bin/env bash
# Runner for the report-view capture spike.
#
# Serves a static site (report HTML + same-origin assets) on the host, then runs
# one of the harness scripts inside a Chromium container against it.
#
#   scripts/report-view-spike/run.sh <site_dir> <script.js> [out_subdir]
#
# <site_dir> must contain: report.html, assets/ (echarts + katex),
# oda-chart-runtime.js, oda-math-runtime.js. See README.md for how to assemble it.
set -euo pipefail

SITE_DIR="${1:?usage: run.sh <site_dir> <script.js> [out_subdir]}"
SCRIPT="${2:?usage: run.sh <site_dir> <script.js> [out_subdir]}"
OUT_SUBDIR="${3:-out}"
PORT="${SPIKE_PORT:-8899}"
IMAGE="${SPIKE_IMAGE:-zenika/alpine-chrome:with-puppeteer}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SITE_DIR="$(cd "$SITE_DIR" && pwd)"
OUT_DIR="$SITE_DIR/../$OUT_SUBDIR"
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
chmod -R a+rwX "$OUT_DIR"

log() { printf '[spike] %s\n' "$*" >&2; }

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

log "serving $SITE_DIR on 127.0.0.1:$PORT"
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$SITE_DIR" >/tmp/rv-spike-http.log 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$PORT/report.html" 2>/dev/null; then break; fi
  sleep 0.1
done

log "running $SCRIPT in $IMAGE"
docker run --rm --network host \
  --entrypoint sh \
  -v "$HERE:/harness:ro" \
  -v "$SITE_DIR:/spike/site:ro" \
  -v "$OUT_DIR:/spike/out" \
  -w /spike \
  -e NODE_PATH=/usr/src/app/node_modules \
  -e PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium \
  -e SPIKE_URL="http://127.0.0.1:$PORT/${SPIKE_PAGE:-report.html}" \
  -e SPIKE_OUT=/spike/out \
  -e SPIKE_MODE -e SPIKE_RUN -e SPIKE_SLICE_HEIGHT -e SPIKE_VIEWPORT_WIDTH \
  "$IMAGE" -c "node /harness/$SCRIPT"
