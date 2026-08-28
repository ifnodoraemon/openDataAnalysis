import { defineStore } from "pinia";
import { ref } from "vue";
import { useAgentStore } from "./agent.js";

export const useDataSourceStore = defineStore("dataSource", () => {
  const sessionSources = ref([]);
  const workspaceDataSources = ref([]);
  const sourceTypes = ref([]);
  const semanticProfileSummaries = ref([]);
  const semanticProfileDetails = ref({});
  const loading = ref(false);
  const lastError = ref("");

  async function fetchSessionSources(sessionId) {
    if (!sessionId) return { ok: false, error: "缺少会话 ID" };
    return withLoading(async () => {
      const result = await requestJSON(`/api/sessions/${sessionId}/sources`, {
        defaultMessage: "加载会话数据源失败",
      });
      if (!result.ok) return result;

      const data = result.data || {};
      const sources = data.sources || [];
      sessionSources.value = sources;
      const profiles =
        data.profiles ||
        sources.filter((source) => source.profile_id || source.profile_status);
      semanticProfileSummaries.value = profiles.map((p) => ({
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
        defaultMessage: "加载工作区数据源失败",
      });
      if (!result.ok) return result;

      const data = result.data || {};
      workspaceDataSources.value = data.data_sources || [];
      return result;
    });
  }

  async function fetchSourceTypes() {
    return withLoading(async () => {
      const result = await requestJSON("/api/data-source-types", {
        defaultMessage: "加载数据源类型失败",
      });
      if (!result.ok) return result;

      sourceTypes.value = result.data?.source_types || [];
      return result;
    });
  }

  async function fetchProfileDetail(profileId) {
    if (!profileId) return { ok: false, error: "缺少画像 ID" };
    return withLoading(async () => {
      const result = await requestJSON(`/api/semantic-profiles/${profileId}`, {
        defaultMessage: "加载数据画像失败",
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
      defaultMessage: "确认画像补丁失败",
    });
  }

  async function createSQLSource(name, sourceType, config) {
    const { publicConfig, credential } = splitSQLConfig(config);
    const result = await requestJSON("/api/data-sources", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name,
        source_type: sourceType,
        config: publicConfig,
        credential,
      }),
      defaultMessage: "创建数据源失败",
    });
    if (result.ok) {
      await fetchWorkspaceDataSources();
    }
    return result;
  }

  async function updateSQLSource(sourceId, name, config) {
    const { publicConfig, credential } = splitSQLConfig(config);
    const payload = { name, config: publicConfig };
    if (credential.password) {
      payload.credential = credential;
    }
    const result = await requestJSON(`/api/data-sources/${sourceId}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      defaultMessage: "更新数据源失败",
    });
    if (result.ok) {
      await fetchWorkspaceDataSources();
    }
    return result;
  }

  async function deleteWorkspaceSource(sourceId) {
    const result = await requestJSON(`/api/data-sources/${sourceId}`, {
      method: "DELETE",
      defaultMessage: "删除数据源失败",
    });
    if (result.ok) {
      await fetchWorkspaceDataSources();
    }
    return result;
  }

  async function testConnection(sourceId) {
    const result = await requestJSON(`/api/data-sources/${sourceId}/test`, {
      method: "POST",
      defaultMessage: "请求失败",
    });
    return result.ok ? result.data : { success: false, error: result.error };
  }

  async function fetchSourceCatalog(sourceId) {
    return requestJSON(`/api/data-sources/${sourceId}/catalog`, {
      defaultMessage: "加载可导入对象失败",
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
      defaultMessage: "导入数据失败",
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
        defaultMessage: "移除会话数据源失败",
      },
    );
    if (result.ok) {
      await fetchSessionSources(sessionId);
    }
    return result;
  }

  function splitSQLConfig(config = {}) {
    const password = config?.password || "";
    const publicConfig = {
      host: config?.host,
      port: config?.port,
      database_name: config?.database_name,
      username: config?.username,
      allowlist: config?.allowlist,
    };
    if (config?.security_mode_field && config?.security_mode) {
      publicConfig[config.security_mode_field] = config.security_mode;
    }
    return {
      publicConfig,
      credential: { password },
    };
  }

  function getAuthHeaders() {
    const token = localStorage.getItem("oda_token");
    return token ? { Authorization: `Bearer ${token}` } : {};
  }

  async function readResponseError(res, defaultMessage) {
    const body = await res.text().catch(() => "");
    const message = body.trim() || `${defaultMessage} (HTTP ${res.status})`;
    return message.replace(/\s+$/g, "");
  }

  async function requestJSON(
    url,
    { defaultMessage, headers = {}, ...options } = {},
  ) {
    try {
      const res = await fetch(url, {
        ...options,
        headers: { ...getAuthHeaders(), ...headers },
      });
      if (!res.ok) {
        if (res.status === 401) {
          useAgentStore().logout();
        }
        return {
          ok: false,
          error: await readResponseError(res, defaultMessage),
        };
      }
      return { ok: true, data: await readResponseJSON(res) };
    } catch (e) {
      return { ok: false, error: e.message || "网络请求失败" };
    }
  }

  async function withLoading(operation) {
    loading.value = true;
    lastError.value = "";
    try {
      const result = await operation();
      if (result?.ok === false) {
        lastError.value = result.error || "请求失败";
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
    sourceTypes,
    semanticProfileSummaries,
    semanticProfileDetails,
    loading,
    lastError,
    fetchSessionSources,
    fetchWorkspaceDataSources,
    fetchSourceTypes,
    fetchProfileDetail,
    confirmProfile,
    createSQLSource,
    updateSQLSource,
    deleteWorkspaceSource,
    testConnection,
    fetchSourceCatalog,
    importFromSource,
    removeSessionSource,
    splitSQLConfig,
  };
});
