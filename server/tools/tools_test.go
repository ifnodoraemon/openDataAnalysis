package tools

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/data"
)

type stubSubgoalChecker struct {
	canFinalize bool
	blockers    []string
}

func (s stubSubgoalChecker) CanFinalize() (bool, []string) {
	return s.canFinalize, s.blockers
}

func TestMain(m *testing.M) {
	config.Cfg = &config.Config{
		LLMAPIKey: "mock-key",
	}
	os.Exit(m.Run())
}

func TestListTablesToolReturnsStructuredEmptyState(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_empty"); err != nil {
		t.Fatalf("init db: %v", err)
	}

	tool := &ListTablesTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("expected ok=true, got %#v", payload["ok"])
	}
	if payload["empty"] != true {
		t.Fatalf("expected empty=true, got %#v", payload["empty"])
	}
}

func TestListTablesToolReturnsStructuredTableList(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_tables"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE sales (id INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE _oda_table_metadata (table_name TEXT)`); err != nil {
		t.Fatalf("create internal metadata table: %v", err)
	}

	tool := &ListTablesTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK         bool     `json:"ok"`
		TableCount float64  `json:"table_count"`
		Tables     []string `json:"tables"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.TableCount != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if len(payload.Tables) != 1 || payload.Tables[0] != "sales" {
		t.Fatalf("unexpected tables: %#v", payload.Tables)
	}
}

func TestDescribeDataToolReturnsStructuredSchema(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_describe"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE sales (month TEXT, revenue INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := ing.GetDB().Exec(`INSERT INTO sales (month, revenue) VALUES ('2025-01', 100), ('2025-02', 120)`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	tool := &DescribeDataTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{"table_name":"sales","sample_rows":0}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK          bool            `json:"ok"`
		TableName   string          `json:"table_name"`
		ColumnCount int             `json:"column_count"`
		Schema      data.SchemaInfo `json:"schema"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.TableName != "sales" || payload.ColumnCount != 2 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload.Schema.RowCount != 2 || len(payload.Schema.Columns) != 2 {
		t.Fatalf("unexpected schema: %#v", payload.Schema)
	}
}

func TestDescribeDataToolDoesNotInventMetricGroupsFromNames(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_describe_ambiguity"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE revenue_metrics (channel TEXT, gross_revenue INTEGER, net_revenue INTEGER, recognized_revenue INTEGER, ad_spend INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := ing.GetDB().Exec(`INSERT INTO revenue_metrics (channel, gross_revenue, net_revenue, recognized_revenue, ad_spend) VALUES ('Search', 100, 90, 80, 10), ('Social', 120, 100, 95, 20)`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	tool := &DescribeDataTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{"table_name":"revenue_metrics","sample_rows":0}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if _, exists := payload["ambiguous_metric_groups"]; exists {
		t.Fatalf("column names must not create semantic metric groups: %#v", payload)
	}
}

func TestDescribeDataToolDoesNotInferTimeSemantics(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_describe_time"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE spend (dt TEXT, channel TEXT, ad_spend INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := ing.GetDB().Exec(`
		INSERT INTO spend (dt, channel, ad_spend) VALUES
		('2025-01-05','Search',980),
		('2025-01-12','Search',1040),
		('2025-01-19','Search',1120),
		('2025-01-26','Search',1190),
		('2025-01-06','Social',760),
		('2025-01-13','Social',790),
		('2025-01-20','Social',820),
		('2025-01-27','Social',850),
		('2025-02-02','Search',1210),
		('2025-02-09','Search',1260),
		('2025-02-16','Social',870),
		('2025-02-23','Social',910)
	`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	tool := &DescribeDataTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{"table_name":"spend","sample_rows":0}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	for _, field := range []string{"time_column_count", "time_column_candidates", "primary_time_column", "grain"} {
		if _, exists := payload[field]; exists {
			t.Fatalf("runtime must not infer %s: %#v", field, payload)
		}
	}
}

func TestDescribeDataToolReturnsStructuredFailure(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_describe_fail"); err != nil {
		t.Fatalf("init db: %v", err)
	}

	tool := &DescribeDataTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{"table_name":"missing_table","sample_rows":0}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "schema_lookup_failed" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestQueryDataToolReturnsStructuredSuccess(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_query"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := ing.GetDB().Exec(`CREATE TABLE sales (month TEXT, revenue INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := ing.GetDB().Exec(`INSERT INTO sales (month, revenue) VALUES ('2025-01', 100), ('2025-02', 120)`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	tool := &QueryDataTool{Ingester: ing}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT month, revenue FROM sales ORDER BY month","timeout_seconds":5}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK       bool                     `json:"ok"`
		RowCount int                      `json:"row_count"`
		Columns  []string                 `json:"columns"`
		Rows     []map[string]interface{} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.RowCount != 2 || len(payload.Rows) != 2 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if len(payload.Columns) != 2 || payload.Columns[0] != "month" || payload.Columns[1] != "revenue" {
		t.Fatalf("unexpected columns: %#v", payload.Columns)
	}
}

func TestQueryDataToolReturnsStructuredFailure(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_query_fail"); err != nil {
		t.Fatalf("init db: %v", err)
	}

	tool := &QueryDataTool{Ingester: ing}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT * FROM missing_table","timeout_seconds":5}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "query_failed" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestQueryDataToolRejectsMissingExecutionContext(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_query_context"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	result, err := (&QueryDataTool{Ingester: ing}).Execute(json.RawMessage(`{"sql":"SELECT 1","timeout_seconds":5}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if payload["error_code"] != "missing_execution_context" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	for i := range gotCopy {
		if gotCopy[i] != wantCopy[i] {
			return false
		}
	}
	return true
}

func TestManageReportBlocksAndFinalizeReturnStructuredPayloads(t *testing.T) {
	t.Parallel()

	state := &ReportState{Results: map[string]AnalysisResult{
		"res_growth": {ID: "res_growth", ToolName: "data_query_sql", Operation: "SELECT revenue_growth FROM sales_metrics"},
	}}
	blockTool := &ManageReportBlocksTool{ReportState: state}
	finalizeTool := &FinalizeReportTool{ReportState: state}

	blockResult, err := blockTool.Execute(json.RawMessage(`{
		"action":"append",
		"block_id":"summary",
		"block_kind":"markdown",
		"title":"执行摘要",
		"content":"收入增长 20%",
		"sources":[{"kind":"result","result_id":"res_growth"}]
	}`))
	if err != nil {
		t.Fatalf("block execute: %v", err)
	}
	var blockPayload struct {
		OK         bool   `json:"ok"`
		BlockKind  string `json:"block_kind"`
		BlockCount int    `json:"block_count"`
	}
	if err := json.Unmarshal([]byte(blockResult), &blockPayload); err != nil {
		t.Fatalf("expected block json payload: %v", err)
	}
	if !blockPayload.OK || blockPayload.BlockKind != "markdown" || blockPayload.BlockCount != 1 {
		t.Fatalf("unexpected block payload: %#v", blockPayload)
	}

	finalizeResult, err := finalizeTool.Execute(json.RawMessage(`{"report_title":"销售分析","author":"AI 数据分析师"}`))
	if err != nil {
		t.Fatalf("finalize execute: %v", err)
	}
	var finalizePayload struct {
		OK          bool   `json:"ok"`
		ReportTitle string `json:"report_title"`
		BlockCount  int    `json:"block_count"`
		ChartCount  int    `json:"chart_count"`
		UISummary   string `json:"ui_summary"`
	}
	if err := json.Unmarshal([]byte(finalizeResult), &finalizePayload); err != nil {
		t.Fatalf("expected finalize json payload: %v", err)
	}
	if !finalizePayload.OK || finalizePayload.ReportTitle != "销售分析" || finalizePayload.BlockCount != 1 || finalizePayload.ChartCount != 0 {
		t.Fatalf("unexpected finalize payload: %#v", finalizePayload)
	}
	if finalizePayload.UISummary != "报告已定稿，共 1 个内容块、0 个图表。" {
		t.Fatalf("unexpected finalize ui_summary: %#v", finalizePayload.UISummary)
	}
	if state.NeedsFinalize {
		t.Fatal("expected finalize to clear draft flag")
	}
}

func TestManageReportBlocksRejectsTitleBlockKind(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	blockTool := &ManageReportBlocksTool{ReportState: state}

	if _, err := blockTool.Execute(json.RawMessage(`{"action":"append","block_id":"heading","block_kind":"title","title":"只写标题"}`)); err == nil {
		t.Fatal("expected title block_kind to be rejected")
	}
}

func TestFinalizeReportRejectsOpenActiveBranch(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{},
		Subgoals: stubSubgoalChecker{
			canFinalize: false,
			blockers:    []string{"验证销售波动[pending] -> 拆分 East 区域[running]"},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected structured tool failure instead of error, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize failure json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "active_branches_block_finalize" {
		t.Fatalf("unexpected finalize failure payload: %#v", payload)
	}
	if payload["blocker_kind"] != "active_branches" {
		t.Fatalf("expected blocker_kind=active_branches, got %#v", payload["blocker_kind"])
	}
	if payload["active_branch_count"].(float64) != 1 {
		t.Fatalf("expected active branch count in payload: %#v", payload)
	}
	branches, ok := payload["active_branches"].([]interface{})
	if !ok || len(branches) != 1 || !strings.Contains(branches[0].(string), "East") {
		t.Fatalf("expected blocker details in payload, got %#v", payload["active_branches"])
	}
}

func TestFinalizeReportRejectsInvalidReportState(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{ID: "analysis", Kind: "markdown", Content: "结论\n\n{{chart:chart_sales}}\n\n{{chart:chart_missing}}"},
				{ID: "sales_chart", Kind: "chart", ChartID: "chart_sales"},
			},
			Charts: []ChartData{
				{ID: "chart_sales"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected structured tool failure instead of error, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize failure json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "report_state_invalid" {
		t.Fatalf("unexpected finalize failure payload: %#v", payload)
	}
	issues, ok := payload["finalize_issues"].([]interface{})
	if !ok {
		t.Fatalf("expected finalize_issues in payload: %#v", payload)
	}
	joined := make([]string, 0, len(issues))
	for _, issue := range issues {
		joined = append(joined, issue.(string))
	}
	if !strings.Contains(strings.Join(joined, ","), "missing_chart:chart_missing") {
		t.Fatalf("expected missing chart issue, got %#v", issues)
	}
	if !strings.Contains(strings.Join(joined, ","), "duplicate_chart:chart_sales(x2)") {
		t.Fatalf("expected duplicate chart issue, got %#v", issues)
	}
}

func TestFinalizeReportDoesNotInterpretSemanticAmbiguityInProse(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{ReportState: &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Content: "收入增长 20%，净收入增长 12%。",
				Sources: []EvidenceRef{{Kind: "result", ResultID: "res_1"}},
			},
		},
		Results: map[string]AnalysisResult{"res_1": {ID: "res_1", ToolName: "data_query_sql"}},
	}}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected structured tool failure instead of error, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize failure json payload: %v", err)
	}
	if payload["ok"] != true || payload["delivery_state"] != "finalized" {
		t.Fatalf("finalize must not infer semantic state from prose, got %#v", payload)
	}
}

func TestFinalizeReportDoesNotTreatDefinitionMarkersAsAuthority(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{ReportState: &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Content: "收入 definition：按业务字段展示。口径说明：收入增长 12%。",
				Sources: []EvidenceRef{{Kind: "result", ResultID: "res_1"}},
			},
		},
		Results: map[string]AnalysisResult{"res_1": {ID: "res_1", ToolName: "data_query_sql"}},
	}}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected structured tool failure instead of error, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize failure json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("finalize must not parse definition markers, got %#v", payload)
	}
}

func TestFinalizeReportDoesNotParseAssumptionTextAsConfirmation(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Content: "口径假设：收入采用 net_revenue。收入增长 12%。",
				Sources: []EvidenceRef{{Kind: "result", ResultID: "res_1"}},
			},
		},
		Results: map[string]AnalysisResult{"res_1": {ID: "res_1", ToolName: "data_query_sql"}},
	}
	tool := &FinalizeReportTool{ReportState: state}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("finalize execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize json payload: %v", err)
	}
	if payload["ok"] != true || payload["delivery_state"] != "finalized" {
		t.Fatalf("unexpected finalize payload: %#v", payload)
	}
}

func TestFinalizeReportAllowsUnusedSemanticAmbiguity(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{ReportState: &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "markdown", Content: "库存周转率稳定，未使用收入表。"},
		},
	}}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"库存分析"}`))
	if err != nil {
		t.Fatalf("finalize execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize json payload: %v", err)
	}
	if payload["ok"] != true || payload["delivery_state"] != "finalized" {
		t.Fatalf("expected unused ambiguity not to block finalize, got %#v", payload)
	}
}

func TestFinalizeReportRejectsTitleOnlyBlocks(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{ID: "heading", Kind: "title", Title: "只有标题"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected structured tool failure instead of error, got %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize failure json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "report_state_invalid" {
		t.Fatalf("unexpected finalize failure payload: %#v", payload)
	}
	issues, ok := payload["finalize_issues"].([]interface{})
	if !ok || len(issues) != 1 || issues[0] != "report_has_no_blocks" {
		t.Fatalf("expected report_has_no_blocks issue, got %#v", payload["finalize_issues"])
	}
}

func TestFinalizeReportAllowsDuplicateBlockHeadingAndMissingChartCaption(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{ID: "overview", Kind: "markdown", Title: "数据概览", Content: "## 一、数据概览\n\n正文"},
				{ID: "sales_chart", Kind: "chart", Title: "销售趋势", ChartID: "chart_sales"},
			},
			Charts: []ChartData{
				{ID: "chart_sales", Sources: []EvidenceRef{{Kind: "result", ResultID: "res_1"}}},
			},
			Results: map[string]AnalysisResult{"res_1": {ID: "res_1", ToolName: "data_query_sql"}},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected finalize to succeed, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("expected finalize to succeed, got %#v", payload)
	}
}

func TestFinalizeReportDoesNotGuessAnalyticalBlocksFromDigits(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{ID: "analysis", Kind: "markdown", Content: "收入增长 20%。"},
			},
		},
	}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("expected structured tool failure instead of error, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize failure json payload: %v", err)
	}
	if payload["ok"] != true || payload["delivery_state"] != "finalized" {
		t.Fatalf("finalize must not interpret numeric prose, got %#v", payload)
	}
}

func TestConfigureReportToolMergeAndReset(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	tool := &ConfigureReportTool{ReportState: state}

	result, err := tool.Execute(json.RawMessage(`{
		"action":"merge",
		"custom_css":".hero{color:red;}",
		"body_class":"magazine"
	}`))
	if err != nil {
		t.Fatalf("merge execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected report_configure_layout json payload: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("expected ok=true, got %#v", payload["ok"])
	}
	if state.Layout.BodyClass != "magazine" || state.Layout.CustomCSS != ".hero{color:red;}" {
		t.Fatalf("unexpected layout after merge: %#v", state.Layout)
	}
	if !state.NeedsFinalize {
		t.Fatal("expected layout merge to require finalize")
	}

	if _, err := tool.Execute(json.RawMessage(`{"unknown_field":"x"}`)); err == nil {
		t.Fatal("expected strict contract error for unsupported field")
	}

	if _, err := tool.Execute(json.RawMessage(`{"action":"reset"}`)); err != nil {
		t.Fatalf("reset execute: %v", err)
	}
	if state.Layout != (ReportLayout{}) {
		t.Fatalf("expected empty layout after reset, got %#v", state.Layout)
	}
	if !state.NeedsFinalize {
		t.Fatal("expected layout reset to require finalize")
	}
}

func TestConfigureReportToolRespectsExplicitPartialScope(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	tool := &ConfigureReportTool{
		ReportState: state,
		EditState:   &ReportEditState{ScopeKindValue: "partial_block", TargetBlockID: "b1"},
	}
	result, err := tool.Execute(json.RawMessage(`{"action":"merge","body_class":"wide"}`))
	if err != nil {
		t.Fatalf("expected structured scope failure: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode scope failure: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "edit_scope_violation" {
		t.Fatalf("unexpected scope failure: %#v", payload)
	}
	if state.Layout != (ReportLayout{}) {
		t.Fatalf("layout changed outside explicit scope: %#v", state.Layout)
	}
}

func TestManageReportBlocksToolSupportsCRUD(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	tool := &ManageReportBlocksTool{ReportState: state}

	if _, err := tool.Execute(json.RawMessage(`{"action":"append","block_id":"intro","block_kind":"markdown","title":"导言","content":"第一段"}`)); err != nil {
		t.Fatalf("append intro: %v", err)
	}
	if _, err := tool.Execute(json.RawMessage(`{"action":"append","block_id":"chart-1","block_kind":"chart","title":"趋势图","chart_id":"chart_sales"}`)); err != nil {
		t.Fatalf("append chart block: %v", err)
	}
	if len(state.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(state.Blocks))
	}
	if !state.NeedsFinalize {
		t.Fatal("expected block mutation to require finalize")
	}

	if _, err := tool.Execute(json.RawMessage(`{"action":"upsert","block_id":"intro","block_kind":"markdown","title":"导言","content":"更新后的第一段"}`)); err != nil {
		t.Fatalf("upsert intro: %v", err)
	}
	if state.Blocks[0].Content != "更新后的第一段" {
		t.Fatalf("expected updated intro block, got %#v", state.Blocks[0])
	}

	if _, err := tool.Execute(json.RawMessage(`{"action":"move","block_id":"chart-1","before_block_id":"intro"}`)); err != nil {
		t.Fatalf("move chart block: %v", err)
	}
	if state.Blocks[0].ID != "chart-1" {
		t.Fatalf("expected chart block moved first, got %#v", state.Blocks)
	}

	if _, err := tool.Execute(json.RawMessage(`{"action":"remove","block_id":"intro"}`)); err != nil {
		t.Fatalf("remove intro block: %v", err)
	}
	if len(state.Blocks) != 1 || state.Blocks[0].ID != "chart-1" {
		t.Fatalf("expected only chart block remaining, got %#v", state.Blocks)
	}
	if !state.NeedsFinalize {
		t.Fatal("expected report state to remain draft after block edits")
	}
}

func TestDescribeReportDeliveryStateTracksDraftAndFinalized(t *testing.T) {
	t.Parallel()

	empty := DescribeReportDeliveryState(&ReportState{})
	if empty.DeliveryState != "empty" || empty.HasContent {
		t.Fatalf("unexpected empty delivery state: %#v", empty)
	}

	draft := DescribeReportDeliveryState(&ReportState{
		Blocks:        []ReportBlock{{ID: "b1", Kind: "markdown", Content: "test"}},
		NeedsFinalize: true,
	})
	if draft.DeliveryState != "draft" || draft.IsFinalized {
		t.Fatalf("unexpected draft delivery state: %#v", draft)
	}

	finalized := DescribeReportDeliveryState(&ReportState{
		Blocks:        []ReportBlock{{ID: "b1", Kind: "markdown", Content: "test"}},
		FinalTitle:    "报告",
		FinalAuthor:   "AI",
		NeedsFinalize: false,
	})
	if finalized.DeliveryState != "finalized" || !finalized.IsFinalized {
		t.Fatalf("unexpected finalized delivery state: %#v", finalized)
	}
}
