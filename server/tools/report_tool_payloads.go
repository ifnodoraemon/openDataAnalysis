package tools

import (
	"strconv"
	"strings"
)

const reportDraftMessage = "报告当前为草稿，尚未生成定稿文件。"

func reportDraftSuccess(toolName string, state *ReportState, fields map[string]interface{}) string {
	payload := reportDraftPayload(state, fields)
	return toolSuccess(toolName, payload)
}

func reportDraftPayload(state *ReportState, fields map[string]interface{}) map[string]interface{} {
	payload := clonePayload(fields)
	delivery := describeReportDeliveryStateLocked(state)
	payload["delivery_state"] = delivery.DeliveryState
	payload["is_finalized"] = delivery.IsFinalized
	payload["needs_finalize"] = delivery.NeedsFinalize
	payload["requires_finalize_for_delivery"] = delivery.HasContent
	if _, hasUISummary := payload["ui_summary"]; !hasUISummary {
		payload["ui_summary"] = reportDraftMessage
	}
	return payload
}

func reportEditScopeFailure(toolName, targetKey, targetID, targetLabel, uiSummary string, fields map[string]interface{}, editState *ReportEditState) string {
	payload := clonePayload(fields)
	payload[targetKey] = targetID
	payload["ui_summary"] = uiSummary
	addEditScopeFailureFacts(payload, editState)
	return toolFailure(toolName, "edit_scope_violation", "current edit scope does not allow modifying this "+targetLabel, payload)
}

func addEditScopeFailureFacts(payload map[string]interface{}, editState *ReportEditState) {
	if editState == nil || !editState.Active() {
		return
	}
	scope := editState.Snapshot()
	payload["active_edit_scope"] = scope
	if scopeKind, ok := scope["scope_kind"].(string); ok && scopeKind != "" {
		payload["scope_kind"] = scopeKind
	}
	targetBlockID, _ := scope["target_block_id"].(string)
	if strings.TrimSpace(targetBlockID) != "" {
		payload["target_block_id"] = targetBlockID
		payload["allowed_block_ids"] = []string{targetBlockID}
	}
	targetChartID, _ := scope["target_chart_id"].(string)
	if strings.TrimSpace(targetChartID) != "" {
		payload["target_chart_id"] = targetChartID
	}
	if allowedCharts, ok := scope["allowed_chart_ids"].([]string); ok {
		payload["allowed_chart_ids"] = allowedCharts
	}
	if scope["scope_kind"] == "partial_selection" {
		payload["allowed_block_actions"] = []string{"upsert"}
		payload["selection_mutation_contract"] = "Only the target block content may change; content outside the selected range, block title, block kind, chart_id, and sources remain protected."
	}
}

func reportFinalizeBlockedFailure(state *ReportState, blockers []string) string {
	return toolFailure("report_finalize", "active_branches_block_finalize", "detected active goal branches; delivery_state stays draft.", mergePayloads(
		reportDraftPayload(state, nil),
		map[string]interface{}{
			"blocker_kind":        "active_branches",
			"active_branch_count": len(blockers),
			"active_branches":     blockers,
			"ui_summary":          formatFinalizeBlockedSummary(len(blockers)),
		},
	))
}

func reportFinalizeIssuesFailure(state *ReportState, issues []string) string {
	return toolFailure("report_finalize", "report_state_invalid", "report validation failed; delivery_state stays draft.", mergePayloads(
		reportDraftPayload(state, nil),
		map[string]interface{}{
			"finalize_issue_count": len(issues),
			"finalize_issues":      issues,
			"ui_summary":           formatFinalizeIssuesSummary(len(issues)),
		},
	))
}

func reportAlreadyFinalizedFailure(state *ReportState) string {
	return toolFailure("report_finalize", "report_already_finalized", "delivery_state is already finalized; no new draft changes detected.", mergePayloads(
		reportDraftPayload(state, nil),
		map[string]interface{}{
			"ui_summary": "报告已经定稿，未检测到新的草稿变更。",
		},
	))
}

func reportFinalizeSuccess(fields map[string]interface{}) string {
	payload := clonePayload(fields)
	payload["delivery_state"] = "finalized"
	payload["is_finalized"] = true
	payload["needs_finalize"] = false
	return toolSuccess("report_finalize", payload)
}

func clonePayload(fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return map[string]interface{}{}
	}
	payload := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		payload[key] = value
	}
	return payload
}

func mergePayloads(base map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	payload := clonePayload(base)
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func formatFinalizeBlockedSummary(count int) string {
	return "报告定稿受阻，仍有 " + strconv.Itoa(count) + " 个活动目标分支。"
}

func formatFinalizeIssuesSummary(count int) string {
	return "报告定稿受阻，仍有 " + strconv.Itoa(count) + " 个结构问题。"
}
