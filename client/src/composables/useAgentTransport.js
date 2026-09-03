import { ref, watch } from "vue";
import { useAgentStore } from "../stores/agent.js";
import { useDataSourceStore } from "../stores/datasource.js";
import {
  applyRuntimeState,
  deserializeRunMessages,
  handleEvent,
} from "./runtimeEventDispatch.js";

const RECONNECT_BASE_DELAY_MS = 1000;
const RECONNECT_MAX_DELAY_MS = 30000;
const AUTH_EXEMPT_PATHS = new Set(["/api/auth/login", "/api/auth/register"]);

const connected = ref(false);
let eventSourceInstance = null;
let eventSourceSessionId = "";
let connectPromise = null;
let reconnectTimer = null;
let reconnectDelay = RECONNECT_BASE_DELAY_MS;
// Last SSE event id received for the current session; carried into manual
// reconnects as last_event_id so the server replays anything missed.
let lastEventId = "";
let watchedSessionStore = null;

export function useAgentTransport() {
  const store = useAgentStore();
  const dataSourceStore = useDataSourceStore();

  bindSessionWatcher(store);

  function authHeaders() {
    return store.token ? { Authorization: `Bearer ${store.token}` } : {};
  }

  async function request(url, options = {}) {
    const res = await fetch(url, { credentials: "include", ...options });
    if (!res.ok) {
      if (res.status === 401 && !AUTH_EXEMPT_PATHS.has(url)) {
        disconnect();
        store.logout();
      }
      throw new Error(await res.text());
    }
    return res;
  }

  function closeEventSource() {
    if (eventSourceInstance) {
      eventSourceInstance.close();
      eventSourceInstance = null;
    }
    eventSourceSessionId = "";
    connectPromise = null;
  }

  function scheduleReconnect() {
    if (reconnectTimer) return;
    const delay = reconnectDelay;
    reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_DELAY_MS);
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (!store.token || !store.sessionId) {
        store.setConnectionState("disconnected");
        return;
      }
      connect();
    }, delay);
  }

  async function verifyAuthAndReconnect() {
    let unauthorized = false;
    try {
      const res = await fetch("/api/bootstrap", {
        headers: authHeaders(),
        credentials: "include",
      });
      unauthorized = res.status === 401;
    } catch {
      unauthorized = false;
    }
    if (unauthorized) {
      disconnect();
      store.logout();
      return;
    }
    scheduleReconnect();
  }

  function connect() {
    if (!store.token) return Promise.reject(new Error("未登录"));
    if (!store.sessionId) return Promise.resolve(null);
    if (
      eventSourceInstance &&
      eventSourceSessionId === store.sessionId &&
      eventSourceInstance.readyState === EventSource.OPEN
    ) {
      return Promise.resolve(eventSourceInstance);
    }
    // Dedupe in-flight connects for the same session before tearing anything
    // down; closing the source first would leave the pending promise dangling.
    if (connectPromise && eventSourceSessionId === store.sessionId) {
      return connectPromise;
    }
    if (eventSourceInstance) closeEventSource();

    store.setConnectionState("connecting");
    connected.value = false;

    let url = `/api/sse?session_id=${encodeURIComponent(store.sessionId)}`;
    if (lastEventId) {
      url += `&last_event_id=${encodeURIComponent(lastEventId)}`;
    }
    const pending = new Promise((resolve, reject) => {
      const es = new EventSource(url);
      eventSourceInstance = es;
      eventSourceSessionId = store.sessionId;
      let opened = false;

      es.onopen = () => {
        opened = true;
        reconnectDelay = RECONNECT_BASE_DELAY_MS;
        connected.value = true;
        store.setConnectionState("connected");
        if (lastEventId) {
          // This open is a reconnect: replayed events already restore most
          // state, but run status persisted server-side is the source of
          // truth for isRunning.
          resyncSessionState().catch((err) => {
            console.error("重连后状态同步失败", err);
          });
        }
        resolve(es);
      };

      es.onmessage = (e) => {
        if (e.lastEventId) lastEventId = e.lastEventId;
        try {
          const data = JSON.parse(e.data);
          if (
            !data ||
            typeof data !== "object" ||
            typeof data.type !== "string"
          )
            throw new Error("事件不符合运行事件协议");
          handleEvent(data, store);
        } catch (err) {
          console.error("事件流消息解析失败", err);
        }
      };

      es.onerror = () => {
        connected.value = false;
        if (!opened) {
          closeEventSource();
          store.setConnectionState("disconnected");
          reject(new Error("事件流连接失败"));
          return;
        }
        store.setConnectionState("reconnecting");
        if (es.readyState === EventSource.CLOSED) {
          closeEventSource();
          verifyAuthAndReconnect();
        }
      };
    });

    connectPromise = pending;
    pending.catch(() => {
      if (connectPromise === pending) connectPromise = null;
    });
    return pending;
  }

  async function resyncSessionState() {
    if (!store.sessionId) return;
    const res = await request(`/api/sessions/${store.sessionId}`, {
      headers: authHeaders(),
    });
    const data = await res.json();
    store.setRuns(data.runs || []);
    applyRuntimeState(data.runtimeState, store);
    const activeRun = store.getRun(store.activeRunId);
    if (activeRun && ["completed", "failed", "cancelled"].includes(activeRun.status)) {
      store.finishRun();
    }
  }

  function disconnect() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    reconnectDelay = RECONNECT_BASE_DELAY_MS;
    lastEventId = "";
    closeEventSource();
    connected.value = false;
    store.setConnectionState("disconnected");
  }

  function bindSessionWatcher(currentStore) {
    if (watchedSessionStore === currentStore) return;
    watchedSessionStore = currentStore;
    watch(
      () => [currentStore.token, currentStore.sessionId],
      ([nextToken, nextSessionId]) => {
        if (!nextToken) {
          // Token cleared (logout): the event stream must not stay open.
          disconnect();
          return;
        }
        if (!nextSessionId) {
          disconnect();
          return;
        }
        if (eventSourceSessionId === nextSessionId && eventSourceInstance)
          return;
        disconnect();
        connect();
      },
    );
  }

  async function initializeApp() {
    store.setBootstrapState("loading");
    try {
      await bootstrap();
      await connect();
      store.setBootstrapState("idle");
    } catch (error) {
      store.setBootstrapState(
        "error",
        "无法连接工作区，请检查服务状态后重试。",
      );
      throw error;
    }
  }

  async function bootstrap() {
    if (!store.token) throw new Error("未登录");
    const res = await request("/api/bootstrap", {
      headers: authHeaders(),
    });
    const data = await res.json();
    store.setIdentity(data.user, data.workspace);
    store.setWorkspaces(data.workspaces);
    if (data.session?.id) {
      store.setSession(data.session.id);
      await Promise.allSettled([
        dataSourceStore.fetchSessionSources(data.session.id),
        dataSourceStore.fetchWorkspaceDataSources(),
      ]);
      // Restore the latest conversation so a page reload shows history.
      if (data.runs?.length) {
        store.setRuns(data.runs || []);
        await restoreLatestRunMessages(data.runs);
      }
    }
  }

  async function login(email, password, workspaceId = "") {
    const res = await request("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password, workspaceId }),
    });
    const data = await res.json();
    store.setToken(data.token);
    store.setIdentity(data.user, data.workspace);
    store.setWorkspaces(data.workspaces);
    store.resetAnalysis();
    store.setSessions([]);
    store.setBootstrapState("idle");
  }

  async function register(name, email, password, workspaceName) {
    const res = await request("/api/auth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, email, password, workspaceName }),
    });
    const data = await res.json();
    store.setToken(data.token);
    store.setIdentity(data.user, data.workspace);
    store.setWorkspaces(data.workspaces);
    store.resetAnalysis();
    store.setSessions([]);
    store.setBootstrapState("idle");
  }

  async function switchWorkspace(workspaceId) {
    const res = await request("/api/auth/switch-workspace", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ workspaceId }),
    });
    const data = await res.json();
    disconnect();
    store.setToken(data.token);
    store.setWorkspace(data.workspace);
    store.resetAnalysis();
    store.setSessions([]);
    store.setBootstrapState("idle");
    await initializeApp();
  }

  async function createSession(options = {}) {
    const res = await request("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ title: options.title || "" }),
    });
    const data = await res.json();
    if (!data.session?.id) throw new Error("创建会话响应缺少会话 ID");
    store.setSession(data.session.id);
    return data.session;
  }

  async function ensureSession() {
    if (store.sessionId) return store.sessionId;
    const session = await createSession();
    return session.id;
  }

  async function sendMessage(content, options = {}) {
    const value = String(content || "").trim();
    const wireValue = String(options.payloadContent || value).trim();
    if (!value) return false;

    const sessionId = await ensureSession();
    const waitingRunId = store.activeRunId;
    const waitingRun = waitingRunId ? store.getRun(waitingRunId) : null;
    const isAnsweringUserRequest = waitingRun?.status === "waiting_user_input";

    store.setRunning(true);
    store.addMessage({
      type: "user",
      content: value,
      editContext: isAnsweringUserRequest ? null : options.editContext || null,
      turnContext: isAnsweringUserRequest ? null : options.turnContext || null,
    });

    try {
      if (isAnsweringUserRequest && waitingRunId) {
        await request(`/api/runs/${waitingRunId}/input`, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders() },
          body: JSON.stringify({ response: wireValue }),
        });
        // The resumed run does not re-emit run_started, so patch the local
        // status here — otherwise the waiting-input option chips stay
        // rendered for the whole resumed execution.
        store.patchRun(waitingRunId, {
          status: "running",
          updatedAt: new Date().toISOString(),
        });
      } else {
        const res = await request(`/api/sessions/${sessionId}/chat`, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders() },
          body: JSON.stringify({
            content: value,
            turnContext: options.turnContext,
            editContext: options.editContext,
          }),
        });
        const data = await res.json();
        if (!data.run_id) throw new Error("启动任务响应缺少任务 ID");
        store.startRun(data.run_id);
        store.upsertRun({
          id: data.run_id,
          sessionId,
          status: "running",
          inputMessage: value,
          createdAt: new Date().toISOString(),
        });
      }
      return true;
    } catch (err) {
      store.finishRun();
      store.addMessage({
        type: "error",
        content: err instanceof Error ? err.message : "消息发送失败",
      });
      return false;
    }
  }

  async function stop() {
    if (!store.activeRunId) return;
    try {
      await request(`/api/runs/${store.activeRunId}/cancel`, {
        method: "POST",
        headers: authHeaders(),
      });
    } catch (err) {
      store.addMessage({
        type: "error",
        content: "停止任务失败",
      });
      console.error("停止任务失败", err);
    }
  }

  async function loadSessions() {
    const res = await request("/api/sessions", {
      headers: authHeaders(),
    });
    const data = await res.json();
    store.setSessions(data.sessions || []);
  }

  async function openSession(sessionId) {
    const res = await request(`/api/sessions/${sessionId}`, {
      headers: authHeaders(),
    });
    const data = await res.json();
    if (data.session?.id !== sessionId) throw new Error("会话响应与请求不一致");
    const switchingSessions = store.sessionId !== sessionId;
    store.setSession(data.session.id);
    if (switchingSessions) {
      // Switching sessions must not keep the previous session's chat on
      // screen; the latest run's messages load right below.
      store.setMessages([]);
      store.finishRun();
    }
    store.setRuns(data.runs || []);
    applyRuntimeState(data.runtimeState, store);
    await dataSourceStore.fetchSessionSources(sessionId);
    await restoreLatestRunMessages(data.runs || []);
  }

  function latestRunOf(runs) {
    if (!runs.length) return null;
    return [...runs].sort(
      (a, b) => new Date(b.createdAt || 0) - new Date(a.createdAt || 0),
    )[0];
  }

  // Opening a session or reloading the page should show the most recent
  // conversation instead of an empty chat; earlier runs stay reachable via
  // the run tree.
  async function restoreLatestRunMessages(runs) {
    const latest = latestRunOf(runs);
    if (!latest) return;
    try {
      await openRun(latest.id);
    } catch (err) {
      console.error("加载最近对话失败：", err);
    }
  }

  async function openRun(runId) {
    const res = await request(`/api/runs/${runId}`, {
      headers: authHeaders(),
    });
    const data = await res.json();
    if (data.run?.id !== runId) throw new Error("任务响应与请求不一致");
    store.setSelectedRun(runId);
    store.setMessages(deserializeRunMessages(data.messages));
    applyRuntimeState(data.runtimeState, store);
    return data.run;
  }

  async function logout() {
    // Revoke the bearer token and session cookie server-side before clearing
    // local state; a purely client-side logout would leave tokens valid.
    try {
      await request("/api/auth/logout", {
        method: "POST",
        headers: authHeaders(),
      });
    } catch (err) {
      console.error("服务端登出失败", err);
    }
    disconnect();
    store.logout();
  }

  async function renameSession(sessionId, title) {
    await request(`/api/sessions/${sessionId}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ title }),
    });
    await loadSessions();
  }

  async function deleteSession(sessionId) {
    await request(`/api/sessions/${sessionId}`, {
      method: "DELETE",
      headers: authHeaders(),
    });
    await loadSessions();
    if (store.sessionId === sessionId) {
      disconnect();
      store.resetAnalysis();
    }
  }

  async function createNewSession() {
    disconnect();
    store.resetAnalysis();
    const session = await createSession();
    await connect();
    return session;
  }

  return {
    connected,
    bootstrap,
    initializeApp,
    connect,
    login,
    register,
    switchWorkspace,
    loadSessions,
    openSession,
    openRun,
    disconnect,
    logout,
    sendMessage,
    stop,
    createNewSession,
    ensureSession,
    renameSession,
    deleteSession,
  };
}
