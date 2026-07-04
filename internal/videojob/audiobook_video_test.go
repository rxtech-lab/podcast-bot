package videojob

import (
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestAudioBookVideoOptionsCarryPodcastLanguage(t *testing.T) {
	opts := audioBookVideoOptions(&config.DebateTopic{
		Title:    "History",
		Language: "zh-TW",
	}, nil, nil)
	if opts.Language != "zh-TW" {
		t.Fatalf("Language = %q, want zh-TW", opts.Language)
	}
}
