package domain

import "time"

type RunStatus string
type RunKind string

const (
	RunStatusQueued           RunStatus = "queued"
	RunStatusRunning          RunStatus = "running"
	RunStatusWaitingUserInput RunStatus = "waiting_user_input"
	RunStatusCompleted        RunStatus = "completed"
	RunStatusFailed           RunStatus = "failed"
	RunStatusCancelled        RunStatus = "cancelled"

	RunKindRoot     RunKind = "root"
	RunKindDelegate RunKind = "delegate"
)

type AnalysisRun struct {
	ID           string
	SessionID    string
	WorkspaceID  string
	UserID       string
	ParentRunID  *string
	RunKind      RunKind
	DelegateRole string
	GoalID       *string
	Status       RunStatus
	InputMessage string
	Summary      string
	ErrorMessage *string
	ReportFileID *string
	// PromptTokens and CompletionTokens are the model usage accumulated for this
	// run. They are operational facts used for cost visibility, not billing.
	PromptTokens     int
	CompletionTokens int
	StartedAt        *time.Time
	FinishedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
