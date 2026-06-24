package tools

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

// RunPythonTool 通过 MCP 服务执行 Python 代码
type RunPythonTool struct {
	MCPEndpoint string // Python MCP 服务地址，如 http://python-executor:8081
	childCtx    context.Context
}

var pythonHealthCache = struct {
	sync.Mutex
	key       string
	checkedAt time.Time
	err       error
}{}

const pythonHealthCacheTTL = 30 * time.Second
const pythonHealthFailureCacheTTL = 2 * time.Second

func (t *RunPythonTool) SetExecutionContext(ctx context.Context) {
	t.childCtx = ctx
}

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		// PythonTool 的真正激活在引擎层判断，或在执行时进行 health check
		return &RunPythonTool{MCPEndpoint: ""} // 默认配置，由引擎初始化或读取全局 config
	})
}

func (t *RunPythonTool) Name() string { return "code_run_python" }
func (t *RunPythonTool) Description() string {
	return "Execute Python code in a sandboxed environment via MCP service. Returns stdout, stderr, generated file URLs, execution duration, and truncation flag. Side effects: may produce output files accessible via API. Failure conditions: MCP service unavailable, timeout exceeded, runtime error. The code runs in an isolated container with no persistent state between calls; data must be re-read or passed explicitly."
}

func (t *RunPythonTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"code": {"type": "string", "description": "Python code to execute."},
			"timeout": {"type": "integer", "description": "Timeout in seconds, default 30", "default": 30}
		},
		"required": ["code"]
	}`)
}

type pyExecRequest struct {
	Code    string `json:"code"`
	Timeout int    `json:"timeout"`
}

type pyExecResponse struct {
	Success    bool     `json:"success"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	Error      *string  `json:"error"`
	Files      []string `json:"files"`
	DurationMs int      `json:"duration_ms"`
	Truncated  bool     `json:"truncated"`
}

func (t *RunPythonTool) Endpoint() string {
	endpoint := strings.TrimSpace(t.MCPEndpoint)
	if endpoint == "" {
		endpoint = "http://python-executor:8081"
	}
	return strings.TrimRight(endpoint, "/")
}

func configuredProxyToken() string {
	if config.Cfg != nil && strings.TrimSpace(config.Cfg.ProxyToken) != "" {
		return strings.TrimSpace(config.Cfg.ProxyToken)
	}
	return strings.TrimSpace(os.Getenv("PROXY_TOKEN"))
}

func configuredPublicAPIBaseURL() string {
	if config.Cfg != nil && strings.TrimSpace(config.Cfg.PublicAPIBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(config.Cfg.PublicAPIBaseURL), "/")
	}
	if value := strings.TrimSpace(os.Getenv("PUBLIC_API_BASE_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("API_BASE_URL")), "/")
}

func (t *RunPythonTool) HealthCheck(ctx context.Context) error {
	proxyToken := configuredProxyToken()
	if strings.TrimSpace(proxyToken) == "" {
		return fmt.Errorf("PROXY_TOKEN is not configured")
	}
	cacheKey := t.Endpoint() + "\x00" + proxyToken
	pythonHealthCache.Lock()
	cacheAge := time.Since(pythonHealthCache.checkedAt)
	cacheTTL := pythonHealthCacheTTL
	if pythonHealthCache.err != nil {
		cacheTTL = pythonHealthFailureCacheTTL
	}
	if pythonHealthCache.key == cacheKey && cacheAge < cacheTTL {
		err := pythonHealthCache.err
		pythonHealthCache.Unlock()
		return err
	}
	pythonHealthCache.Unlock()

	healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 1500 * time.Millisecond}

	err := t.runHealthCheck(healthCtx, client, proxyToken)
	pythonHealthCache.Lock()
	pythonHealthCache.key = cacheKey
	pythonHealthCache.checkedAt = time.Now()
	pythonHealthCache.err = err
	pythonHealthCache.Unlock()
	return err
}

func (t *RunPythonTool) runHealthCheck(ctx context.Context, client *http.Client, proxyToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Endpoint()+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, _ := json.Marshal(pyExecRequest{Code: "print('ok')", Timeout: 5})
	execReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint()+"/execute", bytes.NewReader(body))
	if err != nil {
		return err
	}
	execReq.Header.Set("Content-Type", "application/json")
	execReq.Header.Set("X-Proxy-Token", proxyToken)
	execResp, err := client.Do(execReq)
	if err != nil {
		return err
	}
	defer execResp.Body.Close()
	execBody, _ := io.ReadAll(io.LimitReader(execResp.Body, 4096))
	if execResp.StatusCode >= 300 {
		return fmt.Errorf("execute status=%d body=%s", execResp.StatusCode, strings.TrimSpace(string(execBody)))
	}
	var result pyExecResponse
	if err := json.Unmarshal(execBody, &result); err != nil {
		return fmt.Errorf("failed to parse execute health response: %w", err)
	}
	if !result.Success {
		detail := strings.TrimSpace(result.Stderr)
		if result.Error != nil && strings.TrimSpace(*result.Error) != "" {
			detail = strings.TrimSpace(*result.Error)
		}
		return fmt.Errorf("execute health failed: %s", detail)
	}
	return nil
}

func (t *RunPythonTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		Code    string `json:"code"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if params.Timeout <= 0 {
		params.Timeout = 30
	}

	endpoint := t.Endpoint()

	reqBody, _ := json.Marshal(pyExecRequest{
		Code:    params.Code,
		Timeout: params.Timeout,
	})

	execCtx := t.childCtx
	if execCtx == nil {
		execCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(execCtx, time.Duration(params.Timeout+5)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/execute", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	proxyToken := configuredProxyToken()
	if proxyToken == "" {
		return "", fmt.Errorf("PROXY_TOKEN is not configured")
	}
	req.Header.Set("X-Proxy-Token", proxyToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Python MCP service unavailable: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("Python MCP service returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result pyExecResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse Python execution result: %w", err)
	}

	apiBaseURL := configuredPublicAPIBaseURL()
	meta := ExecutionMetadataFromContext(execCtx)
	for i, f := range result.Files {
		result.Files[i] = buildPythonFileURL(apiBaseURL, f, meta)
	}

	return formatPythonResult(result), nil
}

func buildPythonFileURL(apiBaseURL, filename string, meta ExecutionMetadata) string {
	path := fmt.Sprintf("/api/python-files/%s", url.PathEscape(filename))
	base := strings.TrimRight(strings.TrimSpace(apiBaseURL), "/") + path
	secret := ""
	if config.Cfg != nil {
		secret = strings.TrimSpace(config.Cfg.AuthSecret)
	}
	if secret == "" || strings.TrimSpace(meta.WorkspaceID) == "" || strings.TrimSpace(meta.SessionID) == "" || strings.TrimSpace(meta.RunID) == "" {
		return base
	}

	values := url.Values{}
	values.Set("session_id", meta.SessionID)
	values.Set("run_id", meta.RunID)
	values.Set("sig", SignPythonFileAccess(filename, meta, secret))
	return base + "?" + values.Encode()
}

func SignPythonFileAccess(filename string, meta ExecutionMetadata, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(pythonFileAccessMessage(filename, meta)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func VerifyPythonFileAccessSignature(filename string, meta ExecutionMetadata, secret, sig string) bool {
	secret = strings.TrimSpace(secret)
	sig = strings.TrimSpace(sig)
	if secret == "" || sig == "" || strings.TrimSpace(filename) == "" ||
		strings.TrimSpace(meta.WorkspaceID) == "" ||
		strings.TrimSpace(meta.SessionID) == "" ||
		strings.TrimSpace(meta.RunID) == "" {
		return false
	}
	want := SignPythonFileAccess(filename, meta, secret)
	return hmac.Equal([]byte(want), []byte(sig))
}

func pythonFileAccessMessage(filename string, meta ExecutionMetadata) string {
	return filename + "\n" + meta.WorkspaceID + "\n" + meta.SessionID + "\n" + meta.RunID
}

func formatPythonResult(result pyExecResponse) string {
	payload := map[string]interface{}{
		"duration_ms": result.DurationMs,
		"stdout":      result.Stdout,
		"stderr":      result.Stderr,
		"files":       result.Files,
	}
	if result.Success {
		payload["ui_summary"] = fmt.Sprintf("Python execution succeeded (%dms)", result.DurationMs)
		return toolSuccess("code_run_python", payload)
	}

	errorText := ""
	if result.Error != nil {
		errorText = strings.TrimSpace(*result.Error)
	}
	if errorText == "" {
		errorText = strings.TrimSpace(result.Stderr)
	}
	payload["detail"] = errorText
	payload["ui_summary"] = fmt.Sprintf("Python execution failed (%dms)", result.DurationMs)
	return toolFailure("code_run_python", "execution_failed", "Python execution failed", payload)
}
