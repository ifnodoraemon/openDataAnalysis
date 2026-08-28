package agent

import (
	"encoding/json"
	"testing"
)

func TestReportEditContextRequiresExplicitSelectionRangeSet(t *testing.T) {
	t.Parallel()

	var withoutFlag ReportEditContext
	if err := json.Unmarshal([]byte(`{"selectionText":"收入","selectionStart":0,"selectionEnd":2}`), &withoutFlag); err != nil {
		t.Fatalf("unmarshal without flag: %v", err)
	}
	if withoutFlag.SelectionRangeSet {
		t.Fatal("expected selectionRangeSet to remain false when the field is omitted")
	}

	var withFlag ReportEditContext
	if err := json.Unmarshal([]byte(`{"selectionText":"收入","selectionStart":0,"selectionEnd":2,"selectionRangeSet":true}`), &withFlag); err != nil {
		t.Fatalf("unmarshal with flag: %v", err)
	}
	if !withFlag.SelectionRangeSet {
		t.Fatal("expected explicit selectionRangeSet=true to be preserved")
	}
}

func TestReportEditContextValidateRequiresScopeTargets(t *testing.T) {
	t.Parallel()

	invalid := []*ReportEditContext{
		{},
		{ScopeKind: "partial_block"},
		{ScopeKind: "partial_chart"},
		{ScopeKind: "partial_selection", BlockID: "b1", SelectionText: "text"},
	}
	for _, edit := range invalid {
		if err := edit.Validate(); err == nil {
			t.Fatalf("expected invalid edit context to fail: %#v", edit)
		}
	}
	valid := &ReportEditContext{ScopeKind: "partial_selection", BlockID: "b1", SelectionText: "text", SelectionStart: 0, SelectionEnd: 4, SelectionRangeSet: true}
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected explicit selection scope to pass: %v", err)
	}
}
