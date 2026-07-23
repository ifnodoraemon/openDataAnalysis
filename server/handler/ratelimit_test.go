package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
