package agent

import (
	"encoding/json"
	"fmt"
)

// repeatNoticeThresholds are the identical-call counts that earn the model an
// advisory notice. They match the escalation DSH uses (3, 5, 8): early enough
// to break a loop, sparse enough not to nag.
var repeatNoticeThresholds = []int{3, 5, 8}

// canonicalToolArguments normalizes tool arguments so two calls that differ only
// in JSON key order count as the same call. json.Marshal sorts object keys.
func canonicalToolArguments(raw string) string {
	var payload interface{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return raw
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// recordRepeatCall counts one executed call and queues an advisory notice when
// the same call has now been made an escalating number of times.
//
// The notice is advisory: it never blocks or delays a legitimate repeated call.
// It is delivered as a runtime fact on the next request rather than as a
// replacement for the tool result, so the model still sees what the tool said.
func (e *Engine) recordRepeatCall(call LLMToolCall) {
	key := call.Function.Name + "\x00" + canonicalToolArguments(call.Function.Arguments)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.repeatCalls == nil {
		e.repeatCalls = make(map[string]int)
	}
	e.repeatCalls[key]++
	count := e.repeatCalls[key]
	for _, threshold := range repeatNoticeThresholds {
		if count != threshold {
			continue
		}
		content, err := json.Marshal(map[string]interface{}{
			"tool":            call.Function.Name,
			"identical_calls": count,
			"notice":          fmt.Sprintf("This tool has now been called %d times with identical arguments. Inspect the previous result and change approach or finish, instead of repeating the same call.", count),
		})
		if err != nil {
			continue
		}
		e.pendingNotices = append(e.pendingNotices, RuntimeContextBlock{
			Name:    "repeat_tool_notice",
			Role:    LLMRoleUser,
			Content: string(content),
		})
	}
}

// takePendingNotices returns and clears the notices queued since the last
// request, so each advisory is delivered exactly once.
func (e *Engine) takePendingNotices() []RuntimeContextBlock {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pendingNotices) == 0 {
		return nil
	}
	notices := e.pendingNotices
	e.pendingNotices = nil
	return notices
}

// resetRepeatTracking clears per-conversation repeat state. A new user message
// is a new intent, so the previous loop history no longer applies.
func (e *Engine) resetRepeatTracking() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.repeatCalls = make(map[string]int)
	e.pendingNotices = nil
}
