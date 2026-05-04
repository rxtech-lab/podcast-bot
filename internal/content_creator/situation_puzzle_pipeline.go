package contentcreator

import (
	"regexp"
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
// the surface (湯面) narration to flag a scene-image swap point. The
// canonical form is `<scene/>` but the regex tolerates LLM drift —
// case-insensitive, optional whitespace before the slash, optional `<scene>
// </scene>` paired form, and the bracketed `[scene]` variant some models
// prefer. Anything that matches is stripped from the spoken text and the
// subtitle BEFORE TTS sees it, so the synthesizer never voices the cue.
var sceneMarkerRe = regexp.MustCompile(`(?i)<\s*/?\s*scene\s*/?\s*>|\[\s*scene\s*\]`)

// stripSceneMarkers returns sent with every scene-marker occurrence removed
// plus a per-position breakdown so the pipeline can fire SceneAdvanceMsg
// events at the right wall-clock moment:
//
//   - leading: markers that appeared at the very start of the sentence
//     (before any spoken text). These ride with this sentence's
//     TranscriptMsg — the cut should land when this sentence's audio
//     starts.
//   - trailing: markers at the very end of the sentence (after all
//     spoken text). These should fire AFTER this sentence's audio
//     finishes, so the on-screen image only advances once the audience
//     has heard the closing words of the previous beat. Without this
//     deferral, an LLM that emits "[paragraph 1 last sentence] <scene/>"
//     causes the next image to flash in mid-paragraph-1 — the "image
//     one ahead of audio" bug.
//   - middle: markers inside the sentence (rare). Treated like leading
//     for simplicity — they still ride with the sentence's TranscriptMsg.
//
// The cleaned text is what flows downstream into TTS, the on-air
// subtitle, the transcript log, and the persisted history — markers
// must NEVER leak into any of those surfaces.
func stripSceneMarkers(sent string) (clean string, leading, trailing int) {
	if !sceneMarkerRe.MatchString(sent) {
		return sent, 0, 0
	}
	// Peel leading markers. After each match-at-start we keep peeling
	// (an LLM occasionally emits two markers back-to-back; the prompt
	// forbids this but we tolerate it).
	for {
		loc := sceneMarkerRe.FindStringIndex(sent)
		if loc == nil {
			break
		}
		if strings.TrimSpace(sent[:loc[0]]) != "" {
			break
		}
		leading++
		sent = strings.TrimSpace(sent[loc[1]:])
	}
	// Peel trailing markers symmetrically.
	for {
		all := sceneMarkerRe.FindAllStringIndex(sent, -1)
		if len(all) == 0 {
			break
		}
		last := all[len(all)-1]
		if strings.TrimSpace(sent[last[1]:]) != "" {
			break
		}
		trailing++
		sent = strings.TrimSpace(sent[:last[0]])
	}
	// Anything left is in the middle — fold those into the leading count
	// (fire with this sentence's TranscriptMsg). Mid-sentence markers are
	// against the prompt rules anyway; this is best-effort recovery.
	if mid := sceneMarkerRe.FindAllStringIndex(sent, -1); len(mid) > 0 {
		leading += len(mid)
		sent = strings.TrimSpace(sceneMarkerRe.ReplaceAllString(sent, ""))
	}
	return sent, leading, trailing
}
