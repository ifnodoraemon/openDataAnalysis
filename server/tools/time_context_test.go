package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/service"
)

func TestInspectTimeContextToolReturnsClockAndImportFactsOnly(t *testing.T) {
	t.Parallel()

	loc := time.FixedZone("CST", 8*60*60)
	tool := &InspectTimeContextTool{
		Now: func() time.Time {
			return time.Date(2026, 4, 23, 9, 30, 0, 0, loc)
		},
		SessionSourcesProvider: func(ctx context.Context) ([]service.SessionSourceSummary, error) {
			return []service.SessionSourceSummary{
				{
					SourceID:          "src_1",
					DisplayName:       "销售数据",
					SourceType:        "file",
					AnalysisTableName: "sales",
					ProfileID:         "profile_1",
					LastImportedAt:    time.Date(2026, 4, 22, 18, 0, 0, 0, time.UTC),
				},
			}, nil
		},
	}
	tool.SetExecutionContext(context.Background())

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var payload struct {
		OK          bool   `json:"ok"`
		CurrentDate string `json:"current_date"`
		Sources     []struct {
			AnalysisTableName string `json:"analysis_table_name"`
			LastImportedAt    string `json:"last_imported_at"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !payload.OK || payload.CurrentDate != "2026-04-23" {
		t.Fatalf("unexpected current date payload: %#v", payload)
	}
	if len(payload.Sources) != 1 {
		t.Fatalf("unexpected source facts: %#v", payload.Sources)
	}
	period := payload.Sources[0]
	if period.AnalysisTableName != "sales" {
		t.Fatalf("unexpected source time fact: %#v", period)
	}
	if period.LastImportedAt == "" {
		t.Fatalf("expected exact import timestamp fact: %#v", period)
	}
}

func TestInspectTimeContextToolKeepsInjectedTimezoneDate(t *testing.T) {
	t.Parallel()

	loc := time.FixedZone("CST", 8*60*60)
	tool := &InspectTimeContextTool{
		Now: func() time.Time {
			return time.Date(2026, 4, 23, 0, 30, 0, 0, loc)
		},
	}
	tool.SetExecutionContext(context.Background())

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["current_date"] != "2026-04-23" || payload["timezone_offset_seconds"] != float64(8*60*60) {
		t.Fatalf("unexpected timezone payload: %#v", payload)
	}
}

func TestInspectTimeContextToolWorksWithoutSources(t *testing.T) {
	t.Parallel()

	tool := &InspectTimeContextTool{
		Now: func() time.Time {
			return time.Date(2026, 4, 23, 1, 2, 3, 0, time.UTC)
		},
	}
	tool.SetExecutionContext(context.Background())

	result, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["current_date"] != "2026-04-23" || payload["source_count"] != float64(0) {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}
