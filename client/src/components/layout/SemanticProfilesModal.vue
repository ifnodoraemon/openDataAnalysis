<template>
  <div v-if="open" class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-card">
      <div class="modal-header">
        <h2>🧠 语义模型与工作区知识库</h2>
        <button class="close-btn" @click="$emit('close')">✕</button>
      </div>

      <div class="modal-body">
        <p class="description">
          Open Data Analysis Agent 在分析过程中会自动理解并沉淀工作区的列语义、业务指标口径与数据签名。
        </p>

        <div class="knowledge-cards">
          <div class="k-card">
            <div class="k-header">
              <span class="icon">📊</span>
              <span class="title">自动推导语义 Profile</span>
            </div>
            <p class="text">
              自动探测数值分布、时间粒度、主外键关联与空值占比，为 LLM 生成精确定制 SQL / Python 描述。
            </p>
          </div>

          <div class="k-card">
            <div class="k-header">
              <span class="icon">✅</span>
              <span class="title">指标与口径确认机制</span>
            </div>
            <p class="text">
              在遇到歧义指标或重名维度时，智能体会发起主动确认询问（Confirmation），确认后的计算公式将沉淀为企业全局知识。
            </p>
          </div>
        </div>
      </div>

      <div class="modal-footer">
        <button class="btn-primary" @click="$emit('close')">知道了</button>
      </div>
    </div>
  </div>
</template>

<script setup>
defineProps({
  open: { type: Boolean, default: false },
});
defineEmits(["close"]);
</script>

<style scoped>
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  backdrop-filter: blur(4px);
  z-index: 1000;
  display: grid;
  place-items: center;
  padding: 16px;
}

.modal-card {
  width: min(540px, 100%);
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 16px;
  padding: 24px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.modal-header h2 {
  font-size: 1.2rem;
  font-weight: 600;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  gap: 8px;
}

.close-btn {
  background: transparent;
  border: none;
  font-size: 1.2rem;
  color: var(--text-muted);
  cursor: pointer;
}

.description {
  font-size: 0.88rem;
  color: var(--text-secondary);
  line-height: 1.5;
}

.knowledge-cards {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 6px;
}

.k-card {
  padding: 14px;
  background: var(--bg-primary);
  border: 1px solid var(--border);
  border-radius: 12px;
}

.k-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.92rem;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 6px;
}

.k-card .text {
  font-size: 0.82rem;
  color: var(--text-muted);
  line-height: 1.5;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
}

.btn-primary {
  padding: 8px 18px;
  background: var(--accent-blue);
  color: white;
  border: none;
  border-radius: 8px;
  font-weight: 600;
  cursor: pointer;
}
</style>
