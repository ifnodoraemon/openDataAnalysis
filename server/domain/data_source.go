package domain

import "time"

type SourceType string
type SourceStatus string

const (
	SourceTypeFileUpload         SourceType = "file_upload"
	SourceTypePostgresConnection SourceType = "postgres_connection"
	SourceTypeMySQLConnection    SourceType = "mysql_connection"

	SourceStatusActive   SourceStatus = "active"
	SourceStatusInvalid  SourceStatus = "invalid"
	SourceStatusDisabled SourceStatus = "disabled"
)

type DataSource struct {
	ID          string
	WorkspaceID string
	Name        string
	SourceType  SourceType
	Status      SourceStatus
	FileID      *string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
