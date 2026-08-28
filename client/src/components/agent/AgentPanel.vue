<template>
  <div class="agent-panel">
    <div v-if="activeRun || selectedRun" class="run-summary">
      <span v-if="activeRun" class="summary-pill live">
        ⚡ 正在运行:
        {{
          truncate(
            activeRun.summary || activeRun.inputMessage || activeRun.id,
            32,
          )
        }}
      </span>
      <span
        v-if="selectedRun && selectedRun.id !== activeRun?.id"
        class="summary-pill history"
      >
        🔍 查看历史运行:
        {{
          truncate(
            selectedRun.summary || selectedRun.inputMessage || selectedRun.id,
            32,
          )
        }}
      </span>
    </div>

    <!-- Agent Execution Accordions -->
    <RunTree />
    <WorkingMemoryPanel />
    <SubgoalTree />

    <!-- Chat Messages Stream -->
    <div class="messages" ref="messagesEl">
      <!-- Empty Hero Welcome Card Grid -->
      <div v-if="messages.length === 0" class="hero-welcome">
        <div class="hero-header">
          <div class="hero-icon">✨</div>
          <h2 class="hero-title">OpenDataAnalysis 数据智能助手</h2>
          <p class="hero-subtitle">
            描述目标与约束，智能体会按当前数据和可用工具自主选择分析路径
          </p>
        </div>

        <div class="quick-cards-grid">
          <div class="frame-card">
            <div class="card-icon blue">📊</div>
            <div class="card-content">
              <span class="card-label">目标</span>
              <span class="card-desc">说明想回答的问题或要交付的结果</span>
            </div>
          </div>

          <div class="frame-card">
            <div class="card-icon purple">🧠</div>
            <div class="card-content">
              <span class="card-label">上下文</span>
              <span class="card-desc">指出相关数据、业务背景和已知定义</span>
            </div>
          </div>

          <div class="frame-card">
            <div class="card-icon orange">🔍</div>
            <div class="card-content">
              <span class="card-label">约束</span>
              <span class="card-desc"
                >补充口径、范围、时间、格式或不可更改项</span
              >
            </div>
          </div>

          <div class="frame-card">
            <div class="card-icon green">📈</div>
            <div class="card-content">
              <span class="card-label">完成标准</span>
              <span class="card-desc">说明什么证据和交付物才算完成</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Messages Loop -->
      <TransitionGroup name="fade">
        <div
          v-for="msg in messages"
          :key="msg.id"
          class="message"
          :class="'msg-' + msg.type"
        >
          <!-- User Message -->
          <template v-if="msg.type === 'user'">
            <div class="msg-avatar user-avatar">👤</div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender">您的请求</span>
              </div>
              <div v-if="msg.editContext?.selectionText" class="quote-preview">
                <div class="quote-title">
                  📌 引用研报段落 ({{ editContextLabel(msg.editContext) }})
                </div>
                <p>{{ truncate(msg.editContext.selectionText, 200) }}</p>
              </div>
              <div
                class="msg-content markdown-body"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <!-- Status Message -->
          <template v-else-if="msg.type === 'assistant_status'">
            <div class="msg-avatar status-avatar">🤖</div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender">智能体思考状态</span>
              </div>
              <div
                class="msg-content markdown-body assistant-status"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <!-- Tool Call -->
          <template v-else-if="msg.type === 'tool_call'">
            <div class="msg-avatar tool-avatar">⚙️</div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender">调用执行工具</span>
                <span class="tool-name-badge">{{ msg.name }}</span>
              </div>
              <details class="tool-details-box">
                <summary class="details-summary">查看输入参数</summary>
                <pre class="code-block">{{
                  formatJSON(msg.arguments || msg.content)
                }}</pre>
              </details>
            </div>
          </template>

          <!-- Human-in-the-loop Input (with options) -->
          <template
            v-else-if="
              msg.type === 'user_request_input' && hasUserRequestOptions(msg)
            "
          >
            <UserRequestInput
              :msg="formatUserRequestMsg(msg)"
              :render-markdown="renderMarkdown"
            />
          </template>

          <!-- Human-in-the-loop Input (no options, render as normal assistant message) -->
          <template v-else-if="msg.type === 'user_request_input'">
            <div class="msg-avatar status-avatar">🤖</div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender">智能体提问</span>
              </div>
              <div
                class="msg-content markdown-body"
                v-html="renderMarkdown(getUserRequestQuestion(msg))"
              ></div>
            </div>
          </template>

          <!-- Tool Result -->
          <template v-else-if="msg.type === 'tool_result'">
            <div class="msg-avatar result-avatar">
              {{ msg.success ? "✅" : "❌" }}
            </div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender">{{ msg.name }} 执行输出</span>
                <span class="duration-badge" v-if="msg.duration"
                  >{{ msg.duration }} 毫秒</span
                >
              </div>
              <div
                v-if="toolResultSummary(msg)"
                class="msg-content tool-result-summary"
              >
                {{ toolResultSummary(msg) }}
              </div>
              <details class="tool-details-box">
                <summary class="details-summary">查看详细数据</summary>
                <pre class="code-block">{{ truncate(msg.result, 1500) }}</pre>
              </details>
            </div>
          </template>

          <!-- Completion -->
          <template v-else-if="msg.type === 'complete'">
            <div class="msg-avatar complete-avatar">🎉</div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender complete-title">运行完成</span>
              </div>
              <div
                class="msg-content markdown-body"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <!-- Error -->
          <template v-else-if="msg.type === 'error'">
            <div class="msg-avatar error-avatar">⚠️</div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender error-title">异常反馈</span>
              </div>
              <div
                class="msg-content markdown-body error-text"
                v-html="renderMarkdown(msg.content)"
              ></div>
            </div>
          </template>

          <span class="msg-timestamp">{{ msg.timestamp }}</span>
        </div>
      </TransitionGroup>

      <!-- Running Progress Indicator -->
      <div v-if="isRunning" class="running-indicator">
        <div class="dot-flashing">
          <span class="dot"></span>
          <span class="dot"></span>
          <span class="dot"></span>
        </div>
        <span class="running-text">智能体正在分析中……</span>
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

const messages = computed(() =>
  store.messages.filter((msg) => msg.type !== "child_run_tokens"),
);
const isRunning = computed(() => store.isRunning);
const selectedRunId = computed(() => store.selectedRunId);
const activeRunId = computed(() => store.activeRunId);
const selectedRun = computed(() => store.getRun(selectedRunId.value));
const activeRun = computed(() => store.getRun(activeRunId.value));
const messagesEl = ref(null);

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("go", go);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("json", json);
hljs.registerLanguage("plaintext", plaintext);
hljs.registerLanguage("python", python);
hljs.registerLanguage("sql", sql);
hljs.registerLanguage("xml", xml);

marked.setOptions({
  gfm: true,
  breaks: true,
});

const MARKDOWN_CACHE_LIMIT = 200;

const markdownCache = new Map();
function renderMarkdown(content) {
  if (markdownCache.has(content)) {
    const cached = markdownCache.get(content);
    markdownCache.delete(content);
    markdownCache.set(content, cached);
    return cached;
  }
  const result = sanitizeMarkdownHTML(marked.parse(String(content || "")));
  markdownCache.set(content, result);
  if (markdownCache.size > MARKDOWN_CACHE_LIMIT) {
    const oldestKey = markdownCache.keys().next().value;
    markdownCache.delete(oldestKey);
  }
  return result;
}

function highlightCodeBlocks() {
  if (!messagesEl.value) return;
  messagesEl.value.querySelectorAll("pre code").forEach((block) => {
    if (block.dataset.highlighted === "yes") return;
    hljs.highlightElement(block);
  });
}

watch(
  () => messages.value,
  async () => {
    await nextTick();
    highlightCodeBlocks();
    if (messagesEl.value) {
      messagesEl.value.scrollTop = messagesEl.value.scrollHeight;
    }
  },
  { immediate: true },
);

function formatJSON(obj) {
  if (typeof obj === "string") {
    try {
      obj = JSON.parse(obj);
    } catch (e) {
      return obj;
    }
  }
  return JSON.stringify(obj, null, 2);
}

function formatUserRequestMsg(msg) {
  return msg;
}

function hasUserRequestOptions(msg) {
  const formatted = formatUserRequestMsg(msg);
  const opts = formatted.options || [];
  return Array.isArray(opts) && opts.length > 0;
}

function getUserRequestQuestion(msg) {
  const formatted = formatUserRequestMsg(msg);
  return formatted.question || formatted.content || "";
}

function truncate(str, max) {
  if (!str) return "";
  return str.length > max ? str.slice(0, max) + "..." : str;
}

function toolResultSummary(msg) {
  const payload = msg?.parsedResult;
  if (!payload || typeof payload !== "object") return "";
  return payload.ui_summary || payload.message || "";
}

function editContextLabel(editContext) {
  return editContext.blockLabel || editContext.blockId || "选区";
}
</script>

<style scoped>
.agent-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-workspace);
}

.run-summary {
  display: flex;
  gap: 8px;
  padding: 10px 16px 4px;
}

.summary-pill {
  display: inline-flex;
  align-items: center;
  padding: 4px 12px;
  border-radius: 20px;
  font-size: 0.75rem;
  font-weight: 500;
}

.summary-pill.live {
  color: var(--primary-blue);
  background: rgba(37, 99, 235, 0.08);
  border: 1px solid rgba(37, 99, 235, 0.2);
}

.summary-pill.history {
  color: var(--text-sub);
  background: var(--bg-card-hover);
  border: 1px solid var(--border-subtle);
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.hero-welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 32px 16px;
  text-align: center;
  gap: 24px;
  margin: auto 0;
}

.hero-header {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.hero-icon {
  width: 48px;
  height: 48px;
  border-radius: 14px;
  background: linear-gradient(
    135deg,
    var(--primary-blue),
    var(--accent-purple)
  );
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.5rem;
  box-shadow: 0 0 24px rgba(37, 99, 235, 0.2);
}

.hero-title {
  font-size: 1.25rem;
  font-weight: 700;
  color: var(--text-main);
  letter-spacing: -0.01em;
}

.hero-subtitle {
  font-size: 0.85rem;
  color: var(--text-sub);
  max-width: 460px;
  line-height: 1.5;
}

.quick-cards-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
  width: 100%;
  max-width: 680px;
}

.frame-card {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  cursor: pointer;
  text-align: left;
  transition: all var(--transition-fast);
}

.frame-card:hover {
  background: var(--bg-card-hover);
  border-color: var(--border-accent);
  transform: translateY(-2px);
  box-shadow: var(--shadow-panel);
}

.frame-card .card-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1rem;
  flex-shrink: 0;
}

.frame-card .card-content {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.frame-card .card-label {
  font-size: 0.85rem;
  font-weight: 600;
  color: var(--text-main);
}

.frame-card .card-desc {
  font-size: 0.73rem;
  color: var(--text-muted);
  line-height: 1.4;
}

.message {
  display: flex;
  gap: 12px;
  padding: 14px 16px;
  border-radius: 12px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  position: relative;
  transition: all var(--transition-fast);
}

.msg-avatar {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.95rem;
  flex-shrink: 0;
  background: var(--bg-card-hover);
}

.msg-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.msg-header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.msg-sender {
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--text-sub);
}

.tool-name-badge {
  background: var(--primary-glow);
  color: var(--primary-blue);
  font-size: 0.7rem;
  font-weight: 600;
  padding: 1px 8px;
  border-radius: 6px;
  border: 1px solid var(--border-accent);
}

.duration-badge {
  font-size: 0.7rem;
  color: var(--text-muted);
}

.msg-content {
  font-size: 0.88rem;
  line-height: 1.6;
  color: var(--text-main);
}

.quote-preview {
  padding: 8px 12px;
  border-left: 3px solid var(--primary-blue);
  background: rgba(37, 99, 235, 0.08);
  border-radius: 6px;
}

.quote-title {
  font-size: 0.73rem;
  font-weight: 700;
  color: var(--primary-blue);
  margin-bottom: 2px;
}

.tool-details-box {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 6px 10px;
  margin-top: 4px;
}

.details-summary {
  font-size: 0.75rem;
  color: var(--text-muted);
  cursor: pointer;
  font-weight: 500;
}

.code-block {
  font-family: "SF Mono", "Fira Code", monospace;
  font-size: 0.78rem;
  color: #334155;
  padding: 8px;
  margin-top: 6px;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
}

.msg-timestamp {
  position: absolute;
  top: 14px;
  right: 16px;
  font-size: 0.68rem;
  color: var(--text-muted);
}

.msg-user {
  background: rgba(59, 130, 246, 0.05);
  border-color: rgba(59, 130, 246, 0.2);
}

.running-indicator {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  justify-content: center;
  color: var(--text-sub);
  font-size: 0.8rem;
}

.dot-flashing {
  display: flex;
  gap: 4px;
}

.dot-flashing .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--primary-blue);
  animation: pulseGlow 1.2s infinite ease-in-out;
}

.dot-flashing .dot:nth-child(2) {
  animation-delay: 0.2s;
}
.dot-flashing .dot:nth-child(3) {
  animation-delay: 0.4s;
}
</style>
