<template>
  <div class="login-shell">
    <div class="login-card">
      <div class="brand">📊 OpenDataAnalysis</div>
      <h1>{{ isRegister ? "注册新账号" : "欢迎登录" }}</h1>
      <p class="hint">
        {{
          isRegister
            ? "创建专属账号并自动生成独立工作区"
            : "输入账号进入智能数据分析系统"
        }}
      </p>

      <form class="form" @submit.prevent="handleSubmit">
        <label v-if="isRegister" class="field">
          <span>姓名 / 昵称</span>
          <input
            v-model.trim="name"
            type="text"
            placeholder="例如: 张三"
            required
          />
        </label>

        <label class="field">
          <span>邮箱账号</span>
          <input
            v-model.trim="email"
            type="email"
            autocomplete="username"
            placeholder="name@example.com"
            required
          />
        </label>

        <label class="field">
          <span>密码</span>
          <input
            v-model="password"
            type="password"
            :autocomplete="isRegister ? 'new-password' : 'current-password'"
            :placeholder="isRegister ? '至少8位包含字母与数字' : '请输入密码'"
            required
          />
        </label>

        <label v-if="isRegister" class="field">
          <span>工作区名称（选填）</span>
          <input
            v-model.trim="workspaceName"
            type="text"
            placeholder="默认: 个人分析工作区"
          />
        </label>

        <button class="submit" :disabled="loading">
          {{
            loading
              ? isRegister
                ? "注册中..."
                : "登录中..."
              : isRegister
                ? "立即注册"
                : "登录"
          }}
        </button>
      </form>

      <div class="toggle-mode">
        <span>{{ isRegister ? "已有账号？" : "还没有账号？" }}</span>
        <button type="button" class="toggle-btn" @click="toggleMode">
          {{ isRegister ? "返回登录" : "免费注册" }}
        </button>
      </div>

      <p v-if="error" class="error">{{ error }}</p>
    </div>
  </div>
</template>

<script setup>
import { ref } from "vue";
import { useAgentTransport } from "../../composables/useAgentTransport.js";

const emit = defineEmits(["success"]);
const { login, register } = useAgentTransport();

const isRegister = ref(false);
const name = ref("");
const email = ref("");
const password = ref("");
const workspaceName = ref("");
const loading = ref(false);
const error = ref("");

function toggleMode() {
  isRegister.value = !isRegister.value;
  error.value = "";
}

async function handleSubmit() {
  if (loading.value) return;
  loading.value = true;
  error.value = "";

  try {
    if (isRegister.value) {
      await register(
        name.value,
        email.value,
        password.value,
        workspaceName.value,
      );
    } else {
      await login(email.value, password.value);
    }
    emit("success");
  } catch (err) {
    error.value = err.message || (isRegister.value ? "注册失败" : "登录失败");
  } finally {
    loading.value = false;
  }
}
</script>

<style scoped>
.login-shell {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 24px;
  background: var(--bg-card);
}

.login-card {
  width: min(420px, 100%);
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 18px;
  padding: 28px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.08);
}

.brand {
  display: inline-block;
  padding: 6px 12px;
  border-radius: 999px;
  background: rgba(37, 99, 235, 0.1);
  color: var(--primary-blue);
  font-size: 0.8rem;
  font-weight: 600;
  margin-bottom: 14px;
}

h1 {
  font-size: 1.6rem;
  margin-bottom: 6px;
  color: var(--text-main);
}

.hint {
  color: var(--text-sub);
  font-size: 0.88rem;
  margin-bottom: 20px;
}

.form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  color: var(--text-sub);
  font-size: 0.8rem;
}

.field input {
  border: 1px solid var(--border-subtle);
  background: var(--bg-app);
  color: var(--text-main);
  border-radius: 10px;
  padding: 12px 14px;
  outline: none;
  font-size: 0.9rem;
  transition: border-color 0.2s;
}

.field input:focus {
  border-color: var(--primary-blue);
}

.submit {
  margin-top: 8px;
  border: none;
  border-radius: 10px;
  background: var(--primary-blue);
  color: white;
  padding: 12px 16px;
  font-size: 0.95rem;
  font-weight: 600;
  cursor: pointer;
  transition: opacity 0.2s;
}

.submit:hover:not(:disabled) {
  opacity: 0.9;
}

.submit:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.toggle-mode {
  margin-top: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-size: 0.85rem;
  color: var(--text-sub);
}

.toggle-btn {
  background: transparent;
  border: none;
  color: var(--primary-blue);
  font-weight: 600;
  cursor: pointer;
  padding: 0;
}

.toggle-btn:hover {
  text-decoration: underline;
}

.error {
  margin-top: 14px;
  color: #ff7b72;
  font-size: 0.82rem;
  text-align: center;
}
</style>
