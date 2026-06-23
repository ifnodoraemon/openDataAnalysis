package handler

import (
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

func TestEnsureRequiredConfigRejectsPlaceholderValues(t *testing.T) {
	prev := config.Cfg
	config.Cfg = validRequiredConfig()
	config.Cfg.AuthSecret = "replace-with-a-long-random-secret"
	t.Cleanup(func() { config.Cfg = prev })

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected placeholder config panic")
		}
		if !strings.Contains(recovered.(string), "AUTH_SECRET") {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()

	ensureRequiredConfig()
}

func TestEnsureRequiredConfigAllowsRandomLookingValues(t *testing.T) {
	prev := config.Cfg
	config.Cfg = validRequiredConfig()
	t.Cleanup(func() { config.Cfg = prev })

	ensureRequiredConfig()
}

func validRequiredConfig() *config.Config {
	return &config.Config{
		AuthSecret:           "8f3995c9dbd14f1eb1cf8d3f9a296e11",
		DefaultUserID:        "example_user",
		DefaultUserEmail:     "admin@example.com",
		DefaultUserName:      "Administrator",
		DefaultUserPassword:  "secure-password-123",
		DefaultWorkspaceID:   "workspace_default",
		DefaultWorkspaceName: "Default Workspace",
	}
}
