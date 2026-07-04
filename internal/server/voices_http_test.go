package server

import (
	"strings"
	"testing"
)

func TestVoicePreviewSampleTextUsesPlanLanguage(t *testing.T) {
	tests := []struct {
		language string
		want     string
	}{
		{language: "zh-CN", want: "这是"},
		{language: "zh-Hans", want: "这是"},
		{language: "zh-TW", want: "這是"},
		{language: "zh-Hant-HK", want: "這是"},
		{language: "ja-JP", want: "こんにちは"},
		{language: "ko-KR", want: "안녕하세요"},
		{language: "es-ES", want: "Hola"},
		{language: "fr-FR", want: "Bonjour"},
		{language: "de-DE", want: "Hallo"},
		{language: "en-US", want: "Hello"},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			if got := voicePreviewSampleText(tt.language); !strings.Contains(got, tt.want) {
				t.Fatalf("voicePreviewSampleText(%q) = %q, want substring %q", tt.language, got, tt.want)
			}
		})
	}
}

func TestVoicePreviewObjectNameIncludesSampleTextHash(t *testing.T) {
	voice := "zh-CN-XiaochenNeural"
	language := "zh-CN"
	chinese := voicePreviewSampleText(language)
	english := voicePreviewSampleText("en-US")

	chineseKey := voicePreviewObjectName(voice, language, chinese)
	englishKey := voicePreviewObjectName(voice, language, english)
	if chineseKey == englishKey {
		t.Fatalf("keys should differ when sample text differs: %q", chineseKey)
	}
	if !voicePreviewKeyMatchesText(chineseKey, chinese) {
		t.Fatalf("voicePreviewKeyMatchesText(%q, chinese) = false, want true", chineseKey)
	}
	if voicePreviewKeyMatchesText(chineseKey, english) {
		t.Fatalf("voicePreviewKeyMatchesText(%q, english) = true, want false", chineseKey)
	}
	if voicePreviewKeyMatchesText("voice-previews/zh-CN-XiaochenNeural-zh-CN.mp3", chinese) {
		t.Fatal("legacy voice preview key without text hash should not be reused")
	}
}
