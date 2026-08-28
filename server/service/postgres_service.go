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
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

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

func (s *SourceService) FetchPostgresLiveObjectMetadata(ctx context.Context, sourceConfig *domain.SourceConfig, authSecret string, object SourceObjectRef) (*LiveObjectMetadata, error) {
	pgDB, err := OpenPostgresConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return nil, err
	}
	defer pgDB.Close()

	columnRows, err := pgDB.QueryContext(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`,
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

	var estimate int64
	estimateErr := pgDB.QueryRowContext(ctx,
		`SELECT GREATEST(c.reltuples, 0)::bigint
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`,
		object.Schema, object.Name).Scan(&estimate)
	if estimateErr != nil {
		if errors.Is(estimateErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("upstream object %s.%s does not exist", object.Schema, object.Name)
		}
		estimate = 0
	}

	return &LiveObjectMetadata{Columns: columns, RowCountEstimate: estimate}, nil
}

func (s *SourceService) ExecutePostgresLiveQuery(ctx context.Context, sourceConfig *domain.SourceConfig, authSecret, sql string, timeoutSeconds, maxRows int) (*LiveQueryRows, error) {
	if timeoutSeconds < 1 {
		return nil, fmt.Errorf("timeout_seconds must be positive")
	}
	if maxRows < 1 {
		return nil, fmt.Errorf("max_rows must be positive")
	}
	pgDB, err := OpenPostgresConnection(ctx, sourceConfig, authSecret)
	if err != nil {
		return nil, err
	}
	defer pgDB.Close()

	queryCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	conn, err := pgDB.Conn(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire upstream connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(queryCtx, "BEGIN TRANSACTION READ ONLY"); err != nil {
		return nil, fmt.Errorf("failed to begin read-only transaction: %w", err)
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rollbackCancel()
		_, _ = conn.ExecContext(rollbackCtx, "ROLLBACK")
	}()

	if _, err := conn.ExecContext(queryCtx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeoutSeconds*1000)); err != nil {
		return nil, fmt.Errorf("failed to set statement timeout: %w", err)
	}

	wrapped := fmt.Sprintf("SELECT * FROM (%s) AS _oda_live_query LIMIT %d", sql, maxRows)
	rows, err := conn.QueryContext(queryCtx, wrapped)
	if err != nil {
		return nil, fmt.Errorf("live query execution failed: %w", err)
	}
	defer rows.Close()
	return scanLiveQueryRows(queryCtx, rows, "postgres")
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
