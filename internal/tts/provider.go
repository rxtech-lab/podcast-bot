package tts

import (
	"context"
	"errors"
	"io"
)

// ErrSSMLUnsupported is returned by SynthesizeSSML on providers that don't
// accept raw SSML input. Callers MUST fall back to plain-text
// SynthesizeStream with a single voice when they see this — the multi-voice
// feature gracefully degrades instead of failing the turn.
var ErrSSMLUnsupported = errors.New("tts: provider does not support raw SSML")

// Provider is the abstraction every TTS backend (Azure, ElevenLabs, ...)
// satisfies. Implementations MUST return MP3 byte streams in the same format
// (audio-24khz-48kbitrate-mono-mp3) so the downstream LiveStream pacing,
// per-turn concat with `ffmpeg -c copy`, and AudioBytesPerSec subtitle
// alignment work without provider-specific branches.
type Provider interface {
	// FetchVoices lists voices the provider can render. The `language` hint
	// (e.g. "en-US", "zh-CN") may be used by the provider to tag returned
	// voices' Locale so the agent voice picker treats them as eligible —
	// useful for multilingual providers like ElevenLabs.
	FetchVoices(ctx context.Context, language string) ([]Voice, error)

	// SynthesizeStream returns a chunked MP3 reader for `text` rendered with
	// `voice` in `lang`. The caller MUST Close the returned reader.
	SynthesizeStream(ctx context.Context, voice, text, lang string) (io.ReadCloser, error)

	// SynthesizeSSML synthesises a fully-formed SSML envelope (caller is
	// responsible for building it — see BuildMultiVoiceSSML). Returns
	// ErrSSMLUnsupported on backends that don't expose raw SSML input.
	// The caller MUST Close the returned reader on success.
	SynthesizeSSML(ctx context.Context, ssml string) (io.ReadCloser, error)
}
