package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func TestMySQLConnectorNormalizeConfigBuildsGenericSourceConfig(t *testing.T) {
	t.Parallel()

	secret := "12345678901234567890123456789012"
	connector := NewMySQLConnector(nil)
	cfg, err := connector.NormalizeConfig(context.Background(), SourceConfigRequest{
		RawConfig: []byte(`{
			"host":"db.example.com",
			"port":3306,
			"database_name":"analytics",
			"username":"reader",
			"allowlist":[{"name":"orders","kind":"table"}]
		}`),
		RawCredential:     []byte(`{"password":"secret"}`),
		RequireCredential: true,
		AuthSecret:        secret,
	})
	if err != nil {
		t.Fatalf("NormalizeConfig returned error: %v", err)
	}
	if cfg.ConnectorType != domain.SourceTypeMySQLConnection {
		t.Fatalf("unexpected connector type: %s", cfg.ConnectorType)
	}
	if strings.Contains(cfg.ConfigJSON, "secret") || strings.Contains(cfg.ConfigJSON, "password") {
		t.Fatalf("config json must not contain credentials: %s", cfg.ConfigJSON)
	}

	parsed, err := ParseMySQLSourceConfig(cfg)
	if err != nil {
		t.Fatalf("ParseMySQLSourceConfig returned error: %v", err)
	}
	if parsed.Driver != "mysql" {
		t.Fatalf("expected mysql driver, got %q", parsed.Driver)
	}
	if parsed.DefaultSchema != "analytics" {
		t.Fatalf("expected database_name as default schema, got %q", parsed.DefaultSchema)
	}
	if len(parsed.Allowlist) != 1 || parsed.Allowlist[0].Schema != "analytics" {
		t.Fatalf("expected default schema applied to allowlist, got %#v", parsed.Allowlist)
	}

	var credential MySQLCredential
	if err := DecryptCredential(cfg.CredentialCiphertext, secret, &credential); err != nil {
		t.Fatalf("DecryptCredential returned error: %v", err)
	}
	if credential.Password != "secret" {
		t.Fatalf("unexpected decrypted credential: %#v", credential)
	}
}
