package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
)

type sessionDeletionPlan struct {
	sourceFileIDs []string
	reportFileIDs []string
	storageKeys   []string
}

func deleteSessionResources(ctx context.Context, session domain.Session) error {
	if metadataStore == nil || metadataStore.DB == nil {
		return fmt.Errorf("metadata store is not initialized")
	}

	plan, err := buildSessionDeletionPlan(ctx, metadataStore.DB, session.ID)
	if err != nil {
		return err
	}

	if sessionManager != nil {
		if err := sessionManager.Stop(session.ID, session.WorkspaceID, session.UserID); err != nil {
			return err
		}
	}
	if len(plan.storageKeys) > 0 {
		if fileService == nil || fileService.Storage == nil {
			return fmt.Errorf("object storage is not initialized for session deletion")
		}
		var storageErrors error
		for _, key := range plan.storageKeys {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key {
				storageErrors = errors.Join(storageErrors, fmt.Errorf("stored object key must be a non-empty exact value"))
				continue
			}
			if err := fileService.Storage.Delete(ctx, key); err != nil {
				storageErrors = errors.Join(storageErrors, fmt.Errorf("delete session storage object %s: %w", key, err))
			}
		}
		if storageErrors != nil {
			return storageErrors
		}
	}

	tx, err := metadataStore.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback session deletion session_id=%s: %v", session.ID, rollbackErr)
		}
	}()

	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM run_messages WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM reports WHERE run_id IN (SELECT id FROM analysis_runs WHERE session_id = ?)`), session.ID); err != nil {
		return err
	}
	if len(plan.reportFileIDs) > 0 {
		if err := deleteFilesByIDs(ctx, tx, plan.reportFileIDs); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM analysis_runs WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM session_files WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM semantic_confirmations WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM semantic_profiles WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM source_snapshots WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM session_source_bindings WHERE session_id = ?`), session.ID); err != nil {
		return err
	}
	if len(plan.sourceFileIDs) > 0 {
		if err := deleteFilesByIDs(ctx, tx, plan.sourceFileIDs); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM sessions WHERE id = ?`), session.ID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	GlobalSSEBroker.CloseSession(session.ID)
	if sessionManager != nil {
		if err := sessionManager.Delete(session.ID, session.WorkspaceID, session.UserID); err != nil {
			log.Printf("delete in-memory session failed session_id=%s err=%v", session.ID, err)
		}
	}

	return nil
}

func buildSessionDeletionPlan(ctx context.Context, db *sql.DB, sessionID string) (sessionDeletionPlan, error) {
	sourceFileIDs, err := queryStrings(ctx, db, `
		SELECT DISTINCT sf1.file_id
		FROM session_files sf1
		JOIN files f ON f.id = sf1.file_id
		WHERE sf1.session_id = ?
		  AND f.visibility = 'private'
		  AND NOT EXISTS (
		      SELECT 1 FROM session_files sf2
		      WHERE sf2.file_id = sf1.file_id AND sf2.session_id != ?
		  )
	`, sessionID, sessionID)
	if err != nil {
		return sessionDeletionPlan{}, err
	}
	reportFileIDs, err := queryStrings(ctx, db, `
		SELECT DISTINCT report_file_id
		FROM analysis_runs
		WHERE session_id = ? AND report_file_id IS NOT NULL AND report_file_id != ''
	`, sessionID)
	if err != nil {
		return sessionDeletionPlan{}, err
	}
	storageKeys, err := queryStrings(ctx, db, `
		SELECT DISTINCT storage_key
		FROM files
		WHERE id IN (
			SELECT sf1.file_id FROM session_files sf1
			JOIN files f ON f.id = sf1.file_id
			WHERE sf1.session_id = ?
			  AND f.visibility = 'private'
			  AND NOT EXISTS (
			      SELECT 1 FROM session_files sf2
			      WHERE sf2.file_id = sf1.file_id AND sf2.session_id != ?
			  )
			UNION
			SELECT report_file_id FROM analysis_runs WHERE session_id = ? AND report_file_id IS NOT NULL AND report_file_id != ''
		)
	`, sessionID, sessionID, sessionID)
	if err != nil {
		return sessionDeletionPlan{}, err
	}
	reportHTMLKeys, err := queryStrings(ctx, db, `
		SELECT DISTINCT html_storage_key
		FROM reports
		WHERE run_id IN (SELECT id FROM analysis_runs WHERE session_id = ?)
	`, sessionID)
	if err != nil {
		return sessionDeletionPlan{}, err
	}
	return sessionDeletionPlan{
		sourceFileIDs: sourceFileIDs,
		reportFileIDs: reportFileIDs,
		storageKeys:   uniqueStrings(append(storageKeys, reportHTMLKeys...)),
	}, nil
}

func rebindSessionDeletionQuery(query string) string {
	if metadataStore == nil || metadataStore.Dialect != metadata.DialectPostgres {
		return query
	}
	var sb strings.Builder
	param := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			param++
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(param))
			continue
		}
		sb.WriteByte(query[i])
	}
	return sb.String()
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, rebindSessionDeletionQuery(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]string, 0, 8)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return uniqueStrings(items), rows.Err()
}

func deleteFilesByIDs(ctx context.Context, tx *sql.Tx, ids []string) error {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := tx.ExecContext(ctx, rebindSessionDeletionQuery(`DELETE FROM files WHERE id IN (`+placeholders+`)`), args...)
	return err
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	unique := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		unique = append(unique, item)
	}
	return unique
}
