<template>
  <aside class="history-panel">
    <!-- Panel Header: Title & New Analysis Button -->
    <div class="panel-header">
      <div class="header-title-row">
        <span class="panel-title">历史分析对话</span>
        <span class="count-pill">{{ sessions.length }}</span>
      </div>

      <button class="btn-new-chat" @click="createNewSession">
        <span class="icon">+</span>
        <span>新建分析</span>
      </button>

      <!-- Search Input -->
      <div class="search-box">
        <span class="search-icon">🔍</span>
        <input
          v-model="searchQuery"
          class="search-input"
          placeholder="搜索会话..."
        />
      </div>
    </div>

    <!-- Session List -->
    <div class="session-list-container">
      <div v-if="filteredSessions.length === 0" class="empty-sessions">
        <span class="empty-icon">💬</span>
        <p>暂无关联会话</p>
      </div>

      <div v-else class="session-list">
        <div
          v-for="session in filteredSessions"
          :key="session.id"
          class="session-item-wrapper"
        >
          <!-- Editing Title Mode -->
          <div v-if="editingSessionId === session.id" class="session-item editing">
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
            class="session-item"
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

    <!-- Workspace Selector Footer -->
    <div class="panel-footer">
      <div class="ws-pill" v-if="workspaceOptions.length > 0">
        <span class="ws-label">工作区</span>
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
    </div>
  </aside>
</template>

<script setup>
import { computed, ref, nextTick } from "vue";
import { useWebSocket } from "../../composables/useWebSocket.js";
import { useAgentStore } from "../../stores/agent.js";

const {
  createNewSession,
  switchWorkspace,
  openSession,
  renameSession,
  deleteSession,
} = useWebSocket();
const store = useAgentStore();

const searchQuery = ref("");
const editingSessionId = ref("");
const editingTitle = ref("");
const editInput = ref(null);

const workspaceOptions = computed(() => store.workspaces || []);
const workspaceId = computed(() => store.workspace?.id || "");
const sessions = computed(() => store.sessions || []);
const currentSessionId = computed(() => store.sessionId || "");

const filteredSessions = computed(() => {
  const query = searchQuery.value.trim().toLowerCase();
  if (!query) return sessions.value;
  return sessions.value.filter(
    (s) =>
      (s.title || "").toLowerCase().includes(query) ||
      (s.id || "").toLowerCase().includes(query),
  );
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
  if (confirm("确定要删除这条历史会话吗？关联记录将不可恢复。")) {
    try {
      await deleteSession(sessionId);
    } catch (err) {
      console.error("delete failed", err);
    }
  }
}
</script>

<style scoped>
.history-panel {
  width: var(--history-width);
  height: 100%;
  background-color: var(--bg-history);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  overflow: hidden;
  user-select: none;
}

.panel-header {
  padding: 14px 12px;
  border-bottom: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.header-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.panel-title {
  font-size: 0.85rem;
  font-weight: 700;
  color: var(--text-main);
  letter-spacing: -0.01em;
}

.count-pill {
  font-size: 0.7rem;
  font-weight: 700;
  background: rgba(255, 255, 255, 0.08);
  color: var(--text-sub);
  padding: 1px 6px;
  border-radius: 10px;
}

.btn-new-chat {
  width: 100%;
  padding: 8px 12px;
  background: linear-gradient(135deg, var(--primary-blue), #2563eb);
  border: 1px solid rgba(255, 255, 255, 0.15);
  border-radius: 8px;
  color: white;
  font-size: 0.83rem;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  cursor: pointer;
  transition: all var(--transition-fast);
  box-shadow: 0 2px 8px rgba(37, 99, 235, 0.3);
}

.btn-new-chat:hover {
  transform: translateY(-1px);
  box-shadow: 0 4px 12px rgba(37, 99, 235, 0.45);
}

.search-box {
  display: flex;
  align-items: center;
  gap: 6px;
  background: var(--bg-app);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  padding: 4px 8px;
}

.search-icon {
  font-size: 0.75rem;
  color: var(--text-muted);
}

.search-input {
  width: 100%;
  background: transparent;
  border: none;
  color: var(--text-main);
  font-size: 0.78rem;
  outline: none;
}

.search-input::placeholder {
  color: var(--text-muted);
}

.session-list-container {
  flex: 1;
  overflow-y: auto;
  padding: 8px 6px;
}

.empty-sessions {
  padding: 24px 12px;
  text-align: center;
  color: var(--text-muted);
  font-size: 0.8rem;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
}

.empty-icon { font-size: 1.4rem; opacity: 0.4; }

.session-list {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.session-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  border: 1px solid transparent;
  background: transparent;
  border-radius: 6px;
  color: var(--text-sub);
  cursor: pointer;
  transition: all var(--transition-fast);
  text-align: left;
}

.session-item:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
}

.session-item.active {
  background: rgba(59, 130, 246, 0.14);
  border-color: rgba(59, 130, 246, 0.3);
  color: var(--text-main);
  font-weight: 600;
}

.item-icon { font-size: 0.85rem; opacity: 0.7; }

.session-title {
  flex: 1;
  font-size: 0.82rem;
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

.session-item:hover .hover-actions {
  display: flex;
}

.action-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 4px;
  font-size: 0.78rem;
  transition: all var(--transition-fast);
}

.action-btn:hover {
  background: rgba(255, 255, 255, 0.15);
  color: white;
}

.action-btn.delete:hover {
  color: var(--accent-rose);
  background: rgba(244, 63, 94, 0.15);
}

.session-item.editing {
  background: var(--bg-app);
  border-color: var(--primary-blue);
}

.edit-input {
  flex: 1;
  border: none;
  background: transparent;
  outline: none;
  color: white;
  font-size: 0.82rem;
  width: 100%;
}

.panel-footer {
  padding: 10px 12px;
  border-top: 1px solid var(--border-subtle);
}

.ws-pill {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ws-label {
  font-size: 0.65rem;
  color: var(--text-muted);
  font-weight: 600;
  text-transform: uppercase;
}

.ws-select {
  width: 100%;
  padding: 5px 8px;
  background: var(--bg-app);
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  color: var(--text-main);
  font-size: 0.78rem;
  outline: none;
  cursor: pointer;
}
</style>
