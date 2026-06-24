package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func TestSelectProfileForSnapshotUsesActiveSessionSnapshot(t *testing.T) {
	t.Parallel()

	profiles := []domain.SemanticProfile{
		{ID: "sp_other_session", SessionID: "s_other", SnapshotID: "snap_active"},
		{ID: "sp_old", SessionID: "s_1", SnapshotID: "snap_old"},
		{ID: "sp_active", SessionID: "s_1", SnapshotID: "snap_active"},
	}

	got := selectProfileForSnapshot(profiles, "s_1", "snap_active")
	if got == nil || got.ID != "sp_active" {
		t.Fatalf("expected active session snapshot profile, got %#v", got)
	}
}

func TestRemoveResolvedAmbiguitiesUsesOverrideKeysOnly(t *testing.T) {
	t.Parallel()

	profile := `{
		"time_candidates":[{"column_name":"month"}],
		"metric_candidates":[{"column_name":"net_revenue"}],
		"ambiguities":[
			{"kind":"multiple_time_columns","candidates":["month","created_at"]},
			{"kind":"ambiguous_metrics","candidates":["gross_revenue","net_revenue"]},
			{"kind":"ambiguous_metrics","candidates":["cost_actual","cost_plan"]}
		]
	}`

	unchanged := removeResolvedAmbiguities(profile, []string{`{"note":"accepted"}`})
	if ambiguityKinds(t, unchanged)["multiple_time_columns"] == false || ambiguityKinds(t, unchanged)["ambiguous_metrics"] == false {
		t.Fatalf("expected irrelevant override not to resolve ambiguities, got %s", unchanged)
	}
	candidatesOnly := removeResolvedAmbiguities(profile, []string{`{
		"time_candidates":[{"column_name":"month"}],
		"metric_candidates":[{"column_name":"net_revenue"}]
	}`})
	if ambiguityCount(t, candidatesOnly) != 3 {
		t.Fatalf("expected candidate arrays not to resolve ambiguities, got %s", candidatesOnly)
	}

	partial := removeResolvedAmbiguities(profile, []string{`{"primary_time_column":"month"}`})
	kinds := ambiguityKinds(t, partial)
	if kinds["multiple_time_columns"] || !kinds["ambiguous_metrics"] {
		t.Fatalf("expected only time ambiguity resolved, got %s", partial)
	}

	oneMetricLeft := removeResolvedAmbiguities(profile, []string{
		`{"primary_time_column":"month"}`,
		`{"metric_definitions":{"net_revenue":"recognized net revenue"}}`,
	})
	kinds = ambiguityKinds(t, oneMetricLeft)
	if kinds["multiple_time_columns"] || !kinds["ambiguous_metrics"] || ambiguityCount(t, oneMetricLeft) != 1 {
		t.Fatalf("expected only matching metric ambiguity to remain, got %s", oneMetricLeft)
	}

	resolved := removeResolvedAmbiguities(profile, []string{
		`{"primary_time_column":"month"}`,
		`{"metric_definitions":{"net_revenue":"recognized net revenue","cost_actual":"actual cost"}}`,
	})
	if len(ambiguityKinds(t, resolved)) != 0 {
		t.Fatalf("expected all ambiguities resolved, got %s", resolved)
	}
	if profileStatusForJSON(resolved) != domain.ProfileStatusConfirmed {
		t.Fatalf("expected resolved profile to be confirmed")
	}
}

func TestApplyConfirmationsMergesInOrder(t *testing.T) {
	t.Parallel()

	profile := `{"primary_time_column":"created_at","metric_definitions":{"revenue":"gross"}}`
	confirmations := []domain.SemanticConfirmation{
		{
			Scope:         domain.ConfirmationScopeWorkspace,
			OverridesJSON: `{"primary_time_column":"month","metric_definitions":{"revenue":"net"}}`,
			CreatedAt:     time.Unix(1, 0),
		},
		{
			Scope:         domain.ConfirmationScopeSession,
			OverridesJSON: `{"metric_definitions":{"revenue":"recognized"}}`,
			CreatedAt:     time.Unix(2, 0),
		},
	}

	merged := applyConfirmationsToProfile(profile, confirmations)
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(merged), &payload); err != nil {
		t.Fatalf("parse merged profile: %v", err)
	}
	if payload["primary_time_column"] != "month" {
		t.Fatalf("expected workspace time override, got %#v", payload)
	}
	metricDefs := payload["metric_definitions"].(map[string]interface{})
	if metricDefs["revenue"] != "recognized" {
		t.Fatalf("expected later session metric override, got %#v", payload)
	}
}

func TestConfirmProfileRejectsInvalidOverridesJSONWithoutAmbiguities(t *testing.T) {
	t.Parallel()

	profile := &domain.SemanticProfile{
		ID:          "sp_1",
		SessionID:   "s_1",
		ProfileJSON: `{}`,
	}
	service := &SourceService{
		SemanticProfileRepo:      &fakeSemanticProfileRepo{profile: profile},
		SemanticConfirmationRepo: &fakeSemanticConfirmationRepo{},
	}

	_, err := service.ConfirmProfile(context.Background(), "sp_1", "ws_1", "s_1", "u_1", "session", `{"bad"`)
	if err == nil || !strings.Contains(err.Error(), "invalid overrides_json") {
		t.Fatalf("expected invalid overrides_json error, got %v", err)
	}
}

func TestWorkspaceConfirmationPersistsReusableSemanticAssets(t *testing.T) {
	t.Parallel()

	profile := &domain.SemanticProfile{
		ID:                "sp_1",
		SessionID:         "s_1",
		SourceID:          "ds_1",
		SchemaSignature:   "sig_1",
		AnalysisTableName: "sales",
		ProfileJSON: `{
			"ambiguities":[
				{"kind":"multiple_time_columns","candidates":["month","created_at"]},
				{"kind":"ambiguous_metrics","candidates":["gross_revenue","net_revenue"]}
			]
		}`,
	}
	assetRepo := &fakeSemanticAssetRepo{}
	auditRepo := &fakeAuditEventRepo{}
	service := &SourceService{
		SemanticProfileRepo:      &fakeSemanticProfileRepo{profile: profile},
		SemanticConfirmationRepo: &fakeSemanticConfirmationRepo{},
		SemanticAssetRepo:        assetRepo,
		AuditEventRepo:           auditRepo,
	}

	_, err := service.ConfirmProfile(context.Background(), "sp_1", "ws_1", "s_1", "u_1", "workspace", `{
		"primary_time_column":"month",
		"metric_definitions":{"net_revenue":"recognized net revenue"},
		"percentage_columns":["conversion_rate"]
	}`)
	if err != nil {
		t.Fatalf("ConfirmProfile returned error: %v", err)
	}
	if len(assetRepo.assets) != 3 {
		t.Fatalf("expected 3 semantic assets, got %#v", assetRepo.assets)
	}
	if _, ok := assetRepo.assets["time_column:primary_time_column"]; !ok {
		t.Fatalf("expected primary time column asset, got %#v", assetRepo.assets)
	}
	if _, ok := assetRepo.assets["metric_definition:metric:net_revenue"]; !ok {
		t.Fatalf("expected metric definition asset, got %#v", assetRepo.assets)
	}
	if _, ok := assetRepo.assets["unit_annotation:unit:conversion_rate"]; !ok {
		t.Fatalf("expected unit annotation asset, got %#v", assetRepo.assets)
	}
	if len(auditRepo.events) == 0 {
		t.Fatalf("expected semantic confirmation/asset audit events")
	}
}

func TestSemanticAssetsBecomeWorkspaceOverrides(t *testing.T) {
	t.Parallel()

	assets := []domain.SemanticAsset{
		{
			WorkspaceID:     "ws_1",
			SchemaSignature: "sig_1",
			AssetKind:       domain.SemanticAssetKindTimeColumn,
			AssetKey:        "primary_time_column",
			AssetValueJSON:  `{"column_name":"month"}`,
		},
		{
			WorkspaceID:     "ws_1",
			SchemaSignature: "sig_1",
			AssetKind:       domain.SemanticAssetKindMetricDefinition,
			AssetKey:        "metric:net_revenue",
			AssetValueJSON:  `{"column_name":"net_revenue","definition":"recognized net revenue"}`,
		},
	}
	overridesJSON := semanticAssetsToOverridesJSON(assets)
	merged := removeResolvedAmbiguities(applyConfirmationsToProfile(`{
		"ambiguities":[
			{"kind":"multiple_time_columns","candidates":["month","created_at"]},
			{"kind":"ambiguous_metrics","candidates":["gross_revenue","net_revenue"]}
		]
	}`, []domain.SemanticConfirmation{{OverridesJSON: overridesJSON}}), []string{overridesJSON})
	if profileStatusForJSON(merged) != domain.ProfileStatusConfirmed {
		t.Fatalf("expected semantic assets to resolve ambiguities, got %s", merged)
	}
}

func TestSemanticAssetIDIsStableForUpsertKey(t *testing.T) {
	t.Parallel()

	first := semanticAssetID("ws_1", "sig_1", domain.SemanticAssetKindMetricDefinition, "metric:latency_ms")
	second := semanticAssetID("ws_1", "sig_1", domain.SemanticAssetKindMetricDefinition, "metric:latency_ms")
	different := semanticAssetID("ws_1", "sig_1", domain.SemanticAssetKindMetricDefinition, "metric:error_rate")

	if first == "" || !strings.HasPrefix(first, "sa_") {
		t.Fatalf("expected stable semantic asset id, got %q", first)
	}
	if first != second {
		t.Fatalf("expected same upsert key to produce same id, got %q and %q", first, second)
	}
	if first == different {
		t.Fatalf("expected different asset key to produce different id")
	}
}

func TestRecordAuditEventFillsRequiredFields(t *testing.T) {
	t.Parallel()

	auditRepo := &fakeAuditEventRepo{}
	service := &SourceService{AuditEventRepo: auditRepo}

	service.RecordAuditEvent(context.Background(), domain.AuditEvent{
		WorkspaceID:  "ws_1",
		EventType:    "data_source_imported",
		ResourceType: "source_snapshot",
		ResourceID:   "snap_1",
	})

	if len(auditRepo.events) != 1 {
		t.Fatalf("expected one audit event, got %#v", auditRepo.events)
	}
	event := auditRepo.events[0]
	if !strings.HasPrefix(event.ID, "ae_") {
		t.Fatalf("expected generated audit event id, got %q", event.ID)
	}
	if event.PayloadJSON != "{}" {
		t.Fatalf("expected default payload json, got %q", event.PayloadJSON)
	}
	if event.CreatedAt.IsZero() {
		t.Fatal("expected generated created_at")
	}
}

func TestGetSessionProfileDetailRejectsCrossSessionProfile(t *testing.T) {
	t.Parallel()

	service := &SourceService{
		SemanticProfileRepo: &fakeSemanticProfileRepo{profile: &domain.SemanticProfile{
			ID:        "sp_1",
			SessionID: "other_session",
		}},
		SemanticConfirmationRepo: &fakeSemanticConfirmationRepo{},
	}

	_, _, err := service.GetSessionProfileDetail(context.Background(), "current_session", "sp_1")
	if err == nil || !strings.Contains(err.Error(), "does not belong to session") {
		t.Fatalf("expected cross-session profile to be rejected, got %v", err)
	}
}

func TestCreateSemanticProfileMarksUnambiguousFactsConfirmed(t *testing.T) {
	t.Parallel()

	repo := &fakeSemanticProfileRepo{}
	service := &SourceService{
		SemanticProfileRepo: repo,
	}

	profile, err := service.CreateSemanticProfile(context.Background(), "s_1", "ws_1", "ds_1", "snap_1", "sales", "sig_1", ProfiledFacts{})
	if err != nil {
		t.Fatalf("CreateSemanticProfile returned error: %v", err)
	}
	if profile.ProfileStatus != domain.ProfileStatusConfirmed {
		t.Fatalf("expected unambiguous profile to be confirmed, got %q", profile.ProfileStatus)
	}
	if repo.profile == nil || repo.profile.ProfileStatus != domain.ProfileStatusConfirmed {
		t.Fatalf("expected persisted profile status to be confirmed, got %#v", repo.profile)
	}
}

func TestCreateSemanticProfileKeepsAmbiguousFactsProfiled(t *testing.T) {
	t.Parallel()

	repo := &fakeSemanticProfileRepo{}
	service := &SourceService{
		SemanticProfileRepo: repo,
	}

	profile, err := service.CreateSemanticProfile(context.Background(), "s_1", "ws_1", "ds_1", "snap_1", "sales", "sig_1", ProfiledFacts{
		Ambiguities: []Ambiguity{
			{Kind: "ambiguous_metrics", Candidates: []string{"revenue", "net_revenue"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateSemanticProfile returned error: %v", err)
	}
	if profile.ProfileStatus != domain.ProfileStatusProfiled {
		t.Fatalf("expected ambiguous profile to stay profiled, got %q", profile.ProfileStatus)
	}
}

func TestSourceScopedPGTableNameAvoidsCrossSourceCollision(t *testing.T) {
	t.Parallel()

	first := sourceScopedPGTableName("public", "orders", "ds_alpha_12345678")
	second := sourceScopedPGTableName("public", "orders", "ds_beta_87654321")
	if first == second {
		t.Fatalf("expected source-scoped table names to differ, both %q", first)
	}
	if first != "public_orders__12345678" || second != "public_orders__87654321" {
		t.Fatalf("unexpected scoped table names: %q %q", first, second)
	}

	otherSchema := sourceScopedPGTableName("sales", "orders", "ds_alpha_12345678")
	if otherSchema == first {
		t.Fatalf("expected schema to participate in scoped table name, got %q", otherSchema)
	}
}

func TestSourceObjectKeyUsesObjectLevelIdentity(t *testing.T) {
	t.Parallel()

	fileKey := SourceObjectKey("ds_file", "file_upload", "", "")
	if fileKey != "file_upload:ds_file" {
		t.Fatalf("unexpected file source key %q", fileKey)
	}

	first := SourceObjectKey("ds_pg", "postgres_connection", "public", "orders")
	second := SourceObjectKey("ds_pg", "postgres_connection", "sales", "orders")
	if first == second || first != "postgres_connection:public.orders" || second != "postgres_connection:sales.orders" {
		t.Fatalf("expected schema-qualified postgres keys, got %q %q", first, second)
	}
}

func TestDataSizeTierAndProfileModeForRows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		rows        int
		wantTier    string
		wantProfile domain.ProfileMode
	}{
		{rows: 9999, wantTier: DataSizeTierSmall, wantProfile: domain.ProfileModeExact},
		{rows: 10000, wantTier: DataSizeTierMedium, wantProfile: domain.ProfileModeMixed},
		{rows: 99999, wantTier: DataSizeTierMedium, wantProfile: domain.ProfileModeMixed},
		{rows: 100000, wantTier: DataSizeTierLarge, wantProfile: domain.ProfileModeSampled},
		{rows: 999999, wantTier: DataSizeTierLarge, wantProfile: domain.ProfileModeSampled},
		{rows: 1000000, wantTier: DataSizeTierXLarge, wantProfile: domain.ProfileModeSampled},
	}

	for _, tc := range cases {
		if got := DataSizeTierForRows(tc.rows); got != tc.wantTier {
			t.Fatalf("rows=%d tier=%q, want %q", tc.rows, got, tc.wantTier)
		}
		if got := ProfileModeForRows(tc.rows); got != tc.wantProfile {
			t.Fatalf("rows=%d profile=%q, want %q", tc.rows, got, tc.wantProfile)
		}
	}
}

func TestQuotePGIdentifierEscapesQuotes(t *testing.T) {
	t.Parallel()

	got, err := quotePGIdentifier(`Sales "Archive"`)
	if err != nil {
		t.Fatalf("quotePGIdentifier returned error: %v", err)
	}
	if got != `"Sales ""Archive"""` {
		t.Fatalf("unexpected quoted identifier: %s", got)
	}

	if _, err := quotePGIdentifier(""); err == nil {
		t.Fatalf("expected empty identifier error")
	}
}

type fakeSemanticProfileRepo struct {
	profile *domain.SemanticProfile
}

func (r *fakeSemanticProfileRepo) Create(ctx context.Context, profile *domain.SemanticProfile) error {
	cp := *profile
	r.profile = &cp
	return nil
}

func (r *fakeSemanticProfileRepo) GetByID(ctx context.Context, id string) (*domain.SemanticProfile, error) {
	if r.profile != nil && r.profile.ID == id {
		return r.profile, nil
	}
	return nil, errors.New("profile not found")
}

func (r *fakeSemanticProfileRepo) ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticProfile, error) {
	return nil, errors.New("not implemented")
}

func (r *fakeSemanticProfileRepo) ListBySource(ctx context.Context, sourceID string) ([]domain.SemanticProfile, error) {
	return nil, errors.New("not implemented")
}

func (r *fakeSemanticProfileRepo) UpdateStatus(ctx context.Context, id string, status domain.ProfileStatus) error {
	return errors.New("not implemented")
}

func (r *fakeSemanticProfileRepo) UpdateProfileJSON(ctx context.Context, id string, profileJSON string) error {
	return errors.New("not implemented")
}

func (r *fakeSemanticProfileRepo) FindWorkspaceConfirmation(ctx context.Context, workspaceID, schemaSignature string) (*domain.SemanticConfirmation, error) {
	return nil, nil
}

func (r *fakeSemanticProfileRepo) Delete(ctx context.Context, id string) error {
	return errors.New("not implemented")
}

type fakeSemanticConfirmationRepo struct{}

func (r *fakeSemanticConfirmationRepo) Create(ctx context.Context, confirmation *domain.SemanticConfirmation) error {
	return nil
}

func (r *fakeSemanticConfirmationRepo) ListByProfile(ctx context.Context, profileID string) ([]domain.SemanticConfirmation, error) {
	return nil, nil
}

func (r *fakeSemanticConfirmationRepo) ListBySession(ctx context.Context, sessionID string) ([]domain.SemanticConfirmation, error) {
	return nil, nil
}

func (r *fakeSemanticConfirmationRepo) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticConfirmation, error) {
	return nil, nil
}

func (r *fakeSemanticConfirmationRepo) DeleteByProfile(ctx context.Context, profileID string) error {
	return nil
}

type fakeSemanticAssetRepo struct {
	assets map[string]domain.SemanticAsset
}

func (r *fakeSemanticAssetRepo) Upsert(ctx context.Context, asset *domain.SemanticAsset) error {
	if r.assets == nil {
		r.assets = map[string]domain.SemanticAsset{}
	}
	key := string(asset.AssetKind) + ":" + asset.AssetKey
	r.assets[key] = *asset
	return nil
}

func (r *fakeSemanticAssetRepo) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SemanticAsset, error) {
	var out []domain.SemanticAsset
	for _, asset := range r.assets {
		if asset.WorkspaceID == workspaceID {
			out = append(out, asset)
		}
	}
	return out, nil
}

func (r *fakeSemanticAssetRepo) ListBySchema(ctx context.Context, workspaceID, schemaSignature string) ([]domain.SemanticAsset, error) {
	var out []domain.SemanticAsset
	for _, asset := range r.assets {
		if asset.WorkspaceID == workspaceID && asset.SchemaSignature == schemaSignature {
			out = append(out, asset)
		}
	}
	return out, nil
}

type fakeAuditEventRepo struct {
	events []domain.AuditEvent
}

func (r *fakeAuditEventRepo) Create(ctx context.Context, event *domain.AuditEvent) error {
	r.events = append(r.events, *event)
	return nil
}

func (r *fakeAuditEventRepo) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.AuditEvent, error) {
	var out []domain.AuditEvent
	for _, event := range r.events {
		if event.WorkspaceID == workspaceID {
			out = append(out, event)
		}
	}
	return out, nil
}

func ambiguityKinds(t *testing.T, profileJSON string) map[string]bool {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(profileJSON), &payload); err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	out := map[string]bool{}
	raw, ok := payload["ambiguities"].([]interface{})
	if !ok {
		return out
	}
	for _, item := range raw {
		amb, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := amb["kind"].(string)
		if kind != "" {
			out[kind] = true
		}
	}
	return out
}

func ambiguityCount(t *testing.T, profileJSON string) int {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(profileJSON), &payload); err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	raw, ok := payload["ambiguities"].([]interface{})
	if !ok {
		return 0
	}
	return len(raw)
}
