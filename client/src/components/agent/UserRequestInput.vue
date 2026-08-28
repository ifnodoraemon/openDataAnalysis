<template>
  <div class="msg-icon">🙋</div>
  <div class="msg-body">
    <div class="msg-label ask-user-label">{{ label }}</div>
    <div
      class="msg-content markdown-body ask-user-question"
      v-html="renderMarkdown(msg.question)"
    ></div>

    <div v-if="metaRows.length" class="ask-user-meta">
      <div v-for="row in metaRows" :key="row" class="ask-user-meta-row">
        {{ row }}
      </div>
    </div>

    <div v-if="msg.authorization" class="ask-authorization">
      <div>待授权动作：{{ msg.authorization.action }}</div>
      <div>目标资源：{{ msg.authorization.resource_ref }}</div>
    </div>

    <div v-if="hasOptions" class="ask-custom-hint">
      请在下方输入框选择或输入您的回复。
    </div>
    <div v-else class="ask-custom-hint">请在下方输入框中输入您的回复。</div>
  </div>
</template>

<script setup>
import { computed } from "vue";

const props = defineProps({
  msg: {
    type: Object,
    required: true,
  },
  renderMarkdown: {
    type: Function,
    required: true,
  },
});

const normalizedOptions = computed(() =>
  (props.msg.options || [])
    .map((option) => {
      const id = String(option?.id || "").trim();
      const label = String(option?.label || "").trim();
      return {
        id,
        label,
        hint: String(option?.hint || "").trim(),
      };
    })
    .filter((option) => option.id && option.label),
);

const hasOptions = computed(() => normalizedOptions.value.length > 0);
const isMultiple = computed(() => props.msg.selection_mode === "multiple");
const label = computed(() => {
  if (!hasOptions.value) return "等待您描述";
  return isMultiple.value ? "请选择一个或多个选项" : "请选择一个选项";
});
const metaRows = computed(() => {
  const rows = [];
  if (props.msg.reason) rows.push(`需要确认：${props.msg.reason}`);
  if (props.msg.context_ref) rows.push(`上下文：${props.msg.context_ref}`);
  if (props.msg.input_hint && hasOptions.value)
    rows.push(`输入提示：${props.msg.input_hint}`);
  return rows;
});
</script>

<style scoped>
.msg-icon {
  flex-shrink: 0;
  font-size: 1.2rem;
  margin-top: 2px;
}

.msg-body {
  flex: 1;
  min-width: 0;
}

.msg-label {
  font-size: 0.75rem;
  margin-bottom: 4px;
}

.msg-content {
  color: var(--text-main);
  font-size: 0.85rem;
  line-height: 1.5;
}

.ask-user-label {
  color: var(--accent-orange);
  font-weight: 600;
}

.ask-user-question {
  margin-top: 4px;
}

.ask-user-meta {
  margin-top: 10px;
  color: var(--text-muted);
  font-size: 0.82rem;
}

.ask-user-meta-row + .ask-user-meta-row {
  margin-top: 4px;
}

.ask-authorization {
  margin-top: 12px;
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  color: var(--text-sub);
  font-size: 0.8rem;
  padding: 9px 11px;
}

.ask-custom-hint {
  color: var(--text-muted);
  font-size: 0.82rem;
  margin-top: 10px;
}
</style>
