package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
	anthropic "github.com/liushuangls/go-anthropic/v2"
)

func TestConvertResponsesResponseMapsUsage(t *testing.T) {
	t.Parallel()

	client := &LLMClient{}
	resp := client.convertResponsesResponse(&responsesAPIResponse{
		OutputText: "done",
		Usage: responsesAPIUsage{
			InputTokens:  321,
			OutputTokens: 45,
			TotalTokens:  366,
		},
	})

	if resp.Usage.PromptTokens != 321 {
		t.Fatalf("expected prompt tokens 321, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 45 {
		t.Fatalf("expected completion tokens 45, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 366 {
		t.Fatalf("expected total tokens 366, got %d", resp.Usage.TotalTokens)
	}
}

func TestNewLLMClientUsesConfigurableTimeouts(t *testing.T) {
	previous := config.Cfg
	defer func() { config.Cfg = previous }()
	config.Cfg = &config.Config{
		LLMProvider:       "openai",
		LLMModel:          "gpt-4o",
		LLMHTTPTimeoutSec: 123,
		LLMRetryBudgetSec: 456,
	}

	client := NewLLMClient()
	if client.httpClient.Timeout != 123*time.Second {
		t.Fatalf("expected http timeout 123s, got %s", client.httpClient.Timeout)
	}
	if got := llmRetryBudget(); got != 456*time.Second {
		t.Fatalf("expected retry budget 456s, got %s", got)
	}
}

func TestNewLLMClientWithoutConfigDoesNotPanic(t *testing.T) {
	previous := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = previous })

	client := NewLLMClient()
	if client == nil || client.httpClient == nil {
		t.Fatal("expected client to initialize without global config")
	}
	_, err := client.ChatWithTools(context.Background(), &PromptBundle{Task: "hello"}, nil)
	if err == nil || !strings.Contains(err.Error(), "LLM config is not initialized") {
		t.Fatalf("expected missing config error, got %v", err)
	}
}

func TestChatWithToolsRejectsPlaceholderAPIKey(t *testing.T) {
	previous := config.Cfg
	config.Cfg = &config.Config{
		LLMProvider:    "openai",
		LLMModel:       "gpt-4o",
		LLMAPIKey:      "REPLACE_WITH_YOUR_API_KEY",
		LLMAPIEndpoint: "https://api.openai.com/v1/responses",
	}
	t.Cleanup(func() { config.Cfg = previous })

	client := NewLLMClient()
	_, err := client.ChatWithTools(context.Background(), &PromptBundle{Task: "hello"}, nil)
	if err == nil || !strings.Contains(err.Error(), "LLM_API_KEY is not configured") {
		t.Fatalf("expected missing api key error, got %v", err)
	}
}

func TestParseResponsesBodySupportsSSECompletedEvent(t *testing.T) {
	t.Parallel()

	body := []byte(`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output_text":"done","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}

data: [DONE]
`)
	resp, err := parseResponsesBody(body)
	if err != nil {
		t.Fatalf("expected SSE body to parse: %v", err)
	}
	if resp.OutputText != "done" {
		t.Fatalf("expected output_text done, got %q", resp.OutputText)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Fatalf("expected total tokens 5, got %d", resp.Usage.TotalTokens)
	}
}

func TestParseResponsesBodyAllowsTextConfigObject(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"id":"resp_bad_route",
		"status":"completed",
		"instructions":"codex cli prompt",
		"output_text":null,
		"output":[],
		"text":{"format":{"type":"text"},"verbosity":"medium"},
		"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}
	}`)

	resp, err := parseResponsesBody(body)
	if err != nil {
		t.Fatalf("expected text config object to parse: %v", err)
	}
	if resp.OutputText != "" {
		t.Fatalf("expected text config object not to become output text, got %q", resp.OutputText)
	}
	if !resp.isEmptyOutput() {
		t.Fatalf("expected response with only text config metadata to be empty")
	}
	if !hasPromptMismatch("data-analysis prompt", resp.Instructions) {
		t.Fatalf("expected prompt mismatch to remain detectable")
	}
}

func TestPromptMismatchOnlyFlagsNonEmptyDifference(t *testing.T) {
	t.Parallel()

	if hasPromptMismatch("data prompt", "data prompt") {
		t.Fatalf("expected identical prompts to match")
	}
	if hasPromptMismatch("", "other") {
		t.Fatalf("expected empty request prompt to skip mismatch detection")
	}
	if !hasPromptMismatch("data prompt", "codex prompt") {
		t.Fatalf("expected different non-empty prompts to mismatch")
	}
}

func TestConvertAnthropicResponseMapsUsage(t *testing.T) {
	t.Parallel()

	client := &LLMClient{}
	text := "done"
	resp := client.convertAnthropicResponse(&anthropic.MessagesResponse{
		Content: []anthropic.MessageContent{
			anthropic.NewTextMessageContent(text),
		},
		StopReason: "end_turn",
		Usage: anthropic.MessagesUsage{
			InputTokens:  111,
			OutputTokens: 22,
		},
	})

	if resp.Choices[0].FinishReason != LLMFinishReasonStop {
		t.Fatalf("expected stop finish reason, got %s", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 111 {
		t.Fatalf("expected prompt tokens 111, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 22 {
		t.Fatalf("expected completion tokens 22, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 133 {
		t.Fatalf("expected total tokens 133, got %d", resp.Usage.TotalTokens)
	}
}

func TestOpenAIBuildResponsesRequestFormatsRuntimeContext(t *testing.T) {
	t.Parallel()

	client := &LLMClient{model: "gpt-4o"}
	bundle := &PromptBundle{
		RuntimeContext: []RuntimeContextBlock{
			{Name: "active_subgoals", Role: "user", Content: "[g1] test_goal (pending)"},
		},
	}

	req, err := client.buildResponsesRequest(bundle, nil)
	if err != nil {
		t.Fatalf("expected no error building request: %v", err)
	}

	if len(req.Input) != 1 {
		t.Fatalf("expected 1 input, got %d", len(req.Input))
	}
	if req.Input[0]["role"] != "user" {
		t.Fatalf("expected runtime context to use user role, got %#v", req.Input[0]["role"])
	}
	expected := "[runtime_context role=user name=active_subgoals]\n[g1] test_goal (pending)"
	contentStr := req.Input[0]["content"].(string)
	if contentStr != expected {
		t.Fatalf("expected explicitly prefixed runtime core, got %q", contentStr)
	}
}

func TestBuildResponsesRequestIncludesStrictToolSpecs(t *testing.T) {
	t.Parallel()

	client := &LLMClient{model: "gpt-4o"}
	req, err := client.buildResponsesRequest(&PromptBundle{}, []tools.ToolSpec{
		{
			Type: "function",
			Function: tools.FunctionSpec{
				Name:        "report_create_chart",
				Description: "Create a chart",
				Parameters:  json.RawMessage(`{"type":"object"}`),
				Strict:      true,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildResponsesRequest: %v", err)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Tools))
	}
	if !req.Tools[0].Strict {
		t.Fatalf("expected strict tool flag to be preserved: %#v", req.Tools[0])
	}
	if req.Tools[0].Name != "report_create_chart" {
		t.Fatalf("unexpected tool name: %#v", req.Tools[0])
	}
}

func TestResolveOpenAIEndpointUsesDeepSeekChatCompletions(t *testing.T) {
	previous := config.Cfg
	defer func() { config.Cfg = previous }()
	config.Cfg = &config.Config{
		LLMBaseURL:     "https://api.deepseek.com",
		LLMAPIEndpoint: "https://api.openai.com/v1/responses",
	}

	client := &LLMClient{}
	endpoint, apiKind, err := client.resolveOpenAIEndpoint()
	if err != nil {
		t.Fatalf("resolveOpenAIEndpoint: %v", err)
	}
	if apiKind != openAIAPIChatCompletions {
		t.Fatalf("expected chat completions kind, got %s", apiKind)
	}
	if endpoint != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
}

func TestNormalizeDeepSeekResponsesEndpointToChatCompletions(t *testing.T) {
	previous := config.Cfg
	defer func() { config.Cfg = previous }()
	config.Cfg = &config.Config{
		LLMBaseURL:     "https://api.deepseek.com",
		LLMAPIEndpoint: "https://api.deepseek.com/v1/responses",
	}

	client := &LLMClient{}
	endpoint, apiKind, err := client.resolveOpenAIEndpoint()
	if err != nil {
		t.Fatalf("resolveOpenAIEndpoint: %v", err)
	}
	if apiKind != openAIAPIChatCompletions {
		t.Fatalf("expected chat completions kind, got %s", apiKind)
	}
	if endpoint != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
}

func TestBuildChatCompletionsRequestKeepsRuntimeContextInUserMessages(t *testing.T) {
	previous := config.Cfg
	defer func() { config.Cfg = previous }()
	config.Cfg = &config.Config{LLMReasoningEffort: "none", LLMMaxTokens: 4096}

	client := &LLMClient{model: "deepseek-v4-pro"}
	req, err := client.buildChatCompletionsRequest(&PromptBundle{
		Policy: "policy",
		RuntimeContext: []RuntimeContextBlock{
			{Name: "active_subgoals", Role: "user", Content: "[g1] inspect data"},
		},
		Task: "finish analysis",
	}, nil)
	if err != nil {
		t.Fatalf("buildChatCompletionsRequest: %v", err)
	}
	if req.ReasoningEffort != "none" {
		t.Fatalf("expected reasoning_effort none, got %q", req.ReasoningEffort)
	}
	if req.MaxTokens != 4096 {
		t.Fatalf("expected max_tokens 4096, got %d", req.MaxTokens)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected system, runtime context, and task messages, got %#v", req.Messages)
	}
	if req.Messages[0].Role != "system" {
		t.Fatalf("expected policy as system message, got %q", req.Messages[0].Role)
	}
	if req.Messages[1].Role != "user" {
		t.Fatalf("expected runtime context as user message for chat completions compatibility, got %q", req.Messages[1].Role)
	}
	if !strings.Contains(req.Messages[1].Content, "[runtime_context role=user name=active_subgoals]") {
		t.Fatalf("expected runtime context prefix, got %q", req.Messages[1].Content)
	}
}

func TestConvertChatCompletionsResponseMapsToolCallsAndUsage(t *testing.T) {
	t.Parallel()

	client := &LLMClient{}
	resp := client.convertChatCompletionsResponse(&chatCompletionsResponse{
		Choices: []chatCompletionChoice{
			{
				Index: 0,
				Message: chatCompletionMessage{
					Role: LLMRoleAssistant,
					ToolCalls: []chatCompletionToolCall{
						{
							ID:   "call_1",
							Type: LLMToolTypeFunction,
							Function: chatCompletionFunction{
								Name:      "data_list_tables",
								Arguments: `{}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	})
	if resp.Choices[0].FinishReason != LLMFinishReasonToolCalls {
		t.Fatalf("expected tool call finish reason, got %s", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", resp.Choices[0].Message.ToolCalls)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Fatalf("expected total tokens 15, got %d", resp.Usage.TotalTokens)
	}
}

func TestBuildAnthropicSystemPromptExcludesRuntimeContext(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy: "policy",
		RuntimeContext: []RuntimeContextBlock{
			{Name: "active_edit_scope", Role: "user", Content: "TargetBlockID: block-1"},
			{Name: "digest", Role: "user", Content: "- user: asked for update"},
		},
	}

	systemPrompt := buildAnthropicSystemPrompt(bundle)
	if !strings.Contains(systemPrompt, "policy") {
		t.Fatalf("expected policy in system prompt, got %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, "TargetBlockID: block-1") || strings.Contains(systemPrompt, "[digest]") {
		t.Fatalf("did not expect runtime facts in system prompt, got %q", systemPrompt)
	}
}

func TestBuildAnthropicMessagesKeepsUserRuntimeContextInUserStream(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		RuntimeContext: []RuntimeContextBlock{
			{Name: "active_edit_scope", Role: "user", Content: "TargetBlockID: block-1"},
			{Name: "digest", Role: "user", Content: "- user: asked for update"},
		},
		Task: "finish analysis",
	}

	msgs := buildAnthropicMessages(bundle)
	if len(msgs) != 1 {
		t.Fatalf("expected one user message, got %d", len(msgs))
	}
	if msgs[0].Role != anthropic.RoleUser {
		t.Fatalf("expected user role, got %q", msgs[0].Role)
	}
	if len(msgs[0].Content) != 3 {
		t.Fatalf("expected runtime context blocks and task content, got %#v", msgs[0].Content)
	}
	if msgs[0].Content[0].Text == nil || *msgs[0].Content[0].Text != "[runtime_context role=user name=active_edit_scope]\nTargetBlockID: block-1" {
		t.Fatalf("unexpected first content block: %#v", msgs[0].Content[0])
	}
	if msgs[0].Content[1].Text == nil || *msgs[0].Content[1].Text != "[runtime_context role=user name=digest]\n- user: asked for update" {
		t.Fatalf("unexpected second content block: %#v", msgs[0].Content[1])
	}
	if msgs[0].Content[2].Text == nil || *msgs[0].Content[2].Text != "finish analysis" {
		t.Fatalf("unexpected task content block: %#v", msgs[0].Content[2])
	}
}
