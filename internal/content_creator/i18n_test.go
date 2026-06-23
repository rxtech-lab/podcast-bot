package contentcreator

import (
	"testing"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

func TestLangFromAcceptLanguage(t *testing.T) {
	cases := []struct {
		header string
		want   Lang
	}{
		{"", LangEN},
		{"   ", LangEN},
		{"en", LangEN},
		{"en-US,en;q=0.9", LangEN},
		{"fr-FR,fr;q=0.9", LangEN}, // unsupported → English fallback
		{"zh-CN", LangHans},
		{"zh-Hans", LangHans},
		{"zh-Hans-CN", LangHans},
		{"zh-TW", LangHant},
		{"zh-HK", LangHant},
		{"zh-Hant", LangHant},
		{"zh-Hant-HK", LangHant},
		{"zh-Hant-TW", LangHant},
		// Highest q-value wins: Traditional listed first.
		{"zh-Hant-HK,zh-Hans;q=0.9,en;q=0.8", LangHant},
		// English preferred over a lower-weighted Chinese.
		{"en;q=0.9,zh-CN;q=0.5", LangEN},
	}
	for _, c := range cases {
		if got := LangFromAcceptLanguage(c.header); got != c.want {
			t.Errorf("LangFromAcceptLanguage(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

func TestPhaseLabelLang(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		phase       agent.Phase
		en, hans, hant string
	}{
		{"discussion opening", config.ContentTypeDiscussion, agent.PhaseOpening, "Opening", "开场", "開場"},
		{"discussion free", config.ContentTypeDiscussion, agent.PhaseFreeSpeech, "Discussion", "讨论", "討論"},
		{"discussion closing", config.ContentTypeDiscussion, agent.PhaseConclusion, "Conclusion", "总结", "總結"},
		{"puzzle setup", config.ContentTypeSituationPuzzle, agent.PhaseOpening, "Puzzle", "出题", "出題"},
		{"puzzle qa", config.ContentTypeSituationPuzzle, agent.PhaseFreeSpeech, "Q&A", "问答", "問答"},
		{"puzzle reveal", config.ContentTypeSituationPuzzle, agent.PhaseVerdict, "Reveal", "揭晓", "揭曉"},
		{"series recap", config.ContentTypeSeries, agent.PhaseOpening, "Recap", "上集回顾", "上集回顧"},
		{"series body", config.ContentTypeSeries, agent.PhaseFreeSpeech, "Episode", "本集", "本集"},
		{"debate opening", config.ContentTypeDebate, agent.PhaseOpening, "Opening", "立论", "立論"},
		{"debate free", config.ContentTypeDebate, agent.PhaseFreeSpeech, "Free Debate", "自由辩论", "自由辯論"},
		{"debate verdict", config.ContentTypeDebate, agent.PhaseVerdict, "Verdict", "判决", "判決"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PhaseLabelLang(c.contentType, c.phase, LangEN); got != c.en {
				t.Errorf("EN = %q, want %q", got, c.en)
			}
			if got := PhaseLabelLang(c.contentType, c.phase, LangHans); got != c.hans {
				t.Errorf("Hans = %q, want %q", got, c.hans)
			}
			if got := PhaseLabelLang(c.contentType, c.phase, LangHant); got != c.hant {
				t.Errorf("Hant = %q, want %q", got, c.hant)
			}
		})
	}
}

// PhaseLabel must stay Traditional-default so existing callers (video renderer
// on-frame chip, pipeline label stamp) are unchanged.
func TestPhaseLabelTraditionalDefault(t *testing.T) {
	if got := PhaseLabel(config.ContentTypeDiscussion, agent.PhaseOpening); got != "開場" {
		t.Errorf("PhaseLabel discussion opening = %q, want 開場", got)
	}
	if got := PhaseLabel(config.ContentTypeDebate, agent.PhaseFreeSpeech); got != "自由辯論" {
		t.Errorf("PhaseLabel debate free = %q, want 自由辯論", got)
	}
}
