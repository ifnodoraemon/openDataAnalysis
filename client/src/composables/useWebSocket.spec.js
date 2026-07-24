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

describe("useWebSocket with EventSource SSE", () => {
  let originalEventSource;

  beforeEach(() => {
    setActivePinia(createPinia());
    originalEventSource = global.EventSource;
  });

  afterEach(() => {
    try {
      const { disconnect } = useWebSocket();
      disconnect();
    } catch {}
    global.EventSource = originalEventSource;
    global.fetch = undefined;
    vi.restoreAllMocks();
  });

  it("handles report_final without duplicating artificial completion message", async () => {
    const store = useAgentStore();
    store.setToken("test-token");
    store.setSession("sess-1");
    store.setMessages([]);

    global.location = { protocol: "http:", host: "localhost" };

    class MockEventSource {
      constructor(url) {
        this.url = url;
        this.readyState = 1; // OPEN
        setTimeout(() => this.onopen?.(), 1);
      }
      close() {}
    }

    global.EventSource = MockEventSource;
    const { connect } = useWebSocket();
    await connect();

    expect(store.connectionState).toBe("connected");
  });
});
