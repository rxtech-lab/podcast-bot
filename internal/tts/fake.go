package tts

import (
	"bytes"
	"context"
	_ "embed"
	"io"
)

// silenceMP3 is a ~0.8s clip of silence encoded as audio-24khz-48kbitrate-mono-mp3
// — the exact format every Provider must emit so the pipeline's per-turn
// `ffmpeg -c copy` concat works. In E2E mode every synthesized turn returns this
// same clip, so all segments share identical codec params.
//
//go:embed silence.mp3
var silenceMP3 []byte

// FakeProvider is a Provider used only in E2E mode. It never calls a real speech
// backend: FetchVoices returns a small fixed roster and every synthesis request
// returns the embedded silent MP3. This keeps podcast generation fully hermetic
// and instant while still exercising the real audio-mixing pipeline.
type FakeProvider struct{}

// NewFake builds the E2E fake speech provider.
func NewFake() *FakeProvider { return &FakeProvider{} }

// FetchVoices returns a fixed roster tagged with the requested locale so the
// agent voice picker always finds eligible voices for any topic language.
func (f *FakeProvider) FetchVoices(ctx context.Context, language string) ([]Voice, error) {
	locale := language
	if locale == "" {
		locale = "en-US"
	}
	mk := func(name, gender string) Voice {
		return Voice{ShortName: name, Locale: locale, Gender: gender, VoiceType: "Neural", LocaleName: locale}
	}
	return []Voice{
		mk("e2e-voice-1", "Female"),
		mk("e2e-voice-2", "Male"),
		mk("e2e-voice-3", "Female"),
		mk("e2e-voice-4", "Male"),
		mk("e2e-voice-5", "Female"),
		mk("e2e-voice-6", "Male"),
	}, nil
}

// SynthesizeStream returns the embedded silent clip regardless of voice/text.
func (f *FakeProvider) SynthesizeStream(ctx context.Context, voice, text, lang string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(silenceMP3)), nil
}

// SynthesizeSSML returns the embedded silent clip for any SSML envelope.
func (f *FakeProvider) SynthesizeSSML(ctx context.Context, ssml string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(silenceMP3)), nil
}
