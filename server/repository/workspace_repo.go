package repository

import (
	"context"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type WorkspaceRepository interface {
	GetByID(ctx context.Context, workspaceID string) (*domain.Workspace, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Workspace, error)
	IsMember(ctx context.Context, workspaceID, userID string) (bool, error)
	GetMemberRole(ctx context.Context, workspaceID, userID string) (domain.WorkspaceRole, bool, error)
	CreateWorkspace(ctx context.Context, workspace *domain.Workspace) error
	AddMember(ctx context.Context, member *domain.WorkspaceMember) error
}
