package stt

import "testing"

// azureFixture is a trimmed real-shaped fast-transcription response.
const azureFixture = `{
  "durationMilliseconds": 182439,
  "combinedPhrases": [{"channel": 0, "text": "Good afternoon. 欢迎大家。"}],
  "phrases": [
    {
      "channel": 0, "speaker": 1, "offsetMilliseconds": 960, "durationMilliseconds": 640,
      "text": "Good afternoon.", "locale": "en-US", "confidence": 0.93,
      "words": [
        {"text": "Good", "offsetMilliseconds": 960, "durationMilliseconds": 240},
        {"text": "afternoon.", "offsetMilliseconds": 1200, "durationMilliseconds": 400}
      ]
    },
    {
      "channel": 0, "speaker": 2, "offsetMilliseconds": 10080, "durationMilliseconds": 24920,
      "text": "欢迎大家。", "locale": "zh-CN", "confidence": 0.9,
      "words": [
        {"text": "欢", "offsetMilliseconds": 10080, "durationMilliseconds": 120},
        {"text": "迎", "offsetMilliseconds": 10200, "durationMilliseconds": 120},
        {"text": "大", "offsetMilliseconds": 10320, "durationMilliseconds": 120},
        {"text": "家。", "offsetMilliseconds": 10440, "durationMilliseconds": 120}
      ]
    }
  ]
}`

func TestDecodeAzureFastResponse(t *testing.T) {
	tr, err := decodeAzureFastResponse([]byte(azureFixture))
	if err != nil {
		t.Fatal(err)
	}
	if tr.DurationMS != 182439 {
		t.Fatalf("duration = %d", tr.DurationMS)
	}
	if len(tr.Phrases) != 2 {
		t.Fatalf("phrases = %d", len(tr.Phrases))
	}
	p := tr.Phrases[0]
	if p.Speaker != 1 || p.OffsetMS != 960 || p.DurationMS != 640 || p.Text != "Good afternoon." {
		t.Fatalf("phrase 0 wrong: %#v", p)
	}
	if len(p.Words) != 2 || p.Words[1].Text != "afternoon." || p.Words[1].OffsetMS != 1200 {
		t.Fatalf("words wrong: %#v", p.Words)
	}
	if tr.Phrases[1].Speaker != 2 {
		t.Fatalf("phrase 1 speaker wrong: %#v", tr.Phrases[1])
	}

	cues := SentenceCues(tr)
	if len(cues) != 2 {
		t.Fatalf("expected 2 sentence cues, got %d: %#v", len(cues), cues)
	}
	if cues[0].Text != "Good afternoon." || cues[1].Text != "欢迎大家。" {
		t.Fatalf("cue text wrong: %#v", cues)
	}
}

func TestNewAzureFastEndpointDerivation(t *testing.T) {
	a := NewAzureFast("", "eastus", "key")
	if a.endpoint != "https://eastus.api.cognitive.microsoft.com" {
		t.Fatalf("derived endpoint wrong: %q", a.endpoint)
	}
	b := NewAzureFast("https://myresource.cognitiveservices.azure.com/", "eastus", "key")
	if b.endpoint != "https://myresource.cognitiveservices.azure.com" {
		t.Fatalf("explicit endpoint wrong: %q", b.endpoint)
	}
}
