package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SemanticConfirmationRepository struct{ db *sql.DB }

func NewSemanticConfirmationRepository(db *sql.DB) *SemanticConfirmationRepository {
	return &SemanticConfirmationRepository{db: db}
}

func (r *SemanticConfirmationRepository) Create(ctx context.Context, confirmation *domain.SemanticConfirmation) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO semantic_confirmations (id, profile_id, workspace_id, session_id, confirmed_by, confirmation_receipt_id, provenance, scope, overrides_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		confirmation.ID, confirmation.ProfileID, confirmation.WorkspaceID, confirmation.SessionID, confirmation.ConfirmedBy, confirmation.ConfirmationReceiptID, string(confirmation.Provenance), string(confirmation.Scope), confirmation.OverridesJSON, confirmation.CreatedAt)
	return err
}

func (r *SemanticConfirmationRepository) ListByProfile(ctx context.Context, profileID string) ([]domain.SemanticConfirmation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, profile_id, workspace_id, session_id, confirmed_by, confirmation_receipt_id, provenance, scope, overrides_json, created_at FROM semantic_confirmations WHERE profile_id = ? ORDER BY created_at DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.SemanticConfirmation
	for rows.Next() {
		var c domain.SemanticConfirmation
		var scope, provenance string
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.WorkspaceID, &c.SessionID, &c.ConfirmedBy, &c.ConfirmationReceiptID, &provenance, &scope, &c.OverridesJSON, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Scope = domain.ConfirmationScope(scope)
		c.Provenance = domain.ConfirmationProvenance(provenance)
		results = append(results, c)
	}
	return results, rows.Err()
}

func (r *SemanticConfirmationRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticConfirmation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, profile_id, workspace_id, session_id, confirmed_by, confirmation_receipt_id, provenance, scope, overrides_json, created_at FROM semantic_confirmations WHERE session_id = ? ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.SemanticConfirmation
	for rows.Next() {
		var c domain.SemanticConfirmation
		var scope, provenance string
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.WorkspaceID, &c.SessionID, &c.ConfirmedBy, &c.ConfirmationReceiptID, &provenance, &scope, &c.OverridesJSON, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Scope = domain.ConfirmationScope(scope)
		c.Provenance = domain.ConfirmationProvenance(provenance)
		results = append(results, c)
	}
	return results, rows.Err()
}

func (r *SemanticConfirmationRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticConfirmation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, profile_id, workspace_id, session_id, confirmed_by, confirmation_receipt_id, provenance, scope, overrides_json, created_at FROM semantic_confirmations WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.SemanticConfirmation
	for rows.Next() {
		var c domain.SemanticConfirmation
		var scope, provenance string
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.WorkspaceID, &c.SessionID, &c.ConfirmedBy, &c.ConfirmationReceiptID, &provenance, &scope, &c.OverridesJSON, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Scope = domain.ConfirmationScope(scope)
		c.Provenance = domain.ConfirmationProvenance(provenance)
		results = append(results, c)
	}
	return results, rows.Err()
}

func (r *SemanticConfirmationRepository) DeleteByProfile(ctx context.Context, profileID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM semantic_confirmations WHERE profile_id = ?`, profileID)
	return err
}
