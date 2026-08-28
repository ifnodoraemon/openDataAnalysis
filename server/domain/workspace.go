package domain

import "time"

type WorkspaceStatus string
type WorkspaceRole string

const (
	WorkspaceStatusActive WorkspaceStatus = "active"
	WorkspaceRoleOwner    WorkspaceRole   = "owner"
	WorkspaceRoleAdmin    WorkspaceRole   = "admin"
	WorkspaceRoleMember   WorkspaceRole   = "member"
)

type Workspace struct {
	ID          string
	Name        string
	Slug        string
	OwnerUserID string
	Status      WorkspaceStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type WorkspaceMember struct {
	ID          int64
	WorkspaceID string
	UserID      string
	Role        WorkspaceRole
	CreatedAt   time.Time
}
