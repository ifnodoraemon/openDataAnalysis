<template>
  <div v-if="open" class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-card">
      <div class="modal-header">
        <h2>⚙️ 工作区与团队配置</h2>
        <button class="close-btn" @click="$emit('close')">✕</button>
      </div>

      <div class="modal-body">
        <div class="info-section">
          <h3>当前工作区</h3>
          <div class="info-grid">
            <div class="info-item">
              <span class="label">工作区名称</span>
              <span class="value font-semibold">{{ workspace?.name || "未设置" }}</span>
            </div>
            <div class="info-item">
              <span class="label">工作区 ID</span>
              <span class="value code">{{ workspace?.id || "-" }}</span>
            </div>
            <div class="info-item">
              <span class="label">团队角色 (RBAC)</span>
              <span class="value badge-role">{{ userRole }}</span>
            </div>
            <div class="info-item">
              <span class="label">API 限流配额</span>
              <span class="value">120 次/分钟</span>
            </div>
          </div>
        </div>

        <div class="members-section">
          <h3>成员列表与权限</h3>
          <div class="member-list">
            <div class="member-item">
              <div class="member-avatar">{{ userInitial }}</div>
              <div class="member-info">
                <span class="name">{{ user?.name || user?.email }}</span>
                <span class="email">{{ user?.email }}</span>
              </div>
              <span class="role-tag owner">Owner</span>
            </div>
          </div>
        </div>
      </div>

      <div class="modal-footer">
        <button class="btn-primary" @click="$emit('close')">关闭</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from "vue";
import { useAgentStore } from "../../stores/agent";

defineProps({
  open: { type: Boolean, default: false },
});
defineEmits(["close"]);

const store = useAgentStore();
const workspace = computed(() => store.workspace);
const user = computed(() => store.user);

const userInitial = computed(() => {
  const name = user.value?.name || user.value?.email || "U";
  return name.charAt(0).toUpperCase();
});

const userRole = computed(() => "Owner (工作区所有者)");
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
  width: min(520px, 100%);
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 16px;
  padding: 24px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  gap: 20px;
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

.modal-body {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.info-section h3,
.members-section h3 {
  font-size: 0.85rem;
  color: var(--text-muted);
  margin-bottom: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.info-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  background: var(--bg-primary);
  padding: 16px;
  border-radius: 12px;
  border: 1px solid var(--border);
}

.info-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.info-item .label {
  font-size: 0.75rem;
  color: var(--text-muted);
}

.info-item .value {
  font-size: 0.88rem;
  color: var(--text-primary);
}

.code {
  font-family: monospace;
  font-size: 0.8rem !important;
}

.badge-role {
  color: var(--accent-blue) !important;
  font-weight: 600;
}

.member-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.member-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  background: var(--bg-primary);
  border-radius: 10px;
  border: 1px solid var(--border);
}

.member-avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: linear-gradient(135deg, var(--accent-blue), var(--accent-purple));
  color: white;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 600;
  font-size: 0.85rem;
}

.member-info {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.member-info .name {
  font-size: 0.85rem;
  font-weight: 500;
  color: var(--text-primary);
}

.member-info .email {
  font-size: 0.75rem;
  color: var(--text-muted);
}

.role-tag {
  font-size: 0.75rem;
  padding: 2px 8px;
  border-radius: 999px;
  font-weight: 600;
}

.role-tag.owner {
  background: rgba(37, 99, 235, 0.12);
  color: var(--accent-blue);
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
