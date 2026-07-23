package auth

import (
	"context"
	"encoding/json"
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
				token = strings.TrimSpace(r.URL.Query().Get("token"))
			}
			if token == "" {
				if cookie, err := r.Cookie("oda_token"); err == nil {
					token = cookie.Value
				}
			}
			if token == "" {
				wsProtos := r.Header.Get("Sec-WebSocket-Protocol")
				if wsProtos != "" {
					parts := strings.Split(wsProtos, ",")
					for _, p := range parts {
						p = strings.TrimSpace(p)
						if strings.HasPrefix(p, "token-") {
							token = strings.TrimPrefix(p, "token-")
							break
						}
					}
				}
			}
			if token == "" || tokenManager == nil {
				writeAuthError(w, http.StatusUnauthorized, "not authenticated")
				return
			}

			identity, err := tokenManager.Parse(token)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, err.Error())
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
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
