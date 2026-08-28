package agent

import (
	"encoding/json"
	"testing"
)

func TestCompactToolResultPreservesBoundedQueryFacts(t *testing.T) {
	rows := make([]interface{}, 50)
	for i := range rows {
		rows[i] = map[string]interface{}{
			"id":    float64(i),
			"value": float64(i * 10),
			"name":  "row",
		}
	}

	payload := map[string]interface{}{
		"ok":         true,
		"tool":       "data_query_sql",
		"sql":        "SELECT * FROM big_table",
		"row_count":  50,
		"columns":    []interface{}{"id", "value", "name"},
		"rows":       rows,
		"ui_summary": "should be removed",
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := compactToolResult("data_query_sql", string(raw))
	if err != nil {
		t.Fatalf("compact: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resultRows := decoded["rows"].([]interface{})
	if len(resultRows) != len(rows) {
		t.Fatalf("expected all %d bounded rows, got %d", len(rows), len(resultRows))
	}
	if _, ok := decoded["ui_summary"]; ok {
		t.Fatal("expected ui_summary to be stripped")
	}
	for _, field := range []string{"_truncated", "_row_count", "_original_row_count", "column_stats", "_note"} {
		if _, ok := decoded[field]; ok {
			t.Fatalf("runtime must not synthesize %s", field)
		}
	}
}
