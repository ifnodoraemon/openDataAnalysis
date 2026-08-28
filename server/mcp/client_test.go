package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func TestMCPSyncedToolSetExecutionContext(t *testing.T) {
	t.Parallel()

	tool := &MCPSyncedTool{}
	ctx, cancel := context.WithCancel(context.Background())

	tool.SetExecutionContext(ctx)
	if tool.parentCtx != ctx {
		t.Fatal("SetExecutionContext did not store context")
	}

	cancel()
	if tool.parentCtx.Err() == nil {
		t.Fatal("expected cancelled context")
	}
}

func TestMCPSyncedToolExecuteUsesParentContext(t *testing.T) {
	t.Parallel()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	parentCancel()

	client := NewClient()
	client.RegisterServer("test", "http://127.0.0.1:0", "")
	tool := &MCPSyncedTool{
		Schema:     ToolSchema{Name: "test_tool"},
		ServerName: "test",
		Client:     client,
	}
	tool.SetExecutionContext(parentCtx)

	_, err := tool.Execute(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestMCPSyncedToolExecuteRejectsMissingExecutionContext(t *testing.T) {
	t.Parallel()

	client := NewClient()
	tool := &MCPSyncedTool{
		Schema:     ToolSchema{Name: "test_tool"},
		ServerName: "nonexistent",
		Client:     client,
	}

	_, err := tool.Execute(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error (no server registered)")
	}
	if !strings.Contains(err.Error(), "execution context is not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsDuplicateServerRegistration(t *testing.T) {
	t.Parallel()

	client := NewClient()
	if err := client.RegisterServer("test", "http://localhost:8088", "token123"); err != nil {
		t.Fatalf("register server: %v", err)
	}
	if err := client.RegisterServer("test", "http://localhost:8089", "token456"); err == nil {
		t.Fatal("expected duplicate server name to be rejected")
	}
}

func TestClientRejectsInexactServerConfiguration(t *testing.T) {
	t.Parallel()

	client := NewClient()
	for _, testCase := range []struct {
		name     string
		endpoint string
	}{
		{name: "", endpoint: "http://localhost:8088"},
		{name: " padded ", endpoint: "http://localhost:8088"},
		{name: "server", endpoint: "localhost:8088"},
		{name: "server", endpoint: "http://localhost:8088/"},
		{name: "server", endpoint: "http://localhost:8088?mode=guess"},
	} {
		if err := client.RegisterServer(testCase.name, testCase.endpoint, ""); err == nil {
			t.Fatalf("expected configuration to be rejected: %#v", testCase)
		}
	}
}

func TestClientListServers(t *testing.T) {
	t.Parallel()

	client := NewClient()
	client.RegisterServer("a", "http://a:8088", "")
	client.RegisterServer("b", "http://b:8088", "")

	servers := client.ListServers()
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
}

func TestClientConcurrency(t *testing.T) {
	t.Parallel()

	client := NewClient()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			client.RegisterServer("srv"+string(rune('A'+n%26)), "http://localhost:8088", "")
		}(i)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = client.ListServers()
		}()
	}
	wg.Wait()
}

func TestNewRegistrySyncRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected missing target registry to panic")
		}
	}()
	NewRegistrySync(NewClient(), nil)
}

func TestRegistrySyncPreservesExactServerOrigin(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tools":[{"name":"remote_observe","description":"Return remote facts.","parameters":{"type":"object","properties":{}}}]}`))
	}))
	defer server.Close()

	client := NewClient()
	if err := client.RegisterServer("facts_server", server.URL, ""); err != nil {
		t.Fatalf("register server: %v", err)
	}
	target := tools.NewRegistry()
	syncer := NewRegistrySync(client, target)
	count, err := syncer.Sync(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("sync count=%d error=%v", count, err)
	}
	registered, err := target.Get("remote_observe")
	if err != nil {
		t.Fatalf("get synced tool: %v", err)
	}
	if registered.(*MCPSyncedTool).ServerName != "facts_server" {
		t.Fatalf("unexpected server origin: %#v", registered)
	}
	count, err = syncer.Sync(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("idempotent sync count=%d error=%v", count, err)
	}
}

func TestRegistrySyncRejectsChangedRemoteContract(t *testing.T) {
	t.Parallel()

	description := "Return remote facts."
	var responseMu sync.RWMutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseMu.RLock()
		currentDescription := description
		responseMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"tools": []map[string]interface{}{{
				"name":        "remote_observe",
				"description": currentDescription,
				"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			}},
		})
	}))
	defer server.Close()

	client := NewClient()
	if err := client.RegisterServer("facts_server", server.URL, ""); err != nil {
		t.Fatalf("register server: %v", err)
	}
	syncer := NewRegistrySync(client, tools.NewRegistry())
	if count, err := syncer.Sync(context.Background()); err != nil || count != 1 {
		t.Fatalf("initial sync count=%d error=%v", count, err)
	}
	responseMu.Lock()
	description = "Return changed remote facts."
	responseMu.Unlock()
	if _, err := syncer.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("expected changed contract error, got %v", err)
	}
}

func TestRegistrySyncRejectsDuplicateNamesWithoutMutation(t *testing.T) {
	t.Parallel()

	newServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tools":[{"name":"duplicate_tool","description":"Return facts.","parameters":{"type":"object","properties":{}}}]}`))
		}))
	}
	first := newServer()
	defer first.Close()
	second := newServer()
	defer second.Close()

	client := NewClient()
	if err := client.RegisterServer("first", first.URL, ""); err != nil {
		t.Fatalf("register first server: %v", err)
	}
	if err := client.RegisterServer("second", second.URL, ""); err != nil {
		t.Fatalf("register second server: %v", err)
	}
	target := tools.NewRegistry()
	if _, err := NewRegistrySync(client, target).Sync(context.Background()); err == nil {
		t.Fatal("expected duplicate remote tool names to be rejected")
	}
	if len(target.ListTools()) != 0 {
		t.Fatalf("expected atomic sync failure, got %#v", target.ListTools())
	}
}

func TestClientDiscoverToolsEmpty(t *testing.T) {
	t.Parallel()

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools from empty registry, got %d", len(tools))
	}
}

func TestClientExecuteToolRejectsInexactArgumentsBeforeTransport(t *testing.T) {
	t.Parallel()

	client := NewClient()
	for _, testCase := range []struct {
		serverName string
		toolName   string
		args       json.RawMessage
	}{
		{serverName: " server", toolName: "tool", args: json.RawMessage(`{}`)},
		{serverName: "server", toolName: "tool ", args: json.RawMessage(`{}`)},
		{serverName: "server", toolName: "tool", args: json.RawMessage(`[]`)},
		{serverName: "server", toolName: "tool", args: json.RawMessage(`null`)},
		{serverName: "server", toolName: "tool", args: json.RawMessage(`{"key":1,"key":2}`)},
	} {
		if _, err := client.ExecuteTool(context.Background(), testCase.serverName, testCase.toolName, testCase.args); err == nil {
			t.Fatalf("expected request to be rejected: %#v", testCase)
		}
	}
}
