package contentcreator

import (
	"context"
	_ "embed"
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
	charOpenRe              = regexp.MustCompile(`(?i)<\s*char[\s\-_]?(\d+)\s*>`)
	charCloseRe             = regexp.MustCompile(`(?i)<\s*/\s*char[\s\-_]?\d*\s*>`)
	naturalMarkerRe         = regexp.MustCompile(`(?i)<\s*(?:pause\b[^>]*|breath\b[^>]*)/?\s*>|\[\s*(?:pause\b[^\]]*|breath\b[^\]]*)\]`)
	naturalMarkerDurationRe = regexp.MustCompile(`(?i)(\d{1,5})\s*(ms|s)?`)
)

//go:embed assets/breath.mp3
var seriesBreathMP3 []byte

const (
	defaultPauseMS = 500
	minPauseMS     = 150
	maxPauseMS     = 1200
)

// charSpan is one inner span captured between an open and close marker.
// idx is the character index from the open tag; text is the literal
// inner content. Spans the regex pair couldn't pair up (close without
// open, or vice versa) are dropped — the bare text leaks back into the
// narrator span.
type charSpan struct {
	idx   int
	text  string
	nodes []tts.SpeechNode
}

type naturalChunk struct {
	spans       []charSpan
	breathAfter bool
}

type naturalMarker struct {
	kind    string
	pauseMS int
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
	clean, segments, had, _ = splitCharacterSpansFrom(sent, -1)
	return clean, segments, had
}

// splitCharacterSpansFrom is splitCharacterSpans with an explicit starting
// character index so an open `<char-N>` span can continue across sentence
// boundaries within a turn. start is the index in effect at the beginning of
// sent (-1 = narrator); end is the index still in effect after it, to feed the
// next sentence. When start >= 0 the sentence is inside a guest span, so `had`
// is reported true even with no markers of its own — that keeps the multi-voice
// TTS envelope and the per-speaker transcript attribution consistent.
func splitCharacterSpansFrom(sent string, start int) (clean string, segments []charSpan, had bool, end int) {
	if !charOpenRe.MatchString(sent) && !charCloseRe.MatchString(sent) {
		return sent, []charSpan{{idx: start, text: sent}}, start >= 0, start
	}
	var cleanB strings.Builder
	cleanB.Grow(len(sent))
	current := start
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
	return cleanB.String(), segments, had, current
}

func splitNaturalSpeech(spans []charSpan) (clean string, chunks []naturalChunk, hadPause, hadBreath bool) {
	var cleanB strings.Builder
	var cur []charSpan
	addSpan := func(idx int, text string, node tts.SpeechNode) {
		if text != "" {
			cleanB.WriteString(text)
		}
		if node.Text == "" && node.BreakMS <= 0 {
			return
		}
		cur = append(cur, charSpan{idx: idx, text: text, nodes: []tts.SpeechNode{node}})
	}
	flushBreath := func() {
		chunks = append(chunks, naturalChunk{spans: cur, breathAfter: true})
		cur = nil
		hadBreath = true
	}
	for _, span := range spans {
		text := span.text
		if !naturalMarkerRe.MatchString(text) {
			addSpan(span.idx, text, tts.SpeechNode{Text: text})
			continue
		}
		pos := 0
		for _, loc := range naturalMarkerRe.FindAllStringIndex(text, -1) {
			if loc[0] > pos {
				part := text[pos:loc[0]]
				addSpan(span.idx, part, tts.SpeechNode{Text: part})
			}
			m := parseNaturalMarker(text[loc[0]:loc[1]])
			switch m.kind {
			case "pause":
				addSpan(span.idx, "", tts.SpeechNode{BreakMS: m.pauseMS})
				hadPause = true
			case "breath":
				flushBreath()
			}
			pos = loc[1]
		}
		if pos < len(text) {
			part := text[pos:]
			addSpan(span.idx, part, tts.SpeechNode{Text: part})
		}
	}
	if len(cur) > 0 {
		chunks = append(chunks, naturalChunk{spans: cur})
	}
	return cleanB.String(), chunks, hadPause, hadBreath
}

func parseNaturalMarker(raw string) naturalMarker {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "breath") {
		return naturalMarker{kind: "breath"}
	}
	ms := defaultPauseMS
	if m := naturalMarkerDurationRe.FindStringSubmatch(lower); len(m) == 3 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			ms = n
			if m[2] == "s" {
				ms *= 1000
			}
		}
	}
	if ms < minPauseMS {
		ms = minPauseMS
	}
	if ms > maxPauseMS {
		ms = maxPauseMS
	}
	return naturalMarker{kind: "pause", pauseMS: ms}
}

// synthVoice picks the right TTS path for one cleaned sentence: if the
// speaker is a SeriesHost with a populated cast AND the sentence had
// character voice markers AND the provider supports raw SSML, build a
// multi-voice envelope and call SynthesizeSSML. Otherwise (or on
// ErrSSMLUnsupported / build failure) fall back to single-voice
// SynthesizeStream with the speaker's own voice. Returned reader must
// be Closed by the caller.
func (p *Pipeline) synthVoice(ctx context.Context, t *Turn, sent string,
	hadCharMarkers bool, spans []charSpan, hadSpeechNodes bool,
) (io.ReadCloser, error) {
	speakerVoice := t.Speaker.Voice().ShortName
	if !hadCharMarkers && !hadSpeechNodes {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	host, ok := t.Speaker.(*agent.SeriesHost)
	if !ok && !hadSpeechNodes {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	var parts []tts.VoicePart
	if ok {
		parts = buildSeriesVoiceParts(speakerVoice, host.Characters(), spans)
	}
	if len(parts) == 0 && !hadSpeechNodes {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	var ssml string
	if len(parts) > 0 {
		ssml = tts.BuildMultiVoiceSSML(parts, p.d.Language)
	} else {
		ssml = tts.BuildSSMLNodes(speakerVoice, speechNodesFromSpans(spans), p.d.Language)
	}
	if ssml == "" {
		return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
	}
	body, err := p.d.TTS.SynthesizeSSML(ctx, ssml)
	if err != nil {
		if errors.Is(err, tts.ErrSSMLUnsupported) {
			if p.d.Log != nil {
				p.d.Log.Info("series SSML unsupported by provider, falling back",
					"turn", t.ID)
			}
			return p.d.TTS.SynthesizeStream(ctx, speakerVoice, sent, p.d.Language)
		}
		return nil, err
	}
	p.d.Log.Info("series multi-voice synth",
		"turn", t.ID, "spans", len(spans), "voice_parts", len(parts))
	return body, nil
}

func (p *Pipeline) synthNaturalChunks(ctx context.Context, t *Turn, sent string,
	chunks []naturalChunk, hadCharMarkers bool, sink io.Writer,
) (int64, error) {
	if len(chunks) == 0 && sent != "" {
		chunks = []naturalChunk{{spans: []charSpan{{idx: -1, text: sent}}}}
	}
	var total int64
	for _, chunk := range chunks {
		if naturalChunkHasSpeech(chunk) {
			chunkText := naturalChunkText(chunk)
			body, err := p.synthVoice(ctx, t, chunkText,
				naturalChunkHasCharVoiceMarker(chunk), chunk.spans, naturalChunkHasBreak(chunk))
			if err != nil {
				return total, err
			}
			n, err := io.Copy(sink, body)
			closeErr := body.Close()
			total += n
			if err != nil {
				return total, err
			}
			if closeErr != nil {
				return total, closeErr
			}
		}
		if chunk.breathAfter {
			n, err := sink.Write(seriesBreathMP3)
			total += int64(n)
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func isSeriesNaturalTurn(t *Turn) bool {
	return t != nil && t.Speaker != nil && t.Speaker.Role() == agent.RoleSeriesHost
}

func naturalChunksHaveAudio(chunks []naturalChunk) bool {
	for _, c := range chunks {
		if c.breathAfter || naturalChunkHasSpeech(c) {
			return true
		}
	}
	return false
}

func naturalChunkHasSpeech(c naturalChunk) bool {
	for _, s := range c.spans {
		for _, n := range speechNodesForSpan(s) {
			if n.Text != "" || n.BreakMS > 0 {
				return true
			}
		}
	}
	return false
}

func naturalChunkHasBreak(c naturalChunk) bool {
	for _, s := range c.spans {
		for _, n := range speechNodesForSpan(s) {
			if n.BreakMS > 0 {
				return true
			}
		}
	}
	return false
}

func naturalChunkHasCharVoiceMarker(c naturalChunk) bool {
	for _, s := range c.spans {
		if s.idx >= 0 {
			return true
		}
	}
	return false
}

func naturalChunkText(c naturalChunk) string {
	var sb strings.Builder
	for _, s := range c.spans {
		sb.WriteString(s.text)
	}
	return sb.String()
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
		parts = append(parts, tts.VoicePart{Voice: voice, Text: s.text, Nodes: speechNodesForSpan(s)})
	}
	if !hasCharVoice {
		return nil
	}
	return parts
}

func speechNodesFromSpans(spans []charSpan) []tts.SpeechNode {
	var nodes []tts.SpeechNode
	for _, s := range spans {
		nodes = append(nodes, speechNodesForSpan(s)...)
	}
	return nodes
}

func speechNodesForSpan(s charSpan) []tts.SpeechNode {
	if len(s.nodes) > 0 {
		return s.nodes
	}
	if s.text == "" {
		return nil
	}
	return []tts.SpeechNode{{Text: s.text}}
}
