package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/service"
)

func TestConfirmSourceProfileToolReturnsFactsWithoutNextActionHint(t *testing.T) {
	t.Parallel()

	tool := &ConfirmSourceProfileTool{
		Confirmer: func(ctx context.Context, profileID, scope, overridesJSON, receiptID string) ([]string, error) {
			if profileID != "profile_1" || scope != "session" || receiptID != "ucr_1" {
				t.Fatalf("unexpected confirmation args: profileID=%q scope=%q receiptID=%q", profileID, scope, receiptID)
			}
			if !strings.Contains(overridesJSON, "annotation") {
				t.Fatalf("expected overrides_json to be passed through, got %q", overridesJSON)
			}
			return nil, nil
		},
	}
	tool.SetExecutionContext(context.Background())

	result, err := tool.Execute(json.RawMessage(`{
		"profile_id":"profile_1",
		"scope":"session",
		"overrides_json":"{\"annotation\":\"user supplied\"}",
		"confirmation_receipt_id":"ucr_1"
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK        bool   `json:"ok"`
		Tool      string `json:"tool"`
		ProfileID string `json:"profile_id"`
		Scope     string `json:"scope"`
		ReceiptID string `json:"confirmation_receipt_id"`
		UISummary string `json:"ui_summary"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.Tool != "profile_patch_commit" || payload.ProfileID != "profile_1" || payload.Scope != "session" || payload.ReceiptID != "ucr_1" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	lowerSummary := strings.ToLower(payload.UISummary)
	if strings.Contains(lowerSummary, "run ") || strings.Contains(lowerSummary, "call ") || strings.Contains(payload.UISummary, "state_session_sources_inspect") {
		t.Fatalf("ui_summary must not contain next-action hints, got %q", payload.UISummary)
	}
}

func TestInspectGovernanceToolReturnsFactsWithoutNextActionHint(t *testing.T) {
	t.Parallel()

	tool := &InspectGovernanceTool{
		Provider: func(ctx context.Context) (service.GovernanceInspection, error) {
			return service.GovernanceInspection{
				SourceCount:        1,
				SemanticAssetCount: 0,
				ProfileWarnings: []service.GovernanceProfileWarning{
					{SourceID: "source_1", ProfileID: "profile_1", Warning: "model interpretation is uncertain"},
				},
			}, nil
		},
	}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		OK              bool                               `json:"ok"`
		Tool            string                             `json:"tool"`
		ProfileWarnings []service.GovernanceProfileWarning `json:"profile_warnings"`
		Issues          interface{}                        `json:"issues"`
		SeverityCounts  interface{}                        `json:"severity_counts"`
		UISummary       string                             `json:"ui_summary"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.Tool != "state_governance_inspect" || len(payload.ProfileWarnings) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload.Issues != nil || payload.SeverityCounts != nil {
		t.Fatalf("runtime must not classify or rank governance facts: %#v", payload)
	}
	lowerSummary := strings.ToLower(payload.UISummary)
	if strings.Contains(lowerSummary, "next") || strings.Contains(lowerSummary, "call ") || strings.Contains(lowerSummary, "should") {
		t.Fatalf("ui_summary must not contain next-action hints, got %q", payload.UISummary)
	}
}
