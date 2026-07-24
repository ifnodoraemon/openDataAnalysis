<template>
  <div v-if="isRestoring" class="app-loading">
    <div class="loading-spinner"></div>
    <span>正在初始化工作区...</span>
  </div>
  <div v-else-if="hasRestoreError" class="app-loading error-state">
    <div class="error-card">
      <span class="error-icon">⚠️</span>
      <p class="title">工作区加载遇到阻碍</p>
      <p class="error-message">{{ restoreError }}</p>
      <button class="retry-btn" type="button" @click="retryInit">重新连接</button>
    </div>
  </div>
  <LoginScreen v-else-if="!isAuthenticated" />

  <!-- 3-Column Studio Workspace -->
  <div v-else class="app-workspace">
    <!-- Col 1: Slim Feature Icon Rail (56px) -->
    <IconRail
      :isHistoryOpen="isHistoryOpen"
      @toggle-history="isHistoryOpen = !isHistoryOpen"
      @open-data-sources="showSourcesDrawer = true"
      @open-semantic="showSemanticModal = true"
      @open-reports="handleOpenReports"
      @open-workspace-settings="showWorkspaceModal = true"
    />

    <!-- Col 2: Collapsible History Conversations Pane (250px) -->
    <HistoryPanel v-if="isHistoryOpen" />

    <!-- Col 3: Main Dual-Pane Workspace (Chat + Interactive Studio) -->
    <div class="main-content">
      <!-- Center: AI Agent Chat & Reasoning Panel -->
      <div class="chat-area" :style="{ width: leftWidth + '%' }">
        <!-- Top Workspace Bar -->
        <header class="top-workspace-bar">
          <div class="bar-left">
            <button
              class="icon-toggle-btn"
              @click="isHistoryOpen = !isHistoryOpen"
              title="切换历史对话列"
            >
              <span>{{ isHistoryOpen ? '◀' : '▶' }}</span>
            </button>
            <div class="session-badge">
              <span class="session-dot"></span>
              <span class="session-title-text">{{ currentSessionTitle }}</span>
            </div>
          </div>

          <div class="bar-center">
            <span class="model-pill">
              <span class="sparkle">✨</span>
              <span>Gemini 3.5 Flash + Python Sandbox</span>
            </span>
          </div>

          <div class="bar-right">
            <button
              class="quick-tool-btn"
              @click="showSourcesDrawer = true"
              title="关联数据源"
            >
              <span>📁 数据源</span>
              <span class="tool-count" v-if="dataSourceStore.sessionSources?.length">
                {{ dataSourceStore.sessionSources.length }}
              </span>
            </button>

            <button
              class="quick-tool-btn"
              @click="showSemanticModal = true"
              title="语义模型"
            >
              <span>🧠 语义模型</span>
            </button>

            <button
              class="quick-tool-btn"
              @click="showWorkspaceModal = true"
              title="工作区"
            >
              <span>⚙️</span>
            </button>
          </div>
        </header>

        <!-- Center Agent Messages Panel -->
        <AgentPanel class="panel-left" />

        <!-- Input Bar Container -->
        <InputBar class="input-bar-container" />
      </div>

      <!-- Resizable Splitter -->
      <div
        class="splitter"
        role="separator"
        aria-orientation="vertical"
        :aria-valuenow="Math.round(leftWidth)"
        aria-valuemin="25"
        aria-valuemax="75"
        tabindex="0"
        @mousedown="startDrag"
        @keydown="handleSplitterKey"
        :class="{ dragging: isDragging }"
      >
        <div class="splitter-line"></div>
      </div>

      <!-- Right Panel: Data & Report Studio Canvas -->
      <ReportPreview
        class="panel-right"
        :style="{ width: 100 - leftWidth + '%' }"
      />
    </div>

    <!-- Modals & Drawers -->
    <DataSourceDrawer
      :open="showSourcesDrawer"
      :sessionId="store.sessionId"
      :sessionSources="dataSourceStore.sessionSources"
      :workspaceDataSources="dataSourceStore.workspaceDataSources"
      :pendingProfiles="[]"
      @close="showSourcesDrawer = false"
    />

    <WorkspaceSettingsModal
      :open="showWorkspaceModal"
      @close="showWorkspaceModal = false"
    />

    <SemanticProfilesModal
      :open="showSemanticModal"
      @close="showSemanticModal = false"
    />
  </div>
</template>

<script setup>
import { computed, ref, onMounted, watch } from "vue";
import { useWebSocket } from "./composables/useWebSocket.js";
import { useAgentStore } from "./stores/agent.js";
import { useDataSourceStore } from "./stores/datasource.js";
import IconRail from "./components/layout/IconRail.vue";
import HistoryPanel from "./components/layout/HistoryPanel.vue";
import AgentPanel from "./components/agent/AgentPanel.vue";
import ReportPreview from "./components/report/ReportPreview.vue";
import InputBar from "./components/layout/InputBar.vue";
import LoginScreen from "./components/auth/LoginScreen.vue";
import DataSourceDrawer from "./components/datasource/DataSourceDrawer.vue";
import WorkspaceSettingsModal from "./components/layout/WorkspaceSettingsModal.vue";
import SemanticProfilesModal from "./components/layout/SemanticProfilesModal.vue";

const { initializeApp } = useWebSocket();
const store = useAgentStore();
const dataSourceStore = useDataSourceStore();

const leftWidth = ref(48);
const isDragging = ref(false);
const isHistoryOpen = ref(true);

const showSourcesDrawer = ref(false);
const showWorkspaceModal = ref(false);
const showSemanticModal = ref(false);

const isAuthenticated = computed(() => !!store.token && !!store.user);
const isRestoring = computed(() => store.bootstrapState === "loading");
const hasRestoreError = computed(
  () => store.bootstrapState === "error" && !!store.token,
);
const restoreError = computed(() => store.bootstrapError || "请稍后重试。");

const currentSessionTitle = computed(() => {
  if (!store.sessionId) return "新数据探索会话";
  const activeSession = store.sessions?.find((s) => s.id === store.sessionId);
  return activeSession?.title || store.sessionId;
});

onMounted(() => {
  void initApp();
});

watch(
  () => store.token,
  (nextToken, prevToken) => {
    if (nextToken && nextToken !== prevToken) {
      void initApp();
    } else if (!nextToken) {
      store.setBootstrapState("idle");
    }
  },
);

function initApp() {
  return initializeApp().catch((err) => {
    console.error("bootstrap failed:", err);
  });
}

function retryInit() {
  void initApp();
}

function handleOpenReports() {
  if (leftWidth.value > 45) {
    leftWidth.value = 40;
  }
}

function startDrag(e) {
  isDragging.value = true;
  const startX = e.clientX;
  const startWidth = leftWidth.value;

  function onMove(e) {
    const dx = e.clientX - startX;
    const containerWidth = document.querySelector(".main-content").offsetWidth;
    const newWidth = startWidth + (dx / containerWidth) * 100;
    leftWidth.value = Math.max(25, Math.min(75, newWidth));
  }

  function onUp() {
    isDragging.value = false;
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
  }

  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

function handleSplitterKey(e) {
  const step = 2;
  if (e.key === "ArrowLeft") {
    leftWidth.value = Math.max(25, leftWidth.value - step);
  } else if (e.key === "ArrowRight") {
    leftWidth.value = Math.min(75, leftWidth.value + step);
  }
}
</script>

<style scoped>
.app-loading {
  height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 16px;
  background: var(--bg-app);
  color: var(--text-sub);
  font-size: 0.9rem;
}

.loading-spinner {
  width: 32px;
  height: 32px;
  border: 3px solid var(--border-strong);
  border-top-color: var(--primary-blue);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

.error-state {
  padding: 24px;
}

.error-card {
  max-width: 400px;
  padding: 24px;
  border: 1px solid var(--border-strong);
  border-radius: 14px;
  background: var(--bg-card);
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}

.error-icon {
  font-size: 2rem;
}

.error-card .title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--text-main);
}

.error-message {
  color: var(--accent-rose);
  font-size: 0.85rem;
}

.retry-btn {
  padding: 8px 20px;
  border: none;
  border-radius: 8px;
  background: var(--primary-blue);
  color: white;
  font-weight: 600;
  cursor: pointer;
}

.app-workspace {
  height: 100vh;
  width: 100vw;
  display: flex;
  background: var(--bg-app);
  overflow: hidden;
}

.main-content {
  flex: 1;
  display: flex;
  overflow: hidden;
  position: relative;
  background: var(--bg-workspace);
}

.chat-area {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 320px;
  background: var(--bg-workspace);
  border-right: 1px solid var(--border-subtle);
}

.top-workspace-bar {
  height: var(--topbar-height);
  padding: 0 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--bg-workspace);
  flex-shrink: 0;
  z-index: 5;
}

.bar-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.icon-toggle-btn {
  background: transparent;
  border: 1px solid var(--border-subtle);
  color: var(--text-sub);
  font-size: 0.75rem;
  cursor: pointer;
  padding: 3px 6px;
  border-radius: 6px;
  transition: all var(--transition-fast);
}

.icon-toggle-btn:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
}

.session-badge {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 10px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 20px;
}

.session-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--primary-blue);
  box-shadow: 0 0 6px var(--primary-blue);
}

.session-title-text {
  font-size: 0.82rem;
  font-weight: 600;
  color: var(--text-main);
  max-width: 200px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.bar-center {
  display: flex;
  align-items: center;
}

.model-pill {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.75rem;
  font-weight: 500;
  color: var(--text-sub);
  padding: 3px 10px;
  background: rgba(59, 130, 246, 0.08);
  border: 1px solid rgba(59, 130, 246, 0.2);
  border-radius: 20px;
}

.model-pill .sparkle {
  color: #60a5fa;
}

.bar-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.quick-tool-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  color: var(--text-sub);
  font-size: 0.78rem;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--transition-fast);
}

.quick-tool-btn:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
  border-color: var(--border-strong);
}

.tool-count {
  background: var(--primary-blue);
  color: white;
  font-size: 0.65rem;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 8px;
}

.panel-left {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  max-width: 880px;
  margin: 0 auto;
  width: 100%;
}

.input-bar-container {
  padding: 12px 20px;
  background: var(--bg-workspace);
  max-width: 880px;
  margin: 0 auto;
  width: 100%;
}

.panel-right {
  overflow: hidden;
  min-width: 320px;
}

.splitter {
  width: 5px;
  background: var(--bg-app);
  border-left: 1px solid var(--border-subtle);
  border-right: 1px solid var(--border-subtle);
  cursor: col-resize;
  transition: background var(--transition-fast);
  flex-shrink: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: center;
}

.splitter-line {
  width: 2px;
  height: 24px;
  background: var(--text-muted);
  border-radius: 2px;
  opacity: 0.4;
}

.splitter:hover,
.splitter.dragging {
  background: var(--primary-blue);
  border-color: var(--primary-blue);
}

.splitter:hover .splitter-line,
.splitter.dragging .splitter-line {
  background: white;
  opacity: 1;
}
</style>
