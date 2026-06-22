package config

import (
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
	StorageRoot          string
	CacheRoot            string
	MetadataDBPath       string
	TempDir              string
	PythonMCPURL         string
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
	PostgresImportRowLimit int // PostgreSQL snapshot import row cap, 0 = unlimited
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
		StorageRoot:          getEnv("STORAGE_ROOT", "./data/storage"),
		CacheRoot:            getEnv("CACHE_ROOT", "./data/cache"),
		MetadataDBPath:       getEnv("METADATA_DB_PATH", "./data/metadata/app.db"),
		TempDir:              getEnv("TEMP_DIR", "./data/tmp"),
		PythonMCPURL:         getEnv("PYTHON_MCP_URL", ""),
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

		PostgresImportRowLimit: getEnvInt("POSTGRES_IMPORT_ROW_LIMIT", 1000000),
	}

	if Cfg.LLMAPIKey == "" {
		log.Println("Warning: LLM_API_KEY is not set")
	}

	if Cfg.AuthSecret == "" || Cfg.AuthSecret == "replace-with-a-long-random-secret" {
		log.Println("CRITICAL: AUTH_SECRET is not set or uses the default placeholder. Tokens may be forgeable. Set a strong random secret.")
	}

	if len(Cfg.AuthSecret) < 32 {
		log.Printf("Warning: AUTH_SECRET is too short (%d chars). Recommend at least 32 characters.", len(Cfg.AuthSecret))
	}

	if Cfg.ReportEchartsUrl != "" && !trustedReportScriptURL(Cfg.ReportEchartsUrl) {
		log.Printf("Warning: REPORT_ECHARTS_URL is not same-origin or an allowed ECharts CDN, ignoring: %s", Cfg.ReportEchartsUrl)
		Cfg.ReportEchartsUrl = ""
	}

	log.Printf("config loaded llm_provider=%s llm_model=%s llm_endpoint=%s", Cfg.LLMProvider, Cfg.LLMModel, Cfg.LLMAPIEndpoint)
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
