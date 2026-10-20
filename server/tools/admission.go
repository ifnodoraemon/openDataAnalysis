package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

// AdmissionKind is the runtime's decision about whether one tool call may run.
type AdmissionKind string

const (
	// AdmissionAllow means the call may execute.
	AdmissionAllow AdmissionKind = "allow"
	// AdmissionAsk means the call is blocked until the user authorizes exactly
	// this action. The runtime does not execute it on this attempt.
	AdmissionAsk AdmissionKind = "ask"
	// AdmissionDeny means the call is refused and must not execute.
	AdmissionDeny AdmissionKind = "deny"
)

// Admission is the single decision for one tool call. The engine consults it
// before executing anything, so no gated action depends on a tool remembering
// to check its own authorization.
type Admission struct {
	Kind   AdmissionKind
	Reason string
}

// ApprovalRequest is the exact authorization a gated call needs.
type ApprovalRequest struct {
	Action      string
	ResourceRef string
	PayloadJSON string
}

// ApprovalGate is implemented by tools whose execution requires a user
// authorization receipt.
type ApprovalGate interface {
	// ApprovalRequest returns the exact authorization these arguments need. It
	// must not depend on the receipt being present.
	ApprovalRequest(args json.RawMessage) (ApprovalRequest, error)
	// ApprovalReceiptID returns the receipt supplied with this call, or "".
	ApprovalReceiptID(args json.RawMessage) string
}

// ApprovalStore verifies that a receipt authorizes exactly one request.
type ApprovalStore interface {
	VerifyApproval(receiptID string, request ApprovalRequest) error
}

// DecideToolCall is the runtime's single admission decision for a tool call.
//
// Tools that do not require authorization are always allowed. A gated tool is
// blocked unless the call carries a receipt that a store confirms authorizes
// exactly this action, resource and payload. Every failure path is closed: a
// missing receipt asks for authorization, and a receipt that cannot be verified
// is denied rather than executed.
func DecideToolCall(tool Tool, args json.RawMessage, approvals ApprovalStore) Admission {
	gate, ok := tool.(ApprovalGate)
	if !ok {
		return Admission{Kind: AdmissionAllow}
	}

	request, err := gate.ApprovalRequest(args)
	if err != nil {
		return Admission{
			Kind:   AdmissionDeny,
			Reason: fmt.Sprintf("this action cannot be authorized as requested: %v", err),
		}
	}
	if strings.TrimSpace(request.Action) == "" || strings.TrimSpace(request.ResourceRef) == "" {
		return Admission{
			Kind:   AdmissionDeny,
			Reason: "this action does not declare the authorization it requires, so it cannot be approved",
		}
	}

	receiptID := strings.TrimSpace(gate.ApprovalReceiptID(args))
	if receiptID == "" {
		return Admission{
			Kind: AdmissionAsk,
			Reason: fmt.Sprintf(
				"action %q on %q requires explicit user authorization. Ask the user with user_request_input and an authorization block naming exactly this action, resource_ref and payload_json, then retry this call with the returned confirmation_receipt_id.",
				request.Action, request.ResourceRef),
		}
	}
	if approvals == nil {
		return Admission{
			Kind:   AdmissionDeny,
			Reason: "no authorization store is available, so this action cannot be verified and must not run",
		}
	}
	if err := approvals.VerifyApproval(receiptID, request); err != nil {
		return Admission{Kind: AdmissionDeny, Reason: err.Error()}
	}
	return Admission{Kind: AdmissionAllow}
}

// SemanticConfirmationAuthorizationPayload canonicalizes the exact payload a
// profile patch confirmation authorizes. The runtime and the tool both use this
// function, so the approved payload and the committed payload cannot drift.
func SemanticConfirmationAuthorizationPayload(scope, overridesJSON string) (string, error) {
	if scope == "" || scope != strings.TrimSpace(scope) {
		return "", fmt.Errorf("scope must be a non-empty exact value")
	}
	if overridesJSON == "" || overridesJSON != strings.TrimSpace(overridesJSON) {
		return "", fmt.Errorf("overrides_json must be a non-empty exact JSON object")
	}
	var overrides map[string]interface{}
	if err := jsoncontract.Decode([]byte(overridesJSON), &overrides); err != nil {
		return "", fmt.Errorf("overrides_json must be a strict JSON object: %w", err)
	}
	payload, err := json.Marshal(map[string]interface{}{"scope": scope, "overrides": overrides})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}
