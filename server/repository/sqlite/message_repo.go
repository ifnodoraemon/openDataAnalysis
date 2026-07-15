package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type MessageRepository struct{ db *sql.DB }

func NewMessageRepository(db *sql.DB) *MessageRepository { return &MessageRepository{db: db} }
func (r *MessageRepository) Create(ctx context.Context, msg *domain.RunMessage) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO run_messages (id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, msg.ID, msg.RunID, msg.SessionID, msg.WorkspaceID, msg.Type, msg.Name, msg.ToolCallID, msg.Content, msg.Success, msg.Duration, msg.CreatedAt)
	return err
}

func (r *MessageRepository) ListByRun(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at
		FROM run_messages
		WHERE run_id = ?
		ORDER BY created_at ASC
		LIMIT 5000
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.RunMessage
	for rows.Next() {
		var msg domain.RunMessage
		var success sql.NullBool
		var duration sql.NullInt64
		var toolCallID sql.NullString
		if err := rows.Scan(
			&msg.ID, &msg.RunID, &msg.SessionID, &msg.WorkspaceID,
			&msg.Type, &msg.Name, &toolCallID, &msg.Content, &success, &duration, &msg.CreatedAt,
		); err != nil {
			return nil, err
		}
		if toolCallID.Valid {
			t := toolCallID.String
			msg.ToolCallID = &t
		}
		if success.Valid {
			s := success.Bool
			msg.Success = &s
		}
		if duration.Valid {
			d := duration.Int64
			msg.Duration = &d
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

func (r *MessageRepository) ListRecentByRun(ctx context.Context, runID string, limit int) ([]domain.RunMessage, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at
		FROM run_messages
		WHERE run_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []domain.RunMessage
	for rows.Next() {
		var msg domain.RunMessage
		var success sql.NullBool
		var duration sql.NullInt64
		var toolCallID sql.NullString
		if err := rows.Scan(
			&msg.ID, &msg.RunID, &msg.SessionID, &msg.WorkspaceID,
			&msg.Type, &msg.Name, &toolCallID, &msg.Content, &success, &duration, &msg.CreatedAt,
		); err != nil {
			return nil, err
		}
		if toolCallID.Valid {
			t := toolCallID.String
			msg.ToolCallID = &t
		}
		if success.Valid {
			s := success.Bool
			msg.Success = &s
		}
		if duration.Valid {
			d := duration.Int64
			msg.Duration = &d
		}
		messages = append(messages, msg)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, rows.Err()
}
