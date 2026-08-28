package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
	anthropic "github.com/liushuangls/go-anthropic/v2"
)

// LLMClient 统一的 LLM 客户端，支持 OpenAI 和 Anthropic
type LLMClient struct {
	provider        string
	model           string
	anthropicClient *anthropic.Client
	httpClient      *http.Client
}

// NewLLMClient 创建 LLM 客户端
func NewLLMClient() *LLMClient {
	provider := ""
	model := ""
	if config.Cfg != nil {
		provider = config.Cfg.LLMProvider
		model = config.Cfg.LLMModel
	}
	client := &LLMClient{
		provider: provider,
		model:    model,
		httpClient: &http.Client{
			Timeout: llmHTTPTimeout(),
		},
	}

	if config.Cfg != nil && client.provider == "anthropic" {
		client.initAnthropic()
	}

	return client
}

func llmHTTPTimeout() time.Duration {
	seconds := 240
	if config.Cfg != nil && config.Cfg.LLMHTTPTimeoutSec > 0 {
		seconds = config.Cfg.LLMHTTPTimeoutSec
	}
	return time.Duration(seconds) * time.Second
}

func llmRetryBudget() time.Duration {
	seconds := 360
	if config.Cfg != nil && config.Cfg.LLMRetryBudgetSec > 0 {
		seconds = config.Cfg.LLMRetryBudgetSec
	}
	return time.Duration(seconds) * time.Second
}

func (l *LLMClient) initAnthropic() {
	opts := []anthropic.ClientOption{}

	baseURL := config.Cfg.LLMBaseURL
	if baseURL != "" {
		opts = append(opts, anthropic.WithBaseURL(baseURL))
	}

	l.anthropicClient = anthropic.NewClient(config.Cfg.LLMAPIKey, opts...)
}

type retryableError interface {
	Retryable() bool
}

type httpStatusError struct {
	Operation  string
	StatusCode int
	Body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: status=%d body=%s", e.Operation, e.StatusCode, e.Body)
}

func (e *httpStatusError) Retryable() bool {
	return e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode == http.StatusConflict ||
		e.StatusCode == http.StatusTooEarly ||
		e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode >= http.StatusInternalServerError
}

func isTransientTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var declared retryableError
	if errors.As(err, &declared) {
		return declared.Retryable()
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() || netErr.Temporary() {
			return true
		}
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

// isRetryableLLMError uses typed transport and provider errors. It never
// classifies failures by matching human-readable error text.
func isRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	var requestErr *anthropic.RequestError
	if errors.As(err, &requestErr) {
		return (&httpStatusError{StatusCode: requestErr.StatusCode}).Retryable()
	}
	var apiErr *anthropic.APIError
	if errors.As(err, &apiErr) {
		return apiErr.IsRateLimitErr() || apiErr.IsApiErr() || apiErr.IsOverloadedErr()
	}
	return isTransientTransportError(err)
}

// ChatWithTools 统一的调用接口，包含对底层网络不稳定的重试逻辑（指数退避，区分可重试错误）
func (l *LLMClient) ChatWithTools(ctx context.Context, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	if err := validatePromptBundle(bundle); err != nil {
		return nil, err
	}
	if config.Cfg == nil {
		return nil, fmt.Errorf("LLM config is not initialized")
	}
	if strings.TrimSpace(config.Cfg.LLMAPIKey) == "" {
		return nil, fmt.Errorf("LLM_API_KEY is not configured")
	}
	if strings.TrimSpace(config.Cfg.LLMAPIKey) != config.Cfg.LLMAPIKey {
		return nil, fmt.Errorf("LLM_API_KEY must not contain leading or trailing whitespace")
	}

	retryCtx, retryCancel := context.WithTimeout(ctx, llmRetryBudget())
	defer retryCancel()

	var resp *LLMResponse
	var err error

	// 指数退避：1s, 3s, 8s（共 3 次重试，第 0 次无等待）
	retryDelays := []time.Duration{time.Second, 3 * time.Second, 8 * time.Second}

	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if retryCtx.Err() != nil {
			return nil, fmt.Errorf("LLM retry budget exceeded: %w", retryCtx.Err())
		}

		switch l.provider {
		case "anthropic":
			resp, err = l.chatAnthropic(retryCtx, bundle, toolSpecs)
		case "google":
			resp, err = l.chatGoogle(retryCtx, bundle, toolSpecs)
		case "openai":
			resp, err = l.chatOpenAI(retryCtx, bundle, toolSpecs)
		default:
			return nil, fmt.Errorf("unsupported LLM_PROVIDER %q", l.provider)
		}

		if err == nil {
			return resp, nil
		}

		// 不可重试的错误直接返回
		if !isRetryableLLMError(err) {
			return nil, err
		}

		if attempt < len(retryDelays) {
			log.Printf("LLM transient error (attempt %d, retry in %.0fs): %v", attempt+1, retryDelays[attempt].Seconds(), err)
			select {
			case <-retryCtx.Done():
				return nil, fmt.Errorf("LLM retry budget exceeded: %w", retryCtx.Err())
			case <-time.After(retryDelays[attempt]):
			}
		}
	}

	return nil, fmt.Errorf("LLM API request failed after %d retries: %v", len(retryDelays), err)
}

func countRuntimeContextChars(ctxs []RuntimeContextBlock) int {
	var total int
	for _, c := range ctxs {
		total += len([]rune(c.Content))
	}
	return total
}

func runtimeContextTransportRole(block RuntimeContextBlock) string {
	return LLMRoleUser
}

func responsesRuntimeContextRole(block RuntimeContextBlock) string {
	return LLMRoleUser
}

func countHistoryChars(hist []ConversationItem) int {
	var total int
	for _, h := range hist {
		total += len([]rune(h.Content))
	}
	return total
}

type openAIAPIKind string

const (
	openAIAPIResponses       openAIAPIKind = "responses"
	openAIAPIChatCompletions openAIAPIKind = "chat_completions"
)

// chatOpenAI 调用 OpenAI-compatible provider，按 endpoint 选择 Responses 或 Chat Completions。
func (l *LLMClient) chatOpenAI(ctx context.Context, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	endpoint, apiKind, err := l.resolveOpenAIEndpoint()
	if err != nil {
		return nil, err
	}
	if apiKind == openAIAPIChatCompletions {
		return l.chatOpenAIChatCompletions(ctx, endpoint, bundle, toolSpecs)
	}

	reqBody, err := l.buildResponsesRequest(bundle, toolSpecs)
	if err != nil {
		return nil, err
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize Responses request: %w", err)
	}
	start := time.Now()
	span := llmDebugWriter.StartSpan(TraceMetadataFromContext(ctx), "llm", l.provider, "", "")
	requestPath := llmDebugWriter.WriteBlob(span, "request.json", reqBytes)
	l.debugLog(span, "llm.request", map[string]interface{}{
		"provider":              l.provider,
		"model":                 l.model,
		"endpoint":              endpoint,
		"message_count":         len(bundle.History),
		"tool_count":            len(toolSpecs),
		"tools":                 summarizeTools(toolSpecs),
		"user_preview":          clipText(lastUserMessage(bundle.History), 240),
		"instruction_chars":     len([]rune(reqBody.Instructions)),
		"policy_chars":          len([]rune(bundle.Policy)),
		"task_chars":            len([]rune(bundle.Task)),
		"runtime_context_chars": countRuntimeContextChars(bundle.RuntimeContext),
		"history_chars":         countHistoryChars(bundle.History),
		"request_bytes":         len(reqBytes),
		"request_sha256":        blobSHA256(reqBytes),
		"request_path":          requestPath,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create Responses request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+config.Cfg.LLMAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI Responses API call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		responsePath := llmDebugWriter.WriteBlob(span, "response.error.txt", body)
		l.debugLog(span, "llm.error", map[string]interface{}{
			"status":          resp.StatusCode,
			"duration_ms":     time.Since(start).Milliseconds(),
			"error_preview":   clipText(string(body), 500),
			"response_bytes":  len(body),
			"response_sha256": blobSHA256(body),
			"response_path":   responsePath,
		})
		statusErr := &httpStatusError{Operation: "OpenAI Responses API call failed", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		if readErr != nil {
			return nil, errors.Join(statusErr, fmt.Errorf("failed to read error response: %w", readErr))
		}
		return nil, statusErr
	}

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Responses body: %w", err)
	}
	responsePath := llmDebugWriter.WriteBlob(span, "response.json", respBytes)

	apiResp, err := parseResponsesBody(respBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Responses body: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("OpenAI Responses API call failed: code=%s message=%s", apiResp.Error.Code, apiResp.Error.Message)
	}
	if err := validateResponsesAPIResponse(apiResp); err != nil {
		return nil, fmt.Errorf("invalid Responses API response: %w", err)
	}
	if hasPromptMismatch(reqBody.Instructions, apiResp.Instructions) && apiResp.isEmptyOutput() {
		l.debugLog(span, "llm.prompt_mismatch", map[string]interface{}{
			"request_instruction_preview":  clipText(reqBody.Instructions, 240),
			"response_instruction_preview": clipText(apiResp.Instructions, 240),
		})
		return nil, fmt.Errorf("upstream LLM gateway returned a response with mismatched instructions and no output; check model routing or gateway configuration")
	}
	l.debugLog(span, "llm.response", map[string]interface{}{
		"duration_ms":         time.Since(start).Milliseconds(),
		"output_preview":      clipText(apiResp.OutputText, 300),
		"output_chars":        len([]rune(apiResp.OutputText)),
		"item_count":          len(apiResp.Output),
		"status":              apiResp.Status,
		"instructions_match":  !hasPromptMismatch(reqBody.Instructions, apiResp.Instructions),
		"tool_call_count":     countResponsesToolCalls(apiResp.Output),
		"tool_calls":          responseToolNames(apiResp.Output),
		"usage_input_tokens":  apiResp.Usage.InputTokens,
		"usage_output_tokens": apiResp.Usage.OutputTokens,
		"usage_total_tokens":  apiResp.Usage.TotalTokens,
		"response_bytes":      len(respBytes),
		"response_sha256":     blobSHA256(respBytes),
		"response_path":       responsePath,
	})
	return l.convertResponsesResponse(apiResp), nil
}

func (l *LLMClient) resolveOpenAIEndpoint() (string, openAIAPIKind, error) {
	endpoint := config.Cfg.LLMAPIEndpoint
	if endpoint == "" {
		return "", "", fmt.Errorf("LLM_API_ENDPOINT not configured")
	}
	switch config.Cfg.LLMAPIProtocol {
	case string(openAIAPIChatCompletions):
		return endpoint, openAIAPIChatCompletions, nil
	case string(openAIAPIResponses):
		return endpoint, openAIAPIResponses, nil
	default:
		return "", "", fmt.Errorf("LLM_API_PROTOCOL must be responses or chat_completions for LLM_PROVIDER=openai")
	}
}

type responsesAPIRequest struct {
	Model        string              `json:"model"`
	Instructions string              `json:"instructions,omitempty"`
	Input        []responsesInput    `json:"input,omitempty"`
	Tools        []responsesTool     `json:"tools,omitempty"`
	ToolChoice   string              `json:"tool_choice,omitempty"`
	Reasoning    *responsesReasoning `json:"reasoning,omitempty"`
	Text         *responsesText      `json:"text,omitempty"`
}

type responsesInput map[string]interface{}

type chatCompletionsRequest struct {
	Model           string                  `json:"model"`
	Messages        []chatCompletionMessage `json:"messages"`
	Tools           []chatCompletionTool    `json:"tools,omitempty"`
	ToolChoice      string                  `json:"tool_choice,omitempty"`
	Stream          bool                    `json:"stream"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	MaxTokens       int                     `json:"max_tokens,omitempty"`
}

type chatCompletionMessage struct {
	Role             string                   `json:"role"`
	Content          string                   `json:"content,omitempty"`
	ReasoningContent string                   `json:"reasoning_content,omitempty"`
	ToolCalls        []chatCompletionToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string                   `json:"tool_call_id,omitempty"`
}

type chatCompletionToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function chatCompletionFunction `json:"function"`
}

type chatCompletionFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionTool struct {
	Type     string                     `json:"type"`
	Function chatCompletionToolFunction `json:"function"`
}

type chatCompletionToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
	Strict      bool        `json:"strict,omitempty"`
}

type chatCompletionsResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   chatCompletionUsage    `json:"usage"`
	Error   *responsesAPIError     `json:"error"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type responsesTool struct {
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
	Strict      bool        `json:"strict,omitempty"`
}

type responsesAPIResponse struct {
	ID           string                `json:"id"`
	Status       string                `json:"status"`
	Model        string                `json:"model"`
	Instructions string                `json:"instructions"`
	OutputText   string                `json:"output_text"`
	Output       []responsesOutputItem `json:"output"`
	Usage        responsesAPIUsage     `json:"usage"`
	Error        *responsesAPIError    `json:"error"`
}

type responsesAPIUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type responsesAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
}

type responsesOutputItem struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id"`
	CallID    string                   `json:"call_id"`
	Name      string                   `json:"name"`
	Arguments string                   `json:"arguments"`
	Role      string                   `json:"role"`
	Content   []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseResponsesBody(body []byte) (*responsesAPIResponse, error) {
	if err := jsoncontract.Validate(body); err != nil {
		return nil, err
	}
	var apiResp responsesAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}
	return &apiResp, nil
}

func validateResponsesAPIResponse(resp *responsesAPIResponse) error {
	if resp == nil {
		return fmt.Errorf("response is nil")
	}
	if resp.Status != "completed" {
		return fmt.Errorf("status must be completed, got %q", resp.Status)
	}
	for i, item := range resp.Output {
		switch item.Type {
		case "message":
			for j, content := range item.Content {
				if content.Type != "output_text" {
					return fmt.Errorf("output[%d].content[%d] has unsupported type %q", i, j, content.Type)
				}
			}
		case "function_call":
			if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
				return fmt.Errorf("output[%d] function_call requires call_id and name", i)
			}
			if strings.TrimSpace(item.CallID) != item.CallID || strings.TrimSpace(item.Name) != item.Name {
				return fmt.Errorf("output[%d] function_call identifiers must not be padded", i)
			}
			var args map[string]interface{}
			if err := jsoncontract.Decode([]byte(item.Arguments), &args); err != nil {
				return fmt.Errorf("output[%d] function_call arguments: %w", i, err)
			}
		default:
			return fmt.Errorf("output[%d] has unsupported type %q", i, item.Type)
		}
	}
	return nil
}

func (r *responsesAPIResponse) isEmptyOutput() bool {
	if r == nil {
		return true
	}
	if strings.TrimSpace(r.OutputText) != "" {
		return false
	}
	return len(r.Output) == 0
}

func hasPromptMismatch(requestInstructions, responseInstructions string) bool {
	if strings.TrimSpace(requestInstructions) == "" || strings.TrimSpace(responseInstructions) == "" {
		return false
	}
	return requestInstructions != responseInstructions
}

func (l *LLMClient) buildResponsesRequest(bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*responsesAPIRequest, error) {
	req := &responsesAPIRequest{
		Model: l.model,
	}
	if config.Cfg != nil {
		if config.Cfg.LLMReasoningEffort != "" {
			req.Reasoning = &responsesReasoning{Effort: config.Cfg.LLMReasoningEffort}
		}
		if config.Cfg.LLMTextVerbosity != "" {
			req.Text = &responsesText{Verbosity: config.Cfg.LLMTextVerbosity}
		}
	}

	if bundle.Policy != "" {
		instructions := bundle.Policy
		if bundle.PolicyAppendix != "" {
			instructions += "\n\n## Delegate Additional Constraints\n" + bundle.PolicyAppendix
		}
		req.Instructions = instructions
	}

	for _, block := range bundle.RuntimeContext {
		req.Input = append(req.Input, responsesInput{
			"role":    responsesRuntimeContextRole(block),
			"content": fmt.Sprintf("[runtime_context role=%s name=%s]\n%s", runtimeContextTransportRole(block), block.Name, block.Content),
		})
	}

	for _, msg := range bundle.History {
		switch msg.Role {
		case LLMRoleUser:
			req.Input = append(req.Input, responsesInput{
				"role":    "user",
				"content": msg.Content,
			})
		case LLMRoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				req.Input = append(req.Input, responsesInput{
					"role":    "assistant",
					"content": msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				req.Input = append(req.Input, responsesInput{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		case LLMRoleTool:
			req.Input = append(req.Input, responsesInput{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  msg.Content,
			})
		}
	}

	if bundle.Task != "" {
		req.Input = append(req.Input, responsesInput{
			"role":    "user",
			"content": bundle.Task,
		})
	}

	for _, tool := range toolSpecs {
		req.Tools = append(req.Tools, responsesTool{
			Type:        tool.Type,
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
			Strict:      tool.Function.Strict,
		})
	}
	if len(req.Tools) > 0 {
		req.ToolChoice = "auto"
	}

	return req, nil
}

func (l *LLMClient) chatOpenAIChatCompletions(ctx context.Context, endpoint string, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	reqBody, err := l.buildChatCompletionsRequest(bundle, toolSpecs)
	if err != nil {
		return nil, err
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize Chat Completions request: %w", err)
	}

	start := time.Now()
	span := llmDebugWriter.StartSpan(TraceMetadataFromContext(ctx), "llm", l.provider, "", "")
	requestPath := llmDebugWriter.WriteBlob(span, "request.json", reqBytes)
	l.debugLog(span, "llm.request", map[string]interface{}{
		"provider":              l.provider,
		"model":                 l.model,
		"endpoint":              endpoint,
		"api_kind":              string(openAIAPIChatCompletions),
		"message_count":         len(bundle.History),
		"tool_count":            len(toolSpecs),
		"tools":                 summarizeTools(toolSpecs),
		"user_preview":          clipText(lastUserMessage(bundle.History), 240),
		"instruction_chars":     len([]rune(buildChatSystemPrompt(bundle))),
		"policy_chars":          len([]rune(bundle.Policy)),
		"task_chars":            len([]rune(bundle.Task)),
		"runtime_context_chars": countRuntimeContextChars(bundle.RuntimeContext),
		"history_chars":         countHistoryChars(bundle.History),
		"request_bytes":         len(reqBytes),
		"request_sha256":        blobSHA256(reqBytes),
		"request_path":          requestPath,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create Chat Completions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+config.Cfg.LLMAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI Chat Completions API call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		responsePath := llmDebugWriter.WriteBlob(span, "response.error.txt", body)
		l.debugLog(span, "llm.error", map[string]interface{}{
			"status":          resp.StatusCode,
			"duration_ms":     time.Since(start).Milliseconds(),
			"error_preview":   clipText(string(body), 500),
			"response_bytes":  len(body),
			"response_sha256": blobSHA256(body),
			"response_path":   responsePath,
		})
		statusErr := &httpStatusError{Operation: "OpenAI Chat Completions API call failed", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		if readErr != nil {
			return nil, errors.Join(statusErr, fmt.Errorf("failed to read error response: %w", readErr))
		}
		return nil, statusErr
	}

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Chat Completions body: %w", err)
	}
	responsePath := llmDebugWriter.WriteBlob(span, "response.json", respBytes)

	if err := jsoncontract.Validate(respBytes); err != nil {
		return nil, fmt.Errorf("failed to validate Chat Completions body: %w", err)
	}
	var apiResp chatCompletionsResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse Chat Completions body: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("OpenAI Chat Completions API call failed: code=%s message=%s", apiResp.Error.Code, apiResp.Error.Message)
	}
	if err := validateChatCompletionsResponse(&apiResp); err != nil {
		return nil, fmt.Errorf("invalid Chat Completions response: %w", err)
	}
	converted := l.convertChatCompletionsResponse(&apiResp)
	outputPreview := ""
	if len(converted.Choices) > 0 {
		outputPreview = converted.Choices[0].Message.Content
	}
	l.debugLog(span, "llm.response", map[string]interface{}{
		"duration_ms":         time.Since(start).Milliseconds(),
		"output_preview":      clipText(outputPreview, 300),
		"output_chars":        len([]rune(outputPreview)),
		"item_count":          len(apiResp.Choices),
		"tool_call_count":     countChatCompletionToolCalls(apiResp.Choices),
		"tool_calls":          chatCompletionToolNames(apiResp.Choices),
		"usage_input_tokens":  apiResp.Usage.PromptTokens,
		"usage_output_tokens": apiResp.Usage.CompletionTokens,
		"usage_total_tokens":  apiResp.Usage.TotalTokens,
		"response_bytes":      len(respBytes),
		"response_sha256":     blobSHA256(respBytes),
		"response_path":       responsePath,
	})
	return converted, nil
}

func validateChatCompletionsResponse(resp *chatCompletionsResponse) error {
	if resp == nil || len(resp.Choices) == 0 {
		return fmt.Errorf("at least one choice is required")
	}
	for i, choice := range resp.Choices {
		if choice.Message.Role != LLMRoleAssistant {
			return fmt.Errorf("choice[%d] role must be assistant, got %q", i, choice.Message.Role)
		}
		switch choice.FinishReason {
		case LLMFinishReasonStop:
			if len(choice.Message.ToolCalls) != 0 {
				return fmt.Errorf("choice[%d] finish_reason stop conflicts with tool calls", i)
			}
		case LLMFinishReasonToolCalls:
			if len(choice.Message.ToolCalls) == 0 {
				return fmt.Errorf("choice[%d] finish_reason tool_calls requires tool calls", i)
			}
		default:
			return fmt.Errorf("choice[%d] has unsupported finish_reason %q", i, choice.FinishReason)
		}
		for j, toolCall := range choice.Message.ToolCalls {
			if toolCall.Type != LLMToolTypeFunction {
				return fmt.Errorf("choice[%d].tool_calls[%d] has unsupported type %q", i, j, toolCall.Type)
			}
			if strings.TrimSpace(toolCall.ID) == "" || strings.TrimSpace(toolCall.Function.Name) == "" {
				return fmt.Errorf("choice[%d].tool_calls[%d] requires id and function name", i, j)
			}
			if strings.TrimSpace(toolCall.ID) != toolCall.ID || strings.TrimSpace(toolCall.Function.Name) != toolCall.Function.Name {
				return fmt.Errorf("choice[%d].tool_calls[%d] identifiers must not be padded", i, j)
			}
			var args map[string]interface{}
			if err := jsoncontract.Decode([]byte(toolCall.Function.Arguments), &args); err != nil {
				return fmt.Errorf("choice[%d].tool_calls[%d] arguments: %w", i, j, err)
			}
		}
	}
	return nil
}

func buildChatSystemPrompt(bundle *PromptBundle) string {
	systemPrompt := bundle.Policy
	if bundle.PolicyAppendix != "" {
		systemPrompt += "\n\n## Delegate Additional Constraints\n" + bundle.PolicyAppendix
	}
	return systemPrompt
}

func (l *LLMClient) buildChatCompletionsRequest(bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*chatCompletionsRequest, error) {
	req := &chatCompletionsRequest{
		Model:  l.model,
		Stream: false,
	}
	if config.Cfg != nil {
		if config.Cfg.LLMReasoningEffort != "" {
			req.ReasoningEffort = config.Cfg.LLMReasoningEffort
		}
		if config.Cfg.LLMMaxTokens > 0 {
			req.MaxTokens = config.Cfg.LLMMaxTokens
		}
	}

	if systemPrompt := buildChatSystemPrompt(bundle); systemPrompt != "" {
		req.Messages = append(req.Messages, chatCompletionMessage{
			Role:    LLMRoleSystem,
			Content: systemPrompt,
		})
	}
	for _, block := range bundle.RuntimeContext {
		req.Messages = append(req.Messages, chatCompletionMessage{
			Role:    LLMRoleUser,
			Content: fmt.Sprintf("[runtime_context role=%s name=%s]\n%s", runtimeContextTransportRole(block), block.Name, block.Content),
		})
	}
	for _, msg := range bundle.History {
		switch msg.Role {
		case LLMRoleUser:
			req.Messages = append(req.Messages, chatCompletionMessage{
				Role:    LLMRoleUser,
				Content: msg.Content,
			})
		case LLMRoleAssistant:
			assistantMsg := chatCompletionMessage{
				Role:             LLMRoleAssistant,
				Content:          msg.Content,
				ReasoningContent: msg.ReasoningContent,
			}
			for _, tc := range msg.ToolCalls {
				assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, chatCompletionToolCall{
					ID:   tc.ID,
					Type: LLMToolTypeFunction,
					Function: chatCompletionFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
			if strings.TrimSpace(assistantMsg.Content) != "" || len(assistantMsg.ToolCalls) > 0 {
				req.Messages = append(req.Messages, assistantMsg)
			}
		case LLMRoleTool:
			req.Messages = append(req.Messages, chatCompletionMessage{
				Role:       LLMRoleTool,
				ToolCallID: msg.ToolCallID,
				Content:    msg.Content,
			})
		}
	}
	if bundle.Task != "" {
		req.Messages = append(req.Messages, chatCompletionMessage{
			Role:    LLMRoleUser,
			Content: bundle.Task,
		})
	}

	for _, tool := range toolSpecs {
		req.Tools = append(req.Tools, chatCompletionTool{
			Type: tool.Type,
			Function: chatCompletionToolFunction{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
				Strict:      tool.Function.Strict,
			},
		})
	}
	if len(req.Tools) > 0 {
		req.ToolChoice = "auto"
	}
	return req, nil
}

func (l *LLMClient) convertResponsesResponse(resp *responsesAPIResponse) *LLMResponse {
	choice := LLMChoice{
		Index: 0,
		Message: LLMMessage{
			Role: LLMRoleAssistant,
		},
		FinishReason: LLMFinishReasonStop,
	}

	var textParts []string
	if strings.TrimSpace(resp.OutputText) != "" {
		textParts = append(textParts, resp.OutputText)
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			if strings.TrimSpace(resp.OutputText) == "" {
				for _, content := range item.Content {
					if strings.TrimSpace(content.Text) != "" {
						textParts = append(textParts, content.Text)
					}
				}
			}
		case "function_call":
			choice.FinishReason = LLMFinishReasonToolCalls
			choice.Message.ToolCalls = append(choice.Message.ToolCalls, LLMToolCall{
				ID:   item.CallID,
				Type: LLMToolTypeFunction,
				Function: LLMFunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		default:
			l.debugLog(SpanInfo{}, "llm.output_item", map[string]interface{}{
				"type": item.Type,
				"name": item.Name,
				"id":   item.ID,
			})
		}
	}
	choice.Message.Content = strings.Join(textParts, "")
	return &LLMResponse{
		Choices: []LLMChoice{choice},
		Usage: LLMUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
}

func (l *LLMClient) convertChatCompletionsResponse(resp *chatCompletionsResponse) *LLMResponse {
	out := &LLMResponse{
		Usage: LLMUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	for _, choice := range resp.Choices {
		msg := LLMMessage{
			Role:             choice.Message.Role,
			Content:          choice.Message.Content,
			ReasoningContent: choice.Message.ReasoningContent,
		}
		for _, tc := range choice.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, LLMToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: LLMFunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		out.Choices = append(out.Choices, LLMChoice{
			Index:        choice.Index,
			Message:      msg,
			FinishReason: choice.FinishReason,
		})
	}
	return out
}

func (l *LLMClient) debugLog(span SpanInfo, event string, payload map[string]interface{}) {
	llmDebugWriter.WriteEvent(span, event, payload)
}

func summarizeTools(toolSpecs []tools.ToolSpec) []string {
	names := make([]string, 0, len(toolSpecs))
	for _, tool := range toolSpecs {
		names = append(names, tool.Function.Name)
	}
	return names
}

// clipText 已迁移至 stringutil.go

func firstAnthropicText(content []anthropic.MessageContent) string {
	for _, block := range content {
		if block.Type == "text" && block.Text != nil {
			return *block.Text
		}
	}
	return ""
}

func lastUserMessage(messages []ConversationItem) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == LLMRoleUser {
			return messages[i].Content
		}
	}
	return ""
}

func countResponsesToolCalls(items []responsesOutputItem) int {
	count := 0
	for _, item := range items {
		if item.Type == "function_call" {
			count++
		}
	}
	return count
}

func responseToolNames(items []responsesOutputItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type == "function_call" && strings.TrimSpace(item.Name) != "" {
			names = append(names, item.Name)
		}
	}
	return names
}

func countChatCompletionToolCalls(choices []chatCompletionChoice) int {
	count := 0
	for _, choice := range choices {
		count += len(choice.Message.ToolCalls)
	}
	return count
}

func chatCompletionToolNames(choices []chatCompletionChoice) []string {
	names := make([]string, 0)
	for _, choice := range choices {
		for _, tc := range choice.Message.ToolCalls {
			if strings.TrimSpace(tc.Function.Name) != "" {
				names = append(names, tc.Function.Name)
			}
		}
	}
	return names
}

func countAnthropicToolUses(content []anthropic.MessageContent) int {
	count := 0
	for _, block := range content {
		if block.Type == "tool_use" {
			count++
		}
	}
	return count
}

func anthropicToolNames(content []anthropic.MessageContent) []string {
	names := make([]string, 0, len(content))
	for _, block := range content {
		if block.Type == "tool_use" && strings.TrimSpace(block.Name) != "" {
			names = append(names, block.Name)
		}
	}
	return names
}

func buildAnthropicSystemPrompt(bundle *PromptBundle) string {
	systemPrompt := bundle.Policy
	if bundle.PolicyAppendix != "" {
		systemPrompt += "\n\n## Delegate Additional Constraints\n" + bundle.PolicyAppendix
	}
	return systemPrompt
}

func buildAnthropicMessages(bundle *PromptBundle) []anthropic.Message {
	var anthropicMsgs []anthropic.Message
	var currentUserContent []anthropic.MessageContent

	flushUserContent := func() {
		if len(currentUserContent) > 0 {
			anthropicMsgs = append(anthropicMsgs, anthropic.Message{
				Role:    anthropic.RoleUser,
				Content: currentUserContent,
			})
			currentUserContent = nil
		}
	}

	for _, block := range bundle.RuntimeContext {
		currentUserContent = append(currentUserContent, anthropic.NewTextMessageContent(
			fmt.Sprintf("[runtime_context role=%s name=%s]\n%s", LLMRoleUser, block.Name, block.Content),
		))
	}

	for _, msg := range bundle.History {
		switch msg.Role {
		case LLMRoleUser:
			currentUserContent = append(currentUserContent, anthropic.NewTextMessageContent(msg.Content))
		case LLMRoleAssistant:
			flushUserContent()
			var content []anthropic.MessageContent
			if msg.Content != "" {
				content = append(content, anthropic.NewTextMessageContent(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				inputRaw := json.RawMessage(tc.Function.Arguments)
				content = append(content, anthropic.NewToolUseMessageContent(tc.ID, tc.Function.Name, inputRaw))
			}
			if len(content) > 0 {
				anthropicMsgs = append(anthropicMsgs, anthropic.Message{
					Role:    anthropic.RoleAssistant,
					Content: content,
				})
			}
		case LLMRoleTool:
			currentUserContent = append(currentUserContent, anthropic.NewToolResultMessageContent(msg.ToolCallID, msg.Content, false))
		}
	}

	if bundle.Task != "" {
		currentUserContent = append(currentUserContent, anthropic.NewTextMessageContent(bundle.Task))
	}
	flushUserContent()
	return anthropicMsgs
}

// chatAnthropic Anthropic 格式调用，转换为统一的内部响应格式返回
func (l *LLMClient) chatAnthropic(ctx context.Context, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	span := llmDebugWriter.StartSpan(TraceMetadataFromContext(ctx), "llm", l.provider, "", "")

	systemPrompt := buildAnthropicSystemPrompt(bundle)
	anthropicMsgs := buildAnthropicMessages(bundle)

	// 转换内部工具定义: ToolSpec → Anthropic 格式
	var anthropicTools []anthropic.ToolDefinition
	for _, tool := range toolSpecs {
		anthropicTools = append(anthropicTools, anthropic.ToolDefinition{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}

	// 调用 Anthropic API
	req := anthropic.MessagesRequest{
		Model:     anthropic.Model(l.model),
		MaxTokens: 8192,
		Messages:  anthropicMsgs,
	}
	if systemPrompt != "" {
		req.System = systemPrompt
	}
	if len(anthropicTools) > 0 {
		req.Tools = anthropicTools
	}
	reqBytes, err := json.Marshal(req)
	if err == nil {
		requestPath := llmDebugWriter.WriteBlob(span, "request.json", reqBytes)
		l.debugLog(span, "llm.request", map[string]interface{}{
			"provider":              l.provider,
			"model":                 l.model,
			"endpoint":              config.Cfg.LLMAPIEndpoint,
			"message_count":         len(bundle.History),
			"tool_count":            len(toolSpecs),
			"tools":                 summarizeTools(toolSpecs),
			"user_preview":          clipText(lastUserMessage(bundle.History), 240),
			"instruction_chars":     len([]rune(systemPrompt)),
			"policy_chars":          len([]rune(bundle.Policy)),
			"task_chars":            len([]rune(bundle.Task)),
			"runtime_context_chars": countRuntimeContextChars(bundle.RuntimeContext),
			"history_chars":         countHistoryChars(bundle.History),
			"request_bytes":         len(reqBytes),
			"request_sha256":        blobSHA256(reqBytes),
			"request_path":          requestPath,
		})
	} else {
		l.debugLog(span, "llm.debug_serialize_error", map[string]interface{}{"error": err.Error()})
	}
	start := time.Now()

	resp, err := l.anthropicClient.CreateMessages(ctx, req)
	if err != nil {
		l.debugLog(span, "llm.error", map[string]interface{}{
			"duration_ms":   time.Since(start).Milliseconds(),
			"error_preview": clipText(err.Error(), 500),
		})
		return nil, fmt.Errorf("Anthropic API call failed: %w", err)
	}
	if respBytes, marshalErr := json.Marshal(resp); marshalErr == nil {
		responsePath := llmDebugWriter.WriteBlob(span, "response.json", respBytes)
		l.debugLog(span, "llm.response", map[string]interface{}{
			"duration_ms":         time.Since(start).Milliseconds(),
			"output_preview":      clipText(firstAnthropicText(resp.Content), 300),
			"output_chars":        len([]rune(firstAnthropicText(resp.Content))),
			"item_count":          len(resp.Content),
			"tool_call_count":     countAnthropicToolUses(resp.Content),
			"tool_calls":          anthropicToolNames(resp.Content),
			"usage_input_tokens":  resp.Usage.InputTokens,
			"usage_output_tokens": resp.Usage.OutputTokens,
			"usage_total_tokens":  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			"response_bytes":      len(respBytes),
			"response_sha256":     blobSHA256(respBytes),
			"response_path":       responsePath,
		})
	}

	// 转换响应: Anthropic → 内部统一格式
	return l.convertAnthropicResponse(&resp)
}

// convertAnthropicResponse 将 Anthropic 响应转换为内部统一格式
func (l *LLMClient) convertAnthropicResponse(resp *anthropic.MessagesResponse) (*LLMResponse, error) {
	choice := LLMChoice{
		Index: 0,
		Message: LLMMessage{
			Role: LLMRoleAssistant,
		},
	}

	var textParts []string

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if block.Text != nil {
				textParts = append(textParts, *block.Text)
			}
		case "tool_use":
			if strings.TrimSpace(block.ID) == "" || strings.TrimSpace(block.Name) == "" {
				return nil, fmt.Errorf("Anthropic tool_use requires id and name")
			}
			if strings.TrimSpace(block.ID) != block.ID || strings.TrimSpace(block.Name) != block.Name {
				return nil, fmt.Errorf("Anthropic tool_use identifiers must not be padded")
			}
			argsBytes, err := json.Marshal(block.Input)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize Anthropic tool call %s arguments: %w", block.ID, err)
			}
			choice.Message.ToolCalls = append(choice.Message.ToolCalls, LLMToolCall{
				ID:   block.ID,
				Type: LLMToolTypeFunction,
				Function: LLMFunctionCall{
					Name:      block.Name,
					Arguments: string(argsBytes),
				},
			})
		default:
			return nil, fmt.Errorf("Anthropic response has unsupported content type %q", block.Type)
		}
	}

	choice.Message.Content = strings.Join(textParts, "\n")

	switch resp.StopReason {
	case "end_turn":
		choice.FinishReason = LLMFinishReasonStop
	case "tool_use":
		choice.FinishReason = LLMFinishReasonToolCalls
	default:
		return nil, fmt.Errorf("Anthropic response has unsupported stop_reason %q", resp.StopReason)
	}
	if choice.FinishReason == LLMFinishReasonToolCalls && len(choice.Message.ToolCalls) == 0 {
		return nil, fmt.Errorf("Anthropic stop_reason tool_use requires a tool call")
	}
	if choice.FinishReason == LLMFinishReasonStop && len(choice.Message.ToolCalls) != 0 {
		return nil, fmt.Errorf("Anthropic stop_reason end_turn conflicts with tool calls")
	}

	return &LLMResponse{
		Choices: []LLMChoice{choice},
		Usage: LLMUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}, nil
}
