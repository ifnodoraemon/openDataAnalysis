package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type WorkspaceRepository struct{ db *sql.DB }

func NewWorkspaceRepository(db *sql.DB) *WorkspaceRepository { return &WorkspaceRepository{db: db} }
func (r *WorkspaceRepository) GetByID(ctx context.Context, workspaceID string) (*domain.Workspace, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, name, slug, owner_user_id, status, created_at, updated_at FROM workspaces WHERE id = ?`, workspaceID)
	var workspace domain.Workspace
	var status string
	if err := row.Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.OwnerUserID, &status, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	workspace.Status = domain.WorkspaceStatus(status)
	return &workspace, nil
}

func (r *WorkspaceRepository) ListByUser(ctx context.Context, userID string) ([]domain.Workspace, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT w.id, w.name, w.slug, w.owner_user_id, w.status, w.created_at, w.updated_at
		FROM workspaces w
		INNER JOIN workspace_members wm ON wm.workspace_id = w.id
		WHERE wm.user_id = ?
		ORDER BY w.created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []domain.Workspace
	for rows.Next() {
		var workspace domain.Workspace
		var status string
		if err := rows.Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.OwnerUserID, &status, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
			return nil, err
		}
		workspace.Status = domain.WorkspaceStatus(status)
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

func (r *WorkspaceRepository) IsMember(ctx context.Context, workspaceID, userID string) (bool, error) {
	row := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *WorkspaceRepository) GetMemberRole(ctx context.Context, workspaceID, userID string) (domain.WorkspaceRole, bool, error) {
	row := r.db.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	var role string
	if err := row.Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return domain.WorkspaceRole(role), true, nil
}

func (r *WorkspaceRepository) CreateWorkspace(ctx context.Context, workspace *domain.Workspace) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, owner_user_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspace.ID, workspace.Name, workspace.Slug, workspace.OwnerUserID, string(workspace.Status), workspace.CreatedAt, workspace.UpdatedAt)
	return err
}

func (r *WorkspaceRepository) AddMember(ctx context.Context, member *domain.WorkspaceMember) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		member.WorkspaceID, member.UserID, string(member.Role), member.CreatedAt)
	return err
}
