global.localStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
  clear: () => {},
};

import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { useAgentStore } from "../stores/agent.js";
import { deserializeRunMessages, handleEvent } from "./runtimeEventDispatch.js";

describe("runtimeEventDispatch", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("applies the canonical run_started event", () => {
    const store = useAgentStore();
    store.setSession("session-1");

    handleEvent(
      {
        type: "run_started",
        sessionId: "session-1",
        runId: "run-1",
        data: { runId: "run-1" },
      },
      store,
    );

    expect(store.activeRunId).toBe("run-1");
    expect(store.getRun("run-1")).toMatchObject({
      id: "run-1",
      sessionId: "session-1",
      status: "running",
    });
  });

  it("preserves the authorization payload on user_request_input", () => {
    const store = useAgentStore();
    store.setSession("session-1");

    const authorization = {
      action: "execute_sql",
      resource_ref: "workspace/demo/orders",
      risk: "write",
    };

    handleEvent(
      {
        type: "user_request_input",
        sessionId: "session-1",
        runId: "run-1",
        data: {
          question: "是否执行该写操作？",
          reason: "涉及数据修改",
          scope: "workspace",
          context_ref: "ctx-1",
          input_hint: "确认或取消",
          required: true,
          selection_mode: "single",
          allow_custom: false,
          options: [{ id: "approve", label: "同意执行" }],
          authorization,
        },
      },
      store,
    );

    const message = store.messages.at(-1);
    expect(message).toMatchObject({
      type: "user_request_input",
      question: "是否执行该写操作？",
      reason: "涉及数据修改",
      scope: "workspace",
      context_ref: "ctx-1",
      input_hint: "确认或取消",
      required: true,
      selection_mode: "single",
      allow_custom: false,
      options: [{ id: "approve", label: "同意执行" }],
    });
    expect(message.authorization).toEqual(authorization);
  });

  it("maps persisted tool messages without guessing fields", () => {
    const messages = deserializeRunMessages([
      {
        id: "message-1",
        type: "tool_result",
        name: "data_query_sql",
        toolCallId: "call-1",
        content: '{"ok":true,"ui_summary":"查询完成"}',
        success: true,
        duration: 12,
        createdAt: "2026-08-11T00:00:00Z",
      },
    ]);

    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({
      id: "message-1",
      type: "tool_result",
      tool_call_id: "call-1",
      result: '{"ok":true,"ui_summary":"查询完成"}',
      parsedResult: { ok: true, ui_summary: "查询完成" },
    });
  });
});
