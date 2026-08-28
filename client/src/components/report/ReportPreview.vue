<template>
  <div class="report-preview">
    <div class="panel-header">
      <div class="header-main">
        <span>📄 研报预览</span>
        <span v-if="runMetaLabel" class="run-meta">{{ runMetaLabel }}</span>
      </div>
      <div class="toolbar">
        <button
          class="toolbar-btn"
          :class="{ active: mode === 'preview' }"
          @click="mode = 'preview'"
        >
          预览
        </button>
        <button
          class="toolbar-btn"
          :class="{ active: mode === 'source' }"
          @click="mode = 'source'"
        >
          源码
        </button>
        <div v-if="reportHTML" class="export-menu">
          <button
            class="toolbar-btn"
            :disabled="isRunning"
            @click="quoteWholeReport"
          >
            编辑整篇
          </button>
          <button class="toolbar-btn export" @click="toggleExportMenu">
            ⬇ 导出报告
          </button>
          <div v-if="showExportMenu" class="export-dropdown">
            <button class="export-option" @click="exportReport('pdf')">
              导出 PDF
            </button>
            <button class="export-option" @click="exportReport('word')">
              导出 Word
            </button>
            <button class="export-option" @click="exportReport('html')">
              导出 HTML
            </button>
          </div>
        </div>
      </div>
    </div>
    <div v-if="mode === 'preview' && reportHTML" class="edit-strip">
      <template v-if="selectedBlockId">
        <div class="edit-meta">
          <span class="edit-label">{{
            selectedByText ? "选中文本" : "当前章节"
          }}</span>
          <span class="edit-value">{{ selectedBlockLabel }}</span>
        </div>
        <p class="selection-preview">{{ selectedBlockText }}</p>
        <div class="edit-actions">
          <button
            class="toolbar-btn primary"
            :disabled="isRunning || !selectedBlockText"
            @click="quoteSelection"
          >
            引用到对话
          </button>
          <button
            class="toolbar-btn"
            :disabled="isRunning"
            @click="clearSelection"
          >
            取消选择
          </button>
        </div>
      </template>
      <p v-else class="edit-hint">
        划词选中报告内容后引用到左侧对话；也可以点击章节引用整段。
      </p>
    </div>
    <div class="preview-area">
      <div v-if="!reportHTML" class="empty-state">
        <div class="empty-icon">📊</div>
        <p>研究报告将在这里实时渲染</p>
      </div>
      <!--
        sandbox with allow-same-origin is NOT a security boundary:
        the DOM sanitizer (sanitizeReportHTML) is the primary defense.
        allow-same-origin is required so the iframe can access its own
        canvas elements for ECharts rendering and PDF/Word export.
      -->
      <iframe
        v-else-if="mode === 'preview'"
        ref="reportFrame"
        :srcdoc="sanitizedReportHTML"
        title="研究报告预览"
        class="report-iframe"
        sandbox="allow-scripts allow-same-origin"
        @load="handleFrameLoad"
      ></iframe>
      <pre v-else class="source-code">{{ sanitizedReportHTML }}</pre>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useAgentStore } from "../../stores/agent.js";
import { sanitizeReportHTML } from "../../utils/sanitize.js";

const store = useAgentStore();
const reportHTML = computed(() => store.reportHTML);
const sanitizedReportHTML = computed(() =>
  sanitizeReportHTML(reportHTML.value),
);
const selectedRun = computed(() => store.getRun(store.selectedRunId) || null);
const activeRun = computed(() => store.getRun(store.activeRunId) || null);
const selectedReport = computed(() => selectedRun.value?.report || null);
const isRunning = computed(() => store.isRunning);
const reportFrame = ref(null);
const showExportMenu = ref(false);
const selectedBlockId = ref("");
const selectedFragmentIndex = ref("");
const selectedBlockLabel = ref("");
const selectedBlockText = ref("");
const selectedSelectionStart = ref(null);
const selectedSelectionEnd = ref(null);
const selectedByText = ref(false);
const runMetaLabel = computed(() => {
  if (selectedReport.value?.title) {
    const suffix = selectedReport.value.author
      ? ` · ${selectedReport.value.author}`
      : "";
    return `${selectedReport.value.title}${suffix}`;
  }
  if (
    selectedRun.value?.id &&
    activeRun.value?.id &&
    selectedRun.value.id !== activeRun.value.id
  ) {
    return `当前查看历史任务 ${selectedRun.value.id}`;
  }
  if (selectedRun.value?.id) {
    return `当前报告 ${selectedRun.value.id}`;
  }
  if (activeRun.value?.id) {
    return `正在执行 ${activeRun.value.id}`;
  }
  return "";
});
const mode = ref("preview");
const pendingTimers = new Set();

function scheduleTimer(callback, delay) {
  const timerId = window.setTimeout(() => {
    pendingTimers.delete(timerId);
    callback();
  }, delay);
  pendingTimers.add(timerId);
  return timerId;
}

watch(
  sanitizedReportHTML,
  () => {
    showExportMenu.value = false;
    clearSelection();
  },
  { immediate: true },
);

watch(mode, (nextMode) => {
  if (nextMode !== "preview") {
    clearSelection();
  }
});

onBeforeUnmount(() => {
  pendingTimers.forEach((timerId) => window.clearTimeout(timerId));
  pendingTimers.clear();
  const doc = reportFrame.value?.contentWindow?.document;
  doc?.removeEventListener("mouseup", handleFrameMouseUp);
  doc?.removeEventListener("keyup", handleFrameMouseUp);
});

function toggleExportMenu() {
  showExportMenu.value = !showExportMenu.value;
}

function clearSelection() {
  selectedBlockId.value = "";
  selectedFragmentIndex.value = "";
  selectedBlockLabel.value = "";
  selectedBlockText.value = "";
  selectedSelectionStart.value = null;
  selectedSelectionEnd.value = null;
  selectedByText.value = false;
  applySelectionHighlight("");
}

function handleFrameLoad() {
  decorateFrameBlocks();
  if (selectedFragmentIndex.value) {
    applySelectionHighlight(selectedFragmentIndex.value);
  }
}

function decorateFrameBlocks() {
  const doc = reportFrame.value?.contentWindow?.document;
  if (!doc) return;

  ensureFrameStyles(doc);
  doc.removeEventListener("mouseup", handleFrameMouseUp);
  doc.removeEventListener("keyup", handleFrameMouseUp);
  doc.addEventListener("mouseup", handleFrameMouseUp);
  doc.addEventListener("keyup", handleFrameMouseUp);
  doc.querySelectorAll("[data-block-id]").forEach((node, idx) => {
    if (node.dataset.codexBound === "1") return;
    node.dataset.codexBound = "1";
    node.dataset.fragmentIndex = String(idx);
    node.classList.add("report-block-selectable");
    node.addEventListener("click", handleBlockClick);
  });
}

function ensureFrameStyles(doc) {
  if (doc.getElementById("report-block-selection-style")) return;
  const style = doc.createElement("style");
  style.id = "report-block-selection-style";
  style.textContent = `
    .report-block-selectable {
      cursor: pointer;
      transition: outline-color 0.16s ease, box-shadow 0.16s ease;
    }
    .report-block-selectable:hover {
      outline: 2px solid rgba(37, 99, 235, 0.45);
      outline-offset: 4px;
    }
    .report-block-selectable.report-block-selected {
      outline: 3px solid #2563eb;
      outline-offset: 4px;
      box-shadow: 0 0 0 6px rgba(37, 99, 235, 0.12);
    }
    ::selection {
      background: rgba(37, 99, 235, 0.24);
    }
  `;
  doc.head.appendChild(style);
}

function handleFrameMouseUp() {
  scheduleTimer(() => {
    const frameWindow = reportFrame.value?.contentWindow;
    const selection = frameWindow?.getSelection?.();
    const selectedText = selection?.toString?.().trim() || "";
    if (!selection || selection.isCollapsed || !selectedText) return;

    const range = selection.rangeCount > 0 ? selection.getRangeAt(0) : null;
    const block = findClosestReportBlock(range?.commonAncestorContainer);
    if (!block) return;
    const offsets = computeSelectionOffsets(block, range);

    selectReportBlock(block, {
      text: selectedText,
      byText: true,
      selectionStart: offsets?.start ?? null,
      selectionEnd: offsets?.end ?? null,
    });
  }, 0);
}

function handleBlockClick(event) {
  const frameSelection = reportFrame.value?.contentWindow?.getSelection?.();
  if (
    frameSelection &&
    !frameSelection.isCollapsed &&
    frameSelection.toString().trim()
  ) {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  const block = event.currentTarget;
  selectReportBlock(block, {
    text: block.textContent?.trim() || "",
    byText: false,
  });
}

function selectReportBlock(block, options = {}) {
  const blockId = block?.dataset?.blockId || "";
  if (!blockId) return;

  const fragmentIdx = block?.dataset?.fragmentIndex || "";
  selectedBlockId.value = blockId;
  selectedFragmentIndex.value = fragmentIdx;
  selectedBlockLabel.value = extractBlockLabel(block);
  selectedBlockText.value = trimSelectionText(options.text || "");
  selectedSelectionStart.value = Number.isInteger(options.selectionStart)
    ? options.selectionStart
    : null;
  selectedSelectionEnd.value = Number.isInteger(options.selectionEnd)
    ? options.selectionEnd
    : null;
  selectedByText.value = !!options.byText;
  applySelectionHighlight(fragmentIdx);
}

function findClosestReportBlock(node) {
  if (!node) return null;
  const element =
    node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
  return element?.closest?.("[data-block-id]") || null;
}

function extractBlockLabel(block) {
  const heading = block.querySelector("h1, h2, h3, h4, h5, h6");
  const headingText = heading?.textContent?.trim();
  return (
    headingText ||
    block.dataset.blockTitle ||
    block.dataset.blockId ||
    "未命名段落"
  );
}

function applySelectionHighlight(fragmentIdx) {
  const doc = reportFrame.value?.contentWindow?.document;
  if (!doc) return;
  doc.querySelectorAll("[data-block-id]").forEach((node) => {
    node.classList.toggle(
      "report-block-selected",
      node.dataset.fragmentIndex === fragmentIdx && fragmentIdx !== "",
    );
  });
}

function quoteSelection() {
  if (!selectedBlockId.value || !selectedBlockText.value) return;
  store.setReportQuote({
    scopeKind: selectedByText.value ? "partial_selection" : "partial_block",
    targetRunId: selectedRun.value?.id || activeRun.value?.id || "",
    blockId: selectedBlockId.value,
    blockLabel: selectedBlockLabel.value,
    selectionText: selectedBlockText.value,
    selectionStart: selectedSelectionStart.value,
    selectionEnd: selectedSelectionEnd.value,
    selectionRangeSet:
      selectedByText.value &&
      Number.isInteger(selectedSelectionStart.value) &&
      Number.isInteger(selectedSelectionEnd.value),
  });
}

function quoteWholeReport() {
  const label =
    selectedReport.value?.title || selectedRun.value?.id || "当前报告";
  store.setReportQuote({
    scopeKind: "whole_report",
    targetRunId: selectedRun.value?.id || activeRun.value?.id || "",
    blockId: "",
    blockLabel: label,
    selectionText: `整篇报告：${label}`,
  });
  clearSelection();
}

function trimSelectionText(text) {
  const normalized = String(text || "")
    .replace(/\s+/g, " ")
    .trim();
  return normalized.length > 3000
    ? `${normalized.slice(0, 3000)}...`
    : normalized;
}

function computeSelectionOffsets(block, range) {
  if (!block || !range) return null;
  try {
    const fullRange = block.ownerDocument.createRange();
    fullRange.selectNodeContents(block);

    const startRange = fullRange.cloneRange();
    startRange.setEnd(range.startContainer, range.startOffset);
    const start = startRange.toString().length;
    const length = range.toString().length;
    return {
      start,
      end: start + length,
    };
  } catch {
    return null;
  }
}

async function exportReport(format) {
  showExportMenu.value = false;
  if (!reportHTML.value) return;
  try {
    if (format === "pdf") {
      await exportPDF();
      return;
    }
    if (format === "word") {
      await exportWord();
      return;
    }
    exportHTML();
  } catch (error) {
    const message = error instanceof Error ? error.message : "报告导出失败";
    store.addMessage({ type: "error", content: message });
  }
}

function exportHTML() {
  downloadBlob(
    new Blob([sanitizedReportHTML.value], { type: "text/html;charset=utf-8" }),
    `${buildFilename()}.html`,
  );
}

async function exportWord() {
  const targetRunId = selectedRun.value?.id || activeRun.value?.id || "";
  if (!targetRunId) {
    throw new Error("缺少已完成报告对应的任务 ID");
  }
  const res = await fetch("/api/report-exports/docx", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(store.token ? { Authorization: `Bearer ${store.token}` } : {}),
    },
    body: JSON.stringify({ runId: targetRunId }),
  });
  if (!res.ok) {
    throw new Error(await res.text());
  }
  const blob = await res.blob();
  downloadBlob(blob, `${buildFilename()}.docx`);
}

async function exportPDF() {
  const snapshotHTML = await buildRenderedSnapshotHTML();
  const url = URL.createObjectURL(
    new Blob([snapshotHTML], { type: "text/html;charset=utf-8" }),
  );
  const printWindow = window.open(url, "_blank", "width=1200,height=900");
  if (!printWindow) return;
  await waitForPrintWindow(printWindow);
  printWindow.focus();
  printWindow.print();
  window.setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 60 * 1000);
}

function downloadBlob(blob, filename) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

function buildFilename() {
  const title = selectedReport.value?.title || "研究报告";
  const safeTitle = title.replace(/[\\/:*?"<>|]/g, "-").trim() || "研究报告";
  const date = new Date().toISOString().slice(0, 10);
  return `${safeTitle}_${date}`;
}

function waitForPrintWindow(targetWindow) {
  return new Promise((resolve) => {
    const checkReady = () => {
      try {
        if (targetWindow.document.readyState === "complete") {
          resolve();
          return;
        }
      } catch (_) {
        resolve();
        return;
      }
      scheduleTimer(checkReady, 60);
    };
    checkReady();
  });
}

async function buildRenderedSnapshotHTML() {
  const frameWindow = reportFrame.value?.contentWindow;
  const frameDocument = frameWindow?.document;
  if (!frameDocument?.documentElement) {
    return sanitizedReportHTML.value;
  }

  await waitForFrameReady(frameDocument);
  const clonedDocument = frameDocument.documentElement.cloneNode(true);
  const sourceCanvases = Array.from(frameDocument.querySelectorAll("canvas"));
  clonedDocument.querySelectorAll("canvas").forEach((canvasNode, index) => {
    const sourceCanvas = sourceCanvases[index];
    if (!sourceCanvas?.toDataURL) return;
    const image = clonedDocument.ownerDocument.createElement("img");
    image.src = sourceCanvas.toDataURL("image/png");
    image.alt = sourceCanvas.getAttribute("aria-label") || "图表";
    const rect = sourceCanvas.getBoundingClientRect();
    const width = Math.max(
      Math.round(
        rect.width || sourceCanvas.clientWidth || sourceCanvas.width || 0,
      ),
      1,
    );
    const height = Math.max(
      Math.round(
        rect.height || sourceCanvas.clientHeight || sourceCanvas.height || 0,
      ),
      1,
    );
    image.width = width;
    image.height = height;
    image.style.display = "block";
    image.style.width = `${width}px`;
    image.style.maxWidth = "100%";
    image.style.height = `${height}px`;
    image.style.objectFit = "contain";
    image.style.margin = "0 auto";
    canvasNode.replaceWith(image);
  });
  optimizeSnapshotForPDF(clonedDocument);
  clonedDocument.querySelectorAll("script").forEach((node) => node.remove());
  return `<!DOCTYPE html>\n${clonedDocument.outerHTML}`;
}

function optimizeSnapshotForPDF(documentNode) {
  const doc = documentNode.ownerDocument;
  const head = documentNode.querySelector("head");
  const body = documentNode.querySelector("body");
  if (!head || !body) return;

  body
    .querySelectorAll(".report-titlebar, .report-toc, .section")
    .forEach((node) => {
      node.style.maxWidth = "none";
      node.style.boxShadow = "none";
      node.style.borderRadius = "0";
      node.style.margin = node.classList.contains("report-titlebar")
        ? "0 0 12pt 0"
        : "0 0 14pt 0";
      node.style.padding = node.classList.contains("section")
        ? "0"
        : "0 0 12pt 0";
      node.style.background = "#ffffff";
      node.style.border = "none";
      node.style.breakInside = "auto";
      node.style.pageBreakInside = "auto";
    });
  body.querySelectorAll(".chart-box").forEach((node) => {
    node.style.height = "auto";
    node.style.minHeight = "0";
    node.style.margin = "8pt 0 10pt 0";
    node.style.padding = "0";
    node.style.border = "none";
    node.style.boxShadow = "none";
    node.style.background = "#ffffff";
    node.style.overflow = "visible";
    node.style.breakInside = "auto";
    node.style.pageBreakInside = "auto";
  });
  body.querySelectorAll(".chart-box div").forEach((node) => {
    node.style.height = "auto";
    node.style.minHeight = "0";
    node.style.margin = "0";
    node.style.padding = "0";
    node.style.overflow = "visible";
  });
  body.querySelectorAll(".chart-box img").forEach((node) => {
    node.style.display = "block";
    node.style.width = "auto";
    node.style.maxWidth = "100%";
    node.style.height = "auto";
    node.style.maxHeight = "76mm";
    node.style.objectFit = "contain";
    node.style.margin = "0 auto";
  });

  const exportStyle = doc.createElement("style");
  exportStyle.id = "report-pdf-export-style";
  exportStyle.textContent = `
    @page { size: A4; margin: 15mm 14mm; }
    html, body {
      background: #ffffff !important;
    }
    body {
      color: #111827 !important;
      font-size: 11pt !important;
      line-height: 1.62 !important;
      margin: 0 !important;
      padding: 0 !important;
      print-color-adjust: exact;
      -webkit-print-color-adjust: exact;
    }
    * {
      animation: none !important;
      transition: none !important;
      text-shadow: none !important;
    }
    .report-titlebar h1 {
      font-size: 22pt !important;
      line-height: 1.25 !important;
      margin: 0 0 6pt 0 !important;
    }
    .report-titlebar .meta,
    .report-toc li,
    .content p,
    .content li {
      font-size: 10.5pt !important;
    }
    .report-toc {
      break-after: avoid !important;
      page-break-after: avoid !important;
    }
    .report-block-wrapper,
    .section,
    .content {
      break-inside: auto !important;
      page-break-inside: auto !important;
    }
    .section h2,
    .content h3,
    .content h4,
    .content h5 {
      break-after: avoid !important;
      page-break-after: avoid !important;
      margin-top: 0 !important;
    }
    .content p {
      text-indent: 0 !important;
      margin: 0 0 7pt 0 !important;
      orphans: 3;
      widows: 3;
    }
    .chart-box,
    .chart-box img {
      break-inside: auto !important;
      page-break-inside: auto !important;
    }
    .chart-box {
      margin: 8pt 0 10pt 0 !important;
      overflow: visible !important;
    }
    .chart-box div {
      height: auto !important;
      min-height: 0 !important;
      margin: 0 !important;
      padding: 0 !important;
      overflow: visible !important;
    }
    .chart-box img,
    .chart-box canvas {
      width: auto !important;
      max-width: 100% !important;
      height: auto !important;
      max-height: 76mm !important;
      object-fit: contain !important;
    }
    table {
      width: 100% !important;
      break-inside: auto !important;
      page-break-inside: auto !important;
    }
    tr {
      break-inside: avoid !important;
      page-break-inside: avoid !important;
    }
  `;
  head.appendChild(exportStyle);
}

function waitForFrameReady(frameDocument) {
  return new Promise((resolve) => {
    const checkReady = () => {
      if (frameDocument.readyState === "complete") {
        scheduleTimer(resolve, 120);
        return;
      }
      scheduleTimer(checkReady, 80);
    };
    checkReady();
  });
}
</script>

<style scoped>
.report-preview {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-app);
  border-left: 1px solid var(--border-subtle);
}

.panel-header {
  padding: 16px;
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--text-main);
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-shrink: 0;
  background: var(--bg-app);
}

.header-main {
  display: flex;
  align-items: center;
  gap: 10px;
}

.run-meta {
  font-size: 0.75rem;
  color: var(--text-muted);
  font-weight: 400;
}

.toolbar {
  display: flex;
  gap: 8px;
}

.export-menu {
  position: relative;
}

.toolbar-btn {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  color: var(--text-sub);
  padding: 4px 12px;
  border-radius: 6px;
  font-size: 0.75rem;
  cursor: pointer;
  transition: all var(--transition-fast);
}

.toolbar-btn:hover {
  border-color: var(--border-light);
  color: var(--text-main);
  background: var(--bg-card-hover);
}
.toolbar-btn.active {
  background: var(--text-main);
  color: var(--bg-app);
  border-color: var(--text-main);
}
.toolbar-btn.export {
  background: var(--primary-blue);
  color: white;
  border-color: var(--primary-blue);
}
.toolbar-btn.export:hover {
  opacity: 0.9;
  background: var(--primary-blue);
}

.export-dropdown {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  min-width: 140px;
  background: var(--bg-app);
  border: 1px solid var(--border-subtle);
  border-radius: 10px;
  box-shadow: 0 12px 30px rgba(15, 23, 42, 0.12);
  padding: 6px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  z-index: 10;
}

.export-option {
  border: none;
  background: transparent;
  color: var(--text-main);
  text-align: left;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 0.8rem;
}

.export-option:hover {
  background: var(--bg-card);
}

.preview-area {
  flex: 1;
  overflow: hidden;
  position: relative;
  background: var(--bg-card);
}

.edit-strip {
  display: flex;
  gap: 12px;
  align-items: center;
  padding: 12px 16px 0;
  flex-wrap: wrap;
}

.edit-meta {
  display: flex;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.edit-label {
  font-size: 0.78rem;
  color: var(--text-muted);
}

.edit-value {
  font-size: 0.84rem;
  color: var(--text-main);
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 999px;
  padding: 4px 10px;
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.selection-preview {
  flex: 1;
  min-width: 280px;
  max-width: 520px;
  max-height: 54px;
  overflow: hidden;
  margin: 0;
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  background: var(--bg-card);
  color: var(--text-main);
  padding: 10px 12px;
  font-size: 0.78rem;
  line-height: 1.45;
}

.edit-actions {
  display: flex;
  gap: 8px;
}

.toolbar-btn.primary {
  background: var(--text-main);
  color: var(--bg-app);
}

.toolbar-btn.primary:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.edit-hint {
  margin: 0;
  color: var(--text-muted);
  font-size: 0.85rem;
}

.empty-state {
  text-align: center;
  padding: 4rem 2rem;
  color: var(--text-muted);
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
}

.empty-icon {
  font-size: 3rem;
  margin-bottom: 1rem;
  opacity: 0.5;
}

.report-iframe {
  width: 100%;
  height: 100%;
  border: none;
  background: var(--bg-app);
  box-shadow: -4px 0 16px rgba(0, 0, 0, 0.02);
}

.source-code {
  font-size: 0.8rem;
  font-family: "SF Mono", "Fira Code", monospace;
  padding: 24px;
  overflow: auto;
  height: 100%;
  color: var(--text-sub);
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--bg-app);
  margin: 0;
}
</style>
