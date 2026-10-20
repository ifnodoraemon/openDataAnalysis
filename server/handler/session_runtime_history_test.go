package handler

import (
	"context"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// Hydrating a session that is not live must restore the model conversation
// history from the persisted event log, so a run started after a restart
// continues the existing conversation instead of starting blank.
func TestHydrateSessionFromPersistenceRebuildsConversationHistory(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	setupRuntimeStateRepos(t, ctx, store)

	now := time.Now()
	mustCreateRunMessage(t, ctx, &domain.AnalysisRun{
		ID:           "run_hist_root",
		SessionID:    "sess_1",
		WorkspaceID:  "ws_1",
		UserID:       "user_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "分析销售额",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	callID := "call_hist_1"
	messages := []*domain.RunMessage{
		{
			ID:        "hist_user",
			RunID:     "run_hist_root",
			SessionID: "sess_1",
			Type:      "user",
			Content:   "分析销售额",
		},
		{
			ID:        "hist_status",
			RunID:     "run_hist_root",
			SessionID: "sess_1",
			Type:      string(agent.EventAssistantStatus),
			Content:   "先看表结构",
		},
		{
			ID:         "hist_call",
			RunID:      "run_hist_root",
			SessionID:  "sess_1",
			Type:       string(agent.EventToolCall),
			Name:       "data_list_tables",
			ToolCallID: &callID,
			Content:    `{"schema":"main"}`,
		},
		{
			ID:         "hist_result",
			RunID:      "run_hist_root",
			SessionID:  "sess_1",
			Type:       string(agent.EventToolResult),
			Name:       "data_list_tables",
			ToolCallID: &callID,
			Content:    `{"ok":true,"tool":"data_list_tables"}`,
		},
		{
			ID:        "hist_complete",
			RunID:     "run_hist_root",
			SessionID: "sess_1",
			Type:      string(agent.EventRunCompleted),
			Content:   "销售额为 100",
		},
	}
	for index, msg := range messages {
		msg.WorkspaceID = "ws_1"
		msg.CreatedAt = now.Add(time.Duration(index+1) * time.Second)
		mustCreateRunMessage(t, ctx, msg)
	}

	sess := &session.Session{
		ID:          "sess_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		ReportState: &tools.ReportState{},
		EditState:   &tools.ReportEditState{},
		Memory:      agent.NewWorkingMemory(),
		Subgoals:    agent.NewSubgoalManager(),
		Engine:      agent.NewEngine(tools.NewRegistry()),
	}

	if err := hydrateSessionFromPersistence(ctx, sess); err != nil {
		t.Fatalf("hydrate session: %v", err)
	}

	history := sess.Engine.History()
	if len(history) != 4 {
		t.Fatalf("expected 4 rebuilt history items, got %d: %#v", len(history), history)
	}
	if history[0].Role != agent.LLMRoleUser || history[0].Content != "分析销售额" {
		t.Fatalf("unexpected first item: %#v", history[0])
	}
	if history[1].Role != agent.LLMRoleAssistant || len(history[1].ToolCalls) != 1 {
		t.Fatalf("expected merged assistant tool call, got %#v", history[1])
	}
	if history[1].Content != "先看表结构" {
		t.Fatalf("expected assistant status text to survive, got %q", history[1].Content)
	}
	if history[2].Role != agent.LLMRoleTool || history[2].ToolCallID != callID {
		t.Fatalf("unexpected tool item: %#v", history[2])
	}
	if history[3].Role != agent.LLMRoleAssistant || history[3].Content != "销售额为 100" {
		t.Fatalf("unexpected final item: %#v", history[3])
	}
}

// A session with no persisted events hydrates to an empty history rather than
// failing.
func TestHydrateSessionFromPersistenceWithoutEventsLeavesHistoryEmpty(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	setupRuntimeStateRepos(t, ctx, store)

	sess := &session.Session{
		ID:          "sess_empty",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		ReportState: &tools.ReportState{},
		EditState:   &tools.ReportEditState{},
		Memory:      agent.NewWorkingMemory(),
		Subgoals:    agent.NewSubgoalManager(),
		Engine:      agent.NewEngine(tools.NewRegistry()),
	}

	if err := hydrateSessionFromPersistence(ctx, sess); err != nil {
		t.Fatalf("hydrate session: %v", err)
	}
	if history := sess.Engine.History(); len(history) != 0 {
		t.Fatalf("expected empty history, got %#v", history)
	}
}
