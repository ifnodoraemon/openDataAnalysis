<template>
  <div v-if="subgoals.length > 0" class="subgoal-tree">
    <div class="tree-header" @click="isCollapsed = !isCollapsed">
      <div class="header-title">
        <span class="tree-icon">🎯</span>
        <span>阶段目标 ({{ completedCount }}/{{ subgoals.length }})</span>
      </div>
      <button class="toggle-btn">
        <span class="arrow" :class="{ collapsed: isCollapsed }">▼</span>
      </button>
    </div>

    <div class="tree-content" v-show="!isCollapsed">
      <transition-group name="list" tag="div" class="goal-list">
        <SubgoalTreeNode
          v-for="goal in rootGoals"
          :key="goal.id"
          :goal="goal"
          :all-goals="subgoals"
        />
      </transition-group>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from "vue";
import { useAgentStore } from "../../stores/agent.js";
import SubgoalTreeNode from "./SubgoalTreeNode.vue";

const store = useAgentStore();
const subgoals = computed(() => store.subgoals || []);
const isCollapsed = ref(false);

const completedCount = computed(() => {
  return subgoals.value.filter((g) => g.status === "complete").length;
});
const rootGoals = computed(() =>
  subgoals.value.filter((goal) => !goal.parentGoalId),
);
</script>

<style scoped>
.subgoal-tree {
  margin: 12px 12px 0 12px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  overflow: hidden;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
}

.tree-header {
  padding: 10px 14px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  cursor: pointer;
  background: var(--bg-card);
  border-bottom: 1px solid var(--border-subtle);
  transition: background 0.2s;
}

.tree-header:hover {
  background: var(--bg-card-hover);
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
  padding: 4px;
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
  background: var(--bg-app);
}

.goal-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.list-enter-active,
.list-leave-active {
  transition: all 0.4s ease;
}
.list-enter-from,
.list-leave-to {
  opacity: 0;
  transform: translateX(-15px);
}
</style>
