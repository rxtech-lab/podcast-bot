package contentcreator

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

// syncTolerance bounds how far a persisted VTT cue may sit from the audio it
// describes in the hermetic sync test. The only wall-clock noise in a dry run
// is the LiveStream `-re` pacer's startup plus goroutine scheduling (well
// under 200ms in practice); 600ms gives margin both ways while staying far
// below the ~1.1s constant bias this test exists to catch regressing.
const syncTolerance = 600 * time.Millisecond

// fakeClipDuration is the exact play time of the fake TTS provider's embedded
// silence clip: 20736 bytes at the pipeline's 24000 B/s CBR contract.
const fakeClipDuration = 864 * time.Millisecond

type syncTestAgent struct {
	scripts []string
	call    int
	voice   tts.Voice
}

func (a *syncTestAgent) Name() string     { return "Narrator" }
func (a *syncTestAgent) SafeName() string { return "narrator" }
func (a *syncTestAgent) Role() agent.Role { return agent.RoleHost }
func (a *syncTestAgent) Side() string     { return "" }
func (a *syncTestAgent) Model() string    { return "static" }
func (a *syncTestAgent) Voice() tts.Voice {
	if a.voice.ShortName == "" {
		return tts.Voice{ShortName: "e2e-voice-1", Locale: "en-US"}
	}
	return a.voice
}
func (a *syncTestAgent) SetVoice(v tts.Voice) { a.voice = v }
func (a *syncTestAgent) Speak(ctx context.Context, p agent.SpeakPrompt) (*llm.Stream, error) {
	script := ""
	if a.call < len(a.scripts) {
		script = a.scripts[a.call]
	}
	a.call++
	return llm.NewStaticStream(script), nil
}
func (a *syncTestAgent) Listen(ctx context.Context, line agent.TranscriptLine) error { return nil }
func (a *syncTestAgent) Compress(ctx context.Context) error                          { return nil }

type scriptedPlanner struct {
	turns []*Turn
	next  int
}

func (p *scriptedPlanner) Next(ctx context.Context) (*Turn, bool) {
	if p.next >= len(p.turns) {
		return nil, false
	}
	t := p.turns[p.next]
	p.next++
	return t, true
}

// TestAudioBookPipeline_SubtitlesSyncWithRecordedAudio is the end-to-end
// regression test for the "captions run ahead of the audio" bug. It drives
// the real Pipeline.Run with the fake TTS provider (every sentence becomes
// the same 0.864s CBR clip), records the LiveStream to audio.mp3 exactly the
// way videojob does, and then checks the persisted VTT timeline against the
// actual (ffprobe-measured) audio. Sentence k must start at k×0.864s; the
// old cue math placed every cue a constant ~1.1-3.1s early, which trips the
// per-cue assertion below.
func TestAudioBookPipeline_SubtitlesSyncWithRecordedAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("realtime-paced pipeline test skipped in -short mode")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	// The audiobook QA inspector flags the all-silence fixture clip as a
	// dropout on every sentence, adding retries and log spam without
	// changing what is written. Disable it so the test isolates timing.
	savedInspect := inspectSynthAudioDropout
	inspectSynthAudioDropout = nil
	defer func() { inspectSynthAudioDropout = savedInspect }()

	// Shrink the post-producer tail hold; waitAudioDrained still waits for
	// the realtime pacer to play out the full recording.
	savedGrace := postProducerGrace
	postProducerGrace = 300 * time.Millisecond
	defer func() { postProducerGrace = savedGrace }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	live, err := audio.NewLiveStream(ctx, logger)
	if err != nil {
		t.Fatalf("NewLiveStream: %v", err)
	}

	// Each turn's script is one LLM stream; the sentence splitter cuts it on
	// punctuation. Every sentence stays under vttMaxRunesPerCue (44) so it
	// maps to exactly one cue, and above the splitter's MinChars (6) so it
	// is never coalesced: 6 sentences → 6 cues → 6 fake clips.
	narrator := &syncTestAgent{scripts: []string{
		"The quick fox jumps over the dog. A second sentence keeps it going. The third sentence closes turn one.",
		"Turn two begins with a new thought. Another line carries the middle. The final sentence ends the story.",
	}}
	const wantCues = 6

	turns := make([]*Turn, 2)
	for i := range turns {
		turns[i] = &Turn{
			ID:        i + 1,
			Phase:     agent.PhaseSetup,
			Speaker:   narrator,
			Directive: "story",
			Budget:    30 * time.Second,
			TextOut:   make(chan string, 16),
		}
	}

	outDir := t.TempDir()
	p := NewPipeline(Deps{
		Planner:     &scriptedPlanner{turns: turns},
		Tracker:     NewTracker(time.Minute),
		Registry:    &agent.Registry{Host: narrator},
		TTS:         tts.NewFake(),
		OutDir:      outDir,
		Send:        func(any) {},
		Log:         logger,
		Topic:       "sync test",
		Language:    "en-US",
		ContentType: config.ContentTypeAudioBook,
		AudioOnly:   true,
		Transcript:  NewTranscript(),
		LiveStream:  live,
	})

	// Record the LiveStream to audio.mp3 the same way videojob does: a
	// subscriber attached before Run so the file starts at stream t=0.
	audioPath := filepath.Join(outDir, "audio.mp3")
	recFile, err := os.Create(audioPath)
	if err != nil {
		t.Fatalf("create audio file: %v", err)
	}
	chunks, _ := live.Subscribe(1024)
	recDone := make(chan struct{})
	go func() {
		defer close(recDone)
		defer recFile.Close()
		for chunk := range chunks {
			if _, werr := recFile.Write(chunk); werr != nil {
				t.Errorf("audio recorder write failed: %v", werr)
				return
			}
		}
	}()

	if _, err := p.Run(ctx); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}
	if err := live.CloseInput(); err != nil {
		t.Fatalf("CloseInput: %v", err)
	}
	select {
	case <-recDone:
	case <-time.After(30 * time.Second):
		t.Fatal("audio recorder did not drain within 30s")
	}

	audioDur := probeDuration(t, audioPath)
	wantAudio := time.Duration(wantCues) * fakeClipDuration
	if diff := absDuration(audioDur - wantAudio); diff > 350*time.Millisecond {
		t.Fatalf("recorded audio duration = %v, want %v ±350ms (fake TTS produced unexpected bytes)", audioDur, wantAudio)
	}

	cues := p.SubtitleCues()
	if len(cues) != wantCues {
		t.Fatalf("cue count = %d, want %d: %+v", len(cues), wantCues, cues)
	}
	for k, c := range cues {
		want := time.Duration(k) * fakeClipDuration
		if diff := absDuration(c.Start - want); diff > syncTolerance {
			t.Errorf("cue %d starts at %v, want %v ±%v (text %q)", k, c.Start, want, syncTolerance, c.Text)
		}
		if dur := c.End - c.Start; absDuration(dur-fakeClipDuration) > 20*time.Millisecond {
			t.Errorf("cue %d duration = %v, want %v ±20ms (byte-rate contract broken?)", k, dur, fakeClipDuration)
		}
		if k > 0 && c.Start < cues[k-1].Start {
			t.Errorf("cue %d start %v precedes cue %d start %v — non-monotonic timeline", k, c.Start, k-1, cues[k-1].Start)
		}
	}

	// The core sync invariant: the caption track must end where the audio
	// ends. The shipped bug had the VTT ending seconds before the audio.
	lastEnd := cues[len(cues)-1].End
	if diff := absDuration(lastEnd - audioDur); diff > syncTolerance {
		t.Errorf("last cue ends at %v but audio is %v (Δ %v > %v) — captions out of sync with audio", lastEnd, audioDur, diff, syncTolerance)
	}

	// The persisted file must carry the same timeline the in-memory cues do.
	vttPath := filepath.Join(outDir, "subtitles.vtt")
	fileCues := parseVTTTimes(t, vttPath)
	if len(fileCues) != wantCues {
		t.Fatalf("subtitles.vtt cue count = %d, want %d", len(fileCues), wantCues)
	}
	if diff := absDuration(fileCues[len(fileCues)-1].end - audioDur); diff > syncTolerance {
		t.Errorf("subtitles.vtt last cue ends at %v but audio is %v (Δ %v)", fileCues[len(fileCues)-1].end, audioDur, diff)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// probeDuration returns the container duration of a media file via ffprobe.
func probeDuration(t *testing.T, path string) time.Duration {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("parse ffprobe duration %q: %v", out, err)
	}
	return time.Duration(secs * float64(time.Second))
}

type vttTimeSpan struct {
	start, end time.Duration
}

// parseVTTTimes extracts the "HH:MM:SS.mmm --> HH:MM:SS.mmm" cue lines from
// a WebVTT file.
func parseVTTTimes(t *testing.T, path string) []vttTimeSpan {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var spans []vttTimeSpan
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), " --> ")
		if len(parts) != 2 {
			continue
		}
		spans = append(spans, vttTimeSpan{
			start: parseVTTTimestamp(t, parts[0]),
			end:   parseVTTTimestamp(t, parts[1]),
		})
	}
	return spans
}

func parseVTTTimestamp(t *testing.T, s string) time.Duration {
	t.Helper()
	var h, m int
	var sec float64
	if _, err := parseHMS(s, &h, &m, &sec); err != nil {
		t.Fatalf("parse VTT timestamp %q: %v", s, err)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(sec*float64(time.Second))
}

func parseHMS(s string, h, m *int, sec *float64) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, strconv.ErrSyntax
	}
	var err error
	if *h, err = strconv.Atoi(parts[0]); err != nil {
		return 0, err
	}
	if *m, err = strconv.Atoi(parts[1]); err != nil {
		return 0, err
	}
	if *sec, err = strconv.ParseFloat(parts[2], 64); err != nil {
		return 0, err
	}
	return 3, nil
}
