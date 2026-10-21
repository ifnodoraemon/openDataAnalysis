package repository

import (
	"context"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type MessageRepository interface {
	Create(ctx context.Context, msg *domain.RunMessage) error
	ListByRun(ctx context.Context, runID string) ([]domain.RunMessage, error)
	// CountByRun reports how many events a run actually holds, so a bounded
	// read can state what it left behind instead of truncating silently.
	CountByRun(ctx context.Context, runID string) (int, error)
	ListByRunPath(ctx context.Context, runID string) ([]domain.RunMessage, error)
	ListRecentByRun(ctx context.Context, runID string, limit int) ([]domain.RunMessage, error)
}
