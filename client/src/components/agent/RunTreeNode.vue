<template>
  <div class="run-branch">
    <button
      class="run-node"
      :class="nodeClasses"
      type="button"
      @click="$emit('select', run.id)"
    >
      <span class="run-node-main">
        <span class="run-status" :class="'status-' + run.status"></span>
        <span class="run-title">{{ runTitle }}</span>
      </span>
      <span class="run-badges">
        <span v-if="run.runKind === 'delegate'" class="run-kind delegate"
          >子任务</span
        >
        <span v-if="run.id === activeRunId" class="run-kind live">实时</span>
      </span>
    </button>

    <div v-if="runMetaText" class="run-meta">
      {{ runMetaText }}
    </div>

    <div v-if="previewMessages.length > 0" class="run-preview">
      <div
        v-for="(item, index) in previewMessages"
        :key="index"
        class="preview-line"
      >
        <span class="preview-type">{{ previewType(item) }}</span>
        <span class="preview-text">{{ item.summary }}</span>
      </div>
    </div>

    <div v-if="children.length > 0" class="run-children">
      <RunTreeNode
        v-for="child in children"
        :key="child.id"
        :run="child"
        :selected-run-id="selectedRunId"
        :active-run-id="activeRunId"
        @select="$emit('select', $event)"
      />
    </div>
  </div>
</template>

<script setup>
import { computed } from "vue";

defineOptions({
  name: "RunTreeNode",
});

const props = defineProps({
  run: {
    type: Object,
    required: true,
  },
  selectedRunId: {
    type: String,
    default: "",
  },
  activeRunId: {
    type: String,
    default: "",
  },
});

defineEmits(["select"]);

const children = computed(() => props.run.childRuns || []);

const runTitle = computed(() => {
  return (
    props.run.delegateRole ||
    props.run.summary ||
    props.run.inputMessage ||
    props.run.id
  );
});

const runMetaText = computed(() => {
  if (props.run.errorMessage) return props.run.errorMessage;
  if (props.run.summary && props.run.summary !== runTitle.value)
    return props.run.summary;
  if (props.run.inputMessage && props.run.inputMessage !== runTitle.value)
    return props.run.inputMessage;
  return "";
});

const previewMessages = computed(() => props.run.previewMessages || []);

const nodeClasses = computed(() => ({
  active: props.run.id === props.selectedRunId,
  live: props.run.id === props.activeRunId,
}));

function previewType(item) {
  switch (item?.type) {
    case "assistant_status":
      return "状态";
    case "tool_call":
      return "调用";
    case "tool_result":
      return "结果";
    case "error":
      return "错误";
    case "run_completed":
      return "完成";
    default:
      return "事件";
  }
}
</script>

<style scoped>
.run-branch {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.run-node {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  background: var(--bg-app);
  color: var(--text-main);
  cursor: pointer;
  text-align: left;
}

.run-node.active {
  border-color: var(--primary-blue);
  box-shadow: inset 0 0 0 1px rgba(47, 129, 247, 0.2);
}

.run-node.live {
  background: rgba(47, 129, 247, 0.05);
}

.run-node-main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.run-status {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.status-running {
  background: var(--primary-blue);
}
.status-completed {
  background: var(--accent-green);
}
.status-cancelled {
  background: var(--accent-orange);
}
.status-failed {
  background: var(--accent-rose);
}
.status-queued {
  background: var(--text-muted);
}

.run-title {
  font-size: 0.82rem;
  line-height: 1.35;
  word-break: break-word;
}

.run-badges {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.run-kind {
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 0.65rem;
  color: var(--text-sub);
  background: rgba(139, 148, 158, 0.14);
}

.run-kind.delegate {
  color: var(--primary-blue);
  background: rgba(47, 129, 247, 0.12);
}

.run-kind.live {
  color: #d2e9ff;
  background: rgba(47, 129, 247, 0.28);
}

.run-meta {
  margin-top: -2px;
  margin-left: 18px;
  font-size: 0.74rem;
  line-height: 1.35;
  color: var(--text-sub);
}

.run-preview {
  margin-left: 18px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.preview-line {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 0.72rem;
  line-height: 1.35;
  color: var(--text-sub);
}

.preview-type {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 999px;
  background: rgba(139, 148, 158, 0.14);
}

.preview-text {
  word-break: break-word;
}

.run-children {
  margin-left: 18px;
  padding-left: 12px;
  border-left: 1px dashed var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
