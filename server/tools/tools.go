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

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &ListTablesTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &DescribeDataTool{
			Ingester:    ctx.Ingester,
			QueryLocker: ctx.QueryLocker,
		}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &QueryDataTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker, ReportState: ctx.ReportState}
	})
}

// ListTablesTool 列出所有已导入的表
type ListTablesTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
}

func (t *ListTablesTool) Name() string { return "data_list_tables" }
func (t *ListTablesTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *ListTablesTool) Description() string {
	return "Return a list of all imported table names in the internal database. Returns table_count, tables list, and empty flag. Does not modify any state. Failure conditions: database not initialized."
}
func (t *ListTablesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *ListTablesTool) Execute(args json.RawMessage) (string, error) {
	var params struct{}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
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
			"table_count": 0,
			"tables":      []string{},
			"empty":       true,
			"ui_summary":  "当前没有已导入的数据表。",
		}), nil
	}

	return toolSuccess("data_list_tables", map[string]interface{}{
		"table_count": len(tables),
		"tables":      tables,
		"empty":       false,
		"ui_summary":  fmt.Sprintf("当前共有 %d 个已导入数据表。", len(tables)),
	}), nil
}

// DescribeDataTool 获取数据 Schema 和统计摘要
type DescribeDataTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
}

func (t *DescribeDataTool) Name() string { return "data_describe_table" }
func (t *DescribeDataTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *DescribeDataTool) Description() string {
	return "Return observed schema facts for a specified table. sample_rows is explicit: 0 requests exact statistics; a positive value requests a bounded structural sample. It does not assign meaning to fields, apply confirmation patches, or select an interpretation."
}
func (t *DescribeDataTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"table_name": {"type": "string", "description": "Exact table name"},
			"sample_rows": {"type": "integer", "minimum": 0, "maximum": 10000, "description": "Explicit structural-statistics sample bound; 0 means exact full-table statistics"}
		},
		"required": ["table_name", "sample_rows"]
	}`)
}

func (t *DescribeDataTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		TableName  string `json:"table_name"`
		SampleRows *int   `json:"sample_rows"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	if t.Ingester == nil {
		return "", fmt.Errorf("database not initialized")
	}
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	if err := data.ValidateSQLIdent(params.TableName); err != nil {
		return toolFailure("data_describe_table", "invalid_table_name", err.Error(), map[string]interface{}{
			"table_name": params.TableName,
		}), nil
	}
	if params.SampleRows == nil || *params.SampleRows < 0 || *params.SampleRows > 10000 {
		return toolFailure("data_describe_table", "invalid_sample_rows", "sample_rows must be explicitly set between 0 and 10000", map[string]interface{}{"sample_rows": params.SampleRows}), nil
	}

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
	}
	defer func() {
		if t.QueryLocker != nil {
			t.QueryLocker.RUnlockQuery()
		}
	}()

	schema, err := data.ExtractSchemaBounded(db, params.TableName, *params.SampleRows)
	if err != nil {
		return toolFailure("data_describe_table", "schema_lookup_failed", "failed to read table structure", map[string]interface{}{
			"table_name": params.TableName,
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
	childCtx    context.Context
}

func (t *QueryDataTool) SetExecutionContext(ctx context.Context) { t.childCtx = ctx }

func (t *QueryDataTool) Name() string { return "data_query_sql" }
func (t *QueryDataTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *QueryDataTool) Description() string {
	return "Execute a single read-only SQL query on the internal database. Only SELECT or WITH statements are allowed; INSERT/UPDATE/DELETE/DDL are forbidden. Side effects: none (read-only). Returns result_id, sql, row_count, columns, and rows. Maximum 200 rows returned; queries exceeding this row limit fail. timeout_seconds is a required explicit 1-30 second execution bound. Limitations: SQLite dialect."
}
func (t *QueryDataTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"sql": {"type": "string", "description": "The SQL SELECT query to execute"},
			"timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 30, "description": "Explicit execution time bound in seconds."}
		},
		"required": ["sql", "timeout_seconds"]
	}`)
}

func (t *QueryDataTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		SQL            string `json:"sql"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	if t.Ingester == nil {
		return "", fmt.Errorf("database not initialized")
	}
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
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
	timeout := time.Duration(*params.TimeoutSeconds) * time.Second

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
		defer t.QueryLocker.RUnlockQuery()
	}
	execCtx := t.childCtx
	if execCtx == nil {
		return toolFailure("data_query_sql", "missing_execution_context", "tool execution context is not initialized", nil), nil
	}
	queryResult, err := data.ExecuteQueryDetailedContext(execCtx, db, params.SQL, timeout)
	if err != nil {
		return toolFailure("data_query_sql", "query_failed", "SQL execution failed", map[string]interface{}{
			"sql":    params.SQL,
			"detail": err.Error(),
		}), nil
	}
	rows := queryResult.Rows

	resultID := "res_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	columns := queryResult.Columns
	if t.ReportState != nil {
		if err := t.ReportState.RecordResult(AnalysisResult{
			ID: resultID, ToolName: "data_query_sql", Operation: params.SQL,
			Columns: columns, Rows: rows, RowCount: len(rows), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return "", fmt.Errorf("failed to record analysis result: %w", err)
		}
	}
	metrics.AnalysisResultsTotal.WithLabelValues("data_query_sql").Inc()
	return toolSuccess("data_query_sql", map[string]interface{}{
		"result_id":  resultID,
		"sql":        params.SQL,
		"row_count":  len(rows),
		"columns":    columns,
		"rows":       rows,
		"ui_summary": fmt.Sprintf("查询成功，返回 %d 行。", len(rows)),
	}), nil
}
