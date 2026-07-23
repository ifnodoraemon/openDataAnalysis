package repository

import (
	"context"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type MessageRepository interface {
	Create(ctx context.Context, msg *domain.RunMessage) error
	ListByRun(ctx context.Context, runID string) ([]domain.RunMessage, error)
	ListByRunPath(ctx context.Context, runID string) ([]domain.RunMessage, error)
	ListRecentByRun(ctx context.Context, runID string, limit int) ([]domain.RunMessage, error)
}
