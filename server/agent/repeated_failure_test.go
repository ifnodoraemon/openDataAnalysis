package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// alwaysFailingTool returns a deterministic contract failure.
type alwaysFailingTool struct{ plainTool }

func (t *alwaysFailingTool) Name() string { return "deterministic_failure_tool" }
func (t *alwaysFailingTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true}
}
func (t *alwaysFailingTool) Execute(json.RawMessage) (string, error) {
	return `{"ok":false,"tool":"deterministic_failure_tool","error_code":"query_failed","message":"column does not exist"}`, nil
}

// A call that keeps failing the same way is a dead loop. After the threshold the
// runtime refuses to run it again and hands the model the facts, instead of
// spending the iteration budget on a call that cannot succeed.
func TestRepeatedDeterministicFailureIsBlocked(t *testing.T) {
	responses := make([]string, 0, 6)
	for i := 0; i < 5; i++ {
		responses = append(responses, responsesAPIToolCallBodyWithArgs(t, "deterministic_failure_tool", `{"sql":"select 1"}`))
	}
	responses = append(responses, responsesAPITextBody(t, "改用了另一条路径。"))
	server, _ := scriptedLLMServer(t, responses)
	installTestLLMConfig(t, server)

	engine := &Engine{
		llm:                  NewLLMClient(),
		registry:             tools.NewRegistry(),
		policy:               "system",
		confirmationReceipts: make(map[string]*ConfirmationReceipt),
	}
	engine.registry.Register(&alwaysFailingTool{})

	var events []RuntimeEvent
	engine.Run(context.Background(), "查一下", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	failures, blocked, completed := 0, 0, false
	for _, ev := range events {
		switch ev.Type {
		case EventToolResult:
			data := ev.Data.(ToolResultData)
			if data.Success {
				continue
			}
			if strings.Contains(data.Result, "repeated_failure_blocked") {
				blocked++
				if !strings.Contains(data.Result, "query_failed") {
					t.Fatalf("the block must name the failure that caused it: %s", data.Result)
				}
			} else {
				failures++
			}
		case EventRunCompleted:
			completed = true
		case EventError:
			t.Fatalf("a failure loop must not kill the run: %#v", ev.Data)
		}
	}

	if failures != repeatedToolFailureThreshold() {
		t.Fatalf("expected %d real failures before the block, got %d", repeatedToolFailureThreshold(), failures)
	}
	if blocked == 0 {
		t.Fatalf("expected later calls to be blocked after %d identical failures", failures)
	}
	if !completed {
		t.Fatalf("the run must still be able to finish")
	}
}
