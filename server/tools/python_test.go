package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

func resetPythonHealthCacheForTest(t *testing.T) {
	t.Helper()
	pythonHealthCache.Lock()
	pythonHealthCache.key = ""
	pythonHealthCache.checkedAt = time.Time{}
	pythonHealthCache.err = nil
	pythonHealthCache.Unlock()
}

func TestFormatPythonResultReturnsStructuredSuccess(t *testing.T) {
	t.Parallel()

	result := formatPythonResult(pyExecResponse{
		Success:    true,
		Stdout:     "42\n",
		Stderr:     "",
		Files:      []string{"plot.png"},
		DurationMs: 123,
	})

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("expected ok=true, got %#v", payload["ok"])
	}
	if payload["tool"] != "code_run_python" {
		t.Fatalf("unexpected tool: %#v", payload["tool"])
	}
}

func TestFormatPythonResultReturnsStructuredFailure(t *testing.T) {
	t.Parallel()

	errorText := "NameError"
	result := formatPythonResult(pyExecResponse{
		Success:    false,
		Stdout:     "",
		Stderr:     "Traceback",
		Error:      &errorText,
		Files:      nil,
		DurationMs: 88,
	})

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload["ok"])
	}
	if payload["error_code"] != "execution_failed" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestRunPythonToolSignsGeneratedFileURLs(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{AuthSecret: "abcdefghijklmnopqrstuvwxyz123456"}
	t.Cleanup(func() { config.Cfg = prevCfg })
	t.Setenv("API_BASE_URL", "http://api.test")
	t.Setenv("PROXY_TOKEN", "proxy-token")

	const filename = "req_12345678_plot.png"
	meta := ExecutionMetadata{
		WorkspaceID: "w_1",
		SessionID:   "s_1",
		RunID:       "r_1",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(pyExecResponse{
			Success:    true,
			Files:      []string{filename},
			DurationMs: 10,
		})
	}))
	t.Cleanup(server.Close)

	tool := &RunPythonTool{MCPEndpoint: server.URL}
	tool.SetExecutionContext(WithExecutionMetadata(context.Background(), meta))
	result, err := tool.Execute(json.RawMessage(`{"code":"print(1)","timeout":1}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var payload struct {
		OK    bool     `json:"ok"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || len(payload.Files) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	parsed, err := url.Parse(payload.Files[0])
	if err != nil {
		t.Fatalf("parse file url: %v", err)
	}
	if parsed.Path != "/api/python-files/"+filename {
		t.Fatalf("unexpected file path: %s", parsed.Path)
	}
	query := parsed.Query()
	if query.Get("session_id") != meta.SessionID || query.Get("run_id") != meta.RunID {
		t.Fatalf("missing run scope in query: %s", parsed.RawQuery)
	}
	if !VerifyPythonFileAccessSignature(filename, meta, config.Cfg.AuthSecret, query.Get("sig")) {
		t.Fatalf("invalid file access signature in URL: %s", payload.Files[0])
	}
}

func TestRunPythonToolRequiresProxyTokenBeforeExecute(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = prevCfg })
	t.Setenv("PROXY_TOKEN", "")

	tool := &RunPythonTool{MCPEndpoint: "http://127.0.0.1:1"}
	_, err := tool.Execute(json.RawMessage(`{"code":"print(1)","timeout":1}`))
	if err == nil || !strings.Contains(err.Error(), "PROXY_TOKEN is not configured") {
		t.Fatalf("expected missing proxy token error, got %v", err)
	}
}

func TestRunPythonToolExecutionTimeout(t *testing.T) {
	t.Setenv("PROXY_TOKEN", "proxy-token")

	fakeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(7 * time.Second) // wait longer than the client timeout (1 + 5 = 6s)
		w.WriteHeader(http.StatusOK)
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping network-dependent test due to inability to bind port: %v", err)
	}

	server := httptest.NewUnstartedServer(fakeHandler)
	server.Listener = l
	server.Start()
	t.Cleanup(func() { server.Close() })

	tool := &RunPythonTool{
		MCPEndpoint: server.URL,
	}

	start := time.Now()
	_, err = tool.Execute(json.RawMessage(`{"code": "import time; time.sleep(10)", "timeout": 1}`))
	dur := time.Since(start)

	if err == nil {
		t.Fatal("expected run to fail with a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "Python MCP") {
		t.Fatalf("expected MCP unavailable error, got %v", err)
	}
	if dur > 8*time.Second {
		t.Fatalf("expected tool to time out at around 6s, but it took %v", dur)
	}
}

func TestBuildPythonFileURLDefaultsToRelativeAPIPath(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{AuthSecret: "abcdefghijklmnopqrstuvwxyz123456"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	meta := ExecutionMetadata{
		WorkspaceID: "w_1",
		SessionID:   "s_1",
		RunID:       "r_1",
	}
	got := buildPythonFileURL("", "plot 1.png", meta)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse file url: %v", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		t.Fatalf("expected relative API path, got %s", got)
	}
	if parsed.EscapedPath() != "/api/python-files/plot%201.png" {
		t.Fatalf("unexpected relative path: %s", parsed.EscapedPath())
	}
	if parsed.Query().Get("session_id") != meta.SessionID || parsed.Query().Get("run_id") != meta.RunID {
		t.Fatalf("missing run scope query: %s", parsed.RawQuery)
	}
}

func TestRunPythonToolHealthCheckCachesProbe(t *testing.T) {
	resetPythonHealthCacheForTest(t)
	t.Setenv("PROXY_TOKEN", "proxy-token")

	var executeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/execute":
			executeCalls.Add(1)
			if r.Header.Get("X-Proxy-Token") != "proxy-token" {
				t.Fatalf("missing proxy token header")
			}
			_ = json.NewEncoder(w).Encode(pyExecResponse{Success: true, DurationMs: 1})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	tool := &RunPythonTool{MCPEndpoint: server.URL}
	if err := tool.HealthCheck(context.Background()); err != nil {
		t.Fatalf("first HealthCheck returned error: %v", err)
	}
	if err := tool.HealthCheck(context.Background()); err != nil {
		t.Fatalf("second HealthCheck returned error: %v", err)
	}
	if executeCalls.Load() != 1 {
		t.Fatalf("expected execute probe to be cached, got %d calls", executeCalls.Load())
	}
}

func TestRunPythonToolHealthCheckFailureCacheExpiresQuickly(t *testing.T) {
	resetPythonHealthCacheForTest(t)
	t.Setenv("PROXY_TOKEN", "proxy-token")

	var executeCalls atomic.Int32
	failExecute := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/execute":
			executeCalls.Add(1)
			if failExecute {
				http.Error(w, "cold start", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(pyExecResponse{Success: true, DurationMs: 1})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	tool := &RunPythonTool{MCPEndpoint: server.URL}
	if err := tool.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected first HealthCheck to fail")
	}
	if err := tool.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected cached failed HealthCheck to fail")
	}
	if executeCalls.Load() != 1 {
		t.Fatalf("expected immediate retry to use failure cache, got %d execute calls", executeCalls.Load())
	}

	time.Sleep(pythonHealthFailureCacheTTL + 100*time.Millisecond)
	failExecute = false
	if err := tool.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected HealthCheck to retry after short failure cache and succeed: %v", err)
	}
	if executeCalls.Load() != 2 {
		t.Fatalf("expected retry after failure cache expiry, got %d execute calls", executeCalls.Load())
	}
}
