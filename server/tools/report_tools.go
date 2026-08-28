package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/metrics"
)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &ManageReportBlocksTool{ReportState: ctx.ReportState, EditState: ctx.EditState}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &ConfigureReportTool{ReportState: ctx.ReportState, EditState: ctx.EditState}
	})
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &FinalizeReportTool{
			ReportState: ctx.ReportState,
			Subgoals:    ctx.Subgoals,
		}
	})
}

type ConfigureReportTool struct {
	ReportState *ReportState
	EditState   *ReportEditState
}

type ManageReportBlocksTool struct {
	ReportState *ReportState
	EditState   *ReportEditState
}

// FinalizeReportTool 校验并更新报告交付状态
type FinalizeReportTool struct {
	ReportState *ReportState
	Subgoals    SubgoalChecker
}

func (t *ConfigureReportTool) Name() string { return "report_configure_layout" }
func (t *ConfigureReportTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, EmitsReportPreview: true}
}
func (t *ConfigureReportTool) Description() string {
	return "Read and modify report layout configuration. Supports updating or resetting CSS and body class; modifies report layout state but does not directly modify blocks or charts. Returns updated layout facts and delivery_state."
}
func (t *ConfigureReportTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"action": {"type": "string", "enum": ["merge", "reset"], "description": "Exact layout mutation to perform."},
			"custom_css": {"type": "string", "description": "Custom CSS appended to the page."},
			"body_class": {"type": "string", "description": "Class appended to the body element."}
		},
		"required": ["action"]
	}`)
}

func (t *ConfigureReportTool) Execute(args json.RawMessage) (string, error) {
	if t.ReportState == nil {
		return "", fmt.Errorf("report state is not initialized")
	}
	if t.EditState != nil && !t.EditState.LayoutMutationAllowed() {
		return reportEditScopeFailure("report_configure_layout", "layout", "report_layout", "layout", "report layout is outside current partial edit scope", nil, t.EditState), nil
	}
	var params reportLayoutParams
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	t.ReportState.Lock()
	result, err := applyReportLayoutMutation(t.ReportState, params)
	if err != nil {
		t.ReportState.Unlock()
		return "", err
	}

	success := reportDraftSuccess("report_configure_layout", t.ReportState, map[string]interface{}{
		"action":         result.Action,
		"has_custom_css": result.HasCustomCSS,
		"body_class":     result.BodyClass,
		"ui_summary":     result.UISummary,
	})
	t.ReportState.Unlock()
	return success, nil
}

func (t *ManageReportBlocksTool) Name() string { return "report_manage_blocks" }
func (t *ManageReportBlocksTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, EmitsReportPreview: true}
}
func (t *ManageReportBlocksTool) Description() string {
	return "Apply an explicit append, upsert, remove, or move mutation to one report block. action and block_id are always required; append/upsert also require block_kind. Markdown/html content may contain `{{chart:chart_id}}` references, while chart blocks use chart_id. The runtime does not choose a block kind, generate an ID, retain omitted citations, or infer an operation. Returns block_id, block_count, and delivery_state facts. Partial edit scope limits which blocks may change."
}
func (t *ManageReportBlocksTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"action": {"type": "string", "enum": ["append", "upsert", "remove", "move"], "description": "Exact mutation to perform."},
			"block_id": {"type": "string", "description": "Caller-provided stable block ID."},
			"block_kind": {"type": "string", "enum": ["markdown", "html", "chart"], "description": "Block type."},
			"title": {"type": "string", "description": "Block title."},
			"content": {"type": "string", "description": "Block content. Markdown/HTML blocks support {{chart:chart_id}} for inline charts; chart blocks use this as caption below the chart."},
			"chart_id": {"type": "string", "description": "Chart ID referenced by a chart block."},
			"before_block_id": {"type": "string", "description": "Insert before this block ID."},
			"after_block_id": {"type": "string", "description": "Insert after this block ID."},
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
		"required": ["action", "block_id"]
	}`)
}

func (t *ManageReportBlocksTool) Execute(args json.RawMessage) (string, error) {
	if t.ReportState == nil {
		return "", fmt.Errorf("report state is not initialized")
	}
	var params reportBlockMutationParams
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	t.ReportState.Lock()
	result, err := applyReportBlockMutation(t.ReportState, t.EditState, params)
	if err != nil {
		t.ReportState.Unlock()
		var scopeErr reportBlockScopeError
		if errors.As(err, &scopeErr) {
			return reportEditScopeFailure("report_manage_blocks", "block_id", scopeErr.BlockID, " block", fmt.Sprintf("block %s is outside current partial edit scope", scopeErr.BlockID), map[string]interface{}{
				"action": scopeErr.Action,
			}, t.EditState), nil
		}
		return "", err
	}

	success := reportDraftSuccess("report_manage_blocks", t.ReportState, map[string]interface{}{
		"action":      result.Action,
		"block_id":    result.BlockID,
		"block_kind":  result.BlockKind,
		"block_count": result.BlockCount,
		"ui_summary":  result.UISummary,
	})
	t.ReportState.Unlock()
	return success, nil
}

func (t *FinalizeReportTool) Name() string { return "report_finalize" }
func (t *FinalizeReportTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, DeliveryBoundary: true}
}
func (t *FinalizeReportTool) Description() string {
	return "Validate report structure, active goal branches, chart references, and result/artifact citation IDs; if valid, write final title/author and set delivery_state to finalized. It does not interpret report prose, choose business semantics, auto-complete content, or silently rewrite blocks or charts."
}
func (t *FinalizeReportTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"report_title": {"type": "string", "description": "Report title"},
			"author": {"type": "string", "description": "Author/analyst name"}
		},
		"required": ["report_title"]
	}`)
}

func (t *FinalizeReportTool) Execute(args json.RawMessage) (string, error) {
	if t.ReportState == nil {
		return "", fmt.Errorf("report state is not initialized")
	}
	var params reportFinalizeParams
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}

	t.ReportState.Lock()

	result, err := finalizeReportState(t.ReportState, t.Subgoals, params)
	if err != nil {
		metrics.ReportFinalizeTotal.WithLabelValues("failure").Inc()
		var blockedErr reportFinalizeBlockedError
		if errors.As(err, &blockedErr) {
			failure := reportFinalizeBlockedFailure(t.ReportState, blockedErr.Blockers)
			t.ReportState.Unlock()
			return failure, nil
		}
		var issuesErr reportFinalizeIssuesError
		if errors.As(err, &issuesErr) {
			failure := reportFinalizeIssuesFailure(t.ReportState, issuesErr.Issues)
			t.ReportState.Unlock()
			return failure, nil
		}
		var alreadyFinalizedErr reportAlreadyFinalizedError
		if errors.As(err, &alreadyFinalizedErr) {
			failure := reportAlreadyFinalizedFailure(t.ReportState)
			t.ReportState.Unlock()
			return failure, nil
		}
		t.ReportState.Unlock()
		return "", err
	}

	metrics.ReportFinalizeTotal.WithLabelValues("success").Inc()
	success := reportFinalizeSuccess(map[string]interface{}{
		"report_title": result.ReportTitle,
		"author":       result.Author,
		"block_count":  result.BlockCount,
		"chart_count":  result.ChartCount,
		"ui_summary":   fmt.Sprintf("报告已定稿，共 %d 个内容块、%d 个图表。", result.BlockCount, result.ChartCount),
	})
	t.ReportState.Unlock()
	return success, nil
}
