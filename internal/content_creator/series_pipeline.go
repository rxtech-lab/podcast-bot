package contentcreator

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/tts"
)

// charMarkerRe matches the per-character voice marker pair the series
// host emits to wrap dialogue: `<char-N>...</char-N>` (or `<char N>...`
// with a space). Index N is 0-based against the cast roster set on the
// SeriesHost. The regex is intentionally permissive on whitespace and
// case so light LLM drift doesn't drop a marker silently.
var (
	charOpenRe  = regexp.MustCompile(`(?i)<\s*char[\s\-_]?(\d+)\s*>`)
	charCloseRe = regexp.MustCompile(`(?i)<\s*/\s*char[\s\-_]?\d*\s*>`)
)

// charSpan is one inner span captured between an open and close marker.
// idx is the character index from the open tag; text is the literal
// inner content. Spans the regex pair couldn't pair up (close without
// open, or vice versa) are dropped — the bare text leaks back into the
// narrator span.
type charSpan struct {
	idx  int
	text string
}

// splitCharacterSpans walks `sent` and returns the cleaned text (markers
// removed) along with a parallel list of (segment, charIdx) tuples. The
// result is a sequence of segments: charIdx == -1 means narrator; >= 0
// means the cast member at that index. Segments are emitted in document
// order, adjacent narrator segments are NOT coalesced (the SSML builder
// handles that). Whitespace is preserved verbatim except that the
// markers themselves are replaced by the empty string.
//
// Returns (cleanText, segments, hadAnyMarker). When hadAnyMarker is
// false, cleanText == sent and segments has exactly one narrator entry
// — the caller can shortcut to the single-voice TTS path.
func splitCharacterSpans(sent string) (clean string, segments []charSpan, had bool) {
	if !charOpenRe.MatchString(sent) && !charCloseRe.MatchString(sent) {
		return sent, []charSpan{{idx: -1, text: sent}}, false
	}
	var cleanB strings.Builder
	cleanB.Grow(len(sent))
	current := -1
	pos := 0
	flush := func(end int) {
		if end <= pos {
			return
		}
		segments = append(segments, charSpan{idx: current, text: sent[pos:end]})
		cleanB.WriteString(sent[pos:end])
	}
	for pos < len(sent) {
		// Find the nearest opener / closer from `pos`.
		openLoc := charOpenRe.FindStringSubmatchIndex(sent[pos:])
		closeLoc := charCloseRe.FindStringIndex(sent[pos:])
		// Translate to absolute offsets.
		if openLoc != nil {
			openLoc[0] += pos
			openLoc[1] += pos
			openLoc[2] += pos
			openLoc[3] += pos
		}
		if closeLoc != nil {
			closeLoc[0] += pos
			closeLoc[1] += pos
		}
		// Pick whichever marker appears first.
		var loc []int
		isOpen := false
		switch {
		case openLoc == nil && closeLoc == nil:
			flush(len(sent))
			pos = len(sent)
			continue
		case openLoc == nil:
			loc = closeLoc
		case closeLoc == nil:
			loc = openLoc[:2]
			isOpen = true
		case openLoc[0] < closeLoc[0]:
			loc = openLoc[:2]
			isOpen = true
		default:
			loc = closeLoc
		}
		had = true
		flush(loc[0])
		if isOpen {
			n, err := strconv.Atoi(sent[openLoc[2]:openLoc[3]])
			if err == nil {
				current = n
			}
		} else {
			current = -1
		}
		pos = loc[1]
	}
	if len(segments) == 0 {
		segments = append(segments, charSpan{idx: -1, text: ""})
	}
	return cleanB.String(), segments, had
}

// synthVoice picks the right TTS path for one cleaned sentence: if the
// speaker is a SeriesHost with a populated cast AND the sentence had
// character voice markers AND the provider supports raw SSML, build a
// multi-voice envelope and call SynthesizeSSML. Otherwise (or on
// ErrSSMLUnsupported / build failure) fall back to single-voice
// SynthesizeStream with the speaker's own voice. Returned reader must
// be Closed by the caller.
func (p *Pipeline) synthVoice(ctx context.Context, t *Turn, sent string,
	hadCharMarkers bool, spans []charSpan,
) (io.ReadCloser, error) {
	speakerVoice := t.Speaker.Voice().ShortName
	if !hadCharMarkers {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	host, ok := t.Speaker.(*agent.SeriesHost)
	if !ok {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	parts := buildSeriesVoiceParts(speakerVoice, host.Characters(), spans)
	if len(parts) == 0 {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	ssml := tts.BuildMultiVoiceSSML(parts, p.d.Language)
	if ssml == "" {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	body, err := p.d.TTS.SynthesizeSSML(ctx, ssml)
	if err != nil {
		if errors.Is(err, tts.ErrSSMLUnsupported) {
			p.d.Log.Info("series multi-voice SSML unsupported by provider, falling back",
				"turn", t.ID)
			return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
		}
		return nil, err
	}
	p.d.Log.Info("series multi-voice synth",
		"turn", t.ID, "spans", len(spans), "voice_parts", len(parts))
	return body, nil
}

// buildSeriesVoiceParts maps the parsed character spans to tts.VoicePart
// entries. narratorVoice is the host's own assigned ShortName; cast is
// the cast roster (per-character AzureVoice ShortNames). Spans pointing
// at an out-of-range index, or at a cast entry whose AzureVoice is
// empty (voice-pool exhaustion), fall back to the narrator. Returns nil
// when the resulting envelope would be entirely narrator (caller should
// stay on the single-voice path).
func buildSeriesVoiceParts(narratorVoice string, cast []agent.SeriesCharacter, spans []charSpan) []tts.VoicePart {
	if len(spans) == 0 {
		return nil
	}
	parts := make([]tts.VoicePart, 0, len(spans))
	hasCharVoice := false
	for _, s := range spans {
		voice := narratorVoice
		if s.idx >= 0 && s.idx < len(cast) {
			if v := cast[s.idx].AzureVoice; v != "" {
				voice = v
				if voice != narratorVoice {
					hasCharVoice = true
				}
			}
		}
		parts = append(parts, tts.VoicePart{Voice: voice, Text: s.text})
	}
	if !hasCharVoice {
		return nil
	}
	return parts
}
