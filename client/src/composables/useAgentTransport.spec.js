global.localStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
  clear: () => {},
};

import { setActivePinia, createPinia } from "pinia";
import { useAgentTransport } from "./useAgentTransport";
import { useAgentStore } from "../stores/agent";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

function flush(ms = 5) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

describe("useAgentTransport", () => {
  let originalEventSource;

  beforeEach(() => {
    setActivePinia(createPinia());
    originalEventSource = global.EventSource;
  });

  afterEach(() => {
    try {
      const { disconnect } = useAgentTransport();
      disconnect();
    } catch {}
    global.EventSource = originalEventSource;
    global.fetch = undefined;
    vi.restoreAllMocks();
  });

  it("connects to the canonical event stream", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.setMessages([]);

    class MockEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      constructor(url) {
        this.url = url;
        this.readyState = 1; // OPEN
        setTimeout(() => this.onopen?.(), 1);
      }
      close() {}
    }

    global.EventSource = MockEventSource;
    const { connect } = useAgentTransport();
    await connect();

    expect(store.connectionState).toBe("connected");
  });

  it("rebinds the event stream when the session changes", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");

    const sources = [];
    class MockEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      constructor(url) {
        this.url = url;
        this.readyState = 1;
        this.closed = false;
        sources.push(this);
        setTimeout(() => this.onopen?.(), 1);
      }
      close() {
        this.closed = true;
      }
    }

    global.EventSource = MockEventSource;
    const { connect } = useAgentTransport();
    await connect();

    expect(sources).toHaveLength(1);
    expect(sources[0].url).toBe("/api/sse?session_id=sess-1");

    store.setSession("sess-2");
    await flush();

    expect(sources[0].closed).toBe(true);
    expect(sources).toHaveLength(2);
    expect(sources[1].url).toBe("/api/sse?session_id=sess-2");
    expect(store.connectionState).toBe("connected");
  });

  it("does not open the event stream without a session", async () => {
    const store = useAgentStore();
    store.setToken("test-token");

    let constructed = 0;
    class MockEventSource {
      constructor() {
        constructed += 1;
      }
      close() {}
    }

    global.EventSource = MockEventSource;
    const { connect } = useAgentTransport();
    const result = await connect();

    expect(result).toBeNull();
    expect(constructed).toBe(0);
    expect(store.connectionState).toBe("disconnected");
  });

  it("reconnects with backoff after a permanent stream failure", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");

    const sources = [];
    class FlakyEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      constructor(url) {
        this.url = url;
        this.readyState = 1;
        sources.push(this);
        setTimeout(() => this.onopen?.(), 1);
        if (sources.length === 1) {
          setTimeout(() => {
            this.readyState = 2;
            this.onerror?.();
          }, 3);
        }
      }
      close() {}
    }

    global.EventSource = FlakyEventSource;
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({}),
    });

    const { connect } = useAgentTransport();
    await connect();
    expect(store.connectionState).toBe("connected");

    await flush(20);
    expect(store.connectionState).toBe("reconnecting");

    await flush(1300);
    expect(sources.length).toBeGreaterThanOrEqual(2);
    expect(store.connectionState).toBe("connected");
  }, 5000);

  it("logs out when the auth probe reports 401 after a stream failure", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");

    class FlakyEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      constructor() {
        this.readyState = 1;
        setTimeout(() => this.onopen?.(), 1);
        setTimeout(() => {
          this.readyState = 2;
          this.onerror?.();
        }, 3);
      }
      close() {}
    }

    global.EventSource = FlakyEventSource;
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      text: async () => "登录凭证无效或已过期",
    });

    const { connect } = useAgentTransport();
    await connect();

    await flush(20);
    expect(store.token).toBe("");
    expect(store.connectionState).toBe("disconnected");
  }, 5000);

  it("records the exact run id returned by chat", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: "started", run_id: "run-1" }),
    });

    const { sendMessage } = useAgentTransport();
    expect(await sendMessage("分析上传的数据")).toBe(true);
    expect(store.activeRunId).toBe("run-1");
    expect(store.selectedRunId).toBe("run-1");
    expect(store.getRun("run-1")).toMatchObject({
      id: "run-1",
      sessionId: "sess-1",
      status: "running",
    });
  });

  it("rejects an initial event stream failure", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");

    class FailedEventSource {
      constructor() {
        setTimeout(() => this.onerror?.(), 1);
      }
      close() {}
    }

    global.EventSource = FailedEventSource;
    const { connect } = useAgentTransport();
    await expect(connect()).rejects.toThrow("事件流连接失败");
    expect(store.connectionState).toBe("disconnected");
  });
});
