package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/storage"
	localstorage "github.com/ifnodoraemon/openDataAnalysis/storage/local"
)

type flakyDeleteStorage struct {
	inner    storage.ObjectStorage
	failKeys map[string]bool
}

func (s *flakyDeleteStorage) Put(ctx context.Context, req storage.PutObjectRequest) (*storage.StoredObject, error) {
	return s.inner.Put(ctx, req)
}

func (s *flakyDeleteStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.inner.Get(ctx, key)
}

func (s *flakyDeleteStorage) Delete(ctx context.Context, key string) error {
	if s.failKeys[key] {
		return context.DeadlineExceeded
	}
	return s.inner.Delete(ctx, key)
}

func (s *flakyDeleteStorage) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *flakyDeleteStorage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.inner.PresignGet(ctx, key, ttl)
}

func TestDeleteSessionResourcesRemovesRuntimeStateAndArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config.Cfg = &config.Config{}
	store, err := metadata.Open(root + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	metadataStore = store
	userRepo = sqliterepo.NewUserRepository(store.DB)
	workspaceRepo = sqliterepo.NewWorkspaceRepository(store.DB)
	fileRepo := sqliterepo.NewFileRepository(store.DB)
	reportRepo = sqliterepo.NewReportRepository(store.DB)
	sessionRepo = sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)

	fileService = &service.FileService{
		Storage:       localstorage.New(root+"/objects", ""),
		FileRepo:      fileRepo,
		ReportRepo:    reportRepo,
		WorkspaceRepo: workspaceRepo,
		TempDir:       root + "/tmp",
	}
	sessionManager = session.NewManager(root+"/cache", fileService, nil)
	sessionManager.SetSessionRepository(sessionRepo)

	now := time.Now()
	if err := userRepo.Create(ctx, &domain.User{
		ID:           "u_1",
		Email:        "admin@example.com",
		PasswordHash: mustHashPassword(t, "admin@123"),
		Name:         "Admin",
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := workspaceRepo.CreateWorkspace(ctx, &domain.Workspace{
		ID:          "w_1",
		Name:        "Workspace",
		Slug:        "workspace",
		OwnerUserID: "u_1",
		Status:      domain.WorkspaceStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := workspaceRepo.AddMember(ctx, &domain.WorkspaceMember{
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Role:        domain.WorkspaceRoleOwner,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if err := sessionRepo.Create(ctx, &domain.Session{
		ID:          "s_1",
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Title:       "Test Session",
		Status:      domain.SessionStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastSeenAt:  now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	liveSession, _, err := sessionManager.GetOrCreate(ctx, "s_1", "w_1", "u_1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	liveSession.ActiveRun = &session.RunState{
		RunID:  "r_1",
		Status: "running",
		Cancel: func() {
			go func() {
				time.Sleep(50 * time.Millisecond)
				liveSession.FinishRun("r_1", "cancelled")
			}()
		},
		StartedAt: now,
	}

	sourceObj := putObject(t, ctx, fileService.Storage, "workspaces/w_1/files/f_source/source/data.csv", "col\n1\n")
	reportObj := putObject(t, ctx, fileService.Storage, "workspaces/w_1/runs/r_1/report/report.html", "<html></html>")

	sourceFile := &domain.File{
		ID:              "f_source",
		WorkspaceID:     "w_1",
		UploadedBy:      "u_1",
		DisplayName:     "data.csv",
		Purpose:         domain.FilePurposeSource,
		ContentType:     "text/csv",
		SizeBytes:       sourceObj.Size,
		StorageProvider: sourceObj.Provider,
		StorageKey:      sourceObj.Key,
		Status:          domain.FileStatusUploaded,
		Visibility:      domain.FileVisibilityPrivate,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	reportFile := &domain.File{
		ID:              "rep_r_1",
		WorkspaceID:     "w_1",
		UploadedBy:      "u_1",
		DisplayName:     "report.html",
		Purpose:         domain.FilePurposeReport,
		ContentType:     "text/html",
		SizeBytes:       reportObj.Size,
		StorageProvider: reportObj.Provider,
		StorageKey:      reportObj.Key,
		Status:          domain.FileStatusReady,
		Visibility:      domain.FileVisibilityPrivate,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := fileRepo.Create(ctx, sourceFile); err != nil {
		t.Fatalf("create source file: %v", err)
	}
	if err := fileRepo.Create(ctx, reportFile); err != nil {
		t.Fatalf("create report file: %v", err)
	}
	if err := fileRepo.AttachFilesToSession(ctx, "s_1", []string{"f_source"}); err != nil {
		t.Fatalf("attach source file: %v", err)
	}

	startedAt := now
	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           "r_1",
		SessionID:    "s_1",
		WorkspaceID:  "w_1",
		UserID:       "u_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "analyze",
		ReportFileID: ptr("rep_r_1"),
		StartedAt:    &startedAt,
		FinishedAt:   &startedAt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := reportRepo.Create(ctx, &domain.Report{
		ID:                  "report_r_1",
		RunID:               "r_1",
		WorkspaceID:         "w_1",
		Title:               "Report",
		Author:              "AI",
		HTMLStorageProvider: reportObj.Provider,
		HTMLStorageKey:      reportObj.Key,
		SnapshotJSON:        `{"version":"v3","title":"Report","author":"AI","blocks":[],"charts":[]}`,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("create report: %v", err)
	}
	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "m_1",
		RunID:       "r_1",
		SessionID:   "s_1",
		WorkspaceID: "w_1",
		Type:        "assistant_status",
		Content:     "working",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	record, err := sessionRepo.GetByID(ctx, "s_1")
	if err != nil {
		t.Fatalf("get session before delete: %v", err)
	}
	if err := deleteSessionResources(ctx, *record); err != nil {
		t.Fatalf("delete session resources: %v", err)
	}

	if _, err := sessionRepo.GetByID(ctx, "s_1"); err == nil {
		t.Fatal("expected session to be deleted")
	}
	if _, err := runRepo.GetByID(ctx, "r_1"); err == nil {
		t.Fatal("expected run to be deleted")
	}
	if _, err := fileRepo.GetByID(ctx, "f_source"); err == nil {
		t.Fatal("expected source file to be deleted")
	}
	if _, err := fileRepo.GetByID(ctx, "rep_r_1"); err == nil {
		t.Fatal("expected report file to be deleted")
	}
	messages, err := messageRepo.ListByRun(ctx, "r_1")
	if err != nil {
		t.Fatalf("list messages after delete: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected run messages to be deleted, got %d", len(messages))
	}
	if exists, err := fileService.Storage.Exists(ctx, sourceObj.Key); err != nil || exists {
		t.Fatalf("expected source object deleted, exists=%t err=%v", exists, err)
	}
	if exists, err := fileService.Storage.Exists(ctx, reportObj.Key); err != nil || exists {
		t.Fatalf("expected report object deleted, exists=%t err=%v", exists, err)
	}
	if _, ok, err := sessionManager.Peek("s_1", "w_1", "u_1"); err != nil || ok {
		t.Fatalf("expected session manager to drop session, ok=%t err=%v", ok, err)
	}
}

func TestDeleteSessionDoesNotDeleteSharedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, _ := metadata.Open(root + "/metadata.db")
	t.Cleanup(func() { _ = store.DB.Close() })

	fRepo := sqliterepo.NewFileRepository(store.DB)
	sRepo := sqliterepo.NewSessionRepository(store.DB)
	fs := &service.FileService{
		Storage:  localstorage.New(root+"/objects", ""),
		FileRepo: fRepo,
	}

	_ = sRepo.Create(ctx, &domain.Session{
		ID: "s_shared", WorkspaceID: "w_1", UserID: "u_1", Status: domain.SessionStatusActive,
	})

	sharedObj := putObject(t, ctx, fs.Storage, "workspaces/w_1/files/f_shared/data.csv", "1,2,3")
	sharedFile := &domain.File{
		ID:          "f_shared",
		WorkspaceID: "w_1",
		Visibility:  domain.FileVisibilityWorkspace, // Shared file
		Status:      domain.FileStatusReady,
		StorageKey:  sharedObj.Key,
	}
	_ = fRepo.Create(ctx, sharedFile)
	_ = fRepo.AttachFilesToSession(ctx, "s_shared", []string{"f_shared"})

	// Simulate deletion
	record, _ := sRepo.GetByID(ctx, "s_shared")

	// Temporarily patch globals for the scope of the test if needed,
	// but deleteSessionResources uses globals. We must set them up.
	prevFs := fileService
	prevMs := metadataStore
	fileService = fs
	metadataStore = store
	t.Cleanup(func() {
		fileService = prevFs
		metadataStore = prevMs
	})

	err := deleteSessionResources(ctx, *record)
	if err != nil {
		t.Fatalf("delete session resources: %v", err)
	}

	if _, err := sRepo.GetByID(ctx, "s_shared"); err == nil {
		t.Fatal("expected session to be deleted")
	}
	if _, err := fRepo.GetByID(ctx, "f_shared"); err != nil {
		t.Fatalf("expected shared file to remain in DB, got err: %v", err)
	}
	if exists, _ := fs.Storage.Exists(ctx, sharedObj.Key); !exists {
		t.Fatal("expected shared object file to remain in object storage")
	}
}

func TestDeleteSessionResourcesKeepsMetadataUntilStorageCleanupSucceeds(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, err := metadata.Open(root + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	prevMetadataStore := metadataStore
	prevFileService := fileService
	prevSessionRepo := sessionRepo
	prevRunRepo := runRepo
	prevMessageRepo := messageRepo
	prevSessionManager := sessionManager
	t.Cleanup(func() {
		metadataStore = prevMetadataStore
		fileService = prevFileService
		sessionRepo = prevSessionRepo
		runRepo = prevRunRepo
		messageRepo = prevMessageRepo
		sessionManager = prevSessionManager
	})

	metadataStore = store
	fileRepo := sqliterepo.NewFileRepository(store.DB)
	sessionRepo = sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)

	baseStorage := localstorage.New(root+"/objects", "")
	objectKey := "workspaces/w_1/runs/r_1/report/report.html"
	if _, err := baseStorage.Put(ctx, storage.PutObjectRequest{
		Key:         objectKey,
		Body:        bytes.NewBufferString("<html></html>"),
		Size:        int64(len("<html></html>")),
		ContentType: "text/html",
	}); err != nil {
		t.Fatalf("put object: %v", err)
	}

	flakyStorage := &flakyDeleteStorage{
		inner:    baseStorage,
		failKeys: map[string]bool{objectKey: true},
	}
	fileService = &service.FileService{
		Storage:  flakyStorage,
		FileRepo: fileRepo,
		TempDir:  root + "/tmp",
	}
	sessionManager = session.NewManager(root+"/cache", fileService, nil)
	sessionManager.SetSessionRepository(sessionRepo)

	now := time.Now()
	if err := sessionRepo.Create(ctx, &domain.Session{
		ID:          "s_1",
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Title:       "Delete Me",
		Status:      domain.SessionStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastSeenAt:  now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := fileRepo.Create(ctx, &domain.File{
		ID:              "rep_r_1",
		WorkspaceID:     "w_1",
		UploadedBy:      "u_1",
		DisplayName:     "report.html",
		Purpose:         domain.FilePurposeReport,
		ContentType:     "text/html",
		SizeBytes:       int64(len("<html></html>")),
		StorageProvider: "local",
		StorageKey:      objectKey,
		Status:          domain.FileStatusReady,
		Visibility:      domain.FileVisibilityPrivate,
		CreatedAt:       now,
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("create file: %v", err)
	}
	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           "r_1",
		SessionID:    "s_1",
		WorkspaceID:  "w_1",
		UserID:       "u_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "analyze",
		ReportFileID: ptr("rep_r_1"),
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	record, err := sessionRepo.GetByID(ctx, "s_1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if err := deleteSessionResources(ctx, *record); err == nil {
		t.Fatal("expected storage deletion failure")
	}

	if _, err := sessionRepo.GetByID(ctx, "s_1"); err != nil {
		t.Fatalf("expected session metadata to remain retryable: %v", err)
	}
	if _, err := runRepo.GetByID(ctx, "r_1"); err != nil {
		t.Fatalf("expected run metadata to remain retryable: %v", err)
	}
	if exists, err := baseStorage.Exists(ctx, objectKey); err != nil || !exists {
		t.Fatalf("expected failed object deletion to remain visible, exists=%t err=%v", exists, err)
	}

	delete(flakyStorage.failKeys, objectKey)
	if err := deleteSessionResources(ctx, *record); err != nil {
		t.Fatalf("retry session deletion: %v", err)
	}
	if _, err := sessionRepo.GetByID(ctx, "s_1"); err == nil {
		t.Fatal("expected session metadata to be deleted after successful retry")
	}
	if _, err := runRepo.GetByID(ctx, "r_1"); err == nil {
		t.Fatal("expected run metadata to be deleted after successful retry")
	}
	if exists, err := baseStorage.Exists(ctx, objectKey); err != nil || exists {
		t.Fatalf("expected object to be deleted after successful retry, exists=%t err=%v", exists, err)
	}
}

func TestRebindSessionDeletionQueryConvertsPlaceholdersForPostgres(t *testing.T) {
	t.Parallel()

	prevMetadataStore := metadataStore
	t.Cleanup(func() {
		metadataStore = prevMetadataStore
	})

	metadataStore = nil
	query := `DELETE FROM sessions WHERE id = ? AND workspace_id = ?`
	if got := rebindSessionDeletionQuery(query); got != query {
		t.Fatalf("expected unchanged query without metadata store, got %q", got)
	}

	metadataStore = &metadata.Store{Dialect: metadata.DialectSQLite}
	if got := rebindSessionDeletionQuery(query); got != query {
		t.Fatalf("expected unchanged query for sqlite, got %q", got)
	}

	metadataStore = &metadata.Store{Dialect: metadata.DialectPostgres}
	if got := rebindSessionDeletionQuery(query); got != `DELETE FROM sessions WHERE id = $1 AND workspace_id = $2` {
		t.Fatalf("expected postgres placeholders, got %q", got)
	}
	if got := rebindSessionDeletionQuery(`DELETE FROM files WHERE id IN (?,?,?)`); got != `DELETE FROM files WHERE id IN ($1,$2,$3)` {
		t.Fatalf("expected postgres placeholders for IN list, got %q", got)
	}
}

func putObject(t *testing.T, ctx context.Context, store storage.ObjectStorage, key, body string) *storage.StoredObject {
	t.Helper()
	obj, err := store.Put(ctx, storage.PutObjectRequest{
		Key:         key,
		Body:        bytes.NewBufferString(body),
		Size:        int64(len(body)),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("put object %s: %v", key, err)
	}
	return obj
}

func mustHashPassword(t *testing.T, password string) string {
	t.Helper()
	encoded, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return encoded
}

func ptr[T any](value T) *T {
	return &value
}

func assertNotFound(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected not found error")
	}
	if errors.Is(err, repository.ErrNotFound) {
		return
	}
	t.Fatalf("expected repository.ErrNotFound, got %v", err)
}
