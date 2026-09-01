<template>
  <div v-if="open" class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-container">
      <!-- Modal Header -->
      <div class="modal-header">
        <div class="header-title">
          <span class="icon">📁</span>
          <h2>数据源中心</h2>
        </div>
        <button class="close-btn" @click="$emit('close')">&times;</button>
      </div>

      <!-- Modal Body with Sidebar -->
      <div class="modal-body">
        <aside class="modal-sidebar">
          <button
            class="nav-item"
            :class="{ active: currentTab === 'active' }"
            @click="currentTab = 'active'"
          >
            <span class="nav-icon">🟢</span>
            当前会话数据源
          </button>
          <button
            class="nav-item"
            :class="{ active: currentTab === 'workspace' }"
            @click="currentTab = 'workspace'"
          >
            <span class="nav-icon">🏢</span>
            工作区数据库
          </button>
          <button
            class="nav-item"
            :class="{ active: currentTab === 'upload' }"
            @click="currentTab = 'upload'"
          >
            <span class="nav-icon">📤</span>
            上传 Excel / CSV
          </button>
        </aside>

        <main class="modal-content-area">
          <!-- Worksheet picker for multi-sheet Excel uploads -->
          <div
            v-if="pendingWorksheet && currentTab === 'active'"
            class="worksheet-picker"
          >
            <div class="worksheet-picker-header">
              <strong>{{ pendingWorksheet.filename }}</strong>
              <span
                >包含 {{ pendingWorksheet.sheets.length }} 个工作表，请选择要导入的一个：</span
              >
              <button class="btn-text" @click="cancelWorksheetSelection">
                取消
              </button>
            </div>
            <div class="worksheet-list">
              <button
                v-for="sheet in pendingWorksheet.sheets"
                :key="sheet"
                class="worksheet-btn"
                :disabled="isImportingSheet"
                @click="handleSelectWorksheet(sheet)"
              >
                {{ isImportingSheet ? "导入中..." : sheet }}
              </button>
            </div>
          </div>

          <!-- Tab: Active Session Sources -->
          <div v-if="currentTab === 'active'" class="tab-pane">
            <div class="section-header">
              <h3>当前会话已关联的数据</h3>
              <p class="subtitle">这些数据源已进入当前会话的可查询范围。</p>
            </div>

            <div v-if="sessionSources.length === 0" class="empty-state">
              <div class="empty-icon">📭</div>
              <p>当前会话暂无数据源，请从工作区绑定数据库或上传文件。</p>
            </div>

            <div class="grid-list">
              <div
                v-for="source in sessionSources"
                :key="source.source_object_key"
                class="source-card"
              >
                <div class="card-header">
                  <span class="card-title">{{ source.display_name }}</span>
                  <button
                    class="btn-text danger"
                    @click="handleRemoveSessionSource(source)"
                  >
                    移除
                  </button>
                </div>
                <div class="card-body">
                  <span class="type-badge">{{
                    sourceTypeLabel(source.source_type)
                  }}</span>
                  <span v-if="source.mode === 'live'" class="meta-item live-badge"
                    >实时直连</span
                  >
                  <span v-if="source.row_count" class="meta-item"
                    >{{ source.row_count.toLocaleString() }}{{ source.mode === 'live' ? ' 行（估算）' : ' 行' }}</span
                  >
                  <span :class="['status-badge', source.snapshot_status]">{{
                    snapshotStatusLabel(source.snapshot_status)
                  }}</span>
                </div>
              </div>
            </div>
          </div>

          <!-- Tab: Workspace Databases -->
          <div v-if="currentTab === 'workspace'" class="tab-pane">
            <div class="section-header">
              <h3>工作区数据库连接</h3>
              <button
                class="btn-primary"
                @click="showCreateForm = !showCreateForm"
              >
                {{ showCreateForm ? "取消新增" : "+ 新增数据库链接" }}
              </button>
            </div>

            <!-- Create DB Form -->
            <div v-if="showCreateForm" class="form-card">
              <h4>新增关系型数据库连接</h4>
              <div class="form-grid">
                <input
                  v-model="newSource.name"
                  placeholder="连接名称"
                  class="input-base"
                />
                <select
                  v-model="newSource.source_type"
                  class="input-base"
                  @change="resetSourceTypeFields"
                >
                  <option disabled value="">选择数据库类型</option>
                  <option
                    v-for="type in configurableSQLSourceTypes"
                    :key="type.source_type"
                    :value="type.source_type"
                  >
                    {{ type.label }}
                  </option>
                </select>
                <input
                  v-model="newSource.host"
                  placeholder="主机地址"
                  class="input-base"
                />
                <input
                  v-model.number="newSource.port"
                  type="number"
                  placeholder="端口"
                  class="input-base"
                />
                <input
                  v-model="newSource.database_name"
                  placeholder="数据库名"
                  class="input-base"
                />
                <input
                  v-model="newSource.username"
                  placeholder="用户名"
                  class="input-base"
                />
                <input
                  v-model="newSource.password"
                  type="password"
                  placeholder="密码"
                  class="input-base"
                />
                <select
                  v-if="selectedSourceType?.security_mode_options?.length"
                  v-model="newSource.security_mode"
                  class="input-base"
                >
                  <option disabled value="">选择传输安全模式</option>
                  <option
                    v-for="mode in selectedSourceType.security_mode_options"
                    :key="mode.value"
                    :value="mode.value"
                  >
                    {{ mode.label }}
                  </option>
                </select>
              </div>

              <div class="allowlist-box">
                <h5>白名单配置（可绑定的表）</h5>
                <div
                  v-for="(entry, idx) in newSource.allowlist"
                  :key="idx"
                  class="allowlist-row"
                >
                  <input
                    v-model="entry.schema"
                    placeholder="数据库模式"
                    class="input-base sm"
                  />
                  <input
                    v-model="entry.name"
                    placeholder="表名"
                    class="input-base sm"
                  />
                  <select v-model="entry.kind" class="input-base sm">
                    <option disabled value="">选择类型</option>
                    <option value="table">表</option>
                    <option value="view">视图</option>
                  </select>
                  <button
                    class="btn-icon danger"
                    @click="newSource.allowlist.splice(idx, 1)"
                  >
                    &times;
                  </button>
                </div>
                <button
                  class="btn-text"
                  @click="
                    newSource.allowlist.push({ schema: '', name: '', kind: '' })
                  "
                >
                  + 添加表
                </button>
              </div>

              <div v-if="createError" class="error-banner">
                {{ createError }}
              </div>

              <div class="form-actions">
                <button
                  class="btn-primary"
                  @click="handleCreateSource"
                  :disabled="creating"
                >
                  {{ creating ? "保存中..." : "保存连接" }}
                </button>
              </div>
            </div>

            <div
              v-if="sqlWorkspaceDataSources.length === 0 && !showCreateForm"
              class="empty-state"
            >
              <div class="empty-icon">🗄️</div>
              <p>暂无配置的数据库连接，点击右上角新增。</p>
            </div>

            <div class="db-list">
              <div
                v-for="ds in sqlWorkspaceDataSources"
                :key="ds.id"
                class="db-card"
              >
                <div class="db-card-header">
                  <div>
                    <h4>{{ ds.name }}</h4>
                    <span class="db-url"
                      >{{ ds.config?.host }}:{{ ds.config?.port }}/{{
                        ds.config?.database_name
                      }}</span
                    >
                  </div>
                  <div class="db-actions">
                    <button class="btn-text" @click="handleTestSource(ds)">
                      测试连接
                    </button>
                    <button class="btn-text" @click="openImportFor(ds)">
                      绑定到会话
                    </button>
                    <button
                      class="btn-text danger"
                      @click="handleDeleteWorkspaceSource(ds)"
                    >
                      删除
                    </button>
                  </div>
                </div>
              </div>
            </div>

            <!-- Import Catalog View -->
            <div v-if="importingSource" class="import-overlay">
              <div class="import-dialog">
                <h4>绑定表到当前会话（实时只读直连）- {{ importingSource.name }}</h4>
                <div v-if="importError" class="error-banner">
                  {{ importError }}
                </div>
                <div class="table-list">
                  <div
                    v-for="obj in importCatalog"
                    :key="obj.schema + '.' + obj.name"
                    class="table-item"
                  >
                    <span class="table-name"
                      >{{ obj.schema }}.{{ obj.name }}</span
                    >
                    <button
                      class="btn-primary sm"
                      @click="
                        handleImport(importingSource.id, obj.schema, obj.name)
                      "
                      :disabled="isImporting"
                    >
                      实时绑定
                    </button>
                  </div>
                </div>
                <button
                  class="btn-default mt-4"
                  @click="
                    importingSource = null;
                    importCatalog = [];
                  "
                >
                  关闭
                </button>
              </div>
            </div>
          </div>

          <!-- Tab: File Upload -->
          <div v-if="currentTab === 'upload'" class="tab-pane">
            <div class="section-header">
              <h3>本地文件上传</h3>
              <p class="subtitle">
                支持 .csv、.xlsx 格式。文件将按原始字段和值转入分析引擎。
              </p>
            </div>

            <div class="upload-dropzone">
              <div class="upload-icon">📄</div>
              <h4>点击或拖拽文件到此处上传</h4>
              <p>单文件最大支持 50MB</p>
              <label
                class="btn-primary upload-btn"
                :class="{ disabled: isUploading }"
              >
                {{ isUploading ? "上传解析中..." : "选择文件" }}
                <input
                  type="file"
                  accept=".csv,.xlsx"
                  multiple
                  @change="handleFileUpload"
                  :disabled="isUploading"
                  hidden
                />
              </label>
            </div>
          </div>
        </main>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from "vue";
import { useDataSourceStore } from "../../stores/datasource";
import { useAgentStore } from "../../stores/agent";
import { useAgentTransport } from "../../composables/useAgentTransport";

const props = defineProps({
  open: Boolean,
  sessionSources: { type: Array, default: () => [] },
  workspaceDataSources: { type: Array, default: () => [] },
  sessionId: { type: String, default: "" },
});

const emit = defineEmits(["close"]);
const store = useDataSourceStore();
const agentStore = useAgentStore();
const { ensureSession } = useAgentTransport();

const currentTab = ref("active");
const showCreateForm = ref(false);
const creating = ref(false);
const importingSource = ref(null);
const importCatalog = ref([]);
const isImporting = ref(false);
const importError = ref("");
const createError = ref("");
const pendingWorksheet = ref(null);
const isImportingSheet = ref(false);
const isUploading = ref(false);

const configurableSQLSourceTypes = computed(() =>
  store.sourceTypes.filter(
    (type) => type.category === "sql" && type.configurable,
  ),
);
const selectedSourceType = computed(() =>
  configurableSQLSourceTypes.value.find(
    (type) => type.source_type === newSource.value.source_type,
  ),
);
const sqlSourceTypeSet = computed(
  () =>
    new Set(configurableSQLSourceTypes.value.map((type) => type.source_type)),
);
const sqlWorkspaceDataSources = computed(() =>
  props.workspaceDataSources.filter((ds) =>
    sqlSourceTypeSet.value.has(ds.source_type),
  ),
);

function sourceTypeLabel(sourceType) {
  return (
    store.sourceTypes.find((item) => item.source_type === sourceType)?.label ||
    "数据源"
  );
}

function snapshotStatusLabel(status) {
  return (
    {
      creating: "创建中",
      ready: "就绪",
      failed: "失败",
    }[status] || "状态未知"
  );
}

const defaultSQLSourceForm = (sourceType = "") => {
  return {
    name: "",
    source_type: sourceType,
    host: "",
    port: 0,
    database_name: "",
    security_mode: "",
    username: "",
    password: "",
    allowlist: [{ schema: "", name: "", kind: "" }],
  };
};

const newSource = ref(defaultSQLSourceForm());

watch(
  () => props.open,
  async (open) => {
    if (open && store.sourceTypes.length === 0) {
      await store.fetchSourceTypes();
    }
  },
);

function resetSourceTypeFields() {
  newSource.value = defaultSQLSourceForm(newSource.value.source_type);
}

async function handleCreateSource() {
  creating.value = true;
  createError.value = "";
  try {
    await store.createSQLSource(
      newSource.value.name,
      newSource.value.source_type,
      {
        ...newSource.value,
        security_mode_field:
          selectedSourceType.value?.security_mode_field || "",
      },
    );
    showCreateForm.value = false;
    newSource.value = defaultSQLSourceForm();
  } catch (err) {
    // Keep the form open with the entered values so the failure is visible
    // and fixable instead of silently discarding the input.
    createError.value =
      "创建连接失败：" + (err?.message || "请检查连接配置后重试");
  } finally {
    creating.value = false;
  }
}

async function handleRemoveSessionSource(source) {
  if (confirm(`确定要移除 ${source.display_name} 吗？`)) {
    await store.removeSessionSource(
      props.sessionId,
      source.source_id,
      source.source_object_key,
    );
  }
}

async function handleDeleteWorkspaceSource(ds) {
  if (confirm(`确定要删除连接 ${ds.name} 吗？`)) {
    await store.deleteWorkspaceSource(ds.id);
  }
}

async function handleTestSource(ds) {
  const result = await store.testConnection(ds.id);
  alert(
    result.ui_summary || (result.success ? "连接测试成功" : "连接测试失败"),
  );
}

async function openImportFor(ds) {
  importingSource.value = ds;
  importCatalog.value = [];
  importError.value = "";
  const result = await store.fetchSourceCatalog(ds.id);
  if (result?.ok === false) {
    importError.value = result.error || "加载可绑定对象失败";
  } else {
    importCatalog.value = result.data || [];
  }
}

async function handleImport(sourceId, schema, name) {
  isImporting.value = true;
  try {
    const result = await store.importFromSource(
      sourceId,
      props.sessionId,
      schema,
      name,
    );
    if (result?.ok === false) {
      throw new Error(result.error || "绑定失败");
    }
    importingSource.value = null;
    emit("close");
  } catch (err) {
    importError.value = "绑定失败：" + err.message;
  } finally {
    isImporting.value = false;
  }
}

const MAX_FILE_SIZE = 50 * 1024 * 1024;

async function handleFileUpload(e) {
  const files = Array.from(e.target.files || []);
  if (files.length === 0) return;
  const uploadableFiles = files.filter((file) => file.size <= MAX_FILE_SIZE);
  const rejectedFiles = files.filter((file) => file.size > MAX_FILE_SIZE);
  if (rejectedFiles.length > 0) {
    const names = rejectedFiles.map((file) => file.name).join("、");
    alert(
      `以下文件超过 ${MAX_FILE_SIZE / 1024 / 1024} MB 上限，已被跳过：${names}`,
    );
  }
  if (uploadableFiles.length === 0) return;

  isUploading.value = true;
  try {
    const sessionId = await ensureSession();
    for (const file of uploadableFiles) {
      const formData = new FormData();
      formData.append("file", file);
      const response = await fetch(
        `/api/upload?session_id=${encodeURIComponent(sessionId)}`,
        {
          method: "POST",
          headers: agentStore.token
            ? { Authorization: `Bearer ${agentStore.token}` }
            : {},
          body: formData,
        },
      );
      if (!response.ok) {
        throw new Error((await response.text()) || `HTTP ${response.status}`);
      }
      const data = await response.json().catch(() => null);
      if (data?.ingest_status === "worksheet_selection_required") {
        pendingWorksheet.value = {
          sourceId: data.source_id,
          sessionId,
          filename: file.name,
          sheets: data.worksheets || [],
        };
        currentTab.value = "active";
        continue;
      }
      if (data?.ingest_status === "failed") {
        throw new Error(data.message || "文件已上传，但导入失败");
      }
    }
    if (!pendingWorksheet.value) {
      await store.fetchSessionSources(sessionId);
      currentTab.value = "active";
    }
  } catch (err) {
    alert("上传失败：" + err.message);
  } finally {
    isUploading.value = false;
    e.target.value = "";
  }
}

async function handleSelectWorksheet(sheet) {
  const pending = pendingWorksheet.value;
  if (!pending || !sheet) return;
  isImportingSheet.value = true;
  try {
    const result = await store.importFromSource(
      pending.sourceId,
      pending.sessionId,
      "",
      "",
      sheet,
    );
    if (result?.ok === false) {
      throw new Error(result.error || "导入失败");
    }
    pendingWorksheet.value = null;
    await store.fetchSessionSources(pending.sessionId);
  } catch (err) {
    alert("导入工作表失败：" + err.message);
  } finally {
    isImportingSheet.value = false;
  }
}

function cancelWorksheetSelection() {
  pendingWorksheet.value = null;
}
</script>

<style scoped>
.worksheet-picker {
  margin: 0 0 16px;
  padding: 16px;
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  background: var(--bg-card);
}

.worksheet-picker-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}

.worksheet-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.worksheet-btn {
  padding: 6px 14px;
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  background: var(--bg-app);
  color: var(--text-primary);
  cursor: pointer;
}

.worksheet-btn:hover:not(:disabled) {
  border-color: var(--accent);
}

.worksheet-btn:disabled {
  opacity: 0.6;
  cursor: wait;
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  width: 100vw;
  height: 100vh;
  background: rgba(17, 24, 39, 0.4);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-container {
  width: 900px;
  max-width: 95vw;
  height: 650px;
  max-height: 90vh;
  background: var(--bg-app);
  border-radius: 16px;
  box-shadow: 0 20px 40px rgba(0, 0, 0, 0.1);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--border-subtle);
}

.modal-header {
  padding: 16px 24px;
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: var(--bg-card);
}

.header-title {
  display: flex;
  align-items: center;
  gap: 10px;
}

.header-title h2 {
  font-size: 1.15rem;
  font-weight: 600;
  color: var(--text-main);
  margin: 0;
}

.close-btn {
  background: transparent;
  border: none;
  font-size: 1.5rem;
  color: var(--text-muted);
  cursor: pointer;
  transition: color var(--transition-fast);
}

.close-btn:hover {
  color: var(--text-main);
}

.modal-body {
  display: flex;
  flex: 1;
  overflow: hidden;
}

.modal-sidebar {
  width: 220px;
  background: var(--bg-rail);
  border-right: 1px solid var(--border-subtle);
  padding: 16px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border: none;
  background: transparent;
  border-radius: 8px;
  color: var(--text-sub);
  font-size: 0.9rem;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--transition-fast);
  text-align: left;
}

.nav-item:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
}

.nav-item.active {
  background: var(--primary-glow);
  color: var(--primary-blue);
  font-weight: 600;
}

.nav-icon {
  font-size: 1.1rem;
}

.modal-content-area {
  flex: 1;
  padding: 24px;
  overflow-y: auto;
  background: var(--bg-workspace);
}

.section-header {
  margin-bottom: 24px;
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
}

.section-header h3 {
  font-size: 1.25rem;
  font-weight: 700;
  color: var(--text-main);
  margin: 0 0 6px 0;
}

.subtitle {
  color: var(--text-sub);
  font-size: 0.9rem;
  margin: 0;
}

/* UI Elements */
.btn-primary {
  background: var(--primary-blue);
  color: white;
  border: none;
  padding: 8px 16px;
  border-radius: 8px;
  font-weight: 600;
  cursor: pointer;
  transition: opacity var(--transition-fast);
}

.btn-primary:hover {
  opacity: 0.9;
}

.btn-primary.sm {
  padding: 4px 10px;
  font-size: 0.8rem;
}

.btn-default {
  background: var(--bg-card-hover);
  color: var(--text-main);
  border: 1px solid var(--border-strong);
  padding: 8px 16px;
  border-radius: 8px;
  font-weight: 500;
  cursor: pointer;
}

.btn-text {
  background: transparent;
  border: none;
  color: var(--primary-blue);
  cursor: pointer;
  font-size: 0.85rem;
  font-weight: 500;
}

.btn-text.danger {
  color: var(--accent-rose);
}

.input-base {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--bg-input);
  color: var(--text-main);
  font-size: 0.9rem;
  outline: none;
}

.input-base:focus {
  border-color: var(--primary-blue);
  box-shadow: 0 0 0 2px var(--primary-glow);
}

.input-base.sm {
  padding: 6px 8px;
  font-size: 0.85rem;
}

/* Empty States */
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  text-align: center;
  color: var(--text-muted);
}

.empty-icon {
  font-size: 3rem;
  margin-bottom: 16px;
  opacity: 0.5;
}

/* Grids & Cards */
.grid-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}

.source-card,
.db-card {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 10px;
  padding: 16px;
  box-shadow: var(--shadow-panel);
}

.card-header,
.db-card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 12px;
}

.card-title {
  font-weight: 600;
  color: var(--text-main);
  font-size: 1rem;
}

.db-url {
  display: block;
  font-size: 0.8rem;
  color: var(--text-muted);
  margin-top: 4px;
}

.card-body {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.type-badge {
  background: rgba(0, 0, 0, 0.05);
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 0.75rem;
  color: var(--text-sub);
}

.live-badge {
  background: rgba(34, 197, 94, 0.12);
  color: #16a34a;
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 0.75rem;
}

.status-badge {
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 0.75rem;
  font-weight: 600;
}

.status-badge.ready {
  background: rgba(5, 150, 105, 0.1);
  color: var(--accent-emerald);
}

.db-actions {
  display: flex;
  gap: 12px;
}

/* Form Styles */
.form-card {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  padding: 20px;
  margin-bottom: 24px;
}

.form-card h4 {
  margin: 0 0 16px 0;
  color: var(--text-main);
}

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 20px;
}

.allowlist-box {
  background: var(--bg-rail);
  padding: 16px;
  border-radius: 8px;
  margin-bottom: 20px;
}

.allowlist-box h5 {
  margin: 0 0 10px 0;
  font-size: 0.85rem;
  color: var(--text-sub);
}

.allowlist-row {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}

/* Upload Dropzone */
.upload-dropzone {
  border: 2px dashed var(--border-strong);
  border-radius: 16px;
  padding: 60px 20px;
  text-align: center;
  background: var(--bg-card);
  transition: border-color var(--transition-fast);
}

.upload-dropzone:hover {
  border-color: var(--primary-blue);
  background: var(--primary-glow);
}

.upload-icon {
  font-size: 3rem;
  margin-bottom: 16px;
}

.upload-dropzone h4 {
  margin: 0 0 8px 0;
  color: var(--text-main);
}

.upload-dropzone p {
  color: var(--text-muted);
  font-size: 0.9rem;
  margin-bottom: 24px;
}

/* Import Overlay */
.import-overlay {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: rgba(255, 255, 255, 0.9);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
}

.import-dialog {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  padding: 24px;
  width: 500px;
  max-width: 90%;
  box-shadow: var(--shadow-panel);
}

.table-list {
  max-height: 300px;
  overflow-y: auto;
  margin-top: 16px;
}

.table-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px;
  border-bottom: 1px solid var(--border-subtle);
}

.mt-4 {
  margin-top: 16px;
}
</style>
