package tts

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/audio"
)

// TestFakeClipMatchesByteRateContract pins the pipeline-wide CBR assumption:
// caption cue durations are computed as raw TTS byte count divided by
// audio.AudioBytesPerSec (24000 B/s, i.e. the pipeline's 48kHz/192kbps stereo MP3). The
// fake provider's embedded clip must honour the same contract, both so
// hermetic sync tests stay representative and so a future TTS format change
// fails here by name instead of silently skewing every subtitle timeline.
func TestFakeClipMatchesByteRateContract(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	clipPath := filepath.Join(t.TempDir(), "silence.mp3")
	if err := os.WriteFile(clipPath, silenceMP3, 0o644); err != nil {
		t.Fatal(err)
	}
	probe := func(entry, selector string) string {
		t.Helper()
		args := []string{"-v", "error"}
		if selector != "" {
			args = append(args, "-select_streams", selector)
		}
		args = append(args, "-show_entries", entry,
			"-of", "default=noprint_wrappers=1:nokey=1", clipPath)
		out, err := exec.Command("ffprobe", args...).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", entry, err)
		}
		return strings.TrimSpace(string(out))
	}

	if got := probe("stream=sample_rate", "a:0"); got != "48000" {
		t.Errorf("fake clip sample rate = %s, want 48000 (48kHz/192kbps stereo MP3)", got)
	}
	if got := probe("stream=channels", "a:0"); got != "2" {
		t.Errorf("fake clip channels = %s, want stereo", got)
	}

	wantSecs := float64(len(silenceMP3)) / float64(audio.AudioBytesPerSec)
	rawDur := probe("format=duration", "")
	gotSecs, err := strconv.ParseFloat(rawDur, 64)
	if err != nil {
		t.Fatalf("parse duration %q: %v", rawDur, err)
	}
	if math.Abs(gotSecs-wantSecs) > 0.010 {
		t.Errorf("fake clip duration = %.4fs but %d bytes / %d B/s = %.4fs — byte-rate contract broken",
			gotSecs, len(silenceMP3), audio.AudioBytesPerSec, wantSecs)
	}
}
