package musicmixer

import (
	"encoding/binary"
	"testing"
)

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

func TestApplyTTSDeClickFadesBurstEdges(t *testing.T) {
	samples := ttsDeClickSamples * 2
	pcm := make([]byte, samples*pcmSampleBytes)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(pcm[i*pcmSampleBytes:], uint16(int16(10_000)))
	}

	applyTTSDeClick(pcm, true, true)

	first := int16(binary.LittleEndian.Uint16(pcm[0:]))
	middle := int16(binary.LittleEndian.Uint16(pcm[ttsDeClickSamples*pcmSampleBytes:]))
	last := int16(binary.LittleEndian.Uint16(pcm[(samples-1)*pcmSampleBytes:]))

	if first <= 0 || first >= 10_000 {
		t.Fatalf("first sample = %d, want faded positive sample below full scale", first)
	}
	if middle != 10_000 {
		t.Fatalf("middle sample = %d, want unchanged full scale", middle)
	}
	if last <= 0 || last >= 10_000 {
		t.Fatalf("last sample = %d, want faded positive sample below full scale", last)
	}
}
