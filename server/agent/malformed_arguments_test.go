package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// A model occasionally emits tool arguments that are not a valid JSON object.
// That is recoverable: the call fails with a structured error the model can act
// on, and the run continues. Treating it as a transport failure used to end the
// whole run and lose the work already done.
func TestRunReportsMalformedToolArgumentsAsRecoverableFailure(t *testing.T) {
	server, _ := scriptedLLMServer(t, []string{
		// Duplicate key: strict decoding rejects it, but the run must survive.
		responsesAPIToolCallBodyWithArgs(t, "plain_tool", `{"a":1,"a":2}`),
		responsesAPITextBody(t, "已跳过那次无效调用并给出结论。"),
	})
	installTestLLMConfig(t, server)

	engine := &Engine{
		llm:                  NewLLMClient(),
		registry:             tools.NewRegistry(),
		policy:               "system",
		confirmationReceipts: make(map[string]*ConfirmationReceipt),
	}
	engine.registry.Register(&plainTool{})

	var events []RuntimeEvent
	engine.Run(context.Background(), "看一下时间上下文", nil, func(ev RuntimeEvent) {
		events = append(events, ev)
	})

	var toolResult *ToolResultData
	var completed *CompleteData
	for _, ev := range events {
		switch ev.Type {
		case EventToolResult:
			data := ev.Data.(ToolResultData)
			toolResult = &data
		case EventRunCompleted:
			data := ev.Data.(CompleteData)
			completed = &data
		case EventError:
			t.Fatalf("a malformed tool call must not fail the run: %#v", ev.Data)
		}
	}

	if toolResult == nil {
		t.Fatalf("expected the malformed call to be answered with a failure result")
	}
	if toolResult.Success {
		t.Fatalf("a malformed call must not be reported as success")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(toolResult.Result), &payload); err != nil {
		t.Fatalf("failure result is not JSON: %v", err)
	}
	if payload["error_code"] != "invalid_tool_arguments" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
	if !strings.Contains(toolResult.Result, "duplicate JSON object key") {
		t.Fatalf("the failure must say what was wrong, got %q", toolResult.Result)
	}

	if completed == nil {
		t.Fatalf("the run must continue after the failure and finish")
	}

	// The conversation stays protocol-valid: the call has a result.
	for _, item := range engine.History() {
		if item.Role == LLMRoleAssistant && len(item.ToolCalls) > 0 {
			found := false
			for _, other := range engine.History() {
				if other.Role == LLMRoleTool && other.ToolCallID == item.ToolCalls[0].ID {
					found = true
				}
			}
			if !found {
				t.Fatalf("the failed call must still be paired with a result")
			}
		}
	}
}

// A tool name the model invented is also its own output. The call fails, the
// rest of the turn survives.
func TestRunReportsUnknownToolAsRecoverableFailure(t *testing.T) {
	server, _ := scriptedLLMServer(t, []string{
		responsesAPIToolCallBodyWithArgs(t, "no_such_tool", `{}`),
		responsesAPITextBody(t, "改用已有工具完成。"),
	})
	installTestLLMConfig(t, server)

	engine := &Engine{
		llm:                  NewLLMClient(),
		registry:             tools.NewRegistry(),
		policy:               "system",
		confirmationReceipts: make(map[string]*ConfirmationReceipt),
	}
	engine.registry.Register(&plainTool{})

	var events []RuntimeEvent
	engine.Run(context.Background(), "做点分析", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	var sawUnknown, sawCompleted bool
	for _, ev := range events {
		switch ev.Type {
		case EventToolResult:
			data := ev.Data.(ToolResultData)
			if !data.Success && strings.Contains(data.Result, "unknown_tool") {
				sawUnknown = true
			}
		case EventRunCompleted:
			sawCompleted = true
		case EventError:
			t.Fatalf("an invented tool name must not fail the run: %#v", ev.Data)
		}
	}
	if !sawUnknown {
		t.Fatalf("expected the unknown tool call to fail as a fact")
	}
	if !sawCompleted {
		t.Fatalf("the run must continue after the unknown tool call")
	}
}
