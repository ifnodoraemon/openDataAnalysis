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
  // Browsers normalize backslashes to slashes, so "/\evil.com" parses as the
  // protocol-relative URL "//evil.com" despite not starting with "//".
  if (raw.includes("\\")) return "";
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
  if (raw.includes("\\")) return false;
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
  // Inline style values must not carry any url() load; background images can
  // use <img> elements that go through the attribute sanitizer instead.
  if (/url\s*\(/i.test(value)) return "";
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

// Report scripts load exclusively from same-origin static assets; every
// (id, path) pair is an exact match, so CDN URLs and path tricks like
// /assets/echarts.min.js/../../evil.js are rejected.
const REPORT_SCRIPT_SOURCES = new Map([
  ["oda-echarts-loader", "/assets/echarts.min.js"],
  ["oda-chart-runtime", "/oda-chart-runtime.js"],
  ["oda-math-loader", "/assets/katex/katex.min.js"],
  ["oda-math-auto-render", "/assets/katex/contrib/auto-render.min.js"],
  ["oda-math-runtime", "/oda-math-runtime.js"],
]);

function isAllowedReportScript(node) {
  const src = node.getAttribute("src") || "";
  const id = node.getAttribute("id") || "";
  if (!id || !src) return false;
  const expected = REPORT_SCRIPT_SOURCES.get(id);
  if (!expected) return false;
  try {
    const url = new URL(src, window.location.origin);
    return url.origin === window.location.origin && url.pathname === expected;
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
    if (!isAllowedReportScript(node)) {
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
