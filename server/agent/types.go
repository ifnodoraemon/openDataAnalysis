package agent

import (
	"fmt"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

// RuntimeEvent 是运行时事件的传输结构。
type RuntimeEvent struct {
	Type      string      `json:"type"`
	SessionID string      `json:"sessionId,omitempty"`
	RunID     string      `json:"runId,omitempty"`
	Data      interface{} `json:"data"`
}

// 事件类型常量
const (
	EventRunStarted             = "run_started"
	EventAssistantStatus        = "assistant_status"
	EventToolCall               = "tool_call"
	EventToolResult             = "tool_result"
	EventReportUpdate           = "report_update"
	EventReportFinal            = "report_final"
	EventError                  = "error"
	EventRunCompleted           = "run_completed"
	EventRunCancelled           = "run_cancelled"
	EventUserRequestInput       = "user_request_input"
	EventStateSubgoalsUpdated   = "state_subgoals_updated"
	EventStateMemoryUpdated     = "state_memory_updated"
	EventStateReportEditUpdated = "state_report_edit_updated"
	EventStateChildRunsUpdated  = "state_child_runs_updated"
	// EventHistoryCompacted records that earlier conversation was replaced by a
	// summary (or, when summarization failed, dropped) so history can be
	// rebuilt deterministically from the persisted log.
	EventHistoryCompacted = "history_compacted"
	// EventApprovalDenied records that the runtime refused to execute a gated
	// tool call. It is the auditable trace of a fail-closed admission decision.
	EventApprovalDenied = "approval_denied"
	// EventRunUsage reports the model usage a run accumulated. It is emitted
	// once the run settles and carries operational cost facts.
	EventRunUsage = "run_usage"
	// EventSystemFeedback records a corrective turn the runtime wrote into the
	// model conversation, such as rejecting an answer that leaked raw tool-call
	// markup. It is logged so a rebuilt history reproduces the same
	// conversation instead of silently dropping the turn.
	EventSystemFeedback = "system_feedback"
)

// SystemFeedbackData is one corrective turn the runtime wrote into the model
// conversation. It is model-facing text, not a user statement.
type SystemFeedbackData struct {
	Content string `json:"content"`
	Reason  string `json:"reason,omitempty"`
}

// RunUsageData is the usage a run accumulated.
type RunUsageData struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ApprovalDeniedData describes one blocked tool call.
type ApprovalDeniedData struct {
	Tool     string `json:"tool"`
	Decision string `json:"decision"` // ask | deny
	Reason   string `json:"reason"`
}

// ApprovalOutcome reports what a user's answer to an authorization request
// meant, so the caller can record the decision.
type ApprovalOutcome struct {
	Requested   bool
	Approved    bool
	Action      string
	ResourceRef string
}

// HistoryCompactedData is the durable checkpoint written when context pressure
// forces older conversation out of the active window.
//
// ShadowedItems counts the conversation items that were removed from the front
// of the history at that moment; a rebuild replays the same fold and drops the
// same number of items. Method is "summary" when the items were replaced by a
// model-written summary and "trim" when summarization failed and the items were
// dropped outright.
type HistoryCompactedData struct {
	Summary       string `json:"summary,omitempty"`
	ShadowedItems int    `json:"shadowed_items"`
	// PinnedItems counts leading conversation items compaction never touches,
	// so the user's original task survives every compaction.
	PinnedItems   int    `json:"pinned_items,omitempty"`
	Method        string `json:"method"`
	PromptTokens  int    `json:"prompt_tokens,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
}

const (
	HistoryCompactionMethodSummary = "summary"
	HistoryCompactionMethodTrim    = "trim"
)

// UserMessage 用户输入
type UserMessage struct {
	Content     string             `json:"content"`
	EditContext *ReportEditContext `json:"editContext,omitempty"`
	TurnContext *TurnContext       `json:"turnContext,omitempty"`
}

func (m UserMessage) Validate() error {
	if strings.TrimSpace(m.Content) == "" {
		return fmt.Errorf("用户消息内容不能为空")
	}
	if err := m.EditContext.Validate(); err != nil {
		return err
	}
	if m.TurnContext != nil {
		if m.TurnContext.ReportTargetRunID != strings.TrimSpace(m.TurnContext.ReportTargetRunID) {
			return fmt.Errorf("turnContext.reportTargetRunId 必须保持原值")
		}
	}
	return nil
}

type ReportEditContext struct {
	ScopeKind         string `json:"scopeKind"`
	TargetRunID       string `json:"targetRunId,omitempty"`
	BlockID           string `json:"blockId,omitempty"`
	BlockLabel        string `json:"blockLabel,omitempty"`
	ChartID           string `json:"chartId,omitempty"`
	SelectionText     string `json:"selectionText,omitempty"`
	SelectionStart    int    `json:"selectionStart,omitempty"`
	SelectionEnd      int    `json:"selectionEnd,omitempty"`
	SelectionRangeSet bool   `json:"selectionRangeSet,omitempty"`
}

func (c *ReportEditContext) Validate() error {
	if c == nil {
		return nil
	}
	for field, value := range map[string]string{
		"scopeKind":   c.ScopeKind,
		"targetRunId": c.TargetRunID,
		"blockId":     c.BlockID,
		"chartId":     c.ChartID,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("editContext.%s 不能包含首尾空白", field)
		}
	}
	switch c.ScopeKind {
	case "whole_report", "layout":
		return nil
	case "partial_block":
		if strings.TrimSpace(c.BlockID) == "" {
			return fmt.Errorf("partial_block 范围必须提供 editContext.blockId")
		}
		return nil
	case "partial_chart":
		if strings.TrimSpace(c.ChartID) == "" {
			return fmt.Errorf("partial_chart 范围必须提供 editContext.chartId")
		}
		return nil
	case "partial_selection":
		if strings.TrimSpace(c.BlockID) == "" || strings.TrimSpace(c.SelectionText) == "" {
			return fmt.Errorf("partial_selection 范围必须提供 editContext.blockId 和 selectionText")
		}
		if !c.SelectionRangeSet || c.SelectionStart < 0 || c.SelectionEnd <= c.SelectionStart {
			return fmt.Errorf("partial_selection 范围必须提供明确且有效的选区")
		}
		return nil
	default:
		return fmt.Errorf("editContext.scopeKind 必须是 whole_report、layout、partial_block、partial_chart 或 partial_selection")
	}
}

type TurnContext struct {
	ReportTargetRunID string `json:"reportTargetRunId,omitempty"`
	ReportTitle       string `json:"reportTitle,omitempty"`
}

type RunStartedData struct {
	RunID string `json:"runId"`
}

// AssistantStatusData is visible progress/status text emitted before tool use.
type AssistantStatusData struct {
	Content string `json:"content"`
}

// ToolCallData 工具调用事件
type ToolCallData struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Arguments interface{} `json:"arguments"`
}

// ToolResultData 工具结果事件
type ToolResultData struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Result   string `json:"result"`
	Duration int64  `json:"duration"` // 毫秒
	Success  bool   `json:"success"`
}

// AskUserOption 是用户确认卡片中的可选项。
type AskUserOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

// AskUserData 等待用户回答的事件。选项只是 UI affordance，用户仍可按配置自行描述。
type AskUserData struct {
	Question      string                      `json:"question"`
	Reason        string                      `json:"reason,omitempty"`      // 为什么需要用户确认
	Scope         string                      `json:"scope,omitempty"`       // Uninterpreted caller-defined correlation label.
	ContextRef    string                      `json:"context_ref,omitempty"` // 关联上下文（表名、列名等）
	InputHint     string                      `json:"input_hint,omitempty"`  // 可选的自定义描述提示
	Required      bool                        `json:"required"`
	SelectionMode string                      `json:"selection_mode,omitempty"`
	AllowCustom   bool                        `json:"allow_custom"`
	Options       []AskUserOption             `json:"options,omitempty"`
	Authorization *ActionAuthorizationRequest `json:"authorization,omitempty"`
	// ToolCallID is the id of the user_request_input tool call that suspended
	// the run. The engine stamps it so the persisted event log stays a faithful
	// record of the assistant's tool call and history can be rebuilt from it.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type ActionAuthorizationRequest struct {
	Action      string `json:"action"`
	ResourceRef string `json:"resource_ref"`
	PayloadJSON string `json:"payload_json"`
}

type MemoryUpdatedData struct {
	Entries map[string]MemoryEntry `json:"entries"`
}

type EditStateUpdatedData struct {
	Active      bool               `json:"active"`
	ScopeKind   string             `json:"scopeKind,omitempty"`
	EditContext *ReportEditContext `json:"editContext,omitempty"`
}

type ChildRunsUpdatedData struct {
	ParentRunID string                   `json:"parentRunId"`
	ChildRuns   []map[string]interface{} `json:"childRuns"`
}

// ReportUpdateData 研报更新事件
type ReportUpdateData struct {
	HTML           string                 `json:"html"`
	SectionID      string                 `json:"sectionId,omitempty"`
	Title          string                 `json:"title,omitempty"`
	ReportFileID   string                 `json:"reportFileId,omitempty"`
	ReportSnapshot *domain.ReportSnapshot `json:"report_snapshot,omitempty"`
}

// ErrorData 错误事件
type ErrorData struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// CompleteData 完成事件
type CompleteData struct {
	Summary     string `json:"summary"`
	DownloadURL string `json:"downloadUrl,omitempty"`
}
