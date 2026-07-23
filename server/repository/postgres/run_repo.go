package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type RunRepository struct {
	db DBTX
}

func NewRunRepository(db DBTX) *RunRepository {
	return &RunRepository{db: db}
}

func (r *RunRepository) Create(ctx context.Context, run *domain.AnalysisRun) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO analysis_runs (id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		run.ID, run.SessionID, run.WorkspaceID, run.UserID, run.ParentRunID, string(run.RunKind), run.DelegateRole, run.GoalID, string(run.Status), run.InputMessage, run.Summary, run.ErrorMessage, run.ReportFileID, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create run: %w", err)
	}
	return nil
}

func (r *RunRepository) GetByID(ctx context.Context, runID string) (*domain.AnalysisRun, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at FROM analysis_runs WHERE id = $1`,
		runID,
	)
	var run domain.AnalysisRun
	var runKind, status string
	err := row.Scan(&run.ID, &run.SessionID, &run.WorkspaceID, &run.UserID, &run.ParentRunID, &runKind, &run.DelegateRole, &run.GoalID, &status, &run.InputMessage, &run.Summary, &run.ErrorMessage, &run.ReportFileID, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get run by id: %w", err)
	}
	run.RunKind = domain.RunKind(runKind)
	run.Status = domain.RunStatus(status)
	return &run, nil
}

func (r *RunRepository) ListBySession(ctx context.Context, sessionID string, limit int) ([]domain.AnalysisRun, error) {
	if limit == 0 {
		limit = 20
	}
	query := `SELECT id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at FROM analysis_runs WHERE session_id = $1 AND (parent_run_id IS NULL OR parent_run_id = '') ORDER BY created_at DESC`
	var (
		rows pgx.Rows
		err  error
	)
	if limit < 0 {
		rows, err = r.db.Query(ctx, query, sessionID)
	} else {
		rows, err = r.db.Query(ctx, query+` LIMIT $2`, sessionID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list runs by session: %w", err)
	}
	defer rows.Close()

	initialCap := limit
	if initialCap < 0 {
		initialCap = 0
	}
	runs := make([]domain.AnalysisRun, 0, initialCap)
	for rows.Next() {
		var run domain.AnalysisRun
		var runKind, status string
		if err := rows.Scan(&run.ID, &run.SessionID, &run.WorkspaceID, &run.UserID, &run.ParentRunID, &runKind, &run.DelegateRole, &run.GoalID, &status, &run.InputMessage, &run.Summary, &run.ErrorMessage, &run.ReportFileID, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan run: %w", err)
		}
		run.RunKind = domain.RunKind(runKind)
		run.Status = domain.RunStatus(status)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating runs: %w", err)
	}
	return runs, nil
}

func (r *RunRepository) ListByParent(ctx context.Context, parentRunID string) ([]domain.AnalysisRun, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at FROM analysis_runs WHERE parent_run_id = $1 ORDER BY created_at ASC LIMIT 200`,
		parentRunID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list runs by parent: %w", err)
	}
	defer rows.Close()

	runs := make([]domain.AnalysisRun, 0, 8)
	for rows.Next() {
		var run domain.AnalysisRun
		var runKind, status string
		if err := rows.Scan(&run.ID, &run.SessionID, &run.WorkspaceID, &run.UserID, &run.ParentRunID, &runKind, &run.DelegateRole, &run.GoalID, &status, &run.InputMessage, &run.Summary, &run.ErrorMessage, &run.ReportFileID, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan run: %w", err)
		}
		run.RunKind = domain.RunKind(runKind)
		run.Status = domain.RunStatus(status)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating runs by parent: %w", err)
	}
	return runs, nil
}

func (r *RunRepository) UpdateStatus(ctx context.Context, runID string, status domain.RunStatus, errMsg *string) error {
	now := time.Now()
	var finishedAt *time.Time
	switch status {
	case domain.RunStatusCompleted, domain.RunStatusCancelled, domain.RunStatusFailed:
		finishedAt = &now
	}
	_, err := r.db.Exec(ctx,
		`UPDATE analysis_runs SET status = $1, error_message = $2, finished_at = COALESCE($3, finished_at), updated_at = $4 WHERE id = $5`,
		string(status), errMsg, finishedAt, now, runID,
	)
	if err != nil {
		return fmt.Errorf("failed to update run status: %w", err)
	}
	return nil
}

func (r *RunRepository) UpdateSummary(ctx context.Context, runID, summary string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE analysis_runs SET summary = $1, updated_at = $2 WHERE id = $3`,
		summary, time.Now(), runID,
	)
	if err != nil {
		return fmt.Errorf("failed to update run summary: %w", err)
	}
	return nil
}

func (r *RunRepository) BindReportFile(ctx context.Context, runID, reportFileID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE analysis_runs SET report_file_id = $1, updated_at = $2 WHERE id = $3`,
		reportFileID, time.Now(), runID,
	)
	if err != nil {
		return fmt.Errorf("failed to bind report file: %w", err)
	}
	return nil
}
