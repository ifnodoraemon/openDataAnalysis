package queue

import (
	"context"
	"fmt"
	"log"

	"github.com/riverqueue/river"
)

// AnalysisRunJobArgs payload for asynchronous analysis task processing.
type AnalysisRunJobArgs struct {
	RunID       string `json:"run_id"`
	SessionID   string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

func (AnalysisRunJobArgs) Kind() string { return "analysis_run" }

// AnalysisRunWorker executes background analysis runs.
type AnalysisRunWorker struct {
	river.WorkerDefaults[AnalysisRunJobArgs]
	ExecuteFunc func(ctx context.Context, args AnalysisRunJobArgs) error
}

func (w *AnalysisRunWorker) Work(ctx context.Context, job *river.Job[AnalysisRunJobArgs]) error {
	log.Printf("[RiverWorker] processing analysis_run run_id=%s session_id=%s", job.Args.RunID, job.Args.SessionID)
	if w.ExecuteFunc != nil {
		if err := w.ExecuteFunc(ctx, job.Args); err != nil {
			return fmt.Errorf("analysis_run worker failed run_id=%s: %w", job.Args.RunID, err)
		}
	}
	return nil
}

// SessionCleanupJobArgs payload for background session TTL cleanup.
type SessionCleanupJobArgs struct {
	SessionTTLHours    int  `json:"session_ttl_hours"`
	TraceRetentionDays int  `json:"trace_retention_days"`
	TempCleanupOnStart bool `json:"temp_cleanup_on_start"`
}

func (SessionCleanupJobArgs) Kind() string { return "session_cleanup" }

// SessionCleanupWorker executes periodic session cleanup.
type SessionCleanupWorker struct {
	river.WorkerDefaults[SessionCleanupJobArgs]
	CleanupFunc func(ctx context.Context, args SessionCleanupJobArgs) error
}

func (w *SessionCleanupWorker) Work(ctx context.Context, job *river.Job[SessionCleanupJobArgs]) error {
	log.Printf("[RiverWorker] executing periodic session_cleanup")
	if w.CleanupFunc != nil {
		if err := w.CleanupFunc(ctx, job.Args); err != nil {
			return fmt.Errorf("session_cleanup worker failed: %w", err)
		}
	}
	return nil
}
