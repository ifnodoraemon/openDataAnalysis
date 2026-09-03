package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

type SourceService struct {
	DataSourceRepo           repository.DataSourceRepository
	SourceConfigRepo         repository.SourceConfigRepository
	SnapshotRepo             repository.SourceSnapshotRepository
	SessionSourceBindingRepo repository.SessionSourceBindingRepository
	SemanticProfileRepo      repository.SemanticProfileRepository
	SemanticConfirmationRepo repository.SemanticConfirmationRepository
	SemanticAssetRepo        repository.SemanticAssetRepository
	AuditEventRepo           repository.AuditEventRepository

	credentialSecret      string
	liveConnectorResolver func(domain.SourceType) (LiveQueryConnector, error)
}

func NewSourceService(
	dsRepo repository.DataSourceRepository,
	sourceConfigRepo repository.SourceConfigRepository,
	snapRepo repository.SourceSnapshotRepository,
	bindingRepo repository.SessionSourceBindingRepository,
	profileRepo repository.SemanticProfileRepository,
	confirmRepo repository.SemanticConfirmationRepository,
	assetRepo repository.SemanticAssetRepository,
	auditRepo repository.AuditEventRepository,
) *SourceService {
	for name, dependency := range map[string]interface{}{
		"data source repository":            dsRepo,
		"source config repository":          sourceConfigRepo,
		"snapshot repository":               snapRepo,
		"session source binding repository": bindingRepo,
		"semantic profile repository":       profileRepo,
		"semantic confirmation repository":  confirmRepo,
		"semantic asset repository":         assetRepo,
		"audit event repository":            auditRepo,
	} {
		if dependency == nil {
			panic(name + " is not configured")
		}
	}
	return &SourceService{
		DataSourceRepo:           dsRepo,
		SourceConfigRepo:         sourceConfigRepo,
		SnapshotRepo:             snapRepo,
		SessionSourceBindingRepo: bindingRepo,
		SemanticProfileRepo:      profileRepo,
		SemanticConfirmationRepo: confirmRepo,
		SemanticAssetRepo:        assetRepo,
		AuditEventRepo:           auditRepo,
	}
}

type FileMaterializeResult struct {
	SourceID   string
	SnapshotID string
	TableName  string
	RowCount   int
	ColCount   int
}

type SourceRuntimeTable struct {
	SessionID string
	TableName string
}

func SourceObjectKey(sourceID, upstreamKind, upstreamSchema, upstreamObject string) string {
	identity, err := json.Marshal(struct {
		SourceID       string `json:"source_id"`
		UpstreamKind   string `json:"upstream_kind"`
		UpstreamSchema string `json:"upstream_schema"`
		UpstreamObject string `json:"upstream_object"`
	}{
		SourceID:       sourceID,
		UpstreamKind:   upstreamKind,
		UpstreamSchema: upstreamSchema,
		UpstreamObject: upstreamObject,
	})
	if err != nil {
		panic(fmt.Sprintf("encode source object identity: %v", err))
	}
	digest := sha256.Sum256(identity)
	return "source_object_" + fmt.Sprintf("%x", digest[:])
}

func (s *SourceService) EnsureFileSource(ctx context.Context, workspaceID, fileID, displayName, uploadedBy string) (*domain.DataSource, error) {
	existing, err := s.DataSourceRepo.GetByFileID(ctx, fileID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		return existing, nil
	}
	ds := &domain.DataSource{
		ID:          "ds_" + uuid.New().String()[:12],
		WorkspaceID: workspaceID,
		Name:        displayName,
		SourceType:  domain.SourceTypeFileUpload,
		Status:      domain.SourceStatusActive,
		FileID:      &fileID,
		CreatedBy:   uploadedBy,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.DataSourceRepo.Create(ctx, ds); err != nil {
		return nil, fmt.Errorf("failed to create file-backed data source: %w", err)
	}
	return ds, nil
}

// PendingFileSource describes an uploaded file source in the workspace that
// has no active binding in the session yet — e.g. a multi-sheet workbook
// awaiting worksheet selection, or a file the strict importer rejected. The
// agent can read the original bytes (code_run_python source_file input) and
// import cleaned data (data_import_artifact).
type PendingFileSource struct {
	SourceID    string `json:"source_id"`
	DisplayName string `json:"display_name"`
}

// ListPendingFileSources returns workspace file-upload sources that are not
// bound to the session. It gives the agent pull-based discovery of uploaded
// files that never reached the deterministic import path.
func (s *SourceService) ListPendingFileSources(ctx context.Context, workspaceID, sessionID string) ([]PendingFileSource, error) {
	bindings, err := s.SessionSourceBindingRepo.GetBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	bound := make(map[string]struct{}, len(bindings))
	for _, b := range bindings {
		bound[b.SourceID] = struct{}{}
	}
	sources, err := s.DataSourceRepo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	pending := make([]PendingFileSource, 0)
	for _, ds := range sources {
		if ds.SourceType != domain.SourceTypeFileUpload {
			continue
		}
		if _, ok := bound[ds.ID]; ok {
			continue
		}
		pending = append(pending, PendingFileSource{SourceID: ds.ID, DisplayName: ds.Name})
	}
	return pending, nil
}

func (s *SourceService) GetSessionSources(ctx context.Context, sessionID string) ([]SessionSourceSummary, error) {	bindings, err := s.SessionSourceBindingRepo.GetBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var summaries []SessionSourceSummary
	var partialErrors []string
	for _, b := range bindings {
		ds, err := s.DataSourceRepo.GetByID(ctx, b.SourceID)
		if err != nil {
			log.Printf("GetSessionSources: source lookup failed source_id=%s err=%v", b.SourceID, err)
			partialErrors = append(partialErrors, fmt.Sprintf("source_id=%s: %v", b.SourceID, err))
			continue
		}
		snapshot, err := s.SnapshotRepo.GetByID(ctx, b.ActiveSnapshotID)
		if err != nil {
			log.Printf("GetSessionSources: snapshot lookup failed snapshot_id=%s err=%v", b.ActiveSnapshotID, err)
			partialErrors = append(partialErrors, fmt.Sprintf("snapshot_id=%s: %v", b.ActiveSnapshotID, err))
			continue
		}
		profiles, profErr := s.SemanticProfileRepo.ListBySource(ctx, b.SourceID)
		if profErr != nil {
			log.Printf("GetSessionSources: profile list failed source_id=%s err=%v", b.SourceID, profErr)
			partialErrors = append(partialErrors, fmt.Sprintf("source_id=%s profile list: %v", b.SourceID, profErr))
		}
		var profileStatus string
		var profileID string
		var confirmedOverrideCount int
		profile, profileSelectionErr := selectProfileForSnapshot(profiles, sessionID, snapshot.ID)
		if profileSelectionErr != nil {
			partialErrors = append(partialErrors, profileSelectionErr.Error())
		} else if profile != nil {
			profileStatus = string(profile.ProfileStatus)
			profileID = profile.ID
			confs, confErr := s.SemanticConfirmationRepo.ListByProfile(ctx, profile.ID)
			if confErr != nil {
				partialErrors = append(partialErrors, fmt.Sprintf("profile_id=%s confirmation list: %v", profile.ID, confErr))
			} else {
				confirmedOverrideCount = len(confs)
			}
		} else {
			profileStatus = "pending"
		}
		summaries = append(summaries, SessionSourceSummary{
			SourceID:               ds.ID,
			SourceObjectKey:        b.SourceObjectKey,
			DisplayName:            ds.Name,
			SourceType:             string(ds.SourceType),
			ActiveSnapshotID:       b.ActiveSnapshotID,
			UpstreamKind:           snapshot.UpstreamKind,
			UpstreamSchema:         snapshot.UpstreamSchema,
			UpstreamObject:         snapshot.UpstreamObject,
			AnalysisTableName:      snapshot.AnalysisTableName,
			SnapshotStatus:         string(snapshot.Status),
			ProfileStatus:          profileStatus,
			ProfileID:              profileID,
			ConfirmedOverrideCount: confirmedOverrideCount,
			RowCount:               snapshot.RowCount,
			ColCount:               snapshot.ColumnCount,
			LastImportedAt:         snapshot.ImportedAt,
			RowsImported:           snapshot.RowsImported,
			RowsSkipped:            snapshot.RowsSkipped,
			ImportRowLimit:         snapshot.ImportRowLimit,
			ImportUnbounded:        snapshot.Mode != domain.SnapshotModeLive && snapshot.ImportRowLimit == 0,
			ImportTruncated:        snapshot.ImportTruncated,
			ImportDurationMs:       snapshot.ImportDurationMs,
			ProfileDurationMs:      snapshot.ProfileDurationMs,
			SnapshotSizeBytes:      snapshot.SnapshotSizeBytes,
			ProfileMode:            string(snapshot.ProfileMode),
			Mode:                   string(snapshot.Mode),
			RowCountEstimated:      snapshot.Mode == domain.SnapshotModeLive,
			ErrorMessage: func() string {
				if snapshot.ErrorMessage != nil {
					return *snapshot.ErrorMessage
				}
				return ""
			}(),
		})
	}
	if len(partialErrors) > 0 {
		return summaries, fmt.Errorf("partial errors: %s", strings.Join(partialErrors, "; "))
	}
	return summaries, nil
}

func selectProfileForSnapshot(profiles []domain.SemanticProfile, sessionID, snapshotID string) (*domain.SemanticProfile, error) {
	var match *domain.SemanticProfile
	for i := range profiles {
		if profiles[i].SessionID == sessionID && profiles[i].SnapshotID == snapshotID {
			if match != nil {
				return nil, fmt.Errorf("multiple semantic profiles match session_id=%s snapshot_id=%s", sessionID, snapshotID)
			}
			match = &profiles[i]
		}
	}
	return match, nil
}

func (s *SourceService) RecordSnapshotError(ctx context.Context, snapshotID, errMsg string) error {
	return s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &errMsg)
}

func (s *SourceService) RemoveSessionSource(ctx context.Context, sessionID, sourceID, sourceObjectKey string, dropRuntimeTable func(SourceRuntimeTable) error) error {
	if sourceObjectKey == "" || sourceObjectKey != strings.TrimSpace(sourceObjectKey) {
		return fmt.Errorf("source_object_key is required and must be exact")
	}
	_, err := s.SessionSourceBindingRepo.GetBySessionSourceObject(ctx, sessionID, sourceID, sourceObjectKey)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	snapshots, err := s.SnapshotRepo.ListBySource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("list source snapshots: %w", err)
	}
	selectedSnapshots := make([]domain.SourceSnapshot, 0)
	snapshotIDs := make(map[string]struct{})
	for _, snapshot := range snapshots {
		if snapshot.SessionID == sessionID && SourceObjectKey(sourceID, snapshot.UpstreamKind, snapshot.UpstreamSchema, snapshot.UpstreamObject) == sourceObjectKey {
			selectedSnapshots = append(selectedSnapshots, snapshot)
			snapshotIDs[snapshot.ID] = struct{}{}
		}
	}

	if dropRuntimeTable == nil {
		return fmt.Errorf("runtime table dropper is required")
	}
	for _, snapshot := range selectedSnapshots {
		if snapshot.AnalysisTableName == "" {
			continue
		}
		if err := dropRuntimeTable(SourceRuntimeTable{SessionID: snapshot.SessionID, TableName: snapshot.AnalysisTableName}); err != nil {
			return fmt.Errorf("drop analysis table %s: %w", snapshot.AnalysisTableName, err)
		}
	}

	profiles, err := s.SemanticProfileRepo.ListBySource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("list semantic profiles: %w", err)
	}
	for _, profile := range profiles {
		if _, ok := snapshotIDs[profile.SnapshotID]; !ok {
			continue
		}
		if err := s.SemanticConfirmationRepo.DeleteByProfile(ctx, profile.ID); err != nil {
			return fmt.Errorf("delete confirmations for profile %s: %w", profile.ID, err)
		}
		if err := s.SemanticProfileRepo.Delete(ctx, profile.ID); err != nil {
			return fmt.Errorf("delete semantic profile %s: %w", profile.ID, err)
		}
	}

	for _, snapshot := range selectedSnapshots {
		if err := s.SnapshotRepo.Delete(ctx, snapshot.ID); err != nil {
			return fmt.Errorf("delete source snapshot %s: %w", snapshot.ID, err)
		}
	}

	if err := s.SessionSourceBindingRepo.Delete(ctx, sessionID, sourceID, sourceObjectKey); err != nil {
		return err
	}
	return nil
}

func (s *SourceService) DeleteWorkspaceSource(ctx context.Context, sourceID string, dropRuntimeTable func(SourceRuntimeTable) error) error {
	snapshots, listErr := s.SnapshotRepo.ListBySource(ctx, sourceID)
	if listErr != nil {
		return listErr
	}
	if dropRuntimeTable == nil {
		return fmt.Errorf("runtime table dropper is required")
	}
	for _, snap := range snapshots {
		if snap.AnalysisTableName == "" {
			continue
		}
		if err := dropRuntimeTable(SourceRuntimeTable{
			SessionID: snap.SessionID,
			TableName: snap.AnalysisTableName,
		}); err != nil {
			return fmt.Errorf("drop analysis table %s for session %s: %w", snap.AnalysisTableName, snap.SessionID, err)
		}
	}

	if err := s.DataSourceRepo.Delete(ctx, sourceID); err != nil {
		return err
	}
	return nil
}

func (s *SourceService) GetSessionProfiles(ctx context.Context, sessionID string) ([]SemanticProfileSummary, error) {
	profiles, err := s.SemanticProfileRepo.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var summaries []SemanticProfileSummary
	for _, p := range profiles {
		summaries = append(summaries, SemanticProfileSummary{
			ProfileID:         p.ID,
			SourceID:          p.SourceID,
			AnalysisTableName: p.AnalysisTableName,
			ProfileStatus:     string(p.ProfileStatus),
			SchemaSignature:   p.SchemaSignature,
		})
	}
	return summaries, nil
}

func (s *SourceService) GetProfileDetail(ctx context.Context, profileID string) (*domain.SemanticProfile, []domain.SemanticConfirmation, error) {
	profile, err := s.SemanticProfileRepo.GetByID(ctx, profileID)
	if err != nil {
		return nil, nil, err
	}
	confirmations, err := s.SemanticConfirmationRepo.ListByProfile(ctx, profileID)
	if err != nil {
		return profile, nil, err
	}
	return profile, confirmations, nil
}

func (s *SourceService) GetSessionProfileDetail(ctx context.Context, sessionID, profileID string) (*domain.SemanticProfile, []domain.SemanticConfirmation, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil, fmt.Errorf("session_id is required")
	}
	profile, confirmations, err := s.GetProfileDetail(ctx, profileID)
	if err != nil {
		return nil, nil, err
	}
	if profile.SessionID != sessionID {
		return nil, nil, fmt.Errorf("profile %s does not belong to session %s", profileID, sessionID)
	}
	return profile, confirmations, nil
}

type ProfiledFacts struct {
	Schema            *data.SchemaInfo `json:"schema"`
	ProfileMode       string           `json:"profile_mode"`
	ImportRowLimit    int              `json:"import_row_limit,omitempty"`
	ImportTruncated   bool             `json:"import_truncated,omitempty"`
	SnapshotSizeBytes int64            `json:"snapshot_size_bytes,omitempty"`
	Warnings          []string         `json:"warnings"`
}

func buildProfileFacts(schema *data.SchemaInfo, profileMode domain.ProfileMode, snapshotSizeBytes int64, importRowLimit int, importTruncated bool) (ProfiledFacts, error) {
	if schema == nil {
		return ProfiledFacts{}, fmt.Errorf("schema facts are required")
	}
	if snapshotSizeBytes < 0 || importRowLimit < 0 {
		return ProfiledFacts{}, fmt.Errorf("snapshot size and import row limit must not be negative")
	}
	if importTruncated && importRowLimit == 0 {
		return ProfiledFacts{}, fmt.Errorf("a truncated import requires a positive import row limit")
	}
	facts := ProfiledFacts{
		Schema:            schema,
		ProfileMode:       string(profileMode),
		ImportRowLimit:    importRowLimit,
		ImportTruncated:   importTruncated,
		SnapshotSizeBytes: snapshotSizeBytes,
	}

	switch profileMode {
	case domain.ProfileModeExact:
	case domain.ProfileModeSampled:
		facts.Warnings = append(facts.Warnings, "profile is based on sampled data; statistics are estimated, not exact")
	default:
		return ProfiledFacts{}, fmt.Errorf("unsupported profile mode: %q", profileMode)
	}
	if importTruncated {
		facts.Warnings = append(facts.Warnings, fmt.Sprintf("snapshot import was capped at %d rows; analysis is based on the imported subset, not the full upstream object", importRowLimit))
	}
	return facts, nil
}

func (s *SourceService) CreateSemanticProfile(ctx context.Context, sessionID, sourceID, snapshotID, analysisTableName, schemaSignature string, facts ProfiledFacts) (*domain.SemanticProfile, error) {
	profileJSON, err := json.Marshal(facts)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize profile: %w", err)
	}

	profile := &domain.SemanticProfile{
		ID:                "sp_" + uuid.New().String()[:12],
		SessionID:         sessionID,
		SourceID:          sourceID,
		SnapshotID:        snapshotID,
		AnalysisTableName: analysisTableName,
		SchemaSignature:   schemaSignature,
		ProfileStatus:     domain.ProfileStatusProfiled,
		ProfileJSON:       string(profileJSON),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := s.SemanticProfileRepo.Create(ctx, profile); err != nil {
		return nil, fmt.Errorf("failed to create semantic profile: %w", err)
	}

	return profile, nil
}

func (s *SourceService) ConfirmProfile(ctx context.Context, profileID, workspaceID, sessionID, confirmedBy, scope, overridesJSON, confirmationReceiptID string, provenance domain.ConfirmationProvenance) (*domain.SemanticProfile, []string, error) {
	if scope != string(domain.ConfirmationScopeSession) && scope != string(domain.ConfirmationScopeWorkspace) {
		return nil, nil, fmt.Errorf("invalid confirmation scope: %q; must be \"session\" or \"workspace\"", scope)
	}
	if provenance != domain.ConfirmationProvenanceAuthenticatedRequest && provenance != domain.ConfirmationProvenanceAuthorizationReceipt {
		return nil, nil, fmt.Errorf("invalid confirmation provenance: %q", provenance)
	}
	if confirmationReceiptID != strings.TrimSpace(confirmationReceiptID) {
		return nil, nil, fmt.Errorf("confirmation_receipt_id must not contain leading or trailing whitespace")
	}
	if provenance == domain.ConfirmationProvenanceAuthorizationReceipt && confirmationReceiptID == "" {
		return nil, nil, fmt.Errorf("authorization receipt provenance requires confirmation_receipt_id")
	}
	profile, err := s.SemanticProfileRepo.GetByID(ctx, profileID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get profile: %w", err)
	}
	if sessionID != "" && profile.SessionID != sessionID {
		return nil, nil, fmt.Errorf("profile %s does not belong to session %s", profileID, sessionID)
	}

	if strings.TrimSpace(overridesJSON) == "" || strings.TrimSpace(overridesJSON) == "null" {
		return nil, nil, fmt.Errorf("overrides_json must be an explicit JSON object")
	}
	var patch map[string]json.RawMessage
	if err := decodeStrictJSON([]byte(overridesJSON), &patch); err != nil {
		return nil, nil, fmt.Errorf("invalid overrides_json: %w", err)
	}
	if patch == nil {
		return nil, nil, fmt.Errorf("overrides_json must be a JSON object")
	}
	for key := range patch {
		if key == "" || key != strings.TrimSpace(key) {
			return nil, nil, fmt.Errorf("overrides_json keys must be non-empty and exact")
		}
	}

	confirmation := &domain.SemanticConfirmation{
		ID:                    "sc_" + uuid.New().String()[:12],
		ProfileID:             profileID,
		WorkspaceID:           workspaceID,
		SessionID:             sessionID,
		ConfirmedBy:           confirmedBy,
		ConfirmationReceiptID: confirmationReceiptID,
		Provenance:            provenance,
		Scope:                 domain.ConfirmationScope(scope),
		OverridesJSON:         overridesJSON,
		CreatedAt:             time.Now(),
	}
	alreadyStored := false
	if confirmationReceiptID != "" {
		confirmations, err := s.SemanticConfirmationRepo.ListByProfile(ctx, profileID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to inspect confirmation receipt history: %w", err)
		}
		for i := range confirmations {
			existing := &confirmations[i]
			if existing.ConfirmationReceiptID != confirmationReceiptID {
				continue
			}
			if existing.WorkspaceID != workspaceID || existing.SessionID != sessionID || existing.ConfirmedBy != confirmedBy || existing.Scope != confirmation.Scope || existing.Provenance != provenance || existing.OverridesJSON != overridesJSON {
				return nil, nil, fmt.Errorf("confirmation_receipt_id is already bound to different confirmation facts")
			}
			confirmation = existing
			alreadyStored = true
			break
		}
	}
	if confirmation.ID == "" || confirmation.ID != strings.TrimSpace(confirmation.ID) || confirmation.CreatedAt.IsZero() {
		return nil, nil, fmt.Errorf("stored semantic confirmation is structurally invalid")
	}
	if !alreadyStored {
		if err := s.SemanticConfirmationRepo.Create(ctx, confirmation); err != nil {
			return nil, nil, fmt.Errorf("failed to create semantic confirmation: %w", err)
		}
		metrics.SemanticConfirmationsTotal.WithLabelValues(scope, string(provenance)).Inc()
	}
	var auditErrors []string
	if scope == string(domain.ConfirmationScopeWorkspace) {
		assetAuditErrors, err := s.upsertSemanticAssetsFromConfirmation(ctx, profile, confirmation)
		if err != nil {
			return nil, auditErrors, fmt.Errorf("semantic asset persistence failed: %w", err)
		}
		auditErrors = append(auditErrors, assetAuditErrors...)
	}

	status := domain.ProfileStatusConfirmed
	if err := s.SemanticProfileRepo.UpdateStatus(ctx, profileID, status); err != nil {
		return nil, auditErrors, fmt.Errorf("failed to update profile status: %w", err)
	}
	profile.ProfileStatus = status
	auditPayload, err := auditPayloadJSON(map[string]interface{}{
		"scope":                   scope,
		"schema_signature":        profile.SchemaSignature,
		"confirmation_receipt_id": confirmation.ConfirmationReceiptID,
		"provenance":              provenance,
	})
	if err != nil {
		return nil, auditErrors, fmt.Errorf("failed to serialize profile confirmation audit facts: %w", err)
	}
	if err := s.recordAudit(ctx, domain.AuditEvent{
		WorkspaceID:  workspaceID,
		SessionID:    sessionID,
		ActorUserID:  confirmedBy,
		EventType:    "semantic_profile_confirmed",
		ResourceType: "semantic_profile",
		ResourceID:   profileID,
		PayloadJSON:  auditPayload,
	}); err != nil {
		auditErrors = append(auditErrors, fmt.Sprintf("semantic profile %s: %v", profileID, err))
	}

	return profile, auditErrors, nil
}

func (s *SourceService) CreateConfiguredSource(ctx context.Context, workspaceID, name, createdBy string, sourceType domain.SourceType, sourceConfig *domain.SourceConfig) (*domain.DataSource, error) {
	if sourceType != domain.SourceTypeFileUpload && sourceConfig == nil {
		return nil, fmt.Errorf("source config is required for source type %s", sourceType)
	}
	ds := &domain.DataSource{
		ID:          "ds_" + uuid.New().String()[:12],
		WorkspaceID: workspaceID,
		Name:        name,
		SourceType:  sourceType,
		Status:      domain.SourceStatusActive,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.DataSourceRepo.Create(ctx, ds); err != nil {
		return nil, err
	}
	if sourceConfig != nil {
		sourceConfig.SourceID = ds.ID
		sourceConfig.ConnectorType = sourceType
		now := time.Now()
		if sourceConfig.CreatedAt.IsZero() {
			sourceConfig.CreatedAt = now
		}
		sourceConfig.UpdatedAt = now
		if err := s.SourceConfigRepo.Create(ctx, sourceConfig); err != nil {
			return nil, fmt.Errorf("failed to persist source config: %w", err)
		}
	}
	log.Printf("Created data source id=%s type=%s name=%s", ds.ID, sourceType, name)
	return ds, nil
}

func ComputeSchemaSignature(schema *data.SchemaInfo) string {
	h := sha256.New()
	for _, col := range schema.Columns {
		h.Write([]byte(col.Name + ":" + col.DeclaredType + ":"))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

type SessionSourceSummary struct {
	SourceID               string    `json:"source_id"`
	SourceObjectKey        string    `json:"source_object_key"`
	DisplayName            string    `json:"display_name"`
	SourceType             string    `json:"source_type"`
	ActiveSnapshotID       string    `json:"active_snapshot_id"`
	UpstreamKind           string    `json:"upstream_kind"`
	UpstreamSchema         string    `json:"upstream_schema"`
	UpstreamObject         string    `json:"upstream_object"`
	AnalysisTableName      string    `json:"analysis_table_name"`
	SnapshotStatus         string    `json:"snapshot_status"`
	ProfileStatus          string    `json:"profile_status"`
	ProfileID              string    `json:"profile_id,omitempty"`
	ConfirmedOverrideCount int       `json:"confirmed_override_count"`
	RowCount               int       `json:"row_count"`
	ColCount               int       `json:"column_count"`
	LastImportedAt         time.Time `json:"last_imported_at"`
	RowsImported           int       `json:"rows_imported"`
	RowsSkipped            int       `json:"rows_skipped"`
	// ImportRowLimit is 0 when the import ran without a row cap; the explicit
	// ImportUnbounded flag disambiguates "no limit" from "not set".
	ImportRowLimit         int       `json:"import_row_limit,omitempty"`
	ImportUnbounded        bool      `json:"import_unbounded"`
	ImportTruncated        bool      `json:"import_truncated,omitempty"`
	ImportDurationMs       int       `json:"import_duration_ms"`
	ProfileDurationMs      int       `json:"profile_duration_ms"`
	SnapshotSizeBytes      int64     `json:"snapshot_size_bytes"`
	ProfileMode            string    `json:"profile_mode"`
	Mode                   string    `json:"mode"`
	RowCountEstimated      bool      `json:"row_count_estimated"`
	ErrorMessage           string    `json:"error_message,omitempty"`
}

type SemanticProfileSummary struct {
	ProfileID         string `json:"profile_id"`
	SourceID          string `json:"source_id"`
	AnalysisTableName string `json:"analysis_table_name"`
	ProfileStatus     string `json:"profile_status"`
	SchemaSignature   string `json:"schema_signature"`
}
