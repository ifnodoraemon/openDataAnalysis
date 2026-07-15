package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type MCPSyncedTool struct {
	Schema     ToolSchema
	ServerName string
	Client     *Client
	parentCtx  context.Context
}

func (t *MCPSyncedTool) Name() string { return t.Schema.Name }

func (t *MCPSyncedTool) Description() string { return t.Schema.Description }

func (t *MCPSyncedTool) Parameters() json.RawMessage {
	if t.Schema.InputSchema == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	raw, err := json.Marshal(t.Schema.InputSchema)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}

func (t *MCPSyncedTool) SetExecutionContext(ctx context.Context) {
	t.parentCtx = ctx
}

func (t *MCPSyncedTool) Execute(args json.RawMessage) (string, error) {
	execCtx := t.parentCtx
	if execCtx == nil {
		execCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(execCtx, 120*time.Second)
	defer cancel()

	result, err := t.Client.ExecuteTool(ctx, t.ServerName, t.Schema.Name, args)
	if err != nil {
		return "", fmt.Errorf("MCP tool %s/%s execution failed: %w", t.ServerName, t.Schema.Name, err)
	}

	return string(result), nil
}

type RegistrySync struct {
	Client *Client
	Target *tools.Registry
	mu     sync.Mutex
	synced map[string]struct{}
}

func NewRegistrySync(client *Client, target *tools.Registry) *RegistrySync {
	return &RegistrySync{
		Client: client,
		Target: target,
		synced: make(map[string]struct{}),
	}
}

func (s *RegistrySync) Sync(ctx context.Context) (int, error) {
	discovered, err := s.Client.DiscoverTools(ctx)
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	registrered := 0
	for _, schema := range discovered {
		if _, exists := s.synced[schema.Name]; exists {
			continue
		}

		serverName := s.findServerForTool(schema.Name)
		if serverName == "" {
			continue
		}

		tool := &MCPSyncedTool{
			Schema:     schema,
			ServerName: serverName,
			Client:     s.Client,
		}
		s.Target.Register(tool)
		s.synced[schema.Name] = struct{}{}
		registrered++
	}

	return registrered, nil
}

func (s *RegistrySync) findServerForTool(toolName string) string {
	for _, srv := range s.Client.ListServers() {
		for _, schema := range s.cachedToolsFor(srv.Name) {
			if schema.Name == toolName {
				return srv.Name
			}
		}
	}
	return ""
}

func (s *RegistrySync) cachedToolsFor(serverName string) []ToolSchema {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.Client.mu.RLock()
	srv, ok := s.Client.configs[serverName]
	s.Client.mu.RUnlock()
	if !ok {
		return nil
	}
	tools, err := s.Client.discoverFromServer(ctx, srv)
	if err != nil {
		return nil
	}
	return tools
}

func (s *RegistrySync) RemoveOrphaned(ctx context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Target == nil {
		return 0
	}

	removed := 0
	for name := range s.synced {
		if !s.Target.HasTool(name) {
			delete(s.synced, name)
			removed++
		}
	}
	return removed
}
