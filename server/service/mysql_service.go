package service

import (
	"context"
	"database/sql"
	"errors"
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
	Host         string           `json:"host"`
	Port         int              `json:"port"`
	DatabaseName string           `json:"database_name"`
	TLSMode      string           `json:"tls_mode"`
	Username     string           `json:"username"`
	Allowlist    []AllowlistEntry `json:"allowlist"`
}

type MySQLCredential struct {
	Password string `json:"password"`
}

func ParseMySQLSourceConfig(sourceConfig *domain.SourceConfig) (MySQLSourceConfig, error) {
	if sourceConfig == nil {
		return MySQLSourceConfig{}, fmt.Errorf("source config does not exist")
	}
	var cfg MySQLSourceConfig
	if err := decodeStrictJSON([]byte(sourceConfig.ConfigJSON), &cfg); err != nil {
		return MySQLSourceConfig{}, fmt.Errorf("failed to parse mysql source config: %w", err)
	}
	if err := validateMySQLConfig(cfg); err != nil {
		return MySQLSourceConfig{}, err
	}
	allowlist, err := ValidateAllowlist(cfg.Allowlist)
	if err != nil {
		return MySQLSourceConfig{}, err
	}
	cfg.Allowlist = allowlist
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
	mysqlCfg.TLSConfig = map[string]string{
		"disabled":        "false",
		"preferred":       "preferred",
		"verify_identity": "true",
		"skip_verify":     "skip-verify",
	}[cfg.TLSMode]
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
		closeErr := db.Close()
		return nil, errors.Join(fmt.Errorf("connection test failed: %w", err), closeErr)
	}
	return db, nil
}

func (s *SourceService) TestMySQLConnection(ctx context.Context, sourceID, authSecret string) (result SourceTestResult) {
	sourceConfig, err := s.findSourceConfig(ctx, sourceID)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}
	}

	mysqlDB, err := OpenMySQLConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}
	}
	defer func() {
		if closeErr := mysqlDB.Close(); closeErr != nil {
			result = SourceTestResult{Success: false, Error: fmt.Sprintf("failed to close tested connection: %v", closeErr)}
		}
	}()

	cfg, err := ParseMySQLSourceConfig(sourceConfig)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}
	}
	validated := make([]SourceObjectTestFact, 0, len(cfg.Allowlist))
	for _, entry := range cfg.Allowlist {
		exists, err := s.checkMySQLObjectExists(ctx, mysqlDB, entry)
		if err != nil {
			return SourceTestResult{Success: false, Error: fmt.Sprintf("failed to inspect %s.%s: %v", entry.Schema, entry.Name, err), Objects: validated}
		}
		validated = append(validated, SourceObjectTestFact{
			Schema: entry.Schema, Name: entry.Name, Kind: entry.Kind, Exists: exists,
		})
	}

	return SourceTestResult{Success: true, Objects: validated}
}

func (s *SourceService) streamMySQLImportToSQLite(ctx context.Context, mysqlDB *sql.DB, schema, object string, ingester *data.Ingester, tableName string, importRowLimit int) (rowCountResult, colCountResult, rowsSkippedResult int, importTruncatedResult bool, resultErr error) {
	if err := data.ValidateSQLIdent(tableName); err != nil {
		return 0, 0, 0, false, fmt.Errorf("invalid SQLite table name: %w", err)
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
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close upstream rows: %w", closeErr))
		}
	}()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("failed to read column types: %w", err)
	}
	colCount := len(colTypes)
	colNames := make([]string, colCount)
	for i, ct := range colTypes {
		colNames[i] = ct.Name()
	}

	sqliteColTypes := make([]data.ColumnType, colCount)
	for i := range colTypes {
		sqliteColTypes[i] = data.TypePreserve
	}

	if err := ingester.CreateTypedTable(tableName, colNames, sqliteColTypes); err != nil {
		return 0, 0, 0, false, fmt.Errorf("failed to create SQLite table: %w", err)
	}

	batchSize := 5000
	batch := make([][]interface{}, 0, batchSize)
	rowCount := 0
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
			return rowCount, colCount, 0, importTruncated, fmt.Errorf("failed to scan upstream row %d: %w", rowCount+len(batch)+1, err)
		}
		row := make([]interface{}, colCount)
		for i := range vals {
			row[i] = cloneSQLValue(vals[i])
		}
		batch = append(batch, row)
		if len(batch) >= batchSize {
			if err := ingester.InsertBatchValues(tableName, colNames, batch); err != nil {
				return rowCount, colCount, 0, importTruncated, err
			}
			rowCount += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := ingester.InsertBatchValues(tableName, colNames, batch); err != nil {
			return rowCount, colCount, 0, importTruncated, err
		}
		rowCount += len(batch)
	}
	if err := rows.Err(); err != nil {
		return rowCount, colCount, 0, importTruncated, fmt.Errorf("failed while reading upstream data: %w", err)
	}
	return rowCount, colCount, 0, importTruncated, nil
}

func quoteMySQLIdentifier(name string) (string, error) {
	if name == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("empty MySQL identifier")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("MySQL identifier contains NUL byte")
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

func (s *SourceService) checkMySQLObjectExists(ctx context.Context, db *sql.DB, entry AllowlistEntry) (bool, error) {
	tableType, ok := map[string]string{"table": "BASE TABLE", "view": "VIEW"}[entry.Kind]
	if !ok {
		return false, fmt.Errorf("unsupported object kind %q", entry.Kind)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = ? AND table_name = ? AND table_type = ?`,
		entry.Schema, entry.Name, tableType,
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func cloneSQLValue(v interface{}) interface{} {
	if bytes, ok := v.([]byte); ok {
		return append([]byte(nil), bytes...)
	}
	return v
}
