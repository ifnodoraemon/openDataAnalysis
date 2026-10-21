package agent

import "testing"

// askSuspendedEngine builds an engine whose history ends with an unanswered
// user_request_input call, exactly as the suspend path leaves it.
func askSuspendedEngine(t *testing.T) *Engine {
	t.Helper()
	calls := []LLMToolCall{{
		ID: "call_ask",
		Function: LLMFunctionCall{
			Name:      "user_request_input",
			Arguments: `{"question":"采用哪个口径？","required":true,"selection_mode":"single","allow_custom":true}`,
		},
	}}
	engine := &Engine{
		registry:             askUserTestRegistry(),
		confirmationReceipts: make(map[string]*ConfirmationReceipt),
		history:              []ConversationItem{{Role: LLMRoleAssistant, ToolCalls: calls}},
	}
	engine.pendingSuspensionID = "call_ask"
	return engine
}

// The run that suspends ends through the same deferred path that repairs
// dangling tool calls. Repair must not answer the pending question, otherwise
// the user's reply is rejected as a duplicate.
func TestRepairKeepsPendingSuspensionUnanswered(t *testing.T) {
	t.Parallel()

	engine := askSuspendedEngine(t)
	engine.repairDanglingToolCalls()

	if len(engine.history) != 1 {
		t.Fatalf("a pending suspension must not gain a synthetic result, got %#v", engine.history)
	}

	answer := `{"response_type":"selection","selected_option_ids":[],"custom_response":"用毛利口径"}`
	if _, err := engine.ProvideAskUserResult(answer, "user_1"); err != nil {
		t.Fatalf("resuming a suspended run must accept the answer: %v", err)
	}
	if len(engine.history) != 2 || engine.history[1].Role != LLMRoleTool || engine.history[1].ToolCallID != "call_ask" {
		t.Fatalf("expected the answer to be recorded as the tool result, got %#v", engine.history)
	}
	if engine.pendingSuspensionID != "" {
		t.Fatalf("a supplied answer must clear the pending suspension, got %q", engine.pendingSuspensionID)
	}
}

// An abandoned suspension is repaired: the model is told the outcome is
// unknown instead of the conversation carrying an unanswered call forever.
func TestAbandonedSuspensionGetsUnknownOutcome(t *testing.T) {
	t.Parallel()

	engine := askSuspendedEngine(t)
	engine.clearAbandonedSuspension()

	if len(engine.history) != 2 || engine.history[1].Role != LLMRoleTool {
		t.Fatalf("expected the abandoned call to be paired, got %#v", engine.history)
	}
	if engine.pendingSuspensionID != "" {
		t.Fatalf("the abandoned mark must be cleared, got %q", engine.pendingSuspensionID)
	}
}

// A dangling call that is not a suspension still gets a synthetic outcome, so
// the tool-call protocol stays valid for the next request.
func TestDanglingNonSuspensionCallIsRepaired(t *testing.T) {
	t.Parallel()

	engine := &Engine{
		registry: askUserTestRegistry(),
		history: []ConversationItem{{
			Role:      LLMRoleAssistant,
			ToolCalls: []LLMToolCall{{ID: "call_x", Function: LLMFunctionCall{Name: "data_query_sql", Arguments: `{}`}}},
		}},
	}
	engine.repairDanglingToolCalls()

	if len(engine.history) != 2 || engine.history[1].Role != LLMRoleTool {
		t.Fatalf("expected the dangling call to be paired, got %#v", engine.history)
	}
}

// Cold start: the question is the last thing in the log and was never
// answered. A rebuilt history must keep it pending so the answer sent to a
// restarted process is accepted instead of rejected as a duplicate.
func TestRebuiltHistoryAcceptsAnswerForPendingQuestion(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "分析销售数据").
		add("r1", EventAssistantStatus, "需要确认口径").
		addTool("r1", EventUserRequestInput, "", "call_ask",
			`{"question":"采用哪个口径？","tool_call_id":"call_ask"}`)

	items, _, _, err := RebuildHistoryFromMessages(log.messages)
	if err != nil {
		t.Fatalf("RebuildHistoryFromMessages: %v", err)
	}
	for _, item := range items {
		if item.Role == LLMRoleTool {
			t.Fatalf("a pending question must not be answered by rebuild: %#v", items)
		}
	}

	engine := &Engine{
		registry:             askUserTestRegistry(),
		confirmationReceipts: make(map[string]*ConfirmationReceipt),
	}
	engine.SetHistory(items, 0)
	answer := `{"response_type":"selection","selected_option_ids":[],"custom_response":"用毛利口径"}`
	if _, err := engine.ProvideAskUserResult(answer, "user_1"); err != nil {
		t.Fatalf("a restarted process must accept the answer: %v", err)
	}
	if last := engine.history[len(engine.history)-1]; last.Role != LLMRoleTool || last.ToolCallID != "call_ask" {
		t.Fatalf("expected the answer recorded against the pending call, got %#v", last)
	}
}
