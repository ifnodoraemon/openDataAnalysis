<template>
  <div class="input-bar">
    <!-- Worksheet picker for multi-sheet Excel uploads -->
    <div v-if="pendingWorksheet" class="worksheet-bar">
      <span class="worksheet-label">
        📊 {{ pendingWorksheet.filename }} 包含
        {{ pendingWorksheet.sheets.length }} 个工作表，请选择要导入的表：
      </span>
      <div class="worksheet-options">
        <button
          v-for="sheet in pendingWorksheet.sheets"
          :key="sheet"
          type="button"
          class="worksheet-chip"
          :disabled="isImportingSheet"
          @click="pickWorksheet(sheet)"
        >
          {{ sheet }}
        </button>
        <button
          type="button"
          class="worksheet-chip"
          :disabled="isImportingSheet"
          @click="handoffToAgent(pendingWorksheet)"
        >
          🤖 交给智能体处理
        </button>
        <button
          type="button"
          class="worksheet-chip cancel"
          :disabled="isImportingSheet"
          @click="pendingWorksheet = null"
        >
          取消
        </button>
      </div>
    </div>

    <!-- Handoff bar for uploads whose structure the strict importer rejects -->
    <div v-if="pendingAgent" class="worksheet-bar">
      <span class="worksheet-label">
        📎 {{ pendingAgent.filename }} 已上传，但结构不符合直接导入要求（{{
          pendingAgent.reason
        }}）
      </span>
      <div class="worksheet-options">
        <button
          type="button"
          class="worksheet-chip"
          @click="handoffToAgent(pendingAgent)"
        >
          🤖 让智能体读懂并导入
        </button>
        <button
          type="button"
          class="worksheet-chip cancel"
          @click="pendingAgent = null"
        >
          暂不处理
        </button>
      </div>
    </div>

    <!-- Active Data Sources Tag Bar -->
    <div class="upload-area" v-if="dataSourceStore.sessionSources.length > 0">
      <span
        v-for="src in dataSourceStore.sessionSources"
        :key="src.source_object_key"
        class="source-tag"
      >
        <span class="tag-icon">🔗</span>
        <span class="tag-name">{{ src.analysis_table_name }}</span>
        <span class="source-meta" v-if="src.row_count"
          >({{ src.row_count }} 行)</span
        >
      </span>
    </div>

    <!-- Report Quote Context Bar -->
    <div v-if="reportQuote && !isWaitingUserInput" class="quote-context">
      <div class="quote-main">
        <span class="quote-kicker">📌 引用研报段落编辑</span>
        <span class="quote-title">{{
          reportQuote.blockLabel || reportQuote.blockId || "选区"
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

    <!-- Input Box Card -->
    <div
      class="input-box-card"
      :class="{ focused: isFocused, disabled: inputDisabled }"
    >
      <!-- User Request Options -->
      <div
        v-if="requestOptions.length > 0"
        class="request-options"
        :class="{ 'multi-select': isMultiSelect }"
      >
        <button
          v-for="option in requestOptions"
          :key="option.id"
          class="request-option-btn"
          :class="{ selected: selectedOptionIds.includes(option.id) }"
          @click="toggleOption(option.id)"
        >
          <span>{{ option.label }}</span>
          <small v-if="option.hint">{{ option.hint }}</small>
        </button>
      </div>

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
              accept=".csv,.xlsx"
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
            v-if="!inputDisabled || isWaitingUserInput"
            class="send-submit-btn"
            @click="handleSend"
            :disabled="!input.trim() && selectedOptionIds.length === 0"
          >
            <span>{{ isWaitingUserInput ? "提交回复" : "发送分析" }}</span>
            <span class="shortcut-hint">⏎</span>
          </button>
          <button v-else-if="isRunning" class="stop-btn" @click="handleStop">
            ■ 中断分析
          </button>
        </div>
      </div>
    </div>

    <DataSourceModal
      :open="showSourcesDrawer"
      :sessionId="store.sessionId"
      :sessionSources="dataSourceStore.sessionSources"
      :workspaceDataSources="dataSourceStore.workspaceDataSources"
      @close="showSourcesDrawer = false"
    />
  </div>
</template>

<script setup>
import { ref, computed, watch } from "vue";
import { useAgentTransport } from "../../composables/useAgentTransport.js";
import { useAgentStore } from "../../stores/agent.js";
import { useDataSourceStore } from "../../stores/datasource.js";
import DataSourceModal from "../datasource/DataSourceModal.vue";

const { sendMessage, stop, ensureSession } = useAgentTransport();
const store = useAgentStore();
const dataSourceStore = useDataSourceStore();

const input = ref("");

// 智能体接管请求（本组件或数据源弹窗发起）：预填一条引导消息，由用户确认发送
watch(
  () => store.agentHandoff,
  (req) => {
    if (!req) return;
    input.value = `请读取数据源 ${req.sourceId}（文件：${req.filename}）的原始文件，先向我说明你识别到的表结构（各工作表的表头与示例数据），我确认后你再清洗数据并导入为可查询的数据表。`;
    store.agentHandoff = null;
  },
);
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
  () => isRunning.value && !isWaitingUserInput.value,
);

const inputPlaceholder = computed(() => {
  if (isWaitingUserInput.value)
    return "请直接在此回复，或选择上方卡片中的选项...";
  if (reportQuote.value) return "说明希望如何修改引用区域...";
  return "输入目标、上下文、约束或完成标准 (按 Enter 发送)...";
});

const quotePreview = computed(() => {
  const text = reportQuote.value?.selectionText || "";
  return text.length > 120 ? `${text.slice(0, 120)}...` : text;
});

const userInputRequestMessage = computed(() => {
  if (!isWaitingUserInput.value) return null;
  const msgs = store.messages;
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m.type === "user_request_input") return m;
  }
  return null;
});

const requestOptions = computed(() => {
  const msg = userInputRequestMessage.value;
  if (!msg) return [];
  const opts = msg.options || [];
  return opts
    .map((o) => ({
      id: String(o?.id || "").trim(),
      label: String(o?.label || "").trim(),
      hint: String(o?.hint || "").trim(),
    }))
    .filter((o) => o.id && o.label);
});

const isMultiSelect = computed(() => {
  const msg = userInputRequestMessage.value;
  if (!msg) return false;
  return msg.selection_mode === "multiple";
});

const selectedOptionIds = ref([]);

function toggleOption(id) {
  if (isMultiSelect.value) {
    const idx = selectedOptionIds.value.indexOf(id);
    if (idx >= 0) selectedOptionIds.value.splice(idx, 1);
    else selectedOptionIds.value.push(id);
  } else {
    selectedOptionIds.value = [id];
  }
}

const MAX_FILE_SIZE = 50 * 1024 * 1024;

const pendingWorksheet = ref(null);
const pendingAgent = ref(null);
const isImportingSheet = ref(false);

function handoffToAgent(pending) {
  if (!pending?.sourceId) return;
  store.agentHandoff = {
    sourceId: pending.sourceId,
    filename: pending.filename,
  };
  pendingWorksheet.value = null;
  pendingAgent.value = null;
}

async function pickWorksheet(sheet) {
  const pending = pendingWorksheet.value;
  if (!pending || !sheet) return;
  isImportingSheet.value = true;
  try {
    const result = await dataSourceStore.importFromSource(
      pending.sourceId,
      pending.sessionId,
      "",
      "",
      sheet,
    );
    if (result?.ok === false) {
      throw new Error(result.error || "导入失败");
    }
    if (result?.ingest_status === "needs_agent") {
      pendingAgent.value = {
        sourceId: pending.sourceId,
        filename: pending.filename,
        reason: result.import_error || "结构较复杂",
      };
      pendingWorksheet.value = null;
      return;
    }
    store.addMessage({
      type: "user",
      content: `📎 已导入工作表「${sheet}」（${pending.filename}）`,
    });
    pendingWorksheet.value = null;
    await dataSourceStore.fetchSessionSources(pending.sessionId);
  } catch (err) {
    store.addMessage({
      type: "error",
      content: `导入工作表失败（${sheet}）: ${err.message}`,
    });
  } finally {
    isImportingSheet.value = false;
  }
}

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
        const data = await res.json();
        if (data?.ingest_status === "worksheet_selection_required") {
          pendingWorksheet.value = {
            sourceId: data.source_id,
            sessionId,
            filename: file.name,
            sheets: data.worksheets || [],
          };
          store.addMessage({
            type: "user",
            content: `📎 ${file.name} 已上传（包含多个工作表，请选择要导入的表）`,
          });
          continue;
        }
        if (data?.ingest_status === "needs_agent") {
          pendingAgent.value = {
            sourceId: data.source_id,
            filename: file.name,
            reason: data.import_error || "结构较复杂",
          };
          store.addMessage({
            type: "user",
            content: `📎 ${file.name} 已上传（结构较复杂，无法直接导入）`,
          });
          continue;
        }
        if (data?.ingest_status === "failed") {
          throw new Error(data.message || "文件已上传，但导入失败");
        }
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
  // Allow sending if there's text OR selected options
  if (!input.value.trim() && selectedOptionIds.value.length === 0) return;
  if (isRunning.value && !isWaitingUserInput.value) return;

  let payloadContent = input.value.trim();

  // When answering a user_request_input with options selected
  if (
    isWaitingUserInput.value &&
    requestOptions.value.length > 0 &&
    selectedOptionIds.value.length > 0
  ) {
    const selectedOpts = requestOptions.value.filter((o) =>
      selectedOptionIds.value.includes(o.id),
    );
    const labels = selectedOpts.map((o) => `"${o.label}"`).join("、");
    const displayText = `选择：${labels}`;
    payloadContent = payloadContent
      ? `${displayText}\n${payloadContent}`
      : displayText;
  }

  if (!payloadContent) return;

  // Build turnContext for report editing
  const turnContext =
    selectedRun.value?.id && selectedRun.value?.report
      ? {
          reportTargetRunId: selectedRun.value.id,
          reportTitle: selectedRun.value.report?.title || "",
        }
      : null;

  // Build editContext from quote
  const quote = reportQuote.value;
  const editContext = quote
    ? {
        scopeKind: quote.scopeKind || "",
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
      }
    : null;

  const request = userInputRequestMessage.value;
  let wireResponse = payloadContent;
  if (
    isWaitingUserInput.value &&
    request?.authorization &&
    selectedOptionIds.value.length === 1
  ) {
    wireResponse = JSON.stringify({
      response_type: "authorization",
      authorization_decision: selectedOptionIds.value[0],
      action: request.authorization.action,
      resource_ref: request.authorization.resource_ref,
    });
  }

  const sent = await sendMessage(payloadContent, {
    ...(wireResponse !== payloadContent
      ? { payloadContent: wireResponse }
      : {}),
    ...(editContext ? { editContext } : {}),
    ...(turnContext ? { turnContext } : {}),
  });

  if (sent) {
    input.value = "";
    selectedOptionIds.value = [];
    store.clearReportQuote();
  } else {
    // Restore input if send failed
    input.value = payloadContent;
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

.worksheet-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 10px;
  padding: 8px 12px;
  font-size: 0.8rem;
}

.worksheet-label {
  color: var(--text-main);
}

.worksheet-options {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.worksheet-chip {
  border: 1px solid var(--border-subtle);
  background: var(--bg-app);
  color: var(--text-main);
  border-radius: 20px;
  padding: 3px 12px;
  font-size: 0.78rem;
  cursor: pointer;
}

.worksheet-chip:hover:not(:disabled) {
  border-color: var(--accent);
}

.worksheet-chip:disabled {
  opacity: 0.6;
  cursor: wait;
}

.worksheet-chip.cancel {
  color: var(--text-sub);
}

.source-tag {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.76rem;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  padding: 4px 10px;
  border-radius: 20px;
  color: var(--text-sub);
}

.tag-icon {
  font-size: 0.8rem;
}
.tag-name {
  font-weight: 600;
  color: var(--text-main);
}
.source-meta {
  color: var(--text-muted);
  font-size: 0.7rem;
}

.quote-context {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid rgba(59, 130, 246, 0.3);
  border-left: 4px solid var(--primary-blue);
  border-radius: 10px;
  background: rgba(59, 130, 246, 0.08);
}

.quote-main {
  flex: 1;
  min-width: 0;
}
.quote-kicker {
  font-size: 0.72rem;
  color: var(--primary-blue);
  font-weight: 700;
}
.quote-title {
  font-size: 0.82rem;
  color: var(--text-main);
  font-weight: 600;
}
.quote-context p {
  margin-top: 2px;
  color: var(--text-sub);
  font-size: 0.78rem;
}

.quote-clear {
  width: 22px;
  height: 22px;
  border: none;
  border-radius: 50%;
  background: rgba(0, 0, 0, 0.05);
  color: var(--text-sub);
  cursor: pointer;
}

.quote-clear:hover {
  background: rgba(0, 0, 0, 0.1);
  color: var(--text-main);
}

.input-box-card {
  display: flex;
  flex-direction: column;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 14px;
  padding: 10px 14px;
  transition: all var(--transition-fast);
  box-shadow: var(--shadow-panel);
}

.input-box-card.focused {
  border-color: var(--border-accent);
  box-shadow: var(--shadow-glow);
}

.input-box-card.disabled {
  opacity: 0.6;
}

.input-field {
  width: 100%;
  background: transparent;
  border: none;
  color: var(--text-main);
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
  border-top: 1px solid rgba(0, 0, 0, 0.05);
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
  background: rgba(0, 0, 0, 0.02);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  color: var(--text-sub);
  font-size: 0.78rem;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--transition-fast);
}

.action-icon-btn:hover:not(.disabled) {
  background: var(--bg-card-hover);
  color: var(--text-main);
}

.send-submit-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 16px;
  background: linear-gradient(135deg, var(--primary-blue), #2563eb);
  border: none;
  border-radius: 8px;
  color: white;
  font-size: 0.82rem;
  font-weight: 600;
  cursor: pointer;
  transition: all var(--transition-fast);
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
  background: rgba(239, 68, 68, 0.05);
  border: 1px solid rgba(239, 68, 68, 0.2);
  border-radius: 8px;
  color: var(--accent-rose);
  font-size: 0.82rem;
  font-weight: 600;
  cursor: pointer;
  transition: all var(--transition-fast);
}

.stop-btn:hover {
  background: rgba(239, 68, 68, 0.1);
}

.request-options {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
  padding-bottom: 12px;
  border-bottom: 1px dashed var(--border-subtle);
}

.request-option-btn {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  background: rgba(0, 0, 0, 0.03);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 8px 12px;
  cursor: pointer;
  transition: all var(--transition-fast);
  text-align: left;
}

.request-option-btn:hover {
  background: var(--bg-card-hover);
  border-color: var(--primary-blue);
}

.request-option-btn.selected {
  background: rgba(59, 130, 246, 0.1);
  border-color: var(--primary-blue);
  color: var(--primary-blue);
}

.request-option-btn span {
  font-size: 0.85rem;
  font-weight: 500;
}

.request-option-btn small {
  font-size: 0.7rem;
  color: var(--text-muted);
  margin-top: 4px;
}
</style>
