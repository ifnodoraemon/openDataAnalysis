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
	"github.com/ifnodoraemon/openDataAnalysis/config"
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
	// lastPromptTokens is the provider-reported prompt size of the previous
	// request, used as a lower bound when estimating the next one.
	lastPromptTokens int
	// repeatCalls counts identical tool calls in the current conversation and
	// pendingNotices carries the advisories queued for the next request.
	repeatCalls    map[string]int
	pendingNotices []RuntimeContextBlock
	// pendingSuspensionID is the tool call the run suspended on. Its result is
	// deliberately absent until the user answers, so pairing repair must not
	// synthesize an outcome for it.
	pendingSuspensionID string
	// historySummary carries the compacted digest of conversation that is no
	// longer in history. It is delivered as a runtime-context fact rather than a
	// fake user turn, so history stays a pure record of what was said.
	historySummary string
	// jobs is the session's background job manager. Finished jobs are reported
	// to the model as runtime facts so they need not be polled for.
	jobs *JobManager
	// promptTokens and completionTokens accumulate provider-reported usage for
	// this run so the caller can persist cost facts.
	promptTokens         int
	completionTokens     int
	confirmationReceipts map[string]*ConfirmationReceipt
	reportState          *tools.ReportState
	mu                   sync.Mutex
}

// ConfirmationReceipt binds one authenticated user response to the exact
// user_request_input context that requested it. Receipts are single-use and
// expire: an approval is valid only for the change the user actually saw, and
// only for a bounded window.
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
	ExpiresAt    time.Time
	ConsumedAt   *time.Time
}

const (
	// recentContextWindow is the number of most recent history items kept
	// verbatim when older conversation is compacted.
	recentContextWindow   = 12
	maxMainLoopIterations = 50
	// confirmationReceiptTTL bounds how long a user approval stays usable. An
	// expired receipt fails closed: the model must ask again.
	confirmationReceiptTTL = 30 * time.Minute
)

type eventEmitterAware interface {
	SetEventEmitter(func(RuntimeEvent))
}

type executionContextAware interface {
	SetExecutionContext(context.Context)
}

type suspensionEventTool interface {
	SuspensionEvent(toolCallID string, args json.RawMessage) (RuntimeEvent, error)
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

// toolCallTimeout is the budget for one observation call. A configured zero
// means "use the default".
func toolCallTimeout() time.Duration {
	seconds := defaultToolCallTimeoutSeconds
	if config.Cfg != nil && config.Cfg.ToolCallTimeoutSeconds > 0 {
		seconds = config.Cfg.ToolCallTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// defaultToolCallTimeoutSeconds bounds an observation call that does not
// declare its own budget.
const defaultToolCallTimeoutSeconds = 300

// toolTimeoutError reports that an observation call exceeded its budget. It has
// its own error code so the model can tell a slow backend from a bug.
type toolTimeoutError struct {
	Tool    string
	Timeout time.Duration
}

func (e *toolTimeoutError) Error() string {
	return fmt.Sprintf("tool %s exceeded its %s execution budget", e.Tool, e.Timeout)
}

// executeToolCall runs one tool call, bounding observation calls by the
// configured budget.
//
// A timed-out observation call is reported as a failure and its worker is left
// to finish on its own; observation tools are read-only, so a straggler cannot
// change state and its result is simply discarded. Action tools are never
// abandoned: a mutating call may already have committed, and reporting a
// failure the runtime cannot prove would be worse than waiting. They stay
// bounded by the run context and by their own internal timeouts.
func executeToolCall(ctx context.Context, registry *tools.Registry, toolName string, args json.RawMessage) (string, error) {
	tool, err := registry.Get(toolName)
	if err != nil {
		return registry.Execute(toolName, args)
	}
	capability := toolCapability(tool)
	// Only tools that declare themselves read-only are abandoned on timeout.
	// Everything else either mutates state (where an abandoned call may still
	// commit) or carries its own budget, as MCP tools do.
	if capability.Mode != "observe" {
		return registry.Execute(toolName, args)
	}

	timeout := toolCallTimeout()
	type outcome struct {
		result string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, execErr := registry.Execute(toolName, args)
		done <- outcome{result: result, err: execErr}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case settled := <-done:
		return settled.result, settled.err
	case <-timer.C:
		return "", &toolTimeoutError{Tool: toolName, Timeout: timeout}
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// retryableToolExec retries transient execution failures within a bounded
// budget.
//
// Only read-only tools are retried. Re-running a tool that mutates state can
// duplicate its effect — a second import, a second report mutation — so an
// action tool's transient failure is surfaced to the model instead of being
// replayed behind its back.
func retryableToolExec(ctx context.Context, registry *tools.Registry, toolName string, args json.RawMessage) (string, error) {
	if !toolIsIdempotent(registry, toolName) {
		return executeToolCall(ctx, registry, toolName, args)
	}
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var result string
	var execErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		result, execErr = executeToolCall(ctx, registry, toolName, args)
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

// toolIsIdempotent reports whether repeating a call is safe. Observation tools
// are read-only; an action tool must opt in explicitly.
func toolIsIdempotent(registry *tools.Registry, toolName string) bool {
	if registry == nil {
		return false
	}
	tool, err := registry.Get(toolName)
	if err != nil {
		return false
	}
	capability := toolCapability(tool)
	return capability.Idempotent || capability.Mode == "observe"
}

// compactWorkerBundle 对子代理消息历史做上下文压缩。
//
// Delegated children are bounded by maxWorkerIterations and their internal
// history is never rebuilt from the session log, so they trim rather than
// summarize and write no compaction checkpoint.
//
// omitted is the running total of items already dropped from this child's
// history; the returned value is the updated total. The window fact is
// replaced rather than appended so a long delegated run does not accumulate
// duplicate runtime-context blocks.
func compactWorkerBundle(bundle *PromptBundle, promptTokens int, omitted int) int {
	if len(bundle.History) <= 1 {
		return omitted
	}
	if promptTokens <= 0 || promptTokens <= contextCompactTriggerTokens() {
		return omitted
	}

	recentStart := len(bundle.History) - recentContextWindow
	if recentStart <= 0 {
		return omitted
	}

	recentStart = adjustCompactionBoundary(bundle.History, recentStart)

	omitted += recentStart
	retained := bundle.RuntimeContext[:0]
	for _, block := range bundle.RuntimeContext {
		if block.Name != "history_window" {
			retained = append(retained, block)
		}
	}
	bundle.RuntimeContext = append(retained, RuntimeContextBlock{
		Name: "history_window", Role: "user", Content: fmt.Sprintf(`{"omitted_message_count":%d}`, omitted),
	})
	bundle.History = bundle.History[recentStart:]
	return omitted
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

// SetJobManager attaches the session's background job manager, so completed
// jobs can be reported as runtime facts on the next request.
func (e *Engine) SetJobManager(jobs *JobManager) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobs = jobs
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

// SetHistory replaces the in-memory conversation history. It is used when a
// session is rehydrated from the persisted event log, so a run started after a
// restart or cache eviction continues the existing conversation instead of
// starting with no context.
//
// omitted reports how many older items the history no longer carries; it is
// surfaced to the model as a factual omission count.
func (e *Engine) SetHistory(items []ConversationItem, omitted int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append([]ConversationItem(nil), items...)
	if omitted < 0 {
		omitted = 0
	}
	e.omittedHistoryMessages = omitted
}

// History returns a copy of the current conversation history.
func (e *Engine) History() []ConversationItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ConversationItem(nil), e.history...)
}

// SetHistorySummary restores the compacted digest that belongs beside history.
// It is empty until a compaction has happened.
func (e *Engine) SetHistorySummary(summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.historySummary = strings.TrimSpace(summary)
}

// runTokens returns the usage accumulated for this run.
func (e *Engine) runTokens() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.promptTokens, e.completionTokens
}

// repairDanglingToolCalls pairs any assistant tool call that has no recorded
// result. It is idempotent: a fully paired history is returned unchanged.
//
// A run that suspends leaves its suspending call intentionally unanswered; that
// call is skipped here so the pending question survives until the user answers.
func (e *Engine) repairDanglingToolCalls() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = repairToolPairing(e.history, e.pendingSuspensionID)
}

// clearAbandonedSuspension repairs any tool call a previous run left
// unanswered. Resuming a run answers its pending call before Run is entered, so
// anything still unanswered here was abandoned; it gets an explicit "outcome
// unknown" result to keep the derived history protocol-valid.
func (e *Engine) clearAbandonedSuspension() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingSuspensionID = ""
	e.history = repairToolPairing(e.history, "")
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
				if checkErr := probeToolAvailability(ctx, checker); checkErr != nil {
					log.Printf("engine: tool %s unavailable for this run: %v", spec.Function.Name, checkErr)
					continue
				}
			}
		}
		filtered = append(filtered, spec)
	}
	return filtered
}

// availabilityProbeTimeout bounds how long starting a run may wait on one
// tool's health check. Some checks make a live HTTP call, and an unbounded wait
// stalls every run start behind an unhealthy backend.
const availabilityProbeTimeout = 500 * time.Millisecond

// probeToolAvailability runs one availability check under a bounded deadline.
//
// An inconclusive probe (the check did not finish in time) keeps the tool
// advertised: the runtime does not know the tool is down, and the tool reports
// its own failure when called. Only a definite negative removes it from the
// request. This trades a possible wasted call for never stalling a run start.
func probeToolAvailability(ctx context.Context, checker tools.AvailabilityTool) error {
	probeCtx, cancel := context.WithTimeout(ctx, availabilityProbeTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- checker.CheckAvailability(probeCtx)
	}()

	select {
	case err := <-done:
		return err
	case <-probeCtx.Done():
		if ctx.Err() != nil {
			// The run itself was cancelled; surface that rather than pretending
			// the tool is fine.
			return ctx.Err()
		}
		log.Printf("engine: availability probe exceeded %s; keeping the tool advertised", availabilityProbeTimeout)
		return nil
	}
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
	// A run can end without completing its tool batch (cancellation, engine
	// error, iteration limit). An assistant tool call left unanswered would make
	// the next request in this session invalid, so the history is repaired on
	// every exit path.
	defer e.repairDanglingToolCalls()
	// Usage is an operational fact about the run, so it is reported once the run
	// settles on every exit path.
	defer func() {
		promptTokens, completionTokens := e.runTokens()
		if promptTokens == 0 && completionTokens == 0 {
			return
		}
		emit(RuntimeEvent{Type: EventRunUsage, Data: RunUsageData{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}})
	}()
	e.prepareRuntimeTools(ctx, emit)
	// Usage is reported per run, so the accumulator starts clean. The Engine
	// lives for the whole session; without this reset every later run would
	// report the session's cumulative usage as its own.
	e.mu.Lock()
	e.promptTokens = 0
	e.completionTokens = 0
	e.mu.Unlock()
	e.clearAbandonedSuspension()

	e.mu.Lock()
	toolSpecs := e.registry.GetToolSpecs()
	e.mu.Unlock()
	// Availability probes do live HTTP checks on cache expiry (RunPythonTool:
	// up to ~5-6s while the executor is unhealthy), so they must not run
	// inside e.mu; otherwise one bad backend stalls every Run start.
	toolSpecs = e.filterUnavailableToolSpecs(ctx, toolSpecs)

	userTask := userInput
	if userInput != "" {
		// A new user message is a new intent: previous repeat tracking no
		// longer applies.
		e.resetRepeatTracking()
	}
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

		// Context pressure is controlled before the request is sent, so an
		// over-budget prompt is never issued first and trimmed afterwards.
		bundle := e.prepareRequestBundle(ctx, emit, userTask, getRuntimeVars, continuationFacts, toolSpecs)
		// Continuation facts describe one follow-up request only. Clearing them
		// here keeps a stale delivery-state fact from being re-injected on every
		// later iteration of the same run.
		continuationFacts = nil

		// The request is assembled first so the new user turn travels as the
		// task, not twice. It enters history before the call is issued: the
		// message is already persisted by the caller, so a failed request must
		// not leave the live conversation missing a turn the log holds.
		if userTask != "" {
			e.mu.Lock()
			e.history = append(e.history, ConversationItem{
				Role:    LLMRoleUser,
				Content: userTask,
			})
			e.mu.Unlock()
			userTask = ""
		}

		resp, llmErr := e.llm.ChatWithTools(ctx, bundle, toolSpecs)
		if llmErr != nil {
			emitEngineError(emit, "llm_request_failed", "模型服务调用失败", llmErr)
			return
		}

		e.mu.Lock()
		if resp.Usage.PromptTokens > 0 {
			e.lastPromptTokens = resp.Usage.PromptTokens
		}
		e.promptTokens += resp.Usage.PromptTokens
		e.completionTokens += resp.Usage.CompletionTokens
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
					// The corrective turn is written to the log too, so a
					// rebuilt history reproduces it instead of silently losing
					// a turn the live engine saw.
					emit(RuntimeEvent{
						Type: EventSystemFeedback,
						Data: SystemFeedbackData{Content: finalMarkupCorrectionMessage, Reason: "invalid_final_output"},
					})
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

			// The batch is planned once so admission and concurrency are
			// decided in the model's order; the loop below only consumes them.
			plan, planErr := e.planToolCalls(ctx, choice.Message.ToolCalls)
			if planErr != nil {
				emitEngineError(emit, "tool_not_available", "请求的工具不可用", planErr)
				return
			}
			futures := make(map[string]*toolFuture)
			parallelBudget := make(chan struct{}, maxParallelToolCalls())

			for index, planned := range plan {
				toolCall := planned.call
				registeredTool := planned.tool
				capability := planned.capability

				// Single admission point: a gated action never executes without
				// a receipt that authorizes exactly this call. Both a missing
				// receipt and an unverifiable one are closed.
				if planned.admission.Kind != tools.AdmissionAllow {
					if blocked, blockedErr := e.blockToolCall(emit, toolCall, planned.admission); blockedErr != nil {
						emitEngineError(emit, "tool_admission_failed", "处理授权拦截失败", blockedErr)
						return
					} else if blocked {
						continue
					}
				}

				// Start the maximal run of parallel-safe calls beginning here.
				// An exclusive call is never overlapped, so it stays an
				// ordering barrier.
				if planned.safe {
					startParallelRun(plan, index, futures, parallelBudget, ctx, e.registry)
				}

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
					event, suspendErr := provider.SuspensionEvent(toolCall.ID, json.RawMessage(toolCall.Function.Arguments))
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
					// The call stays unanswered until the user replies; pairing
					// repair must not synthesize an outcome for it.
					e.mu.Lock()
					e.pendingSuspensionID = toolCall.ID
					e.mu.Unlock()
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

				result, execErr := runPlannedToolCall(ctx, e.registry, toolCall, futures)

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
				compactedResult = pruneToolResultForHistory(toolCall.Function.Name, compactedResult)
				e.recordRepeatCall(toolCall)
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
	bundle := e.buildBundle("", getRuntimeVars, nil)
	baseHistory := append([]ConversationItem(nil), bundle.History...)

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
	if usage.PromptTokens > 0 {
		e.lastPromptTokens = usage.PromptTokens
	}
	e.promptTokens += usage.PromptTokens
	e.completionTokens += usage.CompletionTokens
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
		code := "execution_error"
		var timeoutErr *toolTimeoutError
		if errors.As(execErr, &timeoutErr) {
			code = "tool_timeout"
		}
		encoded, err := json.Marshal(map[string]interface{}{
			"ok": false, "tool": toolName, "error_code": code, "message": sanitizeToolError(execErr.Error()),
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
//
// The returned outcome reports what the answer meant for an authorization
// request, so the caller can record the decision.
func (e *Engine) ProvideAskUserResult(userResponse, actorUserID string) (ApprovalOutcome, error) {
	var outcome ApprovalOutcome
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.registry == nil {
		return outcome, fmt.Errorf("tool registry is not initialized")
	}
	if actorUserID == "" || actorUserID != strings.TrimSpace(actorUserID) {
		return outcome, fmt.Errorf("authenticated actor user id is required")
	}
	e.purgeExpiredReceiptsLocked(time.Now())

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
						return outcome, fmt.Errorf("pending user_request_input arguments are invalid: %w", err)
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
		return outcome, fmt.Errorf("no pending user_request_input tool call found")
	}
	if len(pending) != 1 {
		return outcome, fmt.Errorf("expected one pending user response tool call, found %d", len(pending))
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
				outcome.Requested = true
				outcome.Action = ask.Args.Authorization.Action
				outcome.ResourceRef = ask.Args.Authorization.ResourceRef
				var err error
				approved, err = authorizationResponseApproved(userResponse, ask.Args.Authorization)
				if err != nil {
					return outcome, err
				}
			}
			outcome.Approved = approved
			if approved {
				payloadHash, err := authorizationPayloadHash(ask.Args.Authorization.PayloadJSON)
				if err != nil {
					return outcome, err
				}
				now := time.Now()
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
					CreatedAt:    now,
					ExpiresAt:    now.Add(confirmationReceiptTTL),
				}
			}
			toolResult, err := buildAskUserToolResult(userResponse, receiptID)
			if err != nil {
				return outcome, err
			}
			e.history = append(e.history, ConversationItem{
				Role:       LLMRoleTool,
				Content:    toolResult,
				ToolCallID: ask.ID,
			})
			if e.pendingSuspensionID == ask.ID {
				// The question is answered, so pairing repair may treat this
				// call as normal history from now on.
				e.pendingSuspensionID = ""
			}
			appended++
		}
	}

	if appended == 0 {
		return outcome, fmt.Errorf("all user_request_input calls already have results")
	}

	return outcome, nil
}

// blockToolCall records a refused or approval-pending tool call without running
// it. The model receives a structured failure it can act on, the UI sees the
// attempt and the outcome, and the decision is emitted for auditing.
func (e *Engine) blockToolCall(emit func(RuntimeEvent), toolCall LLMToolCall, admission tools.Admission) (bool, error) {
	errorCode := "approval_required"
	if admission.Kind == tools.AdmissionDeny {
		errorCode = "approval_denied"
	}
	if admission.Code != "" {
		errorCode = admission.Code
	}
	encoded, err := json.Marshal(map[string]interface{}{
		"ok":         false,
		"tool":       toolCall.Function.Name,
		"error_code": errorCode,
		"message":    admission.Reason,
	})
	if err != nil {
		return false, err
	}
	result := string(encoded)

	emit(RuntimeEvent{
		Type: EventToolCall,
		Data: ToolCallData{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: json.RawMessage(toolCall.Function.Arguments),
		},
	})
	emit(RuntimeEvent{
		Type: EventApprovalDenied,
		Data: ApprovalDeniedData{
			Tool:     toolCall.Function.Name,
			Decision: string(admission.Kind),
			Reason:   admission.Reason,
		},
	})
	emit(RuntimeEvent{
		Type: EventToolResult,
		Data: ToolResultData{
			ID:      toolCall.ID,
			Name:    toolCall.Function.Name,
			Result:  result,
			Success: false,
		},
	})

	e.mu.Lock()
	e.history = append(e.history, ConversationItem{
		Role:         LLMRoleTool,
		Content:      result,
		ToolCallID:   toolCall.ID,
		ToolCallName: toolCall.Function.Name,
	})
	e.mu.Unlock()
	return true, nil
}

// purgeExpiredReceiptsLocked drops receipts that are past their validity window
// so an old approval cannot be replayed much later. The caller holds e.mu.
func (e *Engine) purgeExpiredReceiptsLocked(now time.Time) {
	for id, receipt := range e.confirmationReceipts {
		if receipt == nil || (!receipt.ExpiresAt.IsZero() && now.After(receipt.ExpiresAt)) {
			delete(e.confirmationReceipts, id)
		}
	}
}

// VerifyApproval implements tools.ApprovalStore. It is the runtime's single
// check that a receipt authorizes exactly the call being admitted, and it fails
// closed on every mismatch.
func (e *Engine) VerifyApproval(receiptID string, request tools.ApprovalRequest) error {
	if receiptID == "" || receiptID != strings.TrimSpace(receiptID) {
		return fmt.Errorf("authorization receipt id must be a non-empty exact value")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.confirmationReceipts == nil {
		return fmt.Errorf("no authorization receipts are available in this session; ask the user for authorization again")
	}
	receipt := e.confirmationReceipts[receiptID]
	if receipt == nil {
		return fmt.Errorf("authorization receipt %q is unknown or has expired; ask the user for authorization again", receiptID)
	}
	if receipt.ConsumedAt != nil {
		return fmt.Errorf("authorization receipt %q has already been used", receiptID)
	}
	if !receipt.ExpiresAt.IsZero() && time.Now().After(receipt.ExpiresAt) {
		delete(e.confirmationReceipts, receiptID)
		return fmt.Errorf("authorization receipt %q has expired; ask the user for authorization again", receiptID)
	}
	if receipt.Action != request.Action {
		return fmt.Errorf("authorization receipt %q approves action %q, not %q", receiptID, receipt.Action, request.Action)
	}
	if receipt.ResourceRef != request.ResourceRef {
		return fmt.Errorf("authorization receipt %q approves resource %q, not %q", receiptID, receipt.ResourceRef, request.ResourceRef)
	}
	payloadHash, err := authorizationPayloadHash(request.PayloadJSON)
	if err != nil {
		return fmt.Errorf("authorization payload is invalid: %w", err)
	}
	if receipt.PayloadHash != payloadHash {
		return fmt.Errorf("authorization receipt %q approves a different payload than the one being committed", receiptID)
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
	if !receipt.ExpiresAt.IsZero() && time.Now().After(receipt.ExpiresAt) {
		delete(e.confirmationReceipts, receiptID)
		return fmt.Errorf("confirmation receipt has expired; ask the user for authorization again")
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
