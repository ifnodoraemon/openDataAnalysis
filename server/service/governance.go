package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type GovernanceInspection struct {
	WorkspaceID        string                    `json:"workspace_id"`
	SessionID          string                    `json:"session_id"`
	SourceCount        int                       `json:"source_count"`
	SemanticAssetCount int                       `json:"semantic_asset_count"`
	IssueCount         int                       `json:"issue_count"`
	SeverityCounts     map[string]int            `json:"severity_counts"`
	Sources            []GovernanceSourceSummary `json:"sources"`
	Issues             []GovernanceIssue         `json:"issues"`
	GeneratedAt        time.Time                 `json:"generated_at"`
}

type GovernanceSourceSummary struct {
	SourceID           string `json:"source_id"`
	SourceObjectKey    string `json:"source_object_key"`
	AnalysisTableName  string `json:"analysis_table_name"`
	SourceType         string `json:"source_type"`
	SnapshotStatus     string `json:"snapshot_status"`
	SemanticStatus     string `json:"semantic_status"`
	ProfileID          string `json:"profile_id,omitempty"`
	SchemaSignature    string `json:"schema_signature,omitempty"`
	RowCount           int    `json:"row_count"`
	ColumnCount        int    `json:"column_count"`
	DataSizeTier       string `json:"data_size_tier"`
	ProfileMode        string `json:"profile_mode"`
	ImportTruncated    bool   `json:"import_truncated"`
	SemanticAssetCount int    `json:"semantic_asset_count"`
}

type GovernanceIssue struct {
	ID                string                 `json:"id"`
	Severity          string                 `json:"severity"`
	Domain            string                 `json:"domain"`
	Kind              string                 `json:"kind"`
	SourceID          string                 `json:"source_id,omitempty"`
	SourceObjectKey   string                 `json:"source_object_key,omitempty"`
	ProfileID         string                 `json:"profile_id,omitempty"`
	AnalysisTableName string                 `json:"analysis_table_name,omitempty"`
	Message           string                 `json:"message"`
	Facts             map[string]interface{} `json:"facts,omitempty"`
}

func (s *SourceService) InspectSessionGovernance(ctx context.Context, workspaceID, sessionID string) (GovernanceInspection, error) {
	inspection := GovernanceInspection{
		WorkspaceID:    workspaceID,
		SessionID:      sessionID,
		SeverityCounts: map[string]int{},
		GeneratedAt:    time.Now(),
	}
	sources, err := s.GetSessionSources(ctx, sessionID)
	if err != nil {
		if len(sources) == 0 {
			return inspection, err
		}
		inspection.addIssue("high", "source", "session_sources_partial_error", SessionSourceSummary{}, map[string]interface{}{
			"error": err.Error(),
		}, "some session source facts could not be loaded")
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
			SemanticStatus:    source.SemanticStatus,
			ProfileID:         source.ProfileID,
			RowCount:          source.RowCount,
			ColumnCount:       source.ColCount,
			DataSizeTier:      source.DataSizeTier,
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
						summary.SemanticAssetCount = len(assets)
						for _, asset := range assets {
							key := semanticAssetIdentity(asset)
							if key == "" || seenAssets[key] {
								continue
							}
							seenAssets[key] = true
							inspection.SemanticAssetCount++
						}
					} else {
						inspection.addIssue("medium", "semantic", "semantic_asset_lookup_failed", source, map[string]interface{}{
							"profile_id":       source.ProfileID,
							"schema_signature": profile.SchemaSignature,
							"error":            assetErr.Error(),
						}, fmt.Sprintf("source %s semantic assets could not be loaded", source.AnalysisTableName))
					}
				}
				inspection.addProfileWarnings(source, profile.ProfileJSON)
			} else if err != nil {
				inspection.addIssue("high", "semantic", "semantic_profile_lookup_failed", source, map[string]interface{}{
					"profile_id": source.ProfileID,
					"error":      err.Error(),
				}, fmt.Sprintf("source %s semantic profile could not be loaded", source.AnalysisTableName))
			}
		}
		inspection.Sources = append(inspection.Sources, summary)
		inspection.addSourceIssues(source, summary.SemanticAssetCount)
	}
	inspection.IssueCount = len(inspection.Issues)
	return inspection, nil
}

func semanticAssetIdentity(asset domain.SemanticAsset) string {
	if strings.TrimSpace(asset.ID) != "" {
		return asset.ID
	}
	parts := []string{
		strings.TrimSpace(asset.WorkspaceID),
		strings.TrimSpace(asset.SchemaSignature),
		string(asset.AssetKind),
		strings.TrimSpace(asset.AssetKey),
	}
	if strings.Join(parts, "") == "" {
		return ""
	}
	return strings.Join(parts, "\x00")
}

func (g *GovernanceInspection) addSourceIssues(source SessionSourceSummary, semanticAssetCount int) {
	if strings.TrimSpace(source.SnapshotStatus) != "" && source.SnapshotStatus != "ready" {
		g.addIssue("high", "source", "snapshot_not_ready", source, map[string]interface{}{
			"snapshot_status": source.SnapshotStatus,
			"error_message":   source.ErrorMessage,
		}, fmt.Sprintf("source %s snapshot status is %s", source.AnalysisTableName, source.SnapshotStatus))
	}
	if strings.TrimSpace(source.ErrorMessage) != "" {
		g.addIssue("high", "source", "snapshot_error", source, map[string]interface{}{
			"error_message": source.ErrorMessage,
		}, fmt.Sprintf("source %s has snapshot error", source.AnalysisTableName))
	}
	if source.ImportTruncated {
		g.addIssue("high", "coverage", "import_truncated", source, map[string]interface{}{
			"rows_imported":    source.RowsImported,
			"import_row_limit": source.ImportRowLimit,
		}, fmt.Sprintf("source %s import was truncated", source.AnalysisTableName))
	}
	if source.ProfileID == "" || source.SemanticStatus == "pending" {
		g.addIssue("medium", "semantic", "semantic_profile_missing", source, nil, fmt.Sprintf("source %s has no semantic profile", source.AnalysisTableName))
	}
	if source.AmbiguityCount > 0 {
		severity := "medium"
		if source.AmbiguityCount >= 3 {
			severity = "high"
		}
		g.addIssue(severity, "semantic", "semantic_ambiguity", source, map[string]interface{}{
			"ambiguity_count": source.AmbiguityCount,
		}, fmt.Sprintf("source %s has unresolved semantic ambiguities", source.AnalysisTableName))
	}
	if source.ProfileMode == stringProfileModeSampled && source.RowCount > 0 {
		g.addIssue("low", "coverage", "profile_sampled", source, map[string]interface{}{
			"row_count":      source.RowCount,
			"profile_mode":   source.ProfileMode,
			"data_size_tier": source.DataSizeTier,
		}, fmt.Sprintf("source %s profile is sampled", source.AnalysisTableName))
	}
}

const stringProfileModeSampled = "sampled"

func (g *GovernanceInspection) addProfileWarnings(source SessionSourceSummary, profileJSON string) {
	var facts ProfiledFacts
	if err := json.Unmarshal([]byte(profileJSON), &facts); err != nil {
		g.addIssue("medium", "semantic", "profile_json_unreadable", source, map[string]interface{}{
			"error": err.Error(),
		}, fmt.Sprintf("source %s profile json is unreadable", source.AnalysisTableName))
		return
	}
	for i, warning := range facts.Warnings {
		severity := "low"
		if strings.Contains(strings.ToLower(warning), "capped") || strings.Contains(strings.ToLower(warning), "truncated") {
			severity = "high"
		} else if strings.Contains(strings.ToLower(warning), "sampled") || strings.Contains(strings.ToLower(warning), "estimated") {
			severity = "medium"
		}
		g.addIssue(severity, "profile", "profile_warning", source, map[string]interface{}{
			"warning": warning,
			"index":   i,
		}, fmt.Sprintf("source %s profile warning: %s", source.AnalysisTableName, warning))
	}
}

func (g *GovernanceInspection) addIssue(severity, domainName, kind string, source SessionSourceSummary, facts map[string]interface{}, message string) {
	if g.SeverityCounts == nil {
		g.SeverityCounts = map[string]int{}
	}
	g.SeverityCounts[severity]++
	issueNumber := len(g.Issues) + 1
	subject := issueSubject(source)
	g.Issues = append(g.Issues, GovernanceIssue{
		ID:                fmt.Sprintf("%s:%s:%d", subject, kind, issueNumber),
		Severity:          severity,
		Domain:            domainName,
		Kind:              kind,
		SourceID:          source.SourceID,
		SourceObjectKey:   source.SourceObjectKey,
		ProfileID:         source.ProfileID,
		AnalysisTableName: source.AnalysisTableName,
		Message:           message,
		Facts:             facts,
	})
}

func issueSubject(source SessionSourceSummary) string {
	parts := []string{}
	if strings.TrimSpace(source.SourceID) != "" {
		parts = append(parts, strings.TrimSpace(source.SourceID))
	}
	if strings.TrimSpace(source.SourceObjectKey) != "" {
		parts = append(parts, strings.TrimSpace(source.SourceObjectKey))
	}
	if len(parts) == 0 {
		return "session"
	}
	return strings.Join(parts, ":")
}
