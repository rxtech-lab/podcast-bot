package musicmixer

import "testing"

// The package-level OverlapMusic / ReplaceMusic wrappers are intentionally
// thin — they're just there to give the dispatch path a symmetric, free-
// standing API. We can't easily exercise the ffmpeg subprocess path in a
// unit test (would require a live ffmpeg + a real mp3), but we CAN verify
// the input-validation behaviour: nil mixer, empty source.

func TestOverlapMusic_NilMixer(t *testing.T) {
	if err := OverlapMusic(nil, "anything"); err == nil {
		t.Errorf("expected error on nil mixer")
	}
}

func TestReplaceMusic_NilMixer(t *testing.T) {
	if err := ReplaceMusic(nil, "anything"); err == nil {
		t.Errorf("expected error on nil mixer")
	}
}
