<template>
  <aside class="sidebar">
    <!-- Header: Platform Brand & New Analysis Button -->
    <div class="sidebar-header">
      <div class="brand">
        <div class="logo-badge">
          <span class="logo-icon">📊</span>
        </div>
        <div class="brand-info">
          <span class="brand-name">OpenDataAnalysis</span>
          <span class="brand-tag">企业数据智能</span>
        </div>
        <button
          class="collapse-btn"
          @click="$emit('toggle')"
          title="收起侧边栏"
        >
          <span>◀</span>
        </button>
      </div>

      <button class="btn-new-analysis" @click="handleCreateSession">
        <span class="plus-icon">+</span>
        <span>新建分析会话</span>
      </button>
    </div>

    <div class="sidebar-scrollable">
      <!-- Section 1: Core Navigation Hub -->
      <div class="nav-section">
        <div class="section-label">功能工作台</div>

        <button class="feature-nav-card" @click="$emit('open-data-sources')">
          <div class="card-icon blue">📁</div>
          <div class="card-info">
            <div class="card-title">数据源中心</div>
            <div class="card-sub">数据库、对象存储与表格文件</div>
          </div>
          <span class="count-badge" v-if="sourceCount > 0">{{
            sourceCount
          }}</span>
        </button>

        <button class="feature-nav-card" @click="$emit('open-semantic')">
          <div class="card-icon purple">🧠</div>
          <div class="card-info">
            <div class="card-title">数据事实与用户补丁</div>
            <div class="card-sub">观测事实、授权记录与可复用补丁</div>
          </div>
        </button>

        <button class="feature-nav-card" @click="$emit('open-reports')">
          <div class="card-icon green">📜</div>
          <div class="card-info">
            <div class="card-title">分析报告归档</div>
            <div class="card-sub">可交互研报与快照</div>
          </div>
        </button>

        <button
          class="feature-nav-card"
          @click="$emit('open-workspace-settings')"
        >
          <div class="card-icon orange">⚙️</div>
          <div class="card-info">
            <div class="card-title">工作区与团队权限</div>
            <div class="card-sub">权限管理与成员角色</div>
          </div>
        </button>
      </div>

      <div class="divider"></div>

      <!-- Section 2: Session History List -->
      <div class="nav-section">
        <div class="section-label-row">
          <span class="section-label">历史分析记录</span>
          <span class="session-count">{{ sessions.length }}</span>
        </div>

        <div v-if="sessions.length === 0" class="empty-sessions">
          <span class="icon">💬</span>
          <p>暂无历史会话</p>
          <span class="hint">点击上方“新建分析”开启探索</span>
        </div>

        <div v-else class="session-list">
          <div
            v-for="session in sessions"
            :key="session.id"
            class="session-card-wrapper"
          >
            <!-- Editing Mode -->
            <div
              v-if="editingSessionId === session.id"
              class="session-card editing"
            >
              <span class="item-icon">💬</span>
              <input
                ref="editInput"
                class="edit-input"
                v-model="editingTitle"
                @blur="saveRename(session.id)"
                @keyup.enter="saveRename(session.id)"
                @keyup.escape="cancelRename"
              />
            </div>

            <!-- Normal Item -->
            <button
              v-else
              class="session-card"
              :class="{ active: session.id === currentSessionId }"
              @click="handleSessionClick(session.id)"
            >
              <span class="item-icon">💬</span>
              <span class="session-title" :title="session.title || session.id">
                {{ session.title || session.id }}
              </span>

              <div class="hover-actions" @click.stop>
                <button
                  class="action-btn"
                  @click.stop="startRename(session)"
                  title="重命名"
                >
                  ✏️
                </button>
                <button
                  class="action-btn delete"
                  @click.stop="confirmDelete(session.id)"
                  title="删除"
                >
                  🗑️
                </button>
              </div>
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Sidebar Footer: Workspace & User Profile -->
    <div class="sidebar-footer">
      <div class="workspace-pill" v-if="workspaceOptions.length > 0">
        <span class="ws-label">当前工作区</span>
        <select
          class="ws-select"
          :value="workspaceId"
          @change="handleWorkspaceChange"
        >
          <option
            v-for="item in workspaceOptions"
            :key="item.id"
            :value="item.id"
          >
            🏢 {{ item.name }}
          </option>
        </select>
      </div>

      <div class="user-card">
        <div class="avatar">{{ userInitial }}</div>
        <div class="user-details">
          <span class="user-name">{{ userName }}</span>
          <div class="status-badge">
            <span class="status-dot"></span>
            <span>SSE 已连接</span>
          </div>
        </div>
        <button class="logout-btn" @click="logout" title="安全退出">
          登出
        </button>
      </div>
    </div>
  </aside>
</template>

<script setup>
import { computed, ref, nextTick } from "vue";
import { useAgentTransport } from "../../composables/useAgentTransport.js";
import { useAgentStore } from "../../stores/agent.js";
import { useDataSourceStore } from "../../stores/datasource.js";

defineEmits([
  "toggle",
  "open-data-sources",
  "open-semantic",
  "open-reports",
  "open-workspace-settings",
]);

const {
  createNewSession,
  disconnect,
  switchWorkspace,
  openSession,
  renameSession,
  deleteSession,
} = useAgentTransport();
const store = useAgentStore();
const dataSourceStore = useDataSourceStore();

const sourceCount = computed(
  () => dataSourceStore.workspaceDataSources?.length || 0,
);
const workspaceOptions = computed(() => store.workspaces || []);
const workspaceId = computed(() => store.workspace?.id || "");
const sessions = computed(() => store.sessions || []);
const currentSessionId = computed(() => store.sessionId || "");

const editingSessionId = ref("");
const editingTitle = ref("");
const editInput = ref(null);

const userName = computed(() => {
  return store.user?.name || store.user?.email || "管理员";
});

const userInitial = computed(() => {
  return userName.value.charAt(0).toUpperCase();
});

async function handleCreateSession() {
  try {
    await createNewSession();
  } catch (err) {
    store.addMessage({ type: "error", content: "新建分析会话失败" });
    console.error("新建分析会话失败：", err);
  }
}

async function handleSessionClick(sessionId) {
  if (!sessionId || sessionId === currentSessionId.value) return;
  if (editingSessionId.value === sessionId) return;
  try {
    await openSession(sessionId);
  } catch (err) {
    store.addMessage({ type: "error", content: "打开历史会话失败" });
    console.error("打开历史会话失败：", err);
  }
}

async function handleWorkspaceChange(event) {
  const nextWorkspaceId = event.target.value;
  if (!nextWorkspaceId || nextWorkspaceId === workspaceId.value) return;
  try {
    await switchWorkspace(nextWorkspaceId);
  } catch (err) {
    store.addMessage({ type: "error", content: "切换工作区失败" });
    console.error("切换工作区失败：", err);
  }
}

function startRename(session) {
  editingSessionId.value = session.id;
  editingTitle.value = session.title || "";
  nextTick(() => {
    if (editInput.value && editInput.value.length > 0) {
      editInput.value[0].focus();
    } else if (editInput.value && editInput.value.focus) {
      editInput.value.focus();
    }
  });
}

function cancelRename() {
  editingSessionId.value = "";
  editingTitle.value = "";
}

async function saveRename(sessionId) {
  if (!editingSessionId.value || editingSessionId.value !== sessionId) return;
  const newTitle = editingTitle.value.trim();
  const oldTitle = sessions.value.find((s) => s.id === sessionId)?.title || "";

  editingSessionId.value = "";
  editingTitle.value = "";

  if (newTitle && newTitle !== oldTitle) {
    try {
      await renameSession(sessionId, newTitle);
    } catch (err) {
      store.addMessage({ type: "error", content: "重命名会话失败" });
      console.error("重命名会话失败：", err);
    }
  }
}

async function confirmDelete(sessionId) {
  if (confirm("确定要删除这条历史会话吗？关联运行记录将不再恢复。")) {
    try {
      await deleteSession(sessionId);
    } catch (err) {
      store.addMessage({ type: "error", content: "删除会话失败" });
      console.error("删除会话失败：", err);
    }
  }
}

function logout() {
  disconnect();
  store.logout();
}
</script>

<style scoped>
.sidebar {
  width: 280px;
  height: 100%;
  background: var(--bg-card);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  overflow: hidden;
  user-select: none;
}

.sidebar-header {
  padding: 16px 14px 12px;
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 12px;
  background: rgba(0, 0, 0, 0.2);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
}

.logo-badge {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: linear-gradient(
    135deg,
    var(--primary-blue),
    var(--accent-purple)
  );
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 0 12px rgba(59, 130, 246, 0.4);
}

.logo-icon {
  font-size: 1.1rem;
}

.brand-info {
  display: flex;
  flex-direction: column;
  flex: 1;
}

.brand-name {
  font-size: 0.95rem;
  font-weight: 700;
  color: var(--text-main);
  letter-spacing: -0.01em;
}

.brand-tag {
  font-size: 0.68rem;
  color: var(--primary-blue);
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
}

.collapse-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 6px;
  font-size: 0.75rem;
  transition: all var(--transition-fast);
}

.collapse-btn:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
}

.btn-new-analysis {
  width: 100%;
  padding: 10px 14px;
  background: linear-gradient(135deg, var(--primary-blue), #2563eb);
  border: 1px solid rgba(255, 255, 255, 0.15);
  border-radius: 10px;
  color: white;
  font-size: 0.88rem;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  cursor: pointer;
  transition: all var(--transition-fast);
  box-shadow: 0 4px 12px rgba(37, 99, 235, 0.35);
}

.btn-new-analysis:hover {
  transform: translateY(-1px);
  box-shadow: 0 6px 16px rgba(37, 99, 235, 0.5);
}

.sidebar-scrollable {
  flex: 1;
  overflow-y: auto;
  padding: 12px 10px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.nav-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.section-label {
  font-size: 0.72rem;
  font-weight: 700;
  color: var(--text-muted);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  padding: 0 6px;
  margin-bottom: 2px;
}

.section-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 6px;
}

.session-count {
  font-size: 0.7rem;
  background: var(--bg-card-hover);
  color: var(--text-sub);
  padding: 1px 6px;
  border-radius: 10px;
  font-weight: 600;
}

.feature-nav-card {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid transparent;
  background: rgba(255, 255, 255, 0.02);
  border-radius: 8px;
  cursor: pointer;
  text-align: left;
  transition: all var(--transition-fast);
}

.feature-nav-card:hover {
  background: var(--bg-card-hover);
  border-color: var(--border-subtle);
}

.card-icon {
  width: 28px;
  height: 28px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.9rem;
  flex-shrink: 0;
}

.card-icon.blue {
  background: rgba(59, 130, 246, 0.15);
  color: #60a5fa;
}
.card-icon.purple {
  background: rgba(139, 92, 246, 0.15);
  color: #c084fc;
}
.card-icon.green {
  background: rgba(16, 185, 129, 0.15);
  color: #34d399;
}
.card-icon.orange {
  background: rgba(245, 158, 11, 0.15);
  color: #fbbf24;
}

.card-info {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.card-title {
  font-size: 0.83rem;
  font-weight: 600;
  color: var(--text-main);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.card-sub {
  font-size: 0.7rem;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.count-badge {
  background: var(--primary-blue);
  color: white;
  font-size: 0.7rem;
  font-weight: 700;
  padding: 1px 6px;
  border-radius: 10px;
}

.divider {
  height: 1px;
  background: var(--border-subtle);
  margin: 4px 0;
}

.empty-sessions {
  padding: 20px 10px;
  text-align: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}

.empty-sessions .icon {
  font-size: 1.5rem;
  opacity: 0.4;
}
.empty-sessions p {
  font-size: 0.82rem;
  font-weight: 500;
}
.empty-sessions .hint {
  font-size: 0.72rem;
  color: var(--text-muted);
}

.session-list {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.session-card {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid transparent;
  background: transparent;
  border-radius: 8px;
  color: var(--text-sub);
  cursor: pointer;
  transition: all var(--transition-fast);
  text-align: left;
}

.session-card:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
}

.session-card.active {
  background: rgba(59, 130, 246, 0.12);
  border-color: rgba(59, 130, 246, 0.3);
  color: var(--text-main);
  font-weight: 600;
}

.item-icon {
  font-size: 0.9rem;
  opacity: 0.7;
}

.session-title {
  flex: 1;
  font-size: 0.83rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.hover-actions {
  display: none;
  align-items: center;
  gap: 2px;
  margin-left: auto;
}

.session-card:hover .hover-actions {
  display: flex;
}

.action-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 3px 5px;
  border-radius: 4px;
  font-size: 0.8rem;
  transition: all var(--transition-fast);
}

.action-btn:hover {
  background: rgba(255, 255, 255, 0.15);
  color: white;
}

.action-btn.delete:hover {
  color: var(--accent-rose);
  background: rgba(239, 68, 68, 0.15);
}

.session-card.editing {
  background: var(--bg-card-hover);
  border-color: var(--primary-blue);
}

.edit-input {
  flex: 1;
  border: none;
  background: transparent;
  outline: none;
  color: white;
  font-size: 0.83rem;
  width: 100%;
}

.sidebar-footer {
  padding: 12px;
  border-top: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 10px;
  background: rgba(0, 0, 0, 0.15);
}

.workspace-pill {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ws-label {
  font-size: 0.68rem;
  color: var(--text-muted);
  font-weight: 600;
  text-transform: uppercase;
}

.ws-select {
  width: 100%;
  padding: 6px 10px;
  background: var(--bg-card-hover);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  color: var(--text-main);
  font-size: 0.8rem;
  outline: none;
  cursor: pointer;
}

.user-card {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 8px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 8px;
  border: 1px solid var(--border-subtle);
}

.avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: linear-gradient(
    135deg,
    var(--primary-blue),
    var(--accent-purple)
  );
  color: white;
  font-weight: 700;
  font-size: 0.85rem;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.user-details {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.user-name {
  font-size: 0.82rem;
  font-weight: 600;
  color: var(--text-main);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.status-badge {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 0.68rem;
  color: var(--accent-green);
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent-green);
  box-shadow: 0 0 6px var(--accent-green);
}

.logout-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  font-size: 0.78rem;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;
  transition: all var(--transition-fast);
}

.logout-btn:hover {
  background: rgba(239, 68, 68, 0.15);
  color: var(--accent-rose);
}
</style>
