package auth

import (
	"sync"
	"time"
)

// Revoker is an in-memory jti denylist. Entries are evicted once their
// recorded token expiry passes, so the map never grows without bound.
// Lookups only inspect the requested entry; full sweeps run on writes and
// when loading persisted revocations.
type Revoker struct {
	mu      sync.Mutex
	revoked map[string]time.Time
}

func NewRevoker() *Revoker {
	return &Revoker{revoked: make(map[string]time.Time)}
}

func (r *Revoker) Revoke(jti string, expiresAt time.Time) {
	if r == nil || jti == "" {
		return
	}
	now := time.Now()
	if !now.Before(expiresAt) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictExpiredLocked(now)
	r.revoked[jti] = expiresAt
}

func (r *Revoker) IsRevoked(jti string) bool {
	if r == nil || jti == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	expiresAt, ok := r.revoked[jti]
	if !ok {
		return false
	}
	if !time.Now().Before(expiresAt) {
		delete(r.revoked, jti)
		return false
	}
	return true
}

// ReplaceAll swaps the in-memory denylist with the given revocations.
func (r *Revoker) ReplaceAll(revocations map[string]time.Time) {
	if r == nil {
		return
	}
	now := time.Now()
	fresh := make(map[string]time.Time, len(revocations))
	for jti, expiresAt := range revocations {
		if now.Before(expiresAt) {
			fresh[jti] = expiresAt
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = fresh
}

func (r *Revoker) evictExpiredLocked(now time.Time) {
	for jti, expiresAt := range r.revoked {
		if !now.Before(expiresAt) {
			delete(r.revoked, jti)
		}
	}
}
