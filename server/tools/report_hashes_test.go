package tools

import (
	"encoding/json"
	"testing"
)

func hashedState() *ReportState {
	return &ReportState{
		Blocks: []ReportBlock{
			{ID: "b1", Kind: "markdown", Title: "结论", Content: "文本 {{chart:c1}}"},
			{ID: "b2", Kind: "html", Title: "片段", Content: "<div>片段</div>"},
		},
		Charts: []ChartData{
			{ID: "c1", Option: json.RawMessage(`{"series":[{"type":"bar","data":[1,2]}]}`)},
		},
		Layout: ReportLayout{CustomCSS: ".a{color:red}", BodyClass: "report"},
	}
}

func TestBlockContentHashesAreStableAndPerBlock(t *testing.T) {
	state := hashedState()
	first := state.BlockContentHashes()
	second := state.BlockContentHashes()

	if len(first) != 2 {
		t.Fatalf("expected one hash per block, got %d", len(first))
	}
	if first["b1"] == "" || first["b2"] == "" {
		t.Fatalf("missing hashes: %+v", first)
	}
	if first["b1"] == first["b2"] {
		t.Fatal("different blocks must not share a hash")
	}
	for id, hash := range first {
		if second[id] != hash {
			t.Fatalf("hash for %s is not stable across calls", id)
		}
	}
}

// The hash exists to answer "did this block's pixels change". Anything that can
// change them has to move it, and unrelated edits must not.
func TestBlockContentHashesTrackPixelAffectingChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ReportState)
	}{
		{"block content", func(s *ReportState) { s.Blocks[0].Content = "改过的文本 {{chart:c1}}" }},
		{"chart option", func(s *ReportState) { s.Charts[0].Option = json.RawMessage(`{"series":[]}`) }},
		{"layout css", func(s *ReportState) { s.Layout.CustomCSS = ".a{color:blue}" }},
		{"block title", func(s *ReportState) { s.Blocks[0].Title = "新标题" }},
	}
	for _, tc := range cases {
		state := hashedState()
		before := state.BlockContentHashes()
		tc.mutate(state)
		after := state.BlockContentHashes()
		if before["b1"] == after["b1"] {
			t.Errorf("%s did not change the block hash", tc.name)
		}
	}

	// Editing an unrelated block must leave the other block's hash alone, or
	// dirty-block capture degenerates into full re-capture.
	state := hashedState()
	before := state.BlockContentHashes()
	state.Blocks[1].Content = "<div>换过的片段</div>"
	after := state.BlockContentHashes()
	if before["b1"] != after["b1"] {
		t.Fatal("unrelated block edit invalidated a different block")
	}
	if before["b2"] == after["b2"] {
		t.Fatal("edited block kept its old hash")
	}
}

func TestBlockContentHashesHandlesEmptyState(t *testing.T) {
	var nilState *ReportState
	if hashes := nilState.BlockContentHashes(); hashes != nil {
		t.Fatalf("nil state must produce no hashes, got %+v", hashes)
	}
	if hashes := (&ReportState{}).BlockContentHashes(); len(hashes) != 0 {
		t.Fatalf("empty state must produce no hashes, got %+v", hashes)
	}
}
