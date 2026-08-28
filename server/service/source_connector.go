package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

func decodeStrictJSON(raw []byte, out interface{}) error {
	return jsoncontract.Decode(raw, out)
}

func validateExactConfigText(field, value string) error {
	if value == "" || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain leading or trailing whitespace", field)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s contains a NUL byte", field)
	}
	return nil
}

type SourceConnector interface {
	Type() domain.SourceType
	Spec() SourceConnectorSpec
	NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error)
	PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error)
	Test(ctx context.Context, req SourceTestRequest) (SourceTestResult, error)
	Catalog(ctx context.Context, sourceID string) ([]SourceCatalogObject, error)
}

type ImportingConnector interface {
	SourceConnector
	Import(ctx context.Context, req SourceImportRequest) (*SnapshotImportResult, error)
}

type SourceConnectorSpec struct {
	SourceType          domain.SourceType           `json:"source_type"`
	Label               string                      `json:"label"`
	Category            string                      `json:"category"`
	Configurable        bool                        `json:"configurable"`
	SecurityModeField   string                      `json:"security_mode_field,omitempty"`
	SecurityModeOptions []SourceConnectorEnumOption `json:"security_mode_options,omitempty"`
	SupportsAllowlist   bool                        `json:"supports_allowlist"`
	SupportsCatalog     bool                        `json:"supports_catalog"`
	SupportsImport      bool                        `json:"supports_import"`
}

type SourceConnectorEnumOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type SourceConnectorRegistry struct {
	connectors map[domain.SourceType]SourceConnector
}

func NewSourceConnectorRegistry() *SourceConnectorRegistry {
	return &SourceConnectorRegistry{connectors: map[domain.SourceType]SourceConnector{}}
}

func (r *SourceConnectorRegistry) Register(connector SourceConnector) {
	if r == nil || r.connectors == nil {
		panic("source connector registry is not initialized")
	}
	if connector == nil {
		panic("source connector must not be nil")
	}
	sourceType := connector.Type()
	if sourceType == "" || string(sourceType) != strings.TrimSpace(string(sourceType)) {
		panic("source connector type must be a non-empty exact value")
	}
	if _, exists := r.connectors[sourceType]; exists {
		panic(fmt.Sprintf("source connector %q is already registered", sourceType))
	}
	r.connectors[sourceType] = connector
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

func (r *SourceConnectorRegistry) Specs() []SourceConnectorSpec {
	if r == nil || r.connectors == nil {
		panic("source connector registry is not initialized")
	}
	specs := make([]SourceConnectorSpec, 0, len(r.connectors))
	for _, connector := range r.connectors {
		specs = append(specs, connector.Spec())
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Category != specs[j].Category {
			return specs[i].Category < specs[j].Category
		}
		return specs[i].Label < specs[j].Label
	})
	return specs
}

type SourceTestRequest struct {
	SourceID   string
	AuthSecret string
}

type SourceObjectTestFact struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Exists bool   `json:"exists"`
}

type SourceTestResult struct {
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
	Objects   []SourceObjectTestFact `json:"objects,omitempty"`
	UISummary string                 `json:"ui_summary"`
}

type SourceConfigRequest struct {
	SourceID           string
	RawConfig          json.RawMessage
	ConfigProvided     bool
	RawCredential      json.RawMessage
	CredentialProvided bool
	Existing           *domain.SourceConfig
	RequireCredential  bool
	AuthSecret         string
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
	CleanupErrors     []string
}

func (s *SourceService) BeginSnapshotImport(ctx context.Context, sessionID, sourceID, upstreamKind, upstreamSchema, upstreamObject string) (*domain.SourceSnapshot, error) {
	snapshotID := "snap_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	analysisTableName, err := SnapshotScopedAnalysisTableName(snapshotID)
	if err != nil {
		return nil, err
	}
	snapshot := &domain.SourceSnapshot{
		ID:                snapshotID,
		SessionID:         sessionID,
		SourceID:          sourceID,
		UpstreamKind:      upstreamKind,
		UpstreamSchema:    upstreamSchema,
		UpstreamObject:    upstreamObject,
		AnalysisTableName: analysisTableName,
		Status:            domain.SnapshotStatusCreating,
		ImportedAt:        time.Now(),
		ProfileMode:       domain.ProfileModePending,
	}
	if err := s.SnapshotRepo.Create(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("failed to create source snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *SourceService) FinalizeSnapshotImport(ctx context.Context, req SnapshotImportCompletion) (*SnapshotImportResult, error) {
	if req.Ingester == nil || req.Ingester.GetDB() == nil {
		return nil, fmt.Errorf("analysis database is not initialized")
	}
	if err := validateExactConfigText("analysis_table_name", req.AnalysisTableName); err != nil {
		return nil, err
	}
	if err := validateExactConfigText("snapshot_id", req.SnapshotID); err != nil {
		return nil, err
	}
	if err := validateExactConfigText("session_id", req.SessionID); err != nil {
		return nil, err
	}
	if err := validateExactConfigText("source_id", req.SourceID); err != nil {
		return nil, err
	}
	if err := validateExactConfigText("upstream_kind", req.UpstreamKind); err != nil {
		return nil, err
	}
	if req.RowCount < 0 || req.ColCount < 0 || req.RowsImported < 0 || req.RowsSkipped < 0 || req.ImportRowLimit < 0 || req.SnapshotSizeBytes < 0 {
		return nil, fmt.Errorf("snapshot completion counts and sizes cannot be negative")
	}
	if req.RowsImported != req.RowCount {
		return nil, fmt.Errorf("rows_imported must equal the observed analysis row_count")
	}
	snapshot, err := s.SnapshotRepo.GetByID(ctx, req.SnapshotID)
	if err != nil {
		return nil, fmt.Errorf("load snapshot before completion: %w", err)
	}
	if snapshot.SessionID != req.SessionID || snapshot.SourceID != req.SourceID || snapshot.UpstreamKind != req.UpstreamKind || snapshot.UpstreamSchema != req.UpstreamSchema || snapshot.UpstreamObject != req.UpstreamObject || snapshot.AnalysisTableName != req.AnalysisTableName {
		return nil, fmt.Errorf("snapshot completion identity does not match the creating snapshot")
	}
	if snapshot.Status != domain.SnapshotStatusCreating {
		return nil, fmt.Errorf("snapshot %s is not in creating state", req.SnapshotID)
	}

	schema, schemaErr := data.ExtractSchema(req.Ingester.GetDB(), req.AnalysisTableName)
	if schemaErr != nil {
		statusErr := s.failSnapshotIfPresent(ctx, req.SnapshotID, schemaErr)
		return nil, errors.Join(fmt.Errorf("schema extraction failed: %w", schemaErr), statusErr)
	}
	if schema.RowCount != req.RowCount || len(schema.Columns) != req.ColCount {
		shapeErr := fmt.Errorf("snapshot completion shape rows=%d columns=%d does not match observed rows=%d columns=%d", req.RowCount, req.ColCount, schema.RowCount, len(schema.Columns))
		return nil, errors.Join(shapeErr, s.failSnapshotIfPresent(ctx, req.SnapshotID, shapeErr))
	}
	profileMode := domain.ProfileModeExact
	if schema.Sampling.Estimated {
		profileMode = domain.ProfileModeSampled
	}
	schemaSig := ComputeSchemaSignature(schema)

	profileStart := time.Now()
	snapshotSizeBytes := req.SnapshotSizeBytes
	dbSize, err := analysisDBSize(req.Ingester)
	if err != nil {
		return nil, errors.Join(err, s.failSnapshotIfPresent(ctx, req.SnapshotID, err))
	}
	if dbSize > 0 {
		snapshotSizeBytes = dbSize
	}

	facts, err := buildProfileFacts(schema, profileMode, snapshotSizeBytes, req.ImportRowLimit, req.ImportTruncated)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build structural profile facts: %w", err), s.failSnapshotIfPresent(ctx, req.SnapshotID, err))
	}
	facts.Warnings = append(facts.Warnings, req.ExtraWarnings...)
	profileDuration := time.Since(profileStart)

	snapshotID := req.SnapshotID
	if err := s.SnapshotRepo.UpdateSnapshotCompletion(ctx, snapshotID, req.RowCount, req.ColCount, schemaSig,
		req.RowsImported, req.RowsSkipped, req.ImportRowLimit, req.ImportTruncated,
		int(req.ImportDuration.Milliseconds()), int(profileDuration.Milliseconds()), snapshotSizeBytes, profileMode); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to update snapshot completion facts: %w", err), s.failSnapshotIfPresent(ctx, snapshotID, err))
	}
	if err := s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusReady, nil); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to update snapshot status: %w", err), s.failSnapshotIfPresent(ctx, snapshotID, err))
	}

	profile, profErr := s.CreateSemanticProfile(
		ctx, req.SessionID, req.SourceID, snapshotID,
		req.AnalysisTableName, schemaSig, facts,
	)
	profileID := ""
	if profile != nil {
		profileID = profile.ID
	}
	if profErr != nil {
		return nil, errors.Join(fmt.Errorf("failed to persist structural profile facts: %w", profErr), s.failSnapshotIfPresent(ctx, snapshotID, profErr))
	}
	sourceObjectKey := SourceObjectKey(req.SourceID, req.UpstreamKind, req.UpstreamSchema, req.UpstreamObject)
	binding := &domain.SessionSourceBinding{
		SessionID: req.SessionID, SourceID: req.SourceID, SourceObjectKey: sourceObjectKey,
		ActiveSnapshotID: snapshotID, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.SessionSourceBindingRepo.Upsert(ctx, binding); err != nil {
		errMsg := "failed to activate completed source snapshot"
		statusErr := s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &errMsg)
		return nil, errors.Join(fmt.Errorf("failed to activate completed source snapshot: %w", err), statusErr)
	}
	cleanupErrors := s.cleanupSupersededSnapshots(ctx, req.Ingester, req.SessionID, req.SourceID, sourceObjectKey, snapshotID)

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
		CleanupErrors:     cleanupErrors,
	}, nil
}

func SnapshotScopedAnalysisTableName(snapshotID string) (string, error) {
	if snapshotID == "" || snapshotID != strings.TrimSpace(snapshotID) {
		return "", fmt.Errorf("snapshot ID must be a non-empty exact value")
	}
	tableName := "analysis_" + snapshotID
	if err := data.ValidateSQLIdent(tableName); err != nil {
		return "", fmt.Errorf("snapshot ID cannot form an analysis table identity: %w", err)
	}
	return tableName, nil
}

func (s *SourceService) cleanupSupersededSnapshots(ctx context.Context, ingester *data.Ingester, sessionID, sourceID, sourceObjectKey, activeSnapshotID string) []string {
	snapshots, err := s.SnapshotRepo.ListBySource(ctx, sourceID)
	if err != nil {
		return []string{fmt.Sprintf("list superseded snapshots: %v", err)}
	}
	profiles, err := s.SemanticProfileRepo.ListBySource(ctx, sourceID)
	if err != nil {
		return []string{fmt.Sprintf("list superseded profiles: %v", err)}
	}
	profilesBySnapshot := make(map[string][]domain.SemanticProfile)
	for _, profile := range profiles {
		profilesBySnapshot[profile.SnapshotID] = append(profilesBySnapshot[profile.SnapshotID], profile)
	}
	var cleanupErrors []string
	for _, snapshot := range snapshots {
		if snapshot.ID == activeSnapshotID || snapshot.SessionID != sessionID || SourceObjectKey(sourceID, snapshot.UpstreamKind, snapshot.UpstreamSchema, snapshot.UpstreamObject) != sourceObjectKey {
			continue
		}
		if ingester != nil && snapshot.AnalysisTableName != "" {
			if err := ingester.DropTable(snapshot.AnalysisTableName); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("drop table %s: %v", snapshot.AnalysisTableName, err))
				continue
			}
		}
		failed := false
		for _, profile := range profilesBySnapshot[snapshot.ID] {
			if err := s.SemanticConfirmationRepo.DeleteByProfile(ctx, profile.ID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("delete confirmations for %s: %v", profile.ID, err))
				failed = true
				continue
			}
			if err := s.SemanticProfileRepo.Delete(ctx, profile.ID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("delete profile %s: %v", profile.ID, err))
				failed = true
			}
		}
		if failed {
			continue
		}
		if err := s.SnapshotRepo.Delete(ctx, snapshot.ID); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("delete snapshot %s: %v", snapshot.ID, err))
			continue
		}
	}
	return cleanupErrors
}

func (s *SourceService) failSnapshotIfPresent(ctx context.Context, snapshotID string, err error) error {
	if strings.TrimSpace(snapshotID) == "" || err == nil {
		return nil
	}
	errMsg := err.Error()
	if statusErr := s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &errMsg); statusErr != nil {
		return fmt.Errorf("failed to persist snapshot failure state: %w", statusErr)
	}
	return nil
}

func analysisDBSize(ingester *data.Ingester) (int64, error) {
	if ingester == nil || strings.TrimSpace(ingester.DBPath()) == "" {
		return 0, fmt.Errorf("analysis database path is unavailable")
	}
	fi, err := os.Stat(ingester.DBPath())
	if err != nil {
		return 0, fmt.Errorf("inspect analysis database size: %w", err)
	}
	return fi.Size(), nil
}

type FileUploadConnector struct {
	Sources *SourceService
	Files   *FileService
}

func NewFileUploadConnector(sources *SourceService, files *FileService) *FileUploadConnector {
	if sources == nil || files == nil {
		panic("file upload connector requires source and file services")
	}
	return &FileUploadConnector{Sources: sources, Files: files}
}

func (c *FileUploadConnector) Type() domain.SourceType { return domain.SourceTypeFileUpload }

func (c *FileUploadConnector) Spec() SourceConnectorSpec {
	return SourceConnectorSpec{
		SourceType:      domain.SourceTypeFileUpload,
		Label:           "上传文件",
		Category:        "file",
		Configurable:    false,
		SupportsCatalog: true,
		SupportsImport:  true,
	}
}

func (c *FileUploadConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	return nil, fmt.Errorf("file_upload sources do not accept connector configuration")
}

func (c *FileUploadConnector) PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (c *FileUploadConnector) Test(ctx context.Context, req SourceTestRequest) (SourceTestResult, error) {
	ds, err := c.Sources.DataSourceRepo.GetByID(ctx, req.SourceID)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}, nil
	}
	if ds.FileID == nil || strings.TrimSpace(*ds.FileID) == "" {
		return SourceTestResult{Success: false, Error: "file-backed source has no file_id"}, nil
	}
	if _, err := c.Files.GetFile(ctx, *ds.FileID); err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}, nil
	}
	return SourceTestResult{Success: true}, nil
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

	preSnapshot, err := c.Sources.BeginSnapshotImport(
		ctx, req.SessionID, ds.ID,
		string(domain.SourceTypeFileUpload), "", "",
	)
	if err != nil {
		return nil, err
	}

	importStart := time.Now()
	tableName, rowCount, colCount, err := req.Ingester.ImportFileRawAs(tempPath, preSnapshot.AnalysisTableName)
	importDuration := time.Since(importStart)
	if err != nil {
		errMsg := err.Error()
		statusErr := c.Sources.SnapshotRepo.UpdateStatus(ctx, preSnapshot.ID, domain.SnapshotStatusFailed, &errMsg)
		return nil, errors.Join(fmt.Errorf("import failed: %w", err), statusErr)
	}

	return c.Sources.FinalizeSnapshotImport(ctx, SnapshotImportCompletion{
		SnapshotID:        preSnapshot.ID,
		SessionID:         req.SessionID,
		SourceID:          ds.ID,
		UpstreamKind:      string(domain.SourceTypeFileUpload),
		UpstreamSchema:    "",
		UpstreamObject:    "",
		AnalysisTableName: tableName,
		RowCount:          rowCount,
		ColCount:          colCount,
		RowsImported:      rowCount,
		RowsSkipped:       0,
		ImportDuration:    importDuration,
		SnapshotSizeBytes: file.SizeBytes,
		Ingester:          req.Ingester,
	})
}
