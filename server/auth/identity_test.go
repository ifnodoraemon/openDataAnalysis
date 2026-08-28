package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddlewareAcceptsQueryTokenOnlyForEventStream(t *testing.T) {
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
	if eventRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected event stream query token acceptance, got %d", eventRecorder.Code)
	}
}
