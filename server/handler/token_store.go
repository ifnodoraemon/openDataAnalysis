package handler

import (
	"context"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

// revocationStoreAdapter adapts the persisted revoked-token repository to the
// auth package's RevocationStore contract.
type revocationStoreAdapter struct {
	repo repository.RevokedTokenRepository
}

func newRevocationStoreAdapter(repo repository.RevokedTokenRepository) auth.RevocationStore {
	if repo == nil {
		return nil
	}
	return &revocationStoreAdapter{repo: repo}
}

func (a *revocationStoreAdapter) SaveRevocation(ctx context.Context, jti string, expiresAt time.Time) error {
	return a.repo.CreateRevokedToken(ctx, jti, expiresAt)
}

func (a *revocationStoreAdapter) LoadRevocations(ctx context.Context) (map[string]time.Time, error) {
	return a.repo.ListActiveRevokedTokens(ctx, time.Now())
}

func (a *revocationStoreAdapter) PruneRevocations(ctx context.Context, now time.Time) error {
	_, err := a.repo.DeleteExpiredRevokedTokens(ctx, now)
	return err
}
