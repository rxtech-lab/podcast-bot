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
// and the count of removed markers. A non-zero count means the producer
// should publish that many SceneAdvanceMsg events synced with this
// sentence's audio start. The cleaned text is what flows downstream into
// TTS, the on-air subtitle, the transcript log, and the persisted history —
// markers must NEVER leak into any of those surfaces.
func stripSceneMarkers(sent string) (clean string, count int) {
	matches := sceneMarkerRe.FindAllStringIndex(sent, -1)
	if len(matches) == 0 {
		return sent, 0
	}
	clean = sceneMarkerRe.ReplaceAllString(sent, "")
	clean = strings.TrimSpace(clean)
	return clean, len(matches)
}
