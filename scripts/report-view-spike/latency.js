// Warm-instance latency breakdown: what a capture actually costs per step.
// Compares one full-page shot against per-slice clip captures.
const puppeteer = require("puppeteer");

const URL = process.env.SPIKE_URL || "http://127.0.0.1:8899/report.html";
const CAP = Number(process.env.SPIKE_SLICE_HEIGHT || 960);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function settle(page) {
  const t0 = Date.now();
  let prev = null;
  while (Date.now() - t0 < 8000) {
    const cur = await page.evaluate(() =>
      Array.from(document.querySelectorAll(".chart-box[data-chart-id]")).map((el) => {
        const c = el.querySelector("canvas");
        return c ? c.toDataURL("image/png").length : -1;
      }),
    );
    if (prev && JSON.stringify(prev) === JSON.stringify(cur) && cur.every((n) => n > 4000)) {
      return Date.now() - t0;
    }
    prev = cur;
    await sleep(100);
  }
  return -1;
}

(async () => {
  const out = { iterations: [] };
  const browser = await puppeteer.launch({
    executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars", "--force-color-profile=srgb"],
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1200, height: 1200, deviceScaleFactor: 1 });

  for (let i = 0; i < 3; i++) {
    const it = {};
    const g0 = Date.now();
    await page.goto(URL, { waitUntil: "load", timeout: 20000 });
    it.goto_ms = Date.now() - g0;
    it.settle_ms = await settle(page);

    const rects = await page.evaluate(() =>
      Array.from(document.querySelectorAll(".report-block-wrapper")).map((el) => {
        const r = el.getBoundingClientRect();
        return { y: Math.round(r.top + window.scrollY), h: Math.round(r.height) };
      }),
    );

    const f0 = Date.now();
    await page.screenshot({ fullPage: true, encoding: "base64" });
    it.fullpage_ms = Date.now() - f0;

    const c0 = Date.now();
    let clips = 0;
    for (const b of rects) {
      const parts = Math.ceil(b.h / CAP);
      for (let k = 0; k < parts; k++) {
        const y = b.y + k * CAP;
        const h = Math.min(CAP, b.y + b.h - y);
        if (h <= 0) continue;
        await page.screenshot({
          clip: { x: 0, y, width: 1200, height: h },
          captureBeyondViewport: true,
          encoding: "base64",
        });
        clips++;
      }
    }
    it.clips = clips;
    it.clips_total_ms = Date.now() - c0;
    it.per_clip_ms = Math.round(it.clips_total_ms / Math.max(1, clips));
    out.iterations.push(it);
  }
  console.log(JSON.stringify(out, null, 2));
  await browser.close();
})().catch((e) => {
  console.error("LATENCY FAILED:", e.stack || e);
  process.exit(1);
});
