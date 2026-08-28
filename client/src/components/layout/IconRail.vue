<template>
  <nav class="icon-rail">
    <!-- Brand Logo -->
    <div class="rail-top">
      <div class="logo-box" title="OpenDataAnalysis 数据分析平台">
        <span class="logo-icon">📊</span>
      </div>
    </div>

    <!-- Feature Icon List -->
    <div class="rail-menu">
      <button
        class="rail-btn"
        :class="{ active: isHistoryOpen }"
        @click="$emit('toggle-history')"
        title="历史对话列表"
      >
        <span class="btn-icon">💬</span>
        <span class="btn-tooltip">历史对话</span>
      </button>

      <button
        class="rail-btn"
        @click="$emit('open-data-sources')"
        title="数据源中心"
      >
        <span class="btn-icon">📁</span>
        <span class="badge" v-if="sourceCount > 0">{{ sourceCount }}</span>
        <span class="btn-tooltip">数据源管理</span>
      </button>

      <button
        class="rail-btn"
        @click="$emit('open-semantic')"
        title="数据事实与用户补丁"
      >
        <span class="btn-icon">🧠</span>
        <span class="btn-tooltip">数据事实</span>
      </button>

      <button
        class="rail-btn"
        @click="$emit('open-reports')"
        title="分析报告归档"
      >
        <span class="btn-icon">📜</span>
        <span class="btn-tooltip">报告归档</span>
      </button>

      <button
        class="rail-btn"
        @click="$emit('open-workspace-settings')"
        title="工作区与团队权限设置"
      >
        <span class="btn-icon">⚙️</span>
        <span class="btn-tooltip">工作区配置</span>
      </button>
    </div>

    <!-- Rail Footer: User Profile Avatar -->
    <div class="rail-footer">
      <div class="user-avatar-box" :title="userName">
        <span>{{ userInitial }}</span>
      </div>
      <button class="logout-icon-btn" @click="logout" title="退出登录">
        <span>🚪</span>
      </button>
    </div>
  </nav>
</template>

<script setup>
import { computed } from "vue";
import { useAgentTransport } from "../../composables/useAgentTransport.js";
import { useAgentStore } from "../../stores/agent.js";
import { useDataSourceStore } from "../../stores/datasource.js";

defineProps({
  isHistoryOpen: { type: Boolean, default: true },
});

defineEmits([
  "toggle-history",
  "open-data-sources",
  "open-semantic",
  "open-reports",
  "open-workspace-settings",
]);

const { disconnect } = useAgentTransport();
const store = useAgentStore();
const dataSourceStore = useDataSourceStore();

const sourceCount = computed(
  () => dataSourceStore.workspaceDataSources?.length || 0,
);

const userName = computed(() => {
  return store.user?.name || store.user?.email || "用户";
});

const userInitial = computed(() => {
  return userName.value.charAt(0).toUpperCase();
});

function logout() {
  disconnect();
  store.logout();
}
</script>

<style scoped>
.icon-rail {
  width: var(--rail-width);
  height: 100%;
  background-color: var(--bg-rail);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: space-between;
  padding: 12px 0;
  flex-shrink: 0;
  z-index: 20;
}

.rail-top {
  display: flex;
  justify-content: center;
}

.logo-box {
  width: 38px;
  height: 38px;
  border-radius: 10px;
  background: linear-gradient(
    135deg,
    var(--primary-blue),
    var(--accent-purple)
  );
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.2rem;
  box-shadow: 0 0 14px rgba(59, 130, 246, 0.4);
}

.rail-menu {
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: center;
}

.rail-btn {
  position: relative;
  width: 40px;
  height: 40px;
  border: 1px solid transparent;
  background: transparent;
  border-radius: 10px;
  color: var(--text-sub);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all var(--transition-fast);
}

.rail-btn:hover {
  background: var(--bg-card-hover);
  color: var(--text-main);
  border-color: var(--border-subtle);
}

.rail-btn.active {
  background: var(--primary-glow);
  color: var(--primary-blue);
  border-color: var(--border-accent);
}

.btn-icon {
  font-size: 1.1rem;
}

.badge {
  position: absolute;
  top: 2px;
  right: 2px;
  background: var(--primary-blue);
  color: white;
  font-size: 0.65rem;
  font-weight: 700;
  padding: 1px 4px;
  border-radius: 8px;
  line-height: 1;
}

/* Tooltip on Hover */
.btn-tooltip {
  position: absolute;
  left: 50px;
  top: 50%;
  transform: translateY(-50%);
  background: #1e293b;
  color: #f8fafc;
  font-size: 0.75rem;
  font-weight: 600;
  padding: 5px 10px;
  border-radius: 6px;
  white-space: nowrap;
  pointer-events: none;
  opacity: 0;
  visibility: hidden;
  transition: all var(--transition-fast);
  box-shadow: var(--shadow-panel);
  border: 1px solid var(--border-strong);
  z-index: 100;
}

.rail-btn:hover .btn-tooltip {
  opacity: 1;
  visibility: visible;
  left: 54px;
}

.rail-footer {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}

.user-avatar-box {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  background: linear-gradient(135deg, #4f46e5, #06b6d4);
  color: white;
  font-weight: 700;
  font-size: 0.85rem;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 0 10px rgba(79, 70, 229, 0.4);
}

.logout-icon-btn {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 1rem;
  padding: 4px;
  border-radius: 6px;
  transition: all var(--transition-fast);
}

.logout-icon-btn:hover {
  color: var(--accent-rose);
  background: rgba(244, 63, 94, 0.15);
}
</style>
