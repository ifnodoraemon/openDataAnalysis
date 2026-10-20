package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
	"github.com/ifnodoraemon/openDataAnalysis/storage/local"
)

// Re-finalizing the same run must replace the previous report instead of
// colliding with reports.run_id and the files primary key, because the artifact
// identifiers are derived from the run id.
func TestSaveReportHTMLIsIdempotentForOneRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, err := metadata.Open(filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	now := time.Now()
	if _, err := store.DB.Exec(
		`INSERT INTO users (id, email, password_hash, name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"user_1", "u@example.com", "hash", "User", "active", now, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_user_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"ws_1", "WS", "ws", "user_1", "active", now, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		"ws_1", "user_1", "owner", now); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	if _, err := store.DB.Exec(
		`INSERT INTO sessions (id, workspace_id, user_id, title, status, created_at, updated_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"s_1", "ws_1", "user_1", "Session", "active", now, now, now); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO analysis_runs (id, session_id, workspace_id, user_id, run_kind, status, input_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"r_1", "s_1", "ws_1", "user_1", "root", "completed", "task", now, now); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	fileService := &FileService{
		Storage:       local.New(filepath.Join(root, "objects"), ""),
		FileRepo:      sqliterepo.NewFileRepository(store.DB),
		ReportRepo:    sqliterepo.NewReportRepository(store.DB),
		WorkspaceRepo: sqliterepo.NewWorkspaceRepository(store.DB),
		TempDir:       filepath.Join(root, "tmp"),
	}

	input := SaveReportInput{
		RunID:       "r_1",
		SessionID:   "s_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		HTML:        "<p>first</p>",
		Snapshot:    domain.ReportSnapshot{Version: "v3", Title: "T", NeedsFinalize: false},
	}
	if _, err := fileService.SaveReportHTML(ctx, input); err != nil {
		t.Fatalf("first save: %v", err)
	}

	input.HTML = "<p>second</p>"
	input.Snapshot.Title = "T2"
	file, err := fileService.SaveReportHTML(ctx, input)
	if err != nil {
		t.Fatalf("re-saving the same run must replace, not collide: %v", err)
	}
	if file == nil || file.ID != "rep_r_1" {
		t.Fatalf("unexpected artifact: %#v", file)
	}

	report, err := fileService.ReportRepo.GetByRunID(ctx, "r_1")
	if err != nil {
		t.Fatalf("read back report: %v", err)
	}
	if report.Title != "T2" {
		t.Fatalf("expected the replacement report, got %q", report.Title)
	}

	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(1) FROM reports WHERE run_id = ?`, "r_1").Scan(&count); err != nil {
		t.Fatalf("count reports: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one report row per run, got %d", count)
	}
}
