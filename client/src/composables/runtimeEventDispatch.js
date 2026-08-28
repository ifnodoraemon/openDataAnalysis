export function shouldApplyReportEvent(eventRunId, store) {
  if (!eventRunId) return true;
  if (!store.selectedRunId) return true;
  if (store.selectedRunId === eventRunId) return true;

  const selectedRun = store.getRun(store.selectedRunId);
  const eventRun = store.getRun(eventRunId);
  if (eventRun?.parentRunId === store.selectedRunId) return true;
  if (selectedRun?.parentRunId === eventRunId) return true;

  return false;
}

export function shouldShowRunEvent(eventRunId, store) {
  return (
    !store.selectedRunId || !eventRunId || eventRunId === store.selectedRunId
  );
}

export function clipPreviewText(input, max = 120) {
  const text = String(input || "")
    .trim()
    .replace(/\s+/g, " ");
  if (!text) return "";
  return text.length > max ? `${text.slice(0, max)}...` : text;
}

export function summarizeEventForPreview(event) {
  switch (event.type) {
    case "assistant_status":
      return clipPreviewText(event.data?.content);
    case "tool_call":
      return event.data?.name || "工具调用";
    case "tool_result": {
      const raw = event.data?.result || "";
      try {
        const parsed = JSON.parse(raw);
        return clipPreviewText(
          parsed.ui_summary || `${event.data?.name || "工具"}已返回结果`,
        );
      } catch {
        return clipPreviewText(
          `${event.data?.name || "工具"}已返回非结构化结果`,
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

export function appendRunPreview(event, store) {
  if (!event.runId) return;
  const summary = summarizeEventForPreview(event);
  if (!summary) return;
  store.appendRunPreview(event.runId, {
    type: event.type,
    name: event.data?.name,
    summary,
  });
}

export function applyRuntimeState(runtimeState, store) {
  store.setSubgoals(runtimeState?.subgoals || []);
  store.setMemoryEntries(runtimeState?.memory_entries || {});
  store.updateReport(runtimeState?.report_html || "");
  store.setReportEditState(runtimeState?.edit_state || null);
}

export function deserializeRunMessages(messages) {
  return (messages || []).map((message) => {
    const item = {
      id: message.id,
      type: message.type,
      name: message.name,
      content: message.content,
      tool_call_id: message.toolCallId,
      duration: message.duration,
      success: message.success,
      timestamp: message.createdAt
        ? new Date(message.createdAt).toLocaleTimeString()
        : "",
    };
    if (message.type === "tool_call") {
      item.arguments = message.content;
    }
    if (message.type === "tool_result") {
      item.result = message.content;
      try {
        item.parsedResult = JSON.parse(message.content);
      } catch {
        item.parsedResult = null;
      }
    }
    return item;
  });
}

export function handleEvent(event, store) {
  if (event.sessionId && store.sessionId && event.sessionId !== store.sessionId)
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
    case "run_started":
      store.startRun(event.data.runId);
      store.upsertRun({
        id: event.data.runId,
        sessionId: store.sessionId,
        status: "running",
        inputMessage:
          store.messages.filter((msg) => msg.type === "user").at(-1)?.content ||
          "",
        createdAt: new Date().toISOString(),
      });
      break;
    case "assistant_status":
      appendRunPreview(event, store);
      if (!shouldShowRunEvent(event.runId, store)) break;
      store.addMessage({
        type: "assistant_status",
        content: event.data.content,
      });
      break;
    case "tool_call":
      appendRunPreview(event, store);
      if (!shouldShowRunEvent(event.runId, store)) break;
      store.addMessage({
        type: "tool_call",
        name: event.data.name,
        arguments: event.data.arguments,
        id: event.data.id,
      });
      break;
    case "tool_result": {
      appendRunPreview(event, store);
      if (!shouldShowRunEvent(event.runId, store)) break;
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
      if (shouldApplyReportEvent(event.runId, store)) {
        store.updateReport(event.data.html);
      }
      break;
    case "report_final":
      if (shouldApplyReportEvent(event.runId, store)) {
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
      appendRunPreview(event, store);
      if (shouldShowRunEvent(event.runId, store) && event.data.summary) {
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
      appendRunPreview(event, store);
      if (shouldShowRunEvent(event.runId, store)) {
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
      appendRunPreview(event, store);
      if (shouldShowRunEvent(event.runId, store)) {
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
      appendRunPreview(event, store);
      if (!shouldShowRunEvent(event.runId, store)) break;
      store.addMessage({
        type: "user_request_input",
        question: event.data.question,
        reason: event.data.reason,
        scope: event.data.scope,
        context_ref: event.data.context_ref,
        input_hint: event.data.input_hint,
        required: event.data.required,
        selection_mode: event.data.selection_mode,
        allow_custom: event.data.allow_custom,
        options: event.data.options || [],
        authorization: event.data.authorization || null,
      });
      break;
    case "state_subgoals_updated":
      if (event.data?.goals) store.setSubgoals(event.data.goals);
      break;
    case "state_memory_updated":
      if (event.data?.entries) store.setMemoryEntries(event.data.entries);
      break;
    case "state_report_edit_updated":
      store.setReportEditState(event.data || null);
      break;
    case "state_child_runs_updated":
      if (event.data?.childRuns)
        store.setRunChildren(event.data.parentRunId, event.data.childRuns);
      break;
    default:
      console.debug("收到未知的运行事件类型", event.type);
      break;
  }
}
