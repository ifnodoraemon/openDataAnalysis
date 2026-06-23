import { defineStore } from "pinia";
import { ref } from "vue";

export const useDataSourceStore = defineStore("dataSource", () => {
  const sessionSources = ref([]);
  const workspaceDataSources = ref([]);
  const semanticProfileSummaries = ref([]);
  const semanticProfileDetails = ref({});
  const loading = ref(false);
  const lastError = ref("");

  async function fetchSessionSources(sessionId) {
    if (!sessionId) return { ok: false, error: "session_id is required" };
    return withLoading(async () => {
      const result = await requestJSON(`/api/sessions/${sessionId}/sources`, {
        fallback: "failed to fetch session sources",
      });
      if (!result.ok) return result;

      const data = result.data || {};
      sessionSources.value = data.sources || [];
      semanticProfileSummaries.value = (data.profiles || []).map((p) => ({
        profile_id: p.profile_id,
        source_id: p.source_id,
        analysis_table_name: p.analysis_table_name,
        profile_status: p.profile_status,
        schema_signature: p.schema_signature,
      }));
      return result;
    });
  }

  async function fetchWorkspaceDataSources() {
    return withLoading(async () => {
      const result = await requestJSON("/api/data-sources", {
        fallback: "failed to fetch data sources",
      });
      if (!result.ok) return result;

      const data = result.data || {};
      workspaceDataSources.value = data.data_sources || [];
      return result;
    });
  }

  async function fetchProfileDetail(profileId) {
    if (!profileId) return { ok: false, error: "profile_id is required" };
    return withLoading(async () => {
      const result = await requestJSON(`/api/semantic-profiles/${profileId}`, {
        fallback: "failed to fetch profile",
      });
      if (!result.ok) return result;

      const data = result.data || {};
      semanticProfileDetails.value[profileId] = data;
      return result;
    });
  }

  async function confirmProfile(profileId, scope, overrides, sessionId = "") {
    return requestJSON(`/api/semantic-profiles/${profileId}/confirm`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sessionId, scope, overrides }),
      fallback: "confirm failed",
    });
  }

  async function createPostgresSource(name, config) {
    const { publicConfig, credential } = splitPostgresConfig(config);
    const result = await requestJSON("/api/data-sources", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name,
        source_type: "postgres_connection",
        config: publicConfig,
        credential,
      }),
      fallback: "create failed",
    });
    if (result.ok) {
      await fetchWorkspaceDataSources();
    }
    return result;
  }

  async function updatePostgresSource(sourceId, name, config) {
    const { publicConfig, credential } = splitPostgresConfig(config);
    const payload = { name, config: publicConfig };
    if (credential.password) {
      payload.credential = credential;
    }
    const result = await requestJSON(`/api/data-sources/${sourceId}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      fallback: "update failed",
    });
    if (result.ok) {
      await fetchWorkspaceDataSources();
    }
    return result;
  }

  async function deleteWorkspaceSource(sourceId) {
    const result = await requestJSON(`/api/data-sources/${sourceId}`, {
      method: "DELETE",
      fallback: "delete failed",
    });
    if (result.ok) {
      await fetchWorkspaceDataSources();
    }
    return result;
  }

  async function testConnection(sourceId) {
    const result = await requestJSON(`/api/data-sources/${sourceId}/test`, {
      method: "POST",
      fallback: "请求失败",
    });
    return result.ok ? result.data : { success: false, message: result.error };
  }

  async function fetchSourceCatalog(sourceId) {
    return requestJSON(`/api/data-sources/${sourceId}/catalog`, {
      fallback: "加载可导入对象失败",
    });
  }

  async function importFromSource(sourceId, sessionId, schemaName, objectName) {
    const result = await requestJSON(`/api/data-sources/${sourceId}/import`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        session_id: sessionId,
        schema_name: schemaName,
        object_name: objectName,
      }),
      fallback: "import failed",
    });
    if (result.ok) {
      await fetchSessionSources(sessionId);
      return result.data;
    }
    return result;
  }

  async function removeSessionSource(sessionId, sourceId, sourceObjectKey) {
    const params = new URLSearchParams({
      source_object_key: sourceObjectKey || "",
    });
    const result = await requestJSON(
      `/api/sessions/${sessionId}/sources/${sourceId}?${params.toString()}`,
      {
        method: "DELETE",
        fallback: "remove failed",
      },
    );
    if (result.ok) {
      await fetchSessionSources(sessionId);
    }
    return result;
  }

  function splitPostgresConfig(config = {}) {
    const { password = "", ...publicConfig } = config || {};
    return {
      publicConfig,
      credential: { password },
    };
  }

  function getAuthHeaders() {
    const token = localStorage.getItem("oda_token");
    return token ? { Authorization: `Bearer ${token}` } : {};
  }

  async function readResponseError(res, fallback) {
    const body = await res.text().catch(() => "");
    const message = body.trim() || `${fallback} (HTTP ${res.status})`;
    return message.replace(/\s+$/g, "");
  }

  async function requestJSON(url, { fallback, headers = {}, ...options } = {}) {
    try {
      const res = await fetch(url, {
        ...options,
        headers: { ...getAuthHeaders(), ...headers },
      });
      if (!res.ok) {
        return { ok: false, error: await readResponseError(res, fallback) };
      }
      return { ok: true, data: await readResponseJSON(res) };
    } catch (e) {
      return { ok: false, error: e.message || "network error" };
    }
  }

  async function withLoading(operation) {
    loading.value = true;
    lastError.value = "";
    try {
      const result = await operation();
      if (result?.ok === false) {
        lastError.value = result.error || "request failed";
      }
      return result;
    } finally {
      loading.value = false;
    }
  }

  async function readResponseJSON(res) {
    if (res.status === 204) return null;
    if (typeof res.text !== "function") {
      return typeof res.json === "function" ? await res.json() : null;
    }
    const body = await res.text();
    return body.trim() ? JSON.parse(body) : null;
  }

  return {
    sessionSources,
    workspaceDataSources,
    semanticProfileSummaries,
    semanticProfileDetails,
    loading,
    lastError,
    fetchSessionSources,
    fetchWorkspaceDataSources,
    fetchProfileDetail,
    confirmProfile,
    createPostgresSource,
    updatePostgresSource,
    deleteWorkspaceSource,
    testConnection,
    fetchSourceCatalog,
    importFromSource,
    removeSessionSource,
    splitPostgresConfig,
  };
});
