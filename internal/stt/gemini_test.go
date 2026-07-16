package stt

import "testing"

func TestDecodeGeminiResponse(t *testing.T) {
	raw := `{"candidates":[{"content":{"parts":[{"text":"{\"durationMs\":60000,\"phrases\":[{\"speaker\":1,\"offsetMs\":0,\"durationMs\":3000,\"text\":\"Hello there, welcome.\"},{\"speaker\":2,\"offsetMs\":3000,\"durationMs\":2000,\"text\":\"谢谢，很高兴来到这里。\"}]}"}]}}]}`
	tr, err := decodeGeminiResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tr.DurationMS != 60000 || len(tr.Phrases) != 2 {
		t.Fatalf("decode wrong: %#v", tr)
	}
	if tr.Phrases[0].Speaker != 1 || tr.Phrases[1].Text != "谢谢，很高兴来到这里。" {
		t.Fatalf("phrases wrong: %#v", tr.Phrases)
	}
	if len(tr.Phrases[0].Words) != 0 {
		t.Fatalf("gemini phrases must have no word timings")
	}
	cues := SentenceCues(tr)
	if len(cues) != 4 {
		t.Fatalf("expected 4 cues (comma splits), got %d: %#v", len(cues), cues)
	}
}

func TestDecodeGeminiResponseEmpty(t *testing.T) {
	if _, err := decodeGeminiResponse([]byte(`{"candidates":[]}`)); err == nil {
		t.Fatal("expected error for empty response")
	}
}

func TestGeminiModelSupportsAudio(t *testing.T) {
	gen := []string{"generateContent", "countTokens"}
	cases := map[bool][]struct {
		id      string
		methods []string
	}{
		true: {
			{"gemini-2.5-flash", gen},
			{"gemini-2.5-pro", gen},
			{"gemini-3.5-flash", gen},
		},
		false: {
			{"gemini-embedding-001", gen},
			{"gemini-2.5-flash-preview-tts", gen},
			{"gemini-2.5-flash-native-audio-dialog", gen},
			{"gemini-2.5-flash-image", gen},
			{"gemini-2.0-flash-live-001", gen},
			{"gemma-3-27b-it", gen},
			{"veo-2.0-generate-001", gen},
			{"gemini-2.5-flash", []string{"embedContent"}}, // no generateContent
		},
	}
	for want, list := range cases {
		for _, c := range list {
			if got := geminiModelSupportsAudio(c.id, c.methods); got != want {
				t.Errorf("geminiModelSupportsAudio(%q) = %v, want %v", c.id, got, want)
			}
		}
	}
}
