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
});
