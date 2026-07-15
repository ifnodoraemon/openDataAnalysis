package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/data"
)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &ListTablesTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &DescribeDataTool{
			Ingester:                   ctx.Ingester,
			ConfirmedOverridesProvider: ctx.ConfirmedOverridesProvider,
			KnownRowCount:              ctx.KnownRowCount,
			QueryLocker:                ctx.QueryLocker,
		}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &QueryDataTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker}
	})
}

// ListTablesTool 列出所有已导入的表
type ListTablesTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
}

func (t *ListTablesTool) Name() string { return "data_list_tables" }
func (t *ListTablesTool) Description() string {
	return "Return a list of all imported table names in the internal database. Returns table_count, tables list, and empty flag. Does not modify any state. Failure conditions: database not initialized."
}
func (t *ListTablesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

func (t *ListTablesTool) Execute(args json.RawMessage) (string, error) {
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_oda_%' ORDER BY name")
	if t.QueryLocker != nil {
		t.QueryLocker.RUnlockQuery()
	}
	if err != nil {
		return "", fmt.Errorf("failed to query table list: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			tables = append(tables, name)
		}
	}

	if len(tables) == 0 {
		return toolSuccess("data_list_tables", map[string]interface{}{
			"table_count": 0,
			"tables":      []string{},
			"empty":       true,
			"ui_summary":  "no imported data tables yet",
		}), nil
	}

	return toolSuccess("data_list_tables", map[string]interface{}{
		"table_count": len(tables),
		"tables":      tables,
		"empty":       false,
		"ui_summary":  fmt.Sprintf("%d tables imported", len(tables)),
	}), nil
}

type ConfirmedOverridesProvider func(tableName string) map[string]interface{}

// DescribeDataTool 获取数据 Schema 和统计摘要
type KnownRowCountProvider func(tableName string) (int, bool)

type DescribeDataTool struct {
	Ingester                   *data.Ingester
	ConfirmedOverridesProvider ConfirmedOverridesProvider
	KnownRowCount              KnownRowCountProvider
	QueryLocker                QueryLocker
}

func (t *DescribeDataTool) Name() string { return "data_describe_table" }
func (t *DescribeDataTool) Description() string {
	return "Return the schema and statistical summary for a specified table, including column info, row count, basic statistics, sample values, and confirmed overrides. Unconfirmed semantic candidates are returned with confirmed=false labels; only confirmed overrides are applied to the displayed values."
}
func (t *DescribeDataTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"table_name": {"type": "string", "description": "Table name"}
		},
		"required": ["table_name"]
	}`)
}

func (t *DescribeDataTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		TableName string `json:"table_name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
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

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
	}
	defer func() {
		if t.QueryLocker != nil {
			t.QueryLocker.RUnlockQuery()
		}
	}()

	var rowCount int
	useKnownRowCount := false
	if t.KnownRowCount != nil {
		if known, ok := t.KnownRowCount(params.TableName); ok {
			rowCount = known
			useKnownRowCount = true
		}
	}
	if !useKnownRowCount {
		if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", params.TableName)).Scan(&rowCount); err != nil {
			return toolFailure("data_describe_table", "row_count_failed", "failed to count rows", map[string]interface{}{
				"table_name": params.TableName,
				"detail":     err.Error(),
			}), nil
		}
	}

	var schema *data.SchemaInfo
	var err error
	if rowCount > 10000 {
		schema, err = data.ExtractSchemaSampled(db, params.TableName)
	} else {
		schema, err = data.ExtractSchema(db, params.TableName)
	}
	if err != nil {
		return toolFailure("data_describe_table", "schema_lookup_failed", "failed to read table structure", map[string]interface{}{
			"table_name": params.TableName,
			"detail":     err.Error(),
		}), nil
	}
	ambiguousMetricGroups := data.InferAmbiguousMetricGroups(schema.Columns)
	uiSummary := fmt.Sprintf("table %s schema analysis complete, %d columns, %d rows", schema.TableName, len(schema.Columns), schema.RowCount)
	if len(ambiguousMetricGroups) > 0 {
		uiSummary += fmt.Sprintf("; %d ambiguous metric candidate groups found", len(ambiguousMetricGroups))
	}
	primaryTimeColumn := choosePrimaryTimeColumn(schema.TimeColumns)
	if primaryTimeColumn != nil && primaryTimeColumn.CoverageStart != "" && primaryTimeColumn.CoverageEnd != "" {
		uiSummary += fmt.Sprintf("; candidate primary time field %s is %s grain, covering %s to %s", primaryTimeColumn.Name, primaryTimeColumn.Grain, primaryTimeColumn.CoverageStart, primaryTimeColumn.CoverageEnd)
	}

	timeColumnCandidates := make([]map[string]interface{}, 0, len(schema.TimeColumns))
	for _, tc := range schema.TimeColumns {
		candidate := map[string]interface{}{
			"column_name": tc.Name,
			"grain":       tc.Grain,
			"confirmed":   false,
		}
		if tc.CoverageStart != "" {
			candidate["coverage_start"] = tc.CoverageStart
		}
		if tc.CoverageEnd != "" {
			candidate["coverage_end"] = tc.CoverageEnd
		}
		if primaryTimeColumn != nil && tc.Name == primaryTimeColumn.Name {
			candidate["heuristic_primary"] = true
		}
		timeColumnCandidates = append(timeColumnCandidates, candidate)
	}

	schemaForResult := *schema
	schemaForResult.TimeColumns = nil

	result := map[string]interface{}{
		"table_name":                   schemaForResult.TableName,
		"row_count":                    schemaForResult.RowCount,
		"column_count":                 len(schemaForResult.Columns),
		"schema":                       schemaForResult,
		"time_column_count":            len(schema.TimeColumns),
		"time_column_candidates":       timeColumnCandidates,
		"ambiguous_metric_group_count": len(ambiguousMetricGroups),
		"ambiguous_metric_groups":      ambiguousMetricGroups,
		"note":                         "time_column_candidates and ambiguous_metric_groups are inferred candidates, not confirmed facts; check confirmed_sections for user-validated selections",
		"ui_summary":                   uiSummary,
	}

	if t.ConfirmedOverridesProvider != nil {
		if overrides := t.ConfirmedOverridesProvider(params.TableName); len(overrides) > 0 {
			result["confirmed_sections"] = overrides
		}
	}

	return toolSuccess("data_describe_table", result), nil
}

func choosePrimaryTimeColumn(columns []data.TimeColumnInfo) *data.TimeColumnInfo {
	if len(columns) == 0 {
		return nil
	}
	best := columns[0]
	for _, item := range columns[1:] {
		if item.DistinctPeriodCount > best.DistinctPeriodCount {
			best = item
			continue
		}
		if item.DistinctPeriodCount == best.DistinctPeriodCount && strings.TrimSpace(item.Name) < strings.TrimSpace(best.Name) {
			best = item
		}
	}
	return &best
}

type QueryDataTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
}

func (t *QueryDataTool) Name() string { return "data_query_sql" }
func (t *QueryDataTool) Description() string {
	return "Execute a single read-only SQL query on the internal database. Only SELECT or WITH statements are allowed; INSERT/UPDATE/DELETE/DDL are forbidden. Side effects: none (read-only). Returns sql, row_count, columns, and rows. Maximum 200 rows returned; queries exceeding this row limit will fail. Failure conditions: SQL syntax error, reference to nonexistent table or column, execution timeout, row limit exceeded. Limitations: only SQLite dialect supported. Large tables (>100K rows) get a 30s timeout; others get 5s."
}
func (t *QueryDataTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"sql": {"type": "string", "description": "The SQL SELECT query to execute"},
			"timeout_mode": {"type": "string", "description": "Timeout mode: 'auto' (default, detects from table size), 'quick' (5s), 'large_aggregate' (30s for complex aggregates on any table size)", "enum": ["auto", "quick", "large_aggregate"], "default": "auto"}
		},
		"required": ["sql"]
	}`)
}

func (t *QueryDataTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		SQL         string `json:"sql"`
		TimeoutMode string `json:"timeout_mode"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	var timeout time.Duration
	switch params.TimeoutMode {
	case "quick":
		timeout = data.QueryTimeoutQuick
	case "large_aggregate":
		timeout = data.QueryTimeoutLarge
	default:
		timeout = data.QueryTimeoutForDB(db, params.SQL)
	}

	if t.QueryLocker != nil {
		t.QueryLocker.RLockQuery()
	}
	rows, err := data.ExecuteQueryWithTimeout(db, params.SQL, timeout)
	if t.QueryLocker != nil {
		t.QueryLocker.RUnlockQuery()
	}
	if err != nil {
		return toolFailure("data_query_sql", "query_failed", "SQL execution failed", map[string]interface{}{
			"sql":    params.SQL,
			"detail": err.Error(),
		}), nil
	}

	return toolSuccess("data_query_sql", map[string]interface{}{
		"sql":        params.SQL,
		"row_count":  len(rows),
		"columns":    queryResultColumns(rows),
		"rows":       rows,
		"ui_summary": fmt.Sprintf("SQL query succeeded, %d rows returned", len(rows)),
	}), nil
}

func queryResultColumns(rows []map[string]interface{}) []string {
	if len(rows) == 0 {
		return []string{}
	}

	columns := make([]string, 0, len(rows[0]))
	for name := range rows[0] {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	return columns
}
