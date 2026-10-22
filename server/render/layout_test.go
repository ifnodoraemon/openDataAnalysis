package render

import "testing"

func TestPlanSlicesSplitsLongBlocks(t *testing.T) {
	layout := Layout{
		Blocks: []BlockRect{{BlockID: "b1", Kind: "markdown", Y: 100, H: 2400, W: 1200}},
	}
	slices := PlanSlices(layout, 960)
	if len(slices) != 3 {
		t.Fatalf("expected 3 slices, got %d", len(slices))
	}
	wantHeights := []int{960, 960, 480}
	for index, want := range wantHeights {
		if slices[index].Height != want {
			t.Errorf("slice %d height = %d, want %d", index, slices[index].Height, want)
		}
		if slices[index].Part != partLabel(index, 3) {
			t.Errorf("slice %d part = %q", index, slices[index].Part)
		}
	}
	if slices[0].Y != 100 || slices[2].Y != 100+1920 {
		t.Errorf("slice offsets are wrong: %+v", slices)
	}
}

// TestPlanSlicesAvoidsCuttingCharts is the rule the spike proved matters: a chart
// cut in half produces a picture the model will reason about incorrectly.
func TestPlanSlicesAvoidsCuttingCharts(t *testing.T) {
	layout := Layout{
		Blocks: []BlockRect{{BlockID: "b1", Kind: "markdown", Y: 0, H: 2000, W: 1200}},
		Atomic: []AtomicRect{{Tag: "div", Y: 800, H: 450, W: 720}},
	}
	slices := PlanSlices(layout, 960)
	if slices[0].Height != 798 {
		t.Fatalf("first slice should stop before the chart at 798, got %d", slices[0].Height)
	}
	// The chart now sits whole inside the second slice.
	second := slices[1]
	if _, crosses := atomicBoundary(layout.Atomic, second.Y+second.Height); crosses {
		// The cut may still land inside the chart only if there is no other way;
		// assert it does not happen for this geometry.
		t.Fatalf("second cut at %d still crosses the chart", second.Y+second.Height)
	}
}

func TestPlanSlicesKeepsHardCutWhenBoundaryWouldBeTooEarly(t *testing.T) {
	layout := Layout{
		Blocks: []BlockRect{{BlockID: "b1", Kind: "markdown", Y: 0, H: 1600, W: 1200}},
		// An element starting at 100 runs past the 960 cut; moving the cut to 98
		// would produce a sliver, so the hard cut wins.
		Atomic: []AtomicRect{{Tag: "table", Y: 100, H: 1200, W: 1200}},
	}
	slices := PlanSlices(layout, 960)
	if slices[0].Height != 960 {
		t.Fatalf("expected the hard cut to be kept, got %d", slices[0].Height)
	}
}

func TestPlanSlicesSkipsDegenerateBlocks(t *testing.T) {
	layout := Layout{
		Blocks: []BlockRect{
			{BlockID: "empty", Y: 0, H: 0, W: 1200},
			{BlockID: "no_width", Y: 0, H: 100, W: 0},
			{BlockID: "ok", Y: 0, H: 100, W: 1200},
		},
	}
	slices := PlanSlices(layout, 960)
	if len(slices) != 1 || slices[0].BlockID != "ok" {
		t.Fatalf("expected only the usable block, got %+v", slices)
	}
	if slices[0].Part != "1/1" {
		t.Fatalf("unexpected part label %q", slices[0].Part)
	}
}
