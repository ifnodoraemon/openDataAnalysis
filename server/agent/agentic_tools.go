package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func init() {
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Memory == nil {
			return nil
		}
		memory, ok := ctx.Memory.(*WorkingMemory)
		if !ok {
			panic("tool context memory has an invalid type")
		}
		return &SaveMemoryTool{
			Memory:      memory,
			ReportState: ctx.ReportState,
			EmitFunc:    toolEventEmitterFromContext(ctx),
		}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Memory == nil {
			return nil
		}
		memory, ok := ctx.Memory.(*WorkingMemory)
		if !ok {
			panic("tool context memory has an invalid type")
		}
		return &InspectMemoryTool{Memory: memory}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		return &AskUserTool{}
	})
}

// SaveMemoryTool 允许 Agent 主动将关键结论写入 Working Memory，以便脱离上下文窗口长期保存
type SaveMemoryTool struct {
	Memory      *WorkingMemory
	ReportState *tools.ReportState
	EmitFunc    func(RuntimeEvent)
}

type InspectMemoryTool struct {
	Memory *WorkingMemory
}

func (t *SaveMemoryTool) Name() string {
	return "memory_save_entry"
}
func (t *SaveMemoryTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "action", RuntimeEnabled: true, Delegable: true}
}

func (t *SaveMemoryTool) Description() string {
	return "Record a typed working-memory entry as observed, inferred, or assumed. Observed entries require valid analysis result IDs; inferred entries may cite results; assumptions are explicitly labeled. The tool cannot create user-confirmed facts."
}

func (t *SaveMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"key": {
				"type": "string",
				"description": "Caller-defined memory key. Writing the same key overwrites the old entry."
			},
			"statement": {
				"type": "string",
				"description": "The statement to retain."
			},
			"status":{"type":"string","enum":["observed","inferred","assumed"]},
			"source_result_ids":{"type":"array","items":{"type":"string"}},
			"confidence":{"type":"number","minimum":0,"maximum":1}
		},
		"required": ["key", "statement", "status"]
	}`)
}

func (t *SaveMemoryTool) Execute(args json.RawMessage) (string, error) {
	if t.Memory == nil {
		return "", fmt.Errorf("working memory is not initialized")
	}
	if t.EmitFunc == nil {
		return "", fmt.Errorf("memory update event emitter is not initialized")
	}

	var payload struct {
		Key             string   `json:"key"`
		Statement       string   `json:"statement"`
		Status          string   `json:"status"`
		SourceResultIDs []string `json:"source_result_ids"`
		Confidence      *float64 `json:"confidence"`
	}
	if err := jsoncontract.Decode(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}

	if payload.Key == "" || payload.Statement == "" {
		return "", fmt.Errorf("key and statement are required")
	}
	if payload.Status != "observed" && payload.Status != "inferred" && payload.Status != "assumed" {
		return "", fmt.Errorf("status must be observed, inferred, or assumed")
	}
	if payload.Status == "observed" && len(payload.SourceResultIDs) == 0 {
		return "", fmt.Errorf("observed entries require source_result_ids")
	}
	for _, resultID := range payload.SourceResultIDs {
		if t.ReportState == nil {
			return "", fmt.Errorf("analysis result store is unavailable")
		}
		t.ReportState.RLock()
		_, ok := t.ReportState.Results[resultID]
		t.ReportState.RUnlock()
		if !ok {
			return "", fmt.Errorf("analysis result %s not found", resultID)
		}
	}

	_, existed := t.Memory.EntrySnapshot()[payload.Key]
	saved, err := t.Memory.SaveEntry(payload.Key, MemoryEntry{Statement: payload.Statement, Status: payload.Status, SourceResultIDs: payload.SourceResultIDs, Confidence: payload.Confidence, CreatedBy: "agent", CreatedAt: time.Now()})
	if err != nil {
		return "", err
	}
	if !saved {
		return marshalToolPayload(map[string]interface{}{
			"ok":          false,
			"tool":        "memory_save_entry",
			"memory_key":  payload.Key,
			"error":       fmt.Sprintf("working memory full (%d entries max)", maxMemoryEntries),
			"entry_count": len(t.Memory.EntrySnapshot()),
			"ui_summary":  fmt.Sprintf("工作记忆已满，无法保存 [%s]。", payload.Key),
		})
	}
	entries := t.Memory.EntrySnapshot()
	t.EmitFunc(RuntimeEvent{
		Type: EventStateMemoryUpdated,
		Data: MemoryUpdatedData{Entries: entries},
	})
	return marshalToolPayload(map[string]interface{}{
		"ok":                      true,
		"tool":                    "memory_save_entry",
		"memory_key":              payload.Key,
		"entry":                   entries[payload.Key],
		"entry_count":             len(entries),
		"overwrote_existing":      existed,
		"affects_report_delivery": false,
		"affects_goal_state":      false,
		"ui_summary":              fmt.Sprintf("工作记忆 [%s] 已写入。", payload.Key),
	})
}

func (t *SaveMemoryTool) SetEventEmitter(emit func(RuntimeEvent)) {
	t.EmitFunc = emit
}

func (t *InspectMemoryTool) Name() string {
	return "state_memory_inspect"
}
func (t *InspectMemoryTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectMemoryTool) Description() string {
	return "Read typed working-memory entries and their provenance fields. It does not modify state."
}

func (t *InspectMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectMemoryTool) Execute(args json.RawMessage) (string, error) {
	if err := tools.ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Memory == nil {
		return "", fmt.Errorf("working memory is not initialized")
	}
	entries := t.Memory.EntrySnapshot()
	payload := map[string]interface{}{
		"ok":          true,
		"tool":        "state_memory_inspect",
		"entry_count": len(entries),
		"entries":     entries,
		"ui_summary":  fmt.Sprintf("工作记忆共有 %d 条记录。", len(entries)),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// AskUserTool 允许 Agent 主动中断当前执行流，向用户发起提问或索要确切指令
type AskUserTool struct{}

func (t *AskUserTool) Name() string {
	return "user_request_input"
}
func (t *AskUserTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "action", RuntimeEnabled: true, RunControl: "suspend"}
}

func (t *AskUserTool) Description() string {
	return "Send an input request to the user and suspend the current run as waiting_user_input. Applies when the current run needs a user decision or clarification before continuing; a normal assistant text response that asks a question is final output and does not suspend the run. Supports optional selectable options, explicit selection_mode (single or multiple), and optional custom text. The model decides selection_mode; the runtime does not infer it from wording. Reads question, reason, scope, context_ref, input_hint, required, selection_mode, allow_custom, and options. Does not directly return the user answer; the subsequent user reply is written back as the tool call result."
}

func (t *AskUserTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"question": {
				"type": "string",
				"description": "Question text."
			},
			"reason": {
				"type": "string",
				"description": "Why user input is needed."
			},
			"scope": {
				"type": "string",
				"description": "Optional caller-defined correlation label. The runtime does not interpret this value."
			},
			"context_ref": {
				"type": "string",
				"description": "Opaque associated context reference."
			},
			"input_hint": {
				"type": "string",
				"description": "Optional concise hint for a custom answer."
			},
			"required": {
				"type": "boolean",
				"description": "Whether the user may skip this request. Choose explicitly."
			},
			"selection_mode": {
				"type": "string",
				"enum": ["single", "multiple"],
				"description": "Whether options should be presented as a single-choice or multiple-choice control. Choose explicitly based on the user's decision surface."
			},
			"allow_custom": {
				"type": "boolean",
				"description": "Whether the user may provide a custom text answer instead of, or in addition to, selecting options. Choose explicitly."
			},
			"options": {
				"type": "array",
				"items": {
					"type": "object",
					"additionalProperties": false,
					"properties": {
						"id": {"type": "string", "description": "Stable option ID."},
						"label": {"type": "string", "description": "Display label."},
						"hint": {"type": "string", "description": "Optional short explanation."}
					},
					"required": ["id", "label"]
				},
				"description": "Optional selectable options. If omitted, the UI presents a custom text answer only."
			},
			"authorization": {
				"type":"object",
				"additionalProperties":false,
				"description":"Optional exact action authorization request. The runtime replaces normal choices with fixed approve/reject controls and binds an approval receipt to action, resource_ref, and canonical payload_json.",
				"properties":{
					"action":{"type":"string"},
					"resource_ref":{"type":"string"},
					"payload_json":{"type":"string","description":"Exact proposed change as valid JSON."}
				},
				"required":["action","resource_ref","payload_json"]
			}
		},
		"required": ["question", "selection_mode", "allow_custom", "required"]
	}`)
}

type askUserToolCallArguments struct {
	Question      string                      `json:"question"`
	Reason        string                      `json:"reason"`
	Scope         string                      `json:"scope"`
	ContextRef    string                      `json:"context_ref"`
	InputHint     string                      `json:"input_hint"`
	Required      *bool                       `json:"required"`
	SelectionMode string                      `json:"selection_mode"`
	AllowCustom   *bool                       `json:"allow_custom"`
	Options       []AskUserOption             `json:"options"`
	Authorization *ActionAuthorizationRequest `json:"authorization"`
}

func parseAskUserToolCallArguments(rawArgs string) (AskUserData, error) {
	var args askUserToolCallArguments
	if err := jsoncontract.Decode([]byte(rawArgs), &args); err != nil {
		return AskUserData{}, fmt.Errorf("user_request_input parameter parse failed: %w", err)
	}
	if strings.TrimSpace(args.Question) == "" {
		return AskUserData{}, fmt.Errorf("user_request_input question is required")
	}
	options, err := validateAskUserOptions(args.Options)
	if err != nil {
		return AskUserData{}, err
	}
	allowCustom := false
	selectionMode := ""
	if args.Authorization != nil {
		if strings.TrimSpace(args.Authorization.Action) == "" || strings.TrimSpace(args.Authorization.ResourceRef) == "" {
			return AskUserData{}, fmt.Errorf("authorization action and resource_ref are required")
		}
		if _, err := authorizationPayloadHash(args.Authorization.PayloadJSON); err != nil {
			return AskUserData{}, err
		}
		if args.Authorization.Action != strings.TrimSpace(args.Authorization.Action) || args.Authorization.ResourceRef != strings.TrimSpace(args.Authorization.ResourceRef) {
			return AskUserData{}, fmt.Errorf("authorization action and resource_ref must not contain leading or trailing whitespace")
		}
		options = []AskUserOption{{ID: "approve", Label: "批准"}, {ID: "reject", Label: "拒绝"}}
		selectionMode = "single"
		allowCustom = false
		required := true
		args.Required = &required
	} else {
		if args.AllowCustom == nil {
			return AskUserData{}, fmt.Errorf("user_request_input allow_custom is required when authorization is absent")
		}
		if args.Required == nil {
			return AskUserData{}, fmt.Errorf("user_request_input required is required when authorization is absent")
		}
		allowCustom = *args.AllowCustom
		selectionMode, err = validateAskUserSelectionMode(args.SelectionMode)
		if err != nil {
			return AskUserData{}, err
		}
		if len(options) == 0 && !allowCustom {
			return AskUserData{}, fmt.Errorf("user_request_input requires options when allow_custom is false")
		}
	}
	return AskUserData{
		Question:      args.Question,
		Reason:        args.Reason,
		Scope:         args.Scope,
		ContextRef:    args.ContextRef,
		InputHint:     args.InputHint,
		Required:      *args.Required,
		SelectionMode: selectionMode,
		AllowCustom:   allowCustom,
		Options:       options,
		Authorization: args.Authorization,
	}, nil
}

func validateAskUserSelectionMode(selectionMode string) (string, error) {
	switch selectionMode {
	case "single":
		return "single", nil
	case "multiple":
		return "multiple", nil
	default:
		return "", fmt.Errorf("user_request_input selection_mode must be single or multiple")
	}
}

func (t *AskUserTool) Execute(args json.RawMessage) (string, error) {
	payload, err := parseAskUserToolCallArguments(string(args))
	if err != nil {
		return "", err
	}

	return marshalToolPayload(map[string]interface{}{
		"ok":             true,
		"tool":           "user_request_input",
		"question":       payload.Question,
		"reason":         payload.Reason,
		"scope":          payload.Scope,
		"context_ref":    payload.ContextRef,
		"input_hint":     payload.InputHint,
		"required":       payload.Required,
		"selection_mode": payload.SelectionMode,
		"allow_custom":   payload.AllowCustom,
		"options":        payload.Options,
		"authorization":  payload.Authorization,
		"run_status":     "waiting_user_input",
		"ui_summary":     "用户输入请求已发送。",
	})
}

func (t *AskUserTool) SuspensionEvent(args json.RawMessage) (RuntimeEvent, error) {
	payload, err := parseAskUserToolCallArguments(string(args))
	if err != nil {
		return RuntimeEvent{}, err
	}
	return RuntimeEvent{Type: EventUserRequestInput, Data: payload}, nil
}

func (t *AskUserTool) AcceptsUserResponse() bool { return true }

func validateAskUserOptions(options []AskUserOption) ([]AskUserOption, error) {
	out := make([]AskUserOption, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for index, option := range options {
		if option.ID == "" || strings.TrimSpace(option.ID) == "" || option.Label == "" || strings.TrimSpace(option.Label) == "" {
			return nil, fmt.Errorf("user_request_input options[%d] requires non-empty id and label", index)
		}
		if option.ID != strings.TrimSpace(option.ID) || option.Label != strings.TrimSpace(option.Label) || option.Hint != strings.TrimSpace(option.Hint) {
			return nil, fmt.Errorf("user_request_input options[%d] fields must not contain leading or trailing whitespace", index)
		}
		if _, exists := seen[option.ID]; exists {
			return nil, fmt.Errorf("user_request_input option id %q is duplicated", option.ID)
		}
		seen[option.ID] = struct{}{}
		out = append(out, AskUserOption{
			ID:    option.ID,
			Label: option.Label,
			Hint:  option.Hint,
		})
	}
	return out, nil
}
