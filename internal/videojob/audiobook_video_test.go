package videojob

import (
	"reflect"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

func TestSnapshotAudioBookVideoImagesDropsUnfiredBeats(t *testing.T) {
	imgs := []contentcreator.AudioBookImage{
		{Beat: 0, Path: "/x/v0.png", Animation: "stall"},
		{Beat: 1, Path: "/x/v1.png", Animation: "zoomin"},
		{Beat: 2, Path: "/x/v2.png", Animation: "panleft"},
		{Beat: 3, Path: "/x/v3.png", Animation: "zoomout"},
	}
	// Chapter-limited run: markers for beats 0/1/3 fired, beat 2 never did.
	offsets := map[int]float64{0: 0, 1: 33.2, 3: 61.9}
	paths, anims, starts, beats, skipped := snapshotAudioBookVideoImages(imgs, offsets)
	if !reflect.DeepEqual(paths, []string{"/x/v0.png", "/x/v1.png", "/x/v3.png"}) {
		t.Fatalf("paths = %v", paths)
	}
	if !reflect.DeepEqual(anims, []string{"stall", "zoomin", "zoomout"}) {
		t.Fatalf("anims = %v", anims)
	}
	if !reflect.DeepEqual(starts, []float64{0, 33.2, 61.9}) {
		t.Fatalf("starts = %v", starts)
	}
	if !reflect.DeepEqual(beats, []int{0, 1, 3}) {
		t.Fatalf("beats = %v", beats)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}

func TestSnapshotAudioBookVideoImagesNoOffsetsKeepsAll(t *testing.T) {
	imgs := []contentcreator.AudioBookImage{
		{Beat: 0, Path: "/x/v0.png"},
		{Beat: 1, Path: "/x/v1.png"},
		{Beat: 2, Path: ""},
	}
	paths, _, starts, beats, skipped := snapshotAudioBookVideoImages(imgs, nil)
	if len(paths) != 2 || starts != nil || skipped != 0 {
		t.Fatalf("legacy fallback: paths=%v starts=%v skipped=%d", paths, starts, skipped)
	}
	if !reflect.DeepEqual(beats, []int{0, 1}) {
		t.Fatalf("beats = %v", beats)
	}
}

func TestAudioBookVideoOptionsCarryPodcastLanguage(t *testing.T) {
	opts := audioBookVideoOptions(&config.DebateTopic{
		Title:    "History",
		Language: "zh-TW",
	}, nil, nil)
	if opts.Language != "zh-TW" {
		t.Fatalf("Language = %q, want zh-TW", opts.Language)
	}
}
