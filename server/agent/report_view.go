package agent

import (
	"encoding/json"
	"fmt"
)

// ReportView is the current rendered report as the model sees it: a version
// stamp, the render facts, and native-resolution slices.
//
// It is rebuilt for every request and never enters conversation history. That is
// deliberate: history is a text record that gets replayed, compacted and
// audited, while a view is a snapshot of "what the report looks like now". Put
// images in history and replay, compaction and provider migration all have to
// grow image handling.
type ReportView struct {
	// ReportVersion is ReportState.MutationVersion at capture time. A view older
	// than the current report is useless: the model would reason about a draft
	// that no longer exists.
	ReportVersion int
	// CapturedAt records why the capture happened: finalize_precheck,
	// dirty_blocks or manual_screen.
	CapturedAt    string
	ViewportWidth int
	ViewportDPR   int
	// Facts is the render_facts object from the capture service, passed through
	// verbatim: charts mounted, sanitizer removals, failed images, overflow.
	Facts       json.RawMessage
	Slices      []ReportViewSlice
	NotCaptured []ReportViewSkipped
}

// ReportViewSlice is one image of the view, aligned to a report block.
type ReportViewSlice struct {
	BlockID string
	Kind    string
	Part    string
	Width   int
	Height  int
	PNG     []byte
}

// ReportViewSkipped records a block the capture could not produce, so its absence
// is explainable rather than a silent gap.
type ReportViewSkipped struct {
	BlockID string `json:"block_id"`
	Reason  string `json:"reason"`
}

const (
	reportViewBlockName = "report_view"
	// Keep each block well inside maxMessageParts / maxMessageImages: images are
	// the payload and the fact header is one part. A long report therefore
	// arrives as a few blocks instead of one oversized payload.
	maxReportViewImagesPerBlock = 20
)

type reportViewBlockSlice struct {
	BlockID string `json:"block_id"`
	Kind    string `json:"kind,omitempty"`
	Part    string `json:"part,omitempty"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type reportViewBlockFact struct {
	ReportVersion int                    `json:"report_version"`
	CapturedAt    string                 `json:"captured_at,omitempty"`
	Continuation  bool                   `json:"continuation,omitempty"`
	Chunk         string                 `json:"chunk,omitempty"`
	Viewport      *reportViewBlockSize   `json:"viewport,omitempty"`
	Blocks        []reportViewBlockSlice `json:"blocks"`
	NotCaptured   []ReportViewSkipped    `json:"not_captured,omitempty"`
	RenderFacts   json.RawMessage        `json:"render_facts,omitempty"`
}

type reportViewBlockSize struct {
	Width int `json:"width"`
	DPR   int `json:"dpr"`
}

// RuntimeContextBlocks renders the view as runtime context: one text part of
// facts followed by the slice images, split across as many blocks as the payload
// bound requires.
func (v *ReportView) RuntimeContextBlocks() []RuntimeContextBlock {
	if v == nil || v.ReportVersion <= 0 {
		return nil
	}
	slices := v.usableSlices()
	// Facts-only views are legitimate: a capture that failed still has something
	// worth telling the model, which is the point of the facts channel.
	if len(slices) == 0 && len(v.NotCaptured) == 0 && len(v.Facts) == 0 {
		return nil
	}

	chunks := chunkReportViewSlices(slices, maxReportViewImagesPerBlock)
	if len(chunks) == 0 {
		// Facts-only view: the capture failed but the failure is still worth
		// telling the model, which is the whole point of the facts channel.
		chunks = [][]ReportViewSlice{nil}
	}

	blocks := make([]RuntimeContextBlock, 0, len(chunks))
	for index, chunk := range chunks {
		fact := reportViewBlockFact{
			ReportVersion: v.ReportVersion,
			CapturedAt:    v.CapturedAt,
			Blocks:        make([]reportViewBlockSlice, 0, len(chunk)),
		}
		if len(chunks) > 1 {
			fact.Chunk = fmt.Sprintf("%d/%d", index+1, len(chunks))
		}
		if index == 0 {
			if v.ViewportWidth > 0 {
				fact.Viewport = &reportViewBlockSize{Width: v.ViewportWidth, DPR: v.ViewportDPR}
			}
			fact.NotCaptured = v.NotCaptured
			fact.RenderFacts = v.Facts
		} else {
			// Later chunks describe their own slices only; repeating the facts
			// would spend tokens saying the same thing again.
			fact.Continuation = true
		}
		for _, slice := range chunk {
			fact.Blocks = append(fact.Blocks, reportViewBlockSlice{
				BlockID: slice.BlockID,
				Kind:    slice.Kind,
				Part:    slice.Part,
				Width:   slice.Width,
				Height:  slice.Height,
			})
		}
		content, err := json.Marshal(fact)
		if err != nil {
			continue
		}
		parts := make([]MessagePart, 0, len(chunk))
		for _, slice := range chunk {
			parts = append(parts, ImageMessagePart("image/png", slice.PNG, slice.Width, slice.Height))
		}
		blocks = append(blocks, RuntimeContextBlock{
			Name:    reportViewBlockName,
			Role:    LLMRoleUser,
			Content: string(content),
			Parts:   parts,
		})
	}
	return blocks
}

// usableSlices drops slices that cannot be sent. A capture bug must degrade into
// a missing image, never into a request the gateway rejects.
func (v *ReportView) usableSlices() []ReportViewSlice {
	usable := make([]ReportViewSlice, 0, len(v.Slices))
	for _, slice := range v.Slices {
		if len(slice.PNG) == 0 || slice.Width <= 0 || slice.Height <= 0 {
			continue
		}
		if slice.Width > maxMessageImageSide || slice.Height > maxMessageImageSide {
			continue
		}
		usable = append(usable, slice)
	}
	return usable
}

func chunkReportViewSlices(slices []ReportViewSlice, size int) [][]ReportViewSlice {
	if len(slices) == 0 || size <= 0 {
		return nil
	}
	chunks := make([][]ReportViewSlice, 0, (len(slices)+size-1)/size)
	for start := 0; start < len(slices); start += size {
		end := start + size
		if end > len(slices) {
			end = len(slices)
		}
		chunks = append(chunks, slices[start:end])
	}
	return chunks
}
