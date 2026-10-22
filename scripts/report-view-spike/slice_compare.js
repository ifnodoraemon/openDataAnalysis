// Captures one full-page screenshot AND per-slice clips of the same page, so
// pixelcmp.py can prove the two are equivalent.
//
// Output: <OUT>/full.png, <OUT>/clip-<block>-<part>.png, <OUT>/slices.json
const fs = require("fs");
const path = require("path");
const puppeteer = require("puppeteer");

const URL = process.env.SPIKE_URL || "http://127.0.0.1:8899/report.html";
const OUT = process.env.SPIKE_OUT || "/spike/out";
const CAP = Number(process.env.SPIKE_SLICE_HEIGHT || 960);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const browser = await puppeteer.launch({
    executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars", "--force-color-profile=srgb"],
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1200, height: 1200, deviceScaleFactor: 1 });
  await page.goto(URL, { waitUntil: "load", timeout: 20000 });
  await page.evaluate(() => document.fonts.ready);
  let prev = null;
  while (true) {
    const cur = await page.evaluate(() =>
      Array.from(document.querySelectorAll(".chart-box[data-chart-id]")).map((el) => {
        const c = el.querySelector("canvas");
        return c ? c.toDataURL("image/png").length : -1;
      }),
    );
    if (prev && JSON.stringify(prev) === JSON.stringify(cur) && cur.every((n) => n > 4000)) break;
    prev = cur;
    await sleep(100);
  }

  const blocks = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".report-block-wrapper")).map((el) => {
      const r = el.getBoundingClientRect();
      return { block_id: el.getAttribute("data-block-id"), y: Math.round(r.top + window.scrollY), h: Math.round(r.height) };
    }),
  );

  const slices = [];
  for (const b of blocks) {
    const parts = Math.ceil(b.h / CAP);
    for (let i = 0; i < parts; i++) {
      const y = b.y + i * CAP;
      const h = Math.min(CAP, b.y + b.h - y);
      if (h > 0) slices.push({ block_id: b.block_id, part: `${i + 1}/${parts}`, y, h });
    }
  }

  for (const s of slices) {
    await page.screenshot({
      path: path.join(OUT, `clip-${s.block_id}-${s.part.replace("/", "of")}.png`),
      clip: { x: 0, y: s.y, width: 1200, height: s.h },
      captureBeyondViewport: true,
    });
  }
  await page.screenshot({ path: path.join(OUT, "full.png"), fullPage: true });

  fs.writeFileSync(path.join(OUT, "slices.json"), JSON.stringify({ slices }, null, 2));
  console.log(JSON.stringify({ slices: slices.length, cap: CAP }));
  await browser.close();
})().catch((e) => {
  console.error("COMPARE FAILED:", e.stack || e);
  process.exit(1);
});
