package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type MySQLConnector struct {
	Sources *SourceService
}

func NewMySQLConnector(sources *SourceService) *MySQLConnector {
	return &MySQLConnector{Sources: sources}
}

func (c *MySQLConnector) Type() domain.SourceType { return domain.SourceTypeMySQLConnection }

func (c *MySQLConnector) Spec() SourceConnectorSpec {
	return SourceConnectorSpec{
		SourceType:        domain.SourceTypeMySQLConnection,
		Label:             "MySQL",
		Category:          "sql",
		Configurable:      true,
		DefaultPort:       3306,
		SupportsAllowlist: true,
		SupportsCatalog:   true,
		SupportsImport:    true,
	}
}

func (c *MySQLConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	if strings.TrimSpace(req.AuthSecret) == "" || len(req.AuthSecret) < 32 {
		return nil, fmt.Errorf("AUTH_SECRET too short, cannot store source credentials")
	}
	cfg, err := normalizeMySQLConfigJSON(req.RawConfig, req.Existing)
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize mysql config: %w", err)
	}

	credentialCiphertext := []byte(nil)
	if req.Existing != nil {
		credentialCiphertext = req.Existing.CredentialCiphertext
	}
	credentialProvided := len(req.RawCredential) > 0 && strings.TrimSpace(string(req.RawCredential)) != "" && strings.TrimSpace(string(req.RawCredential)) != "null"
	if credentialProvided {
		var credential MySQLCredential
		if err := json.Unmarshal(req.RawCredential, &credential); err != nil {
			return nil, fmt.Errorf("invalid mysql credential: %w", err)
		}
		if strings.TrimSpace(credential.Password) == "" {
			return nil, fmt.Errorf("mysql credential password is required")
		}
		ciphertext, err := EncryptCredential(credential, req.AuthSecret)
		if err != nil {
			return nil, fmt.Errorf("credential encryption failed: %w", err)
		}
		credentialCiphertext = ciphertext
	}
	if req.RequireCredential && len(credentialCiphertext) == 0 {
		return nil, fmt.Errorf("mysql credential is required")
	}

	now := time.Now()
	return &domain.SourceConfig{
		SourceID:             req.SourceID,
		ConnectorType:        domain.SourceTypeMySQLConnection,
		ConfigJSON:           string(configJSON),
		CredentialCiphertext: credentialCiphertext,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

func (c *MySQLConnector) PublicConfig(ctx context.Context, sourceID string) (map[string]interface{}, error) {
	sourceConfig, err := c.Sources.findSourceConfig(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParseMySQLSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"driver":             cfg.Driver,
		"host":               cfg.Host,
		"port":               cfg.Port,
		"database_name":      cfg.DatabaseName,
		"default_schema":     cfg.DefaultSchema,
		"username":           cfg.Username,
		"allowlist":          cfg.Allowlist,
		"last_tested_at":     sourceConfig.LastTestedAt,
		"last_test_status":   sourceConfig.LastTestStatus,
		"last_error_message": sourceConfig.LastErrorMessage,
	}, nil
}

func (c *MySQLConnector) Test(ctx context.Context, req SourceTestRequest) (map[string]interface{}, error) {
	return c.Sources.TestMySQLConnection(ctx, req.SourceID, req.AuthSecret), nil
}

func (c *MySQLConnector) Catalog(ctx context.Context, sourceID string) ([]SourceCatalogObject, error) {
	sourceConfig, err := c.Sources.findSourceConfig(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParseMySQLSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	objects := make([]SourceCatalogObject, 0, len(cfg.Allowlist))
	for _, entry := range cfg.Allowlist {
		objects = append(objects, SourceCatalogObject{
			Schema:          entry.Schema,
			Name:            entry.Name,
			Kind:            entry.Kind,
			SourceObjectKey: SourceObjectKey(sourceID, string(domain.SourceTypeMySQLConnection), entry.Schema, entry.Name),
		})
	}
	return objects, nil
}

func (c *MySQLConnector) Import(ctx context.Context, req SourceImportRequest) (*SnapshotImportResult, error) {
	if c.Sources == nil {
		return nil, fmt.Errorf("mysql connector is not initialized")
	}
	if req.Ingester == nil {
		return nil, fmt.Errorf("analysis database is not initialized")
	}
	source, err := c.Sources.DataSourceRepo.GetByID(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	if source.SourceType != domain.SourceTypeMySQLConnection {
		return nil, fmt.Errorf("source %s is not a mysql connection", req.SourceID)
	}
	sourceConfig, err := c.Sources.findSourceConfig(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParseMySQLSourceConfig(sourceConfig)
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

	mysqlDB, err := OpenMySQLConnection(ctx, sourceConfig, req.AuthSecret)
	if err != nil {
		return nil, err
	}
	defer mysqlDB.Close()

	tableName := sourceScopedMySQLTableName(resolvedSchema, objectName, req.SourceID)
	preSnapshot, err := c.Sources.BeginSnapshotImport(
		ctx, req.SessionID, req.SourceID,
		string(domain.SourceTypeMySQLConnection), resolvedSchema, objectName, tableName,
	)
	if err != nil {
		return nil, err
	}

	importStart := time.Now()
	rowCount, colCount, rowsSkipped, importTruncated, err := c.Sources.streamMySQLImportToSQLite(ctx, mysqlDB, resolvedSchema, objectName, req.Ingester, tableName, importRowLimit)
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
		UpstreamKind:      string(domain.SourceTypeMySQLConnection),
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

func normalizeMySQLConfigJSON(raw json.RawMessage, existing *domain.SourceConfig) (MySQLSourceConfig, error) {
	rawText := strings.TrimSpace(string(raw))
	if rawText == "" || rawText == "null" {
		if existing == nil {
			return MySQLSourceConfig{}, fmt.Errorf("mysql config is required")
		}
		return ParseMySQLSourceConfig(existing)
	}
	var cfg MySQLSourceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return MySQLSourceConfig{}, fmt.Errorf("invalid mysql config: %w", err)
	}
	cfg.Driver = strings.TrimSpace(cfg.Driver)
	if cfg.Driver == "" {
		cfg.Driver = "mysql"
	}
	if cfg.Driver != "mysql" {
		return MySQLSourceConfig{}, fmt.Errorf("mysql config driver must be mysql")
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.DatabaseName = strings.TrimSpace(cfg.DatabaseName)
	cfg.DefaultSchema = strings.TrimSpace(cfg.DefaultSchema)
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.DefaultSchema == "" {
		cfg.DefaultSchema = cfg.DatabaseName
	}
	if cfg.Host == "" || cfg.DatabaseName == "" || cfg.Username == "" {
		return MySQLSourceConfig{}, fmt.Errorf("host, database_name and username are required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return MySQLSourceConfig{}, fmt.Errorf("port must be between 1 and 65535")
	}
	allowlist, err := NormalizeAllowlist(cfg.Allowlist, cfg.DefaultSchema)
	if err != nil {
		return MySQLSourceConfig{}, err
	}
	cfg.Allowlist = allowlist
	return cfg, nil
}
