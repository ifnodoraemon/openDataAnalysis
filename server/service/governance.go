package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type GovernanceInspection struct {
	WorkspaceID        string                       `json:"workspace_id"`
	SessionID          string                       `json:"session_id"`
	SourceCount        int                          `json:"source_count"`
	SemanticAssetCount int                          `json:"semantic_asset_count"`
	Sources            []GovernanceSourceSummary    `json:"sources"`
	ProfileWarnings    []GovernanceProfileWarning   `json:"profile_warnings"`
	ObservationErrors  []GovernanceObservationError `json:"observation_errors"`
	GeneratedAt        time.Time                    `json:"generated_at"`
}

type GovernanceSourceSummary struct {
	SourceID           string `json:"source_id"`
	SourceObjectKey    string `json:"source_object_key"`
	AnalysisTableName  string `json:"analysis_table_name"`
	SourceType         string `json:"source_type"`
	SnapshotStatus     string `json:"snapshot_status"`
	ProfileStatus      string `json:"profile_status"`
	ProfileID          string `json:"profile_id,omitempty"`
	SchemaSignature    string `json:"schema_signature,omitempty"`
	RowCount           int    `json:"row_count"`
	ColumnCount        int    `json:"column_count"`
	ProfileMode        string `json:"profile_mode"`
	ImportTruncated    bool   `json:"import_truncated"`
	SemanticAssetCount int    `json:"semantic_asset_count"`
}

type GovernanceProfileWarning struct {
	SourceID  string `json:"source_id"`
	ProfileID string `json:"profile_id"`
	Index     int    `json:"index"`
	Warning   string `json:"warning"`
}

type GovernanceObservationError struct {
	Operation string `json:"operation"`
	SourceID  string `json:"source_id,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	Detail    string `json:"detail"`
}

func (s *SourceService) InspectSessionGovernance(ctx context.Context, workspaceID, sessionID string) (GovernanceInspection, error) {
	inspection := GovernanceInspection{
		WorkspaceID: workspaceID,
		SessionID:   sessionID,
		GeneratedAt: time.Now(),
	}
	sources, err := s.GetSessionSources(ctx, sessionID)
	if err != nil {
		if len(sources) == 0 {
			return inspection, err
		}
		inspection.ObservationErrors = append(inspection.ObservationErrors, GovernanceObservationError{
			Operation: "load_session_sources",
			Detail:    err.Error(),
		})
	}
	inspection.SourceCount = len(sources)
	seenAssets := map[string]bool{}
	for _, source := range sources {
		summary := GovernanceSourceSummary{
			SourceID:          source.SourceID,
			SourceObjectKey:   source.SourceObjectKey,
			AnalysisTableName: source.AnalysisTableName,
			SourceType:        source.SourceType,
			SnapshotStatus:    source.SnapshotStatus,
			ProfileStatus:     source.ProfileStatus,
			ProfileID:         source.ProfileID,
			RowCount:          source.RowCount,
			ColumnCount:       source.ColCount,
			ProfileMode:       source.ProfileMode,
			ImportTruncated:   source.ImportTruncated,
		}
		if source.ProfileID != "" {
			profile, _, err := s.GetProfileDetail(ctx, source.ProfileID)
			if err == nil && profile != nil {
				summary.SchemaSignature = profile.SchemaSignature
				if s.SemanticAssetRepo != nil {
					assets, assetErr := s.SemanticAssetRepo.ListBySchema(ctx, workspaceID, profile.SchemaSignature)
					if assetErr == nil {
						for _, asset := range assets {
							if asset.ID == "" || asset.ID != strings.TrimSpace(asset.ID) {
								inspection.ObservationErrors = append(inspection.ObservationErrors, GovernanceObservationError{
									Operation: "validate_semantic_asset",
									SourceID:  source.SourceID,
									ProfileID: source.ProfileID,
									Detail:    "semantic asset repository returned a record without an exact ID",
								})
								continue
							}
							summary.SemanticAssetCount++
							if seenAssets[asset.ID] {
								continue
							}
							seenAssets[asset.ID] = true
							inspection.SemanticAssetCount++
						}
					} else {
						inspection.ObservationErrors = append(inspection.ObservationErrors, GovernanceObservationError{
							Operation: "load_semantic_assets",
							SourceID:  source.SourceID,
							ProfileID: source.ProfileID,
							Detail:    assetErr.Error(),
						})
					}
				} else {
					inspection.ObservationErrors = append(inspection.ObservationErrors, GovernanceObservationError{
						Operation: "load_semantic_assets",
						SourceID:  source.SourceID,
						ProfileID: source.ProfileID,
						Detail:    "semantic asset repository is not configured",
					})
				}
				inspection.addProfileFacts(source, profile.ProfileJSON)
			} else if err != nil {
				inspection.ObservationErrors = append(inspection.ObservationErrors, GovernanceObservationError{
					Operation: "load_semantic_profile",
					SourceID:  source.SourceID,
					ProfileID: source.ProfileID,
					Detail:    err.Error(),
				})
			} else {
				inspection.ObservationErrors = append(inspection.ObservationErrors, GovernanceObservationError{
					Operation: "load_semantic_profile",
					SourceID:  source.SourceID,
					ProfileID: source.ProfileID,
					Detail:    "profile repository returned an empty record",
				})
			}
		}
		inspection.Sources = append(inspection.Sources, summary)
	}
	return inspection, nil
}

func (g *GovernanceInspection) addProfileFacts(source SessionSourceSummary, profileJSON string) {
	var facts ProfiledFacts
	if err := json.Unmarshal([]byte(profileJSON), &facts); err != nil {
		g.ObservationErrors = append(g.ObservationErrors, GovernanceObservationError{
			Operation: "parse_semantic_profile",
			SourceID:  source.SourceID,
			ProfileID: source.ProfileID,
			Detail:    err.Error(),
		})
		return
	}
	for i, warning := range facts.Warnings {
		g.ProfileWarnings = append(g.ProfileWarnings, GovernanceProfileWarning{
			SourceID:  source.SourceID,
			ProfileID: source.ProfileID,
			Index:     i,
			Warning:   warning,
		})
	}
}
