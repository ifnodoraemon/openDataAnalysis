package repository

import (
	"context"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type DataSourceRepository interface {
	Create(ctx context.Context, ds *domain.DataSource) error
	GetByID(ctx context.Context, id string) (*domain.DataSource, error)
	GetByFileID(ctx context.Context, fileID string) (*domain.DataSource, error)
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.DataSource, error)
	Update(ctx context.Context, ds *domain.DataSource) error
	UpdateStatus(ctx context.Context, id string, status domain.SourceStatus) error
	Delete(ctx context.Context, id string) error
}

type SourceConfigRepository interface {
	Create(ctx context.Context, cfg *domain.SourceConfig) error
	GetBySourceID(ctx context.Context, sourceID string) (*domain.SourceConfig, error)
	Update(ctx context.Context, cfg *domain.SourceConfig) error
	UpdateTestResult(ctx context.Context, sourceID string, testedAt *time.Time, status string, errMsg *string) error
}

type SourceSnapshotRepository interface {
	Create(ctx context.Context, snapshot *domain.SourceSnapshot) error
	GetByID(ctx context.Context, id string) (*domain.SourceSnapshot, error)
	ListBySession(ctx context.Context, sessionID string) ([]domain.SourceSnapshot, error)
	ListBySource(ctx context.Context, sourceID string) ([]domain.SourceSnapshot, error)
	UpdateStatus(ctx context.Context, id string, status domain.SnapshotStatus, errorMsg *string) error
	UpdateRuntimeFacts(ctx context.Context, id string, rowsImported, rowsSkipped, importRowLimit int, importTruncated bool, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) error
	UpdateSnapshotCompletion(ctx context.Context, id string, rowCount, colCount int, schemaSignature string, rowsImported, rowsSkipped, importRowLimit int, importTruncated bool, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) error
	Delete(ctx context.Context, id string) error
}

type SessionSourceBindingRepository interface {
	Upsert(ctx context.Context, binding *domain.SessionSourceBinding) error
	GetBySession(ctx context.Context, sessionID string) ([]domain.SessionSourceBinding, error)
	GetBySessionSourceObject(ctx context.Context, sessionID, sourceID, sourceObjectKey string) (*domain.SessionSourceBinding, error)
	Delete(ctx context.Context, sessionID, sourceID, sourceObjectKey string) error
}

type SemanticProfileRepository interface {
	Create(ctx context.Context, profile *domain.SemanticProfile) error
	GetByID(ctx context.Context, id string) (*domain.SemanticProfile, error)
	ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticProfile, error)
	ListBySource(ctx context.Context, sourceID string) ([]domain.SemanticProfile, error)
	UpdateStatus(ctx context.Context, id string, status domain.ProfileStatus) error
	UpdateProfileJSON(ctx context.Context, id string, profileJSON string) error
	FindWorkspaceConfirmation(ctx context.Context, workspaceID, schemaSignature string) (*domain.SemanticConfirmation, error)
	Delete(ctx context.Context, id string) error
}

type SemanticConfirmationRepository interface {
	Create(ctx context.Context, confirmation *domain.SemanticConfirmation) error
	ListByProfile(ctx context.Context, profileID string) ([]domain.SemanticConfirmation, error)
	ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticConfirmation, error)
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticConfirmation, error)
	DeleteByProfile(ctx context.Context, profileID string) error
}

type SemanticAssetRepository interface {
	Upsert(ctx context.Context, asset *domain.SemanticAsset) error
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticAsset, error)
	ListBySchema(ctx context.Context, workspaceID, schemaSignature string) ([]domain.SemanticAsset, error)
}

type AuditEventRepository interface {
	Create(ctx context.Context, event *domain.AuditEvent) error
	ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.AuditEvent, error)
}
