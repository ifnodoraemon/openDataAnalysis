<template>
  <div v-if="open" class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-card">
      <div class="modal-header">
        <h2>🧠 数据事实与用户补丁</h2>
        <button class="close-btn" @click="$emit('close')">✕</button>
      </div>

      <div class="modal-body">
        <p class="description">
          这里展示运行时采集的结构事实，以及用户明确授权的可复用补丁。运行时不会依据字段名或预设场景补写含义。
        </p>

        <div class="knowledge-cards">
          <div class="k-card">
            <div class="k-header">
              <span class="icon">📊</span>
              <span class="title">结构与测量事实</span>
            </div>
            <p class="text">
              展示列类型、空值率、去重计数、采样方式和值格式覆盖。它们是观测结果，不是字段含义或分析结论。
            </p>
          </div>

          <div class="k-card">
            <div class="k-header">
              <span class="icon">✅</span>
              <span class="title">用户确认与可复用补丁</span>
            </div>
            <p class="text">
              模型可在缺少必要定义时请求澄清。只有经认证用户批准的精确变更才会写入；工作区补丁按数据结构签名复用，运行时不猜测其业务含义。
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
  border: 1px solid var(--border-subtle);
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
  color: var(--text-main);
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
  color: var(--text-sub);
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
  background: var(--bg-app);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
}

.k-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.92rem;
  font-weight: 600;
  color: var(--text-main);
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
  background: var(--primary-blue);
  color: white;
  border: none;
  border-radius: 8px;
  font-weight: 600;
  cursor: pointer;
}
</style>
