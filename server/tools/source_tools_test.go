package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfirmSourceProfileToolReturnsFactsWithoutNextActionHint(t *testing.T) {
	t.Parallel()

	tool := &ConfirmSourceProfileTool{
		Confirmer: func(profileID, confirmedBy, scope, overridesJSON string) error {
			if profileID != "profile_1" || confirmedBy != "agent" || scope != "session" {
				t.Fatalf("unexpected confirmation args: profileID=%q confirmedBy=%q scope=%q", profileID, confirmedBy, scope)
			}
			if !strings.Contains(overridesJSON, "primary_time_column") {
				t.Fatalf("expected overrides_json to be passed through, got %q", overridesJSON)
			}
			return nil
		},
	}

	result, err := tool.Execute(json.RawMessage(`{
		"profile_id":"profile_1",
		"scope":"session",
		"overrides_json":"{\"primary_time_column\":\"month\"}"
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK        bool   `json:"ok"`
		Tool      string `json:"tool"`
		ProfileID string `json:"profile_id"`
		Scope     string `json:"scope"`
		UISummary string `json:"ui_summary"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.Tool != "state_source_confirm_profile" || payload.ProfileID != "profile_1" || payload.Scope != "session" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	lowerSummary := strings.ToLower(payload.UISummary)
	if strings.Contains(lowerSummary, "run ") || strings.Contains(lowerSummary, "call ") || strings.Contains(payload.UISummary, "state_session_sources_inspect") {
		t.Fatalf("ui_summary must not contain next-action hints, got %q", payload.UISummary)
	}
}
