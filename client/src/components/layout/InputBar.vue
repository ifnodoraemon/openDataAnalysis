<template>
  <div class="input-bar">
    <!-- Active Data Sources Tag Bar -->
    <div class="upload-area" v-if="dataSourceStore.sessionSources.length > 0">
      <span
        v-for="src in dataSourceStore.sessionSources"
        :key="src.source_object_key || src.active_snapshot_id"
        class="source-tag"
      >
        <span class="tag-icon">🔗</span>
        <span class="tag-name">{{ src.analysis_table_name || src.source_name }}</span>
        <span class="source-meta" v-if="src.row_count">({{ src.row_count }} 行)</span>
      </span>
    </div>

    <!-- Report Quote Context Bar -->
    <div v-if="reportQuote && !isWaitingUserInput" class="quote-context">
      <div class="quote-main">
        <span class="quote-kicker">📌 引用研报段落编辑</span>
        <span class="quote-title">{{ reportQuote.blockLabel || reportQuote.blockId || "选区" }}</span>
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

    <!-- Input Box Card -->
    <div class="input-box-card" :class="{ focused: isFocused, disabled: inputDisabled }">
      <textarea
        v-model="input"
        class="input-field"
        :placeholder="inputPlaceholder"
        @keydown.enter.exact.prevent="handleSend"
        @focus="isFocused = true"
        @blur="isFocused = false"
        rows="2"
        :disabled="inputDisabled"
      ></textarea>

      <div class="input-actions-bar">
        <div class="left-tools">
          <label
            class="action-icon-btn"
            :class="{ disabled: isUploading }"
            title="上传 CSV / Excel 数据表"
          >
            <span class="btn-icon">📁</span>
            <span class="btn-label" v-if="!isUploading">上传文件</span>
            <span class="btn-label" v-else>上传中...</span>
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
            class="action-icon-btn"
            @click="showSourcesDrawer = true"
            title="关联数据源"
          >
            <span class="btn-icon">🔗</span>
            <span class="btn-label">数据源</span>
          </button>
        </div>

        <div class="right-tools">
          <button
            v-if="!inputDisabled"
            class="send-submit-btn"
            @click="handleSend"
            :disabled="!input.trim()"
          >
            <span>发送分析</span>
            <span class="shortcut-hint">⏎</span>
          </button>
          <button v-else-if="isRunning" class="stop-btn" @click="handleStop">
            ■ 中断分析
          </button>
          <button v-else class="send-submit-btn" disabled>
            等待交互确认
          </button>
        </div>
      </div>
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
const isFocused = ref(false);
const isUploading = ref(false);
const showSourcesDrawer = ref(false);

const isRunning = computed(() => store.isRunning);
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
  return "输入分析目标、业务问题或筛选条件 (按 Enter 发送)...";
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
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: transparent;
  flex-shrink: 0;
}

.upload-area {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.source-tag {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.76rem;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  padding: 4px 10px;
  border-radius: 20px;
  color: var(--text-secondary);
}

.tag-icon { font-size: 0.8rem; }
.tag-name { font-weight: 600; color: var(--text-primary); }
.source-meta { color: var(--text-muted); font-size: 0.7rem; }

.quote-context {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid rgba(59, 130, 246, 0.3);
  border-left: 4px solid var(--accent-blue);
  border-radius: 10px;
  background: rgba(59, 130, 246, 0.08);
}

.quote-main { flex: 1; min-width: 0; }
.quote-kicker { font-size: 0.72rem; color: var(--accent-blue); font-weight: 700; }
.quote-title { font-size: 0.82rem; color: var(--text-primary); font-weight: 600; }
.quote-context p { margin-top: 2px; color: var(--text-secondary); font-size: 0.78rem; }

.quote-clear {
  width: 22px;
  height: 22px;
  border: none;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.1);
  color: var(--text-secondary);
  cursor: pointer;
}

.quote-clear:hover { background: rgba(255, 255, 255, 0.2); color: white; }

.input-box-card {
  display: flex;
  flex-direction: column;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 10px 14px;
  transition: all var(--transition);
  box-shadow: var(--shadow-md);
}

.input-box-card.focused {
  border-color: var(--border-glow);
  box-shadow: var(--shadow-glow);
}

.input-box-card.disabled {
  opacity: 0.6;
}

.input-field {
  width: 100%;
  background: transparent;
  border: none;
  color: var(--text-primary);
  font-size: 0.9rem;
  font-family: inherit;
  resize: none;
  outline: none;
  line-height: 1.5;
}

.input-field::placeholder {
  color: var(--text-muted);
}

.input-actions-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid rgba(255, 255, 255, 0.05);
}

.left-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

.action-icon-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text-secondary);
  font-size: 0.78rem;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--transition);
}

.action-icon-btn:hover:not(.disabled) {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.send-submit-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 16px;
  background: linear-gradient(135deg, var(--accent-blue), #2563eb);
  border: none;
  border-radius: 8px;
  color: white;
  font-size: 0.82rem;
  font-weight: 600;
  cursor: pointer;
  transition: all var(--transition);
  box-shadow: 0 2px 8px rgba(37, 99, 235, 0.3);
}

.send-submit-btn:hover:not(:disabled) {
  transform: translateY(-1px);
  box-shadow: 0 4px 12px rgba(37, 99, 235, 0.5);
}

.send-submit-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
  transform: none;
  box-shadow: none;
}

.shortcut-hint {
  font-size: 0.72rem;
  opacity: 0.7;
}

.stop-btn {
  padding: 7px 16px;
  background: rgba(239, 68, 68, 0.15);
  border: 1px solid rgba(239, 68, 68, 0.4);
  border-radius: 8px;
  color: var(--accent-red);
  font-size: 0.82rem;
  font-weight: 600;
  cursor: pointer;
  transition: all var(--transition);
}

.stop-btn:hover {
  background: rgba(239, 68, 68, 0.25);
}
</style>
