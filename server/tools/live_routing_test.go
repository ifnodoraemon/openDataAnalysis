package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/data"
)

func TestQueryDataToolRoutesLiveSource(t *testing.T) {
	t.Parallel()

	tool := &QueryDataTool{
		LiveQuery: func(ctx context.Context, req LiveQueryRequest) (*LiveQueryResult, error) {
			if req.SourceID != "ds_live" {
				t.Fatalf("unexpected source id %q", req.SourceID)
			}
			if req.SQL != "SELECT * FROM public.orders" {
				t.Fatalf("unexpected sql %q", req.SQL)
			}
			if req.TimeoutSeconds != 9 {
				t.Fatalf("timeout not forwarded: %d", req.TimeoutSeconds)
			}
			if req.MaxRows != 201 {
				t.Fatalf("expected 201-row probe bound, got %d", req.MaxRows)
			}
			return &LiveQueryResult{
				SourceID: "ds_live",
				Dialect:  "postgres",
				Columns:  []string{"id"},
				Rows:     []map[string]interface{}{{"id": int64(1)}},
				RowCount: 1,
			}, nil
		},
	}
	tool.SetExecutionContext(context.Background())

	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT * FROM public.orders","timeout_seconds":9,"source_id":"ds_live"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		OK       bool   `json:"ok"`
		Scope    string `json:"scope"`
		SourceID string `json:"source_id"`
		Dialect  string `json:"dialect"`
		RowCount int    `json:"row_count"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.OK || payload.Scope != "live_source" || payload.SourceID != "ds_live" || payload.Dialect != "postgres" || payload.RowCount != 1 {
		t.Fatalf("unexpected live payload: %#v", payload)
	}
}

func TestQueryDataToolLiveFailureIsStructured(t *testing.T) {
	t.Parallel()

	tool := &QueryDataTool{
		LiveQuery: func(ctx context.Context, req LiveQueryRequest) (*LiveQueryResult, error) {
			return nil, errors.New("connection refused")
		},
	}
	tool.SetExecutionContext(context.Background())

	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT 1","timeout_seconds":5,"source_id":"ds_live"}`))
	if err != nil {
		t.Fatalf("execute must not return a Go error for structured failures: %v", err)
	}
	var payload struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"error_code"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.OK || payload.ErrorCode != "query_failed" || !strings.Contains(payload.Detail, "connection refused") {
		t.Fatalf("unexpected failure payload: %#v", payload)
	}
}

func TestListTablesToolRoutesLiveSource(t *testing.T) {
	t.Parallel()

	tool := &ListTablesTool{
		LiveTables: func(ctx context.Context, sourceID string) ([]LiveSourceTable, error) {
			return []LiveSourceTable{{
				Schema:           "public",
				Name:             "orders",
				QualifiedName:    "public.orders",
				Kind:             "table",
				RowCountEstimate: 4200,
				Estimated:        true,
				ProfileID:        "sp_1",
				SnapshotID:       "snap_1",
				Dialect:          "postgres",
			}}, nil
		},
	}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{"source_id":"ds_live"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		OK         bool `json:"ok"`
		TableCount int  `json:"table_count"`
		Tables     []struct {
			QualifiedName    string `json:"qualified_name"`
			RowCountEstimate int64  `json:"row_count_estimate"`
			Estimated        bool   `json:"estimated"`
			Dialect          string `json:"dialect"`
		} `json:"tables"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.OK || payload.TableCount != 1 || len(payload.Tables) != 1 {
		t.Fatalf("unexpected live listing: %#v", payload)
	}
	table := payload.Tables[0]
	if table.QualifiedName != "public.orders" || table.RowCountEstimate != 4200 || !table.Estimated || table.Dialect != "postgres" {
		t.Fatalf("unexpected live table fact: %#v", table)
	}
}

func TestDescribeDataToolLiveRequiresSchemaName(t *testing.T) {
	t.Parallel()

	tool := &DescribeDataTool{
		LiveDescribe: func(ctx context.Context, sourceID, schema, name string, sampleRows int) (*LiveTableDescription, error) {
			t.Fatal("describe must not be invoked without schema_name")
			return nil, nil
		},
	}
	result, err := tool.Execute(json.RawMessage(`{"table_name":"orders","sample_rows":0,"source_id":"ds_live"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.OK || payload.ErrorCode != "invalid_schema_name" {
		t.Fatalf("expected invalid_schema_name failure: %#v", payload)
	}
}

func TestDescribeDataToolLiveReturnsStructuralFacts(t *testing.T) {
	t.Parallel()

	tool := &DescribeDataTool{
		LiveDescribe: func(ctx context.Context, sourceID, schema, name string, sampleRows int) (*LiveTableDescription, error) {
			if sampleRows != 0 {
				t.Fatalf("sample_rows not forwarded: %d", sampleRows)
			}
			return &LiveTableDescription{
				SourceID:         sourceID,
				Schema:           schema,
				Name:             name,
				QualifiedName:    "public.orders",
				Dialect:          "postgres",
				RowCountEstimate: 4200,
				Estimated:        true,
				ColumnCount:      2,
				Columns: []LiveColumnFacts{
					{Name: "id", DeclaredType: "integer"},
					{Name: "amount", DeclaredType: "numeric"},
				},
				SampleRows: 0,
			}, nil
		},
	}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{"table_name":"orders","sample_rows":0,"source_id":"ds_live","schema_name":"public"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		OK             bool                   `json:"ok"`
		Scope          string                 `json:"scope"`
		RowCountSource string                 `json:"row_count_source"`
		ColumnCount    int                    `json:"column_count"`
		Sample         map[string]interface{} `json:"sample"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.OK || payload.Scope != "live_source" || payload.RowCountSource != "upstream_engine_estimate" || payload.ColumnCount != 2 {
		t.Fatalf("unexpected live describe payload: %#v", payload)
	}
	if payload.Sample != nil {
		t.Fatalf("sample_rows=0 must not run an upstream sample")
	}
}

func TestQueryDataToolLocalScopeUnchanged(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_local_scope"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE t (a INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	tool := &QueryDataTool{Ingester: ing}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT a FROM t","timeout_seconds":5}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		OK    bool   `json:"ok"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.OK || payload.Scope != "session_local" {
		t.Fatalf("unexpected local payload: %#v", payload)
	}
}
