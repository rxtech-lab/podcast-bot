package musicmixer

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMapTTSToOutput(t *testing.T) {
	m := &Mixer{syncPoints: []timelineSyncPoint{
		{tts: 0, out: 10 * time.Second},               // first burst after 10s bed pre-roll
		{tts: 5 * time.Second, out: 18 * time.Second}, // 3s bed-only gap after 5s of TTS
		{tts: 9 * time.Second, out: 22 * time.Second}, // another gap
	}}
	tests := []struct{ tts, want time.Duration }{
		{0, 10 * time.Second},
		{2 * time.Second, 12 * time.Second},
		{5 * time.Second, 18 * time.Second},
		{7 * time.Second, 20 * time.Second},
		{9 * time.Second, 22 * time.Second},
		{12 * time.Second, 25 * time.Second}, // extrapolates past newest point
	}
	for _, tt := range tests {
		if got := m.MapTTSToOutput(tt.tts); got != tt.want {
			t.Errorf("MapTTSToOutput(%v) = %v, want %v", tt.tts, got, tt.want)
		}
	}
	// No sync points yet → identity.
	empty := &Mixer{}
	if got := empty.MapTTSToOutput(3 * time.Second); got != 3*time.Second {
		t.Errorf("empty map should be identity, got %v", got)
	}
}

// TestMixerTimelineMapMatchesAudibleOnsets is the end-to-end regression test
// for the "captions drift ahead of mixer-backed audio" bug. It runs a real
// mixer session (music bed + two tone clips separated by an idle gap),
// records the output, finds the tones' actual onsets acoustically, and
// asserts MapTTSToOutput places the TTS timeline at those positions. The
// old wall-clock cue math had no way to see the bed-only gap; this map must.
func TestMixerTimelineMapMatchesAudibleOnsets(t *testing.T) {
	if testing.Short() {
		t.Skip("realtime mixer test skipped in -short mode")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()

	musicPath := filepath.Join(dir, "bed.mp3")
	if out, err := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "sine=frequency=110:duration=30",
		"-ar", "48000", "-ac", "2", "-c:a", "libmp3lame", "-b:a", "192k", "-write_xing", "0",
		musicPath).CombinedOutput(); err != nil {
		t.Fatalf("generate bed: %v (%s)", err, out)
	}
	// ffmpeg's sine source generates at ~0.125 amplitude; boost the tone
	// to full scale so it stands far above the 10%-volume bed in the mix.
	tonePath := filepath.Join(dir, "tone.mp3")
	if out, err := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=1.0",
		"-af", "volume=8",
		"-ar", "48000", "-ac", "2", "-c:a", "libmp3lame", "-b:a", "192k", "-write_xing", "0",
		tonePath).CombinedOutput(); err != nil {
		t.Fatalf("generate tone: %v (%s)", err, out)
	}
	clip, err := os.ReadFile(tonePath)
	if err != nil {
		t.Fatal(err)
	}
	clipDur := time.Duration(len(clip)) * time.Second / 24000 // CBR 192kbps

	// The TTS mp3 decoder buffers tens of KB of input before its first
	// output — a single short clip would sit undecoded until EOF. Prime it
	// with a long, effectively silent clip (mixes inaudibly under the bed)
	// so the loud clips decode promptly when written.
	quietPath := filepath.Join(dir, "quiet.mp3")
	if out, err := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "sine=frequency=200:duration=8",
		"-af", "volume=0.01",
		"-ar", "48000", "-ac", "2", "-c:a", "libmp3lame", "-b:a", "192k", "-write_xing", "0",
		quietPath).CombinedOutput(); err != nil {
		t.Fatalf("generate quiet primer: %v (%s)", err, out)
	}
	quiet, err := os.ReadFile(quietPath)
	if err != nil {
		t.Fatal(err)
	}
	quietDur := time.Duration(len(quiet)) * time.Second / 24000

	outPath := filepath.Join(dir, "mixed.mp3")
	sink, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewSession(musicPath, sink)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	outPos := func() time.Duration {
		m.syncMu.Lock()
		defer m.syncMu.Unlock()
		return pcmBytesToDuration(m.outMixed)
	}
	ttsPos := func() time.Duration {
		m.syncMu.Lock()
		defer m.syncMu.Unlock()
		return pcmBytesToDuration(m.ttsMixed)
	}
	waitFor := func(what string, cond func() bool) {
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s (out=%v tts=%v)", what, outPos(), ttsPos())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// The -re music decoder bursts a few seconds of PCM up front, during
	// which the mix loop races ahead of wall time and then stalls to let
	// realtime catch up — anything written in that window lands back-to-
	// back regardless of wall-clock gaps. Wait for steady state (output
	// position tracking wall clock with a small constant lead) before
	// staging the scenario.
	start := time.Now()
	waitFor("steady state", func() bool {
		lead := outPos() - time.Since(start)
		return outPos() > 2*time.Second && lead < time.Second
	})
	t.Logf("steady after %v (out=%v)", time.Since(start).Round(time.Millisecond), outPos())

	// Prime the decoder, then wait until most of the primer has mixed so
	// the loud clips land in true steady state.
	if _, err := m.Write(quiet); err != nil {
		t.Fatalf("write primer: %v", err)
	}
	waitFor("primer mixed", func() bool { return ttsPos() >= quietDur*9/10 })

	// The decoder holds a clip's final frames until more input or EOF
	// arrives, so "mixed" thresholds leave a ~400ms tail allowance.
	const decoderTail = 400 * time.Millisecond
	if _, err := m.Write(clip); err != nil {
		t.Fatalf("write clip 1: %v", err)
	}
	// Wait until clip 1 has (almost) fully mixed, then hold a bed-only gap.
	waitFor("clip 1 mixed", func() bool { return ttsPos() >= quietDur+clipDur-decoderTail })
	time.Sleep(2 * time.Second)
	if _, err := m.Write(clip); err != nil {
		t.Fatalf("write clip 2: %v", err)
	}
	waitFor("clip 2 mixed", func() bool { return ttsPos() >= quietDur+2*clipDur-decoderTail })
	if err := m.Close(); err != nil {
		t.Logf("mixer close: %v", err)
	}
	sink.Close()

	predicted1 := m.MapTTSToOutput(quietDur)
	predicted2 := m.MapTTSToOutput(quietDur + clipDur)
	m.syncMu.Lock()
	t.Logf("sync points: %v, ttsMixed=%v outMixed=%v", m.syncPoints,
		pcmBytesToDuration(m.ttsMixed), pcmBytesToDuration(m.outMixed))
	m.syncMu.Unlock()

	onsets := detectToneOnsets(t, outPath)
	if len(onsets) != 2 {
		t.Fatalf("expected 2 tone onsets in mix, found %d: %v (predicted %v / %v)",
			len(onsets), onsets, predicted1, predicted2)
	}
	const tol = 300 * time.Millisecond
	if diff := absDur(onsets[0] - predicted1); diff > tol {
		t.Errorf("clip 1: map says %v, audible onset at %v (Δ %v > %v)", predicted1, onsets[0], diff, tol)
	}
	if diff := absDur(onsets[1] - predicted2); diff > tol {
		t.Errorf("clip 2: map says %v, audible onset at %v (Δ %v > %v)", predicted2, onsets[1], diff, tol)
	}
	// The map must reflect the real bed-only gap: clip 2 starts well after
	// clip 1's end, not back-to-back.
	if predicted2 < predicted1+clipDur+time.Second {
		t.Errorf("map lost the idle gap: clip1 %v + dur %v vs clip2 %v", predicted1, clipDur, predicted2)
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// detectToneOnsets decodes the mixed file to PCM and returns the start times
// of loud bursts. The bed sits at musicVolume (0.10); the tone clips play at
// ttsVolume (0.80), so a simple RMS threshold separates them cleanly.
func detectToneOnsets(t *testing.T, path string) []time.Duration {
	t.Helper()
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-ac", "1", "-ar", "8000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatalf("decode mix: %v", err)
	}
	const sr = 8000
	const win = sr / 20 // 50ms windows
	samples := len(raw) / 2
	var onsets []time.Duration
	loud := false
	quietRun := 0
	for w := 0; w+win <= samples; w += win {
		var sum float64
		for i := w; i < w+win; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(raw[2*i:])))
			sum += v * v
		}
		rms := math.Sqrt(sum/float64(win)) / 32768
		if rms > 0.15 {
			if !loud {
				onsets = append(onsets, time.Duration(w)*time.Second/sr)
				loud = true
			}
			quietRun = 0
		} else if loud {
			// Require a sustained quiet stretch before re-arming so the
			// tone's own amplitude ripple doesn't double-count an onset.
			if quietRun++; quietRun >= 6 { // 300ms
				loud = false
				quietRun = 0
			}
		}
	}
	if len(onsets) > 0 {
		t.Logf("detected onsets: %v", onsets)
	}
	return onsets
}
