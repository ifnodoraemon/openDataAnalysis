package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		return &InspectTimeContextTool{
			SessionSourcesProvider: ctx.SessionSourcesProvider,
			Now:                    ctx.Now,
		}
	})
}

type InspectTimeContextTool struct {
	SessionSourcesProvider SessionSourcesProvider
	Now                    func() time.Time
	parentCtx              context.Context
}

func (t *InspectTimeContextTool) SetExecutionContext(ctx context.Context) { t.parentCtx = ctx }

func (t *InspectTimeContextTool) Name() string { return "state_time_context_inspect" }
func (t *InspectTimeContextTool) Capability() ToolCapability {
	return ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectTimeContextTool) Description() string {
	return "Read current wall-clock facts and operational import timestamps for current session sources. It does not infer time columns, data periods, grains, or field meanings."
}

func (t *InspectTimeContextTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}

func (t *InspectTimeContextTool) Execute(args json.RawMessage) (string, error) {
	if err := ValidateNoArgs(args); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if t.parentCtx == nil {
		return "", fmt.Errorf("tool execution context is not initialized")
	}
	if t.Now == nil {
		return "", fmt.Errorf("wall-clock provider is not initialized")
	}
	now := t.Now()
	current := now
	zoneName, zoneOffset := current.Zone()

	sourceFacts := []map[string]interface{}{}
	sourceCount := 0
	if t.SessionSourcesProvider != nil {
		sources, err := t.SessionSourcesProvider(t.parentCtx)
		if err != nil {
			return "", err
		}
		sourceCount = len(sources)
		for _, source := range sources {
			item := map[string]interface{}{
				"source_id":           source.SourceID,
				"display_name":        source.DisplayName,
				"source_type":         source.SourceType,
				"analysis_table_name": source.AnalysisTableName,
			}
			if !source.LastImportedAt.IsZero() {
				item["last_imported_at"] = source.LastImportedAt.Format(time.RFC3339)
			}
			sourceFacts = append(sourceFacts, item)
		}
	}

	payload := map[string]interface{}{
		"current_date":            current.Format("2006-01-02"),
		"current_datetime":        current.Format(time.RFC3339),
		"timezone":                current.Location().String(),
		"timezone_abbreviation":   zoneName,
		"timezone_offset_seconds": zoneOffset,
		"source_count":            sourceCount,
		"sources":                 sourceFacts,
		"ui_summary":              fmt.Sprintf("当前日期：%s；会话数据源：%d 个", current.Format("2006-01-02"), sourceCount),
	}
	return toolSuccess("state_time_context_inspect", payload), nil
}
