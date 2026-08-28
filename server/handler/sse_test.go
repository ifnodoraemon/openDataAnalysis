package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
)

func TestSSEBrokerCloseSessionClosesOnlyTargetSession(t *testing.T) {
	t.Parallel()

	broker := &SSEBroker{clients: make(map[string]map[*sseClient]struct{})}
	first := broker.Register("session-1")
	second := broker.Register("session-1")
	other := broker.Register("session-2")

	broker.CloseSession("session-1")

	for index, client := range []*sseClient{first, second} {
		select {
		case <-client.done:
		default:
			t.Fatalf("expected target client %d to be closed", index)
		}
	}
	select {
	case <-other.done:
		t.Fatal("expected other session client to remain open")
	default:
	}
	if _, exists := broker.clients["session-1"]; exists {
		t.Fatal("expected target session to be removed")
	}
	if len(broker.clients["session-2"]) != 1 {
		t.Fatal("expected other session registration to remain")
	}

	broker.Unregister(first)
	broker.Unregister(second)
	broker.Unregister(other)
}

func TestSSEBrokerBroadcastDropsWhenClientBufferFull(t *testing.T) {
	t.Parallel()

	broker := &SSEBroker{clients: make(map[string]map[*sseClient]struct{})}
	client := broker.Register("session-drop")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 120; i++ {
			broker.Broadcast("session-drop", agent.RuntimeEvent{Type: agent.EventAssistantStatus, Data: agent.AssistantStatusData{Content: "x"}}, "m_live")
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast blocked on a slow client")
	}

	if client.dropped.Load() == 0 {
		t.Fatal("expected dropped events to be counted")
	}
	if !client.overflow.Load() {
		t.Fatal("expected client to be marked overflowed")
	}
	drained := 0
	for {
		select {
		case <-client.send:
			drained++
			continue
		default:
		}
		break
	}
	if drained != 100 {
		t.Fatalf("expected exactly the buffered events to be kept, got %d", drained)
	}

	broker.Unregister(client)
}

func TestSSEMessageDeliveryDeduplicatesReplayedIDs(t *testing.T) {
	t.Parallel()

	replayed := map[string]struct{}{"m_3": {}}
	buf := &bytes.Buffer{}

	delivered, err := deliverSSEMessage(buf, sseMessage{
		id:  "m_3",
		evt: agent.RuntimeEvent{Type: agent.EventAssistantStatus, Data: agent.AssistantStatusData{Content: "dup"}},
	}, replayed)
	if err != nil {
		t.Fatalf("deliver deduplicated message: %v", err)
	}
	if delivered {
		t.Fatal("expected replayed message to be dropped")
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no bytes for deduplicated message, got %q", buf.String())
	}

	delivered, err = deliverSSEMessage(buf, sseMessage{
		id:  "m_4",
		evt: agent.RuntimeEvent{Type: agent.EventAssistantStatus, Data: agent.AssistantStatusData{Content: "live"}},
	}, replayed)
	if err != nil {
		t.Fatalf("deliver live message: %v", err)
	}
	if !delivered {
		t.Fatal("expected non-replayed message to be delivered")
	}
	if !strings.Contains(buf.String(), "id: m_4\n") {
		t.Fatalf("expected SSE id field on delivered message, got %q", buf.String())
	}
}

func TestSessionReplayMessagesLocatesCursorInTimeOrder(t *testing.T) {
	t.Parallel()

	messages := []domain.RunMessage{
		{ID: "m_1", Type: "user", Content: "one", CreatedAt: time.Now().Add(-4 * time.Second)},
		{ID: "m_2", Type: "assistant_status", Content: "two", CreatedAt: time.Now().Add(-3 * time.Second)},
		{ID: "m_3", Type: "tool_call", Content: "three", CreatedAt: time.Now().Add(-2 * time.Second)},
		{ID: "m_4", Type: "tool_result", Content: "four", CreatedAt: time.Now().Add(-1 * time.Second)},
	}

	if got := sessionReplayMessages(messages, ""); len(got) != 0 {
		t.Fatalf("expected no replay without cursor, got %d", len(got))
	}
	if got := sessionReplayMessages(messages, "m_2"); len(got) != 2 || got[0].ID != "m_3" || got[1].ID != "m_4" {
		t.Fatalf("expected replay strictly after cursor, got %#v", got)
	}
	if got := sessionReplayMessages(messages, "missing"); len(got) != len(messages) {
		t.Fatalf("expected full replay for unknown cursor, got %d", len(got))
	}
}

func TestRunMessageToRuntimeEventReconstructsPayloads(t *testing.T) {
	t.Parallel()

	toolCallID := "call_1"
	duration := int64(42)
	success := true
	msgs := []domain.RunMessage{
		{ID: "m_1", Type: "user", SessionID: "s_1", RunID: "r_1", Content: "hello"},
		{ID: "m_2", Type: agent.EventToolCall, SessionID: "s_1", RunID: "r_1", Name: "tool", ToolCallID: &toolCallID, Content: `{"q":1}`},
		{ID: "m_3", Type: agent.EventToolResult, SessionID: "s_1", RunID: "r_1", Name: "tool", ToolCallID: &toolCallID, Content: "result body", Duration: &duration, Success: &success},
		{ID: "m_4", Type: agent.EventReportUpdate, SessionID: "s_1", RunID: "r_1", Content: `{"html":"<p>x</p>","title":"T"}`},
	}

	want := []agent.RuntimeEvent{
		{Type: "user", SessionID: "s_1", RunID: "r_1", Data: agent.UserMessage{Content: "hello"}},
		{Type: agent.EventToolCall, SessionID: "s_1", RunID: "r_1", Data: agent.ToolCallData{ID: "call_1", Name: "tool", Arguments: json.RawMessage(`{"q":1}`)}},
		{Type: agent.EventToolResult, SessionID: "s_1", RunID: "r_1", Data: agent.ToolResultData{ID: "call_1", Name: "tool", Result: "result body", Duration: 42, Success: true}},
		{Type: agent.EventReportUpdate, SessionID: "s_1", RunID: "r_1", Data: agent.ReportUpdateData{HTML: "<p>x</p>", Title: "T"}},
	}

	for i, msg := range msgs {
		evt, err := runMessageToRuntimeEvent(msg)
		if err != nil {
			t.Fatalf("reconstruct event %d: %v", i, err)
		}
		encoded, err := json.Marshal(evt)
		if err != nil {
			t.Fatalf("marshal event %d: %v", i, err)
		}
		expected, err := json.Marshal(want[i])
		if err != nil {
			t.Fatalf("marshal expected %d: %v", i, err)
		}
		if string(encoded) != string(expected) {
			t.Fatalf("event %d mismatch:\n got %s\nwant %s", i, encoded, expected)
		}
	}
}

func TestSSEHandlerReplaysPersistedEventsAfterLastEventID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, err := metadata.Open(root + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
		GlobalSSEBroker.CloseSession("s_sse")
	})

	prevSessionRepo := sessionRepo
	prevRunRepo := runRepo
	prevMessageRepo := messageRepo
	t.Cleanup(func() {
		sessionRepo = prevSessionRepo
		runRepo = prevRunRepo
		messageRepo = prevMessageRepo
	})

	sessionRepo = sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)

	now := time.Now()
	if err := sessionRepo.Create(ctx, &domain.Session{
		ID:          "s_sse",
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Title:       "SSE",
		Status:      domain.SessionStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastSeenAt:  now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           "r_sse",
		SessionID:    "s_sse",
		WorkspaceID:  "w_1",
		UserID:       "u_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusRunning,
		InputMessage: "analyze",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	replayMessages := []domain.RunMessage{
		{ID: "m_1", RunID: "r_sse", SessionID: "s_sse", WorkspaceID: "w_1", Type: "user", Content: "first", CreatedAt: now},
		{ID: "m_2", RunID: "r_sse", SessionID: "s_sse", WorkspaceID: "w_1", Type: "user", Content: "second", CreatedAt: now.Add(time.Second)},
		{ID: "m_3", RunID: "r_sse", SessionID: "s_sse", WorkspaceID: "w_1", Type: agent.EventAssistantStatus, Content: "working", CreatedAt: now.Add(2 * time.Second)},
		{ID: "m_4", RunID: "r_sse", SessionID: "s_sse", WorkspaceID: "w_1", Type: agent.EventRunCompleted, Content: "done", CreatedAt: now.Add(3 * time.Second)},
	}
	for i := range replayMessages {
		if err := messageRepo.Create(ctx, &replayMessages[i]); err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sse?session_id=s_sse", nil)
	request.Header.Set("Last-Event-ID", "m_2")
	requestCtx, cancelRequest := context.WithCancel(auth.WithIdentity(request.Context(), auth.Identity{
		UserID:      "u_1",
		WorkspaceID: "w_1",
	}))
	request = request.WithContext(requestCtx)
	time.AfterFunc(100*time.Millisecond, cancelRequest)
	recorder := httptest.NewRecorder()
	SSEHandler(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, "\"sessionId\":\"s_sse\"") {
		t.Fatalf("expected connected event, got %q", body)
	}
	first := strings.Index(body, "id: m_3")
	second := strings.Index(body, "id: m_4")
	if first < 0 || second < 0 {
		t.Fatalf("expected replayed events with id fields, got %q", body)
	}
	if first > second {
		t.Fatalf("expected replay in persisted order, got %q", body)
	}
	if strings.Contains(body, "id: m_1") || strings.Contains(body, "id: m_2") {
		t.Fatalf("expected events at or before the cursor to be skipped, got %q", body)
	}
	if !strings.Contains(body, `"content":"working"`) || !strings.Contains(body, `"summary":"done"`) {
		t.Fatalf("expected reconstructed payloads, got %q", body)
	}
}
