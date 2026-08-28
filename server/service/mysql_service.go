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

func (s *SourceService) FetchMySQLLiveObjectMetadata(ctx context.Context, sourceConfig *domain.SourceConfig, authSecret string, object SourceObjectRef) (*LiveObjectMetadata, error) {
	mysqlDB, err := OpenMySQLConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return nil, err
	}
	defer mysqlDB.Close()

	columnRows, err := mysqlDB.QueryContext(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`,
		object.Schema, object.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to read upstream column metadata: %w", err)
	}
	columns := make([]LiveColumn, 0, 16)
	for columnRows.Next() {
		var name, declaredType string
		if err := columnRows.Scan(&name, &declaredType); err != nil {
			columnRows.Close()
			return nil, fmt.Errorf("failed to scan upstream column metadata: %w", err)
		}
		columns = append(columns, LiveColumn{Name: name, DeclaredType: declaredType})
	}
	closeErr := columnRows.Close()
	if err := columnRows.Err(); err != nil {
		return nil, fmt.Errorf("failed while reading upstream column metadata: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close upstream column metadata rows: %w", closeErr)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("upstream object %s.%s does not exist or exposes no columns", object.Schema, object.Name)
	}

	var estimate sql.NullInt64
	estimateErr := mysqlDB.QueryRowContext(ctx,
		`SELECT table_rows FROM information_schema.tables WHERE table_schema = ? AND table_name = ?`,
		object.Schema, object.Name).Scan(&estimate)
	if estimateErr != nil {
		if errors.Is(estimateErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("upstream object %s.%s does not exist", object.Schema, object.Name)
		}
	}
	if !estimate.Valid || estimate.Int64 < 0 {
		estimate.Int64 = 0
	}

	return &LiveObjectMetadata{Columns: columns, RowCountEstimate: estimate.Int64}, nil
}

func (s *SourceService) ExecuteMySQLLiveQuery(ctx context.Context, sourceConfig *domain.SourceConfig, authSecret, sql string, timeoutSeconds, maxRows int) (*LiveQueryRows, error) {
	if timeoutSeconds < 1 {
		return nil, fmt.Errorf("timeout_seconds must be positive")
	}
	if maxRows < 1 {
		return nil, fmt.Errorf("max_rows must be positive")
	}
	mysqlDB, err := OpenMySQLConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return nil, err
	}
	defer mysqlDB.Close()

	queryCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	conn, err := mysqlDB.Conn(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire upstream connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(queryCtx, fmt.Sprintf("SET SESSION max_execution_time=%d", timeoutSeconds*1000)); err != nil {
		return nil, fmt.Errorf("failed to set statement timeout: %w", err)
	}
	if _, err := conn.ExecContext(queryCtx, "START TRANSACTION READ ONLY"); err != nil {
		return nil, fmt.Errorf("failed to begin read-only transaction: %w", err)
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rollbackCancel()
		_, _ = conn.ExecContext(rollbackCtx, "ROLLBACK")
	}()

	wrapped := fmt.Sprintf("SELECT * FROM (%s) AS _oda_live_query LIMIT %d", sql, maxRows)
	rows, err := conn.QueryContext(queryCtx, wrapped)
	if err != nil {
		return nil, fmt.Errorf("live query execution failed: %w", err)
	}
	defer rows.Close()
	return scanLiveQueryRows(queryCtx, rows, "mysql")
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
