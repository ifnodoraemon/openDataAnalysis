package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SourceConnector interface {
	Type() domain.SourceType
	NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error)
	PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error)
	Test(ctx context.Context, req SourceTestRequest) (map[string]interface{}, error)
	Catalog(ctx context.Context, sourceID string) ([]SourceCatalogObject, error)
	Import(ctx context.Context, req SourceImportRequest) (*SnapshotImportResult, error)
}

type SourceConnectorRegistry struct {
	connectors map[domain.SourceType]SourceConnector
}

func NewSourceConnectorRegistry() *SourceConnectorRegistry {
	return &SourceConnectorRegistry{connectors: map[domain.SourceType]SourceConnector{}}
}

func (r *SourceConnectorRegistry) Register(connector SourceConnector) {
	if connector == nil {
		return
	}
	r.connectors[connector.Type()] = connector
}

func (r *SourceConnectorRegistry) Get(sourceType domain.SourceType) (SourceConnector, error) {
	if r == nil {
		return nil, fmt.Errorf("source connector registry is not initialized")
	}
	connector := r.connectors[sourceType]
	if connector == nil {
		return nil, fmt.Errorf("unsupported data source type: %s", sourceType)
	}
	return connector, nil
}

type SourceTestRequest struct {
	SourceID   string
	AuthSecret string
}

type SourceConfigRequest struct {
	SourceID          string
	RawConfig         json.RawMessage
	RawCredential     json.RawMessage
	Existing          *domain.SourceConfig
	RequireCredential bool
	AuthSecret        string
}

type SourceObjectRef struct {
	Schema string
	Name   string
	Kind   string
}

type SourceCatalogObject struct {
	Schema          string `json:"schema,omitempty"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	SourceObjectKey string `json:"source_object_key,omitempty"`
}

type SourceImportRequest struct {
	SourceID       string
	WorkspaceID    string
	SessionID      string
	Object         SourceObjectRef
	Ingester       *data.Ingester
	AuthSecret     string
	ImportRowLimit int
}

type SnapshotImportCompletion struct {
	SnapshotID        string
	SessionID         string
	WorkspaceID       string
	SourceID          string
	UpstreamKind      string
	UpstreamSchema    string
	UpstreamObject    string
	AnalysisTableName string
	RowCount          int
	ColCount          int
	RowsImported      int
	RowsSkipped       int
	ImportRowLimit    int
	ImportTruncated   bool
	ImportDuration    time.Duration
	SnapshotSizeBytes int64
	AnalyzeSemantics  bool
	ExtraWarnings     []string
	Ingester          *data.Ingester
}

type SnapshotImportResult struct {
	SnapshotID        string
	ProfileID         string
	TableName         string
	RowCount          int
	ColCount          int
	RowsImported      int
	RowsSkipped       int
	ImportRowLimit    int
	ImportTruncated   bool
	ImportDurationMs  int
	ProfileDurationMs int
	SnapshotSizeBytes int64
	ProfileMode       domain.ProfileMode
	DataSizeTier      string
	ProfErr           error
}

func (s *SourceService) BeginSnapshotImport(ctx context.Context, sessionID, sourceID, upstreamKind, upstreamSchema, upstreamObject, analysisTableName string) (*domain.SourceSnapshot, error) {
	sourceObjectKey := SourceObjectKey(sourceID, upstreamKind, upstreamSchema, upstreamObject)
	snapshot := &domain.SourceSnapshot{
		ID:                "snap_" + uuid.New().String()[:12],
		SessionID:         sessionID,
		SourceID:          sourceID,
		UpstreamKind:      upstreamKind,
		UpstreamSchema:    upstreamSchema,
		UpstreamObject:    upstreamObject,
		AnalysisTableName: analysisTableName,
		Status:            domain.SnapshotStatusCreating,
		ImportedAt:        time.Now(),
		ProfileMode:       domain.ProfileModeSampled,
	}
	if err := s.SnapshotRepo.Create(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("failed to create source snapshot: %w", err)
	}
	binding := &domain.SessionSourceBinding{
		SessionID:        sessionID,
		SourceID:         sourceID,
		SourceObjectKey:  sourceObjectKey,
		ActiveSnapshotID: snapshot.ID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := s.SessionSourceBindingRepo.Upsert(ctx, binding); err != nil {
		errMsg := "failed to create session source binding"
		_ = s.SnapshotRepo.UpdateStatus(ctx, snapshot.ID, domain.SnapshotStatusFailed, &errMsg)
		return nil, fmt.Errorf("failed to create session source binding: %w", err)
	}
	return snapshot, nil
}

func (s *SourceService) FinalizeSnapshotImport(ctx context.Context, req SnapshotImportCompletion) (*SnapshotImportResult, error) {
	if req.Ingester == nil || req.Ingester.GetDB() == nil {
		return nil, fmt.Errorf("analysis database is not initialized")
	}
	if strings.TrimSpace(req.AnalysisTableName) == "" {
		return nil, fmt.Errorf("analysis table name is required")
	}

	profileMode := ProfileModeForRows(req.RowCount)

	var schema *data.SchemaInfo
	var schemaErr error
	if profileMode == domain.ProfileModeExact {
		schema, schemaErr = data.ExtractSchema(req.Ingester.GetDB(), req.AnalysisTableName)
	} else {
		schema, schemaErr = data.ExtractSchemaSampled(req.Ingester.GetDB(), req.AnalysisTableName)
	}
	if schemaErr != nil {
		s.failSnapshotIfPresent(ctx, req.SnapshotID, schemaErr)
		return nil, fmt.Errorf("schema extraction failed: %w", schemaErr)
	}
	schemaSig := ComputeSchemaSignature(schema)

	profileStart := time.Now()
	var semanticProfile *data.SemanticProfile
	if req.AnalyzeSemantics && req.Ingester.SemanticEnricher != nil {
		activeTables := req.Ingester.GetActiveTables()
		semCtx, semCancel := context.WithTimeout(ctx, 30*time.Second)
		sp, semErr := data.AnalyzeTableSemantics(semCtx, req.Ingester.SemanticEnricher, schema, activeTables)
		semCancel()
		if semErr != nil {
			req.ExtraWarnings = append(req.ExtraWarnings, fmt.Sprintf("LLM semantic analysis skipped: %v", semErr))
		} else {
			semanticProfile = sp
		}
	}

	snapshotSizeBytes := req.SnapshotSizeBytes
	if dbSize := analysisDBSize(req.Ingester); dbSize > 0 {
		snapshotSizeBytes = dbSize
	}

	facts := s.BuildProfileFacts(schema, semanticProfile, nil, string(profileMode), snapshotSizeBytes, req.ImportRowLimit, req.ImportTruncated)
	facts.Warnings = append(facts.Warnings, req.ExtraWarnings...)
	profileDuration := time.Since(profileStart)

	snapshotID := req.SnapshotID
	if snapshotID == "" {
		snapshot, err := s.CreateSnapshot(
			ctx, req.SessionID, req.SourceID,
			req.UpstreamKind, req.UpstreamSchema, req.UpstreamObject,
			req.AnalysisTableName, req.RowCount, req.ColCount, schemaSig,
			req.RowsImported, req.RowsSkipped, req.ImportRowLimit, req.ImportTruncated,
			int(req.ImportDuration.Milliseconds()), int(profileDuration.Milliseconds()), snapshotSizeBytes, profileMode,
		)
		if err != nil {
			return nil, err
		}
		snapshotID = snapshot.ID
	} else {
		if err := s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusReady, nil); err != nil {
			return nil, fmt.Errorf("failed to update snapshot status: %w", err)
		}
		if err := s.SnapshotRepo.UpdateSnapshotCompletion(ctx, snapshotID, req.RowCount, req.ColCount, schemaSig,
			req.RowsImported, req.RowsSkipped, req.ImportRowLimit, req.ImportTruncated,
			int(req.ImportDuration.Milliseconds()), int(profileDuration.Milliseconds()), snapshotSizeBytes, profileMode); err != nil {
			return nil, fmt.Errorf("failed to update snapshot completion facts: %w", err)
		}
	}

	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		ds, err := s.DataSourceRepo.GetByID(ctx, req.SourceID)
		if err != nil {
			return nil, err
		}
		workspaceID = ds.WorkspaceID
	}

	profile, profErr := s.CreateSemanticProfile(
		ctx, req.SessionID, workspaceID, req.SourceID, snapshotID,
		req.AnalysisTableName, schemaSig, facts,
	)
	profileID := ""
	if profile != nil {
		profileID = profile.ID
	}
	if profErr != nil {
		errMsg := profErr.Error()
		_ = s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &errMsg)
	}

	return &SnapshotImportResult{
		SnapshotID:        snapshotID,
		ProfileID:         profileID,
		TableName:         req.AnalysisTableName,
		RowCount:          req.RowCount,
		ColCount:          req.ColCount,
		RowsImported:      req.RowsImported,
		RowsSkipped:       req.RowsSkipped,
		ImportRowLimit:    req.ImportRowLimit,
		ImportTruncated:   req.ImportTruncated,
		ImportDurationMs:  int(req.ImportDuration.Milliseconds()),
		ProfileDurationMs: int(profileDuration.Milliseconds()),
		SnapshotSizeBytes: snapshotSizeBytes,
		ProfileMode:       profileMode,
		DataSizeTier:      DataSizeTierForRows(req.RowCount),
		ProfErr:           profErr,
	}, nil
}

func (s *SourceService) failSnapshotIfPresent(ctx context.Context, snapshotID string, err error) {
	if strings.TrimSpace(snapshotID) == "" || err == nil {
		return
	}
	errMsg := err.Error()
	_ = s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &errMsg)
}

func analysisDBSize(ingester *data.Ingester) int64 {
	if ingester == nil || strings.TrimSpace(ingester.DBPath()) == "" {
		return 0
	}
	if fi, err := os.Stat(ingester.DBPath()); err == nil {
		return fi.Size()
	}
	return 0
}

type FileUploadConnector struct {
	Sources *SourceService
	Files   *FileService
}

func NewFileUploadConnector(sources *SourceService, files *FileService) *FileUploadConnector {
	return &FileUploadConnector{Sources: sources, Files: files}
}

func (c *FileUploadConnector) Type() domain.SourceType { return domain.SourceTypeFileUpload }

func (c *FileUploadConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	return nil, nil
}

func (c *FileUploadConnector) PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (c *FileUploadConnector) Test(ctx context.Context, req SourceTestRequest) (map[string]interface{}, error) {
	ds, err := c.Sources.DataSourceRepo.GetByID(ctx, req.SourceID)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}, nil
	}
	if ds.FileID == nil || strings.TrimSpace(*ds.FileID) == "" {
		return map[string]interface{}{"success": false, "message": "file-backed source has no file_id"}, nil
	}
	if _, err := c.Files.GetFile(ctx, *ds.FileID); err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}, nil
	}
	return map[string]interface{}{"success": true, "message": "file source is available"}, nil
}

func (c *FileUploadConnector) Catalog(ctx context.Context, sourceID string) ([]SourceCatalogObject, error) {
	ds, err := c.Sources.DataSourceRepo.GetByID(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if ds.FileID == nil || strings.TrimSpace(*ds.FileID) == "" {
		return nil, fmt.Errorf("file-backed source has no file_id")
	}
	file, err := c.Files.GetFile(ctx, *ds.FileID)
	if err != nil {
		return nil, err
	}
	return []SourceCatalogObject{{
		Name:            file.DisplayName,
		Kind:            string(domain.SourceTypeFileUpload),
		SourceObjectKey: SourceObjectKey(sourceID, string(domain.SourceTypeFileUpload), "", ""),
	}}, nil
}

func (c *FileUploadConnector) Import(ctx context.Context, req SourceImportRequest) (*SnapshotImportResult, error) {
	if c.Sources == nil || c.Files == nil {
		return nil, fmt.Errorf("file upload connector is not initialized")
	}
	if req.Ingester == nil {
		return nil, fmt.Errorf("analysis database is not initialized")
	}
	ds, err := c.Sources.DataSourceRepo.GetByID(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	if ds.FileID == nil || strings.TrimSpace(*ds.FileID) == "" {
		return nil, fmt.Errorf("file-backed source has no file_id")
	}

	tempPath, file, err := c.Files.MaterializeToTemp(ctx, req.SessionID, req.WorkspaceID, *ds.FileID)
	if err != nil {
		return nil, fmt.Errorf("file materialization failed: %w", err)
	}
	defer os.Remove(tempPath)

	importStart := time.Now()
	tableName, rowCount, colCount, err := req.Ingester.ImportFileRawAs(tempPath, SourceScopedFileTableName(file.DisplayName, ds.ID))
	importDuration := time.Since(importStart)
	if err != nil {
		return nil, fmt.Errorf("import failed: %w", err)
	}

	return c.Sources.FinalizeSnapshotImport(ctx, SnapshotImportCompletion{
		SessionID:         req.SessionID,
		WorkspaceID:       req.WorkspaceID,
		SourceID:          ds.ID,
		UpstreamKind:      string(domain.SourceTypeFileUpload),
		UpstreamSchema:    "",
		UpstreamObject:    file.DisplayName,
		AnalysisTableName: tableName,
		RowCount:          rowCount,
		ColCount:          colCount,
		RowsImported:      rowCount,
		RowsSkipped:       0,
		ImportDuration:    importDuration,
		SnapshotSizeBytes: file.SizeBytes,
		AnalyzeSemantics:  false,
		Ingester:          req.Ingester,
	})
}

func SourceScopedFileTableName(displayName, sourceID string) string {
	ext := filepath.Ext(displayName)
	base := strings.TrimSuffix(filepath.Base(displayName), ext)
	if strings.TrimSpace(base) == "" {
		base = "table"
	}
	suffix := sourceTableSuffix(sourceID)
	if suffix == "" {
		return base
	}
	return base + "__" + suffix
}
