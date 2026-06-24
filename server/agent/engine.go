package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// Engine Agent 主循环引擎
type Engine struct {
	llm           *LLMClient
	registry      *tools.Registry
	policy        string
	history       []ConversationItem
	contextDigest string
	mu            sync.Mutex
}

const (
	contextBudgetTokens         = 128000
	contextCompactTriggerTokens = contextBudgetTokens * 9 / 10
	recentContextWindow         = 12
	historyDigestPrefix         = "=== History Digest ==="
	maxDigestBulletCount        = 24
	maxMainLoopIterations       = 50
)

type eventEmitterAware interface {
	SetEventEmitter(func(WSEvent))
}

type executionContextAware interface {
	SetExecutionContext(context.Context)
}

type specialToolHandler func(context.Context, LLMToolCall, string, func(WSEvent)) (string, error, bool)

// isRetryableToolError 判断工具执行错误是否属于可重试的网络临时故障。
func isRetryableToolError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset by peer")
}

// retryableToolExec 在工具执行层面对瞬态网络错误做最多 3 次指数退避重试。
// 注意：special handler（user_request_input / report_finalize）不经过此函数。
func retryableToolExec(ctx context.Context, registry *tools.Registry, toolName string, args json.RawMessage) (string, error) {
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var result string
	var execErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		result, execErr = registry.Execute(toolName, args)
		if execErr == nil || !isRetryableToolError(execErr) {
			return result, execErr
		}
		if attempt < len(delays) {
			log.Printf("Tool %s transient error (attempt %d): %v — retry after %s", toolName, attempt+1, execErr, delays[attempt])
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delays[attempt]):
			}
		}
	}
	return result, execErr
}

// compactWorkerBundle 对子代理消息历史做上下文压缩。
func compactWorkerBundle(bundle *PromptBundle, promptTokens int) {
	if len(bundle.History) <= 1 {
		return
	}
	if promptTokens <= 0 || promptTokens <= contextCompactTriggerTokens {
		return
	}

	recentStart := len(bundle.History) - recentContextWindow
	if recentStart <= 0 {
		return
	}

	recentStart = adjustCompactionBoundary(bundle.History, recentStart)

	existingDigest := ""
	for _, ctx := range bundle.RuntimeContext {
		if ctx.Name == "digest" {
			existingDigest = ctx.Content
		}
	}

	digest := buildHistoryDigest(existingDigest, bundle.History[:recentStart])
	if digest == "" {
		return
	}

	found := false
	for i := range bundle.RuntimeContext {
		if bundle.RuntimeContext[i].Name == "digest" {
			bundle.RuntimeContext[i].Content = digest
			if strings.TrimSpace(bundle.RuntimeContext[i].Role) == "" {
				bundle.RuntimeContext[i].Role = "user"
			}
			found = true
			break
		}
	}
	if !found {
		bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{Name: "digest", Role: "user", Content: digest})
	}

	bundle.History = bundle.History[recentStart:]
}

// NewEngine 创建 Agent 引擎（支持多轮对话）
func NewEngine(registry *tools.Registry, systemPrompt string) *Engine {
	if systemPrompt == "" {
		systemPrompt = BuildPolicyPrompt()
	}
	return &Engine{
		llm:      NewLLMClient(),
		registry: registry,
		policy:   systemPrompt,
	}
}

func (e *Engine) ResetMessages() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = nil
	e.contextDigest = ""
}

// RestoreHistory 从外部持久化存储恢复 LLM 执行历史
func (e *Engine) RestoreHistory(history []ConversationItem) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.history) == 0 {
		e.history = history
	}
}

// summarizeMessageForDigest 为历史摘要提取每条消息的最有价值片段。
// - tool result：优先使用 ui_summary/message 等语义字段（由 digestSummary 提取），不截断
// - assistant status/content：取末段结论（结论往往在末尾），而非首部截断
// - user / tool_call：取末段，400 字上限
func summarizeMessageForDigest(msg ConversationItem) string {
	switch msg.Role {
	case LLMRoleUser:
		if text := digestSummary(msg.Content, 400); text != "" {
			return "user: " + text
		}
	case LLMRoleAssistant:
		parts := make([]string, 0, len(msg.ToolCalls)+1)
		// 取 assistant 可见状态文本的末段结论，400 字
		if text := digestSummary(msg.Content, 400); text != "" {
			parts = append(parts, "assistant: "+text)
		}
		if len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			parts = append(parts, "tool calls: "+strings.Join(names, ", "))
		}
		return strings.Join(parts, " | ")
	case LLMRoleTool:
		// digestSummary 会优先提取 ui_summary 等语义字段，不截断语义完整的摘要
		rawSummary := extractToolSummary(msg.Content)
		if summary := digestSummary(rawSummary, 400); summary != "" {
			return "tool result: " + summary
		}
	}
	return ""
}

func extractToolSummary(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		if summary := buildStructuredToolSummary(payload); summary != "" {
			return summary
		}
		if result, ok := payload["result"].(string); ok && strings.TrimSpace(result) != "" {
			return result
		}
		if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
			return message
		}
		if summary, ok := payload["ui_summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return summary
		}
		if tool, ok := payload["tool"].(string); ok {
			return fmt.Sprintf("tool=%s", tool)
		}
	}
	return trimmed
}

func buildStructuredToolSummary(payload map[string]interface{}) string {
	parts := make([]string, 0, 8)
	if tool, ok := payload["tool"].(string); ok && strings.TrimSpace(tool) != "" {
		parts = append(parts, "tool="+strings.TrimSpace(tool))
	}
	if ok, exists := payload["ok"].(bool); exists {
		parts = append(parts, fmt.Sprintf("ok=%t", ok))
	}
	for _, key := range []string{
		"error_code",
		"action",
		"status",
		"memory_key",
		"table_name",
		"row_count",
		"table_count",
		"file_count",
		"fact_count",
		"goal_count",
		"goal_id",
		"active_branch_count",
		"active_roots",
		"affects_report_delivery",
		"run_status",
		"child_run_status",
		"child_result",
		"block_count",
		"chart_count",
		"delivery_state",
		"finalize_issue_count",
		"target_block_id",
		"child_run_id",
		"delegate_role",
		"report_title",
	} {
		if value, exists := payload[key]; exists {
			if part := formatSummaryField(key, value); part != "" {
				parts = append(parts, part)
			}
		}
	}
	return strings.Join(parts, ", ")
}

func formatSummaryField(key string, value interface{}) string {
	switch typed := value.(type) {
	case string:
		typed = strings.TrimSpace(typed)
		if typed == "" {
			return ""
		}
		return fmt.Sprintf("%s=%s", key, typed)
	case bool:
		return fmt.Sprintf("%s=%t", key, typed)
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%s=%d", key, int64(typed))
		}
		return fmt.Sprintf("%s=%g", key, typed)
	default:
		return ""
	}
}

func buildHistoryDigest(existing string, messages []ConversationItem) string {
	// 收集本轮新增的 bullet 条目（不含已有 digest 文本）
	bullets := make([]string, 0, len(messages))
	for _, msg := range messages {
		if summary := summarizeMessageForDigest(msg); summary != "" {
			bullets = append(bullets, "- "+summary)
		}
	}

	// 对新增 bullets 超限时截断，existing digest 整段保留不参与 bullet 计数
	if len(bullets) > maxDigestBulletCount {
		bullets = append(bullets[:maxDigestBulletCount-1], "- Earlier execution details have been compacted.")
	}

	// 拼接：existing digest（已有摘要）在前，新 bullets 在后
	// 注意：此函数返回纯 digest 内容（不含 historyDigestPrefix），
	// 由调用方在注入 RuntimeContext 时统一添加前缀，避免“摘要包摘要”的前缀累积。
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(existing); trimmed != "" {
		// RuntimeContext 注入时会带展示前缀；digest 正文内部保持无前缀。
		trimmed = strings.TrimPrefix(trimmed, historyDigestPrefix)
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(bullets) > 0 {
		parts = append(parts, strings.Join(bullets, "\n"))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func (e *Engine) compactMessagesLocked(promptTokens int) {
	if len(e.history) <= 1 {
		return
	}
	if promptTokens <= 0 || promptTokens <= contextCompactTriggerTokens {
		return
	}

	recentStart := len(e.history) - recentContextWindow
	if recentStart <= 0 {
		return
	}

	recentStart = adjustCompactionBoundary(e.history, recentStart)

	digest := buildHistoryDigest(e.contextDigest, e.history[:recentStart])
	if digest == "" {
		return
	}

	e.contextDigest = digest
	e.history = e.history[recentStart:]
}

func adjustCompactionBoundary(history []ConversationItem, boundary int) int {
	if boundary <= 0 || boundary >= len(history) {
		return boundary
	}
	msg := history[boundary]
	if msg.Role == LLMRoleTool {
		for i := boundary - 1; i >= 0 && i >= boundary-10; i-- {
			if history[i].Role == LLMRoleAssistant && len(history[i].ToolCalls) > 0 {
				toolCallCount := len(history[i].ToolCalls)
				resultCount := 0
				for j := i + 1; j < len(history) && j <= i+toolCallCount; j++ {
					if history[j].Role == LLMRoleTool {
						resultCount++
					} else {
						break
					}
				}
				if resultCount < toolCallCount {
					for k := i - 1; k >= 0 && k >= i-5; k-- {
						if history[k].Role == LLMRoleUser || (history[k].Role == LLMRoleAssistant && len(history[k].ToolCalls) == 0) {
							return k + 1
						}
					}
				}
				return i
			}
		}
	}
	if msg.Role == LLMRoleAssistant && len(msg.ToolCalls) > 0 {
		return boundary
	}
	return boundary
}

func (e *Engine) prepareRuntimeTools(ctx context.Context, emit func(WSEvent)) {
	if e.registry == nil {
		return
	}
	for _, tool := range e.registry.ListTools() {
		if next, ok := tool.(eventEmitterAware); ok {
			next.SetEventEmitter(emit)
		}
		if next, ok := tool.(executionContextAware); ok {
			next.SetExecutionContext(ctx)
		}
	}
}

func (e *Engine) specialToolHandlers() map[string]specialToolHandler {
	return map[string]specialToolHandler{
		"user_request_input": func(ctx context.Context, toolCall LLMToolCall, assistantContent string, emit func(WSEvent)) (string, error, bool) {
			payload, err := parseAskUserToolCallArguments(toolCall.Function.Arguments)
			if err != nil {
				// Fallback when question is missing from tool arguments but is provided in choice.Message.Content
				trimmedContent := strings.TrimSpace(assistantContent)
				if trimmedContent != "" {
					var args askUserToolCallArguments
					_ = json.Unmarshal([]byte(toolCall.Function.Arguments), &args)

					allowCustom := true
					if args.AllowCustom != nil {
						allowCustom = *args.AllowCustom
					}
					options, _ := validateAskUserOptions(args.Options)
					selectionMode, _ := normalizeAskUserSelectionMode(args.SelectionMode)

					payload = AskUserData{
						Question:      trimmedContent,
						Reason:        strings.TrimSpace(args.Reason),
						Scope:         strings.TrimSpace(args.Scope),
						ContextRef:    strings.TrimSpace(args.ContextRef),
						InputHint:     strings.TrimSpace(args.InputHint),
						Required:      args.Required,
						SelectionMode: selectionMode,
						AllowCustom:   allowCustom,
						Options:       options,
					}
					err = nil
				}
			}
			if err != nil {
				return "", err, true
			}
			emit(WSEvent{Type: EventUserRequestInput, Data: payload})
			return "", nil, true
		},
		"report_finalize": func(ctx context.Context, toolCall LLMToolCall, assistantContent string, emit func(WSEvent)) (string, error, bool) {
			result, err := e.registry.Execute(toolCall.Function.Name, json.RawMessage(toolCall.Function.Arguments))
			if err == nil && result != "" {
				result = applyReportHTMLGuardrail(result)
			}
			return result, err, false
		},
	}
}

// Run 执行 Agent 主循环
// userInput 为空字符串时表示从 user_request_input 挂起点恢复执行，
// 用户答案已通过 ProvideAskUserResult 注入历史，此时不再追加额外的 user 消息。
func (e *Engine) Run(ctx context.Context, userInput string, getRuntimeVars func() []RuntimeContextBlock, emit func(WSEvent)) {
	if emit == nil {
		emit = func(WSEvent) {}
	}
	e.prepareRuntimeTools(ctx, emit)
	specialHandlers := e.specialToolHandlers()

	e.mu.Lock()
	toolSpecs := e.registry.GetToolSpecs()
	e.mu.Unlock()

	userTask := userInput

	for i := 1; ; i++ {
		select {
		case <-ctx.Done():
			emit(WSEvent{Type: EventRunCancelled, Data: ErrorData{Message: "task cancelled"}})
			return
		default:
		}

		if i > maxMainLoopIterations {
			emit(WSEvent{Type: EventError, Data: ErrorData{Message: fmt.Sprintf("main loop exceeded max iterations %d", maxMainLoopIterations)}})
			return
		}

		e.mu.Lock()
		bundle := &PromptBundle{
			Policy: e.policy,
			Task:   userTask,
		}
		if getRuntimeVars != nil {
			bundle.RuntimeContext = append(bundle.RuntimeContext, getRuntimeVars()...)
		}
		if e.contextDigest != "" {
			digestBody := strings.TrimPrefix(strings.TrimSpace(e.contextDigest), historyDigestPrefix)
			digestBody = strings.TrimSpace(digestBody)
			if digestBody != "" {
				bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
					Name:    "digest",
					Role:    "user",
					Content: historyDigestPrefix + "\n" + digestBody,
				})
			}
		}
		bundle.History = append([]ConversationItem(nil), e.history...)
		e.mu.Unlock()

		resp, err := e.llm.ChatWithTools(ctx, bundle, toolSpecs)
		if err != nil {
			emit(WSEvent{Type: EventError, Data: ErrorData{Message: err.Error()}})
			return
		}

		e.mu.Lock()
		if userTask != "" {
			e.history = append(e.history, ConversationItem{
				Role:    LLMRoleUser,
				Content: userTask,
			})
			userTask = ""
		}
		e.compactMessagesLocked(resp.Usage.PromptTokens)
		e.mu.Unlock()

		if len(resp.Choices) == 0 {
			emit(WSEvent{Type: EventError, Data: ErrorData{Message: "LLM returned empty response"}})
			return
		}

		choice := resp.Choices[0]

		// 有文本内容时，推送模型面向用户的状态说明。
		if choice.Message.Content != "" {
			if len(choice.Message.ToolCalls) > 0 {
				emit(WSEvent{Type: EventAssistantStatus, Data: AssistantStatusData{Content: choice.Message.Content}})
			} else {
				// 有文本 + 无工具调用 → 最终回复
				e.mu.Lock()
				e.history = append(e.history, ConversationItem{
					Role:             LLMRoleAssistant,
					Content:          choice.Message.Content,
					ReasoningContent: choice.Message.ReasoningContent,
				})
				e.mu.Unlock()
				emit(WSEvent{Type: EventRunCompleted, Data: CompleteData{Summary: choice.Message.Content}})
				return
			}
		}

		// 如果 finish_reason 是 stop 且没有工具调用，结束
		if choice.FinishReason == LLMFinishReasonStop && len(choice.Message.ToolCalls) == 0 {
			if strings.TrimSpace(choice.Message.Content) == "" {
				emit(WSEvent{Type: EventError, Data: ErrorData{
					Message: "模型没有返回可展示的分析内容，请重试或检查当前 LLM 网关配置。",
					Code:    "empty_llm_output",
				}})
				return
			}
			e.mu.Lock()
			e.history = append(e.history, ConversationItem{
				Role:             LLMRoleAssistant,
				Content:          choice.Message.Content,
				ReasoningContent: choice.Message.ReasoningContent,
			})
			e.mu.Unlock()
			emit(WSEvent{Type: EventRunCompleted, Data: CompleteData{Summary: choice.Message.Content}})
			return
		}

		// 处理工具调用
		if len(choice.Message.ToolCalls) > 0 {
			// 将 assistant 消息加入历史
			e.mu.Lock()
			e.history = append(e.history, compactAssistantMessage(choice.Message))
			e.mu.Unlock()

			for _, toolCall := range choice.Message.ToolCalls {
				toolSpan := llmDebugWriter.StartSpan(
					TraceMetadataFromContext(ctx),
					"tool",
					toolCall.Function.Name,
					"",
					toolCall.ID,
				)

				// 通知前端: 工具调用
				emit(WSEvent{
					Type: EventToolCall,
					Data: ToolCallData{
						ID:        toolCall.ID,
						Name:      toolCall.Function.Name,
						Arguments: json.RawMessage(toolCall.Function.Arguments),
					},
				})
				argPath := llmDebugWriter.WriteBlob(toolSpan, "arguments.json", []byte(toolCall.Function.Arguments))
				llmDebugWriter.WriteEvent(toolSpan, "tool.call", map[string]interface{}{
					"tool_name":        toolCall.Function.Name,
					"tool_call_id":     toolCall.ID,
					"arguments_path":   argPath,
					"arguments_bytes":  len([]byte(toolCall.Function.Arguments)),
					"arguments_sha256": blobSHA256([]byte(toolCall.Function.Arguments)),
				})

				// 执行工具
				start := time.Now()

				var result string
				var execErr error

				if handler, ok := specialHandlers[toolCall.Function.Name]; ok {
					var stop bool
					result, execErr, stop = handler(ctx, toolCall, choice.Message.Content, emit)
					if execErr != nil && toolCall.Function.Name == "user_request_input" {
						emit(WSEvent{Type: EventError, Data: ErrorData{Message: execErr.Error()}})
						return
					}
					if stop {
						return
					}
				} else {
					result, execErr = retryableToolExec(ctx, e.registry, toolCall.Function.Name, json.RawMessage(toolCall.Function.Arguments))
				}

				duration := time.Since(start).Milliseconds()

				// If we got canceled during execution (or context ended), drop the result, abort tool loop, allow ctx.Done to catch in next loop
				if ctx.Err() != nil {
					return
				}

				success := toolCallSucceeded(result, execErr)
				if execErr != nil {
					sanitized := sanitizeToolError(execErr.Error())
					result = fmt.Sprintf("tool execution error: %s", sanitized)
					log.Printf("Tool %s error: %v", toolCall.Function.Name, execErr)
				}
				resultBytes := []byte(result)
				resultPath := llmDebugWriter.WriteBlob(toolSpan, "result.txt", resultBytes)
				llmDebugWriter.WriteEvent(toolSpan, "tool.result", map[string]interface{}{
					"tool_name":       toolCall.Function.Name,
					"tool_call_id":    toolCall.ID,
					"duration_ms":     duration,
					"success":         success,
					"result_preview":  clipText(result, 300),
					"result_bytes":    len(resultBytes),
					"result_sha256":   blobSHA256(resultBytes),
					"result_path":     resultPath,
					"execution_error": errorString(execErr),
				})

				// 通知前端: 工具结果
				emit(WSEvent{
					Type: EventToolResult,
					Data: ToolResultData{
						ID:       toolCall.ID,
						Name:     toolCall.Function.Name,
						Result:   result,
						Duration: duration,
						Success:  success,
					},
				})
				if ctx.Err() != nil {
					return
				}

				// 将工具结果加入消息历史
				e.mu.Lock()
				e.history = append(e.history, ConversationItem{
					Role:       LLMRoleTool,
					Content:    compactToolResult(toolCall.Function.Name, result),
					ToolCallID: toolCall.ID,
				})
				e.mu.Unlock()

				if toolCall.Function.Name == "report_finalize" && isSuccessfulFinalizeResult(result) {
					summary, summaryErr := e.finalResponseAfterFinalize(ctx, getRuntimeVars)
					if summaryErr != nil {
						emit(WSEvent{Type: EventError, Data: ErrorData{Message: summaryErr.Error()}})
						return
					}
					emit(WSEvent{Type: EventRunCompleted, Data: CompleteData{Summary: summary}})
					return
				}
			}

			continue // 继续循环
		}

		// 保护性错误路径：正常流程不会到达此处。不要硬编码最终回复；
		// 若模型没有给出文本或工具调用，让 run 以可诊断错误结束。
		emit(WSEvent{Type: EventError, Data: ErrorData{
			Message: "模型没有返回可展示的分析内容，也没有请求工具调用。",
			Code:    "empty_llm_output",
		}})
		return
	}

}

func isSuccessfulFinalizeResult(result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return false
	}
	tool, _ := payload["tool"].(string)
	ok, _ := payload["ok"].(bool)
	finalized, _ := payload["is_finalized"].(bool)
	deliveryState, _ := payload["delivery_state"].(string)
	return tool == "report_finalize" && ok && finalized && strings.EqualFold(strings.TrimSpace(deliveryState), "finalized")
}

func (e *Engine) finalResponseAfterFinalize(ctx context.Context, getRuntimeVars func() []RuntimeContextBlock) (string, error) {
	e.mu.Lock()
	bundle := &PromptBundle{
		Policy: e.policy,
	}
	if getRuntimeVars != nil {
		bundle.RuntimeContext = append(bundle.RuntimeContext, getRuntimeVars()...)
	}
	if e.contextDigest != "" {
		digestBody := strings.TrimPrefix(strings.TrimSpace(e.contextDigest), historyDigestPrefix)
		digestBody = strings.TrimSpace(digestBody)
		if digestBody != "" {
			bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
				Name:    "digest",
				Role:    "user",
				Content: historyDigestPrefix + "\n" + digestBody,
			})
		}
	}
	bundle.History = append([]ConversationItem(nil), e.history...)
	e.mu.Unlock()

	resp, err := e.llm.ChatWithTools(ctx, bundle, nil)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned empty response")
	}
	summary := strings.TrimSpace(resp.Choices[0].Message.Content)
	if summary == "" {
		return "", fmt.Errorf("LLM returned empty final response")
	}
	e.mu.Lock()
	e.history = append(e.history, ConversationItem{
		Role:             LLMRoleAssistant,
		Content:          summary,
		ReasoningContent: resp.Choices[0].Message.ReasoningContent,
	})
	e.compactMessagesLocked(resp.Usage.PromptTokens)
	e.mu.Unlock()
	return summary, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func compactAssistantMessage(message LLMMessage) ConversationItem {
	item := ConversationItem{
		Role:             message.Role,
		Content:          message.Content,
		ReasoningContent: message.ReasoningContent,
	}
	if len(message.ToolCalls) > 0 {
		for _, toolCall := range message.ToolCalls {
			next := toolCall
			next.Function.Arguments = compactToolArguments(toolCall.Function.Name, toolCall.Function.Arguments)
			item.ToolCalls = append(item.ToolCalls, next)
		}
	}
	return item
}

func compactToolArguments(toolName, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}

	switch toolName {
	case "report_create_chart":
		// report_create_chart 的参数结构会直接影响后续轮次的工具调用，
		// 这里保留原始参数，避免把摘要字段误导回模型。
		return raw
	case "report_manage_blocks":
		// report_manage_blocks 的参数也是可再次参考的报告状态变更。
		// 保留原始参数，避免把 content_head/content_chars 这类历史摘要误导成下一轮工具参数。
		return raw
	case "report_finalize":
		var payload struct {
			ReportTitle string `json:"report_title"`
			Author      string `json:"author"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			summary, _ := json.Marshal(map[string]interface{}{
				"report_title": payload.ReportTitle,
				"author":       payload.Author,
			})
			return string(summary)
		}
	}

	return raw
}

// clipHistoryText 已迁移至 stringutil.go clipText

func compactToolResult(toolName, result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return result
	}

	switch toolName {
	case "data_query_sql":
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			return compactQueryResult(payload)
		}
	case "data_describe_table":
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			minified, _ := json.Marshal(stripHistorySummaryFields(payload))
			return string(minified)
		}
	case "code_run_python":
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			minified, _ := json.Marshal(stripHistorySummaryFields(payload))
			return string(minified)
		}
	case "data_list_tables":
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			minified, _ := json.Marshal(stripHistorySummaryFields(payload))
			return string(minified)
		}
		return strings.Join(strings.Fields(trimmed), " ")
	case "task_delegate":
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			minified, _ := json.Marshal(map[string]interface{}{
				"ok":            payload["ok"],
				"tool":          payload["tool"],
				"child_run_id":  payload["child_run_id"],
				"delegate_role": payload["delegate_role"],
				"goal_id":       payload["goal_id"],
				"allowed_tools": payload["allowed_tools"],
				"child_result":  payload["child_result"],
				"trace_count":   delegateTraceCount(payload),
			})
			return string(minified)
		}
	}

	return result
}

func stripHistorySummaryFields(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if key == "ui_summary" {
			continue
		}
		cloned[key] = value
	}
	return cloned
}

const queryCompactRowThreshold = 20
const queryCompactKeepRows = 10

// compactQueryResult 为 data_query_sql 的大结果添加列统计摘要，截断多余行数据。
// 满足两个目标：提供统计摘要加速理解，同时截断数据降低上下文膨胀。
func compactQueryResult(payload map[string]interface{}) string {
	cloned := stripHistorySummaryFields(payload)

	rows, ok := cloned["rows"].([]interface{})
	if !ok || len(rows) <= queryCompactRowThreshold {
		minified, _ := json.Marshal(cloned)
		return string(minified)
	}

	// 为数值列生成统计摘要
	cloned["_row_count"] = len(rows)
	cloned["_original_row_count"] = cloned["row_count"]
	cloned["row_count"] = queryCompactKeepRows
	if columns, ok := cloned["columns"].([]interface{}); ok && len(rows) > 0 {
		stats := buildColumnStats(columns, rows)
		if len(stats) > 0 {
			cloned["column_stats"] = stats
		}
	}

	// 截断行数据，降低上下文膨胀
	cloned["rows"] = rows[:queryCompactKeepRows]
	cloned["_truncated"] = true
	cloned["_note"] = "result truncated for history context; full result is saved in traces."

	minified, _ := json.Marshal(cloned)
	return string(minified)
}

// buildColumnStats 为每个数值列生成 min/max/count 摘要
func buildColumnStats(columns []interface{}, rows []interface{}) map[string]interface{} {
	stats := make(map[string]interface{})
	for _, colRaw := range columns {
		col, ok := colRaw.(string)
		if !ok {
			continue
		}

		var min, max float64
		numericCount := 0
		for _, rowRaw := range rows {
			row, ok := rowRaw.(map[string]interface{})
			if !ok {
				continue
			}
			val, exists := row[col]
			if !exists || val == nil {
				continue
			}
			num, ok := val.(float64)
			if !ok {
				continue
			}
			if numericCount == 0 {
				min, max = num, num
			} else {
				if num < min {
					min = num
				}
				if num > max {
					max = num
				}
			}
			numericCount++
		}
		// 只为数值列生成统计
		if numericCount > 0 {
			stats[col] = map[string]interface{}{
				"min":   min,
				"max":   max,
				"count": numericCount,
			}
		}
	}
	return stats
}

func delegateTraceCount(payload map[string]interface{}) int {
	if count, ok := payload["trace_count"].(float64); ok {
		return int(count)
	}
	return traceCount(payload["trace"])
}

func traceCount(value interface{}) int {
	items, ok := value.([]interface{})
	if !ok {
		return 0
	}
	return len(items)
}

func toolCallSucceeded(result string, execErr error) bool {
	if execErr != nil {
		return false
	}

	var payload struct {
		OK *bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result)), &payload); err == nil && payload.OK != nil {
		return *payload.OK
	}

	return true
}

// ProvideAskUserResult 将用户回复作为 user_request_input 工具的结构化执行结果注入 LLM 对话上下文。
// 如果同一轮有多个 user_request_input 调用，同一个用户回复会填充所有未答复的调用。
func (e *Engine) ProvideAskUserResult(userResponse string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var pendingIDs []string
	for i := len(e.history) - 1; i >= 0; i-- {
		msg := e.history[i]
		if msg.Role == LLMRoleAssistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name == "user_request_input" {
					pendingIDs = append(pendingIDs, tc.ID)
				}
			}
		}
		if len(pendingIDs) > 0 {
			break
		}
	}

	if len(pendingIDs) == 0 {
		return fmt.Errorf("no pending user_request_input tool call found")
	}

	appended := 0
	for _, id := range pendingIDs {
		hasResult := false
		for j := len(e.history) - 1; j >= 0; j-- {
			if e.history[j].Role == LLMRoleTool && e.history[j].ToolCallID == id {
				hasResult = true
				break
			}
		}
		if !hasResult {
			e.history = append(e.history, ConversationItem{
				Role:       LLMRoleTool,
				Content:    buildAskUserToolResult(userResponse),
				ToolCallID: id,
			})
			appended++
		}
	}

	if appended == 0 {
		return fmt.Errorf("all user_request_input calls already have results")
	}

	return nil
}

func buildAskUserToolResult(userResponse string) string {
	trimmed := strings.TrimSpace(userResponse)
	payload := map[string]interface{}{
		"ok":            true,
		"tool":          "user_request_input",
		"response_text": trimmed,
		"response_json": false,
		"ui_summary":    "User input received.",
	}
	var parsed interface{}
	if trimmed != "" && json.Unmarshal([]byte(trimmed), &parsed) == nil {
		payload["response"] = parsed
		payload["response_json"] = true
	} else {
		payload["response"] = trimmed
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"tool":"user_request_input","error":"failed to encode user response"}`
	}
	return string(encoded)
}

var toolErrorSanitizePatterns = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key)\s*[=:]\s*\S+`), "${1}=***"},
	{regexp.MustCompile(`(?i)(postgresql?|mysql|mongodb)://[^\s]+`), "${1}://***"},
	{regexp.MustCompile(`/home/[^\s]+`), "/home/***"},
	{regexp.MustCompile(`/tmp/[^\s]+`), "/tmp/***"},
	{regexp.MustCompile(`/var/[^\s]+`), "/var/***"},
	{regexp.MustCompile(`C:\\[^\s]+`), "C:\\***"},
}

func sanitizeToolError(msg string) string {
	for _, s := range toolErrorSanitizePatterns {
		msg = s.pattern.ReplaceAllString(msg, s.replace)
	}
	if len(msg) > 500 {
		msg = msg[:500] + "...(truncated)"
	}
	return msg
}
