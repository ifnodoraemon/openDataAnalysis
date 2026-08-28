package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
)

func TestBuildProfileFactsRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	_, err := buildProfileFacts(&data.SchemaInfo{}, domain.ProfileMode("unknown"), 0, 0, false)
	if err == nil {
		t.Fatal("expected unknown profile mode to be rejected")
	}
}

func TestNewSourceServiceRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected missing repositories to panic")
		}
	}()
	NewSourceService(nil, nil, nil, nil, nil, nil, nil, nil)
}

func TestBuildProfileFactsRequiresLimitForTruncation(t *testing.T) {
	t.Parallel()

	_, err := buildProfileFacts(&data.SchemaInfo{}, domain.ProfileModeExact, 0, 0, true)
	if err == nil {
		t.Fatal("expected truncated import without a row limit to be rejected")
	}
}

func TestSnapshotImportDoesNotActivateOrDestroyDataBeforeFinalize(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	defer store.DB.Close()
	now := time.Now()
	if _, err := store.DB.Exec(`INSERT INTO users (id,email,name,created_at,updated_at) VALUES (?,?,?,?,?)`, "u_1", "u@example.com", "User", now, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.DB.Exec(`INSERT INTO workspaces (id,name,slug,owner_user_id,created_at,updated_at) VALUES (?,?,?,?,?,?)`, "ws_1", "Workspace", "workspace", "u_1", now, now); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := store.DB.Exec(`INSERT INTO sessions (id,workspace_id,user_id,title,created_at,updated_at,last_seen_at) VALUES (?,?,?,?,?,?,?)`, "s_1", "ws_1", "u_1", "", now, now, now); err != nil {
		t.Fatalf("create session: %v", err)
	}
	dsRepo := sqliterepo.NewDataSourceRepository(store.DB)
	if err := dsRepo.Create(ctx, &domain.DataSource{ID: "ds_1", WorkspaceID: "ws_1", Name: "Source", SourceType: domain.SourceTypeFileUpload, Status: domain.SourceStatusActive, CreatedBy: "u_1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create source: %v", err)
	}
	service := NewSourceService(
		dsRepo,
		sqliterepo.NewSourceConfigRepository(store.DB),
		sqliterepo.NewSourceSnapshotRepository(store.DB),
		sqliterepo.NewSessionSourceBindingRepository(store.DB),
		sqliterepo.NewSemanticProfileRepository(store.DB),
		sqliterepo.NewSemanticConfirmationRepository(store.DB),
		sqliterepo.NewSemanticAssetRepository(store.DB),
		sqliterepo.NewAuditEventRepository(store.DB),
	)
	ingester := data.NewIngester(t.TempDir())
	if err := ingester.InitDB("s_1"); err != nil {
		t.Fatalf("init analysis db: %v", err)
	}
	defer ingester.Destroy()

	finalize := func(snapshot *domain.SourceSnapshot, value string) *SnapshotImportResult {
		if err := ingester.CreateTypedTable(snapshot.AnalysisTableName, []string{"value"}, []data.ColumnType{data.TypeText}); err != nil {
			t.Fatalf("create snapshot table: %v", err)
		}
		if err := ingester.InsertBatchValues(snapshot.AnalysisTableName, []string{"value"}, [][]interface{}{{value}}); err != nil {
			t.Fatalf("insert snapshot value: %v", err)
		}
		result, err := service.FinalizeSnapshotImport(ctx, SnapshotImportCompletion{
			SnapshotID: snapshot.ID, SessionID: "s_1", SourceID: "ds_1", UpstreamKind: "file_upload",
			AnalysisTableName: snapshot.AnalysisTableName, RowCount: 1, ColCount: 1, RowsImported: 1, Ingester: ingester,
		})
		if err != nil {
			t.Fatalf("finalize snapshot: %v", err)
		}
		return result
	}

	first, err := service.BeginSnapshotImport(ctx, "s_1", "ds_1", "file_upload", "", "")
	if err != nil {
		t.Fatalf("begin first snapshot: %v", err)
	}
	firstResult := finalize(first, "old")

	second, err := service.BeginSnapshotImport(ctx, "s_1", "ds_1", "file_upload", "", "")
	if err != nil {
		t.Fatalf("begin replacement snapshot: %v", err)
	}
	fileSourceKey := SourceObjectKey("ds_1", "file_upload", "", "")
	binding, err := service.SessionSourceBindingRepo.GetBySessionSourceObject(ctx, "s_1", "ds_1", fileSourceKey)
	if err != nil || binding == nil || binding.ActiveSnapshotID != first.ID {
		t.Fatalf("creating snapshot changed active binding: binding=%#v err=%v", binding, err)
	}
	queryResult, err := data.ExecuteQueryDetailedContext(ctx, ingester.GetDB(), `SELECT value FROM "`+firstResult.TableName+`"`, 5*time.Second)
	var rows []map[string]interface{}
	if queryResult != nil {
		rows = queryResult.Rows
	}
	if err != nil || len(rows) != 1 || rows[0]["value"] != "old" {
		t.Fatalf("old snapshot stopped being readable: rows=%#v err=%v", rows, err)
	}

	secondResult := finalize(second, "new")
	binding, err = service.SessionSourceBindingRepo.GetBySessionSourceObject(ctx, "s_1", "ds_1", fileSourceKey)
	if err != nil || binding == nil || binding.ActiveSnapshotID != second.ID {
		t.Fatalf("completed snapshot was not activated: binding=%#v err=%v", binding, err)
	}
	if _, err := data.ExecuteQueryDetailedContext(ctx, ingester.GetDB(), `SELECT value FROM "`+firstResult.TableName+`"`, 5*time.Second); err == nil {
		t.Fatal("superseded snapshot table was not cleaned up")
	}
	queryResult, err = data.ExecuteQueryDetailedContext(ctx, ingester.GetDB(), `SELECT value FROM "`+secondResult.TableName+`"`, 5*time.Second)
	if queryResult != nil {
		rows = queryResult.Rows
	}
	if err != nil || len(rows) != 1 || rows[0]["value"] != "new" {
		t.Fatalf("active replacement snapshot is unreadable: rows=%#v err=%v", rows, err)
	}
}

func TestSelectProfileForSnapshotUsesActiveSessionSnapshot(t *testing.T) {
	t.Parallel()

	profiles := []domain.SemanticProfile{
		{ID: "sp_other_session", SessionID: "s_other", SnapshotID: "snap_active"},
		{ID: "sp_old", SessionID: "s_1", SnapshotID: "snap_old"},
		{ID: "sp_active", SessionID: "s_1", SnapshotID: "snap_active"},
	}

	got, err := selectProfileForSnapshot(profiles, "s_1", "snap_active")
	if err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if got == nil || got.ID != "sp_active" {
		t.Fatalf("expected active session snapshot profile, got %#v", got)
	}
}

func TestSelectProfileForSnapshotRejectsMultipleMatches(t *testing.T) {
	t.Parallel()

	profiles := []domain.SemanticProfile{
		{ID: "sp_1", SessionID: "s_1", SnapshotID: "snap_1"},
		{ID: "sp_2", SessionID: "s_1", SnapshotID: "snap_1"},
	}
	if _, err := selectProfileForSnapshot(profiles, "s_1", "snap_1"); err == nil {
		t.Fatal("expected duplicate matching profiles to be rejected")
	}
}

func TestConfirmProfileRejectsInvalidPatchJSON(t *testing.T) {
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

	_, _, err := service.ConfirmProfile(context.Background(), "sp_1", "ws_1", "s_1", "u_1", "session", `{"bad"`, "", domain.ConfirmationProvenanceAuthenticatedRequest)
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
		AnalysisTableName: "records",
		ProfileJSON:       `{"schema":{"table_name":"records"}}`,
	}
	assetRepo := &fakeSemanticAssetRepo{}
	auditRepo := &fakeAuditEventRepo{}
	service := &SourceService{
		SemanticProfileRepo:      &fakeSemanticProfileRepo{profile: profile},
		SemanticConfirmationRepo: &fakeSemanticConfirmationRepo{},
		SemanticAssetRepo:        assetRepo,
		AuditEventRepo:           auditRepo,
	}

	confirmed, auditErrors, err := service.ConfirmProfile(context.Background(), "sp_1", "ws_1", "s_1", "u_1", "workspace", `{
		"annotations":{"field_a":"user value"},
		"display":{"precision":3},
		"notes":["user supplied"]
	}`, "", domain.ConfirmationProvenanceAuthenticatedRequest)
	if err != nil {
		t.Fatalf("ConfirmProfile returned error: %v", err)
	}
	if len(auditErrors) != 0 {
		t.Fatalf("unexpected audit errors: %v", auditErrors)
	}
	if confirmed.ProfileJSON != `{"schema":{"table_name":"records"}}` {
		t.Fatalf("observed profile facts were rewritten by a confirmation: %s", confirmed.ProfileJSON)
	}
	if len(assetRepo.assets) != 1 {
		t.Fatalf("expected one exact confirmation patch asset, got %#v", assetRepo.assets)
	}
	for _, asset := range assetRepo.assets {
		if asset.AssetKey != asset.CreatedFromConfirmationID || !strings.Contains(asset.AssetValueJSON, `"annotations"`) || !strings.Contains(asset.AssetValueJSON, `"display"`) || !strings.Contains(asset.AssetValueJSON, `"notes"`) {
			t.Fatalf("expected the complete authorized patch under an opaque confirmation identity, got %#v", asset)
		}
	}
	if len(auditRepo.events) == 0 {
		t.Fatalf("expected semantic confirmation/asset audit events")
	}
}

func TestSemanticAssetIDIsStableForConfirmationIdentity(t *testing.T) {
	t.Parallel()

	first := semanticAssetID("ws_1", "sig_1", domain.SemanticAssetKindPatch, "sc_1")
	second := semanticAssetID("ws_1", "sig_1", domain.SemanticAssetKindPatch, "sc_1")
	different := semanticAssetID("ws_1", "sig_1", domain.SemanticAssetKindPatch, "sc_2")

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

func TestGetSemanticAssetsRequiresRepositoryAndExactIdentity(t *testing.T) {
	t.Parallel()

	service := &SourceService{}
	if _, err := service.GetSemanticAssets(context.Background(), "ws_1", "sig_1"); err == nil {
		t.Fatal("expected missing semantic asset repository error")
	}

	service.SemanticAssetRepo = &fakeSemanticAssetRepo{}
	if _, err := service.GetSemanticAssets(context.Background(), " ws_1", "sig_1"); err == nil {
		t.Fatal("expected non-exact workspace identity error")
	}
}

func TestRecordAuditEventFillsRequiredFields(t *testing.T) {
	t.Parallel()

	auditRepo := &fakeAuditEventRepo{}
	service := &SourceService{AuditEventRepo: auditRepo}

	err := service.RecordAuditEvent(context.Background(), domain.AuditEvent{
		WorkspaceID:  "ws_1",
		EventType:    "data_source_imported",
		ResourceType: "source_snapshot",
		ResourceID:   "snap_1",
		PayloadJSON:  "{}",
	})
	if err != nil {
		t.Fatalf("RecordAuditEvent returned error: %v", err)
	}

	if len(auditRepo.events) != 1 {
		t.Fatalf("expected one audit event, got %#v", auditRepo.events)
	}
	event := auditRepo.events[0]
	if !strings.HasPrefix(event.ID, "ae_") {
		t.Fatalf("expected generated audit event id, got %q", event.ID)
	}
	if event.PayloadJSON != "{}" {
		t.Fatalf("expected exact payload json, got %q", event.PayloadJSON)
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

func TestCreateSemanticProfileDoesNotSelfConfirmFacts(t *testing.T) {
	t.Parallel()

	repo := &fakeSemanticProfileRepo{}
	service := &SourceService{
		SemanticProfileRepo: repo,
	}

	profile, err := service.CreateSemanticProfile(context.Background(), "s_1", "ds_1", "snap_1", "records", "sig_1", ProfiledFacts{})
	if err != nil {
		t.Fatalf("CreateSemanticProfile returned error: %v", err)
	}
	if profile.ProfileStatus != domain.ProfileStatusProfiled {
		t.Fatalf("expected model-profiled facts to remain unconfirmed, got %q", profile.ProfileStatus)
	}
	if repo.profile == nil || repo.profile.ProfileStatus != domain.ProfileStatusProfiled {
		t.Fatalf("expected persisted profile status to remain unconfirmed, got %#v", repo.profile)
	}
}

func TestSourceObjectKeyUsesObjectLevelIdentity(t *testing.T) {
	t.Parallel()

	fileKey := SourceObjectKey("ds_file", "file_upload", "", "")
	if !strings.HasPrefix(fileKey, "source_object_") || len(fileKey) != len("source_object_")+64 {
		t.Fatalf("expected canonical opaque file source key, got %q", fileKey)
	}

	first := SourceObjectKey("ds_pg", "postgres_connection", "public", "orders")
	second := SourceObjectKey("ds_pg", "postgres_connection", "sales", "orders")
	if first == second {
		t.Fatalf("expected structured source identities to produce distinct keys, got %q %q", first, second)
	}
	if first != SourceObjectKey("ds_pg", "postgres_connection", "public", "orders") {
		t.Fatal("expected source object identity to be deterministic")
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
	if r.profile == nil || r.profile.ID != id {
		return errors.New("profile not found")
	}
	r.profile.ProfileStatus = status
	return nil
}

func (r *fakeSemanticProfileRepo) UpdateProfileJSON(ctx context.Context, id string, profileJSON string) error {
	return errors.New("not implemented")
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
