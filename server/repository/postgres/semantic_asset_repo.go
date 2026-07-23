package postgres

import (
	"context"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type SemanticAssetRepository struct {
	db DBTX
}

func NewSemanticAssetRepository(db DBTX) *SemanticAssetRepository {
	return &SemanticAssetRepository{db: db}
}

func (r *SemanticAssetRepository) Upsert(ctx context.Context, asset *domain.SemanticAsset) error {
	query := `
		INSERT INTO semantic_assets (
			id, workspace_id, source_id, schema_signature, asset_kind, asset_key, asset_value_json,
			created_from_profile_id, created_from_confirmation_id, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT(workspace_id, schema_signature, asset_kind, asset_key) DO UPDATE SET
			source_id = EXCLUDED.source_id,
			asset_value_json = EXCLUDED.asset_value_json,
			created_from_profile_id = EXCLUDED.created_from_profile_id,
			created_from_confirmation_id = EXCLUDED.created_from_confirmation_id,
			created_by = EXCLUDED.created_by,
			updated_at = EXCLUDED.updated_at`

	_, err := r.db.Exec(ctx, query,
		asset.ID, asset.WorkspaceID, asset.SourceID, asset.SchemaSignature, string(asset.AssetKind), asset.AssetKey, asset.AssetValueJSON,
		asset.CreatedFromProfileID, asset.CreatedFromConfirmationID, asset.CreatedBy, asset.CreatedAt, asset.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert semantic asset: %w", err)
	}
	return nil
}

func (r *SemanticAssetRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticAsset, error) {
	query := `
		SELECT id, workspace_id, source_id, schema_signature, asset_kind, asset_key, asset_value_json,
		       created_from_profile_id, created_from_confirmation_id, created_by, created_at, updated_at
		FROM semantic_assets
		WHERE workspace_id = $1
		ORDER BY updated_at DESC, asset_kind ASC, asset_key ASC`

	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic assets by workspace: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticAsset)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic assets: %w", err)
	}
	return results, nil
}

func (r *SemanticAssetRepository) ListBySchema(ctx context.Context, workspaceID, schemaSignature string) ([]domain.SemanticAsset, error) {
	query := `
		SELECT id, workspace_id, source_id, schema_signature, asset_kind, asset_key, asset_value_json,
		       created_from_profile_id, created_from_confirmation_id, created_by, created_at, updated_at
		FROM semantic_assets
		WHERE workspace_id = $1 AND schema_signature = $2
		ORDER BY asset_kind ASC, asset_key ASC`

	rows, err := r.db.Query(ctx, query, workspaceID, schemaSignature)
	if err != nil {
		return nil, fmt.Errorf("failed to query semantic assets by schema: %w", err)
	}

	results, err := pgx.CollectRows(rows, scanSemanticAsset)
	if err != nil {
		return nil, fmt.Errorf("failed to collect semantic assets: %w", err)
	}
	return results, nil
}

func scanSemanticAsset(row pgx.CollectableRow) (domain.SemanticAsset, error) {
	var asset domain.SemanticAsset
	var kind string
	err := row.Scan(
		&asset.ID, &asset.WorkspaceID, &asset.SourceID, &asset.SchemaSignature, &kind, &asset.AssetKey, &asset.AssetValueJSON,
		&asset.CreatedFromProfileID, &asset.CreatedFromConfirmationID, &asset.CreatedBy, &asset.CreatedAt, &asset.UpdatedAt,
	)
	asset.AssetKind = domain.SemanticAssetKind(kind)
	return asset, err
}
