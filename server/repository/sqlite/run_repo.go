package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type RunRepository struct{ db *sql.DB }

func NewRunRepository(db *sql.DB) *RunRepository { return &RunRepository{db: db} }
func (r *RunRepository) Create(ctx context.Context, run *domain.AnalysisRun) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO analysis_runs (id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.SessionID, run.WorkspaceID, run.UserID, run.ParentRunID, string(run.RunKind), run.DelegateRole, run.GoalID, string(run.Status), run.InputMessage, run.Summary, run.ErrorMessage, run.ReportFileID, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	return err
}

func (r *RunRepository) GetByID(ctx context.Context, runID string) (*domain.AnalysisRun, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at FROM analysis_runs WHERE id = ?`, runID)
	var run domain.AnalysisRun
	var status, runKind string
	var parentRunID, errMsg, reportID, goalID sql.NullString
	var startedAt, finishedAt sql.NullTime
	if err := row.Scan(&run.ID, &run.SessionID, &run.WorkspaceID, &run.UserID, &parentRunID, &runKind, &run.DelegateRole, &goalID, &status, &run.InputMessage, &run.Summary, &errMsg, &reportID, &startedAt, &finishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	if parentRunID.Valid {
		run.ParentRunID = &parentRunID.String
	}
	if goalID.Valid {
		run.GoalID = &goalID.String
	}
	run.RunKind = domain.RunKind(runKind)
	run.Status = domain.RunStatus(status)
	if errMsg.Valid {
		run.ErrorMessage = &errMsg.String
	}
	if reportID.Valid {
		run.ReportFileID = &reportID.String
	}
	if startedAt.Valid {
		run.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		run.FinishedAt = &finishedAt.Time
	}
	return &run, nil
}

func (r *RunRepository) ListBySession(ctx context.Context, sessionID string, limit int) ([]domain.AnalysisRun, error) {
	if limit == 0 {
		limit = 20
	}
	query := `SELECT id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at FROM analysis_runs WHERE session_id = ? AND (parent_run_id IS NULL OR parent_run_id = '') ORDER BY created_at DESC`
	var (
		rows *sql.Rows
		err  error
	)
	if limit < 0 {
		rows, err = r.db.QueryContext(ctx, query, sessionID)
	} else {
		rows, err = r.db.QueryContext(ctx, query+` LIMIT ?`, sessionID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	initialCap := limit
	if initialCap < 0 {
		initialCap = 0
	}
	runs := make([]domain.AnalysisRun, 0, initialCap)
	for rows.Next() {
		var run domain.AnalysisRun
		var status, runKind string
		var parentRunID, errMsg, reportID, goalID sql.NullString
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&run.ID, &run.SessionID, &run.WorkspaceID, &run.UserID, &parentRunID, &runKind, &run.DelegateRole, &goalID, &status, &run.InputMessage, &run.Summary, &errMsg, &reportID, &startedAt, &finishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		if parentRunID.Valid {
			run.ParentRunID = &parentRunID.String
		}
		if goalID.Valid {
			run.GoalID = &goalID.String
		}
		run.RunKind = domain.RunKind(runKind)
		run.Status = domain.RunStatus(status)
		if errMsg.Valid {
			run.ErrorMessage = &errMsg.String
		}
		if reportID.Valid {
			run.ReportFileID = &reportID.String
		}
		if startedAt.Valid {
			run.StartedAt = &startedAt.Time
		}
		if finishedAt.Valid {
			run.FinishedAt = &finishedAt.Time
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (r *RunRepository) ListByParent(ctx context.Context, parentRunID string) ([]domain.AnalysisRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, session_id, workspace_id, user_id, parent_run_id, run_kind, delegate_role, goal_id, status, input_message, summary, error_message, report_file_id, started_at, finished_at, created_at, updated_at FROM analysis_runs WHERE parent_run_id = ? ORDER BY created_at ASC LIMIT 200`, parentRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]domain.AnalysisRun, 0, 8)
	for rows.Next() {
		var run domain.AnalysisRun
		var status, runKind string
		var nextParentRunID, errMsg, reportID, goalID sql.NullString
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&run.ID, &run.SessionID, &run.WorkspaceID, &run.UserID, &nextParentRunID, &runKind, &run.DelegateRole, &goalID, &status, &run.InputMessage, &run.Summary, &errMsg, &reportID, &startedAt, &finishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		if nextParentRunID.Valid {
			run.ParentRunID = &nextParentRunID.String
		}
		if goalID.Valid {
			run.GoalID = &goalID.String
		}
		run.RunKind = domain.RunKind(runKind)
		run.Status = domain.RunStatus(status)
		if errMsg.Valid {
			run.ErrorMessage = &errMsg.String
		}
		if reportID.Valid {
			run.ReportFileID = &reportID.String
		}
		if startedAt.Valid {
			run.StartedAt = &startedAt.Time
		}
		if finishedAt.Valid {
			run.FinishedAt = &finishedAt.Time
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (r *RunRepository) UpdateStatus(ctx context.Context, runID string, status domain.RunStatus, errMsg *string) error {
	now := time.Now()
	var finishedAt interface{}
	switch status {
	case domain.RunStatusCompleted, domain.RunStatusCancelled, domain.RunStatusFailed:
		finishedAt = now
	default:
		finishedAt = nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE analysis_runs SET status = ?, error_message = ?, finished_at = COALESCE(?, finished_at), updated_at = ? WHERE id = ?`, string(status), errMsg, finishedAt, now, runID)
	return err
}

func (r *RunRepository) UpdateSummary(ctx context.Context, runID, summary string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE analysis_runs SET summary = ?, updated_at = ? WHERE id = ?`, summary, time.Now(), runID)
	return err
}

func (r *RunRepository) BindReportFile(ctx context.Context, runID, reportFileID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE analysis_runs SET report_file_id = ?, updated_at = ? WHERE id = ?`, reportFileID, time.Now(), runID)
	return err
}
