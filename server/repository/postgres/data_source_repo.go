package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type DataSourceRepository struct {
	db DBTX
}

func NewDataSourceRepository(db DBTX) *DataSourceRepository {
	return &DataSourceRepository{db: db}
}

func scanDataSource(row pgx.Row) (*domain.DataSource, error) {
	var ds domain.DataSource
	var sourceType, status string
	err := row.Scan(&ds.ID, &ds.WorkspaceID, &ds.Name, &sourceType, &status, &ds.FileID, &ds.CreatedBy, &ds.CreatedAt, &ds.UpdatedAt)
	if err != nil {
		return nil, err
	}
	ds.SourceType = domain.SourceType(sourceType)
	ds.Status = domain.SourceStatus(status)
	return &ds, nil
}

func (r *DataSourceRepository) Create(ctx context.Context, ds *domain.DataSource) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO data_sources (id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ds.ID, ds.WorkspaceID, ds.Name, string(ds.SourceType), string(ds.Status), ds.FileID, ds.CreatedBy, ds.CreatedAt, ds.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create data source: %w", err)
	}
	return nil
}

func (r *DataSourceRepository) GetByID(ctx context.Context, id string) (*domain.DataSource, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at FROM data_sources WHERE id = $1`, id,
	)
	ds, err := scanDataSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get data source by id: %w", err)
	}
	return ds, nil
}

func (r *DataSourceRepository) GetByFileID(ctx context.Context, fileID string) (*domain.DataSource, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at FROM data_sources WHERE file_id = $1`, fileID,
	)
	ds, err := scanDataSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get data source by file id: %w", err)
	}
	return ds, nil
}

func (r *DataSourceRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.DataSource, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at FROM data_sources WHERE workspace_id = $1 ORDER BY created_at ASC`, workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list data sources by workspace: %w", err)
	}
	defer rows.Close()

	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.DataSource, error) {
		ds, err := scanDataSource(row)
		if err != nil {
			return domain.DataSource{}, err
		}
		return *ds, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to collect data sources by workspace: %w", err)
	}
	return results, nil
}

func (r *DataSourceRepository) Update(ctx context.Context, ds *domain.DataSource) error {
	ds.UpdatedAt = time.Now()
	_, err := r.db.Exec(ctx,
		`UPDATE data_sources SET name = $1, status = $2, updated_at = $3 WHERE id = $4`,
		ds.Name, string(ds.Status), ds.UpdatedAt, ds.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update data source: %w", err)
	}
	return nil
}

func (r *DataSourceRepository) UpdateStatus(ctx context.Context, id string, status domain.SourceStatus) error {
	_, err := r.db.Exec(ctx, `UPDATE data_sources SET status = $1, updated_at = $2 WHERE id = $3`, string(status), time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update data source status: %w", err)
	}
	return nil
}

func (r *DataSourceRepository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM data_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete data source: %w", err)
	}
	return nil
}
