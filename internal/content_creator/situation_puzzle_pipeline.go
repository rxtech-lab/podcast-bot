package contentcreator

import (
	"regexp"
	"strconv"
	"strings"
)

// directiveWantsPrevText reports whether a directive's payload should be
// filled in from t.PrevTurn.FullText() at production time. Today only the
// puzzle host's answer / evaluate-solution directives use this — both are
// emitted with a trailing ":" by the planner and resolved here once the
// predecessor turn's text is final.
func directiveWantsPrevText(directive string) bool {
	return directive == "answer:" || directive == "evaluate-solution:"
}

// sceneMarkerRe matches the scene-switch token the puzzle host emits during
// the surface (湯面) and conclusion narration. The canonical form is
// `<scene N/>` where N is the 0-based absolute frame index the host is
// about to start narrating; the renderer jumps directly to that frame so
// images stay locked to the planner's beat list. The regex tolerates LLM
// drift: case-insensitive; whitespace before/after slashes; `<scene N>` or
// `</scene>` paired forms; the bracketed `[scene N]` variant. The number
// is optional — an unnumbered `<scene/>` falls through to the legacy
// "advance by one" semantics handled downstream.
var sceneMarkerRe = regexp.MustCompile(
	`(?i)<\s*/?\s*scene(?:\s+(\d+))?\s*/?\s*>|\[\s*scene(?:\s+(\d+))?\s*\]`)

// markerIdxNoNumber is the sentinel returned for an unnumbered marker
// (legacy `<scene/>`). The pipeline interprets it as "advance the current
// scene index by one" — preserves the prior behaviour for older transcripts
// or hosts that didn't get the numbered directive.
const markerIdxNoNumber = -1

// stripSceneMarkers returns sent with every scene-marker occurrence removed
// plus a per-position breakdown so the pipeline can fire SceneAdvanceMsg
// events at the right wall-clock moment. Each entry in the leading /
// trailing slices is the marker's absolute frame index, or markerIdxNoNumber
// (-1) for an unnumbered legacy marker.
//
//   - leading: markers at the very start of the sentence (before any
//     spoken text). These ride with this sentence's TranscriptMsg — the
//     cut should land when this sentence's audio starts.
//   - trailing: markers at the very end (after all spoken text). Fire
//     AFTER this sentence's audio finishes so the new image appears once
//     the audience has heard the closing words of the previous beat.
//   - middle: markers inside the sentence (rare, against prompt rules).
//     Folded into leading for best-effort recovery.
//
// The cleaned text is what flows downstream into TTS, the on-air
// subtitle, the transcript log, and the persisted history — markers
// must NEVER leak into any of those surfaces.
func stripSceneMarkers(sent string) (clean string, leading, trailing []int) {
	if !sceneMarkerRe.MatchString(sent) {
		return sent, nil, nil
	}
	// Peel leading markers. After each match-at-start we keep peeling
	// (an LLM occasionally emits two markers back-to-back; the prompt
	// forbids this but we tolerate it).
	for {
		loc := sceneMarkerRe.FindStringSubmatchIndex(sent)
		if loc == nil {
			break
		}
		if strings.TrimSpace(sent[:loc[0]]) != "" {
			break
		}
		leading = append(leading, parseMarkerIdx(sent, loc))
		sent = strings.TrimSpace(sent[loc[1]:])
	}
	// Peel trailing markers symmetrically. Walk from the right.
	for {
		all := sceneMarkerRe.FindAllStringSubmatchIndex(sent, -1)
		if len(all) == 0 {
			break
		}
		last := all[len(all)-1]
		if strings.TrimSpace(sent[last[1]:]) != "" {
			break
		}
		// Prepend so the slice remains in document order.
		trailing = append([]int{parseMarkerIdx(sent, last)}, trailing...)
		sent = strings.TrimSpace(sent[:last[0]])
	}
	// Anything left is in the middle — fold into leading (fire with this
	// sentence's TranscriptMsg). Mid-sentence markers are against the
	// prompt rules anyway; this is best-effort recovery.
	if mid := sceneMarkerRe.FindAllStringSubmatchIndex(sent, -1); len(mid) > 0 {
		for _, loc := range mid {
			leading = append(leading, parseMarkerIdx(sent, loc))
		}
		sent = strings.TrimSpace(sceneMarkerRe.ReplaceAllString(sent, ""))
	}
	return sent, leading, trailing
}

// parseMarkerIdx extracts the captured digit from one regex submatch
// location. Returns markerIdxNoNumber when neither capture group fired
// (either the bare `<scene/>` form or `[scene]`). The two capture groups
// (one per alternation in sceneMarkerRe) are mutually exclusive — at most
// one fires per match.
func parseMarkerIdx(s string, loc []int) int {
	// loc layout for a regex with two capture groups:
	//   [match_start, match_end, g1_start, g1_end, g2_start, g2_end]
	// A non-firing group has both bounds = -1.
	for g := 1; g <= 2; g++ {
		gs, ge := loc[2*g], loc[2*g+1]
		if gs < 0 || ge < 0 {
			continue
		}
		n, err := strconv.Atoi(s[gs:ge])
		if err == nil {
			return n
		}
	}
	return markerIdxNoNumber
}

// SoundCueMode is the dispatch mode embedded in a `<sound-…/>` marker.
// Overlap mixes the planner-generated clip on top of the running music
// bed (atmospheric stinger). Replace cross-fades the bed itself over to
// the new clip so the underlying texture changes (e.g. tonal shift at a
// key beat).
type SoundCueMode string

const (
	SoundCueOverlap SoundCueMode = "overlap"
	SoundCueReplace SoundCueMode = "replace"
)

// SoundMarker is one parsed sound-cue token. Mode comes from the
// marker's verb ("overlapped" → overlap, "replace" → replace) and Index
// is the 0-based slot the planner assigned to the underlying clip; the
// pipeline emits one SoundCueMsg per marker so the mixer can dispatch.
type SoundMarker struct {
	Mode  SoundCueMode
	Index int
}

// soundMarkerRe matches `<sound-overlapped-N/>` and `<sound-replace-N/>`
// (plus tolerant variants — same drift handling as sceneMarkerRe).
// Capture group 1 is the verb ("overlapped" | "replace"), group 2 is the
// numeric index. Index is required: an unindexed sound marker has no
// clip to play, so the pipeline drops malformed forms silently.
var soundMarkerRe = regexp.MustCompile(
	`(?i)<\s*/?\s*sound-(overlapped|replace)-(\d+)\s*/?\s*>|\[\s*sound-(overlapped|replace)-(\d+)\s*\]`)

// parseSoundMarker pulls the verb + index out of one regex submatch
// location. Returns ok=false when the verb is unrecognised or the index
// fails to parse — caller drops the marker entirely in that case (the
// raw text is still cleaned out of the sentence either way).
func parseSoundMarker(s string, loc []int) (SoundMarker, bool) {
	// Two alternations × (verb, index) capture groups → 4 groups total.
	for base := 1; base <= 3; base += 2 {
		vs, ve := loc[2*base], loc[2*base+1]
		ns, ne := loc[2*(base+1)], loc[2*(base+1)+1]
		if vs < 0 || ve < 0 || ns < 0 || ne < 0 {
			continue
		}
		idx, err := strconv.Atoi(s[ns:ne])
		if err != nil {
			continue
		}
		verb := strings.ToLower(s[vs:ve])
		switch verb {
		case "overlapped":
			return SoundMarker{Mode: SoundCueOverlap, Index: idx}, true
		case "replace":
			return SoundMarker{Mode: SoundCueReplace, Index: idx}, true
		}
	}
	return SoundMarker{}, false
}

// stripSoundMarkers mirrors stripSceneMarkers for `<sound-…-N/>` cues.
// Returns the cleaned text plus leading + trailing marker buckets — same
// dispatch semantics: leading fires when the sentence's first audio byte
// reaches the listener, trailing fires after the sentence finishes, mid-
// sentence is folded into leading as best-effort recovery.
func stripSoundMarkers(sent string) (clean string, leading, trailing []SoundMarker) {
	if !soundMarkerRe.MatchString(sent) {
		return sent, nil, nil
	}
	for {
		loc := soundMarkerRe.FindStringSubmatchIndex(sent)
		if loc == nil {
			break
		}
		if strings.TrimSpace(sent[:loc[0]]) != "" {
			break
		}
		if m, ok := parseSoundMarker(sent, loc); ok {
			leading = append(leading, m)
		}
		sent = strings.TrimSpace(sent[loc[1]:])
	}
	for {
		all := soundMarkerRe.FindAllStringSubmatchIndex(sent, -1)
		if len(all) == 0 {
			break
		}
		last := all[len(all)-1]
		if strings.TrimSpace(sent[last[1]:]) != "" {
			break
		}
		if m, ok := parseSoundMarker(sent, last); ok {
			trailing = append([]SoundMarker{m}, trailing...)
		}
		sent = strings.TrimSpace(sent[:last[0]])
	}
	if mid := soundMarkerRe.FindAllStringSubmatchIndex(sent, -1); len(mid) > 0 {
		for _, loc := range mid {
			if m, ok := parseSoundMarker(sent, loc); ok {
				leading = append(leading, m)
			}
		}
		sent = strings.TrimSpace(soundMarkerRe.ReplaceAllString(sent, ""))
	}
	return sent, leading, trailing
}
