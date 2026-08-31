package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

func (l *LLMClient) chatGoogle(ctx context.Context, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	req := geminiRequest{
		Contents: []geminiContent{},
	}

	if bundle.Policy != "" || bundle.PolicyAppendix != "" {
		systemText := bundle.Policy
		if bundle.PolicyAppendix != "" {
			systemText += "\n\n" + bundle.PolicyAppendix
		}
		req.SystemInstruction = &geminiContent{
			Role:  "user",
			Parts: []geminiPart{{Text: systemText}},
		}
	}

	toolNames := make(map[string]string)
	// Runtime context precedes history and uses the same transport wrapper as
	// the OpenAI/Anthropic providers, so continuation turns cannot produce
	// consecutive user-role contents.
	for _, c := range bundle.RuntimeContext {
		req.Contents = append(req.Contents, geminiContent{Role: "user", Parts: []geminiPart{{Text: fmt.Sprintf("[runtime_context role=%s name=%s]\n%s", runtimeContextTransportRole(c), c.Name, c.Content)}}})
	}
	if bundle.Task != "" {
		req.Contents = append(req.Contents, geminiContent{Role: "user", Parts: []geminiPart{{Text: bundle.Task}}})
	}
	for _, h := range bundle.History {
		if h.Role == LLMRoleUser {
			req.Contents = append(req.Contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: h.Content}},
			})
		} else if h.Role == LLMRoleAssistant {
			parts := make([]geminiPart, 0, len(h.ToolCalls)+1)
			if strings.TrimSpace(h.Content) != "" {
				parts = append(parts, geminiPart{Text: h.Content})
			}
			if len(h.ToolCalls) > 0 {
				for _, tc := range h.ToolCalls {
					var args map[string]interface{}
					if err := jsoncontract.Decode([]byte(tc.Function.Arguments), &args); err != nil {
						return nil, fmt.Errorf("Google request tool call %s has invalid arguments: %w", tc.ID, err)
					}
					toolNames[tc.ID] = tc.Function.Name
					parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
					}, ThoughtSignature: tc.Function.ThoughtSignature})
				}
			}
			req.Contents = append(req.Contents, geminiContent{
				Role:  "model",
				Parts: parts,
			})
		} else if h.Role == LLMRoleTool {
			var response map[string]interface{}
			if err := jsoncontract.Decode([]byte(h.Content), &response); err != nil {
				return nil, fmt.Errorf("Google request tool result %s is not a JSON object: %w", h.ToolCallID, err)
			}
			toolName := h.ToolCallName
			if strings.TrimSpace(toolName) != toolName {
				return nil, fmt.Errorf("Google request tool result %s has a padded tool name", h.ToolCallID)
			}
			if toolName == "" {
				toolName = toolNames[h.ToolCallID]
			}
			if toolName == "" {
				return nil, fmt.Errorf("Google request cannot match tool result %s to a tool name", h.ToolCallID)
			}
			req.Contents = append(req.Contents, geminiContent{Role: "user", Parts: []geminiPart{{FunctionResponse: &geminiFunctionResponse{Name: toolName, Response: response}}}})
		}
	}

	if len(toolSpecs) > 0 {
		tool := geminiTool{
			FunctionDeclarations: []geminiFunctionDeclaration{},
		}
		for _, ts := range toolSpecs {
			parameters, err := StripAdditionalProperties(ts.Function.Parameters)
			if err != nil {
				return nil, fmt.Errorf("Google tool %s schema: %w", ts.Function.Name, err)
			}
			tool.FunctionDeclarations = append(tool.FunctionDeclarations, geminiFunctionDeclaration{
				Name:        ts.Function.Name,
				Description: ts.Function.Description,
				Parameters:  parameters,
			})
		}
		req.Tools = []geminiTool{tool}
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize Google request: %w", err)
	}
	log.Printf("Gemini request model=%s contents=%d tools=%d", l.model, len(req.Contents), len(toolSpecs))

	endpoint, err := googleGenerateContentEndpoint(l.model)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := config.Cfg.LLMAPIKey; apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}

	resp, err := l.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Google response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{Operation: "Google API error", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBytes))}
	}

	var gResp geminiResponse
	if err := jsoncontract.Validate(respBytes); err != nil {
		return nil, fmt.Errorf("failed to validate Google response: %w", err)
	}
	if err := json.Unmarshal(respBytes, &gResp); err != nil {
		return nil, err
	}

	if len(gResp.Candidates) == 0 {
		return nil, fmt.Errorf("Google API returned no candidates")
	}

	candidate := gResp.Candidates[0]
	// A non-STOP finish reason (e.g. MAX_TOKENS) with usable content should
	// not discard an otherwise valid partial response.
	if candidate.FinishReason != "STOP" {
		log.Printf("Gemini finish_reason=%s parts=%d", candidate.FinishReason, len(candidate.Content.Parts))
		if len(candidate.Content.Parts) == 0 {
			return nil, fmt.Errorf("Google API did not complete normally: finish_reason=%s", candidate.FinishReason)
		}
	}
	llmResp := &LLMResponse{
		Choices: []LLMChoice{{Index: 0}},
		Usage:   LLMUsage{PromptTokens: gResp.UsageMetadata.PromptTokenCount, CompletionTokens: gResp.UsageMetadata.CandidatesTokenCount, TotalTokens: gResp.UsageMetadata.TotalTokenCount},
	}

	msg := LLMMessage{Role: LLMRoleAssistant}
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			msg.Content += part.Text
		}
		if part.FunctionCall != nil {
			argsBytes, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize Google tool call %s arguments: %w", part.FunctionCall.Name, err)
			}
			msg.ToolCalls = append(msg.ToolCalls, LLMToolCall{
				ID:   "call_" + uuid.New().String(),
				Type: "function",
				Function: LLMFunctionCall{
					Name:             part.FunctionCall.Name,
					Arguments:        string(argsBytes),
					ThoughtSignature: part.ThoughtSignature,
				},
			})
		}
	}
	llmResp.Choices[0].Message = msg

	if len(msg.ToolCalls) > 0 {
		llmResp.Choices[0].FinishReason = "tool_calls"
	} else {
		llmResp.Choices[0].FinishReason = "stop"
	}

	return llmResp, nil
}

func googleGenerateContentEndpoint(model string) (string, error) {
	if config.Cfg == nil {
		return "", fmt.Errorf("LLM config is not initialized")
	}
	base := config.Cfg.LLMAPIEndpoint
	if base == "" {
		return "", fmt.Errorf("LLM_API_ENDPOINT not configured")
	}
	// The API key travels in the x-goog-api-key header, never in the URL:
	// transport errors echo the full URL, which would leak the secret into logs.
	base = strings.ReplaceAll(base, "{model}", url.PathEscape(model))
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid Google API endpoint: %w", err)
	}
	if parsed.Query().Get("key") != "" {
		return "", fmt.Errorf("LLM_API_ENDPOINT must not carry the API key in a query parameter; use the LLM_API_KEY config instead")
	}
	return parsed.String(), nil
}
