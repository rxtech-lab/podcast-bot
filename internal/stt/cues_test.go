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
	if len(cues) != 3 {
		t.Fatalf("expected 3 cues, got %d: %#v", len(cues), cues)
	}
	if cues[0].Text != "Good afternoon," || cues[0].StartMS != 100 || cues[0].EndMS != 600 {
		t.Fatalf("cue 0 wrong: %#v", cues[0])
	}
	if cues[1].Text != "everyone." || cues[1].StartMS != 700 || cues[1].EndMS != 1100 {
		t.Fatalf("cue 1 wrong: %#v", cues[1])
	}
	if cues[2].Text != "Welcome!" || cues[2].StartMS != 1200 || cues[2].EndMS != 1700 {
		t.Fatalf("cue 2 wrong: %#v", cues[2])
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
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %#v", len(cues), cues)
	}
	if cues[0].Text != "欢迎来到今天的讨论，" {
		t.Fatalf("cue 0 text wrong (no spaces expected): %q", cues[0].Text)
	}
	if strings.Contains(cues[0].Text, " ") {
		t.Fatalf("CJK cue must not contain spaces: %q", cues[0].Text)
	}
	if cues[1].Text != "我们开始。" {
		t.Fatalf("cue 1 text wrong: %q", cues[1].Text)
	}
	if cues[0].Speaker != 2 || cues[1].Speaker != 2 {
		t.Fatalf("speaker not preserved: %#v", cues)
	}
}

func TestSentenceCuesTextFallbackProportional(t *testing.T) {
	tr := &Transcript{Phrases: []Phrase{{
		Speaker: 1, OffsetMS: 1000, DurationMS: 3000,
		Text: "各位听众朋友们，欢迎来到今天的圆桌讨论。",
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

func TestClampMaxSpeakers(t *testing.T) {
	for in, want := range map[int]int{0: 2, 1: 2, 2: 2, 10: 10, 35: 35, 99: 35} {
		if got := ClampMaxSpeakers(in); got != want {
			t.Fatalf("ClampMaxSpeakers(%d) = %d, want %d", in, got, want)
		}
	}
}
