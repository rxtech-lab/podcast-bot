package contentcreator

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// fillerWatchdog spots the degenerate tail an audiobook narrator can fall
// into once the planned chapters are finished but the stream is still open:
// an unbroken run of short sign-off lines ("导演旁白：本集结束。", "再见。",
// "The end.") that repeats until something outside the stream cancels it.
// The post-turn boundary judge can't catch this — the whole loop happens
// inside one turn — so the producer feeds every spoken sentence through
// Observe and stops the turn as soon as it trips.
const (
	// fillerRunLimit is how many consecutive closing-filler sentences it
	// takes to declare the narration degenerate. Real endings are one or
	// two sign-off lines; six in a row only happens in a loop.
	fillerRunLimit = 6
	// fillerMaxRunes bounds a sign-off line. Longer sentences are treated
	// as real narration even when they contain a closing phrase.
	fillerMaxRunes = 48
	// fillerRecentWindow is how many normalized sentences are kept for the
	// near-duplicate check.
	fillerRecentWindow = 10
)

var closingFillerRe = regexp.MustCompile(`(?i)(结束|結束|完结|完結|完毕|完畢|全剧终|全劇終|再见|再見|再会|再會|下集|下回|下一章|收听愉快|收聽愉快|敬请期待|敬請期待|制作完成|製作完成|感谢|感謝|谢谢|謝謝|旁白[：:]|the end|goodbye|good ?night|thanks for listening|see you next|that's all)`)

var fillerMarkupRe = regexp.MustCompile(`<[^>]*>`)

type fillerWatchdog struct {
	recent []string
	run    int
}

// Observe accumulates one spoken sentence and reports whether the stream has
// degenerated into a closing-filler loop. A sentence counts toward the run
// when it is short AND either repeats a recent sentence or reads as a
// sign-off; anything else resets the run.
func (w *fillerWatchdog) Observe(sentence string) bool {
	if w == nil {
		return false
	}
	spoken := strings.TrimSpace(fillerMarkupRe.ReplaceAllString(sentence, " "))
	if spoken == "" {
		return false
	}
	norm := normalizeFillerSentence(spoken)
	dup := false
	for _, prev := range w.recent {
		if prev != "" && prev == norm {
			dup = true
			break
		}
	}
	w.recent = append(w.recent, norm)
	if len(w.recent) > fillerRecentWindow {
		w.recent = w.recent[1:]
	}
	if utf8.RuneCountInString(spoken) <= fillerMaxRunes && (dup || closingFillerRe.MatchString(spoken)) {
		w.run++
	} else {
		w.run = 0
	}
	return w.run >= fillerRunLimit
}

func normalizeFillerSentence(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
