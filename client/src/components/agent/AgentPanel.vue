<template>
  <div class="agent-panel">
    <div v-if="activeRun || selectedRun" class="run-summary">
      <span v-if="activeRun" class="summary-pill live">
        正在执行
        {{
          truncate(
            activeRun.summary || activeRun.inputMessage || activeRun.id,
            36,
          )
        }}
      </span>
      <span
        v-if="selectedRun && selectedRun.id !== activeRun?.id"
        class="summary-pill history"
      >
        当前查看历史任务
        {{
          truncate(
            selectedRun.summary || selectedRun.inputMessage || selectedRun.id,
            36,
          )
        }}
      </span>
    </div>
    <RunTree />
    <WorkingMemoryPanel />
    <SubgoalTree />
    <div class="messages" ref="messagesEl">
      <div v-if="messages.length === 0" class="empty-state">
        <div class="empty-icon">🔍</div>
        <p>添加数据源，输入分析需求</p>
        <p class="empty-hint">Agent 会基于目标与状态自主分析并组织输出</p>
      </div>
      <TransitionGroup name="fade">
        <div
          v-for="msg in messages"
          :key="msg.id"
          class="message"
          :class="'msg-' + msg.type"
        >
          <!-- 用户消息 -->
          <template v-if="msg.type === 'user'">
            <div class="msg-icon">👤</div>
            <div class="msg-body">
              <div class="msg-label">用户指令</div>
              <div v-if="msg.editContext?.selectionText" class="quote-preview">
                <div class="quote-preview-title">
                  引用报告：{{ editContextLabel(msg.editContext) }}
                </div>
                <p>{{ truncate(msg.editContext.selectionText, 220) }}</p>
              </div>
              <div
                class="msg-content markdown-body"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <!-- 状态说明 -->
          <template v-else-if="msg.type === 'assistant_status'">
            <div class="msg-icon">●</div>
            <div class="msg-body">
              <div class="msg-label">状态</div>
              <div
                class="msg-content markdown-body assistant-status"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <!-- 工具调用 -->
          <template v-else-if="msg.type === 'tool_call'">
            <div class="msg-icon">🔧</div>
            <div class="msg-body">
              <div class="msg-label">
                工具调用
                <span class="tool-name">{{ msg.name }}</span>
              </div>
              <details class="tool-details">
                <summary>查看参数</summary>
                <pre class="tool-args">{{ formatJSON(msg.arguments) }}</pre>
              </details>
            </div>
          </template>

          <template v-else-if="msg.type === 'user_request_input'">
            <UserRequestInput :msg="msg" :render-markdown="renderMarkdown" />
          </template>

          <!-- 工具结果 -->
          <template v-else-if="msg.type === 'tool_result'">
            <div class="msg-icon">{{ msg.success ? "✅" : "❌" }}</div>
            <div class="msg-body">
              <div class="msg-label">
                {{ msg.name }} 结果
                <span class="duration">{{ msg.duration }}ms</span>
              </div>
              <div
                v-if="toolResultSummary(msg)"
                class="msg-content tool-result-summary"
              >
                {{ toolResultSummary(msg) }}
              </div>
              <details class="tool-details">
                <summary>查看结果</summary>
                <pre class="tool-result">{{ truncate(msg.result, 2000) }}</pre>
              </details>
            </div>
          </template>

          <!-- 完成 -->
          <template v-else-if="msg.type === 'complete'">
            <div class="msg-icon">🎉</div>
            <div class="msg-body">
              <div class="msg-label complete-label">分析完成</div>
              <div
                class="msg-content markdown-body"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <template v-else-if="msg.type === 'cancelled'">
            <div class="msg-icon">⏹</div>
            <div class="msg-body">
              <div class="msg-label">任务已停止</div>
              <div
                class="msg-content markdown-body"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <!-- 错误 -->
          <template v-else-if="msg.type === 'error'">
            <div class="msg-icon">❌</div>
            <div class="msg-body">
              <div class="msg-label error-label">错误</div>
              <div
                class="msg-content markdown-body error-content"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <div class="msg-time">{{ msg.timestamp }}</div>
        </div>
      </TransitionGroup>

      <div v-if="isRunning" class="running-indicator">
        <span class="dot"></span>
        <span class="dot"></span>
        <span class="dot"></span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch, nextTick } from "vue";
import { marked } from "marked";
import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import go from "highlight.js/lib/languages/go";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import plaintext from "highlight.js/lib/languages/plaintext";
import python from "highlight.js/lib/languages/python";
import sql from "highlight.js/lib/languages/sql";
import xml from "highlight.js/lib/languages/xml";
import { useAgentStore } from "../../stores/agent.js";
import { sanitizeMarkdownHTML } from "../../utils/sanitize.js";
import RunTree from "./RunTree.vue";
import SubgoalTree from "./SubgoalTree.vue";
import UserRequestInput from "./UserRequestInput.vue";
import WorkingMemoryPanel from "./WorkingMemoryPanel.vue";

const store = useAgentStore();
const messages = computed(() => store.messages);
const isRunning = computed(() => store.isRunning);
const selectedRunId = computed(() => store.selectedRunId);
const activeRunId = computed(() => store.activeRunId);
const selectedRun = computed(() => store.getRun(selectedRunId.value));
const activeRun = computed(() => store.getRun(activeRunId.value));
const messagesEl = ref(null);

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("sh", bash);
hljs.registerLanguage("go", go);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("js", javascript);
hljs.registerLanguage("json", json);
hljs.registerLanguage("plaintext", plaintext);
hljs.registerLanguage("text", plaintext);
hljs.registerLanguage("python", python);
hljs.registerLanguage("py", python);
hljs.registerLanguage("sql", sql);
hljs.registerLanguage("xml", xml);
hljs.registerLanguage("html", xml);

marked.setOptions({
  gfm: true,
  breaks: true,
  highlight(code, language) {
    if (language && hljs.getLanguage(language)) {
      return hljs.highlight(code, { language }).value;
    }
    return hljs.highlightAuto(code, [
      "python",
      "sql",
      "json",
      "javascript",
      "bash",
    ]).value;
  },
});

const markdownCache = new Map();
const MARKDOWN_CACHE_MAX = 200;

function renderMarkdown(content) {
  const key = content;
  if (markdownCache.has(key)) return markdownCache.get(key);
  const result = sanitizeMarkdownHTML(marked.parse(String(content || "")));
  if (markdownCache.size >= MARKDOWN_CACHE_MAX) {
    const firstKey = markdownCache.keys().next().value;
    markdownCache.delete(firstKey);
  }
  markdownCache.set(key, result);
  return result;
}

watch(
  () => messages.value.length,
  async () => {
    await nextTick();
    if (messagesEl.value) {
      const el = messagesEl.value;
      const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 150;
      if (nearBottom) el.scrollTop = el.scrollHeight;
    }
  },
);

function formatJSON(obj) {
  try {
    return typeof obj === "string"
      ? JSON.stringify(JSON.parse(obj), null, 2)
      : JSON.stringify(obj, null, 2);
  } catch {
    return String(obj);
  }
}

function truncate(str, max) {
  if (!str) return "";
  return str.length > max ? str.slice(0, max) + "\n... (已截断)" : str;
}

function toolResultSummary(msg) {
  const payload = msg?.parsedResult;
  if (!payload || typeof payload !== "object") return "";
  if (typeof payload.ui_summary === "string" && payload.ui_summary.trim())
    return payload.ui_summary;
  if (typeof payload.message === "string" && payload.message.trim())
    return payload.message;
  return "";
}

function editContextLabel(editContext) {
  return (
    editContext.blockLabel ||
    editContext.blockId ||
    editContext.targetBlockId ||
    "选区"
  );
}
</script>

<style scoped>
.agent-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-primary);
}

.run-summary {
  display: flex;
  gap: 8px;
  padding: 10px 12px 0;
  flex-wrap: wrap;
}

.summary-pill {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 999px;
  font-size: 0.7rem;
}

.summary-pill.live {
  color: #d2e9ff;
  background: rgba(47, 129, 247, 0.18);
  border: 1px solid rgba(47, 129, 247, 0.35);
}

.summary-pill.history {
  color: var(--text-secondary);
  background: rgba(139, 148, 158, 0.12);
  border: 1px solid rgba(139, 148, 158, 0.2);
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 12px;
}

.empty-state {
  text-align: center;
  padding: 4rem 2rem;
  color: var(--text-muted);
}

.empty-icon {
  font-size: 3rem;
  margin-bottom: 1rem;
}
.empty-hint {
  font-size: 0.8rem;
  margin-top: 0.5rem;
}

.message {
  display: flex;
  gap: 10px;
  padding: 10px 12px;
  border-radius: 8px;
  margin-bottom: 6px;
  position: relative;
  animation: slideIn 0.3s ease;
}

.msg-icon {
  font-size: 1.2rem;
  flex-shrink: 0;
  margin-top: 2px;
}

.msg-body {
  flex: 1;
  min-width: 0;
}

.msg-label {
  font-size: 0.75rem;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-weight: 500;
}

.msg-content {
  font-size: 0.85rem;
  line-height: 1.5;
  color: var(--text-primary);
}

.quote-preview {
  margin: 0 0 8px;
  padding: 9px 10px;
  border-left: 3px solid var(--accent-blue);
  border-radius: 8px;
  background: rgba(37, 99, 235, 0.07);
}

.quote-preview-title {
  color: var(--accent-blue);
  font-size: 0.72rem;
  font-weight: 700;
  margin-bottom: 4px;
}

.quote-preview p {
  margin: 0;
  color: var(--text-secondary);
  font-size: 0.78rem;
  line-height: 1.45;
}

.tool-result-summary {
  margin-bottom: 8px;
}

.markdown-body :deep(p),
.markdown-body :deep(ul),
.markdown-body :deep(ol),
.markdown-body :deep(blockquote),
.markdown-body :deep(pre) {
  margin: 0 0 0.75rem;
}

.markdown-body :deep(p:last-child),
.markdown-body :deep(ul:last-child),
.markdown-body :deep(ol:last-child),
.markdown-body :deep(blockquote:last-child),
.markdown-body :deep(pre:last-child) {
  margin-bottom: 0;
}

.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  padding-left: 1.25rem;
}

.markdown-body :deep(li + li) {
  margin-top: 0.2rem;
}

.markdown-body :deep(code) {
  font-family: "SF Mono", "Fira Code", monospace;
  font-size: 0.8em;
  background: rgba(139, 148, 158, 0.14);
  padding: 0.1rem 0.35rem;
  border-radius: 4px;
}

.markdown-body :deep(pre) {
  overflow-x: auto;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 0.85rem 1rem;
}

.markdown-body :deep(pre code) {
  background: transparent;
  padding: 0;
}

.markdown-body :deep(blockquote) {
  padding-left: 0.85rem;
  border-left: 3px solid var(--border);
  color: var(--text-secondary);
}

.markdown-body :deep(a) {
  color: var(--accent-blue);
}

.markdown-body :deep(strong) {
  font-weight: 700;
}

.msg-time {
  position: absolute;
  top: 10px;
  right: 12px;
  font-size: 0.65rem;
  color: var(--text-muted);
}

.msg-user {
  background: var(--bg-secondary);
  border-radius: 12px;
  align-self: flex-end;
  max-width: 85%;
}

.msg-assistant_status,
.msg-tool_call,
.msg-tool_result,
.msg-complete,
.msg-error {
  background: transparent;
  border-left: none;
}

.assistant-status {
  color: var(--text-muted);
  font-style: italic;
}

.tool-name {
  background: var(--bg-hover);
  color: var(--text-secondary);
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 0.7rem;
  margin-left: 6px;
  border: 1px solid var(--border);
}

.running-indicator {
  display: flex;
  gap: 4px;
  padding: 12px 16px;
  justify-content: center;
}

.running-indicator .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent-blue);
  animation: pulse 1.4s infinite ease-in-out;
}

.running-indicator .dot:nth-child(2) {
  animation-delay: 0.2s;
}
.running-indicator .dot:nth-child(3) {
  animation-delay: 0.4s;
}

.msg-user_request_input {
  background: rgba(255, 152, 0, 0.08);
  border: 1px solid rgba(255, 152, 0, 0.3);
}
</style>
