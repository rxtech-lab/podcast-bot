package summarizer

import (
	"strings"
	"testing"
)

func TestSessionAssemblesPartsInOrder(t *testing.T) {
	s := &summarySession{}
	// Write parts out of order; they must concatenate by ascending part_index.
	if _, terminal := s.dispatch("write_summary_chunk", `{"part_index":1,"markdown":"## Second"}`); terminal {
		t.Fatal("write_summary_chunk must not be terminal")
	}
	if _, terminal := s.dispatch("write_summary_chunk", `{"part_index":0,"markdown":"# First"}`); terminal {
		t.Fatal("write_summary_chunk must not be terminal")
	}
	if !s.hasParts() {
		t.Fatal("expected parts to be recorded")
	}
	_, terminal := s.dispatch("finalize_summary", `{}`)
	if !terminal {
		t.Fatal("finalize_summary must be terminal")
	}
	got := s.assemble()
	want := "# First\n\n## Second"
	if got != want {
		t.Fatalf("assemble() = %q, want %q", got, want)
	}
}

func TestFinalizeBeforeWritingIsNotTerminal(t *testing.T) {
	s := &summarySession{}
	result, terminal := s.dispatch("finalize_summary", `{}`)
	if terminal {
		t.Fatal("finalize_summary with no chunks must not terminate the loop")
	}
	if !strings.Contains(result, "no chunks written") {
		t.Fatalf("expected a nudge to write chunks first, got %q", result)
	}
}

func TestWriteChunkRejectsEmptyMarkdown(t *testing.T) {
	s := &summarySession{}
	result, _ := s.dispatch("write_summary_chunk", `{"part_index":0,"markdown":"  "}`)
	if !strings.Contains(result, "empty") {
		t.Fatalf("expected empty-markdown rejection, got %q", result)
	}
	if s.hasParts() {
		t.Fatal("empty markdown must not be recorded as a part")
	}
}

func TestSegmentTranscriptChunksLongInput(t *testing.T) {
	short := "a: hello\nb: world"
	if segs := segmentTranscript(short); len(segs) != 1 || segs[0] != short {
		t.Fatalf("short transcript should be a single segment, got %d segments", len(segs))
	}

	var sb strings.Builder
	line := strings.Repeat("x", 200)
	for sb.Len() <= maxTranscriptChars+transcriptSegmentChars {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	segs := segmentTranscript(sb.String())
	if len(segs) < 2 {
		t.Fatalf("long transcript should split into multiple segments, got %d", len(segs))
	}
}
