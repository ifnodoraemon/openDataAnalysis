package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func TestCanonicalToolArgumentsIgnoresKeyOrder(t *testing.T) {
	t.Parallel()

	first := canonicalToolArguments(`{"b":2,"a":1}`)
	second := canonicalToolArguments(`{"a":1,"b":2}`)
	if first != second {
		t.Fatalf("key order must not change the identity of a call: %q vs %q", first, second)
	}
	if canonicalToolArguments("not-json") != "not-json" {
		t.Fatalf("undecodable arguments must fall back to the raw value")
	}
}

func TestRecordRepeatCallQueuesEscalatingNotices(t *testing.T) {
	t.Parallel()

	engine := &Engine{policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}
	call := LLMToolCall{ID: "c1", Function: LLMFunctionCall{Name: "data_query_sql", Arguments: `{"sql":"select 1"}`}}

	for i := 1; i <= 2; i++ {
		engine.recordRepeatCall(call)
		if notices := engine.takePendingNotices(); len(notices) != 0 {
			t.Fatalf("no notice is due before the first threshold (call %d)", i)
		}
	}

	engine.recordRepeatCall(call)
	notices := engine.takePendingNotices()
	if len(notices) != 1 {
		t.Fatalf("expected one notice at the first threshold, got %d", len(notices))
	}
	if notices[0].Name != "repeat_tool_notice" || notices[0].Role != LLMRoleUser {
		t.Fatalf("unexpected notice shape: %#v", notices[0])
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(notices[0].Content), &payload); err != nil {
		t.Fatalf("notice must be structured JSON: %v", err)
	}
	if payload["identical_calls"] != float64(3) || payload["tool"] != "data_query_sql" {
		t.Fatalf("unexpected notice payload: %#v", payload)
	}

	// A notice is delivered exactly once.
	if again := engine.takePendingNotices(); len(again) != 0 {
		t.Fatalf("a notice must not be delivered twice")
	}

	// The next threshold escalates.
	engine.recordRepeatCall(call)
	engine.recordRepeatCall(call)
	if notices := engine.takePendingNotices(); len(notices) != 1 {
		t.Fatalf("expected the second threshold to fire, got %d", len(notices))
	}
}

func TestRecordRepeatCallTracksCallsSeparately(t *testing.T) {
	t.Parallel()

	engine := &Engine{policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}
	first := LLMToolCall{ID: "c1", Function: LLMFunctionCall{Name: "data_query_sql", Arguments: `{"sql":"select 1"}`}}
	// Same tool, different arguments: a different call, so it must not inherit
	// the other call's repeat count.
	second := LLMToolCall{ID: "c2", Function: LLMFunctionCall{Name: "data_query_sql", Arguments: `{"sql":"select 2"}`}}

	for i := 0; i < 3; i++ {
		engine.recordRepeatCall(first)
		engine.recordRepeatCall(second)
	}
	notices := engine.takePendingNotices()
	if len(notices) != 2 {
		t.Fatalf("each distinct call reaches its own threshold, got %d notices", len(notices))
	}
}

func TestResetRepeatTrackingClearsState(t *testing.T) {
	t.Parallel()

	engine := &Engine{policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}
	call := LLMToolCall{ID: "c1", Function: LLMFunctionCall{Name: "data_query_sql", Arguments: `{}`}}
	engine.recordRepeatCall(call)
	engine.recordRepeatCall(call)
	engine.recordRepeatCall(call)
	engine.resetRepeatTracking()

	if notices := engine.takePendingNotices(); len(notices) != 0 {
		t.Fatalf("a new user message must clear queued notices")
	}
	engine.recordRepeatCall(call)
	if notices := engine.takePendingNotices(); len(notices) != 0 {
		t.Fatalf("repeat counting must restart after a new user message")
	}
}

// blockingAvailabilityTool reports availability only after a long delay, which
// is what an unhealthy backend looks like to the run-start path.
type blockingAvailabilityTool struct {
	plainTool
	delay time.Duration
	calls int
	mu    sync.Mutex
}

func (t *blockingAvailabilityTool) Name() string { return "blocking_availability_tool" }

func (t *blockingAvailabilityTool) CheckAvailability(ctx context.Context) error {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	select {
	case <-time.After(t.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type failingAvailabilityTool struct{ plainTool }

func (t *failingAvailabilityTool) Name() string { return "failing_availability_tool" }

func (t *failingAvailabilityTool) CheckAvailability(context.Context) error {
	return errAlwaysUnavailable
}

var errAlwaysUnavailable = &unavailableError{}

type unavailableError struct{}

func (e *unavailableError) Error() string { return "backend is unavailable" }

// A slow probe must not stall the run start, and an inconclusive probe keeps
// the tool advertised rather than hiding a tool that may be healthy.
func TestProbeToolAvailabilityIsBoundedAndOptimistic(t *testing.T) {
	t.Parallel()

	tool := &blockingAvailabilityTool{delay: 5 * time.Second}
	start := time.Now()
	if err := probeToolAvailability(context.Background(), tool); err != nil {
		t.Fatalf("an inconclusive probe must keep the tool advertised, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the probe must be bounded, took %s", elapsed)
	}
}

// A definite negative still removes the tool from the request.
func TestProbeToolAvailabilitySurfacesDefiniteFailure(t *testing.T) {
	t.Parallel()

	if err := probeToolAvailability(context.Background(), &failingAvailabilityTool{}); err == nil {
		t.Fatalf("a definite failure must be reported")
	}
}

// A cancelled run is not an inconclusive probe.
func TestProbeToolAvailabilityHonoursRunCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probeToolAvailability(ctx, &blockingAvailabilityTool{delay: time.Second}); err == nil {
		t.Fatalf("a cancelled run must surface the cancellation")
	}
}

func TestFilterUnavailableToolSpecsDropsOnlyDefiniteFailures(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&failingAvailabilityTool{plainTool{}})
	registry.Register(&blockingAvailabilityTool{delay: 5 * time.Second})
	registry.Register(&plainTool{})

	engine := &Engine{registry: registry, policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}
	specs := []tools.ToolSpec{
		{Type: "function", Function: tools.FunctionSpec{Name: "plain_tool"}},
		{Type: "function", Function: tools.FunctionSpec{Name: "blocking_availability_tool"}},
		{Type: "function", Function: tools.FunctionSpec{Name: "failing_availability_tool"}},
	}

	filtered := engine.filterUnavailableToolSpecs(context.Background(), specs)
	if len(filtered) != 2 {
		t.Fatalf("expected only the definite failure to be dropped, got %#v", filtered)
	}
	for _, spec := range filtered {
		if strings.Contains(spec.Function.Name, "failing") {
			t.Fatalf("the unavailable tool must be filtered out")
		}
	}
}

// deadlineAvailabilityTool reports failure only because the probe's own budget
// expired. That is inconclusive: denying the call would report a healthy but
// slow backend as unavailable.
type deadlineAvailabilityTool struct{ plainTool }

func (t *deadlineAvailabilityTool) Name() string { return "deadline_availability_tool" }

func (t *deadlineAvailabilityTool) CheckAvailability(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestProbeTreatsItsOwnDeadlineAsInconclusive(t *testing.T) {
	t.Parallel()

	start := time.Now()
	if err := probeToolAvailability(context.Background(), &deadlineAvailabilityTool{}); err != nil {
		t.Fatalf("a probe that only hit its own deadline must stay inconclusive, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the probe must be bounded, took %s", elapsed)
	}
}
