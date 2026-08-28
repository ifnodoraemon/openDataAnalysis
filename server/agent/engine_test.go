package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func TestNewEngineRejectsMissingRegistry(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected missing tool registry to panic")
		}
	}()
	NewEngine(nil)
}

func TestCompactAssistantMessage(t *testing.T) {
	t.Parallel()

	message := LLMMessage{
		Role: LLMRoleAssistant,
		ToolCalls: []LLMToolCall{
			{
				ID:   "call_1",
				Type: LLMToolTypeFunction,
				Function: LLMFunctionCall{
					Name:      "report_create_chart",
					Arguments: `{"chart_id":"chart_a","title":"Chart A","option":{"series":[{"data":[1,2,3]}]}}`,
				},
			},
			{
				ID:   "call_2",
				Type: LLMToolTypeFunction,
				Function: LLMFunctionCall{
					Name:      "report_manage_blocks",
					Arguments: `{"block_kind":"markdown","title":"Trend Analysis","content":"` + strings.Repeat("x", 400) + `"}`,
				},
			},
		},
	}

	compacted := compactAssistantMessage(message)
	if compacted.ToolCalls[0].Function.Arguments != message.ToolCalls[0].Function.Arguments {
		t.Fatal("expected report_create_chart arguments to stay unchanged")
	}
	if compacted.ToolCalls[1].Function.Arguments != message.ToolCalls[1].Function.Arguments {
		t.Fatal("expected report_manage_blocks arguments to stay unchanged")
	}
}

func TestCompactToolResult(t *testing.T) {
	t.Parallel()

	queryResult := `{
  "ok": true,
  "tool": "data_query_sql",
  "row_count": 2,
  "columns": ["month", "revenue"],
  "rows": [
    {"month":"2025-01","revenue":100},
    {"month":"2025-02","revenue":120}
  ]
}`
	compactedQuery, err := compactToolResult("data_query_sql", queryResult)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compactedQuery, "\n") {
		t.Fatalf("expected minified query result, got %q", compactedQuery)
	}

	describeResult := `{
  "tableName":"sales",
  "rowCount":2,
  "columns":[{"name":"month"}]
}`
	compactedDescribe, err := compactToolResult("data_describe_table", describeResult)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compactedDescribe, "\n") {
		t.Fatalf("expected minified describe result, got %q", compactedDescribe)
	}

	listTablesResult := `{
  "ok": true,
  "tool": "data_list_tables",
  "tables": ["sales"],
  "table_count": 1
}`
	compactedTables, err := compactToolResult("data_list_tables", listTablesResult)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compactedTables, "\n") {
		t.Fatalf("expected minified data_list_tables result, got %q", compactedTables)
	}

	runPythonResult := `{
  "ok": false,
  "tool": "code_run_python",
  "ui_summary": "Python execution failed (12ms)",
  "error_code": "execution_failed",
  "detail": "NameError"
}`
	compactedPython, err := compactToolResult("code_run_python", runPythonResult)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compactedPython, "\n") {
		t.Fatalf("expected minified code_run_python result, got %q", compactedPython)
	}
	if strings.Contains(compactedPython, "ui_summary") {
		t.Fatalf("expected compacted code_run_python result to drop ui_summary, got %q", compactedPython)
	}
}

func TestNormalizeToolExecutionResult(t *testing.T) {
	t.Parallel()

	if _, success, err := normalizeToolExecutionResult("demo", "", errors.New("boom")); err != nil || success {
		t.Fatalf("expected encoded execution failure, success=%t err=%v", success, err)
	}
	if _, success, err := normalizeToolExecutionResult("demo", `{"ok":false,"tool":"demo","error_code":"missing_option"}`, nil); err != nil || success {
		t.Fatalf("expected ok=false result, success=%t err=%v", success, err)
	}
	if _, success, err := normalizeToolExecutionResult("demo", `{"ok":true,"tool":"demo"}`, nil); err != nil || !success {
		t.Fatalf("expected ok=true result, success=%t err=%v", success, err)
	}
	if _, success, err := normalizeToolExecutionResult("demo", "plain text result", nil); err == nil || success {
		t.Fatalf("expected plain text contract failure, success=%t err=%v", success, err)
	}
}

func TestSuccessfulDeliveryBoundaryResultDetection(t *testing.T) {
	t.Parallel()

	success := `{"ok":true,"tool":"report_finalize","delivery_state":"finalized","is_finalized":true,"report_title":"全面对比分析报告","author":"数据分析助手","block_count":6,"chart_count":6,"ui_summary":"delivery_state=finalized; block_count=6; chart_count=6"}`
	if !isSuccessfulDeliveryBoundaryResult(success) {
		t.Fatal("expected finalized report result to stop the run")
	}

	blocked := `{"ok":false,"tool":"report_finalize","delivery_state":"draft","is_finalized":false}`
	if isSuccessfulDeliveryBoundaryResult(blocked) {
		t.Fatal("expected blocked finalize result not to stop the run")
	}
}

func TestInspectGoalsToolReturnsFactsOnly(t *testing.T) {
	t.Parallel()

	sm := NewSubgoalManager()
	_, _ = sm.AddGoalWithBlocking("Analyze sales", "", false)
	_, _ = sm.AddGoalWithBlocking("Analyze marketing", "", false)

	tool := &InspectGoalsTool{Subgoals: sm}
	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["tool"] != "state_goal_inspect" {
		t.Fatalf("unexpected tool: %#v", payload["tool"])
	}
	if payload["goal_count"].(float64) != 2 {
		t.Fatalf("unexpected goal count: %#v", payload["goal_count"])
	}
	if payload["active_branch_count"].(float64) != 0 {
		t.Fatalf("expected non-blocking scratchpad goals not to block finalize: %#v", payload)
	}
	activeRoots, ok := payload["active_root_goal_ids"].([]interface{})
	if !ok || len(activeRoots) != 2 {
		t.Fatalf("expected active_root_goal_ids in payload: %#v", payload["active_root_goal_ids"])
	}
}

func TestInspectMemoryToolReturnsTypedEntriesOnly(t *testing.T) {
	t.Parallel()

	mem := NewWorkingMemory()
	mem.SaveEntry("retained_statement", MemoryEntry{Statement: "observed statement", Status: "inferred", CreatedBy: "test"})

	tool := &InspectMemoryTool{Memory: mem}
	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["tool"] != "state_memory_inspect" {
		t.Fatalf("unexpected tool: %#v", payload["tool"])
	}
	if payload["entry_count"].(float64) != 1 {
		t.Fatalf("unexpected entry count: %#v", payload["entry_count"])
	}
	entries := payload["entries"].(map[string]interface{})
	entry := entries["retained_statement"].(map[string]interface{})
	if entry["statement"] != "observed statement" || entry["status"] != "inferred" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestInspectReportStateToolReturnsFactsOnly(t *testing.T) {
	t.Parallel()

	tool := &InspectReportStateTool{
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "analysis", Kind: "markdown", Title: "Sales Analysis", Content: "## 1. Sales Analysis\n\n{{chart:chart_sales}}\n\n{{chart:chart_missing}}"},
				{ID: "sales_chart", Kind: "chart", ChartID: "chart_sales"},
			},
			Charts: []tools.ChartData{
				{ID: "chart_sales"},
				{ID: "chart_unused"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["tool"] != "state_report_inspect" {
		t.Fatalf("unexpected tool: %#v", payload["tool"])
	}
	if payload["chart_count"].(float64) != 2 {
		t.Fatalf("unexpected chart count: %#v", payload["chart_count"])
	}
	if payload["renderable_block_count"].(float64) != 2 {
		t.Fatalf("unexpected renderable block count: %#v", payload["renderable_block_count"])
	}
	if len(payload["missing_chart_references"].([]interface{})) != 1 {
		t.Fatalf("expected one missing chart ref, got %#v", payload["missing_chart_references"])
	}
	if len(payload["unreferenced_charts"].([]interface{})) != 1 {
		t.Fatalf("expected one unreferenced chart, got %#v", payload["unreferenced_charts"])
	}
	if _, exists := payload["blocks_with_duplicate_heading"]; exists {
		t.Fatalf("report observation must not inject formatting judgments: %#v", payload)
	}
	if _, exists := payload["chart_blocks_missing_caption"]; exists {
		t.Fatalf("report observation must not inject caption advice: %#v", payload)
	}
	if _, exists := payload["can_finalize_structurally"]; exists {
		t.Fatalf("did not expect can_finalize_structurally in payload: %#v", payload["can_finalize_structurally"])
	}
	if _, exists := payload["report_shape_facts"]; exists {
		t.Fatalf("did not expect report_shape_facts in payload: %#v", payload["report_shape_facts"])
	}
}

func TestInspectReportEditStateToolWholeReportScope(t *testing.T) {
	t.Parallel()

	tool := &InspectReportEditStateTool{
		EditState: &tools.ReportEditState{
			ScopeKindValue: "whole_report",
		},
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "analysis", Kind: "markdown", Title: "Sales Analysis", Content: "body"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["tool"] != "state_report_edit_inspect" {
		t.Fatalf("unexpected tool: %#v", payload["tool"])
	}
	if payload["scope_kind"] != "whole_report" {
		t.Fatalf("expected whole_report scope, got %#v", payload["scope_kind"])
	}
	if payload["ui_summary"] != "当前正在编辑整份报告。" {
		t.Fatalf("unexpected ui_summary: %#v", payload["ui_summary"])
	}
	if _, exists := payload["target_block"]; exists {
		t.Fatalf("did not expect target_block for whole-report scope: %#v", payload["target_block"])
	}
}

func TestInspectReportEditStateToolChartScope(t *testing.T) {
	t.Parallel()

	tool := &InspectReportEditStateTool{
		EditState: &tools.ReportEditState{
			ScopeKindValue: "partial_chart",
			TargetChartID:  "chart_sales",
		},
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "analysis", Kind: "chart", Title: "销售趋势", ChartID: "chart_sales"},
			},
			Charts: []tools.ChartData{
				{ID: "chart_sales"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["scope_kind"] != "partial_chart" {
		t.Fatalf("expected partial_chart scope, got %#v", payload["scope_kind"])
	}
	if payload["ui_summary"] != "当前正在局部编辑图表 chart_sales。" {
		t.Fatalf("unexpected ui_summary: %#v", payload["ui_summary"])
	}
	targetChart, ok := payload["target_chart"].(map[string]interface{})
	if !ok || targetChart["id"] != "chart_sales" {
		t.Fatalf("expected target_chart payload, got %#v", payload["target_chart"])
	}
	if _, exists := payload["target_block"]; exists {
		t.Fatalf("did not expect target_block for chart-only scope: %#v", payload["target_block"])
	}
}

func TestInspectReportEditStateToolSelectionScope(t *testing.T) {
	t.Parallel()

	tool := &InspectReportEditStateTool{
		EditState: &tools.ReportEditState{
			ScopeKindValue:    "partial_selection",
			TargetBlockID:     "analysis",
			TargetBlockLabel:  "销售结论",
			SelectionText:     "其中这句需要重写",
			SelectionStart:    12,
			SelectionEnd:      20,
			SelectionRangeSet: true,
		},
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "analysis", Kind: "markdown", Title: "销售结论", Content: "body"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["scope_kind"] != "partial_selection" {
		t.Fatalf("expected partial_selection scope, got %#v", payload["scope_kind"])
	}
	if payload["selection_start"] != float64(12) || payload["selection_end"] != float64(20) {
		t.Fatalf("expected selection range payload, got start=%#v end=%#v", payload["selection_start"], payload["selection_end"])
	}
	if payload["ui_summary"] != "当前正在局部编辑内容块 analysis 的 12-20 区间。" {
		t.Fatalf("unexpected ui_summary: %#v", payload["ui_summary"])
	}
	if payload["selection_text"] != "其中这句需要重写" {
		t.Fatalf("expected selection_text payload, got %#v", payload["selection_text"])
	}
}

func TestInspectReportEditStateToolLayoutScope(t *testing.T) {
	t.Parallel()

	tool := &InspectReportEditStateTool{
		EditState: &tools.ReportEditState{
			ScopeKindValue: "layout",
		},
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "analysis", Kind: "markdown", Title: "Sales Analysis", Content: "body"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["scope_kind"] != "layout" {
		t.Fatalf("expected layout scope, got %#v", payload["scope_kind"])
	}
	if payload["ui_summary"] != "当前正在编辑报告布局。" {
		t.Fatalf("unexpected ui_summary: %#v", payload["ui_summary"])
	}
}

func TestCompactMessagesByMeasuredPromptTokens(t *testing.T) {
	t.Parallel()

	engine := &Engine{
		policy:  "system",
		history: []ConversationItem{},
	}

	for i := 0; i < 16; i++ {
		engine.history = append(engine.history,
			ConversationItem{Role: LLMRoleUser, Content: strings.Repeat("a", 40000)},
			ConversationItem{Role: LLMRoleAssistant, Content: "ok"},
		)
	}

	engine.compactMessagesLocked(contextCompactTriggerTokens + 1)

	if len(engine.history) >= 33 {
		t.Fatalf("expected messages to be compacted, got %d", len(engine.history))
	}
	if engine.omittedHistoryMessages == 0 {
		t.Fatalf("expected omitted-message count after compaction")
	}
}

func TestCompactMessagesLockedSkipsWithoutMeasuredTokens(t *testing.T) {
	t.Parallel()

	engine := &Engine{
		policy: "system",
		history: []ConversationItem{
			{Role: LLMRoleUser, Content: "user"},
			{Role: LLMRoleAssistant, Content: "assistant"},
			{Role: LLMRoleUser, Content: strings.Repeat("x", 1000)},
		},
	}

	before := len(engine.history)
	engine.compactMessagesLocked(0)
	if len(engine.history) != before {
		t.Fatalf("expected no compaction without measured tokens")
	}
}

func TestSanitizeToolErrorRedactsGenericConnectionURI(t *testing.T) {
	t.Parallel()

	got := sanitizeToolError("dial customdb://user:secret@db.example/internal failed")
	if strings.Contains(got, "user:secret") || !strings.Contains(got, "customdb://***") {
		t.Fatalf("expected generic connection URI redaction, got %q", got)
	}
}

func TestAskUserSuspensionEventRequiresCompleteArguments(t *testing.T) {
	t.Parallel()

	tool := &AskUserTool{}
	event, err := tool.SuspensionEvent(json.RawMessage(`{"question":"How are you?","required":true,"selection_mode":"single","allow_custom":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != EventUserRequestInput {
		t.Fatalf("expected user input event, got %#v", event)
	}
	payload := event.Data.(AskUserData)
	if payload.Question != "How are you?" {
		t.Fatalf("expected question 'How are you?', got %q", payload.Question)
	}

	_, err = tool.SuspensionEvent(json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Fatalf("expected error containing 'question is required', got: %v", err)
	}
}
