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
	connector := NewPostgresConnector(&SourceService{})
	cfg, err := connector.NormalizeConfig(context.Background(), SourceConfigRequest{
		ConfigProvided: true,
		RawConfig: []byte(`{
			"host":"db.example.com",
			"port":5432,
			"database_name":"analytics",
			"ssl_mode":"disable",
			"username":"reader",
			"allowlist":[{"schema":"public","name":"orders","kind":"table"}]
		}`),
		RawCredential:      []byte(`{"password":"secret"}`),
		CredentialProvided: true,
		RequireCredential:  true,
		AuthSecret:         secret,
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
	if parsed.SSLMode != "disable" {
		t.Fatalf("unexpected ssl mode: %q", parsed.SSLMode)
	}
	if len(parsed.Allowlist) != 1 || parsed.Allowlist[0].Schema != "public" {
		t.Fatalf("unexpected allowlist: %#v", parsed.Allowlist)
	}

	var credential PostgresCredential
	if err := DecryptCredential(cfg.CredentialCiphertext, secret, &credential); err != nil {
		t.Fatalf("DecryptCredential returned error: %v", err)
	}
	if credential.Password != "secret" {
		t.Fatalf("unexpected decrypted credential: %#v", credential)
	}
}

func TestPostgresConnectorRejectsImplicitOrUnknownConfig(t *testing.T) {
	t.Parallel()

	connector := NewPostgresConnector(&SourceService{})
	base := SourceConfigRequest{
		ConfigProvided:     true,
		RawCredential:      []byte(`{"password":"secret"}`),
		CredentialProvided: true,
		RequireCredential:  true,
		AuthSecret:         "12345678901234567890123456789012",
	}
	for _, raw := range []string{
		`{"host":"db.example.com","port":5432,"database_name":"analytics","username":"reader","allowlist":[{"schema":"public","name":"orders","kind":"table"}]}`,
		`{"driver":"postgres","host":"db.example.com","port":5432,"database_name":"analytics","ssl_mode":"disable","username":"reader","allowlist":[{"schema":"public","name":"orders","kind":"table"}]}`,
		`{"host":"db.example.com","port":5432,"database_name":"analytics","ssl_mode":"disable","username":"reader","allowlist":[{"schema":"","name":"orders","kind":"table"}]}`,
	} {
		request := base
		request.RawConfig = []byte(raw)
		if _, err := connector.NormalizeConfig(context.Background(), request); err == nil {
			t.Fatalf("expected config to be rejected: %s", raw)
		}
	}
}
