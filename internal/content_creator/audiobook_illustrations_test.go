package contentcreator

import "testing"

func TestAudioBookIllustrationTimeline(t *testing.T) {
	o := &Orchestrator{}
	o.SetAudioBookImages([]AudioBookImage{
		{Beat: 0, URL: "https://img/0.webp", Key: "audiobooks/x/image-1.webp", Caption: " opening "},
		{Beat: 1, URL: "https://img/1.webp", Key: "audiobooks/x/image-2.webp", Caption: "bar"},
		{Beat: 2, URL: "https://img/2.webp", Key: "audiobooks/x/image-3.webp", Caption: "star"},
		{Beat: 3, URL: "https://img/3.webp", Caption: "never fired"},
	})
	// Beat 0 fires at the (slightly non-zero) music pre-roll offset; beats 1-2
	// fire mid-run; beat 3 never fires and must be omitted.
	o.recordAudioBookImageOffset(0, 1.2)
	o.recordAudioBookImageOffset(2, 78.694)
	o.recordAudioBookImageOffset(1, 36.406)

	cues := o.AudioBookIllustrationTimeline()
	if len(cues) != 3 {
		t.Fatalf("cues = %d, want 3", len(cues))
	}
	// Earliest cue clamps to 0 so the opening image covers playback from t=0.
	if cues[0].StartMS != 0 || cues[0].ImageURL != "https://img/0.webp" {
		t.Fatalf("cues[0] = %+v, want opening at 0", cues[0])
	}
	if cues[0].Caption != "opening" {
		t.Fatalf("caption not trimmed: %q", cues[0].Caption)
	}
	if cues[1].StartMS != 36406 || cues[2].StartMS != 78694 {
		t.Fatalf("cue order/offsets wrong: %+v", cues[1:])
	}
	if cues[1].ImageKey != "audiobooks/x/image-2.webp" {
		t.Fatalf("image key missing: %+v", cues[1])
	}
}

func TestAudioBookIllustrationTimelineEmpty(t *testing.T) {
	o := &Orchestrator{}
	if got := o.AudioBookIllustrationTimeline(); got != nil {
		t.Fatalf("no offsets should yield nil, got %+v", got)
	}
	o.SetAudioBookImages([]AudioBookImage{{Beat: 0, URL: "https://img/0.webp"}})
	if got := o.AudioBookIllustrationTimeline(); got != nil {
		t.Fatalf("images without fired offsets should yield nil, got %+v", got)
	}
}
