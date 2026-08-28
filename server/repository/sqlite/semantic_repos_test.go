package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
)

func TestSemanticAssetRepositoryUpsertAndList(t *testing.T) {
	t.Parallel()

	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	repo := NewSemanticAssetRepository(store.DB)
	now := time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)
	asset := &domain.SemanticAsset{
		ID:                        "sa_latency",
		WorkspaceID:               "ws_1",
		SourceID:                  "src_1",
		SchemaSignature:           "sig_1",
		AssetKind:                 domain.SemanticAssetKindPatch,
		AssetKey:                  "latency_definition",
		AssetValueJSON:            `{"latency_definition":"p95 latency"}`,
		CreatedFromProfileID:      "sp_1",
		CreatedFromConfirmationID: "sc_1",
		CreatedBy:                 "user_1",
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if err := repo.Upsert(context.Background(), asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}

	asset.SourceID = "src_2"
	asset.AssetValueJSON = `{"latency_definition":"p99 latency"}`
	asset.UpdatedAt = now.Add(time.Minute)
	if err := repo.Upsert(context.Background(), asset); err != nil {
		t.Fatalf("upsert asset: %v", err)
	}

	assets, err := repo.ListBySchema(context.Background(), "ws_1", "sig_1")
	if err != nil {
		t.Fatalf("list by schema: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected one upserted asset, got %#v", assets)
	}
	got := assets[0]
	if got.ID != "sa_latency" || got.SourceID != "src_2" || got.AssetValueJSON != asset.AssetValueJSON {
		t.Fatalf("asset was not updated on conflict: %#v", got)
	}

	workspaceAssets, err := repo.ListByWorkspace(context.Background(), "ws_1")
	if err != nil {
		t.Fatalf("list by workspace: %v", err)
	}
	if len(workspaceAssets) != 1 || workspaceAssets[0].AssetKey != "latency_definition" {
		t.Fatalf("unexpected workspace assets: %#v", workspaceAssets)
	}
}

func TestAuditEventRepositoryCreateAndListByWorkspace(t *testing.T) {
	t.Parallel()

	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })

	repo := NewAuditEventRepository(store.DB)
	older := domain.AuditEvent{
		ID:           "ae_old",
		WorkspaceID:  "ws_1",
		SessionID:    "s_1",
		ActorUserID:  "user_1",
		EventType:    "semantic_profile_confirmed",
		ResourceType: "semantic_profile",
		ResourceID:   "sp_1",
		PayloadJSON:  `{"scope":"workspace"}`,
		CreatedAt:    time.Date(2026, 6, 24, 8, 0, 0, 0, time.UTC),
	}
	newer := older
	newer.ID = "ae_new"
	newer.EventType = "semantic_asset_upserted"
	newer.ResourceType = "semantic_asset"
	newer.ResourceID = "metric:latency_ms"
	newer.CreatedAt = older.CreatedAt.Add(time.Hour)

	if err := repo.Create(context.Background(), &older); err != nil {
		t.Fatalf("create older event: %v", err)
	}
	if err := repo.Create(context.Background(), &newer); err != nil {
		t.Fatalf("create newer event: %v", err)
	}

	events, err := repo.ListByWorkspace(context.Background(), "ws_1", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two events, got %#v", events)
	}
	if events[0].ID != "ae_new" || events[1].ID != "ae_old" {
		t.Fatalf("expected newest event first, got %#v", events)
	}

	limited, err := repo.ListByWorkspace(context.Background(), "ws_1", 1)
	if err != nil {
		t.Fatalf("list limited events: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "ae_new" {
		t.Fatalf("unexpected limited events: %#v", limited)
	}
}
