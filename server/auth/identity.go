package auth

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type Identity struct {
	UserID      string `json:"userId"`
	UserName    string `json:"userName"`
	UserEmail   string `json:"userEmail"`
	WorkspaceID string `json:"workspaceId"`
	Workspace   string `json:"workspaceName"`
}

type contextKey string

const identityKey contextKey = "identity"

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

func FromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey).(Identity)
	return identity, ok
}

func Middleware(tokenManager *TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				token = strings.TrimSpace(authHeader[7:])
			}
			if token == "" {
				if cookie, err := r.Cookie("oda_token"); err == nil {
					token = cookie.Value
				}
			}
			if token == "" || tokenManager == nil {
				writeAuthError(w, http.StatusUnauthorized, "未登录")
				return
			}

			identity, err := tokenManager.Parse(token)
			if err != nil {
				log.Printf("invalid authentication token: %v", err)
				writeAuthError(w, http.StatusUnauthorized, "登录凭证无效或已过期")
				return
			}
			ctx := WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	}); err != nil {
		log.Printf("write authentication error response: %v", err)
	}
}
