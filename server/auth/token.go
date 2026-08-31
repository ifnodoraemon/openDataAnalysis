package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const TokenIssuer = "openDataAnalysis"

type Claims struct {
	JWTID       string `json:"jti,omitempty"`
	Issuer      string `json:"iss,omitempty"`
	Subject     string `json:"sub,omitempty"`
	UserID      string `json:"userId"`
	UserName    string `json:"userName"`
	UserEmail   string `json:"userEmail"`
	WorkspaceID string `json:"workspaceId"`
	Workspace   string `json:"workspaceName"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

type TokenManager struct {
	secret  []byte
	revoker *Revoker
	store   RevocationStore
}

// RevocationStore persists token revocations so the jti denylist survives
// process restarts. Implementations must be safe for concurrent use.
type RevocationStore interface {
	SaveRevocation(ctx context.Context, jti string, expiresAt time.Time) error
	LoadRevocations(ctx context.Context) (map[string]time.Time, error)
	PruneRevocations(ctx context.Context, now time.Time) error
}

func NewTokenManager(secret string) *TokenManager {
	return &TokenManager{secret: []byte(secret), revoker: NewRevoker()}
}

// SetRevocationStore attaches a persistence backend for revocations.
func (m *TokenManager) SetRevocationStore(store RevocationStore) {
	m.store = store
}

// LoadRevocations prunes expired persisted rows and restores the active
// denylist from the store. It must be called before the server starts
// accepting traffic.
func (m *TokenManager) LoadRevocations(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	if err := m.store.PruneRevocations(ctx, time.Now()); err != nil {
		return fmt.Errorf("prune revoked tokens: %w", err)
	}
	revocations, err := m.store.LoadRevocations(ctx)
	if err != nil {
		return fmt.Errorf("load revoked tokens: %w", err)
	}
	m.revoker.ReplaceAll(revocations)
	return nil
}

func (m *TokenManager) Sign(identity Identity, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		JWTID:       uuid.New().String(),
		Issuer:      TokenIssuer,
		Subject:     identity.UserID,
		UserID:      identity.UserID,
		UserName:    identity.UserName,
		UserEmail:   identity.UserEmail,
		WorkspaceID: identity.WorkspaceID,
		Workspace:   identity.Workspace,
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, m.secret)
	if _, err := mac.Write([]byte(encodedPayload)); err != nil {
		return "", fmt.Errorf("sign token payload: %w", err)
	}
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

func (m *TokenManager) Parse(token string) (Identity, error) {
	claims, err := m.verifiedClaims(token)
	if err != nil {
		return Identity{}, err
	}
	if claims.ExpiresAt < time.Now().Unix() {
		return Identity{}, errors.New("token expired")
	}
	if claims.Issuer != TokenIssuer {
		return Identity{}, errors.New("invalid token issuer")
	}
	if claims.JWTID != "" && m.revoker.IsRevoked(claims.JWTID) {
		return Identity{}, errors.New("token revoked")
	}

	userID := claims.UserID
	if userID == "" {
		userID = claims.Subject
	}

	return Identity{
		UserID:      userID,
		UserName:    claims.UserName,
		UserEmail:   claims.UserEmail,
		WorkspaceID: claims.WorkspaceID,
		Workspace:   claims.Workspace,
	}, nil
}

func (m *TokenManager) Revoke(token string) error {
	claims, err := m.verifiedClaims(token)
	if err != nil {
		return err
	}
	if claims.ExpiresAt <= time.Now().Unix() {
		return errors.New("token expired")
	}
	if claims.JWTID == "" {
		return errors.New("token has no jti")
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	m.revoker.Revoke(claims.JWTID, expiresAt)
	if m.store != nil {
		if err := m.store.SaveRevocation(context.Background(), claims.JWTID, expiresAt); err != nil {
			// The in-memory denylist still enforces the revocation for this
			// process; surface the persistence failure to the caller.
			return fmt.Errorf("persist token revocation: %w", err)
		}
	}
	return nil
}

func (m *TokenManager) verifiedClaims(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, errors.New("invalid token format")
	}

	mac := hmac.New(sha256.New, m.secret)
	if _, err := mac.Write([]byte(parts[0])); err != nil {
		return Claims{}, fmt.Errorf("verify token signature: %w", err)
	}
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return Claims{}, errors.New("invalid token signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("token parse failed: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, fmt.Errorf("token claims parse failed: %w", err)
	}
	return claims, nil
}
