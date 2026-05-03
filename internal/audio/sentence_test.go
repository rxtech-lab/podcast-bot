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
