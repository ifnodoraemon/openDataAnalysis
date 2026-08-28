package tools

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	memoryrepo "github.com/ifnodoraemon/openDataAnalysis/repository/memory"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	localstorage "github.com/ifnodoraemon/openDataAnalysis/storage/local"
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

func TestRunPythonToolPersistsGeneratedFilesAsArtifacts(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{ProxyToken: "proxy-token"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	const filename = "req_12345678_plot.png"
	meta := ExecutionMetadata{
		UserID:      "u_1",
		WorkspaceID: "w_1",
		SessionID:   "s_1",
		RunID:       "r_1",
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/execute":
			_ = json.NewEncoder(w).Encode(pyExecResponse{Success: true, Files: []string{filename}, DurationMs: 10})
		case "/files/" + filename:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("artifact-body"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network namespace does not permit a loopback test server: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	workspaceRepo := memoryrepo.NewWorkspaceRepository()
	if err := workspaceRepo.CreateWorkspace(context.Background(), &domain.Workspace{ID: meta.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	if err := workspaceRepo.AddMember(context.Background(), &domain.WorkspaceMember{WorkspaceID: meta.WorkspaceID, UserID: meta.UserID, Role: domain.WorkspaceRoleOwner}); err != nil {
		t.Fatal(err)
	}
	fileRepo := memoryrepo.NewFileRepository()
	fileService := &service.FileService{Storage: localstorage.New(t.TempDir(), ""), FileRepo: fileRepo, WorkspaceRepo: workspaceRepo}
	reportState := &ReportState{}
	tool := &RunPythonTool{MCPEndpoint: server.URL, FileService: fileService, ReportState: reportState}
	tool.SetExecutionContext(WithExecutionMetadata(context.Background(), meta))
	result, err := tool.Execute(json.RawMessage(`{"code":"print(1)","timeout":5}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var payload struct {
		OK        bool             `json:"ok"`
		Files     []string         `json:"files"`
		Artifacts []ArtifactRecord `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || len(payload.Files) != 1 || len(payload.Artifacts) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	artifact := payload.Artifacts[0]
	if !strings.HasPrefix(artifact.ID, "art_") || payload.Files[0] != "/api/files/"+artifact.ID {
		t.Fatalf("expected durable artifact URL, got %#v", payload)
	}
	reader, _, err := fileService.OpenForDownload(context.Background(), meta.UserID, meta.WorkspaceID, artifact.ID)
	if err != nil {
		t.Fatalf("open persisted artifact: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "artifact-body" {
		t.Fatalf("unexpected artifact body %q err=%v", body, err)
	}
	if _, ok := reportState.Artifacts[artifact.ID]; !ok {
		t.Fatalf("expected artifact in report-state ledger: %#v", reportState.Artifacts)
	}
}

func TestRunPythonToolRequiresProxyTokenBeforeExecute(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = prevCfg })
	t.Setenv("PROXY_TOKEN", "")

	tool := &RunPythonTool{MCPEndpoint: "http://127.0.0.1:1"}
	tool.SetExecutionContext(WithExecutionMetadata(context.Background(), ExecutionMetadata{UserID: "u_1", WorkspaceID: "w_1", SessionID: "s_1", RunID: "r_1"}))
	_, err := tool.Execute(json.RawMessage(`{"code":"print(1)","timeout":5}`))
	if err == nil || !strings.Contains(err.Error(), "PROXY_TOKEN is not configured") {
		t.Fatalf("expected missing proxy token error, got %v", err)
	}
}

func TestRunPythonToolExecutionTimeout(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{ProxyToken: "proxy-token"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	fakeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(11 * time.Second) // wait longer than the client timeout (5 + 5 = 10s)
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
	tool.SetExecutionContext(WithExecutionMetadata(context.Background(), ExecutionMetadata{UserID: "u_1", WorkspaceID: "w_1", SessionID: "s_1", RunID: "r_1"}))

	start := time.Now()
	_, err = tool.Execute(json.RawMessage(`{"code": "import time; time.sleep(20)", "timeout": 5}`))
	dur := time.Since(start)

	if err == nil {
		t.Fatal("expected run to fail with a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "Python MCP") {
		t.Fatalf("expected MCP unavailable error, got %v", err)
	}
	if dur > 12*time.Second {
		t.Fatalf("expected tool to time out at around 10s, but it took %v", dur)
	}
}

func TestRunPythonToolHealthCheckCachesProbe(t *testing.T) {
	resetPythonHealthCacheForTest(t)
	prevCfg := config.Cfg
	config.Cfg = &config.Config{ProxyToken: "proxy-token"}
	t.Cleanup(func() { config.Cfg = prevCfg })

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
	prevCfg := config.Cfg
	config.Cfg = &config.Config{ProxyToken: "proxy-token"}
	t.Cleanup(func() { config.Cfg = prevCfg })

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
