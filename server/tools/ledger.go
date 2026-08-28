package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.ReportState == nil {
			return nil
		}
		return &InspectAnalysisLedgerTool{ReportState: ctx.ReportState}
	})
}

// InspectAnalysisLedgerTool exposes durable in-run result and artifact facts
// without injecting their potentially large payloads into every model turn.
type InspectAnalysisLedgerTool struct {
	ReportState *ReportState
}

func (t *InspectAnalysisLedgerTool) Name() string { return "state_analysis_ledger_inspect" }
func (t *InspectAnalysisLedgerTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *InspectAnalysisLedgerTool) Description() string {
	return "Read the current run's analysis-result and artifact ledger. With no ID, returns compact indexes; with result_id or artifact_id, returns that exact stored record. Does not execute analysis or modify state."
}
func (t *InspectAnalysisLedgerTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"result_id":{"type":"string"},"artifact_id":{"type":"string"}},"required":[]}`)
}
func (t *InspectAnalysisLedgerTool) Execute(args json.RawMessage) (string, error) {
	if t.ReportState == nil {
		return "", fmt.Errorf("analysis ledger is not initialized")
	}
	var params struct {
		ResultID   string `json:"result_id"`
		ArtifactID string `json:"artifact_id"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("invalid ledger inspection parameters: %w", err)
	}
	if params.ResultID != strings.TrimSpace(params.ResultID) || params.ArtifactID != strings.TrimSpace(params.ArtifactID) {
		return "", fmt.Errorf("ledger IDs must not contain leading or trailing whitespace")
	}
	if params.ResultID != "" && params.ArtifactID != "" {
		return toolFailure(t.Name(), "ambiguous_lookup", "provide result_id or artifact_id, not both", nil), nil
	}

	t.ReportState.RLock()
	defer t.ReportState.RUnlock()
	if params.ResultID != "" {
		result, ok := t.ReportState.Results[params.ResultID]
		if !ok {
			return toolFailure(t.Name(), "result_not_found", "analysis result does not exist", map[string]interface{}{"result_id": params.ResultID}), nil
		}
		return toolSuccess(t.Name(), map[string]interface{}{"result": result, "ui_summary": "已加载分析结果：" + params.ResultID}), nil
	}
	if params.ArtifactID != "" {
		artifact, ok := t.ReportState.Artifacts[params.ArtifactID]
		if !ok {
			return toolFailure(t.Name(), "artifact_not_found", "artifact does not exist", map[string]interface{}{"artifact_id": params.ArtifactID}), nil
		}
		return toolSuccess(t.Name(), map[string]interface{}{"artifact": artifact, "ui_summary": "已加载分析产物：" + params.ArtifactID}), nil
	}

	type resultIndex struct {
		ID        string `json:"id"`
		ToolName  string `json:"tool_name"`
		Operation string `json:"operation"`
		RowCount  int    `json:"row_count"`
		CreatedAt string `json:"created_at"`
	}
	results := make([]resultIndex, 0, len(t.ReportState.Results))
	for _, result := range t.ReportState.Results {
		results = append(results, resultIndex{ID: result.ID, ToolName: result.ToolName, Operation: result.Operation, RowCount: result.RowCount, CreatedAt: result.CreatedAt})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	artifacts := make([]ArtifactRecord, 0, len(t.ReportState.Artifacts))
	for _, artifact := range t.ReportState.Artifacts {
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	return toolSuccess(t.Name(), map[string]interface{}{
		"result_count": len(results), "results": results,
		"artifact_count": len(artifacts), "artifacts": artifacts,
		"ui_summary": fmt.Sprintf("分析台账包含 %d 个结果和 %d 个产物。", len(results), len(artifacts)),
	}), nil
}
