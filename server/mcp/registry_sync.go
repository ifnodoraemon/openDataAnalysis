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
		panic("MCP tool input schema is not initialized")
	}
	raw, err := json.Marshal(t.Schema.InputSchema)
	if err != nil {
		panic(fmt.Sprintf("MCP tool input schema cannot be encoded: %v", err))
	}
	return raw
}

func (t *MCPSyncedTool) SetExecutionContext(ctx context.Context) {
	t.parentCtx = ctx
}

func (t *MCPSyncedTool) Execute(args json.RawMessage) (string, error) {
	execCtx := t.parentCtx
	if execCtx == nil {
		return "", fmt.Errorf("MCP tool execution context is not initialized")
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
	synced map[string]string
}

func NewRegistrySync(client *Client, target *tools.Registry) *RegistrySync {
	if client == nil || target == nil {
		panic("MCP client and target registry are required")
	}
	return &RegistrySync{
		Client: client,
		Target: target,
		synced: make(map[string]string),
	}
}

func (s *RegistrySync) Sync(ctx context.Context) (int, error) {
	discovered, err := s.Client.discoverToolsWithOrigins(ctx)
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	type pendingTool struct {
		item        discoveredTool
		fingerprint string
	}
	seen := make(map[string]struct{}, len(discovered))
	pending := make([]pendingTool, 0, len(discovered))
	for _, item := range discovered {
		schema := item.Schema
		seen[schema.Name] = struct{}{}
		encodedSchema, err := json.Marshal(schema)
		if err != nil {
			return 0, fmt.Errorf("encode MCP tool schema %q: %w", schema.Name, err)
		}
		fingerprint := item.ServerName + "\x00" + string(encodedSchema)
		if existing, exists := s.synced[schema.Name]; exists {
			if existing != fingerprint {
				return 0, fmt.Errorf("MCP tool schema %q changed after registration", schema.Name)
			}
			continue
		}
		if s.Target.HasTool(schema.Name) {
			return 0, fmt.Errorf("MCP tool name %q collides with an existing runtime tool", schema.Name)
		}
		pending = append(pending, pendingTool{item: item, fingerprint: fingerprint})
	}
	for name := range s.synced {
		if _, exists := seen[name]; !exists {
			return 0, fmt.Errorf("registered MCP tool %q is no longer advertised by its server", name)
		}
	}
	for _, candidate := range pending {
		tool := &MCPSyncedTool{
			Schema:     candidate.item.Schema,
			ServerName: candidate.item.ServerName,
			Client:     s.Client,
		}
		s.Target.Register(tool)
		s.synced[candidate.item.Schema.Name] = candidate.fingerprint
	}

	return len(pending), nil
}
