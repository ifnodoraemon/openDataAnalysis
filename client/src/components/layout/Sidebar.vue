<template>
  <aside class="sidebar">
    <div class="sidebar-header">
      <button class="toggle-btn" @click="$emit('toggle')" title="收起侧边栏">
        <span class="icon">▤</span>
      </button>
      <button class="new-chat-btn" @click="createNewSession">
        <span class="icon">✨</span>
        <span class="text">新建分析</span>
      </button>
    </div>

    <div class="sidebar-content">
      <!-- Feature Navigation Section -->
      <div class="nav-group">
        <div class="group-title">核心功能</div>
        <button class="nav-item" @click="$emit('open-data-sources')">
          <span class="nav-icon">📁</span>
          <span class="nav-text">数据源管理</span>
          <span class="badge" v-if="sourceCount > 0">{{ sourceCount }}</span>
        </button>
        <button class="nav-item" @click="$emit('open-semantic')">
          <span class="nav-icon">🧠</span>
          <span class="nav-text">语义模型与指标</span>
        </button>
        <button class="nav-item" @click="$emit('open-reports')">
          <span class="nav-icon">📜</span>
          <span class="nav-text">分析报告库</span>
        </button>
        <button class="nav-item" @click="$emit('open-workspace-settings')">
          <span class="nav-icon">⚙️</span>
          <span class="nav-text">工作区与团队设置</span>
        </button>
      </div>

      <!-- Session History Section -->
      <div v-if="sessions.length === 0" class="empty-sessions">
        暂无历史对话
      </div>
      <div class="session-list" v-else>
        <div class="session-group">
          <div class="group-title">历史分析对话</div>
          <div
            v-for="session in sessions"
            :key="session.id"
            class="session-item-wrapper"
          >
            <!-- Editing State -->
            <div
              v-if="editingSessionId === session.id"
              class="session-item editing"
            >
              <span class="session-icon">💬</span>
              <input
                ref="editInput"
                class="edit-input"
                v-model="editingTitle"
                @blur="saveRename(session.id)"
                @keyup.enter="saveRename(session.id)"
                @keyup.escape="cancelRename"
              />
            </div>
            <!-- Normal State -->
            <button
              v-else
              class="session-item"
              :class="{ active: session.id === currentSessionId }"
              @click="handleSessionClick(session.id)"
            >
              <span class="session-icon">💬</span>
              <span class="session-title" :title="session.title">{{
                session.title || session.id
              }}</span>

              <div class="session-actions" @click.stop>
                <button
                  class="action-btn"
                  @click.stop="startRename(session)"
                  title="重命名"
                  aria-label="重命名"
                >
                  ✏️
                </button>
                <button
                  class="action-btn delete"
                  @click.stop="confirmDelete(session.id)"
                  title="删除"
                  aria-label="删除"
                >
                  🗑️
                </button>
              </div>
            </button>
          </div>
        </div>
      </div>
    </div>

    <div class="sidebar-footer">
      <div class="workspace-selector" v-if="workspaceOptions.length > 0">
        <span class="label">工作区</span>
        <select
          class="select-input"
          :value="workspaceId"
          @change="handleWorkspaceChange"
        >
          <option
            v-for="item in workspaceOptions"
            :key="item.id"
            :value="item.id"
          >
            {{ item.name }}
          </option>
        </select>
      </div>

      <div class="user-profile">
        <div class="avatar">{{ userInitial }}</div>
        <div class="user-info">
          <span class="user-name">{{
            store.user?.name ||
            store.user?.username ||
            store.user?.email ||
            "User"
          }}</span>
          <div class="status-indicator">
            <span class="dot" :class="connected ? 'online' : 'offline'"></span>
            <span class="text">{{ statusText }}</span>
          </div>
        </div>
        <button class="logout-btn" @click="logout" title="退出">登出</button>
      </div>
    </div>
  </aside>
</template>

<script setup>
import { computed, ref, nextTick } from "vue";
import { useWebSocket } from "../../composables/useWebSocket.js";
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
  connected,
  createNewSession,
  disconnect,
  switchWorkspace,
  openSession,
  renameSession,
  deleteSession,
} = useWebSocket();
const store = useAgentStore();
const dataSourceStore = useDataSourceStore();

const sourceCount = computed(() => dataSourceStore.workspaceDataSources?.length || 0);
const workspaceOptions = computed(() => store.workspaces || []);
const workspaceId = computed(() => store.workspace?.id || "");
const sessions = computed(() => store.sessions || []);
const currentSessionId = computed(() => store.sessionId || "");

// Rename logic
const editingSessionId = ref("");
const editingTitle = ref("");
const editInput = ref(null);

// Computed user initials
const userInitial = computed(() => {
  const name =
    store.user?.name || store.user?.username || store.user?.email || "U";
  return name.charAt(0).toUpperCase();
});

const statusText = computed(() => {
  switch (store.connectionState) {
    case "connected":
      return "已连接";
    case "reconnecting":
      return "重连中...";
    case "disconnected":
      return "未连接";
    default:
      return "连接中...";
  }
});

async function handleSessionClick(sessionId) {
  if (!sessionId || sessionId === currentSessionId.value) return;
  if (editingSessionId.value === sessionId) return;
  try {
    await openSession(sessionId);
  } catch (err) {
    console.error("open session failed:", err);
  }
}

async function handleWorkspaceChange(event) {
  const nextWorkspaceId = event.target.value;
  if (!nextWorkspaceId || nextWorkspaceId === workspaceId.value) return;
  try {
    await switchWorkspace(nextWorkspaceId);
  } catch (err) {
    console.error("switch workspace failed:", err);
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
      console.error("rename failed", err);
    }
  }
}

async function confirmDelete(sessionId) {
  if (confirm("确定要删除这条对话记录吗？此操作无法恢复。")) {
    try {
      await deleteSession(sessionId);
    } catch (err) {
      console.error("delete failed", err);
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
  width: 260px;
  height: 100%;
  background-color: var(--bg-secondary);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  overflow: hidden;
  transition: transform var(--transition);
}

.sidebar-header {
  padding: 16px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
}

.toggle-btn {
  background: transparent;
  border: none;
  color: var(--text-secondary);
  font-size: 1.2rem;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;
  transition: all var(--transition);
  display: flex;
  align-items: center;
  justify-content: center;
}

.toggle-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.new-chat-btn {
  flex: 1;
  padding: 8px 14px;
  background-color: var(--bg-primary);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text-primary);
  font-size: 0.85rem;
  font-weight: 500;
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  transition: all var(--transition);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.05);
}

.new-chat-btn:hover {
  background-color: var(--bg-hover);
  border-color: var(--border-light);
}

.sidebar-content {
  flex: 1;
  overflow-y: auto;
  padding: 0 12px;
}

.empty-sessions {
  padding: 24px 12px;
  text-align: center;
  color: var(--text-muted);
  font-size: 0.85rem;
}

.nav-group {
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border);
}

.nav-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border: none;
  background: transparent;
  border-radius: 8px;
  color: var(--text-primary);
  cursor: pointer;
  text-align: left;
  font-size: 0.86rem;
  font-weight: 500;
  transition: all var(--transition);
  margin-bottom: 2px;
}

.nav-item:hover {
  background-color: var(--bg-hover);
}

.nav-icon {
  font-size: 1rem;
  flex-shrink: 0;
}

.nav-text {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.badge {
  background: rgba(37, 99, 235, 0.12);
  color: var(--accent-blue);
  font-size: 0.72rem;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 999px;
}

.session-group {
  margin-bottom: 24px;
}

.group-title {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--text-muted);
  padding: 8px 12px;
  margin-bottom: 4px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.session-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: none;
  background: transparent;
  border-radius: 6px;
  color: var(--text-primary);
  cursor: pointer;
  text-align: left;
  transition: background-color var(--transition);
  margin-bottom: 4px;
}

.session-item:hover {
  background-color: var(--bg-hover);
}

.session-item.active {
  background-color: var(--bg-hover);
  font-weight: 500;
}

.session-icon {
  font-size: 1rem;
  opacity: 0.7;
}

.session-title {
  flex: 1;
  font-size: 0.85rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.session-item-wrapper {
  position: relative;
  display: block;
}

.session-actions {
  display: none;
  align-items: center;
  gap: 2px;
  margin-left: auto;
}

.session-item:hover .session-actions {
  display: flex;
}

.action-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 0.9rem;
  transition: all var(--transition);
}

.action-btn:hover {
  background: var(--bg-primary);
  color: var(--text-primary);
}

.action-btn.delete:hover {
  color: var(--accent-red);
}

.session-item.editing {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background-color: var(--bg-primary);
  border-radius: 6px;
  margin-bottom: 4px;
  border: 1px solid var(--accent-blue);
}

.edit-input {
  flex: 1;
  border: none;
  background: transparent;
  outline: none;
  color: var(--text-primary);
  font-size: 0.85rem;
  width: 100%;
}

.sidebar-footer {
  padding: 16px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.workspace-selector {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border);
}

.workspace-selector .label {
  font-size: 0.75rem;
  color: var(--text-muted);
  font-weight: 500;
}

.select-input {
  width: 100%;
  padding: 6px 10px;
  background-color: var(--bg-primary);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text-primary);
  font-size: 0.85rem;
  outline: none;
  cursor: pointer;
}

.user-profile {
  display: flex;
  align-items: center;
  gap: 10px;
  border-radius: 8px;
  padding: 4px;
}

.avatar {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: linear-gradient(135deg, var(--accent-blue), var(--accent-purple));
  color: white;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.9rem;
  font-weight: 600;
  flex-shrink: 0;
}

.user-info {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 2px;
}

.user-name {
  font-size: 0.85rem;
  font-weight: 500;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.status-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.75rem;
  color: var(--text-secondary);
}

.status-indicator .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}

.dot.online {
  background-color: var(--accent-green);
  box-shadow: 0 0 4px var(--accent-green);
}

.dot.offline {
  background-color: var(--accent-orange);
  animation: pulse 1.5s infinite;
}

.logout-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  font-size: 0.8rem;
  cursor: pointer;
  padding: 6px 12px;
  border-radius: 6px;
  transition: all var(--transition);
  flex-shrink: 0;
}

.logout-btn:hover {
  background-color: rgba(220, 38, 38, 0.05);
  color: var(--accent-red);
}
</style>
