<template>
  <div class="goal-branch">
    <div class="goal-item" :class="'status-' + goal.status">
      <div class="goal-main">
        <span class="goal-status-icon" :title="getStatusText(goal.status)">
          {{ getStatusIcon(goal.status) }}
        </span>
        <span class="goal-desc">{{ goal.description }}</span>
      </div>

      <div v-if="goal.result" class="goal-result">
        <span class="result-brand">↳</span>
        <span class="result-text">{{ goal.result }}</span>
      </div>
    </div>

    <div v-if="children.length > 0" class="goal-children">
      <SubgoalTreeNode
        v-for="child in children"
        :key="child.id"
        :goal="child"
        :all-goals="allGoals"
      />
    </div>
  </div>
</template>

<script setup>
import { computed } from "vue";

defineOptions({
  name: "SubgoalTreeNode",
});

const props = defineProps({
  goal: {
    type: Object,
    required: true,
  },
  allGoals: {
    type: Array,
    required: true,
  },
});

const children = computed(() => {
  return props.allGoals.filter(
    (item) => (item.parentGoalId || "") === props.goal.id,
  );
});

function getStatusIcon(status) {
  switch (status) {
    case "complete":
      return "✅";
    case "running":
      return "⏳";
    case "rejected":
      return "❌";
    default:
      return "⚪";
  }
}

function getStatusText(status) {
  switch (status) {
    case "complete":
      return "已完成";
    case "running":
      return "进行中";
    case "rejected":
      return "已放弃";
    default:
      return "待处理";
  }
}
</script>

<style scoped>
.goal-branch {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.goal-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--bg-card);
  border-left: 3px solid transparent;
  transition: all 0.3s ease;
}

.goal-item.status-pending {
  border-left-color: var(--text-muted);
}

.goal-item.status-running {
  border-left-color: var(--primary-blue);
  background: rgba(47, 129, 247, 0.05);
}

.goal-item.status-complete {
  border-left-color: var(--accent-green);
}

.goal-item.status-rejected {
  border-left-color: var(--accent-orange);
}

.goal-main {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}

.goal-status-icon {
  font-size: 0.85rem;
  margin-top: 2px;
  flex-shrink: 0;
}

.goal-desc {
  font-size: 0.85rem;
  color: var(--text-main);
  line-height: 1.4;
  word-break: break-word;
}

.status-complete .goal-desc {
  color: var(--text-sub);
}

.status-rejected .goal-desc {
  color: var(--text-muted);
  text-decoration: line-through;
}

.goal-result {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin-left: 20px;
  font-size: 0.75rem;
  color: var(--text-muted);
  background: rgba(0, 0, 0, 0.03);
  padding: 4px 8px;
  border-radius: 4px;
  margin-top: 2px;
}

.result-brand {
  color: var(--text-muted);
}

.result-text {
  line-height: 1.3;
}

.goal-children {
  margin-left: 18px;
  padding-left: 12px;
  border-left: 1px dashed var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
