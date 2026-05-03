package agent

import (
	"log/slog"
	"math/rand"
	"strings"

	"github.com/sirily11/debate-bot/internal/tts"
)

// AssignVoices assigns one Azure neural voice to every agent. Voices are
// filtered by the topic language (locale prefix). Duplicates are avoided when
// supply >= demand; otherwise voices recycle and a warning is logged.
//
// seed makes the assignment deterministic when desired.
func AssignVoices(voices []tts.Voice, agents []Agent, language string, seed int64, log *slog.Logger) {
	prefix := strings.ToLower(strings.SplitN(language, "-", 2)[0])
	var pool []tts.Voice
	for _, v := range voices {
		if v.VoiceType != "" && !strings.Contains(strings.ToLower(v.VoiceType), "neural") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(v.Locale), prefix) {
			pool = append(pool, v)
		}
	}
	if len(pool) == 0 {
		log.Warn("no voices match language; falling back to all voices", "language", language)
		pool = voices
	}
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })

	for i, a := range agents {
		if len(pool) == 0 {
			log.Warn("no voices available; agent will use default", "agent", a.Name())
			continue
		}
		a.SetVoice(pool[i%len(pool)])
		if i >= len(pool) {
			log.Warn("recycling voice for agent", "agent", a.Name(), "voice", pool[i%len(pool)].ShortName)
		}
	}
}
