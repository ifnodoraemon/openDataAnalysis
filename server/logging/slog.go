package logging

import (
	"context"
	"log/slog"
	"os"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
)

var Logger *slog.Logger

func init() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	Logger = slog.New(handler)
	slog.SetDefault(Logger)
}

// WithContext returns a logger enriched with trace_id, user_id, and workspace_id from ctx.
func WithContext(ctx context.Context) *slog.Logger {
	logger := Logger
	if reqID := middleware.GetReqID(ctx); reqID != "" {
		logger = logger.With(slog.String("req_id", reqID))
	}
	if identity, ok := auth.FromContext(ctx); ok {
		if identity.UserID != "" {
			logger = logger.With(slog.String("user_id", identity.UserID))
		}
		if identity.WorkspaceID != "" {
			logger = logger.With(slog.String("workspace_id", identity.WorkspaceID))
		}
	}
	return logger
}
