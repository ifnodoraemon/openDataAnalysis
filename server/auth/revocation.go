package auth

import (
	"sync"
	"time"
)

// Revoker is an in-memory jti denylist. Entries are evicted once their
// recorded token expiry passes, so the map never grows without bound.
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
	r.evictExpiredLocked(time.Now())
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

func (r *Revoker) evictExpiredLocked(now time.Time) {
	for jti, expiresAt := range r.revoked {
		if !now.Before(expiresAt) {
			delete(r.revoked, jti)
		}
	}
}
