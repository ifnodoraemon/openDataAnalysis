// Headless capture spike: renders a report HTML at a fixed viewport, waits for
// render settlement, and emits a layout snapshot plus per-block slices.
//
// This is a spike harness, not production code. It exists to keep the numbers in
// docs/report-view-capture.zh-CN.md reproducible.
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const puppeteer = require("puppeteer");

const URL = process.env.SPIKE_URL || "http://127.0.0.1:8899/report.html";
const OUT = process.env.SPIKE_OUT || "/spike/out";
const RUN = process.env.SPIKE_RUN || "1";
const MODE = process.env.SPIKE_MODE || "full"; // full | facts
const VIEWPORT_WIDTH = Number(process.env.SPIKE_VIEWPORT_WIDTH || 1200);
// 960 keeps 1200x960 within the per-image pixel budget of Anthropic-style
// vision encoders, so no implicit downscale happens at the provider.
const SLICE_MAX_HEIGHT = Number(process.env.SPIKE_SLICE_HEIGHT || 960);
const MIN_PART = 320;

const ATOMIC_SELECTOR =
  ".chart-box[data-chart-id], table, pre, blockquote, figure, img, .katex-display, h1, h2, h3, h4";

const sha256 = (b) => crypto.createHash("sha256").update(b).digest("hex").slice(0, 16);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ECharts exposes no usable readiness flag on the instance: isFinished() is not
// present, and any check built on it waits until timeout. Stable serialized
// canvas pixels are the only reliable signal.
async function waitForSettle(page, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let prev = null;
  let samples = 0;
  while (Date.now() < deadline) {
    const state = await page.evaluate(() => {
      const charts = Array.from(document.querySelectorAll(".chart-box[data-chart-id]"));
      return {
        fonts: document.fonts ? document.fonts.status : "n/a",
        imgs_ready: Array.from(document.images).every((i) => i.complete),
        scroll: { w: document.documentElement.scrollWidth, h: document.documentElement.scrollHeight },
        canvases: charts.map((el) => {
          const c = el.querySelector("canvas");
          if (!c) return { id: el.getAttribute("data-chart-id"), len: -1 };
          let len = 0;
          try { len = c.toDataURL("image/png").length; } catch (e) { len = -2; }
          return { id: el.getAttribute("data-chart-id"), len, w: c.width, h: c.height };
        }),
        katex_errors: document.querySelectorAll(".katex-error").length,
        injected_script_ran: window.__SPIKE_PWNED__ === true,
        images_failed: Array.from(document.images)
          .filter((i) => !i.complete || i.naturalWidth === 0)
          .map((i) => i.getAttribute("src")),
      };
    });
    samples++;
    // Readiness means "nothing is still moving", not "the page looks correct".
    // Requiring a canvas, or a minimum pixel count, makes a chart-free report and
    // a chart the renderer rejected both wait out the whole timeout: no canvas is
    // a legitimate end state that belongs in the facts, not in the wait condition.
    const stable =
      prev &&
      state.fonts === "loaded" &&
      state.imgs_ready &&
      JSON.stringify(prev.canvases) === JSON.stringify(state.canvases) &&
      JSON.stringify(prev.scroll) === JSON.stringify(state.scroll);
    if (stable) return { ok: true, samples, state };
    prev = state;
    await sleep(120);
  }
  return { ok: false, samples, state: prev };
}

function planSlices(block, atomic, cap) {
  // Never cut through a chart, table, pre, figure or formula: a half chart is
  // worse than no chart, because the model will conclude from half a picture.
  const cuts = [];
  let cursor = block.y;
  const end = block.y + block.h;
  while (end - cursor > cap) {
    let candidate = cursor + cap;
    const hit = atomic
      .filter((a) => a.y < candidate && a.y + a.h > candidate)
      .sort((a, b) => a.y - b.y)[0];
    if (hit) {
      const safe = hit.y - 2 - cursor;
      if (safe > MIN_PART) candidate = cursor + safe;
    }
    cuts.push({ from: cursor, to: candidate });
    cursor = candidate;
  }
  if (end > cursor) cuts.push({ from: cursor, to: end });
  return cuts;
}

(async () => {
  const started = Date.now();
  const browser = await puppeteer.launch({
    executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
    args: [
      "--no-sandbox",
      "--disable-dev-shm-usage",
      "--hide-scrollbars",
      "--force-color-profile=srgb",
      "--font-render-hinting=none",
    ],
  });
  const page = await browser.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  page.on("requestfailed", (r) => pageErrors.push("requestfailed:" + r.url()));
  await page.setViewport({ width: VIEWPORT_WIDTH, height: 1200, deviceScaleFactor: 1 });

  const tLoad = Date.now();
  await page.goto(URL, { waitUntil: "load", timeout: 20000 });
  const loadMs = Date.now() - tLoad;
  await page.evaluate(() => document.fonts.ready);
  const tSettle = Date.now();
  const settle = await waitForSettle(page, 8000);
  const settleMs = Date.now() - tSettle;

  const layout = await page.evaluate((sel) => {
    const sy = window.scrollY;
    const box = (el) => {
      const r = el.getBoundingClientRect();
      return { y: Math.round(r.top + sy), h: Math.round(r.height), x: Math.round(r.left), w: Math.round(r.width) };
    };
    return {
      scroll: { w: document.documentElement.scrollWidth, h: document.documentElement.scrollHeight },
      viewport_w: window.innerWidth,
      overflow_x: document.documentElement.scrollWidth > window.innerWidth + 1,
      blocks: Array.from(document.querySelectorAll(".report-block-wrapper")).map((el) => ({
        block_id: el.getAttribute("data-block-id"),
        kind: el.getAttribute("data-block-kind"),
        ...box(el),
      })),
      atomic: Array.from(document.querySelectorAll(sel)).map((el) => ({ tag: el.tagName.toLowerCase(), ...box(el) })),
    };
  }, ATOMIC_SELECTOR);

  const meta = {
    run: RUN,
    mode: MODE,
    url: URL,
    viewport: { width: VIEWPORT_WIDTH, dpr: 1, slice_height: SLICE_MAX_HEIGHT },
    timing: { load_ms: loadMs, settle_ms: settleMs, total_ms: Date.now() - started },
    readiness: {
      ok: settle.ok,
      samples: settle.samples,
      canvases: settle.state.canvases,
      katex_errors: settle.state.katex_errors,
      injected_script_ran: settle.state.injected_script_ran,
      images_failed: settle.state.images_failed,
      charts_missing: settle.state.canvases.filter((c) => c.len === -1).map((c) => c.id),
    },
    layout: { scroll: layout.scroll, overflow_x: layout.overflow_x, blocks: layout.blocks, atomic: layout.atomic },
    page_errors: pageErrors,
    slices: [],
    full: null,
  };

  for (const block of layout.blocks) {
    const cuts = planSlices(block, layout.atomic, SLICE_MAX_HEIGHT);
    cuts.forEach((cut, i) => {
      meta.slices.push({
        block_id: block.block_id,
        kind: block.kind,
        part: `${i + 1}/${cuts.length}`,
        y: Math.round(cut.from),
        h: Math.round(cut.to - cut.from),
        w: VIEWPORT_WIDTH,
        _cut: cut,
      });
    });
  }

  if (MODE === "full") {
    for (const s of meta.slices) {
      const file = path.join(OUT, `run${RUN}-${s.block_id}-${s.part.replace("/", "of")}.png`);
      await page.screenshot({
        path: file,
        clip: { x: 0, y: s._cut.from, width: VIEWPORT_WIDTH, height: s._cut.to - s._cut.from },
        captureBeyondViewport: true,
      });
      const buf = fs.readFileSync(file);
      s.bytes = buf.length;
      s.sha = sha256(buf);
    }
    const fullPath = path.join(OUT, `run${RUN}-full.png`);
    await page.screenshot({ path: fullPath, fullPage: true });
    const fullBuf = fs.readFileSync(fullPath);
    meta.full = { w: layout.scroll.w, h: layout.scroll.h, bytes: fullBuf.length, sha: sha256(fullBuf) };
  }
  meta.slices.forEach((s) => delete s._cut);

  fs.writeFileSync(path.join(OUT, `meta-run${RUN}.json`), JSON.stringify(meta, null, 2));
  console.log(JSON.stringify({
    run: RUN,
    mode: MODE,
    readiness_ok: settle.ok,
    timing: meta.timing,
    scroll: `${layout.scroll.w}x${layout.scroll.h}`,
    blocks: layout.blocks.length,
    slices: meta.slices.length,
    max_slice_h: Math.max(...meta.slices.map((s) => s.h)),
    overflow_x: layout.overflow_x,
    katex_errors: meta.readiness.katex_errors,
    charts: meta.readiness.canvases.map((c) => c.len),
    page_errors: pageErrors,
  }));
  await browser.close();
})().catch((e) => {
  console.error("CAPTURE FAILED:", e && e.stack ? e.stack : e);
  process.exit(1);
});
