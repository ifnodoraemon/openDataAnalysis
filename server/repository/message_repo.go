package repository

import (
	"context"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type MessageRepository interface {
	Create(ctx context.Context, msg *domain.RunMessage) error
	// ListByRun returns a run's events in chronological order. A run longer
	// than the read window is returned as its oldest events plus its newest
	// ones: dropping either end loses state that history rebuild depends on
	// (the opening task and first compaction checkpoint at the start, the
	// current turn at the end). Callers surface the omitted middle through
	// CountByRun instead of treating the window as the whole run.
	ListByRun(ctx context.Context, runID string) ([]domain.RunMessage, error)
	// CountByRun reports how many events a run actually holds, so a bounded
	// read can state what it left behind instead of truncating silently.
	CountByRun(ctx context.Context, runID string) (int, error)
	ListByRunPath(ctx context.Context, runID string) ([]domain.RunMessage, error)
	ListRecentByRun(ctx context.Context, runID string, limit int) ([]domain.RunMessage, error)
}
