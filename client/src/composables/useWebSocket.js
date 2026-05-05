import { ref } from "vue";
import { useAgentStore } from "../stores/agent";
import { useDataSourceStore } from "../stores/datasource";

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;
const RECONNECT_MAX_ATTEMPTS = 20;
const PENDING_QUEUE_MAX = 100;

let wsInstance = null;
let reconnectTimer = null;
let connectPromise = null;
let bootstrapPromise = null;
let reconnectEnabled = false;
let reconnectAttempts = 0;
const pendingMessages = [];
const connected = ref(false);

function enqueueMessage(payload) {
  if (pendingMessages.length >= PENDING_QUEUE_MAX) {
    pendingMessages.shift();
  }
  pendingMessages.push(payload);
}

function flushPendingMessages(socket) {
  while (pendingMessages.length > 0) {
    const msg = pendingMessages.shift();
    try {
      socket.send(JSON.stringify(msg));
    } catch {
      pendingMessages.unshift(msg);
      break;
    }
  }
}

function getReconnectDelay() {
  const jitter = Math.random() * 500;
  const delay = Math.min(
    RECONNECT_BASE_MS * Math.pow(2, reconnectAttempts),
    RECONNECT_MAX_MS,
  );
  return delay + jitter;
}

export function useWebSocket() {
  const store = useAgentStore();
  const dataSourceStore = useDataSourceStore();

  function shouldApplyReportEvent(eventRunId) {
    if (!eventRunId) return true;
    if (!store.selectedRunId) return true;
    if (store.selectedRunId === eventRunId) return true;

    const selectedRun = store.getRun(store.selectedRunId);
    const eventRun = store.getRun(eventRunId);
    if (eventRun?.parentRunId === store.selectedRunId) return true;
    if (selectedRun?.parentRunId === eventRunId) return true;

    return false;
  }

  function shouldShowRunEvent(eventRunId) {
    return (
      !store.selectedRunId || !eventRunId || eventRunId === store.selectedRunId
    );
  }

  function appendRunPreview(event) {
    if (!event.runId) return;
    const summary = summarizeEventForPreview(event);
    if (!summary) return;
    store.appendRunPreview(event.runId, {
      type: event.type,
      name: event.data?.name,
      summary,
    });
  }

  function summarizeEventForPreview(event) {
    switch (event.type) {
      case "assistant_status":
        return clipPreviewText(event.data?.content);
      case "tool_call":
        return event.data?.name || "tool_call";
      case "tool_result": {
        const raw = event.data?.result || "";
        try {
          const parsed = JSON.parse(raw);
          return clipPreviewText(
            parsed.ui_summary ||
              parsed.message ||
              `${event.data?.name || "tool_result"}: ${raw}`,
          );
        } catch {
          return clipPreviewText(
            `${event.data?.name || "tool_result"}: ${raw}`,
          );
        }
      }
      case "run_completed":
        return clipPreviewText(event.data?.summary);
      case "run_cancelled":
        return clipPreviewText(event.data?.message || "任务已取消");
      case "error":
        return clipPreviewText(event.data?.message);
      case "user_request_input":
        return clipPreviewText(event.data?.question || "等待用户输入");
      default:
        return "";
    }
  }

  function clipPreviewText(input, max = 120) {
    const text = String(input || "")
      .trim()
      .replace(/\s+/g, " ");
    if (!text) return "";
    return text.length > max ? `${text.slice(0, max)}...` : text;
  }

  function authHeaders() {
    return store.token ? { Authorization: `Bearer ${store.token}` } : {};
  }

  function clearReconnectTimer() {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  async function loadRunReport(runId) {
    if (!runId) return;
    const res = await fetch(`/api/runs/${runId}/report`, {
      headers: authHeaders(),
    });
    if (!res.ok) {
      if (res.status !== 404) throw new Error(await res.text());
      return;
    }
    const html = await res.text();
    store.updateReport(html);
  }

  async function tryLoadRunReport(runId) {
    try {
      await loadRunReport(runId);
    } catch (err) {
      console.warn(`load run report failed for ${runId}:`, err);
    }
  }

  function applyRuntimeState(runtimeState) {
    store.setSubgoals(runtimeState?.subgoals || []);
    store.setMemoryFacts(runtimeState?.memory || {});
    store.updateReport(runtimeState?.report_html || "");
    store.setReportEditState(runtimeState?.edit_state || null);
  }

  function applySessionState(sessionId, runs, runtimeState = null) {
    store.resetAnalysis();
    store.setSession(sessionId || "");
    store.setRuns(runs || []);
    applyRuntimeState(runtimeState);
    if (sessionId) {
      dataSourceStore.fetchSessionSources(sessionId);
      dataSourceStore.fetchWorkspaceDataSources();
    }

    const latestRun = (runs || [])[0];
    store.setSelectedRun(latestRun?.id || "");
    if (latestRun?.status === "running") {
      store.startRun(latestRun.id);
    } else if (latestRun?.status === "waiting_user_input") {
      store.startRun(latestRun.id);
      store.setRunning(false);
    } else {
      store.finishRun();
      store.setSelectedRun(latestRun?.id || "");
    }
    return latestRun;
  }

  function deriveSessionTitle(input) {
    const value = String(input || "")
      .trim()
      .replace(/\s+/g, " ");
    if (!value) return "未命名分析";
    return value.length > 28 ? `${value.slice(0, 28)}...` : value;
  }

  function restoreBootstrapState(data) {
    const nextSessionId = data.session?.id || "";
    store.setSessions(data.sessions || []);
    return applySessionState(nextSessionId, data.runs || [], data.runtimeState);
  }

  async function bootstrap() {
    if (!store.token) throw new Error("未登录");
    const res = await fetch("/api/bootstrap", { headers: authHeaders() });
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
    let latestRun = restoreBootstrapState(data);
    if (!data.session?.id) {
      const session = await createSession({ refreshSessions: true });
      latestRun = session?.latestRun || null;
    }
    if (
      !data.runtimeState?.report_html &&
      (latestRun?.runKind === "report" ||
        latestRun?.reportFileId ||
        latestRun?.report)
    ) {
      await tryLoadRunReport(latestRun.id);
    }
  }

  async function initializeApp() {
    if (!store.token) throw new Error("未登录");
    if (bootstrapPromise) return bootstrapPromise;

    const pending = (async () => {
      store.setBootstrapState("loading");
      try {
        await bootstrap();
        await connect();
        store.setBootstrapState("ready");
      } catch (err) {
        disconnect();
        const message = err instanceof Error ? err.message : "工作区恢复失败";
        store.setBootstrapState("error", message);
        throw err;
      } finally {
        if (bootstrapPromise === pending) bootstrapPromise = null;
      }
    })();

    bootstrapPromise = pending;
    return pending;
  }

  async function createSession({ refreshSessions = true } = {}) {
    const res = await fetch("/api/sessions", {
      method: "POST",
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    if (data.session) store.upsertSession(data.session);
    const latestRun = applySessionState(
      data.session?.id || "",
      data.runs || [],
      data.runtimeState,
    );
    if (refreshSessions) await loadSessions();
    return { ...data.session, latestRun };
  }

  let sessionCreatePromise = null;

  async function ensureSession() {
    if (store.sessionId) return store.sessionId;
    if (sessionCreatePromise) return sessionCreatePromise;
    sessionCreatePromise = (async () => {
      try {
        const session = await createSession({ refreshSessions: true });
        if (!session?.id) throw new Error("创建会话失败");
        return session.id;
      } finally {
        sessionCreatePromise = null;
      }
    })();
    return sessionCreatePromise;
  }

  async function loadSessions() {
    const res = await fetch("/api/sessions", { headers: authHeaders() });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    store.setSessions(data.sessions || []);
    return data.sessions || [];
  }

  async function openSession(sessionId) {
    const res = await fetch(`/api/sessions/${encodeURIComponent(sessionId)}`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    disconnect();
    const latestRun = applySessionState(
      data.session?.id || "",
      data.runs || [],
      data.runtimeState,
    );
    try {
      if (
        !data.runtimeState?.report_html &&
        (latestRun?.runKind === "report" ||
          latestRun?.reportFileId ||
          latestRun?.report)
      ) {
        await tryLoadRunReport(latestRun.id);
      }
    } finally {
      await connect();
    }
  }

  async function openRun(runId) {
    if (!runId) return;
    store.setSelectedRun(runId);
    store.updateReport("");

    try {
      const res = await fetch(`/api/runs/${encodeURIComponent(runId)}`, {
        headers: authHeaders(),
      });
      if (res.ok) {
        const data = await res.json();
        if (data.run) store.upsertRun(data.run);
        if (data?.messages) {
          const historicalMessages = data.messages.map((msg) => {
            let parsedArgs = msg.content;
            let parsedResult = null;
            if (msg.type === "tool_call") {
              try {
                parsedArgs = JSON.parse(msg.content);
              } catch {
                parsedArgs = msg.content;
              }
            } else if (msg.type === "user_request_input") {
              try {
                parsedArgs = JSON.parse(msg.content);
              } catch {
                parsedArgs = {};
              }
            }
            if (msg.type === "tool_result") {
              try {
                parsedResult = JSON.parse(msg.content);
              } catch {
                parsedResult = null;
              }
            }
            return {
              id: msg.id,
              type: msg.type,
              content:
                msg.type !== "tool_call" &&
                msg.type !== "tool_result" &&
                msg.type !== "user_request_input"
                  ? msg.content
                  : undefined,
              name: msg.name,
              arguments: msg.type === "tool_call" ? parsedArgs : undefined,
              question:
                msg.type === "user_request_input"
                  ? parsedArgs?.question
                  : undefined,
              reason:
                msg.type === "user_request_input"
                  ? parsedArgs?.reason
                  : undefined,
              scope:
                msg.type === "user_request_input"
                  ? parsedArgs?.scope
                  : undefined,
              context_ref:
                msg.type === "user_request_input"
                  ? parsedArgs?.context_ref
                  : undefined,
              input_hint:
                msg.type === "user_request_input"
                  ? parsedArgs?.input_hint
                  : undefined,
              required:
                msg.type === "user_request_input"
                  ? parsedArgs?.required || false
                  : undefined,
              selection_mode:
                msg.type === "user_request_input"
                  ? parsedArgs?.selection_mode || "single"
                  : undefined,
              allow_custom:
                msg.type === "user_request_input"
                  ? parsedArgs?.allow_custom !== false
                  : undefined,
              options:
                msg.type === "user_request_input"
                  ? parsedArgs?.options || []
                  : undefined,
              result: msg.type === "tool_result" ? msg.content : undefined,
              parsedResult,
              duration: msg.duration,
              success: msg.success,
              timestamp: new Date(msg.createdAt).toLocaleTimeString(),
            };
          });
          store.setMessages(historicalMessages);
        }
        applyRuntimeState(data.runtimeState);
      }
    } catch (err) {
      console.error("Failed to load run messages:", err);
    }

    if (!store.reportHTML) await loadRunReport(runId);
  }

  function connect(options = {}) {
    const { resetReconnectAttempts = true } = options;
    if (!store.token) return Promise.reject(new Error("未登录"));
    if (wsInstance?.readyState === WebSocket.OPEN) {
      reconnectEnabled = true;
      return Promise.resolve(wsInstance);
    }
    if (wsInstance?.readyState === WebSocket.CONNECTING && connectPromise) {
      reconnectEnabled = true;
      return connectPromise;
    }

    reconnectEnabled = true;
    if (resetReconnectAttempts) {
      reconnectAttempts = 0;
    }
    clearReconnectTimer();
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const params = new URLSearchParams();
    if (store.sessionId) params.set("session_id", store.sessionId);
    if (store.workspace?.id) params.set("workspace_id", store.workspace.id);
    const sessionQuery = params.toString() ? `?${params.toString()}` : "";
    const url = `${protocol}//${location.host}/ws${sessionQuery}`;
    const socket = new WebSocket(url, ["mcp-token", `token-${store.token}`]);
    wsInstance = socket;
    store.setConnectionState("connecting");
    connected.value = false;

    const pending = new Promise((resolve, reject) => {
      let settled = false;

      function resolveOnce(value) {
        if (settled) return;
        settled = true;
        if (connectPromise === pending) connectPromise = null;
        resolve(value);
      }

      function rejectOnce(error) {
        if (settled) return;
        settled = true;
        if (connectPromise === pending) connectPromise = null;
        reject(error);
      }

      socket.onopen = () => {
        if (wsInstance !== socket) {
          resolveOnce(socket);
          return;
        }
        connected.value = true;
        reconnectAttempts = 0;
        store.setConnectionState("connected");
        flushPendingMessages(socket);
        resolveOnce(socket);
      };

      socket.onmessage = (event) => {
        if (wsInstance !== socket) return;
        let data;
        try {
          data = JSON.parse(event.data);
        } catch {
          return;
        }
        handleEvent(data, store);
      };

      socket.onclose = () => {
        if (wsInstance !== socket) {
          rejectOnce(new Error("连接已被替换"));
          return;
        }
        wsInstance = null;
        connected.value = false;
        rejectOnce(new Error("WebSocket 连接已关闭"));
        if (!store.token || !reconnectEnabled) {
          store.setConnectionState("disconnected");
          return;
        }
        if (reconnectAttempts >= RECONNECT_MAX_ATTEMPTS) {
          store.setConnectionState("disconnected");
          console.error(
            `WebSocket: 已达最大重连次数 (${RECONNECT_MAX_ATTEMPTS})，停止重连`,
          );
          return;
        }
        store.setConnectionState("reconnecting");
        const delay = getReconnectDelay();
        reconnectAttempts++;
        console.log(
          `WebSocket 断开，${Math.round(delay)}ms 后重连 (第 ${reconnectAttempts} 次)...`,
        );
        clearReconnectTimer();
        reconnectTimer = setTimeout(() => {
          void connect({ resetReconnectAttempts: false }).catch((err) =>
            console.error("WebSocket 重连失败:", err),
          );
        }, delay);
      };

      socket.onerror = () => {
        if (wsInstance !== socket) return;
        console.error("WebSocket 连接错误");
      };
    });

    connectPromise = pending;
    return pending;
  }

  function handleEvent(event, store) {
    if (
      event.sessionId &&
      store.sessionId &&
      event.sessionId !== store.sessionId
    )
      return;
    const relevantRunIds = [store.activeRunId, store.selectedRunId].filter(
      Boolean,
    );
    const selectedRunScopedTypes = new Set([
      "assistant_status",
      "tool_call",
      "tool_result",
      "user_request_input",
    ]);
    if (
      event.runId &&
      relevantRunIds.length > 0 &&
      !relevantRunIds.includes(event.runId) &&
      selectedRunScopedTypes.has(event.type)
    )
      return;

    switch (event.type) {
      case "session_ready": {
        store.setSession(event.data.sessionId);
        applyRuntimeState(event.data);
        if (event.data.sessionId) {
          dataSourceStore.fetchSessionSources(event.data.sessionId);
        }
        const existingSession = store.sessions.find(
          (s) => s.id === event.data.sessionId,
        );
        store.upsertSession({
          id: event.data.sessionId,
          title: event.data.title || existingSession?.title || "未命名分析",
          lastSeenAt: new Date().toISOString(),
        });
        break;
      }
      case "session_reset":
        store.resetAnalysis();
        if (store.sessionId) {
          dataSourceStore.fetchSessionSources(store.sessionId);
        }
        break;
      case "run_started":
        store.startRun(event.data.runId);
        store.upsertRun({
          id: event.data.runId,
          sessionId: store.sessionId,
          status: "running",
          inputMessage:
            store.messages.filter((msg) => msg.type === "user").at(-1)
              ?.content || "",
          createdAt: new Date().toISOString(),
        });
        break;
      case "assistant_status":
        appendRunPreview(event);
        if (!shouldShowRunEvent(event.runId)) break;
        store.addMessage({
          type: "assistant_status",
          content: event.data.content,
        });
        break;
      case "tool_call":
        appendRunPreview(event);
        if (!shouldShowRunEvent(event.runId)) break;
        store.addMessage({
          type: "tool_call",
          name: event.data.name,
          arguments: event.data.arguments,
          id: event.data.id,
        });
        break;
      case "tool_result": {
        appendRunPreview(event);
        if (!shouldShowRunEvent(event.runId)) break;
        let parsedResult = null;
        try {
          parsedResult = JSON.parse(event.data.result);
        } catch {
          parsedResult = null;
        }
        store.addMessage({
          type: "tool_result",
          name: event.data.name,
          result: event.data.result,
          parsedResult,
          duration: event.data.duration,
          success: event.data.success,
          id: event.data.id,
        });
        break;
      }
      case "report_update":
        if (shouldApplyReportEvent(event.runId)) {
          store.updateReport(event.data.html);
        }
        break;
      case "report_final":
        if (!store.selectedRunId || store.selectedRunId === event.runId) {
          store.setSelectedRun(event.runId);
          store.updateReport(event.data.html);
        }
        if (event.data.title && store.sessionId) {
          store.upsertSession({
            id: store.sessionId,
            title: event.data.title,
            lastSeenAt: new Date().toISOString(),
          });
        }
        if (event.data.reportFileId && event.runId) {
          if (
            !store.patchRun(event.runId, {
              reportFileId: event.data.reportFileId,
            })
          ) {
            store.upsertRun({
              id: event.runId,
              reportFileId: event.data.reportFileId,
            });
          }
        }
        break;
      case "run_completed": {
        const patch = {
          status: "completed",
          summary: event.data.summary,
          updatedAt: new Date().toISOString(),
        };
        if (!store.patchRun(event.runId, patch))
          store.upsertRun({ id: event.runId, ...patch });
        appendRunPreview(event);
        if (shouldShowRunEvent(event.runId) && event.data.summary) {
          store.addMessage({ type: "complete", content: event.data.summary });
        }
        store.finishRun(event.runId);
        break;
      }
      case "run_cancelled": {
        const patch = {
          status: "cancelled",
          updatedAt: new Date().toISOString(),
        };
        if (!store.patchRun(event.runId, patch))
          store.upsertRun({ id: event.runId, ...patch });
        appendRunPreview(event);
        if (shouldShowRunEvent(event.runId)) {
          store.addMessage({
            type: "cancelled",
            content: event.data.message || "任务已取消",
          });
        }
        store.finishRun(event.runId);
        break;
      }
      case "error": {
        if (event.runId) {
          const patch = {
            status: "failed",
            errorMessage: event.data.message,
            updatedAt: new Date().toISOString(),
          };
          if (!store.patchRun(event.runId, patch))
            store.upsertRun({ id: event.runId, ...patch });
        }
        appendRunPreview(event);
        if (shouldShowRunEvent(event.runId)) {
          store.addMessage({ type: "error", content: event.data.message });
        }
        store.finishRun(event.runId);
        break;
      }
      case "user_request_input":
        store.setRunning(false);
        if (event.runId)
          store.patchRun(event.runId, {
            status: "waiting_user_input",
            updatedAt: new Date().toISOString(),
          });
        appendRunPreview(event);
        if (!shouldShowRunEvent(event.runId)) break;
        store.addMessage({
          type: "user_request_input",
          question: event.data.question,
          reason: event.data.reason,
          scope: event.data.scope,
          context_ref: event.data.context_ref,
          input_hint: event.data.input_hint,
          required: event.data.required || false,
          selection_mode: event.data.selection_mode || "single",
          allow_custom: event.data.allow_custom !== false,
          options: event.data.options || [],
        });
        break;
      case "state_subgoals_updated":
        if (event.data?.goals) store.setSubgoals(event.data.goals);
        break;
      case "state_memory_updated":
        if (event.data?.facts) store.setMemoryFacts(event.data.facts);
        break;
      case "state_report_edit_updated":
        store.setReportEditState(event.data || null);
        break;
      case "state_child_runs_updated":
        if (event.data?.childRuns)
          store.setRunChildren(event.data.parentRunId, event.data.childRuns);
        break;
    }
  }

  function send(type, data = {}, runId = "") {
    const payload = { type, sessionId: store.sessionId, runId, data };
    if (wsInstance?.readyState === WebSocket.OPEN) {
      try {
        wsInstance.send(JSON.stringify(payload));
        return true;
      } catch (err) {
        console.warn("websocket send failed:", err);
        return false;
      }
    } else {
      enqueueMessage(payload);
      return true;
    }
  }

  async function login(email, password, workspaceId = "") {
    const res = await fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
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

  async function switchWorkspace(workspaceId) {
    const res = await fetch("/api/auth/switch-workspace", {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
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

  function disconnect() {
    reconnectEnabled = false;
    reconnectAttempts = 0;
    clearReconnectTimer();
    pendingMessages.length = 0;
    if (wsInstance) {
      const socket = wsInstance;
      wsInstance = null;
      socket.close();
    }
    connected.value = false;
    connectPromise = null;
    store.setConnectionState("disconnected");
  }

  async function ensureSocketOpen() {
    if (wsInstance?.readyState === WebSocket.OPEN) return wsInstance;
    await connect();
    if (wsInstance?.readyState !== WebSocket.OPEN)
      throw new Error("连接尚未建立，请稍后重试。");
    return wsInstance;
  }

  async function sendMessage(content, options = {}) {
    const value = String(content || "").trim();
    if (!value) return false;

    try {
      await ensureSession();
      await ensureSocketOpen();
    } catch (err) {
      const message =
        err instanceof Error ? err.message : "连接尚未建立，请稍后重试。";
      store.addMessage({ type: "error", content: message });
      return false;
    }

    const waitingRunId = store.activeRunId;
    const waitingRun = waitingRunId ? store.getRun(waitingRunId) : null;
    const isAnsweringUserRequest = waitingRun?.status === "waiting_user_input";

    const payload = { content: String(options.payloadContent || value).trim() };
    if (!isAnsweringUserRequest && options.editContext)
      payload.editContext = options.editContext;
    if (!isAnsweringUserRequest && options.turnContext)
      payload.turnContext = options.turnContext;
    if (!send("user_message", payload)) {
      store.addMessage({ type: "error", content: "消息发送失败，请重试。" });
      return false;
    }

    store.setRunning(true);
    if (isAnsweringUserRequest) {
      store.patchRun(waitingRunId, {
        status: "running",
        updatedAt: new Date().toISOString(),
      });
    }
    if (!isAnsweringUserRequest && store.sessionId) {
      store.upsertSession({
        id: store.sessionId,
        title: deriveSessionTitle(value),
        lastSeenAt: new Date().toISOString(),
      });
    }
    store.addMessage({
      type: "user",
      content: value,
      editContext: isAnsweringUserRequest ? null : options.editContext || null,
      turnContext: isAnsweringUserRequest ? null : options.turnContext || null,
    });
    return true;
  }

  function stop() {
    send("stop_run", { runId: store.activeRunId }, store.activeRunId);
  }

  function resetSession() {
    send("reset_session", {});
  }

  async function createNewSession() {
    disconnect();
    store.resetAnalysis();
    store.updateReport("");
    await createSession({ refreshSessions: true });
    await connect();
  }

  async function renameSession(sessionId, title) {
    if (!sessionId || !title.trim()) return;
    const res = await fetch(`/api/sessions/${encodeURIComponent(sessionId)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ title: title.trim() }),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    if (data.session) store.upsertSession(data.session);
  }

  async function deleteSession(sessionId) {
    if (!sessionId) return;
    const res = await fetch(`/api/sessions/${encodeURIComponent(sessionId)}`, {
      method: "DELETE",
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error(await res.text());
    await loadSessions();
    if (store.sessionId === sessionId) await createNewSession();
  }

  return {
    connected,
    bootstrap,
    initializeApp,
    connect,
    login,
    switchWorkspace,
    loadSessions,
    openSession,
    openRun,
    disconnect,
    sendMessage,
    stop,
    resetSession,
    createNewSession,
    ensureSession,
    renameSession,
    deleteSession,
  };
}
