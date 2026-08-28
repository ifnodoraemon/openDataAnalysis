package tools

import "testing"

func TestFinalizeReportStateDefaultsAuthor(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "summary", Kind: "markdown", Content: "结论"},
		},
	}

	result, err := finalizeReportState(state, nil, reportFinalizeParams{
		ReportTitle: "销售分析",
	})
	if err != nil {
		t.Fatalf("finalizeReportState: %v", err)
	}
	if result.Author != "" {
		t.Fatalf("expected empty author, got %#v", result)
	}
	if state.FinalAuthor != "" || state.NeedsFinalize {
		t.Fatalf("expected state to be finalized with empty author, got %#v", state)
	}
}

func TestFinalizeReportStateRejectsDuplicateFinalize(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "summary", Kind: "markdown", Content: "结论"},
		},
		FinalTitle: "已完成报告",
	}

	if _, err := finalizeReportState(state, nil, reportFinalizeParams{
		ReportTitle: "再次 finalize",
	}); err == nil {
		t.Fatal("expected duplicate finalize to be rejected")
	} else {
		if _, ok := err.(reportAlreadyFinalizedError); !ok {
			t.Fatalf("expected reportAlreadyFinalizedError, got %T %v", err, err)
		}
	}
}
