package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	QueryTimeoutQuick       = 5 * time.Second
	QueryTimeoutLarge       = 30 * time.Second
	queryTimeout            = QueryTimeoutQuick
	queryRowLimit           = 200
	queryProbeRows          = queryRowLimit + 1
	schemaDistinctProbeRows = 200
)

// SchemaInfo 表 Schema 信息
type SchemaInfo struct {
	TableName string       `json:"tableName"`
	RowCount  int          `json:"rowCount"`
	Columns   []ColumnInfo `json:"columns"`
	Sampling  SamplingInfo `json:"sampling"`
}

type SamplingInfo struct {
	Method     string `json:"method"`
	SourceRows int    `json:"sourceRows"`
	SampleRows int    `json:"sampleRows"`
	Estimated  bool   `json:"estimated"`
}

// ColumnInfo 列信息
type ColumnInfo struct {
	Name         string   `json:"name"`
	DeclaredType string   `json:"declaredType"`
	NonEmptyRate float64  `json:"nonEmptyRate"`
	UniqueCount  int      `json:"uniqueCount"`
	SampleValues []string `json:"sampleValues"`
	Estimated    bool     `json:"estimated"`
}

// ExtractSchema 提取表的 Schema 和统计摘要 (full table scan)
func ExtractSchema(db *sql.DB, tableName string) (*SchemaInfo, error) {
	return extractSchemaInternal(db, tableName, 0)
}

// ExtractSchemaBounded uses an explicit positive row bound for structural
// statistics. A zero bound requests exact full-table statistics.
func ExtractSchemaBounded(db *sql.DB, tableName string, sampleRows int) (*SchemaInfo, error) {
	if sampleRows < 0 {
		return nil, fmt.Errorf("sample row bound cannot be negative")
	}
	return extractSchemaInternal(db, tableName, sampleRows)
}

func extractSchemaInternal(db *sql.DB, tableName string, sampleRows int) (result *SchemaInfo, resultErr error) {
	if err := ValidateSQLIdent(tableName); err != nil {
		return nil, fmt.Errorf("invalid table name: %w", err)
	}
	quotedTable, err := QuoteSQLiteIdentifier(tableName)
	if err != nil {
		return nil, err
	}
	schema := &SchemaInfo{TableName: tableName}

	// 获取行数 (always exact — COUNT(*) is a single scan)
	var rowCount int
	err = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedTable)).Scan(&rowCount)
	if err != nil {
		return nil, fmt.Errorf("failed to get row count: %w", err)
	}
	schema.RowCount = rowCount
	schema.Sampling = SamplingInfo{Method: "full_table", SourceRows: rowCount, SampleRows: rowCount}

	// For sampled mode, create a temporary bounded sample view
	sampleTable := ""
	actuallySampled := false
	if sampleRows > 0 && rowCount > sampleRows {
		sampleTable = "_oda_sample_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		quotedSampleTable, err := QuoteSQLiteIdentifier(sampleTable)
		if err != nil {
			return nil, err
		}
		if _, err := db.Exec(fmt.Sprintf("DROP VIEW IF EXISTS %s", quotedSampleTable)); err != nil {
			return nil, fmt.Errorf("failed to clear bounded schema sample: %w", err)
		}
		stride := int(math.Ceil(float64(rowCount) / float64(sampleRows)))
		_, err = db.Exec(fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT * FROM %s WHERE (rowid - 1) %% %d = 0 LIMIT %d", quotedSampleTable, quotedTable, stride, sampleRows))
		if err != nil {
			return nil, fmt.Errorf("failed to create bounded schema sample: %w", err)
		}
		actuallySampled = true
		defer func() {
			if _, err := db.Exec(fmt.Sprintf("DROP VIEW IF EXISTS %s", quotedSampleTable)); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("failed to drop bounded schema sample: %w", err))
			}
		}()
		var sampleRows int
		if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedSampleTable)).Scan(&sampleRows); err != nil {
			return nil, fmt.Errorf("failed to count bounded schema sample: %w", err)
		}
		schema.Sampling = SamplingInfo{Method: "systematic_rowid_stride", SourceRows: rowCount, SampleRows: sampleRows, Estimated: true}
	}
	queryTable := tableName
	if sampleTable != "" {
		queryTable = sampleTable
	}
	quotedQueryTable, err := QuoteSQLiteIdentifier(queryTable)
	if err != nil {
		return nil, err
	}

	// 获取列信息
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", quotedTable))
	if err != nil {
		return nil, fmt.Errorf("failed to get column info: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close column metadata rows: %w", closeErr))
		}
	}()

	type observedColumn struct {
		name         string
		declaredType string
	}
	var columns []observedColumn
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultVal, pk interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("failed to read column metadata: %w", err)
		}
		columns = append(columns, observedColumn{name: name, declaredType: colType})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed while reading column metadata: %w", err)
	}

	// 分析每一列
	for _, observed := range columns {
		col := observed.name
		quotedCol, err := QuoteSQLiteIdentifier(col)
		if err != nil {
			return nil, fmt.Errorf("invalid observed column name: %w", err)
		}
		colInfo := ColumnInfo{Name: col, DeclaredType: observed.declaredType, Estimated: actuallySampled}

		// 非空字符串率
		if sampleTable != "" {
			// Estimated from sample
			var sampleNonEmpty int
			var sampleTotal int
			if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s IS NOT NULL AND %s != ''", quotedQueryTable, quotedCol, quotedCol)).Scan(&sampleNonEmpty); err != nil {
				return nil, fmt.Errorf("failed to count non-empty values for %q: %w", col, err)
			}
			if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedQueryTable)).Scan(&sampleTotal); err != nil {
				return nil, fmt.Errorf("failed to count sample rows for %q: %w", col, err)
			}
			if sampleTotal > 0 {
				colInfo.NonEmptyRate = float64(sampleNonEmpty) / float64(sampleTotal)
			}
		} else {
			var nonEmptyCount int
			if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s IS NOT NULL AND %s != ''", quotedQueryTable, quotedCol, quotedCol)).Scan(&nonEmptyCount); err != nil {
				return nil, fmt.Errorf("failed to count non-empty values for %q: %w", col, err)
			}
			if rowCount > 0 {
				colInfo.NonEmptyRate = float64(nonEmptyCount) / float64(rowCount)
			}
		}

		// 唯一值数
		if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(DISTINCT %s) FROM %s WHERE %s IS NOT NULL AND %s != ''", quotedCol, quotedQueryTable, quotedCol, quotedCol)).Scan(&colInfo.UniqueCount); err != nil {
			return nil, fmt.Errorf("failed to count distinct values for %q: %w", col, err)
		}

		observedValues, err := collectDistinctColumnValues(db, queryTable, col, schemaDistinctProbeRows)
		if err != nil {
			return nil, fmt.Errorf("failed to observe distinct values for %q: %w", col, err)
		}
		if len(observedValues) > 5 {
			colInfo.SampleValues = append(colInfo.SampleValues, observedValues[:5]...)
		} else {
			colInfo.SampleValues = append(colInfo.SampleValues, observedValues...)
		}
		schema.Columns = append(schema.Columns, colInfo)
	}

	return schema, nil
}

func collectDistinctColumnValues(db *sql.DB, tableName, column string, limit int) ([]string, error) {
	quotedTable, err := QuoteSQLiteIdentifier(tableName)
	if err != nil {
		return nil, err
	}
	quotedColumn, err := QuoteSQLiteIdentifier(column)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf(
		"SELECT DISTINCT CAST(%s AS TEXT) FROM %s WHERE %s IS NOT NULL AND CAST(%s AS TEXT) != '' ORDER BY 1 LIMIT %d",
		quotedColumn, quotedTable, quotedColumn, quotedColumn, limit,
	))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]string, 0, limit)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type QueryResult struct {
	Columns []string
	Rows    []map[string]interface{}
}

func ExecuteQueryDetailedContext(parent context.Context, db *sql.DB, query string, timeout time.Duration) (queryResult *QueryResult, resultErr error) {
	normalizedQuery, err := normalizeReadOnlyQuery(query)
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("query timeout must be greater than zero")
	}

	if parent == nil {
		return nil, fmt.Errorf("query parent context is required")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection: %w", err)
	}
	defer func() {
		resetCtx, resetCancel := context.WithTimeout(context.Background(), time.Second)
		defer resetCancel()
		if _, resetErr := conn.ExecContext(resetCtx, "PRAGMA query_only = OFF"); resetErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to disable read-only query mode: %w", resetErr))
		}
		if closeErr := conn.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to close query connection: %w", closeErr))
		}
	}()

	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return nil, fmt.Errorf("failed to enable read-only query mode: %w", err)
	}

	wrappedQuery := fmt.Sprintf("SELECT * FROM (%s) AS _oda_query LIMIT %d", normalizedQuery, queryProbeRows)
	rows, err := conn.QueryContext(ctx, wrappedQuery)
	if err != nil {
		return nil, fmt.Errorf("SQL execution failed: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to close SQL result rows: %w", closeErr))
		}
	}()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to read SQL result columns: %w", err)
	}
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, exists := seenColumns[column]; exists {
			return nil, fmt.Errorf("SQL result contains duplicate column name %q; use explicit aliases", column)
		}
		seenColumns[column] = struct{}{}
	}
	var result []map[string]interface{}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan SQL result row: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			// 将 []byte 转为 string
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		result = append(result, row)
		if len(result) >= queryProbeRows {
			return nil, fmt.Errorf("query probe exceeds %d row limit", queryProbeRows)
		}
	}

	if err := rows.Err(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("SQL query timeout (>%ds)", int(timeout/time.Second))
		}
		return nil, fmt.Errorf("failed to read SQL results: %w", err)
	}
	return &QueryResult{Columns: append([]string(nil), columns...), Rows: result}, nil
}

func normalizeReadOnlyQuery(query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("SQL cannot be empty")
	}
	if query != strings.TrimSpace(query) {
		return "", fmt.Errorf("SQL must not contain leading or trailing whitespace")
	}

	if hasMultipleStatements(query) {
		return "", fmt.Errorf("only single SQL statement allowed")
	}

	inspection := stripSQLStringsAndComments(query)
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(inspection)))
	if len(fields) == 0 || (fields[0] != "SELECT" && fields[0] != "WITH") {
		return "", fmt.Errorf("only read-only SELECT / WITH queries allowed")
	}

	return query, nil
}

// NormalizeReadOnlyQuery exposes the same parser used by the query executor so
// live query paths can validate a model-selected SELECT.
func NormalizeReadOnlyQuery(query string) (string, error) {
	return normalizeReadOnlyQuery(query)
}

func hasMultipleStatements(query string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); i++ {
		ch := query[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(query) && query[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inSingleQuote {
			if ch == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					i++
					continue
				}
				inSingleQuote = false
			}
			continue
		}
		if inDoubleQuote {
			if ch == '"' {
				if i+1 < len(query) && query[i+1] == '"' {
					i++
					continue
				}
				inDoubleQuote = false
			}
			continue
		}

		if ch == '-' && i+1 < len(query) && query[i+1] == '-' {
			inLineComment = true
			i++
			continue
		}
		if ch == '/' && i+1 < len(query) && query[i+1] == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '\'' {
			inSingleQuote = true
			continue
		}
		if ch == '"' {
			inDoubleQuote = true
			continue
		}
		if ch == ';' {
			return true
		}
	}

	return false
}

func stripSQLStringsAndComments(query string) string {
	var b strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); i++ {
		ch := query[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				b.WriteByte(' ')
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(query) && query[i+1] == '/' {
				inBlockComment = false
				i++
				b.WriteByte(' ')
			}
			continue
		}
		if inSingleQuote {
			if ch == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					i++
					continue
				}
				inSingleQuote = false
				b.WriteByte(' ')
			}
			continue
		}
		if inDoubleQuote {
			if ch == '"' {
				if i+1 < len(query) && query[i+1] == '"' {
					i++
					continue
				}
				inDoubleQuote = false
				b.WriteByte(' ')
			}
			continue
		}

		if ch == '-' && i+1 < len(query) && query[i+1] == '-' {
			inLineComment = true
			i++
			continue
		}
		if ch == '/' && i+1 < len(query) && query[i+1] == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '\'' {
			inSingleQuote = true
			b.WriteByte(' ')
			continue
		}
		if ch == '"' {
			inDoubleQuote = true
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(ch)
	}

	return b.String()
}
