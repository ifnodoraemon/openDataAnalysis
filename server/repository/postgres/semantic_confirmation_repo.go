package postgres

import (
	"context"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type SemanticConfirmationRepository struct {
	db DBTX
}

func NewSemanticConfirmationRepository(db DBTX) *SemanticConfirmationRepository {
	return &SemanticConfirmationRepository{db: db}
}

func (r *SemanticConfirmationRepository) Create(ctx context.Context, confirmation *domain.SemanticConfirmation) error {
	query := `INSERT INTO semantic_confirmations (id, profile_id, workspace_id, session_id, confirmed_by, scope, overrides_json, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.db.Exec(ctx, query,
		confirmation.ID, confirmation.ProfileID, confirmation.WorkspaceID, confirmation.SessionID, confirmation.ConfirmedBy, string(confirmation.Scope), confirmation.OverridesJSON, confirmation.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to create semantic confirmation: %w", err)
	}
	return nil
}

func (r *SemanticConfirmationRepository) ListByProfile(ctx context.Context, profileID string) ([]domain.SemanticConfirmation, error) {
	query := `SELECT id, profile_id, workspace_id, session_id, confirmed_by, scope, overrides_json, created_at
		FROM semantic_confirmations WHERE profile_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, profileID)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic confirmations by profile: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticConfirmation)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic confirmations: %w", err)
	}
	return results, nil
}

func (r *SemanticConfirmationRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticConfirmation, error) {
	query := `SELECT id, profile_id, workspace_id, session_id, confirmed_by, scope, overrides_json, created_at
		FROM semantic_confirmations WHERE session_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic confirmations by session: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticConfirmation)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic confirmations: %w", err)
	}
	return results, nil
}

func (r *SemanticConfirmationRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticConfirmation, error) {
	query := `SELECT id, profile_id, workspace_id, session_id, confirmed_by, scope, overrides_json, created_at
		FROM semantic_confirmations WHERE workspace_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic confirmations by workspace: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticConfirmation)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic confirmations: %w", err)
	}
	return results, nil
}

func (r *SemanticConfirmationRepository) DeleteByProfile(ctx context.Context, profileID string) error {
	query := `DELETE FROM semantic_confirmations WHERE profile_id = $1`

	_, err := r.db.Exec(ctx, query, profileID)
	if err != nil {
		return fmt.Errorf("failed to delete semantic confirmations by profile: %w", err)
	}
	return nil
}

func scanSemanticConfirmation(row pgx.CollectableRow) (domain.SemanticConfirmation, error) {
	var c domain.SemanticConfirmation
	var scope string
	err := row.Scan(&c.ID, &c.ProfileID, &c.WorkspaceID, &c.SessionID, &c.ConfirmedBy, &scope, &c.OverridesJSON, &c.CreatedAt)
	c.Scope = domain.ConfirmationScope(scope)
	return c, err
}
