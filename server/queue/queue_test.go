package queue

import (
	"testing"
)

func TestAnalysisRunJobArgsKind(t *testing.T) {
	args := AnalysisRunJobArgs{
		RunID:       "run_123",
		SessionID:   "sess_123",
		WorkspaceID: "ws_123",
		UserID:      "usr_123",
	}
	if args.Kind() != "analysis_run" {
		t.Fatalf("unexpected job kind: %s", args.Kind())
	}
}

func TestSessionCleanupJobArgsKind(t *testing.T) {
	args := SessionCleanupJobArgs{
		SessionTTLHours:    24,
		TraceRetentionDays: 7,
	}
	if args.Kind() != "session_cleanup" {
		t.Fatalf("unexpected job kind: %s", args.Kind())
	}
}
