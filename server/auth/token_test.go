package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func signRawClaims(t *testing.T, tm *TokenManager, claims Claims) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, tm.secret)
	if _, err := mac.Write([]byte(encodedPayload)); err != nil {
		t.Fatalf("sign claims: %v", err)
	}
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestTokenManager_SignAndParse(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	identity := Identity{
		UserID:      "user1",
		UserName:    "Test User",
		UserEmail:   "test@example.com",
		WorkspaceID: "ws1",
		Workspace:   "Default",
	}

	token, err := tm.Sign(identity, 1*time.Hour)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}

	parsed, err := tm.Parse(token)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if parsed.UserID != identity.UserID {
		t.Errorf("UserID mismatch: got %q, want %q", parsed.UserID, identity.UserID)
	}
	if parsed.WorkspaceID != identity.WorkspaceID {
		t.Errorf("WorkspaceID mismatch: got %q, want %q", parsed.WorkspaceID, identity.WorkspaceID)
	}
}

func TestTokenManager_ExpiredToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	identity := Identity{UserID: "user1", UserName: "Test", UserEmail: "t@e.com", WorkspaceID: "ws1", Workspace: "W"}

	token, err := tm.Sign(identity, -1*time.Second)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	_, err = tm.Parse(token)
	if err == nil {
		t.Error("expected error for expired token, got nil")
	}
}

func TestTokenManager_InvalidSignature(t *testing.T) {
	tm1 := NewTokenManager("secret-1")
	tm2 := NewTokenManager("secret-2")
	identity := Identity{UserID: "user1", UserName: "Test", UserEmail: "t@e.com", WorkspaceID: "ws1", Workspace: "W"}

	token, _ := tm1.Sign(identity, 1*time.Hour)
	_, err := tm2.Parse(token)
	if err == nil {
		t.Error("expected error for invalid signature, got nil")
	}
}

func TestTokenManager_MalformedToken(t *testing.T) {
	tm := NewTokenManager("secret")

	_, err := tm.Parse("not.a.valid-token-format")
	if err == nil {
		t.Error("expected error for malformed token, got nil")
	}

	_, err = tm.Parse("no-dot")
	if err == nil {
		t.Error("expected error for token without dots, got nil")
	}
}

func TestTokenManager_RejectsMissingIssuer(t *testing.T) {
	tm := NewTokenManager("secret")
	token := signRawClaims(t, tm, Claims{
		UserID:      "user1",
		UserName:    "Test",
		UserEmail:   "t@e.com",
		WorkspaceID: "ws1",
		Workspace:   "W",
		IssuedAt:    time.Now().Add(-time.Minute).Unix(),
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	})

	if _, err := tm.Parse(token); err == nil {
		t.Error("expected error for token without issuer, got nil")
	}
}

func TestTokenManager_RejectsWrongIssuer(t *testing.T) {
	tm := NewTokenManager("secret")
	token := signRawClaims(t, tm, Claims{
		Issuer:      "someone-else",
		UserID:      "user1",
		UserName:    "Test",
		UserEmail:   "t@e.com",
		WorkspaceID: "ws1",
		Workspace:   "W",
		IssuedAt:    time.Now().Add(-time.Minute).Unix(),
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	})

	if _, err := tm.Parse(token); err == nil {
		t.Error("expected error for token with wrong issuer, got nil")
	}
}

func TestTokenManager_RejectsRevokedToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	identity := Identity{UserID: "user1", UserName: "Test", UserEmail: "t@e.com", WorkspaceID: "ws1", Workspace: "W"}

	token, err := tm.Sign(identity, 1*time.Hour)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if _, err := tm.Parse(token); err != nil {
		t.Fatalf("expected fresh token to parse, got: %v", err)
	}
	if err := tm.Revoke(token); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if _, err := tm.Parse(token); err == nil {
		t.Error("expected revoked token to be rejected, got nil")
	}

	otherToken, err := tm.Sign(identity, 1*time.Hour)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if _, err := tm.Parse(otherToken); err != nil {
		t.Errorf("expected non-revoked token to parse, got: %v", err)
	}
}

func TestTokenManager_RevokeRejectsInvalidToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	if err := tm.Revoke("tampered.token"); err == nil {
		t.Error("expected revoke of invalid token to fail, got nil")
	}
}

func TestRevoker_ExpiryEviction(t *testing.T) {
	r := NewRevoker()
	now := time.Now()

	r.Revoke("active-jti", now.Add(time.Hour))

	r.mu.Lock()
	countActive := len(r.revoked)
	r.mu.Unlock()
	if countActive != 1 {
		t.Fatalf("expected 1 revoked jti, got %d", countActive)
	}

	r.Revoke("already-expired-jti", now.Add(-time.Second))
	r.mu.Lock()
	countAfterPastRevoke := len(r.revoked)
	r.mu.Unlock()
	if countAfterPastRevoke != 1 {
		t.Fatalf("expected expired revoke to be evicted immediately, got %d entries", countAfterPastRevoke)
	}

	r.mu.Lock()
	r.revoked["later-expired-jti"] = now.Add(-time.Second)
	r.mu.Unlock()

	if !r.IsRevoked("active-jti") {
		t.Error("expected active jti to remain revoked")
	}
	if r.IsRevoked("later-expired-jti") {
		t.Error("expected expired jti to be evicted, not reported revoked")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.revoked) != 1 {
		t.Fatalf("expected expired entry to be evicted, got %d entries", len(r.revoked))
	}
	if _, ok := r.revoked["later-expired-jti"]; ok {
		t.Fatal("expected expired jti to be removed from the map")
	}
}
