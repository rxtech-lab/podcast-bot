package agent

import (
	"log/slog"
	"math/rand"
	"sort"
	"strings"

	"github.com/sirily11/debate-bot/internal/tts"
)

// AssignVoices assigns one Azure neural voice to every agent. Voices are
// filtered by the topic language (locale prefix), then ranked so HD voices
// (e.g. "...DragonHDFlashLatestNeural") and standard un-accented locales
// (e.g. "zh-CN" rather than "zh-CN-shaanxi") are picked first. Duplicates are
// avoided when supply >= demand; otherwise voices recycle and a warning is
// logged.
//
// seed makes intra-tier ordering deterministic when desired.
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

	// Shuffle first so voices in the same score tier vary between runs.
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })

	// Stable sort by score (desc): HD voices in the requested locale rank
	// highest, then HD anywhere, then standard-locale voices, then the rest
	// (regional accents like zh-CN-shaanxi etc).
	sort.SliceStable(pool, func(i, j int) bool {
		return voiceScore(pool[i], language) > voiceScore(pool[j], language)
	})

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

// voiceScore ranks a voice for assignment. Higher = preferred.
//   +2 for HD voices (Azure's high-fidelity DragonHD family).
//   +1 for an exact locale match (un-accented base locale, e.g. "zh-CN"
//      rather than a regional variant like "zh-CN-shaanxi").
func voiceScore(v tts.Voice, language string) int {
	s := 0
	if strings.Contains(v.ShortName, "HD") {
		s += 2
	}
	if strings.EqualFold(v.Locale, language) {
		s += 1
	}
	return s
}
