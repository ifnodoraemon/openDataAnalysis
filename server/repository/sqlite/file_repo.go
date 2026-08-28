package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type FileRepository struct{ db *sql.DB }

func NewFileRepository(db *sql.DB) *FileRepository { return &FileRepository{db: db} }
func (r *FileRepository) Create(ctx context.Context, file *domain.File) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO files (id, workspace_id, uploaded_by, display_name, purpose, content_type, size_bytes, storage_provider, bucket, storage_key, checksum, status, visibility, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		file.ID, file.WorkspaceID, file.UploadedBy, file.DisplayName, string(file.Purpose), file.ContentType, file.SizeBytes, file.StorageProvider, file.Bucket, file.StorageKey, file.Checksum, string(file.Status), string(file.Visibility), file.CreatedAt, file.UpdatedAt, file.DeletedAt)
	return err
}

func (r *FileRepository) Delete(ctx context.Context, fileID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, fileID)
	return err
}

func (r *FileRepository) GetByID(ctx context.Context, fileID string) (*domain.File, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, workspace_id, uploaded_by, display_name, purpose, content_type, size_bytes, storage_provider, bucket, storage_key, checksum, status, visibility, created_at, updated_at, deleted_at FROM files WHERE id = ?`, fileID)
	var file domain.File
	var status, visibility, purpose string
	var deletedAt sql.NullTime
	if err := row.Scan(&file.ID, &file.WorkspaceID, &file.UploadedBy, &file.DisplayName, &purpose, &file.ContentType, &file.SizeBytes, &file.StorageProvider, &file.Bucket, &file.StorageKey, &file.Checksum, &status, &visibility, &file.CreatedAt, &file.UpdatedAt, &deletedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	file.Purpose = domain.FilePurpose(purpose)
	file.Status = domain.FileStatus(status)
	file.Visibility = domain.FileVisibility(visibility)
	if deletedAt.Valid {
		file.DeletedAt = &deletedAt.Time
	}
	return &file, nil
}

func (r *FileRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.File, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.id, f.workspace_id, f.uploaded_by, f.display_name, f.purpose, f.content_type, f.size_bytes,
		       f.storage_provider, f.bucket, f.storage_key, f.checksum, f.status, f.visibility,
		       f.created_at, f.updated_at, f.deleted_at
		FROM files f
		INNER JOIN session_files sf ON sf.file_id = f.id
		WHERE sf.session_id = ?
		ORDER BY sf.created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []domain.File
	for rows.Next() {
		var file domain.File
		var status, visibility, purpose string
		var deletedAt sql.NullTime
		if err := rows.Scan(&file.ID, &file.WorkspaceID, &file.UploadedBy, &file.DisplayName, &purpose, &file.ContentType, &file.SizeBytes,
			&file.StorageProvider, &file.Bucket, &file.StorageKey, &file.Checksum, &status, &visibility,
			&file.CreatedAt, &file.UpdatedAt, &deletedAt); err != nil {
			return nil, err
		}
		file.Purpose = domain.FilePurpose(purpose)
		file.Status = domain.FileStatus(status)
		file.Visibility = domain.FileVisibility(visibility)
		if deletedAt.Valid {
			file.DeletedAt = &deletedAt.Time
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *FileRepository) AttachFilesToSession(ctx context.Context, sessionID string, fileIDs []string) error {
	now := time.Now()
	for _, fileID := range fileIDs {
		if _, err := r.db.ExecContext(ctx, `INSERT INTO session_files (session_id, file_id, created_at) VALUES (?, ?, ?)`, sessionID, fileID, now); err != nil {
			return err
		}
	}
	return nil
}
