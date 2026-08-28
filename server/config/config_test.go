package config

import (
	"os"
	"strings"
	"testing"
)

func TestTrustedReportScriptURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js", false},
		{"https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js", true},
		{"https://cdnjs.cloudflare.com/ajax/libs/echarts/5.5.0/echarts.min.js", true},
		{"https://evil.com/echarts.min.js", false},
		{"/assets/echarts.min.js", true},
		{" /assets/echarts.min.js", false},
		{"", false},
		{"//cdn.jsdelivr.net/npm/echarts/dist/echarts.min.js", false},
	}

	for _, tt := range tests {
		t.Run("url="+tt.url, func(t *testing.T) {
			got := trustedReportScriptURL(tt.url)
			if got != tt.want {
				t.Errorf("trustedReportScriptURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestNilConfigurationFailsClosed(t *testing.T) {
	t.Parallel()

	var cfg *Config
	if err := cfg.ValidateProductionReadiness(); err == nil {
		t.Fatal("expected nil configuration to fail validation")
	}
}

func TestLoadLLMTimeoutConfig(t *testing.T) {
	previous := Cfg
	defer func() { Cfg = previous }()

	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("LLM_API_PROTOCOL", "responses")
	t.Setenv("LLM_API_ENDPOINT", "https://api.example.com/v1/responses")
	t.Setenv("LLM_MODEL", "model-id")
	t.Setenv("AUTH_SECRET", "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("LLM_HTTP_TIMEOUT_SECONDS", "321")
	t.Setenv("LLM_RETRY_BUDGET_SECONDS", "654")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com,https://admin.example.com")
	t.Setenv("PROXY_TOKEN", "proxy-token")
	t.Setenv("PUBLIC_API_BASE_URL", "https://analysis.example.com")

	Load()

	if Cfg.LLMHTTPTimeoutSec != 321 {
		t.Fatalf("expected LLMHTTPTimeoutSec=321, got %d", Cfg.LLMHTTPTimeoutSec)
	}
	if Cfg.LLMRetryBudgetSec != 654 {
		t.Fatalf("expected LLMRetryBudgetSec=654, got %d", Cfg.LLMRetryBudgetSec)
	}
	if Cfg.DeploymentMode != "development" {
		t.Fatalf("expected development mode by default, got %q", Cfg.DeploymentMode)
	}
	if len(Cfg.AllowedOrigins) != 2 || Cfg.AllowedOrigins[0] != "https://app.example.com" || Cfg.AllowedOrigins[1] != "https://admin.example.com" {
		t.Fatalf("unexpected allowed origins: %#v", Cfg.AllowedOrigins)
	}
	if Cfg.ProxyToken != "proxy-token" {
		t.Fatalf("expected proxy token to load from env")
	}
	if Cfg.PublicAPIBaseURL != "https://analysis.example.com" {
		t.Fatalf("unexpected public api base url: %q", Cfg.PublicAPIBaseURL)
	}
}

func TestIsOriginAllowed(t *testing.T) {
	cfg := &Config{
		AllowedOrigins: []string{"https://app.example.com", "http://localhost:5173"},
	}

	if !cfg.IsOriginAllowed("https://app.example.com") {
		t.Fatal("expected configured origin to be allowed")
	}
	if !cfg.IsOriginAllowed("") {
		t.Fatal("expected empty origin to be allowed for non-browser/internal requests")
	}
	if cfg.IsOriginAllowed("https://evil.example.com") {
		t.Fatal("did not expect unknown origin to be allowed")
	}
}

func TestProductionReadinessRejectsDevelopmentBackends(t *testing.T) {
	cfg := &Config{
		DeploymentMode:           "production",
		LLMProvider:              "openai",
		LLMAPIProtocol:           "responses",
		LLMAPIEndpoint:           "https://api.example.com/v1/responses",
		LLMAPIKey:                "provider-key",
		LLMModel:                 "model-id",
		LLMHTTPTimeoutSec:        240,
		LLMRetryBudgetSec:        360,
		AllowedOrigins:           []string{"http://localhost:5173"},
		MetadataStore:            "sqlite",
		StorageProvider:          "local",
		RunBackend:               "inprocess",
		AnalysisStore:            "session_sqlite",
		PythonArtifactStore:      "object_storage",
		BootstrapDefaultIdentity: true,
		AuthSecret:               "abcdefghijklmnopqrstuvwxyz123456",
		PythonMaxTimeoutSec:      120,
	}

	err := cfg.ValidateProductionReadiness()
	if err == nil {
		t.Fatal("expected production readiness validation to fail")
	}
	for _, want := range []string{
		"METADATA_STORE=sqlite",
		"STORAGE_PROVIDER=local",
		"RUN_BACKEND=inprocess",
		"ANALYSIS_STORE=session_sqlite",
		"CORS_ALLOWED_ORIGINS",
		"BOOTSTRAP_DEFAULT_IDENTITY",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected production readiness error to contain %q, got %v", want, err)
		}
	}
}

func TestProductionReadinessAllowsDevelopmentMode(t *testing.T) {
	cfg := &Config{
		DeploymentMode:      "development",
		LLMProvider:         "openai",
		LLMAPIProtocol:      "responses",
		LLMAPIEndpoint:      "https://api.example.com/v1/responses",
		LLMAPIKey:           "provider-key",
		LLMModel:            "model-id",
		LLMHTTPTimeoutSec:   240,
		LLMRetryBudgetSec:   360,
		AllowedOrigins:      []string{"http://localhost:5173"},
		MetadataStore:       "sqlite",
		StorageProvider:     "local",
		RunBackend:          "inprocess",
		AnalysisStore:       "session_sqlite",
		PythonArtifactStore: "object_storage",
		AuthSecret:          "abcdefghijklmnopqrstuvwxyz123456",
		PythonMaxTimeoutSec: 120,
	}

	if err := cfg.ValidateProductionReadiness(); err != nil {
		t.Fatalf("development mode should allow local defaults: %v", err)
	}
}

func TestRuntimeConfigurationRejectsAliasesAndImplicitProtocols(t *testing.T) {
	for _, cfg := range []*Config{
		{DeploymentMode: "prod", LLMProvider: "openai", LLMAPIProtocol: "responses", LLMAPIEndpoint: "https://api.example.com/v1/responses", LLMAPIKey: "provider-key", LLMModel: "model-id", AuthSecret: "abcdefghijklmnopqrstuvwxyz123456", PythonMaxTimeoutSec: 120, LLMHTTPTimeoutSec: 240, LLMRetryBudgetSec: 360},
		{DeploymentMode: "development", LLMProvider: "openai", LLMAPIEndpoint: "https://api.example.com/v1/responses", LLMAPIKey: "provider-key", LLMModel: "model-id", AuthSecret: "abcdefghijklmnopqrstuvwxyz123456", PythonMaxTimeoutSec: 120, LLMHTTPTimeoutSec: 240, LLMRetryBudgetSec: 360},
		{DeploymentMode: "development", LLMProvider: "OpenAI", LLMAPIProtocol: "responses", LLMAPIEndpoint: "https://api.example.com/v1/responses", LLMAPIKey: "provider-key", LLMModel: "model-id", AuthSecret: "abcdefghijklmnopqrstuvwxyz123456", PythonMaxTimeoutSec: 120, LLMHTTPTimeoutSec: 240, LLMRetryBudgetSec: 360},
	} {
		if err := cfg.ValidateProductionReadiness(); err == nil {
			t.Fatalf("expected explicit configuration contract to reject %#v", cfg)
		}
	}
}

func TestGetEnv(t *testing.T) {
	os.Unsetenv("TEST_GETENV_KEY")

	got := getEnv("TEST_GETENV_KEY", "default")
	if got != "default" {
		t.Errorf("expected default, got %q", got)
	}

	os.Setenv("TEST_GETENV_KEY", "custom")
	defer os.Unsetenv("TEST_GETENV_KEY")

	got = getEnv("TEST_GETENV_KEY", "default")
	if got != "custom" {
		t.Errorf("expected custom, got %q", got)
	}
}

func TestGetEnvBool(t *testing.T) {
	os.Unsetenv("TEST_BOOL_KEY")

	if getEnvBool("TEST_BOOL_KEY", true) != true {
		t.Error("expected default true")
	}

	t.Setenv("TEST_BOOL_KEY", "true")
	if !getEnvBool("TEST_BOOL_KEY", false) {
		t.Error("expected exact true")
	}

	t.Setenv("TEST_BOOL_KEY", "false")
	if getEnvBool("TEST_BOOL_KEY", true) {
		t.Error("expected exact false")
	}

	t.Setenv("TEST_BOOL_KEY", "yes")
	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid boolean to fail closed")
		}
	}()
	_ = getEnvBool("TEST_BOOL_KEY", false)

}

func TestGetEnvInt(t *testing.T) {
	os.Unsetenv("TEST_INT_KEY")

	if getEnvInt("TEST_INT_KEY", 42) != 42 {
		t.Error("expected default 42")
	}

	t.Setenv("TEST_INT_KEY", "100")

	if getEnvInt("TEST_INT_KEY", 42) != 100 {
		t.Error("expected 100")
	}

	t.Setenv("TEST_INT_KEY", "not-a-number")
	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid integer to fail closed")
		}
	}()
	_ = getEnvInt("TEST_INT_KEY", 42)
}
