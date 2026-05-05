package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func init() {
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Subgoals == nil {
			return nil
		}
		var memory *WorkingMemory
		if ctx.Memory != nil {
			if typed, ok := ctx.Memory.(*WorkingMemory); ok {
				memory = typed
			}
		}
		return &ManageSubgoalsTool{
			Subgoals: func() *SubgoalManager {
				if sm, ok := ctx.Subgoals.(*SubgoalManager); ok {
					return sm
				}
				return nil
			}(),
			EmitFunc: func(event WSEvent) {
				if ctx.EmitFunc != nil {
					ctx.EmitFunc(event)
				}
			},
			Memory: memory,
		}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		var memory *WorkingMemory
		if ctx.Memory != nil {
			if typed, ok := ctx.Memory.(*WorkingMemory); ok {
				memory = typed
			}
		}
		var subgoals *SubgoalManager
		if ctx.Subgoals != nil {
			if sm, ok := ctx.Subgoals.(*SubgoalManager); ok {
				subgoals = sm
			}
		}
		return &DelegateTaskTool{
			BaseRegistry: ctx.DelegateRegistry,
			EmitFunc: func(event WSEvent) {
				if ctx.EmitFunc != nil {
					ctx.EmitFunc(event)
				}
			},
			Memory:   memory,
			Subgoals: subgoals,
		}
	})
}

type ManageSubgoalsTool struct {
	Subgoals *SubgoalManager
	EmitFunc func(WSEvent)
	Memory   *WorkingMemory
}

func (t *ManageSubgoalsTool) SetEventEmitter(emit func(WSEvent)) {
	t.EmitFunc = emit
}

func (t *ManageSubgoalsTool) Name() string {
	return "goal_manage"
}

func (t *ManageSubgoalsTool) Description() string {
	return "Record or update node states in the goal tree. Supports add, complete, reject; only modifies goal state, does not execute tasks. Added goals are non-blocking scratchpad notes unless blocking=true is provided. Returns the changed goal_id and current goal tree facts."
}

func (t *ManageSubgoalsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["add", "complete", "reject"],
				"description": "Action to perform. add (record a goal on the board), complete (mark goal as fully resolved), reject (goal cannot be completed, abandon)."
			},
			"description": {
				"type": "string",
				"description": "Required only for action=add. Clear description of the goal to record."
			},
			"parent_goal_id": {
				"type": "string",
				"description": "Optional, only for action=add. Parent goal ID. Used to express that the current goal is a sub-step of a larger goal."
			},
			"blocking": {
				"type": "boolean",
				"description": "Optional, only for action=add. When true on a root goal, report_finalize is blocked until that goal branch is terminal. Defaults to false for scratchpad goals."
			},
			"goal_id": {
				"type": "string",
				"description": "Required for action=complete or reject. The goal ID whose status you want to change."
			},
			"result": {
				"type": "string",
				"description": "Required for action=complete or reject. Final conclusion or reason for abandonment to append to the goal."
			}
		},
		"required": ["action"]
	}`)
}

func (t *ManageSubgoalsTool) Execute(args json.RawMessage) (string, error) {
	if t.Subgoals == nil {
		return "", fmt.Errorf("subgoal manager is not initialized")
	}

	var payload struct {
		Action       string `json:"action"`
		Description  string `json:"description"`
		ParentGoalID string `json:"parent_goal_id"`
		Blocking     bool   `json:"blocking"`
		GoalID       string `json:"goal_id"`
		Result       string `json:"result"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}

	switch payload.Action {
	case "add":
		if payload.Description == "" {
			return "", fmt.Errorf("description is required for add action")
		}
		id, addErr := t.Subgoals.AddGoalWithBlocking(payload.Description, payload.ParentGoalID, payload.Blocking)
		if addErr != nil {
			result := map[string]interface{}{
				"ok":         false,
				"tool":       "goal_manage",
				"action":     "add",
				"error":      addErr.Error(),
				"ui_summary": fmt.Sprintf("Goal creation failed: %v", addErr),
			}
			return marshalToolPayload(result)
		}
		t.emitUpdate()
		result := buildGoalStateFacts(t.Subgoals, false)
		result["ok"] = true
		result["tool"] = "goal_manage"
		result["action"] = "add"
		result["goal_id"] = id
		result["status"] = StatusPending
		result["description"] = payload.Description
		result["blocking"] = payload.Blocking
		if strings.TrimSpace(payload.ParentGoalID) != "" {
			result["parent_goal_id"] = payload.ParentGoalID
		}
		result["ui_summary"] = fmt.Sprintf("Goal %s recorded.", id)
		return marshalToolPayload(result)
	case "complete":
		if payload.GoalID == "" {
			return "", fmt.Errorf("goal_id is required for complete action")
		}
		if err := t.Subgoals.UpdateGoalStatus(payload.GoalID, StatusComplete, payload.Result); err != nil {
			return "", err
		}
		t.emitUpdate()
		result := buildGoalStateFacts(t.Subgoals, false)
		result["ok"] = true
		result["tool"] = "goal_manage"
		result["action"] = "complete"
		result["goal_id"] = payload.GoalID
		result["status"] = StatusComplete
		result["result"] = payload.Result
		result["ui_summary"] = fmt.Sprintf("Goal %s marked as complete.", payload.GoalID)
		return marshalToolPayload(result)
	case "reject":
		if payload.GoalID == "" {
			return "", fmt.Errorf("goal_id is required for reject action")
		}
		if err := t.Subgoals.UpdateGoalStatus(payload.GoalID, StatusRejected, payload.Result); err != nil {
			return "", err
		}
		t.emitUpdate()
		result := buildGoalStateFacts(t.Subgoals, false)
		result["ok"] = true
		result["tool"] = "goal_manage"
		result["action"] = "reject"
		result["goal_id"] = payload.GoalID
		result["status"] = StatusRejected
		result["result"] = payload.Result
		result["ui_summary"] = fmt.Sprintf("Goal %s marked as rejected.", payload.GoalID)
		return marshalToolPayload(result)
	default:
		return "", fmt.Errorf("unknown action: %s", payload.Action)
	}
}

func (t *ManageSubgoalsTool) emitUpdate() {
	if t.EmitFunc == nil {
		return
	}
	t.EmitFunc(WSEvent{
		Type: EventStateSubgoalsUpdated,
		Data: map[string]interface{}{"goals": t.Subgoals.ListAll()},
	})
}

type DelegateTaskTool struct {
	BaseRegistry    *tools.Registry
	RegistryFactory func([]string) *tools.Registry
	EmitFunc        func(WSEvent)
	ParentContext   context.Context
	Memory          *WorkingMemory
	Subgoals        *SubgoalManager
}

type delegateTraceItem struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

func (t *DelegateTaskTool) SetEventEmitter(emit func(WSEvent)) {
	t.EmitFunc = emit
}

func (t *DelegateTaskTool) SetExecutionContext(ctx context.Context) {
	t.ParentContext = ctx
}

func (t *DelegateTaskTool) Name() string {
	return "task_delegate"
}

func (t *DelegateTaskTool) Description() string {
	return "Create a constrained sub-agent and execute a specified task. Reads role_name, task_instruction, allowed_tools, and optional goal_id/policy_appendix; allowed_tools only describes the tool boundary visible to the sub-agent; user_request_input and report_finalize cannot be delegated. On success returns child_run_id, child_result, trace_count, and child_run_status; detailed child trace is persisted for UI/debug instead of being returned to the parent context."
}

func (t *DelegateTaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"role_name": {
				"type": "string",
				"description": "Label for the sub-agent, used to distinguish different instances."
			},
			"policy_appendix": {
				"type": "string",
				"description": "Additional constraint rules appended to the sub-agent. Limited to behavioral guidelines; background facts, data samples, or history records are prohibited."
			},
			"task_instruction": {
				"type": "string",
				"description": "The specific task for the sub-agent to complete."
			},
			"allowed_tools": {
				"type": "array",
				"items": {"type": "string"},
				"description": "List of tools the sub-agent is allowed to use. Cannot include user_request_input or report_finalize."
			},
			"goal_id": {
				"type": "string",
				"description": "Optional. Associated goal ID."
			}
		},
		"required": ["role_name", "task_instruction", "allowed_tools"]
	}`)
}

func (t *DelegateTaskTool) Execute(args json.RawMessage) (string, error) {
	if t.BaseRegistry == nil && t.RegistryFactory == nil {
		return delegateToolFailure("", "", "", nil, "", "delegate_registry_missing", "delegate base registry is not configured", nil), nil
	}

	var payload struct {
		RoleName        string   `json:"role_name"`
		PolicyAppendix  string   `json:"policy_appendix"`
		TaskInstruction string   `json:"task_instruction"`
		AllowedTools    []string `json:"allowed_tools"`
		GoalID          string   `json:"goal_id"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return delegateToolFailure("", "", "", nil, "", "invalid_arguments", fmt.Sprintf("invalid arguments: %v", err), nil), nil
	}
	if strings.TrimSpace(payload.RoleName) == "" {
		return delegateToolFailure("", "", payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "missing_role_name", "role_name is required", nil), nil
	}
	if strings.TrimSpace(payload.TaskInstruction) == "" {
		return delegateToolFailure("", payload.RoleName, "", payload.AllowedTools, payload.GoalID, "missing_task_instruction", "task_instruction is required", nil), nil
	}
	if len(payload.AllowedTools) == 0 {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, nil, payload.GoalID, "missing_allowed_tools", "allowed_tools is required", nil), nil
	}
	if forbidden := disallowedDelegateTools(payload.AllowedTools); len(forbidden) > 0 {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "disallowed_delegate_tools", "delegate cannot use these tools: "+strings.Join(forbidden, ", "), map[string]interface{}{
			"disallowed_tools": forbidden,
		}), nil
	}
	if err := validatePolicyAppendix(payload.PolicyAppendix); err != nil {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "policy_appendix_invalid", err.Error(), nil), nil
	}

	subReg := t.buildDelegateRegistry(payload.AllowedTools)
	if len(subReg.ListTools()) == 0 {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "no_allowed_tools_resolved", "no allowed tools resolved for delegate", nil), nil
	}

	parentCtx := t.ParentContext
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	parentEmit := t.EmitFunc

	ctx := parentCtx
	const delegateMaxDuration = 5 * time.Minute
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, delegateMaxDuration)
	defer cancel()

	emit := t.EmitFunc
	if emit == nil {
		emit = func(WSEvent) {}
	}
	persistence := DelegateRunPersistenceFromContext(ctx)
	childRunID := ""
	if persistence != nil {
		var err error
		childRunID, err = persistence.StartChildRun(ctx, ChildRunStart{
			ParentRunID:  TraceMetadataFromContext(ctx).RunID,
			RoleName:     payload.RoleName,
			InputMessage: payload.TaskInstruction,
			GoalID:       payload.GoalID,
			AllowedTools: payload.AllowedTools,
		})
		if err != nil {
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "child_run_start_failed", fmt.Sprintf("failed to start child run: %v", err), nil), nil
		}
	}
	childCtx := ctx
	if childRunID != "" {
		parentMeta := TraceMetadataFromContext(ctx)
		childMeta := TraceMetadata{
			WorkspaceID: parentMeta.WorkspaceID,
			SessionID:   parentMeta.SessionID,
			RunID:       childRunID,
			TraceID:     childRunID,
		}
		childCtx = WithTraceMetadata(ctx, childMeta)
		childCtx = tools.WithExecutionMetadata(childCtx, tools.ExecutionMetadata{
			WorkspaceID: parentMeta.WorkspaceID,
			SessionID:   parentMeta.SessionID,
			RunID:       childRunID,
		})
	}
	childEmit := func(ev WSEvent) {
		if strings.TrimSpace(childRunID) != "" && strings.TrimSpace(ev.RunID) == "" {
			ev.RunID = childRunID
		}
		emit(ev)
	}
	prepareRegistryRuntimeTools(subReg, childCtx, childEmit)
	defer t.restoreParentRegistryRuntimeTools(parentCtx, parentEmit)

	if t.Subgoals != nil && payload.GoalID != "" {
		if err := t.Subgoals.UpdateGoalStatus(payload.GoalID, StatusRunning, ""); err == nil {
			childEmit(WSEvent{
				Type: EventStateSubgoalsUpdated,
				Data: map[string]interface{}{"goals": t.Subgoals.ListAll()},
			})
		}
	}

	childPrompt := BuildPolicyPrompt()
	llmClient := NewLLMClient()
	bundle := &PromptBundle{
		Policy:         childPrompt,
		PolicyAppendix: strings.TrimSpace(payload.PolicyAppendix),
		Task:           payload.TaskInstruction,
	}
	toolSpecs := subReg.GetToolSpecs()
	trace := make([]delegateTraceItem, 0, 12)

	const maxWorkerIterations = 25
	totalPromptTokens := 0
	totalCompletionTokens := 0
	for i := 0; i < maxWorkerIterations; i++ {
		if childCtx.Err() != nil {
			if t.Subgoals != nil && payload.GoalID != "" {
				_ = t.Subgoals.UpdateGoalStatus(payload.GoalID, StatusPending, "task cancelled")
				childEmit(WSEvent{
					Type: EventStateSubgoalsUpdated,
					Data: map[string]interface{}{"goals": t.Subgoals.ListAll()},
				})
			}
			if persistence != nil && childRunID != "" {
				cancelMsg := "task cancelled"
				// 使用独立的 Background context 确保取消后仍能写入 DB
				bgCtx := context.Background()
				logPersistErr("UpdateChildRunStatus", persistence.UpdateChildRunStatus(bgCtx, childRunID, string(domain.RunStatusCancelled), &cancelMsg))
				// 补充 ISSUE5 修复：取消时也记录已累计的 token 消耗
				logPersistErr("UpdateChildRunTokens", persistence.UpdateChildRunTokens(bgCtx, childRunID, totalPromptTokens, totalCompletionTokens))
			}
			return "", childCtx.Err()
		}

		resp, err := llmClient.ChatWithTools(childCtx, bundle, toolSpecs)
		if err == nil {
			// 累计 token 消耗，并触发上下文压缩
			totalPromptTokens += resp.Usage.PromptTokens
			totalCompletionTokens += resp.Usage.CompletionTokens

			if bundle.Task != "" {
				bundle.History = append(bundle.History, ConversationItem{
					Role:    LLMRoleUser,
					Content: bundle.Task,
				})
				bundle.Task = ""
			}
			// ISSUE6 注释：
			// 注意，这里的 compactWorkerBundle 逻辑与 engine.go 中的 compactMessagesLocked 不同。
			// 主代理由于需要维护长期存在的 Session，会执行较激进的压缩（提炼 Digest、保留少数最近的对话）；
			// 而子代理 (worker) 的生命周期较短，关注点更内聚。为了避免中间过程的细节事实在频繁提炼中丢失，
			// 我们通常采取较温和的滑动窗口策略（例如仅截断较早轮次，而不强制使用大范围摘要压缩）。
			compactWorkerBundle(bundle, resp.Usage.PromptTokens)
		}
		if err != nil {
			if t.Subgoals != nil && payload.GoalID != "" {
				_ = t.Subgoals.UpdateGoalStatus(payload.GoalID, StatusPending, err.Error())
				childEmit(WSEvent{
					Type: EventStateSubgoalsUpdated,
					Data: map[string]interface{}{"goals": t.Subgoals.ListAll()},
				})
			}
			if persistence != nil && childRunID != "" {
				msg := err.Error()
				logPersistErr("UpdateChildRunStatus", persistence.UpdateChildRunStatus(childCtx, childRunID, string(domain.RunStatusFailed), &msg))
				logPersistErr("UpdateChildRunTokens", persistence.UpdateChildRunTokens(childCtx, childRunID, totalPromptTokens, totalCompletionTokens))
			}
			return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_execution_failed", fmt.Sprintf("delegated agent failed: %v", err), map[string]interface{}{
				"child_run_status": string(domain.RunStatusFailed),
			}), nil
		}
		if len(resp.Choices) == 0 {
			if persistence != nil && childRunID != "" {
				msg := "delegated agent returned no response"
				logPersistErr("UpdateChildRunStatus", persistence.UpdateChildRunStatus(childCtx, childRunID, string(domain.RunStatusFailed), &msg))
			}
			return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_no_response", "delegated agent returned no response", map[string]interface{}{
				"child_run_status": string(domain.RunStatusFailed),
			}), nil
		}

		choice := resp.Choices[0]
		if choice.Message.Content != "" {
			content := strings.TrimSpace(choice.Message.Content)
			trace = append(trace, delegateTraceItem{Kind: "assistant_status", Summary: clipText(content, 160)})
			ev := WSEvent{Type: EventAssistantStatus, RunID: childRunID, Data: AssistantStatusData{Content: content}}
			childEmit(ev)
		}

		if choice.FinishReason == LLMFinishReasonStop && len(choice.Message.ToolCalls) == 0 {
			result := strings.TrimSpace(choice.Message.Content)
			childEmit(WSEvent{Type: EventRunCompleted, RunID: childRunID, Data: CompleteData{Summary: result}})
			if persistence != nil && childRunID != "" {
				logPersistErr("UpdateChildRunSummary", persistence.UpdateChildRunSummary(childCtx, childRunID, result))
				logPersistErr("UpdateChildRunStatus", persistence.UpdateChildRunStatus(childCtx, childRunID, string(domain.RunStatusCompleted), nil))
				logPersistErr("UpdateChildRunTokens", persistence.UpdateChildRunTokens(childCtx, childRunID, totalPromptTokens, totalCompletionTokens))
			}
			return delegateToolSuccess(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, result, trace), nil
		}

		if len(choice.Message.ToolCalls) == 0 {
			continue
		}

		bundle.History = append(bundle.History, compactAssistantMessage(choice.Message))
		for _, toolCall := range choice.Message.ToolCalls {
			trace = append(trace, delegateTraceItem{
				Kind:    "tool_call",
				Summary: fmt.Sprintf("%s(%s)", toolCall.Function.Name, clipText(toolCall.Function.Arguments, 120)),
			})
			callEv := WSEvent{
				Type:  EventToolCall,
				RunID: childRunID,
				Data: ToolCallData{
					ID:        toolCall.ID,
					Name:      toolCall.Function.Name,
					Arguments: json.RawMessage(toolCall.Function.Arguments),
				},
			}
			childEmit(callEv)

			start := time.Now()
			// 修复 #13：子代理工具调用与主代理保持一致，走 retryableToolExec 指数退避重试
			result, execErr := retryableToolExec(childCtx, subReg, toolCall.Function.Name, json.RawMessage(toolCall.Function.Arguments))
			duration := time.Since(start).Milliseconds()

			if execErr != nil {
				trace = append(trace, delegateTraceItem{
					Kind:    "tool_error",
					Summary: fmt.Sprintf("%s failed: %s", toolCall.Function.Name, clipText(execErr.Error(), 160)),
				})
				resultEv := WSEvent{Type: EventToolResult, RunID: childRunID, Data: ToolResultData{
					ID:       toolCall.ID,
					Name:     toolCall.Function.Name,
					Result:   delegateChildToolFailure(toolCall.Function.Name, execErr.Error()),
					Success:  false,
					Duration: duration,
				}}
				childEmit(resultEv)
				bundle.History = append(bundle.History, ConversationItem{
					Role:       LLMRoleTool,
					Content:    delegateChildToolFailure(toolCall.Function.Name, execErr.Error()),
					ToolCallID: toolCall.ID,
				})
				continue
			}

			trace = append(trace, delegateTraceItem{
				Kind:    "tool_result",
				Summary: fmt.Sprintf("%s ok: %s", toolCall.Function.Name, clipDelegateToolResult(result, 160)),
			})
			resultEv := WSEvent{Type: EventToolResult, RunID: childRunID, Data: ToolResultData{
				ID:       toolCall.ID,
				Name:     toolCall.Function.Name,
				Result:   result,
				Success:  true,
				Duration: duration,
			}}
			childEmit(resultEv)
			bundle.History = append(bundle.History, ConversationItem{
				Role:       LLMRoleTool,
				Content:    result,
				ToolCallID: toolCall.ID,
			})
		}
	}

	if persistence != nil && childRunID != "" {
		msg := fmt.Sprintf("delegated agent %s max iterations reached", payload.RoleName)
		logPersistErr("UpdateChildRunStatus", persistence.UpdateChildRunStatus(childCtx, childRunID, string(domain.RunStatusFailed), &msg))
		logPersistErr("UpdateChildRunTokens", persistence.UpdateChildRunTokens(childCtx, childRunID, totalPromptTokens, totalCompletionTokens))
	}
	return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_max_iterations_reached", fmt.Sprintf("delegated agent %s max iterations reached", payload.RoleName), map[string]interface{}{
		"child_run_status": string(domain.RunStatusFailed),
	}), nil
}

func (t *DelegateTaskTool) restoreParentRegistryRuntimeTools(ctx context.Context, emit func(WSEvent)) {
	if t.BaseRegistry == nil {
		return
	}
	prepareRegistryRuntimeTools(t.BaseRegistry, ctx, emit)
}

func delegateToolSuccess(childRunID, roleName, taskInstruction string, allowedTools []string, goalID, summary string, trace []delegateTraceItem) string {
	payload := map[string]interface{}{
		"ok":               true,
		"tool":             "task_delegate",
		"child_run_id":     childRunID,
		"delegate_role":    roleName,
		"task_instruction": taskInstruction,
		"allowed_tools":    allowedTools,
		"child_result":     summary,
		"child_run_status": string(domain.RunStatusCompleted),
		"ui_summary":       fmt.Sprintf("Sub-agent %s completed: %s", roleName, summary),
		"trace_count":      len(trace),
	}
	if strings.TrimSpace(goalID) != "" {
		payload["goal_id"] = goalID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"tool":"task_delegate","message":"delegate response marshal failed"}`
	}
	return string(encoded)
}

func delegateToolFailure(childRunID, roleName, taskInstruction string, allowedTools []string, goalID, code, message string, extra map[string]interface{}) string {
	payload := map[string]interface{}{
		"ok":               false,
		"tool":             "task_delegate",
		"error_code":       code,
		"message":          message,
		"delegate_role":    roleName,
		"task_instruction": taskInstruction,
		"allowed_tools":    allowedTools,
		"ui_summary":       fmt.Sprintf("Sub-agent %s failed.", roleName),
	}
	if strings.TrimSpace(childRunID) != "" {
		payload["child_run_id"] = childRunID
	}
	if strings.TrimSpace(goalID) != "" {
		payload["goal_id"] = goalID
	}
	if strings.TrimSpace(roleName) == "" {
		payload["ui_summary"] = "Sub-agent failed."
	}
	for key, value := range extra {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"tool":"task_delegate","error_code":"delegate_response_marshal_failed","message":"delegate response marshal failed"}`
	}
	return string(encoded)
}

func (t *DelegateTaskTool) buildDelegateRegistry(allowed []string) *tools.Registry {
	if t.RegistryFactory != nil {
		return t.RegistryFactory(allowed)
	}
	if t.BaseRegistry == nil {
		return tools.NewRegistry()
	}
	return t.BaseRegistry.CloneFiltered(allowed)
}

func prepareRegistryRuntimeTools(reg *tools.Registry, ctx context.Context, emit func(WSEvent)) {
	if reg == nil {
		return
	}
	for _, tool := range reg.ListTools() {
		if next, ok := tool.(eventEmitterAware); ok {
			next.SetEventEmitter(emit)
		}
		if next, ok := tool.(executionContextAware); ok {
			next.SetExecutionContext(ctx)
		}
	}
}

func validatePolicyAppendix(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if len([]rune(trimmed)) > 280 {
		return fmt.Errorf("policy_appendix too long; it can only contain short constraints, not context dumps")
	}
	lines := strings.Split(trimmed, "\n")
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	if nonEmpty > 6 {
		return fmt.Errorf("policy_appendix has too many lines; it can only contain a few constraint rules")
	}
	lower := strings.ToLower(trimmed)
	suspiciousTokens := []string{
		"```", "{\"", "\"tool\"", "|---", "context:", "history:", "runtime:", "schema:",
		"背景：", "背景:", "上下文：", "上下文:", "历史：", "历史:", "已知事实", "用户原话", "表结构", "列如下",
	}
	for _, token := range suspiciousTokens {
		if strings.Contains(lower, strings.ToLower(token)) {
			return fmt.Errorf("policy_appendix can only contain constraints, not background facts, history records, or structured context dumps")
		}
	}
	return nil
}

func disallowedDelegateTools(allowed []string) []string {
	disallowedSet := map[string]struct{}{
		"user_request_input": {},
		"report_finalize":    {},
		"task_delegate":      {},
	}
	var forbidden []string
	for _, name := range allowed {
		trimmed := strings.TrimSpace(name)
		if _, ok := disallowedSet[trimmed]; ok {
			forbidden = append(forbidden, trimmed)
		}
	}
	return forbidden
}

func delegateChildToolFailure(toolName, message string) string {
	payload := map[string]interface{}{
		"ok":         false,
		"tool":       toolName,
		"error_code": "execution_error",
		"message":    message,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"tool":"%s","error_code":"execution_error","message":"%s"}`, toolName, clipText(message, 120))
	}
	return string(encoded)
}

func clipDelegateToolResult(raw string, max int) string {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); err == nil {
		if summary, ok := payload["ui_summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return clipText(summary, max)
		}
		if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
			return clipText(message, max)
		}
		encoded, _ := json.Marshal(payload)
		return clipText(string(encoded), max)
	}
	return clipText(raw, max)
}

// clipText 已迁移至 stringutil.go clipText

func logPersistErr(op string, err error) {
	if err != nil {
		log.Printf("delegate persistence: %s failed: %v", op, err)
	}
}
