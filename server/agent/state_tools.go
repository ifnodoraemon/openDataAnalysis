package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func init() {
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Subgoals == nil {
			return nil
		}
		manager, ok := ctx.Subgoals.(*SubgoalManager)
		if !ok {
			panic("tool context subgoals has an invalid type")
		}
		return &InspectGoalsTool{Subgoals: manager}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.ReportState == nil {
			return nil
		}
		return &InspectReportStateTool{ReportState: ctx.ReportState}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.EditState == nil {
			return nil
		}
		return &InspectReportEditStateTool{
			EditState:   ctx.EditState,
			ReportState: ctx.ReportState,
		}
	})
}

type InspectGoalsTool struct {
	Subgoals *SubgoalManager
}

func (t *InspectGoalsTool) Name() string {
	return "state_goal_inspect"
}
func (t *InspectGoalsTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectGoalsTool) Description() string {
	return "Read the fact state of the goal tree. Returns goal count, status distribution, active root goals, and active branches blocking closure; does not modify any state."
}

func (t *InspectGoalsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectGoalsTool) Execute(args json.RawMessage) (string, error) {
	if err := tools.ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.Subgoals == nil {
		return "", fmt.Errorf("subgoals is not initialized")
	}

	payload := buildGoalStateFacts(t.Subgoals, true)
	payload["ok"] = true
	payload["tool"] = "state_goal_inspect"
	payload["ui_summary"] = fmt.Sprintf("当前共有 %d 个目标，其中 %d 个活动分支。", payload["goal_count"], payload["active_branch_count"])

	return marshalToolPayload(payload)
}

type InspectReportStateTool struct {
	ReportState *tools.ReportState
}

type InspectReportEditStateTool struct {
	EditState   *tools.ReportEditState
	ReportState *tools.ReportState
}

func (t *InspectReportStateTool) Name() string {
	return "state_report_inspect"
}
func (t *InspectReportStateTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectReportStateTool) Description() string {
	return "Read the fact view of the current report state. Returns blocks, charts, reference relationships, delivery_state, and finalize completeness counts. Block ids and titles are the stable references for addressing existing sections when the user asks to revise a specific part. Does not modify any state."
}

func (t *InspectReportStateTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectReportStateTool) Execute(args json.RawMessage) (string, error) {
	if err := tools.ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.ReportState == nil {
		return "", fmt.Errorf("report state is not initialized")
	}

	t.ReportState.RLock()

	type reportBlockSnapshot struct {
		ID         string   `json:"id"`
		Kind       string   `json:"kind"`
		Title      string   `json:"title,omitempty"`
		ChartID    string   `json:"chart_id,omitempty"`
		ChartRefs  []string `json:"chart_refs,omitempty"`
		HasContent bool     `json:"has_content"`
	}

	blocks := make([]reportBlockSnapshot, 0, len(t.ReportState.Blocks))
	referenceCounts := make(map[string]int)
	for _, block := range t.ReportState.Blocks {
		refs := tools.ChartReferences(block.Content)
		if block.Kind == "chart" && block.ChartID != "" {
			refs = append(refs, block.ChartID)
		}
		for _, ref := range refs {
			referenceCounts[ref]++
		}
		blocks = append(blocks, reportBlockSnapshot{
			ID:         block.ID,
			Kind:       block.Kind,
			Title:      block.Title,
			ChartID:    block.ChartID,
			ChartRefs:  refs,
			HasContent: strings.TrimSpace(block.Content) != "",
		})
	}

	chartIDs := make([]string, 0, len(t.ReportState.Charts))
	chartSet := make(map[string]struct{}, len(t.ReportState.Charts))
	for _, chart := range t.ReportState.Charts {
		chartID := chart.ID
		if chartID == "" {
			continue
		}
		chartIDs = append(chartIDs, chartID)
		chartSet[chartID] = struct{}{}
	}
	sort.Strings(chartIDs)

	var unreferencedCharts []string
	for _, chartID := range chartIDs {
		if referenceCounts[chartID] == 0 {
			unreferencedCharts = append(unreferencedCharts, chartID)
		}
	}

	var missingChartRefs []string
	for chartID := range referenceCounts {
		if _, ok := chartSet[chartID]; !ok {
			missingChartRefs = append(missingChartRefs, chartID)
		}
	}
	sort.Strings(missingChartRefs)

	duplicated := make(map[string]int)
	for chartID, count := range referenceCounts {
		if count > 1 {
			duplicated[chartID] = count
		}
	}

	delivery := tools.DescribeReportDeliveryStateLocked(t.ReportState)
	finalizeIssues := tools.ReportFinalizeIssuesForAgentLocked(t.ReportState)
	renderableBlockCount := tools.RenderableReportBlockCountLocked(t.ReportState)
	blockCount := len(t.ReportState.Blocks)

	t.ReportState.RUnlock()

	payload := map[string]interface{}{
		"ok":                       true,
		"tool":                     "state_report_inspect",
		"block_count":              blockCount,
		"renderable_block_count":   renderableBlockCount,
		"chart_count":              len(chartIDs),
		"blocks":                   blocks,
		"chart_ids":                chartIDs,
		"chart_reference_counts":   referenceCounts,
		"unreferenced_charts":      unreferencedCharts,
		"missing_chart_references": missingChartRefs,
		"duplicated_chart_refs":    duplicated,
		"has_content":              delivery.HasContent,
		"delivery_state":           delivery.DeliveryState,
		"is_finalized":             delivery.IsFinalized,
		"needs_finalize":           delivery.NeedsFinalize,
		"final_title":              delivery.FinalTitle,
		"final_author":             delivery.FinalAuthor,
		"finalize_issue_count":     len(finalizeIssues),
		"finalize_issues":          finalizeIssues,
		"ui_summary":               fmt.Sprintf("当前报告包含 %d 个可渲染内容块和 %d 个图表。", renderableBlockCount, len(chartIDs)),
	}
	return marshalToolPayload(payload)
}

func (t *InspectReportEditStateTool) Name() string {
	return "state_report_edit_inspect"
}
func (t *InspectReportEditStateTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectReportEditStateTool) Description() string {
	return "Read the fact state of the current report edit scope. Returns whether the active scope is whole-report, partial-block, partial-selection, partial-chart, or layout, plus grounded target facts and selection anchors when applicable. Does not modify any state."
}

func (t *InspectReportEditStateTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectReportEditStateTool) Execute(args json.RawMessage) (string, error) {
	if err := tools.ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.EditState == nil {
		return "", fmt.Errorf("report edit state is not initialized")
	}
	payload := t.EditState.Snapshot()
	if t.ReportState != nil && t.EditState.Active() && t.EditState.TargetBlockID != "" {
		t.ReportState.RLock()
		if block, ok := findEditTargetBlock(t.ReportState, t.EditState.TargetBlockID); ok {
			payload["target_block"] = map[string]interface{}{
				"id":       block.ID,
				"kind":     block.Kind,
				"title":    block.Title,
				"chart_id": block.ChartID,
				"content":  block.Content,
			}
		}
		if chart, ok := findEditTargetChart(t.ReportState, t.EditState.TargetChartID); ok {
			payload["target_chart"] = map[string]interface{}{
				"id":     chart.ID,
				"width":  chart.Width,
				"height": chart.Height,
				"option": chart.Option,
			}
		}
		t.ReportState.RUnlock()
	} else if t.ReportState != nil && t.EditState.Active() && t.EditState.TargetChartID != "" {
		t.ReportState.RLock()
		if chart, ok := findEditTargetChart(t.ReportState, t.EditState.TargetChartID); ok {
			payload["target_chart"] = map[string]interface{}{
				"id":     chart.ID,
				"width":  chart.Width,
				"height": chart.Height,
				"option": chart.Option,
			}
		}
		t.ReportState.RUnlock()
	}
	payload["ok"] = true
	payload["tool"] = "state_report_edit_inspect"
	if active, _ := payload["active"].(bool); active {
		switch t.EditState.ScopeKind() {
		case "whole_report":
			payload["ui_summary"] = "当前正在编辑整份报告。"
		case "layout":
			payload["ui_summary"] = "当前正在编辑报告布局。"
		case "partial_chart":
			payload["ui_summary"] = fmt.Sprintf("当前正在局部编辑图表 %s。", t.EditState.TargetChartID)
		case "partial_selection":
			if t.EditState.SelectionRangeSet && t.EditState.SelectionEnd > t.EditState.SelectionStart {
				payload["ui_summary"] = fmt.Sprintf("当前正在局部编辑内容块 %s 的 %d-%d 区间。", t.EditState.TargetBlockID, t.EditState.SelectionStart, t.EditState.SelectionEnd)
			} else {
				payload["ui_summary"] = fmt.Sprintf("当前正在局部编辑内容块 %s。", t.EditState.TargetBlockID)
			}
		default:
			payload["ui_summary"] = fmt.Sprintf("当前正在局部编辑内容块 %s。", t.EditState.TargetBlockID)
		}
	} else {
		payload["ui_summary"] = "当前没有活动的报告编辑范围。"
	}
	return marshalToolPayload(payload)
}

func findEditTargetBlock(state *tools.ReportState, blockID string) (tools.ReportBlock, bool) {
	if state == nil {
		return tools.ReportBlock{}, false
	}
	for _, block := range state.Blocks {
		if block.ID == blockID {
			return block, true
		}
	}
	return tools.ReportBlock{}, false
}

func findEditTargetChart(state *tools.ReportState, chartID string) (tools.ChartData, bool) {
	if state == nil {
		return tools.ChartData{}, false
	}
	for _, chart := range state.Charts {
		if chart.ID == chartID {
			return chart, true
		}
	}
	return tools.ChartData{}, false
}

func marshalToolPayload(payload map[string]interface{}) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
