package domain

import "time"

type SessionSourceBinding struct {
	SessionID        string
	SourceID         string
	SourceObjectKey  string
	ActiveSnapshotID string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
