import { setActivePinia, createPinia } from "pinia";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { useDataSourceStore } from "./datasource";

global.localStorage = {
  getItem: () => "test-token",
  setItem: () => {},
  removeItem: () => {},
  clear: () => {},
};

describe("data source store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  afterEach(() => {
    global.fetch = undefined;
    vi.restoreAllMocks();
  });

  it("sends session_id when confirming a session-scoped semantic profile", async () => {
    const store = useDataSourceStore();
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ profile_id: "sp_1", profile_status: "confirmed" }),
    });
    global.fetch = fetchMock;

    const result = await store.confirmProfile(
      "sp_1",
      "session",
      { primary_time_column: "month" },
      "sess_1",
    );

    expect(result.ok).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/semantic-profiles/sp_1/confirm",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          session_id: "sess_1",
          scope: "session",
          overrides: { primary_time_column: "month" },
        }),
      }),
    );
  });

  it("surfaces session source fetch failures instead of silently ignoring them", async () => {
    const store = useDataSourceStore();
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      text: async () => "failed to get data sources\n",
    });

    const result = await store.fetchSessionSources("sess_1");

    expect(result).toEqual({
      ok: false,
      error: "failed to get data sources",
    });
    expect(store.lastError).toBe("failed to get data sources");
    expect(store.sessionSources).toEqual([]);
  });

  it("returns backend failure text when testing a connection fails", async () => {
    const store = useDataSourceStore();
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 403,
      text: async () => "not authorized",
    });

    const result = await store.testConnection("ds_1");

    expect(result).toEqual({ success: false, message: "not authorized" });
  });
});
