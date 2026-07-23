package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetricsEndpointAndMiddleware(t *testing.T) {
	testHandler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test-metrics", nil)
	rr := httptest.NewRecorder()
	testHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}

	metricsReq := httptest.NewRequest("GET", "/metrics", nil)
	metricsRR := httptest.NewRecorder()
	Handler().ServeHTTP(metricsRR, metricsReq)

	if metricsRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /metrics, got %d", metricsRR.Code)
	}
}
