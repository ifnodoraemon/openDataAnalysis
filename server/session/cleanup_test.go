package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupExpiredSessions_RemovesIdleSessions(t *testing.T) {
	t.Parallel()

	manager := NewManager(t.TempDir(), nil, nil)

	// 创建两个 session：一个过期一个活跃
	expired := &Session{
		ID:          "s_old",
		WorkspaceID: "w1",
		UserID:      "u1",
		LastSeenAt:  time.Now().Add(-3 * time.Hour),
		CacheRoot:   t.TempDir(),
	}
	active := &Session{
		ID:          "s_new",
		WorkspaceID: "w1",
		UserID:      "u1",
		LastSeenAt:  time.Now(),
		CacheRoot:   t.TempDir(),
	}

	manager.sessions["s_old"] = expired
	manager.sessions["s_new"] = active
	manager.SetFullDeleteFunc(func(ctx context.Context, sessionID string) error {
		return manager.Delete(sessionID, "w1", "u1")
	})

	cleaned, err := manager.CleanupExpiredSessions(2)
	if err != nil {
		t.Fatalf("cleanup expired sessions: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 cleaned, got %d", cleaned)
	}
	if _, exists := manager.sessions["s_old"]; exists {
		t.Fatal("expected s_old to be removed")
	}
	if _, exists := manager.sessions["s_new"]; !exists {
		t.Fatal("expected s_new to remain")
	}
}

func TestCleanupExpiredSessions_SkipsRunning(t *testing.T) {
	t.Parallel()

	manager := NewManager(t.TempDir(), nil, nil)
	running := &Session{
		ID:          "s_running",
		WorkspaceID: "w1",
		UserID:      "u1",
		LastSeenAt:  time.Now().Add(-5 * time.Hour),
		CacheRoot:   t.TempDir(),
		ActiveRun: &RunState{
			RunID:  "r_1",
			Status: "running",
		},
	}
	manager.sessions["s_running"] = running
	manager.SetFullDeleteFunc(func(ctx context.Context, sessionID string) error {
		return manager.Delete(sessionID, "w1", "u1")
	})

	cleaned, err := manager.CleanupExpiredSessions(1)
	if err != nil {
		t.Fatalf("cleanup expired sessions: %v", err)
	}
	if cleaned != 0 {
		t.Fatalf("expected 0 cleaned (running session), got %d", cleaned)
	}
}

func TestCleanupOldTraces(t *testing.T) {
	t.Parallel()

	traceDir := t.TempDir()

	// 创建一个过期目录和一个未过期目录
	oldDir := filepath.Join(traceDir, "2020-01-01")
	newDir := filepath.Join(traceDir, "2099-12-31")
	os.MkdirAll(oldDir, 0o755)
	os.MkdirAll(newDir, 0o755)

	cleaned, err := CleanupOldTraces(traceDir, 7)
	if err != nil {
		t.Fatalf("cleanup old traces: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 cleaned, got %d", cleaned)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("expected old trace dir to be removed")
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatal("expected new trace dir to remain")
	}
}

func TestCleanupTempDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "stale.csv"), []byte("data"), 0o644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0o755)

	if err := CleanupTempDir(tmpDir); err != nil {
		t.Fatalf("cleanup temp: %v", err)
	}

	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 0 {
		t.Fatalf("expected empty temp dir, got %d entries", len(entries))
	}
}

func TestCleanupExpiredSessions_ZeroTTL_NoOp(t *testing.T) {
	t.Parallel()

	manager := NewManager(t.TempDir(), nil, nil)
	manager.sessions["s_1"] = &Session{
		ID:         "s_1",
		LastSeenAt: time.Now().Add(-100 * time.Hour),
		CacheRoot:  t.TempDir(),
	}

	cleaned, err := manager.CleanupExpiredSessions(0)
	if err != nil {
		t.Fatalf("zero TTL cleanup: %v", err)
	}
	if cleaned != 0 {
		t.Fatalf("expected 0 for zero TTL, got %d", cleaned)
	}
}

func TestCleanupExpiredSessionsRejectsMissingCompleteDeletion(t *testing.T) {
	t.Parallel()

	manager := NewManager(t.TempDir(), nil, nil)
	if _, err := manager.CleanupExpiredSessions(1); err == nil {
		t.Fatal("expected missing complete deletion function to fail")
	}
}

func TestCleanupExpiredSessions_RestoresSessionWhenFullDeleteFails(t *testing.T) {
	t.Parallel()

	manager := NewManager(t.TempDir(), nil, nil)
	expired := &Session{
		ID:          "s_fail",
		WorkspaceID: "w1",
		UserID:      "u1",
		LastSeenAt:  time.Now().Add(-3 * time.Hour),
		CacheRoot:   t.TempDir(),
	}
	manager.sessions["s_fail"] = expired

	var deletedIDs []string
	manager.SetFullDeleteFunc(func(ctx context.Context, sessionID string) error {
		deletedIDs = append(deletedIDs, sessionID)
		return errors.New("storage unavailable")
	})

	cleaned, err := manager.CleanupExpiredSessions(2)
	if err == nil {
		t.Fatal("expected full delete failure to be reported")
	}
	if cleaned != 0 {
		t.Fatalf("expected 0 cleaned, got %d", cleaned)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "s_fail" {
		t.Fatalf("expected full delete to be attempted once, got %v", deletedIDs)
	}
	if _, exists := manager.sessions["s_fail"]; !exists {
		t.Fatal("expected session object to be restored after failed full delete")
	}
}

func TestCleanupExpiredSessions_WaitingSessionIsIdleAndCleaned(t *testing.T) {
	t.Parallel()

	manager := NewManager(t.TempDir(), nil, nil)
	waiting := &Session{
		ID:          "s_waiting",
		WorkspaceID: "w1",
		UserID:      "u1",
		LastSeenAt:  time.Now().Add(-3 * time.Hour),
		CacheRoot:   t.TempDir(),
		ActiveRun: &RunState{
			RunID:  "r_1",
			Status: "waiting_user_input",
		},
	}
	manager.sessions["s_waiting"] = waiting

	var deletedIDs []string
	manager.SetFullDeleteFunc(func(ctx context.Context, sessionID string) error {
		deletedIDs = append(deletedIDs, sessionID)
		return nil
	})

	cleaned, err := manager.CleanupExpiredSessions(2)
	if err != nil {
		t.Fatalf("cleanup expired sessions: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 cleaned, got %d", cleaned)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "s_waiting" {
		t.Fatalf("expected waiting session to be fully deleted, got %v", deletedIDs)
	}
	if _, exists := manager.sessions["s_waiting"]; exists {
		t.Fatal("expected s_waiting to be removed")
	}
}

func TestCleanupOldTracesReportsMissingDirectory(t *testing.T) {
	t.Parallel()

	if _, err := CleanupOldTraces(filepath.Join(t.TempDir(), "missing"), 7); err == nil {
		t.Fatal("expected missing trace directory to fail")
	}
}

func TestStartPeriodicCleanupRejectsIncompleteTTLConfiguration(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected incomplete TTL cleanup configuration to panic")
		}
	}()
	NewManager(t.TempDir(), nil, nil).StartPeriodicCleanup(1, 0, "", "", false)
}

// ---- P1-8: 状态串扰回归测试 ----

func TestRapidStartStopRestart_NoStateLeak(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID:          "s_rapid",
		WorkspaceID: "w1",
		UserID:      "u1",
		CacheRoot:   t.TempDir(),
	}

	// 快速开始
	runID1, _, err := sess.StartRun(context.Background())
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if sess.ActiveRun == nil {
		t.Fatal("expected run to be active")
	}

	// 快速停止
	sess.FinishRun(runID1, "completed")
	if sess.ActiveRun != nil {
		t.Fatal("expected no active run after finish")
	}

	// 再次开始
	runID2, _, err := sess.StartRun(context.Background())
	if err != nil {
		t.Fatalf("start run 2: %v", err)
	}
	if sess.ActiveRun == nil {
		t.Fatal("expected run 2 to be active")
	}

	// 确认非活跃 run 不影响当前 run
	sess.FinishRun(runID1, "cancelled")
	if sess.ActiveRun == nil || sess.ActiveRun.RunID != runID2 {
		t.Fatal("finishing inactive run should not affect current run")
	}

	sess.FinishRun(runID2, "completed")
	if sess.ActiveRun != nil {
		t.Fatal("expected no active run at end")
	}
}

func TestOldRunCancel_DoesNotAffectNewRun(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID:          "s_cancel",
		WorkspaceID: "w1",
		UserID:      "u1",
		CacheRoot:   t.TempDir(),
	}

	//  开始 r1 并取消
	runID1, _, err := sess.StartRun(context.Background())
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	sess.CancelRun(runID1)

	// 等待 r1 完全结束
	sess.FinishRun(runID1, "cancelled")

	// 开始 r2
	runID2, _, err := sess.StartRun(context.Background())
	if err != nil {
		t.Fatalf("start run 2: %v", err)
	}

	// 再次尝试取消 r1（不应影响 r2）
	sess.CancelRun(runID1)
	if sess.ActiveRun == nil || sess.ActiveRun.RunID != runID2 {
		t.Fatal("cancel of old run should not affect active run")
	}
}

func TestWaitUntilIdle_ReturnsTrueWhenNoRun(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID:          "s_idle",
		WorkspaceID: "w1",
		UserID:      "u1",
		CacheRoot:   t.TempDir(),
	}

	if !sess.WaitUntilIdle(10 * time.Millisecond) {
		t.Fatal("expected idle when no run is active")
	}
}
