package contentcreator

import (
	"strings"

	"golang.org/x/text/language"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// Lang is the negotiated display language for human-readable labels. Hong Kong
// and Taiwan both collapse to Traditional (LangHant) — the short phase labels
// are identical across those regions.
type Lang int

const (
	LangEN   Lang = iota // English
	LangHans             // Simplified Chinese (zh-Hans / zh-CN)
	LangHant             // Traditional Chinese (zh-Hant / zh-TW / zh-HK)
)

// LangFromAcceptLanguage maps an HTTP Accept-Language header value to one of the
// supported display languages. Missing or unrecognized headers fall back to
// English.
//
// We classify the parsed tags ourselves (in q-value order) rather than using
// language.NewMatcher: the matcher gives Simplified Chinese special "default zh"
// treatment and will cross-match a high-q zh-Hant-HK to Simplified, which is
// wrong for a script-sensitive choice. Walking the tags by preference and
// keying off script/region is deterministic — zh-CN/zh-SG→Hans,
// zh-TW/zh-HK/zh-MO/zh-Hant-*→Hant, en-*→English.
func LangFromAcceptLanguage(header string) Lang {
	tags, _, err := language.ParseAcceptLanguage(strings.TrimSpace(header))
	if err != nil || len(tags) == 0 {
		return LangEN
	}
	for _, tag := range tags {
		s := strings.ToLower(tag.String())
		switch {
		case strings.HasPrefix(s, "en"):
			return LangEN
		case strings.HasPrefix(s, "zh"):
			switch {
			case strings.Contains(s, "hant"),
				strings.Contains(s, "-tw"), strings.Contains(s, "-hk"), strings.Contains(s, "-mo"):
				return LangHant
			default:
				// Simplified (zh-Hans, zh-CN, zh-SG) and bare "zh".
				return LangHans
			}
		}
		// Any other base language: skip and try the next preferred tag.
	}
	return LangEN
}

// phaseTriple holds a label in each supported language. Traditional (hant) is
// the original on-frame/SSE text the app shipped with; en and hans are the new
// translations.
type phaseTriple struct{ en, hans, hant string }

func (t phaseTriple) pick(l Lang) string {
	switch l {
	case LangHans:
		return t.hans
	case LangEN:
		return t.en
	default:
		return t.hant
	}
}

// phaseFromString reverses agent.Phase.String() for callers that only have the
// serialized phase value (e.g. a persisted job snapshot read back from the DB).
func phaseFromString(s string) (agent.Phase, bool) {
	switch s {
	case "setup":
		return agent.PhaseSetup, true
	case "opening":
		return agent.PhaseOpening, true
	case "free-debate":
		return agent.PhaseFreeSpeech, true
	case "closing":
		return agent.PhaseClosing, true
	case "verdict":
		return agent.PhaseVerdict, true
	case "conclusion":
		return agent.PhaseConclusion, true
	case "ended":
		return agent.PhaseEnded, true
	}
	return agent.PhaseSetup, false
}

// PhaseLabelFromString localizes a phase label given the phase's serialized
// string form (agent.Phase.String()). It backs REST snapshot endpoints (the
// persisted Job only stores the phase string), mirroring the per-connection
// SSE/WS localization. ok is false when the phase string isn't recognized, so
// callers can keep any existing label rather than overwriting it with a blank.
func PhaseLabelFromString(contentType, phase string, lang Lang) (string, bool) {
	p, ok := phaseFromString(phase)
	if !ok {
		return "", false
	}
	return PhaseLabelLang(contentType, p, lang), true
}

// PhaseLabelLang returns the human-readable phase name for the given content
// type in the requested language. It is the language-aware core that
// PhaseLabel (Traditional-default) delegates to, and the single source of truth
// for both the video renderer's on-frame chip and the SSE/WS PhaseMsg label.
func PhaseLabelLang(contentType string, p agent.Phase, lang Lang) string {
	switch contentType {
	case config.ContentTypeSituationPuzzle:
		switch p {
		case agent.PhaseSetup, agent.PhaseOpening:
			return phaseTriple{"Puzzle", "出题", "出題"}.pick(lang)
		case agent.PhaseFreeSpeech:
			return phaseTriple{"Q&A", "问答", "問答"}.pick(lang)
		case agent.PhaseVerdict:
			return phaseTriple{"Reveal", "揭晓", "揭曉"}.pick(lang)
		case agent.PhaseEnded, agent.PhaseConclusion:
			return phaseTriple{"Summary", "总结", "總結"}.pick(lang)
		}
	case config.ContentTypeDiscussion:
		switch p {
		case agent.PhaseSetup, agent.PhaseOpening:
			return phaseTriple{"Opening", "开场", "開場"}.pick(lang)
		case agent.PhaseFreeSpeech:
			return phaseTriple{"Discussion", "讨论", "討論"}.pick(lang)
		case agent.PhaseClosing, agent.PhaseConclusion, agent.PhaseEnded:
			return phaseTriple{"Conclusion", "总结", "總結"}.pick(lang)
		}
	case config.ContentTypeNews:
		switch p {
		case agent.PhaseSetup, agent.PhaseOpening:
			return phaseTriple{"Headlines", "头条", "頭條"}.pick(lang)
		case agent.PhaseFreeSpeech:
			return phaseTriple{"Stories", "新闻", "新聞"}.pick(lang)
		case agent.PhaseClosing, agent.PhaseConclusion, agent.PhaseEnded:
			return phaseTriple{"Sign-off", "结束", "結束"}.pick(lang)
		}
	case config.ContentTypeSeries:
		// Series episodes: recap → main body → end (see PhaseLabel notes).
		switch p {
		case agent.PhaseSetup, agent.PhaseOpening:
			return phaseTriple{"Recap", "上集回顾", "上集回顧"}.pick(lang)
		case agent.PhaseFreeSpeech:
			return phaseTriple{"Episode", "本集", "本集"}.pick(lang)
		case agent.PhaseEnded, agent.PhaseConclusion:
			return phaseTriple{"End", "完", "完"}.pick(lang)
		}
	case config.ContentTypeAudioBook:
		switch p {
		case agent.PhaseSetup, agent.PhaseOpening:
			return phaseTriple{"Opening", "开篇", "開篇"}.pick(lang)
		case agent.PhaseFreeSpeech:
			return phaseTriple{"Chapter", "章节", "章節"}.pick(lang)
		case agent.PhaseEnded, agent.PhaseConclusion:
			return phaseTriple{"End", "完", "完"}.pick(lang)
		}
	default:
		// Debate (and unknown types — match the existing on-frame chip).
		switch p {
		case agent.PhaseOpening:
			return phaseTriple{"Opening", "立论", "立論"}.pick(lang)
		case agent.PhaseFreeSpeech:
			return phaseTriple{"Free Debate", "自由辩论", "自由辯論"}.pick(lang)
		case agent.PhaseClosing:
			return phaseTriple{"Closing", "结辩", "結辯"}.pick(lang)
		case agent.PhaseVerdict:
			return phaseTriple{"Verdict", "判决", "判決"}.pick(lang)
		case agent.PhaseConclusion:
			return phaseTriple{"Conclusion", "总结", "總結"}.pick(lang)
		}
	}
	return strings.ToUpper(p.String())
}
