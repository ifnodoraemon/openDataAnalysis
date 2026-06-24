package tools

import (
	"encoding/json"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/service"
)

type SessionSourcesProvider func() ([]service.SessionSourceSummary, error)
type SessionProfilesProvider func() ([]service.SemanticProfileSummary, error)
type ProfileDetailProvider func(profileID string) (profileJSON string, confirmationsJSON string, err error)
type ProfileConfirmer func(profileID, confirmedBy, scope, overridesJSON string) error
type GovernanceProvider func() (service.GovernanceInspection, error)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.SessionSourcesProvider == nil {
			return nil
		}
		return &InspectSessionSourcesTool{Provider: ctx.SessionSourcesProvider}
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
			Detail:      ctx.ProfileDetailProvider,
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
	Provider SessionSourcesProvider
}

func (t *InspectSessionSourcesTool) Name() string { return "state_session_sources_inspect" }
func (t *InspectSessionSourcesTool) Description() string {
	return "Read current session's data source facts: each source's type, snapshot table name, row count, column count, profile status, ambiguity summary, confirmed overrides, large table flag. Does not modify any state."
}
func (t *InspectSessionSourcesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}

func (t *InspectSessionSourcesTool) Execute(args json.RawMessage) (string, error) {
	if t.Provider == nil {
		return "", fmt.Errorf("session sources provider is not initialized")
	}
	sources, err := t.Provider()
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"source_count": len(sources),
		"sources":      sources,
		"ui_summary":   fmt.Sprintf("%d data sources in current session.", len(sources)),
	}
	return toolSuccess("state_session_sources_inspect", payload), nil
}

type InspectSemanticProfileTool struct {
	Provider ProfileDetailProvider
}

func (t *InspectSemanticProfileTool) Name() string { return "state_semantic_profile_inspect" }
func (t *InspectSemanticProfileTool) Description() string {
	return "Read detailed facts for a specified semantic profile: schema, candidate time columns, candidate metrics, candidate joins, candidate units, ambiguity list, warnings, applied confirmation overrides. Does not modify any state."
}
func (t *InspectSemanticProfileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"profile_id":{"type":"string","description":"The semantic profile ID to inspect"}},"required":["profile_id"]}`)
}

func (t *InspectSemanticProfileTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		ProfileID string `json:"profile_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Provider == nil {
		return "", fmt.Errorf("profile detail provider is not initialized")
	}
	profileJSON, confirmationsJSON, err := t.Provider(params.ProfileID)
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"profile_id":    params.ProfileID,
		"profile_json":  json.RawMessage(profileJSON),
		"confirmations": json.RawMessage(confirmationsJSON),
	}
	return toolSuccess("state_semantic_profile_inspect", payload), nil
}

type InspectGovernanceTool struct {
	Provider GovernanceProvider
}

func (t *InspectGovernanceTool) Name() string { return "state_governance_inspect" }
func (t *InspectGovernanceTool) Description() string {
	return "Read current session's general data governance facts: source coverage, snapshot status, semantic ambiguity count, profile warnings, reusable semantic asset count, and issue severities. Does not modify any state and does not prescribe follow-up actions."
}
func (t *InspectGovernanceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}

func (t *InspectGovernanceTool) Execute(args json.RawMessage) (string, error) {
	if t.Provider == nil {
		return "", fmt.Errorf("governance provider is not initialized")
	}
	inspection, err := t.Provider()
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"source_count":         inspection.SourceCount,
		"semantic_asset_count": inspection.SemanticAssetCount,
		"issue_count":          inspection.IssueCount,
		"severity_counts":      inspection.SeverityCounts,
		"sources":              inspection.Sources,
		"issues":               inspection.Issues,
		"generated_at":         inspection.GeneratedAt,
		"ui_summary":           fmt.Sprintf("%d governance facts inspected; %d issues found.", inspection.SourceCount, inspection.IssueCount),
	}
	return toolSuccess("state_governance_inspect", payload), nil
}

type ConfirmSourceProfileTool struct {
	Confirmer   ProfileConfirmer
	Detail      ProfileDetailProvider
	SessionID   string
	WorkspaceID string
}

func (t *ConfirmSourceProfileTool) Name() string { return "state_source_confirm_profile" }
func (t *ConfirmSourceProfileTool) Description() string {
	return "Resolve semantic ambiguities detected during data profiling. Accepts a profile_id and a JSON object of overrides specifying which interpretations to confirm (e.g. primary time column, percentage unit columns, metric definitions, confirmed join candidates). Writes confirmation overrides for the requested scope and returns the confirmed profile_id and scope. This tool does not create or modify data sources."
}
func (t *ConfirmSourceProfileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"profile_id": {"type": "string", "description": "The semantic profile ID to confirm"},
			"scope": {"type": "string", "enum": ["session", "workspace"], "description": "Scope of the confirmation. Use 'session' for session-level overrides."},
			"overrides_json": {"type": "string", "description": "JSON object specifying overrides. For multiple_time_columns: {\"primary_time_column\":\"column_name\"}. For ambiguous_units: {\"percentage_columns\":[\"col1\",\"col2\"]}. For ambiguous_metrics: {\"metric_definitions\":{\"col1\":\"definition\",...}}. For ambiguous_join: {\"confirmed_join_candidates\":[\"candidate\"]}."}
		},
		"required": ["profile_id", "scope", "overrides_json"]
	}`)
}

func (t *ConfirmSourceProfileTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		ProfileID     string `json:"profile_id"`
		Scope         string `json:"scope"`
		OverridesJSON string `json:"overrides_json"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Confirmer == nil {
		return "", fmt.Errorf("profile confirmer is not initialized")
	}
	if err := t.Confirmer(params.ProfileID, "agent", params.Scope, params.OverridesJSON); err != nil {
		return "", err
	}
	return toolSuccess("state_source_confirm_profile", map[string]interface{}{
		"profile_id": params.ProfileID,
		"scope":      params.Scope,
		"ui_summary": fmt.Sprintf("Profile %s confirmed with scope=%s.", params.ProfileID, params.Scope),
	}), nil
}
