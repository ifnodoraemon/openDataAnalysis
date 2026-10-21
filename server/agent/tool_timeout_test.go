package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// slowTool is an observation tool that ignores cancellation, which is what a
// hung backend looks like to the runtime.
type slowTool struct {
	name  string
	mode  string
	delay time.Duration
}

func (t *slowTool) Name() string        { return t.name }
func (t *slowTool) Description() string { return "slow" }
func (t *slowTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (t *slowTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: t.mode}
}

func (t *slowTool) Execute(json.RawMessage) (string, error) {
	time.Sleep(t.delay)
	return `{"ok":true}`, nil
}

func withToolTimeout(t *testing.T, seconds int) {
	t.Helper()
	previous := config.Cfg
	config.Cfg = &config.Config{ToolCallTimeoutSeconds: seconds}
	t.Cleanup(func() { config.Cfg = previous })
}

// An observation call that exceeds its budget fails with a typed timeout the
// model can act on, instead of blocking the run.
func TestObservationToolCallIsBounded(t *testing.T) {
	withToolTimeout(t, 1)

	registry := tools.NewRegistry()
	registry.Register(&slowTool{name: "slow_observe", mode: "observe", delay: 3 * time.Second})

	start := time.Now()
	result, err := executeToolCall(context.Background(), registry, "slow_observe", json.RawMessage(`{}`))
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("the call must be bounded, took %s", elapsed)
	}
	var timeoutErr *toolTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected a timeout error, got %v", err)
	}

	encoded, ok, _ := normalizeToolExecutionResult("slow_observe", result, err)
	if ok {
		t.Fatalf("a timeout must not be reported as success")
	}
	if !strings.Contains(encoded, `"error_code":"tool_timeout"`) {
		t.Fatalf("expected the tool_timeout code, got %s", encoded)
	}
}

// A mutating call is never abandoned: reporting a failure the runtime cannot
// prove would be worse than waiting for the commit to settle.
func TestActionToolCallIsNotAbandoned(t *testing.T) {
	withToolTimeout(t, 1)

	registry := tools.NewRegistry()
	registry.Register(&slowTool{name: "slow_action", mode: "action", delay: 1200 * time.Millisecond})

	result, err := executeToolCall(context.Background(), registry, "slow_action", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("an action call must not be abandoned, got %v", err)
	}
	if result != `{"ok":true}` {
		t.Fatalf("unexpected result %q", result)
	}
}
