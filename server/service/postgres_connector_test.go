package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func TestPostgresConnectorNormalizeConfigBuildsGenericSourceConfig(t *testing.T) {
	t.Parallel()

	secret := "12345678901234567890123456789012"
	connector := NewPostgresConnector(nil)
	cfg, err := connector.NormalizeConfig(context.Background(), SourceConfigRequest{
		RawConfig: []byte(`{
			"host":"db.example.com",
			"port":5432,
			"database_name":"analytics",
			"default_schema":"public",
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
	if cfg.ConnectorType != domain.SourceTypePostgresConnection {
		t.Fatalf("unexpected connector type: %s", cfg.ConnectorType)
	}
	if strings.Contains(cfg.ConfigJSON, "secret") || strings.Contains(cfg.ConfigJSON, "password") {
		t.Fatalf("config json must not contain credentials: %s", cfg.ConfigJSON)
	}

	parsed, err := ParsePostgresSourceConfig(cfg)
	if err != nil {
		t.Fatalf("ParsePostgresSourceConfig returned error: %v", err)
	}
	if parsed.Driver != "postgres" || parsed.SSLMode != "disable" {
		t.Fatalf("expected postgres defaults, got driver=%q ssl=%q", parsed.Driver, parsed.SSLMode)
	}
	if len(parsed.Allowlist) != 1 || parsed.Allowlist[0].Schema != "public" {
		t.Fatalf("expected default schema applied to allowlist, got %#v", parsed.Allowlist)
	}

	var credential PostgresCredential
	if err := DecryptCredential(cfg.CredentialCiphertext, secret, &credential); err != nil {
		t.Fatalf("DecryptCredential returned error: %v", err)
	}
	if credential.Password != "secret" {
		t.Fatalf("unexpected decrypted credential: %#v", credential)
	}
}
