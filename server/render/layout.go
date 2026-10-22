package render

// Layout is the geometry a capture reports back: the page as a whole plus the
// report blocks and the elements that must not be cut.
type Layout struct {
	Scroll Size         `json:"scroll"`
	Blocks []BlockRect  `json:"blocks"`
	Atomic []AtomicRect `json:"atomic"`
}

type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

type BlockRect struct {
	BlockID string `json:"block_id"`
	Kind    string `json:"kind"`
	Y       int    `json:"y"`
	H       int    `json:"h"`
	X       int    `json:"x"`
	W       int    `json:"w"`
}

// AtomicRect is an element a slice boundary must not cross. Cutting a chart or a
// table in half is worse than dropping it: the model would conclude from half a
// picture.
type AtomicRect struct {
	Tag string `json:"tag"`
	Y   int    `json:"y"`
	H   int    `json:"h"`
	X   int    `json:"x"`
	W   int    `json:"w"`
}

const (
	// DefaultSliceHeight keeps a 1200px-wide slice under the per-image pixel
	// budget of vision encoders, so no second downscale happens at the provider.
	DefaultSliceHeight = 960
	// MinPartHeight avoids producing slivers when a safe cut sits near the start
	// of a slice; below this we prefer the hard cut.
	MinPartHeight = 320
)

// Slice is one image to crop out of the full-page capture.
type Slice struct {
	BlockID string
	Kind    string
	Part    string
	Y       int
	Height  int
}

// PlanSlices turns a layout into per-block crops, avoiding atomic elements.
func PlanSlices(layout Layout, maxHeight int) []Slice {
	if maxHeight <= 0 {
		maxHeight = DefaultSliceHeight
	}
	slices := make([]Slice, 0, len(layout.Blocks))
	for _, block := range layout.Blocks {
		if block.H <= 0 || block.W <= 0 {
			continue
		}
		cuts := planBlockCuts(block, layout.Atomic, maxHeight)
		for index, cut := range cuts {
			slices = append(slices, Slice{
				BlockID: block.BlockID,
				Kind:    block.Kind,
				Part:    partLabel(index, len(cuts)),
				Y:       cut.from,
				Height:  cut.to - cut.from,
			})
		}
	}
	return slices
}

type blockCut struct {
	from int
	to   int
}

func planBlockCuts(block BlockRect, atomic []AtomicRect, maxHeight int) []blockCut {
	cursor := block.Y
	end := block.Y + block.H
	cuts := make([]blockCut, 0, (block.H/maxHeight)+1)
	for end-cursor > maxHeight {
		candidate := cursor + maxHeight
		if boundary, ok := atomicBoundary(atomic, candidate); ok {
			safe := boundary - 2 - cursor
			if safe > MinPartHeight {
				candidate = cursor + safe
			}
		}
		if candidate <= cursor {
			// Defensive: never emit an empty or inverted cut.
			candidate = cursor + maxHeight
		}
		cuts = append(cuts, blockCut{from: cursor, to: candidate})
		cursor = candidate
	}
	if end > cursor {
		cuts = append(cuts, blockCut{from: cursor, to: end})
	}
	return cuts
}

// atomicBoundary reports whether the cut line crosses an atomic element, and
// where that element starts.
func atomicBoundary(atomic []AtomicRect, cut int) (int, bool) {
	best := 0
	found := false
	for _, element := range atomic {
		if element.Y < cut && element.Y+element.H > cut {
			if !found || element.Y < best {
				best = element.Y
				found = true
			}
		}
	}
	return best, found
}

func partLabel(index, total int) string {
	return itoa(index+1) + "/" + itoa(total)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
}
