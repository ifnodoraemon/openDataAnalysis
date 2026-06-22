package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SourceSnapshotRepository struct{ db *sql.DB }

func NewSourceSnapshotRepository(db *sql.DB) *SourceSnapshotRepository {
	return &SourceSnapshotRepository{db: db}
}

const snapshotCols = `id, session_id, source_id, upstream_kind, upstream_schema, upstream_object, analysis_table_name, row_count, column_count, status, error_message, schema_signature, imported_at, rows_imported, rows_skipped, import_row_limit, import_truncated, import_duration_ms, profile_duration_ms, snapshot_size_bytes, profile_mode`

func scanSnapshot(row interface{ Scan(...interface{}) error }) (*domain.SourceSnapshot, error) {
	var s domain.SourceSnapshot
	var status, profileMode string
	var errMsg sql.NullString
	if err := row.Scan(&s.ID, &s.SessionID, &s.SourceID, &s.UpstreamKind, &s.UpstreamSchema, &s.UpstreamObject, &s.AnalysisTableName, &s.RowCount, &s.ColumnCount, &status, &errMsg, &s.SchemaSignature, &s.ImportedAt, &s.RowsImported, &s.RowsSkipped, &s.ImportRowLimit, &s.ImportTruncated, &s.ImportDurationMs, &s.ProfileDurationMs, &s.SnapshotSizeBytes, &profileMode); err != nil {
		return nil, err
	}
	s.Status = domain.SnapshotStatus(status)
	s.ProfileMode = domain.ProfileMode(profileMode)
	if errMsg.Valid {
		s.ErrorMessage = &errMsg.String
	}
	return &s, nil
}

func (r *SourceSnapshotRepository) Create(ctx context.Context, snapshot *domain.SourceSnapshot) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO source_snapshots (`+snapshotCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.ID, snapshot.SessionID, snapshot.SourceID, snapshot.UpstreamKind, snapshot.UpstreamSchema, snapshot.UpstreamObject, snapshot.AnalysisTableName, snapshot.RowCount, snapshot.ColumnCount, string(snapshot.Status), snapshot.ErrorMessage, snapshot.SchemaSignature, snapshot.ImportedAt, snapshot.RowsImported, snapshot.RowsSkipped, snapshot.ImportRowLimit, snapshot.ImportTruncated, snapshot.ImportDurationMs, snapshot.ProfileDurationMs, snapshot.SnapshotSizeBytes, string(snapshot.ProfileMode))
	return err
}

func (r *SourceSnapshotRepository) GetByID(ctx context.Context, id string) (*domain.SourceSnapshot, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+snapshotCols+` FROM source_snapshots WHERE id = ?`, id)
	return scanSnapshot(row)
}

func (r *SourceSnapshotRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.SourceSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+snapshotCols+` FROM source_snapshots WHERE session_id = ? ORDER BY imported_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []domain.SourceSnapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *s)
	}
	return results, rows.Err()
}

func (r *SourceSnapshotRepository) ListBySource(ctx context.Context, sourceID string) ([]domain.SourceSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+snapshotCols+` FROM source_snapshots WHERE source_id = ? ORDER BY imported_at DESC`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []domain.SourceSnapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *s)
	}
	return results, rows.Err()
}

func (r *SourceSnapshotRepository) UpdateStatus(ctx context.Context, id string, status domain.SnapshotStatus, errorMsg *string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE source_snapshots SET status = ?, error_message = ? WHERE id = ?`,
		string(status), errorMsg, id)
	return err
}

func (r *SourceSnapshotRepository) UpdateRuntimeFacts(ctx context.Context, id string, rowsImported, rowsSkipped, importRowLimit int, importTruncated bool, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE source_snapshots SET rows_imported = ?, rows_skipped = ?, import_row_limit = ?, import_truncated = ?, import_duration_ms = ?, profile_duration_ms = ?, snapshot_size_bytes = ?, profile_mode = ? WHERE id = ?`,
		rowsImported, rowsSkipped, importRowLimit, importTruncated, importDurationMs, profileDurationMs, snapshotSizeBytes, string(profileMode), id)
	return err
}

func (r *SourceSnapshotRepository) UpdateSnapshotCompletion(ctx context.Context, id string, rowCount, colCount int, schemaSignature string, rowsImported, rowsSkipped, importRowLimit int, importTruncated bool, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE source_snapshots SET row_count = ?, column_count = ?, schema_signature = ?, rows_imported = ?, rows_skipped = ?, import_row_limit = ?, import_truncated = ?, import_duration_ms = ?, profile_duration_ms = ?, snapshot_size_bytes = ?, profile_mode = ? WHERE id = ?`,
		rowCount, colCount, schemaSignature, rowsImported, rowsSkipped, importRowLimit, importTruncated, importDurationMs, profileDurationMs, snapshotSizeBytes, string(profileMode), id)
	return err
}

func (r *SourceSnapshotRepository) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM source_snapshots WHERE id = ?`, id)
	return err
}
