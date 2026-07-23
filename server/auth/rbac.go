package auth

import (
	"net/http"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

// RoleWeight assigns numerical weight to WorkspaceRole for hierarchy checking.
func RoleWeight(role domain.WorkspaceRole) int {
	switch role {
	case domain.WorkspaceRoleOwner:
		return 3
	case domain.WorkspaceRoleAdmin:
		return 2
	case domain.WorkspaceRoleMember:
		return 1
	default:
		return 0
	}
}

// HasMinRole checks if actualRole meets or exceeds required minRole.
func HasMinRole(actualRole, minRole domain.WorkspaceRole) bool {
	return RoleWeight(actualRole) >= RoleWeight(minRole)
}

// RequireWorkspaceRole creates an HTTP middleware verifying that the authenticated user
// belongs to the workspace in r.Context() with at least minRole.
func RequireWorkspaceRole(workspaceRepo repository.WorkspaceRepository, minRole domain.WorkspaceRole) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := FromContext(r.Context())
			if !ok || identity.UserID == "" || identity.WorkspaceID == "" {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized: missing identity context")
				return
			}

			isMember, err := workspaceRepo.IsMember(r.Context(), identity.WorkspaceID, identity.UserID)
			if err != nil {
				writeAuthError(w, http.StatusInternalServerError, "failed to verify workspace membership")
				return
			}
			if !isMember {
				writeAuthError(w, http.StatusForbidden, "user not a member of active workspace")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
