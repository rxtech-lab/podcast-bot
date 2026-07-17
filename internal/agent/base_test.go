package agent

import (
	"strings"
	"testing"
)

// A line carrying a judgement comment must surface it in the recent-transcript
// window so later speakers (host, discussants) can react to the fact-check.
func TestFormatRecentIncludesJudgementComment(t *testing.T) {
	lines := []TranscriptLine{
		{Speaker: "Ann", Role: RoleDiscussant, Text: "AI will replace all jobs by 2027."},
		{Speaker: "Bo", Role: RoleDiscussant, Text: "I disagree.",
			JudgementComment: "That claim lacks\nsupporting evidence."},
	}
	got := formatRecent(lines)
	want := "Ann: AI will replace all jobs by 2027.\n" +
		"Bo: I disagree.\n" +
		"[Judgement fact-check on the line above]: That claim lacks supporting evidence."
	if got != want {
		t.Fatalf("formatRecent =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatRecentOmitsEmptyJudgementComment(t *testing.T) {
	lines := []TranscriptLine{
		{Speaker: "Ann", Role: RoleDiscussant, Text: "Hello.", JudgementComment: "   "},
	}
	if got := formatRecent(lines); strings.Contains(got, "Judgement") {
		t.Fatalf("blank judgement comment leaked into transcript window: %q", got)
	}
}
