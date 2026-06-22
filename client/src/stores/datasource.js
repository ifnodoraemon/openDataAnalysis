import { defineStore } from "pinia";
import { ref } from "vue";

export const useDataSourceStore = defineStore("dataSource", () => {
  const sessionSources = ref([]);
  const workspaceDataSources = ref([]);
  const semanticProfileSummaries = ref([]);
  const semanticProfileDetails = ref({});
  const loading = ref(false);

  async function fetchSessionSources(sessionId) {
    if (!sessionId) return;
    loading.value = true;
    try {
      const res = await fetch(`/api/sessions/${sessionId}/sources`, {
        headers: getAuthHeaders(),
      });
      if (res.ok) {
        const data = await res.json();
        sessionSources.value = data.sources || [];
        semanticProfileSummaries.value = (data.profiles || []).map((p) => ({
          profile_id: p.profile_id,
          source_id: p.source_id,
          analysis_table_name: p.analysis_table_name,
          profile_status: p.profile_status,
          schema_signature: p.schema_signature,
        }));
      }
    } finally {
      loading.value = false;
    }
  }

  async function fetchWorkspaceDataSources() {
    loading.value = true;
    try {
      const res = await fetch("/api/data-sources", {
        headers: getAuthHeaders(),
      });
      if (res.ok) {
        const data = await res.json();
        workspaceDataSources.value = data.data_sources || [];
      }
    } finally {
      loading.value = false;
    }
  }

  async function fetchProfileDetail(profileId) {
    if (!profileId) return;
    loading.value = true;
    try {
      const res = await fetch(`/api/semantic-profiles/${profileId}`, {
        headers: getAuthHeaders(),
      });
      if (res.ok) {
        const data = await res.json();
        semanticProfileDetails.value[profileId] = data;
      }
    } finally {
      loading.value = false;
    }
  }

  async function confirmProfile(profileId, scope, overrides) {
    const res = await fetch(`/api/semantic-profiles/${profileId}/confirm`, {
      method: "POST",
      headers: { ...getAuthHeaders(), "Content-Type": "application/json" },
      body: JSON.stringify({ scope, overrides }),
    });
    if (!res.ok) {
      const errBody = await res.text().catch(() => "");
      return {
        ok: false,
        error: errBody || `confirm failed (HTTP ${res.status})`,
      };
    }
    return { ok: true, data: await res.json() };
  }

  async function createPostgresSource(name, config) {
    const res = await fetch("/api/data-sources", {
      method: "POST",
      headers: { ...getAuthHeaders(), "Content-Type": "application/json" },
      body: JSON.stringify({
        name,
        source_type: "postgres_connection",
        postgres: config,
      }),
    });
    if (res.ok) {
      await fetchWorkspaceDataSources();
      return { ok: true, data: await res.json() };
    }
    const errBody = await res.text().catch(() => "");
    return {
      ok: false,
      error: errBody || `create failed (HTTP ${res.status})`,
    };
  }

  async function updatePostgresSource(sourceId, name, config) {
    const res = await fetch(`/api/data-sources/${sourceId}`, {
      method: "PUT",
      headers: { ...getAuthHeaders(), "Content-Type": "application/json" },
      body: JSON.stringify({ name, postgres: config }),
    });
    if (!res.ok) {
      const errBody = await res.text().catch(() => "");
      return {
        ok: false,
        error: errBody || `update failed (HTTP ${res.status})`,
      };
    }
    await fetchWorkspaceDataSources();
    return { ok: true, data: await res.json() };
  }

  async function deleteWorkspaceSource(sourceId) {
    const res = await fetch(`/api/data-sources/${sourceId}`, {
      method: "DELETE",
      headers: getAuthHeaders(),
    });
    if (!res.ok) {
      const errBody = await res.text().catch(() => "");
      return {
        ok: false,
        error: errBody || `delete failed (HTTP ${res.status})`,
      };
    }
    await fetchWorkspaceDataSources();
    return { ok: true };
  }

  async function testConnection(sourceId) {
    const res = await fetch(`/api/data-sources/${sourceId}/test`, {
      method: "POST",
      headers: getAuthHeaders(),
    });
    return res.ok ? await res.json() : { success: false, message: "请求失败" };
  }

  async function importFromSource(sourceId, sessionId, schemaName, objectName) {
    try {
      const res = await fetch(`/api/data-sources/${sourceId}/import`, {
        method: "POST",
        headers: { ...getAuthHeaders(), "Content-Type": "application/json" },
        body: JSON.stringify({
          session_id: sessionId,
          schema_name: schemaName,
          object_name: objectName,
        }),
      });
      if (res.ok) {
        await fetchSessionSources(sessionId);
        return await res.json();
      }
      const errBody = await res.text().catch(() => "");
      return {
        ok: false,
        error: errBody || `import failed (HTTP ${res.status})`,
      };
    } catch (e) {
      return { ok: false, error: e.message || "network error" };
    }
  }

  async function removeSessionSource(sessionId, sourceId, sourceObjectKey) {
    const params = new URLSearchParams({
      source_object_key: sourceObjectKey || "",
    });
    const res = await fetch(
      `/api/sessions/${sessionId}/sources/${sourceId}?${params.toString()}`,
      {
        method: "DELETE",
        headers: getAuthHeaders(),
      },
    );
    if (!res.ok) {
      const errBody = await res.text().catch(() => "");
      return {
        ok: false,
        error: errBody || `remove failed (HTTP ${res.status})`,
      };
    }
    await fetchSessionSources(sessionId);
    return { ok: true };
  }

  function getAuthHeaders() {
    const token = localStorage.getItem("oda_token");
    return token ? { Authorization: `Bearer ${token}` } : {};
  }

  return {
    sessionSources,
    workspaceDataSources,
    semanticProfileSummaries,
    semanticProfileDetails,
    loading,
    fetchSessionSources,
    fetchWorkspaceDataSources,
    fetchProfileDetail,
    confirmProfile,
    createPostgresSource,
    updatePostgresSource,
    deleteWorkspaceSource,
    testConnection,
    importFromSource,
    removeSessionSource,
  };
});
