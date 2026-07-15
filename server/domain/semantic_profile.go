package domain

import "time"

type ProfileStatus string

const (
	ProfileStatusDraft     ProfileStatus = "draft"
	ProfileStatusProfiled  ProfileStatus = "profiled"
	ProfileStatusConfirmed ProfileStatus = "confirmed"
	ProfileStatusRejected  ProfileStatus = "rejected"
)

type SemanticProfile struct {
	ID                string
	SessionID         string
	SourceID          string
	SnapshotID        string
	AnalysisTableName string
	SchemaSignature   string
	ProfileStatus     ProfileStatus
	ProfileJSON       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
