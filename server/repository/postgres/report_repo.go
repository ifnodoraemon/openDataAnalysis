package postgres

import (
	"context"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type ReportRepository struct {
	db DBTX
}

func NewReportRepository(db DBTX) *ReportRepository {
	return &ReportRepository{db: db}
}

func (r *ReportRepository) Create(ctx context.Context, report *domain.Report) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO reports (id, run_id, workspace_id, title, author, html_storage_provider, html_bucket, html_storage_key, snapshot_json, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		report.ID, report.RunID, report.WorkspaceID, report.Title, report.Author,
		report.HTMLStorageProvider, report.HTMLBucket, report.HTMLStorageKey, report.SnapshotJSON, report.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create report: %w", err)
	}
	return nil
}

func (r *ReportRepository) GetByRunID(ctx context.Context, runID string) (*domain.Report, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, run_id, workspace_id, title, author, html_storage_provider, html_bucket, html_storage_key, snapshot_json, created_at
		FROM reports WHERE run_id = $1`,
		runID,
	)
	var report domain.Report
	err := row.Scan(
		&report.ID, &report.RunID, &report.WorkspaceID, &report.Title, &report.Author,
		&report.HTMLStorageProvider, &report.HTMLBucket, &report.HTMLStorageKey, &report.SnapshotJSON, &report.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get report by run id: %w", normalizeLookupError(err))
	}
	return &report, nil
}

func (r *ReportRepository) Delete(ctx context.Context, reportID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM reports WHERE id = $1`, reportID)
	if err != nil {
		return fmt.Errorf("failed to delete report: %w", err)
	}
	return nil
}
