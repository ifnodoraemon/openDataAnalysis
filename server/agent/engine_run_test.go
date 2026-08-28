package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type cancellingTestTool struct {
	cancel context.CancelFunc
}

func (t *cancellingTestTool) Name() string        { return "cancel_test_tool" }
func (t *cancellingTestTool) Description() string { return "test tool that cancels the run context" }
func (t *cancellingTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *cancellingTestTool) Execute(json.RawMessage) (string, error) {
	t.cancel()
	return `{"ok":true,"tool":"cancel_test_tool"}`, nil
}

func scriptedLLMServer(t *testing.T, responses []string) (*httptest.Server, func() [][]byte) {
	t.Helper()

	var mu sync.Mutex
	var requests [][]byte
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request failed", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		index := callCount
		callCount++
		mu.Unlock()
		if index >= len(responses) {
			http.Error(w, "unexpected extra llm call", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[index]))
	}))
	t.Cleanup(server.Close)
	return server, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), requests...)
	}
}

func installTestLLMConfig(t *testing.T, server *httptest.Server) {
	t.Helper()

	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })
	config.Cfg = &config.Config{
		LLMProvider:    "openai",
		LLMModel:       "gpt-test",
		LLMAPIKey:      "test-key",
		LLMAPIEndpoint: server.URL,
		LLMAPIProtocol: "responses",
	}
}

func responsesAPITextBody(t *testing.T, text string) string {
	payload := map[string]interface{}{
		"id":     "resp_text",
		"status": "completed",
		"model":  "gpt-test",
		"output": []map[string]interface{}{
			{
				"type": "message",
				"id":   "msg_1",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": text},
				},
			},
		},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode responses api body: %v", err)
	}
	return string(encoded)
}

func responsesAPIToolCallBody(t *testing.T, toolName string) string {
	payload := map[string]interface{}{
		"id":     "resp_tool",
		"status": "completed",
		"model":  "gpt-test",
		"output": []map[string]interface{}{
			{
				"type":      "function_call",
				"id":        "fc_1",
				"call_id":   "call_1",
				"name":      toolName,
				"arguments": "{}",
			},
		},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode responses api body: %v", err)
	}
	return string(encoded)
}

func TestRunEmitsSingleCancelWhenToolExecutionCancelsRun(t *testing.T) {
	server, snapshot := scriptedLLMServer(t, []string{responsesAPIToolCallBody(t, "cancel_test_tool")})
	installTestLLMConfig(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := tools.NewRegistry()
	registry.Register(&cancellingTestTool{cancel: cancel})

	engine := &Engine{
		llm:      NewLLMClient(),
		registry: registry,
		policy:   "system",
	}

	var events []RuntimeEvent
	engine.Run(ctx, "分析数据", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	cancelled := 0
	for _, ev := range events {
		if ev.Type == EventRunCompleted {
			t.Fatalf("unexpected run completion: %#v", ev)
		}
		if ev.Type == EventRunCancelled {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("expected exactly one run_cancelled event, got %d: %#v", cancelled, events)
	}
	if got := len(snapshot()); got != 1 {
		t.Fatalf("expected one llm call, got %d", got)
	}
}

type mutatingReportTestTool struct {
	state *tools.ReportState
}

func (t *mutatingReportTestTool) Name() string { return "mutate_report_tool" }
func (t *mutatingReportTestTool) Description() string {
	return "test tool that mutates the report draft state"
}
func (t *mutatingReportTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *mutatingReportTestTool) Execute(json.RawMessage) (string, error) {
	t.state.Lock()
	t.state.MutationVersion++
	t.state.NeedsFinalize = true
	t.state.Unlock()
	return `{"ok":true,"tool":"mutate_report_tool"}`, nil
}

func TestRunAcceptsTextAfterDraftDeliveryStateInformedOnce(t *testing.T) {
	server, snapshot := scriptedLLMServer(t, []string{
		responsesAPIToolCallBody(t, "mutate_report_tool"),
		responsesAPITextBody(t, "第一轮进度说明"),
		responsesAPITextBody(t, "最终结论"),
	})
	installTestLLMConfig(t, server)

	state := &tools.ReportState{MutationVersion: 2}
	registry := tools.NewRegistry()
	registry.Register(&mutatingReportTestTool{state: state})
	engine := &Engine{
		llm:         NewLLMClient(),
		registry:    registry,
		policy:      "system",
		reportState: state,
	}

	var events []RuntimeEvent
	engine.Run(context.Background(), "分析数据", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	completions := 0
	for _, ev := range events {
		if ev.Type == EventRunCancelled || ev.Type == EventError {
			t.Fatalf("unexpected terminal event: %#v", ev)
		}
		if ev.Type != EventRunCompleted {
			continue
		}
		completions++
		complete, ok := ev.Data.(CompleteData)
		if !ok || complete.Summary != "最终结论" {
			t.Fatalf("unexpected completion payload: %#v", ev.Data)
		}
	}
	if completions != 1 {
		t.Fatalf("expected exactly one run_completed event, got %d: %#v", completions, events)
	}

	requests := snapshot()
	if len(requests) != 3 {
		t.Fatalf("expected exactly three llm calls, got %d", len(requests))
	}
	if strings.Contains(string(requests[0]), "report_delivery_state") || strings.Contains(string(requests[1]), "report_delivery_state") {
		t.Fatalf("turns before the first draft text must not carry delivery-state facts: %s", requests[1])
	}
	if !strings.Contains(string(requests[2]), "report_delivery_state") || !strings.Contains(string(requests[2]), "report_mutation_version=3") {
		t.Fatalf("turn after the draft text must carry injected delivery-state facts: %s", requests[2])
	}

	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.history) != 5 {
		t.Fatalf("expected user, tool-call, tool-result, and two assistant messages, got %#v", engine.history)
	}
	if engine.history[0].Role != LLMRoleUser || engine.history[0].Content != "分析数据" {
		t.Fatalf("unexpected first history item: %#v", engine.history[0])
	}
	if len(engine.history[1].ToolCalls) != 1 || engine.history[1].ToolCalls[0].Function.Name != "mutate_report_tool" {
		t.Fatalf("unexpected tool-call history item: %#v", engine.history[1])
	}
	if engine.history[2].Role != LLMRoleTool {
		t.Fatalf("unexpected tool-result history item: %#v", engine.history[2])
	}
	if engine.history[3].Role != LLMRoleAssistant || engine.history[3].Content != "第一轮进度说明" {
		t.Fatalf("unexpected draft-continuation history item: %#v", engine.history[3])
	}
	if engine.history[4].Role != LLMRoleAssistant || engine.history[4].Content != "最终结论" {
		t.Fatalf("unexpected final history item: %#v", engine.history[4])
	}
}
