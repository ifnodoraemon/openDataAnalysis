<template>
  <div v-if="entries.length > 0" class="memory-panel">
    <div class="memory-header" @click="isCollapsed = !isCollapsed">
      <div class="header-title">
        <span class="memory-icon">🧾</span>
        <span>工作记忆 ({{ entries.length }})</span>
      </div>
      <button class="toggle-btn" type="button">
        <span class="arrow" :class="{ collapsed: isCollapsed }">▼</span>
      </button>
    </div>

    <div v-show="!isCollapsed" class="memory-content">
      <div v-for="[key, value] in entries" :key="key" class="memory-item">
        <div class="memory-key">{{ key }}</div>
        <div class="memory-value">{{ value.statement }}</div>
        <div class="memory-meta">
          {{ memoryStatusLabel(value.status) }} ·
          {{ memoryCreatorLabel(value.created_by) }}
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from "vue";
import { useAgentStore } from "../../stores/agent.js";

const store = useAgentStore();
const isCollapsed = ref(false);
const entries = computed(() => Object.entries(store.memoryEntries || {}));

function memoryStatusLabel(status) {
  return (
    {
      observed: "已观测",
      inferred: "推断",
      assumed: "假设",
    }[status] || "未标注"
  );
}

function memoryCreatorLabel(createdBy) {
  return createdBy === "agent" ? "智能体" : createdBy || "来源未知";
}
</script>

<style scoped>
.memory-panel {
  margin: 12px 12px 0 12px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  overflow: hidden;
}

.memory-header {
  padding: 10px 14px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  cursor: pointer;
  background: var(--bg-card);
  border-bottom: 1px solid var(--border-subtle);
}

.header-title {
  font-size: 0.85rem;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-main);
}

.memory-content {
  padding: 8px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.memory-item {
  padding: 8px 10px;
  background: var(--bg-app);
  border-radius: 6px;
  border-left: 3px solid var(--accent-green);
}

.memory-key {
  font-size: 0.75rem;
  font-weight: 700;
  color: var(--text-sub);
  margin-bottom: 4px;
}

.memory-value {
  font-size: 0.85rem;
  color: var(--text-main);
  line-height: 1.4;
  word-break: break-word;
}

.memory-meta {
  margin-top: 4px;
  font-size: 0.7rem;
  color: var(--text-sub);
}

.toggle-btn {
  background: transparent;
  border: none;
  color: var(--text-sub);
  cursor: pointer;
}

.arrow {
  font-size: 0.6rem;
  transition: transform 0.3s ease;
}

.arrow.collapsed {
  transform: rotate(-90deg);
}
</style>
