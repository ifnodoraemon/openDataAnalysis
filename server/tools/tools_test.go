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
	"github.com/ifnodoraemon/openDataAnalysis/service"
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
	data.AnalyzeTableSemantics = func(ctx context.Context, chatFn data.LLMChatFunc, schema *data.SchemaInfo, activeTables []string) (*data.SemanticProfile, error) {
		return &data.SemanticProfile{
			TableSummary: "Mock test semantics",
		}, nil
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
	result, err := tool.Execute(nil)
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
	result, err := tool.Execute(nil)
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
	result, err := tool.Execute(json.RawMessage(`{"table_name":"sales"}`))
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

func TestDescribeDataToolExposesAmbiguousMetricGroups(t *testing.T) {
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
	result, err := tool.Execute(json.RawMessage(`{"table_name":"revenue_metrics"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK                        bool                `json:"ok"`
		AmbiguousMetricGroupCount int                 `json:"ambiguous_metric_group_count"`
		AmbiguousMetricGroups     map[string][]string `json:"ambiguous_metric_groups"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.AmbiguousMetricGroupCount != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !sameStrings(payload.AmbiguousMetricGroups["revenue"], []string{"gross_revenue", "net_revenue", "recognized_revenue"}) {
		t.Fatalf("unexpected ambiguous metric groups: %#v", payload.AmbiguousMetricGroups)
	}
}

func TestDescribeDataToolExposesTimeCoverageFacts(t *testing.T) {
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
	result, err := tool.Execute(json.RawMessage(`{"table_name":"spend"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK                   bool                     `json:"ok"`
		TimeColumnCount      int                      `json:"time_column_count"`
		TimeColumnCandidates []map[string]interface{} `json:"time_column_candidates"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if !payload.OK || payload.TimeColumnCount != 1 || len(payload.TimeColumnCandidates) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	candidate := payload.TimeColumnCandidates[0]
	name, _ := candidate["column_name"].(string)
	grain, _ := candidate["grain"].(string)
	isPrimary, _ := candidate["heuristic_primary"].(bool)
	if name != "dt" || grain != "day" {
		t.Fatalf("unexpected time column candidate: %#v", candidate)
	}
	if !isPrimary {
		t.Fatalf("expected heuristic_primary=true")
	}
	if candidate["coverage_start"] != "2025-01-05" || candidate["coverage_end"] != "2025-02-23" {
		t.Fatalf("unexpected time coverage: %#v", candidate)
	}
}

func TestDescribeDataToolReturnsStructuredFailure(t *testing.T) {
	t.Parallel()

	ing := data.NewIngester(t.TempDir())
	if err := ing.InitDB("session_describe_fail"); err != nil {
		t.Fatalf("init db: %v", err)
	}

	tool := &DescribeDataTool{Ingester: ing}
	result, err := tool.Execute(json.RawMessage(`{"table_name":"missing_table"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "row_count_failed" {
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
	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT month, revenue FROM sales ORDER BY month"}`))
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
	result, err := tool.Execute(json.RawMessage(`{"sql":"SELECT * FROM missing_table"}`))
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

	state := &ReportState{}
	blockTool := &ManageReportBlocksTool{ReportState: state}
	finalizeTool := &FinalizeReportTool{ReportState: state}

	blockResult, err := blockTool.Execute(json.RawMessage(`{
		"block_id":"summary",
		"block_kind":"markdown",
		"title":"执行摘要",
		"content":"收入增长 20%",
		"sources":[{"kind":"sql","sql":"SELECT revenue_growth FROM sales_metrics","summary":"收入增长率"}]
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
	if finalizePayload.UISummary != "delivery_state=finalized; block_count=1; chart_count=0" {
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

	if _, err := blockTool.Execute(json.RawMessage(`{"block_id":"heading","block_kind":"title","title":"只写标题"}`)); err == nil {
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

func TestFinalizeReportRejectsUnresolvedSemanticAmbiguity(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{
					ID:      "analysis",
					Kind:    "markdown",
					Content: "收入增长 20%，净收入增长 12%。",
					Sources: []EvidenceRef{{Kind: "sql", SQL: "SELECT net_revenue FROM revenue_metrics"}},
				},
			},
		},
		SessionSourcesProvider: func() ([]service.SessionSourceSummary, error) {
			return []service.SessionSourceSummary{
				{
					SourceID:          "src_1",
					AnalysisTableName: "revenue_metrics",
					ProfileID:         "sp_1",
					SemanticStatus:    "profiled",
					AmbiguityCount:    1,
				},
			}, nil
		},
		ProfileDetailProvider: func(profileID string) (string, string, error) {
			return `{"ambiguities":[{"kind":"ambiguous_metrics","candidates":["gross_revenue","net_revenue"]}]}`, "[]", nil
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
	if !ok || len(issues) != 1 || !strings.Contains(issues[0].(string), "unresolved_semantic_ambiguity:revenue_metrics") {
		t.Fatalf("expected unresolved semantic ambiguity issue, got %#v", payload["finalize_issues"])
	}
}

func TestFinalizeReportRejectsSemanticAmbiguityWithOnlyDefinitionMarker(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{
					ID:      "analysis",
					Kind:    "markdown",
					Content: "收入 definition：按业务字段展示。口径说明：收入增长 12%。",
					Sources: []EvidenceRef{{Kind: "table", TableName: "revenue_metrics"}},
				},
			},
		},
		SessionSourcesProvider: func() ([]service.SessionSourceSummary, error) {
			return []service.SessionSourceSummary{
				{
					SourceID:          "src_1",
					AnalysisTableName: "revenue_metrics",
					ProfileID:         "sp_1",
					SemanticStatus:    "profiled",
					AmbiguityCount:    1,
				},
			}, nil
		},
		ProfileDetailProvider: func(profileID string) (string, string, error) {
			return `{"ambiguities":[{"kind":"ambiguous_metrics","candidates":["gross_revenue","net_revenue"]}]}`, "[]", nil
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
	if !ok || len(issues) != 1 || !strings.Contains(issues[0].(string), "unresolved_semantic_ambiguity:revenue_metrics") {
		t.Fatalf("expected unresolved semantic ambiguity issue, got %#v", payload["finalize_issues"])
	}
}

func TestFinalizeReportRejectsDocumentedSemanticAssumptionWithoutConfirmation(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Content: "口径假设：收入采用 net_revenue。收入增长 12%。",
				Sources: []EvidenceRef{{Kind: "sql", SQL: "SELECT net_revenue FROM revenue_metrics"}},
			},
		},
	}
	tool := &FinalizeReportTool{
		ReportState: state,
		SessionSourcesProvider: func() ([]service.SessionSourceSummary, error) {
			return []service.SessionSourceSummary{
				{
					SourceID:          "src_1",
					AnalysisTableName: "revenue_metrics",
					ProfileID:         "sp_1",
					SemanticStatus:    "profiled",
					AmbiguityCount:    1,
				},
			}, nil
		},
		ProfileDetailProvider: func(profileID string) (string, string, error) {
			return `{"ambiguities":[{"kind":"ambiguous_metrics","candidates":["gross_revenue","net_revenue"]}]}`, "[]", nil
		},
	}

	result, err := tool.Execute(json.RawMessage(`{"report_title":"销售分析"}`))
	if err != nil {
		t.Fatalf("finalize execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected finalize json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "report_state_invalid" {
		t.Fatalf("unexpected finalize payload: %#v", payload)
	}
	issues, ok := payload["finalize_issues"].([]interface{})
	if !ok || len(issues) != 1 || !strings.Contains(issues[0].(string), "unresolved_semantic_ambiguity:revenue_metrics") {
		t.Fatalf("expected unresolved semantic ambiguity issue, got %#v", payload["finalize_issues"])
	}
}

func TestFinalizeReportAllowsUnusedSemanticAmbiguity(t *testing.T) {
	t.Parallel()

	tool := &FinalizeReportTool{
		ReportState: &ReportState{
			Blocks: []ReportBlock{
				{ID: "analysis", Kind: "markdown", Content: "库存周转率稳定，未使用收入表。"},
			},
		},
		SessionSourcesProvider: func() ([]service.SessionSourceSummary, error) {
			return []service.SessionSourceSummary{
				{
					SourceID:          "src_1",
					DisplayName:       "Revenue Metrics",
					AnalysisTableName: "revenue_metrics",
					SemanticStatus:    "profiled",
					AmbiguityCount:    1,
				},
			}, nil
		},
	}

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
				{ID: "chart_sales", Sources: []EvidenceRef{{Kind: "sql", SQL: "SELECT month, sales FROM sales_metrics"}}},
			},
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

func TestFinalizeReportRejectsAnalyticalBlockWithoutEvidence(t *testing.T) {
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
	if payload["ok"] != false || payload["error_code"] != "report_state_invalid" {
		t.Fatalf("unexpected finalize failure payload: %#v", payload)
	}
	issues, ok := payload["finalize_issues"].([]interface{})
	if !ok || len(issues) != 1 || issues[0] != "missing_evidence:analysis" {
		t.Fatalf("expected missing_evidence issue, got %#v", payload["finalize_issues"])
	}
}

func TestConfigureReportToolMergeAndReset(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	tool := &ConfigureReportTool{ReportState: state}

	result, err := tool.Execute(json.RawMessage(`{
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

	rejected, err := tool.Execute(json.RawMessage(`{"unknown_field":"x"}`))
	if err != nil {
		t.Fatalf("unsupported merge execute: %v", err)
	}
	var rejectedPayload map[string]interface{}
	if err := json.Unmarshal([]byte(rejected), &rejectedPayload); err != nil {
		t.Fatalf("expected failure payload: %v", err)
	}
	if rejectedPayload["ok"] != false {
		t.Fatalf("expected ok=false for unsupported options, got %#v", rejectedPayload["ok"])
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

func TestManageReportBlocksToolSupportsCRUD(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	tool := &ManageReportBlocksTool{ReportState: state}

	if _, err := tool.Execute(json.RawMessage(`{"block_id":"intro","block_kind":"markdown","title":"导言","content":"第一段"}`)); err != nil {
		t.Fatalf("append intro: %v", err)
	}
	if _, err := tool.Execute(json.RawMessage(`{"block_id":"chart-1","block_kind":"chart","title":"趋势图","chart_id":"chart_sales"}`)); err != nil {
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
