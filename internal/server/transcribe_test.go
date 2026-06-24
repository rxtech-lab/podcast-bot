package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTranscribeSample exercises the real Gemini transcription path end-to-end
// against the committed sample clip (assets/testspeech.m4a, which says "test
// test"). It runs only when GEMINI_API_KEY is set — CI provides it from a GitHub
// secret; locally without the key it skips, so `go test ./...` stays
// offline-friendly. The model comes from GEMINI_TRANSCRIBE_MODEL (same var the
// server reads), falling back to the package default.
func TestTranscribeSample(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set; skipping live transcription test")
	}
	model := strings.TrimSpace(os.Getenv("GEMINI_TRANSCRIBE_MODEL"))
	if model == "" {
		model = defaultTranscribeModel
	}

	audio, err := os.ReadFile(filepath.Join("..", "..", "assets", "testspeech.m4a"))
	if err != nil {
		t.Fatalf("read sample audio: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	text, err := geminiTranscribe(ctx, apiKey, model, audio, geminiAudioMIME("testspeech.m4a"))
	if err != nil {
		t.Fatalf("transcribe (%s): %v", model, err)
	}
	t.Logf("model=%s transcript=%q", model, text)
	if strings.TrimSpace(text) == "" {
		t.Fatalf("empty transcript from %s", model)
	}
	// The clip is a person saying "test test"; assert the keyword survived rather
	// than an exact match, since the model may vary casing/punctuation.
	if !strings.Contains(strings.ToLower(text), "test") {
		t.Fatalf("transcript %q does not contain expected word %q", text, "test")
	}
}
