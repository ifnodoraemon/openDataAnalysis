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

type SessionRepository struct {
	db DBTX
}

func NewSessionRepository(db DBTX) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, session *domain.Session) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO sessions (id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		session.ID, session.WorkspaceID, session.UserID, session.Title, string(session.Status), session.LastRunID, session.CreatedAt, session.UpdatedAt, session.LastSeenAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	return nil
}

func (r *SessionRepository) GetByID(ctx context.Context, sessionID string) (*domain.Session, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at FROM sessions WHERE id = $1`,
		sessionID,
	)
	var session domain.Session
	var status string
	err := row.Scan(&session.ID, &session.WorkspaceID, &session.UserID, &session.Title, &status, &session.LastRunID, &session.CreatedAt, &session.UpdatedAt, &session.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session by id: %w", err)
	}
	session.Status = domain.SessionStatus(status)
	return &session, nil
}

func (r *SessionRepository) ListByUserWorkspace(ctx context.Context, userID, workspaceID string, limit int) ([]domain.Session, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.Query(ctx,
		`SELECT id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at FROM sessions WHERE user_id = $1 AND workspace_id = $2 ORDER BY last_seen_at DESC, created_at DESC LIMIT $3`,
		userID, workspaceID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions by user workspace: %w", err)
	}
	defer rows.Close()

	sessions := make([]domain.Session, 0, limit)
	for rows.Next() {
		var session domain.Session
		var status string
		if err := rows.Scan(&session.ID, &session.WorkspaceID, &session.UserID, &session.Title, &status, &session.LastRunID, &session.CreatedAt, &session.UpdatedAt, &session.LastSeenAt); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		session.Status = domain.SessionStatus(status)
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}
	return sessions, nil
}

func (r *SessionRepository) ListExpired(ctx context.Context, cutoff time.Time, limit int) ([]domain.Session, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.Query(ctx,
		`SELECT id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at FROM sessions WHERE last_seen_at < $1 ORDER BY last_seen_at ASC LIMIT $2`,
		cutoff, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list expired sessions: %w", err)
	}
	defer rows.Close()

	var sessions []domain.Session
	for rows.Next() {
		var session domain.Session
		var status string
		if err := rows.Scan(&session.ID, &session.WorkspaceID, &session.UserID, &session.Title, &status, &session.LastRunID, &session.CreatedAt, &session.UpdatedAt, &session.LastSeenAt); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		session.Status = domain.SessionStatus(status)
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating expired sessions: %w", err)
	}
	return sessions, nil
}

func (r *SessionRepository) UpdateTitle(ctx context.Context, sessionID, title string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE sessions SET title = $1, updated_at = $2 WHERE id = $3`,
		title, time.Now(), sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update session title: %w", err)
	}
	return nil
}

func (r *SessionRepository) Delete(ctx context.Context, sessionID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (r *SessionRepository) UpdateLastSeen(ctx context.Context, sessionID string) error {
	now := time.Now()
	_, err := r.db.Exec(ctx,
		`UPDATE sessions SET last_seen_at = $1, updated_at = $2 WHERE id = $3`,
		now, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update session last seen: %w", err)
	}
	return nil
}

func (r *SessionRepository) UpdateLastRun(ctx context.Context, sessionID, runID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE sessions SET last_run_id = $1, updated_at = $2 WHERE id = $3`,
		runID, time.Now(), sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update session last run: %w", err)
	}
	return nil
}
