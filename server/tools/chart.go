package tools

import (
	"encoding/json"
	"errors"
	"fmt"
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
	ChartID string          `json:"chart_id"`
	Title   string          `json:"title"`
	Option  json.RawMessage `json:"option"`
	Width   string          `json:"width"`
	Height  string          `json:"height"`
	Sources []EvidenceRef   `json:"sources"`
}

// CreateChartTool 创建 ECharts 图表
type CreateChartTool struct {
	ReportState *ReportState
	EditState   *ReportEditState
}

func (t *CreateChartTool) Name() string { return "report_create_chart" }
func (t *CreateChartTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, EmitsReportPreview: true}
}

func (t *CreateChartTool) Strict() bool { return true }

func (t *CreateChartTool) Description() string {
	return "Create or update an ECharts chart from an explicit native option object. Accepts source citations and optional container dimensions; returns chart_id, chart_ref, and delivery_state facts. Modifies report chart state but does not infer a chart type, series mapping, title, or content block. To embed it inline, use `{{chart:chart_id}}` in a markdown/html block. When a partial edit scope is active, only the authorized chart_id can be modified."
}

func (t *CreateChartTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"chart_id": {"type": "string", "description": "Unique chart identifier."},
			"title": {"type": "string", "description": "Caller-provided display title; the runtime does not derive it from option."},
			"option": {"type": "object", "description": "Complete native ECharts option object."},
			"width": {"type": "string", "description": "Optional chart container width."},
			"height": {"type": "string", "description": "Optional chart container height."},
				"sources": {
					"type": "array",
					"description": "Structured citations resolving to an existing analysis result, artifact, or chart ledger ID.",
					"items": {
						"type": "object",
						"additionalProperties": false,
						"properties": {
							"kind":       {"type": "string", "enum": ["result", "artifact", "chart"]},
							"result_id":  {"type": "string"},
							"artifact_id":{"type": "string"},
							"chart_id":   {"type": "string"},
							"label":      {"type": "string"}
						},
						"required": ["kind"]
					}
				}
			},
			"required": ["chart_id", "option"]
		}`)
}

func (t *CreateChartTool) Execute(args json.RawMessage) (string, error) {
	if t.ReportState == nil {
		return "", fmt.Errorf("report state is not initialized")
	}
	var params createChartParams
	if err := decodeToolArgs(args, &params); err != nil {
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
		"ui_summary": fmt.Sprintf("图表 %s 已%s报告，当前仍为草稿。", result.ChartID, map[bool]string{true: "更新到", false: "写入"}[result.Replaced]),
	}), nil
}

func chartValidationFeedback(code, chartID, title, message, detail string) string {
	payload := map[string]interface{}{
		"chart_id": chartID,
		"title":    title,
	}
	if detail != "" {
		payload["detail"] = detail
	}
	payload["required_fields"] = []string{"chart_id", "option"}
	return toolFailure("report_create_chart", code, message, payload)
}
