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
	connector := NewMySQLConnector(&SourceService{})
	cfg, err := connector.NormalizeConfig(context.Background(), SourceConfigRequest{
		ConfigProvided: true,
		RawConfig: []byte(`{
			"host":"db.example.com",
			"port":3306,
			"database_name":"analytics",
			"tls_mode":"verify_identity",
			"username":"reader",
			"allowlist":[{"schema":"analytics","name":"orders","kind":"table"}]
		}`),
		RawCredential:      []byte(`{"password":"secret"}`),
		CredentialProvided: true,
		RequireCredential:  true,
		AuthSecret:         secret,
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
	if len(parsed.Allowlist) != 1 || parsed.Allowlist[0].Schema != "analytics" {
		t.Fatalf("unexpected allowlist: %#v", parsed.Allowlist)
	}
	if parsed.TLSMode != "verify_identity" {
		t.Fatalf("unexpected tls mode: %q", parsed.TLSMode)
	}

	var credential MySQLCredential
	if err := DecryptCredential(cfg.CredentialCiphertext, secret, &credential); err != nil {
		t.Fatalf("DecryptCredential returned error: %v", err)
	}
	if credential.Password != "secret" {
		t.Fatalf("unexpected decrypted credential: %#v", credential)
	}
}

func TestValidateAllowlistRejectsImplicitNormalization(t *testing.T) {
	t.Parallel()

	for _, entries := range [][]AllowlistEntry{
		{{Schema: "analytics", Name: "orders", Kind: ""}},
		{{Schema: "", Name: "orders", Kind: "table"}},
		{{Schema: "analytics", Name: "orders", Kind: "TABLE"}},
		{{Schema: "analytics", Name: "orders", Kind: "table"}, {Schema: "analytics", Name: "orders", Kind: "table"}},
	} {
		if _, err := ValidateAllowlist(entries); err == nil {
			t.Fatalf("expected allowlist to be rejected: %#v", entries)
		}
	}
}
