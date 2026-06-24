package domain

import "time"

type AuditEvent struct {
	ID           string
	WorkspaceID  string
	SessionID    string
	RunID        string
	ActorUserID  string
	EventType    string
	ResourceType string
	ResourceID   string
	PayloadJSON  string
	CreatedAt    time.Time
}
