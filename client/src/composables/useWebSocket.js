import { ref } from "vue";
import { useAgentStore } from "../stores/agent.js";
import { useDataSourceStore } from "../stores/datasource.js";

const connected = ref(false);
let eventSourceInstance = null;
let connectPromise = null;
let reconnectTimer = null;

function clearReconnectTimer() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
}

function handleEvent(event, store, dataSourceStore) {
  if (!event || !event.type) return;

  switch (event.type) {
    case "connected":
    case "session_ready":
      if (event.data?.session_id) {
        store.setSession(event.data.session_id);
      }
      break;

    case "run_start":
    case "run_status":
      if (event.data?.run_id) {
        store.setSelectedRun(event.data.run_id);
        if (event.data.status === "running") {
          store.startRun(event.data.run_id);
        } else if (event.data.status === "waiting_user_input") {
          store.startRun(event.data.run_id);
          store.setRunning(false);
        } else if (event.data.status === "finished" || event.data.status === "failed") {
          store.finishRun();
        }
      }
      break;

    case "subgoal_update":
    case "subgoal_tree_update":
      if (event.data?.subgoals) {
        store.setSubgoals(event.data.subgoals);
      }
      break;

    case "message":
    case "run_message":
      if (event.data) {
        const msg = event.data;
        store.addMessage({
          id: msg.id,
          type: msg.type || "assistant",
          name: msg.name,
          content: msg.content,
          arguments: msg.arguments,
          tool_call_id: msg.tool_call_id,
          duration: msg.duration,
          success: msg.success,
          timestamp: new Date().toLocaleTimeString(),
        });
      }
      break;

    case "report_update":
    case "report_final":
      if (event.data?.html) {
        store.updateReport(event.data.html);
      }
      break;

    case "error":
      if (event.data?.message) {
        store.addMessage({
          type: "error",
          content: event.data.message,
        });
      }
      store.finishRun();
      break;
  }
}

export function useWebSocket() {
  const store = useAgentStore();
  const dataSourceStore = useDataSourceStore();

  function authHeaders() {
    return store.token ? { Authorization: `Bearer ${store.token}` } : {};
  }

  function connect() {
    if (!store.token) return Promise.reject(new Error("未登录"));
    if (eventSourceInstance && eventSourceInstance.readyState === EventSource.OPEN) {
      return Promise.resolve(eventSourceInstance);
    }
    if (connectPromise) return connectPromise;

    clearReconnectTimer();

    const params = new URLSearchParams();
    if (store.token) params.set("token", store.token);
    if (store.sessionId) params.set("session_id", store.sessionId);
    if (store.workspace?.id) params.set("workspace_id", store.workspace.id);
    const sessionQuery = params.toString() ? `?${params.toString()}` : "";
    const url = `/api/sse${sessionQuery}`;

    store.setConnectionState("connecting");
    connected.value = false;

    const pending = new Promise((resolve) => {
      const es = new EventSource(url);
      eventSourceInstance = es;

      es.onopen = () => {
        connected.value = true;
        store.setConnectionState("connected");
        resolve(es);
      };

      es.onmessage = (e) => {
        try {
          const data = JSON.parse(e.data);
          handleEvent(data, store, dataSourceStore);
        } catch (err) {}
      };

      es.onerror = () => {
        connected.value = false;
        store.setConnectionState("disconnected");
      };
    });

    connectPromise = pending;
    return pending;
  }

  function disconnect() {
    clearReconnectTimer();
    if (eventSourceInstance) {
      eventSourceInstance.close();
      eventSourceInstance = null;
    }
    connectPromise = null;
    connected.value = false;
    store.setConnectionState("disconnected");
  }

  async function initializeApp() {
    await bootstrap();
    await connect();
  }

  async function bootstrap() {
    if (!store.token) throw new Error("未登录");
    const res = await fetch("/api/bootstrap", {
      headers: authHeaders(),
      credentials: "include",
    });
    if (!res.ok) {
      if (res.status === 401) {
        disconnect();
        store.logout();
      }
      throw new Error("bootstrap 失败");
    }
    const data = await res.json();
    store.setIdentity(data.user, data.workspace);
    store.setWorkspaces(data.workspaces || []);
    if (data.session?.id) {
      store.setSession(data.session.id);
      dataSourceStore.fetchSessionSources(data.session.id);
      dataSourceStore.fetchWorkspaceDataSources();
    }
  }

  async function login(email, password, workspaceId = "") {
    const res = await fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ email, password, workspaceId }),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    store.setToken(data.token);
    store.setIdentity(data.user, data.workspace);
    store.setWorkspaces(data.workspaces || []);
    store.resetAnalysis();
    store.setSessions([]);
    store.setBootstrapState("idle");
  }

  async function register(name, email, password, workspaceName = "") {
    const res = await fetch("/api/auth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ name, email, password, workspaceName }),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    store.setToken(data.token);
    store.setIdentity(data.user, data.workspace);
    store.setWorkspaces(data.workspaces || [data.workspace]);
    store.resetAnalysis();
    store.setSessions([]);
    store.setBootstrapState("idle");
  }

  async function switchWorkspace(workspaceId) {
    const res = await fetch("/api/auth/switch-workspace", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      credentials: "include",
      body: JSON.stringify({ workspaceId }),
    });
    if (!res.ok) throw new Error(await res.text());
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
    const res = await fetch("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      credentials: "include",
      body: JSON.stringify({ title: options.title || "" }),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    store.setSession(data.id);
    return data;
  }

  async function ensureSession() {
    if (store.sessionId) return store.sessionId;
    const session = await createSession();
    return session.id;
  }

  async function sendMessage(content, options = {}) {
    const value = String(content || "").trim();
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
        const res = await fetch(`/api/runs/${waitingRunId}/input`, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders() },
          body: JSON.stringify({ response: value }),
        });
        if (!res.ok) throw new Error(await res.text());
      } else {
        const res = await fetch(`/api/sessions/${sessionId}/chat`, {
          method: "POST",
          headers: { "Content-Type": "application/json", ...authHeaders() },
          body: JSON.stringify({
            content: value,
            turnContext: options.turnContext,
            editContext: options.editContext,
          }),
        });
        if (!res.ok) throw new Error(await res.text());
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
      await fetch(`/api/runs/${store.activeRunId}/cancel`, {
        method: "POST",
        headers: authHeaders(),
      });
    } catch (err) {}
  }

  async function loadSessions() {
    const res = await fetch("/api/sessions", {
      headers: authHeaders(),
    });
    if (res.ok) {
      const data = await res.json();
      store.setSessions(data.sessions || []);
    }
  }

  async function openSession(sessionId) {
    const res = await fetch(`/api/sessions/${sessionId}`, {
      headers: authHeaders(),
    });
    if (res.ok) {
      const data = await res.json();
      store.setSession(data.session?.id || sessionId);
      store.setRuns(data.runs || []);
      dataSourceStore.fetchSessionSources(sessionId);
    }
  }

  async function renameSession(sessionId, title) {
    const res = await fetch(`/api/sessions/${sessionId}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ title }),
    });
    if (res.ok) {
      await loadSessions();
    }
  }

  async function deleteSession(sessionId) {
    const res = await fetch(`/api/sessions/${sessionId}`, {
      method: "DELETE",
      headers: authHeaders(),
    });
    if (res.ok) {
      await loadSessions();
      if (store.sessionId === sessionId) {
        store.resetAnalysis();
      }
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
    disconnect,
    sendMessage,
    stop,
    createNewSession,
    ensureSession,
    renameSession,
    deleteSession,
  };
}
