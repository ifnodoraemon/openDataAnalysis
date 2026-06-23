package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type PGImportResult = SnapshotImportResult

type PostgresSourceConfig struct {
	Driver        string           `json:"driver"`
	Host          string           `json:"host"`
	Port          int              `json:"port"`
	DatabaseName  string           `json:"database_name"`
	DefaultSchema string           `json:"default_schema"`
	SSLMode       string           `json:"ssl_mode"`
	Username      string           `json:"username"`
	Allowlist     []AllowlistEntry `json:"allowlist"`
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

	payloadJSON, _ := json.Marshal(payload)

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
	if err := json.Unmarshal(plaintext, out); err != nil {
		return fmt.Errorf("failed to parse credential payload: %w", err)
	}
	return nil
}

func ParsePostgresSourceConfig(sourceConfig *domain.SourceConfig) (PostgresSourceConfig, error) {
	if sourceConfig == nil {
		return PostgresSourceConfig{}, fmt.Errorf("source config does not exist")
	}
	var cfg PostgresSourceConfig
	if err := json.Unmarshal([]byte(sourceConfig.ConfigJSON), &cfg); err != nil {
		return PostgresSourceConfig{}, fmt.Errorf("failed to parse postgres source config: %w", err)
	}
	if cfg.Driver == "" {
		cfg.Driver = "postgres"
	}
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

	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	connURL := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.Username, credential.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   cfg.DatabaseName,
	}
	q := connURL.Query()
	q.Set("sslmode", sslMode)
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
		_ = db.Close()
		return nil, fmt.Errorf("connection test failed: %w", err)
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
	if err := json.Unmarshal([]byte(allowlistJSON), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse allowlist: %w", err)
	}
	return entries, nil
}

func (s *SourceService) TestPostgresConnection(ctx context.Context, sourceID, authSecret string) map[string]interface{} {
	sourceConfig, err := s.findSourceConfig(ctx, sourceID)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}

	pgDB, err := OpenPostgresConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}
	defer pgDB.Close()

	cfg, err := ParsePostgresSourceConfig(sourceConfig)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}
	var validated []map[string]interface{}
	for _, entry := range cfg.Allowlist {
		exists := s.checkObjectExists(ctx, pgDB, entry)
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

var pgIdentifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func quotePGIdentifier(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return "", fmt.Errorf("empty PostgreSQL identifier")
	}
	if strings.ContainsRune(clean, 0) {
		return "", fmt.Errorf("PostgreSQL identifier contains NUL byte")
	}
	return `"` + strings.ReplaceAll(clean, `"`, `""`) + `"`, nil
}

func (s *SourceService) streamImportToSQLite(ctx context.Context, pgDB *sql.DB, schema, object string, ingester *data.Ingester, tableName string, importRowLimit int) (int, int, int, bool, error) {
	if err := data.ValidateSQLIdent(tableName); err != nil {
		return 0, 0, 0, false, fmt.Errorf("invalid SQLite table name after sanitization: %w", err)
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
		case strings.Contains(dbType, "INT"), strings.Contains(dbType, "SERIAL"):
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
			log.Printf("pg import: scan error on row %d table=%s err=%v", rowCount, tableName, err)
			rowsSkipped++
			continue
		}
		row := make([]string, colCount)
		for i := range vals {
			if vals[i] == nil {
				row[i] = ""
			} else {
				row[i] = formatPGValue(vals[i])
			}
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

func (s *SourceService) findSourceConfig(ctx context.Context, sourceID string) (*domain.SourceConfig, error) {
	return s.SourceConfigRepo.GetBySourceID(ctx, sourceID)
}

func (s *SourceService) checkObjectExists(ctx context.Context, pgDB *sql.DB, entry AllowlistEntry) bool {
	kindTable, ok := map[string]string{"table": "tables", "view": "views"}[entry.Kind]
	if !ok {
		return false
	}
	var count int
	query := fmt.Sprintf(
		"SELECT COUNT(1) FROM information_schema.%s WHERE table_schema = $1 AND table_name = $2",
		kindTable,
	)
	err := pgDB.QueryRowContext(ctx, query, entry.Schema, entry.Name).Scan(&count)
	return err == nil && count > 0
}

func sanitizePGTableName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		name = "t_" + name
	}
	if name == "" || !pgIdentifierPattern.MatchString(name) {
		return "_table_invalid"
	}
	return name
}

func sourceScopedPGTableName(schemaName, objectName, sourceID string) string {
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

func sourceTableSuffix(sourceID string) string {
	cleaned := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.TrimSpace(sourceID))
	if len(cleaned) > 8 {
		return cleaned[len(cleaned)-8:]
	}
	return cleaned
}

func sanitizePGColumnName(name string) string {
	clean := strings.ToLower(strings.TrimSpace(name))
	clean = strings.ReplaceAll(clean, ".", "_")
	clean = strings.ReplaceAll(clean, "-", "_")
	clean = strings.ReplaceAll(clean, " ", "_")
	if clean == "" || !pgIdentifierPattern.MatchString(clean) {
		return "_col_invalid"
	}
	return clean
}

func isInAllowlist(entries []AllowlistEntry, schema, object string) bool {
	for _, e := range entries {
		if e.Schema == schema && e.Name == object {
			return true
		}
		if strings.EqualFold(e.Schema, schema) && strings.EqualFold(e.Name, object) {
			return true
		}
	}
	return false
}

func formatPGValue(v interface{}) string {
	switch tv := v.(type) {
	case time.Time:
		return tv.Format("2006-01-02 15:04:05")
	case *time.Time:
		if tv == nil {
			return ""
		}
		return tv.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", v)
	}
}
