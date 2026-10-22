// Dumps block and atomic element rects so slice plans can be computed offline.
// Cheap: no screenshots.
const fs = require("fs");
const puppeteer = require("puppeteer");

const URL = process.env.SPIKE_URL || "http://127.0.0.1:8899/report.html";
const OUT = process.env.SPIKE_OUT || "/spike/out";
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const ATOMIC =
  ".chart-box[data-chart-id], table, pre, blockquote, figure, img, .katex-display, h1, h2, h3, h4";

(async () => {
  const browser = await puppeteer.launch({
    executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
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
  const data = await page.evaluate((sel) => {
    const sy = window.scrollY;
    const box = (el) => {
      const r = el.getBoundingClientRect();
      return { y: Math.round(r.top + sy), h: Math.round(r.height) };
    };
    return {
      scroll_h: document.documentElement.scrollHeight,
      blocks: Array.from(document.querySelectorAll(".report-block-wrapper")).map((el) => ({
        id: el.getAttribute("data-block-id"),
        kind: el.getAttribute("data-block-kind"),
        ...box(el),
      })),
      atomic: Array.from(document.querySelectorAll(sel)).map((el) => ({ tag: el.tagName.toLowerCase(), ...box(el) })),
    };
  }, ATOMIC);
  fs.writeFileSync(`${OUT}/rects.json`, JSON.stringify(data, null, 2));
  console.log(JSON.stringify({ blocks: data.blocks.length, atomic: data.atomic.length, scroll_h: data.scroll_h }));
  await browser.close();
})().catch((e) => {
  console.error("RECTS FAILED:", e.stack || e);
  process.exit(1);
});
