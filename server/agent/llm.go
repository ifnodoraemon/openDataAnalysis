package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
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
	provider := "openai"
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
	if baseURL != "" && baseURL != "https://api.anthropic.com" {
		opts = append(opts, anthropic.WithBaseURL(baseURL))
	}

	l.anthropicClient = anthropic.NewClient(config.Cfg.LLMAPIKey, opts...)
}

// SimpleChatFunc 返回一个简单的聊天函数，适配 data.LLMChatFunc 签名。
// 用于语义预分析等不需要 tool calling 的场景。
func (l *LLMClient) SimpleChatFunc() func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		bundle := &PromptBundle{
			Policy: systemPrompt,
			Task:   userPrompt,
		}
		resp, err := l.ChatWithTools(ctx, bundle, nil)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("LLM returned empty response")
		}
		return resp.Choices[0].Message.Content, nil
	}
}

// isRetryableLLMError 判断 LLM 调用错误是否属于可重试的瞬态错误。
// 4xx（认证/权限/请求格式错误）不可重试；5xx 和网络层错误可重试。
func isRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 非重试：客户端错误（4xx）
	for _, code := range []string{"status=400", "status=401", "status=403", "status=404", "status=422"} {
		if strings.Contains(msg, code) {
			return false
		}
	}
	// 可重试：常见瞬态错误
	if strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "status=429") || // rate limit
		strings.Contains(msg, "status=500") ||
		strings.Contains(msg, "status=502") ||
		strings.Contains(msg, "status=503") ||
		strings.Contains(msg, "status=504") {
		return true
	}
	// context 取消不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return false
}

// ChatWithTools 统一的调用接口，包含对底层网络不稳定的重试逻辑（指数退避，区分可重试错误）
func (l *LLMClient) ChatWithTools(ctx context.Context, bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*LLMResponse, error) {
	if config.Cfg == nil {
		return nil, fmt.Errorf("LLM config is not initialized")
	}
	if strings.TrimSpace(config.Cfg.LLMAPIKey) == "" || config.IsPlaceholderValue(config.Cfg.LLMAPIKey) {
		return nil, fmt.Errorf("LLM_API_KEY is not configured")
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
		default:
			resp, err = l.chatOpenAI(retryCtx, bundle, toolSpecs)
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		responsePath := llmDebugWriter.WriteBlob(span, "response.error.txt", body)
		l.debugLog(span, "llm.error", map[string]interface{}{
			"status":          resp.StatusCode,
			"duration_ms":     time.Since(start).Milliseconds(),
			"error_preview":   clipText(string(body), 500),
			"response_bytes":  len(body),
			"response_sha256": blobSHA256(body),
			"response_path":   responsePath,
		})
		return nil, fmt.Errorf("OpenAI Responses API call failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	endpoint := strings.TrimSpace(config.Cfg.LLMAPIEndpoint)
	baseURL := strings.TrimSpace(config.Cfg.LLMBaseURL)
	if endpoint == "" {
		if baseURL == "" {
			return "", "", fmt.Errorf("LLM_API_ENDPOINT not configured")
		}
		endpoint = defaultOpenAIEndpointForBase(baseURL)
	}

	if isDeepSeekEndpoint(endpoint) || isDeepSeekEndpoint(baseURL) {
		chatEndpoint := endpoint
		if isDeepSeekEndpoint(baseURL) && !isDeepSeekEndpoint(endpoint) {
			chatEndpoint = defaultOpenAIEndpointForBase(baseURL)
		}
		return normalizeChatCompletionsEndpoint(chatEndpoint), openAIAPIChatCompletions, nil
	}
	lowered := strings.ToLower(endpoint)
	if strings.Contains(lowered, "/chat/completions") {
		return endpoint, openAIAPIChatCompletions, nil
	}
	if strings.Contains(lowered, "/responses") {
		return endpoint, openAIAPIResponses, nil
	}
	if isBaseLikeEndpoint(endpoint) {
		return defaultOpenAIEndpointForBase(endpoint), openAIAPIChatCompletions, nil
	}
	return endpoint, openAIAPIResponses, nil
}

func defaultOpenAIEndpointForBase(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && strings.EqualFold(parsed.Hostname(), "api.openai.com") {
		return trimmed + "/v1/responses"
	}
	if isDeepSeekEndpoint(trimmed) {
		return trimmed + "/chat/completions"
	}
	if strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		return trimmed + "/chat/completions"
	}
	return trimmed + "/v1/chat/completions"
}

func isDeepSeekEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.deepseek.com" || strings.HasSuffix(host, ".deepseek.com")
}

func isBaseLikeEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	path := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	return path == "" || path == "v1"
}

func normalizeChatCompletionsEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return trimmed
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(strings.ToLower(path), "/chat/completions"):
		return parsed.String()
	case path == "" || path == "/":
		parsed.Path = "/chat/completions"
	case strings.EqualFold(path, "/v1"):
		parsed.Path = "/v1/chat/completions"
	case strings.HasSuffix(strings.ToLower(path), "/responses"):
		parsed.Path = path[:len(path)-len("/responses")] + "/chat/completions"
	default:
		parsed.Path = path + "/chat/completions"
	}
	return parsed.String()
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
	Message      *responsesMessage     `json:"message"`
	Content      interface{}           `json:"content"`
	Text         interface{}           `json:"text"`
}

type responsesAPIUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
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

type responsesMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type responsesSSEEvent struct {
	Type     string                `json:"type"`
	Response *responsesAPIResponse `json:"response"`
	Error    *responsesAPIError    `json:"error"`
	Delta    string                `json:"delta"`
}

func parseResponsesBody(body []byte) (*responsesAPIResponse, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "data:") {
		return parseResponsesSSE(trimmed)
	}

	var apiResp responsesAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}
	apiResp.normalize()
	return &apiResp, nil
}

func parseResponsesSSE(body string) (*responsesAPIResponse, error) {
	var apiResp responsesAPIResponse
	var deltas []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event responsesSSEEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Response != nil {
			apiResp = *event.Response
		}
		if event.Error != nil {
			apiResp.Error = event.Error
		}
		if event.Type == "response.output_text.delta" && event.Delta != "" {
			deltas = append(deltas, event.Delta)
		}
	}
	if apiResp.ID == "" && apiResp.Error == nil && len(deltas) == 0 {
		return nil, fmt.Errorf("no parseable Responses SSE events")
	}
	if strings.TrimSpace(apiResp.OutputText) == "" && len(deltas) > 0 {
		apiResp.OutputText = strings.Join(deltas, "")
	}
	apiResp.normalize()
	return &apiResp, nil
}

func (r *responsesAPIResponse) normalize() {
	if r == nil {
		return
	}
	if r.Usage.InputTokens == 0 {
		r.Usage.InputTokens = r.Usage.PromptTokens
	}
	if r.Usage.OutputTokens == 0 {
		r.Usage.OutputTokens = r.Usage.CompletionTokens
	}
	if r.Usage.TotalTokens == 0 {
		r.Usage.TotalTokens = r.Usage.InputTokens + r.Usage.OutputTokens
	}
	if strings.TrimSpace(r.OutputText) == "" {
		r.OutputText = firstText(contentToText(r.Text), contentToText(r.Content), messageText(r.Message))
	}
}

func (r *responsesAPIResponse) isEmptyOutput() bool {
	if r == nil {
		return true
	}
	if strings.TrimSpace(r.OutputText) != "" || strings.TrimSpace(contentToText(r.Text)) != "" {
		return false
	}
	if strings.TrimSpace(contentToText(r.Content)) != "" || strings.TrimSpace(messageText(r.Message)) != "" {
		return false
	}
	return len(r.Output) == 0
}

func hasPromptMismatch(requestInstructions, responseInstructions string) bool {
	requestInstructions = strings.TrimSpace(requestInstructions)
	responseInstructions = strings.TrimSpace(responseInstructions)
	if requestInstructions == "" || responseInstructions == "" {
		return false
	}
	return requestInstructions != responseInstructions
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func messageText(message *responsesMessage) string {
	if message == nil {
		return ""
	}
	return contentToText(message.Content)
}

func contentToText(content interface{}) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if text, ok := itemMap["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
					continue
				}
				if text, ok := itemMap["content"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		if text, ok := typed["text"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
		if text, ok := typed["content"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
		if text, ok := typed["value"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
		return ""
	default:
		return ""
	}
}

func (l *LLMClient) buildResponsesRequest(bundle *PromptBundle, toolSpecs []tools.ToolSpec) (*responsesAPIRequest, error) {
	req := &responsesAPIRequest{
		Model: l.model,
	}
	if config.Cfg != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(l.model)), "gpt-5") {
		if effort := strings.TrimSpace(config.Cfg.LLMReasoningEffort); effort != "" {
			req.Reasoning = &responsesReasoning{Effort: effort}
		}
		if verbosity := strings.TrimSpace(config.Cfg.LLMTextVerbosity); verbosity != "" {
			req.Text = &responsesText{Verbosity: verbosity}
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		responsePath := llmDebugWriter.WriteBlob(span, "response.error.txt", body)
		l.debugLog(span, "llm.error", map[string]interface{}{
			"status":          resp.StatusCode,
			"duration_ms":     time.Since(start).Milliseconds(),
			"error_preview":   clipText(string(body), 500),
			"response_bytes":  len(body),
			"response_sha256": blobSHA256(body),
			"response_path":   responsePath,
		})
		return nil, fmt.Errorf("OpenAI Chat Completions API call failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Chat Completions body: %w", err)
	}
	responsePath := llmDebugWriter.WriteBlob(span, "response.json", respBytes)

	var apiResp chatCompletionsResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse Chat Completions body: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("OpenAI Chat Completions API call failed: code=%s message=%s", apiResp.Error.Code, apiResp.Error.Message)
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
		if effort := strings.TrimSpace(config.Cfg.LLMReasoningEffort); effort != "" {
			req.ReasoningEffort = effort
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
	resp.normalize()
	choice := LLMChoice{
		Index: 0,
		Message: LLMMessage{
			Role: LLMRoleAssistant,
		},
		FinishReason: LLMFinishReasonStop,
	}

	var textParts []string
	if strings.TrimSpace(resp.OutputText) != "" {
		// 如果有聚合好的文本，则直接使用，避免和 message 分块重复
		textParts = append(textParts, resp.OutputText)
	}
	if strings.TrimSpace(resp.OutputText) == "" {
		if text := contentToText(resp.Content); text != "" {
			textParts = append(textParts, text)
		}
		if text := messageText(resp.Message); text != "" {
			textParts = append(textParts, text)
		}
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			// 如果 OutputText 为空，才从零散的 message 块聚合文本
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
	choice.Message.Content = strings.TrimSpace(strings.Join(textParts, "\n"))
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
		finishReason := choice.FinishReason
		if finishReason == "" {
			finishReason = LLMFinishReasonStop
		}
		msg := LLMMessage{
			Role:             choice.Message.Role,
			Content:          strings.TrimSpace(choice.Message.Content),
			ReasoningContent: strings.TrimSpace(choice.Message.ReasoningContent),
		}
		if msg.Role == "" {
			msg.Role = LLMRoleAssistant
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
		if len(msg.ToolCalls) > 0 {
			finishReason = LLMFinishReasonToolCalls
		}
		out.Choices = append(out.Choices, LLMChoice{
			Index:        choice.Index,
			Message:      msg,
			FinishReason: finishReason,
		})
	}
	if len(out.Choices) == 0 {
		out.Choices = append(out.Choices, LLMChoice{
			Index: 0,
			Message: LLMMessage{
				Role: LLMRoleAssistant,
			},
			FinishReason: LLMFinishReasonStop,
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
	return l.convertAnthropicResponse(&resp), nil
}

// convertAnthropicResponse 将 Anthropic 响应转换为内部统一格式
func (l *LLMClient) convertAnthropicResponse(resp *anthropic.MessagesResponse) *LLMResponse {
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
			if block.ID != "" {
				argsBytes, _ := json.Marshal(block.Input)
				choice.Message.ToolCalls = append(choice.Message.ToolCalls, LLMToolCall{
					ID:   block.ID,
					Type: LLMToolTypeFunction,
					Function: LLMFunctionCall{
						Name:      block.Name,
						Arguments: string(argsBytes),
					},
				})
			}
		}
	}

	choice.Message.Content = strings.Join(textParts, "\n")

	switch resp.StopReason {
	case "end_turn":
		choice.FinishReason = LLMFinishReasonStop
	case "tool_use":
		choice.FinishReason = LLMFinishReasonToolCalls
	default:
		choice.FinishReason = LLMFinishReasonStop
	}

	return &LLMResponse{
		Choices: []LLMChoice{choice},
		Usage: LLMUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}
