package render

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
)

// SliceImage is one cropped slice, ready to become a message part.
type SliceImage struct {
	BlockID string
	Kind    string
	Part    string
	Width   int
	Height  int
	PNG     []byte
}

// CropSlices cuts slices out of one full-page PNG.
//
// The capture service returns a single screenshot rather than one screenshot per
// block: per-block clipping was measured at ~7x the cost, and cropping the full
// page produces byte-identical pixels.
func CropSlices(fullPNG []byte, slices []Slice) ([]SliceImage, error) {
	decoded, err := png.Decode(bytes.NewReader(fullPNG))
	if err != nil {
		return nil, fmt.Errorf("decode full-page capture: %w", err)
	}
	bounds := decoded.Bounds()
	cropped := make([]SliceImage, 0, len(slices))
	for _, slice := range slices {
		top := slice.Y
		bottom := slice.Y + slice.Height
		if top < 0 {
			top = 0
		}
		if bottom > bounds.Max.Y {
			bottom = bounds.Max.Y
		}
		if bottom <= top {
			// The layout moved between capture and crop; skip rather than emit a
			// degenerate image the provider would reject.
			continue
		}
		// The decoder returns NRGBA for most screenshots and Paletted for some, so
		// copy through a plain RGBA instead of asserting a concrete type.
		source := image.Rect(bounds.Min.X, bounds.Min.Y+top, bounds.Max.X, bounds.Min.Y+bottom)
		destination := image.NewRGBA(image.Rect(0, 0, source.Dx(), source.Dy()))
		draw.Draw(destination, destination.Bounds(), decoded, source.Min, draw.Src)

		var buffer bytes.Buffer
		if err := png.Encode(&buffer, destination); err != nil {
			return nil, fmt.Errorf("encode slice %s: %w", slice.BlockID, err)
		}
		cropped = append(cropped, SliceImage{
			BlockID: slice.BlockID,
			Kind:    slice.Kind,
			Part:    slice.Part,
			Width:   destination.Bounds().Dx(),
			Height:  destination.Bounds().Dy(),
			PNG:     buffer.Bytes(),
		})
	}
	return cropped, nil
}
