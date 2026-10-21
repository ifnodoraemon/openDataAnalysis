package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// recordingDelegatePersistence accepts the child run lifecycle without a store.
type recordingDelegatePersistence struct{ childRuns int }

func (p *recordingDelegatePersistence) StartChildRun(context.Context, ChildRunStart) (string, error) {
	p.childRuns++
	return fmt.Sprintf("r_child_%d", p.childRuns), nil
}
func (p *recordingDelegatePersistence) AppendChildEvent(context.Context, string, RuntimeEvent) error {
	return nil
}
func (p *recordingDelegatePersistence) UpdateChildRunStatus(context.Context, string, string, *string) error {
	return nil
}
func (p *recordingDelegatePersistence) UpdateChildRunSummary(context.Context, string, string) error {
	return nil
}
func (p *recordingDelegatePersistence) UpdateChildRunTokens(context.Context, string, int, int) error {
	return nil
}

// A continued sub-agent must answer from the conversation it already had. If
// the child's history were dropped, the follow-up would be a fresh delegation
// wearing the same id.
func TestDelegateContinuesExistingSubagentConversation(t *testing.T) {
	server, requests := scriptedLLMServer(t, []string{
		responsesAPITextBody(t, "第一轮：字段口径确认为含税收入"),
		responsesAPITextBody(t, "第二轮：基于已确认口径，收入为 100"),
	})
	installTestLLMConfig(t, server)

	registry := tools.NewRegistry()
	registry.Register(&runtimeAwareTestTool{})

	children := NewChildAgentRegistry()
	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(registry),
		Children:        children,
		EmitFunc:        func(RuntimeEvent) {},
		ParentContext:   WithDelegateRunPersistence(context.Background(), &recordingDelegatePersistence{}),
	}

	first, err := tool.Execute(json.RawMessage(`{
		"action":"start",
		"role_name":"analyst",
		"task_instruction":"确认字段口径",
		"allowed_tools":["runtime_aware_test_tool"]
	}`))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var firstPayload map[string]interface{}
	if err := json.Unmarshal([]byte(first), &firstPayload); err != nil {
		t.Fatalf("start payload: %v", err)
	}
	if firstPayload["ok"] != true {
		t.Fatalf("expected a successful start, got %#v", firstPayload)
	}
	subagentID, _ := firstPayload["subagent_id"].(string)
	if subagentID == "" {
		t.Fatalf("a started sub-agent must be addressable for a follow-up: %#v", firstPayload)
	}

	second, err := tool.Execute(json.RawMessage(fmt.Sprintf(`{
		"action":"continue",
		"subagent_id":%q,
		"task_instruction":"给出结论"
	}`, subagentID)))
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	var secondPayload map[string]interface{}
	if err := json.Unmarshal([]byte(second), &secondPayload); err != nil {
		t.Fatalf("continue payload: %v", err)
	}
	if secondPayload["ok"] != true {
		t.Fatalf("expected a successful continue, got %#v", secondPayload)
	}
	if got, _ := secondPayload["subagent_id"].(string); got != subagentID {
		t.Fatalf("continue must reuse the same conversation, got %q want %q", got, subagentID)
	}

	// The second request carries the first turn: the instruction and the answer.
	captured := requests()
	if len(captured) < 2 {
		t.Fatalf("expected two child requests, got %d", len(captured))
	}
	secondRequest := string(captured[1])
	for _, want := range []string{"确认字段口径", "第一轮：字段口径确认为含税收入", "给出结论"} {
		if !strings.Contains(secondRequest, want) {
			t.Fatalf("continued request is missing %q; history was not reused:\n%s", want, secondRequest)
		}
	}

	// The stored conversation keeps both turns for a later continue.
	stored, err := children.Get(subagentID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.History) < 4 {
		t.Fatalf("expected both turns in the stored conversation, got %d items", len(stored.History))
	}
}

// Continuing an unknown id must say so instead of silently starting over.
func TestDelegateContinueRejectsUnknownSubagent(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&runtimeAwareTestTool{})
	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(registry),
		Children:        NewChildAgentRegistry(),
		EmitFunc:        func(RuntimeEvent) {},
		ParentContext:   context.Background(),
	}

	result, err := tool.Execute(json.RawMessage(`{"action":"continue","subagent_id":"sub_missing","task_instruction":"继续"}`))
	if err != nil {
		t.Fatalf("expected a structured failure, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["error_code"] != "unknown_subagent" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}
