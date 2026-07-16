package stt

import (
	"strings"
	"testing"
)

func TestSentenceCuesFromWordsEnglish(t *testing.T) {
	tr := &Transcript{Phrases: []Phrase{{
		Speaker: 1, OffsetMS: 100, DurationMS: 2000,
		Text: "Good afternoon, everyone. Welcome!",
		Words: []Word{
			{Text: "Good", OffsetMS: 100, DurationMS: 200},
			{Text: "afternoon,", OffsetMS: 300, DurationMS: 300},
			{Text: "everyone.", OffsetMS: 700, DurationMS: 400},
			{Text: "Welcome!", OffsetMS: 1200, DurationMS: 500},
		},
	}}}
	cues := SentenceCues(tr)
	if len(cues) != 2 {
		t.Fatalf("expected 2 sentence cues, got %d: %#v", len(cues), cues)
	}
	if cues[0].Text != "Good afternoon, everyone." || cues[0].StartMS != 100 || cues[0].EndMS != 1100 {
		t.Fatalf("cue 0 wrong: %#v", cues[0])
	}
	if cues[1].Text != "Welcome!" || cues[1].StartMS != 1200 || cues[1].EndMS != 1700 {
		t.Fatalf("cue 1 wrong: %#v", cues[1])
	}
}

func TestSentenceCuesFromWordsCJK(t *testing.T) {
	words := []Word{}
	text := "欢迎来到今天的讨论，我们开始。"
	offset := int64(0)
	for _, r := range text {
		words = append(words, Word{Text: string(r), OffsetMS: offset, DurationMS: 100})
		offset += 100
	}
	tr := &Transcript{Phrases: []Phrase{{Speaker: 2, OffsetMS: 0, DurationMS: offset, Text: text, Words: words}}}
	cues := SentenceCues(tr)
	if len(cues) != 1 {
		t.Fatalf("expected one complete-sentence cue, got %d: %#v", len(cues), cues)
	}
	if cues[0].Text != text {
		t.Fatalf("cue 0 text wrong (no spaces expected): %q", cues[0].Text)
	}
	if strings.Contains(cues[0].Text, " ") {
		t.Fatalf("CJK cue must not contain spaces: %q", cues[0].Text)
	}
	if cues[0].Speaker != 2 {
		t.Fatalf("speaker not preserved: %#v", cues)
	}
}

func TestSentenceCuesTextFallbackProportional(t *testing.T) {
	tr := &Transcript{Phrases: []Phrase{{
		Speaker: 1, OffsetMS: 1000, DurationMS: 3000,
		Text: "各位听众朋友们。欢迎来到今天的圆桌讨论。",
	}}}
	cues := SentenceCues(tr)
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %#v", len(cues), cues)
	}
	if cues[0].StartMS != 1000 {
		t.Fatalf("first cue must start at phrase offset: %#v", cues[0])
	}
	if cues[len(cues)-1].EndMS != 4000 {
		t.Fatalf("last cue must end at phrase end: %#v", cues[len(cues)-1])
	}
	// Monotonic, gap-free timeline.
	for i := 1; i < len(cues); i++ {
		if cues[i].StartMS != cues[i-1].EndMS {
			t.Fatalf("cues not contiguous at %d: %#v", i, cues)
		}
	}
	// Longer piece gets the longer duration.
	if d0, d1 := cues[0].EndMS-cues[0].StartMS, cues[1].EndMS-cues[1].StartMS; d0 >= d1 {
		t.Fatalf("expected second (longer) piece to dwell longer: %d vs %d", d0, d1)
	}
}

func TestSentenceCuesSpeakerBoundary(t *testing.T) {
	tr := &Transcript{Phrases: []Phrase{
		{Speaker: 1, OffsetMS: 0, DurationMS: 1000, Text: "First speaker line"},
		{Speaker: 2, OffsetMS: 1000, DurationMS: 1000, Text: "Second speaker line"},
	}}
	cues := SentenceCues(tr)
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(cues))
	}
	if cues[0].Speaker != 1 || cues[1].Speaker != 2 {
		t.Fatalf("speakers must never merge: %#v", cues)
	}
}

func TestSentenceCuesMergeCommaSeparatedProviderPhrases(t *testing.T) {
	tr := &Transcript{Phrases: []Phrase{
		{Speaker: 1, OffsetMS: 22_000, DurationMS: 3_000, Text: "这是一个每天都在各大科技公司上演的真实故事，"},
		{Speaker: 1, OffsetMS: 25_000, DurationMS: 2_000, Text: "当 AI 既能写代码又能画原型，"},
		{Speaker: 1, OffsetMS: 27_000, DurationMS: 4_000, Text: "这两个角色的边界会发生什么变化？"},
	}}
	cues := SentenceCues(tr)
	if len(cues) != 1 {
		t.Fatalf("expected one complete sentence, got %d: %#v", len(cues), cues)
	}
	if cues[0].StartMS != 22_000 || cues[0].EndMS != 31_000 {
		t.Fatalf("merged sentence timing wrong: %#v", cues[0])
	}
	if cues[0].Text != "这是一个每天都在各大科技公司上演的真实故事，当 AI 既能写代码又能画原型，这两个角色的边界会发生什么变化？" {
		t.Fatalf("merged sentence text wrong: %q", cues[0].Text)
	}
}

func TestSentenceCuesLongUnpunctuatedSplits(t *testing.T) {
	tr := &Transcript{Phrases: []Phrase{{
		Speaker: 1, OffsetMS: 0, DurationMS: 10_000,
		Text: strings.Repeat("这是一个没有标点的超长句子", 20),
	}}}
	cues := SentenceCues(tr)
	if len(cues) < 2 {
		t.Fatalf("expected the unpunctuated run to split, got %d cues", len(cues))
	}
	for _, c := range cues {
		if n := len([]rune(c.Text)); n > cueMaxRunes {
			t.Fatalf("cue exceeds max runes (%d): %q", n, c.Text)
		}
	}
}

func TestSentenceCuesEmptyAndNil(t *testing.T) {
	if got := SentenceCues(nil); got != nil {
		t.Fatalf("nil transcript should give nil cues")
	}
	if got := SentenceCues(&Transcript{Phrases: []Phrase{{Speaker: 1, Text: "   "}}}); len(got) != 0 {
		t.Fatalf("blank phrase should give no cues: %#v", got)
	}
}

func TestSentenceCuesClampOverlappingPhrases(t *testing.T) {
	// Timing validation only requires monotonic offsets, so a provider can
	// report a duration that overruns the next phrase (real Gemini output:
	// a 13s–35s phrase over a 22s–25s successor).
	tr := &Transcript{Phrases: []Phrase{
		{Speaker: 1, OffsetMS: 13_000, DurationMS: 22_000, Text: "我们今天的主题是思维对决。"},
		{Speaker: 1, OffsetMS: 22_000, DurationMS: 3_000, Text: "这是一个每天都在上演的真实故事。"},
	}}
	cues := SentenceCues(tr)
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %#v", len(cues), cues)
	}
	if cues[0].EndMS != 22_000 {
		t.Fatalf("overlapping cue end must clamp to next start: %#v", cues[0])
	}
	if cues[0].Text != "我们今天的主题是思维对决。" || cues[1].StartMS != 22_000 || cues[1].EndMS != 25_000 {
		t.Fatalf("clamp must not disturb text or the successor: %#v", cues)
	}
}

func TestSentenceCuesClampCollapsedCueMergesTextForward(t *testing.T) {
	// Pathological same-offset phrases: the first cue's clamped range
	// collapses, but its words must survive into the transcript.
	tr := &Transcript{Phrases: []Phrase{
		{Speaker: 1, OffsetMS: 5_000, DurationMS: 2_000, Text: "前半句。"},
		{Speaker: 1, OffsetMS: 5_000, DurationMS: 3_000, Text: "后半句。"},
	}}
	cues := SentenceCues(tr)
	if len(cues) != 1 {
		t.Fatalf("collapsed cue should merge into successor, got %d: %#v", len(cues), cues)
	}
	if cues[0].Text != "前半句。后半句。" || cues[0].StartMS != 5_000 || cues[0].EndMS != 8_000 {
		t.Fatalf("merged cue wrong: %#v", cues[0])
	}
}

func TestClampMaxSpeakers(t *testing.T) {
	for in, want := range map[int]int{0: 2, 1: 2, 2: 2, 10: 10, 35: 35, 99: 35} {
		if got := ClampMaxSpeakers(in); got != want {
			t.Fatalf("ClampMaxSpeakers(%d) = %d, want %d", in, got, want)
		}
	}
}
