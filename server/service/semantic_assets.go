package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func (s *SourceService) upsertSemanticAssetsFromConfirmation(ctx context.Context, profile *domain.SemanticProfile, confirmation *domain.SemanticConfirmation) ([]string, error) {
	if s.SemanticAssetRepo == nil {
		return nil, fmt.Errorf("semantic asset repository is not configured")
	}
	if profile == nil || confirmation == nil {
		return nil, fmt.Errorf("profile and confirmation are required")
	}
	assets, err := semanticAssetsFromConfirmation(profile, confirmation)
	if err != nil {
		return nil, err
	}
	var auditErrors []string
	for i := range assets {
		if err := s.SemanticAssetRepo.Upsert(ctx, &assets[i]); err != nil {
			return auditErrors, fmt.Errorf("upsert semantic asset %s/%s: %w", assets[i].AssetKind, assets[i].AssetKey, err)
		}
		payloadJSON, err := auditPayloadJSON(map[string]interface{}{
			"asset_kind":       string(assets[i].AssetKind),
			"asset_key":        assets[i].AssetKey,
			"asset_id":         assets[i].ID,
			"schema_signature": assets[i].SchemaSignature,
			"profile_id":       profile.ID,
			"confirmation_id":  confirmation.ID,
		})
		if err != nil {
			return auditErrors, fmt.Errorf("serialize semantic asset audit facts: %w", err)
		}
		if err := s.recordAudit(ctx, domain.AuditEvent{
			WorkspaceID:  assets[i].WorkspaceID,
			SessionID:    confirmation.SessionID,
			ActorUserID:  confirmation.ConfirmedBy,
			EventType:    "semantic_asset_upserted",
			ResourceType: "semantic_asset",
			ResourceID:   string(assets[i].AssetKind) + ":" + assets[i].AssetKey,
			PayloadJSON:  payloadJSON,
		}); err != nil {
			auditErrors = append(auditErrors, fmt.Sprintf("semantic asset %s/%s: %v", assets[i].AssetKind, assets[i].AssetKey, err))
		}
	}
	return auditErrors, nil
}

func semanticAssetsFromConfirmation(profile *domain.SemanticProfile, confirmation *domain.SemanticConfirmation) ([]domain.SemanticAsset, error) {
	var patch map[string]json.RawMessage
	if err := decodeStrictJSON([]byte(confirmation.OverridesJSON), &patch); err != nil {
		return nil, err
	}
	if patch == nil {
		return nil, fmt.Errorf("confirmation patch must be a JSON object")
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("serialize confirmation patch: %w", err)
	}
	if confirmation.ID == "" || confirmation.ID != strings.TrimSpace(confirmation.ID) {
		return nil, fmt.Errorf("confirmation ID must be a non-empty exact value")
	}
	now := time.Now()
	return []domain.SemanticAsset{{
		ID:                        semanticAssetID(confirmation.WorkspaceID, profile.SchemaSignature, domain.SemanticAssetKindPatch, confirmation.ID),
		WorkspaceID:               confirmation.WorkspaceID,
		SourceID:                  profile.SourceID,
		SchemaSignature:           profile.SchemaSignature,
		AssetKind:                 domain.SemanticAssetKindPatch,
		AssetKey:                  confirmation.ID,
		AssetValueJSON:            string(patchJSON),
		CreatedFromProfileID:      profile.ID,
		CreatedFromConfirmationID: confirmation.ID,
		CreatedBy:                 confirmation.ConfirmedBy,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}}, nil
}

func semanticAssetID(workspaceID, schemaSignature string, kind domain.SemanticAssetKind, key string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{workspaceID, schemaSignature, string(kind), key}, "\x00")))
	return "sa_" + fmt.Sprintf("%x", sum[:])[:20]
}

func auditPayloadJSON(payload map[string]interface{}) (string, error) {
	out, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (s *SourceService) recordAudit(ctx context.Context, event domain.AuditEvent) error {
	if s.AuditEventRepo == nil || strings.TrimSpace(event.WorkspaceID) == "" || strings.TrimSpace(event.EventType) == "" {
		return fmt.Errorf("audit repository, workspace_id, and event_type are required")
	}
	for field, value := range map[string]string{
		"workspace_id":  event.WorkspaceID,
		"session_id":    event.SessionID,
		"run_id":        event.RunID,
		"actor_user_id": event.ActorUserID,
		"event_type":    event.EventType,
		"resource_type": event.ResourceType,
		"resource_id":   event.ResourceID,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("audit %s must be an exact value", field)
		}
	}
	if event.PayloadJSON == "" || event.PayloadJSON != strings.TrimSpace(event.PayloadJSON) {
		return fmt.Errorf("audit payload_json must be a non-empty exact JSON object")
	}
	var payload map[string]json.RawMessage
	if err := decodeStrictJSON([]byte(event.PayloadJSON), &payload); err != nil {
		return fmt.Errorf("audit payload_json must be a strict JSON object: %w", err)
	}
	if payload == nil {
		return fmt.Errorf("audit payload_json must be a JSON object")
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = "ae_" + uuid.New().String()[:12]
	} else if event.ID != strings.TrimSpace(event.ID) {
		return fmt.Errorf("audit ID must be an exact value")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	return s.AuditEventRepo.Create(ctx, &event)
}

func (s *SourceService) RecordAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	return s.recordAudit(ctx, event)
}

func (s *SourceService) GetSemanticAssets(ctx context.Context, workspaceID, schemaSignature string) ([]domain.SemanticAsset, error) {
	if s.SemanticAssetRepo == nil {
		return nil, fmt.Errorf("semantic asset repository is not configured")
	}
	if workspaceID == "" || workspaceID != strings.TrimSpace(workspaceID) || schemaSignature == "" || schemaSignature != strings.TrimSpace(schemaSignature) {
		return nil, fmt.Errorf("workspace ID and schema signature must be non-empty exact values")
	}
	return s.SemanticAssetRepo.ListBySchema(ctx, workspaceID, schemaSignature)
}
