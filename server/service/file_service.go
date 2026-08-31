package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/storage"
)

type UploadFileInput struct {
	UserID      string
	WorkspaceID string
	SessionID   string
	FileName    string
	ContentType string
	Size        int64
	Body        io.Reader
}

type FileService struct {
	Storage       storage.ObjectStorage
	FileRepo      repository.FileRepository
	ReportRepo    repository.ReportRepository
	WorkspaceRepo repository.WorkspaceRepository
	TempDir       string
}

func (s *FileService) Upload(ctx context.Context, in UploadFileInput) (*domain.File, error) {
	ok, err := s.WorkspaceRepo.IsMember(ctx, in.WorkspaceID, in.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("user not authorized to access workspace")
	}

	fileID := "f_" + uuid.New().String()[:8]
	key := SourceFileKey(in.WorkspaceID, fileID, sanitizeFilename(in.FileName))
	obj, err := s.Storage.Put(ctx, storage.PutObjectRequest{
		Key:         key,
		Body:        in.Body,
		Size:        in.Size,
		ContentType: in.ContentType,
		Metadata: map[string]string{
			"workspace_id": in.WorkspaceID,
			"session_id":   in.SessionID,
			"uploaded_by":  in.UserID,
			"file_id":      fileID,
		},
	})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	file := &domain.File{
		ID:              fileID,
		WorkspaceID:     in.WorkspaceID,
		UploadedBy:      in.UserID,
		DisplayName:     in.FileName,
		Purpose:         domain.FilePurposeSource,
		ContentType:     in.ContentType,
		SizeBytes:       obj.Size,
		StorageProvider: obj.Provider,
		Bucket:          obj.Bucket,
		StorageKey:      obj.Key,
		Checksum:        obj.ETag,
		Status:          domain.FileStatusUploaded,
		Visibility:      domain.FileVisibilityPrivate,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.FileRepo.Create(ctx, file); err != nil {
		return nil, errors.Join(err, s.Storage.Delete(ctx, obj.Key))
	}
	if err := s.FileRepo.AttachFilesToSession(ctx, in.SessionID, []string{file.ID}); err != nil {
		return nil, errors.Join(err, s.Storage.Delete(ctx, obj.Key), s.FileRepo.Delete(ctx, file.ID))
	}
	return file, nil
}

func (s *FileService) GetSessionFiles(ctx context.Context, sessionID string) ([]domain.File, error) {
	return s.FileRepo.ListBySession(ctx, sessionID)
}

func (s *FileService) GetFile(ctx context.Context, fileID string) (*domain.File, error) {
	return s.FileRepo.GetByID(ctx, fileID)
}

func (s *FileService) MaterializeToTemp(ctx context.Context, sessionID, workspaceID, fileID string) (string, *domain.File, error) {
	file, err := s.FileRepo.GetByID(ctx, fileID)
	if err != nil {
		return "", nil, err
	}
	if file.WorkspaceID != workspaceID {
		return "", nil, fmt.Errorf("file does not belong to current workspace")
	}

	sessionFiles, err := s.GetSessionFiles(ctx, sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read session file list: %w", err)
	}
	found := false
	for _, sf := range sessionFiles {
		if sf.ID == fileID {
			found = true
			break
		}
	}
	if !found {
		return "", nil, fmt.Errorf("security block: cannot access file not mounted in current session")
	}

	reader, err := s.Storage.Get(ctx, file.StorageKey)
	if err != nil {
		return "", nil, err
	}
	defer reader.Close()

	if err := os.MkdirAll(s.TempDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	displayName := sanitizeFilename(file.DisplayName)
	ext := filepath.Ext(displayName)
	dest, err := os.CreateTemp(s.TempDir, fmt.Sprintf("%s-%s-*%s", file.ID, strings.TrimSuffix(displayName, ext), ext))
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := dest.Name()
	defer dest.Close()

	if _, err := io.Copy(dest, reader); err != nil {
		os.Remove(tempPath)
		return "", nil, fmt.Errorf("failed to write temp file: %w", err)
	}

	return tempPath, file, nil
}

type SaveReportInput struct {
	UserID      string
	WorkspaceID string
	SessionID   string
	RunID       string
	HTML        string
	Snapshot    domain.ReportSnapshot
}

type SaveArtifactInput struct {
	UserID      string
	WorkspaceID string
	SessionID   string
	RunID       string
	FileName    string
	ContentType string
	Body        io.Reader
	Size        int64
}

func (s *FileService) SaveArtifact(ctx context.Context, in SaveArtifactInput) (*domain.File, error) {
	ok, err := s.WorkspaceRepo.IsMember(ctx, in.WorkspaceID, in.UserID)
	if err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("user not authorized to access workspace")
	}
	name := sanitizeFilename(in.FileName)
	fileID := "art_" + uuid.New().String()[:12]
	key := ArtifactKey(in.WorkspaceID, in.RunID, fileID+"-"+name)
	obj, err := s.Storage.Put(ctx, storage.PutObjectRequest{
		Key: key, Body: in.Body, Size: in.Size, ContentType: in.ContentType,
		Metadata: map[string]string{
			"workspace_id": in.WorkspaceID, "session_id": in.SessionID,
			"uploaded_by": in.UserID, "run_id": in.RunID, "file_id": fileID,
		},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	file := &domain.File{
		ID: fileID, WorkspaceID: in.WorkspaceID, UploadedBy: in.UserID,
		DisplayName: name, Purpose: domain.FilePurposeArtifact, ContentType: obj.ContentType,
		SizeBytes: obj.Size, StorageProvider: obj.Provider, Bucket: obj.Bucket,
		StorageKey: obj.Key, Checksum: obj.ETag, Status: domain.FileStatusReady,
		Visibility: domain.FileVisibilityPrivate, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.FileRepo.Create(ctx, file); err != nil {
		return nil, errors.Join(err, s.Storage.Delete(ctx, obj.Key))
	}
	if strings.TrimSpace(in.SessionID) != "" {
		if err := s.FileRepo.AttachFilesToSession(ctx, in.SessionID, []string{file.ID}); err != nil {
			return nil, errors.Join(err, s.Storage.Delete(ctx, obj.Key), s.FileRepo.Delete(ctx, file.ID))
		}
	}
	return file, nil
}

func (s *FileService) DeleteReportFile(ctx context.Context, fileID string, runID string) error {
	var cleanupErrors []error
	if s.ReportRepo != nil {
		report, err := s.ReportRepo.GetByRunID(ctx, runID)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("load report cleanup target: %w", err))
		} else if report == nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("report repository returned an empty cleanup target"))
		} else {
			if err := s.Storage.Delete(ctx, report.HTMLStorageKey); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete report object: %w", err))
			}
			if err := s.ReportRepo.Delete(ctx, report.ID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete report metadata: %w", err))
			}
		}
	}
	file, err := s.FileRepo.GetByID(ctx, fileID)
	if err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("load report file cleanup target: %w", err))
	} else if file == nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("file repository returned an empty report cleanup target"))
	} else if err := s.FileRepo.Delete(ctx, file.ID); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete report file metadata: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func (s *FileService) SaveReportHTML(ctx context.Context, in SaveReportInput) (*domain.File, error) {
	ok, err := s.WorkspaceRepo.IsMember(ctx, in.WorkspaceID, in.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("user not authorized to access workspace")
	}

	var snapshotJSON []byte
	if s.ReportRepo != nil {
		snapshotJSON, err = json.Marshal(in.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize report snapshot: %w", err)
		}
	}

	fileID := "rep_" + in.RunID
	displayName := "report-" + in.RunID + ".html"

	body := []byte(in.HTML)
	key := ReportHTMLKey(in.WorkspaceID, in.RunID)
	obj, err := s.Storage.Put(ctx, storage.PutObjectRequest{
		Key:         key,
		Body:        bytes.NewReader(body),
		Size:        int64(len(body)),
		ContentType: "text/html; charset=utf-8",
		Metadata: map[string]string{
			"workspace_id": in.WorkspaceID,
			"session_id":   in.SessionID,
			"uploaded_by":  in.UserID,
			"run_id":       in.RunID,
			"file_id":      fileID,
		},
	})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	file := &domain.File{
		ID:              fileID,
		WorkspaceID:     in.WorkspaceID,
		UploadedBy:      in.UserID,
		DisplayName:     displayName,
		Purpose:         domain.FilePurposeReport,
		ContentType:     obj.ContentType,
		SizeBytes:       obj.Size,
		StorageProvider: obj.Provider,
		Bucket:          obj.Bucket,
		StorageKey:      obj.Key,
		Checksum:        obj.ETag,
		Status:          domain.FileStatusReady,
		Visibility:      domain.FileVisibilityPrivate,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.FileRepo.Create(ctx, file); err != nil {
		return nil, errors.Join(err, s.Storage.Delete(ctx, obj.Key))
	}

	if s.ReportRepo != nil {
		report := &domain.Report{
			ID:                  "report_" + in.RunID,
			RunID:               in.RunID,
			WorkspaceID:         in.WorkspaceID,
			Title:               in.Snapshot.Title,
			Author:              in.Snapshot.Author,
			HTMLStorageProvider: obj.Provider,
			HTMLBucket:          obj.Bucket,
			HTMLStorageKey:      obj.Key,
			SnapshotJSON:        string(snapshotJSON),
			CreatedAt:           now,
		}
		if err := s.ReportRepo.Create(ctx, report); err != nil {
			return nil, errors.Join(fmt.Errorf("failed to save report metadata: %w", err), s.Storage.Delete(ctx, obj.Key), s.FileRepo.Delete(ctx, file.ID))
		}
	}
	return file, nil
}

func (s *FileService) OpenForDownload(ctx context.Context, userID, workspaceID, fileID string) (io.ReadCloser, *domain.File, error) {
	ok, err := s.WorkspaceRepo.IsMember(ctx, workspaceID, userID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("user not authorized to access workspace")
	}

	file, err := s.FileRepo.GetByID(ctx, fileID)
	if err != nil {
		return nil, nil, err
	}
	if file.WorkspaceID != workspaceID {
		return nil, nil, fmt.Errorf("file does not belong to current workspace")
	}

	reader, err := s.Storage.Get(ctx, file.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return reader, file, nil
}

func (s *FileService) OpenStoredObject(ctx context.Context, userID, workspaceID, storageKey string) (io.ReadCloser, error) {
	ok, err := s.WorkspaceRepo.IsMember(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("user not authorized to access workspace")
	}
	return s.Storage.Get(ctx, storageKey)
}

// unsafeFilenameChars covers path separators and shell/URI-significant
// characters; Unicode letters (e.g. Chinese filenames) are kept as-is.
var unsafeFilenameChars = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|]`)

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == ".." {
		return "upload.bin"
	}
	if unsafeFilenameChars.MatchString(name) || len(name) > 128 {
		return "upload.bin"
	}
	return name
}
