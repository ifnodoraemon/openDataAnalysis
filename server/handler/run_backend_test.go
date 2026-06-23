package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

func TestConfiguredRunBackendDefaultsToInProcess(t *testing.T) {
	prev := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = prev })

	if got := configuredRunBackend(); got != "inprocess" {
		t.Fatalf("expected default run backend inprocess, got %q", got)
	}
}

func TestDispatchRunExecutionRejectsUnsupportedBackend(t *testing.T) {
	prev := config.Cfg
	config.Cfg = &config.Config{RunBackend: "durable"}
	t.Cleanup(func() { config.Cfg = prev })

	err := dispatchRunExecution(runExecution{Context: context.Background()})
	if err == nil {
		t.Fatal("expected unsupported backend error")
	}
	if !strings.Contains(err.Error(), `run backend "durable" is not implemented`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDispatchRunExecutionRejectsMissingRuntime(t *testing.T) {
	prev := config.Cfg
	config.Cfg = &config.Config{RunBackend: "inprocess"}
	t.Cleanup(func() { config.Cfg = prev })

	err := dispatchRunExecution(runExecution{Context: context.Background()})
	if err == nil || !strings.Contains(err.Error(), "session runtime is not initialized") {
		t.Fatalf("expected missing session error, got %v", err)
	}

	err = dispatchRunExecution(runExecution{Context: context.Background(), Session: &session.Session{}})
	if err == nil || !strings.Contains(err.Error(), "agent engine is not initialized") {
		t.Fatalf("expected missing engine error, got %v", err)
	}
}
