package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type WorkspaceRepository struct {
	db DBTX
}

func NewWorkspaceRepository(db DBTX) *WorkspaceRepository {
	return &WorkspaceRepository{db: db}
}

func (r *WorkspaceRepository) GetByID(ctx context.Context, workspaceID string) (*domain.Workspace, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, name, slug, owner_user_id, status, created_at, updated_at FROM workspaces WHERE id = $1`,
		workspaceID,
	)
	var workspace domain.Workspace
	var status string
	err := row.Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.OwnerUserID, &status, &workspace.CreatedAt, &workspace.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace by id: %w", normalizeLookupError(err))
	}
	workspace.Status = domain.WorkspaceStatus(status)
	return &workspace, nil
}

func (r *WorkspaceRepository) ListByUser(ctx context.Context, userID string) ([]domain.Workspace, error) {
	rows, err := r.db.Query(ctx, `
		SELECT w.id, w.name, w.slug, w.owner_user_id, w.status, w.created_at, w.updated_at
		FROM workspaces w
		INNER JOIN workspace_members wm ON wm.workspace_id = w.id
		WHERE wm.user_id = $1
		ORDER BY w.created_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces by user: %w", err)
	}
	defer rows.Close()

	var workspaces []domain.Workspace
	for rows.Next() {
		var workspace domain.Workspace
		var status string
		if err := rows.Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.OwnerUserID, &status, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan workspace: %w", err)
		}
		workspace.Status = domain.WorkspaceStatus(status)
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating workspaces: %w", err)
	}
	return workspaces, nil
}

func (r *WorkspaceRepository) IsMember(ctx context.Context, workspaceID, userID string) (bool, error) {
	row := r.db.QueryRow(ctx, `SELECT COUNT(1) FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("failed to check workspace member: %w", err)
	}
	return count > 0, nil
}

func (r *WorkspaceRepository) GetMemberRole(ctx context.Context, workspaceID, userID string) (domain.WorkspaceRole, bool, error) {
	row := r.db.QueryRow(ctx, `SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
	var role string
	if err := row.Scan(&role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to read workspace member role: %w", err)
	}
	return domain.WorkspaceRole(role), true, nil
}

func (r *WorkspaceRepository) CreateWorkspace(ctx context.Context, workspace *domain.Workspace) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_user_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		workspace.ID, workspace.Name, workspace.Slug, workspace.OwnerUserID, string(workspace.Status), workspace.CreatedAt, workspace.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create workspace: %w", err)
	}
	return nil
}

func (r *WorkspaceRepository) AddMember(ctx context.Context, member *domain.WorkspaceMember) error {
	if member.CreatedAt.IsZero() {
		member.CreatedAt = time.Now()
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`,
		member.WorkspaceID, member.UserID, string(member.Role), member.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to add workspace member: %w", err)
	}
	return nil
}
