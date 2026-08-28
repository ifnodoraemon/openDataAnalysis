package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func toolEventEmitterFromContext(ctx tools.ToolContext) func(RuntimeEvent) {
	if ctx.EmitFunc == nil {
		return nil
	}
	return func(ev RuntimeEvent) { ctx.EmitFunc(ev) }
}

func init() {
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Subgoals == nil {
			return nil
		}
		var memory *WorkingMemory
		if ctx.Memory != nil {
			var ok bool
			memory, ok = ctx.Memory.(*WorkingMemory)
			if !ok {
				panic("tool context memory has an invalid type")
			}
		}
		subgoals, ok := ctx.Subgoals.(*SubgoalManager)
		if !ok {
			panic("tool context subgoals has an invalid type")
		}
		return &ManageSubgoalsTool{
			Subgoals: subgoals,
			EmitFunc: toolEventEmitterFromContext(ctx),
			Memory:   memory,
		}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		var memory *WorkingMemory
		if ctx.Memory != nil {
			var ok bool
			memory, ok = ctx.Memory.(*WorkingMemory)
			if !ok {
				panic("tool context memory has an invalid type")
			}
		}
		var subgoals *SubgoalManager
		if ctx.Subgoals != nil {
			var ok bool
			subgoals, ok = ctx.Subgoals.(*SubgoalManager)
			if !ok {
				panic("tool context subgoals has an invalid type")
			}
		}
		return &DelegateTaskTool{
			RegistryFactory: ctx.DelegateRegistryFactory,
			EmitFunc:        toolEventEmitterFromContext(ctx),
			Memory:          memory,
			Subgoals:        subgoals,
		}
	})
}

type ManageSubgoalsTool struct {
	Subgoals *SubgoalManager
	EmitFunc func(RuntimeEvent)
	Memory   *WorkingMemory
}

func (t *ManageSubgoalsTool) SetEventEmitter(emit func(RuntimeEvent)) {
	t.EmitFunc = emit
}

func (t *ManageSubgoalsTool) Name() string {
	return "goal_manage"
}
func (t *ManageSubgoalsTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "action", RuntimeEnabled: true}
}

func (t *ManageSubgoalsTool) Description() string {
	return "Record or update node states in the goal tree. Supports add, complete, reject; only modifies goal state and does not execute tasks. The caller explicitly sets blocking for each added goal; a blocking root branch gates report finalization until terminal. Returns the changed goal_id and current goal tree facts."
}

func (t *ManageSubgoalsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
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
				"description": "Required for action=add. When true on a root goal, report_finalize is blocked until that goal branch is terminal."
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
	if t.EmitFunc == nil {
		return "", fmt.Errorf("goal update event emitter is not initialized")
	}

	var payload struct {
		Action       string `json:"action"`
		Description  string `json:"description"`
		ParentGoalID string `json:"parent_goal_id"`
		Blocking     *bool  `json:"blocking"`
		GoalID       string `json:"goal_id"`
		Result       string `json:"result"`
	}
	if err := jsoncontract.Decode(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}

	switch payload.Action {
	case "add":
		if payload.Description == "" {
			return "", fmt.Errorf("description is required for add action")
		}
		if payload.Blocking == nil {
			return "", fmt.Errorf("blocking must be explicitly true or false for add action")
		}
		if payload.ParentGoalID != strings.TrimSpace(payload.ParentGoalID) {
			return "", fmt.Errorf("parent_goal_id must be an exact value")
		}
		id, addErr := t.Subgoals.AddGoalWithBlocking(payload.Description, payload.ParentGoalID, *payload.Blocking)
		if addErr != nil {
			result := map[string]interface{}{
				"ok":         false,
				"tool":       "goal_manage",
				"action":     "add",
				"error":      addErr.Error(),
				"ui_summary": fmt.Sprintf("目标创建失败：%v", addErr),
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
		result["blocking"] = *payload.Blocking
		if payload.ParentGoalID != "" {
			result["parent_goal_id"] = payload.ParentGoalID
		}
		result["ui_summary"] = fmt.Sprintf("目标 %s 已记录。", id)
		return marshalToolPayload(result)
	case "complete":
		if payload.GoalID == "" || payload.GoalID != strings.TrimSpace(payload.GoalID) {
			return "", fmt.Errorf("goal_id is required for complete action")
		}
		if strings.TrimSpace(payload.Result) == "" {
			return "", fmt.Errorf("result is required for complete action")
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
		result["ui_summary"] = fmt.Sprintf("目标 %s 已标记为完成。", payload.GoalID)
		return marshalToolPayload(result)
	case "reject":
		if payload.GoalID == "" || payload.GoalID != strings.TrimSpace(payload.GoalID) {
			return "", fmt.Errorf("goal_id is required for reject action")
		}
		if strings.TrimSpace(payload.Result) == "" {
			return "", fmt.Errorf("result is required for reject action")
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
		result["ui_summary"] = fmt.Sprintf("目标 %s 已标记为拒绝。", payload.GoalID)
		return marshalToolPayload(result)
	default:
		return "", fmt.Errorf("unknown action: %s", payload.Action)
	}
}

func (t *ManageSubgoalsTool) emitUpdate() {
	t.EmitFunc(RuntimeEvent{
		Type: EventStateSubgoalsUpdated,
		Data: map[string]interface{}{"goals": t.Subgoals.ListAll()},
	})
}

type DelegateTaskTool struct {
	RegistryFactory tools.RegistryFactory
	EmitFunc        func(RuntimeEvent)
	ParentContext   context.Context
	Memory          *WorkingMemory
	Subgoals        *SubgoalManager
}

type delegateTraceItem struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

func (t *DelegateTaskTool) SetEventEmitter(emit func(RuntimeEvent)) {
	t.EmitFunc = emit
}

func (t *DelegateTaskTool) SetExecutionContext(ctx context.Context) {
	t.ParentContext = ctx
}

func (t *DelegateTaskTool) Name() string {
	return "task_delegate"
}
func (t *DelegateTaskTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "action", RuntimeEnabled: true}
}

func (t *DelegateTaskTool) Description() string {
	return "Create a constrained sub-agent and execute a specified task. allowed_tools is intersected with tools whose capability contract declares delegable=true. Returns child status plus structured result IDs, artifact IDs, and tool failures collected during execution; detailed trace remains persisted for UI/debug."
}

func (t *DelegateTaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
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
				"description": "Requested tool capability names. The runtime returns unavailable or non-delegable names as facts."
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
	if t.RegistryFactory == nil {
		return delegateToolFailure("", "", "", nil, "", "delegate_registry_missing", "delegate registry factory is not configured", nil)
	}

	var payload struct {
		RoleName        string   `json:"role_name"`
		PolicyAppendix  string   `json:"policy_appendix"`
		TaskInstruction string   `json:"task_instruction"`
		AllowedTools    []string `json:"allowed_tools"`
		GoalID          string   `json:"goal_id"`
	}
	if err := jsoncontract.Decode(args, &payload); err != nil {
		return delegateToolFailure("", "", "", nil, "", "invalid_arguments", fmt.Sprintf("invalid arguments: %v", err), nil)
	}
	if strings.TrimSpace(payload.RoleName) == "" {
		return delegateToolFailure("", "", payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "missing_role_name", "role_name is required", nil)
	}
	if strings.TrimSpace(payload.TaskInstruction) == "" {
		return delegateToolFailure("", payload.RoleName, "", payload.AllowedTools, payload.GoalID, "missing_task_instruction", "task_instruction is required", nil)
	}
	if len(payload.AllowedTools) == 0 {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, nil, payload.GoalID, "missing_allowed_tools", "allowed_tools is required", nil)
	}
	if payload.RoleName != strings.TrimSpace(payload.RoleName) || payload.GoalID != strings.TrimSpace(payload.GoalID) {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "non_exact_identifier", "role_name and goal_id must use exact values", nil)
	}
	seenAllowedTools := make(map[string]struct{}, len(payload.AllowedTools))
	for _, requested := range payload.AllowedTools {
		if requested == "" || requested != strings.TrimSpace(requested) {
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "non_exact_tool_name", "allowed_tools must contain non-empty exact names", nil)
		}
		if _, exists := seenAllowedTools[requested]; exists {
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "duplicate_tool_name", "allowed_tools must not contain duplicates", map[string]interface{}{"tool_name": requested})
		}
		seenAllowedTools[requested] = struct{}{}
	}
	if err := validatePolicyAppendix(payload.PolicyAppendix); err != nil {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "policy_appendix_invalid", err.Error(), nil)
	}

	subReg := t.buildDelegateRegistry(payload.AllowedTools)
	if subReg == nil {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_registry_unavailable", "delegate registry factory returned no registry", nil)
	}
	resolved := make(map[string]struct{})
	for _, tool := range subReg.ListTools() {
		resolved[tool.Name()] = struct{}{}
	}
	var unavailable []string
	for _, requested := range payload.AllowedTools {
		if _, ok := resolved[requested]; !ok {
			unavailable = append(unavailable, requested)
		}
	}
	if len(unavailable) > 0 {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "tools_not_delegable", "one or more requested tools are unknown, unavailable, or not delegable", map[string]interface{}{"unavailable_tools": unavailable})
	}
	if len(subReg.ListTools()) == 0 {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "no_allowed_tools_resolved", "no allowed tools resolved for delegate", nil)
	}

	parentCtx := t.ParentContext
	if parentCtx == nil {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "execution_context_missing", "delegate execution context is not initialized", nil)
	}
	ctx := parentCtx
	const delegateMaxDuration = 5 * time.Minute
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, delegateMaxDuration)
	defer cancel()

	emit := t.EmitFunc
	if emit == nil {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "event_emitter_missing", "delegate event emitter is not initialized", nil)
	}
	persistence := DelegateRunPersistenceFromContext(ctx)
	if persistence == nil {
		return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "child_run_persistence_missing", "delegate child-run persistence is not initialized", nil)
	}
	childRunID := ""
	childEmit := func(ev RuntimeEvent) {
		if childRunID != "" && ev.RunID == "" {
			ev.RunID = childRunID
		}
		emit(ev)
	}
	if payload.GoalID != "" {
		if t.Subgoals == nil {
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "goal_store_missing", "goal_id was provided but the goal store is unavailable", nil)
		}
		if !t.Subgoals.HasGoal(payload.GoalID) {
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "goal_not_found", "goal_id does not identify an existing goal", nil)
		}
	}
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
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "child_run_start_failed", fmt.Sprintf("failed to start child run: %v", err), nil)
		}
		if childRunID == "" || childRunID != strings.TrimSpace(childRunID) {
			return delegateToolFailure("", payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "child_run_identity_invalid", "delegate persistence returned an invalid child run ID", nil)
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
		parentExecutionMeta := tools.ExecutionMetadataFromContext(ctx)
		childCtx = tools.WithExecutionMetadata(childCtx, tools.ExecutionMetadata{
			UserID:      parentExecutionMeta.UserID,
			WorkspaceID: parentMeta.WorkspaceID,
			SessionID:   parentMeta.SessionID,
			RunID:       childRunID,
		})
	}
	prepareRegistryRuntimeTools(subReg, childCtx, childEmit)

	childPrompt := BuildPolicyPrompt()
	llmClient := NewLLMClient()
	bundle := &PromptBundle{
		Policy:         childPrompt,
		PolicyAppendix: payload.PolicyAppendix,
		Task:           payload.TaskInstruction,
	}
	toolSpecs := subReg.GetToolSpecs()
	trace := make([]delegateTraceItem, 0, 12)
	evidence := delegateEvidence{}

	const maxWorkerIterations = 25
	totalPromptTokens := 0
	totalCompletionTokens := 0
	for i := 0; i < maxWorkerIterations; i++ {
		if childCtx.Err() != nil {
			var cleanupErrors []error
			if persistence != nil && childRunID != "" {
				cancelMsg := "task cancelled"
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if err := persistence.UpdateChildRunStatus(cleanupCtx, childRunID, string(domain.RunStatusCancelled), &cancelMsg); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("persist cancelled child status: %w", err))
				}
				if err := persistence.UpdateChildRunTokens(cleanupCtx, childRunID, totalPromptTokens, totalCompletionTokens); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("persist child tokens: %w", err))
				}
				cleanupCancel()
				childEmit(RuntimeEvent{Type: EventRunCancelled, RunID: childRunID, Data: ErrorData{Message: "任务已取消"}})
			}
			return "", errors.Join(append([]error{childCtx.Err()}, cleanupErrors...)...)
		}

		resp, err := llmClient.ChatWithTools(childCtx, bundle, toolSpecs)
		if err == nil {
			// 累计 token 消耗，并按结构窗口压缩历史。
			totalPromptTokens += resp.Usage.PromptTokens
			totalCompletionTokens += resp.Usage.CompletionTokens

			if bundle.Task != "" {
				bundle.History = append(bundle.History, ConversationItem{
					Role:    LLMRoleUser,
					Content: bundle.Task,
				})
				bundle.Task = ""
			}
			// 子任务仅记录被省略的消息数量，不生成运行时语义摘要。
			compactWorkerBundle(bundle, resp.Usage.PromptTokens)
		}
		if err != nil {
			var stateErrors []string
			if persistence != nil && childRunID != "" {
				msg := err.Error()
				stateErrors = append(stateErrors, persistDelegateFailure(persistence, childCtx, childRunID, domain.RunStatusFailed, &msg, totalPromptTokens, totalCompletionTokens)...)
			}
			return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_execution_failed", fmt.Sprintf("delegated agent failed: %v", err), delegateStatusFacts(domain.RunStatusFailed, stateErrors))
		}
		if len(resp.Choices) == 0 {
			var stateErrors []string
			if persistence != nil && childRunID != "" {
				msg := "delegated agent returned no response"
				stateErrors = append(stateErrors, persistDelegateFailure(persistence, childCtx, childRunID, domain.RunStatusFailed, &msg, totalPromptTokens, totalCompletionTokens)...)
			}
			return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_no_response", "delegated agent returned no response", delegateStatusFacts(domain.RunStatusFailed, stateErrors))
		}

		choice := resp.Choices[0]
		if choice.Message.Content != "" {
			content := choice.Message.Content
			trace = append(trace, delegateTraceItem{Kind: "assistant_status", Summary: clipText(content, 160)})
			ev := RuntimeEvent{Type: EventAssistantStatus, RunID: childRunID, Data: AssistantStatusData{Content: content}}
			childEmit(ev)
		}

		if choice.FinishReason == LLMFinishReasonStop && len(choice.Message.ToolCalls) == 0 {
			result := choice.Message.Content
			if persistence != nil && childRunID != "" {
				stateErrors := persistDelegateCompletion(persistence, childCtx, childRunID, result, totalPromptTokens, totalCompletionTokens)
				if len(stateErrors) > 0 {
					return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "child_run_persistence_failed", "delegated agent finished but its terminal state was not durably persisted", map[string]interface{}{
						"child_result":            result,
						"child_run_status_target": string(domain.RunStatusCompleted),
						"persistence_errors":      stateErrors,
					})
				}
			}
			childEmit(RuntimeEvent{Type: EventRunCompleted, RunID: childRunID, Data: CompleteData{Summary: result}})
			return delegateToolSuccess(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, result, trace, evidence)
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
			callEv := RuntimeEvent{
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
			// 子任务复用同一份类型化瞬态错误重试边界。
			result, execErr := retryableToolExec(childCtx, subReg, toolCall.Function.Name, json.RawMessage(toolCall.Function.Arguments))
			duration := time.Since(start).Milliseconds()
			result, success, contractErr := normalizeToolExecutionResult(toolCall.Function.Name, result, execErr)
			if contractErr != nil {
				execErr = contractErr
			}

			if !success {
				detail := result
				if execErr != nil {
					detail = execErr.Error()
				}
				evidence.ToolFailures = append(evidence.ToolFailures, map[string]string{"tool": toolCall.Function.Name, "detail": clipText(detail, 300)})
				trace = append(trace, delegateTraceItem{
					Kind:    "tool_error",
					Summary: fmt.Sprintf("%s failed: %s", toolCall.Function.Name, clipText(detail, 160)),
				})
				resultEv := RuntimeEvent{Type: EventToolResult, RunID: childRunID, Data: ToolResultData{
					ID:       toolCall.ID,
					Name:     toolCall.Function.Name,
					Result:   result,
					Success:  false,
					Duration: duration,
				}}
				childEmit(resultEv)
				bundle.History = append(bundle.History, ConversationItem{
					Role:         LLMRoleTool,
					Content:      result,
					ToolCallID:   toolCall.ID,
					ToolCallName: toolCall.Function.Name,
				})
				continue
			}
			if evidenceErr := collectDelegateEvidence(result, &evidence); evidenceErr != nil {
				evidence.ToolFailures = append(evidence.ToolFailures, map[string]string{"tool": toolCall.Function.Name, "detail": evidenceErr.Error()})
				failureResult, marshalErr := delegateChildToolFailure(toolCall.Function.Name, "invalid structured tool result: "+evidenceErr.Error())
				if marshalErr != nil {
					return "", fmt.Errorf("failed to encode delegated tool contract failure: %w", marshalErr)
				}
				childEmit(RuntimeEvent{Type: EventToolResult, RunID: childRunID, Data: ToolResultData{ID: toolCall.ID, Name: toolCall.Function.Name, Result: failureResult, Success: false, Duration: duration}})
				bundle.History = append(bundle.History, ConversationItem{Role: LLMRoleTool, Content: failureResult, ToolCallID: toolCall.ID, ToolCallName: toolCall.Function.Name})
				continue
			}

			trace = append(trace, delegateTraceItem{
				Kind:    "tool_result",
				Summary: fmt.Sprintf("%s ok: %s", toolCall.Function.Name, clipDelegateToolResult(result, 160)),
			})
			resultEv := RuntimeEvent{Type: EventToolResult, RunID: childRunID, Data: ToolResultData{
				ID:       toolCall.ID,
				Name:     toolCall.Function.Name,
				Result:   result,
				Success:  true,
				Duration: duration,
			}}
			childEmit(resultEv)
			bundle.History = append(bundle.History, ConversationItem{
				Role:         LLMRoleTool,
				Content:      result,
				ToolCallID:   toolCall.ID,
				ToolCallName: toolCall.Function.Name,
			})
		}
	}

	if persistence != nil && childRunID != "" {
		msg := fmt.Sprintf("delegated agent %s max iterations reached", payload.RoleName)
		stateErrors := persistDelegateFailure(persistence, childCtx, childRunID, domain.RunStatusFailed, &msg, totalPromptTokens, totalCompletionTokens)
		return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_max_iterations_reached", msg, delegateStatusFacts(domain.RunStatusFailed, stateErrors))
	}
	return delegateToolFailure(childRunID, payload.RoleName, payload.TaskInstruction, payload.AllowedTools, payload.GoalID, "delegate_max_iterations_reached", fmt.Sprintf("delegated agent %s max iterations reached", payload.RoleName), map[string]interface{}{
		"child_run_status_target": string(domain.RunStatusFailed),
	})
}

func persistDelegateFailure(persistence DelegateRunPersistence, ctx context.Context, childRunID string, status domain.RunStatus, message *string, promptTokens, completionTokens int) []string {
	var failures []string
	if err := persistence.UpdateChildRunStatus(ctx, childRunID, string(status), message); err != nil {
		failures = append(failures, "update child status: "+err.Error())
	}
	if err := persistence.UpdateChildRunTokens(ctx, childRunID, promptTokens, completionTokens); err != nil {
		failures = append(failures, "update child tokens: "+err.Error())
	}
	return failures
}

func persistDelegateCompletion(persistence DelegateRunPersistence, ctx context.Context, childRunID, summary string, promptTokens, completionTokens int) []string {
	var failures []string
	if err := persistence.UpdateChildRunSummary(ctx, childRunID, summary); err != nil {
		failures = append(failures, "update child summary: "+err.Error())
	}
	failures = append(failures, persistDelegateFailure(persistence, ctx, childRunID, domain.RunStatusCompleted, nil, promptTokens, completionTokens)...)
	return failures
}

func delegateStatusFacts(status domain.RunStatus, persistenceErrors []string) map[string]interface{} {
	facts := map[string]interface{}{"child_run_status_target": string(status)}
	if len(persistenceErrors) == 0 {
		facts["child_run_status"] = string(status)
	} else {
		facts["persistence_errors"] = persistenceErrors
	}
	return facts
}

type delegateEvidence struct {
	ResultIDs    []string
	ArtifactIDs  []string
	ToolFailures []map[string]string
}

func delegateToolSuccess(childRunID, roleName, taskInstruction string, allowedTools []string, goalID, summary string, trace []delegateTraceItem, evidence delegateEvidence) (string, error) {
	payload := map[string]interface{}{
		"ok":               true,
		"tool":             "task_delegate",
		"child_run_id":     childRunID,
		"delegate_role":    roleName,
		"task_instruction": taskInstruction,
		"allowed_tools":    allowedTools,
		"child_result":     summary,
		"child_run_status": string(domain.RunStatusCompleted),
		"ui_summary":       fmt.Sprintf("子智能体 %s 已完成：%s", roleName, summary),
		"trace_count":      len(trace),
		"result_ids":       evidence.ResultIDs,
		"artifact_ids":     evidence.ArtifactIDs,
		"tool_failures":    evidence.ToolFailures,
	}
	if goalID != "" {
		payload["goal_id"] = goalID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func collectDelegateEvidence(raw string, evidence *delegateEvidence) error {
	if evidence == nil {
		return fmt.Errorf("delegate evidence collector is unavailable")
	}
	var payload map[string]interface{}
	if err := jsoncontract.Decode([]byte(raw), &payload); err != nil {
		return err
	}
	if value, exists := payload["result_id"]; exists {
		id, ok := value.(string)
		if !ok || id == "" || id != strings.TrimSpace(id) {
			return fmt.Errorf("result_id must be a non-empty exact string")
		}
		evidence.ResultIDs = appendUniqueString(evidence.ResultIDs, id)
	}
	if value, exists := payload["artifacts"]; exists {
		artifacts, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("artifacts must be an array")
		}
		for _, item := range artifacts {
			object, ok := item.(map[string]interface{})
			if !ok {
				return fmt.Errorf("artifact entries must be objects")
			}
			id, ok := object["id"].(string)
			if !ok || id == "" || id != strings.TrimSpace(id) {
				return fmt.Errorf("artifact id must be a non-empty exact string")
			}
			evidence.ArtifactIDs = appendUniqueString(evidence.ArtifactIDs, id)
		}
	}
	return nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func delegateToolFailure(childRunID, roleName, taskInstruction string, allowedTools []string, goalID, code, message string, extra map[string]interface{}) (string, error) {
	payload := map[string]interface{}{
		"ok":               false,
		"tool":             "task_delegate",
		"error_code":       code,
		"message":          message,
		"delegate_role":    roleName,
		"task_instruction": taskInstruction,
		"allowed_tools":    allowedTools,
		"ui_summary":       fmt.Sprintf("子智能体 %s 执行失败。", roleName),
	}
	if strings.TrimSpace(childRunID) != "" {
		payload["child_run_id"] = childRunID
	}
	if strings.TrimSpace(goalID) != "" {
		payload["goal_id"] = goalID
	}
	if strings.TrimSpace(roleName) == "" {
		payload["ui_summary"] = "子智能体执行失败。"
	}
	for key, value := range extra {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (t *DelegateTaskTool) buildDelegateRegistry(allowed []string) *tools.Registry {
	return t.RegistryFactory(allowed)
}

func prepareRegistryRuntimeTools(reg *tools.Registry, ctx context.Context, emit func(RuntimeEvent)) {
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
	if raw != trimmed {
		return fmt.Errorf("policy_appendix must not contain leading or trailing whitespace")
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
	return nil
}

func delegateChildToolFailure(toolName, message string) (string, error) {
	payload := map[string]interface{}{
		"ok":         false,
		"tool":       toolName,
		"error_code": "execution_error",
		"message":    message,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func clipDelegateToolResult(raw string, max int) string {
	var payload map[string]interface{}
	if err := jsoncontract.Decode([]byte(raw), &payload); err == nil {
		if summary, ok := payload["ui_summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return clipText(summary, max)
		}
		encoded, err := json.Marshal(payload)
		if err == nil {
			return clipText(string(encoded), max)
		}
	}
	return clipText(raw, max)
}

// clipText 已迁移至 stringutil.go clipText
