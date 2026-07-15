package agent

import (
	"encoding/json"
	"testing"
)

func TestCompactQueryResult_SmallResult(t *testing.T) {
	// 小于阈值的结果应该只做 stripHistorySummaryFields，不截断
	payload := map[string]interface{}{
		"ok":         true,
		"tool":       "data_query_sql",
		"sql":        "SELECT * FROM t",
		"row_count":  3,
		"columns":    []interface{}{"a", "b"},
		"rows":       []interface{}{map[string]interface{}{"a": 1.0, "b": "x"}},
		"ui_summary": "should be removed",
	}

	result := compactQueryResult(payload)

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// ui_summary should be stripped
	if _, ok := decoded["ui_summary"]; ok {
		t.Fatal("expected ui_summary to be stripped")
	}
	// _truncated should not be set
	if _, ok := decoded["_truncated"]; ok {
		t.Fatal("small result should not be truncated")
	}
}

func TestCompactQueryResult_LargeResult(t *testing.T) {
	// 生成 50 行结果（超过 queryCompactRowThreshold=20）
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

	result := compactQueryResult(payload)

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 应截断到 queryCompactKeepRows (10) 行
	resultRows := decoded["rows"].([]interface{})
	if len(resultRows) != queryCompactKeepRows {
		t.Fatalf("expected rows truncated to %d, got %d", queryCompactKeepRows, len(resultRows))
	}

	// 应标记 _truncated
	if decoded["_truncated"] != true {
		t.Fatal("expected _truncated=true for large result")
	}

	// 应有 _row_count 记录原始行数
	if decoded["_row_count"] != float64(50) {
		t.Fatalf("expected _row_count=50, got %v", decoded["_row_count"])
	}

	// 应有 column_stats
	stats, ok := decoded["column_stats"].(map[string]interface{})
	if !ok {
		t.Fatal("expected column_stats")
	}
	// id and value 是数值列，应有统计
	if _, ok := stats["id"]; !ok {
		t.Fatal("expected stats for 'id' column")
	}
	if _, ok := stats["value"]; !ok {
		t.Fatal("expected stats for 'value' column")
	}
	// name 不是数值列，不应有统计
	if _, ok := stats["name"]; ok {
		t.Fatal("name column (string) should not have stats")
	}

	// ui_summary 应被删除
	if _, ok := decoded["ui_summary"]; ok {
		t.Fatal("expected ui_summary to be stripped")
	}
}

func TestBuildColumnStats(t *testing.T) {
	columns := []interface{}{"revenue", "city"}
	rows := []interface{}{
		map[string]interface{}{"revenue": 100.0, "city": "Beijing"},
		map[string]interface{}{"revenue": 200.0, "city": "Shanghai"},
		map[string]interface{}{"revenue": 50.0, "city": "Guangzhou"},
	}

	stats := buildColumnStats(columns, rows)

	revStats, ok := stats["revenue"].(map[string]interface{})
	if !ok {
		t.Fatal("expected revenue stats")
	}
	if revStats["min"] != 50.0 {
		t.Fatalf("expected min=50, got %v", revStats["min"])
	}
	if revStats["max"] != 200.0 {
		t.Fatalf("expected max=200, got %v", revStats["max"])
	}
	if revStats["count"] != 3 {
		t.Fatalf("expected count=3, got %v", revStats["count"])
	}

	// city 是字符串列，不应有统计
	if _, ok := stats["city"]; ok {
		t.Fatal("city (string) should not have stats")
	}
}
