global.localStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
  clear: () => {},
};

import { setActivePinia, createPinia } from "pinia";
import { useWebSocket } from "./useWebSocket";
import { useAgentStore } from "../stores/agent";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

describe("useWebSocket deduplication", () => {
  let originalWebSocket;

  beforeEach(() => {
    setActivePinia(createPinia());
    originalWebSocket = global.WebSocket;
  });

  afterEach(() => {
    try {
      const { disconnect } = useWebSocket();
      disconnect();
    } catch {}
    global.WebSocket = originalWebSocket;
    global.fetch = undefined;
    vi.restoreAllMocks();
  });

  it("does not inject an artificial completion message on report_final", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");

    // Simulate setting up a run
    store.upsertRun({ id: "run-1", sessionId: "sess-1", status: "running" });
    store.setSelectedRun("run-1");
    store.setMessages([]); // Start with empty messages

    global.location = { protocol: "http:", host: "localhost" };

    // Mock WebSocket
    let activeSocket = null;
    class MockWebSocket {
      constructor(url, protocols) {
        this.url = url;
        this.protocols = protocols;
        this.readyState = 1; // OPEN
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }

      triggerMessage(dataObj) {
        if (this.onmessage) {
          this.onmessage({ data: JSON.stringify(dataObj) });
        }
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    const { connect, disconnect } = useWebSocket();
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const wsPromise = connect();
    await new Promise((resolve) => setTimeout(resolve, 5));

    await wsPromise;
    expect(activeSocket).not.toBeNull();

    // Simulate report_final event incoming
    activeSocket.triggerMessage({
      type: "report_final",
      runId: "run-1",
      data: {
        html: "<p>report</p>",
        title: "Report Title",
        reportFileId: "file-xyz", // The key element of Finding 2 fix
      },
    });

    // Assert that the run object now securely holds the reportFileId
    expect(store.getRun("run-1")?.reportFileId).toBe("file-xyz");

    // report_final should only update report state; it must not inject a fake trace message
    const msgCountAfterFinal = store.messages.length;
    expect(msgCountAfterFinal).toBe(0);

    // Simulate run_completed incoming immediately after report_final
    activeSocket.triggerMessage({
      type: "run_completed",
      runId: "run-1",
      data: {
        summary: "model response",
      },
    });

    // run_completed carries the user-facing closing summary even when report_final already bound a file.
    expect(store.messages.length).toBe(msgCountAfterFinal + 1);
    expect(store.messages.at(-1)?.type).toBe("complete");
    expect(store.messages.at(-1)?.content).toContain("model response");
    expect(store.getRun("run-1").status).toBe("completed");

    disconnect();
    global.fetch = undefined;
  });

  it("still processes lifecycle events for the active run when a different run is selected", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.startRun("run-root");
    store.upsertRun({ id: "run-root", sessionId: "sess-1", status: "running" });
    store.upsertRun({
      id: "run-child",
      sessionId: "sess-1",
      parentRunId: "run-root",
      status: "running",
    });
    store.setSelectedRun("run-child");
    store.setMessages([]);

    global.location = { protocol: "http:", host: "localhost" };

    let activeSocket = null;
    class MockWebSocket {
      constructor(url, protocols) {
        this.url = url;
        this.protocols = protocols;
        this.readyState = 1;
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }
      triggerMessage(dataObj) {
        this.onmessage?.({ data: JSON.stringify(dataObj) });
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect } = useWebSocket();
    await connect();

    activeSocket.triggerMessage({
      type: "run_completed",
      runId: "run-root",
      data: {
        summary: "root finished",
      },
    });

    expect(store.isRunning).toBe(false);
    expect(store.activeRunId).toBe("");
    expect(store.getRun("run-root")?.status).toBe("completed");
    expect(store.getRun("run-root")?.previewMessages.at(-1)?.summary).toBe(
      "root finished",
    );
    expect(store.messages.length).toBe(0);

    disconnect();
    global.fetch = undefined;
  });

  it("does not append report_final completion text while another run is selected", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.upsertRun({ id: "run-root", sessionId: "sess-1", status: "running" });
    store.upsertRun({
      id: "run-child",
      sessionId: "sess-1",
      parentRunId: "run-root",
      status: "running",
    });
    store.setSelectedRun("run-child");
    store.setMessages([]);

    global.location = { protocol: "http:", host: "localhost" };

    let activeSocket = null;
    class MockWebSocket {
      constructor(url, protocols) {
        this.url = url;
        this.protocols = protocols;
        this.readyState = 1;
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }
      triggerMessage(dataObj) {
        this.onmessage?.({ data: JSON.stringify(dataObj) });
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect } = useWebSocket();
    await connect();

    activeSocket.triggerMessage({
      type: "report_final",
      runId: "run-root",
      data: {
        html: "<p>report</p>",
        title: "Report Title",
        reportFileId: "file-xyz",
      },
    });

    expect(store.messages).toHaveLength(0);
    expect(store.selectedRunId).toBe("run-child");
    expect(store.getRun("run-root")?.reportFileId).toBe("file-xyz");

    disconnect();
    global.fetch = undefined;
  });

  it("applies shared runtime state updates even when they come from a child run", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.upsertRun({ id: "run-root", sessionId: "sess-1", status: "running" });
    store.upsertRun({
      id: "run-child",
      sessionId: "sess-1",
      parentRunId: "run-root",
      status: "running",
    });
    store.setSelectedRun("run-root");

    global.location = { protocol: "http:", host: "localhost" };

    let activeSocket = null;
    class MockWebSocket {
      constructor() {
        this.readyState = 1;
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }
      triggerMessage(dataObj) {
        this.onmessage?.({ data: JSON.stringify(dataObj) });
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect } = useWebSocket();
    await connect();

    activeSocket.triggerMessage({
      type: "state_memory_updated",
      runId: "run-child",
      data: {
        facts: { roi_definition: "revenue / spend" },
      },
    });

    activeSocket.triggerMessage({
      type: "state_subgoals_updated",
      runId: "run-child",
      data: {
        goals: [{ id: "g1", description: "check roi", status: "running" }],
      },
    });

    expect(store.memoryFacts.roi_definition).toBe("revenue / spend");
    expect(store.subgoals).toHaveLength(1);
    expect(store.subgoals[0].id).toBe("g1");

    disconnect();
    global.fetch = undefined;
  });

  it("applies shared report updates even when they come from a child run", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.upsertRun({ id: "run-root", sessionId: "sess-1", status: "running" });
    store.upsertRun({
      id: "run-child",
      sessionId: "sess-1",
      parentRunId: "run-root",
      status: "running",
    });
    store.setSelectedRun("run-root");

    global.location = { protocol: "http:", host: "localhost" };

    let activeSocket = null;
    class MockWebSocket {
      constructor() {
        this.readyState = 1;
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }
      triggerMessage(dataObj) {
        this.onmessage?.({ data: JSON.stringify(dataObj) });
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect } = useWebSocket();
    await connect();

    activeSocket.triggerMessage({
      type: "report_update",
      runId: "run-child",
      data: {
        html: "<p>delegate draft update</p>",
      },
    });

    expect(store.reportHTML).toBe("<p>delegate draft update</p>");

    disconnect();
    global.fetch = undefined;
  });

  it("hydrates report draft from session_ready runtime state", async () => {
    const store = useAgentStore();
    store.setToken("test-token");

    global.location = { protocol: "http:", host: "localhost" };

    let activeSocket = null;
    class MockWebSocket {
      constructor() {
        this.readyState = 1;
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }
      triggerMessage(dataObj) {
        this.onmessage?.({ data: JSON.stringify(dataObj) });
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect } = useWebSocket();
    await connect();

    activeSocket.triggerMessage({
      type: "session_ready",
      data: {
        sessionId: "sess-1",
        files: [],
        memory: { confirmed_metric: "GMV uses settled orders" },
        subgoals: [],
        report_html: "<p>persisted shared draft</p>",
      },
    });

    expect(store.sessionId).toBe("sess-1");
    expect(store.memoryFacts.confirmed_metric).toBe("GMV uses settled orders");
    expect(store.reportHTML).toBe("<p>persisted shared draft</p>");

    disconnect();
    global.fetch = undefined;
  });

  it("openRun keeps session-scoped runtime state when loading a child run", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    const fetchMock = vi.fn(async (url) => {
      if (url === "/api/runs/run-child") {
        return {
          ok: true,
          json: async () => ({
            run: {
              id: "run-child",
              sessionId: "sess-1",
              parentRunId: "run-root",
              status: "completed",
            },
            messages: [],
            runtimeState: {
              memory: { confirmed_metric: "GMV uses settled orders" },
              subgoals: [
                {
                  id: "goal_123",
                  description: "Inspect revenue quality",
                  status: "pending",
                },
              ],
              report_html: "<p>shared draft html</p>",
            },
          }),
        };
      }
      throw new Error(`unexpected fetch: ${url}`);
    });

    global.fetch = fetchMock;

    const { openRun } = useWebSocket();
    await openRun("run-child");

    expect(store.selectedRunId).toBe("run-child");
    expect(store.memoryFacts.confirmed_metric).toBe("GMV uses settled orders");
    expect(store.subgoals).toHaveLength(1);
    expect(store.subgoals[0].id).toBe("goal_123");
    expect(store.reportHTML).toBe("<p>shared draft html</p>");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("sends waiting user input as a raw tool answer without edit context", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.upsertSession({ id: "sess-1", title: "Existing title" });
    store.upsertRun({
      id: "run-1",
      sessionId: "sess-1",
      status: "waiting_user_input",
    });
    store.startRun("run-1");
    store.setRunning(false);
    store.patchRun("run-1", { status: "waiting_user_input" });

    global.location = { protocol: "http:", host: "localhost" };

    const sent = [];
    class MockWebSocket {
      constructor(url, protocols) {
        this.url = url;
        this.protocols = protocols;
        this.readyState = 1;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send(raw) {
        sent.push(JSON.parse(raw));
      }
      close() {
        this.onclose?.();
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect, sendMessage } = useWebSocket();
    await connect();
    const answerPayload = JSON.stringify({
      response_type: "selection",
      selected_option_ids: ["gross"],
      selected_options: [{ id: "gross", label: "Gross amount" }],
      custom_response: "",
    });
    const result = await sendMessage("选择：Gross amount", {
      payloadContent: answerPayload,
      editContext: { mode: "regenerate_selection", blockId: "b1" },
      turnContext: { reportTargetRunId: "run-old" },
    });

    expect(result).toBe(true);
    expect(sent).toHaveLength(1);
    expect(sent[0].type).toBe("user_message");
    expect(sent[0].data).toEqual({ content: answerPayload });
    expect(store.getRun("run-1")?.status).toBe("running");
    expect(store.sessions[0]?.title).toBe("Existing title");
    expect(store.messages.at(-1)).toMatchObject({
      type: "user",
      content: "选择：Gross amount",
      editContext: null,
      turnContext: null,
    });

    disconnect();
    global.fetch = undefined;
  });

  it("returns false when a message cannot be sent", async () => {
    const store = useAgentStore();
    store.setToken("test-token");

    global.fetch = vi.fn().mockRejectedValue(new Error("offline"));

    const { sendMessage } = useWebSocket();
    const result = await sendMessage("hello");

    expect(result).toBe(false);
    expect(store.messages.at(-1)).toMatchObject({
      type: "error",
      content: "offline",
    });
  });

  it("returns false when websocket send throws without appending a user message", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.setMessages([]);

    global.location = { protocol: "http:", host: "localhost" };

    class MockWebSocket {
      constructor(url, protocols) {
        this.url = url;
        this.protocols = protocols;
        this.readyState = 1;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {
        throw new Error("socket write failed");
      }
      close() {
        this.onclose?.();
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    const { sendMessage } = useWebSocket();
    const result = await sendMessage("hello");

    expect(result).toBe(false);
    expect(store.isRunning).toBe(false);
    expect(store.messages.some((msg) => msg.type === "user")).toBe(false);
    expect(store.messages.at(-1)).toMatchObject({
      type: "error",
      content: "消息发送失败，请重试。",
    });
  });

  it("does not let report_update from another run overwrite the selected historical report", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.upsertRun({ id: "run-root", sessionId: "sess-1", status: "running" });
    store.upsertRun({
      id: "run-history",
      sessionId: "sess-1",
      status: "completed",
      reportFileId: "rep-1",
    });
    store.setSelectedRun("run-history");
    store.updateReport("<p>historical report</p>");

    global.location = { protocol: "http:", host: "localhost" };

    let activeSocket = null;
    class MockWebSocket {
      constructor(url, protocols) {
        this.url = url;
        this.protocols = protocols;
        this.readyState = 1;
        activeSocket = this;
        setTimeout(() => {
          this.onopen?.();
        }, 1);
      }
      send() {}
      close() {
        this.onclose?.();
      }
      triggerMessage(dataObj) {
        this.onmessage?.({ data: JSON.stringify(dataObj) });
      }
    }
    MockWebSocket.CONNECTING = 0;
    MockWebSocket.OPEN = 1;
    MockWebSocket.CLOSING = 2;
    MockWebSocket.CLOSED = 3;
    vi.stubGlobal("WebSocket", MockWebSocket);

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
      text: async () => "",
    });

    const { connect, disconnect } = useWebSocket();
    await connect();

    activeSocket.triggerMessage({
      type: "report_update",
      runId: "run-root",
      data: {
        html: "<p>root draft update</p>",
      },
    });

    expect(store.reportHTML).toBe("<p>historical report</p>");

    disconnect();
    global.fetch = undefined;
  });
});
