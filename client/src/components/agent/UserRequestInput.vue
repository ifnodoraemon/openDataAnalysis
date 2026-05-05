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

    <div
      v-if="hasOptions"
      class="ask-options"
      :class="{ 'multi-select': isMultiple }"
    >
      <button
        v-for="option in normalizedOptions"
        :key="option.id"
        type="button"
        class="ask-option-btn"
        :class="{ selected: selectedIds.includes(option.id) }"
        @click="toggleOption(option.id)"
      >
        <span>{{ option.label }}</span>
        <small v-if="option.hint">{{ option.hint }}</small>
      </button>
    </div>

    <label v-if="allowCustom" class="ask-custom">
      <span>{{ customLabel }}</span>
      <textarea
        v-model="customText"
        class="ask-custom-input"
        :placeholder="customPlaceholder"
        rows="2"
      ></textarea>
    </label>

    <div class="ask-actions">
      <button
        type="button"
        class="ask-submit-btn"
        :disabled="!canSubmit"
        @click="submit"
      >
        提交回复
      </button>
      <span v-if="submitHint" class="ask-submit-hint">{{ submitHint }}</span>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from "vue";
import { useWebSocket } from "../../composables/useWebSocket.js";

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

const { sendMessage } = useWebSocket();
const selectedIds = ref([]);
const customText = ref("");
const submitted = ref(false);

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
const allowCustom = computed(() => props.msg.allow_custom !== false);
const customValue = computed(() => customText.value.trim());
const isMultiple = computed(() => props.msg.selection_mode === "multiple");
const label = computed(() => {
  if (!hasOptions.value) return "等待您描述";
  return isMultiple.value ? "请选择一个或多个选项" : "请选择一个选项";
});
const customLabel = computed(() =>
  hasOptions.value ? "自定义或补充说明" : "您的回复",
);
const customPlaceholder = computed(() => {
  if (props.msg.input_hint) return props.msg.input_hint;
  return hasOptions.value ? "可以补充约束或说明..." : "请直接描述你的回复...";
});
const metaRows = computed(() => {
  const rows = [];
  if (props.msg.reason) rows.push(`需要确认：${props.msg.reason}`);
  if (props.msg.context_ref) rows.push(`上下文：${props.msg.context_ref}`);
  if (props.msg.input_hint && hasOptions.value)
    rows.push(`输入提示：${props.msg.input_hint}`);
  return rows;
});
const canSubmit = computed(
  () =>
    !submitted.value &&
    (selectedIds.value.length > 0 || customValue.value.length > 0),
);
const submitHint = computed(() => {
  if (canSubmit.value) return "";
  if (hasOptions.value && allowCustom.value)
    return "选择一个选项，或填写自定义说明。";
  if (hasOptions.value) return "请选择一个选项。";
  if (!allowCustom.value) return "请求没有可用的回复方式。";
  return "请填写回复内容。";
});

function toggleOption(id) {
  if (isMultiple.value) {
    selectedIds.value = selectedIds.value.includes(id)
      ? selectedIds.value.filter((item) => item !== id)
      : [...selectedIds.value, id];
    return;
  }
  selectedIds.value = selectedIds.value[0] === id ? [] : [id];
}

function selectedOptions() {
  const byID = new Map(
    normalizedOptions.value.map((option) => [option.id, option]),
  );
  return selectedIds.value
    .map((id) => byID.get(id))
    .filter(Boolean)
    .map((option) => ({
      id: option.id,
      label: option.label,
    }));
}

function buildDisplayText(options, custom) {
  const parts = [];
  if (options.length > 0) {
    const labels = options.map((option) => option.label).join("、");
    parts.push(`选择：${labels}`);
  }
  if (custom) parts.push(`说明：${custom}`);
  return parts.join("\n");
}

async function submit() {
  if (!canSubmit.value) return;
  submitted.value = true;
  const options = selectedOptions();
  const custom = customValue.value;
  const payload = {
    response_type:
      options.length > 0 && custom
        ? "selection_with_custom"
        : options.length > 0
          ? "selection"
          : "custom",
    question: props.msg.question || "",
    selected_option_ids: options.map((option) => option.id),
    selected_options: options,
    custom_response: custom,
  };
  const sent = await sendMessage(buildDisplayText(options, custom), {
    payloadContent: JSON.stringify(payload),
  });
  if (!sent) submitted.value = false;
}
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
  color: var(--text-primary);
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

.ask-options {
  display: grid;
  gap: 8px;
  margin-top: 12px;
}

.ask-option-btn {
  display: flex;
  flex-direction: column;
  gap: 3px;
  align-items: flex-start;
  width: 100%;
  background: var(--bg-primary);
  border: 1px solid var(--border);
  border-radius: 10px;
  color: var(--text-primary);
  cursor: pointer;
  padding: 9px 11px;
  text-align: left;
  transition: all 0.16s ease;
}

.ask-option-btn:hover {
  border-color: var(--accent-blue);
  background: var(--bg-hover);
}

.ask-option-btn.selected {
  border-color: var(--accent-blue);
  box-shadow: inset 3px 0 0 var(--accent-blue);
}

.ask-option-btn small {
  color: var(--text-muted);
  font-size: 0.74rem;
}

.ask-custom {
  display: block;
  margin-top: 12px;
  color: var(--text-muted);
  font-size: 0.82rem;
}

.ask-custom-input {
  width: 100%;
  margin-top: 6px;
  resize: vertical;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg-secondary);
  color: var(--text-primary);
  padding: 9px 10px;
  font-family: inherit;
}

.ask-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}

.ask-submit-btn {
  border: none;
  border-radius: 999px;
  background: var(--accent-blue);
  color: white;
  cursor: pointer;
  font-size: 0.82rem;
  font-weight: 600;
  padding: 7px 15px;
}

.ask-submit-btn:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}

.ask-submit-hint {
  color: var(--text-muted);
  font-size: 0.78rem;
}
</style>
