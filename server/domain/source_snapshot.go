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
	ProfileModePending ProfileMode = "pending"
	ProfileModeSampled ProfileMode = "sampled"
	ProfileModeExact   ProfileMode = "exact"
	ProfileModeLive    ProfileMode = "live"
)

type SnapshotMode string

const (
	SnapshotModeImported SnapshotMode = "imported"
	SnapshotModeLive     SnapshotMode = "live"
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
	Mode              SnapshotMode
}
