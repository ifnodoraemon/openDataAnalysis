package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type flakyTestTool struct {
	name       string
	mode       string
	idempotent bool
	failFirst  int
	calls      int
}

func (t *flakyTestTool) Name() string { return t.name }
func (t *flakyTestTool) Description() string {
	return "test tool that fails transiently a bounded number of times"
}
func (t *flakyTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *flakyTestTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: t.mode, Idempotent: t.idempotent}
}
func (t *flakyTestTool) Execute(json.RawMessage) (string, error) {
	t.calls++
	if t.calls <= t.failFirst {
		return "", &httpStatusError{Operation: "test", StatusCode: http.StatusServiceUnavailable}
	}
	return `{"ok":true,"tool":"` + t.name + `"}`, nil
}

// A read-only tool may be replayed after a transient failure; an action tool
// must not be, because a second execution can duplicate its effect.
func TestRetryableToolExecRetriesOnlyIdempotentTools(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	observe := &flakyTestTool{name: "observe_tool", mode: "observe", failFirst: 1}
	action := &flakyTestTool{name: "action_tool", mode: "action", failFirst: 1}
	explicit := &flakyTestTool{name: "explicit_tool", mode: "action", idempotent: true, failFirst: 1}
	registry.Register(observe)
	registry.Register(action)
	registry.Register(explicit)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := retryableToolExec(ctx, registry, "observe_tool", nil); err != nil {
		t.Fatalf("observe tool should recover after a transient failure: %v", err)
	}
	if observe.calls != 2 {
		t.Fatalf("expected the observe tool to be retried once, got %d calls", observe.calls)
	}

	if _, err := retryableToolExec(ctx, registry, "action_tool", nil); err == nil {
		t.Fatalf("expected the action tool failure to surface to the model")
	}
	if action.calls != 1 {
		t.Fatalf("an action tool must not be replayed, got %d calls", action.calls)
	}

	if _, err := retryableToolExec(ctx, registry, "explicit_tool", nil); err != nil {
		t.Fatalf("an explicitly idempotent tool should be retried: %v", err)
	}
	if explicit.calls != 2 {
		t.Fatalf("expected the explicit tool to be retried once, got %d calls", explicit.calls)
	}
}

func TestToolIsIdempotentDefaultsToObservationMode(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&flakyTestTool{name: "obs", mode: "observe"})
	registry.Register(&flakyTestTool{name: "act", mode: "action"})

	if !toolIsIdempotent(registry, "obs") {
		t.Fatalf("observation tools are idempotent by mode")
	}
	if toolIsIdempotent(registry, "act") {
		t.Fatalf("action tools must opt in explicitly")
	}
	if toolIsIdempotent(registry, "missing") {
		t.Fatalf("an unknown tool is not retryable")
	}
	if toolIsIdempotent(nil, "obs") {
		t.Fatalf("a nil registry is not retryable")
	}
}

// Cancelling a run in the middle of a tool batch must not leave the assistant
// tool call unanswered: the next request in the session would be invalid.
func TestRunRepairsDanglingToolCallAfterCancellation(t *testing.T) {
	server, _ := scriptedLLMServer(t, []string{responsesAPIToolCallBody(t, "cancel_test_tool")})
	installTestLLMConfig(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := tools.NewRegistry()
	registry.Register(&cancellingTestTool{cancel: cancel})

	engine := &Engine{llm: NewLLMClient(), registry: registry, policy: "system"}
	engine.Run(ctx, "分析数据", nil, func(RuntimeEvent) {})

	history := engine.History()
	if len(history) == 0 {
		t.Fatalf("expected history to record the cancelled exchange")
	}
	last := history[len(history)-1]
	if last.Role != LLMRoleTool {
		t.Fatalf("expected the dangling tool call to be paired with a result, got %#v", history)
	}
	if last.ToolCallID != "call_1" {
		t.Fatalf("expected the synthesized result to answer the recorded call, got %q", last.ToolCallID)
	}
	if !strings.Contains(last.Content, "tool_outcome_unknown") {
		t.Fatalf("expected an explicit unknown-outcome result, got %q", last.Content)
	}
}

// A long delegated run must not accumulate duplicate history_window facts, and
// its omission count must be cumulative.
func TestCompactWorkerBundleReplacesWindowFactAndAccumulates(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy: "system",
		RuntimeContext: []RuntimeContextBlock{
			{Name: "existing_fact", Role: "user", Content: `{"fact":"kept"}`},
		},
	}
	for i := 0; i < 40; i++ {
		bundle.History = append(bundle.History, ConversationItem{Role: LLMRoleUser, Content: strings.Repeat("a", 4000)})
	}

	omitted := compactWorkerBundle(bundle, contextCompactTriggerTokens()+1, 0)
	if omitted == 0 {
		t.Fatalf("expected the first compaction to omit messages")
	}
	omitted = compactWorkerBundle(bundle, contextCompactTriggerTokens()+1, omitted)

	windows := 0
	kept := 0
	for _, block := range bundle.RuntimeContext {
		switch block.Name {
		case "history_window":
			windows++
		case "existing_fact":
			kept++
		}
	}
	if windows != 1 {
		t.Fatalf("expected exactly one history_window fact, got %d: %#v", windows, bundle.RuntimeContext)
	}
	if kept != 1 {
		t.Fatalf("existing runtime context must be preserved, got %#v", bundle.RuntimeContext)
	}
	if !strings.Contains(bundle.RuntimeContext[len(bundle.RuntimeContext)-1].Content, `"omitted_message_count":`) {
		t.Fatalf("unexpected window fact: %#v", bundle.RuntimeContext)
	}
}

// Worker tool results must enter the child's history the same way the main loop
// stores them: presentation fields stripped and oversized results bounded.
func TestHistoryToolResultContentStripsPresentationFields(t *testing.T) {
	t.Parallel()

	raw := `{"ok":true,"tool":"data_query_sql","ui_summary":"查询完成","rows":[1,2,3]}`
	got := historyToolResultContent("data_query_sql", raw)
	if strings.Contains(got, "ui_summary") {
		t.Fatalf("ui_summary must not enter model history, got %q", got)
	}
	if !strings.Contains(got, `"rows"`) {
		t.Fatalf("model-relevant fields must survive, got %q", got)
	}
}
