package domain

import "time"

type SemanticAssetKind string

const (
	SemanticAssetKindTimeColumn       SemanticAssetKind = "time_column"
	SemanticAssetKindMetricDefinition SemanticAssetKind = "metric_definition"
	SemanticAssetKindUnitAnnotation   SemanticAssetKind = "unit_annotation"
	SemanticAssetKindJoinCandidate    SemanticAssetKind = "join_candidate"
	SemanticAssetKindOverride         SemanticAssetKind = "override"
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
