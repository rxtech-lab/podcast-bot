package contentcreator

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
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

	// audiobookDropoutSampleRate is an ANALYSIS-ONLY decode format for the
	// silence detector — it downmixes the (dual-mono voice) stream to mono
	// 24 kHz, which preserves amplitude, so the PCM threshold stays valid
	// regardless of the pipeline's 48 kHz stereo output format.
	audiobookDropoutSampleRate      = 24000
	audiobookDropoutWindow          = 10 * time.Millisecond
	audiobookDropoutMergeGap        = 50 * time.Millisecond
	audiobookDropoutMinDuration     = 320 * time.Millisecond
	audiobookDropoutMinEdgeDistance = 200 * time.Millisecond
	audiobookDropoutExpectedBreak   = 450 * time.Millisecond
	audiobookDropoutPCMThreshold    = 64
)

var inspectSynthAudioDropout = inspectMP3SynthAudioDropout

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

func transcriptSpansFromNaturalChunks(chunks []naturalChunk) []charSpan {
	out := make([]charSpan, 0, len(chunks))
	for _, chunk := range chunks {
		for _, span := range chunk.spans {
			if span.text == "" {
				continue
			}
			out = append(out, span)
		}
	}
	return out
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
			n, err := p.synthAndWriteNaturalChunk(ctx, t, chunk, sink)
			total += n
			if err != nil {
				return total, err
			}
		}
		if chunk.breathAfter {
			if p != nil && p.d.ContentType == config.ContentTypeAudioBook {
				continue
			}
			n, err := sink.Write(seriesBreathMP3)
			total += int64(n)
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func (p *Pipeline) synthAndWriteNaturalChunk(ctx context.Context, t *Turn, chunk naturalChunk, sink io.Writer) (int64, error) {
	chunkText := naturalChunkText(chunk)
	hadCharMarkers := naturalChunkHasCharVoiceMarker(chunk)
	hadBreak := naturalChunkHasBreak(chunk)
	if !p.shouldInspectSynthChunk() {
		body, err := p.synthVoice(ctx, t, chunkText, hadCharMarkers, chunk.spans, hadBreak)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(sink, body)
		closeErr := body.Close()
		if err != nil {
			return n, err
		}
		if closeErr != nil {
			return n, closeErr
		}
		return n, nil
	}

	const maxAttempts = 2
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, err := p.synthVoice(ctx, t, chunkText, hadCharMarkers, chunk.spans, hadBreak)
		if err != nil {
			return 0, err
		}
		data, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil {
			return 0, readErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		dropout, inspectErr := inspectSynthAudioDropout(ctx, data, hadBreak)
		if inspectErr != nil {
			if p != nil && p.d.Log != nil {
				p.d.Log.Warn("audiobook audio QA skipped",
					"turn", turnIDForLog(t),
					"attempt", attempt,
					"err", inspectErr)
			}
			n, err := io.Copy(sink, bytes.NewReader(data))
			return n, err
		}
		if !dropout.Found {
			n, err := io.Copy(sink, bytes.NewReader(data))
			return n, err
		}
		lastErr = fmt.Errorf("detected silent dropout %.0f-%.0fms",
			float64(dropout.Start)/float64(time.Millisecond),
			float64(dropout.End)/float64(time.Millisecond))
		if p != nil && p.d.Log != nil {
			p.d.Log.Warn("audiobook audio QA retry",
				"turn", turnIDForLog(t),
				"attempt", attempt,
				"start_ms", dropout.Start.Milliseconds(),
				"duration_ms", dropout.Duration().Milliseconds())
		}
		if attempt == maxAttempts {
			n, err := io.Copy(sink, bytes.NewReader(data))
			if err != nil {
				return n, err
			}
			if p != nil && p.d.Log != nil {
				p.d.Log.Warn("audiobook audio QA kept final attempt",
					"turn", turnIDForLog(t),
					"err", lastErr)
			}
			return n, nil
		}
	}
	return 0, lastErr
}

func (p *Pipeline) shouldInspectSynthChunk() bool {
	return p != nil && p.d.ContentType == config.ContentTypeAudioBook && inspectSynthAudioDropout != nil
}

func turnIDForLog(t *Turn) int {
	if t == nil {
		return 0
	}
	return t.ID
}

type synthAudioDropout struct {
	Found bool
	Start time.Duration
	End   time.Duration
}

func (d synthAudioDropout) Duration() time.Duration {
	if !d.Found || d.End <= d.Start {
		return 0
	}
	return d.End - d.Start
}

func inspectMP3SynthAudioDropout(ctx context.Context, mp3 []byte, hasExplicitBreak bool) (synthAudioDropout, error) {
	if len(mp3) == 0 {
		return synthAudioDropout{}, nil
	}
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-i", "pipe:0",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(audiobookDropoutSampleRate),
		"-ac", "1",
		"pipe:1",
	)
	cmd.Stdin = bytes.NewReader(mp3)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return synthAudioDropout{}, fmt.Errorf("decode synthesized mp3: %w: %s", err, msg)
		}
		return synthAudioDropout{}, fmt.Errorf("decode synthesized mp3: %w", err)
	}
	return detectPCMZeroDropout(out.Bytes(), audiobookDropoutSampleRate, hasExplicitBreak), nil
}

func detectPCMZeroDropout(pcm []byte, sampleRate int, hasExplicitBreak bool) synthAudioDropout {
	if sampleRate <= 0 || len(pcm) < 2 {
		return synthAudioDropout{}
	}
	samples := len(pcm) / 2
	windowSamples := int(audiobookDropoutWindow * time.Duration(sampleRate) / time.Second)
	if windowSamples <= 0 {
		windowSamples = 1
	}
	type silentRun struct {
		start time.Duration
		end   time.Duration
	}
	var runs []silentRun
	var runStart = -1
	for offset := 0; offset < samples; offset += windowSamples {
		end := offset + windowSamples
		if end > samples {
			end = samples
		}
		silent := true
		for i := offset; i < end; i++ {
			j := i * 2
			v := int(int16(uint16(pcm[j]) | uint16(pcm[j+1])<<8))
			if v < 0 {
				v = -v
			}
			if v > audiobookDropoutPCMThreshold {
				silent = false
				break
			}
		}
		if silent {
			if runStart < 0 {
				runStart = offset
			}
			continue
		}
		if runStart >= 0 {
			runs = append(runs, silentRun{
				start: samplesToDuration(runStart, sampleRate),
				end:   samplesToDuration(offset, sampleRate),
			})
			runStart = -1
		}
	}
	if runStart >= 0 {
		runs = append(runs, silentRun{
			start: samplesToDuration(runStart, sampleRate),
			end:   samplesToDuration(samples, sampleRate),
		})
	}
	if len(runs) == 0 {
		return synthAudioDropout{}
	}
	merged := runs[:0]
	for _, run := range runs {
		if len(merged) == 0 || run.start-merged[len(merged)-1].end > audiobookDropoutMergeGap {
			merged = append(merged, run)
			continue
		}
		merged[len(merged)-1].end = run.end
	}
	total := samplesToDuration(samples, sampleRate)
	for _, run := range merged {
		dur := run.end - run.start
		if dur < audiobookDropoutMinDuration {
			continue
		}
		if run.start < audiobookDropoutMinEdgeDistance || total-run.end < audiobookDropoutMinEdgeDistance {
			continue
		}
		if hasExplicitBreak && dur >= audiobookDropoutExpectedBreak {
			continue
		}
		return synthAudioDropout{Found: true, Start: run.start, End: run.end}
	}
	return synthAudioDropout{}
}

func samplesToDuration(samples, sampleRate int) time.Duration {
	return time.Duration(float64(samples) / float64(sampleRate) * float64(time.Second))
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
