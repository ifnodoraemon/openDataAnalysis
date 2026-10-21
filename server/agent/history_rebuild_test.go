package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type messageLog struct {
	messages []domain.RunMessage
	seq      int
}

func (l *messageLog) add(runID, eventType, content string) *messageLog {
	return l.addTool(runID, eventType, "", "", content)
}

func (l *messageLog) addTool(runID, eventType, name, callID, content string) *messageLog {
	l.seq++
	msg := domain.RunMessage{
		ID:        "m" + itoa(l.seq),
		RunID:     runID,
		Type:      eventType,
		Name:      name,
		Content:   content,
		CreatedAt: time.Unix(1700000000, 0).Add(time.Duration(l.seq) * time.Millisecond),
	}
	if callID != "" {
		id := callID
		msg.ToolCallID = &id
	}
	l.messages = append(l.messages, msg)
	return l
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func rebuild(t *testing.T, log *messageLog) []ConversationItem {
	t.Helper()
	items, _, _, err := RebuildHistoryFromMessages(log.messages)
	if err != nil {
		t.Fatalf("RebuildHistoryFromMessages: %v", err)
	}
	return items
}

func TestRebuildHistoryEmptyLog(t *testing.T) {
	t.Parallel()
	items, omitted, summary, err := RebuildHistoryFromMessages(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 || omitted != 0 || summary != "" {
		t.Fatalf("expected empty history, got %d items (omitted=%d)", len(items), omitted)
	}
}

func TestRebuildHistoryMergesAssistantTextAndToolCalls(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "分析销售额").
		add("r1", EventAssistantStatus, "先看表结构").
		addTool("r1", EventToolCall, "data_list_tables", "call_1", `{"schema":"main"}`).
		addTool("r1", EventToolResult, "data_list_tables", "call_1", `{"ok":true,"tool":"data_list_tables"}`).
		add("r1", EventRunCompleted, "销售额为 100")

	items := rebuild(t, log)
	if len(items) != 4 {
		t.Fatalf("expected 4 history items, got %d: %#v", len(items), items)
	}
	if items[0].Role != LLMRoleUser || items[0].Content != "分析销售额" {
		t.Fatalf("unexpected first item: %#v", items[0])
	}
	// Text and the tool call belong to one assistant message, exactly as the
	// live engine stores them.
	if items[1].Role != LLMRoleAssistant || items[1].Content != "先看表结构" {
		t.Fatalf("unexpected assistant item: %#v", items[1])
	}
	if len(items[1].ToolCalls) != 1 || items[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected the tool call to merge into the assistant item: %#v", items[1].ToolCalls)
	}
	if items[1].ToolCalls[0].Function.Name != "data_list_tables" {
		t.Fatalf("unexpected tool name: %q", items[1].ToolCalls[0].Function.Name)
	}
	if items[1].ToolCalls[0].Function.Arguments != `{"schema":"main"}` {
		t.Fatalf("unexpected tool arguments: %q", items[1].ToolCalls[0].Function.Arguments)
	}
	if items[2].Role != LLMRoleTool || items[2].ToolCallID != "call_1" || items[2].ToolCallName != "data_list_tables" {
		t.Fatalf("unexpected tool result item: %#v", items[2])
	}
	if items[3].Role != LLMRoleAssistant || items[3].Content != "销售额为 100" {
		t.Fatalf("unexpected final assistant item: %#v", items[3])
	}
}

func TestRebuildHistorySynthesizesUnknownToolOutcome(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "查一下").
		addTool("r1", EventToolCall, "data_query_sql", "call_9", `{"sql":"select 1"}`).
		add("r1", EventError, "boom")

	items := rebuild(t, log)
	if len(items) != 3 {
		t.Fatalf("expected user + assistant + synthesized tool result, got %d: %#v", len(items), items)
	}
	toolItem := items[2]
	if toolItem.Role != LLMRoleTool || toolItem.ToolCallID != "call_9" {
		t.Fatalf("unexpected synthesized item: %#v", toolItem)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(toolItem.Content), &payload); err != nil {
		t.Fatalf("synthesized result is not JSON: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "tool_outcome_unknown" {
		t.Fatalf("unexpected synthesized payload: %#v", payload)
	}
	if payload["tool"] != "data_query_sql" {
		t.Fatalf("synthesized payload must name the tool: %#v", payload)
	}
}

func TestRebuildHistorySynthesizesOnlyTheMissingResult(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "批量").
		addTool("r1", EventToolCall, "data_list_tables", "call_a", `{}`).
		addTool("r1", EventToolCall, "data_describe_table", "call_b", `{}`).
		addTool("r1", EventToolResult, "data_list_tables", "call_a", `{"ok":true,"tool":"data_list_tables"}`)

	items := rebuild(t, log)
	if len(items) != 4 {
		t.Fatalf("expected user + assistant + 2 tool results, got %d: %#v", len(items), items)
	}
	if items[2].ToolCallID != "call_a" || items[2].Content != `{"ok":true,"tool":"data_list_tables"}` {
		t.Fatalf("recorded result must be preserved: %#v", items[2])
	}
	if items[3].ToolCallID != "call_b" || !strings.Contains(items[3].Content, "tool_outcome_unknown") {
		t.Fatalf("missing result must be synthesized: %#v", items[3])
	}
}

func TestRebuildHistoryDropsOrphanToolResult(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "x").
		addTool("r1", EventToolResult, "data_query_sql", "call_orphan", `{"ok":true}`)

	items := rebuild(t, log)
	if len(items) != 1 || items[0].Role != LLMRoleUser {
		t.Fatalf("orphan tool result must be dropped, got %#v", items)
	}
}

func TestRebuildHistoryReconstructsSuspensionAndAnswer(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "帮我改口径").
		add("r1", EventUserRequestInput, `{"question":"用哪个口径?","required":true,"selection_mode":"single","allow_custom":true,"tool_call_id":"call_ask"}`).
		add("r1", "user", "用净收入口径").
		add("r1", EventRunCompleted, "已按净收入口径更新")

	items := rebuild(t, log)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d: %#v", len(items), items)
	}
	ask := items[1]
	if ask.Role != LLMRoleAssistant || len(ask.ToolCalls) != 1 {
		t.Fatalf("suspension must rebuild as an assistant tool call: %#v", ask)
	}
	if ask.ToolCalls[0].ID != "call_ask" || ask.ToolCalls[0].Function.Name != "user_request_input" {
		t.Fatalf("unexpected rebuilt ask call: %#v", ask.ToolCalls[0])
	}
	answer := items[2]
	if answer.Role != LLMRoleTool || answer.ToolCallID != "call_ask" {
		t.Fatalf("the user answer must rebuild as the tool result: %#v", answer)
	}
	if !strings.Contains(answer.Content, "用净收入口径") {
		t.Fatalf("the answer text must survive: %q", answer.Content)
	}
	if items[3].Role != LLMRoleAssistant || items[3].Content != "已按净收入口径更新" {
		t.Fatalf("unexpected final item: %#v", items[3])
	}
}

func TestRebuildHistoryKeepsStatusTextWithSuspension(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "问题").
		add("r1", EventAssistantStatus, "需要你确认一下").
		add("r1", EventUserRequestInput, `{"question":"确认?","required":true,"selection_mode":"single","allow_custom":false,"tool_call_id":"call_ask"}`).
		add("r1", "user", "确认").
		add("r1", EventRunCompleted, "完成")

	items := rebuild(t, log)
	if len(items) != 4 {
		t.Fatalf("expected status text and the ask call to share one assistant item, got %d: %#v", len(items), items)
	}
	if items[1].Content != "需要你确认一下" || len(items[1].ToolCalls) != 1 {
		t.Fatalf("expected merged assistant item, got %#v", items[1])
	}
}

// A trailing unanswered suspension is the question the user is about to
// answer, so the rebuild leaves it open. Pairing it here would make the answer
// look like a duplicate; the engine repairs it at run start only if the
// question was truly abandoned.
func TestRebuildHistoryKeepsUnansweredSuspensionOpen(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "问题").
		add("r1", EventUserRequestInput, `{"question":"确认?","required":true,"selection_mode":"single","allow_custom":false,"tool_call_id":"call_ask"}`)

	items := rebuild(t, log)
	if len(items) != 2 {
		t.Fatalf("expected user + assistant question with no synthesized result, got %d: %#v", len(items), items)
	}
	if items[1].Role != LLMRoleAssistant || len(items[1].ToolCalls) != 1 {
		t.Fatalf("expected the question to stay a pending tool call: %#v", items[1])
	}
	if items[1].ToolCalls[0].ID != "call_ask" {
		t.Fatalf("unexpected pending call id: %#v", items[1].ToolCalls[0])
	}

	// The engine reads this state at run start: an abandoned question is
	// answered as unknown so the next request stays protocol-valid.
	engine := &Engine{registry: askUserTestRegistry(), history: items}
	engine.clearAbandonedSuspension()
	if len(engine.history) != 3 || engine.history[2].Role != LLMRoleTool {
		t.Fatalf("expected the abandoned question to be repaired at run start: %#v", engine.history)
	}
	if !strings.Contains(engine.history[2].Content, "tool_outcome_unknown") {
		t.Fatalf("unexpected synthesized content: %q", engine.history[2].Content)
	}
}

func TestRebuildHistoryIgnoresNonConversationEvents(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "x").
		add("r1", EventStateMemoryUpdated, `{"entries":{}}`).
		add("r1", EventStateSubgoalsUpdated, `{"goals":[]}`).
		add("r1", EventReportUpdate, `{"html":"<p/>"}`).
		add("r1", "child_run_tokens", `{"prompt_tokens":1}`).
		add("r1", EventRunCompleted, "done")

	items := rebuild(t, log)
	if len(items) != 2 {
		t.Fatalf("state events must not enter history, got %#v", items)
	}
}

func TestRebuildHistoryOrdersMultipleRuns(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "第一轮").
		add("r1", EventRunCompleted, "第一轮答案").
		add("r2", "user", "第二轮").
		add("r2", EventRunCompleted, "第二轮答案")

	items := rebuild(t, log)
	want := []string{"第一轮", "第一轮答案", "第二轮", "第二轮答案"}
	if len(items) != len(want) {
		t.Fatalf("expected %d items, got %d: %#v", len(want), len(items), items)
	}
	for i, expected := range want {
		if items[i].Content != expected {
			t.Fatalf("item %d: expected %q, got %q", i, expected, items[i].Content)
		}
	}
}

func TestSetHistoryAndHistoryAreCopies(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	engine.SetHistory([]ConversationItem{{Role: LLMRoleUser, Content: "a"}}, 3)

	first := engine.History()
	if len(first) != 1 || first[0].Content != "a" {
		t.Fatalf("unexpected history: %#v", first)
	}
	first[0].Content = "mutated"

	second := engine.History()
	if second[0].Content != "a" {
		t.Fatalf("History must return a copy, got %q", second[0].Content)
	}
	if engine.omittedHistoryMessages != 3 {
		t.Fatalf("expected omitted count 3, got %d", engine.omittedHistoryMessages)
	}
}

func TestSetHistoryClampsNegativeOmittedCount(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	engine.SetHistory(nil, -5)
	if engine.omittedHistoryMessages != 0 {
		t.Fatalf("expected omitted count clamped to 0, got %d", engine.omittedHistoryMessages)
	}
}

func TestRebuildHistoryAppliesSummaryCheckpoint(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "第一轮").
		add("r1", EventRunCompleted, "第一轮答案").
		add("r1", "user", "第二轮").
		add("r1", EventRunCompleted, "第二轮答案").
		add("r1", EventHistoryCompacted, `{"summary":"用户先问了第一轮，得到答案后追问第二轮。","shadowed_items":2,"pinned_items":1,"method":"summary"}`).
		add("r1", "user", "第三轮").
		add("r1", EventRunCompleted, "第三轮答案")

	items, omitted, summary, err := RebuildHistoryFromMessages(log.messages)
	if err != nil {
		t.Fatalf("RebuildHistoryFromMessages: %v", err)
	}
	if omitted != 0 {
		t.Fatalf("a summary carries the facts, so nothing is merely omitted, got %d", omitted)
	}
	if !strings.Contains(summary, "第一轮") {
		t.Fatalf("expected the digest to be returned beside history, got %q", summary)
	}
	// The digest is not a history item: pinned task + the retained tail + the
	// third turn.
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d: %#v", len(items), items)
	}
	if items[0].Content != "第一轮" {
		t.Fatalf("expected the pinned task to survive, got %q", items[0].Content)
	}
	if items[1].Content != "第二轮答案" {
		t.Fatalf("expected the retained tail to follow the pinned task, got %q", items[1].Content)
	}
	if items[3].Content != "第三轮答案" {
		t.Fatalf("expected later turns to append after the checkpoint, got %#v", items[3])
	}
	for _, item := range items {
		if strings.Contains(item.Content, "<compacted-summary>") {
			t.Fatalf("the digest must not be replayed as a conversation turn: %#v", item)
		}
	}
}

func TestRebuildHistoryAppliesTrimCheckpoint(t *testing.T) {
	t.Parallel()

	log := (&messageLog{}).
		add("r1", "user", "第一轮").
		add("r1", EventRunCompleted, "第一轮答案").
		add("r1", "user", "中间追问").
		add("r1", EventRunCompleted, "中间答案").
		add("r1", EventHistoryCompacted, `{"shadowed_items":2,"pinned_items":1,"method":"trim"}`)

	items, omitted, _, err := RebuildHistoryFromMessages(log.messages)
	if err != nil {
		t.Fatalf("RebuildHistoryFromMessages: %v", err)
	}
	if omitted != 2 {
		t.Fatalf("expected the trimmed count to be reported, got %d", omitted)
	}
	if len(items) != 2 {
		t.Fatalf("expected the shadowed items to be dropped, got %d: %#v", len(items), items)
	}
	if items[0].Content != "第一轮" {
		t.Fatalf("expected the pinned task to survive the trim, got %#v", items[0])
	}
	if items[1].Content != "中间答案" {
		t.Fatalf("unexpected first surviving item: %#v", items[1])
	}
}

func TestRebuildHistoryKeepsContextWhenCheckpointCountExceedsLog(t *testing.T) {
	t.Parallel()

	// A persistence gap can leave the checkpoint shadowing more items than the
	// log holds. Keeping everything duplicates context; dropping live context
	// is the unrecoverable direction.
	log := (&messageLog{}).
		add("r1", "user", "只有一轮").
		add("r1", EventRunCompleted, "答案").
		add("r1", EventHistoryCompacted, `{"shadowed_items":9,"method":"trim"}`)

	items, omitted, _, err := RebuildHistoryFromMessages(log.messages)
	if err != nil {
		t.Fatalf("RebuildHistoryFromMessages: %v", err)
	}
	if omitted != 0 {
		t.Fatalf("nothing may be reported as omitted when the count is unusable, got %d", omitted)
	}
	if len(items) != 2 {
		t.Fatalf("expected the live context to be kept, got %#v", items)
	}
}

func TestRebuildHistoryPrunesOversizedToolResults(t *testing.T) {
	t.Parallel()

	huge := `{"ok":true,"tool":"data_query_sql","rows":["` + strings.Repeat("x", 20000) + `"]}`
	log := (&messageLog{}).
		add("r1", "user", "查一下").
		addTool("r1", EventToolCall, "data_query_sql", "call_big", `{}`).
		addTool("r1", EventToolResult, "data_query_sql", "call_big", huge)

	items := rebuild(t, log)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if !strings.Contains(items[2].Content, `"truncated":true`) {
		t.Fatalf("expected the rebuilt tool result to be pruned like the live history, got %q", items[2].Content)
	}
}
