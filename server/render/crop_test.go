package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// testPagePNG builds a 1200x2400 page whose three 800px bands have distinct
// colors, so a crop can be checked by looking at one pixel.
func testPagePNG(t *testing.T) []byte {
	t.Helper()
	page := image.NewRGBA(image.Rect(0, 0, 1200, 2400))
	bands := []color.RGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
	}
	for y := 0; y < 2400; y++ {
		band := bands[y/800]
		for x := 0; x < 1200; x++ {
			page.SetRGBA(x, y, band)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, page); err != nil {
		t.Fatalf("encode test page: %v", err)
	}
	return buffer.Bytes()
}

func decodePixel(t *testing.T, encoded []byte) (image.Image, color.RGBA) {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode slice: %v", err)
	}
	red, green, blue, alpha := decoded.At(5, 5).RGBA()
	return decoded, color.RGBA{uint8(red >> 8), uint8(green >> 8), uint8(blue >> 8), uint8(alpha >> 8)}
}

func TestCropSlicesCutsTheRequestedRegion(t *testing.T) {
	page := testPagePNG(t)
	slices := []Slice{
		{BlockID: "b1", Kind: "markdown", Part: "1/2", Y: 0, Height: 800},
		{BlockID: "b1", Kind: "markdown", Part: "2/2", Y: 1600, Height: 800},
	}
	cropped, err := CropSlices(page, slices)
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if len(cropped) != 2 {
		t.Fatalf("expected 2 slices, got %d", len(cropped))
	}
	if cropped[0].Width != 1200 || cropped[0].Height != 800 {
		t.Fatalf("unexpected slice dimensions %dx%d", cropped[0].Width, cropped[0].Height)
	}
	if _, pixel := decodePixel(t, cropped[0].PNG); pixel.R == 0 || pixel.G != 0 {
		t.Fatalf("first slice shows the wrong band: %+v", pixel)
	}
	if _, pixel := decodePixel(t, cropped[1].PNG); pixel.B == 0 || pixel.R != 0 {
		t.Fatalf("second slice shows the wrong band: %+v", pixel)
	}
}

func TestCropSlicesClampsAndSkipsOutOfRange(t *testing.T) {
	page := testPagePNG(t)
	slices := []Slice{
		// Taller than the page: must clamp instead of failing.
		{BlockID: "tail", Part: "1/1", Y: 2000, Height: 960},
		// Beyond the page: skipped rather than producing an empty image.
		{BlockID: "gone", Part: "1/1", Y: 5000, Height: 960},
	}
	cropped, err := CropSlices(page, slices)
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if len(cropped) != 1 {
		t.Fatalf("expected the out-of-range slice to be skipped, got %d", len(cropped))
	}
	if cropped[0].Height != 400 {
		t.Fatalf("expected clamped height 400, got %d", cropped[0].Height)
	}
}

func TestCropSlicesRejectsCorruptInput(t *testing.T) {
	if _, err := CropSlices([]byte("not a png"), []Slice{{BlockID: "b", Part: "1/1", Height: 10}}); err == nil {
		t.Fatal("expected an error for a corrupt capture")
	}
}
