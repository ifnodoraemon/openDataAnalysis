package domain

import "time"

type ConfirmationScope string

const (
	ConfirmationScopeSession   ConfirmationScope = "session"
	ConfirmationScopeWorkspace ConfirmationScope = "workspace"
)

type SemanticConfirmation struct {
	ID            string
	ProfileID     string
	WorkspaceID   string
	SessionID     string
	ConfirmedBy   string
	Scope         ConfirmationScope
	OverridesJSON string
	CreatedAt     time.Time
}
