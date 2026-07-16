// Package stt normalizes speech-to-text providers behind one interface. Both
// implementations (Azure fast transcription, Gemini) return the same
// Transcript shape — diarized phrases with millisecond offsets and, when the
// provider supplies them, word-level timings — so downstream cue building and
// plan construction never branch on the provider.
package stt

import "context"

// Word is one word (or CJK glyph) with its position on the audio timeline.
type Word struct {
	Text       string
	OffsetMS   int64
	DurationMS int64
}

// Phrase is one diarized utterance. Words is optional: Azure fast
// transcription supplies it, Gemini's prompted transcription does not, and
// cue building falls back to proportional splitting when it is empty.
type Phrase struct {
	// Speaker is the 1-based diarization speaker id; 0 means unknown.
	Speaker    int
	OffsetMS   int64
	DurationMS int64
	Text       string
	// Locale is the detected BCP-47 language of this phrase when the provider
	// reports one (Azure does, Gemini does not).
	Locale string
	Words  []Word
}

// Transcript is a provider-neutral transcription result.
type Transcript struct {
	DurationMS int64
	Phrases    []Phrase
}

// Request describes one transcription job. AudioURL is a presigned GET the
// provider can fetch directly (Azure) or that we download and re-upload
// (Gemini); it must stay valid for the duration of the call.
type Request struct {
	AudioURL    string
	MIME        string
	SizeBytes   int64
	MaxSpeakers int
	// Language is a BCP-47 hint (e.g. "zh-CN"); empty means auto-detect.
	Language string
}

// Provider transcribes audio with speaker diarization.
type Provider interface {
	Name() string
	Transcribe(ctx context.Context, req Request) (*Transcript, error)
}

// Provider name constants, also used as the admin-config stt_provider values.
const (
	ProviderGemini = "gemini"
	ProviderAzure  = "azure"
)

// ClampMaxSpeakers bounds a user-supplied speaker count to the range Azure
// diarization accepts (2–35); Gemini gets the same bound for consistency.
func ClampMaxSpeakers(n int) int {
	if n < 2 {
		return 2
	}
	if n > 35 {
		return 35
	}
	return n
}
