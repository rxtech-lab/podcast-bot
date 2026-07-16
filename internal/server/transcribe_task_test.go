package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/stt"
)

type stubSTTProvider struct {
	name       string
	transcript *stt.Transcript
	err        error
	calls      int
}

func (p *stubSTTProvider) Name() string { return p.name }

func (p *stubSTTProvider) Transcribe(context.Context, stt.Request) (*stt.Transcript, error) {
	p.calls++
	return p.transcript, p.err
}

func validTimedTranscript() *stt.Transcript {
	return &stt.Transcript{
		DurationMS: 20_000,
		Phrases: []stt.Phrase{
			{OffsetMS: 1_000, DurationMS: 2_000, Text: "第一句。"},
			{OffsetMS: 4_000, DurationMS: 2_000, Text: "第二句。"},
		},
	}
}

func TestTranscribeWithTimingFallbackUsesWordTimedProvider(t *testing.T) {
	invalid := validTimedTranscript()
	invalid.Phrases = append(invalid.Phrases,
		stt.Phrase{OffsetMS: 2_500, DurationMS: 1_000, Text: "倒退的时间。"})
	gemini := &stubSTTProvider{name: stt.ProviderGemini, transcript: invalid}
	azure := &stubSTTProvider{name: stt.ProviderAzure, transcript: validTimedTranscript()}

	got, used, err := transcribeWithTimingFallback(context.Background(), gemini, azure, stt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if used != azure || got != azure.transcript || gemini.calls != 1 || azure.calls != 1 {
		t.Fatalf("fallback result = provider %q transcript=%p calls=%d/%d", used.Name(), got, gemini.calls, azure.calls)
	}
}

func TestTranscribeWithTimingFallbackRejectsInvalidTimelineWithoutFallback(t *testing.T) {
	invalid := validTimedTranscript()
	invalid.Phrases[1].OffsetMS = 500
	gemini := &stubSTTProvider{name: stt.ProviderGemini, transcript: invalid}

	_, used, err := transcribeWithTimingFallback(context.Background(), gemini, nil, stt.Request{})
	if err == nil || used != gemini || !strings.Contains(err.Error(), "invalid transcript timing") {
		t.Fatalf("invalid timing should fail with the primary provider, used=%v err=%v", used, err)
	}
}

func TestTranscribeWithTimingFallbackLeavesProviderErrorsForQueueRetry(t *testing.T) {
	want := errors.New("temporary Gemini failure")
	gemini := &stubSTTProvider{name: stt.ProviderGemini, err: want}
	azure := &stubSTTProvider{name: stt.ProviderAzure, transcript: validTimedTranscript()}

	_, used, err := transcribeWithTimingFallback(context.Background(), gemini, azure, stt.Request{})
	if !errors.Is(err, want) || used != gemini || azure.calls != 0 {
		t.Fatalf("provider error should remain on the primary retry path, used=%v err=%v fallbackCalls=%d", used, err, azure.calls)
	}
}
