package postgres

import (
	"context"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type MessageRepository struct {
	db DBTX
}

func NewMessageRepository(db DBTX) *MessageRepository {
	return &MessageRepository{db: db}
}

func scanRunMessage(row pgx.CollectableRow) (domain.RunMessage, error) {
	var msg domain.RunMessage
	err := row.Scan(
		&msg.ID, &msg.RunID, &msg.SessionID, &msg.WorkspaceID,
		&msg.Type, &msg.Name, &msg.ToolCallID, &msg.Content,
		&msg.Success, &msg.Duration, &msg.CreatedAt,
	)
	return msg, err
}

func (r *MessageRepository) Create(ctx context.Context, msg *domain.RunMessage) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO run_messages (id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, msg.ID, msg.RunID, msg.SessionID, msg.WorkspaceID, msg.Type, msg.Name, msg.ToolCallID, msg.Content, msg.Success, msg.Duration, msg.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to create run message: %w", err)
	}
	return nil
}

func (r *MessageRepository) ListByRun(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at
		FROM run_messages
		WHERE run_id = $1
		ORDER BY created_at ASC
		LIMIT 5000
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages by run: %w", err)
	}
	defer rows.Close()

	messages, err := pgx.CollectRows(rows, scanRunMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to collect messages by run: %w", err)
	}
	return messages, nil
}

func (r *MessageRepository) ListByRunPath(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	rows, err := r.db.Query(ctx, `
		WITH RECURSIVE run_path AS (
			SELECT id, parent_run_id, created_at FROM analysis_runs WHERE id = $1
			UNION ALL
			SELECT r.id, r.parent_run_id, r.created_at FROM analysis_runs r
			INNER JOIN run_path p ON r.id = p.parent_run_id
		)
		SELECT m.id, m.run_id, m.session_id, m.workspace_id, m.type, m.name, m.tool_call_id, m.content, m.success, m.duration, m.created_at
		FROM run_messages m JOIN run_path p ON m.run_id = p.id
		ORDER BY m.created_at ASC LIMIT 5000
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages by run path: %w", err)
	}
	defer rows.Close()

	messages, err := pgx.CollectRows(rows, scanRunMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to collect messages by run path: %w", err)
	}
	return messages, nil
}

func (r *MessageRepository) ListRecentByRun(ctx context.Context, runID string, limit int) ([]domain.RunMessage, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at
		FROM run_messages
		WHERE run_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list recent messages by run: %w", err)
	}
	defer rows.Close()

	messages, err := pgx.CollectRows(rows, scanRunMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to collect recent messages by run: %w", err)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}
