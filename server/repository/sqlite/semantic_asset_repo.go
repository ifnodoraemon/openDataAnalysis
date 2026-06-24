package sqlite

import (
	"context"
	"database/sql"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SemanticAssetRepository struct{ db *sql.DB }

func NewSemanticAssetRepository(db *sql.DB) *SemanticAssetRepository {
	return &SemanticAssetRepository{db: db}
}

func (r *SemanticAssetRepository) Upsert(ctx context.Context, asset *domain.SemanticAsset) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO semantic_assets (
			id, workspace_id, source_id, schema_signature, asset_kind, asset_key, asset_value_json,
			created_from_profile_id, created_from_confirmation_id, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, schema_signature, asset_kind, asset_key) DO UPDATE SET
			source_id = excluded.source_id,
			asset_value_json = excluded.asset_value_json,
			created_from_profile_id = excluded.created_from_profile_id,
			created_from_confirmation_id = excluded.created_from_confirmation_id,
			created_by = excluded.created_by,
			updated_at = excluded.updated_at`,
		asset.ID, asset.WorkspaceID, asset.SourceID, asset.SchemaSignature, string(asset.AssetKind), asset.AssetKey, asset.AssetValueJSON,
		asset.CreatedFromProfileID, asset.CreatedFromConfirmationID, asset.CreatedBy, asset.CreatedAt, asset.UpdatedAt)
	return err
}

func (r *SemanticAssetRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticAsset, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_id, source_id, schema_signature, asset_kind, asset_key, asset_value_json,
		       created_from_profile_id, created_from_confirmation_id, created_by, created_at, updated_at
		FROM semantic_assets
		WHERE workspace_id = ?
		ORDER BY updated_at DESC, asset_kind ASC, asset_key ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticAssets(rows)
}

func (r *SemanticAssetRepository) ListBySchema(ctx context.Context, workspaceID, schemaSignature string) ([]domain.SemanticAsset, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_id, source_id, schema_signature, asset_kind, asset_key, asset_value_json,
		       created_from_profile_id, created_from_confirmation_id, created_by, created_at, updated_at
		FROM semantic_assets
		WHERE workspace_id = ? AND schema_signature = ?
		ORDER BY asset_kind ASC, asset_key ASC`, workspaceID, schemaSignature)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticAssets(rows)
}

func scanSemanticAssets(rows *sql.Rows) ([]domain.SemanticAsset, error) {
	var results []domain.SemanticAsset
	for rows.Next() {
		var asset domain.SemanticAsset
		var kind string
		if err := rows.Scan(
			&asset.ID, &asset.WorkspaceID, &asset.SourceID, &asset.SchemaSignature, &kind, &asset.AssetKey, &asset.AssetValueJSON,
			&asset.CreatedFromProfileID, &asset.CreatedFromConfirmationID, &asset.CreatedBy, &asset.CreatedAt, &asset.UpdatedAt,
		); err != nil {
			return nil, err
		}
		asset.AssetKind = domain.SemanticAssetKind(kind)
		results = append(results, asset)
	}
	return results, rows.Err()
}
