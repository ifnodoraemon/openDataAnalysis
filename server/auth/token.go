package auth

import (
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
	secret []byte
}

func NewTokenManager(secret string) *TokenManager {
	return &TokenManager{secret: []byte(secret)}
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
	_, _ = mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

func (m *TokenManager) Parse(token string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Identity{}, errors.New("invalid token format")
	}

	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return Identity{}, errors.New("invalid token signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, fmt.Errorf("token parse failed: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, fmt.Errorf("token claims parse failed: %w", err)
	}
	if claims.ExpiresAt < time.Now().Unix() {
		return Identity{}, errors.New("token expired")
	}
	if claims.Issuer != "" && claims.Issuer != TokenIssuer {
		return Identity{}, errors.New("invalid token issuer")
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
