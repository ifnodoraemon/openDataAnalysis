package data

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

// Ingester 数据导入引擎: Excel/CSV → SQLite
type Ingester struct {
	CacheDir string
	db       *sql.DB
	dbPath   string
}

// NewIngester 创建导入引擎
func NewIngester(cacheDir string) *Ingester {
	return &Ingester{CacheDir: cacheDir}
}

// StructureError marks deterministic importer failures caused by the file's
// structure (title rows above headers, ragged rows, empty data, unsupported
// worksheet layout) rather than infrastructure. Files failing with this error
// stay uploaded; structural interpretation moves to the agent, which reads the
// original bytes in the python sandbox, cleans them in code, and imports the
// result via data_import_artifact.
type StructureError struct{ Detail string }

func (e *StructureError) Error() string { return e.Detail }

// GetDB 获取当前数据库连接
func (ing *Ingester) GetDB() *sql.DB {
	return ing.db
}

func (ing *Ingester) DBPath() string {
	return ing.dbPath
}

// InitDB 初始化 SQLite 缓存数据库
func (ing *Ingester) InitDB(sessionID string) error {
	if err := os.MkdirAll(ing.CacheDir, 0o755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}
	dbPath := filepath.Join(ing.CacheDir, sessionID+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to create SQLite database: %w", err)
	}
	if err := configureSQLite(db); err != nil {
		return errors.Join(err, db.Close())
	}
	ing.db = db
	ing.dbPath = dbPath
	return nil
}

func configureSQLite(db *sql.DB) error {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA synchronous=NORMAL`,
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to configure SQLite (%s): %w", pragma, err)
		}
	}
	return nil
}

func (ing *Ingester) ResetDB(sessionID string) error {
	if ing.db != nil {
		if err := ing.db.Close(); err != nil {
			return fmt.Errorf("failed to close analysis database before reset: %w", err)
		}
		ing.db = nil
	}
	dbPath := filepath.Join(ing.CacheDir, sessionID+".db")
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete old database: %w", err)
	}
	return ing.InitDB(sessionID)
}

func (ing *Ingester) Destroy() error {
	if ing.db != nil {
		if err := ing.db.Close(); err != nil {
			return fmt.Errorf("failed to close analysis database before destroy: %w", err)
		}
		ing.db = nil
	}
	if ing.dbPath == "" {
		return nil
	}
	if err := removeSQLiteSidecars(ing.dbPath); err != nil {
		return err
	}
	ing.dbPath = ""
	return nil
}

func DestroySessionDB(cacheRoot, sessionID string) error {
	if strings.TrimSpace(cacheRoot) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return removeSQLiteSidecars(filepath.Join(cacheRoot, sessionID+".db"))
}

func removeSQLiteSidecars(dbPath string) error {
	paths := []string{
		dbPath,
		dbPath + "-wal",
		dbPath + "-shm",
		dbPath + "-journal",
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete SQLite cache file: %w", err)
		}
	}
	return nil
}

func (ing *Ingester) ImportFileRaw(filePath string) (tableName string, rowCount int, colCount int, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	baseName := strings.TrimSuffix(filepath.Base(filePath), ext)
	return ing.ImportFileRawAs(filePath, baseName, "")
}

// ImportFileRawAs imports a file under an exact table name. An empty worksheet
// selection requires the workbook to contain exactly one sheet. The importer
// is strictly deterministic: no header guessing or row skipping — files whose
// structure is not a clean rectangular table fail with an actionable error.
// Structural interpretation is the agent's job: it reads the original file in
// the python sandbox (source_file input) and imports cleaned data via
// data_import_artifact.
func (ing *Ingester) ImportFileRawAs(filePath, requestedTableName, worksheet string) (tableName string, rowCount int, colCount int, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if err := ValidateSQLIdent(requestedTableName); err != nil {
		return "", 0, 0, fmt.Errorf("invalid exact table name: %w", err)
	}
	tableName = requestedTableName
	switch ext {
	case ".csv":
		rowCount, colCount, err = ing.importCSV(filePath, tableName)
	case ".xlsx":
		rowCount, colCount, err = ing.importExcel(filePath, tableName, worksheet)
	default:
		err = fmt.Errorf("unsupported file format: %s", ext)
	}
	return
}

// ExcelSheetNames lists the worksheet names of an Excel workbook without
// importing anything.
func ExcelSheetNames(filePath string) ([]string, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open Excel file: %w", err)
	}
	defer f.Close()
	return f.GetSheetList(), nil
}

func (ing *Ingester) DropTable(tableName string) error {
	if ing.db == nil {
		return fmt.Errorf("analysis database is not initialized")
	}
	if err := ValidateSQLIdent(tableName); err != nil {
		return err
	}
	if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
		return fmt.Errorf("failed to drop table %s: %w", tableName, err)
	}
	return nil
}

func (ing *Ingester) importCSV(filePath, tableName string) (_ int, _ int, resultErr error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)

	headers, err := reader.Read()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read CSV headers: %w", err)
	}

	colCount := len(headers)
	if colCount == 0 {
		return 0, 0, &StructureError{Detail: "CSV file has no headers"}
	}
	columnNames := append([]string(nil), headers...)

	colTypes := make([]ColumnType, colCount)

	if err := ing.createTableTyped(tableName, columnNames, colTypes); err != nil {
		return 0, 0, err
	}

	success := false
	defer func() {
		if !success {
			if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
				resultErr = fmt.Errorf("%w; additionally failed to drop incomplete table %s: %v", resultErr, tableName, err)
			}
		}
	}()

	rowCount := 0
	batchSize := 5000
	batch := make([][]string, 0, batchSize)

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rowCount, colCount, &StructureError{Detail: fmt.Sprintf("failed to read CSV row %d: %v", rowCount+len(batch)+2, err)}
		}
		if len(record) != colCount {
			return rowCount, colCount, &StructureError{Detail: fmt.Sprintf("CSV row %d has %d fields; header has %d", rowCount+len(batch)+2, len(record), colCount)}
		}

		batch = append(batch, record)

		if len(batch) >= batchSize {
			if err := ing.insertBatchTyped(tableName, columnNames, colTypes, batch); err != nil {
				return rowCount, colCount, err
			}
			rowCount += len(batch)
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		if err := ing.insertBatchTyped(tableName, columnNames, colTypes, batch); err != nil {
			return rowCount, colCount, err
		}
		rowCount += len(batch)
	}

	success = true
	return rowCount, colCount, nil
}

// importExcel 导入 Excel 文件（流式处理）
func (ing *Ingester) importExcel(filePath, tableName, worksheet string) (_ int, _ int, resultErr error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open Excel file: %w", err)
	}
	defer f.Close()

	sheetNames := f.GetSheetList()
	sheetName := ""
	worksheet = strings.TrimSpace(worksheet)
	if worksheet != "" {
		for _, candidate := range sheetNames {
			if candidate == worksheet {
				sheetName = candidate
				break
			}
		}
		if sheetName == "" {
			return 0, 0, &StructureError{Detail: fmt.Sprintf("worksheet %q not found; available worksheets: %s", worksheet, strings.Join(sheetNames, ", "))}
		}
	} else if len(sheetNames) == 1 {
		sheetName = sheetNames[0]
	} else {
		return 0, 0, &StructureError{Detail: fmt.Sprintf("Excel workbook has %d worksheets; worksheet selection is required (available: %s)", len(sheetNames), strings.Join(sheetNames, ", "))}
	}
	rows, err := f.Rows(sheetName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read Excel data: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, 0, &StructureError{Detail: "Excel file is empty"}
	}

	// 表头
	headers, err := rows.Columns()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read Excel headers: %w", err)
	}
	colCount := len(headers)
	if colCount == 0 {
		return 0, 0, &StructureError{Detail: "Excel file has no headers"}
	}

	columnNames := append([]string(nil), headers...)

	colTypes := make([]ColumnType, colCount)

	// 创建表
	if err := ing.createTableTyped(tableName, columnNames, colTypes); err != nil {
		return 0, 0, err
	}

	success := false
	defer func() {
		if !success {
			if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
				resultErr = fmt.Errorf("%w; additionally failed to drop incomplete table %s: %v", resultErr, tableName, err)
			}
		}
	}()

	rowCount := 0
	batchSize := 5000
	batch := make([][]string, 0, batchSize)

	for rows.Next() {
		row, err := rows.Columns()
		if err != nil {
			return rowCount, colCount, fmt.Errorf("failed to read Excel row %d: %w", rowCount+len(batch)+2, err)
		}
		if len(row) > colCount {
			return rowCount, colCount, &StructureError{Detail: fmt.Sprintf("Excel row %d has %d cells; header has %d", rowCount+len(batch)+2, len(row), colCount)}
		}
		batch = append(batch, row)

		if len(batch) >= batchSize {
			if err := ing.insertBatchTyped(tableName, columnNames, colTypes, batch); err != nil {
				return rowCount, colCount, err
			}
			rowCount += len(batch)
			batch = batch[:0] // 复用 slice
		}
	}

	// 插入最后不足一个 batch 的数据
	if len(batch) > 0 {
		if err := ing.insertBatchTyped(tableName, columnNames, colTypes, batch); err != nil {
			return rowCount, colCount, err
		}
		rowCount += len(batch)
	}
	if rowCount == 0 {
		return 0, 0, &StructureError{Detail: "Excel data area is empty"}
	}

	success = true
	return rowCount, colCount, nil
}

// ColumnType 列类型
type ColumnType int

const (
	TypeText     ColumnType = iota // 默认文本
	TypeInteger                    // 整数
	TypeReal                       // 浮点数
	TypePreserve                   // Preserve driver-observed SQLite storage classes without affinity coercion.
)

func (t ColumnType) SQLType() string {
	switch t {
	case TypeInteger:
		return "INTEGER"
	case TypeReal:
		return "REAL"
	case TypePreserve:
		return "BLOB"
	default:
		return "TEXT"
	}
}

func (ing *Ingester) CreateTypedTable(tableName string, columns []string, types []ColumnType) error {
	return ing.createTableTyped(tableName, columns, types)
}

func (ing *Ingester) InsertBatchTyped(tableName string, columns []string, types []ColumnType, rows [][]string) error {
	return ing.insertBatchTyped(tableName, columns, types, rows)
}

// InsertBatchValues inserts already-observed values without string conversion.
func (ing *Ingester) InsertBatchValues(tableName string, columns []string, rows [][]interface{}) error {
	return ing.insertBatchValues(tableName, columns, rows)
}

// createTableTyped 创建带类型的 SQLite 表
func (ing *Ingester) createTableTyped(tableName string, columns []string, types []ColumnType) error {
	if err := ValidateSQLIdent(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}
	var colDefs []string
	for i, col := range columns {
		quotedCol, err := QuoteSQLiteIdentifier(col)
		if err != nil {
			return fmt.Errorf("invalid column name at position %d: %w", i+1, err)
		}
		sqlType := "TEXT"
		if i < len(types) {
			sqlType = types[i].SQLType()
		}
		colDefs = append(colDefs, fmt.Sprintf("%s %s", quotedCol, sqlType))
	}

	createSQL := fmt.Sprintf("CREATE TABLE \"%s\" (%s)", tableName, strings.Join(colDefs, ", "))
	_, err := ing.db.Exec(createSQL)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}
	return nil
}

// insertBatchTyped 批量插入（带类型转换）
func (ing *Ingester) insertBatchTyped(tableName string, columns []string, types []ColumnType, rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}
	converted := make([][]interface{}, 0, len(rows))
	for _, row := range rows {
		vals := make([]interface{}, len(columns))
		for i := range columns {
			if i >= len(row) {
				vals[i] = nil
				continue
			}

			raw := row[i]
			columnType := TypeText
			if i < len(types) {
				columnType = types[i]
			}
			switch columnType {
			case TypeInteger:
				trimmed := strings.TrimSpace(raw)
				if trimmed == "" {
					vals[i] = nil
				} else if v, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
					vals[i] = v
				} else {
					vals[i] = raw
				}
			case TypeReal:
				trimmed := strings.TrimSpace(raw)
				if trimmed == "" {
					vals[i] = nil
				} else if v, err := strconv.ParseFloat(trimmed, 64); err == nil {
					vals[i] = v
				} else {
					vals[i] = raw
				}
			default:
				vals[i] = raw
			}
		}
		converted = append(converted, vals)
	}
	return ing.insertBatchValues(tableName, columns, converted)
}

func (ing *Ingester) insertBatchValues(tableName string, columns []string, rows [][]interface{}) error {
	if len(rows) == 0 {
		return nil
	}
	if err := ValidateSQLIdent(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	tx, err := ing.db.Begin()
	if err != nil {
		return err
	}

	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	quotedColumns := make([]string, len(columns))
	for i, col := range columns {
		quotedCol, err := QuoteSQLiteIdentifier(col)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("invalid column name at position %d: %w", i+1, err)
		}
		quotedColumns[i] = quotedCol
	}
	insertSQL := fmt.Sprintf("INSERT INTO \"%s\" (%s) VALUES (%s)",
		tableName,
		strings.Join(quotedColumns, ", "),
		strings.Join(placeholders, ", "))

	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		if len(row) != len(columns) {
			tx.Rollback()
			return fmt.Errorf("row has %d values; table has %d columns", len(row), len(columns))
		}
		if _, err := stmt.Exec(row...); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

var invalidSQLIdent = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// ValidateSQLIdent ensures a name only contains safe characters for SQL identifiers.
// This is a defense-in-depth check before interpolating names into DDL/DML statements.
func ValidateSQLIdent(name string) error {
	if name == "" {
		return fmt.Errorf("empty SQL identifier")
	}
	if len(name) > 128 {
		return fmt.Errorf("SQL identifier too long: %s", name[:20])
	}
	if invalidSQLIdent.MatchString(name) {
		return fmt.Errorf("invalid characters in SQL identifier: %s", name)
	}
	return nil
}

// QuoteSQLiteIdentifier quotes an observed SQLite identifier without rewriting it.
func QuoteSQLiteIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty SQLite identifier")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("SQLite identifier contains NUL byte")
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}
