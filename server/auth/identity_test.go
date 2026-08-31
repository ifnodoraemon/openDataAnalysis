package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Query-parameter tokens must be rejected everywhere, including the event
// stream: tokens in URLs leak through access logs, proxies, and referrers.
// The event stream authenticates via cookie or the Authorization header.
func TestMiddlewareRejectsQueryTokenEverywhere(t *testing.T) {
	t.Parallel()

	manager := NewTokenManager("test-secret")
	token, err := manager.Sign(Identity{
		UserID:      "user-1",
		WorkspaceID: "workspace-1",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	handler := Middleware(manager)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	normalRequest := httptest.NewRequest(http.MethodGet, "/api/sessions?token="+token, nil)
	normalRecorder := httptest.NewRecorder()
	handler.ServeHTTP(normalRecorder, normalRequest)
	if normalRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected query token rejection outside event stream, got %d", normalRecorder.Code)
	}

	eventRequest := httptest.NewRequest(http.MethodGet, "/api/sse?token="+token, nil)
	eventRecorder := httptest.NewRecorder()
	handler.ServeHTTP(eventRecorder, eventRequest)
	if eventRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected event stream query token rejection, got %d", eventRecorder.Code)
	}

	headerRequest := httptest.NewRequest(http.MethodGet, "/api/sse", nil)
	headerRequest.Header.Set("Authorization", "Bearer "+token)
	headerRecorder := httptest.NewRecorder()
	handler.ServeHTTP(headerRecorder, headerRequest)
	if headerRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected Authorization header acceptance on event stream, got %d", headerRecorder.Code)
	}
}
