package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
)

type fakeLiveConnector struct {
	dialect     string
	metadata    *LiveObjectMetadata
	metadataErr error
	queryRows   *LiveQueryRows
	queryErr    error
	lastSQL     string
	lastTimeout int
	lastMaxRows int
}

func (c *fakeLiveConnector) FetchLiveObjectMetadata(ctx context.Context, sourceID, authSecret string, object SourceObjectRef) (*LiveObjectMetadata, error) {
	if c.metadataErr != nil {
		return nil, c.metadataErr
	}
	return c.metadata, nil
}

func (c *fakeLiveConnector) ExecuteLiveQuery(ctx context.Context, sourceID, authSecret, sql string, timeoutSeconds, maxRows int) (*LiveQueryRows, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	c.lastSQL = sql
	c.lastTimeout = timeoutSeconds
	c.lastMaxRows = maxRows
	return c.queryRows, nil
}

func (c *fakeLiveConnector) Dialect() string { return c.dialect }

func (c *fakeLiveConnector) QualifyObject(schema, name string) string {
	return schema + "." + name
}

func newLiveTestService(t *testing.T) (*SourceService, *fakeLiveConnector) {
	t.Helper()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { store.DB.Close() })
	now := time.Now()
	for _, stmt := range []string{
		`INSERT INTO users (id,email,name,created_at,updated_at) VALUES ('u_1','u@example.com','User',?,?)`,
		`INSERT INTO workspaces (id,name,slug,owner_user_id,created_at,updated_at) VALUES ('ws_1','Workspace','workspace','u_1',?,?)`,
		`INSERT INTO sessions (id,workspace_id,user_id,title,created_at,updated_at,last_seen_at) VALUES ('s_1','ws_1','u_1','',?,?,?)`,
		`INSERT INTO data_sources (id,workspace_id,name,source_type,status,created_at,updated_at,created_by) VALUES ('ds_live','ws_1','Live DB','postgres_connection','active',?,?,'u_1')`,
	} {
		if _, err := store.DB.Exec(stmt, now, now, now, now); err != nil {
			t.Fatalf("seed metadata: %v stmt=%s err=%v", err, stmt, err)
		}
	}
	svc := NewSourceService(
		sqliterepo.NewDataSourceRepository(store.DB),
		sqliterepo.NewSourceConfigRepository(store.DB),
		sqliterepo.NewSourceSnapshotRepository(store.DB),
		sqliterepo.NewSessionSourceBindingRepository(store.DB),
		sqliterepo.NewSemanticProfileRepository(store.DB),
		sqliterepo.NewSemanticConfirmationRepository(store.DB),
		sqliterepo.NewSemanticAssetRepository(store.DB),
		sqliterepo.NewAuditEventRepository(store.DB),
	)
	connector := &fakeLiveConnector{
		dialect: "postgres",
		metadata: &LiveObjectMetadata{
			Columns: []LiveColumn{
				{Name: "id", DeclaredType: "integer"},
				{Name: "amount", DeclaredType: "numeric"},
			},
			RowCountEstimate: 4200,
		},
		queryRows: &LiveQueryRows{
			Columns: []string{"id", "amount"},
			Rows: []map[string]interface{}{
				{"id": int64(1), "amount": "12.5"},
			},
			Dialect: "postgres",
		},
	}
	svc.SetLiveConnectorResolver(func(sourceType domain.SourceType) (LiveQueryConnector, error) {
		if sourceType != domain.SourceTypePostgresConnection {
			return nil, fmt.Errorf("unexpected source type %s", sourceType)
		}
		return connector, nil
	})
	return svc, connector
}

func TestBindLiveSourceObjectCreatesLiveSnapshotProfileAndBinding(t *testing.T) {
	ctx := context.Background()
	svc, _ := newLiveTestService(t)

	result, err := svc.BindLiveSourceObject(ctx, LiveBindRequest{
		SourceID:    "ds_live",
		WorkspaceID: "ws_1",
		SessionID:   "s_1",
		Object:      SourceObjectRef{Schema: "public", Name: "orders"},
	})
	if err != nil {
		t.Fatalf("bind live source: %v", err)
	}
	if result.TableName != "" {
		t.Fatalf("live bind must not create a local analysis table, got %q", result.TableName)
	}
	if result.ProfileMode != domain.ProfileModeLive {
		t.Fatalf("expected live profile mode, got %q", result.ProfileMode)
	}
	snapshot, err := svc.SnapshotRepo.GetByID(ctx, result.SnapshotID)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if snapshot.Mode != domain.SnapshotModeLive {
		t.Fatalf("expected live snapshot mode, got %q", snapshot.Mode)
	}
	if snapshot.Status != domain.SnapshotStatusReady {
		t.Fatalf("expected ready snapshot, got %q", snapshot.Status)
	}
	if snapshot.AnalysisTableName != "" {
		t.Fatalf("live snapshot must not reference a local table, got %q", snapshot.AnalysisTableName)
	}
	profile, confirmations, err := svc.GetProfileDetail(ctx, result.ProfileID)
	if err != nil {
		t.Fatalf("get profile detail: %v", err)
	}
	if len(confirmations) != 0 {
		t.Fatalf("live profile must not be self-confirmed, got %d confirmations", len(confirmations))
	}
	if !strings.Contains(profile.ProfileJSON, "\"live\"") {
		t.Fatalf("expected live profile mode in profile JSON: %s", profile.ProfileJSON)
	}
	binding, err := svc.SessionSourceBindingRepo.GetBySessionSourceObject(ctx, "s_1", "ds_live", SourceObjectKey("ds_live", "postgres_connection", "public", "orders"))
	if err != nil {
		t.Fatalf("expected live binding persisted: %v", err)
	}
	if binding.ActiveSnapshotID != result.SnapshotID {
		t.Fatalf("binding points at unexpected snapshot %s", binding.ActiveSnapshotID)
	}
}

func TestExecuteSessionLiveQueryRequiresBindingAndForwardsBounds(t *testing.T) {
	ctx := context.Background()
	svc, connector := newLiveTestService(t)

	if _, err := svc.ExecuteSessionLiveQuery(ctx, LiveQueryCall{
		SourceID: "ds_live", SessionID: "s_1", WorkspaceID: "ws_1",
		SQL: "SELECT 1", TimeoutSeconds: 5,
	}); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("expected unbound source rejection, got %v", err)
	}

	if _, err := svc.BindLiveSourceObject(ctx, LiveBindRequest{
		SourceID: "ds_live", WorkspaceID: "ws_1", SessionID: "s_1",
		Object: SourceObjectRef{Schema: "public", Name: "orders"},
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if _, err := svc.ExecuteSessionLiveQuery(ctx, LiveQueryCall{
		SourceID: "ds_live", SessionID: "s_1", WorkspaceID: "ws_1",
		SQL: "DELETE FROM x", TimeoutSeconds: 5,
	}); err == nil {
		t.Fatal("expected non-SELECT rejection")
	}

	rows, err := svc.ExecuteSessionLiveQuery(ctx, LiveQueryCall{
		SourceID: "ds_live", SessionID: "s_1", WorkspaceID: "ws_1",
		SQL: "SELECT * FROM public.orders", TimeoutSeconds: 7,
	})
	if err != nil {
		t.Fatalf("live query: %v", err)
	}
	if rows.Dialect != "postgres" || len(rows.Rows) != 1 {
		t.Fatalf("unexpected live rows: %#v", rows)
	}
	if connector.lastTimeout != 7 || connector.lastMaxRows != liveQueryProbeRows {
		t.Fatalf("bounds not forwarded: timeout=%d maxRows=%d", connector.lastTimeout, connector.lastMaxRows)
	}
	if connector.lastSQL != "SELECT * FROM public.orders" {
		t.Fatalf("sql not forwarded verbatim: %s", connector.lastSQL)
	}
}

func TestListSessionLiveTablesReturnsBoundObjects(t *testing.T) {
	ctx := context.Background()
	svc, _ := newLiveTestService(t)
	if _, err := svc.BindLiveSourceObject(ctx, LiveBindRequest{
		SourceID: "ds_live", WorkspaceID: "ws_1", SessionID: "s_1",
		Object: SourceObjectRef{Schema: "public", Name: "orders"},
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	facts, err := svc.ListSessionLiveTables(ctx, "s_1", "ws_1", "ds_live")
	if err != nil {
		t.Fatalf("list live tables: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one live table fact, got %d", len(facts))
	}
	if facts[0].QualifiedName != "public.orders" || facts[0].RowCountEstimate != 4200 || !facts[0].Estimated {
		t.Fatalf("unexpected fact: %#v", facts[0])
	}
	if facts[0].ProfileID == "" || facts[0].Dialect != "postgres" {
		t.Fatalf("expected profile and dialect facts: %#v", facts[0])
	}
}

func TestDescribeSessionLiveTableServesProfileAndBoundedSample(t *testing.T) {
	ctx := context.Background()
	svc, connector := newLiveTestService(t)
	if _, err := svc.BindLiveSourceObject(ctx, LiveBindRequest{
		SourceID: "ds_live", WorkspaceID: "ws_1", SessionID: "s_1",
		Object: SourceObjectRef{Schema: "public", Name: "orders"},
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	description, err := svc.DescribeSessionLiveTable(ctx, LiveDescribeCall{
		SourceID: "ds_live", SessionID: "s_1", WorkspaceID: "ws_1",
		Schema: "public", Name: "orders", SampleRows: 0,
	})
	if err != nil {
		t.Fatalf("describe live table: %v", err)
	}
	if description.ColumnCount != 2 || len(description.Columns) != 2 {
		t.Fatalf("unexpected columns: %#v", description.Columns)
	}
	if description.Sample != nil {
		t.Fatalf("sample_rows=0 must skip the upstream query")
	}
	if connector.lastSQL != "" {
		t.Fatalf("no upstream query expected for structural-only describe, got %s", connector.lastSQL)
	}

	description, err = svc.DescribeSessionLiveTable(ctx, LiveDescribeCall{
		SourceID: "ds_live", SessionID: "s_1", WorkspaceID: "ws_1",
		Schema: "public", Name: "orders", SampleRows: 10,
	})
	if err != nil {
		t.Fatalf("describe with sample: %v", err)
	}
	if description.Sample == nil || len(description.Sample.Rows) != 1 {
		t.Fatalf("expected bounded sample rows: %#v", description.Sample)
	}
	if connector.lastSQL != "SELECT * FROM public.orders LIMIT 10" {
		t.Fatalf("unexpected sample SQL: %s", connector.lastSQL)
	}
}

func TestRemoveSessionSourceRemovesLiveBindingWithoutTableDrop(t *testing.T) {
	ctx := context.Background()
	svc, _ := newLiveTestService(t)
	if _, err := svc.BindLiveSourceObject(ctx, LiveBindRequest{
		SourceID: "ds_live", WorkspaceID: "ws_1", SessionID: "s_1",
		Object: SourceObjectRef{Schema: "public", Name: "orders"},
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	dropCalled := false
	err := svc.RemoveSessionSource(ctx, "s_1", "ds_live",
		SourceObjectKey("ds_live", "postgres_connection", "public", "orders"),
		func(table SourceRuntimeTable) error {
			dropCalled = true
			return nil
		})
	if err != nil {
		t.Fatalf("remove live session source: %v", err)
	}
	if dropCalled {
		t.Fatal("live removal must not drop local tables")
	}
	if _, err := svc.ListSessionLiveTables(ctx, "s_1", "ws_1", "ds_live"); err == nil {
		t.Fatal("binding should be gone after removal")
	}
}

func TestBindLiveSourceObjectRejectsWorkspaceMismatch(t *testing.T) {
	ctx := context.Background()
	svc, _ := newLiveTestService(t)
	if _, err := svc.BindLiveSourceObject(ctx, LiveBindRequest{
		SourceID: "ds_live", WorkspaceID: "ws_other", SessionID: "s_1",
		Object: SourceObjectRef{Schema: "public", Name: "orders"},
	}); err == nil {
		t.Fatal("expected workspace mismatch rejection")
	}
}
