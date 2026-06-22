package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

type SourceService struct {
	DataSourceRepo           repository.DataSourceRepository
	DBConnectionRepo         repository.DatabaseConnectionRepository
	SnapshotRepo             repository.SourceSnapshotRepository
	SessionSourceBindingRepo repository.SessionSourceBindingRepository
	SemanticProfileRepo      repository.SemanticProfileRepository
	SemanticConfirmationRepo repository.SemanticConfirmationRepository
}

func NewSourceService(
	dsRepo repository.DataSourceRepository,
	dbConnRepo repository.DatabaseConnectionRepository,
	snapRepo repository.SourceSnapshotRepository,
	bindingRepo repository.SessionSourceBindingRepository,
	profileRepo repository.SemanticProfileRepository,
	confirmRepo repository.SemanticConfirmationRepository,
) *SourceService {
	return &SourceService{
		DataSourceRepo:           dsRepo,
		DBConnectionRepo:         dbConnRepo,
		SnapshotRepo:             snapRepo,
		SessionSourceBindingRepo: bindingRepo,
		SemanticProfileRepo:      profileRepo,
		SemanticConfirmationRepo: confirmRepo,
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
	kind := strings.TrimSpace(upstreamKind)
	if kind == "" {
		kind = "source"
	}
	if kind == "file_upload" {
		return "file_upload:" + strings.TrimSpace(sourceID)
	}
	schema := strings.TrimSpace(upstreamSchema)
	object := strings.TrimSpace(upstreamObject)
	if schema != "" || object != "" {
		return kind + ":" + schema + "." + object
	}
	return kind + ":" + strings.TrimSpace(sourceID)
}

func (s *SourceService) cleanupOldSnapshots(ctx context.Context, sessionID, sourceID, sourceObjectKey string) {
	oldSnapshots, err := s.SnapshotRepo.ListBySource(ctx, sourceID)
	if err != nil {
		log.Printf("cleanupOldSnapshots: ListBySource failed source_id=%s err=%v", sourceID, err)
		return
	}
	for _, snap := range oldSnapshots {
		if snap.SessionID != sessionID {
			continue
		}
		if SourceObjectKey(sourceID, snap.UpstreamKind, snap.UpstreamSchema, snap.UpstreamObject) != sourceObjectKey {
			continue
		}
		profiles, profErr := s.SemanticProfileRepo.ListBySource(ctx, sourceID)
		if profErr != nil {
			log.Printf("cleanupOldSnapshots: ListBySource profiles failed source_id=%s err=%v", sourceID, profErr)
		}
		for _, p := range profiles {
			if p.SnapshotID == snap.ID {
				if delErr := s.SemanticConfirmationRepo.DeleteByProfile(ctx, p.ID); delErr != nil {
					log.Printf("cleanupOldSnapshots: delete confirmations failed profile_id=%s err=%v", p.ID, delErr)
				}
				if delErr := s.SemanticProfileRepo.Delete(ctx, p.ID); delErr != nil {
					log.Printf("cleanupOldSnapshots: delete profile failed profile_id=%s err=%v", p.ID, delErr)
				}
			}
		}
		if delErr := s.SnapshotRepo.Delete(ctx, snap.ID); delErr != nil {
			log.Printf("cleanupOldSnapshots: delete snapshot failed snapshot_id=%s err=%v", snap.ID, delErr)
		}
	}
}

func (s *SourceService) EnsureFileSource(ctx context.Context, workspaceID, fileID, displayName, uploadedBy string) (*domain.DataSource, error) {
	existing, err := s.DataSourceRepo.GetByFileID(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
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

func (s *SourceService) CreateSnapshot(ctx context.Context, sessionID, sourceID, upstreamKind, upstreamSchema, upstreamObject, analysisTableName string, rowCount, colCount int, schemaSignature string, rowsImported, rowsSkipped, importDurationMs, profileDurationMs int, snapshotSizeBytes int64, profileMode domain.ProfileMode) (*domain.SourceSnapshot, error) {
	sourceObjectKey := SourceObjectKey(sourceID, upstreamKind, upstreamSchema, upstreamObject)
	s.cleanupOldSnapshots(ctx, sessionID, sourceID, sourceObjectKey)

	snapshot := &domain.SourceSnapshot{
		ID:                "snap_" + uuid.New().String()[:12],
		SessionID:         sessionID,
		SourceID:          sourceID,
		UpstreamKind:      upstreamKind,
		UpstreamSchema:    upstreamSchema,
		UpstreamObject:    upstreamObject,
		AnalysisTableName: analysisTableName,
		RowCount:          rowCount,
		ColumnCount:       colCount,
		Status:            domain.SnapshotStatusReady,
		SchemaSignature:   schemaSignature,
		ImportedAt:        time.Now(),
		RowsImported:      rowsImported,
		RowsSkipped:       rowsSkipped,
		ImportDurationMs:  importDurationMs,
		ProfileDurationMs: profileDurationMs,
		SnapshotSizeBytes: snapshotSizeBytes,
		ProfileMode:       profileMode,
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
		return nil, fmt.Errorf("failed to create session source binding: %w", err)
	}
	return snapshot, nil
}

func (s *SourceService) GetActiveSnapshotForFile(ctx context.Context, sessionID, fileID string) (*domain.SourceSnapshot, error) {
	ds, err := s.DataSourceRepo.GetByFileID(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if ds == nil {
		return nil, nil
	}
	binding, err := s.SessionSourceBindingRepo.GetBySessionSourceObject(ctx, sessionID, ds.ID, SourceObjectKey(ds.ID, "file_upload", "", ""))
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, nil
	}
	return s.SnapshotRepo.GetByID(ctx, binding.ActiveSnapshotID)
}

func (s *SourceService) GetSessionSources(ctx context.Context, sessionID string) ([]SessionSourceSummary, error) {
	bindings, err := s.SessionSourceBindingRepo.GetBySession(ctx, sessionID)
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
		}
		var semanticStatus string
		var profileID string
		var ambiguityCount int
		var confirmedOverrideCount int
		if profile := selectProfileForSnapshot(profiles, sessionID, snapshot.ID); profile != nil {
			semanticStatus = string(profile.ProfileStatus)
			profileID = profile.ID
			var facts ProfiledFacts
			if err := json.Unmarshal([]byte(profile.ProfileJSON), &facts); err == nil {
				ambiguityCount = len(facts.Ambiguities)
			}
			confs, confErr := s.SemanticConfirmationRepo.ListByProfile(ctx, profile.ID)
			if confErr == nil {
				confirmedOverrideCount = len(confs)
			}
		} else {
			semanticStatus = "pending"
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
			SemanticStatus:         semanticStatus,
			ProfileID:              profileID,
			AmbiguityCount:         ambiguityCount,
			ConfirmedOverrideCount: confirmedOverrideCount,
			RowCount:               snapshot.RowCount,
			ColCount:               snapshot.ColumnCount,
			LastImportedAt:         snapshot.ImportedAt,
			LargeDataset:           snapshot.RowCount >= 1000000,
			RowsImported:           snapshot.RowsImported,
			RowsSkipped:            snapshot.RowsSkipped,
			ImportDurationMs:       snapshot.ImportDurationMs,
			ProfileDurationMs:      snapshot.ProfileDurationMs,
			SnapshotSizeBytes:      snapshot.SnapshotSizeBytes,
			ProfileMode:            string(snapshot.ProfileMode),
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

func selectProfileForSnapshot(profiles []domain.SemanticProfile, sessionID, snapshotID string) *domain.SemanticProfile {
	for i := range profiles {
		if profiles[i].SessionID == sessionID && profiles[i].SnapshotID == snapshotID {
			return &profiles[i]
		}
	}
	return nil
}

func (s *SourceService) RecordSnapshotError(ctx context.Context, snapshotID, errMsg string) error {
	return s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &errMsg)
}

func (s *SourceService) RemoveSessionSource(ctx context.Context, sessionID, sourceID, sourceObjectKey string) ([]string, error) {
	if strings.TrimSpace(sourceObjectKey) == "" {
		return nil, fmt.Errorf("source_object_key is required")
	}
	binding, err := s.SessionSourceBindingRepo.GetBySessionSourceObject(ctx, sessionID, sourceID, sourceObjectKey)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, nil
	}

	tableNames := []string{}
	snapshot, snapErr := s.SnapshotRepo.GetByID(ctx, binding.ActiveSnapshotID)
	if snapErr == nil && snapshot != nil {
		tableNames = append(tableNames, snapshot.AnalysisTableName)
	}

	profiles, profErr := s.SemanticProfileRepo.ListBySource(ctx, sourceID)
	if profErr != nil {
		log.Printf("RemoveSessionSource: list profiles failed source_id=%s err=%v", sourceID, profErr)
	}
	for _, profile := range profiles {
		if profile.SessionID != sessionID || profile.SnapshotID != binding.ActiveSnapshotID {
			continue
		}
		if err := s.SemanticConfirmationRepo.DeleteByProfile(ctx, profile.ID); err != nil {
			log.Printf("RemoveSessionSource: delete confirmations failed profile_id=%s err=%v", profile.ID, err)
		}
		if err := s.SemanticProfileRepo.Delete(ctx, profile.ID); err != nil {
			log.Printf("RemoveSessionSource: delete profile failed profile_id=%s err=%v", profile.ID, err)
		}
	}

	if snapshot != nil {
		if err := s.SnapshotRepo.Delete(ctx, snapshot.ID); err != nil {
			log.Printf("RemoveSessionSource: delete snapshot failed snapshot_id=%s err=%v", snapshot.ID, err)
		}
	}

	if err := s.SessionSourceBindingRepo.Delete(ctx, sessionID, sourceID, sourceObjectKey); err != nil {
		return tableNames, err
	}
	return tableNames, nil
}

func (s *SourceService) DeleteWorkspaceSource(ctx context.Context, sourceID string) ([]SourceRuntimeTable, error) {
	snapshots, listErr := s.SnapshotRepo.ListBySource(ctx, sourceID)
	if listErr != nil {
		return nil, listErr
	}
	tables := make([]SourceRuntimeTable, 0, len(snapshots))
	for _, snap := range snapshots {
		if strings.TrimSpace(snap.AnalysisTableName) == "" {
			continue
		}
		tables = append(tables, SourceRuntimeTable{
			SessionID: snap.SessionID,
			TableName: snap.AnalysisTableName,
		})
	}

	profiles, profErr := s.SemanticProfileRepo.ListBySource(ctx, sourceID)
	if profErr != nil {
		log.Printf("DeleteWorkspaceSource: list profiles failed source_id=%s err=%v", sourceID, profErr)
	}
	for _, profile := range profiles {
		if err := s.SemanticConfirmationRepo.DeleteByProfile(ctx, profile.ID); err != nil {
			log.Printf("DeleteWorkspaceSource: delete confirmations failed profile_id=%s err=%v", profile.ID, err)
		}
	}

	if err := s.DataSourceRepo.Delete(ctx, sourceID); err != nil {
		return tables, err
	}
	return tables, nil
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

type ProfiledFacts struct {
	Schema             *data.SchemaInfo    `json:"schema"`
	ProfileMode        string              `json:"profile_mode"`
	SnapshotSizeBytes  int64               `json:"snapshot_size_bytes,omitempty"`
	SemanticCandidates []SemanticCandidate `json:"semantic_candidates"`
	JoinCandidates     []JoinCandidate     `json:"join_candidates"`
	MetricCandidates   []MetricCandidate   `json:"metric_candidates"`
	TimeCandidates     []TimeCandidate     `json:"time_candidates"`
	UnitCandidates     []UnitCandidate     `json:"unit_candidates"`
	Ambiguities        []Ambiguity         `json:"ambiguities"`
	Warnings           []string            `json:"warnings"`
}

type SemanticCandidate struct {
	ColumnName    string `json:"column_name"`
	BusinessAlias string `json:"business_alias"`
	Role          string `json:"role"`
	Estimated     bool   `json:"estimated"`
}

type JoinCandidate struct {
	LeftTable   string `json:"left_table"`
	LeftColumn  string `json:"left_column"`
	RightTable  string `json:"right_table"`
	RightColumn string `json:"right_column"`
	Reason      string `json:"reason"`
	Estimated   bool   `json:"estimated"`
}

type MetricCandidate struct {
	ColumnName  string `json:"column_name"`
	SemanticKey string `json:"semantic_key"`
	Estimated   bool   `json:"estimated"`
}

type TimeCandidate struct {
	ColumnName    string `json:"column_name"`
	Grain         string `json:"grain"`
	CoverageStart string `json:"coverage_start,omitempty"`
	CoverageEnd   string `json:"coverage_end,omitempty"`
	Estimated     bool   `json:"estimated"`
}

type UnitCandidate struct {
	ColumnName   string  `json:"column_name"`
	DetectedUnit string  `json:"detected_unit"`
	Scale        float64 `json:"scale,omitempty"`
	ConflictWith *string `json:"conflict_with,omitempty"`
	Estimated    bool    `json:"estimated"`
}

type Ambiguity struct {
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Candidates  []string `json:"candidates"`
}

func (s *SourceService) BuildProfileFacts(schema *data.SchemaInfo, semanticProfile *data.SemanticProfile, activeTables []string, profileMode string, snapshotSizeBytes int64) ProfiledFacts {
	isEstimated := profileMode != string(domain.ProfileModeExact)
	facts := ProfiledFacts{
		Schema:            schema,
		ProfileMode:       profileMode,
		SnapshotSizeBytes: snapshotSizeBytes,
	}

	for _, col := range schema.Columns {
		facts.SemanticCandidates = append(facts.SemanticCandidates, SemanticCandidate{
			ColumnName:    col.Name,
			BusinessAlias: "",
			Role:          inferColumnRole(col),
			Estimated:     isEstimated,
		})
	}

	if semanticProfile != nil {
		for i := range semanticProfile.Columns {
			col := &semanticProfile.Columns[i]
			for j := range facts.SemanticCandidates {
				if facts.SemanticCandidates[j].ColumnName == col.Name {
					facts.SemanticCandidates[j].BusinessAlias = col.BusinessAlias
					if col.Role != "" {
						facts.SemanticCandidates[j].Role = col.Role
					}
				}
			}
		}
	}

	for _, tc := range schema.TimeColumns {
		facts.TimeCandidates = append(facts.TimeCandidates, TimeCandidate{
			ColumnName:    tc.Name,
			Grain:         tc.Grain,
			CoverageStart: tc.CoverageStart,
			CoverageEnd:   tc.CoverageEnd,
			Estimated:     isEstimated,
		})
	}

	if len(schema.TimeColumns) > 1 {
		candidates := make([]string, len(schema.TimeColumns))
		for i, tc := range schema.TimeColumns {
			candidates[i] = tc.Name
		}
		facts.Ambiguities = append(facts.Ambiguities, Ambiguity{
			Kind:        "multiple_time_columns",
			Description: "multiple time column candidates",
			Candidates:  candidates,
		})
	}

	ambiguousMetricGroups := data.InferAmbiguousMetricGroups(schema.Columns)
	for key, names := range ambiguousMetricGroups {
		facts.Ambiguities = append(facts.Ambiguities, Ambiguity{
			Kind:        "ambiguous_metrics",
			Description: fmt.Sprintf("same-semantics numeric column candidates: %s", key),
			Candidates:  names,
		})
		for _, name := range names {
			facts.MetricCandidates = append(facts.MetricCandidates, MetricCandidate{
				ColumnName:  name,
				SemanticKey: key,
				Estimated:   isEstimated,
			})
		}
	}

	if semanticProfile != nil {
		for _, rel := range semanticProfile.Relations {
			facts.JoinCandidates = append(facts.JoinCandidates, JoinCandidate{
				LeftTable:   schema.TableName,
				LeftColumn:  rel.SourceColumn,
				RightTable:  rel.TargetTable,
				RightColumn: rel.TargetColumn,
				Reason:      rel.Reason,
				Estimated:   true,
			})
		}
	}

	facts.UnitCandidates = inferUnitCandidates(schema.Columns, isEstimated)
	if len(facts.UnitCandidates) > 1 {
		var candidates []string
		for _, uc := range facts.UnitCandidates {
			candidates = append(candidates, uc.ColumnName+"("+uc.DetectedUnit+")")
		}
		facts.Ambiguities = append(facts.Ambiguities, Ambiguity{
			Kind:        "ambiguous_units",
			Description: "multiple columns with conflicting unit candidates",
			Candidates:  candidates,
		})
	}

	if semanticProfile != nil {
		for _, col := range semanticProfile.Columns {
			if len(col.Warnings) > 0 {
				for _, w := range col.Warnings {
					facts.Warnings = append(facts.Warnings, fmt.Sprintf("column %s: %s", col.Name, w))
				}
			}
		}
	}
	if string(profileMode) != string(domain.ProfileModeExact) {
		facts.Warnings = append(facts.Warnings, "profile is based on sampled data; statistics are estimated, not exact")
	}

	return facts
}

func (s *SourceService) CreateSemanticProfile(ctx context.Context, sessionID, workspaceID, sourceID, snapshotID, analysisTableName, schemaSignature string, facts ProfiledFacts) (*domain.SemanticProfile, error) {
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

	wsConfirmation, wsErr := s.SemanticProfileRepo.FindWorkspaceConfirmation(ctx, workspaceID, schemaSignature)
	if wsErr != nil {
		log.Printf("CreateSemanticProfile: FindWorkspaceConfirmation failed workspace_id=%s signature=%s err=%v", workspaceID, schemaSignature, wsErr)
	}
	if wsConfirmation != nil {
		confirmations := []domain.SemanticConfirmation{*wsConfirmation}
		merged := applyConfirmationsToProfile(string(profileJSON), confirmations)
		merged = removeResolvedAmbiguities(merged, confirmationOverrideJSONs(confirmations))
		if err := s.SemanticProfileRepo.UpdateProfileJSON(ctx, profile.ID, merged); err != nil {
			log.Printf("CreateSemanticProfile: merge workspace overrides failed profile_id=%s err=%v", profile.ID, err)
		} else {
			profileJSON = []byte(merged)
			profile.ProfileJSON = merged
		}
		status := profileStatusForJSON(merged)
		_ = s.SemanticProfileRepo.UpdateStatus(ctx, profile.ID, status)
		profile.ProfileStatus = status
		log.Printf("workspace confirmation auto-applied for profile %s (signature=%s)", profile.ID, schemaSignature)
	}

	return profile, nil
}

func (s *SourceService) ConfirmProfile(ctx context.Context, profileID, workspaceID, sessionID, confirmedBy, scope, overridesJSON string) (*domain.SemanticProfile, error) {
	if scope != string(domain.ConfirmationScopeSession) && scope != string(domain.ConfirmationScopeWorkspace) {
		return nil, fmt.Errorf("invalid confirmation scope: %q; must be \"session\" or \"workspace\"", scope)
	}
	profile, err := s.SemanticProfileRepo.GetByID(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}
	if sessionID != "" && profile.SessionID != sessionID {
		return nil, fmt.Errorf("profile %s does not belong to session %s", profileID, sessionID)
	}

	var facts ProfiledFacts
	if err := json.Unmarshal([]byte(profile.ProfileJSON), &facts); err != nil {
		return nil, fmt.Errorf("failed to parse profile json: %w", err)
	}
	var overrides map[string]interface{}
	trimmedOverrides := strings.TrimSpace(overridesJSON)
	if trimmedOverrides != "" && trimmedOverrides != "null" {
		if err := json.Unmarshal([]byte(overridesJSON), &overrides); err != nil {
			return nil, fmt.Errorf("invalid overrides_json: %w", err)
		}
	}
	if len(facts.Ambiguities) > 0 {
		if trimmedOverrides == "" || trimmedOverrides == "{}" || trimmedOverrides == "null" {
			return nil, fmt.Errorf("profile has %d unresolved ambiguities; empty overrides cannot confirm them", len(facts.Ambiguities))
		}
		if len(overrides) == 0 {
			return nil, fmt.Errorf("profile has %d unresolved ambiguities; empty overrides cannot confirm them", len(facts.Ambiguities))
		}
		if !overridesResolveAnyAmbiguity(facts.Ambiguities, []string{overridesJSON}) {
			return nil, fmt.Errorf("overrides_json does not resolve any current profile ambiguity")
		}
	}

	confirmation := &domain.SemanticConfirmation{
		ID:            "sc_" + uuid.New().String()[:12],
		ProfileID:     profileID,
		WorkspaceID:   workspaceID,
		SessionID:     sessionID,
		ConfirmedBy:   confirmedBy,
		Scope:         domain.ConfirmationScope(scope),
		OverridesJSON: normalizedOverridesJSON(overridesJSON),
		CreatedAt:     time.Now(),
	}
	if err := s.SemanticConfirmationRepo.Create(ctx, confirmation); err != nil {
		return nil, fmt.Errorf("failed to create semantic confirmation: %w", err)
	}

	confirmations := s.applicableConfirmations(ctx, workspaceID, profile.SchemaSignature, profileID)
	merged := applyConfirmationsToProfile(profile.ProfileJSON, confirmations)
	merged = removeResolvedAmbiguities(merged, confirmationOverrideJSONs(confirmations))
	if err := s.SemanticProfileRepo.UpdateProfileJSON(ctx, profileID, merged); err != nil {
		log.Printf("ConfirmProfile: UpdateProfileJSON failed profile_id=%s err=%v", profileID, err)
	}
	status := profileStatusForJSON(merged)
	if err := s.SemanticProfileRepo.UpdateStatus(ctx, profileID, status); err != nil {
		log.Printf("ConfirmProfile: UpdateStatus failed profile_id=%s err=%v", profileID, err)
	}
	profile.ProfileJSON = merged
	profile.ProfileStatus = status

	return profile, nil
}

func (s *SourceService) applicableConfirmations(ctx context.Context, workspaceID, schemaSignature, profileID string) []domain.SemanticConfirmation {
	var confirmations []domain.SemanticConfirmation
	if workspaceID != "" && schemaSignature != "" {
		wsConf, wsErr := s.SemanticProfileRepo.FindWorkspaceConfirmation(ctx, workspaceID, schemaSignature)
		if wsErr != nil {
			log.Printf("applicableConfirmations: FindWorkspaceConfirmation failed workspace_id=%s signature=%s err=%v", workspaceID, schemaSignature, wsErr)
		}
		if wsConf != nil {
			confirmations = append(confirmations, *wsConf)
		}
	}
	if profileID != "" {
		profileConfs, confErr := s.SemanticConfirmationRepo.ListByProfile(ctx, profileID)
		if confErr != nil {
			log.Printf("applicableConfirmations: ListByProfile failed profile_id=%s err=%v", profileID, confErr)
		} else {
			sort.SliceStable(profileConfs, func(i, j int) bool {
				return profileConfs[i].CreatedAt.Before(profileConfs[j].CreatedAt)
			})
			for i := range profileConfs {
				if profileConfs[i].Scope == domain.ConfirmationScopeSession {
					confirmations = append(confirmations, profileConfs[i])
				}
			}
		}
	}
	return confirmations
}

func applyConfirmationsToProfile(profileJSON string, confirmations []domain.SemanticConfirmation) string {
	var profile map[string]interface{}
	if err := json.Unmarshal([]byte(profileJSON), &profile); err != nil {
		return profileJSON
	}

	for i := range confirmations {
		profile = deepMergeOverrideIntoProfile(profile, confirmations[i].OverridesJSON)
	}

	return marshalProfile(profile)
}

func deepMergeOverrideIntoProfile(profile map[string]interface{}, overridesJSON string) map[string]interface{} {
	var overrides map[string]interface{}
	if err := json.Unmarshal([]byte(overridesJSON), &overrides); err != nil {
		return profile
	}
	for k, v := range overrides {
		if existing, ok := profile[k]; ok {
			profile[k] = deepMergeValues(existing, v)
		} else {
			profile[k] = v
		}
	}
	return profile
}

func deepMergeValues(base, override interface{}) interface{} {
	baseMap, baseIsMap := base.(map[string]interface{})
	overMap, overIsMap := override.(map[string]interface{})
	if baseIsMap && overIsMap {
		for k, v := range overMap {
			if existing, ok := baseMap[k]; ok {
				baseMap[k] = deepMergeValues(existing, v)
			} else {
				baseMap[k] = v
			}
		}
		return baseMap
	}
	return override
}

func marshalProfile(profile map[string]interface{}) string {
	result, err := json.Marshal(profile)
	if err != nil {
		return "{}"
	}
	return string(result)
}

func removeResolvedAmbiguities(profileJSON string, overridesJSONs []string) string {
	var profile map[string]interface{}
	if err := json.Unmarshal([]byte(profileJSON), &profile); err != nil {
		return profileJSON
	}
	ambiguitiesRaw, ok := profile["ambiguities"]
	if !ok {
		return profileJSON
	}
	ambiguities, ok := ambiguitiesRaw.([]interface{})
	if !ok || len(ambiguities) == 0 {
		return profileJSON
	}

	var remaining []interface{}
	for _, ambRaw := range ambiguities {
		amb, ok := ambRaw.(map[string]interface{})
		if !ok {
			remaining = append(remaining, ambRaw)
			continue
		}
		kind, _ := amb["kind"].(string)
		if ambiguityResolvedByOverrides(kind, ambiguityCandidates(amb), overridesJSONs) {
			continue
		}
		remaining = append(remaining, ambRaw)
	}
	if len(remaining) < len(ambiguities) {
		profile["ambiguities"] = remaining
		result, err := json.Marshal(profile)
		if err != nil {
			return profileJSON
		}
		return string(result)
	}
	return profileJSON
}

func confirmationOverrideJSONs(confirmations []domain.SemanticConfirmation) []string {
	out := make([]string, 0, len(confirmations))
	for i := range confirmations {
		out = append(out, confirmations[i].OverridesJSON)
	}
	return out
}

func profileStatusForJSON(profileJSON string) domain.ProfileStatus {
	var facts ProfiledFacts
	if err := json.Unmarshal([]byte(profileJSON), &facts); err != nil {
		return domain.ProfileStatusProfiled
	}
	if len(facts.Ambiguities) == 0 {
		return domain.ProfileStatusConfirmed
	}
	return domain.ProfileStatusProfiled
}

func overridesResolveAnyAmbiguity(ambiguities []Ambiguity, overridesJSONs []string) bool {
	for _, ambiguity := range ambiguities {
		if ambiguityResolvedByOverrides(ambiguity.Kind, ambiguity.Candidates, overridesJSONs) {
			return true
		}
	}
	return false
}

func ambiguityCandidates(ambiguity map[string]interface{}) []string {
	raw, ok := ambiguity["candidates"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func ambiguityResolvedByOverrides(kind string, candidates []string, overridesJSONs []string) bool {
	for _, raw := range overridesJSONs {
		var overrides map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
			continue
		}
		if ambiguityResolvedByOverrideMap(kind, candidates, overrides) {
			return true
		}
	}
	return false
}

func ambiguityResolvedByOverrideMap(kind string, candidates []string, overrides map[string]interface{}) bool {
	switch kind {
	case "multiple_time_columns":
		return overrideStringMatchesCandidates(overrides, "primary_time_column", candidates) ||
			overrideStringMatchesCandidates(overrides, "confirmed_time_column", candidates)
	case "ambiguous_metrics":
		return overrideMapTouchesCandidates(overrides, "metric_definitions", candidates) ||
			overrideMapTouchesCandidates(overrides, "confirmed_metric_mappings", candidates)
	case "ambiguous_join":
		return overrideStringMatchesCandidates(overrides, "join_key", candidates) ||
			overrideListTouchesCandidates(overrides, "join_keys", candidates) ||
			overrideListTouchesCandidates(overrides, "confirmed_join_candidates", candidates)
	case "ambiguous_units":
		return overrideListTouchesCandidates(overrides, "percentage_columns", candidates) ||
			overrideMapTouchesCandidates(overrides, "unit_annotations", candidates)
	default:
		return false
	}
}

func normalizedOverridesJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

func overrideStringMatchesCandidates(overrides map[string]interface{}, key string, candidates []string) bool {
	value, _ := overrides[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return candidateListContains(candidates, value)
}

func overrideListTouchesCandidates(overrides map[string]interface{}, key string, candidates []string) bool {
	raw, ok := overrides[key].([]interface{})
	if !ok || len(raw) == 0 {
		return false
	}
	for _, item := range raw {
		if value, ok := item.(string); ok && candidateListContains(candidates, value) {
			return true
		}
	}
	return false
}

func overrideMapTouchesCandidates(overrides map[string]interface{}, key string, candidates []string) bool {
	raw, ok := overrides[key].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return false
	}
	for name := range raw {
		if candidateListContains(candidates, name) {
			return true
		}
	}
	return false
}

func candidateListContains(candidates []string, value string) bool {
	value = normalizeAmbiguityCandidate(value)
	if value == "" {
		return false
	}
	for _, candidate := range candidates {
		if normalizeAmbiguityCandidate(candidate) == value {
			return true
		}
	}
	return false
}

func normalizeAmbiguityCandidate(value string) string {
	value = strings.TrimSpace(value)
	if before, _, ok := strings.Cut(value, "("); ok {
		value = strings.TrimSpace(before)
	}
	return strings.ToLower(value)
}

func (s *SourceService) CreatePostgresSource(ctx context.Context, workspaceID, name, createdBy string, conn *domain.DatabaseConnection) (*domain.DataSource, error) {
	ds := &domain.DataSource{
		ID:          "ds_" + uuid.New().String()[:12],
		WorkspaceID: workspaceID,
		Name:        name,
		SourceType:  domain.SourceTypePostgresConnection,
		Status:      domain.SourceStatusActive,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.DataSourceRepo.Create(ctx, ds); err != nil {
		return nil, err
	}
	conn.SourceID = ds.ID
	if err := s.DBConnectionRepo.Create(ctx, conn); err != nil {
		return nil, fmt.Errorf("failed to persist database connection: %w", err)
	}
	log.Printf("Created postgres data source id=%s name=%s", ds.ID, name)
	return ds, nil
}

func ComputeSchemaSignature(schema *data.SchemaInfo) string {
	h := sha256.New()
	h.Write([]byte(schema.TableName + ":"))
	for _, col := range schema.Columns {
		h.Write([]byte(col.Name + ":" + col.Type + ":"))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func inferColumnRole(col data.ColumnInfo) string {
	if col.TimeProfile != nil {
		return "time"
	}
	name := strings.ToLower(col.Name)
	if strings.HasSuffix(name, "_id") || name == "id" {
		return "id"
	}
	if col.Type == "NUMERIC" || col.Type == "INTEGER" || col.Type == "REAL" {
		return "metric"
	}
	return "dimension"
}

var unitPatterns = []struct {
	Pattern string
	Unit    string
	Scale   float64
}{
	{`(?i)amount|revenue|sales|price|cost|fee|pay`, "currency", 1},
	{`(?i)weight|mass|kg|gram`, "mass", 1},
	{`(?i)length|distance|height|width|meter|km`, "length", 1},
	{`(?i)percent|rate|ratio|pct`, "percentage", 1},
}

func inferUnitCandidates(columns []data.ColumnInfo, isEstimated bool) []UnitCandidate {
	var candidates []UnitCandidate
	for _, col := range columns {
		if col.Type != "NUMERIC" && col.Type != "INTEGER" && col.Type != "REAL" {
			continue
		}
		nameLower := strings.ToLower(col.Name)
		for _, p := range unitPatterns {
			matched, _ := regexp.MatchString(p.Pattern, nameLower)
			if matched {
				candidates = append(candidates, UnitCandidate{
					ColumnName:   col.Name,
					DetectedUnit: p.Unit,
					Scale:        p.Scale,
					Estimated:    isEstimated,
				})
				break
			}
		}
	}
	return candidates
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
	SemanticStatus         string    `json:"semantic_status"`
	ProfileID              string    `json:"profile_id,omitempty"`
	AmbiguityCount         int       `json:"ambiguity_count"`
	ConfirmedOverrideCount int       `json:"confirmed_override_count"`
	RowCount               int       `json:"row_count"`
	ColCount               int       `json:"column_count"`
	LastImportedAt         time.Time `json:"last_imported_at"`
	LargeDataset           bool      `json:"large_dataset"`
	RowsImported           int       `json:"rows_imported"`
	RowsSkipped            int       `json:"rows_skipped"`
	ImportDurationMs       int       `json:"import_duration_ms"`
	ProfileDurationMs      int       `json:"profile_duration_ms"`
	SnapshotSizeBytes      int64     `json:"snapshot_size_bytes"`
	ProfileMode            string    `json:"profile_mode"`
	ErrorMessage           string    `json:"error_message,omitempty"`
}

type SemanticProfileSummary struct {
	ProfileID         string `json:"profile_id"`
	SourceID          string `json:"source_id"`
	AnalysisTableName string `json:"analysis_table_name"`
	ProfileStatus     string `json:"profile_status"`
	SchemaSignature   string `json:"schema_signature"`
}
