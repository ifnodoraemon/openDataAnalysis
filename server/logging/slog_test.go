package logging

import (
	"context"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
)

func TestWithContextLogger(t *testing.T) {
	ctx := context.Background()

	logger := WithContext(ctx)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}

	identity := auth.Identity{
		UserID:      "usr_test",
		WorkspaceID: "ws_test",
	}
	ctxWithAuth := auth.WithIdentity(ctx, identity)

	enrichedLogger := WithContext(ctxWithAuth)
	if enrichedLogger == nil {
		t.Fatal("expected non-nil enriched logger")
	}
}
