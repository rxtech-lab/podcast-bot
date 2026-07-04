package video

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAudioBookImageStarts(t *testing.T) {
	// Valid monotonic offsets pass through, with the first pinned to 0.
	got := audioBookImageStarts([]float64{1.2, 30, 61.5}, 3, 120)
	if got[0] != 0 || got[1] != 30 || got[2] != 61.5 {
		t.Errorf("valid offsets mangled: %v", got)
	}
	// Non-monotonic → even split.
	got = audioBookImageStarts([]float64{0, 50, 20}, 3, 90)
	if got[0] != 0 || got[1] != 30 || got[2] != 60 {
		t.Errorf("non-monotonic offsets should fall back to even split: %v", got)
	}
	// Length mismatch → even split.
	got = audioBookImageStarts([]float64{0, 10}, 4, 100)
	for i, want := range []float64{0, 25, 50, 75} {
		if got[i] != want {
			t.Errorf("length-mismatch fallback: got %v", got)
			break
		}
	}
	// Offset beyond duration → even split.
	got = audioBookImageStarts([]float64{0, 200}, 2, 100)
	if got[1] != 50 {
		t.Errorf("out-of-range offset should fall back: %v", got)
	}
	// First image emitted deep into the audio → fall back rather than
	// opening on a long hold of nothing.
	got = audioBookImageStarts([]float64{80, 90}, 2, 100)
	if got[0] != 0 || got[1] != 50 {
		t.Errorf("late first offset should fall back: %v", got)
	}
	// Nil offsets → even split.
	got = audioBookImageStarts(nil, 2, 10)
	if got[0] != 0 || got[1] != 5 {
		t.Errorf("nil offsets: %v", got)
	}
}

func TestAudioBookImageMovements(t *testing.T) {
	moves := audioBookImageMovements([]string{"zoomin", "panleft", "bogus"}, 5)
	if moves[0].Kind != MoveZoomIn || moves[1].Kind != MovePanLeft {
		t.Errorf("planned tokens not honoured: %v %v", moves[0].Kind, moves[1].Kind)
	}
	if moves[2].Kind != MoveStall {
		t.Errorf("unknown token should collapse to stall, got %v", moves[2].Kind)
	}
	// Entries past the plan cycle through the fallback palette.
	if moves[3].Kind == "" || moves[4].Kind == "" {
		t.Errorf("unplanned entries should get fallback moves: %v %v", moves[3].Kind, moves[4].Kind)
	}
}

func TestAudioBookConversationBackgroundIndicesFollowImageOffsets(t *testing.T) {
	segments := []audioBookConversationSegment{
		{Seconds: 4},
		{Seconds: 4},
		{Seconds: 4},
		{Seconds: 4},
		{Seconds: 4},
	}
	starts := []float64{0, 10, 18}

	got := audioBookConversationBackgroundIndices(segments, starts)
	want := []int{0, 0, 0, 1, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("background indices = %v, want %v", got, want)
		}
	}
}

func TestKenBurnsProgressHoldsAfterMove(t *testing.T) {
	starts := []float64{0, 30}
	// Move plays over kenBurnsMaxMoveSeconds then holds at 1.
	if p := kenBurnsProgress(25, 0, starts, 60); p != 1 {
		t.Errorf("expected held progress 1 after max move, got %v", p)
	}
	if p := kenBurnsProgress(0, 0, starts, 60); p != 0 {
		t.Errorf("expected progress 0 at segment start, got %v", p)
	}
	// Mid-move progress is strictly between 0 and 1.
	if p := kenBurnsProgress(10, 0, starts, 60); p <= 0 || p >= 1 {
		t.Errorf("expected mid progress in (0,1), got %v", p)
	}
}

// Smoke test: 3 tiny PNGs + 2s of silence through the full Ken Burns pipe,
// then ffprobe the result. Skipped when ffmpeg isn't installed.
func TestRenderKenBurnsAudioBookVideoSmoke(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	colors := []color.RGBA{{0xff, 0, 0, 0xff}, {0, 0xff, 0, 0xff}, {0, 0, 0xff, 0xff}}
	imgs := make([]string, len(colors))
	for i, c := range colors {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		for y := 0; y < 180; y++ {
			for x := 0; x < 320; x++ {
				img.Set(x, y, c)
			}
		}
		p := filepath.Join(dir, "narration-v"+string(rune('0'+i))+".png")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		imgs[i] = p
	}
	audioPath := filepath.Join(dir, "audio.mp3")
	if out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono",
		"-t", "2", "-c:a", "libmp3lame", audioPath).CombinedOutput(); err != nil {
		t.Fatalf("generate silence: %v (%s)", err, out)
	}

	outPath := filepath.Join(dir, "video.mp4")
	opts := AudioBookVideoOptions{
		Animations:   []string{"zoomin", "panright", "zoomout"},
		ImageOffsets: []float64{0, 0.7, 1.4},
	}
	if err := renderKenBurnsAudioBookVideo(outPath, audioPath, "", imgs, Resolution720p, opts, 2.0); err != nil {
		t.Fatalf("render: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output missing/empty: %v", err)
	}
	dur, err := probeDurationSeconds(outPath)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if dur < 1.5 || dur > 3.0 {
		t.Errorf("unexpected output duration %.2fs, want ~2s", dur)
	}
}
