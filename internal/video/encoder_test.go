package video

import "testing"

func TestOutputDimsDefaultTo1080p(t *testing.T) {
	tests := []struct {
		name string
		res  Resolution
		w    int
		h    int
	}{
		{name: "empty", res: "", w: 1920, h: 1080},
		{name: "unknown", res: "bogus", w: 1920, h: 1080},
		{name: "720p", res: Resolution720p, w: 1280, h: 720},
		{name: "1080p", res: Resolution1080p, w: 1920, h: 1080},
		{name: "4k", res: Resolution4K, w: 3840, h: 2160},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h := outputDims(tt.res)
			if w != tt.w || h != tt.h {
				t.Fatalf("outputDims(%q) = %dx%d, want %dx%d", tt.res, w, h, tt.w, tt.h)
			}
		})
	}
}
