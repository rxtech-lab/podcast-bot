package agent

import (
	"sort"
	"testing"

	"github.com/sirily11/debate-bot/internal/tts"
)

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
