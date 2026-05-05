<template>
  <div class="input-bar">
    <div class="upload-area" v-if="dataSourceStore.sessionSources.length > 0">
      <span
        v-for="src in dataSourceStore.sessionSources"
        :key="src.source_id"
        class="source-tag"
      >
        🔗 {{ src.analysis_table_name || src.source_name }}
        <span class="source-meta" v-if="src.row_count"
          >({{ src.row_count }} rows)</span
        >
      </span>
    </div>
    <div v-if="reportQuote && !isWaitingUserInput" class="quote-context">
      <div class="quote-main">
        <span class="quote-kicker">引用报告选区</span>
        <span class="quote-title">{{
          reportQuote.blockLabel || reportQuote.blockId || "报告片段"
        }}</span>
        <p>{{ quotePreview }}</p>
      </div>
      <button
        class="quote-clear"
        type="button"
        aria-label="取消引用"
        title="取消引用"
        @click="store.clearReportQuote()"
      >
        ×
      </button>
    </div>
    <div class="input-row">
      <label
        class="upload-btn"
        :class="{ disabled: isUploading }"
        title="上传数据"
        aria-label="上传数据"
      >
        📁
        <input
          type="file"
          accept=".csv,.xlsx,.xls"
          multiple
          @change="handleFile"
          :disabled="isUploading"
          hidden
        />
      </label>
      <button
        class="sources-btn"
        @click="showSourcesDrawer = true"
        title="数据源管理"
        aria-label="数据源管理"
      >
        🔗
      </button>
      <textarea
        v-model="input"
        class="input-field"
        :placeholder="inputPlaceholder"
        aria-label="消息输入框"
        @keydown.enter.exact="handleSend"
        rows="1"
        :disabled="inputDisabled"
      ></textarea>
      <button
        v-if="!inputDisabled"
        class="send-btn"
        @click="handleSend"
        :disabled="!input.trim()"
      >
        发送 ⏎
      </button>
      <button v-else-if="isRunning" class="stop-btn" @click="handleStop">
        ■ 停止
      </button>
      <button v-else class="send-btn" disabled>等待确认</button>
    </div>
    <DataSourceDrawer
      :open="showSourcesDrawer"
      :sessionId="store.sessionId"
      :sessionSources="dataSourceStore.sessionSources"
      :workspaceDataSources="dataSourceStore.workspaceDataSources"
      :pendingProfiles="pendingProfiles"
      @close="showSourcesDrawer = false"
    />
  </div>
</template>

<script setup>
import { ref, computed } from "vue";
import { useWebSocket } from "../../composables/useWebSocket.js";
import { useAgentStore } from "../../stores/agent.js";
import { useDataSourceStore } from "../../stores/datasource.js";
import DataSourceDrawer from "../datasource/DataSourceDrawer.vue";

const { sendMessage, stop, ensureSession } = useWebSocket();
const store = useAgentStore();
const dataSourceStore = useDataSourceStore();
const input = ref("");
const isRunning = computed(() => store.isRunning);
const isUploading = ref(false);
const showSourcesDrawer = ref(false);
const reportQuote = computed(() => store.reportQuote);
const selectedRun = computed(() => store.getRun(store.selectedRunId) || null);
const activeRun = computed(() => store.getRun(store.activeRunId) || null);
const isWaitingUserInput = computed(
  () => activeRun.value?.status === "waiting_user_input",
);
const inputDisabled = computed(
  () => isRunning.value || isWaitingUserInput.value,
);
const inputPlaceholder = computed(() => {
  if (isWaitingUserInput.value) return "请在上方确认卡片中回复...";
  if (reportQuote.value) return "说明希望如何修改引用区域...";
  return "输入你的目标、问题或约束...";
});
const quotePreview = computed(() => {
  const text = reportQuote.value?.selectionText || "";
  return text.length > 120 ? `${text.slice(0, 120)}...` : text;
});

const pendingProfiles = computed(() =>
  dataSourceStore.sessionSources.filter(
    (s) => s.semantic_status === "draft" || s.semantic_status === "profiled",
  ),
);

const MAX_FILE_SIZE = 50 * 1024 * 1024;

async function handleFile(e) {
  const files = Array.from(e.target.files || []);
  if (files.length === 0) return;

  const oversized = files.filter((file) => file.size > MAX_FILE_SIZE);
  if (oversized.length > 0) {
    for (const file of oversized) {
      store.addMessage({
        type: "error",
        content: `文件过大（${file.name}，${formatSize(file.size)}），最大支持 ${formatSize(MAX_FILE_SIZE)}`,
      });
    }
  }

  const uploadableFiles = files.filter((file) => file.size <= MAX_FILE_SIZE);
  if (uploadableFiles.length === 0) {
    e.target.value = "";
    return;
  }

  try {
    isUploading.value = true;
    const sessionId = await ensureSession();

    for (const file of uploadableFiles) {
      try {
        const formData = new FormData();
        formData.append("file", file);
        const res = await fetch(
          `/api/upload?session_id=${encodeURIComponent(sessionId)}`,
          {
            method: "POST",
            headers: store.token
              ? { Authorization: `Bearer ${store.token}` }
              : {},
            body: formData,
          },
        );
        if (!res.ok) {
          throw new Error(await res.text());
        }
        await res.json();
        store.addMessage({
          type: "user",
          content: `📎 已添加数据源: ${file.name} (${formatSize(file.size)})`,
        });
      } catch (err) {
        store.addMessage({
          type: "error",
          content: `数据源添加失败（${file.name}）: ${err.message}`,
        });
      }
    }

    await dataSourceStore.fetchSessionSources(sessionId);
  } catch (err) {
    store.addMessage({
      type: "error",
      content: `数据源添加失败: ${err.message}`,
    });
  } finally {
    isUploading.value = false;
    e.target.value = "";
  }
}

async function handleSend() {
  if (!input.value.trim() || isRunning.value) return;
  const quote = reportQuote.value;
  const turnContext =
    selectedRun.value?.id && selectedRun.value?.report
      ? {
          reportTargetRunId: selectedRun.value.id,
          reportTitle: selectedRun.value.report?.title || "",
        }
      : null;
  const editContext = quote
    ? {
        mode: quote.mode || "regenerate_selection",
        targetRunId: quote.targetRunId || "",
        blockId: quote.blockId || "",
        blockLabel: quote.blockLabel || "",
        selectionText: quote.selectionText || "",
        selectionStart: Number.isInteger(quote.selectionStart)
          ? quote.selectionStart
          : undefined,
        selectionEnd: Number.isInteger(quote.selectionEnd)
          ? quote.selectionEnd
          : undefined,
        selectionRangeSet: quote.selectionRangeSet === true,
        preserveOtherBlocks: quote.preserveOtherBlocks !== false,
      }
    : null;
  const message = input.value.trim();
  input.value = "";
  if (quote) store.clearReportQuote();
  const sent = await sendMessage(message, {
    ...(editContext ? { editContext } : {}),
    ...(turnContext ? { turnContext } : {}),
  });
  if (!sent) {
    input.value = message;
    if (quote) store.setReportQuote(quote);
  }
}

function handleStop() {
  stop();
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  return (bytes / (1024 * 1024)).toFixed(1) + " MB";
}
</script>

<style scoped>
.input-bar {
  background: transparent;
  padding: 8px 16px;
  flex-shrink: 0;
}

.upload-area {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}

.quote-context {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 8px;
  padding: 10px 12px;
  border: 1px solid rgba(37, 99, 235, 0.22);
  border-left: 3px solid var(--accent-blue);
  border-radius: 12px;
  background: rgba(37, 99, 235, 0.06);
}

.quote-main {
  min-width: 0;
  flex: 1;
}

.quote-kicker {
  display: block;
  font-size: 0.72rem;
  color: var(--accent-blue);
  font-weight: 700;
  margin-bottom: 2px;
}

.quote-title {
  display: block;
  font-size: 0.82rem;
  color: var(--text-primary);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.quote-context p {
  margin: 4px 0 0;
  color: var(--text-secondary);
  font-size: 0.78rem;
  line-height: 1.5;
}

.quote-clear {
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 999px;
  background: rgba(15, 23, 42, 0.08);
  color: var(--text-secondary);
  cursor: pointer;
  flex: 0 0 auto;
}

.quote-clear:hover {
  background: rgba(15, 23, 42, 0.14);
  color: var(--text-primary);
}

.source-tag {
  font-size: 0.75rem;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  padding: 2px 8px;
  border-radius: 4px;
  color: var(--text-secondary);
}

.source-meta {
  color: var(--text-muted);
}

.input-row {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: 16px;
  padding: 8px 12px;
  box-shadow: 0 2px 6px rgba(0, 0, 0, 0.05);
}

.upload-btn {
  font-size: 1.2rem;
  cursor: pointer;
  padding: 4px;
  border-radius: 6px;
  transition: background var(--transition);
  color: var(--text-secondary);
}

.sources-btn {
  font-size: 1.2rem;
  cursor: pointer;
  padding: 4px;
  border-radius: 6px;
  transition: background var(--transition);
  color: var(--text-secondary);
  background: none;
  border: none;
}

.sources-btn:hover {
  background: var(--border-light);
}

.upload-btn:hover {
  background: var(--border-light);
}
.upload-btn.disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.input-field {
  flex: 1;
  background: transparent;
  border: none;
  padding: 4px 8px;
  color: var(--text-primary);
  font-size: 0.9rem;
  font-family: inherit;
  resize: none;
  outline: none;
}

.input-field::placeholder {
  color: var(--text-muted);
}

.send-btn,
.stop-btn {
  padding: 6px 14px;
  border-radius: 12px;
  font-size: 0.8rem;
  font-weight: 500;
  cursor: pointer;
  border: none;
  transition: all var(--transition);
}

.send-btn {
  background: var(--text-primary);
  color: var(--bg-primary);
}

.send-btn:hover:not(:disabled) {
  opacity: 0.85;
}
.send-btn:disabled {
  opacity: 0.3;
  cursor: not-allowed;
  background: var(--text-muted);
}

.stop-btn {
  background: var(--bg-primary);
  color: var(--accent-red);
  border: 1px solid var(--border);
}

.stop-btn:hover {
  background: rgba(220, 38, 38, 0.05);
}
</style>
