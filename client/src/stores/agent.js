import { defineStore } from "pinia";
import { ref } from "vue";

const MAX_MESSAGES = 500;

export const useAgentStore = defineStore("agent", () => {
  const messages = ref([]);
  const reportHTML = ref("");
  const isRunning = ref(false);
  const token = ref(localStorage.getItem("oda_token") || "");
  const sessionId = ref("");
  const activeRunId = ref("");
  const selectedRunId = ref("");
  const connectionState = ref("disconnected");
  const bootstrapState = ref("idle");
  const bootstrapError = ref("");
  const user = ref(null);
  const workspace = ref(null);
  const workspaces = ref([]);
  const sessions = ref([]);
  const runs = ref([]);
  const subgoals = ref([]);
  const memoryFacts = ref({});
  const reportQuote = ref(null);
  const reportEditState = ref(null);

  function findRunById(items, runId) {
    for (const item of items || []) {
      if (item.id === runId) return item;
      const nested = findRunById(item.childRuns || [], runId);
      if (nested) return nested;
    }
    return null;
  }

  function patchRunInTree(items, runId, patch) {
    return (items || []).map((item) => {
      if (item.id === runId) {
        return { ...item, ...patch };
      }
      if (item.childRuns?.length) {
        return {
          ...item,
          childRuns: patchRunInTree(item.childRuns, runId, patch),
        };
      }
      return item;
    });
  }

  function replaceRunInTree(items, nextRun) {
    return (items || []).map((item) => {
      if (item.id === nextRun.id) {
        return { ...item, ...nextRun };
      }
      if (item.childRuns?.length) {
        return {
          ...item,
          childRuns: replaceRunInTree(item.childRuns, nextRun),
        };
      }
      return item;
    });
  }

  function setChildRunsInTree(items, parentRunId, childRuns) {
    return (items || []).map((item) => {
      if (item.id === parentRunId) {
        return { ...item, childRuns: childRuns || [] };
      }
      if (item.childRuns?.length) {
        return {
          ...item,
          childRuns: setChildRunsInTree(item.childRuns, parentRunId, childRuns),
        };
      }
      return item;
    });
  }

  function insertRunUnderParent(items, parentRunId, run) {
    return (items || []).map((item) => {
      if (item.id === parentRunId) {
        const existingChildren = item.childRuns || [];
        const nextChildren = [...existingChildren, run];
        return { ...item, childRuns: nextChildren };
      }
      if (item.childRuns?.length) {
        return {
          ...item,
          childRuns: insertRunUnderParent(item.childRuns, parentRunId, run),
        };
      }
      return item;
    });
  }

  let _msgSeq = 0;

  function addMessage(msg) {
    messages.value.push({
      ...msg,
      id: `msg_${Date.now()}_${++_msgSeq}`,
      timestamp: new Date().toLocaleTimeString(),
    });
    if (messages.value.length > MAX_MESSAGES) {
      messages.value = messages.value.slice(-MAX_MESSAGES);
    }
  }

  function updateReport(html) {
    reportHTML.value = html;
  }

  function setReportQuote(quote) {
    if (!quote || !String(quote.selectionText || "").trim()) {
      reportQuote.value = null;
      return;
    }
    reportQuote.value = {
      mode: quote.mode || "regenerate_selection",
      targetRunId: quote.targetRunId || "",
      blockId: quote.blockId || "",
      blockLabel: quote.blockLabel || "",
      selectionText: String(quote.selectionText || "").trim(),
      selectionStart: Number.isInteger(quote.selectionStart)
        ? quote.selectionStart
        : null,
      selectionEnd: Number.isInteger(quote.selectionEnd)
        ? quote.selectionEnd
        : null,
      selectionRangeSet: quote.selectionRangeSet === true,
      preserveOtherBlocks: quote.preserveOtherBlocks !== false,
    };
  }

  function clearReportQuote() {
    reportQuote.value = null;
  }

  function setReportEditState(state) {
    if (!state || state.active !== true || !state.editContext) {
      reportEditState.value = null;
      reportQuote.value = null;
      return;
    }
    reportEditState.value = {
      active: true,
      scopeKind: state.scopeKind || "",
      editContext: { ...state.editContext },
    };
    if (
      !isRunning.value &&
      state.scopeKind === "partial_selection" &&
      String(state.editContext.selectionText || "").trim()
    ) {
      setReportQuote(state.editContext);
      return;
    }
    reportQuote.value = null;
  }

  function setRunning(val) {
    isRunning.value = val;
  }

  function setSession(id) {
    sessionId.value = id;
  }

  function setSelectedRun(runId) {
    selectedRunId.value = runId || "";
  }

  function setIdentity(nextUser, nextWorkspace) {
    user.value = nextUser;
    workspace.value = nextWorkspace;
  }

  function setWorkspace(nextWorkspace) {
    workspace.value = nextWorkspace;
  }

  function setToken(nextToken) {
    token.value = nextToken;
    if (nextToken) {
      localStorage.setItem("oda_token", nextToken);
    } else {
      localStorage.removeItem("oda_token");
    }
  }

  function setWorkspaces(items) {
    workspaces.value = items || [];
  }

  function setSessions(items) {
    sessions.value = items || [];
  }

  function upsertSession(session) {
    if (!session?.id) return;
    const index = sessions.value.findIndex((item) => item.id === session.id);
    if (index >= 0) {
      sessions.value.splice(index, 1, { ...sessions.value[index], ...session });
      return;
    }
    sessions.value.unshift(session);
  }

  function upsertRun(run) {
    if (!run?.id) return;
    if (findRunById(runs.value, run.id)) {
      runs.value = replaceRunInTree(runs.value, run);
      return;
    }
    if (run.parentRunId && findRunById(runs.value, run.parentRunId)) {
      runs.value = insertRunUnderParent(runs.value, run.parentRunId, run);
      return;
    }
    runs.value.unshift(run);
  }

  function setRuns(items) {
    runs.value = items || [];
  }

  function setRunChildren(parentRunId, items) {
    if (!parentRunId) return;
    runs.value = setChildRunsInTree(runs.value, parentRunId, items || []);
  }

  function patchRun(runId, patch) {
    if (!runId) return false;
    if (!findRunById(runs.value, runId)) return false;
    runs.value = patchRunInTree(runs.value, runId, patch);
    return true;
  }

  function appendRunPreview(runId, preview, limit = 3) {
    if (!runId || !preview?.summary) return false;
    const run = findRunById(runs.value, runId);
    if (!run) return false;
    const nextPreview = [...(run.previewMessages || []), preview].slice(-limit);
    runs.value = patchRunInTree(runs.value, runId, {
      previewMessages: nextPreview,
      updatedAt: new Date().toISOString(),
    });
    return true;
  }

  function getRun(runId) {
    if (!runId) return null;
    return findRunById(runs.value, runId);
  }

  function setMessages(items) {
    messages.value = items || [];
  }

  function setSubgoals(items) {
    subgoals.value = items || [];
  }

  function setMemoryFacts(items) {
    memoryFacts.value = items || {};
  }

  function setConnectionState(state) {
    connectionState.value = state;
  }

  function setBootstrapState(state, error = "") {
    bootstrapState.value = state;
    bootstrapError.value = error;
  }

  function startRun(runId) {
    activeRunId.value = runId;
    selectedRunId.value = runId;
    isRunning.value = true;
  }

  function finishRun(runId = "") {
    if (!runId || !activeRunId.value || activeRunId.value === runId) {
      activeRunId.value = "";
      isRunning.value = false;
    }
  }

  function resetAnalysis() {
    messages.value = [];
    reportHTML.value = "";
    isRunning.value = false;
    activeRunId.value = "";
    selectedRunId.value = "";
    sessionId.value = "";
    runs.value = [];
    subgoals.value = [];
    memoryFacts.value = {};
    reportQuote.value = null;
    reportEditState.value = null;
  }

  function logout() {
    setToken("");
    user.value = null;
    workspace.value = null;
    workspaces.value = [];
    sessions.value = [];
    resetAnalysis();
    connectionState.value = "disconnected";
    bootstrapState.value = "idle";
    bootstrapError.value = "";
    _msgSeq = 0;
  }

  return {
    messages,
    reportHTML,
    isRunning,
    token,
    sessionId,
    activeRunId,
    selectedRunId,
    connectionState,
    bootstrapState,
    bootstrapError,
    user,
    workspace,
    workspaces,
    sessions,
    runs,
    subgoals,
    memoryFacts,
    reportQuote,
    reportEditState,
    addMessage,
    updateReport,
    setReportQuote,
    clearReportQuote,
    setReportEditState,
    setRunning,
    setSession,
    setSelectedRun,
    setIdentity,
    setWorkspace,
    setToken,
    setWorkspaces,
    setSessions,
    upsertSession,
    setRuns,
    setRunChildren,
    upsertRun,
    patchRun,
    appendRunPreview,
    getRun,
    setMessages,
    setSubgoals,
    setMemoryFacts,
    setConnectionState,
    setBootstrapState,
    startRun,
    finishRun,
    resetAnalysis,
    logout,
  };
});
