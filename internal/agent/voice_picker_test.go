package agent

import (
	"context"
	"log/slog"
	"sort"
	"testing"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

// stubAgent is the minimal Agent for voice-assignment tests: only
// Name/Voice/SetVoice matter to the picker.
type stubAgent struct {
	name  string
	voice tts.Voice
}

func (s *stubAgent) Name() string                                            { return s.name }
func (s *stubAgent) SafeName() string                                        { return s.name }
func (s *stubAgent) Role() Role                                              { return "" }
func (s *stubAgent) Side() string                                            { return "" }
func (s *stubAgent) Model() string                                           { return "" }
func (s *stubAgent) Voice() tts.Voice                                        { return s.voice }
func (s *stubAgent) SetVoice(v tts.Voice)                                    { s.voice = v }
func (s *stubAgent) Speak(context.Context, SpeakPrompt) (*llm.Stream, error) { return nil, nil }
func (s *stubAgent) Listen(context.Context, TranscriptLine) error            { return nil }
func (s *stubAgent) Compress(context.Context) error                          { return nil }

func TestAssignVoicesHonorsOverrides(t *testing.T) {
	voices := []tts.Voice{
		{ShortName: "en-US-AvaNeural", Locale: "en-US", Gender: "Female", VoiceType: "Neural"},
		{ShortName: "en-US-GuyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
		{ShortName: "zh-CN-XiaochenNeural", Locale: "zh-CN", Gender: "Female", VoiceType: "Neural"},
	}
	alice := &stubAgent{name: "Alice"}
	bob := &stubAgent{name: "Bob"}
	log := slog.New(slog.DiscardHandler)

	// Alice's override is cross-locale (zh-CN voice on an en-US plan, matched
	// case-insensitively against the full list); Bob has none and auto-picks.
	AssignVoices(voices, []Agent{alice, bob}, "en-US", 1, log,
		map[string]string{"Alice": "zh-cn-xiaochenneural"}, nil)

	if got := alice.Voice().ShortName; got != "zh-CN-XiaochenNeural" {
		t.Fatalf("Alice voice = %q, want override zh-CN-XiaochenNeural", got)
	}
	if got := bob.Voice().ShortName; got == "" || got == "zh-CN-XiaochenNeural" {
		t.Fatalf("Bob voice = %q, want an automatic non-override pick", got)
	}

	// An override naming a voice absent from the list falls back to auto-pick.
	carol := &stubAgent{name: "Carol"}
	AssignVoices(voices, []Agent{carol}, "en-US", 1, log,
		map[string]string{"Carol": "en-US-DoesNotExistNeural"}, nil)
	if got := carol.Voice().ShortName; got == "" || got == "en-US-DoesNotExistNeural" {
		t.Fatalf("Carol voice = %q, want automatic fallback pick", got)
	}
}

func TestAssignVoicesRecyclesGenderMatchOverFreshMismatch(t *testing.T) {
	voices := []tts.Voice{
		{ShortName: "en-US-AvaNeural", Locale: "en-US", Gender: "Female", VoiceType: "Neural"},
		{ShortName: "en-US-GuyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
		{ShortName: "en-US-TonyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
	}
	first := &stubAgent{name: "Speaker One"}
	second := &stubAgent{name: "Speaker Two"}
	log := slog.New(slog.DiscardHandler)

	genders := map[string]string{"Speaker One": "female", "Speaker Two": "female"}
	AssignVoices(voices, []Agent{first, second}, "en-US", 1, log, nil, genders)

	if got := first.Voice().Gender; got != "Female" {
		t.Fatalf("first agent gender = %q, want Female", got)
	}
	// The only female voice is taken, but a female agent must still never get
	// a fresh male voice — the female one is recycled instead.
	if got := second.Voice().ShortName; got != "en-US-AvaNeural" {
		t.Fatalf("second agent voice = %q, want recycled en-US-AvaNeural", got)
	}
}

func TestAssignVoicesExplicitGenderBeatsNameInference(t *testing.T) {
	voices := []tts.Voice{
		{ShortName: "en-US-AvaNeural", Locale: "en-US", Gender: "Female", VoiceType: "Neural"},
		{ShortName: "en-US-GuyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
	}
	// "Bob" infers male from the name table; the explicit plan gender wins.
	bob := &stubAgent{name: "Bob"}
	log := slog.New(slog.DiscardHandler)

	AssignVoices(voices, []Agent{bob}, "en-US", 1, log, nil,
		map[string]string{"Bob": "female"})

	if got := bob.Voice().Gender; got != "Female" {
		t.Fatalf("Bob voice gender = %q, want Female (explicit gender wins)", got)
	}
}

func TestAssignCharacterVoicesFallsBackToNameGender(t *testing.T) {
	voices := []tts.Voice{
		{ShortName: "en-US-GuyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
		{ShortName: "en-US-AvaNeural", Locale: "en-US", Gender: "Female", VoiceType: "Neural"},
	}
	log := slog.New(slog.DiscardHandler)

	// No plan-authored gender for Linda; the name table infers female.
	out := AssignCharacterVoices(voices, []string{"Linda"}, map[string]string{},
		"en-US", 1, nil, log)

	if got := out["Linda"]; got != "en-US-AvaNeural" {
		t.Fatalf("Linda voice = %q, want en-US-AvaNeural via name inference", got)
	}
}

func TestAssignCharacterVoicesRecyclesGenderMatch(t *testing.T) {
	voices := []tts.Voice{
		{ShortName: "en-US-AvaNeural", Locale: "en-US", Gender: "Female", VoiceType: "Neural"},
		{ShortName: "en-US-GuyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
		{ShortName: "en-US-TonyNeural", Locale: "en-US", Gender: "Male", VoiceType: "Neural"},
	}
	log := slog.New(slog.DiscardHandler)

	genders := map[string]string{"Queen": "female", "Princess": "female"}
	out := AssignCharacterVoices(voices, []string{"Queen", "Princess"}, genders,
		"en-US", 1, nil, log)

	for name, want := range map[string]string{"Queen": "en-US-AvaNeural", "Princess": "en-US-AvaNeural"} {
		if got := out[name]; got != want {
			t.Fatalf("%s voice = %q, want %q (never a fresh male voice)", name, got, want)
		}
	}
}

func TestCinematicVoicesRankAboveGenericHD(t *testing.T) {
	voices := []tts.Voice{
		{ShortName: "zh-CN-OtherDragonHDLatestNeural", Locale: "zh-CN"},
		{ShortName: "zh-CN-YunyeDragonHDFlashLatestNeural", Locale: "zh-CN"},
		{ShortName: "zh-CN-YunfanDragonHDLatestNeural", Locale: "zh-CN"},
		{ShortName: "zh-CN-XiaochenDragonHDLatestNeural", Locale: "zh-CN"},
		{ShortName: "zh-CN-XiaochenDragonHDFlashLatestNeural", Locale: "zh-CN"},
	}

	sort.SliceStable(voices, func(i, j int) bool {
		return voiceScore(voices[i], "zh-CN") > voiceScore(voices[j], "zh-CN")
	})

	want := []string{
		"zh-CN-XiaochenDragonHDFlashLatestNeural",
		"zh-CN-XiaochenDragonHDLatestNeural",
		"zh-CN-YunfanDragonHDLatestNeural",
		"zh-CN-YunyeDragonHDFlashLatestNeural",
	}
	for i, name := range want {
		if voices[i].ShortName != name {
			t.Fatalf("rank %d = %s, want %s", i, voices[i].ShortName, name)
		}
	}
}
