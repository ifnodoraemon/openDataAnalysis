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
	if sources == nil {
		panic("mysql connector requires a source service")
	}
	return &MySQLConnector{Sources: sources}
}

func (c *MySQLConnector) Type() domain.SourceType { return domain.SourceTypeMySQLConnection }

func (c *MySQLConnector) Spec() SourceConnectorSpec {
	return SourceConnectorSpec{
		SourceType:        domain.SourceTypeMySQLConnection,
		Label:             "MySQL",
		Category:          "sql",
		Configurable:      true,
		SecurityModeField: "tls_mode",
		SecurityModeOptions: []SourceConnectorEnumOption{
			{Value: "disabled", Label: "不使用加密"},
			{Value: "preferred", Label: "优先加密"},
			{Value: "verify_identity", Label: "验证证书与主机身份"},
			{Value: "skip_verify", Label: "加密但不验证证书"},
		},
		SupportsAllowlist: true,
		SupportsCatalog:   true,
		SupportsImport:    false,
	}
}

func (c *MySQLConnector) NormalizeConfig(ctx context.Context, req SourceConfigRequest) (*domain.SourceConfig, error) {
	if strings.TrimSpace(req.AuthSecret) == "" || len(req.AuthSecret) < 32 {
		return nil, fmt.Errorf("AUTH_SECRET too short, cannot store source credentials")
	}
	cfg, err := normalizeMySQLConfigJSON(req.RawConfig, req.ConfigProvided, req.Existing)
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
	if req.CredentialProvided {
		var credential MySQLCredential
		if err := decodeStrictJSON(req.RawCredential, &credential); err != nil {
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
		"host":               cfg.Host,
		"port":               cfg.Port,
		"database_name":      cfg.DatabaseName,
		"tls_mode":           cfg.TLSMode,
		"username":           cfg.Username,
		"allowlist":          cfg.Allowlist,
		"last_tested_at":     sourceConfig.LastTestedAt,
		"last_test_status":   sourceConfig.LastTestStatus,
		"last_error_message": sourceConfig.LastErrorMessage,
	}, nil
}

func (c *MySQLConnector) Test(ctx context.Context, req SourceTestRequest) (SourceTestResult, error) {
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

func (c *MySQLConnector) Dialect() string { return "mysql" }

func (c *MySQLConnector) QualifyObject(schema, name string) string {
	qualifiedSchema, schemaErr := quoteMySQLIdentifier(schema)
	qualifiedName, nameErr := quoteMySQLIdentifier(name)
	if schemaErr != nil || nameErr != nil {
		return schema + "." + name
	}
	return qualifiedSchema + "." + qualifiedName
}

func (c *MySQLConnector) FetchLiveObjectMetadata(ctx context.Context, sourceID, authSecret string, object SourceObjectRef) (*LiveObjectMetadata, error) {
	if c.Sources == nil {
		return nil, fmt.Errorf("mysql connector is not initialized")
	}
	sourceConfig, err := c.Sources.findSourceConfig(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	cfg, err := ParseMySQLSourceConfig(sourceConfig)
	if err != nil {
		return nil, err
	}
	if err := validateExactConfigText("schema_name", object.Schema); err != nil {
		return nil, err
	}
	if err := validateExactConfigText("object_name", object.Name); err != nil {
		return nil, err
	}
	if !isInAllowlist(cfg.Allowlist, object.Schema, object.Name) {
		return nil, fmt.Errorf("object %s.%s is not in the data source allowlist", object.Schema, object.Name)
	}
	return c.Sources.FetchMySQLLiveObjectMetadata(ctx, sourceConfig, authSecret, object)
}

func (c *MySQLConnector) ExecuteLiveQuery(ctx context.Context, sourceID, authSecret, sql string, timeoutSeconds, maxRows int) (*LiveQueryRows, error) {
	if c.Sources == nil {
		return nil, fmt.Errorf("mysql connector is not initialized")
	}
	sourceConfig, err := c.Sources.findSourceConfig(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	return c.Sources.ExecuteMySQLLiveQuery(ctx, sourceConfig, authSecret, sql, timeoutSeconds, maxRows)
}

func normalizeMySQLConfigJSON(raw json.RawMessage, provided bool, existing *domain.SourceConfig) (MySQLSourceConfig, error) {
	if !provided {
		if existing == nil {
			return MySQLSourceConfig{}, fmt.Errorf("mysql config is required")
		}
		return ParseMySQLSourceConfig(existing)
	}
	var cfg MySQLSourceConfig
	if err := decodeStrictJSON(raw, &cfg); err != nil {
		return MySQLSourceConfig{}, fmt.Errorf("invalid mysql config: %w", err)
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

func validateMySQLConfig(cfg MySQLSourceConfig) error {
	for _, item := range []struct{ field, value string }{
		{"host", cfg.Host}, {"database_name", cfg.DatabaseName}, {"tls_mode", cfg.TLSMode}, {"username", cfg.Username},
	} {
		if err := validateExactConfigText(item.field, item.value); err != nil {
			return err
		}
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	switch cfg.TLSMode {
	case "disabled", "preferred", "verify_identity", "skip_verify":
	default:
		return fmt.Errorf("unsupported tls_mode: %s", cfg.TLSMode)
	}
	return nil
}
