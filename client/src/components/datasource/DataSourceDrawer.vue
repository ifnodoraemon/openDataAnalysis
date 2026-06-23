<template>
  <div
    v-if="open"
    class="datasource-drawer-overlay"
    @click.self="$emit('close')"
  >
    <div class="datasource-drawer">
      <div class="drawer-header">
        <h3>Sources</h3>
        <button class="close-btn" @click="$emit('close')">&times;</button>
      </div>

      <div class="drawer-section">
        <h4>当前会话 Snapshots</h4>
        <div v-if="sessionSources.length === 0" class="empty-hint">
          暂无数据源
        </div>
        <div
          v-for="source in sessionSources"
          :key="source.source_object_key || source.active_snapshot_id"
          class="source-card"
        >
          <div class="source-title-row">
            <div class="source-name">{{ source.display_name }}</div>
            <button
              class="btn-xs danger"
              @click="handleRemoveSessionSource(source)"
              :disabled="removingSourceKey === source.source_object_key"
            >
              删除
            </button>
          </div>
          <div class="source-meta">
            <span class="badge" :class="source.source_type">{{
              source.source_type
            }}</span>
            <span v-if="source.analysis_table_name" class="table-name">{{
              source.analysis_table_name
            }}</span>
            <span v-if="source.upstream_object" class="table-name">
              {{ source.upstream_schema ? `${source.upstream_schema}.` : ""
              }}{{ source.upstream_object }}
            </span>
            <span v-if="source.row_count"
              >{{ source.row_count.toLocaleString() }} rows</span
            >
            <span v-if="source.data_size_tier" class="badge size-tier">{{
              source.data_size_tier
            }}</span>
            <span v-if="source.rows_skipped" class="badge warning"
              >{{ source.rows_skipped.toLocaleString() }} skipped</span
            >
            <span v-if="source.import_truncated" class="badge warning">
              capped at {{ source.import_row_limit?.toLocaleString() }} rows
            </span>
            <span v-if="source.large_dataset" class="badge large"
              >large dataset</span
            >
          </div>
          <div class="source-status">
            <span :class="['status', source.snapshot_status]">{{
              source.snapshot_status
            }}</span>
            <span v-if="source.profile_mode" class="profile-mode">{{
              source.profile_mode
            }}</span>
            <span v-if="source.error_message" class="error-msg">{{
              source.error_message
            }}</span>
          </div>
          <div
            v-if="source.snapshot_status === 'creating'"
            class="import-progress"
          >
            导入中... {{ source.rows_imported?.toLocaleString() || 0 }} rows
            <span v-if="source.import_duration_ms"
              >({{ (source.import_duration_ms / 1000).toFixed(1) }}s)</span
            >
          </div>
        </div>
      </div>

      <div class="drawer-section">
        <div class="section-header">
          <h4>工作区 SQL Sources</h4>
          <button class="btn-sm" @click="showCreateForm = !showCreateForm">
            + 新增
          </button>
        </div>
        <div v-if="showCreateForm" class="create-form">
          <input v-model="newSource.name" placeholder="名称" class="input-sm" />
          <select
            v-model="newSource.source_type"
            class="input-sm"
            @change="applySourceTypeDefaults(newSource)"
          >
            <option
              v-for="type in configurableSQLSourceTypes"
              :key="type.source_type"
              :value="type.source_type"
            >
              {{ type.label }}
            </option>
          </select>
          <input v-model="newSource.host" placeholder="Host" class="input-sm" />
          <input
            v-model.number="newSource.port"
            type="number"
            placeholder="Port"
            class="input-sm"
          />
          <input
            v-model="newSource.database_name"
            placeholder="Database"
            class="input-sm"
          />
          <input
            v-model="newSource.default_schema"
            placeholder="Schema"
            class="input-sm"
          />
          <select
            v-if="sslModeOptionsForSourceType(newSource.source_type).length"
            v-model="newSource.ssl_mode"
            class="input-sm"
          >
            <option
              v-for="mode in sslModeOptionsForSourceType(newSource.source_type)"
              :key="mode"
              :value="mode"
            >
              {{ mode }}
            </option>
          </select>
          <input
            v-model="newSource.username"
            placeholder="Username"
            class="input-sm"
          />
          <input
            v-model="newSource.password"
            type="password"
            placeholder="Password"
            class="input-sm"
          />
          <div class="allowlist-section">
            <label class="allowlist-label">Allowlist (schema.name.kind)</label>
            <div
              v-for="(entry, idx) in newSource.allowlist"
              :key="idx"
              class="allowlist-row"
            >
              <input
                v-model="entry.schema"
                placeholder="schema"
                class="input-xs"
              />
              <input v-model="entry.name" placeholder="name" class="input-xs" />
              <select v-model="entry.kind" class="input-xs">
                <option value="table">table</option>
                <option value="view">view</option>
              </select>
              <button
                class="btn-xs"
                @click="newSource.allowlist.splice(idx, 1)"
              >
                ×
              </button>
            </div>
            <button
              class="btn-xs"
              @click="
                newSource.allowlist.push({
                  schema:
                    newSource.default_schema ||
                    newSource.database_name ||
                    'public',
                  name: '',
                  kind: 'table',
                })
              "
            >
              + 添加
            </button>
          </div>
          <div class="form-actions">
            <button
              class="btn-sm primary"
              @click="handleCreateSource"
              :disabled="creating || configurableSQLSourceTypes.length === 0"
            >
              创建
            </button>
            <button class="btn-sm" @click="showCreateForm = false">取消</button>
          </div>
        </div>
        <div
          v-if="sqlWorkspaceDataSources.length === 0 && !showCreateForm"
          class="empty-hint"
        >
          暂无 SQL 数据源
        </div>
        <div v-if="sourceMessage" class="source-message">
          {{ sourceMessage }}
        </div>
        <div
          v-for="ds in sqlWorkspaceDataSources"
          :key="ds.id"
          class="source-card"
        >
          <div class="source-title-row">
            <div class="source-name">{{ ds.name }}</div>
            <button
              class="btn-xs danger"
              @click="handleDeleteWorkspaceSource(ds)"
              :disabled="deletingSourceId === ds.id"
            >
              删除
            </button>
          </div>
          <div class="source-meta">
            <span class="badge" :class="ds.source_type">{{
              labelForSourceType(ds.source_type)
            }}</span>
            <span :class="['status', ds.status]">{{ ds.status }}</span>
            <span v-if="ds.config?.host" class="table-name"
              >{{ ds.config.host }}:{{ ds.config.port }}/{{
                ds.config.database_name
              }}</span
            >
            <span
              v-if="ds.config?.last_test_status"
              :class="[
                'status',
                ds.config.last_test_status === 'success' ? 'active' : 'invalid',
              ]"
            >
              test: {{ ds.config.last_test_status }}
            </span>
            <button
              class="btn-xs"
              @click="handleTestSource(ds)"
              :disabled="testingSourceId === ds.id"
            >
              测试
            </button>
            <button class="btn-xs" @click="startEditSource(ds)">编辑</button>
            <button class="btn-xs" @click="openImportFor(ds)">导入</button>
          </div>
          <div
            v-if="testResults[ds.id]"
            :class="[
              'source-status',
              testResults[ds.id].success ? 'status active' : 'status invalid',
            ]"
          >
            {{
              testResults[ds.id].message ||
              (testResults[ds.id].success ? "连接成功" : "连接失败")
            }}
          </div>
          <div v-if="editingSourceId === ds.id" class="create-form edit-form">
            <input
              v-model="editSource.name"
              placeholder="名称"
              class="input-sm"
            />
            <select v-model="editSource.source_type" class="input-sm" disabled>
              <option
                v-for="type in configurableSQLSourceTypes"
                :key="type.source_type"
                :value="type.source_type"
              >
                {{ type.label }}
              </option>
            </select>
            <input
              v-model="editSource.host"
              placeholder="Host"
              class="input-sm"
            />
            <input
              v-model.number="editSource.port"
              type="number"
              placeholder="Port"
              class="input-sm"
            />
            <input
              v-model="editSource.database_name"
              placeholder="Database"
              class="input-sm"
            />
            <input
              v-model="editSource.default_schema"
              placeholder="Schema"
              class="input-sm"
            />
            <select
              v-if="sslModeOptionsForSourceType(editSource.source_type).length"
              v-model="editSource.ssl_mode"
              class="input-sm"
            >
              <option
                v-for="mode in sslModeOptionsForSourceType(
                  editSource.source_type,
                )"
                :key="mode"
                :value="mode"
              >
                {{ mode }}
              </option>
            </select>
            <input
              v-model="editSource.username"
              placeholder="Username"
              class="input-sm"
            />
            <input
              v-model="editSource.password"
              type="password"
              placeholder="Password (留空保持不变)"
              class="input-sm"
            />
            <div class="allowlist-section">
              <label class="allowlist-label"
                >Allowlist (schema.name.kind)</label
              >
              <div
                v-for="(entry, idx) in editSource.allowlist"
                :key="idx"
                class="allowlist-row"
              >
                <input
                  v-model="entry.schema"
                  placeholder="schema"
                  class="input-xs"
                />
                <input
                  v-model="entry.name"
                  placeholder="name"
                  class="input-xs"
                />
                <select v-model="entry.kind" class="input-xs">
                  <option value="table">table</option>
                  <option value="view">view</option>
                </select>
                <button
                  class="btn-xs"
                  @click="editSource.allowlist.splice(idx, 1)"
                >
                  ×
                </button>
              </div>
              <button
                class="btn-xs"
                @click="
                  editSource.allowlist.push({
                    schema:
                      editSource.default_schema ||
                      editSource.database_name ||
                      'public',
                    name: '',
                    kind: 'table',
                  })
                "
              >
                + 添加
              </button>
            </div>
            <div class="form-actions">
              <button
                class="btn-sm primary"
                @click="handleUpdateSource(ds)"
                :disabled="savingSource"
              >
                保存
              </button>
              <button class="btn-sm" @click="cancelEditSource">取消</button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="importingSource" class="drawer-section">
        <h4>导入 {{ importingSource.name }}</h4>
        <div v-if="importError" class="error-msg">{{ importError }}</div>
        <div v-if="importCatalog.length > 0">
          <div
            v-for="obj in importCatalog"
            :key="obj.schema + '.' + obj.name"
            class="source-card"
          >
            <span class="table-name">{{ obj.schema }}.{{ obj.name }}</span>
            <span class="badge">{{ obj.kind }}</span>
            <button
              class="btn-xs"
              @click="handleImport(importingSource.id, obj.schema, obj.name)"
              :disabled="isImporting"
            >
              导入
            </button>
          </div>
        </div>
        <div v-else class="empty-hint">此数据源无可导入对象</div>
        <button
          class="btn-sm"
          @click="
            importingSource = null;
            importCatalog = [];
          "
        >
          关闭
        </button>
      </div>

      <div class="drawer-section">
        <h4>待确认语义项</h4>
        <div v-if="pendingProfiles.length === 0" class="empty-hint">
          无待确认项
        </div>
        <div
          v-for="p in pendingProfiles"
          :key="p.profile_id || p.active_snapshot_id"
          class="source-card"
        >
          <div class="source-name">
            {{ p.analysis_table_name || p.display_name }}
          </div>
          <span :class="['status', p.semantic_status || 'profiled']">{{
            p.semantic_status || "profiled"
          }}</span>
          <div v-if="selectedProfileId === p.profile_id" class="profile-detail">
            <div v-if="profileDetail">
              <div
                v-if="profileDetail.profile_json?.time_candidates?.length"
                class="candidate-section"
              >
                <strong>时间列候选</strong>
                <div
                  v-for="tc in profileDetail.profile_json.time_candidates"
                  :key="tc.column_name"
                  class="candidate-item"
                >
                  {{ tc.column_name }}
                  <span class="grain" v-if="tc.grain">({{ tc.grain }})</span>
                  <span v-if="tc.estimated" class="badge estimated"
                    >estimated</span
                  >
                </div>
              </div>
              <div
                v-if="profileDetail.profile_json?.metric_candidates?.length"
                class="candidate-section"
              >
                <strong>指标候选</strong>
                <div
                  v-for="mc in profileDetail.profile_json.metric_candidates"
                  :key="mc.column_name"
                  class="candidate-item"
                >
                  {{ mc.column_name }}
                  <span class="semantic-key" v-if="mc.semantic_key"
                    >[{{ mc.semantic_key }}]</span
                  >
                  <span v-if="mc.estimated" class="badge estimated"
                    >estimated</span
                  >
                </div>
              </div>
              <div
                v-if="profileDetail.profile_json?.join_candidates?.length"
                class="candidate-section"
              >
                <strong>Join 候选</strong>
                <div
                  v-for="jc in profileDetail.profile_json.join_candidates"
                  :key="jc.left_column + '-' + jc.right_column"
                  class="candidate-item"
                >
                  {{ jc.left_table }}.{{ jc.left_column }} ↔
                  {{ jc.right_table }}.{{ jc.right_column }}
                  <span class="reason" v-if="jc.reason">({{ jc.reason }})</span>
                </div>
              </div>
              <div
                v-if="profileDetail.profile_json?.unit_candidates?.length"
                class="candidate-section"
              >
                <strong>单位候选</strong>
                <div
                  v-for="uc in profileDetail.profile_json.unit_candidates"
                  :key="uc.column_name"
                  class="candidate-item"
                >
                  {{ uc.column_name }} → {{ uc.detected_unit }}
                </div>
              </div>
              <div
                v-if="profileDetail.profile_json?.warnings?.length"
                class="candidate-section"
              >
                <strong>Warnings</strong>
                <div
                  v-for="w in profileDetail.profile_json.warnings"
                  :key="w"
                  class="warning-item"
                >
                  {{ w }}
                </div>
              </div>
              <div
                v-if="profileDetail.profile_json?.ambiguities?.length"
                class="ambiguity-list"
              >
                <strong>歧义项</strong>
                <div
                  v-for="amb in profileDetail.profile_json.ambiguities"
                  :key="amb.kind"
                  class="ambiguity-item"
                >
                  <strong>{{ amb.kind }}</strong
                  >: {{ amb.description }}
                  <div class="candidates">
                    候选: {{ amb.candidates?.join(", ") }}
                  </div>
                </div>
              </div>
              <div
                v-if="profileDetail.profile_json?.ambiguities?.length"
                class="confirm-form"
              >
                <label
                  v-if="hasAmbiguity(profileDetail, 'multiple_time_columns')"
                  class="field-row"
                >
                  <span>主时间列</span>
                  <select
                    v-model="confirmDraft.primary_time_column"
                    class="input-sm"
                  >
                    <option value="">选择</option>
                    <option
                      v-for="tc in profileDetail.profile_json.time_candidates ||
                      []"
                      :key="tc.column_name"
                      :value="tc.column_name"
                    >
                      {{ tc.column_name }}
                    </option>
                  </select>
                </label>
                <div
                  v-if="hasAmbiguity(profileDetail, 'ambiguous_metrics')"
                  class="field-group"
                >
                  <span>指标定义</span>
                  <label
                    v-for="name in ambiguityCandidates(
                      profileDetail,
                      'ambiguous_metrics',
                    )"
                    :key="name"
                    class="field-row"
                  >
                    <span>{{ name }}</span>
                    <input
                      v-model="confirmDraft.metric_definitions[name]"
                      class="input-sm"
                      placeholder="业务口径"
                    />
                  </label>
                </div>
                <div
                  v-if="hasAmbiguity(profileDetail, 'ambiguous_units')"
                  class="field-group"
                >
                  <span>百分比列</span>
                  <label
                    v-for="name in ambiguityCandidates(
                      profileDetail,
                      'ambiguous_units',
                    )"
                    :key="name"
                    class="check-row"
                  >
                    <input
                      type="checkbox"
                      :value="normalizeUnitCandidate(name)"
                      v-model="confirmDraft.percentage_columns"
                    />
                    <span>{{ name }}</span>
                  </label>
                </div>
                <div
                  v-if="hasAmbiguity(profileDetail, 'ambiguous_join')"
                  class="field-group"
                >
                  <span>确认 Join 候选</span>
                  <label
                    v-for="name in ambiguityCandidates(
                      profileDetail,
                      'ambiguous_join',
                    )"
                    :key="name"
                    class="check-row"
                  >
                    <input
                      type="checkbox"
                      :value="name"
                      v-model="confirmDraft.confirmed_join_candidates"
                    />
                    <span>{{ name }}</span>
                  </label>
                </div>
              </div>
              <div v-if="confirmError" class="error-msg">
                {{ confirmError }}
              </div>
              <div class="confirm-actions">
                <button
                  class="btn-sm primary"
                  @click="handleConfirm(p.profile_id, 'session')"
                  :disabled="confirmingProfileId === p.profile_id"
                >
                  确认 (Session)
                </button>
                <button
                  class="btn-sm"
                  @click="handleConfirm(p.profile_id, 'workspace')"
                  :disabled="confirmingProfileId === p.profile_id"
                >
                  确认 (Workspace)
                </button>
              </div>
            </div>
            <div v-else class="empty-hint">加载中...</div>
          </div>
          <button
            v-else-if="p.profile_id"
            class="btn-xs"
            @click="loadProfileDetail(p.profile_id)"
          >
            查看详情
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch, onUnmounted } from "vue";
import { useDataSourceStore } from "../../stores/datasource";

const props = defineProps({
  open: Boolean,
  sessionSources: { type: Array, default: () => [] },
  workspaceDataSources: { type: Array, default: () => [] },
  pendingProfiles: { type: Array, default: () => [] },
  sessionId: { type: String, default: "" },
});

defineEmits(["close"]);

const store = useDataSourceStore();
const showCreateForm = ref(false);
const creating = ref(false);
const importingSource = ref(null);
const importCatalog = ref([]);
const selectedProfileId = ref(null);
const importPollingTimer = ref(null);
const isImporting = ref(false);
const importError = ref("");
const removingSourceKey = ref("");
const editingSourceId = ref("");
const savingSource = ref(false);
const deletingSourceId = ref("");
const testingSourceId = ref("");
const sourceMessage = ref("");
const testResults = ref({});
const confirmDraft = ref({
  primary_time_column: "",
  metric_definitions: {},
  percentage_columns: [],
  confirmed_join_candidates: [],
});
const confirmError = ref("");
const confirmingProfileId = ref("");
const profileDetail = computed(
  () => store.semanticProfileDetails[selectedProfileId.value],
);
const configurableSQLSourceTypes = computed(() =>
  store.sourceTypes.filter(
    (type) => type.category === "sql" && type.configurable,
  ),
);
const sourceTypeSpecs = computed(() =>
  Object.fromEntries(
    configurableSQLSourceTypes.value.map((type) => [type.source_type, type]),
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

function sourceTypeSpec(sourceType) {
  return sourceTypeSpecs.value[sourceType] || null;
}

function labelForSourceType(sourceType) {
  return sourceTypeSpec(sourceType)?.label || sourceType;
}

function sslModeOptionsForSourceType(sourceType) {
  return sourceTypeSpec(sourceType)?.ssl_mode_options || [];
}

function defaultSchemaForSourceType(sourceType, databaseName = "") {
  return sourceTypeSpec(sourceType)?.default_schema || databaseName || "";
}

const defaultSQLSourceForm = (sourceType = "") => {
  const resolvedType =
    sourceType || configurableSQLSourceTypes.value[0]?.source_type || "";
  const defaultSchema = defaultSchemaForSourceType(resolvedType);
  return {
    name: "",
    source_type: resolvedType,
    host: "",
    port: defaultPortForSourceType(resolvedType),
    database_name: "",
    default_schema: defaultSchema,
    ssl_mode: sslModeOptionsForSourceType(resolvedType)[0] || "",
    username: "",
    password: "",
    allowlist: [{ schema: defaultSchema, name: "", kind: "table" }],
  };
};

const newSource = ref(defaultSQLSourceForm());
const editSource = ref(defaultSQLSourceForm());

watch(
  () => props.open,
  async (open) => {
    if (open && store.sourceTypes.length === 0) {
      await store.fetchSourceTypes();
    }
  },
  { immediate: true },
);

watch(
  configurableSQLSourceTypes,
  (types) => {
    if (!types.length) return;
    if (!sqlSourceTypeSet.value.has(newSource.value.source_type)) {
      newSource.value = defaultSQLSourceForm(types[0].source_type);
    }
  },
  { immediate: true },
);

async function handleCreateSource() {
  creating.value = true;
  sourceMessage.value = "";
  try {
    const result = await store.createSQLSource(
      newSource.value.name,
      newSource.value.source_type,
      {
        ...newSource.value,
      },
    );
    if (result?.ok === false) {
      sourceMessage.value = result.error || "创建失败";
      return;
    }
    showCreateForm.value = false;
    newSource.value = defaultSQLSourceForm();
  } finally {
    creating.value = false;
  }
}

async function openImportFor(ds) {
  importingSource.value = ds;
  importCatalog.value = [];
  importError.value = "";
  const result = await store.fetchSourceCatalog(ds.id);
  if (result?.ok === false) {
    importError.value = result.error || "加载可导入对象失败";
    return;
  }
  importCatalog.value = result.data?.objects || [];
}

function startEditSource(ds) {
  const cfg = ds.config || {};
  const defaultSchema = defaultSchemaForSourceType(
    ds.source_type,
    cfg.database_name,
  );
  editingSourceId.value = ds.id;
  sourceMessage.value = "";
  editSource.value = {
    name: ds.name || "",
    source_type:
      ds.source_type || configurableSQLSourceTypes.value[0]?.source_type || "",
    host: cfg.host || "",
    port: cfg.port || defaultPortForSourceType(ds.source_type),
    database_name: cfg.database_name || "",
    default_schema: cfg.default_schema || defaultSchema,
    ssl_mode:
      cfg.ssl_mode || sslModeOptionsForSourceType(ds.source_type)[0] || "",
    username: cfg.username || "",
    password: "",
    allowlist:
      Array.isArray(cfg.allowlist) && cfg.allowlist.length
        ? cfg.allowlist.map((entry) => ({
            schema: entry.schema || defaultSchema,
            name: entry.name || "",
            kind: entry.kind || "table",
          }))
        : [{ schema: defaultSchema, name: "", kind: "table" }],
  };
}

function cancelEditSource() {
  editingSourceId.value = "";
  sourceMessage.value = "";
}

async function handleUpdateSource(ds) {
  if (!ds?.id || savingSource.value) return;
  savingSource.value = true;
  sourceMessage.value = "";
  try {
    const result = await store.updateSQLSource(ds.id, editSource.value.name, {
      ...editSource.value,
    });
    if (result?.ok === false) {
      sourceMessage.value = result.error || "保存失败";
      return;
    }
    editingSourceId.value = "";
  } finally {
    savingSource.value = false;
  }
}

function applySourceTypeDefaults(form) {
  if (!form) return;
  const knownDefaultPorts = configurableSQLSourceTypes.value
    .map((type) => type.default_port)
    .filter(Boolean);
  if (!form.port || knownDefaultPorts.includes(form.port)) {
    form.port = defaultPortForSourceType(form.source_type);
  }
  if (!form.default_schema || form.default_schema === "public") {
    form.default_schema = defaultSchemaForSourceType(
      form.source_type,
      form.database_name,
    );
  }
  const sslOptions = sslModeOptionsForSourceType(form.source_type);
  if (!sslOptions.includes(form.ssl_mode)) {
    form.ssl_mode = sslOptions[0] || "";
  }
}

function defaultPortForSourceType(sourceType) {
  return sourceTypeSpec(sourceType)?.default_port || null;
}

async function handleDeleteWorkspaceSource(ds) {
  if (!ds?.id || deletingSourceId.value) return;
  const ok = window.confirm(
    `删除工作区 SQL 数据源「${ds.name || ds.id}」？已导入到会话的快照和语义项也会被移除。`,
  );
  if (!ok) return;
  deletingSourceId.value = ds.id;
  sourceMessage.value = "";
  try {
    const result = await store.deleteWorkspaceSource(ds.id);
    if (result?.ok === false) {
      sourceMessage.value = result.error || "删除失败";
    }
  } finally {
    deletingSourceId.value = "";
  }
}

async function handleTestSource(ds) {
  if (!ds?.id || testingSourceId.value) return;
  testingSourceId.value = ds.id;
  sourceMessage.value = "";
  try {
    testResults.value = {
      ...testResults.value,
      [ds.id]: await store.testConnection(ds.id),
    };
    await store.fetchWorkspaceDataSources();
  } finally {
    testingSourceId.value = "";
  }
}

async function handleImport(sourceId, schema, object) {
  if (isImporting.value) return;
  isImporting.value = true;
  importError.value = "";
  try {
    const result = await store.importFromSource(
      sourceId,
      props.sessionId,
      schema,
      object,
    );
    if (result && result.ok === false) {
      importError.value = result.error || "导入失败";
      isImporting.value = false;
      return;
    }
    importingSource.value = null;
    importCatalog.value = [];
    startImportPolling();
  } catch (e) {
    importError.value = e.message || "导入异常";
  } finally {
    isImporting.value = false;
  }
}

async function handleRemoveSessionSource(source) {
  if (!props.sessionId || !source?.source_id || removingSourceKey.value) return;
  const sourceObjectKey = source.source_object_key || "";
  if (!sourceObjectKey) {
    importError.value = "缺少 source_object_key，不能删除会话数据源";
    return;
  }
  const ok = window.confirm(
    `从当前会话删除数据源「${source.display_name || source.analysis_table_name || source.source_id}」？`,
  );
  if (!ok) return;
  removingSourceKey.value = sourceObjectKey;
  try {
    const result = await store.removeSessionSource(
      props.sessionId,
      source.source_id,
      sourceObjectKey,
    );
    if (result?.ok === false) {
      importError.value = result.error || "删除失败";
    }
  } finally {
    removingSourceKey.value = "";
  }
}

function startImportPolling() {
  stopImportPolling();
  importPollingTimer.value = setInterval(async () => {
    if (!props.sessionId) return;
    await store.fetchSessionSources(props.sessionId);
    const inProgress = store.sessionSources.some(
      (s) =>
        s.snapshot_status === "creating" || s.snapshot_status === "importing",
    );
    if (!inProgress) {
      stopImportPolling();
    }
  }, 3000);
}

watch(
  () => store.sessionSources,
  (sources) => {
    if (!sources) return;
    const inProgress = sources.some(
      (s) =>
        s.snapshot_status === "creating" || s.snapshot_status === "importing",
    );
    if (inProgress && !importPollingTimer.value) {
      startImportPolling();
    } else if (!inProgress && importPollingTimer.value) {
      stopImportPolling();
    }
  },
  { deep: true, immediate: true },
);

onUnmounted(() => {
  stopImportPolling();
});

function stopImportPolling() {
  if (importPollingTimer.value) {
    clearInterval(importPollingTimer.value);
    importPollingTimer.value = null;
  }
}

async function loadProfileDetail(profileId) {
  selectedProfileId.value = profileId;
  confirmError.value = "";
  const result = await store.fetchProfileDetail(profileId);
  if (result?.ok === false) {
    confirmError.value = result.error || "加载语义详情失败";
    return;
  }
  resetConfirmDraft(store.semanticProfileDetails[profileId]);
}

async function handleConfirm(profileId, scope) {
  const detail = store.semanticProfileDetails[profileId];
  if (!detail) return;
  confirmError.value = "";
  let overrides;
  try {
    overrides = buildConfirmOverrides(detail);
  } catch (e) {
    confirmError.value = e.message || "请补全确认信息";
    return;
  }
  confirmingProfileId.value = profileId;
  try {
    const result = await store.confirmProfile(
      profileId,
      scope,
      overrides,
      scope === "session" ? props.sessionId : "",
    );
    if (result?.ok === false) {
      confirmError.value = result.error || "确认失败";
      return;
    }
    selectedProfileId.value = null;
    await store.fetchSessionSources(props.sessionId);
  } finally {
    confirmingProfileId.value = "";
  }
}

function resetConfirmDraft(detail) {
  const profile = detail?.profile_json || {};
  const metricDefinitions = {};
  for (const name of ambiguityCandidates(detail, "ambiguous_metrics")) {
    metricDefinitions[name] = "";
  }
  confirmDraft.value = {
    primary_time_column:
      hasAmbiguity(detail, "multiple_time_columns") &&
      profile.time_candidates?.length === 1
        ? profile.time_candidates[0].column_name || ""
        : "",
    metric_definitions: metricDefinitions,
    percentage_columns: [],
    confirmed_join_candidates: [],
  };
}

function hasAmbiguity(detail, kind) {
  return (detail?.profile_json?.ambiguities || []).some(
    (amb) => amb?.kind === kind,
  );
}

function ambiguityCandidates(detail, kind) {
  const ambiguities = detail?.profile_json?.ambiguities || [];
  return [
    ...new Set(
      ambiguities
        .filter((amb) => amb?.kind === kind)
        .flatMap((amb) => (Array.isArray(amb.candidates) ? amb.candidates : []))
        .map((name) => `${name || ""}`.trim())
        .filter(Boolean),
    ),
  ];
}

function normalizeUnitCandidate(name) {
  return `${name || ""}`.split("(")[0].trim();
}

function buildConfirmOverrides(detail) {
  const overrides = {};
  if (hasAmbiguity(detail, "multiple_time_columns")) {
    const value = confirmDraft.value.primary_time_column?.trim();
    if (!value) {
      throw new Error("请选择主时间列");
    }
    overrides.primary_time_column = value;
  }

  const metricCandidates = ambiguityCandidates(detail, "ambiguous_metrics");
  if (metricCandidates.length > 0) {
    const definitions = {};
    const missing = [];
    for (const name of metricCandidates) {
      const definition = confirmDraft.value.metric_definitions[name]?.trim();
      if (!definition) {
        missing.push(name);
      } else {
        definitions[name] = definition;
      }
    }
    if (missing.length > 0) {
      throw new Error(`请补全指标口径: ${missing.join(", ")}`);
    }
    overrides.metric_definitions = definitions;
  }

  if (hasAmbiguity(detail, "ambiguous_units")) {
    const percentageColumns = [
      ...new Set(
        confirmDraft.value.percentage_columns
          .map((name) => normalizeUnitCandidate(name))
          .filter(Boolean),
      ),
    ];
    if (percentageColumns.length === 0) {
      throw new Error("请选择需要按百分比解释的列");
    }
    overrides.percentage_columns = percentageColumns;
  }

  if (hasAmbiguity(detail, "ambiguous_join")) {
    const joinCandidates = [
      ...new Set(
        confirmDraft.value.confirmed_join_candidates
          .map((name) => `${name || ""}`.trim())
          .filter(Boolean),
      ),
    ];
    if (joinCandidates.length === 0) {
      throw new Error("请选择确认采用的 Join 候选");
    }
    overrides.confirmed_join_candidates = joinCandidates;
  }

  return overrides;
}
</script>

<style scoped>
.datasource-drawer-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.3);
  z-index: 1000;
  display: flex;
  justify-content: flex-end;
}
.datasource-drawer {
  width: 380px;
  height: 100vh;
  background: #fff;
  box-shadow: -2px 0 8px rgba(0, 0, 0, 0.1);
  overflow-y: auto;
  padding: 16px;
}
.drawer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}
.drawer-header h3 {
  margin: 0;
  font-size: 16px;
}
.close-btn {
  background: none;
  border: none;
  font-size: 20px;
  cursor: pointer;
}
.drawer-section {
  margin-bottom: 20px;
}
.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.section-header h4 {
  font-size: 13px;
  color: #666;
  margin-bottom: 8px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.drawer-section h4 {
  font-size: 13px;
  color: #666;
  margin-bottom: 8px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.empty-hint {
  color: #999;
  font-size: 13px;
}
.source-card {
  padding: 10px;
  border: 1px solid #eee;
  border-radius: 6px;
  margin-bottom: 8px;
}
.source-title-row {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  align-items: center;
}
.source-name {
  font-weight: 500;
  font-size: 14px;
  margin-bottom: 4px;
}
.source-meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: #666;
  flex-wrap: wrap;
  align-items: center;
}
.source-status {
  margin-top: 4px;
  font-size: 12px;
}
.source-message {
  margin: 6px 0;
  color: #c62828;
  font-size: 12px;
}
.badge {
  padding: 1px 6px;
  border-radius: 3px;
  font-size: 11px;
}
.badge.file_upload {
  background: #e3f2fd;
  color: #1565c0;
}
.badge.postgres_connection,
.badge.postgres {
  background: #f3e5f5;
  color: #7b1fa2;
}
.badge.large {
  background: #fff3e0;
  color: #e65100;
}
.badge.warning {
  background: #fff8e1;
  color: #b26a00;
}
.badge.size-tier {
  background: #e8f5e9;
  color: #2e7d32;
}
.table-name {
  font-family: monospace;
}
.status {
  font-size: 11px;
}
.status.profiled,
.status.active {
  color: #2e7d32;
}
.status.draft,
.status.needs_confirmation {
  color: #e65100;
}
.status.confirmed {
  color: #1565c0;
}
.status.failed,
.status.invalid {
  color: #c62828;
}
.profile-mode {
  color: #999;
  font-size: 11px;
  margin-left: 6px;
}
.error-msg {
  color: #c62828;
  font-size: 11px;
}
.btn-sm {
  padding: 3px 10px;
  font-size: 12px;
  border: 1px solid #ddd;
  border-radius: 4px;
  background: #fff;
  cursor: pointer;
}
.btn-sm.primary {
  background: #1976d2;
  color: #fff;
  border-color: #1976d2;
}
.btn-sm:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-xs {
  padding: 1px 6px;
  font-size: 11px;
  border: 1px solid #ddd;
  border-radius: 3px;
  background: #f5f5f5;
  cursor: pointer;
}
.btn-xs.danger {
  color: #c62828;
  border-color: #ffcdd2;
  background: #fff5f5;
}
.create-form {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 10px;
  padding: 8px;
  background: #f9f9f9;
  border-radius: 4px;
}
.edit-form {
  margin-top: 8px;
  margin-bottom: 0;
}
.input-sm {
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid #ddd;
  border-radius: 3px;
}
.form-actions {
  display: flex;
  gap: 6px;
  margin-top: 4px;
}
.profile-detail {
  margin-top: 8px;
  padding: 8px;
  background: #f5f5f5;
  border-radius: 4px;
}
.ambiguity-list {
  margin-bottom: 8px;
}
.ambiguity-item {
  font-size: 12px;
  margin-bottom: 4px;
}
.confirm-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 8px 0;
  padding: 8px;
  background: #fff;
  border: 1px solid #e0e0e0;
  border-radius: 4px;
}
.field-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field-group > span,
.field-row > span:first-child {
  color: #555;
  font-size: 11px;
}
.field-row {
  display: grid;
  grid-template-columns: minmax(80px, 1fr) minmax(130px, 1.5fr);
  gap: 6px;
  align-items: center;
}
.field-row .input-sm {
  min-width: 0;
}
.check-row {
  display: flex;
  gap: 6px;
  align-items: center;
  color: #555;
  font-size: 12px;
}
.candidates {
  color: #666;
  font-size: 11px;
}
.confirm-actions {
  display: flex;
  gap: 6px;
  margin-top: 6px;
}
.candidate-section {
  margin-bottom: 8px;
}
.candidate-section strong {
  font-size: 12px;
  color: #333;
  display: block;
  margin-bottom: 4px;
}
.candidate-item {
  font-size: 12px;
  padding: 2px 0;
  color: #555;
}
.grain,
.semantic-key,
.reason {
  color: #999;
  font-size: 11px;
  margin-left: 4px;
}
.badge.estimated {
  background: #fff3e0;
  color: #e65100;
}
.warning-item {
  font-size: 11px;
  color: #e65100;
  padding: 2px 0;
}
.allowlist-section {
  margin-top: 6px;
}
.allowlist-label {
  font-size: 11px;
  color: #666;
  display: block;
  margin-bottom: 4px;
}
.allowlist-row {
  display: flex;
  gap: 4px;
  margin-bottom: 4px;
  align-items: center;
}
.input-xs {
  padding: 2px 4px;
  font-size: 11px;
  border: 1px solid #ddd;
  border-radius: 2px;
  width: 70px;
}
.source-card .import-progress {
  font-size: 11px;
  color: #1976d2;
  margin-top: 4px;
}
</style>
