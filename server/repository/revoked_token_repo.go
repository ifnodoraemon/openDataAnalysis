package repository

import (
	"context"
	"time"
)

// RevokedTokenRepository persists the token jti denylist so logout and token
// revocation survive process restarts. Expiry values are stored as unix
// seconds to stay dialect-neutral.
type RevokedTokenRepository interface {
	CreateRevokedToken(ctx context.Context, jti string, expiresAt time.Time) error
	ListActiveRevokedTokens(ctx context.Context, now time.Time) (map[string]time.Time, error)
	DeleteExpiredRevokedTokens(ctx context.Context, now time.Time) (int64, error)
}
