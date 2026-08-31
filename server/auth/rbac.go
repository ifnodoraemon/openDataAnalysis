package auth

import (
	"log"
	"net/http"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

// RoleWeight orders workspace roles for minimum-role comparisons.
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

// HasMinRole reports whether actualRole satisfies minRole.
func HasMinRole(actualRole, minRole domain.WorkspaceRole) bool {
	return RoleWeight(actualRole) >= RoleWeight(minRole)
}

// RequireWorkspaceRole creates an HTTP middleware verifying that the
// authenticated user belongs to the workspace in r.Context() with at least
// minRole.
func RequireWorkspaceRole(workspaceRepo repository.WorkspaceRepository, minRole domain.WorkspaceRole) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := FromContext(r.Context())
			if !ok || identity.UserID == "" || identity.WorkspaceID == "" {
				writeAuthError(w, http.StatusUnauthorized, "未登录")
				return
			}

			role, isMember, err := workspaceRepo.GetMemberRole(r.Context(), identity.WorkspaceID, identity.UserID)
			if err != nil {
				log.Printf("rbac: verify workspace membership workspace=%s user=%s err=%v", identity.WorkspaceID, identity.UserID, err)
				writeAuthError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !isMember {
				writeAuthError(w, http.StatusForbidden, "无权访问此工作空间")
				return
			}
			if !HasMinRole(role, minRole) {
				writeAuthError(w, http.StatusForbidden, "当前工作空间角色权限不足")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
