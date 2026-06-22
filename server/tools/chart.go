package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ChartData 图表数据结构
type ChartData struct {
	ID      string          `json:"id"`
	Option  json.RawMessage `json:"option"` // ECharts option JSON
	Width   string          `json:"width,omitempty"`
	Height  string          `json:"height,omitempty"`
	Sources []EvidenceRef   `json:"sources,omitempty"`
}

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &CreateChartTool{ReportState: ctx.ReportState, EditState: ctx.EditState}
	})
}

type createChartParams struct {
	ChartID    string             `json:"chart_id"`
	Title      string             `json:"title"`
	Option     json.RawMessage    `json:"option"`
	ChartType  string             `json:"chart_type"`
	Categories []string           `json:"categories"`
	Series     []chartSeriesInput `json:"series"`
	Values     []chartValueInput  `json:"values"`
	Legend     []string           `json:"legend"`
	XAxisName  string             `json:"x_axis_name"`
	YAxisName  string             `json:"y_axis_name"`
	Y2AxisName string             `json:"y2_axis_name"`
	Sources    []EvidenceRef      `json:"sources"`
}

type chartSeriesInput struct {
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Data   []float64 `json:"data"`
	YAxis  string    `json:"y_axis"`
	Smooth bool      `json:"smooth"`
	Color  string    `json:"color"`
	Stack  string    `json:"stack"`
}

type chartValueInput struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Color string  `json:"color"`
}

// CreateChartTool 创建 ECharts 图表
type CreateChartTool struct {
	ReportState *ReportState
	EditState   *ReportEditState
}

func (t *CreateChartTool) Name() string { return "report_create_chart" }

func (t *CreateChartTool) Strict() bool { return true }

func (t *CreateChartTool) Description() string {
	return "Create or update an ECharts chart. Supports simplified DSL or native option; accepts source citations for the chart data and returns chart_id, chart_ref, and delivery_state facts. Modifies report chart state but does not auto-create or update content blocks. To embed a chart inline, use `{{chart:chart_id}}` placeholder in markdown/html block content. When a partial edit scope is active, only the authorized chart_id can be modified."
}

func (t *CreateChartTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"chart_id": {"type": "string", "description": "Unique chart identifier, e.g. chart_sales_trend"},
			"title": {"type": "string", "description": "Chart title"},
			"option": {"type": "object", "description": "Optional. Native ECharts option; when present, DSL inference is skipped."},
			"chart_type": {"type": "string", "enum": ["bar", "line", "pie"], "description": "Chart type."},
			"categories": {"type": "array", "description": "Category axis labels for bar/line charts", "items": {"type": "string"}},
			"series": {
				"type": "array",
				"description": "Series definition for bar/line charts; data should come from data_query_sql results",
				"items": {
					"type": "object",
					"additionalProperties": false,
					"properties": {
						"name": {"type": "string"},
						"type": {"type": "string", "enum": ["bar", "line"]},
						"data": {"type": "array", "items": {"type": "number"}},
						"y_axis": {"type": "string", "enum": ["left", "right"]},
						"smooth": {"type": "boolean"},
						"color": {"type": "string"},
						"stack": {"type": "string"}
					},
					"required": ["data"]
				}
			},
			"values": {
				"type": "array",
				"description": "Pie chart data",
				"items": {
					"type": "object",
					"additionalProperties": false,
					"properties": {
						"name": {"type": "string"},
						"value": {"type": "number"},
						"color": {"type": "string"}
					},
					"required": ["name", "value"]
				}
			},
				"legend": {"type": "array", "items": {"type": "string"}},
				"x_axis_name": {"type": "string"},
				"y_axis_name": {"type": "string"},
				"y2_axis_name": {"type": "string"},
				"sources": {
					"type": "array",
					"description": "Source citations for the chart data, recording which query/table/tool result produced the series or values.",
					"items": {
						"type": "object",
						"additionalProperties": false,
						"properties": {
							"kind":       {"type": "string", "enum": ["sql", "chart", "table", "python", "tool_result"]},
							"tool_name":  {"type": "string"},
							"sql":        {"type": "string"},
							"table_name": {"type": "string"},
							"chart_id":   {"type": "string"},
							"summary":    {"type": "string"}
						},
						"required": ["kind"]
					}
				}
			},
			"required": ["chart_id", "title"]
		}`)
}

func (t *CreateChartTool) Execute(args json.RawMessage) (string, error) {
	normalizedArgs, err := normalizeStringifiedJSONFields(args, "option", "categories", "series", "values", "legend", "sources")
	if err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	var params createChartParams
	if err := json.Unmarshal(normalizedArgs, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	t.ReportState.Lock()
	result, err := applyReportChartMutation(t.ReportState, t.EditState, params)
	t.ReportState.Unlock()
	if err != nil {
		var validationErr reportChartValidationError
		if errors.As(err, &validationErr) {
			return chartValidationFeedback("invalid_chart_spec", validationErr.ChartID, validationErr.Title, "invalid chart definition", validationErr.Detail), nil
		}
		var scopeErr reportChartScopeError
		if errors.As(err, &scopeErr) {
			return reportEditScopeFailure("report_create_chart", "chart_id", scopeErr.ChartID, "chart", fmt.Sprintf("chart %s is outside current partial edit scope", scopeErr.ChartID), nil, t.EditState), nil
		}
		return "", err
	}

	return reportDraftSuccess("report_create_chart", t.ReportState, map[string]interface{}{
		"chart_id":   result.ChartID,
		"title":      result.Title,
		"chart_ref":  result.ChartRef,
		"ui_summary": fmt.Sprintf("chart %s %s report state; delivery_state=draft", result.ChartID, map[bool]string{true: "updated in", false: "written to"}[result.Replaced]),
	}), nil
}

func buildOptionFromDSL(params createChartParams) (json.RawMessage, error) {
	chartType := strings.ToLower(strings.TrimSpace(params.ChartType))
	if chartType == "" {
		switch {
		case len(params.Values) > 0:
			chartType = "pie"
		case len(params.Series) > 0:
			chartType = firstNonEmptySeriesType(params.Series, "bar")
		}
	}

	switch chartType {
	case "pie":
		return buildPieOption(params)
	case "bar", "line":
		return buildAxisChartOption(params, chartType)
	default:
		return nil, fmt.Errorf("chart_type is required; currently only bar, line, pie are supported")
	}
}

func hasRawChartOption(option json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(option))
	return trimmed != "" && trimmed != "null" && trimmed != "{}"
}

func buildAxisChartOption(params createChartParams, defaultType string) (json.RawMessage, error) {
	if len(params.Categories) == 0 {
		return nil, fmt.Errorf("bar/line charts require categories")
	}
	if len(params.Series) == 0 {
		return nil, fmt.Errorf("bar/line charts require series")
	}

	legend := append([]string(nil), params.Legend...)
	hasRightAxis := false
	series := make([]map[string]interface{}, 0, len(params.Series))
	for i, item := range params.Series {
		if len(item.Data) == 0 {
			return nil, fmt.Errorf("series[%d].data cannot be empty", i)
		}
		if len(item.Data) != len(params.Categories) {
			return nil, fmt.Errorf("series[%d].data length must match categories", i)
		}

		seriesType := strings.ToLower(strings.TrimSpace(item.Type))
		if seriesType == "" {
			seriesType = defaultType
		}
		if seriesType != "bar" && seriesType != "line" {
			return nil, fmt.Errorf("series[%d].type only supports bar or line", i)
		}

		name := strings.TrimSpace(item.Name)
		if name == "" {
			if len(params.Series) == 1 {
				name = params.Title
			} else {
				name = fmt.Sprintf("series_%d", i+1)
			}
		}
		legend = appendIfMissing(legend, name)

		seriesItem := map[string]interface{}{
			"name": name,
			"type": seriesType,
			"data": item.Data,
		}
		if strings.EqualFold(item.YAxis, "right") {
			seriesItem["yAxisIndex"] = 1
			hasRightAxis = true
		}
		if item.Smooth {
			seriesItem["smooth"] = true
		}
		if strings.TrimSpace(item.Stack) != "" {
			seriesItem["stack"] = strings.TrimSpace(item.Stack)
		}
		if strings.TrimSpace(item.Color) != "" {
			seriesItem["itemStyle"] = map[string]interface{}{"color": strings.TrimSpace(item.Color)}
		}
		series = append(series, seriesItem)
	}

	yAxis := []map[string]interface{}{
		{
			"type": "value",
			"name": strings.TrimSpace(params.YAxisName),
		},
	}
	if hasRightAxis {
		rightAxisName := strings.TrimSpace(params.Y2AxisName)
		if rightAxisName == "" {
			rightAxisName = "right_axis"
		}
		yAxis = append(yAxis, map[string]interface{}{
			"type": "value",
			"name": rightAxisName,
		})
	}

	option := map[string]interface{}{
		"title": map[string]interface{}{
			"text": params.Title,
			"left": "center",
			"top":  8,
			"textStyle": map[string]interface{}{
				"fontSize":   18,
				"fontWeight": 700,
				"color":      "#374151",
			},
		},
		"tooltip": map[string]interface{}{
			"trigger": "axis",
		},
		"legend": map[string]interface{}{
			"data": legend,
			"left": "center",
			"top":  42,
			"type": "scroll",
		},
		"grid": map[string]interface{}{
			"left":         "7%",
			"right":        "7%",
			"bottom":       "8%",
			"top":          88,
			"containLabel": true,
		},
		"xAxis": map[string]interface{}{
			"type": "category",
			"data": params.Categories,
			"name": strings.TrimSpace(params.XAxisName),
		},
		"yAxis":  yAxis,
		"series": series,
	}
	normalized, err := json.Marshal(option)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func buildPieOption(params createChartParams) (json.RawMessage, error) {
	if len(params.Values) == 0 {
		return nil, fmt.Errorf("pie charts require values")
	}

	legend := append([]string(nil), params.Legend...)
	pieData := make([]map[string]interface{}, 0, len(params.Values))
	for i, item := range params.Values {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, fmt.Errorf("values[%d].name cannot be empty", i)
		}
		legend = appendIfMissing(legend, name)

		entry := map[string]interface{}{
			"name":  name,
			"value": item.Value,
		}
		if strings.TrimSpace(item.Color) != "" {
			entry["itemStyle"] = map[string]interface{}{"color": strings.TrimSpace(item.Color)}
		}
		pieData = append(pieData, entry)
	}

	option := map[string]interface{}{
		"title": map[string]interface{}{
			"text": params.Title,
			"left": "center",
			"top":  8,
			"textStyle": map[string]interface{}{
				"fontSize":   18,
				"fontWeight": 700,
				"color":      "#374151",
			},
		},
		"tooltip": map[string]interface{}{
			"trigger": "item",
		},
		"legend": map[string]interface{}{
			"data": legend,
			"left": "center",
			"top":  42,
			"type": "scroll",
		},
		"series": []map[string]interface{}{
			{
				"name":   params.Title,
				"type":   "pie",
				"radius": "55%",
				"data":   pieData,
			},
		},
	}
	normalized, err := json.Marshal(option)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func firstNonEmptySeriesType(series []chartSeriesInput, defaultType string) string {
	for _, item := range series {
		if value := strings.ToLower(strings.TrimSpace(item.Type)); value != "" {
			return value
		}
	}
	return defaultType
}

func appendIfMissing(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, existing := range items {
		if existing == value {
			return items
		}
	}
	return append(items, value)
}

func chartValidationFeedback(code, chartID, title, message, detail string) string {
	payload := map[string]interface{}{
		"chart_id": chartID,
		"title":    title,
	}
	if detail != "" {
		payload["detail"] = detail
	}
	payload["required_fields"] = []string{"chart_id", "title"}
	payload["supported_shapes"] = []string{
		"chart_type + categories + series",
		"chart_type=pie + values",
	}
	return toolFailure("report_create_chart", code, message, payload)
}
