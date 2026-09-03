package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

// RunPythonTool 通过 MCP 服务执行 Python 代码
type RunPythonTool struct {
	MCPEndpoint      string // Python MCP 服务地址，如 http://python-executor:8081
	FileService      *service.FileService
	ReportState      *ReportState
	SourceFileLookup SourceFileLookup
	childCtx         context.Context
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
		endpoint := ""
		if config.Cfg != nil {
			endpoint = config.Cfg.PythonMCPURL
		}
		return &RunPythonTool{MCPEndpoint: endpoint, FileService: ctx.FileService, ReportState: ctx.ReportState, SourceFileLookup: ctx.SourceFileLookup}
	})
}

func (t *RunPythonTool) Name() string { return "code_run_python" }
func (t *RunPythonTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, Delegable: true}
}
func (t *RunPythonTool) Description() string {
	return "Execute Python code in an isolated sandbox. Optional inputs mount exact payloads as named files: analysis_result or artifact payloads, or the original file bytes of an uploaded file-upload source (kind source_file). Use source_file to read raw uploads whose structure the deterministic importer rejects (title rows above headers, side-by-side tables, multi-row headers, stacked tables) — parse them in code, write cleaned CSV outputs, and import those via data_import_artifact. Generated files are copied into durable object storage and returned as artifact IDs and authenticated API URLs. Returns stdout, stderr, artifacts, duration, and truncation facts."
}

func (t *RunPythonTool) Parameters() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"code": {"type": "string", "description": "Python code to execute."},
			"timeout": {"type": "integer", "minimum": 5, "maximum": %d, "description": "Explicit timeout in seconds."},
			"inputs": {"type":"array","description":"Payloads to mount in the sandbox. kind source_file mounts the original bytes of an uploaded data source (id = source_id, as shown by state_session_sources_inspect or upload messages).","items":{"type":"object","additionalProperties":false,"properties":{"kind":{"type":"string","enum":["analysis_result","artifact","source_file"]},"id":{"type":"string"},"filename":{"type":"string"}},"required":["kind","id"]}}
		},
		"required": ["code", "timeout"]
	}`, pythonMaxTimeout()))
}

func pythonMaxTimeout() int {
	if config.Cfg != nil && config.Cfg.PythonMaxTimeoutSec >= 5 {
		return config.Cfg.PythonMaxTimeoutSec
	}
	return 120
}

type pyExecRequest struct {
	Code        string        `json:"code"`
	Timeout     int           `json:"timeout"`
	SessionID   string        `json:"session_id,omitempty"`
	WorkspaceID string        `json:"workspace_id,omitempty"`
	Inputs      []pyExecInput `json:"inputs,omitempty"`
}

type pyExecInput struct {
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
}

type pyExecResponse struct {
	Success         bool           `json:"success"`
	Stdout          string         `json:"stdout"`
	Stderr          string         `json:"stderr"`
	Error           *string        `json:"error"`
	Files           []string       `json:"files"`
	DurationMs      int            `json:"duration_ms"`
	Truncated       bool           `json:"truncated"`
	ExecutionLimits map[string]int `json:"execution_limits"`
}

func (t *RunPythonTool) endpointURL(path string) (string, error) {
	base := t.MCPEndpoint
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("PYTHON_MCP_URL is not configured")
	}
	if strings.TrimSpace(base) != base || strings.HasSuffix(base, "/") {
		return "", fmt.Errorf("PYTHON_MCP_URL must be an exact base URL without a trailing slash")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("PYTHON_MCP_URL must be an absolute HTTP(S) base URL")
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("Python executor path must start with a slash")
	}
	return base + path, nil
}

func configuredProxyToken() string {
	if config.Cfg != nil && config.Cfg.ProxyToken != "" {
		return config.Cfg.ProxyToken
	}
	return ""
}

func (t *RunPythonTool) CheckAvailability(ctx context.Context) error {
	if _, err := t.endpointURL(""); err != nil {
		return err
	}
	return t.HealthCheck(ctx)
}

func (t *RunPythonTool) HealthCheck(ctx context.Context) error {
	proxyToken := configuredProxyToken()
	if strings.TrimSpace(proxyToken) == "" {
		return fmt.Errorf("PROXY_TOKEN is not configured")
	}
	if strings.TrimSpace(proxyToken) != proxyToken {
		return fmt.Errorf("PROXY_TOKEN must not contain leading or trailing whitespace")
	}
	base, err := t.endpointURL("")
	if err != nil {
		return err
	}
	cacheKey := base + "\x00" + proxyToken
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

	err = t.runHealthCheck(healthCtx, client, proxyToken)
	pythonHealthCache.Lock()
	pythonHealthCache.key = cacheKey
	pythonHealthCache.checkedAt = time.Now()
	pythonHealthCache.err = err
	pythonHealthCache.Unlock()
	return err
}

func (t *RunPythonTool) runHealthCheck(ctx context.Context, client *http.Client, proxyToken string) error {
	healthURL, err := t.endpointURL("/health")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if readErr != nil {
			return fmt.Errorf("status=%d and failed to read response: %w", resp.StatusCode, readErr)
		}
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	healthNamespace := strings.ReplaceAll(uuid.NewString(), "-", "")
	body, err := json.Marshal(pyExecRequest{Code: "print('ok')", Timeout: 5, SessionID: "health_" + healthNamespace, WorkspaceID: "health_" + healthNamespace})
	if err != nil {
		return fmt.Errorf("failed to encode execute health request: %w", err)
	}
	executeURL, err := t.endpointURL("/execute")
	if err != nil {
		return err
	}
	execReq, err := http.NewRequestWithContext(ctx, http.MethodPost, executeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	execReq.Header.Set("Content-Type", "application/json")
	if reqID := middleware.GetReqID(ctx); reqID != "" {
		execReq.Header.Set("X-Request-ID", reqID)
	}
	execReq.Header.Set("X-Proxy-Token", proxyToken)
	execResp, err := client.Do(execReq)
	if err != nil {
		return err
	}
	defer execResp.Body.Close()
	execBody, err := io.ReadAll(io.LimitReader(execResp.Body, 4096))
	if err != nil {
		return fmt.Errorf("failed to read execute health response: %w", err)
	}
	if execResp.StatusCode >= 300 {
		return fmt.Errorf("execute status=%d body=%s", execResp.StatusCode, strings.TrimSpace(string(execBody)))
	}
	var result pyExecResponse
	if err := jsoncontract.Decode(execBody, &result); err != nil {
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
		Inputs  []struct {
			Kind     string `json:"kind"`
			ID       string `json:"id"`
			Filename string `json:"filename"`
		} `json:"inputs"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if params.Timeout < 5 || params.Timeout > pythonMaxTimeout() {
		return toolFailure("code_run_python", "invalid_timeout", fmt.Sprintf("timeout must be between 5 and %d seconds", pythonMaxTimeout()), map[string]interface{}{"timeout": params.Timeout}), nil
	}
	if strings.TrimSpace(params.Code) == "" {
		return toolFailure("code_run_python", "invalid_code", "code must contain text", nil), nil
	}

	execCtx := t.childCtx
	if execCtx == nil {
		return toolFailure("code_run_python", "missing_execution_context", "tool execution context is not initialized", nil), nil
	}
	meta := ExecutionMetadataFromContext(execCtx)
	if meta.UserID == "" || meta.WorkspaceID == "" || meta.SessionID == "" || meta.RunID == "" {
		return toolFailure("code_run_python", "missing_execution_identity", "authenticated user, workspace, session, and run identities are required", nil), nil
	}
	inputs, err := t.resolveInputs(execCtx, meta, params.Inputs)
	if err != nil {
		return toolFailure("code_run_python", "input_resolution_failed", "Python input could not be resolved", map[string]interface{}{"detail": err.Error()}), nil
	}

	reqBody, err := json.Marshal(pyExecRequest{
		Code:        params.Code,
		Timeout:     params.Timeout,
		SessionID:   meta.SessionID,
		WorkspaceID: meta.WorkspaceID,
		Inputs:      inputs,
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode Python execution request: %w", err)
	}
	ctx, cancel := context.WithTimeout(execCtx, time.Duration(params.Timeout+5)*time.Second)
	defer cancel()

	executeURL, err := t.endpointURL("/execute")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, executeURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if reqID := middleware.GetReqID(execCtx); reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read Python execution response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("Python MCP service returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result pyExecResponse
	if err := jsoncontract.Decode(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse Python execution result: %w", err)
	}

	artifacts := make([]ArtifactRecord, 0, len(result.Files))
	if len(result.Files) > 0 {
		// Artifact persistence gets its own timeout budget: code that ran
		// close to its declared timeout must not lose outputs because the
		// execution budget was already spent.
		artifactCtx, artifactCancel := context.WithTimeout(context.WithoutCancel(execCtx), 60*time.Second)
		defer artifactCancel()
		for _, filePath := range result.Files {
			artifact, artifactErr := t.persistExecutorArtifact(artifactCtx, client, proxyToken, filePath, meta)
			if artifactErr != nil {
				return toolFailure("code_run_python", "artifact_persistence_failed", "Python output could not be persisted", map[string]interface{}{"detail": artifactErr.Error()}), nil
			}
			artifacts = append(artifacts, artifact)
			if t.ReportState != nil {
				if err := t.ReportState.RecordArtifact(artifact); err != nil {
					return "", fmt.Errorf("failed to record Python artifact: %w", err)
				}
			}
		}
	}

	return formatPythonResult(result, artifacts), nil
}

func (t *RunPythonTool) resolveInputs(ctx context.Context, meta ExecutionMetadata, requested []struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Filename string `json:"filename"`
}) ([]pyExecInput, error) {
	inputs := make([]pyExecInput, 0, len(requested))
	for _, item := range requested {
		if item.Kind != strings.TrimSpace(item.Kind) || item.ID == "" || item.ID != strings.TrimSpace(item.ID) || item.Filename != strings.TrimSpace(item.Filename) {
			return nil, fmt.Errorf("Python input kind, id, and filename must use exact values")
		}
		name := item.Filename
		var content []byte
		var err error
		switch item.Kind {
		case "analysis_result":
			if t.ReportState == nil {
				return nil, fmt.Errorf("analysis result store is unavailable")
			}
			t.ReportState.RLock()
			result, ok := t.ReportState.Results[item.ID]
			t.ReportState.RUnlock()
			if !ok {
				return nil, fmt.Errorf("analysis result %s not found", item.ID)
			}
			content, err = json.Marshal(result)
			if err != nil {
				return nil, fmt.Errorf("failed to encode analysis result %s: %w", item.ID, err)
			}
			if name == "" {
				name = result.ID + ".json"
			}
		case "artifact":
			if t.FileService == nil {
				return nil, fmt.Errorf("artifact store is unavailable")
			}
			reader, file, err := t.FileService.OpenForDownload(ctx, meta.UserID, meta.WorkspaceID, item.ID)
			if err != nil {
				return nil, err
			}
			content, err = io.ReadAll(io.LimitReader(reader, 50*1024*1024+1))
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return nil, fmt.Errorf("failed to read artifact input: %w", errors.Join(err, closeErr))
			}
			if len(content) > 50*1024*1024 {
				return nil, fmt.Errorf("artifact input exceeds the 50 MiB limit")
			}
			if name == "" {
				name = file.DisplayName
			}
		case "source_file":
			if t.SourceFileLookup == nil {
				return nil, fmt.Errorf("source file lookup is unavailable")
			}
			fileID, sourceFilename, err := t.SourceFileLookup(ctx, meta.WorkspaceID, item.ID)
			if err != nil {
				return nil, err
			}
			if t.FileService == nil {
				return nil, fmt.Errorf("file service is unavailable")
			}
			reader, _, err := t.FileService.OpenForDownload(ctx, meta.UserID, meta.WorkspaceID, fileID)
			if err != nil {
				return nil, err
			}
			content, err = io.ReadAll(io.LimitReader(reader, 50*1024*1024+1))
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return nil, fmt.Errorf("failed to read source file input: %w", errors.Join(err, closeErr))
			}
			if len(content) > 50*1024*1024 {
				return nil, fmt.Errorf("source file input exceeds the 50 MiB limit")
			}
			if name == "" {
				name = sourceFilename
			}
		default:
			return nil, fmt.Errorf("unsupported input kind %q", item.Kind)
		}
		if name == "." || name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\\`) {
			return nil, fmt.Errorf("invalid input filename %q", name)
		}
		inputs = append(inputs, pyExecInput{Filename: name, ContentBase64: base64.StdEncoding.EncodeToString(content)})
	}
	return inputs, nil
}

func (t *RunPythonTool) persistExecutorArtifact(ctx context.Context, client *http.Client, proxyToken, filePath string, meta ExecutionMetadata) (ArtifactRecord, error) {
	if t.FileService == nil {
		return ArtifactRecord{}, fmt.Errorf("durable artifact store is unavailable")
	}
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(filePath) != filePath || strings.HasPrefix(filePath, "/") || strings.HasSuffix(filePath, "/") {
		return ArtifactRecord{}, fmt.Errorf("executor file path must be a non-empty exact relative path")
	}
	parts := strings.Split(filePath, "/")
	for i := range parts {
		if parts[i] == "" || parts[i] == "." || parts[i] == ".." {
			return ArtifactRecord{}, fmt.Errorf("executor file path contains an invalid segment")
		}
		parts[i] = url.PathEscape(parts[i])
	}
	fileURL, err := t.endpointURL("/files/" + strings.Join(parts, "/"))
	if err != nil {
		return ArtifactRecord{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return ArtifactRecord{}, err
	}
	req.Header.Set("X-Proxy-Token", proxyToken)
	resp, err := client.Do(req)
	if err != nil {
		return ArtifactRecord{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if readErr != nil {
			return ArtifactRecord{}, fmt.Errorf("executor file download status=%d and response read failed: %w", resp.StatusCode, readErr)
		}
		return ArtifactRecord{}, fmt.Errorf("executor file download status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024+1))
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("failed to read executor output: %w", err)
	}
	if len(body) > 50*1024*1024 {
		return ArtifactRecord{}, fmt.Errorf("executor output exceeds the 50 MiB limit")
	}
	name := filepath.Base(filePath)
	file, err := t.FileService.SaveArtifact(ctx, service.SaveArtifactInput{
		UserID: meta.UserID, WorkspaceID: meta.WorkspaceID, SessionID: meta.SessionID,
		RunID: meta.RunID, FileName: name, ContentType: resp.Header.Get("Content-Type"),
		Body: bytes.NewReader(body), Size: int64(len(body)),
	})
	if err != nil {
		return ArtifactRecord{}, err
	}
	return ArtifactRecord{ID: file.ID, Name: file.DisplayName, ContentType: file.ContentType, DownloadURL: "/api/files/" + url.PathEscape(file.ID), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func formatPythonResult(result pyExecResponse, artifactLists ...[]ArtifactRecord) string {
	var artifacts []ArtifactRecord
	if len(artifactLists) > 0 {
		artifacts = artifactLists[0]
	}
	files := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		files = append(files, artifact.DownloadURL)
	}
	payload := map[string]interface{}{
		"duration_ms": result.DurationMs,
		"stdout":      result.Stdout,
		"stderr":      result.Stderr,
		"truncated":   result.Truncated,
		"files":       files,
		"artifacts":   artifacts,
	}
	if len(result.ExecutionLimits) > 0 {
		payload["execution_limits"] = result.ExecutionLimits
	}
	if result.Success {
		payload["ui_summary"] = fmt.Sprintf("Python 执行成功，用时 %d 毫秒。", result.DurationMs)
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
	payload["ui_summary"] = fmt.Sprintf("Python 执行失败，用时 %d 毫秒。", result.DurationMs)
	return toolFailure("code_run_python", "execution_failed", "Python execution failed", payload)
}
