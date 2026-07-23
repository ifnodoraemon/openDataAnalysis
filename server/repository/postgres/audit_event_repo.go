package postgres

import (
	"context"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type AuditEventRepository struct {
	db DBTX
}

func NewAuditEventRepository(db DBTX) *AuditEventRepository {
	return &AuditEventRepository{db: db}
}

func (r *AuditEventRepository) Create(ctx context.Context, event *domain.AuditEvent) error {
	query := `
		INSERT INTO audit_events (
			id, workspace_id, session_id, run_id, actor_user_id, event_type, resource_type, resource_id, payload_json, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, query,
		event.ID, event.WorkspaceID, event.SessionID, event.RunID, event.ActorUserID, event.EventType, event.ResourceType, event.ResourceID, event.PayloadJSON, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to create audit event: %w", err)
	}
	return nil
}

func (r *AuditEventRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, workspace_id, session_id, run_id, actor_user_id, event_type, resource_type, resource_id, payload_json, created_at
		FROM audit_events
		WHERE workspace_id = $1
		ORDER BY created_at DESC
		LIMIT $2`

	rows, err := r.db.Query(ctx, query, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit events by workspace: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanAuditEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to collect audit events: %w", err)
	}
	return results, nil
}

func scanAuditEvent(row pgx.CollectableRow) (domain.AuditEvent, error) {
	var event domain.AuditEvent
	err := row.Scan(
		&event.ID, &event.WorkspaceID, &event.SessionID, &event.RunID, &event.ActorUserID, &event.EventType,
		&event.ResourceType, &event.ResourceID, &event.PayloadJSON, &event.CreatedAt,
	)
	return event, err
}
