package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
)

// seedRunMessages writes count events for one run, ordered by created_at.
func seedRunMessages(t *testing.T, repo *MessageRepository, runID string, count int) {
	t.Helper()
	seedRunParents(t, repo.db, runID)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		msg := &domain.RunMessage{
			ID:          fmt.Sprintf("m%05d", i),
			RunID:       runID,
			SessionID:   "s_1",
			WorkspaceID: "ws_1",
			Type:        "user",
			Content:     fmt.Sprintf("event %d", i),
			CreatedAt:   base.Add(time.Duration(i) * time.Millisecond),
		}
		if err := repo.Create(context.Background(), msg); err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}
}

// seedRunParents inserts the rows run_messages references.
func seedRunParents(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []interface{}
	}{
		{`INSERT OR IGNORE INTO users (id, email, password_hash, name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"u_1", "u@example.com", "hash", "User", "active", now, now}},
		{`INSERT OR IGNORE INTO workspaces (id, name, slug, owner_user_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"ws_1", "WS", "ws", "u_1", "active", now, now}},
		{`INSERT OR IGNORE INTO sessions (id, workspace_id, user_id, title, status, created_at, updated_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{"s_1", "ws_1", "u_1", "Session", "active", now, now, now}},
		{`INSERT OR IGNORE INTO analysis_runs (id, session_id, workspace_id, user_id, run_kind, status, input_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{runID, "s_1", "ws_1", "u_1", "root", "completed", "task", now, now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed parent row: %v", err)
		}
	}
}

// A run longer than the read window must keep both its oldest events (the
// opening task and the first compaction checkpoint) and its newest ones. Keeping
// only the newest is what previously broke history rebuild for long sessions.
func TestListByRunKeepsHeadAndTail(t *testing.T) {
	t.Parallel()

	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	repo := NewMessageRepository(store.DB)
	total := messageWindowHead + messageWindowTail + 500
	seedRunMessages(t, repo, "r_long", total)

	messages, err := repo.ListByRun(context.Background(), "r_long")
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(messages) != messageWindowHead+messageWindowTail {
		t.Fatalf("expected the head and tail windows, got %d of %d", len(messages), total)
	}
	if messages[0].ID != "m00000" {
		t.Fatalf("the oldest event must be kept, got %q", messages[0].ID)
	}
	if messages[len(messages)-1].ID != fmt.Sprintf("m%05d", total-1) {
		t.Fatalf("the newest event must be kept, got %q", messages[len(messages)-1].ID)
	}
	for i := 1; i < len(messages); i++ {
		if messages[i].CreatedAt.Before(messages[i-1].CreatedAt) {
			t.Fatalf("events must be chronological at %d", i)
		}
	}

	// The gap is exactly the events between the two windows.
	gapStart := messageWindowHead
	gapEnd := total - messageWindowTail
	seen := make(map[string]struct{}, len(messages))
	for _, msg := range messages {
		seen[msg.ID] = struct{}{}
	}
	for i := gapStart; i < gapEnd; i++ {
		if _, ok := seen[fmt.Sprintf("m%05d", i)]; ok {
			t.Fatalf("event %d belongs to the omitted middle", i)
		}
	}
}

// A run that fits the window is returned whole and in order.
func TestListByRunReturnsShortRunWhole(t *testing.T) {
	t.Parallel()

	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	repo := NewMessageRepository(store.DB)
	seedRunMessages(t, repo, "r_short", 25)

	messages, err := repo.ListByRun(context.Background(), "r_short")
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(messages) != 25 {
		t.Fatalf("expected every event, got %d", len(messages))
	}
	for i, msg := range messages {
		if msg.ID != fmt.Sprintf("m%05d", i) {
			t.Fatalf("unexpected order at %d: %q", i, msg.ID)
		}
	}
}

// CountByRun is what turns a bounded read into an explicit omission fact.
func TestCountByRunMatchesStoredEvents(t *testing.T) {
	t.Parallel()

	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	repo := NewMessageRepository(store.DB)
	seedRunMessages(t, repo, "r_count", 42)

	count, err := repo.CountByRun(context.Background(), "r_count")
	if err != nil {
		t.Fatalf("CountByRun: %v", err)
	}
	if count != 42 {
		t.Fatalf("expected 42 events, got %d", count)
	}
}
