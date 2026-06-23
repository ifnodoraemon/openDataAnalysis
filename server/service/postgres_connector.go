package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type PostgresConnector struct {
	Sources *SourceService
}

func NewPostgresConnector(sources *SourceService) *PostgresConnector {
	return &PostgresConnector{Sources: sources}
}

func (c *PostgresConnector) Type() domain.SourceType { return domain.SourceTypePostgresConnection }

func (c *PostgresConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	if strings.TrimSpace(req.AuthSecret) == "" || len(req.AuthSecret) < 32 {
		return nil, fmt.Errorf("AUTH_SECRET too short, cannot store source credentials")
	}

	cfg, err := normalizePostgresConfigJSON(req.RawConfig, req.Existing)
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
	credentialProvided := len(req.RawCredential) > 0 && strings.TrimSpace(string(req.RawCredential)) != "" && strings.TrimSpace(string(req.RawCredential)) != "null"
	if credentialProvided {
		var credential PostgresCredential
		if err := json.Unmarshal(req.RawCredential, &credential); err != nil {
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
		"driver":             cfg.Driver,
		"host":               cfg.Host,
		"port":               cfg.Port,
		"database_name":      cfg.DatabaseName,
		"default_schema":     cfg.DefaultSchema,
		"ssl_mode":           cfg.SSLMode,
		"username":           cfg.Username,
		"allowlist":          cfg.Allowlist,
		"last_tested_at":     sourceConfig.LastTestedAt,
		"last_test_status":   sourceConfig.LastTestStatus,
		"last_error_message": sourceConfig.LastErrorMessage,
	}, nil
}

func (c *PostgresConnector) Test(ctx context.Context, req SourceTestRequest) (map[string]interface{}, error) {
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
		importRowLimit = 0
	}

	objectName := strings.TrimSpace(req.Object.Name)
	if objectName == "" {
		return nil, fmt.Errorf("object_name is required")
	}
	resolvedSchema := strings.TrimSpace(req.Object.Schema)
	if resolvedSchema == "" {
		resolvedSchema = cfg.DefaultSchema
	}
	if strings.TrimSpace(resolvedSchema) == "" {
		return nil, fmt.Errorf("schema_name or connection default_schema is required")
	}

	if !isInAllowlist(cfg.Allowlist, resolvedSchema, objectName) {
		return nil, fmt.Errorf("object %s.%s is not in the data source allowlist", resolvedSchema, objectName)
	}

	pgDB, err := OpenPostgresConnection(ctx, sourceConfig, req.AuthSecret)
	if err != nil {
		return nil, err
	}
	defer pgDB.Close()

	tableName := sourceScopedPGTableName(resolvedSchema, objectName, req.SourceID)
	preSnapshot, err := c.Sources.BeginSnapshotImport(
		ctx, req.SessionID, req.SourceID,
		string(domain.SourceTypePostgresConnection), resolvedSchema, objectName, tableName,
	)
	if err != nil {
		return nil, err
	}

	importStart := time.Now()
	rowCount, colCount, rowsSkipped, importTruncated, err := c.Sources.streamImportToSQLite(ctx, pgDB, resolvedSchema, objectName, req.Ingester, tableName, importRowLimit)
	importDuration := time.Since(importStart)
	if err != nil {
		errMsg := err.Error()
		_ = c.Sources.SnapshotRepo.UpdateStatus(ctx, preSnapshot.ID, domain.SnapshotStatusFailed, &errMsg)
		return nil, fmt.Errorf("import failed: %w", err)
	}

	var warnings []string
	if rowsSkipped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d upstream rows were skipped during import because they could not be scanned", rowsSkipped))
	}

	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		workspaceID = source.WorkspaceID
	}

	return c.Sources.FinalizeSnapshotImport(ctx, SnapshotImportCompletion{
		SnapshotID:        preSnapshot.ID,
		SessionID:         req.SessionID,
		WorkspaceID:       workspaceID,
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
		AnalyzeSemantics:  true,
		ExtraWarnings:     warnings,
		Ingester:          req.Ingester,
	})
}

func normalizePostgresConfigJSON(raw json.RawMessage, existing *domain.SourceConfig) (PostgresSourceConfig, error) {
	rawText := strings.TrimSpace(string(raw))
	if rawText == "" || rawText == "null" {
		if existing == nil {
			return PostgresSourceConfig{}, fmt.Errorf("postgres config is required")
		}
		return ParsePostgresSourceConfig(existing)
	}
	var cfg PostgresSourceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return PostgresSourceConfig{}, fmt.Errorf("invalid postgres config: %w", err)
	}
	cfg.Driver = strings.TrimSpace(cfg.Driver)
	if cfg.Driver == "" {
		cfg.Driver = "postgres"
	}
	if cfg.Driver != "postgres" {
		return PostgresSourceConfig{}, fmt.Errorf("postgres config driver must be postgres")
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.DatabaseName = strings.TrimSpace(cfg.DatabaseName)
	cfg.DefaultSchema = strings.TrimSpace(cfg.DefaultSchema)
	cfg.SSLMode = strings.TrimSpace(cfg.SSLMode)
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.Host == "" || cfg.DatabaseName == "" || cfg.Username == "" {
		return PostgresSourceConfig{}, fmt.Errorf("host, database_name and username are required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return PostgresSourceConfig{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}
	switch cfg.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return PostgresSourceConfig{}, fmt.Errorf("unsupported ssl_mode: %s", cfg.SSLMode)
	}
	allowlist, err := NormalizeAllowlist(cfg.Allowlist, cfg.DefaultSchema)
	if err != nil {
		return PostgresSourceConfig{}, err
	}
	cfg.Allowlist = allowlist
	return cfg, nil
}

func NormalizeAllowlist(entries []AllowlistEntry, defaultSchema string) ([]AllowlistEntry, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("allowlist must include at least one table or view")
	}
	normalized := make([]AllowlistEntry, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		schema := strings.TrimSpace(entry.Schema)
		if schema == "" {
			schema = strings.TrimSpace(defaultSchema)
		}
		name := strings.TrimSpace(entry.Name)
		kind := strings.ToLower(strings.TrimSpace(entry.Kind))
		if kind == "" {
			kind = "table"
		}
		if schema == "" || name == "" {
			return nil, fmt.Errorf("allowlist entries require schema and name")
		}
		if kind != "table" && kind != "view" {
			return nil, fmt.Errorf("allowlist kind must be table or view")
		}
		key := strings.ToLower(schema + "." + name + "." + kind)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, AllowlistEntry{Schema: schema, Name: name, Kind: kind})
	}
	return normalized, nil
}
