package handler

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
)

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type RateLimiterMap struct {
	mu       sync.Mutex
	limiters map[string]*clientLimiter
	rate     rate.Limit
	burst    int
}

func NewRateLimiterMap(r rate.Limit, burst int) *RateLimiterMap {
	m := &RateLimiterMap{
		limiters: make(map[string]*clientLimiter),
		rate:     r,
		burst:    burst,
	}
	go m.cleanupLoop(10 * time.Minute)
	return m
}

func (m *RateLimiterMap) getLimiter(key string) *rate.Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.limiters[key]
	if !exists {
		limiter := rate.NewLimiter(m.rate, m.burst)
		m.limiters[key] = &clientLimiter{
			limiter:  limiter,
			lastSeen: time.Now(),
		}
		return limiter
	}
	item.lastSeen = time.Now()
	return item.limiter
}

func (m *RateLimiterMap) cleanupLoop(ttl time.Duration) {
	ticker := time.NewTicker(ttl)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		cutoff := time.Now().Add(-ttl)
		for key, item := range m.limiters {
			if item.lastSeen.Before(cutoff) {
				delete(m.limiters, key)
			}
		}
		m.mu.Unlock()
	}
}

// IPRateLimitMiddleware limits requests by remote IP address.
func IPRateLimitMiddleware(requestsPerMinute int, burst int) func(http.Handler) http.Handler {
	r := rate.Limit(float64(requestsPerMinute) / 60.0)
	limiterMap := NewRateLimiterMap(r, burst)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := getClientIP(r)
			limiter := limiterMap.getLimiter(ip)
			if !limiter.Allow() {
				http.Error(w, "请求过于频繁，请稍后重试", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UserRateLimitMiddleware limits requests by authenticated User/Workspace ID or IP.
func UserRateLimitMiddleware(requestsPerMinute int, burst int) func(http.Handler) http.Handler {
	r := rate.Limit(float64(requestsPerMinute) / 60.0)
	limiterMap := NewRateLimiterMap(r, burst)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := getClientIP(r)
			if identity, ok := auth.FromContext(r.Context()); ok && identity.UserID != "" {
				key = identity.UserID + ":" + identity.WorkspaceID
			}

			limiter := limiterMap.getLimiter(key)
			if !limiter.Allow() {
				http.Error(w, "请求过于频繁，请稍后重试", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// getClientIP resolves the client IP. Proxy headers are only honored when the
// direct peer is within a configured trusted proxy CIDR; otherwise RemoteAddr wins.
func getClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return host
	}
	if !isTrustedProxy(peer) {
		return peer.String()
	}
	if entries := splitForwardedFor(r.Header.Get("X-Forwarded-For")); len(entries) > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			ip := net.ParseIP(entries[i])
			if ip == nil {
				return peer.String()
			}
			if isTrustedProxy(ip) {
				continue
			}
			return ip.String()
		}
		if first := net.ParseIP(entries[0]); first != nil {
			return first.String()
		}
	}
	if xreal := strings.TrimSpace(r.Header.Get("X-Real-IP")); xreal != "" {
		if ip := net.ParseIP(xreal); ip != nil {
			return ip.String()
		}
	}
	return peer.String()
}

func splitForwardedFor(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	entries := make([]string, 0, len(parts))
	for _, part := range parts {
		entries = append(entries, strings.TrimSpace(part))
	}
	return entries
}

func isTrustedProxy(ip net.IP) bool {
	if config.Cfg == nil {
		return false
	}
	for _, cidr := range config.Cfg.TrustedProxyCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
