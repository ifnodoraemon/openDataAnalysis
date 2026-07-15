package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SessionRepository struct{ db *sql.DB }

func NewSessionRepository(db *sql.DB) *SessionRepository { return &SessionRepository{db: db} }
func (r *SessionRepository) Create(ctx context.Context, session *domain.Session) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO sessions (id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.WorkspaceID, session.UserID, session.Title, string(session.Status), session.LastRunID, session.CreatedAt, session.UpdatedAt, session.LastSeenAt)
	return err
}

func (r *SessionRepository) GetByID(ctx context.Context, sessionID string) (*domain.Session, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at FROM sessions WHERE id = ?`, sessionID)
	var session domain.Session
	var status string
	var lastRun sql.NullString
	if err := row.Scan(&session.ID, &session.WorkspaceID, &session.UserID, &session.Title, &status, &lastRun, &session.CreatedAt, &session.UpdatedAt, &session.LastSeenAt); err != nil {
		return nil, err
	}
	session.Status = domain.SessionStatus(status)
	if lastRun.Valid {
		session.LastRunID = &lastRun.String
	}
	return &session, nil
}

func (r *SessionRepository) ListByUserWorkspace(ctx context.Context, userID, workspaceID string, limit int) ([]domain.Session, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at FROM sessions WHERE user_id = ? AND workspace_id = ? ORDER BY last_seen_at DESC, created_at DESC LIMIT ?`, userID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]domain.Session, 0, limit)
	for rows.Next() {
		var session domain.Session
		var status string
		var lastRun sql.NullString
		if err := rows.Scan(&session.ID, &session.WorkspaceID, &session.UserID, &session.Title, &status, &lastRun, &session.CreatedAt, &session.UpdatedAt, &session.LastSeenAt); err != nil {
			return nil, err
		}
		session.Status = domain.SessionStatus(status)
		if lastRun.Valid {
			session.LastRunID = &lastRun.String
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (r *SessionRepository) ListExpired(ctx context.Context, cutoff time.Time, limit int) ([]domain.Session, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, title, status, last_run_id, created_at, updated_at, last_seen_at FROM sessions WHERE last_seen_at < ? ORDER BY last_seen_at ASC LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []domain.Session
	for rows.Next() {
		var session domain.Session
		var status string
		var lastRun sql.NullString
		if err := rows.Scan(&session.ID, &session.WorkspaceID, &session.UserID, &session.Title, &status, &lastRun, &session.CreatedAt, &session.UpdatedAt, &session.LastSeenAt); err != nil {
			return nil, err
		}
		session.Status = domain.SessionStatus(status)
		if lastRun.Valid {
			session.LastRunID = &lastRun.String
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (r *SessionRepository) UpdateTitle(ctx context.Context, sessionID, title string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`, title, time.Now(), sessionID)
	return err
}

func (r *SessionRepository) Delete(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID)
	return err
}

func (r *SessionRepository) UpdateLastSeen(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, updated_at = ? WHERE id = ?`, time.Now(), time.Now(), sessionID)
	return err
}

func (r *SessionRepository) UpdateLastRun(ctx context.Context, sessionID, runID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE sessions SET last_run_id = ?, updated_at = ? WHERE id = ?`, runID, time.Now(), sessionID)
	return err
}
