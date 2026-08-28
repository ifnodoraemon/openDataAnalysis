package handler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

func TestGetClientIPUntrustedPeerIgnoresForwardedHeaders(t *testing.T) {
	previous := config.Cfg
	config.Cfg = &config.Config{}
	defer func() { config.Cfg = previous }()

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "203.0.113.10:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.9.9.9")
	req.Header.Set("X-Real-IP", "5.6.7.8")

	if got := getClientIP(req); got != "203.0.113.10" {
		t.Fatalf("expected untrusted peer RemoteAddr to win, got %q", got)
	}
}

func TestGetClientIPNilConfigIgnoresForwardedHeaders(t *testing.T) {
	previous := config.Cfg
	config.Cfg = nil
	defer func() { config.Cfg = previous }()

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "203.0.113.10:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := getClientIP(req); got != "203.0.113.10" {
		t.Fatalf("expected nil config to distrust proxy headers, got %q", got)
	}
}

func TestGetClientIPTrustedProxyWalksForwardedForRightToLeft(t *testing.T) {
	previous := config.Cfg
	config.Cfg = &config.Config{
		TrustedProxyCIDRs: []*net.IPNet{
			{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)},
		},
	}
	defer func() { config.Cfg = previous }()

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.5:1000"

	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := getClientIP(req); got != "203.0.113.7" {
		t.Fatalf("expected single XFF hop, got %q", got)
	}

	req.Header.Set("X-Forwarded-For", "198.51.100.4, 203.0.113.7, 10.0.0.9")
	if got := getClientIP(req); got != "203.0.113.7" {
		t.Fatalf("expected first untrusted hop from the right, got %q", got)
	}

	req.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	if got := getClientIP(req); got != "10.0.0.1" {
		t.Fatalf("expected leftmost hop when all are trusted, got %q", got)
	}

	req.Header.Del("X-Forwarded-For")
	req.Header.Set("X-Real-IP", "203.0.113.9")
	if got := getClientIP(req); got != "203.0.113.9" {
		t.Fatalf("expected X-Real-IP fallback for trusted peer, got %q", got)
	}

	req.Header.Del("X-Real-IP")
	if got := getClientIP(req); got != "10.0.0.5" {
		t.Fatalf("expected trusted peer RemoteAddr without proxy headers, got %q", got)
	}
}

func TestGetClientIPTrustedProxyFailsClosedOnGarbageHop(t *testing.T) {
	previous := config.Cfg
	config.Cfg = &config.Config{
		TrustedProxyCIDRs: []*net.IPNet{
			{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)},
		},
	}
	defer func() { config.Cfg = previous }()

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.5:1000"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	if got := getClientIP(req); got != "10.0.0.5" {
		t.Fatalf("expected garbage XFF hop to fall back to peer, got %q", got)
	}
}

func TestIPRateLimitMiddlewareIgnoresSpoofedForwardedFor(t *testing.T) {
	previous := config.Cfg
	config.Cfg = &config.Config{}
	defer func() { config.Cfg = previous }()

	mw := IPRateLimitMiddleware(2, 2)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		req.Header.Set("X-Forwarded-For", net.IPv4(198, 18, 0, byte(i)).String())
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200 on request %d despite spoofed XFF, got %d", i+1, rr.Code)
		}
	}

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("X-Forwarded-For", "198.18.0.99")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 regardless of spoofed XFF, got %d", rr.Code)
	}
}

func TestIPRateLimitMiddleware(t *testing.T) {
	// 2 requests per minute, burst of 2
	mw := IPRateLimitMiddleware(2, 2)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Request 1: OK
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("expected 200 on req 1, got %d", rr1.Code)
	}

	// Request 2: OK (burst is 2)
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.100:12345"
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 on req 2, got %d", rr2.Code)
	}

	// Request 3: Exceeded -> 429
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "192.168.1.100:12345"
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on req 3, got %d", rr3.Code)
	}

	// Request from another IP should be OK
	reqOther := httptest.NewRequest("GET", "/test", nil)
	reqOther.RemoteAddr = "192.168.1.101:12345"
	rrOther := httptest.NewRecorder()
	handler.ServeHTTP(rrOther, reqOther)
	if rrOther.Code != http.StatusOK {
		t.Fatalf("expected 200 for different IP, got %d", rrOther.Code)
	}
}
