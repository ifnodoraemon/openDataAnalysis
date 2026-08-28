package domain

import "time"

type ConfirmationScope string
type ConfirmationProvenance string

const (
	ConfirmationScopeSession   ConfirmationScope = "session"
	ConfirmationScopeWorkspace ConfirmationScope = "workspace"
)

const (
	ConfirmationProvenanceAuthenticatedRequest ConfirmationProvenance = "authenticated_request"
	ConfirmationProvenanceAuthorizationReceipt ConfirmationProvenance = "authorization_receipt"
)

type SemanticConfirmation struct {
	ID                    string
	ProfileID             string
	WorkspaceID           string
	SessionID             string
	ConfirmedBy           string
	ConfirmationReceiptID string
	Provenance            ConfirmationProvenance
	Scope                 ConfirmationScope
	OverridesJSON         string
	CreatedAt             time.Time
}
