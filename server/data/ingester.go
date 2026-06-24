package data

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

// Ingester 数据导入引擎: Excel/CSV → SQLite
type Ingester struct {
	CacheDir         string
	db               *sql.DB
	dbPath           string
	SemanticEnricher LLMChatFunc // 可选：导入时自动触发 LLM 语义分析
}

// NewIngester 创建导入引擎
func NewIngester(cacheDir string) *Ingester {
	return &Ingester{CacheDir: cacheDir}
}

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
		_ = db.Close()
		return err
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
			log.Printf("Warning: closing DB in ResetDB: %v", err)
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
			log.Printf("Warning: closing DB in Destroy: %v", err)
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
	return ing.ImportFileRawAs(filePath, baseName)
}

func (ing *Ingester) ImportFileRawAs(filePath, requestedTableName string) (tableName string, rowCount int, colCount int, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	tableName = sanitizeTableName(requestedTableName)
	switch ext {
	case ".csv":
		rowCount, colCount, err = ing.importCSV(filePath, tableName)
	case ".xlsx", ".xls":
		rowCount, colCount, err = ing.importExcel(filePath, tableName)
	default:
		err = fmt.Errorf("unsupported file format: %s", ext)
	}
	return
}

// ImportFile 导入文件到 SQLite（包含自动 schema/semantic 后处理）
func (ing *Ingester) ImportFile(filePath string) (tableName string, rowCount int, colCount int, err error) {
	tableName, rowCount, colCount, err = ing.ImportFileRaw(filePath)
	if err != nil {
		return
	}
	_ = ing.GenerateSchemaMetadata(tableName)
	ing.tryEnrichAfterImport(tableName)
	return
}

func (ing *Ingester) DropTable(tableName string) error {
	if ing.db == nil {
		return nil
	}
	if err := ValidateSQLIdent(tableName); err != nil {
		return err
	}
	if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
		return fmt.Errorf("failed to drop table %s: %w", tableName, err)
	}
	if _, err := ing.db.Exec(`DELETE FROM _oda_table_metadata WHERE table_name = ?`, tableName); err != nil {
		return fmt.Errorf("failed to delete table metadata %s: %w", tableName, err)
	}
	return nil
}

func (ing *Ingester) importCSV(filePath, tableName string) (int, int, error) {
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
	sanitizedHeaders := make([]string, colCount)
	seenCols := make(map[string]int)
	for i, h := range headers {
		base := sanitizeColumnName(h)
		finalName := base
		if count := seenCols[base]; count > 0 {
			finalName = fmt.Sprintf("%s_%d", base, count)
		}
		seenCols[base]++
		sanitizedHeaders[i] = finalName
	}

	sampleSize := 500
	var sampleRows [][]string
	for i := 0; i < sampleSize; i++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		sampleRows = append(sampleRows, record)
	}

	colTypes := inferColumnTypes(sampleRows, colCount)

	if err := ing.createTableTyped(tableName, sanitizedHeaders, colTypes); err != nil {
		return 0, 0, err
	}

	success := false
	defer func() {
		if !success {
			if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
				log.Printf("Warning: failed to drop table %s: %v", tableName, err)
			}
		}
	}()

	rowCount := 0
	if len(sampleRows) > 0 {
		if err := ing.insertBatchTyped(tableName, sanitizedHeaders, colTypes, sampleRows); err != nil {
			return rowCount, colCount, err
		}
		rowCount += len(sampleRows)
	}

	batchSize := 5000
	batch := make([][]string, 0, batchSize)

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		batch = append(batch, record)

		if len(batch) >= batchSize {
			if err := ing.insertBatchTyped(tableName, sanitizedHeaders, colTypes, batch); err != nil {
				return rowCount, colCount, err
			}
			rowCount += len(batch)
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		if err := ing.insertBatchTyped(tableName, sanitizedHeaders, colTypes, batch); err != nil {
			return rowCount, colCount, err
		}
		rowCount += len(batch)
	}

	success = true
	return rowCount, colCount, nil
}

// importExcel 导入 Excel 文件（流式处理，带行数硬上限）
func (ing *Ingester) importExcel(filePath, tableName string) (int, int, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open Excel file: %w", err)
	}
	defer f.Close()

	// 默认使用第一个 sheet
	sheetName := f.GetSheetName(0)
	rows, err := f.Rows(sheetName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read Excel data: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, 0, fmt.Errorf("Excel file is empty")
	}

	// 表头
	headers, err := rows.Columns()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read Excel headers: %w", err)
	}
	colCount := len(headers)
	if colCount == 0 {
		return 0, 0, fmt.Errorf("Excel file has no headers")
	}

	sanitizedHeaders := make([]string, colCount)
	seenColsExcel := make(map[string]int)
	for i, h := range headers {
		base := sanitizeColumnName(h)
		finalName := base
		if count := seenColsExcel[base]; count > 0 {
			finalName = fmt.Sprintf("%s_%d", base, count)
		}
		seenColsExcel[base]++
		sanitizedHeaders[i] = finalName
	}

	// 明确产品上限：最多支持 10 万行
	const maxExcelRows = 100000

	// 读取部分数据进行类型推断（最多扫描 500 行）
	sampleSize := 500
	var sampleRows [][]string
	rowsRead := 0

	for i := 0; i < sampleSize; i++ {
		if !rows.Next() {
			break
		}
		row, err := rows.Columns()
		if err != nil {
			continue
		}
		// 补齐或截断列数
		padded := make([]string, colCount)
		copy(padded, row)
		sampleRows = append(sampleRows, padded)
		rowsRead++
	}

	if rowsRead == 0 {
		return 0, 0, fmt.Errorf("Excel data area is empty")
	}

	// 类型推断
	colTypes := inferColumnTypes(sampleRows, colCount)

	// 创建表
	if err := ing.createTableTyped(tableName, sanitizedHeaders, colTypes); err != nil {
		return 0, 0, err
	}

	success := false
	defer func() {
		if !success {
			if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
				log.Printf("Warning: failed to drop table %s: %v", tableName, err)
			}
		}
	}()

	// 插入已经读出的 sample 行
	rowCount := 0
	if len(sampleRows) > 0 {
		if err := ing.insertBatchTyped(tableName, sanitizedHeaders, colTypes, sampleRows); err != nil {
			return rowCount, colCount, err
		}
		rowCount += len(sampleRows)
	}

	// 流式读取剩余数据并批量插入
	batchSize := 5000
	batch := make([][]string, 0, batchSize)

	for rows.Next() {
		if rowCount+len(batch) >= maxExcelRows {
			return 0, 0, fmt.Errorf("Excel row count exceeds %d limit; for large datasets, convert to CSV before uploading", maxExcelRows)
		}

		row, err := rows.Columns()
		if err != nil {
			continue // 跳过错误行
		}

		// 补齐或截断列数
		padded := make([]string, colCount)
		copy(padded, row)
		batch = append(batch, padded)

		if len(batch) >= batchSize {
			if err := ing.insertBatchTyped(tableName, sanitizedHeaders, colTypes, batch); err != nil {
				return rowCount, colCount, err
			}
			rowCount += len(batch)
			batch = batch[:0] // 复用 slice
		}
	}

	// 插入最后不足一个 batch 的数据
	if len(batch) > 0 {
		if err := ing.insertBatchTyped(tableName, sanitizedHeaders, colTypes, batch); err != nil {
			return rowCount, colCount, err
		}
		rowCount += len(batch)
	}

	success = true
	return rowCount, colCount, nil
}

// ColumnType 列类型
type ColumnType int

const (
	TypeText    ColumnType = iota // 默认文本
	TypeInteger                   // 整数
	TypeReal                      // 浮点数
)

func (t ColumnType) SQLType() string {
	switch t {
	case TypeInteger:
		return "INTEGER"
	case TypeReal:
		return "REAL"
	default:
		return "TEXT"
	}
}

// inferColumnTypes 扫描前 N 行推断列类型
func inferColumnTypes(rows [][]string, colCount int) []ColumnType {
	types := make([]ColumnType, colCount)

	for i := range types {
		types[i] = TypeInteger
	}
	seenNonEmpty := make([]bool, colCount)

	sampleSize := 100
	if len(rows) < sampleSize {
		sampleSize = len(rows)
	}

	for _, row := range rows[:sampleSize] {
		for i := 0; i < colCount && i < len(row); i++ {
			val := strings.TrimSpace(row[i])

			// 空值不影响判断
			if val == "" || val == "-" || val == "N/A" || val == "null" || val == "NULL" {
				continue
			}
			seenNonEmpty[i] = true

			switch types[i] {
			case TypeInteger:
				// 先尝试整数
				if _, err := strconv.ParseInt(val, 10, 64); err != nil {
					// 再尝试浮点
					if _, err := strconv.ParseFloat(val, 64); err != nil {
						types[i] = TypeText // 不是数字 → TEXT
					} else {
						types[i] = TypeReal // 是浮点 → REAL
					}
				}
			case TypeReal:
				// 已确定为浮点，检查是否还是数字
				if _, err := strconv.ParseFloat(val, 64); err != nil {
					types[i] = TypeText // 不是数字 → TEXT
				}
			case TypeText:
				// 已确定为文本，不再变化
			}
		}
	}
	for i, seen := range seenNonEmpty {
		if !seen {
			types[i] = TypeText
		}
	}

	return types
}

func (ing *Ingester) CreateTypedTable(tableName string, columns []string, types []ColumnType) error {
	return ing.createTableTyped(tableName, columns, types)
}

func (ing *Ingester) InsertBatchTyped(tableName string, columns []string, types []ColumnType, rows [][]string) error {
	return ing.insertBatchTyped(tableName, columns, types, rows)
}

// createTableTyped 创建带类型的 SQLite 表
func (ing *Ingester) createTableTyped(tableName string, columns []string, types []ColumnType) error {
	if err := ValidateSQLIdent(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}
	for _, col := range columns {
		if err := ValidateSQLIdent(col); err != nil {
			return fmt.Errorf("invalid column name: %w", err)
		}
	}
	if _, err := ing.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)); err != nil {
		log.Printf("Warning: failed to drop table %s: %v", tableName, err)
	}

	var colDefs []string
	for i, col := range columns {
		sqlType := "TEXT"
		if i < len(types) {
			sqlType = types[i].SQLType()
		}
		colDefs = append(colDefs, fmt.Sprintf("\"%s\" %s", col, sqlType))
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
	if err := ValidateSQLIdent(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}
	for _, col := range columns {
		if err := ValidateSQLIdent(col); err != nil {
			return fmt.Errorf("invalid column name: %w", err)
		}
	}

	tx, err := ing.db.Begin()
	if err != nil {
		return err
	}

	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	insertSQL := fmt.Sprintf("INSERT INTO \"%s\" (%s) VALUES (%s)",
		tableName,
		"\""+strings.Join(columns, "\", \"")+"\"",
		strings.Join(placeholders, ", "))

	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		vals := make([]interface{}, len(columns))
		for i := range columns {
			if i >= len(row) || strings.TrimSpace(row[i]) == "" {
				vals[i] = nil // 空值 → NULL
				continue
			}

			val := strings.TrimSpace(row[i])

			if i < len(types) {
				switch types[i] {
				case TypeInteger:
					if v, err := strconv.ParseInt(val, 10, 64); err == nil {
						vals[i] = v
					} else {
						vals[i] = val // 保留原始文本
					}
				case TypeReal:
					if v, err := strconv.ParseFloat(val, 64); err == nil {
						vals[i] = v
					} else {
						vals[i] = val
					}
				default:
					vals[i] = val
				}
			} else {
				vals[i] = val
			}
		}
		if _, err := stmt.Exec(vals...); err != nil {
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

// sanitizeTableName 清理表名
func sanitizeTableName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "table"
	}
	name = invalidSQLIdent.ReplaceAllString(name, "_")
	name = strings.ToLower(name)
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		name = "t_" + name
	}
	return name
}

// sanitizeColumnName 清理列名
func sanitizeColumnName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "unnamed"
	}
	name = invalidSQLIdent.ReplaceAllString(name, "_")
	name = strings.ToLower(name)
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		name = "c_" + name
	}
	return name
}

// ensureMetadataTable 确保 _oda_table_metadata 表存在，使用新的分层结构
func (ing *Ingester) ensureMetadataTable() {
	if ing.db == nil {
		return
	}
	_, _ = ing.db.Exec(`
		CREATE TABLE IF NOT EXISTS _oda_table_metadata (
			table_name TEXT PRIMARY KEY,
			schema_json TEXT,
			semantic_json TEXT,
			relations_json TEXT,
			schema_ready BOOLEAN DEFAULT 0,
			semantic_ready BOOLEAN DEFAULT 0,
			relations_verified BOOLEAN DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	// 补齐 metadata 表缺失的新列。
	for _, col := range []struct{ name, def string }{
		{"semantic_json", "TEXT"},
		{"relations_json", "TEXT"},
		{"schema_ready", "BOOLEAN DEFAULT 0"},
		{"semantic_ready", "BOOLEAN DEFAULT 0"},
		{"relations_verified", "BOOLEAN DEFAULT 0"},
	} {
		_, _ = ing.db.Exec(fmt.Sprintf("ALTER TABLE _oda_table_metadata ADD COLUMN %s %s", col.name, col.def))
	}
}

// GenerateSchemaMetadata 针对表生成确定性的 schema metadata 和索引。
// 此函数不依赖 LLM，始终可以成功完成。
func (ing *Ingester) GenerateSchemaMetadata(tableName string) error {
	if ing.db == nil {
		return fmt.Errorf("database not initialized")
	}
	if err := ValidateSQLIdent(tableName); err != nil {
		return fmt.Errorf("invalid table name for schema generation: %w", err)
	}

	// 1. 生成系统级统计 (用于 SQL Query Planner)
	_, err := ing.db.Exec(fmt.Sprintf("ANALYZE \"%s\"", tableName))
	if err != nil {
		log.Printf("[Warning] Failed to analyze table %s: %v", tableName, err)
	}

	// 2. 提取确定性 schema (use sampled for large tables)
	var rowCount int
	ing.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", tableName)).Scan(&rowCount)
	var schema *SchemaInfo
	if rowCount > 10000 {
		schema, err = ExtractSchemaSampled(ing.db, tableName)
	} else {
		schema, err = ExtractSchema(ing.db, tableName)
	}
	if err != nil {
		return fmt.Errorf("failed to extract schema: %w", err)
	}

	// 3. 持久化确定性 schema 到 metadata 表，同时清除已失效的 semantic 状态。
	//    schema 变更意味着已有 LLM 分析不再可信。
	ing.ensureMetadataTable()
	schemaBytes, _ := json.Marshal(schema)
	_, err = ing.db.Exec(
		`INSERT INTO _oda_table_metadata (table_name, schema_json, schema_ready, semantic_json, relations_json, semantic_ready, relations_verified, updated_at) 
		 VALUES (?, ?, 1, NULL, NULL, 0, 0, CURRENT_TIMESTAMP)
		 ON CONFLICT(table_name) DO UPDATE SET 
		   schema_json=excluded.schema_json, 
		   schema_ready=1, 
		   semantic_json=NULL, 
		   relations_json=NULL, 
		   semantic_ready=0, 
		   relations_verified=0, 
		   updated_at=excluded.updated_at`,
		tableName, string(schemaBytes),
	)
	if err != nil {
		log.Printf("[Warning] Failed to save schema metadata for %s: %v", tableName, err)
	}

	// 4. 构建索引 — 覆盖候选时间列、id 列、候选 join key 列
	for _, col := range schema.Columns {
		isTimeCol := col.TimeProfile != nil
		isID := strings.HasSuffix(strings.ToLower(col.Name), "_id") || strings.ToLower(col.Name) == "id"
		isJoinKey := isID
		nameLower := strings.ToLower(col.Name)
		if strings.HasSuffix(nameLower, "_key") || strings.HasSuffix(nameLower, "_fk") || strings.Contains(nameLower, "join") {
			isJoinKey = true
		}

		if isTimeCol || isID || isJoinKey {
			idxName := fmt.Sprintf("idx_%s_%s", sanitizeTableName(tableName), sanitizeColumnName(col.Name))
			createIdxSQL := fmt.Sprintf("CREATE INDEX IF NOT EXISTS \"%s\" ON \"%s\" (\"%s\")", idxName, tableName, col.Name)
			_, err := ing.db.Exec(createIdxSQL)
			if err != nil {
				log.Printf("[Warning] Failed to create index %s on table %s: %v", idxName, tableName, err)
			}
		}
	}

	return nil
}

// tryEnrichAfterImport 导入后异步尝试 LLM 语义分析。
// 在后台 goroutine 中执行，不阻塞导入流程。
// 如果 SemanticEnricher 未配置或分析失败，只记录 warning。
func (ing *Ingester) tryEnrichAfterImport(tableName string) {
	if ing.SemanticEnricher == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ing.EnrichSemanticProfile(ctx, tableName, ing.SemanticEnricher); err != nil {
			log.Printf("[Warning] post-import semantic analysis failed (table=%s): %v", tableName, err)
		}
	}()
}

// EnrichSemanticProfile 利用 LLM 对表做语义预分析，是可选的增量增强步骤。
// chatFn 由调用方提供，通常包装 agent.LLMClient。
// 此函数失败不影响已有的确定性 schema metadata。
func (ing *Ingester) EnrichSemanticProfile(ctx context.Context, tableName string, chatFn LLMChatFunc) error {
	if ing.db == nil {
		return fmt.Errorf("database not initialized")
	}

	// 1. 读取已有的确定性 schema_json 作为乐观锁版本
	var schemaStr string
	err := ing.db.QueryRow(`SELECT schema_json FROM _oda_table_metadata WHERE table_name = ?`, tableName).Scan(&schemaStr)
	if err != nil {
		return fmt.Errorf("failed to read schema: %w", err)
	}
	var schema *SchemaInfo
	if err := json.Unmarshal([]byte(schemaStr), &schema); err != nil {
		return fmt.Errorf("failed to parse schema: %w", err)
	}

	// 2. 获取环境中的其他表名供 Join 探测
	ing.ensureMetadataTable()
	var activeTables []string
	tableRows, _ := ing.db.Query(`SELECT table_name FROM _oda_table_metadata WHERE table_name != ?`, tableName)
	if tableRows != nil {
		for tableRows.Next() {
			var tName string
			if tableRows.Scan(&tName) == nil {
				activeTables = append(activeTables, tName)
			}
		}
		tableRows.Close()
	}

	// 3. 调用 LLM 分析
	profile, err := AnalyzeTableSemantics(ctx, chatFn, schema, activeTables)
	if err != nil {
		return fmt.Errorf("LLM semantic analysis failed: %w", err)
	}

	log.Printf("[Info] AI semantic analysis completed, extracted domain aliases and relation predictions for table: %s", tableName)

	// 4. 校验 relation hints
	// 尝试获取目标表的 schema 用于验证
	targetSchemas := make(map[string]*SchemaInfo, len(activeTables))
	for _, at := range activeTables {
		if ts, err := ExtractSchema(ing.db, at); err == nil {
			targetSchemas[strings.ToLower(at)] = ts
		}
	}
	verifiedRelations := ValidateRelationHints(profile.Relations, schema, activeTables, targetSchemas)

	// 5. 用校验过的 relations 替换 profile 中的原始 relations，再序列化
	//    确保 semantic_json 中不泄漏未校验的 relation hints
	profile.Relations = verifiedRelations
	semanticBytes, _ := json.Marshal(profile)
	relationsBytes, _ := json.Marshal(verifiedRelations)

	// 使用乐观锁防止覆盖：只在 schema_json 没变时更新
	result, err := ing.db.Exec(
		`UPDATE _oda_table_metadata SET 
		   semantic_json = ?, semantic_ready = 1,
		   relations_json = ?, relations_verified = ?,
		   updated_at = CURRENT_TIMESTAMP
		 WHERE table_name = ? AND schema_json = ?`,
		string(semanticBytes),
		string(relationsBytes),
		len(verifiedRelations) > 0,
		tableName,
		schemaStr,
	)
	if err != nil {
		return fmt.Errorf("failed to save semantic analysis result: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("semantic analysis discarded: schema changed during LLM call (possibly re-imported)")
	}

	return nil
}

// GetActiveTables 获取当前所有已导入的分析表名。
// 合并 _oda_table_metadata 和 sqlite_master 的结果。
func (ing *Ingester) GetActiveTables() []string {
	if ing.db == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var tables []string

	rows, err := ing.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_oda_%'`)
	if err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				if _, ok := seen[name]; !ok {
					seen[name] = struct{}{}
					tables = append(tables, name)
				}
			}
		}
		rows.Close()
	}

	ing.ensureMetadataTable()
	metaRows, err := ing.db.Query(`SELECT table_name FROM _oda_table_metadata`)
	if err == nil {
		for metaRows.Next() {
			var name string
			if metaRows.Scan(&name) == nil {
				if _, ok := seen[name]; !ok {
					seen[name] = struct{}{}
					tables = append(tables, name)
				}
			}
		}
		metaRows.Close()
	}
	return tables
}

// TableMetadata 表的完整 metadata 视图
type TableMetadata struct {
	SchemaJSON        string `json:"schema_json,omitempty"`
	SemanticJSON      string `json:"semantic_json,omitempty"`
	RelationsJSON     string `json:"relations_json,omitempty"`
	SchemaReady       bool   `json:"schema_ready"`
	SemanticReady     bool   `json:"semantic_ready"`
	RelationsVerified bool   `json:"relations_verified"`
}

// GetTableMetadata 从内置表读取已持久化的 metadata
func (ing *Ingester) GetTableMetadata(tableName string) (string, error) {
	if ing.db == nil {
		return "", fmt.Errorf("database not initialized")
	}
	ing.ensureMetadataTable()

	var meta TableMetadata
	var schemaJSON, semanticJSON, relationsJSON sql.NullString
	err := ing.db.QueryRow(
		`SELECT schema_json, semantic_json, relations_json, schema_ready, semantic_ready, relations_verified 
		 FROM _oda_table_metadata WHERE table_name = ?`, tableName,
	).Scan(&schemaJSON, &semanticJSON, &relationsJSON, &meta.SchemaReady, &meta.SemanticReady, &meta.RelationsVerified)
	if err != nil {
		return "", err
	}

	// 始终返回 schema_json 作为基础（包含 row_count、column types 等确定性字段）
	// 如果也有 semantic_json，合并到结果中而不是替代
	if schemaJSON.Valid && strings.TrimSpace(schemaJSON.String) != "" {
		if semanticJSON.Valid && strings.TrimSpace(semanticJSON.String) != "" {
			// 深合并：以 schema 为基础，把 semantic 字段追加进去
			var schemaMap, semanticMap map[string]interface{}
			if json.Unmarshal([]byte(schemaJSON.String), &schemaMap) == nil &&
				json.Unmarshal([]byte(semanticJSON.String), &semanticMap) == nil {

				// 1. 合并 columns 数组内的新字段
				if semColsRaw, ok := semanticMap["columns"].([]interface{}); ok {
					if schColsRaw, ok := schemaMap["columns"].([]interface{}); ok {
						// 提取 semantic 列字典
						semColMap := make(map[string]map[string]interface{})
						for _, colRaw := range semColsRaw {
							if colMap, ok := colRaw.(map[string]interface{}); ok {
								if name, ok := colMap["name"].(string); ok {
									semColMap[name] = colMap
								}
							}
						}
						// 遍历 schema 列注入
						for _, colRaw := range schColsRaw {
							if colMap, ok := colRaw.(map[string]interface{}); ok {
								if name, ok := colMap["name"].(string); ok {
									if semCol, exists := semColMap[name]; exists {
										for k, v := range semCol {
											// 如果 schema 原本没有这个字段，就填充语义信息
											if _, ok := colMap[k]; !ok {
												colMap[k] = v
											}
										}
									}
								}
							}
						}
					}
				}

				// 2. 合并顶层其他字段
				for k, v := range semanticMap {
					if k == "columns" {
						continue // columns 已处理
					}
					if _, exists := schemaMap[k]; !exists {
						schemaMap[k] = v
					}
				}
				merged, err := json.Marshal(schemaMap)
				if err == nil {
					return string(merged), nil
				}
			}
		}
		return schemaJSON.String, nil
	}
	if semanticJSON.Valid {
		return semanticJSON.String, nil
	}
	return "", nil
}

// GetFullTableMetadata 返回完整的表 metadata 结构
func (ing *Ingester) GetFullTableMetadata(tableName string) (*TableMetadata, error) {
	if ing.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	ing.ensureMetadataTable()

	var meta TableMetadata
	var schemaJSON, semanticJSON, relationsJSON sql.NullString
	err := ing.db.QueryRow(
		`SELECT schema_json, semantic_json, relations_json, schema_ready, semantic_ready, relations_verified 
		 FROM _oda_table_metadata WHERE table_name = ?`, tableName,
	).Scan(&schemaJSON, &semanticJSON, &relationsJSON, &meta.SchemaReady, &meta.SemanticReady, &meta.RelationsVerified)
	if err != nil {
		return nil, err
	}
	if schemaJSON.Valid {
		meta.SchemaJSON = schemaJSON.String
	}
	if semanticJSON.Valid {
		meta.SemanticJSON = semanticJSON.String
	}
	if relationsJSON.Valid {
		meta.RelationsJSON = relationsJSON.String
	}
	return &meta, nil
}

// GetMetadataReadiness 返回表 metadata 的各层就绪状态
func (ing *Ingester) GetMetadataReadiness(tableName string) (schemaReady, semanticReady, relationsVerified bool) {
	if ing.db == nil {
		return false, false, false
	}
	ing.ensureMetadataTable()
	_ = ing.db.QueryRow(
		`SELECT schema_ready, semantic_ready, relations_verified FROM _oda_table_metadata WHERE table_name = ?`,
		tableName,
	).Scan(&schemaReady, &semanticReady, &relationsVerified)
	return
}
