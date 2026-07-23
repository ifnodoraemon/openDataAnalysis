package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type SemanticProfileRepository struct {
	db DBTX
}

func NewSemanticProfileRepository(db DBTX) *SemanticProfileRepository {
	return &SemanticProfileRepository{db: db}
}

func (r *SemanticProfileRepository) Create(ctx context.Context, profile *domain.SemanticProfile) error {
	query := `INSERT INTO semantic_profiles (id, session_id, source_id, snapshot_id, analysis_table_name, schema_signature, profile_status, profile_json, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, query,
		profile.ID, profile.SessionID, profile.SourceID, profile.SnapshotID, profile.AnalysisTableName, profile.SchemaSignature, string(profile.ProfileStatus), profile.ProfileJSON, profile.CreatedAt, profile.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create semantic profile: %w", err)
	}
	return nil
}

func (r *SemanticProfileRepository) GetByID(ctx context.Context, id string) (*domain.SemanticProfile, error) {
	query := `SELECT id, session_id, source_id, snapshot_id, analysis_table_name, schema_signature, profile_status, profile_json, created_at, updated_at FROM semantic_profiles WHERE id = $1`

	row := r.db.QueryRow(ctx, query, id)
	var p domain.SemanticProfile
	var profileStatus string
	if err := row.Scan(&p.ID, &p.SessionID, &p.SourceID, &p.SnapshotID, &p.AnalysisTableName, &p.SchemaSignature, &profileStatus, &p.ProfileJSON, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("failed to get semantic profile by id: %w", err)
	}
	p.ProfileStatus = domain.ProfileStatus(profileStatus)
	return &p, nil
}

func (r *SemanticProfileRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticProfile, error) {
	query := `SELECT id, session_id, source_id, snapshot_id, analysis_table_name, schema_signature, profile_status, profile_json, created_at, updated_at FROM semantic_profiles WHERE session_id = $1 ORDER BY created_at ASC`

	rows, err := r.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic profiles by session: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticProfile)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic profiles: %w", err)
	}
	return results, nil
}

func (r *SemanticProfileRepository) ListBySource(ctx context.Context, sourceID string) ([]domain.SemanticProfile, error) {
	query := `SELECT id, session_id, source_id, snapshot_id, analysis_table_name, schema_signature, profile_status, profile_json, created_at, updated_at FROM semantic_profiles WHERE source_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, sourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic profiles by source: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticProfile)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic profiles: %w", err)
	}
	return results, nil
}

func (r *SemanticProfileRepository) UpdateStatus(ctx context.Context, id string, status domain.ProfileStatus) error {
	query := `UPDATE semantic_profiles SET profile_status = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`

	_, err := r.db.Exec(ctx, query, string(status), id)
	if err != nil {
		return fmt.Errorf("failed to update semantic profile status: %w", err)
	}
	return nil
}

func (r *SemanticProfileRepository) UpdateProfileJSON(ctx context.Context, id string, profileJSON string) error {
	query := `UPDATE semantic_profiles SET profile_json = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`

	_, err := r.db.Exec(ctx, query, profileJSON, id)
	if err != nil {
		return fmt.Errorf("failed to update semantic profile json: %w", err)
	}
	return nil
}

func (r *SemanticProfileRepository) FindWorkspaceConfirmation(ctx context.Context, workspaceID, schemaSignature string) (*domain.SemanticConfirmation, error) {
	query := `SELECT sc.id, sc.profile_id, sc.workspace_id, sc.session_id, sc.confirmed_by, sc.scope, sc.overrides_json, sc.created_at
		FROM semantic_confirmations sc
		JOIN semantic_profiles sp ON sp.id = sc.profile_id
		JOIN data_sources ds ON ds.id = sp.source_id
		WHERE ds.workspace_id = $1 AND sp.schema_signature = $2 AND sc.scope = 'workspace'
		ORDER BY sc.created_at DESC LIMIT 1`

	row := r.db.QueryRow(ctx, query, workspaceID, schemaSignature)
	var c domain.SemanticConfirmation
	var scope string
	if err := row.Scan(&c.ID, &c.ProfileID, &c.WorkspaceID, &c.SessionID, &c.ConfirmedBy, &scope, &c.OverridesJSON, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find workspace confirmation: %w", err)
	}
	c.Scope = domain.ConfirmationScope(scope)
	return &c, nil
}

func (r *SemanticProfileRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM semantic_profiles WHERE id = $1`

	_, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete semantic profile: %w", err)
	}
	return nil
}

func scanSemanticProfile(row pgx.CollectableRow) (domain.SemanticProfile, error) {
	var p domain.SemanticProfile
	var profileStatus string
	err := row.Scan(&p.ID, &p.SessionID, &p.SourceID, &p.SnapshotID, &p.AnalysisTableName, &p.SchemaSignature, &profileStatus, &p.ProfileJSON, &p.CreatedAt, &p.UpdatedAt)
	p.ProfileStatus = domain.ProfileStatus(profileStatus)
	return p, err
}
