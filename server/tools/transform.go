package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &MaterializeTransformTool{Ingester: ctx.Ingester, QueryLocker: ctx.QueryLocker, ReportState: ctx.ReportState}
	})
}

type MaterializeTransformTool struct {
	Ingester    *data.Ingester
	QueryLocker QueryLocker
	ReportState *ReportState
	childCtx    context.Context
}

func (t *MaterializeTransformTool) SetExecutionContext(ctx context.Context) { t.childCtx = ctx }

func (t *MaterializeTransformTool) Name() string { return "data_transform_materialize" }
func (t *MaterializeTransformTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, Delegable: true}
}
func (t *MaterializeTransformTool) Description() string {
	return "Materialize one model-supplied read-only SELECT/WITH query as a session-local derived table. It writes the bounded derived table and optional result lineage; it does not choose the query or interpret its fields."
}
func (t *MaterializeTransformTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"sql":{"type":"string"},"output_table":{"type":"string"},"max_rows":{"type":"integer","minimum":1,"maximum":1000000},"timeout_seconds":{"type":"integer","minimum":1,"maximum":30},"replace":{"type":"boolean"},"source_result_ids":{"type":"array","items":{"type":"string"}}},"required":["sql","output_table","max_rows","timeout_seconds","replace"]}`)
}

func (t *MaterializeTransformTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		SQL             string   `json:"sql"`
		OutputTable     string   `json:"output_table"`
		MaxRows         int      `json:"max_rows"`
		TimeoutSeconds  *int     `json:"timeout_seconds"`
		Replace         *bool    `json:"replace"`
		SourceResultIDs []string `json:"source_result_ids"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if params.OutputTable != strings.TrimSpace(params.OutputTable) {
		return toolFailure(t.Name(), "invalid_output_table", "output_table must not contain leading or trailing whitespace", nil), nil
	}
	if data.ValidateSQLIdent(params.OutputTable) != nil || strings.HasPrefix(params.OutputTable, "_oda_") {
		return toolFailure(t.Name(), "invalid_output_table", "output_table must be a plain non-reserved SQL identifier", nil), nil
	}
	query, err := data.NormalizeReadOnlyQuery(params.SQL)
	if err != nil {
		return toolFailure(t.Name(), "invalid_query", "transformation must be a single read-only SELECT/WITH query", map[string]interface{}{"detail": err.Error()}), nil
	}
	if params.MaxRows <= 0 || params.MaxRows > 1000000 {
		return toolFailure(t.Name(), "row_bound_exceeded", "max_rows must be between 1 and 1000000", nil), nil
	}
	if params.TimeoutSeconds == nil || *params.TimeoutSeconds < 1 || *params.TimeoutSeconds > 30 {
		return toolFailure(t.Name(), "invalid_timeout", "timeout_seconds must be explicitly set between 1 and 30", nil), nil
	}
	if params.Replace == nil {
		return toolFailure(t.Name(), "replace_required", "replace must be explicitly true or false", nil), nil
	}
	seenResultIDs := make(map[string]struct{}, len(params.SourceResultIDs))
	for _, resultID := range params.SourceResultIDs {
		if resultID == "" || resultID != strings.TrimSpace(resultID) {
			return toolFailure(t.Name(), "invalid_source_result", "source_result_ids must be non-empty exact IDs", map[string]interface{}{"result_id": resultID}), nil
		}
		if _, exists := seenResultIDs[resultID]; exists {
			return toolFailure(t.Name(), "duplicate_source_result", "source_result_ids must not contain duplicates", map[string]interface{}{"result_id": resultID}), nil
		}
		seenResultIDs[resultID] = struct{}{}
		if t.ReportState == nil {
			return toolFailure(t.Name(), "lineage_store_unavailable", "result lineage store is unavailable", nil), nil
		}
		t.ReportState.RLock()
		_, ok := t.ReportState.Results[resultID]
		t.ReportState.RUnlock()
		if !ok {
			return toolFailure(t.Name(), "unknown_source_result", "source_result_id does not exist", map[string]interface{}{"result_id": resultID}), nil
		}
	}

	if t.Ingester == nil {
		return "", fmt.Errorf("database not initialized")
	}
	db := t.Ingester.GetDB()
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}
	if locker, ok := t.QueryLocker.(QueryMutationLocker); ok {
		locker.LockQuery()
		defer locker.UnlockQuery()
	}
	parentCtx := t.childCtx
	if parentCtx == nil {
		return toolFailure(t.Name(), "missing_execution_context", "tool execution context is not initialized", nil), nil
	}
	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(*params.TimeoutSeconds)*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, params.OutputTable).Scan(&existing); err != nil {
		return "", fmt.Errorf("failed to inspect output table: %w", err)
	}
	if existing > 0 && !*params.Replace {
		return toolFailure(t.Name(), "output_exists", "output table already exists and replace=false", map[string]interface{}{"output_table": params.OutputTable}), nil
	}
	probeRows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM (%s) AS _oda_transform_probe LIMIT 0`, query))
	if err != nil {
		return toolFailure(t.Name(), "query_probe_failed", "transformation query could not be inspected", map[string]interface{}{"detail": err.Error()}), nil
	}
	columns, err := probeRows.Columns()
	closeErr := probeRows.Close()
	if err != nil || closeErr != nil {
		return "", fmt.Errorf("failed to inspect transformation columns: %w", errors.Join(err, closeErr))
	}
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, exists := seenColumns[column]; exists {
			return toolFailure(t.Name(), "duplicate_output_column", "transformation query has duplicate output columns; use explicit aliases", map[string]interface{}{"column": column}), nil
		}
		seenColumns[column] = struct{}{}
	}
	if existing > 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE "%s"`, params.OutputTable)); err != nil {
			return "", err
		}
	}
	buildMaterializeSQL := func(limit int) string {
		return fmt.Sprintf(`CREATE TABLE "%s" AS SELECT * FROM (%s) AS _oda_transform LIMIT %d`, params.OutputTable, query, limit)
	}
	if _, err := tx.ExecContext(ctx, buildMaterializeSQL(params.MaxRows+1)); err != nil {
		return toolFailure(t.Name(), "materialize_failed", "derived table could not be materialized", map[string]interface{}{"detail": err.Error()}), nil
	}
	var rowCount int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, params.OutputTable)).Scan(&rowCount); err != nil {
		return "", fmt.Errorf("failed to count materialized rows: %w", err)
	}
	truncated := rowCount > params.MaxRows
	if truncated {
		rowIDColumn := ""
		for _, candidate := range []string{"_rowid_", "oid", "rowid"} {
			if _, taken := seenColumns[candidate]; !taken {
				rowIDColumn = candidate
				break
			}
		}
		if rowIDColumn != "" {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE %s > %d`, params.OutputTable, rowIDColumn, params.MaxRows)); err != nil {
				return "", fmt.Errorf("failed to apply materialization row bound: %w", err)
			}
			rowCount = params.MaxRows
		} else {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE "%s"`, params.OutputTable)); err != nil {
				return "", fmt.Errorf("failed to apply materialization row bound: %w", err)
			}
			if _, err := tx.ExecContext(ctx, buildMaterializeSQL(params.MaxRows)); err != nil {
				return toolFailure(t.Name(), "materialize_failed", "derived table could not be materialized", map[string]interface{}{"detail": err.Error()}), nil
			}
			if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, params.OutputTable)).Scan(&rowCount); err != nil {
				return "", fmt.Errorf("failed to count materialized rows: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _oda_derived_lineage (derived_id TEXT PRIMARY KEY, table_name TEXT NOT NULL, sql TEXT NOT NULL, source_result_ids TEXT NOT NULL, row_count INTEGER NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		return "", err
	}
	derivedID := "drv_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	lineageJSON, err := json.Marshal(params.SourceResultIDs)
	if err != nil {
		return "", fmt.Errorf("failed to encode result lineage: %w", err)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO _oda_derived_lineage (derived_id, table_name, sql, source_result_ids, row_count, created_at) VALUES (?, ?, ?, ?, ?, ?)`, derivedID, params.OutputTable, query, string(lineageJSON), rowCount, createdAt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return toolSuccess(t.Name(), map[string]interface{}{
		"derived_id": derivedID, "output_table": params.OutputTable, "row_count": rowCount,
		"row_bound": params.MaxRows, "truncated": truncated, "source_result_ids": params.SourceResultIDs,
		"ui_summary": fmt.Sprintf("派生表 %s 已生成，共 %d 行。", params.OutputTable, rowCount),
	}), nil
}
