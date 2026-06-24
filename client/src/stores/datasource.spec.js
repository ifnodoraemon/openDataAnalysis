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

  it("derives semantic profile summaries from session source facts", async () => {
    const store = useDataSourceStore();
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      text: async () =>
        JSON.stringify({
          sources: [
            {
              source_id: "ds_1",
              analysis_table_name: "sales",
              semantic_status: "profiled",
              profile_id: "sp_1",
              schema_signature: "sig_1",
            },
          ],
        }),
    });

    const result = await store.fetchSessionSources("sess_1");

    expect(result.ok).toBe(true);
    expect(store.semanticProfileSummaries).toEqual([
      {
        profile_id: "sp_1",
        source_id: "ds_1",
        analysis_table_name: "sales",
        profile_status: "profiled",
        schema_signature: "sig_1",
      },
    ]);
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

  it("creates SQL sources with separated public config and credential", async () => {
    const store = useDataSourceStore();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 201,
        text: async () => JSON.stringify({ id: "ds_1" }),
      })
      .mockResolvedValueOnce({
        ok: true,
        text: async () => JSON.stringify({ data_sources: [] }),
      });
    global.fetch = fetchMock;

    await store.createSQLSource("Analytics", "mysql_connection", {
      source_type: "mysql_connection",
      host: "db.example.com",
      port: 3306,
      database_name: "analytics",
      default_schema: "public",
      username: "reader",
      password: "secret",
      allowlist: [{ schema: "public", name: "orders", kind: "table" }],
    });

    const body = JSON.parse(fetchMock.mock.calls[0][1].body);
    expect(body).toEqual({
      name: "Analytics",
      source_type: "mysql_connection",
      config: {
        host: "db.example.com",
        port: 3306,
        database_name: "analytics",
        default_schema: "public",
        username: "reader",
        allowlist: [{ schema: "public", name: "orders", kind: "table" }],
      },
      credential: { password: "secret" },
    });
    expect(JSON.stringify(body.config)).not.toContain("secret");
  });

  it("fetches connector source type specs", async () => {
    const store = useDataSourceStore();
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      text: async () =>
        JSON.stringify({
          source_types: [
            {
              source_type: "mysql_connection",
              label: "MySQL",
              category: "sql",
              configurable: true,
              default_port: 3306,
            },
          ],
        }),
    });

    const result = await store.fetchSourceTypes();

    expect(result.ok).toBe(true);
    expect(store.sourceTypes).toEqual([
      {
        source_type: "mysql_connection",
        label: "MySQL",
        category: "sql",
        configurable: true,
        default_port: 3306,
      },
    ]);
  });

  it("omits credential when updating SQL source without a new password", async () => {
    const store = useDataSourceStore();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        text: async () => JSON.stringify({ id: "ds_1" }),
      })
      .mockResolvedValueOnce({
        ok: true,
        text: async () => JSON.stringify({ data_sources: [] }),
      });
    global.fetch = fetchMock;

    await store.updateSQLSource("ds_1", "Analytics", {
      host: "db.example.com",
      port: 5432,
      database_name: "analytics",
      default_schema: "public",
      ssl_mode: "require",
      username: "reader",
      password: "",
      allowlist: [{ schema: "public", name: "orders", kind: "table" }],
    });

    const body = JSON.parse(fetchMock.mock.calls[0][1].body);
    expect(body.credential).toBeUndefined();
    expect(body.config.password).toBeUndefined();
  });
});
