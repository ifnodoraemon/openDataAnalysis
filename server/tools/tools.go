package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
)

const liveQueryMaxRows = 201

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &ListTablesTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker, LiveTables: ctx.LiveTablesProvider}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &DescribeDataTool{
			Ingester:     ctx.Ingester,
			QueryLocker:  ctx.QueryLocker,
			LiveDescribe: ctx.LiveTableDescribeProvider,
		}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &QueryDataTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker, ReportState: ctx.ReportState, LiveQuery: ctx.LiveQueryProvider}
	})
}

// ListTablesTool 列出所有已导入的表
type ListTablesTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
	LiveTables  LiveTablesProvider
	childCtx    context.Context
}

func (t *ListTablesTool) SetExecutionContext(ctx context.Context) { t.childCtx = ctx }

func (t *ListTablesTool) Name() string { return "data_list_tables" }
func (t *ListTablesTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *ListTablesTool) Description() string {
	return "List queryable tables. With no source_id, returns tables in the session-local SQLite database (imported files). With source_id, returns the objects of that live-bound database source with schema-qualified names, engine row-count estimates, and the upstream dialect. Does not modify any state. Failure conditions: database not initialized, or source_id is not a live-bound session source."
}
func (t *ListTablesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"source_id":{"type":"string","description":"Optional live database source ID; omit it to list session-local SQLite tables"}},"required":[]}`)
}

func (t *ListTablesTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		SourceID string `json:"source_id"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if strings.TrimSpace(params.SourceID) != "" {
		return t.executeLive(params.SourceID)
	}
	return t.executeLocal()
}

func (t *ListTablesTool) toolContext() (context.Context, error) {
	if t.childCtx != nil {
		return t.childCtx, nil
	}
	return nil, fmt.Errorf("execution context is not initialized for this tool call")
}

func (t *ListTablesTool) executeLive(sourceID string) (string, error) {
	if t.LiveTables == nil {
		return toolFailure("data_list_tables", "live_unavailable", "live table listing is not configured for this session", map[string]interface{}{"source_id": sourceID}), nil
	}
	ctx, ctxErr := t.toolContext()
	if ctxErr != nil {
		return toolFailure("data_list_tables", "execution_context_missing", ctxErr.Error(), map[string]interface{}{"source_id": sourceID}), nil
	}
	facts, err := t.LiveTables(ctx, sourceID)
	if err != nil {
		return toolFailure("data_list_tables", "live_table_list_failed", "failed to list live source tables", map[string]interface{}{
			"source_id": sourceID,
			"detail":    err.Error(),
		}), nil
	}
	tables := make([]map[string]interface{}, 0, len(facts))
	for _, fact := range facts {
		tables = append(tables, map[string]interface{}{
			"schema":             fact.Schema,
			"name":               fact.Name,
			"qualified_name":     fact.QualifiedName,
			"kind":               fact.Kind,
			"row_count_estimate": fact.RowCountEstimate,
			"estimated":          fact.Estimated,
			"profile_id":         fact.ProfileID,
			"snapshot_id":        fact.SnapshotID,
			"dialect":            fact.Dialect,
		})
	}
	payload := map[string]interface{}{
		"source_id":   sourceID,
		"scope":       "live_source",
		"table_count": len(tables),
		"tables":      tables,
		"empty":       len(tables) == 0,
		"ui_summary":  fmt.Sprintf("数据源 %s 当前绑定 %d 个实时对象。", sourceID, len(tables)),
	}
	return toolSuccess("data_list_tables", payload), nil
}

func (t *ListTablesTool) executeLocal() (string, error) {
	if t.Ingester == nil {
		return "", fmt.Errorf("database not initialized")
	}
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
		defer t.QueryLocker.RUnlockQuery()
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_oda_%' ORDER BY name")
	if err != nil {
		return "", fmt.Errorf("failed to query table list: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", fmt.Errorf("failed to read table list: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("failed while reading table list: %w", err)
	}

	if len(tables) == 0 {
		return toolSuccess("data_list_tables", map[string]interface{}{
			"scope":       "session_local",
			"table_count": 0,
			"tables":      []string{},
			"empty":       true,
			"ui_summary":  "当前没有已导入的数据表。",
		}), nil
	}

	return toolSuccess("data_list_tables", map[string]interface{}{
		"scope":       "session_local",
		"table_count": len(tables),
		"tables":      tables,
		"empty":       false,
		"ui_summary":  fmt.Sprintf("当前共有 %d 个已导入数据表。", len(tables)),
	}), nil
}

// DescribeDataTool 获取数据 Schema 和统计摘要
type DescribeDataTool struct {
	Ingester     *data.Ingester
	QueryLocker  QueryLocker
	LiveDescribe LiveTableDescribeProvider
	childCtx     context.Context
}

func (t *DescribeDataTool) SetExecutionContext(ctx context.Context) { t.childCtx = ctx }

func (t *DescribeDataTool) Name() string { return "data_describe_table" }
func (t *DescribeDataTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *DescribeDataTool) Description() string {
	return "Return observed schema facts for a specified table. With no source_id, reads the session-local SQLite database: sample_rows is explicit, 0 requests exact statistics and a positive value requests a bounded structural sample. With source_id, reads a live-bound database object: columns come from the upstream catalog (structural facts only, no computed statistics), row_count_estimate is an engine estimate, and a positive sample_rows fetches a bounded upstream sample while 0 skips the upstream query. Does not assign meaning to fields, apply confirmation patches, or select an interpretation."
}
func (t *DescribeDataTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"table_name": {"type": "string", "description": "Exact table name"},
			"sample_rows": {"type": "integer", "minimum": 0, "maximum": 10000, "description": "Explicit structural-statistics sample bound; 0 means exact full-table statistics for session-local tables and no upstream sample for live sources"},
			"source_id": {"type": "string", "description": "Optional live database source ID; omit it to describe a session-local SQLite table"},
			"schema_name": {"type": "string", "description": "Required with source_id: the upstream schema (database) of the object"}
		},
		"required": ["table_name", "sample_rows"]
	}`)
}

func (t *DescribeDataTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		TableName  string `json:"table_name"`
		SampleRows *int   `json:"sample_rows"`
		SourceID   string `json:"source_id"`
		SchemaName string `json:"schema_name"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if params.SampleRows == nil || *params.SampleRows < 0 || *params.SampleRows > 10000 {
		return toolFailure("data_describe_table", "invalid_sample_rows", "sample_rows must be explicitly set between 0 and 10000", map[string]interface{}{"sample_rows": params.SampleRows}), nil
	}
	if strings.TrimSpace(params.SourceID) != "" {
		return t.executeLive(params)
	}
	return t.executeLocal(params.TableName, *params.SampleRows)
}

func (t *DescribeDataTool) toolContext() (context.Context, error) {
	if t.childCtx != nil {
		return t.childCtx, nil
	}
	return nil, fmt.Errorf("execution context is not initialized for this tool call")
}

func (t *DescribeDataTool) executeLive(params struct {
	TableName  string `json:"table_name"`
	SampleRows *int   `json:"sample_rows"`
	SourceID   string `json:"source_id"`
	SchemaName string `json:"schema_name"`
}) (string, error) {
	if t.LiveDescribe == nil {
		return toolFailure("data_describe_table", "live_unavailable", "live table description is not configured for this session", map[string]interface{}{"source_id": params.SourceID}), nil
	}
	if strings.TrimSpace(params.SchemaName) == "" {
		return toolFailure("data_describe_table", "invalid_schema_name", "schema_name is required when source_id is set", map[string]interface{}{
			"source_id": params.SourceID,
		}), nil
	}
	description, err := func() (*LiveTableDescription, error) {
		ctx, ctxErr := t.toolContext()
		if ctxErr != nil {
			return nil, ctxErr
		}
		return t.LiveDescribe(ctx, params.SourceID, params.SchemaName, params.TableName, *params.SampleRows)
	}()
	if err != nil {
		return toolFailure("data_describe_table", "schema_lookup_failed", "failed to read live table structure", map[string]interface{}{
			"source_id":   params.SourceID,
			"schema_name": params.SchemaName,
			"table_name":  params.TableName,
			"detail":      err.Error(),
		}), nil
	}
	columns := make([]map[string]interface{}, 0, len(description.Columns))
	for _, column := range description.Columns {
		columns = append(columns, map[string]interface{}{
			"name":          column.Name,
			"declared_type": column.DeclaredType,
		})
	}
	payload := map[string]interface{}{
		"source_id":        description.SourceID,
		"scope":            "live_source",
		"schema_name":      description.Schema,
		"table_name":       description.Name,
		"qualified_name":   description.QualifiedName,
		"dialect":          description.Dialect,
		"row_count":        description.RowCountEstimate,
		"row_count_source": "upstream_engine_estimate",
		"column_count":     description.ColumnCount,
		"columns":          columns,
		"sample_rows":      description.SampleRows,
		"sampling":         map[string]interface{}{"method": "live_catalog", "estimated": true},
		"ui_summary":       fmt.Sprintf("数据表 %s 的结构检查已完成，共 %d 列。", description.QualifiedName, description.ColumnCount),
	}
	if description.Sample != nil {
		payload["sample"] = map[string]interface{}{
			"columns":   description.Sample.Columns,
			"rows":      description.Sample.Rows,
			"row_count": len(description.Sample.Rows),
		}
	}
	if len(description.Warnings) > 0 {
		payload["warnings"] = description.Warnings
	}
	return toolSuccess("data_describe_table", payload), nil
}

func (t *DescribeDataTool) executeLocal(tableName string, sampleRows int) (string, error) {
	if t.Ingester == nil {
		return "", fmt.Errorf("database not initialized")
	}
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	if err := data.ValidateSQLIdent(tableName); err != nil {
		return toolFailure("data_describe_table", "invalid_table_name", err.Error(), map[string]interface{}{
			"table_name": tableName,
		}), nil
	}

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
	}
	defer func() {
		if t.QueryLocker != nil {
			t.QueryLocker.RUnlockQuery()
		}
	}()

	schema, err := data.ExtractSchemaBounded(db, tableName, sampleRows)
	if err != nil {
		return toolFailure("data_describe_table", "schema_lookup_failed", "failed to read table structure", map[string]interface{}{
			"table_name": tableName,
			"detail":     err.Error(),
		}), nil
	}
	uiSummary := fmt.Sprintf("数据表 %s 的结构检查已完成，共 %d 列、%d 行。", schema.TableName, len(schema.Columns), schema.RowCount)

	result := map[string]interface{}{
		"table_name":   schema.TableName,
		"row_count":    schema.RowCount,
		"column_count": len(schema.Columns),
		"schema":       schema,
		"sampling":     schema.Sampling,
		"ui_summary":   uiSummary,
	}

	return toolSuccess("data_describe_table", result), nil
}

type QueryDataTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
	ReportState *ReportState
	LiveQuery   LiveQueryProvider
	childCtx    context.Context
}

func (t *QueryDataTool) SetExecutionContext(ctx context.Context) { t.childCtx = ctx }

func (t *QueryDataTool) Name() string { return "data_query_sql" }
func (t *QueryDataTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *QueryDataTool) Description() string {
	return "Execute a single read-only SQL query. With no source_id, the query runs on the session-local SQLite database (imported files), SQLite dialect. With source_id, the query runs directly against that live-bound database in its own dialect (postgres or mysql) inside a read-only transaction with a statement timeout; the query uses upstream schema-qualified table names and returns at most 200 rows. Only SELECT or WITH statements are allowed; INSERT/UPDATE/DELETE/DDL are forbidden. A single query touches exactly one source; queries cannot join data across different sources. Returns result_id, sql, row_count, columns, rows, and with source_id also the source_id and dialect. Maximum 200 rows returned; queries exceeding this row limit fail. timeout_seconds is a required explicit 1-30 second execution bound."
}
func (t *QueryDataTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"sql": {"type": "string", "description": "The SQL SELECT query to execute"},
			"timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 30, "description": "Explicit execution time bound in seconds."},
			"source_id": {"type": "string", "description": "Optional live database source ID; omit it to query the session-local SQLite database"}
		},
		"required": ["sql", "timeout_seconds"]
	}`)
}

func (t *QueryDataTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		SQL            string `json:"sql"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
		SourceID       string `json:"source_id"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	if params.TimeoutSeconds == nil || *params.TimeoutSeconds < 1 || *params.TimeoutSeconds > int(data.QueryTimeoutLarge/time.Second) {
		provided := 0
		if params.TimeoutSeconds != nil {
			provided = *params.TimeoutSeconds
		}
		return toolFailure("data_query_sql", "invalid_timeout", "timeout_seconds must be explicitly set between 1 and 30", map[string]interface{}{
			"timeout_seconds": provided,
		}), nil
	}

	if strings.TrimSpace(params.SourceID) != "" {
		return t.executeLive(params.SQL, *params.TimeoutSeconds, params.SourceID)
	}
	return t.executeLocal(params.SQL, *params.TimeoutSeconds)
}

func (t *QueryDataTool) executeLive(sql string, timeoutSeconds int, sourceID string) (string, error) {
	if t.LiveQuery == nil {
		return toolFailure("data_query_sql", "live_unavailable", "live query execution is not configured for this session", map[string]interface{}{"source_id": sourceID}), nil
	}
	if t.childCtx == nil {
		return toolFailure("data_query_sql", "missing_execution_context", "tool execution context is not initialized", nil), nil
	}
	liveResult, err := t.LiveQuery(t.childCtx, LiveQueryRequest{
		SourceID:       sourceID,
		SQL:            sql,
		TimeoutSeconds: timeoutSeconds,
		MaxRows:        liveQueryMaxRows,
	})
	if err != nil {
		return toolFailure("data_query_sql", "query_failed", "live SQL execution failed", map[string]interface{}{
			"sql":       sql,
			"source_id": sourceID,
			"detail":    err.Error(),
		}), nil
	}

	resultID := "res_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	rows := liveResult.Rows
	columns := liveResult.Columns
	if t.ReportState != nil {
		if err := t.ReportState.RecordResult(AnalysisResult{
			ID: resultID, ToolName: "data_query_sql", Operation: sql,
			Columns: columns, Rows: rows, RowCount: len(rows),
			SourceID: sourceID, Dialect: liveResult.Dialect,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return "", fmt.Errorf("failed to record analysis result: %w", err)
		}
	}
	metrics.AnalysisResultsTotal.WithLabelValues("data_query_sql").Inc()
	return toolSuccess("data_query_sql", map[string]interface{}{
		"result_id":  resultID,
		"sql":        sql,
		"source_id":  sourceID,
		"dialect":    liveResult.Dialect,
		"scope":      "live_source",
		"row_count":  len(rows),
		"columns":    columns,
		"rows":       rows,
		"ui_summary": fmt.Sprintf("实时查询成功，返回 %d 行。", len(rows)),
	}), nil
}

func (t *QueryDataTool) executeLocal(sql string, timeoutSeconds int) (string, error) {
	if t.Ingester == nil {
		return "", fmt.Errorf("database not initialized")
	}
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
		defer t.QueryLocker.RUnlockQuery()
	}
	execCtx := t.childCtx
	if execCtx == nil {
		return toolFailure("data_query_sql", "missing_execution_context", "tool execution context is not initialized", nil), nil
	}
	queryResult, err := data.ExecuteQueryDetailedContext(execCtx, db, sql, time.Duration(timeoutSeconds)*time.Second)
	if err != nil {
		return toolFailure("data_query_sql", "query_failed", "SQL execution failed", map[string]interface{}{
			"sql":    sql,
			"detail": err.Error(),
		}), nil
	}
	rows := queryResult.Rows

	resultID := "res_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	columns := queryResult.Columns
	if t.ReportState != nil {
		if err := t.ReportState.RecordResult(AnalysisResult{
			ID: resultID, ToolName: "data_query_sql", Operation: sql,
			Columns: columns, Rows: rows, RowCount: len(rows), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return "", fmt.Errorf("failed to record analysis result: %w", err)
		}
	}
	metrics.AnalysisResultsTotal.WithLabelValues("data_query_sql").Inc()
	return toolSuccess("data_query_sql", map[string]interface{}{
		"result_id":  resultID,
		"sql":        sql,
		"scope":      "session_local",
		"row_count":  len(rows),
		"columns":    columns,
		"rows":       rows,
		"ui_summary": fmt.Sprintf("查询成功，返回 %d 行。", len(rows)),
	}), nil
}
