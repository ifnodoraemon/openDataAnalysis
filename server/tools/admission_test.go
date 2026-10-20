package tools

import (
	"encoding/json"
	"fmt"
	"testing"
)

type gatedFakeTool struct {
	name    string
	receipt string
	binding ApprovalRequest
	bindErr error
}

func (t *gatedFakeTool) Name() string        { return t.name }
func (t *gatedFakeTool) Description() string { return "fake tool" }
func (t *gatedFakeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *gatedFakeTool) Execute(json.RawMessage) (string, error) { return `{"ok":true}`, nil }

// plainFakeTool implements only Tool, so it never consults the approval store.
type plainFakeTool struct{}

func (t *plainFakeTool) Name() string        { return "plain" }
func (t *plainFakeTool) Description() string { return "ungated fake tool" }
func (t *plainFakeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *plainFakeTool) Execute(json.RawMessage) (string, error) { return `{"ok":true}`, nil }

func (t *gatedFakeTool) ApprovalRequest(json.RawMessage) (ApprovalRequest, error) {
	return t.binding, t.bindErr
}

func (t *gatedFakeTool) ApprovalReceiptID(json.RawMessage) string { return t.receipt }

type recordingApprovalStore struct {
	verified []string
	err      error
}

func (s *recordingApprovalStore) VerifyApproval(receiptID string, _ ApprovalRequest) error {
	s.verified = append(s.verified, receiptID)
	return s.err
}

func TestDecideToolCallAllowsUngatedTools(t *testing.T) {
	t.Parallel()

	// A tool that does not implement ApprovalGate never consults the store.
	admission := DecideToolCall(&plainFakeTool{}, json.RawMessage(`{}`), nil)
	if admission.Kind != AdmissionAllow {
		t.Fatalf("expected allow for an ungated tool, got %#v", admission)
	}
}

func TestDecideToolCallAsksWhenNoReceiptWasSupplied(t *testing.T) {
	t.Parallel()

	store := &recordingApprovalStore{}
	tool := &gatedFakeTool{
		name:    "gated",
		binding: ApprovalRequest{Action: "do_thing", ResourceRef: "res_1", PayloadJSON: `{"a":1}`},
	}

	admission := DecideToolCall(tool, json.RawMessage(`{}`), store)
	if admission.Kind != AdmissionAsk {
		t.Fatalf("expected ask without a receipt, got %#v", admission)
	}
	if len(store.verified) != 0 {
		t.Fatalf("an unapproved call must not be verified against the store")
	}
	if admission.Reason == "" {
		t.Fatalf("an ask decision must explain how to obtain authorization")
	}
}

func TestDecideToolCallAllowsVerifiedReceipt(t *testing.T) {
	t.Parallel()

	store := &recordingApprovalStore{}
	tool := &gatedFakeTool{
		name:    "gated",
		receipt: "ucr_1",
		binding: ApprovalRequest{Action: "do_thing", ResourceRef: "res_1", PayloadJSON: `{"a":1}`},
	}

	admission := DecideToolCall(tool, json.RawMessage(`{}`), store)
	if admission.Kind != AdmissionAllow {
		t.Fatalf("expected allow with a verified receipt, got %#v", admission)
	}
	if len(store.verified) != 1 || store.verified[0] != "ucr_1" {
		t.Fatalf("expected the receipt to be verified, got %#v", store.verified)
	}
}

// Every failure path is closed: the call must not execute.
func TestDecideToolCallFailsClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		tool   *gatedFakeTool
		store  ApprovalStore
		reason string
	}{
		{
			name:   "unverifiable receipt",
			tool:   &gatedFakeTool{name: "gated", receipt: "ucr_1", binding: ApprovalRequest{Action: "a", ResourceRef: "r"}},
			store:  &recordingApprovalStore{err: fmt.Errorf("receipt has expired")},
			reason: "deny",
		},
		{
			name:   "no store available",
			tool:   &gatedFakeTool{name: "gated", receipt: "ucr_1", binding: ApprovalRequest{Action: "a", ResourceRef: "r"}},
			store:  nil,
			reason: "deny",
		},
		{
			name:   "arguments cannot be bound to an authorization",
			tool:   &gatedFakeTool{name: "gated", receipt: "ucr_1", bindErr: fmt.Errorf("profile_id is required")},
			store:  &recordingApprovalStore{},
			reason: "deny",
		},
		{
			name:   "tool does not declare its authorization",
			tool:   &gatedFakeTool{name: "gated", receipt: "ucr_1"},
			store:  &recordingApprovalStore{},
			reason: "deny",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admission := DecideToolCall(tc.tool, json.RawMessage(`{}`), tc.store)
			if admission.Kind != AdmissionDeny {
				t.Fatalf("expected deny, got %#v", admission)
			}
			if admission.Reason == "" {
				t.Fatalf("a denial must state a reason")
			}
		})
	}
}

func TestSemanticConfirmationAuthorizationPayloadCanonicalizes(t *testing.T) {
	t.Parallel()

	// Field order inside overrides_json must not change the authorized payload,
	// otherwise an approval could silently cover a different commit.
	first, err := SemanticConfirmationAuthorizationPayload("session", `{"a":1,"b":2}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := SemanticConfirmationAuthorizationPayload("session", `{"b":2,"a":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first != second {
		t.Fatalf("payload canonicalization must be order independent: %q vs %q", first, second)
	}

	if _, err := SemanticConfirmationAuthorizationPayload("", `{}`); err == nil {
		t.Fatalf("an empty scope must be rejected")
	}
	if _, err := SemanticConfirmationAuthorizationPayload("session", "not-json"); err == nil {
		t.Fatalf("a non-object payload must be rejected")
	}
	if _, err := SemanticConfirmationAuthorizationPayload(" session", `{}`); err == nil {
		t.Fatalf("a scope with surrounding whitespace must be rejected")
	}
}
