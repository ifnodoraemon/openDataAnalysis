import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { JSDOM } from "jsdom";
import { sanitizeReportHTML } from "./sanitize";

describe("sanitizeReportHTML", () => {
  let cleanup = {};

  beforeEach(() => {
    const dom = new JSDOM("<!doctype html><html><body></body></html>", {
      url: "http://localhost/",
    });
    cleanup.window = global.window;
    cleanup.document = global.document;
    cleanup.DOMParser = global.DOMParser;
    cleanup.NodeFilter = global.NodeFilter;
    global.window = dom.window;
    global.document = dom.window.document;
    global.DOMParser = dom.window.DOMParser;
    global.NodeFilter = dom.window.NodeFilter;
  });

  afterEach(() => {
    global.window = cleanup.window;
    global.document = cleanup.document;
    global.DOMParser = cleanup.DOMParser;
    global.NodeFilter = cleanup.NodeFilter;
  });

  it("preserves trusted ECharts loader/runtime scripts for report previews", () => {
    const html = `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <script id="oda-echarts-loader" src="/assets/echarts.min.js"></script>
</head>
<body>
  <div class="chart-box" data-chart-id="chart_sales" data-chart-option='{"series":[{"type":"bar","data":[1]}]}' style="height: 400px"></div>
  <script id="oda-chart-runtime" src="/oda-chart-runtime.js"></script>
</body>
</html>`;

    const sanitized = sanitizeReportHTML(html);

    expect(sanitized).toContain('id="oda-echarts-loader"');
    expect(sanitized).toContain("/assets/echarts.min.js");
    expect(sanitized).toContain('id="oda-chart-runtime"');
    expect(sanitized).toContain('src="/oda-chart-runtime.js"');
    expect(sanitized).toContain('data-chart-id="chart_sales"');
    expect(sanitized).toContain("data-chart-option");
  });

  it("removes untrusted inline scripts", () => {
    const html = `<!DOCTYPE html><html><body>
      <script>alert('xss')</script>
      <div class="chart-box" data-chart-id="chart_sales"></div>
    </body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("alert('xss')");
  });

  it("removes inline chart runtime scripts even when they look valid", () => {
    const html = `<!DOCTYPE html><html><body>
      <div class="chart-box" data-chart-id="chart_sales" data-chart-option='{"series":[{"type":"bar","data":[1]}]}'></div>
      <script id="oda-chart-runtime">
        document.addEventListener('DOMContentLoaded', function () {
          document.querySelectorAll('.chart-box[data-chart-id]').forEach(function (el) {
            echarts.init(el).setOption(JSON.parse(el.dataset.chartOption));
          });
        });
      </script>
      <script id="oda-chart-runtime" src="/oda-chart-runtime.js"></script>
    </body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("echarts.init(el)");
    expect(sanitized).toContain('src="/oda-chart-runtime.js"');
  });

  it("strips @import and url() constructs from style element text", () => {
    const html = `<!DOCTYPE html><html><head><style>
      @import url("http://evil.example/evil.css");
      .box { background: url(http://evil.example/i.png); color: #333333; }
    </style></head><body><div class="box">ok</div></body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("@import");
    expect(sanitized).not.toContain("url(");
    expect(sanitized).toContain("color: #333333");
  });

  it("restricts link href to same-origin or relative targets", () => {
    const html = `<!DOCTYPE html><html><head>
      <link rel="stylesheet" href="https://cdn.evil.example/theme.css">
      <link rel="stylesheet" href="/assets/report.css">
    </head><body><p>x</p></body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("cdn.evil.example");
    expect(sanitized).toContain("/assets/report.css");
  });

  it("rejects protocol-relative URLs in href and src", () => {
    const html = `<!DOCTYPE html><html><body>
      <a href="//evil.example/page">link</a>
      <img src="//evil.example/img.png">
    </body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("//evil.example");
    expect(sanitized).toContain(">link</a>");
  });

  it("removes dangerous attributes but keeps allowlisted report ids", () => {
    const html = `<!DOCTYPE html><html><body>
      <div id="keep-id" content="evil">x</div>
    </body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("content=");
    expect(sanitized).toContain('id="keep-id"');
  });

  it("rejects CDN-hosted ECharts loader scripts even on allowlisted CDNs", () => {
    const html = `<!DOCTYPE html><html><head>
      <script id="oda-echarts-loader" src="https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js"></script>
      <script id="oda-echarts-loader" src="https://cdn.jsdelivr.net/gh/attacker/evil@1/echarts.min.js"></script>
    </head><body><p>x</p></body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("cdn.jsdelivr.net");
    expect(sanitized).not.toContain("attacker");
  });

  it("accepts same-origin KaTeX loader scripts with exact paths only", () => {
    const html = `<!DOCTYPE html><html><head>
      <script id="oda-math-loader" src="/assets/katex/katex.min.js"></script>
      <script id="oda-math-auto-render" src="/assets/katex/contrib/auto-render.min.js"></script>
      <script id="oda-math-loader" src="/assets/katex/../../evil.js"></script>
      <script id="oda-math-loader" src="https://cdn.jsdelivr.net/npm/katex@0.16.9/dist/katex.min.js"></script>
    </head><body><p>x</p></body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).toContain('id="oda-math-loader"');
    expect(sanitized).toContain("/assets/katex/katex.min.js");
    expect(sanitized).toContain("/assets/katex/contrib/auto-render.min.js");
    expect(sanitized).not.toContain("evil.js");
    expect(sanitized).not.toContain("cdn.jsdelivr.net");
  });

  it("rejects backslash URL bypasses like /\\evil.com", () => {
    const html = `<!DOCTYPE html><html><body>
      <a href="/\\evil.com/page">link</a>
      <img src="/\\evil.com/img.png">
    </body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("evil.com");
    expect(sanitized).toContain(">link</a>");
  });

  it("strips url() from inline style attribute values", () => {
    const html = `<!DOCTYPE html><html><body>
      <div style="background: url(https://evil.example/i.png); height: 200px">chart</div>
    </body></html>`;

    const sanitized = sanitizeReportHTML(html);
    expect(sanitized).not.toContain("url(");
    expect(sanitized).not.toContain("evil.example");
  });
});
