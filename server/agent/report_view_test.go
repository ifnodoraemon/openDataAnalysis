package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func testSlices(count int) []ReportViewSlice {
	slices := make([]ReportViewSlice, 0, count)
	for i := 0; i < count; i++ {
		slices = append(slices, ReportViewSlice{
			BlockID: "blk_" + string(rune('a'+i%26)),
			Kind:    "markdown",
			Part:    "1/1",
			Width:   1200,
			Height:  960,
			PNG:     []byte{0x89, 'P', 'N', 'G', byte(i)},
		})
	}
	return slices
}

func testReportView(sliceCount int) *ReportView {
	return &ReportView{
		ReportVersion: 17,
		CapturedAt:    "finalize_precheck",
		ViewportWidth: 1200,
		ViewportDPR:   1,
		Facts:         json.RawMessage(`{"charts_mounted":6,"katex_errors":0}`),
		Slices:        testSlices(sliceCount),
	}
}

func TestReportViewSkipsMissingOrStaleView(t *testing.T) {
	if blocks := (*ReportView)(nil).RuntimeContextBlocks(); blocks != nil {
		t.Fatalf("nil view must produce no blocks, got %d", len(blocks))
	}
	stale := &ReportView{ReportVersion: 0, Slices: testSlices(2)}
	if blocks := stale.RuntimeContextBlocks(); blocks != nil {
		t.Fatalf("unstamped view must produce no blocks, got %d", len(blocks))
	}
	empty := &ReportView{ReportVersion: 3}
	if blocks := empty.RuntimeContextBlocks(); blocks != nil {
		t.Fatalf("view with no slices and no failures must produce no blocks, got %d", len(blocks))
	}
}

func TestReportViewCarriesVersionStampAndFacts(t *testing.T) {
	view := testReportView(2)
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(blocks))
	}
	block := blocks[0]
	if block.Name != reportViewBlockName || block.Role != LLMRoleUser {
		t.Fatalf("unexpected block identity: %+v", block)
	}
	for _, want := range []string{`"report_version":17`, `"captured_at":"finalize_precheck"`, `"charts_mounted":6`, `"block_id":"blk_a"`} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("fact payload missing %s: %s", want, block.Content)
		}
	}
	if len(block.Parts) != 2 {
		t.Fatalf("expected two image parts, got %d", len(block.Parts))
	}
	for _, part := range block.Parts {
		if part.Kind != MessagePartImage || part.MIME != "image/png" || part.W != 1200 || part.H != 960 {
			t.Fatalf("unexpected image part: %+v", part)
		}
	}
}

// TestReportViewChunksLongReports guards the payload bound: a 45-slice report
// would otherwise build a single block that exceeds maxMessageImages.
func TestReportViewChunksLongReports(t *testing.T) {
	view := testReportView(45)
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 chunks for 45 slices, got %d", len(blocks))
	}
	total := 0
	for index, block := range blocks {
		images := 0
		for _, part := range block.Parts {
			if part.Kind == MessagePartImage {
				images++
			}
		}
		total += images
		if images > maxReportViewImagesPerBlock {
			t.Fatalf("chunk %d carries %d images, over the %d bound", index, images, maxReportViewImagesPerBlock)
		}
		if err := validateMessageParts(block.Parts); err != nil {
			t.Fatalf("chunk %d is not a valid payload: %v", index, err)
		}
		if index > 0 {
			if !strings.Contains(block.Content, `"continuation":true`) {
				t.Fatalf("chunk %d should be marked as a continuation: %s", index, block.Content)
			}
			if strings.Contains(block.Content, "render_facts") {
				t.Fatalf("chunk %d repeats the render facts: %s", index, block.Content)
			}
		}
	}
	if total != 45 {
		t.Fatalf("expected every slice to survive chunking, got %d", total)
	}
	if !strings.Contains(blocks[0].Content, `"chunk":"1/3"`) {
		t.Fatalf("first chunk should carry its position: %s", blocks[0].Content)
	}
}

// TestReportViewReportsFailedCapture: a capture that produced no image still has
// something to say, and saying it is the point of the facts channel.
func TestReportViewReportsFailedCapture(t *testing.T) {
	view := &ReportView{
		ReportVersion: 4,
		CapturedAt:    "finalize_precheck",
		Facts:         json.RawMessage(`{"ok":false,"reason":"capture_timeout"}`),
		NotCaptured:   []ReportViewSkipped{{BlockID: "blk_9", Reason: "capture_timeout"}},
	}
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected a facts-only block, got %d", len(blocks))
	}
	if len(blocks[0].Parts) != 0 {
		t.Fatalf("facts-only block must carry no images, got %d", len(blocks[0].Parts))
	}
	if !strings.Contains(blocks[0].Content, "capture_timeout") {
		t.Fatalf("failure reason was lost: %s", blocks[0].Content)
	}
	if !strings.Contains(blocks[0].Content, `"block_id":"blk_9"`) {
		t.Fatalf("skipped block was lost: %s", blocks[0].Content)
	}
}

func TestReportViewDropsUnsendableSlices(t *testing.T) {
	view := testReportView(1)
	view.Slices = append(view.Slices,
		ReportViewSlice{BlockID: "no_png", Width: 1200, Height: 960},
		ReportViewSlice{BlockID: "no_size", PNG: []byte{1}},
		ReportViewSlice{BlockID: "too_tall", PNG: []byte{1}, Width: 1200, Height: 9000},
	)
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(blocks))
	}
	if len(blocks[0].Parts) != 1 {
		t.Fatalf("expected only the usable slice, got %d parts", len(blocks[0].Parts))
	}
	for _, unwanted := range []string{"no_png", "no_size", "too_tall"} {
		if strings.Contains(blocks[0].Content, unwanted) {
			t.Fatalf("unsendable slice %q leaked into the payload", unwanted)
		}
	}
}

func TestEngineInjectsReportViewLast(t *testing.T) {
	engine := &Engine{policy: "policy"}
	engine.SetJobManager(nil)
	engine.SetReportViewProvider(func() *ReportView { return testReportView(2) })

	bundle := engine.buildBundle("task", func() []RuntimeContextBlock {
		return []RuntimeContextBlock{{Name: "facts", Role: LLMRoleUser, Content: "session facts"}}
	}, nil, engine.takeReportView())

	if len(bundle.RuntimeContext) != 2 {
		t.Fatalf("expected facts plus view, got %d blocks", len(bundle.RuntimeContext))
	}
	if bundle.RuntimeContext[0].Name != "facts" {
		t.Fatalf("existing runtime context must stay first, got %q", bundle.RuntimeContext[0].Name)
	}
	// Last position is what protects the provider prefix cache: the view changes
	// on every capture, everything before it stays stable.
	last := bundle.RuntimeContext[len(bundle.RuntimeContext)-1]
	if last.Name != reportViewBlockName {
		t.Fatalf("view must be the last runtime block, got %q", last.Name)
	}
	if err := validatePromptBundle(bundle); err != nil {
		t.Fatalf("bundle with a view must validate: %v", err)
	}
}

func TestEngineWithoutViewProviderIsUnchanged(t *testing.T) {
	engine := &Engine{policy: "policy"}
	bundle := engine.buildBundle("task", func() []RuntimeContextBlock {
		return []RuntimeContextBlock{{Name: "facts", Role: LLMRoleUser, Content: "session facts"}}
	}, nil, engine.takeReportView())
	if len(bundle.RuntimeContext) != 1 {
		t.Fatalf("expected only the injected facts, got %d blocks", len(bundle.RuntimeContext))
	}
	absent := &Engine{policy: "policy"}
	absent.SetReportViewProvider(func() *ReportView { return nil })
	empty := absent.buildBundle("task", nil, nil, absent.takeReportView())
	if len(empty.RuntimeContext) != 0 {
		t.Fatalf("nil view must not add a block, got %d", len(empty.RuntimeContext))
	}
}

// TestReportViewNamesUnchangedBlocks: after the first delivery the model only
// receives changed slices, so the fact payload has to say what it is not showing.
// Silently omitting blocks would read as "these blocks no longer exist".
func TestReportViewNamesUnchangedBlocks(t *testing.T) {
	view := testReportView(1)
	view.UnchangedBlocks = []string{"blk_scope", "blk_orders"}
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(blocks))
	}
	for _, want := range []string{`"unchanged_blocks":["blk_scope","blk_orders"]`, `"report_version":17`} {
		if !strings.Contains(blocks[0].Content, want) {
			t.Fatalf("fact payload missing %s: %s", want, blocks[0].Content)
		}
	}
	if len(blocks[0].Parts) != 1 {
		t.Fatalf("expected only the changed slice, got %d parts", len(blocks[0].Parts))
	}
}
