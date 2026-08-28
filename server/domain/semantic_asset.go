package domain

import "time"

type SemanticAssetKind string

const (
	SemanticAssetKindPatch SemanticAssetKind = "patch"
)

type SemanticAsset struct {
	ID                        string
	WorkspaceID               string
	SourceID                  string
	SchemaSignature           string
	AssetKind                 SemanticAssetKind
	AssetKey                  string
	AssetValueJSON            string
	CreatedFromProfileID      string
	CreatedFromConfirmationID string
	CreatedBy                 string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}
