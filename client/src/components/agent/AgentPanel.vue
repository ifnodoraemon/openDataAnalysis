<template>
  <div class="agent-panel">
    <div v-if="activeRun || selectedRun" class="run-summary">
      <span v-if="activeRun" class="summary-pill live">
        ⚡ 正在运行: {{ truncate(activeRun.summary || activeRun.inputMessage || activeRun.id, 32) }}
      </span>
      <span v-if="selectedRun && selectedRun.id !== activeRun?.id" class="summary-pill history">
        🔍 查看历史运行: {{ truncate(selectedRun.summary || selectedRun.inputMessage || selectedRun.id, 32) }}
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
            自主执行数据关联、SQL / Python 分析、语义建模及可视化研报生成
          </p>
        </div>

        <div class="quick-cards-grid">
          <button
            class="preset-card"
            @click="sendPresetPrompt('请对当前工作区的数据源进行整体概览分析，输出关键数据特征与分布趋势。')"
          >
            <div class="card-icon blue">📊</div>
            <div class="card-content">
              <span class="card-label">全量数据特征探索</span>
              <span class="card-desc">自动扫描数据集，概览行数、指标分布与核心特征</span>
            </div>
          </button>

          <button
            class="preset-card"
            @click="sendPresetPrompt('自动检测当前数据集的语义指标，识别数据表主键、时间维度与计算口径。')"
          >
            <div class="card-icon purple">🧠</div>
            <div class="card-content">
              <span class="card-label">语义建模与指标提炼</span>
              <span class="card-desc">构建符合企业口径的统一语义层与确认规则</span>
            </div>
          </button>

          <button
            class="preset-card"
            @click="sendPresetPrompt('分析数据中的缺失值、离群点与异常波动，并给出数据质量评估报告。')"
          >
            <div class="card-icon orange">🔍</div>
            <div class="card-content">
              <span class="card-label">数据质量与异常诊断</span>
              <span class="card-desc">深度发现数值断层、重复记录与清洗建议</span>
            </div>
          </button>

          <button
            class="preset-card"
            @click="sendPresetPrompt('综合当前数据，生成包含图表的可视化分析研报，支持导出。')"
          >
            <div class="card-icon green">📈</div>
            <div class="card-content">
              <span class="card-label">生成交互式可视化研报</span>
              <span class="card-desc">渲染 ECharts 图表，生成多维归因报告与业务策略</span>
            </div>
          </button>
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
                <span class="msg-sender">您的分析需求</span>
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
                <span class="msg-sender">Agent 思考状态</span>
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
                <pre class="code-block">{{ formatJSON(msg.arguments) }}</pre>
              </details>
            </div>
          </template>

          <!-- Human-in-the-loop Input -->
          <template v-else-if="msg.type === 'user_request_input'">
            <UserRequestInput :msg="msg" :render-markdown="renderMarkdown" />
          </template>

          <!-- Tool Result -->
          <template v-else-if="msg.type === 'tool_result'">
            <div class="msg-avatar result-avatar">
              {{ msg.success ? '✅' : '❌' }}
            </div>
            <div class="msg-body">
              <div class="msg-header">
                <span class="msg-sender">{{ msg.name }} 执行输出</span>
                <span class="duration-badge" v-if="msg.duration">{{ msg.duration }}ms</span>
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
                <span class="msg-sender complete-title">分析总结归因完成</span>
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
        <span class="running-text">Agent 正在分析中...</span>
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
import { useWebSocket } from "../../composables/useWebSocket.js";
import { useAgentStore } from "../../stores/agent.js";
import { sanitizeMarkdownHTML } from "../../utils/sanitize.js";
import RunTree from "./RunTree.vue";
import SubgoalTree from "./SubgoalTree.vue";
import UserRequestInput from "./UserRequestInput.vue";
import WorkingMemoryPanel from "./WorkingMemoryPanel.vue";

const { sendMessage } = useWebSocket();
const store = useAgentStore();

const messages = computed(() => store.messages);
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
  highlight(code, language) {
    if (language && hljs.getLanguage(language)) {
      return hljs.highlight(code, { language }).value;
    }
    return hljs.highlightAuto(code, ["python", "sql", "json", "javascript", "bash"]).value;
  },
});

const markdownCache = new Map();
function renderMarkdown(content) {
  if (markdownCache.has(content)) return markdownCache.get(content);
  const result = sanitizeMarkdownHTML(marked.parse(String(content || "")));
  markdownCache.set(content, result);
  return result;
}

watch(
  () => messages.value.length,
  async () => {
    await nextTick();
    if (messagesEl.value) {
      messagesEl.value.scrollTop = messagesEl.value.scrollHeight;
    }
  },
);

function sendPresetPrompt(promptText) {
  void sendMessage(promptText);
}

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
  background: var(--bg-primary);
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
  color: #60a5fa;
  background: rgba(59, 130, 246, 0.12);
  border: 1px solid rgba(59, 130, 246, 0.3);
}

.summary-pill.history {
  color: var(--text-secondary);
  background: var(--bg-hover);
  border: 1px solid var(--border);
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
  background: linear-gradient(135deg, var(--accent-blue), var(--accent-purple));
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.5rem;
  box-shadow: 0 0 24px rgba(59, 130, 246, 0.35);
}

.hero-title {
  font-size: 1.25rem;
  font-weight: 700;
  color: var(--text-primary);
  letter-spacing: -0.01em;
}

.hero-subtitle {
  font-size: 0.85rem;
  color: var(--text-secondary);
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

.preset-card {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px;
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid var(--border);
  border-radius: 12px;
  cursor: pointer;
  text-align: left;
  transition: all var(--transition);
}

.preset-card:hover {
  background: var(--bg-hover);
  border-color: var(--border-glow);
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}

.preset-card .card-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1rem;
  flex-shrink: 0;
}

.preset-card .card-content {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.preset-card .card-label {
  font-size: 0.85rem;
  font-weight: 600;
  color: var(--text-primary);
}

.preset-card .card-desc {
  font-size: 0.73rem;
  color: var(--text-muted);
  line-height: 1.4;
}

.message {
  display: flex;
  gap: 12px;
  padding: 14px 16px;
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid var(--border);
  position: relative;
  transition: all var(--transition);
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
  background: var(--bg-tertiary);
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
  color: var(--text-secondary);
}

.tool-name-badge {
  background: rgba(59, 130, 246, 0.15);
  color: #60a5fa;
  font-size: 0.7rem;
  font-weight: 600;
  padding: 1px 8px;
  border-radius: 6px;
  border: 1px solid rgba(59, 130, 246, 0.3);
}

.duration-badge {
  font-size: 0.7rem;
  color: var(--text-muted);
}

.msg-content {
  font-size: 0.88rem;
  line-height: 1.6;
  color: var(--text-primary);
}

.quote-preview {
  padding: 8px 12px;
  border-left: 3px solid var(--accent-blue);
  background: rgba(59, 130, 246, 0.08);
  border-radius: 6px;
}

.quote-title {
  font-size: 0.73rem;
  font-weight: 700;
  color: var(--accent-blue);
  margin-bottom: 2px;
}

.tool-details-box {
  background: var(--bg-secondary);
  border: 1px solid var(--border);
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
  color: #e2e8f0;
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
  color: var(--text-secondary);
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
  background: var(--accent-blue);
  animation: pulseGlow 1.2s infinite ease-in-out;
}

.dot-flashing .dot:nth-child(2) { animation-delay: 0.2s; }
.dot-flashing .dot:nth-child(3) { animation-delay: 0.4s; }
</style>
