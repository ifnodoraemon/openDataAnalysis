package sqlite

import (
	"context"
	"database/sql"
	"sort"

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

// A run longer than the read window is read as its oldest events plus its
// newest ones. Keeping only the newest events would drop the session's opening
// task and the first compaction checkpoint, which is exactly the state history
// rebuild needs to replay the conversation; keeping only the oldest would drop
// the current turn. The middle is reported as omitted rather than silently
// discarded.
const (
	messageWindowHead = 1500
	messageWindowTail = 3500
)

const runMessageColumns = `id, run_id, session_id, workspace_id, type, name, tool_call_id, content, success, duration, created_at`

func (r *MessageRepository) ListByRun(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	return r.listWindow(ctx,
		`SELECT COUNT(*) FROM run_messages WHERE run_id = ?`,
		`SELECT `+runMessageColumns+` FROM run_messages WHERE run_id = ? ORDER BY created_at ASC`,
		`SELECT `+runMessageColumns+` FROM run_messages WHERE run_id = ? ORDER BY created_at ASC LIMIT ?`,
		`SELECT `+runMessageColumns+` FROM run_messages WHERE run_id = ? ORDER BY created_at DESC LIMIT ?`,
		runID)
}

func (r *MessageRepository) CountByRun(ctx context.Context, runID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_messages WHERE run_id = ?`, runID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *MessageRepository) ListByRunPath(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	const runPath = `WITH RECURSIVE run_path AS (
			SELECT id, parent_run_id, created_at FROM analysis_runs WHERE id = ?
			UNION ALL
			SELECT r.id, r.parent_run_id, r.created_at FROM analysis_runs r
			INNER JOIN run_path p ON r.id = p.parent_run_id
		)
		`
	const fromPath = ` FROM run_messages m JOIN run_path p ON m.run_id = p.id`
	return r.listWindow(ctx,
		runPath+`SELECT COUNT(*)`+fromPath,
		runPath+`SELECT m.id, m.run_id, m.session_id, m.workspace_id, m.type, m.name, m.tool_call_id, m.content, m.success, m.duration, m.created_at`+fromPath+` ORDER BY m.created_at ASC`,
		runPath+`SELECT m.id, m.run_id, m.session_id, m.workspace_id, m.type, m.name, m.tool_call_id, m.content, m.success, m.duration, m.created_at`+fromPath+` ORDER BY m.created_at ASC LIMIT ?`,
		runPath+`SELECT m.id, m.run_id, m.session_id, m.workspace_id, m.type, m.name, m.tool_call_id, m.content, m.success, m.duration, m.created_at`+fromPath+` ORDER BY m.created_at DESC LIMIT ?`,
		runID)
}

// listWindow reads the head and the tail of a result set and returns them in
// chronological order. A result set that fits the window is read whole.
func (r *MessageRepository) listWindow(ctx context.Context, countSQL, allSQL, headSQL, tailSQL, key string) ([]domain.RunMessage, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, countSQL, key).Scan(&total); err != nil {
		return nil, err
	}
	if total <= messageWindowHead+messageWindowTail {
		return r.queryRunMessages(ctx, allSQL, key)
	}

	head, err := r.queryRunMessages(ctx, headSQL, key, messageWindowHead)
	if err != nil {
		return nil, err
	}
	tailDescending, err := r.queryRunMessages(ctx, tailSQL, key, messageWindowTail)
	if err != nil {
		return nil, err
	}
	return mergeMessageWindows(head, tailDescending), nil
}

// mergeMessageWindows joins the head with the newest events returned in
// descending order, dropping any overlap and restoring chronological order.
func mergeMessageWindows(head, tailDescending []domain.RunMessage) []domain.RunMessage {
	merged := make([]domain.RunMessage, 0, len(head)+len(tailDescending))
	seen := make(map[string]struct{}, len(head)+len(tailDescending))
	for _, msg := range head {
		seen[msg.ID] = struct{}{}
		merged = append(merged, msg)
	}
	for i := len(tailDescending) - 1; i >= 0; i-- {
		msg := tailDescending[i]
		if _, ok := seen[msg.ID]; ok {
			continue
		}
		seen[msg.ID] = struct{}{}
		merged = append(merged, msg)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].CreatedAt.Before(merged[j].CreatedAt)
	})
	return merged
}

func (r *MessageRepository) queryRunMessages(ctx context.Context, query string, args ...interface{}) ([]domain.RunMessage, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
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
