package imagegen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makeTestPNG renders a small solid-color PNG, standing in for the PNG/JPEG
// bytes an image model returns.
func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 0x8E, G: 0x5C, B: 0xF7, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestToWebPProducesDecodableWebP(t *testing.T) {
	raw := makeTestPNG(t)

	out, err := ToWebP(raw)
	if err != nil {
		t.Fatalf("ToWebP: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("ToWebP returned empty output")
	}
	// RIFF....WEBP container header.
	if len(out) < 12 || string(out[0:4]) != "RIFF" || string(out[8:12]) != "WEBP" {
		t.Fatalf("output is not a WebP RIFF container: % x", out[:min(12, len(out))])
	}
}

func TestToWebPRejectsNonImage(t *testing.T) {
	if _, err := ToWebP([]byte("not an image")); err == nil {
		t.Fatal("expected error for non-image input, got nil")
	}
}
