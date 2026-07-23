package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterHandlerValidations(t *testing.T) {
	// Weak password test
	reqBody, _ := json.Marshal(map[string]string{
		"name":     "Test User",
		"email":    "test@example.com",
		"password": "123",
	})
	req := httptest.NewRequest("POST", "/api/auth/register", bytes.NewReader(reqBody))
	rr := httptest.NewRecorder()

	RegisterHandler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for weak password, got %d", rr.Code)
	}
}
