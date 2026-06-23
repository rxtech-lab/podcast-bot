package imagegen

import (
	"bytes"
	"fmt"
	"image"

	"github.com/HugoSmits86/nativewebp"
)

// ToWebP decodes a PNG/JPEG byte slice (the formats image models return) and
// re-encodes it as lossless WebP. Cover art is flat, low-detail podcast
// artwork, so VP8L lossless keeps the file small while avoiding a cgo libwebp
// dependency. Returns an error if the input can't be decoded as an image.
func ToWebP(raw []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("imagegen: decode for webp: %w", err)
	}
	var buf bytes.Buffer
	if err := nativewebp.Encode(&buf, img, nil); err != nil {
		return nil, fmt.Errorf("imagegen: webp encode: %w", err)
	}
	return buf.Bytes(), nil
}
