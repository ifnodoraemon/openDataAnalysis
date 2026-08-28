package tools

import (
	"encoding/json"
	"testing"
)

func TestApplyReportBlockMutationUsesExactSourcesOnUpsert(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "summary",
				Kind:    "markdown",
				Title:   "摘要",
				Content: "原内容",
				Sources: []EvidenceRef{
					{Kind: "result", ResultID: "res_1"},
				},
			},
		},
	}

	result, err := applyReportBlockMutation(state, nil, reportBlockMutationParams{
		Action:    "upsert",
		BlockID:   "summary",
		BlockKind: "markdown",
		Title:     "摘要",
		Content:   "新内容",
	})
	if err != nil {
		t.Fatalf("applyReportBlockMutation: %v", err)
	}

	if result.BlockID != "summary" || result.BlockKind != "markdown" {
		t.Fatalf("unexpected mutation result: %#v", result)
	}
	if len(state.Blocks) != 1 || state.Blocks[0].Content != "新内容" {
		t.Fatalf("expected block to be updated in place, got %#v", state.Blocks)
	}
	if len(state.Blocks[0].Sources) != 0 {
		t.Fatalf("expected omitted sources to remain omitted, got %#v", state.Blocks[0].Sources)
	}
	if !state.NeedsFinalize {
		t.Fatal("expected mutation to mark report as needing finalize")
	}
}

func TestApplyReportBlockMutationPartialSelectionKeepsMetadata(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "summary",
				Kind:    "markdown",
				Title:   "摘要",
				Content: "前文。需要改写的句子。后文。",
				Sources: []EvidenceRef{{Kind: "result", ResultID: "res_1"}},
			},
		},
	}
	editState := &ReportEditState{
		ScopeKindValue:    "partial_selection",
		TargetBlockID:     "summary",
		SelectionText:     "需要改写的句子",
		SelectionStart:    3,
		SelectionEnd:      10,
		SelectionRangeSet: true,
	}
	editState.RefreshFromReportState(state)

	_, err := applyReportBlockMutation(state, editState, reportBlockMutationParams{
		Action:    "upsert",
		BlockID:   "summary",
		BlockKind: "markdown",
		Title:     "改名",
		Content:   "前文。新的句子。后文。",
	})
	if err == nil {
		t.Fatal("expected title mutation outside partial selection to be rejected")
	}

	result, err := applyReportBlockMutation(state, editState, reportBlockMutationParams{
		Action:    "upsert",
		BlockID:   "summary",
		BlockKind: "markdown",
		Title:     "摘要",
		Content:   "前文。新的句子。后文。",
		Sources:   []EvidenceRef{{Kind: "result", ResultID: "res_1"}},
	})
	if err != nil {
		t.Fatalf("expected content-only selection mutation to succeed: %v", err)
	}
	if result.BlockID != "summary" || state.Blocks[0].Content != "前文。新的句子。后文。" {
		t.Fatalf("unexpected mutation result=%#v state=%#v", result, state.Blocks)
	}
	if len(state.Blocks[0].Sources) != 1 || state.Blocks[0].Sources[0].ResultID != "res_1" {
		t.Fatalf("expected existing sources to be preserved before scope check, got %#v", state.Blocks[0].Sources)
	}
}

func TestManageReportBlocksScopeFailureReturnsTargetFacts(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "summary", Kind: "markdown", Title: "摘要", Content: "前文。需要改写的句子。后文。"},
		},
	}
	editState := &ReportEditState{
		ScopeKindValue:    "partial_selection",
		TargetBlockID:     "summary",
		SelectionText:     "需要改写的句子",
		SelectionStart:    3,
		SelectionEnd:      10,
		SelectionRangeSet: true,
	}
	editState.RefreshFromReportState(state)

	tool := &ManageReportBlocksTool{ReportState: state, EditState: editState}
	result, err := tool.Execute(json.RawMessage(`{
		"action":"append",
		"block_id":"new-section",
		"block_kind":"markdown",
		"title":"新段落",
		"content":"新的句子。"
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["ok"] != false || payload["error_code"] != "edit_scope_violation" {
		t.Fatalf("unexpected scope payload: %#v", payload)
	}
	actions, ok := payload["allowed_block_actions"].([]interface{})
	if payload["scope_kind"] != "partial_selection" || payload["target_block_id"] != "summary" || !ok || len(actions) != 1 || actions[0] != "upsert" {
		t.Fatalf("expected target selection facts in payload: %#v", payload)
	}
}

func TestManageReportBlocksToolRejectsStringifiedSources(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	tool := &ManageReportBlocksTool{ReportState: state}
	_, err := tool.Execute(json.RawMessage(`{
		"action":"append",
		"block_id":"summary",
		"block_kind":"markdown",
		"title":"摘要",
		"content":"结论内容",
		"sources":"[{\"kind\":\"result\",\"result_id\":\"res_1\",\"label\":\"测试查询\"}]"
	}`))
	if err == nil {
		t.Fatal("expected exact array contract to reject stringified sources")
	}
}

func TestUpsertReportBlockFailureLeavesExistingBlockUnchanged(t *testing.T) {
	t.Parallel()

	state := &ReportState{Blocks: []ReportBlock{{ID: "summary", Kind: "markdown", Title: "Original", Content: "original content"}}}
	_, err := applyReportBlockMutation(state, nil, reportBlockMutationParams{
		Action: "upsert", BlockID: "summary", BlockKind: "markdown", Title: "Replacement", Content: "replacement content", BeforeBlockID: "missing",
	})
	if err == nil {
		t.Fatal("expected invalid insertion reference to fail")
	}
	if len(state.Blocks) != 1 || state.Blocks[0].Title != "Original" || state.Blocks[0].Content != "original content" || state.MutationVersion != 0 {
		t.Fatalf("failed upsert mutated report state: %#v", state)
	}
}

func TestReportBlockMutationRejectsWhitespaceAliases(t *testing.T) {
	t.Parallel()

	state := &ReportState{}
	for _, params := range []reportBlockMutationParams{
		{Action: " append", BlockID: "summary", BlockKind: "markdown", Content: "content"},
		{Action: "append", BlockID: "summary ", BlockKind: "markdown", Content: "content"},
		{Action: "append", BlockID: "summary", BlockKind: "Markdown", Content: "content"},
	} {
		if _, err := applyReportBlockMutation(state, nil, params); err == nil {
			t.Fatalf("expected exact mutation contract to reject %#v", params)
		}
	}
	if len(state.Blocks) != 0 {
		t.Fatalf("rejected mutations changed state: %#v", state.Blocks)
	}
}
