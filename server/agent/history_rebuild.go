package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

// RebuildHistoryFromMessages folds a session's persisted runtime events back
// into the model conversation history that produced them.
//
// The persisted event log is the durable record of a session, but the engine's
// in-memory history is not itself persisted. Without this fold, a session that
// is rehydrated after a restart (or after cache eviction) starts a new run with
// no conversational context, even though the log still holds every user turn,
// assistant message and tool exchange.
//
// The fold is intentionally structural: it reconstructs the exact message shape
// the live engine appends (assistant text and its tool calls are one item), and
// it never invents semantic content. The only synthesized content is a
// "tool outcome unknown" result for a call whose result the log does not
// record, which keeps the derived history protocol-valid for providers that
// require every tool call to be answered.
//
// The returned omitted count is the number of conversation items that are no
// longer in history because compaction dropped them outright. The returned
// summary is the compacted digest, which is delivered to the model as a
// runtime-context fact instead of a synthetic user turn.
func RebuildHistoryFromMessages(messages []domain.RunMessage) ([]ConversationItem, int, string, error) {
	ordered := append([]domain.RunMessage(nil), messages...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})

	folder := &historyFolder{}
	for _, msg := range ordered {
		if err := folder.consume(msg); err != nil {
			return nil, 0, "", err
		}
	}
	folder.flush()

	// A trailing user_request_input without an answer is the question the user
	// is about to answer. It stays unanswered so the resumed run can supply the
	// real result instead of a synthetic "unknown" one.
	return repairToolPairing(folder.items, folder.openAskID), folder.omitted, folder.summary, nil
}

type historyFolder struct {
	items []ConversationItem
	// pending accumulates the assistant message currently being rebuilt. Text
	// arrives first (assistant_status) and the tool calls that belong to the
	// same assistant message follow, so they are merged into one item exactly
	// as the live engine stores them.
	pending *ConversationItem
	// openAskID is the tool call id of a user_request_input suspension whose
	// answer has not been observed yet. The answer is persisted as a plain
	// "user" event in the same run, not as a tool result.
	openAskID  string
	openAskRun string
	omitted    int
	summary    string
}

func (f *historyFolder) consume(msg domain.RunMessage) error {
	switch msg.Type {
	case "user":
		if f.openAskID != "" && msg.RunID == f.openAskRun {
			result, err := buildAskUserToolResult(msg.Content, "")
			if err != nil {
				return err
			}
			f.flush()
			f.items = append(f.items, ConversationItem{
				Role:         LLMRoleTool,
				Content:      result,
				ToolCallID:   f.openAskID,
				ToolCallName: "user_request_input",
			})
			f.openAskID = ""
			f.openAskRun = ""
			return nil
		}
		f.flush()
		f.items = append(f.items, ConversationItem{Role: LLMRoleUser, Content: msg.Content})

	case EventAssistantStatus:
		f.flush()
		f.pending = &ConversationItem{Role: LLMRoleAssistant, Content: msg.Content}

	case EventToolCall:
		f.pendingToolCall(toolCallIDOr(msg, "call_"), msg.Name, msg.Content)

	case EventToolResult:
		f.flush()
		f.items = append(f.items, ConversationItem{
			Role:         LLMRoleTool,
			Content:      historyToolResultContent(msg.Name, msg.Content),
			ToolCallID:   toolCallIDOr(msg, "call_"),
			ToolCallName: msg.Name,
		})

	case EventUserRequestInput:
		var ask AskUserData
		if err := jsoncontract.Decode([]byte(msg.Content), &ask); err != nil {
			return fmt.Errorf("decode user_request_input event %s: %w", msg.ID, err)
		}
		callID := strings.TrimSpace(ask.ToolCallID)
		if callID == "" {
			callID = "ask_" + msg.ID
		}
		// The suspension is the assistant's tool call; it belongs to the same
		// assistant message as any status text emitted just before it.
		//
		// The persisted payload carries the runtime-stamped tool_call_id, which
		// is not part of the tool's argument contract. Rebuilding the call with
		// it would make the arguments invalid for a strict decode, so it is
		// stripped here: what the model originally sent is what the history
		// replays.
		ask.ToolCallID = ""
		arguments, err := json.Marshal(ask)
		if err != nil {
			return fmt.Errorf("rebuild user_request_input arguments %s: %w", msg.ID, err)
		}
		f.pendingToolCall(callID, "user_request_input", string(arguments))
		f.flush()
		f.openAskID = callID
		f.openAskRun = msg.RunID

	case EventRunCompleted:
		f.flush()
		if strings.TrimSpace(msg.Content) != "" {
			f.items = append(f.items, ConversationItem{Role: LLMRoleAssistant, Content: msg.Content})
		}

	case EventHistoryCompacted:
		return f.applyCompactionCheckpoint(msg)

	case EventSystemFeedback:
		var feedback SystemFeedbackData
		if err := jsoncontract.Decode([]byte(msg.Content), &feedback); err != nil {
			return fmt.Errorf("decode system_feedback event %s: %w", msg.ID, err)
		}
		f.flush()
		if strings.TrimSpace(feedback.Content) != "" {
			f.items = append(f.items, ConversationItem{Role: LLMRoleUser, Content: feedback.Content})
		}

	case EventError, EventRunCancelled:
		f.flush()

	default:
		// State, report and child-run events are not model-visible conversation.
	}
	return nil
}

// applyCompactionCheckpoint rewinds the fold to the state the engine had right
// after it compacted history: the shadowed items are dropped, while the leading
// pinned items and the compacted digest are reproduced exactly as the engine
// held them.
func (f *historyFolder) applyCompactionCheckpoint(msg domain.RunMessage) error {
	var data HistoryCompactedData
	if err := jsoncontract.Decode([]byte(msg.Content), &data); err != nil {
		return fmt.Errorf("decode history_compacted event %s: %w", msg.ID, err)
	}
	// Compaction happens between steps, so any in-progress assistant message
	// must be settled before the boundary is applied; otherwise the item count
	// would not match the engine's history at that moment.
	f.flush()

	pinned := data.PinnedItems
	if pinned < 0 || pinned > len(f.items) {
		pinned = 0
	}
	shadowed := data.ShadowedItems
	if shadowed < 0 || pinned+shadowed > len(f.items) {
		// The log does not hold exactly the items the checkpoint shadowed.
		// Keeping everything is the safe direction: duplicating context is
		// recoverable, dropping live context is not.
		shadowed = 0
	}

	kept := append([]ConversationItem(nil), f.items[:pinned]...)
	f.items = append(kept, f.items[pinned+shadowed:]...)

	if data.Method == HistoryCompactionMethodSummary && strings.TrimSpace(data.Summary) != "" {
		// The digest is delivered as a runtime-context fact, so it is carried
		// beside history rather than replayed into it.
		f.summary = data.Summary
		return nil
	}

	f.omitted += shadowed
	return nil
}

// historyToolResultContent reproduces the exact content the live engine stores
// for a tool result: the ui_summary presentation field is stripped and an
// oversized result is bounded before it enters the model history.
func historyToolResultContent(toolName, raw string) string {
	compacted, err := compactToolResult(toolName, raw)
	if err != nil {
		return pruneToolResultForHistory(toolName, raw)
	}
	return pruneToolResultForHistory(toolName, compacted)
}

func (f *historyFolder) pendingToolCall(callID, name, arguments string) {
	if f.pending == nil {
		f.pending = &ConversationItem{Role: LLMRoleAssistant}
	}
	f.pending.ToolCalls = append(f.pending.ToolCalls, LLMToolCall{
		ID:       callID,
		Type:     LLMToolTypeFunction,
		Function: LLMFunctionCall{Name: name, Arguments: arguments},
	})
}

func (f *historyFolder) flush() {
	if f.pending == nil {
		return
	}
	item := *f.pending
	f.pending = nil
	if strings.TrimSpace(item.Content) == "" && len(item.ToolCalls) == 0 {
		return
	}
	f.items = append(f.items, item)
}

func toolCallIDOr(msg domain.RunMessage, prefix string) string {
	if msg.ToolCallID != nil && strings.TrimSpace(*msg.ToolCallID) != "" {
		return *msg.ToolCallID
	}
	return prefix + msg.ID
}

// repairToolPairing enforces the tool-call/result protocol the providers
// require: every assistant tool call is followed by exactly the results for
// that call, and no result appears without an owning call.
//
// skipCallID names a call whose result is deliberately absent because the run
// suspended on it. It is left unanswered; that call is the pending question the
// user is about to answer.
func repairToolPairing(items []ConversationItem, skipCallID string) []ConversationItem {
	repaired := make([]ConversationItem, 0, len(items))
	for i := 0; i < len(items); i++ {
		item := items[i]
		if item.Role == LLMRoleTool {
			// Orphaned result: its owning assistant message is not in the log.
			continue
		}
		repaired = append(repaired, item)
		if item.Role != LLMRoleAssistant || len(item.ToolCalls) == 0 {
			continue
		}

		answered := make(map[string]struct{}, len(item.ToolCalls))
		for i+1 < len(items) && items[i+1].Role == LLMRoleTool {
			i++
			answered[items[i].ToolCallID] = struct{}{}
			repaired = append(repaired, items[i])
		}
		for _, call := range item.ToolCalls {
			if _, ok := answered[call.ID]; ok {
				continue
			}
			if skipCallID != "" && call.ID == skipCallID {
				continue
			}
			repaired = append(repaired, ConversationItem{
				Role:         LLMRoleTool,
				Content:      unrecordedToolResult(call.Function.Name),
				ToolCallID:   call.ID,
				ToolCallName: call.Function.Name,
			})
		}
	}
	return repaired
}

// unrecordedToolResult reports a call whose result the session log does not
// hold. It states the uncertainty instead of implying success or failure.
func unrecordedToolResult(toolName string) string {
	payload := map[string]interface{}{
		"ok":         false,
		"tool":       toolName,
		"error_code": "tool_outcome_unknown",
		"message":    "The session log does not record a result for this call, so its outcome is unknown. Do not assume it succeeded; call the tool again only if the result is still needed.",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"error_code":"tool_outcome_unknown"}`
	}
	return string(encoded)
}
