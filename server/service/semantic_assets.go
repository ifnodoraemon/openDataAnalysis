package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func (s *SourceService) semanticAssetConfirmation(ctx context.Context, workspaceID, schemaSignature string) *domain.SemanticConfirmation {
	if s.SemanticAssetRepo == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(schemaSignature) == "" {
		return nil
	}
	assets, err := s.SemanticAssetRepo.ListBySchema(ctx, workspaceID, schemaSignature)
	if err != nil {
		log.Printf("semanticAssetConfirmation: ListBySchema failed workspace_id=%s signature=%s err=%v", workspaceID, schemaSignature, err)
		return nil
	}
	if len(assets) == 0 {
		return nil
	}
	overridesJSON := semanticAssetsToOverridesJSON(assets)
	if overridesJSON == "{}" {
		return nil
	}
	return &domain.SemanticConfirmation{
		WorkspaceID:   workspaceID,
		Scope:         domain.ConfirmationScopeWorkspace,
		OverridesJSON: overridesJSON,
		CreatedAt:     time.Now(),
	}
}

func (s *SourceService) upsertSemanticAssetsFromConfirmation(ctx context.Context, profile *domain.SemanticProfile, confirmation *domain.SemanticConfirmation) error {
	if s.SemanticAssetRepo == nil || profile == nil || confirmation == nil {
		return nil
	}
	assets, err := semanticAssetsFromConfirmation(profile, confirmation)
	if err != nil {
		return err
	}
	for i := range assets {
		if err := s.SemanticAssetRepo.Upsert(ctx, &assets[i]); err != nil {
			return fmt.Errorf("upsert semantic asset %s/%s: %w", assets[i].AssetKind, assets[i].AssetKey, err)
		}
		s.recordAudit(ctx, domain.AuditEvent{
			WorkspaceID:  assets[i].WorkspaceID,
			SessionID:    confirmation.SessionID,
			ActorUserID:  confirmation.ConfirmedBy,
			EventType:    "semantic_asset_upserted",
			ResourceType: "semantic_asset",
			ResourceID:   string(assets[i].AssetKind) + ":" + assets[i].AssetKey,
			PayloadJSON: auditPayloadJSON(map[string]interface{}{
				"asset_kind":       string(assets[i].AssetKind),
				"asset_key":        assets[i].AssetKey,
				"asset_id":         assets[i].ID,
				"schema_signature": assets[i].SchemaSignature,
				"profile_id":       profile.ID,
				"confirmation_id":  confirmation.ID,
			}),
		})
	}
	return nil
}

func semanticAssetsFromConfirmation(profile *domain.SemanticProfile, confirmation *domain.SemanticConfirmation) ([]domain.SemanticAsset, error) {
	var overrides map[string]interface{}
	if err := json.Unmarshal([]byte(normalizedOverridesJSON(confirmation.OverridesJSON)), &overrides); err != nil {
		return nil, err
	}
	now := time.Now()
	var assets []domain.SemanticAsset
	addAsset := func(kind domain.SemanticAssetKind, key string, value map[string]interface{}) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return
		}
		assets = append(assets, domain.SemanticAsset{
			ID:                        semanticAssetID(confirmation.WorkspaceID, profile.SchemaSignature, kind, key),
			WorkspaceID:               confirmation.WorkspaceID,
			SourceID:                  profile.SourceID,
			SchemaSignature:           profile.SchemaSignature,
			AssetKind:                 kind,
			AssetKey:                  key,
			AssetValueJSON:            string(valueJSON),
			CreatedFromProfileID:      profile.ID,
			CreatedFromConfirmationID: confirmation.ID,
			CreatedBy:                 confirmation.ConfirmedBy,
			CreatedAt:                 now,
			UpdatedAt:                 now,
		})
	}

	if value, ok := stringOverride(overrides, "primary_time_column"); ok {
		addAsset(domain.SemanticAssetKindTimeColumn, "primary_time_column", map[string]interface{}{"column_name": value})
	}
	if value, ok := stringOverride(overrides, "confirmed_time_column"); ok {
		addAsset(domain.SemanticAssetKindTimeColumn, "primary_time_column", map[string]interface{}{"column_name": value})
	}
	if defs, ok := mapStringOverride(overrides, "metric_definitions"); ok {
		keys := sortedKeys(defs)
		for _, col := range keys {
			addAsset(domain.SemanticAssetKindMetricDefinition, "metric:"+col, map[string]interface{}{
				"column_name": col,
				"definition":  defs[col],
			})
		}
	}
	if defs, ok := mapStringOverride(overrides, "confirmed_metric_mappings"); ok {
		keys := sortedKeys(defs)
		for _, col := range keys {
			addAsset(domain.SemanticAssetKindMetricDefinition, "metric:"+col, map[string]interface{}{
				"column_name": col,
				"definition":  defs[col],
			})
		}
	}
	if cols, ok := stringListOverride(overrides, "percentage_columns"); ok {
		sort.Strings(cols)
		for _, col := range cols {
			addAsset(domain.SemanticAssetKindUnitAnnotation, "unit:"+col, map[string]interface{}{
				"column_name": col,
				"unit":        "percentage",
			})
		}
	}
	if units, ok := mapStringOverride(overrides, "unit_annotations"); ok {
		keys := sortedKeys(units)
		for _, col := range keys {
			addAsset(domain.SemanticAssetKindUnitAnnotation, "unit:"+col, map[string]interface{}{
				"column_name": col,
				"unit":        units[col],
			})
		}
	}
	if joins, ok := stringListOverride(overrides, "confirmed_join_candidates"); ok {
		sort.Strings(joins)
		for _, join := range joins {
			addAsset(domain.SemanticAssetKindJoinCandidate, "join:"+join, map[string]interface{}{"candidate": join})
		}
	}
	if joins, ok := stringListOverride(overrides, "join_keys"); ok {
		sort.Strings(joins)
		for _, join := range joins {
			addAsset(domain.SemanticAssetKindJoinCandidate, "join:"+join, map[string]interface{}{"candidate": join})
		}
	}
	if value, ok := stringOverride(overrides, "join_key"); ok {
		addAsset(domain.SemanticAssetKindJoinCandidate, "join:"+value, map[string]interface{}{"candidate": value})
	}

	known := map[string]bool{
		"primary_time_column":       true,
		"confirmed_time_column":     true,
		"metric_definitions":        true,
		"confirmed_metric_mappings": true,
		"percentage_columns":        true,
		"unit_annotations":          true,
		"confirmed_join_candidates": true,
		"join_keys":                 true,
		"join_key":                  true,
	}
	for _, key := range sortedInterfaceKeys(overrides) {
		if known[key] {
			continue
		}
		addAsset(domain.SemanticAssetKindOverride, "override:"+key, map[string]interface{}{key: overrides[key]})
	}
	return assets, nil
}

func semanticAssetID(workspaceID, schemaSignature string, kind domain.SemanticAssetKind, key string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{workspaceID, schemaSignature, string(kind), key}, "\x00")))
	return "sa_" + fmt.Sprintf("%x", sum[:])[:20]
}

func semanticAssetsToOverridesJSON(assets []domain.SemanticAsset) string {
	overrides := map[string]interface{}{}
	metricDefinitions := map[string]string{}
	unitAnnotations := map[string]string{}
	var percentageColumns []string
	var joins []string

	for _, asset := range assets {
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(asset.AssetValueJSON), &value); err != nil {
			continue
		}
		switch asset.AssetKind {
		case domain.SemanticAssetKindTimeColumn:
			if col, ok := value["column_name"].(string); ok && strings.TrimSpace(col) != "" {
				overrides["primary_time_column"] = strings.TrimSpace(col)
			}
		case domain.SemanticAssetKindMetricDefinition:
			col, _ := value["column_name"].(string)
			def, _ := value["definition"].(string)
			if strings.TrimSpace(col) != "" && strings.TrimSpace(def) != "" {
				metricDefinitions[strings.TrimSpace(col)] = strings.TrimSpace(def)
			}
		case domain.SemanticAssetKindUnitAnnotation:
			col, _ := value["column_name"].(string)
			unit, _ := value["unit"].(string)
			col = strings.TrimSpace(col)
			unit = strings.TrimSpace(unit)
			if col == "" || unit == "" {
				continue
			}
			unitAnnotations[col] = unit
			if unit == "percentage" {
				percentageColumns = append(percentageColumns, col)
			}
		case domain.SemanticAssetKindJoinCandidate:
			if candidate, ok := value["candidate"].(string); ok && strings.TrimSpace(candidate) != "" {
				joins = append(joins, strings.TrimSpace(candidate))
			}
		case domain.SemanticAssetKindOverride:
			for key, raw := range value {
				if strings.TrimSpace(key) != "" {
					overrides[key] = raw
				}
			}
		}
	}
	if len(metricDefinitions) > 0 {
		overrides["metric_definitions"] = metricDefinitions
	}
	if len(unitAnnotations) > 0 {
		overrides["unit_annotations"] = unitAnnotations
	}
	if len(percentageColumns) > 0 {
		sort.Strings(percentageColumns)
		overrides["percentage_columns"] = uniqueStrings(percentageColumns)
	}
	if len(joins) > 0 {
		sort.Strings(joins)
		overrides["confirmed_join_candidates"] = uniqueStrings(joins)
	}
	if len(overrides) == 0 {
		return "{}"
	}
	out, err := json.Marshal(overrides)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func stringOverride(overrides map[string]interface{}, key string) (string, bool) {
	value, ok := overrides[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func stringListOverride(overrides map[string]interface{}, key string) ([]string, bool) {
	raw, ok := overrides[key].([]interface{})
	if !ok {
		return nil, false
	}
	var values []string
	for _, item := range raw {
		value, ok := item.(string)
		value = strings.TrimSpace(value)
		if ok && value != "" {
			values = append(values, value)
		}
	}
	return uniqueStrings(values), len(values) > 0
}

func mapStringOverride(overrides map[string]interface{}, key string) (map[string]string, bool) {
	raw, ok := overrides[key].(map[string]interface{})
	if !ok {
		return nil, false
	}
	values := map[string]string{}
	for k, v := range raw {
		value, ok := v.(string)
		key := strings.TrimSpace(k)
		value = strings.TrimSpace(value)
		if ok && key != "" && value != "" {
			values[key] = value
		}
	}
	return values, len(values) > 0
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedInterfaceKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func auditPayloadJSON(payload map[string]interface{}) string {
	out, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func (s *SourceService) recordAudit(ctx context.Context, event domain.AuditEvent) {
	if s.AuditEventRepo == nil || strings.TrimSpace(event.WorkspaceID) == "" || strings.TrimSpace(event.EventType) == "" {
		return
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = "ae_" + uuid.New().String()[:12]
	}
	if strings.TrimSpace(event.PayloadJSON) == "" {
		event.PayloadJSON = "{}"
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if err := s.AuditEventRepo.Create(ctx, &event); err != nil {
		log.Printf("recordAudit: create failed event_type=%s resource=%s/%s err=%v", event.EventType, event.ResourceType, event.ResourceID, err)
	}
}

func (s *SourceService) RecordAuditEvent(ctx context.Context, event domain.AuditEvent) {
	s.recordAudit(ctx, event)
}

func (s *SourceService) GetSemanticAssets(ctx context.Context, workspaceID, schemaSignature string) ([]domain.SemanticAsset, error) {
	if s.SemanticAssetRepo == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(schemaSignature) == "" {
		return nil, nil
	}
	return s.SemanticAssetRepo.ListBySchema(ctx, workspaceID, schemaSignature)
}
