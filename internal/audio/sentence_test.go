package audio

import (
	"reflect"
	"testing"
)

func TestSentenceSplitterASCII(t *testing.T) {
	s := &SentenceSplitter{}
	got := s.Push("Hello world. This is fine! ")
	want := []string{"Hello world.", "This is fine!"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
	if rest := s.Push("Trailing"); len(rest) != 0 {
		t.Errorf("unexpected sentences: %v", rest)
	}
	if final := s.Flush(); !reflect.DeepEqual(final, []string{"Trailing"}) {
		t.Errorf("flush got %v", final)
	}
}

func TestSentenceSplitterCJK(t *testing.T) {
	s := &SentenceSplitter{}
	got := s.Push("你好世界。这是一个测试！")
	want := []string{"你好世界。", "这是一个测试！"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

func TestSentenceSplitterCluster(t *testing.T) {
	s := &SentenceSplitter{}
	got := s.Push("Wait... Really?! Yes.")
	got = append(got, s.Flush()...) // last "Yes." has no trailing space — needs flush
	want := []string{"Wait...", "Really?!", "Yes."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

func TestSentenceSplitterStreaming(t *testing.T) {
	s := &SentenceSplitter{}
	out := s.Push("Hel")
	out = append(out, s.Push("lo")...)
	out = append(out, s.Push(" wor")...)
	out = append(out, s.Push("ld. Next.")...)
	out = append(out, s.Flush()...)
	want := []string{"Hello world.", "Next."}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got=%v want=%v", out, want)
	}
}

// The puzzle host's answer pattern is a yes/no acknowledgment followed by a
// short clarifying clause. With MinChars=6 the splitter should hold "是。"
// back and emit the merged "是。 ..." once the clause's terminator arrives,
// so TTS produces one coherent clip instead of two flickering ones.
func TestSentenceSplitterMinCharsPuzzleAck(t *testing.T) {
	s := &SentenceSplitter{MinChars: 6}
	got := s.Push("是。 但更精确地说,他没去车站。")
	got = append(got, s.Flush()...)
	want := []string{"是。 但更精确地说,他没去车站。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

// A short trailing fragment with no follow-up must still emit at flush —
// otherwise audio for the final piece would be silently dropped. The
// splitter only coalesces forward (short → next), so a long sentence
// followed by a short one stays as two emits, and Flush rescues the
// trailing short one.
func TestSentenceSplitterMinCharsFlushShortTail(t *testing.T) {
	s := &SentenceSplitter{MinChars: 6}
	got := s.Push("這是一個比較長的開場句。 是。")
	got = append(got, s.Flush()...)
	want := []string{"這是一個比較長的開場句。", "是。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}
