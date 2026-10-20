package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// concurrencyProbeTool records how many calls overlap and the order in which
// they finish, so the ordering and barrier guarantees can be asserted.
type concurrencyProbeTool struct {
	name    string
	safe    bool
	delay   time.Duration
	mu      sync.Mutex
	started []string
	active  int
	peak    int
}

func (t *concurrencyProbeTool) Name() string        { return t.name }
func (t *concurrencyProbeTool) Description() string { return "concurrency probe" }
func (t *concurrencyProbeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)
}
func (t *concurrencyProbeTool) IsConcurrencySafe(json.RawMessage) bool { return t.safe }
func (t *concurrencyProbeTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &params)

	t.mu.Lock()
	t.started = append(t.started, params.ID)
	t.active++
	if t.active > t.peak {
		t.peak = t.active
	}
	t.mu.Unlock()

	time.Sleep(t.delay)

	t.mu.Lock()
	t.active--
	t.mu.Unlock()
	return fmt.Sprintf(`{"ok":true,"tool":%q,"id":%q}`, t.name, params.ID), nil
}

func (t *concurrencyProbeTool) peakConcurrency() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.peak
}

func (t *concurrencyProbeTool) startOrder() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.started...)
}

func TestToolIsConcurrencySafeRequiresExplicitOptIn(t *testing.T) {
	t.Parallel()

	if toolIsConcurrencySafe(&plainTool{}, nil) {
		t.Fatalf("a tool without the interface must stay exclusive")
	}
	if !toolIsConcurrencySafe(&concurrencyProbeTool{safe: true}, nil) {
		t.Fatalf("an explicit true must opt in")
	}
	if toolIsConcurrencySafe(&concurrencyProbeTool{safe: false}, nil) {
		t.Fatalf("an explicit false must stay exclusive")
	}
	if toolIsConcurrencySafe(&panickingConcurrencyTool{}, nil) {
		t.Fatalf("a panicking classifier must stay exclusive")
	}
}

type plainTool struct{}

func (t *plainTool) Name() string                            { return "plain_tool" }
func (t *plainTool) Description() string                     { return "plain" }
func (t *plainTool) Parameters() json.RawMessage             { return json.RawMessage(`{"type":"object"}`) }
func (t *plainTool) Execute(json.RawMessage) (string, error) { return `{"ok":true}`, nil }

type panickingConcurrencyTool struct{ plainTool }

func (t *panickingConcurrencyTool) IsConcurrencySafe(json.RawMessage) bool {
	panic("classifier must not be trusted")
}

// Parallel-safe calls in one batch overlap, but the batch is still planned and
// consumed in the model's order.
func TestPlanToolCallsMarksSafeCallsAndPreservesOrder(t *testing.T) {
	t.Parallel()

	safe := &concurrencyProbeTool{name: "safe_tool", safe: true}
	unsafe := &concurrencyProbeTool{name: "unsafe_tool", safe: false}
	registry := tools.NewRegistry()
	registry.Register(safe)
	registry.Register(unsafe)

	engine := &Engine{registry: registry, policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}
	calls := []LLMToolCall{
		{ID: "c1", Type: LLMToolTypeFunction, Function: LLMFunctionCall{Name: "safe_tool", Arguments: `{"id":"1"}`}},
		{ID: "c2", Type: LLMToolTypeFunction, Function: LLMFunctionCall{Name: "unsafe_tool", Arguments: `{"id":"2"}`}},
		{ID: "c3", Type: LLMToolTypeFunction, Function: LLMFunctionCall{Name: "safe_tool", Arguments: `{"id":"3"}`}},
	}

	plan, err := engine.planToolCalls(calls)
	if err != nil {
		t.Fatalf("planToolCalls: %v", err)
	}
	if len(plan) != 3 {
		t.Fatalf("expected 3 planned calls, got %d", len(plan))
	}
	if !plan[0].safe || plan[1].safe || !plan[2].safe {
		t.Fatalf("unexpected safety classification: %v %v %v", plan[0].safe, plan[1].safe, plan[2].safe)
	}
	for i, planned := range plan {
		if planned.call.ID != calls[i].ID {
			t.Fatalf("plan must preserve model order at %d", i)
		}
	}
}

// An exclusive call is an ordering barrier: nothing after it starts before the
// loop reaches it.
func TestStartParallelRunStopsAtExclusiveBarrier(t *testing.T) {
	t.Parallel()

	engine := &Engine{registry: tools.NewRegistry(), policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}
	plan := []plannedToolCall{
		{safe: true, call: LLMToolCall{ID: "c1"}},
		{safe: false, call: LLMToolCall{ID: "c2"}},
		{safe: true, call: LLMToolCall{ID: "c3"}},
	}

	futures := map[string]*toolFuture{}
	budget := make(chan struct{}, 10)
	startParallelRun(plan, 0, futures, budget, context.Background(), engine.registry)

	if _, ok := futures["c1"]; !ok {
		t.Fatalf("the leading parallel-safe call must be dispatched")
	}
	if _, ok := futures["c2"]; ok {
		t.Fatalf("an exclusive call must not be prefetched")
	}
	if _, ok := futures["c3"]; ok {
		t.Fatalf("a call after a barrier must not be dispatched before the loop reaches it")
	}
}

func TestRunPlannedToolCallFallsBackToInlineExecution(t *testing.T) {
	t.Parallel()

	tool := &concurrencyProbeTool{name: "inline_tool"}
	registry := tools.NewRegistry()
	registry.Register(tool)

	call := LLMToolCall{ID: "c1", Function: LLMFunctionCall{Name: "inline_tool", Arguments: `{"id":"1"}`}}
	result, err := runPlannedToolCall(context.Background(), registry, call, map[string]*toolFuture{})
	if err != nil {
		t.Fatalf("inline execution failed: %v", err)
	}
	if !strings.Contains(result, `"id":"1"`) {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestRunPlannedToolCallConsumesPrefetchedResult(t *testing.T) {
	t.Parallel()

	future := &toolFuture{done: make(chan struct{}), result: `{"ok":true,"prefetched":true}`}
	futures := map[string]*toolFuture{"c1": future}
	close(future.done)

	call := LLMToolCall{ID: "c1", Function: LLMFunctionCall{Name: "never_called"}}
	result, err := runPlannedToolCall(context.Background(), tools.NewRegistry(), call, futures)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "prefetched") {
		t.Fatalf("expected the prefetched result, got %q", result)
	}
}
