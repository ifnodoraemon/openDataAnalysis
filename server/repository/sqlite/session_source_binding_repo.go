package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SessionSourceBindingRepository struct{ db *sql.DB }

func NewSessionSourceBindingRepository(db *sql.DB) *SessionSourceBindingRepository {
	return &SessionSourceBindingRepository{db: db}
}

func (r *SessionSourceBindingRepository) Upsert(ctx context.Context, binding *domain.SessionSourceBinding) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO session_source_bindings (session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, source_id, source_object_key) DO UPDATE SET active_snapshot_id = excluded.active_snapshot_id, updated_at = excluded.updated_at`,
		binding.SessionID, binding.SourceID, binding.SourceObjectKey, binding.ActiveSnapshotID, binding.CreatedAt, binding.UpdatedAt)
	return err
}

func (r *SessionSourceBindingRepository) GetBySession(ctx context.Context, sessionID string) ([]domain.SessionSourceBinding, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at FROM session_source_bindings WHERE session_id = ? ORDER BY updated_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.SessionSourceBinding
	for rows.Next() {
		var b domain.SessionSourceBinding
		if err := rows.Scan(&b.SessionID, &b.SourceID, &b.SourceObjectKey, &b.ActiveSnapshotID, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		results = append(results, b)
	}
	return results, rows.Err()
}

func (r *SessionSourceBindingRepository) GetBySessionSourceObject(ctx context.Context, sessionID, sourceID, sourceObjectKey string) (*domain.SessionSourceBinding, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at FROM session_source_bindings WHERE session_id = ? AND source_id = ? AND source_object_key = ?`, sessionID, sourceID, sourceObjectKey)
	var b domain.SessionSourceBinding
	if err := row.Scan(&b.SessionID, &b.SourceID, &b.SourceObjectKey, &b.ActiveSnapshotID, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	return &b, nil
}

func (r *SessionSourceBindingRepository) Delete(ctx context.Context, sessionID, sourceID, sourceObjectKey string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM session_source_bindings WHERE session_id = ? AND source_id = ? AND source_object_key = ?`, sessionID, sourceID, sourceObjectKey)
	return err
}
