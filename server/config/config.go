package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// LLM 配置
	LLMProvider        string // "openai" 或 "anthropic"
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
	ServerPort           string
	DeploymentMode       string
	AllowedOrigins       []string
	MetadataStore        string
	StorageProvider      string
	RunBackend           string
	AnalysisStore        string
	PythonArtifactStore  string
	StorageRoot          string
	CacheRoot            string
	MetadataDBPath       string
	TempDir              string
	PythonMCPURL         string
	ProxyToken           string
	PublicAPIBaseURL     string
	AuthSecret           string
	DefaultUserID        string
	DefaultUserEmail     string
	DefaultUserName      string
	DefaultUserPassword  string
	DefaultWorkspaceID   string
	DefaultWorkspaceName string

	// 生命周期管理
	SessionTTLHours    int    // 空闲 session 自动清理阈值（小时），0 = 不自动清理
	TraceRetentionDays int    // LLM debug trace 保留天数，0 = 永久保留
	TempCleanupOnStart bool   // 启动时清理 TempDir
	ReportEchartsUrl   string // ECharts 资源路径，默认为前端自托管静态资源

	// 数据源导入
	SQLImportRowLimit int // SQL snapshot import row cap, 0 = unlimited

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

	provider := strings.ToLower(getEnv("LLM_PROVIDER", "openai"))

	// 根据 Provider 设置默认值
	defaultBaseURL := "https://api.openai.com"
	defaultModel := "gpt-4o"
	if provider == "anthropic" {
		defaultBaseURL = "https://api.anthropic.com"
		defaultModel = "claude-sonnet-4-20250514"
	}
	baseURL := getEnv("LLM_BASE_URL", defaultBaseURL)
	defaultAPIEndpoint := defaultLLMAPIEndpoint(provider, baseURL)

	Cfg = &Config{
		LLMProvider:          provider,
		LLMBaseURL:           baseURL,
		LLMAPIEndpoint:       getEnv("LLM_API_ENDPOINT", defaultAPIEndpoint),
		LLMAPIKey:            getEnv("LLM_API_KEY", ""),
		LLMModel:             getEnv("LLM_MODEL", defaultModel),
		LLMReasoningEffort:   getEnv("LLM_REASONING_EFFORT", ""),
		LLMTextVerbosity:     getEnv("LLM_TEXT_VERBOSITY", ""),
		LLMMaxTokens:         getEnvInt("LLM_MAX_TOKENS", 0),
		LLMHTTPTimeoutSec:    getEnvInt("LLM_HTTP_TIMEOUT_SECONDS", 240),
		LLMRetryBudgetSec:    getEnvInt("LLM_RETRY_BUDGET_SECONDS", 360),
		LLMDebug:             getEnvBool("LLM_DEBUG", false),
		LLMDebugDir:          getEnv("LLM_DEBUG_DIR", "./data/llm-debug"),
		ServerPort:           getEnv("SERVER_PORT", "8080"),
		DeploymentMode:       normalizeMode(getEnv("DEPLOYMENT_MODE", "development")),
		AllowedOrigins:       getEnvList("CORS_ALLOWED_ORIGINS", defaultAllowedOrigins()),
		MetadataStore:        NormalizeBackend(getEnv("METADATA_STORE", "sqlite")),
		StorageProvider:      NormalizeBackend(getEnv("STORAGE_PROVIDER", "local")),
		RunBackend:           NormalizeBackend(getEnv("RUN_BACKEND", "inprocess")),
		AnalysisStore:        NormalizeBackend(getEnv("ANALYSIS_STORE", "session_sqlite")),
		PythonArtifactStore:  NormalizeBackend(getEnv("PYTHON_ARTIFACT_STORE", "executor_local")),
		StorageRoot:          getEnv("STORAGE_ROOT", "./data/storage"),
		CacheRoot:            getEnv("CACHE_ROOT", "./data/cache"),
		MetadataDBPath:       getEnv("METADATA_DB_PATH", "./data/metadata/app.db"),
		TempDir:              getEnv("TEMP_DIR", "./data/tmp"),
		PythonMCPURL:         getEnv("PYTHON_MCP_URL", ""),
		ProxyToken:           getEnv("PROXY_TOKEN", ""),
		PublicAPIBaseURL:     getEnv("PUBLIC_API_BASE_URL", getEnv("API_BASE_URL", "")),
		AuthSecret:           getEnv("AUTH_SECRET", ""),
		DefaultUserID:        getEnv("DEFAULT_USER_ID", ""),
		DefaultUserEmail:     getEnv("DEFAULT_USER_EMAIL", ""),
		DefaultUserName:      getEnv("DEFAULT_USER_NAME", ""),
		DefaultUserPassword:  getEnv("DEFAULT_USER_PASSWORD", ""),
		DefaultWorkspaceID:   getEnv("DEFAULT_WORKSPACE_ID", ""),
		DefaultWorkspaceName: getEnv("DEFAULT_WORKSPACE_NAME", ""),

		SessionTTLHours:    getEnvInt("SESSION_TTL_HOURS", 0),
		TraceRetentionDays: getEnvInt("TRACE_RETENTION_DAYS", 0),
		TempCleanupOnStart: getEnvBool("TEMP_CLEANUP_ON_START", false),
		ReportEchartsUrl:   getEnv("REPORT_ECHARTS_URL", "/assets/echarts.min.js"),

		SQLImportRowLimit: getEnvInt("SQL_IMPORT_ROW_LIMIT", 1000000),

		PostgresDSN:      getEnv("POSTGRES_DSN", "postgres://oda:password@localhost:5432/oda?sslmode=disable"),
		S3Endpoint:       getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3Region:         getEnv("S3_REGION", "us-east-1"),
		S3Bucket:         getEnv("S3_BUCKET", "oda-storage"),
		S3AccessKey:      getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:      getEnv("S3_SECRET_KEY", "minioadmin"),
		S3ForcePathStyle: getEnvBool("S3_FORCE_PATH_STYLE", true),
	}

	if Cfg.LLMAPIKey == "" || IsPlaceholderValue(Cfg.LLMAPIKey) {
		log.Println("Warning: LLM_API_KEY is not set or uses a placeholder")
	}

	if Cfg.AuthSecret == "" || IsPlaceholderValue(Cfg.AuthSecret) {
		log.Println("CRITICAL: AUTH_SECRET is not set or uses the default placeholder. Tokens may be forgeable. Set a strong random secret.")
	}

	if len(Cfg.AuthSecret) < 32 {
		log.Printf("Warning: AUTH_SECRET is too short (%d chars). Recommend at least 32 characters.", len(Cfg.AuthSecret))
	}

	if Cfg.ReportEchartsUrl != "" && !trustedReportScriptURL(Cfg.ReportEchartsUrl) {
		log.Printf("Warning: REPORT_ECHARTS_URL is not same-origin or an allowed ECharts CDN, ignoring: %s", Cfg.ReportEchartsUrl)
		Cfg.ReportEchartsUrl = ""
	}

	log.Printf("config loaded mode=%s metadata_store=%s storage_provider=%s run_backend=%s analysis_store=%s llm_provider=%s llm_model=%s llm_endpoint=%s",
		Cfg.DeploymentMode,
		Cfg.MetadataStore,
		Cfg.StorageProvider,
		Cfg.RunBackend,
		Cfg.AnalysisStore,
		Cfg.LLMProvider,
		Cfg.LLMModel,
		Cfg.LLMAPIEndpoint,
	)
}

func (c *Config) IsProduction() bool {
	if c == nil {
		return false
	}
	return normalizeMode(c.DeploymentMode) == "production"
}

func (c *Config) ValidateProductionReadiness() error {
	if c == nil || !c.IsProduction() {
		return nil
	}

	var issues []string
	for _, rule := range c.productionBackendRules() {
		if NormalizeBackend(rule.Value) == rule.DevelopmentOnly {
			issues = append(issues, rule.Issue)
		}
	}
	if len(c.AllowedOrigins) == 0 || containsWildcard(c.AllowedOrigins) || containsLocalOrigin(c.AllowedOrigins) {
		issues = append(issues, "CORS_ALLOWED_ORIGINS must be explicit production origins; wildcard and localhost origins are not allowed")
	}
	issues = append(issues, "DEFAULT_USER_* bootstrap is development-only; production needs managed user/workspace provisioning")
	if len(issues) > 0 {
		return fmt.Errorf("production deployment is not ready:\n- %s", strings.Join(issues, "\n- "))
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
			Issue:           "RUN_BACKEND=inprocess is single-server only; production needs a durable run/job backend such as river",
		},
		{
			Value:           c.AnalysisStore,
			DevelopmentOnly: "session_sqlite",
			Issue:           "ANALYSIS_STORE=session_sqlite is local scratch state; production needs durable snapshot ownership and worker recovery",
		},
		{
			Value:           c.PythonArtifactStore,
			DevelopmentOnly: "executor_local",
			Issue:           "PYTHON_ARTIFACT_STORE=executor_local cannot survive executor restart or multiple executors",
		},
	}
}

func (c *Config) IsOriginAllowed(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return true
	}
	if c == nil {
		return false
	}
	allowedOrigins := c.AllowedOrigins
	if len(allowedOrigins) == 0 {
		allowedOrigins = defaultAllowedOrigins()
	}
	for _, allowed := range allowedOrigins {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func defaultLLMAPIEndpoint(provider, baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return ""
	}
	if provider == "anthropic" {
		return trimmed + "/v1/messages"
	}
	host := ""
	if parsed, err := url.Parse(trimmed); err == nil {
		host = strings.ToLower(parsed.Hostname())
	}
	if host == "api.openai.com" {
		return trimmed + "/v1/responses"
	}
	if host == "api.deepseek.com" || strings.HasSuffix(host, ".deepseek.com") {
		return trimmed + "/chat/completions"
	}
	if strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		return trimmed + "/chat/completions"
	}
	return trimmed + "/v1/chat/completions"
}

func trustedReportScriptURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "cdn.jsdelivr.net", "cdnjs.cloudflare.com":
		return strings.HasSuffix(strings.ToLower(parsed.Path), "echarts.min.js")
	default:
		return false
	}
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
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
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

func normalizeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production":
		return "production"
	case "test", "testing":
		return "test"
	default:
		return "development"
	}
}

func NormalizeBackend(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func IsPlaceholderValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	collapsed := strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	return strings.HasPrefix(normalized, "change_me") ||
		strings.HasPrefix(normalized, "change-me") ||
		strings.HasPrefix(collapsed, "changeme") ||
		strings.HasPrefix(collapsed, "replacewith") ||
		normalized == "placeholder" ||
		normalized == "password" ||
		normalized == "admin"
}

func containsWildcard(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "*" {
			return true
		}
	}
	return false
}

func containsLocalOrigin(values []string) bool {
	for _, value := range values {
		parsed, err := url.Parse(strings.TrimSpace(value))
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
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func getEnvInt(key string, defaultValue int) int {
	value, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue
	}
	result, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return defaultValue
	}
	return result
}
