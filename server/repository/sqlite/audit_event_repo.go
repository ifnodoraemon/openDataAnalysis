package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type AuditEventRepository struct{ db *sql.DB }

func NewAuditEventRepository(db *sql.DB) *AuditEventRepository {
	return &AuditEventRepository{db: db}
}

func (r *AuditEventRepository) Create(ctx context.Context, event *domain.AuditEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO audit_events (
			id, workspace_id, session_id, run_id, actor_user_id, event_type, resource_type, resource_id, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.WorkspaceID, event.SessionID, event.RunID, event.ActorUserID, event.EventType, event.ResourceType, event.ResourceID, event.PayloadJSON, event.CreatedAt)
	return err
}

func (r *AuditEventRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_id, session_id, run_id, actor_user_id, event_type, resource_type, resource_id, payload_json, created_at
		FROM audit_events
		WHERE workspace_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		if err := rows.Scan(
			&event.ID, &event.WorkspaceID, &event.SessionID, &event.RunID, &event.ActorUserID, &event.EventType,
			&event.ResourceType, &event.ResourceID, &event.PayloadJSON, &event.CreatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
