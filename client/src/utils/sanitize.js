const SAFE_URL_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

const MARKDOWN_ALLOWED_TAGS = new Set([
  "A",
  "BLOCKQUOTE",
  "BR",
  "CODE",
  "DIV",
  "EM",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "LI",
  "OL",
  "P",
  "PRE",
  "SPAN",
  "STRONG",
  "TABLE",
  "TBODY",
  "TD",
  "TH",
  "THEAD",
  "TR",
  "UL",
]);

const REPORT_ALLOWED_TAGS = new Set([
  "HTML",
  "HEAD",
  "BODY",
  "META",
  "LINK",
  "TITLE",
  "STYLE",
  "SCRIPT",
  "DIV",
  "SPAN",
  "P",
  "BR",
  "HR",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "TABLE",
  "THEAD",
  "TBODY",
  "TFOOT",
  "TR",
  "TH",
  "TD",
  "CAPTION",
  "UL",
  "OL",
  "LI",
  "DL",
  "DT",
  "DD",
  "A",
  "STRONG",
  "EM",
  "B",
  "I",
  "U",
  "S",
  "MARK",
  "SMALL",
  "SUB",
  "SUP",
  "BLOCKQUOTE",
  "PRE",
  "CODE",
  "IMG",
  "FIGURE",
  "FIGCAPTION",
  "SECTION",
  "ARTICLE",
  "ASIDE",
  "HEADER",
  "FOOTER",
  "MAIN",
  "NAV",
  "DETAILS",
  "SUMMARY",
]);

const REPORT_REMOVE_TAGS = new Set([
  "IFRAME",
  "OBJECT",
  "EMBED",
  "BASE",
  "FORM",
  "INPUT",
  "BUTTON",
  "SELECT",
  "TEXTAREA",
  "NOSCRIPT",
]);

const DANGEROUS_ATTRS = new Set(["id", "content"]);

const SCRIPT_ID_ALLOWLIST = new Set([
  "oda-echarts-loader",
  "oda-chart-runtime",
  "oda-math-loader",
  "oda-math-auto-render",
  "oda-math-runtime",
]);

function sanitizeClassList(value) {
  return String(value || "")
    .split(/\s+/)
    .map((item) => item.trim())
    .filter((item) => item && /^[A-Za-z0-9_-]+$/.test(item))
    .join(" ");
}

function sanitizeURL(value) {
  const raw = String(value || "").trim();
  if (!raw) return "";
  if (raw.startsWith("//")) return "";
  if (raw.startsWith("#") || raw.startsWith("/")) return raw;
  try {
    const parsed = new URL(raw, window.location.origin);
    return SAFE_URL_PROTOCOLS.has(parsed.protocol) ? raw : "";
  } catch {
    return "";
  }
}

function isSameOriginURL(value) {
  const raw = String(value || "").trim();
  if (!raw) return false;
  if (raw.startsWith("/") && !raw.startsWith("//")) return true;
  try {
    const parsed = new URL(raw, window.location.origin);
    return (
      parsed.origin === window.location.origin &&
      SAFE_URL_PROTOCOLS.has(parsed.protocol)
    );
  } catch {
    return false;
  }
}

function sanitizeStyleText(text) {
  return String(text || "")
    .replace(/@import[^;]*(?:;|$)/gi, "")
    .replace(/url\s*\([^)]*\)/gi, "");
}

function sanitizeStyleValue(value) {
  const lower = String(value || "").toLowerCase();
  if (/[<]/.test(value)) return "";
  if (/(?:expression|javascript|vbscript|behavior|@import)\s*\(/i.test(value))
    return "";
  if (/url\s*\(\s*["']?(?:javascript|vbscript|data|blob)\s*:/i.test(value))
    return "";
  return value;
}

function cleanAttributes(
  node,
  {
    allowMarkdownClasses = false,
    allowChartStyle = false,
    allowReportAttrs = false,
  } = {},
) {
  const attrs = Array.from(node.attributes || []);
  for (const attr of attrs) {
    const name = attr.name.toLowerCase();
    const value = attr.value;
    if (name.startsWith("on")) {
      node.removeAttribute(attr.name);
      continue;
    }
    if (DANGEROUS_ATTRS.has(name)) {
      const allowed =
        name === "id" &&
        (allowReportAttrs ||
          (node.tagName === "SCRIPT" && SCRIPT_ID_ALLOWLIST.has(value)));
      if (!allowed) {
        node.removeAttribute(attr.name);
      }
      continue;
    }
    if (name === "href" || name === "src") {
      const safe =
        node.tagName === "LINK" && name === "href"
          ? isSameOriginURL(value)
            ? value
            : ""
          : sanitizeURL(value);
      if (!safe) {
        node.removeAttribute(attr.name);
      } else {
        node.setAttribute(attr.name, safe);
      }
      if (name === "href" && node.tagName === "A") {
        node.setAttribute("rel", "noopener noreferrer");
        node.setAttribute("target", "_blank");
      }
      continue;
    }
    if (name === "style") {
      if (allowChartStyle || allowReportAttrs) {
        const safe = sanitizeStyleValue(value);
        if (safe) {
          node.setAttribute(attr.name, safe);
        } else {
          node.removeAttribute(attr.name);
        }
      } else {
        node.removeAttribute(attr.name);
      }
      continue;
    }
    if (name === "class") {
      const safe = sanitizeClassList(value);
      if (
        !safe ||
        (!allowMarkdownClasses && !allowReportAttrs && node.tagName !== "BODY")
      ) {
        node.removeAttribute("class");
      } else {
        node.setAttribute("class", safe);
      }
      continue;
    }
    if (
      name === "target" ||
      name === "rel" ||
      name === "charset" ||
      name === "name"
    ) {
      continue;
    }
    if (name.startsWith("data-")) {
      if (allowReportAttrs) continue;
      node.removeAttribute(attr.name);
      continue;
    }
    if (
      allowReportAttrs &&
      [
        "id",
        "title",
        "colspan",
        "rowspan",
        "width",
        "height",
        "alt",
        "role",
        "aria-",
        "lang",
        "dir",
      ].some((ok) => name === ok || name.startsWith(ok))
    ) {
      continue;
    }
    if (!allowReportAttrs && ["title", "colspan", "rowspan"].includes(name)) {
      continue;
    }
    node.removeAttribute(attr.name);
  }
}

function sanitizeTree(root, options = {}) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
  const toRemove = [];
  const toUnwrap = [];
  while (walker.nextNode()) {
    const node = walker.currentNode;
    if (options.removeTags && options.removeTags.has(node.tagName)) {
      toRemove.push(node);
      continue;
    }
    if (options.allowedTags && !options.allowedTags.has(node.tagName)) {
      toUnwrap.push(node);
      continue;
    }
    cleanAttributes(node, options);
  }
  for (const node of toUnwrap.reverse()) {
    const parent = node.parentNode;
    if (!parent) continue;
    while (node.firstChild) {
      parent.insertBefore(node.firstChild, node);
    }
    parent.removeChild(node);
  }
  for (const node of toRemove.reverse()) {
    node.parentNode?.removeChild(node);
  }
}

export function sanitizeMarkdownHTML(html) {
  const parser = new DOMParser();
  const doc = parser.parseFromString(String(html || ""), "text/html");
  sanitizeTree(doc.body, {
    allowedTags: MARKDOWN_ALLOWED_TAGS,
    allowMarkdownClasses: true,
  });
  return doc.body.innerHTML;
}

const CDN_HOSTS = new Set(["cdn.jsdelivr.net", "cdnjs.cloudflare.com"]);

function isEChartsLoaderScript(node) {
  const src = node.getAttribute("src") || "";
  const id = node.getAttribute("id") || "";
  if (id !== "oda-echarts-loader" || !src) return false;
  try {
    const url = new URL(src, window.location.origin);
    const path = url.pathname.toLowerCase();
    if (
      url.origin === window.location.origin &&
      path === "/assets/echarts.min.js"
    ) {
      return true;
    }
    return CDN_HOSTS.has(url.hostname) && path.endsWith("echarts.min.js");
  } catch {
    return false;
  }
}

function isMathScript(node) {
  const src = node.getAttribute("src") || "";
  const id = node.getAttribute("id") || "";
  if (!id.startsWith("oda-math") || !src) return false;
  try {
    const url = new URL(src, window.location.origin);
    const path = url.pathname.toLowerCase();
    if (
      url.origin === window.location.origin &&
      path === "/oda-math-runtime.js"
    ) {
      return true;
    }
    return (
      CDN_HOSTS.has(url.hostname) &&
      (path.includes("katex") ||
        path.includes("mathjax") ||
        path.includes("auto-render"))
    );
  } catch {
    return false;
  }
}

function isChartRuntimeScript(node) {
  const src = node.getAttribute("src") || "";
  const id = node.getAttribute("id") || "";
  if (id !== "oda-chart-runtime" || !src) return false;
  try {
    const url = new URL(src, window.location.origin);
    return (
      url.origin === window.location.origin &&
      url.pathname === "/oda-chart-runtime.js"
    );
  } catch {
    return false;
  }
}

export function sanitizeReportHTML(html) {
  const parser = new DOMParser();
  const doc = parser.parseFromString(String(html || ""), "text/html");

  sanitizeTree(doc.documentElement, {
    allowedTags: REPORT_ALLOWED_TAGS,
    removeTags: REPORT_REMOVE_TAGS,
    allowChartStyle: true,
    allowReportAttrs: true,
  });

  doc.querySelectorAll("script").forEach((node) => {
    const isLoader = isEChartsLoaderScript(node);
    const isRuntime = isChartRuntimeScript(node);
    const isMath = isMathScript(node);
    if (!isLoader && !isRuntime && !isMath) {
      node.remove();
    }
  });

  doc.querySelectorAll("style").forEach((node) => {
    node.textContent = sanitizeStyleText(node.textContent);
  });

  const bodyClass = sanitizeClassList(doc.body.getAttribute("class"));
  if (bodyClass) {
    doc.body.setAttribute("class", bodyClass);
  } else {
    doc.body.removeAttribute("class");
  }

  return `<!DOCTYPE html>\n${doc.documentElement.outerHTML}`;
}
