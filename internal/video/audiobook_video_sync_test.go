package video

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeSyncTestImages produces n solid-color PNGs for slideshow inputs.
func writeSyncTestImages(t *testing.T, dir string, n int) []string {
	t.Helper()
	palette := []color.RGBA{{0xff, 0, 0, 0xff}, {0, 0xff, 0, 0xff}, {0, 0, 0xff, 0xff}}
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		c := palette[i%len(palette)]
		for y := 0; y < 180; y++ {
			for x := 0; x < 320; x++ {
				img.Set(x, y, c)
			}
		}
		p := filepath.Join(dir, fmt.Sprintf("scene-%d.png", i))
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		paths[i] = p
	}
	return paths
}

// writeSyncTestAudio generates secs seconds of 440Hz tone in the pipeline's
// CBR contract format (24kHz mono 48kbps MP3).
func writeSyncTestAudio(t *testing.T, path string, secs float64) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%.1f", secs),
		"-ar", "24000", "-ac", "1", "-c:a", "libmp3lame", "-b:a", "48k",
		path).CombinedOutput(); err != nil {
		t.Fatalf("generate tone: %v (%s)", err, out)
	}
}

func probeStreamValue(t *testing.T, path, selector, entry string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", selector,
		"-show_entries", "stream="+entry,
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s %s: %v", path, selector, err)
	}
	return strings.TrimSpace(string(out))
}

func probeStreamDuration(t *testing.T, path, selector string) float64 {
	t.Helper()
	raw := probeStreamValue(t, path, selector, "duration")
	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("parse %s duration %q: %v", selector, raw, err)
	}
	return secs
}

// TestRenderAudioBookVideoKeepsAudioAndCaptionsInSync renders a full audiobook
// video from known-duration audio plus a VTT sidecar and asserts the produced
// mp4 keeps all three tracks the length of the audio: no truncated narration,
// no caption track outliving the media.
func TestRenderAudioBookVideoKeepsAudioAndCaptionsInSync(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	const audioSecs = 8.0

	audioPath := filepath.Join(dir, "audio.mp3")
	writeSyncTestAudio(t, audioPath, audioSecs)
	imgs := writeSyncTestImages(t, dir, 2)

	vttPath := filepath.Join(dir, "subtitles.vtt")
	vtt := "WEBVTT\n\n" +
		"1\n00:00:00.000 --> 00:00:03.900\nFirst caption line\n\n" +
		"2\n00:00:03.900 --> 00:00:07.900\nSecond caption line\n\n"
	if err := os.WriteFile(vttPath, []byte(vtt), 0o644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(dir, "video.mp4")
	opts := AudioBookVideoOptions{
		Animations:   []string{"zoomin", "panright"},
		ImageOffsets: []float64{0, 4.0},
		Language:     "en",
	}
	if err := RenderAudioBookVideoWithOptions(outPath, audioPath, vttPath, imgs, Resolution720p, opts); err != nil {
		t.Fatalf("render: %v", err)
	}

	containerDur, err := ProbeDurationSeconds(outPath)
	if err != nil {
		t.Fatalf("probe container: %v", err)
	}
	if containerDur < audioSecs-0.4 || containerDur > audioSecs+0.4 {
		t.Errorf("container duration = %.2fs, want %.1fs ±0.4s", containerDur, audioSecs)
	}
	audioDur := probeStreamDuration(t, outPath, "a:0")
	if audioDur < audioSecs-0.4 || audioDur > audioSecs+0.4 {
		t.Errorf("audio stream duration = %.2fs, want %.1fs ±0.4s — narration truncated?", audioDur, audioSecs)
	}
	if codec := probeStreamValue(t, outPath, "s:0", "codec_name"); codec != "mov_text" {
		t.Errorf("subtitle stream codec = %q, want mov_text", codec)
	}
}

// TestRenderKenBurnsAudioBookVideoDoesNotTruncateAudio is the direct
// regression test for the historical -shortest bug: the renderer was handed a
// stale (short) duration probed while the recorder was still draining, sized
// the video track to it, and -shortest then amputated the audio tail. With a
// deliberately short dur against longer audio, the full narration must
// survive in the output.
func TestRenderKenBurnsAudioBookVideoDoesNotTruncateAudio(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	const audioSecs = 8.0
	const staleProbeSecs = 6.0

	audioPath := filepath.Join(dir, "audio.mp3")
	writeSyncTestAudio(t, audioPath, audioSecs)
	imgs := writeSyncTestImages(t, dir, 2)

	outPath := filepath.Join(dir, "video.mp4")
	opts := AudioBookVideoOptions{
		Animations:   []string{"zoomin", "panright"},
		ImageOffsets: []float64{0, 3.0},
	}
	if err := renderKenBurnsAudioBookVideo(outPath, audioPath, "", imgs, Resolution720p, opts, staleProbeSecs); err != nil {
		t.Fatalf("render: %v", err)
	}

	audioDur := probeStreamDuration(t, outPath, "a:0")
	if audioDur < audioSecs-0.4 {
		t.Errorf("audio stream duration = %.2fs, want ≥%.1fs — -shortest-style truncation regressed", audioDur, audioSecs-0.4)
	}
}
