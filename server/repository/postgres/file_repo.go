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

type FileRepository struct {
	db DBTX
}

func NewFileRepository(db DBTX) *FileRepository {
	return &FileRepository{db: db}
}

func (r *FileRepository) Create(ctx context.Context, file *domain.File) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO files (id, workspace_id, uploaded_by, display_name, purpose, content_type, size_bytes, storage_provider, bucket, storage_key, checksum, status, visibility, created_at, updated_at, deleted_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		file.ID, file.WorkspaceID, file.UploadedBy, file.DisplayName, string(file.Purpose), file.ContentType, file.SizeBytes, file.StorageProvider, file.Bucket, file.StorageKey, file.Checksum, string(file.Status), string(file.Visibility), file.CreatedAt, file.UpdatedAt, file.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	return nil
}

func (r *FileRepository) GetByID(ctx context.Context, fileID string) (*domain.File, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, workspace_id, uploaded_by, display_name, purpose, content_type, size_bytes, storage_provider, bucket, storage_key, checksum, status, visibility, created_at, updated_at, deleted_at FROM files WHERE id = $1`,
		fileID,
	)
	var file domain.File
	var purpose, status, visibility string
	err := row.Scan(&file.ID, &file.WorkspaceID, &file.UploadedBy, &file.DisplayName, &purpose, &file.ContentType, &file.SizeBytes, &file.StorageProvider, &file.Bucket, &file.StorageKey, &file.Checksum, &status, &visibility, &file.CreatedAt, &file.UpdatedAt, &file.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get file by id: %w", err)
	}
	file.Purpose = domain.FilePurpose(purpose)
	file.Status = domain.FileStatus(status)
	file.Visibility = domain.FileVisibility(visibility)
	return &file, nil
}

func (r *FileRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.File, error) {
	rows, err := r.db.Query(ctx, `
		SELECT f.id, f.workspace_id, f.uploaded_by, f.display_name, f.purpose, f.content_type, f.size_bytes,
		       f.storage_provider, f.bucket, f.storage_key, f.checksum, f.status, f.visibility,
		       f.created_at, f.updated_at, f.deleted_at
		FROM files f
		INNER JOIN session_files sf ON sf.file_id = f.id
		WHERE sf.session_id = $1
		ORDER BY sf.created_at ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list files by session: %w", err)
	}
	defer rows.Close()

	var files []domain.File
	for rows.Next() {
		var file domain.File
		var purpose, status, visibility string
		if err := rows.Scan(&file.ID, &file.WorkspaceID, &file.UploadedBy, &file.DisplayName, &purpose, &file.ContentType, &file.SizeBytes,
			&file.StorageProvider, &file.Bucket, &file.StorageKey, &file.Checksum, &status, &visibility,
			&file.CreatedAt, &file.UpdatedAt, &file.DeletedAt); err != nil {
			return nil, fmt.Errorf("failed to scan file: %w", err)
		}
		file.Purpose = domain.FilePurpose(purpose)
		file.Status = domain.FileStatus(status)
		file.Visibility = domain.FileVisibility(visibility)
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating files: %w", err)
	}
	return files, nil
}

func (r *FileRepository) AttachFilesToSession(ctx context.Context, sessionID string, fileIDs []string) error {
	now := time.Now()
	for _, fileID := range fileIDs {
		_, err := r.db.Exec(ctx, `INSERT INTO session_files (session_id, file_id, created_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, sessionID, fileID, now)
		if err != nil {
			return fmt.Errorf("failed to attach file %s to session %s: %w", fileID, sessionID, err)
		}
	}
	return nil
}

func (r *FileRepository) Delete(ctx context.Context, fileID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM files WHERE id = $1`, fileID)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}
