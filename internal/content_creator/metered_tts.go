package contentcreator

import (
	"context"
	"io"
	"strings"

	"github.com/sirily11/debate-bot/internal/tts"
)

// meteredTTS wraps a tts.Provider and records the number of characters sent to
// the backend into the run Tracker, so Azure synthesis cost is folded into the
// per-run total. It is otherwise a transparent pass-through; FetchVoices and
// the synthesis byte streams are returned unchanged.
type meteredTTS struct {
	inner   tts.Provider
	tracker *Tracker
}

// newMeteredTTS returns inner wrapped so every synthesis call's character count
// is added to tracker. Returns inner unchanged when there is nothing to meter.
func newMeteredTTS(inner tts.Provider, tracker *Tracker) tts.Provider {
	if inner == nil || tracker == nil {
		return inner
	}
	return &meteredTTS{inner: inner, tracker: tracker}
}

func (m *meteredTTS) FetchVoices(ctx context.Context, language string) ([]tts.Voice, error) {
	return m.inner.FetchVoices(ctx, language)
}

func (m *meteredTTS) SynthesizeStream(ctx context.Context, voice, text, lang string) (io.ReadCloser, error) {
	rc, err := m.inner.SynthesizeStream(ctx, voice, text, lang)
	if err == nil {
		m.tracker.AddTTSCharacters(int64(len([]rune(text))))
	}
	return rc, err
}

func (m *meteredTTS) SynthesizeSSML(ctx context.Context, ssml string) (io.ReadCloser, error) {
	rc, err := m.inner.SynthesizeSSML(ctx, ssml)
	if err == nil {
		m.tracker.AddTTSCharacters(int64(len([]rune(spokenChars(ssml)))))
	}
	return rc, err
}

// spokenChars strips SSML markup (the <...> tags) so only the spoken text is
// counted toward billing, matching how the plain-text path counts characters.
func spokenChars(ssml string) string {
	var b strings.Builder
	depth := 0
	for _, r := range ssml {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
