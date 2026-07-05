package contentcreator

import (
	"testing"
	"time"
)

// TestVTTWriterTimelineMapper pins the late-mapping architecture for
// audio-only cues: cues are stored on the TTS byte timeline and exported
// through the mixer's TTS→output map, so bed-only gaps the session
// accumulated shift every cue after them.
func TestVTTWriterTimelineMapper(t *testing.T) {
	w := newVTTWriter()
	w.Append("first sentence", 0, 2*time.Second)
	w.Append("second sentence", 2*time.Second, 2*time.Second)

	// No mapper: identity export.
	cues := w.Cues()
	if len(cues) != 2 || cues[0].Start != 0 || cues[1].Start != 2*time.Second {
		t.Fatalf("identity export mangled: %+v", cues)
	}

	// Mapper mimicking a mixer map: 10s music pre-roll, then a 3s
	// bed-only gap after the first 2s of TTS.
	w.SetTimelineMapper(func(d time.Duration) time.Duration {
		if d >= 2*time.Second {
			return d + 13*time.Second
		}
		return d + 10*time.Second
	})
	cues = w.Cues()
	if cues[0].Start != 10*time.Second {
		t.Errorf("cue 0 start = %v, want 10s (pre-roll applied)", cues[0].Start)
	}
	if cues[1].Start != 15*time.Second {
		t.Errorf("cue 1 start = %v, want 15s (pre-roll + gap applied)", cues[1].Start)
	}
	if cues[1].End != 17*time.Second {
		t.Errorf("cue 1 end = %v, want 17s", cues[1].End)
	}
}

// TestPipeline_RecordedPosIdentityWithoutMixer pins that dry audio-only
// runs (recorded file == raw TTS bytes) use the TTS timeline as-is.
func TestPipeline_RecordedPosIdentityWithoutMixer(t *testing.T) {
	p := NewPipeline(Deps{AudioOnly: true})
	if got := p.recordedPos(7 * time.Second); got != 7*time.Second {
		t.Fatalf("recordedPos without mixer = %v, want identity", got)
	}
}
