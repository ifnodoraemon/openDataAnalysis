package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

// useTempSpillDir points the spill store at a test directory.
func useTempSpillDir(t *testing.T) {
	t.Helper()
	previous := config.Cfg
	config.Cfg = &config.Config{TempDir: t.TempDir()}
	t.Cleanup(func() { config.Cfg = previous })
}

func TestSpillToolResultRoundTrips(t *testing.T) {
	useTempSpillDir(t)

	content := `{"ok":true,"tool":"data_query_sql","rows":["` + strings.Repeat("数", 500) + `"]}`
	id, err := spillToolResult(content)
	if err != nil {
		t.Fatalf("spillToolResult: %v", err)
	}
	// Content addressing keeps the id stable across runs and rebuilds.
	again, err := spillToolResult(content)
	if err != nil {
		t.Fatalf("second spillToolResult: %v", err)
	}
	if id != again {
		t.Fatalf("expected a content-addressed id, got %q and %q", id, again)
	}

	window, total, err := readSpilledToolResult(id, 0, 100)
	if err != nil {
		t.Fatalf("readSpilledToolResult: %v", err)
	}
	if total != len([]rune(content)) {
		t.Fatalf("expected total %d, got %d", len([]rune(content)), total)
	}
	if len([]rune(window)) != 100 {
		t.Fatalf("expected a 100 rune window, got %d", len([]rune(window)))
	}

	tail, _, err := readSpilledToolResult(id, total-10, 100)
	if err != nil {
		t.Fatalf("readSpilledToolResult tail: %v", err)
	}
	if !strings.HasSuffix(content, tail) {
		t.Fatalf("expected the tail window to match the payload end")
	}
}

func TestSpillRejectsPathLikeIDs(t *testing.T) {
	useTempSpillDir(t)

	for _, id := range []string{"../secrets", "ABC", strings.Repeat("a", 31), strings.Repeat("a", 33)} {
		if _, _, err := readSpilledToolResult(id, 0, 10); err == nil {
			t.Fatalf("expected %q to be rejected", id)
		}
	}
}

// A pruned result must point at the full payload instead of losing it.
func TestPrunedResultCarriesSpillLocator(t *testing.T) {
	useTempSpillDir(t)

	original := `{"ok":true,"tool":"data_query_sql","rows":["` + strings.Repeat("x", 20000) + `"]}`
	pruned := pruneToolResultForHistory("data_query_sql", original)

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(pruned), &payload); err != nil {
		t.Fatalf("pruned result is not JSON: %v", err)
	}
	id, ok := payload["full_result_id"].(string)
	if !ok || !validSpillID(id) {
		t.Fatalf("expected a full_result_id, got %#v", payload["full_result_id"])
	}
	stored, total, err := readSpilledToolResult(id, 0, 50)
	if err != nil {
		t.Fatalf("readSpilledToolResult: %v", err)
	}
	if total != len([]rune(original)) {
		t.Fatalf("expected the full payload to be stored, got %d runes", total)
	}
	if !strings.HasPrefix(stored, `{"ok":true`) {
		t.Fatalf("unexpected stored prefix %q", stored)
	}
}
