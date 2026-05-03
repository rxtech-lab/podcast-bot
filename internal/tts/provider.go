package tts

import (
	"context"
	"io"
)

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
}
