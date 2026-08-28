package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type discoveredTool struct {
	Schema     ToolSchema
	ServerName string
}

type ServerConfig struct {
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint"`
	AuthToken string `json:"auth_token,omitempty"`
}

type Client struct {
	configs map[string]ServerConfig
	mu      sync.RWMutex
}

func NewClient() *Client {
	return &Client{
		configs: make(map[string]ServerConfig),
	}
}

func (c *Client) RegisterServer(name, endpoint, authToken string) error {
	if name == "" || name != strings.TrimSpace(name) {
		return fmt.Errorf("MCP server name must be a non-empty exact value")
	}
	if endpoint == "" || endpoint != strings.TrimSpace(endpoint) || strings.HasSuffix(endpoint, "/") {
		return fmt.Errorf("MCP endpoint must be an exact URL without a trailing slash")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("MCP endpoint must be an absolute HTTP(S) URL without query or fragment")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.configs[name]; exists {
		return fmt.Errorf("MCP server %q is already registered", name)
	}
	c.configs[name] = ServerConfig{
		Name:      name,
		Endpoint:  endpoint,
		AuthToken: authToken,
	}
	return nil
}

func (c *Client) ListServers() []ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ServerConfig, 0, len(c.configs))
	for _, cfg := range c.configs {
		result = append(result, cfg)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (c *Client) DiscoverTools(ctx context.Context) ([]ToolSchema, error) {
	discovered, err := c.discoverToolsWithOrigins(ctx)
	tools := make([]ToolSchema, 0, len(discovered))
	for _, item := range discovered {
		tools = append(tools, item.Schema)
	}
	return tools, err
}

func (c *Client) discoverToolsWithOrigins(ctx context.Context) ([]discoveredTool, error) {
	servers := c.ListServers()
	var allTools []discoveredTool
	var errs []string
	owners := make(map[string]string)

	for _, srv := range servers {
		serverTools, err := c.discoverFromServer(ctx, srv)
		if err != nil {
			log.Printf("mcp: discover tools from %s failed: %v", srv.Name, err)
			errs = append(errs, fmt.Sprintf("%s: %v", srv.Name, err))
			continue
		}
		for index, schema := range serverTools {
			if err := validateDiscoveredToolSchema(schema); err != nil {
				errs = append(errs, fmt.Sprintf("%s tool[%d]: %v", srv.Name, index, err))
				continue
			}
			if owner, exists := owners[schema.Name]; exists {
				errs = append(errs, fmt.Sprintf("tool name %q is declared by both %s and %s", schema.Name, owner, srv.Name))
				continue
			}
			owners[schema.Name] = srv.Name
			allTools = append(allTools, discoveredTool{Schema: schema, ServerName: srv.Name})
		}
	}

	if len(errs) > 0 {
		return allTools, fmt.Errorf("MCP discovery failures: %s", strings.Join(errs, "; "))
	}

	return allTools, nil
}

func validateDiscoveredToolSchema(schema ToolSchema) error {
	if schema.Name == "" || schema.Name != strings.TrimSpace(schema.Name) {
		return fmt.Errorf("tool name must be a non-empty exact value")
	}
	if schema.Description == "" || schema.Description != strings.TrimSpace(schema.Description) {
		return fmt.Errorf("tool description must be a non-empty exact value")
	}
	if schema.InputSchema == nil {
		return fmt.Errorf("tool input schema is required")
	}
	if schemaType, ok := schema.InputSchema["type"].(string); !ok || schemaType != "object" {
		return fmt.Errorf("tool input schema type must be object")
	}
	return nil
}

func (c *Client) discoverFromServer(ctx context.Context, srv ServerConfig) ([]ToolSchema, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.Endpoint+"/tools", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if srv.AuthToken != "" {
		req.Header.Set("X-Proxy-Token", srv.AuthToken)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if readErr != nil {
			return nil, fmt.Errorf("status=%d and failed to read response body: %w", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	const maxDiscoveryResponseBytes = 5 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxDiscoveryResponseBytes {
		return nil, fmt.Errorf("tool discovery response exceeds %d bytes", maxDiscoveryResponseBytes)
	}
	var result struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			Parameters  map[string]interface{} `json:"parameters"`
		} `json:"tools"`
	}
	if err := jsoncontract.Decode(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	tools := make([]ToolSchema, 0, len(result.Tools))
	for _, t := range result.Tools {
		tools = append(tools, ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	return tools, nil
}

func (c *Client) ExecuteTool(ctx context.Context, serverName, toolName string, args json.RawMessage) (json.RawMessage, error) {
	if serverName == "" || serverName != strings.TrimSpace(serverName) || toolName == "" || toolName != strings.TrimSpace(toolName) {
		return nil, fmt.Errorf("MCP server and tool names must be non-empty exact values")
	}
	var argumentObject map[string]interface{}
	if err := jsoncontract.Decode(args, &argumentObject); err != nil {
		return nil, fmt.Errorf("MCP tool arguments must be a strict JSON object: %w", err)
	}
	if argumentObject == nil {
		return nil, fmt.Errorf("MCP tool arguments must be a JSON object")
	}
	c.mu.RLock()
	srv, ok := c.configs[serverName]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("mcp server %q not found", serverName)
	}

	payload := map[string]interface{}{
		"tool_name": toolName,
		"args":      args,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode tool request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.Endpoint+"/execute-tool", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if srv.AuthToken != "" {
		req.Header.Set("X-Proxy-Token", srv.AuthToken)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if err := jsoncontract.Validate(respBody); err != nil {
		return nil, fmt.Errorf("tool response is not strict JSON: %w", err)
	}

	return json.RawMessage(respBody), nil
}
