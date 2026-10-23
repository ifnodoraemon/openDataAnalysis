package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// fakeCaptureClient is the browser-free stand-in for the executor.
type fakeCaptureClient struct {
	page     []byte
	layout   Layout
	facts    json.RawMessage
	err      error
	captures int
}

func (f *fakeCaptureClient) Capture(ctx context.Context, request CaptureRequest) (*CaptureResponse, error) {
	f.captures++
	if f.err != nil {
		return nil, f.err
	}
	return &CaptureResponse{PNG: f.page, Layout: f.layout, Facts: f.facts}, nil
}

func solidPage(t *testing.T, width, height int, shade color.RGBA) []byte {
	t.Helper()
	page := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			page.SetRGBA(x, y, shade)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, page); err != nil {
		t.Fatalf("encode page: %v", err)
	}
	return buffer.Bytes()
}

func buildLayout() Layout {
	return Layout{
		Scroll: Size{W: 1200, H: 1920},
		Blocks: []BlockRect{{BlockID: "b1", Kind: "markdown", Y: 0, H: 1920, W: 1200}},
	}
}

func TestBuildViewCapturesSlicesAndFacts(t *testing.T) {
	client := &fakeCaptureClient{
		page:   solidPage(t, 1200, 1920, color.RGBA{G: 200, A: 255}),
		layout: buildLayout(),
		facts:  json.RawMessage(`{"charts_mounted":3}`),
	}
	builder := NewBuilder(client, NewMemoryCache(8), BuilderOptions{})
	view := builder.BuildView(context.Background(), BuildRequest{
		HTML:          "<html></html>",
		ReportVersion: 7,
		CapturedAt:    "finalize_precheck",
	})
	if view.ReportVersion != 7 || view.CapturedAt != "finalize_precheck" {
		t.Fatalf("view is not stamped: %+v", view)
	}
	if len(view.Slices) != 2 {
		t.Fatalf("expected 2 slices for a 1920px block, got %d", len(view.Slices))
	}
	if view.Slices[0].Width != 1200 || view.Slices[0].Height != DefaultSliceHeight {
		t.Fatalf("unexpected slice size %dx%d", view.Slices[0].Width, view.Slices[0].Height)
	}
	if !strings.Contains(string(view.Facts), "charts_mounted") {
		t.Fatalf("facts were not carried: %s", view.Facts)
	}
	// The view must be usable by the engine's own contract.
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 || len(blocks[0].Parts) != 2 {
		t.Fatalf("view did not render into one block with two images: %+v", blocks)
	}
}

// TestBuildViewReusesCachedSlices: same version and block hashes must not re-crop,
// even though the page bytes changed underneath.
func TestBuildViewReusesCachedSlices(t *testing.T) {
	first := solidPage(t, 1200, 1920, color.RGBA{R: 200, A: 255})
	client := &fakeCaptureClient{page: first, layout: buildLayout(), facts: json.RawMessage(`{}`)}
	cache := NewMemoryCache(8)
	builder := NewBuilder(client, cache, BuilderOptions{})
	request := BuildRequest{HTML: "<html></html>", ReportVersion: 3, BlockHashes: map[string]string{"b1": "hash-a"}}

	view := builder.BuildView(context.Background(), request)
	if len(view.Slices) != 2 {
		t.Fatalf("expected 2 slices, got %d", len(view.Slices))
	}
	firstPNG := append([]byte(nil), view.Slices[0].PNG...)

	// A different page arrives; the cache must win because nothing changed.
	client.page = solidPage(t, 1200, 1920, color.RGBA{B: 200, A: 255})
	repeat := builder.BuildView(context.Background(), request)
	if len(repeat.Slices) != 2 {
		t.Fatalf("expected 2 slices on the repeat, got %d", len(repeat.Slices))
	}
	if !bytes.Equal(firstPNG, repeat.Slices[0].PNG) {
		t.Fatal("unchanged block was re-cropped instead of served from cache")
	}
	if client.captures != 2 {
		t.Fatalf("capture should still run per request, got %d calls", client.captures)
	}

	// Change the block hash: the slice must be rebuilt from the new page.
	request.BlockHashes["b1"] = "hash-b"
	changed := builder.BuildView(context.Background(), request)
	if bytes.Equal(firstPNG, changed.Slices[0].PNG) {
		t.Fatal("dirty block kept the stale slice")
	}
}

// TestBuildViewDegradesToFacts: a broken capture must still produce a view the
// model can read, never a nil that leaves it silently blind.
func TestBuildViewDegradesToFacts(t *testing.T) {
	client := &fakeCaptureClient{err: errors.New("chromium crashed")}
	builder := NewBuilder(client, NewMemoryCache(4), BuilderOptions{})
	view := builder.BuildView(context.Background(), BuildRequest{
		HTML:          "<html></html>",
		ReportVersion: 9,
		CapturedAt:    "finalize_precheck",
	})
	if len(view.Slices) != 0 {
		t.Fatalf("failed capture must not carry slices, got %d", len(view.Slices))
	}
	if !strings.Contains(string(view.Facts), "capture_failed") {
		t.Fatalf("failure reason missing: %s", view.Facts)
	}
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 {
		t.Fatalf("a failed capture should still yield a facts-only block, got %d", len(blocks))
	}
	if len(blocks[0].Parts) != 0 {
		t.Fatalf("facts-only block must carry no images, got %d", len(blocks[0].Parts))
	}
}

func TestBuildViewWithoutClientReportsNotConfigured(t *testing.T) {
	builder := NewBuilder(nil, NewMemoryCache(4), BuilderOptions{})
	view := builder.BuildView(context.Background(), BuildRequest{HTML: "<html></html>", ReportVersion: 1})
	if !strings.Contains(string(view.Facts), "capture_not_configured") {
		t.Fatalf("missing configuration should be stated as a fact: %s", view.Facts)
	}
}

// The builder output must satisfy the engine's payload bounds for a long report.
func TestBuildViewProducesValidPayloadForLongReport(t *testing.T) {
	// 25 slices at 960px: more than one block can carry (20 images), so this also
	// exercises the engine-side chunking path.
	const width, height = 100, 24000
	layout := Layout{
		Scroll: Size{W: width, H: height},
		Blocks: []BlockRect{{BlockID: "big", Kind: "markdown", Y: 0, H: height, W: width}},
	}
	client := &fakeCaptureClient{page: solidPage(t, width, height, color.RGBA{A: 255}), layout: layout}
	builder := NewBuilder(client, NewMemoryCache(64), BuilderOptions{ViewportWidth: width})
	view := builder.BuildView(context.Background(), BuildRequest{HTML: "<html></html>", ReportVersion: 2})

	blocks := view.RuntimeContextBlocks()
	if len(blocks) < 2 {
		t.Fatalf("a 24000px report should need several blocks, got %d", len(blocks))
	}
	// The engine rejects payloads above 32 parts / 24 images, so staying inside
	// those bounds here is what keeps a long report sendable at all.
	for index, block := range blocks {
		images := 0
		for _, part := range block.Parts {
			if part.Kind == "image" {
				images++
			}
		}
		if len(block.Parts) > 24 {
			t.Errorf("block %d carries %d parts, over the payload bound", index, len(block.Parts))
		}
		if images > 24 {
			t.Errorf("block %d carries %d images, over the payload bound", index, images)
		}
	}
}

// The coordinator filters by ContentHash; without it every block looks dirty and
// incremental delivery silently degrades to a full view on every turn.
func TestBuildViewStampsSliceContentHashes(t *testing.T) {
	client := &fakeCaptureClient{page: solidPage(t, 1200, 1920, color.RGBA{A: 255}), layout: buildLayout()}
	builder := NewBuilder(client, NewMemoryCache(8), BuilderOptions{})
	view := builder.BuildView(context.Background(), BuildRequest{
		HTML:          "<html></html>",
		ReportVersion: 4,
		BlockHashes:   map[string]string{"b1": "hash-b1"},
	})
	if len(view.Slices) == 0 {
		t.Fatal("expected slices")
	}
	for _, slice := range view.Slices {
		if slice.ContentHash != "hash-b1" {
			t.Fatalf("slice %s lost its content hash: %q", slice.BlockID, slice.ContentHash)
		}
	}
}

// TestBuildViewFactsOnlySkipsImages is the contract of REPORT_VIEW_CAPTURE=facts:
// the capture still runs (so the facts are real) but nothing image-shaped is
// produced, which is what keeps benchmark runs deterministic and cheap.
func TestBuildViewFactsOnlySkipsImages(t *testing.T) {
	layout := Layout{
		Scroll: Size{W: 1200, H: 1920},
		Blocks: []BlockRect{{BlockID: "b1", Kind: "markdown", Y: 0, H: 1920, W: 1200}},
	}
	client := &fakeCaptureClient{
		page:   solidPage(t, 1200, 1920, color.RGBA{A: 255}),
		layout: layout,
		facts:  json.RawMessage(`{"charts_mounted":2,"overflow_x":false}`),
	}
	builder := NewBuilder(client, NewMemoryCache(8), BuilderOptions{FactsOnly: true})
	view := builder.BuildView(context.Background(), BuildRequest{
		HTML:          "<html></html>",
		ReportVersion: 5,
		BlockHashes:   map[string]string{"b1": "h1"},
	})
	if client.captures != 1 {
		t.Fatalf("facts mode must still capture once, got %d", client.captures)
	}
	if len(view.Slices) != 0 {
		t.Fatalf("facts mode must not produce slices, got %d", len(view.Slices))
	}
	if !strings.Contains(string(view.Facts), "charts_mounted") {
		t.Fatalf("facts were lost: %s", view.Facts)
	}
	// The delivered payload is a single image-free block carrying the facts.
	blocks := view.RuntimeContextBlocks()
	if len(blocks) != 1 {
		t.Fatalf("expected one facts block, got %d", len(blocks))
	}
	if len(blocks[0].Parts) != 0 {
		t.Fatalf("facts block must carry no images, got %d parts", len(blocks[0].Parts))
	}
	if !strings.Contains(blocks[0].Content, "charts_mounted") {
		t.Fatalf("facts block lost the payload: %s", blocks[0].Content)
	}
}
