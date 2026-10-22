package handler

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
)

// Persisting an event and reading it back are two independent switches that must
// stay inverse. A field added to one direction and forgotten in the other is
// silently lossy — the event still replays, just without the field. This test
// round-trips every event type so that drift fails the build.
func TestEventCodecRoundTripsEveryEventType(t *testing.T) {
	store, err := metadata.Open(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	previousRepo := messageRepo
	t.Cleanup(func() { messageRepo = previousRepo })
	messageRepo = sqliterepo.NewMessageRepository(store.DB)

	now := time.Now()
	if _, err := store.DB.Exec(
		`INSERT INTO users (id, email, password_hash, name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"u_1", "u@example.com", "hash", "User", "active", now, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_user_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"ws_1", "WS", "ws", "u_1", "active", now, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO sessions (id, workspace_id, user_id, title, status, created_at, updated_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"s_1", "ws_1", "u_1", "Session", "active", now, now, now); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO analysis_runs (id, session_id, workspace_id, user_id, run_kind, status, input_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"r_1", "s_1", "ws_1", "u_1", "root", "running", "task", now, now); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	callID := "call_1"
	duration := int64(42)
	success := true

	cases := []struct {
		name  string
		event agent.RuntimeEvent
		check func(*testing.T, agent.RuntimeEvent)
	}{
		{
			name:  "user message",
			event: agent.RuntimeEvent{Type: "user", Data: agent.UserMessage{Content: "分析销售额"}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				if got := ev.Data.(agent.UserMessage).Content; got != "分析销售额" {
					t.Fatalf("content lost: %q", got)
				}
			},
		},
		{
			name:  "assistant status",
			event: agent.RuntimeEvent{Type: agent.EventAssistantStatus, Data: agent.AssistantStatusData{Content: "先看表结构"}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				if got := ev.Data.(agent.AssistantStatusData).Content; got != "先看表结构" {
					t.Fatalf("content lost: %q", got)
				}
			},
		},
		{
			name: "tool call",
			event: agent.RuntimeEvent{Type: agent.EventToolCall, Data: agent.ToolCallData{
				ID: callID, Name: "data_query_sql", Arguments: json.RawMessage(`{"sql":"select 1"}`),
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.ToolCallData)
				if data.ID != callID || data.Name != "data_query_sql" {
					t.Fatalf("identity lost: %#v", data)
				}
				if string(data.Arguments.(json.RawMessage)) != `{"sql":"select 1"}` {
					t.Fatalf("arguments lost: %#v", data.Arguments)
				}
			},
		},
		{
			name: "tool result",
			event: agent.RuntimeEvent{Type: agent.EventToolResult, Data: agent.ToolResultData{
				ID: callID, Name: "data_query_sql", Result: `{"ok":true}`, Duration: duration, Success: success,
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.ToolResultData)
				if data.ID != callID || data.Duration != duration || !data.Success {
					t.Fatalf("side fields lost: %#v", data)
				}
			},
		},
		{
			name:  "run completed",
			event: agent.RuntimeEvent{Type: agent.EventRunCompleted, Data: agent.CompleteData{Summary: "结论"}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				if got := ev.Data.(agent.CompleteData).Summary; got != "结论" {
					t.Fatalf("summary lost: %q", got)
				}
			},
		},
		{
			name:  "error",
			event: agent.RuntimeEvent{Type: agent.EventError, Data: agent.ErrorData{Message: "出错了"}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				if got := ev.Data.(agent.ErrorData).Message; got != "出错了" {
					t.Fatalf("message lost: %q", got)
				}
			},
		},
		{
			name: "user input request",
			event: agent.RuntimeEvent{Type: agent.EventUserRequestInput, Data: agent.AskUserData{
				Question: "用哪个口径?", Required: true, SelectionMode: "single", AllowCustom: true,
				ToolCallID: callID,
				Authorization: &agent.ActionAuthorizationRequest{
					Action: "profile_patch_commit", ResourceRef: "p_1", PayloadJSON: `{"a":1}`,
				},
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.AskUserData)
				if data.Question != "用哪个口径?" || !data.Required || data.SelectionMode != "single" {
					t.Fatalf("question fields lost: %#v", data)
				}
				if data.ToolCallID != callID {
					t.Fatalf("tool call id lost: %q", data.ToolCallID)
				}
				if data.Authorization == nil || data.Authorization.Action != "profile_patch_commit" {
					t.Fatalf("authorization lost: %#v", data.Authorization)
				}
			},
		},
		{
			name: "approval denied",
			event: agent.RuntimeEvent{Type: agent.EventApprovalDenied, Data: agent.ApprovalDeniedData{
				Tool: "profile_patch_commit", Decision: "ask", Reason: "needs authorization",
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.ApprovalDeniedData)
				if data.Tool != "profile_patch_commit" || data.Decision != "ask" || data.Reason == "" {
					t.Fatalf("decision fields lost: %#v", data)
				}
			},
		},
		{
			name: "history compacted",
			event: agent.RuntimeEvent{Type: agent.EventHistoryCompacted, Data: agent.HistoryCompactedData{
				Summary: "早前对话摘要", ShadowedItems: 7, Method: agent.HistoryCompactionMethodSummary,
				PromptTokens: 900, ContextWindow: 8000,
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.HistoryCompactedData)
				if data.Summary != "早前对话摘要" || data.ShadowedItems != 7 || data.Method != agent.HistoryCompactionMethodSummary {
					t.Fatalf("checkpoint fields lost: %#v", data)
				}
			},
		},
		{
			name: "run usage",
			event: agent.RuntimeEvent{Type: agent.EventRunUsage, Data: agent.RunUsageData{
				PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.RunUsageData)
				if data.PromptTokens != 10 || data.CompletionTokens != 5 {
					t.Fatalf("usage lost: %#v", data)
				}
			},
		},
		{
			name: "system feedback",
			event: agent.RuntimeEvent{Type: agent.EventSystemFeedback, Data: agent.SystemFeedbackData{
				Content: "[system_feedback] restate cleanly", Reason: "invalid_final_output",
			}},
			check: func(t *testing.T, ev agent.RuntimeEvent) {
				data := ev.Data.(agent.SystemFeedbackData)
				if data.Content != "[system_feedback] restate cleanly" || data.Reason != "invalid_final_output" {
					t.Fatalf("feedback lost: %#v", data)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			row, err := saveEventToDBSync("ws_1", "s_1", "r_1", tc.event, time.Now())
			if err != nil {
				t.Fatalf("persist event: %v", err)
			}
			decoded, err := runMessageToRuntimeEvent(*row)
			if err != nil {
				t.Fatalf("decode event: %v", err)
			}
			if decoded.Type != tc.event.Type {
				t.Fatalf("type changed across the round trip: %q -> %q", tc.event.Type, decoded.Type)
			}
			tc.check(t, decoded)
		})
	}
}

// The decode switch must not silently accept a stored payload it cannot
// interpret as the declared type.
func TestRunMessageToRuntimeEventRejectsMalformedJSONPayload(t *testing.T) {
	t.Parallel()

	_, err := runMessageToRuntimeEvent(domain.RunMessage{Type: string(agent.EventUserRequestInput), Content: "not-json"})
	if err == nil {
		t.Fatalf("expected a malformed payload to be rejected")
	}
}

// A tool call whose arguments the model emitted malformed is still a real
// event: the call happened and was answered with a structured failure. Logging
// it must not fail — a json.RawMessage holding invalid JSON cannot be
// marshalled, so the encoder stores a placeholder instead.
func TestEventPersistsMalformedToolCallArguments(t *testing.T) {
	store, err := metadata.Open(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	previousRepo := messageRepo
	t.Cleanup(func() { messageRepo = previousRepo })
	messageRepo = sqliterepo.NewMessageRepository(store.DB)

	now := time.Now()
	for _, stmt := range []struct {
		q    string
		args []interface{}
	}{
		{`INSERT INTO users (id, email, password_hash, name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"u_1", "u@example.com", "h", "U", "active", now, now}},
		{`INSERT INTO workspaces (id, name, slug, owner_user_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"ws_1", "WS", "ws", "u_1", "active", now, now}},
		{`INSERT INTO sessions (id, workspace_id, user_id, title, status, created_at, updated_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"s_1", "ws_1", "u_1", "S", "active", now, now, now}},
		{`INSERT INTO analysis_runs (id, session_id, workspace_id, user_id, run_kind, status, input_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"r_1", "s_1", "ws_1", "u_1", "root", "running", "t", now, now}},
	} {
		if _, err := store.DB.Exec(stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	event := agent.RuntimeEvent{
		Type: agent.EventToolCall,
		Data: agent.ToolCallData{
			ID:        "call_bad",
			Name:      "data_query_sql",
			Arguments: json.RawMessage(`{"sql":"select 1" "extra":}`),
		},
	}
	row, err := saveEventToDBSync("ws_1", "s_1", "r_1", event, time.Now())
	if err != nil {
		t.Fatalf("persisting a malformed call must not fail the run: %v", err)
	}
	if !strings.Contains(row.Content, "invalid_arguments") {
		t.Fatalf("expected the raw arguments to be recorded as a placeholder, got %q", row.Content)
	}
}
