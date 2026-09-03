package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// Engine Agent 主循环引擎
type Engine struct {
	llm                    *LLMClient
	registry               *tools.Registry
	policy                 string
	history                []ConversationItem
	omittedHistoryMessages int
	confirmationReceipts   map[string]*ConfirmationReceipt
	reportState            *tools.ReportState
	mu                     sync.Mutex
}

// ConfirmationReceipt binds one authenticated user response to the exact
// user_request_input context that requested it. Receipts are single-use.
type ConfirmationReceipt struct {
	ID           string
	ToolCallID   string
	ActorUserID  string
	Scope        string
	ContextRef   string
	Question     string
	ResponseText string
	Action       string
	ResourceRef  string
	PayloadHash  string
	CreatedAt    time.Time
	ConsumedAt   *time.Time
}

const (
	contextBudgetTokens         = 128000
	contextCompactTriggerTokens = contextBudgetTokens * 9 / 10
	recentContextWindow         = 12
	maxMainLoopIterations       = 50
)

type eventEmitterAware interface {
	SetEventEmitter(func(RuntimeEvent))
}

type executionContextAware interface {
	SetExecutionContext(context.Context)
}

type suspensionEventTool interface {
	SuspensionEvent(json.RawMessage) (RuntimeEvent, error)
}

type userResponseTool interface {
	AcceptsUserResponse() bool
}

func emitEngineError(emit func(RuntimeEvent), code, message string, err error) {
	if err != nil {
		log.Printf("engine failure code=%s err=%v", code, err)
	}
	emit(RuntimeEvent{Type: EventError, Data: ErrorData{Message: message, Code: code}})
}

// isRetryableToolError only retries failures that expose a typed transient
// transport contract. Tool behavior is never inferred from error wording.
func isRetryableToolError(err error) bool {
	return isTransientTransportError(err)
}

// retryableToolExec retries transient execution failures within a bounded budget.
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

	bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
		Name: "history_window", Role: "user", Content: fmt.Sprintf(`{"omitted_message_count":%d}`, recentStart),
	})
	bundle.History = bundle.History[recentStart:]
}

// NewEngine 使用项目唯一的静态策略创建智能体引擎。
func NewEngine(registry *tools.Registry) *Engine {
	if registry == nil {
		panic("tool registry is not initialized")
	}
	return &Engine{
		llm:                  NewLLMClient(),
		registry:             registry,
		policy:               BuildPolicyPrompt(),
		confirmationReceipts: make(map[string]*ConfirmationReceipt),
	}
}

func (e *Engine) SetReportState(state *tools.ReportState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reportState = state
}

func (e *Engine) reportDraftAfterMutation(startVersion uint64) (bool, uint64) {
	e.mu.Lock()
	state := e.reportState
	e.mu.Unlock()
	if state == nil {
		return false, 0
	}
	state.RLock()
	defer state.RUnlock()
	return state.MutationVersion > startVersion && state.NeedsFinalize, state.MutationVersion
}

func (e *Engine) ResetMessages() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = nil
	e.omittedHistoryMessages = 0
	e.confirmationReceipts = make(map[string]*ConfirmationReceipt)
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

	e.omittedHistoryMessages += recentStart
	e.history = e.history[recentStart:]
}

func adjustCompactionBoundary(history []ConversationItem, boundary int) int {
	if boundary <= 0 || boundary >= len(history) {
		return boundary
	}
	msg := history[boundary]
	if msg.Role == LLMRoleTool {
		// Walk back until the owning assistant message is found; a fixed cap
		// would cut mid-batch for large parallel tool-call batches and orphan
		// tool results without their tool_calls message.
		for i := boundary - 1; i >= 0; i-- {
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

// filterUnavailableToolSpecs drops tools whose availability check currently
// fails. Availability is re-evaluated per run (backed by each tool's health
// cache), so a tool disabled by a transient backend failure recovers on the
// next run instead of staying disabled for the whole session lifetime.
func (e *Engine) filterUnavailableToolSpecs(ctx context.Context, specs []tools.ToolSpec) []tools.ToolSpec {
	if len(specs) == 0 {
		return specs
	}
	filtered := make([]tools.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		tool, err := e.registry.Get(spec.Function.Name)
		if err == nil {
			if checker, ok := tool.(tools.AvailabilityTool); ok {
				if checkErr := checker.CheckAvailability(ctx); checkErr != nil {
					log.Printf("engine: tool %s unavailable for this run: %v", spec.Function.Name, checkErr)
					continue
				}
			}
		}
		filtered = append(filtered, spec)
	}
	return filtered
}

func (e *Engine) prepareRuntimeTools(ctx context.Context, emit func(RuntimeEvent)) {
	for _, tool := range e.registry.ListTools() {
		if next, ok := tool.(eventEmitterAware); ok {
			next.SetEventEmitter(emit)
		}
		if next, ok := tool.(executionContextAware); ok {
			next.SetExecutionContext(ctx)
		}
	}
}

func toolCapability(tool tools.Tool) tools.ToolCapability {
	provider, ok := tool.(tools.CapabilityTool)
	if !ok {
		return tools.ToolCapability{}
	}
	return provider.Capability()
}

// Run 执行 Agent 主循环
// userInput 为空字符串时表示从 user_request_input 挂起点恢复执行，
// 用户答案已通过 ProvideAskUserResult 注入历史，此时不再追加额外的 user 消息。
func (e *Engine) Run(ctx context.Context, userInput string, getRuntimeVars func() []RuntimeContextBlock, emit func(RuntimeEvent)) {
	if ctx == nil {
		panic("agent run context must not be nil")
	}
	if emit == nil {
		panic("agent runtime event emitter must not be nil")
	}
	e.prepareRuntimeTools(ctx, emit)

	e.mu.Lock()
	toolSpecs := e.filterUnavailableToolSpecs(ctx, e.registry.GetToolSpecs())
	e.mu.Unlock()

	userTask := userInput
	var startMutationVersion uint64
	e.mu.Lock()
	initialReportState := e.reportState
	e.mu.Unlock()
	if initialReportState != nil {
		initialReportState.RLock()
		startMutationVersion = initialReportState.MutationVersion
		initialReportState.RUnlock()
	}
	var continuationFacts []RuntimeContextBlock
	var injectedDeliveryVersion uint64
	finalMarkupRejects := 0

	for i := 1; ; i++ {
		select {
		case <-ctx.Done():
			emit(RuntimeEvent{Type: EventRunCancelled, Data: ErrorData{Message: "任务已取消"}})
			return
		default:
		}

		if i > maxMainLoopIterations {
			emitEngineError(emit, "iteration_limit_exceeded", fmt.Sprintf("任务超过运行轮次上限（%d）", maxMainLoopIterations), nil)
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
		bundle.RuntimeContext = append(bundle.RuntimeContext, continuationFacts...)
		if e.omittedHistoryMessages > 0 {
			bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
				Name: "history_window", Role: "user", Content: fmt.Sprintf(`{"omitted_message_count":%d}`, e.omittedHistoryMessages),
			})
		}
		bundle.History = append([]ConversationItem(nil), e.history...)
		e.mu.Unlock()

		resp, llmErr := e.llm.ChatWithTools(ctx, bundle, toolSpecs)
		if llmErr != nil {
			emitEngineError(emit, "llm_request_failed", "模型服务调用失败", llmErr)
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
			emitEngineError(emit, "empty_llm_response", "模型服务未返回响应内容", nil)
			return
		}

		choice := resp.Choices[0]

		// 有文本内容时，推送模型面向用户的状态说明。
		if choice.Message.Content != "" {
			if len(choice.Message.ToolCalls) > 0 {
				emit(RuntimeEvent{Type: EventAssistantStatus, Data: AssistantStatusData{Content: choice.Message.Content}})
			} else {
				if draft, version := e.reportDraftAfterMutation(startMutationVersion); draft && injectedDeliveryVersion != version {
					e.mu.Lock()
					e.history = append(e.history, ConversationItem{Role: LLMRoleAssistant, Content: choice.Message.Content, ReasoningContent: choice.Message.ReasoningContent})
					e.mu.Unlock()
					emit(RuntimeEvent{Type: EventAssistantStatus, Data: AssistantStatusData{Content: choice.Message.Content}})
					injectedDeliveryVersion = version
					continuationFacts = []RuntimeContextBlock{{
						Name: "report_delivery_state", Role: "user",
						Content: fmt.Sprintf("report_mutation_version=%d; needs_finalize=true; delivery_state=draft; is_finalized=false", version),
					}}
					continue
				}
				// 有文本 + 无工具调用 → 候选最终回复
				if reject, rejectsAfter := finalMarkupGuardDecision(choice.Message.Content, finalMarkupRejects); reject {
					// Thin guardrail: a final answer leaking raw tool-call
					// markup (the model intended a tool call) is invalid
					// output. The leaked content is NOT recorded in history,
					// and the model gets a corrective feedback turn to either
					// call the tool properly or restate the final answer.
					finalMarkupRejects = rejectsAfter
					log.Printf("engine: rejected final output containing raw tool-call markup (attempt %d/%d)", finalMarkupRejects, maxFinalMarkupRejects)
					e.mu.Lock()
					e.history = append(e.history, ConversationItem{
						Role:    LLMRoleUser,
						Content: finalMarkupCorrectionMessage,
					})
					e.mu.Unlock()
					continue
				}
				e.mu.Lock()
				e.history = append(e.history, ConversationItem{
					Role:             LLMRoleAssistant,
					Content:          choice.Message.Content,
					ReasoningContent: choice.Message.ReasoningContent,
				})
				e.mu.Unlock()
				emit(RuntimeEvent{Type: EventRunCompleted, Data: CompleteData{Summary: choice.Message.Content}})
				return
			}
		}

		// 如果 finish_reason 是 stop 且没有工具调用，结束
		if choice.FinishReason == LLMFinishReasonStop && len(choice.Message.ToolCalls) == 0 {
			if strings.TrimSpace(choice.Message.Content) == "" {
				emit(RuntimeEvent{Type: EventError, Data: ErrorData{
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
			emit(RuntimeEvent{Type: EventRunCompleted, Data: CompleteData{Summary: choice.Message.Content}})
			return
		}

		// 处理工具调用
		if len(choice.Message.ToolCalls) > 0 {
			if err := validateToolCallBatch(e.registry, choice.Message.ToolCalls); err != nil {
				emitEngineError(emit, "invalid_tool_call_batch", "工具调用批次不符合协议", err)
				return
			}
			// 将 assistant 消息加入历史
			e.mu.Lock()
			e.history = append(e.history, compactAssistantMessage(choice.Message))
			e.mu.Unlock()

			for _, toolCall := range choice.Message.ToolCalls {
				registeredTool, lookupErr := e.registry.Get(toolCall.Function.Name)
				if lookupErr != nil {
					emitEngineError(emit, "tool_not_available", "请求的工具不可用", lookupErr)
					return
				}
				capability := toolCapability(registeredTool)
				toolSpan := llmDebugWriter.StartSpan(
					TraceMetadataFromContext(ctx),
					"tool",
					toolCall.Function.Name,
					"",
					toolCall.ID,
				)

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

				if capability.RunControl == "suspend" {
					provider, ok := registeredTool.(suspensionEventTool)
					if !ok {
						emitEngineError(emit, "invalid_suspension_contract", "工具挂起协议无效", nil)
						return
					}
					suspendStart := time.Now()
					event, suspendErr := provider.SuspensionEvent(json.RawMessage(toolCall.Function.Arguments))
					if suspendErr != nil {
						// Invalid suspension arguments are a recoverable tool
						// failure, not an engine failure: feed the contract
						// error back to the model as a tool result so it can
						// correct the arguments and retry.
						log.Printf("Tool %s error: %v", toolCall.Function.Name, suspendErr)
						metrics.ToolCallsTotal.WithLabelValues(toolCall.Function.Name, "failure").Inc()
						failureBytes, marshalErr := json.Marshal(map[string]interface{}{
							"ok":      false,
							"tool":    toolCall.Function.Name,
							"message": "invalid suspension tool arguments: " + suspendErr.Error(),
						})
						if marshalErr != nil {
							emitEngineError(emit, "invalid_suspension_event", "工具挂起事件无效", suspendErr)
							return
						}
						emit(RuntimeEvent{
							Type: EventToolResult,
							Data: ToolResultData{
								ID:       toolCall.ID,
								Name:     toolCall.Function.Name,
								Result:   string(failureBytes),
								Duration: time.Since(suspendStart).Milliseconds(),
								Success:  false,
							},
						})
						e.mu.Lock()
						e.history = append(e.history, ConversationItem{
							Role:       LLMRoleTool,
							Content:    string(failureBytes),
							ToolCallID: toolCall.ID,
						})
						e.mu.Unlock()
						continue
					}
					emit(event)
					return
				}

				emit(RuntimeEvent{
					Type: EventToolCall,
					Data: ToolCallData{
						ID:        toolCall.ID,
						Name:      toolCall.Function.Name,
						Arguments: json.RawMessage(toolCall.Function.Arguments),
					},
				})

				result, execErr := retryableToolExec(ctx, e.registry, toolCall.Function.Name, json.RawMessage(toolCall.Function.Arguments))

				duration := time.Since(start).Milliseconds()

				// If we got canceled during execution (or context ended), drop the result and abort the tool loop.
				if ctx.Err() != nil {
					emit(RuntimeEvent{Type: EventRunCancelled, Data: ErrorData{Message: "任务已取消"}})
					return
				}

				result, success, contractErr := normalizeToolExecutionResult(toolCall.Function.Name, result, execErr)
				if contractErr != nil {
					execErr = contractErr
					log.Printf("Tool %s returned an invalid result contract: %v", toolCall.Function.Name, contractErr)
				}
				status := "success"
				if !success {
					status = "failure"
				}
				metrics.ToolCallsTotal.WithLabelValues(toolCall.Function.Name, status).Inc()
				metrics.ToolCallDuration.WithLabelValues(toolCall.Function.Name).Observe(float64(duration) / 1000)
				if execErr != nil {
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
				emit(RuntimeEvent{
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
					emit(RuntimeEvent{Type: EventRunCancelled, Data: ErrorData{Message: "任务已取消"}})
					return
				}

				// 将工具结果加入消息历史
				compactedResult, compactErr := compactToolResult(toolCall.Function.Name, result)
				if compactErr != nil {
					emitEngineError(emit, "tool_result_compaction_failed", "处理工具结果失败", compactErr)
					return
				}
				e.mu.Lock()
				e.history = append(e.history, ConversationItem{
					Role:       LLMRoleTool,
					Content:    compactedResult,
					ToolCallID: toolCall.ID,
				})
				e.mu.Unlock()

				if capability.DeliveryBoundary && isSuccessfulDeliveryBoundaryResult(result) {
					summary, summaryErr := e.finalResponseAfterFinalize(ctx, getRuntimeVars)
					if summaryErr != nil {
						emitEngineError(emit, "final_response_failed", "生成最终回复失败", summaryErr)
						return
					}
					emit(RuntimeEvent{Type: EventRunCompleted, Data: CompleteData{Summary: summary}})
					return
				}
			}

			continue // 继续循环
		}

		// 保护性错误路径：正常流程不会到达此处。不要硬编码最终回复；
		// 若模型没有给出文本或工具调用，让 run 以可诊断错误结束。
		emit(RuntimeEvent{Type: EventError, Data: ErrorData{
			Message: "模型没有返回可展示的分析内容，也没有请求工具调用。",
			Code:    "empty_llm_output",
		}})
		return
	}

}

func validateToolCallBatch(registry *tools.Registry, calls []LLMToolCall) error {
	if registry == nil {
		return fmt.Errorf("tool registry is not initialized")
	}
	suspending := 0
	for _, call := range calls {
		tool, err := registry.Get(call.Function.Name)
		if err != nil {
			return err
		}
		if toolCapability(tool).RunControl == "suspend" {
			suspending++
		}
	}
	if suspending > 0 && len(calls) != 1 {
		return fmt.Errorf("a run-suspending tool call must be the only tool call in an assistant turn")
	}
	return nil
}

func isSuccessfulDeliveryBoundaryResult(result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return false
	}
	ok, _ := payload["ok"].(bool)
	finalized, _ := payload["is_finalized"].(bool)
	deliveryState, _ := payload["delivery_state"].(string)
	return ok && finalized && deliveryState == "finalized"
}

func (e *Engine) finalResponseAfterFinalize(ctx context.Context, getRuntimeVars func() []RuntimeContextBlock) (string, error) {
	e.mu.Lock()
	bundle := &PromptBundle{
		Policy: e.policy,
	}
	if getRuntimeVars != nil {
		bundle.RuntimeContext = append(bundle.RuntimeContext, getRuntimeVars()...)
	}
	if e.omittedHistoryMessages > 0 {
		bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
			Name: "history_window", Role: "user", Content: fmt.Sprintf(`{"omitted_message_count":%d}`, e.omittedHistoryMessages),
		})
	}
	baseHistory := append([]ConversationItem(nil), e.history...)
	bundle.History = baseHistory
	e.mu.Unlock()

	// The continuation runs without tools, so models that "want one more
	// verification call" sometimes leak their native tool-call markup into
	// the summary text. Apply the same final-output guardrail as the main
	// loop: bounded rejection with a corrective retry, then accept as-is.
	var summary string
	var reasoningContent string
	var usage LLMUsage
	for attempt := 0; ; attempt++ {
		resp, err := e.llm.ChatWithTools(ctx, bundle, nil)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("LLM returned empty response")
		}
		summary = resp.Choices[0].Message.Content
		reasoningContent = resp.Choices[0].Message.ReasoningContent
		usage = resp.Usage
		if strings.TrimSpace(summary) == "" {
			return "", fmt.Errorf("LLM returned empty final response")
		}
		if !containsRawToolMarkup(summary) {
			break
		}
		if attempt >= maxFinalMarkupRejects {
			log.Printf("engine: post-finalize response still contains raw tool-call markup after %d rejections; accepting as-is", attempt)
			break
		}
		log.Printf("engine: rejected post-finalize response containing raw tool-call markup (attempt %d/%d)", attempt+1, maxFinalMarkupRejects)
		correctedHistory := append(append([]ConversationItem(nil), baseHistory...), ConversationItem{
			Role:    LLMRoleUser,
			Content: finalMarkupCorrectionMessage,
		})
		bundle = &PromptBundle{
			Policy:         bundle.Policy,
			RuntimeContext: bundle.RuntimeContext,
			History:        correctedHistory,
		}
	}

	e.mu.Lock()
	e.history = append(e.history, ConversationItem{
		Role:             LLMRoleAssistant,
		Content:          summary,
		ReasoningContent: reasoningContent,
	})
	e.compactMessagesLocked(usage.PromptTokens)
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
			item.ToolCalls = append(item.ToolCalls, next)
		}
	}
	return item
}

// clipHistoryText 已迁移至 stringutil.go clipText

func compactToolResult(toolName string, result string) (string, error) {
	var payload map[string]interface{}
	if err := jsoncontract.Decode([]byte(result), &payload); err != nil {
		return "", fmt.Errorf("tool %s result is not a strict JSON object: %w", toolName, err)
	}
	minified, err := json.Marshal(stripHistorySummaryFields(payload))
	if err != nil {
		return "", fmt.Errorf("compact tool %s result: %w", toolName, err)
	}
	return string(minified), nil
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

func normalizeToolExecutionResult(toolName, result string, execErr error) (string, bool, error) {
	if execErr != nil {
		encoded, err := json.Marshal(map[string]interface{}{
			"ok": false, "tool": toolName, "error_code": "execution_error", "message": sanitizeToolError(execErr.Error()),
		})
		if err != nil {
			return "", false, errors.Join(execErr, err)
		}
		return string(encoded), false, nil
	}

	var payload map[string]interface{}
	if err := jsoncontract.Decode([]byte(result), &payload); err != nil {
		contractErr := fmt.Errorf("tool %s result is not a strict result object: %w", toolName, err)
		encoded, marshalErr := json.Marshal(map[string]interface{}{
			"ok": false, "tool": toolName, "error_code": "invalid_tool_result", "message": contractErr.Error(),
		})
		return string(encoded), false, errors.Join(contractErr, marshalErr)
	}
	ok, hasOK := payload["ok"].(bool)
	resultTool, hasTool := payload["tool"].(string)
	if !hasOK || !hasTool || resultTool != toolName {
		contractErr := fmt.Errorf("tool %s result must contain exact ok and tool fields", toolName)
		encoded, marshalErr := json.Marshal(map[string]interface{}{
			"ok": false, "tool": toolName, "error_code": "invalid_tool_result", "message": contractErr.Error(),
		})
		return string(encoded), false, errors.Join(contractErr, marshalErr)
	}
	return result, ok, nil
}

// ProvideAskUserResult 将已认证用户的回复作为唯一挂起工具调用的结构化执行结果注入 LLM 对话上下文。
func (e *Engine) ProvideAskUserResult(userResponse, actorUserID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.registry == nil {
		return fmt.Errorf("tool registry is not initialized")
	}
	if actorUserID == "" || actorUserID != strings.TrimSpace(actorUserID) {
		return fmt.Errorf("authenticated actor user id is required")
	}

	type pendingAsk struct {
		ID   string
		Args askUserToolCallArguments
	}
	var pending []pendingAsk
	for i := len(e.history) - 1; i >= 0; i-- {
		msg := e.history[i]
		if msg.Role == LLMRoleAssistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				tool, err := e.registry.Get(tc.Function.Name)
				provider, ok := tool.(userResponseTool)
				if err == nil && ok && provider.AcceptsUserResponse() {
					var args askUserToolCallArguments
					if err := jsoncontract.Decode([]byte(tc.Function.Arguments), &args); err != nil {
						return fmt.Errorf("pending user_request_input arguments are invalid: %w", err)
					}
					pending = append(pending, pendingAsk{ID: tc.ID, Args: args})
				}
			}
		}
		if len(pending) > 0 {
			break
		}
	}

	if len(pending) == 0 {
		return fmt.Errorf("no pending user_request_input tool call found")
	}
	if len(pending) != 1 {
		return fmt.Errorf("expected one pending user response tool call, found %d", len(pending))
	}

	appended := 0
	for _, ask := range pending {
		hasResult := false
		for j := len(e.history) - 1; j >= 0; j-- {
			if e.history[j].Role == LLMRoleTool && e.history[j].ToolCallID == ask.ID {
				hasResult = true
				break
			}
		}
		if !hasResult {
			receiptID := ""
			approved := false
			if ask.Args.Authorization != nil {
				var err error
				approved, err = authorizationResponseApproved(userResponse, ask.Args.Authorization)
				if err != nil {
					return err
				}
			}
			if approved {
				payloadHash, err := authorizationPayloadHash(ask.Args.Authorization.PayloadJSON)
				if err != nil {
					return err
				}
				receiptID = "ucr_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
				e.confirmationReceipts[receiptID] = &ConfirmationReceipt{
					ID:           receiptID,
					ToolCallID:   ask.ID,
					ActorUserID:  actorUserID,
					Scope:        ask.Args.Scope,
					ContextRef:   ask.Args.ContextRef,
					Question:     ask.Args.Question,
					ResponseText: userResponse,
					Action:       ask.Args.Authorization.Action,
					ResourceRef:  ask.Args.Authorization.ResourceRef,
					PayloadHash:  payloadHash,
					CreatedAt:    time.Now(),
				}
			}
			toolResult, err := buildAskUserToolResult(userResponse, receiptID)
			if err != nil {
				return err
			}
			e.history = append(e.history, ConversationItem{
				Role:       LLMRoleTool,
				Content:    toolResult,
				ToolCallID: ask.ID,
			})
			appended++
		}
	}

	if appended == 0 {
		return fmt.Errorf("all user_request_input calls already have results")
	}

	return nil
}

// CommitWithConfirmationReceipt serializes validation, the caller's durable
// commit, and single-use consumption so a failed commit does not burn the receipt.
func (e *Engine) CommitWithConfirmationReceipt(receiptID, action, resourceRef, payloadJSON string, commit func(actorUserID string) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if commit == nil {
		return fmt.Errorf("confirmation commit callback is required")
	}
	if receiptID == "" || receiptID != strings.TrimSpace(receiptID) || action == "" || action != strings.TrimSpace(action) || resourceRef == "" || resourceRef != strings.TrimSpace(resourceRef) {
		return fmt.Errorf("confirmation receipt ID, action, and resource reference must be non-empty exact values")
	}
	receipt := e.confirmationReceipts[receiptID]
	if receipt == nil {
		return fmt.Errorf("confirmation receipt not found")
	}
	if receipt.ConsumedAt != nil {
		return fmt.Errorf("confirmation receipt has already been consumed")
	}
	if action == "" || receipt.Action != action {
		return fmt.Errorf("confirmation receipt action does not match %q", action)
	}
	if resourceRef == "" || receipt.ResourceRef != resourceRef {
		return fmt.Errorf("confirmation receipt resource does not match %q", resourceRef)
	}
	payloadHash, err := authorizationPayloadHash(payloadJSON)
	if err != nil {
		return err
	}
	if receipt.PayloadHash != payloadHash {
		return fmt.Errorf("confirmation receipt payload does not match authorized change")
	}
	if err := commit(receipt.ActorUserID); err != nil {
		return err
	}
	now := time.Now()
	receipt.ConsumedAt = &now
	return nil
}

func buildAskUserToolResult(userResponse, receiptID string) (string, error) {
	payload := map[string]interface{}{
		"ok":            true,
		"tool":          "user_request_input",
		"response_text": userResponse,
		"response_json": false,
		"ui_summary":    "已收到用户输入。",
	}
	if receiptID != "" {
		payload["confirmation_receipt_id"] = receiptID
	}
	var parsed interface{}
	if strings.TrimSpace(userResponse) != "" && jsoncontract.Decode([]byte(userResponse), &parsed) == nil {
		payload["response"] = parsed
		payload["response_json"] = true
	} else {
		payload["response"] = userResponse
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode user response: %w", err)
	}
	return string(encoded), nil
}

func authorizationResponseApproved(raw string, request *ActionAuthorizationRequest) (bool, error) {
	if request == nil {
		return false, nil
	}
	var response struct {
		ResponseType          string `json:"response_type"`
		AuthorizationDecision string `json:"authorization_decision"`
		Action                string `json:"action"`
		ResourceRef           string `json:"resource_ref"`
	}
	if err := jsoncontract.Decode([]byte(raw), &response); err != nil {
		return false, fmt.Errorf("authorization response must be an exact JSON decision: %w", err)
	}
	if response.ResponseType != "authorization" || (response.AuthorizationDecision != "approve" && response.AuthorizationDecision != "reject") {
		return false, fmt.Errorf("authorization response_type and authorization_decision are invalid")
	}
	if response.Action != request.Action || response.ResourceRef != request.ResourceRef {
		return false, fmt.Errorf("authorization response does not match the requested action and resource")
	}
	return response.AuthorizationDecision == "approve", nil
}

func authorizationPayloadHash(raw string) (string, error) {
	var payload interface{}
	if err := jsoncontract.Decode([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("authorization payload_json must be valid JSON: %w", err)
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

var toolErrorSanitizePatterns = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key)\s*[=:]\s*\S+`), "${1}=***"},
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*)://[^\s]+`), "${1}://***"},
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
