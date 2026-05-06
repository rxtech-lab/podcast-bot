package agent

import (
	"log/slog"
	"math/rand"
	"sort"
	"strings"

	"github.com/sirily11/debate-bot/internal/tts"
)

var cinematicVoicePriority = []string{
	"xiaochendragonhdflashlatest",
	"xiaochendragonhdlatest",
	"yunfandragonhdlatest",
	"yunyedragonhdflashlatest",
}

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

// AssignCharacterVoices assigns one Azure neural voice to each name in
// `names` from the locale-filtered pool, biased by the supplied gender
// hint when present. excludeUsed is the set of voice ShortNames already
// claimed by agents (so the host narrator and a character don't share a
// voice). Returned map is keyed by character name; missing entries (rare
// — only when the entire pool fits inside excludeUsed) are left out so
// the caller can detect & fall back. Same scoring + shuffle pipeline as
// AssignVoices so the picks feel consistent with the rest of the cast.
func AssignCharacterVoices(voices []tts.Voice, names []string, genders map[string]string,
	language string, seed int64, excludeUsed map[string]bool, log *slog.Logger,
) map[string]string {
	if len(names) == 0 {
		return nil
	}
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
		log.Warn("no voices match language for character cast; falling back to all voices", "language", language)
		pool = voices
	}
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	sort.SliceStable(pool, func(i, j int) bool {
		return voiceScore(pool[i], language) > voiceScore(pool[j], language)
	})
	used := map[string]bool{}
	for k, v := range excludeUsed {
		used[k] = v
	}
	out := map[string]string{}
	for _, name := range names {
		gender := genders[name]
		v, ok := pickCharacterVoice(pool, gender, used)
		if !ok {
			log.Warn("no voice available for character", "name", name)
			continue
		}
		if used[v.ShortName] {
			log.Warn("recycling voice for character", "name", name, "voice", v.ShortName)
		}
		used[v.ShortName] = true
		out[name] = v.ShortName
	}
	return out
}

func pickCharacterVoice(pool []tts.Voice, wantGender string, used map[string]bool) (tts.Voice, bool) {
	if len(pool) == 0 {
		return tts.Voice{}, false
	}
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

// pickVoiceFor returns the best unused voice for an agent. Preference order:
//  1. unused voice whose Gender matches the agent's name (Bob → Male)
//  2. unused voice with no gender match
//  3. used voice whose Gender matches (recycled to keep gender right)
//  4. any voice (recycled)
//
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
//
//	+100+ for the curated cinematic narration voices:
//	  Xiaochen Dragon HD Flash Latest, Xiaochen Dragon HD Latest,
//	  Yunfan Dragon HD Latest, Yunye Dragon HD Flash Latest.
//	+2 for HD voices (Azure's high-fidelity DragonHD family).
//	+1 for an exact locale match (un-accented base locale, e.g. "zh-CN"
//	   rather than a regional variant like "zh-CN-shaanxi").
func voiceScore(v tts.Voice, language string) int {
	s := 0
	if p := cinematicVoiceRank(v); p > 0 {
		s += 100 + p
	}
	if strings.Contains(v.ShortName, "HD") {
		s += 2
	}
	if strings.EqualFold(v.Locale, language) {
		s += 1
	}
	return s
}

func cinematicVoiceRank(v tts.Voice) int {
	name := compactVoiceName(v.ShortName)
	if name == "" {
		return 0
	}
	for i, want := range cinematicVoicePriority {
		if strings.Contains(name, want) {
			return len(cinematicVoicePriority) - i
		}
	}
	return 0
}

func compactVoiceName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
