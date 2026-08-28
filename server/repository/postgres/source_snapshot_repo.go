package postgres

import (
	"context"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type SourceSnapshotRepository struct {
	db DBTX
}

func NewSourceSnapshotRepository(db DBTX) *SourceSnapshotRepository {
	return &SourceSnapshotRepository{db: db}
}

const snapshotCols = `id, session_id, source_id, upstream_kind, upstream_schema, upstream_object, analysis_table_name, row_count, column_count, status, error_message, schema_signature, imported_at, rows_imported, rows_skipped, import_row_limit, import_truncated, import_duration_ms, profile_duration_ms, snapshot_size_bytes, profile_mode, mode`

func scanSnapshot(row pgx.Row) (*domain.SourceSnapshot, error) {
	var s domain.SourceSnapshot
	var status, profileMode, mode string
	if err := row.Scan(
		&s.ID, &s.SessionID, &s.SourceID, &s.UpstreamKind, &s.UpstreamSchema, &s.UpstreamObject,
		&s.AnalysisTableName, &s.RowCount, &s.ColumnCount, &status, &s.ErrorMessage,
		&s.SchemaSignature, &s.ImportedAt, &s.RowsImported, &s.RowsSkipped, &s.ImportRowLimit,
		&s.ImportTruncated, &s.ImportDurationMs, &s.ProfileDurationMs, &s.SnapshotSizeBytes, &profileMode, &s.Mode,
	); err != nil {
		return nil, err
	}
	s.Status = domain.SnapshotStatus(status)
	s.ProfileMode = domain.ProfileMode(profileMode)
	s.Mode = domain.SnapshotMode(mode)
	return &s, nil
}

func (r *SourceSnapshotRepository) Create(ctx context.Context, snapshot *domain.SourceSnapshot) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO source_snapshots (`+snapshotCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`,
		snapshot.ID, snapshot.SessionID, snapshot.SourceID, snapshot.UpstreamKind, snapshot.UpstreamSchema, snapshot.UpstreamObject,
		snapshot.AnalysisTableName, snapshot.RowCount, snapshot.ColumnCount, string(snapshot.Status), snapshot.ErrorMessage,
		snapshot.SchemaSignature, snapshot.ImportedAt, snapshot.RowsImported, snapshot.RowsSkipped, snapshot.ImportRowLimit,
		snapshot.ImportTruncated, snapshot.ImportDurationMs, snapshot.ProfileDurationMs, snapshot.SnapshotSizeBytes, string(snapshot.ProfileMode), string(snapshot.Mode),
	)
	if err != nil {
		return fmt.Errorf("failed to create source snapshot: %w", err)
	}
	return nil
}

func (r *SourceSnapshotRepository) GetByID(ctx context.Context, id string) (*domain.SourceSnapshot, error) {
	row := r.db.QueryRow(ctx, `SELECT `+snapshotCols+` FROM source_snapshots WHERE id = $1`, id)
	s, err := scanSnapshot(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get source snapshot by id: %w", normalizeLookupError(err))
	}
	return s, nil
}

func (r *SourceSnapshotRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.SourceSnapshot, error) {
	rows, err := r.db.Query(ctx, `SELECT `+snapshotCols+` FROM source_snapshots WHERE session_id = $1 ORDER BY imported_at DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list source snapshots by session: %w", err)
	}
	defer rows.Close()

	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SourceSnapshot, error) {
		s, err := scanSnapshot(row)
		if err != nil {
			return domain.SourceSnapshot{}, err
		}
		return *s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to collect source snapshots by session: %w", err)
	}
	return results, nil
}

func (r *SourceSnapshotRepository) ListBySource(ctx context.Context, sourceID string) ([]domain.SourceSnapshot, error) {
	rows, err := r.db.Query(ctx, `SELECT `+snapshotCols+` FROM source_snapshots WHERE source_id = $1 ORDER BY imported_at DESC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list source snapshots by source: %w", err)
	}
	defer rows.Close()

	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SourceSnapshot, error) {
		s, err := scanSnapshot(row)
		if err != nil {
			return domain.SourceSnapshot{}, err
		}
		return *s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to collect source snapshots by source: %w", err)
	}
	return results, nil
}

func (r *SourceSnapshotRepository) UpdateStatus(ctx context.Context, id string, status domain.SnapshotStatus, errorMsg *string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE source_snapshots SET status = $1, error_message = $2 WHERE id = $3`,
		string(status), errorMsg, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update source snapshot status: %w", err)
	}
	return nil
}

func (r *SourceSnapshotRepository) UpdateRuntimeFacts(ctx context.Context, id string, rowsImported, rowsSkipped, importRowLimit int, importTruncated bool, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) error {
	_, err := r.db.Exec(ctx,
		`UPDATE source_snapshots SET rows_imported = $1, rows_skipped = $2, import_row_limit = $3, import_truncated = $4, import_duration_ms = $5, profile_duration_ms = $6, snapshot_size_bytes = $7, profile_mode = $8 WHERE id = $9`,
		rowsImported, rowsSkipped, importRowLimit, importTruncated, importDurationMs, profileDurationMs, snapshotSizeBytes, string(profileMode), id,
	)
	if err != nil {
		return fmt.Errorf("failed to update source snapshot runtime facts: %w", err)
	}
	return nil
}

func (r *SourceSnapshotRepository) UpdateSnapshotCompletion(ctx context.Context, id string, rowCount, colCount int, schemaSignature string, rowsImported, rowsSkipped, importRowLimit int, importTruncated bool, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) error {
	_, err := r.db.Exec(ctx,
		`UPDATE source_snapshots SET row_count = $1, column_count = $2, schema_signature = $3, rows_imported = $4, rows_skipped = $5, import_row_limit = $6, import_truncated = $7, import_duration_ms = $8, profile_duration_ms = $9, snapshot_size_bytes = $10, profile_mode = $11 WHERE id = $12`,
		rowCount, colCount, schemaSignature, rowsImported, rowsSkipped, importRowLimit, importTruncated, importDurationMs, profileDurationMs, snapshotSizeBytes, string(profileMode), id,
	)
	if err != nil {
		return fmt.Errorf("failed to update source snapshot completion: %w", err)
	}
	return nil
}

func (r *SourceSnapshotRepository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM source_snapshots WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete source snapshot: %w", err)
	}
	return nil
}
