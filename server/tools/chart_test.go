package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCreateChartToolRequiresExplicitOption(t *testing.T) {
	t.Parallel()

	tool := &CreateChartTool{ReportState: &ReportState{}}
	result, err := tool.Execute(json.RawMessage(`{"chart_id":"chart_1","title":"Observed values"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "call report_create_chart again") {
		t.Fatalf("expected factual validation feedback, got %q", result)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected JSON feedback: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "invalid_chart_spec" {
		t.Fatalf("unexpected validation payload: %#v", payload)
	}
}

func TestCreateChartToolRejectsNonObjectOption(t *testing.T) {
	t.Parallel()

	tool := &CreateChartTool{ReportState: &ReportState{}}
	result, err := tool.Execute(json.RawMessage(`{"chart_id":"chart_1","option":[{"type":"line"}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected JSON feedback: %v", err)
	}
	if payload["error_code"] != "invalid_chart_spec" || payload["detail"] == "" {
		t.Fatalf("unexpected validation payload: %#v", payload)
	}
}

func TestCreateChartToolPreservesExplicitOptionAndDimensions(t *testing.T) {
	t.Parallel()

	tool := &CreateChartTool{ReportState: &ReportState{}}
	result, err := tool.Execute(json.RawMessage(`{
		"chart_id":"chart_custom",
		"title":"Observed values",
		"width":"90%",
		"height":"360px",
		"option":{"xAxis":{"data":["A","B"]},"series":[{"type":"scatter","data":[1,2]}]}
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected JSON feedback: %v", err)
	}
	if payload["ok"] != true || payload["chart_ref"] != "{{chart:chart_custom}}" {
		t.Fatalf("unexpected success payload: %#v", payload)
	}
	if len(tool.ReportState.Charts) != 1 || !tool.ReportState.NeedsFinalize {
		t.Fatalf("unexpected report state: %#v", tool.ReportState)
	}
	chart := tool.ReportState.Charts[0]
	if chart.Width != "90%" || chart.Height != "360px" {
		t.Fatalf("explicit dimensions were not preserved: %#v", chart)
	}
	var option map[string]interface{}
	if err := json.Unmarshal(chart.Option, &option); err != nil {
		t.Fatalf("unmarshal option: %v", err)
	}
	if option["tooltip"] != nil || option["legend"] != nil || option["title"] != nil {
		t.Fatalf("runtime injected presentation defaults: %#v", option)
	}
	series := option["series"].([]interface{})[0].(map[string]interface{})
	if series["type"] != "scatter" {
		t.Fatalf("runtime rewrote chart type: %#v", option)
	}
}

func TestCreateChartToolRejectsStringifiedOption(t *testing.T) {
	t.Parallel()

	tool := &CreateChartTool{ReportState: &ReportState{}}
	result, err := tool.Execute(json.RawMessage(`{"chart_id":"chart_1","option":"{\"series\":[]}"}`))
	if err != nil {
		t.Fatalf("expected structured validation failure, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("parse validation payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "invalid_chart_spec" {
		t.Fatalf("expected exact object contract to reject stringified JSON: %#v", payload)
	}
}

func TestCreateChartPreservesGeneralIdentifierAndDimensions(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	result, err := applyReportChartMutation(state, nil, createChartParams{
		ChartID: "chart.revenue-1", Option: json.RawMessage(`{"series":[]}`), Width: "75%", Height: "24rem",
	})
	if err != nil {
		t.Fatalf("apply chart mutation: %v", err)
	}
	if result.ChartRef != "{{chart:chart.revenue-1}}" || len(state.Charts) != 1 || state.Charts[0].Width != "75%" || state.Charts[0].Height != "24rem" {
		t.Fatalf("chart facts were rewritten: result=%#v state=%#v", result, state.Charts)
	}
	state.Blocks = []ReportBlock{{ID: "analysis", Kind: "markdown", Content: result.ChartRef}}
	html := RenderReportHTML("Report", "", state)
	if !strings.Contains(html, `data-chart-id="chart.revenue-1"`) || !strings.Contains(html, `style="width:75%;height:24rem;"`) {
		t.Fatalf("general chart reference was not rendered exactly: %s", html)
	}
}
