package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type ReportRepository struct{ db *sql.DB }

func NewReportRepository(db *sql.DB) *ReportRepository { return &ReportRepository{db: db} }
func (r *ReportRepository) Create(ctx context.Context, report *domain.Report) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO reports (id, run_id, workspace_id, title, author, html_storage_provider, html_bucket, html_storage_key, snapshot_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		report.ID, report.RunID, report.WorkspaceID, report.Title, report.Author, report.HTMLStorageProvider, report.HTMLBucket, report.HTMLStorageKey, report.SnapshotJSON, report.CreatedAt)
	return err
}

func (r *ReportRepository) GetByRunID(ctx context.Context, runID string) (*domain.Report, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, run_id, workspace_id, title, author, html_storage_provider, html_bucket, html_storage_key, snapshot_json, created_at FROM reports WHERE run_id = ?`, runID)
	var report domain.Report
	if err := row.Scan(&report.ID, &report.RunID, &report.WorkspaceID, &report.Title, &report.Author, &report.HTMLStorageProvider, &report.HTMLBucket, &report.HTMLStorageKey, &report.SnapshotJSON, &report.CreatedAt); err != nil {
		return nil, err
	}
	return &report, nil
}

func (r *ReportRepository) Delete(ctx context.Context, reportID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM reports WHERE id = ?`, reportID)
	return err
}
