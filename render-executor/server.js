// render-executor: headless capture service for report views.
//
// Contract (see docs/report-view-capture.zh-CN.md §5.3):
//   POST /capture  {html, asset_base_url, viewport{width,dpr}, timeout_ms}
//     -> {png_base64, rects, render_facts, settle_ms, capture_ms}
//   GET  /healthz  -> {ok, chromium, puppeteer}
//
// Why a service instead of in-process rendering: the backend is Go, and the
// capture logic (readiness detection, full-page shot, rect extraction) is the
// part that took a spike to get right. Rewriting it in Go would be a second
// implementation of the tricky part; wrapping the proven one is not.
//
// Security: the input is model-authored HTML that the backend has already
// sanitized. This process still refuses to load anything except data: URLs and
// the configured asset origin, so a sanitizer gap cannot turn into an outbound
// request from the capture host.
const http = require("http");
const crypto = require("crypto");
const puppeteer = require("puppeteer");

const PORT = Number(process.env.PORT || 8082);
const PROXY_TOKEN = (process.env.PROXY_TOKEN || "").trim();
const MAX_PAGES = Number(process.env.MAX_CONCURRENT_CAPTURES || 2);
const DEFAULT_TIMEOUT_MS = Number(process.env.CAPTURE_TIMEOUT_MS || 8000);
const MAX_HTML_BYTES = Number(process.env.MAX_HTML_BYTES || 8 * 1024 * 1024);
// 960 keeps a 1200px-wide slice under the per-image pixel budget of vision
// encoders, so the provider does not downscale it again.
const MAX_SLICE_HEIGHT_REPORTED = 100000; // sanity bound for rect payloads

const ATOMIC_SELECTORS = [
  ".chart-box[data-chart-id]",
  "table",
  "pre",
  "blockquote",
  "figure",
  "img",
  ".katex-display",
  "h1",
  "h2",
  "h3",
  "h4",
];

let browserPromise = null;
let active = 0;
const waiting = [];
let cachedChromiumVersion = "";

function log(message, extra) {
  const record = { ts: new Date().toISOString(), msg: message, ...extra };
  console.log(JSON.stringify(record));
}

async function getBrowser() {
  if (!browserPromise) {
    browserPromise = puppeteer
      .launch({
        executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
        args: [
          "--no-sandbox",
          "--disable-dev-shm-usage",
          "--hide-scrollbars",
          "--force-color-profile=srgb",
          "--font-render-hinting=none",
          "--disable-gpu",
        ],
      })
      .then(async (browser) => {
        cachedChromiumVersion = await browser.version();
        browser.on("disconnected", () => {
          browserPromise = null;
        });
        return browser;
      })
      .catch((error) => {
        browserPromise = null;
        throw error;
      });
  }
  return browserPromise;
}

// A capture holds one page for its whole lifetime; bounding concurrency keeps a
// burst of report edits from exhausting memory in the container.
async function withSlot(fn) {
  if (active >= MAX_PAGES) {
    await new Promise((resolve) => waiting.push(resolve));
  }
  active += 1;
  try {
    return await fn();
  } finally {
    active -= 1;
    const next = waiting.shift();
    if (next) next();
  }
}

function injectBase(html, assetBaseURL) {
  if (!assetBaseURL) return html;
  const href = String(assetBaseURL).replace(/"/g, "&quot;");
  const base = `<base href="${href}">`;
  if (/<head[^>]*>/i.test(html)) {
    return html.replace(/<head[^>]*>/i, (match) => match + base);
  }
  return base + html;
}

function originOf(url) {
  try {
    return new URL(url).origin;
  } catch (error) {
    return "";
  }
}

function allowedRequest(rawURL, assetOrigin) {
  const url = String(rawURL || "");
  if (!url) return false;
  if (url === "about:blank" || url.startsWith("about:")) return true;
  if (url.startsWith("data:") || url.startsWith("blob:")) return true;
  const origin = originOf(url);
  return Boolean(assetOrigin) && origin === assetOrigin;
}

async function waitForSettle(page, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let previous = null;
  let samples = 0;
  while (Date.now() < deadline) {
    const state = await page.evaluate(() => {
      const charts = Array.from(document.querySelectorAll(".chart-box[data-chart-id]"));
      return {
        fonts: document.fonts ? document.fonts.status : "n/a",
        imagesReady: Array.from(document.images).every((image) => image.complete),
        scroll: {
          w: document.documentElement.scrollWidth,
          h: document.documentElement.scrollHeight,
        },
        canvases: charts.map((element) => {
          const canvas = element.querySelector("canvas");
          if (!canvas) {
            return { id: element.getAttribute("data-chart-id"), len: -1 };
          }
          let len = 0;
          try {
            len = canvas.toDataURL("image/png").length;
          } catch (error) {
            len = -2;
          }
          return { id: element.getAttribute("data-chart-id"), len };
        }),
      };
    });
    samples += 1;
    // Readiness means "nothing is still moving", not "the page looks correct".
    // Requiring a canvas would make a chart-free report wait out the timeout.
    const stable =
      previous &&
      state.fonts === "loaded" &&
      state.imagesReady &&
      JSON.stringify(previous.canvases) === JSON.stringify(state.canvases) &&
      JSON.stringify(previous.scroll) === JSON.stringify(state.scroll);
    if (stable) return { ok: true, samples, state };
    previous = state;
    await new Promise((resolve) => setTimeout(resolve, 120));
  }
  return { ok: false, samples, state: previous };
}

async function collectFacts(page, blocked) {
  return page.evaluate(
    (atomicSelectors, blockedURLs) => {
      const scrollY = window.scrollY;
      const box = (element) => {
        const rect = element.getBoundingClientRect();
        return {
          y: Math.round(rect.top + scrollY),
          h: Math.round(rect.height),
          x: Math.round(rect.left),
          w: Math.round(rect.width),
        };
      };
      const blocks = Array.from(document.querySelectorAll(".report-block-wrapper")).map(
        (element) => ({
          block_id: element.getAttribute("data-block-id"),
          kind: element.getAttribute("data-block-kind"),
          ...box(element),
        }),
      );
      const atomic = [];
      for (const selector of atomicSelectors) {
        for (const element of Array.from(document.querySelectorAll(selector))) {
          const rect = box(element);
          if (rect.h > 0) atomic.push({ tag: element.tagName.toLowerCase(), ...rect });
        }
      }
      const charts = Array.from(document.querySelectorAll(".chart-box[data-chart-id]")).map(
        (element) => {
          const canvas = element.querySelector("canvas");
          return {
            id: element.getAttribute("data-chart-id"),
            mounted: Boolean(canvas),
            canvas_w: canvas ? canvas.width : 0,
            canvas_h: canvas ? canvas.height : 0,
          };
        },
      );
      return {
        scroll: {
          w: document.documentElement.scrollWidth,
          h: document.documentElement.scrollHeight,
        },
        viewport_width: window.innerWidth,
        overflow_x: document.documentElement.scrollWidth > window.innerWidth + 1,
        blocks,
        atomic,
        charts,
        charts_missing: charts.filter((chart) => !chart.mounted).map((chart) => chart.id),
        katex_errors: document.querySelectorAll(".katex-error").length,
        images_failed: Array.from(document.images)
          .filter((image) => !image.complete || image.naturalWidth === 0)
          .map((image) => image.getAttribute("src")),
        blocked_requests: blockedURLs,
      };
    },
    ATOMIC_SELECTORS,
    blocked,
  );
}

async function capture(body) {
  const html = String(body.html || "");
  if (!html.trim()) throw httpError(400, "html is required");
  if (Buffer.byteLength(html, "utf8") > MAX_HTML_BYTES) {
    throw httpError(413, `html exceeds ${MAX_HTML_BYTES} bytes`);
  }
  const assetBaseURL = String(body.asset_base_url || "").trim();
  const assetOrigin = originOf(assetBaseURL);
  const width = Number((body.viewport && body.viewport.width) || 1200);
  const dpr = Number((body.viewport && body.viewport.dpr) || 1);
  if (!Number.isFinite(width) || width < 320 || width > 4096) {
    throw httpError(400, "viewport.width must be between 320 and 4096");
  }
  if (!Number.isFinite(dpr) || dpr <= 0 || dpr > 4) {
    throw httpError(400, "viewport.dpr must be between 0 and 4");
  }
  const timeoutMs = Math.min(
    DEFAULT_TIMEOUT_MS,
    Math.max(1000, Number(body.timeout_ms || DEFAULT_TIMEOUT_MS)),
  );

  return withSlot(async () => {
    const browser = await getBrowser();
    const page = await browser.newPage();
    const blocked = [];
    const pageErrors = [];
    try {
      await page.setViewport({ width, height: 1200, deviceScaleFactor: dpr });
      await page.setRequestInterception(true);
      page.on("request", (request) => {
        if (allowedRequest(request.url(), assetOrigin)) {
          request.continue().catch(() => {});
          return;
        }
        blocked.push(request.url().slice(0, 300));
        request.abort("blockedbyclient").catch(() => {});
      });
      page.on("pageerror", (error) => {
        pageErrors.push(String(error).slice(0, 300));
      });

      const startedAt = Date.now();
      await page.setContent(injectBase(html, assetBaseURL), { waitUntil: "load", timeout: timeoutMs });
      const settle = await waitForSettle(page, timeoutMs);
      const raw = await collectFacts(page, blocked);
      // Rects travel separately; keeping them inside the facts would repeat a few
      // hundred numbers in every prompt for no signal.
      const rects = { scroll: raw.scroll, blocks: raw.blocks, atomic: raw.atomic };
      const facts = { ...raw };
      delete facts.blocks;
      delete facts.atomic;
      facts.page_errors = pageErrors;
      facts.scroll_settled = settle.ok;
      facts.readiness_samples = settle.samples;
      facts.blocked_requests = blocked;
      // Rects are the contract the caller plans slices from; a page taller than
      // this is not a report.
      if (facts.scroll.h > MAX_SLICE_HEIGHT_REPORTED * 10) {
        throw httpError(422, "rendered page is unexpectedly tall");
      }
      const shotAt = Date.now();
      const png = await page.screenshot({ fullPage: true, encoding: "base64" });
      const captureMS = Date.now() - shotAt;
      return {
        png_base64: png,
        rects,
        render_facts: facts,
        settle_ms: Date.now() - startedAt - captureMS,
        capture_ms: captureMS,
      };
    } finally {
      await page.close().catch(() => {});
    }
  });
}

function httpError(status, message) {
  const error = new Error(message);
  error.status = status;
  return error;
}

function sendJSON(res, status, payload) {
  const body = JSON.stringify(payload);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(body),
  });
  res.end(body);
}

function authorized(req) {
  if (!PROXY_TOKEN) return false;
  const token = String(req.headers["x-proxy-token"] || "");
  if (token.length !== PROXY_TOKEN.length) return false;
  // Constant-time comparison: the length check above already short-circuits the
  // obvious mismatch, and timingSafeEqual requires equal lengths.
  return crypto.timingSafeEqual(Buffer.from(token), Buffer.from(PROXY_TOKEN));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let size = 0;
    const chunks = [];
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > MAX_HTML_BYTES + 4096) {
        reject(httpError(413, "request body too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      if (!raw.trim()) {
        resolve({});
        return;
      }
      try {
        resolve(JSON.parse(raw));
      } catch (error) {
        reject(httpError(400, "request body must be JSON"));
      }
    });
    req.on("error", reject);
  });
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://render-executor");
  if (req.method === "GET" && url.pathname === "/healthz") {
    let chromium = cachedChromiumVersion;
    if (!chromium) {
      try {
        chromium = (await getBrowser()).version ? await (await getBrowser()).version() : "";
      } catch (error) {
        sendJSON(res, 503, { ok: false, error: String(error).slice(0, 200) });
        return;
      }
    }
    sendJSON(res, 200, { ok: true, chromium, active, queued: waiting.length });
    return;
  }
  if (req.method !== "POST" || url.pathname !== "/capture") {
    sendJSON(res, 404, { error: "not found" });
    return;
  }
  if (!PROXY_TOKEN) {
    sendJSON(res, 503, { error: "PROXY_TOKEN not configured; access disabled" });
    return;
  }
  if (!authorized(req)) {
    sendJSON(res, 401, { error: "unauthorized" });
    return;
  }
  try {
    const body = await readBody(req);
    const result = await capture(body);
    log("capture.ok", {
      block_count: result.rects.blocks.length,
      scroll_h: result.rects.scroll.h,
      settle_ms: result.settle_ms,
      capture_ms: result.capture_ms,
      blocked: result.render_facts.blocked_requests.length,
      charts_missing: result.render_facts.charts_missing.length,
    });
    // The PNG is the bulk of the response; base64 keeps it a single JSON field and
    // avoids a second round trip for a binary body.
    const payload = JSON.stringify(result);
    res.writeHead(200, {
      "Content-Type": "application/json; charset=utf-8",
      "Content-Length": Buffer.byteLength(payload),
    });
    res.end(payload);
  } catch (error) {
    const status = error.status || 500;
    if (status >= 500) {
      log("capture.error", { status, error: String(error).slice(0, 300) });
    }
    sendJSON(res, status, { error: String(error.message || error).slice(0, 300) });
  }
});

server.requestTimeout = 0;
server.headersTimeout = 0;
server.listen(PORT, () => {
  log("listening", { port: PORT, max_pages: MAX_PAGES, token_configured: Boolean(PROXY_TOKEN) });
});

function shutdown(signal) {
  log("shutdown", { signal });
  server.close(() => {});
  if (browserPromise) {
    browserPromise.then((browser) => browser.close().catch(() => {})).catch(() => {});
  }
  setTimeout(() => process.exit(0), 3000).unref();
}
process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));
