package render

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
)

// Builder turns a report document into the view the model reads.
//
// It is deliberately total: every failure becomes a facts-only view, because a
// capture problem must not fail the agent run that asked for it. The model then
// learns "the view is unavailable for this reason" instead of silently reasoning
// without one.
// CaptureClient is the part of the executor client the builder needs. Keeping it
// an interface lets tests exercise the builder without a browser.
type CaptureClient interface {
	Capture(ctx context.Context, request CaptureRequest) (*CaptureResponse, error)
}

type Builder struct {
	client        CaptureClient
	cache         Cache
	sliceHeight   int
	viewportWidth int
	viewportDPR   int
	factsOnly     bool
}

type BuilderOptions struct {
	SliceHeight   int
	ViewportWidth int
	ViewportDPR   int
	// FactsOnly skips slicing and cropping: the capture still runs so the render
	// facts are real, but no image is produced. This is the mode for environments
	// that must not pay image tokens or accept pixel-level nondeterminism.
	FactsOnly bool
}

func NewBuilder(client CaptureClient, cache Cache, options BuilderOptions) *Builder {
	height := options.SliceHeight
	if height <= 0 {
		height = DefaultSliceHeight
	}
	width := options.ViewportWidth
	if width <= 0 {
		width = 1200
	}
	dpr := options.ViewportDPR
	if dpr <= 0 {
		dpr = 1
	}
	return &Builder{
		client:        client,
		cache:         cache,
		sliceHeight:   height,
		viewportWidth: width,
		viewportDPR:   dpr,
		factsOnly:     options.FactsOnly,
	}
}

// BuildRequest carries what the builder needs from report state.
type BuildRequest struct {
	HTML         string
	AssetBaseURL string
	// ReportVersion is ReportState.MutationVersion at capture time.
	ReportVersion int
	CapturedAt    string
	// BlockHashes maps a block id to a content hash. When present, unchanged
	// blocks reuse their cached slice instead of being re-encoded.
	BlockHashes map[string]string
}

type captureFailureFacts struct {
	OK       bool   `json:"ok"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
	Captured string `json:"captured_at,omitempty"`
}

// BuildView captures, slices, and crops the current report. It never returns nil
// for a stamped version: an unavailable view is itself a fact.
func (b *Builder) BuildView(ctx context.Context, request BuildRequest) *agent.ReportView {
	view := &agent.ReportView{
		ReportVersion: request.ReportVersion,
		CapturedAt:    request.CapturedAt,
		ViewportWidth: b.viewportWidth,
		ViewportDPR:   b.viewportDPR,
	}
	if b.client == nil {
		view.Facts = mustMarshalFacts(captureFailureFacts{OK: false, Reason: "capture_not_configured"})
		return view
	}

	capture, err := b.client.Capture(ctx, CaptureRequest{
		HTML:          request.HTML,
		AssetBaseURL:  request.AssetBaseURL,
		ViewportWidth: b.viewportWidth,
		ViewportDPR:   b.viewportDPR,
	})
	if err != nil {
		view.Facts = mustMarshalFacts(captureFailureFacts{
			OK:       false,
			Reason:   "capture_failed",
			Detail:   err.Error(),
			Captured: request.CapturedAt,
		})
		return view
	}
	view.Facts = capture.Facts
	if b.factsOnly {
		// Facts are the deliverable in this mode; cropping would only cost CPU.
		return view
	}

	slices := PlanSlices(capture.Layout, b.sliceHeight)
	if len(slices) == 0 {
		view.NotCaptured = append(view.NotCaptured, agent.ReportViewSkipped{
			BlockID: "",
			Reason:  "no_blocks_rendered",
		})
		return view
	}

	// Cache lookup first: a hit skips cropping, which also skips re-encoding.
	pending := make([]Slice, 0, len(slices))
	cached := make(map[string][]byte, len(slices))
	for _, slice := range slices {
		key := b.sliceKey(request, slice)
		if key == "" {
			pending = append(pending, slice)
			continue
		}
		if value, ok := b.cache.Get(key); ok {
			cached[sliceKeyID(slice)] = value
			continue
		}
		pending = append(pending, slice)
	}

	cropped, cropErr := CropSlices(capture.PNG, pending)
	if cropErr != nil {
		view.Facts = mustMarshalFacts(captureFailureFacts{
			OK:       false,
			Reason:   "crop_failed",
			Detail:   cropErr.Error(),
			Captured: request.CapturedAt,
		})
		return view
	}
	fresh := make(map[string]SliceImage, len(cropped))
	keyByID := make(map[string]string, len(pending))
	for _, slice := range pending {
		keyByID[sliceKeyID(slice)] = b.sliceKey(request, slice)
	}
	for _, image := range cropped {
		id := image.BlockID + "\x00" + image.Part
		fresh[id] = image
		if key := keyByID[id]; key != "" {
			b.cache.Put(key, image.PNG)
		}
	}

	for _, slice := range slices {
		id := sliceKeyID(slice)
		if pngBytes, ok := cached[id]; ok {
			view.Slices = append(view.Slices, agent.ReportViewSlice{
				BlockID:     slice.BlockID,
				Kind:        slice.Kind,
				Part:        slice.Part,
				Width:       b.viewportWidth,
				Height:      slice.Height,
				PNG:         pngBytes,
				ContentHash: b.blockHash(request, slice.BlockID),
			})
			continue
		}
		image, ok := fresh[id]
		if !ok {
			view.NotCaptured = append(view.NotCaptured, agent.ReportViewSkipped{
				BlockID: slice.BlockID,
				Reason:  "slice_crop_failed",
			})
			continue
		}
		view.Slices = append(view.Slices, agent.ReportViewSlice{
			BlockID:     image.BlockID,
			Kind:        image.Kind,
			Part:        image.Part,
			Width:       image.Width,
			Height:      image.Height,
			PNG:         image.PNG,
			ContentHash: b.blockHash(request, image.BlockID),
		})
	}
	return view
}

// blockHash returns the block's content hash, or "" when the caller did not
// supply one. The coordinator treats "" as "cannot prove unchanged", which is the
// safe direction: a spurious full view costs tokens, a spurious partial view
// hides content from the model.
func (b *Builder) blockHash(request BuildRequest, blockID string) string {
	if request.BlockHashes == nil {
		return ""
	}
	return request.BlockHashes[blockID]
}

func (b *Builder) sliceKey(request BuildRequest, slice Slice) string {
	contentHash := b.blockHash(request, slice.BlockID)
	return SliceKey(
		strconv.Itoa(request.ReportVersion),
		slice.BlockID,
		slice.Part,
		strconv.Itoa(slice.Height),
		strconv.Itoa(b.viewportWidth),
		strconv.Itoa(b.viewportDPR),
		contentHash,
	)
}

func sliceKeyID(slice Slice) string {
	return slice.BlockID + "\x00" + slice.Part
}

func mustMarshalFacts(value captureFailureFacts) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"ok":false,"reason":%q}`, "facts_encoding_failed"))
	}
	return encoded
}
