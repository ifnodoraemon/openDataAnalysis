package config

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// LLM 配置
	LLMProvider        string // openai, anthropic, or google
	LLMAPIProtocol     string // responses or chat_completions for openai-compatible providers
	LLMBaseURL         string
	LLMAPIEndpoint     string
	LLMAPIKey          string
	LLMModel           string
	LLMReasoningEffort string
	LLMTextVerbosity   string
	LLMMaxTokens       int
	LLMHTTPTimeoutSec  int
	LLMRetryBudgetSec  int
	LLMDebug           bool
	LLMDebugDir        string

	// 服务配置
	ServerPort               string
	DeploymentMode           string
	AllowedOrigins           []string
	MetadataStore            string
	StorageProvider          string
	RunBackend               string
	AnalysisStore            string
	PythonArtifactStore      string
	StorageRoot              string
	CacheRoot                string
	MetadataDBPath           string
	TempDir                  string
	PythonMCPURL             string
	PythonMaxTimeoutSec      int
	ProxyToken               string
	PublicAPIBaseURL         string
	AuthSecret               string
	AuthCookieSecure         bool
	TrustedProxyCIDRs        []*net.IPNet
	DatasourceCredentialSecret string
	DefaultUserID            string
	DefaultUserEmail         string
	DefaultUserName          string
	DefaultUserPassword      string
	DefaultWorkspaceID       string
	DefaultWorkspaceName     string
	BootstrapDefaultIdentity bool

	// 生命周期管理
	SessionTTLHours    int    // 空闲 session 自动清理阈值（小时），0 = 不自动清理
	TraceRetentionDays int    // LLM debug trace 保留天数，0 = 永久保留
	TempCleanupOnStart bool   // 启动时清理 TempDir
	ReportEchartsUrl   string // ECharts 资源路径，默认为前端自托管静态资源
	MetricsExpose      bool   // 是否公开 /metrics 端点，默认关闭（404）
	MetricsAuthToken   string // /metrics 抓取所需的 Bearer token；为空时端点不鉴权（生产模式会校验失败）

	// 数据源导入

	// PostgreSQL配置
	PostgresDSN string

	// S3配置
	S3Endpoint       string
	S3Region         string
	S3Bucket         string
	S3AccessKey      string
	S3SecretKey      string
	S3ForcePathStyle bool
}

type productionBackendRule struct {
	Value           string
	DevelopmentOnly string
	Issue           string
}

var Cfg *Config

func Load() {
	err := godotenv.Load()
	if err != nil {
		if _, statErr := os.Stat(".env"); statErr == nil {
			log.Printf("Warning: failed to load .env: %v", err)
		}
	}

	provider := getEnv("LLM_PROVIDER", "")
	deploymentMode := getEnv("DEPLOYMENT_MODE", "development")
	baseURL := getEnv("LLM_BASE_URL", "")
	apiProtocol := getEnv("LLM_API_PROTOCOL", "")

	Cfg = &Config{
		LLMProvider:              provider,
		LLMAPIProtocol:           apiProtocol,
		LLMBaseURL:               baseURL,
		LLMAPIEndpoint:           getEnv("LLM_API_ENDPOINT", ""),
		LLMAPIKey:                getEnv("LLM_API_KEY", ""),
		LLMModel:                 getEnv("LLM_MODEL", ""),
		LLMReasoningEffort:       getEnv("LLM_REASONING_EFFORT", ""),
		LLMTextVerbosity:         getEnv("LLM_TEXT_VERBOSITY", ""),
		LLMMaxTokens:             getEnvInt("LLM_MAX_TOKENS", 0),
		LLMHTTPTimeoutSec:        getEnvInt("LLM_HTTP_TIMEOUT_SECONDS", 240),
		LLMRetryBudgetSec:        getEnvInt("LLM_RETRY_BUDGET_SECONDS", 360),
		LLMDebug:                 getEnvBool("LLM_DEBUG", false),
		LLMDebugDir:              getEnv("LLM_DEBUG_DIR", "./data/llm-debug"),
		ServerPort:               getEnv("SERVER_PORT", "8080"),
		DeploymentMode:           deploymentMode,
		AllowedOrigins:           getEnvList("CORS_ALLOWED_ORIGINS", defaultAllowedOrigins()),
		MetadataStore:            getEnv("METADATA_STORE", "sqlite"),
		StorageProvider:          getEnv("STORAGE_PROVIDER", "local"),
		RunBackend:               getEnv("RUN_BACKEND", "inprocess"),
		AnalysisStore:            getEnv("ANALYSIS_STORE", "session_sqlite"),
		PythonArtifactStore:      getEnv("PYTHON_ARTIFACT_STORE", "object_storage"),
		StorageRoot:              getEnv("STORAGE_ROOT", "./data/storage"),
		CacheRoot:                getEnv("CACHE_ROOT", "./data/cache"),
		MetadataDBPath:           getEnv("METADATA_DB_PATH", "./data/metadata/app.db"),
		TempDir:                  getEnv("TEMP_DIR", "./data/tmp"),
		PythonMCPURL:             getEnv("PYTHON_MCP_URL", "http://python-executor:8081"),
		PythonMaxTimeoutSec:      getEnvInt("PYTHON_MAX_TIMEOUT_SECONDS", 120),
		ProxyToken:               getEnv("PROXY_TOKEN", ""),
		PublicAPIBaseURL:         getEnv("PUBLIC_API_BASE_URL", ""),
		AuthSecret:               getEnv("AUTH_SECRET", ""),
		AuthCookieSecure:         getEnvBool("AUTH_COOKIE_SECURE", true),
		TrustedProxyCIDRs:        parseTrustedProxyCIDRs(getEnvList("TRUSTED_PROXY_CIDRS", nil)),
		DatasourceCredentialSecret: getEnv("DATASOURCE_CREDENTIAL_SECRET", ""),
		DefaultUserID:            getEnv("DEFAULT_USER_ID", ""),
		DefaultUserEmail:         getEnv("DEFAULT_USER_EMAIL", ""),
		DefaultUserName:          getEnv("DEFAULT_USER_NAME", ""),
		DefaultUserPassword:      getEnv("DEFAULT_USER_PASSWORD", ""),
		DefaultWorkspaceID:       getEnv("DEFAULT_WORKSPACE_ID", ""),
		DefaultWorkspaceName:     getEnv("DEFAULT_WORKSPACE_NAME", ""),
		BootstrapDefaultIdentity: getEnvBool("BOOTSTRAP_DEFAULT_IDENTITY", false),

		SessionTTLHours:    getEnvInt("SESSION_TTL_HOURS", 0),
		TraceRetentionDays: getEnvInt("TRACE_RETENTION_DAYS", 0),
		TempCleanupOnStart: getEnvBool("TEMP_CLEANUP_ON_START", false),
		ReportEchartsUrl:   getEnv("REPORT_ECHARTS_URL", "/assets/echarts.min.js"),
		MetricsExpose:      getEnvBool("METRICS_EXPOSE", false),
		MetricsAuthToken:   getEnv("METRICS_AUTH_TOKEN", ""),

		PostgresDSN:      getEnv("POSTGRES_DSN", ""),
		S3Endpoint:       getEnv("S3_ENDPOINT", ""),
		S3Region:         getEnv("S3_REGION", "us-east-1"),
		S3Bucket:         getEnv("S3_BUCKET", ""),
		S3AccessKey:      getEnv("S3_ACCESS_KEY", ""),
		S3SecretKey:      getEnv("S3_SECRET_KEY", ""),
		S3ForcePathStyle: getEnvBool("S3_FORCE_PATH_STYLE", true),
	}

	if Cfg.ReportEchartsUrl != "" && !trustedReportScriptURL(Cfg.ReportEchartsUrl) {
		panic("REPORT_ECHARTS_URL must be an exact same-origin path or an allowed ECharts HTTPS URL")
	}

	log.Printf("config loaded mode=%s metadata_store=%s storage_provider=%s run_backend=%s analysis_store=%s llm_provider=%s llm_protocol=%s llm_model=%s llm_endpoint=%s",
		Cfg.DeploymentMode,
		Cfg.MetadataStore,
		Cfg.StorageProvider,
		Cfg.RunBackend,
		Cfg.AnalysisStore,
		Cfg.LLMProvider,
		Cfg.LLMAPIProtocol,
		Cfg.LLMModel,
		Cfg.LLMAPIEndpoint,
	)
}

func (c *Config) IsProduction() bool {
	if c == nil {
		return false
	}
	return c.DeploymentMode == "production"
}

func (c *Config) ValidateProductionReadiness() error {
	if c == nil {
		return fmt.Errorf("configuration is not initialized")
	}
	switch c.DeploymentMode {
	case "development", "test", "production":
	default:
		return fmt.Errorf("DEPLOYMENT_MODE must be development, test, or production")
	}
	switch c.LLMProvider {
	case "openai":
		if c.LLMAPIProtocol != "responses" && c.LLMAPIProtocol != "chat_completions" {
			return fmt.Errorf("LLM_API_PROTOCOL must be responses or chat_completions when LLM_PROVIDER=openai")
		}
	case "anthropic":
		if c.LLMAPIProtocol != "messages" {
			return fmt.Errorf("LLM_API_PROTOCOL must be messages when LLM_PROVIDER=anthropic")
		}
	case "google":
		if c.LLMAPIProtocol != "generate_content" {
			return fmt.Errorf("LLM_API_PROTOCOL must be generate_content when LLM_PROVIDER=google")
		}
	default:
		return fmt.Errorf("LLM_PROVIDER must be openai, anthropic, or google")
	}
	for field, value := range map[string]string{
		"LLM_MODEL":   c.LLMModel,
		"LLM_API_KEY": c.LLMAPIKey,
		"AUTH_SECRET": c.AuthSecret,
	} {
		if err := validateExactConfigValue(field, value, true); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"LLM_REASONING_EFFORT": c.LLMReasoningEffort,
		"LLM_TEXT_VERBOSITY":   c.LLMTextVerbosity,
		"PYTHON_MCP_URL":       c.PythonMCPURL,
		"PROXY_TOKEN":          c.ProxyToken,
		"PUBLIC_API_BASE_URL":  c.PublicAPIBaseURL,
		"REPORT_ECHARTS_URL":   c.ReportEchartsUrl,
	} {
		if err := validateExactConfigValue(field, value, false); err != nil {
			return err
		}
	}
	if c.LLMProvider == "openai" || c.LLMProvider == "google" {
		if err := validateExactConfigValue("LLM_API_ENDPOINT", c.LLMAPIEndpoint, true); err != nil {
			return err
		}
	}
	if len(c.AuthSecret) < 32 {
		return fmt.Errorf("AUTH_SECRET must contain at least 32 bytes")
	}
	if c.PythonMaxTimeoutSec < 5 {
		return fmt.Errorf("PYTHON_MAX_TIMEOUT_SECONDS must be at least 5")
	}
	if c.LLMHTTPTimeoutSec <= 0 || c.LLMRetryBudgetSec <= 0 {
		return fmt.Errorf("LLM_HTTP_TIMEOUT_SECONDS and LLM_RETRY_BUDGET_SECONDS must be positive")
	}
	if c.LLMMaxTokens < 0 {
		return fmt.Errorf("LLM_MAX_TOKENS must not be negative")
	}
	if c.LLMProvider == "anthropic" {
		if err := validateExactConfigValue("LLM_BASE_URL", c.LLMBaseURL, true); err != nil {
			return err
		}
	}
	for _, origin := range c.AllowedOrigins {
		if err := validateExactConfigValue("CORS_ALLOWED_ORIGINS entry", origin, true); err != nil {
			return err
		}
	}
	if c.ReportEchartsUrl != "" && !trustedReportScriptURL(c.ReportEchartsUrl) {
		return fmt.Errorf("REPORT_ECHARTS_URL is not trusted")
	}
	if !c.IsProduction() {
		return nil
	}

	var issues []string
	for _, rule := range c.productionBackendRules() {
		if rule.Value == rule.DevelopmentOnly {
			issues = append(issues, rule.Issue)
		}
	}
	if len(c.AllowedOrigins) == 0 || containsWildcard(c.AllowedOrigins) || containsLocalOrigin(c.AllowedOrigins) {
		issues = append(issues, "CORS_ALLOWED_ORIGINS must be explicit production origins; wildcard and localhost origins are not allowed")
	}
	if c.BootstrapDefaultIdentity {
		issues = append(issues, "BOOTSTRAP_DEFAULT_IDENTITY must be false in production; provision users and workspaces through managed identity flows")
	}
	if c.MetadataStore == "postgres" && strings.TrimSpace(c.PostgresDSN) == "" {
		issues = append(issues, "POSTGRES_DSN must be configured when METADATA_STORE=postgres")
	}
	if c.StorageProvider == "s3" {
		if strings.TrimSpace(c.S3Endpoint) == "" || strings.TrimSpace(c.S3Bucket) == "" || strings.TrimSpace(c.S3AccessKey) == "" || strings.TrimSpace(c.S3SecretKey) == "" {
			issues = append(issues, "S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, and S3_SECRET_KEY must be configured when STORAGE_PROVIDER=s3")
		}
	}
	if c.MetricsExpose && strings.TrimSpace(c.MetricsAuthToken) == "" {
		issues = append(issues, "METRICS_AUTH_TOKEN must be configured when METRICS_EXPOSE=true; unauthenticated metrics endpoints leak operational data")
	}
	if !c.AuthCookieSecure {
		issues = append(issues, "AUTH_COOKIE_SECURE must be true in production; the auth cookie must only be sent over HTTPS")
	}
	if len(issues) > 0 {
		return fmt.Errorf("production deployment is not ready:\n- %s", strings.Join(issues, "\n- "))
	}
	return nil
}

func validateExactConfigValue(field, value string, required bool) error {
	if strings.TrimSpace(value) == "" {
		if required {
			return fmt.Errorf("%s must be configured", field)
		}
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain leading or trailing whitespace", field)
	}
	return nil
}

func (c *Config) productionBackendRules() []productionBackendRule {
	return []productionBackendRule{
		{
			Value:           c.MetadataStore,
			DevelopmentOnly: "sqlite",
			Issue:           "METADATA_STORE=sqlite is development-only; production needs a shared metadata store such as postgres",
		},
		{
			Value:           c.StorageProvider,
			DevelopmentOnly: "local",
			Issue:           "STORAGE_PROVIDER=local is development-only; production needs object storage such as s3",
		},
		{
			Value:           c.RunBackend,
			DevelopmentOnly: "inprocess",
			Issue:           "RUN_BACKEND=inprocess is single-server only; this binary does not yet ship a durable distributed run backend",
		},
		{
			Value:           c.AnalysisStore,
			DevelopmentOnly: "session_sqlite",
			Issue:           "ANALYSIS_STORE=session_sqlite is local scratch state; production needs durable snapshot ownership and worker recovery",
		},
	}
}

func (c *Config) IsOriginAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	if c == nil {
		return false
	}
	for _, allowed := range c.AllowedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

func trustedReportScriptURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed != raw {
		return false
	}
	// Report chart/math runtimes load from same-origin static assets only.
	// Third-party CDN URLs are rejected so report rendering never depends on
	// (or trusts) external script origins.
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
		return true
	}
	return false
}

func parseTrustedProxyCIDRs(values []string) []*net.IPNet {
	cidrs := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, parsed, err := net.ParseCIDR(value)
		if err != nil {
			panic(fmt.Sprintf("TRUSTED_PROXY_CIDRS entry %q is not a valid CIDR", value))
		}
		cidrs = append(cidrs, parsed)
	}
	return cidrs
}

func getEnv(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func getEnvList(key string, defaultValue []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return append([]string(nil), defaultValue...)
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			panic(fmt.Sprintf("%s entries must be non-empty exact values", key))
		}
		values = append(values, part)
	}
	return values
}

func defaultAllowedOrigins() []string {
	return []string{
		"http://localhost",
		"http://localhost:5173",
		"http://127.0.0.1",
		"http://127.0.0.1:5173",
	}
}

func containsWildcard(values []string) bool {
	for _, value := range values {
		if value == "*" {
			return true
		}
	}
	return false
}

func containsLocalOrigin(values []string) bool {
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return true
		}
	}
	return false
}

func getEnvBool(key string, defaultValue bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}
	switch value {
	case "true":
		return true
	case "false":
		return false
	default:
		panic(fmt.Sprintf("%s must be exactly true or false", key))
	}
}

func getEnvInt(key string, defaultValue int) int {
	value, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}
	result, err := strconv.Atoi(value)
	if err != nil {
		panic(fmt.Sprintf("%s must be an integer", key))
	}
	return result
}
