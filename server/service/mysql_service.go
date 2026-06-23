package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type MySQLSourceConfig struct {
	Driver        string           `json:"driver"`
	Host          string           `json:"host"`
	Port          int              `json:"port"`
	DatabaseName  string           `json:"database_name"`
	DefaultSchema string           `json:"default_schema"`
	Username      string           `json:"username"`
	Allowlist     []AllowlistEntry `json:"allowlist"`
}

type MySQLCredential struct {
	Password string `json:"password"`
}

func ParseMySQLSourceConfig(sourceConfig *domain.SourceConfig) (MySQLSourceConfig, error) {
	if sourceConfig == nil {
		return MySQLSourceConfig{}, fmt.Errorf("source config does not exist")
	}
	var cfg MySQLSourceConfig
	if err := json.Unmarshal([]byte(sourceConfig.ConfigJSON), &cfg); err != nil {
		return MySQLSourceConfig{}, fmt.Errorf("failed to parse mysql source config: %w", err)
	}
	if cfg.Driver == "" {
		cfg.Driver = "mysql"
	}
	if cfg.DefaultSchema == "" {
		cfg.DefaultSchema = cfg.DatabaseName
	}
	return cfg, nil
}

func OpenMySQLConnection(ctx context.Context, sourceConfig *domain.SourceConfig, authSecret string) (*sql.DB, error) {
	cfg, err := ParseMySQLSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	var credential MySQLCredential
	if err := DecryptCredential(sourceConfig.CredentialCiphertext, authSecret, &credential); err != nil {
		return nil, fmt.Errorf("credential decryption failed: %w", err)
	}
	if strings.TrimSpace(credential.Password) == "" {
		return nil, fmt.Errorf("mysql credential password is empty")
	}

	mysqlCfg := mysql.NewConfig()
	mysqlCfg.User = cfg.Username
	mysqlCfg.Passwd = credential.Password
	mysqlCfg.Net = "tcp"
	mysqlCfg.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	mysqlCfg.DBName = cfg.DatabaseName
	mysqlCfg.ParseTime = true
	mysqlCfg.Timeout = 10 * time.Second
	mysqlCfg.ReadTimeout = 5 * time.Minute
	mysqlCfg.WriteTimeout = 30 * time.Second
	mysqlCfg.Params = map[string]string{
		"charset":   "utf8mb4",
		"collation": "utf8mb4_unicode_ci",
	}

	db, err := sql.Open("mysql", mysqlCfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to create MySQL connection: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connection test failed: %w", err)
	}
	return db, nil
}

func (s *SourceService) TestMySQLConnection(ctx context.Context, sourceID, authSecret string) map[string]interface{} {
	sourceConfig, err := s.findSourceConfig(ctx, sourceID)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}

	mysqlDB, err := OpenMySQLConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}
	defer mysqlDB.Close()

	cfg, err := ParseMySQLSourceConfig(sourceConfig)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}
	var validated []map[string]interface{}
	for _, entry := range cfg.Allowlist {
		exists := s.checkMySQLObjectExists(ctx, mysqlDB, entry)
		validated = append(validated, map[string]interface{}{
			"schema": entry.Schema, "name": entry.Name, "kind": entry.Kind, "exists": exists,
		})
	}

	return map[string]interface{}{
		"success":   true,
		"message":   "connection successful",
		"allowlist": validated,
	}
}

func (s *SourceService) streamMySQLImportToSQLite(ctx context.Context, mysqlDB *sql.DB, schema, object string, ingester *data.Ingester, tableName string, importRowLimit int) (int, int, int, bool, error) {
	if err := data.ValidateSQLIdent(tableName); err != nil {
		return 0, 0, 0, false, fmt.Errorf("invalid SQLite table name after sanitization: %w", err)
	}
	quotedSchema, err := quoteMySQLIdentifier(schema)
	if err != nil {
		return 0, 0, 0, false, err
	}
	quotedObject, err := quoteMySQLIdentifier(object)
	if err != nil {
		return 0, 0, 0, false, err
	}
	query := fmt.Sprintf("SELECT * FROM %s.%s", quotedSchema, quotedObject)
	if importRowLimit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, importRowLimit+1)
	}
	rows, err := mysqlDB.QueryContext(ctx, query)
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("failed to query upstream data: %w", err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("failed to read column types: %w", err)
	}
	colCount := len(colTypes)
	colNames := make([]string, colCount)
	seenCols := make(map[string]int)
	for i, ct := range colTypes {
		base := sanitizePGColumnName(ct.Name())
		finalName := base
		if count := seenCols[base]; count > 0 {
			finalName = fmt.Sprintf("%s_%d", base, count)
		}
		seenCols[base]++
		colNames[i] = finalName
	}

	sqliteColTypes := make([]data.ColumnType, colCount)
	for i, ct := range colTypes {
		dbType := strings.ToUpper(ct.DatabaseTypeName())
		switch {
		case strings.Contains(dbType, "INT"), strings.Contains(dbType, "BIT"), strings.Contains(dbType, "BOOL"):
			sqliteColTypes[i] = data.TypeInteger
		case strings.Contains(dbType, "FLOAT"), strings.Contains(dbType, "DOUBLE"), strings.Contains(dbType, "REAL"), strings.Contains(dbType, "NUMERIC"), strings.Contains(dbType, "DECIMAL"):
			sqliteColTypes[i] = data.TypeReal
		default:
			sqliteColTypes[i] = data.TypeText
		}
	}

	if err := ingester.CreateTypedTable(tableName, colNames, sqliteColTypes); err != nil {
		return 0, 0, 0, false, fmt.Errorf("failed to create SQLite table: %w", err)
	}

	batchSize := 5000
	batch := make([][]string, 0, batchSize)
	rowCount := 0
	rowsSkipped := 0
	importTruncated := false
	vals := make([]interface{}, colCount)
	valPtrs := make([]interface{}, colCount)
	for i := range vals {
		valPtrs[i] = &vals[i]
	}

	for rows.Next() {
		if importRowLimit > 0 && rowCount+len(batch) >= importRowLimit {
			importTruncated = true
			break
		}
		if err := rows.Scan(valPtrs...); err != nil {
			rowsSkipped++
			continue
		}
		row := make([]string, colCount)
		for i := range vals {
			row[i] = formatSQLValue(vals[i])
		}
		batch = append(batch, row)
		if len(batch) >= batchSize {
			if err := ingester.InsertBatchTyped(tableName, colNames, sqliteColTypes, batch); err != nil {
				return rowCount, colCount, rowsSkipped, importTruncated, err
			}
			rowCount += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := ingester.InsertBatchTyped(tableName, colNames, sqliteColTypes, batch); err != nil {
			return rowCount, colCount, rowsSkipped, importTruncated, err
		}
		rowCount += len(batch)
	}
	if err := rows.Err(); err != nil {
		return rowCount, colCount, rowsSkipped, importTruncated, fmt.Errorf("failed while reading upstream data: %w", err)
	}
	if rowCount == 0 && rowsSkipped > 0 {
		return rowCount, colCount, rowsSkipped, importTruncated, fmt.Errorf("all %d upstream rows failed to import", rowsSkipped)
	}
	return rowCount, colCount, rowsSkipped, importTruncated, nil
}

func quoteMySQLIdentifier(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return "", fmt.Errorf("empty MySQL identifier")
	}
	if strings.ContainsRune(clean, 0) {
		return "", fmt.Errorf("MySQL identifier contains NUL byte")
	}
	return "`" + strings.ReplaceAll(clean, "`", "``") + "`", nil
}

func sourceScopedMySQLTableName(schemaName, objectName, sourceID string) string {
	rawName := strings.TrimSpace(objectName)
	if strings.TrimSpace(schemaName) != "" {
		rawName = strings.TrimSpace(schemaName) + "_" + rawName
	}
	base := sanitizePGTableName(rawName)
	suffix := sourceTableSuffix(sourceID)
	if suffix == "" {
		return base
	}
	return sanitizePGTableName(base + "__" + suffix)
}

func (s *SourceService) checkMySQLObjectExists(ctx context.Context, db *sql.DB, entry AllowlistEntry) bool {
	kind := strings.ToUpper(strings.TrimSpace(entry.Kind))
	if kind == "" {
		kind = "TABLE"
	}
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = ? AND table_name = ? AND table_type = ?`,
		entry.Schema, entry.Name, mapMySQLTableType(kind),
	).Scan(&count)
	return err == nil && count > 0
}

func mapMySQLTableType(kind string) string {
	if kind == "VIEW" {
		return "VIEW"
	}
	return "BASE TABLE"
}

func formatSQLValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch tv := v.(type) {
	case []byte:
		return string(tv)
	case time.Time:
		return tv.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprint(tv)
	}
}
