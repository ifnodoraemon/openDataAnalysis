package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type PGImportResult = SnapshotImportResult

type PostgresSourceConfig struct {
	Host         string           `json:"host"`
	Port         int              `json:"port"`
	DatabaseName string           `json:"database_name"`
	SSLMode      string           `json:"ssl_mode"`
	Username     string           `json:"username"`
	Allowlist    []AllowlistEntry `json:"allowlist"`
}

type PostgresCredential struct {
	Password string `json:"password"`
}

func EncryptCredential(payload interface{}, authSecret string) ([]byte, error) {
	key := sha256.Sum256([]byte(authSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode credential payload: %w", err)
	}

	ciphertext := aesGCM.Seal(nonce, nonce, payloadJSON, nil)
	return ciphertext, nil
}

func DecryptCredential(ciphertext []byte, authSecret string, out interface{}) error {
	key := sha256.Sum256([]byte(authSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextBody := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertextBody, nil)
	if err != nil {
		return fmt.Errorf("decryption failed: %w", err)
	}
	if err := decodeStrictJSON(plaintext, out); err != nil {
		return fmt.Errorf("failed to parse credential payload: %w", err)
	}
	return nil
}

func ParsePostgresSourceConfig(sourceConfig *domain.SourceConfig) (PostgresSourceConfig, error) {
	if sourceConfig == nil {
		return PostgresSourceConfig{}, fmt.Errorf("source config does not exist")
	}
	var cfg PostgresSourceConfig
	if err := decodeStrictJSON([]byte(sourceConfig.ConfigJSON), &cfg); err != nil {
		return PostgresSourceConfig{}, fmt.Errorf("failed to parse postgres source config: %w", err)
	}
	if err := validatePostgresConfig(cfg); err != nil {
		return PostgresSourceConfig{}, err
	}
	allowlist, err := ValidateAllowlist(cfg.Allowlist)
	if err != nil {
		return PostgresSourceConfig{}, err
	}
	cfg.Allowlist = allowlist
	return cfg, nil
}

func OpenPostgresConnection(ctx context.Context, sourceConfig *domain.SourceConfig, authSecret string) (*sql.DB, error) {
	cfg, err := ParsePostgresSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	var credential PostgresCredential
	if err := DecryptCredential(sourceConfig.CredentialCiphertext, authSecret, &credential); err != nil {
		return nil, fmt.Errorf("credential decryption failed: %w", err)
	}
	if strings.TrimSpace(credential.Password) == "" {
		return nil, fmt.Errorf("postgres credential password is empty")
	}

	connURL := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.Username, credential.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   cfg.DatabaseName,
	}
	q := connURL.Query()
	q.Set("sslmode", cfg.SSLMode)
	connURL.RawQuery = q.Encode()
	pgConfig, err := pgx.ParseConfig(connURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to parse PostgreSQL connection config: %w", err)
	}
	if pgConfig.RuntimeParams == nil {
		pgConfig.RuntimeParams = map[string]string{}
	}
	pgConfig.RuntimeParams["default_transaction_read_only"] = "on"

	db := stdlib.OpenDB(*pgConfig)

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		closeErr := db.Close()
		return nil, errors.Join(fmt.Errorf("connection test failed: %w", err), closeErr)
	}

	return db, nil
}

type AllowlistEntry struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

func ParseAllowlist(allowlistJSON string) ([]AllowlistEntry, error) {
	var entries []AllowlistEntry
	if err := decodeStrictJSON([]byte(allowlistJSON), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse allowlist: %w", err)
	}
	return entries, nil
}

func (s *SourceService) TestPostgresConnection(ctx context.Context, sourceID, authSecret string) (result SourceTestResult) {
	sourceConfig, err := s.findSourceConfig(ctx, sourceID)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}
	}

	pgDB, err := OpenPostgresConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}
	}
	defer func() {
		if closeErr := pgDB.Close(); closeErr != nil {
			result = SourceTestResult{Success: false, Error: fmt.Sprintf("failed to close tested connection: %v", closeErr)}
		}
	}()

	cfg, err := ParsePostgresSourceConfig(sourceConfig)
	if err != nil {
		return SourceTestResult{Success: false, Error: err.Error()}
	}
	validated := make([]SourceObjectTestFact, 0, len(cfg.Allowlist))
	for _, entry := range cfg.Allowlist {
		exists, err := s.checkObjectExists(ctx, pgDB, entry)
		if err != nil {
			return SourceTestResult{Success: false, Error: fmt.Sprintf("failed to inspect %s.%s: %v", entry.Schema, entry.Name, err), Objects: validated}
		}
		validated = append(validated, SourceObjectTestFact{
			Schema: entry.Schema, Name: entry.Name, Kind: entry.Kind, Exists: exists,
		})
	}

	return SourceTestResult{Success: true, Objects: validated}
}

func (s *SourceService) ImportPostgresSnapshot(ctx context.Context, sourceID, sessionID, schemaName, objectName string, sessIngester *data.Ingester, authSecret string, importRowLimit int) (*PGImportResult, error) {
	return NewPostgresConnector(s).Import(ctx, SourceImportRequest{
		SourceID:       sourceID,
		SessionID:      sessionID,
		Object:         SourceObjectRef{Schema: schemaName, Name: objectName},
		Ingester:       sessIngester,
		AuthSecret:     authSecret,
		ImportRowLimit: importRowLimit,
	})
}

func quotePGIdentifier(name string) (string, error) {
	if name == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("empty PostgreSQL identifier")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("PostgreSQL identifier contains NUL byte")
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}

func (s *SourceService) streamImportToSQLite(ctx context.Context, pgDB *sql.DB, schema, object string, ingester *data.Ingester, tableName string, importRowLimit int) (rowCountResult, colCountResult, rowsSkippedResult int, importTruncatedResult bool, resultErr error) {
	if err := data.ValidateSQLIdent(tableName); err != nil {
		return 0, 0, 0, false, fmt.Errorf("invalid SQLite table name: %w", err)
	}
	quotedSchema, err := quotePGIdentifier(schema)
	if err != nil {
		return 0, 0, 0, false, err
	}
	quotedObject, err := quotePGIdentifier(object)
	if err != nil {
		return 0, 0, 0, false, err
	}
	qualifiedName := fmt.Sprintf("%s.%s", quotedSchema, quotedObject)

	query := fmt.Sprintf("SELECT * FROM %s", qualifiedName)
	if importRowLimit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, importRowLimit+1)
	}
	rows, err := pgDB.QueryContext(ctx, query)
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

func (s *SourceService) findSourceConfig(ctx context.Context, sourceID string) (*domain.SourceConfig, error) {
	return s.SourceConfigRepo.GetBySourceID(ctx, sourceID)
}

func (s *SourceService) checkObjectExists(ctx context.Context, pgDB *sql.DB, entry AllowlistEntry) (bool, error) {
	kindTable, ok := map[string]string{"table": "tables", "view": "views"}[entry.Kind]
	if !ok {
		return false, fmt.Errorf("unsupported object kind %q", entry.Kind)
	}
	var count int
	query := fmt.Sprintf(
		"SELECT COUNT(1) FROM information_schema.%s WHERE table_schema = $1 AND table_name = $2",
		kindTable,
	)
	if err := pgDB.QueryRowContext(ctx, query, entry.Schema, entry.Name).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func isInAllowlist(entries []AllowlistEntry, schema, object string) bool {
	for _, e := range entries {
		if e.Schema == schema && e.Name == object {
			return true
		}
	}
	return false
}
