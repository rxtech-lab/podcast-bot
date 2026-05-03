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
// (e.g. "zh-CN" rather than "zh-CN-shaanxi") are picked first. For each
// agent the picker also prefers voices whose Gender matches the agent's
// name (Bob → Male, Linda → Female via the nameGender table). Duplicates
// are avoided when supply allows; otherwise voices recycle and a warning
// is logged.
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

	// Shuffle then stable-sort by score so HD + un-accented voices rank first
	// while still varying within each tier between runs.
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	sort.SliceStable(pool, func(i, j int) bool {
		return voiceScore(pool[i], language) > voiceScore(pool[j], language)
	})

	used := map[string]bool{}
	for _, a := range agents {
		v, ok := pickVoiceFor(pool, a.Name(), used)
		if !ok {
			log.Warn("no voices available; agent will use default", "agent", a.Name())
			continue
		}
		if used[v.ShortName] {
			log.Warn("recycling voice for agent", "agent", a.Name(), "voice", v.ShortName)
		}
		used[v.ShortName] = true
		a.SetVoice(v)
	}
}

// pickVoiceFor returns the best unused voice for an agent. Preference order:
//  1. unused voice whose Gender matches the agent's name (Bob → Male)
//  2. unused voice with no gender match
//  3. used voice whose Gender matches (recycled to keep gender right)
//  4. any voice (recycled)
// Returns ok=false only if pool is empty.
func pickVoiceFor(pool []tts.Voice, agentName string, used map[string]bool) (tts.Voice, bool) {
	if len(pool) == 0 {
		return tts.Voice{}, false
	}
	wantGender := nameGender(agentName)

	pick := func(matchGender, freshOnly bool) (tts.Voice, bool) {
		for _, v := range pool {
			if freshOnly && used[v.ShortName] {
				continue
			}
			if matchGender && wantGender != "" && !strings.EqualFold(v.Gender, wantGender) {
				continue
			}
			return v, true
		}
		return tts.Voice{}, false
	}

	if wantGender != "" {
		if v, ok := pick(true, true); ok {
			return v, true
		}
	}
	if v, ok := pick(false, true); ok {
		return v, true
	}
	if wantGender != "" {
		if v, ok := pick(true, false); ok {
			return v, true
		}
	}
	return pool[0], true
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
