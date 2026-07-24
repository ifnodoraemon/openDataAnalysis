<template>
  <div v-if="isRestoring" class="app-loading">正在恢复工作区...</div>
  <div v-else-if="hasRestoreError" class="app-loading error-state">
    <div class="error-card">
      <p>工作区恢复失败</p>
      <p class="error-message">{{ restoreError }}</p>
      <button class="retry-btn" type="button" @click="retryInit">重试</button>
    </div>
  </div>
  <LoginScreen v-else-if="!isAuthenticated" />
  <div v-else class="app">
    <Sidebar
      v-if="isSidebarOpen"
      @toggle="toggleSidebar"
      @open-data-sources="showSourcesDrawer = true"
      @open-semantic="showSemanticModal = true"
      @open-reports="handleOpenReports"
      @open-workspace-settings="showWorkspaceModal = true"
    />
    <div class="main-content">
      <div class="chat-area" :style="{ width: leftWidth + '%' }">
        <div class="top-bar-minimal">
          <button
            v-if="!isSidebarOpen"
            class="toggle-sidebar-btn"
            @click="toggleSidebar"
            title="展开侧边栏"
          >
            <span class="icon">▤</span>
          </button>
          <span class="logo">📊 Open Data Analysis</span>
        </div>
        <AgentPanel class="panel-left" />
        <InputBar class="input-bar-container" />
      </div>
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
      ></div>
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
import Sidebar from "./components/layout/Sidebar.vue";
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

const leftWidth = ref(50);
const isDragging = ref(false);
const isSidebarOpen = ref(true);

// Modals state
const showSourcesDrawer = ref(false);
const showWorkspaceModal = ref(false);
const showSemanticModal = ref(false);

const isAuthenticated = computed(() => !!store.token && !!store.user);
const isRestoring = computed(() => store.bootstrapState === "loading");
const hasRestoreError = computed(
  () => store.bootstrapState === "error" && !!store.token,
);
const restoreError = computed(() => store.bootstrapError || "请稍后重试。");

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

function toggleSidebar() {
  isSidebarOpen.value = !isSidebarOpen.value;
}

function handleOpenReports() {
  // If report preview pane is narrow, adjust width for report viewing
  if (leftWidth.value > 50) {
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
  display: grid;
  place-items: center;
  background: var(--bg-primary);
  color: var(--text-secondary);
  letter-spacing: 0.04em;
}

.error-state {
  padding: 24px;
}

.error-card {
  max-width: 420px;
  padding: 20px 24px;
  border: 1px solid var(--border);
  border-radius: 16px;
  background: var(--bg-secondary);
  text-align: center;
}

.error-message {
  margin: 12px 0 0;
  color: var(--accent-red);
  line-height: 1.5;
}

.retry-btn {
  margin-top: 16px;
  padding: 8px 16px;
  border: 1px solid var(--accent-blue);
  border-radius: 8px;
  background: var(--accent-blue);
  color: white;
  cursor: pointer;
}

.app {
  height: 100vh;
  display: flex;
  background: var(--bg-primary);
  overflow: hidden;
}

.main-content {
  flex: 1;
  display: flex;
  overflow: hidden;
  position: relative;
  background: var(--bg-primary);
}

.chat-area {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 300px;
  background: var(--bg-primary);
}

.top-bar-minimal {
  padding: 16px;
  font-size: 1.1rem;
  font-weight: 600;
  color: var(--text-primary);
  background: transparent;
  flex-shrink: 0;
  display: flex;
  align-items: center;
}

.toggle-sidebar-btn {
  background: transparent;
  border: none;
  color: var(--text-secondary);
  font-size: 1.2rem;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;
  margin-right: 12px;
  transition: all var(--transition);
}

.toggle-sidebar-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.logo {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-primary);
}

.panel-left {
  flex: 1;
  overflow-y: auto;
  padding: 0 16px;
  max-width: 800px;
  margin: 0 auto;
  width: 100%;
}

.input-bar-container {
  padding: 16px;
  background: var(--bg-primary);
  border-top: none;
  max-width: 800px;
  margin: 0 auto;
  width: 100%;
}

.panel-right {
  overflow: hidden;
  min-width: 300px;
}

.splitter {
  width: 4px;
  background: var(--border);
  cursor: col-resize;
  transition: background var(--transition);
  flex-shrink: 0;
  z-index: 10;
}

.splitter:hover,
.splitter.dragging {
  background: var(--accent-blue);
}
</style>
