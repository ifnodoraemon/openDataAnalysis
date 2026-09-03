package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/service"
)

type SessionSourcesProvider func(context.Context) ([]service.SessionSourceSummary, error)
type PendingFileSourcesProvider func(context.Context) ([]service.PendingFileSource, error)
type ProfileDetailProvider func(context.Context, string) (profileJSON string, confirmationsJSON string, reusablePatchesJSON string, err error)
type ProfileConfirmer func(context.Context, string, string, string, string) ([]string, error)
type GovernanceProvider func(context.Context) (service.GovernanceInspection, error)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.SessionSourcesProvider == nil {
			return nil
		}
		return &InspectSessionSourcesTool{Provider: ctx.SessionSourcesProvider, PendingProvider: ctx.PendingFileSourcesProvider}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.ProfileDetailProvider == nil {
			return nil
		}
		return &InspectSemanticProfileTool{Provider: ctx.ProfileDetailProvider}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.ProfileConfirmer == nil {
			return nil
		}
		return &ConfirmSourceProfileTool{
			Confirmer:   ctx.ProfileConfirmer,
			SessionID:   ctx.SessionID,
			WorkspaceID: ctx.WorkspaceID,
		}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.GovernanceProvider == nil {
			return nil
		}
		return &InspectGovernanceTool{Provider: ctx.GovernanceProvider}
	})
}

type InspectSessionSourcesTool struct {
	Provider         SessionSourcesProvider
	PendingProvider  PendingFileSourcesProvider
	parentCtx        context.Context
}

func (t *InspectSessionSourcesTool) SetExecutionContext(ctx context.Context) { t.parentCtx = ctx }

func (t *InspectSessionSourcesTool) Name() string { return "state_session_sources_inspect" }
func (t *InspectSessionSourcesTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *InspectSessionSourcesTool) Description() string {
	return "Read current session data-source and snapshot facts, including source type, analysis table, observed size, import mode, profile status, and user patch count. Also lists pending_file_uploads: uploaded workspace files not yet imported into this session (multi-sheet workbooks awaiting selection, or files whose structure the strict importer rejected). To use a pending file, read its original bytes with code_run_python (source_file input) and import cleaned data via data_import_artifact. It does not modify state."
}
func (t *InspectSessionSourcesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectSessionSourcesTool) Execute(args json.RawMessage) (string, error) {
	if err := ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Provider == nil {
		return "", fmt.Errorf("session sources provider is not initialized")
	}
	if t.parentCtx == nil {
		return "", fmt.Errorf("tool execution context is not initialized")
	}
	sources, err := t.Provider(t.parentCtx)
	if err != nil {
		return "", err
	}
	var pending []map[string]string
	if t.PendingProvider != nil {
		if pendingFiles, pendErr := t.PendingProvider(t.parentCtx); pendErr == nil {
			for _, file := range pendingFiles {
				pending = append(pending, map[string]string{
					"source_id":    file.SourceID,
					"display_name": file.DisplayName,
				})
			}
		}
	}
	payload := map[string]interface{}{
		"source_count": len(sources),
		"sources":      sources,
		"ui_summary":   fmt.Sprintf("当前会话包含 %d 个数据源。", len(sources)),
	}
	if len(pending) > 0 {
		payload["pending_file_uploads"] = pending
		payload["ui_summary"] = fmt.Sprintf("当前会话包含 %d 个数据源，另有 %d 个待导入的上传文件。", len(sources), len(pending))
	}
	return toolSuccess("state_session_sources_inspect", payload), nil
}

type InspectSemanticProfileTool struct {
	Provider  ProfileDetailProvider
	parentCtx context.Context
}

func (t *InspectSemanticProfileTool) SetExecutionContext(ctx context.Context) { t.parentCtx = ctx }

func (t *InspectSemanticProfileTool) Name() string { return "state_semantic_profile_inspect" }
func (t *InspectSemanticProfileTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *InspectSemanticProfileTool) Description() string {
	return "Read a stored structural profile, its confirmation records, and reusable workspace patches with the same schema signature. Reusable patches are exposed as facts and are not auto-applied. The tool does not modify state or select an interpretation."
}
func (t *InspectSemanticProfileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"profile_id":{"type":"string","description":"The semantic profile ID to inspect"}},"required":["profile_id"]}`)
}

func (t *InspectSemanticProfileTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		ProfileID string `json:"profile_id"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Provider == nil {
		return "", fmt.Errorf("profile detail provider is not initialized")
	}
	if t.parentCtx == nil {
		return "", fmt.Errorf("tool execution context is not initialized")
	}
	profileJSON, confirmationsJSON, reusablePatchesJSON, err := t.Provider(t.parentCtx, params.ProfileID)
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"profile_id":       params.ProfileID,
		"profile_json":     json.RawMessage(profileJSON),
		"confirmations":    json.RawMessage(confirmationsJSON),
		"reusable_patches": json.RawMessage(reusablePatchesJSON),
	}
	return toolSuccess("state_semantic_profile_inspect", payload), nil
}

type InspectGovernanceTool struct {
	Provider  GovernanceProvider
	parentCtx context.Context
}

func (t *InspectGovernanceTool) SetExecutionContext(ctx context.Context) { t.parentCtx = ctx }

func (t *InspectGovernanceTool) Name() string { return "state_governance_inspect" }
func (t *InspectGovernanceTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *InspectGovernanceTool) Description() string {
	return "Read current session source, snapshot, import, profile, warning, reusable-patch, and observation-error facts. It returns no severity ranking, issue classification, or follow-up action."
}
func (t *InspectGovernanceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectGovernanceTool) Execute(args json.RawMessage) (string, error) {
	if err := ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Provider == nil {
		return "", fmt.Errorf("governance provider is not initialized")
	}
	if t.parentCtx == nil {
		return "", fmt.Errorf("tool execution context is not initialized")
	}
	inspection, err := t.Provider(t.parentCtx)
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"source_count":         inspection.SourceCount,
		"semantic_asset_count": inspection.SemanticAssetCount,
		"sources":              inspection.Sources,
		"profile_warnings":     inspection.ProfileWarnings,
		"observation_errors":   inspection.ObservationErrors,
		"generated_at":         inspection.GeneratedAt,
		"ui_summary":           fmt.Sprintf("已检查 %d 条数据源事实。", inspection.SourceCount),
	}
	return toolSuccess("state_governance_inspect", payload), nil
}

type ConfirmSourceProfileTool struct {
	Confirmer   ProfileConfirmer
	SessionID   string
	WorkspaceID string
	parentCtx   context.Context
}

func (t *ConfirmSourceProfileTool) SetExecutionContext(ctx context.Context) { t.parentCtx = ctx }

func (t *ConfirmSourceProfileTool) Name() string { return "profile_patch_commit" }
func (t *ConfirmSourceProfileTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, RequiresUserReceipt: true}
}
func (t *ConfirmSourceProfileTool) Description() string {
	return "Commit an exact user-authorized patch for one stored profile. Requires a single-use authorization receipt bound to action=profile_patch_commit, this profile_id, and the canonical scope/patch payload. Writes actor and receipt provenance; it does not modify source data or infer additional fields."
}
func (t *ConfirmSourceProfileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
				"profile_id": {"type": "string", "description": "The semantic profile ID to confirm"},
				"scope": {"type": "string", "enum": ["session", "workspace"], "description": "Scope of the confirmation. Use 'session' for session-level overrides."},
				"overrides_json": {"type": "string", "description": "JSON object containing exactly the authorized profile patch."},
				"confirmation_receipt_id": {"type": "string", "description": "Single-use authorization receipt bound to this exact change."}
			},
			"required": ["profile_id", "scope", "overrides_json", "confirmation_receipt_id"]
		}`)
}

func (t *ConfirmSourceProfileTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		ProfileID     string `json:"profile_id"`
		Scope         string `json:"scope"`
		OverridesJSON string `json:"overrides_json"`
		ReceiptID     string `json:"confirmation_receipt_id"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Confirmer == nil {
		return "", fmt.Errorf("profile confirmer is not initialized")
	}
	if t.parentCtx == nil {
		return "", fmt.Errorf("tool execution context is not initialized")
	}
	auditErrors, err := t.Confirmer(t.parentCtx, params.ProfileID, params.Scope, params.OverridesJSON, params.ReceiptID)
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"profile_id":              params.ProfileID,
		"scope":                   params.Scope,
		"confirmation_receipt_id": params.ReceiptID,
		"ui_summary":              fmt.Sprintf("已按作用域 %s 提交画像 %s 的授权补丁。", params.Scope, params.ProfileID),
	}
	if len(auditErrors) > 0 {
		payload["audit_errors"] = auditErrors
	}
	return toolSuccess("profile_patch_commit", payload), nil
}
