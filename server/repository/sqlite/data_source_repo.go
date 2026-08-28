package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type DataSourceRepository struct{ db *sql.DB }

func NewDataSourceRepository(db *sql.DB) *DataSourceRepository { return &DataSourceRepository{db: db} }

func (r *DataSourceRepository) Create(ctx context.Context, ds *domain.DataSource) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO data_sources (id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ds.ID, ds.WorkspaceID, ds.Name, string(ds.SourceType), string(ds.Status), ds.FileID, ds.CreatedBy, ds.CreatedAt, ds.UpdatedAt)
	return err
}

func (r *DataSourceRepository) GetByID(ctx context.Context, id string) (*domain.DataSource, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at FROM data_sources WHERE id = ?`, id)
	var ds domain.DataSource
	var sourceType, status string
	var fileID sql.NullString
	if err := row.Scan(&ds.ID, &ds.WorkspaceID, &ds.Name, &sourceType, &status, &fileID, &ds.CreatedBy, &ds.CreatedAt, &ds.UpdatedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	ds.SourceType = domain.SourceType(sourceType)
	ds.Status = domain.SourceStatus(status)
	if fileID.Valid {
		ds.FileID = &fileID.String
	}
	return &ds, nil
}

func (r *DataSourceRepository) GetByFileID(ctx context.Context, fileID string) (*domain.DataSource, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at FROM data_sources WHERE file_id = ?`, fileID)
	var ds domain.DataSource
	var sourceType, status string
	var fileIDVal sql.NullString
	if err := row.Scan(&ds.ID, &ds.WorkspaceID, &ds.Name, &sourceType, &status, &fileIDVal, &ds.CreatedBy, &ds.CreatedAt, &ds.UpdatedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	ds.SourceType = domain.SourceType(sourceType)
	ds.Status = domain.SourceStatus(status)
	if fileIDVal.Valid {
		ds.FileID = &fileIDVal.String
	}
	return &ds, nil
}

func (r *DataSourceRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.DataSource, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at FROM data_sources WHERE workspace_id = ? ORDER BY created_at ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.DataSource
	for rows.Next() {
		var ds domain.DataSource
		var sourceType, status string
		var fileID sql.NullString
		if err := rows.Scan(&ds.ID, &ds.WorkspaceID, &ds.Name, &sourceType, &status, &fileID, &ds.CreatedBy, &ds.CreatedAt, &ds.UpdatedAt); err != nil {
			return nil, err
		}
		ds.SourceType = domain.SourceType(sourceType)
		ds.Status = domain.SourceStatus(status)
		if fileID.Valid {
			ds.FileID = &fileID.String
		}
		results = append(results, ds)
	}
	return results, rows.Err()
}

func (r *DataSourceRepository) Update(ctx context.Context, ds *domain.DataSource) error {
	ds.UpdatedAt = time.Now()
	_, err := r.db.ExecContext(ctx,
		`UPDATE data_sources SET name = ?, status = ?, updated_at = ? WHERE id = ?`,
		ds.Name, string(ds.Status), ds.UpdatedAt, ds.ID)
	return err
}

func (r *DataSourceRepository) UpdateStatus(ctx context.Context, id string, status domain.SourceStatus) error {
	_, err := r.db.ExecContext(ctx, `UPDATE data_sources SET status = ?, updated_at = ? WHERE id = ?`, string(status), time.Now(), id)
	return err
}

func (r *DataSourceRepository) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM data_sources WHERE id = ?`, id)
	return err
}
