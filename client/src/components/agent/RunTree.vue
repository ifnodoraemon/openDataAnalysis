<template>
  <div v-if="runs.length > 0" class="run-tree">
    <div class="tree-header" @click="isCollapsed = !isCollapsed">
      <div class="header-title">
        <span class="tree-icon">🧭</span>
        <span>任务树</span>
      </div>
      <button class="toggle-btn" type="button">
        <span class="arrow" :class="{ collapsed: isCollapsed }">▼</span>
      </button>
    </div>

    <div v-show="!isCollapsed" class="tree-content">
      <RunTreeNode
        v-for="run in runs"
        :key="run.id"
        :run="run"
        :selected-run-id="selectedRunId"
        :active-run-id="activeRunId"
        @select="handleSelect"
      />
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from "vue";
import { useAgentTransport } from "../../composables/useAgentTransport.js";
import { useAgentStore } from "../../stores/agent.js";
import RunTreeNode from "./RunTreeNode.vue";

const store = useAgentStore();
const { openRun } = useAgentTransport();
const runs = computed(() => store.runs || []);
const selectedRunId = computed(() => store.selectedRunId || "");
const activeRunId = computed(() => store.activeRunId || "");
const isCollapsed = ref(false);

async function handleSelect(runId) {
  if (!runId || runId === selectedRunId.value) return;
  await openRun(runId);
}
</script>

<style scoped>
.run-tree {
  margin: 12px 12px 0;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  overflow: hidden;
}

.tree-header {
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

.tree-icon {
  font-size: 1rem;
}

.toggle-btn {
  background: transparent;
  border: none;
  color: var(--text-sub);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}

.arrow {
  font-size: 0.6rem;
  transition: transform 0.3s ease;
}

.arrow.collapsed {
  transform: rotate(-90deg);
}

.tree-content {
  padding: 8px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: var(--bg-app);
}
</style>
