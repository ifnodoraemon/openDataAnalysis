package domain

import "time"

type SnapshotStatus string

const (
	SnapshotStatusCreating SnapshotStatus = "creating"
	SnapshotStatusReady    SnapshotStatus = "ready"
	SnapshotStatusFailed   SnapshotStatus = "failed"
)

type ProfileMode string

const (
	ProfileModeSampled ProfileMode = "sampled"
	ProfileModeMixed   ProfileMode = "mixed"
	ProfileModeExact   ProfileMode = "exact"
)

type SourceSnapshot struct {
	ID                string
	SessionID         string
	SourceID          string
	UpstreamKind      string
	UpstreamSchema    string
	UpstreamObject    string
	AnalysisTableName string
	RowCount          int
	ColumnCount       int
	Status            SnapshotStatus
	ErrorMessage      *string
	SchemaSignature   string
	ImportedAt        time.Time
	RowsImported      int
	RowsSkipped       int
	ImportRowLimit    int
	ImportTruncated   bool
	ImportDurationMs  int
	ProfileDurationMs int
	SnapshotSizeBytes int64
	ProfileMode       ProfileMode
}
