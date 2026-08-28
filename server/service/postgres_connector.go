package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type PostgresConnector struct {
	Sources *SourceService
}

func NewPostgresConnector(sources *SourceService) *PostgresConnector {
	if sources == nil {
		panic("postgres connector requires a source service")
	}
	return &PostgresConnector{Sources: sources}
}

func (c *PostgresConnector) Type() domain.SourceType { return domain.SourceTypePostgresConnection }

func (c *PostgresConnector) Spec() SourceConnectorSpec {
	return SourceConnectorSpec{
		SourceType:        domain.SourceTypePostgresConnection,
		Label:             "PostgreSQL",
		Category:          "sql",
		Configurable:      true,
		SecurityModeField: "ssl_mode",
		SecurityModeOptions: []SourceConnectorEnumOption{
			{Value: "disable", Label: "不使用加密"},
			{Value: "allow", Label: "允许加密"},
			{Value: "prefer", Label: "优先加密"},
			{Value: "require", Label: "必须加密"},
			{Value: "verify-ca", Label: "验证证书颁发机构"},
			{Value: "verify-full", Label: "完整验证证书与主机名"},
		},
		SupportsAllowlist: true,
		SupportsCatalog:   true,
		SupportsImport:    true,
	}
}

func (c *PostgresConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	if strings.TrimSpace(req.AuthSecret) == "" || len(req.AuthSecret) < 32 {
		return nil, fmt.Errorf("AUTH_SECRET too short, cannot store source credentials")
	}

	cfg, err := normalizePostgresConfigJSON(req.RawConfig, req.ConfigProvided, req.Existing)
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize postgres config: %w", err)
	}

	credentialCiphertext := []byte(nil)
	if req.Existing != nil {
		credentialCiphertext = req.Existing.CredentialCiphertext
	}
	if req.CredentialProvided {
		var credential PostgresCredential
		if err := decodeStrictJSON(req.RawCredential, &credential); err != nil {
			return nil, fmt.Errorf("invalid postgres credential: %w", err)
		}
		if strings.TrimSpace(credential.Password) == "" {
			return nil, fmt.Errorf("postgres credential password is required")
		}
		ciphertext, err := EncryptCredential(credential, req.AuthSecret)
		if err != nil {
			return nil, fmt.Errorf("credential encryption failed: %w", err)
		}
		credentialCiphertext = ciphertext
	}
	if req.RequireCredential && len(credentialCiphertext) == 0 {
		return nil, fmt.Errorf("postgres credential is required")
	}

	now := time.Now()
	return &domain.SourceConfig{
		SourceID:             req.SourceID,
		ConnectorType:        domain.SourceTypePostgresConnection,
		ConfigJSON:           string(configJSON),
		CredentialCiphertext: credentialCiphertext,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

func (c *PostgresConnector) PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error) {
	sourceConfig, err := c.Sources.findSourceConfig(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParsePostgresSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"host":               cfg.Host,
		"port":               cfg.Port,
		"database_name":      cfg.DatabaseName,
		"ssl_mode":           cfg.SSLMode,
		"username":           cfg.Username,
		"allowlist":          cfg.Allowlist,
		"last_tested_at":     sourceConfig.LastTestedAt,
		"last_test_status":   sourceConfig.LastTestStatus,
		"last_error_message": sourceConfig.LastErrorMessage,
	}, nil
}

func (c *PostgresConnector) Test(ctx context.Context, req SourceTestRequest) (SourceTestResult, error) {
	return c.Sources.TestPostgresConnection(ctx, req.SourceID, req.AuthSecret), nil
}

func (c *PostgresConnector) Catalog(ctx context.Context, sourceID string) ([]SourceCatalogObject, error) {
	sourceConfig, err := c.Sources.findSourceConfig(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParsePostgresSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	objects := make([]SourceCatalogObject, 0, len(cfg.Allowlist))
	for _, entry := range cfg.Allowlist {
		objects = append(objects, SourceCatalogObject{
			Schema:          entry.Schema,
			Name:            entry.Name,
			Kind:            entry.Kind,
			SourceObjectKey: SourceObjectKey(sourceID, string(domain.SourceTypePostgresConnection), entry.Schema, entry.Name),
		})
	}
	return objects, nil
}

func (c *PostgresConnector) Import(ctx context.Context, req SourceImportRequest) (*SnapshotImportResult, error) {
	if c.Sources == nil {
		return nil, fmt.Errorf("postgres connector is not initialized")
	}
	if req.Ingester == nil {
		return nil, fmt.Errorf("analysis database is not initialized")
	}

	source, err := c.Sources.DataSourceRepo.GetByID(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	if source.SourceType != domain.SourceTypePostgresConnection {
		return nil, fmt.Errorf("source %s is not a postgres connection", req.SourceID)
	}

	sourceConfig, err := c.Sources.findSourceConfig(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParsePostgresSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	importRowLimit := req.ImportRowLimit
	if importRowLimit < 0 {
		return nil, fmt.Errorf("import_row_limit cannot be negative")
	}

	objectName := req.Object.Name
	if err := validateExactConfigText("object_name", objectName); err != nil {
		return nil, fmt.Errorf("object_name is required")
	}
	resolvedSchema := req.Object.Schema
	if err := validateExactConfigText("schema_name", resolvedSchema); err != nil {
		return nil, err
	}

	if !isInAllowlist(cfg.Allowlist, resolvedSchema, objectName) {
		return nil, fmt.Errorf("object %s.%s is not in the data source allowlist", resolvedSchema, objectName)
	}

	pgDB, err := OpenPostgresConnection(ctx, sourceConfig, req.AuthSecret)
	if err != nil {
		return nil, err
	}
	defer pgDB.Close()

	preSnapshot, err := c.Sources.BeginSnapshotImport(
		ctx, req.SessionID, req.SourceID,
		string(domain.SourceTypePostgresConnection), resolvedSchema, objectName,
	)
	if err != nil {
		return nil, err
	}
	tableName := preSnapshot.AnalysisTableName

	importStart := time.Now()
	rowCount, colCount, rowsSkipped, importTruncated, err := c.Sources.streamImportToSQLite(ctx, pgDB, resolvedSchema, objectName, req.Ingester, tableName, importRowLimit)
	importDuration := time.Since(importStart)
	if err != nil {
		errMsg := err.Error()
		statusErr := c.Sources.SnapshotRepo.UpdateStatus(ctx, preSnapshot.ID, domain.SnapshotStatusFailed, &errMsg)
		return nil, errors.Join(fmt.Errorf("import failed: %w", err), statusErr)
	}

	var warnings []string
	if rowsSkipped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d upstream rows were skipped during import because they could not be scanned", rowsSkipped))
	}

	return c.Sources.FinalizeSnapshotImport(ctx, SnapshotImportCompletion{
		SnapshotID:        preSnapshot.ID,
		SessionID:         req.SessionID,
		SourceID:          req.SourceID,
		UpstreamKind:      string(domain.SourceTypePostgresConnection),
		UpstreamSchema:    resolvedSchema,
		UpstreamObject:    objectName,
		AnalysisTableName: tableName,
		RowCount:          rowCount,
		ColCount:          colCount,
		RowsImported:      rowCount,
		RowsSkipped:       rowsSkipped,
		ImportRowLimit:    importRowLimit,
		ImportTruncated:   importTruncated,
		ImportDuration:    importDuration,
		ExtraWarnings:     warnings,
		Ingester:          req.Ingester,
	})
}

func normalizePostgresConfigJSON(raw json.RawMessage, provided bool, existing *domain.SourceConfig) (PostgresSourceConfig, error) {
	if !provided {
		if existing == nil {
			return PostgresSourceConfig{}, fmt.Errorf("postgres config is required")
		}
		return ParsePostgresSourceConfig(existing)
	}
	var cfg PostgresSourceConfig
	if err := decodeStrictJSON(raw, &cfg); err != nil {
		return PostgresSourceConfig{}, fmt.Errorf("invalid postgres config: %w", err)
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

func validatePostgresConfig(cfg PostgresSourceConfig) error {
	for _, item := range []struct{ field, value string }{
		{"host", cfg.Host}, {"database_name", cfg.DatabaseName}, {"username", cfg.Username}, {"ssl_mode", cfg.SSLMode},
	} {
		if err := validateExactConfigText(item.field, item.value); err != nil {
			return err
		}
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	switch cfg.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("unsupported ssl_mode: %s", cfg.SSLMode)
	}
}

func ValidateAllowlist(entries []AllowlistEntry) ([]AllowlistEntry, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("allowlist must include at least one table or view")
	}
	validated := make([]AllowlistEntry, 0, len(entries))
	type allowlistKey struct{ schema, name, kind string }
	seen := map[allowlistKey]struct{}{}
	for _, entry := range entries {
		if err := validateExactConfigText("allowlist schema", entry.Schema); err != nil {
			return nil, err
		}
		if err := validateExactConfigText("allowlist name", entry.Name); err != nil {
			return nil, err
		}
		if entry.Kind != "table" && entry.Kind != "view" {
			return nil, fmt.Errorf("allowlist kind must be table or view")
		}
		key := allowlistKey{schema: entry.Schema, name: entry.Name, kind: entry.Kind}
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate allowlist entry: %s.%s (%s)", entry.Schema, entry.Name, entry.Kind)
		}
		seen[key] = struct{}{}
		validated = append(validated, entry)
	}
	return validated, nil
}
