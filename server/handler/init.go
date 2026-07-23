package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/queue"
	pgrepo "github.com/ifnodoraemon/openDataAnalysis/repository/postgres"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/storage"
	localstorage "github.com/ifnodoraemon/openDataAnalysis/storage/local"
	s3storage "github.com/ifnodoraemon/openDataAnalysis/storage/s3"
	"github.com/riverqueue/river"
)

var (
	defaultIdentity            auth.Identity
	fileService                *service.FileService
	sourceService              *service.SourceService
	sourceConnectors           *service.SourceConnectorRegistry
	metadataStore              *metadata.Store
	tokenManager               *auth.TokenManager
	userRepo                   repository.UserRepository
	workspaceRepo              repository.WorkspaceRepository
	runRepo                    repository.RunRepository
	sessionRepo                repository.SessionRepository
	reportRepo                 repository.ReportRepository
	messageRepo                repository.MessageRepository
	dataSourceRepo             repository.DataSourceRepository
	sourceConfigRepo           repository.SourceConfigRepository
	snapshotRepo               repository.SourceSnapshotRepository
	sessionSourceBindingRepo   repository.SessionSourceBindingRepository
	semanticProfileRepo        repository.SemanticProfileRepository
	semanticConfirmationRepo   repository.SemanticConfirmationRepository
	semanticAssetRepo          repository.SemanticAssetRepository
	auditEventRepo             repository.AuditEventRepository
	ShutdownEventPersistWorker func()
)


func Initialize() {
	ensureProductionReadiness()
	ensureSupportedBackends()
	ensureRequiredConfig()
	tokenManager = auth.NewTokenManager(config.Cfg.AuthSecret)
	defaultIdentity = auth.Identity{
		UserID:      config.Cfg.DefaultUserID,
		UserName:    config.Cfg.DefaultUserName,
		UserEmail:   config.Cfg.DefaultUserEmail,
		WorkspaceID: config.Cfg.DefaultWorkspaceID,
		Workspace:   config.Cfg.DefaultWorkspaceName,
	}

	var fileRepo repository.FileRepository

	switch config.NormalizeBackend(config.Cfg.MetadataStore) {
	case "postgres":
		fileRepo = initPostgresBackend()
	default:
		fileRepo = initSQLiteBackend()
	}

	now := time.Now()
	defaultPasswordHash, err := auth.HashPassword(config.Cfg.DefaultUserPassword)
	if err != nil {
		panic(err)
	}
	_ = userRepo.Create(context.Background(), &domain.User{
		ID:           defaultIdentity.UserID,
		Email:        defaultIdentity.UserEmail,
		PasswordHash: defaultPasswordHash,
		Name:         defaultIdentity.UserName,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	_ = workspaceRepo.CreateWorkspace(context.Background(), &domain.Workspace{
		ID:          defaultIdentity.WorkspaceID,
		Name:        defaultIdentity.Workspace,
		Slug:        defaultIdentity.WorkspaceID,
		OwnerUserID: defaultIdentity.UserID,
		Status:      domain.WorkspaceStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	_ = workspaceRepo.AddMember(context.Background(), &domain.WorkspaceMember{
		WorkspaceID: defaultIdentity.WorkspaceID,
		UserID:      defaultIdentity.UserID,
		Role:        domain.WorkspaceRoleOwner,
		CreatedAt:   now,
	})

	fileService = &service.FileService{
		Storage:       configuredObjectStorage(),
		FileRepo:      fileRepo,
		ReportRepo:    reportRepo,
		WorkspaceRepo: workspaceRepo,
		TempDir:       config.Cfg.TempDir,
	}

	sourceService = service.NewSourceService(
		dataSourceRepo,
		sourceConfigRepo,
		snapshotRepo,
		sessionSourceBindingRepo,
		semanticProfileRepo,
		semanticConfirmationRepo,
		semanticAssetRepo,
		auditEventRepo,
	)
	sourceConnectors = service.NewSourceConnectorRegistry()
	sourceConnectors.Register(service.NewPostgresConnector(sourceService))
	sourceConnectors.Register(service.NewMySQLConnector(sourceService))
	sourceConnectors.Register(service.NewFileUploadConnector(sourceService, fileService))

	sessionManager = session.NewManager(config.Cfg.CacheRoot, fileService, sourceService)
	sessionManager.SetSessionRepository(sessionRepo)

	// 注册全链路删除回调，供 TTL 清理器使用
	sessionManager.SetFullDeleteFunc(func(ctx context.Context, sessionID string) error {
		sess, err := sessionRepo.GetByID(ctx, sessionID)
		if err != nil {
			return err
		}
		return deleteSessionResources(ctx, *sess)
	})

	sessionManager.StartPeriodicCleanup(
		config.Cfg.SessionTTLHours,
		config.Cfg.TraceRetentionDays,
		config.Cfg.LLMDebugDir,
		config.Cfg.TempDir,
		config.Cfg.TempCleanupOnStart,
	)

	ShutdownEventPersistWorker = startEventPersistWorker()
}

func initSQLiteBackend() repository.FileRepository {
	store, err := metadata.Open(config.Cfg.MetadataDBPath)
	if err != nil {
		panic(err)
	}
	metadataStore = store

	userRepo = sqliterepo.NewUserRepository(store.DB)
	workspaceRepo = sqliterepo.NewWorkspaceRepository(store.DB)
	fileRepo := sqliterepo.NewFileRepository(store.DB)
	reportRepo = sqliterepo.NewReportRepository(store.DB)
	sessionRepo = sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)
	dataSourceRepo = sqliterepo.NewDataSourceRepository(store.DB)
	sourceConfigRepo = sqliterepo.NewSourceConfigRepository(store.DB)
	snapshotRepo = sqliterepo.NewSourceSnapshotRepository(store.DB)
	sessionSourceBindingRepo = sqliterepo.NewSessionSourceBindingRepository(store.DB)
	semanticProfileRepo = sqliterepo.NewSemanticProfileRepository(store.DB)
	semanticConfirmationRepo = sqliterepo.NewSemanticConfirmationRepository(store.DB)
	semanticAssetRepo = sqliterepo.NewSemanticAssetRepository(store.DB)
	auditEventRepo = sqliterepo.NewAuditEventRepository(store.DB)
	return fileRepo
}

func initPostgresBackend() repository.FileRepository {
	ctx := context.Background()
	pgStore, err := metadata.OpenPostgres(ctx, config.Cfg.PostgresDSN)
	if err != nil {
		panic("failed to run postgres migrations: " + err.Error())
	}
	_ = pgStore

	pool, err := pgrepo.NewPool(ctx, config.Cfg.PostgresDSN)
	if err != nil {
		panic("failed to connect to postgres pool: " + err.Error())
	}

	userRepo = pgrepo.NewUserRepository(pool)
	workspaceRepo = pgrepo.NewWorkspaceRepository(pool)
	fileRepo := pgrepo.NewFileRepository(pool)
	reportRepo = pgrepo.NewReportRepository(pool)
	sessionRepo = pgrepo.NewSessionRepository(pool)
	runRepo = pgrepo.NewRunRepository(pool)
	messageRepo = pgrepo.NewMessageRepository(pool)
	dataSourceRepo = pgrepo.NewDataSourceRepository(pool)
	sourceConfigRepo = pgrepo.NewSourceConfigRepository(pool)
	snapshotRepo = pgrepo.NewSourceSnapshotRepository(pool)
	sessionSourceBindingRepo = pgrepo.NewSessionSourceBindingRepository(pool)
	semanticProfileRepo = pgrepo.NewSemanticProfileRepository(pool)
	semanticConfirmationRepo = pgrepo.NewSemanticConfirmationRepository(pool)
	semanticAssetRepo = pgrepo.NewSemanticAssetRepository(pool)
	auditEventRepo = pgrepo.NewAuditEventRepository(pool)

	if config.NormalizeBackend(config.Cfg.RunBackend) == "river" {
		workers := river.NewWorkers()
		river.AddWorker(workers, &queue.AnalysisRunWorker{})
		river.AddWorker(workers, &queue.SessionCleanupWorker{})
		riverClient, err := queue.SetupRiverClient(ctx, pool, workers)
		if err != nil {
			panic("failed to initialize River queue: " + err.Error())
		}
		_ = riverClient
	}

	return fileRepo
}

func ensureProductionReadiness() {
	if err := config.Cfg.ValidateProductionReadiness(); err != nil {
		panic(err)
	}
}

func ensureSupportedBackends() {
	requirements := []struct {
		Env       string
		Value     string
		Supported []string
	}{
		{Env: "METADATA_STORE", Value: config.Cfg.MetadataStore, Supported: []string{"sqlite", "postgres"}},
		{Env: "STORAGE_PROVIDER", Value: config.Cfg.StorageProvider, Supported: []string{"local", "s3"}},
		{Env: "RUN_BACKEND", Value: config.Cfg.RunBackend, Supported: []string{"inprocess", "river"}},
		{Env: "ANALYSIS_STORE", Value: config.Cfg.AnalysisStore, Supported: []string{"session_sqlite"}},
		{Env: "PYTHON_ARTIFACT_STORE", Value: config.Cfg.PythonArtifactStore, Supported: []string{"executor_local"}},
	}
	for _, requirement := range requirements {
		normalized := config.NormalizeBackend(requirement.Value)
		if normalized == "" {
			continue
		}
		isSupported := false
		for _, s := range requirement.Supported {
			if normalized == s {
				isSupported = true
				break
			}
		}
		if !isSupported {
			panic("unsupported backend config: " + requirement.Env + "=" + requirement.Value + " is not implemented in this binary")
		}
	}
}

func configuredObjectStorage() storage.ObjectStorage {
	switch config.NormalizeBackend(config.Cfg.StorageProvider) {
	case "", "local":
		return localstorage.New(config.Cfg.StorageRoot, "")
	case "s3":
		s3Store, err := s3storage.New(context.Background(), s3storage.Config{
			Endpoint:       config.Cfg.S3Endpoint,
			Region:         config.Cfg.S3Region,
			Bucket:         config.Cfg.S3Bucket,
			AccessKey:      config.Cfg.S3AccessKey,
			SecretKey:      config.Cfg.S3SecretKey,
			ForcePathStyle: config.Cfg.S3ForcePathStyle,
		})
		if err != nil {
			panic("failed to initialize S3 storage: " + err.Error())
		}
		return s3Store
	default:
		panic("unsupported storage provider: " + config.Cfg.StorageProvider)
	}
}

func AuthMiddleware(next http.Handler) http.Handler {
	return auth.Middleware(tokenManager)(next)
}

func ensureRequiredConfig() {
	required := map[string]string{
		"AUTH_SECRET":            config.Cfg.AuthSecret,
		"DEFAULT_USER_ID":        config.Cfg.DefaultUserID,
		"DEFAULT_USER_EMAIL":     config.Cfg.DefaultUserEmail,
		"DEFAULT_USER_NAME":      config.Cfg.DefaultUserName,
		"DEFAULT_USER_PASSWORD":  config.Cfg.DefaultUserPassword,
		"DEFAULT_WORKSPACE_ID":   config.Cfg.DefaultWorkspaceID,
		"DEFAULT_WORKSPACE_NAME": config.Cfg.DefaultWorkspaceName,
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			panic("missing required config: " + key)
		}
		if config.IsPlaceholderValue(value) {
			panic("insecure placeholder config: " + key)
		}
	}
}
