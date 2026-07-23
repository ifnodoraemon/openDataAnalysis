package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type SessionSourceBindingRepository struct {
	db DBTX
}

func NewSessionSourceBindingRepository(db DBTX) *SessionSourceBindingRepository {
	return &SessionSourceBindingRepository{db: db}
}

func (r *SessionSourceBindingRepository) Upsert(ctx context.Context, binding *domain.SessionSourceBinding) error {
	query := `INSERT INTO session_source_bindings (session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(session_id, source_id, source_object_key) DO UPDATE SET active_snapshot_id = EXCLUDED.active_snapshot_id, updated_at = EXCLUDED.updated_at`

	_, err := r.db.Exec(ctx, query,
		binding.SessionID, binding.SourceID, binding.SourceObjectKey, binding.ActiveSnapshotID, binding.CreatedAt, binding.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert session source binding: %w", err)
	}
	return nil
}

func (r *SessionSourceBindingRepository) GetBySession(ctx context.Context, sessionID string) ([]domain.SessionSourceBinding, error) {
	query := `SELECT session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at
		FROM session_source_bindings WHERE session_id = $1 ORDER BY updated_at DESC`

	rows, err := r.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query session source bindings: %w", err)
	}

	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SessionSourceBinding, error) {
		var b domain.SessionSourceBinding
		err := row.Scan(&b.SessionID, &b.SourceID, &b.SourceObjectKey, &b.ActiveSnapshotID, &b.CreatedAt, &b.UpdatedAt)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to collect session source bindings: %w", err)
	}
	return results, nil
}

func (r *SessionSourceBindingRepository) GetBySessionSourceObject(ctx context.Context, sessionID, sourceID, sourceObjectKey string) (*domain.SessionSourceBinding, error) {
	query := `SELECT session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at
		FROM session_source_bindings WHERE session_id = $1 AND source_id = $2 AND source_object_key = $3`

	row := r.db.QueryRow(ctx, query, sessionID, sourceID, sourceObjectKey)
	var b domain.SessionSourceBinding
	if err := row.Scan(&b.SessionID, &b.SourceID, &b.SourceObjectKey, &b.ActiveSnapshotID, &b.CreatedAt, &b.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get session source binding: %w", err)
	}
	return &b, nil
}

func (r *SessionSourceBindingRepository) Delete(ctx context.Context, sessionID, sourceID, sourceObjectKey string) error {
	query := `DELETE FROM session_source_bindings WHERE session_id = $1 AND source_id = $2 AND source_object_key = $3`

	_, err := r.db.Exec(ctx, query, sessionID, sourceID, sourceObjectKey)
	if err != nil {
		return fmt.Errorf("failed to delete session source binding: %w", err)
	}
	return nil
}
