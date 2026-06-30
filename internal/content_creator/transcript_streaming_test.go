package contentcreator

import (
	"testing"

	"github.com/sirily11/debate-bot/internal/agent"
)

// TestSplitCharacterSpansFromCarriesIndex verifies that an open <char-N> span
// continues across sentence boundaries within a turn: the second sentence must
// stay attributed to the guest until the closing marker, instead of falling
// back to the narrator.
func TestSplitCharacterSpansFromCarriesIndex(t *testing.T) {
	// Sentence 1 opens a guest span but never closes it.
	_, spans1, had1, end1 := splitCharacterSpansFrom("Maya said <char-0>Hello there", -1)
	if !had1 {
		t.Fatal("sentence 1: expected markers detected")
	}
	if end1 != 0 {
		t.Fatalf("sentence 1: carried index = %d, want 0", end1)
	}
	if spans1[0].idx != -1 || spans1[len(spans1)-1].idx != 0 {
		t.Fatalf("sentence 1: spans = %+v, want narrator then char-0", spans1)
	}

	// Sentence 2 has no markers of its own; carried index keeps it as the guest.
	_, spans2, had2, end2 := splitCharacterSpansFrom("how are you", end1)
	if !had2 {
		t.Fatal("sentence 2: expected had=true because a guest span is open")
	}
	if end2 != 0 {
		t.Fatalf("sentence 2: carried index = %d, want 0", end2)
	}
	if len(spans2) != 1 || spans2[0].idx != 0 {
		t.Fatalf("sentence 2: spans = %+v, want single char-0 span", spans2)
	}

	// Sentence 3 closes the span; text after the close is narrator again.
	_, spans3, _, end3 := splitCharacterSpansFrom("today?</char-0> she smiled", end2)
	if end3 != -1 {
		t.Fatalf("sentence 3: carried index = %d, want -1 after close", end3)
	}
	if spans3[0].idx != 0 || spans3[len(spans3)-1].idx != -1 {
		t.Fatalf("sentence 3: spans = %+v, want char-0 then narrator", spans3)
	}
}

// TestSplitCharacterSpansNoMarkersDefault confirms the plain-narrator shortcut
// is unchanged: no markers, starting at narrator, yields one narrator span.
func TestSplitCharacterSpansNoMarkersDefault(t *testing.T) {
	clean, spans, had, end := splitCharacterSpansFrom("Just narration.", -1)
	if had || end != -1 || clean != "Just narration." {
		t.Fatalf("got had=%v end=%d clean=%q", had, end, clean)
	}
	if len(spans) != 1 || spans[0].idx != -1 {
		t.Fatalf("spans = %+v, want single narrator span", spans)
	}
}

// TestAppendAgentSegmentMergesSameSpeaker verifies consecutive same-speaker
// sentences of one turn grow a single line rather than creating a new bubble.
func TestAppendAgentSegmentMergesSameSpeaker(t *testing.T) {
	tr := NewTranscript()
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Narrator", Role: agent.RoleSeriesHost, Text: "First."})
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Narrator", Role: agent.RoleSeriesHost, Text: "Second."})
	lines := tr.Snapshot()
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1 merged line: %+v", len(lines), lines)
	}
	if lines[0].Text != "First. Second." {
		t.Fatalf("merged text = %q", lines[0].Text)
	}
}

// TestAppendAgentSegmentSplitsOnSpeakerChange verifies a guest turn becomes its
// own bubble instead of being folded into the narrator's line.
func TestAppendAgentSegmentSplitsOnSpeakerChange(t *testing.T) {
	tr := NewTranscript()
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Narrator", Role: agent.RoleSeriesHost, Text: "The guest replied."})
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Guest", Role: agent.RoleSeriesHost, Text: "Absolutely."})
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Narrator", Role: agent.RoleSeriesHost, Text: "She continued."})
	lines := tr.Snapshot()
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (narrator/guest/narrator): %+v", len(lines), lines)
	}
	if lines[0].Speaker != "Narrator" || lines[1].Speaker != "Guest" || lines[2].Speaker != "Narrator" {
		t.Fatalf("speakers = %q/%q/%q", lines[0].Speaker, lines[1].Speaker, lines[2].Speaker)
	}
}

// TestUserMessageInterleavesInOrder is the ordering regression: a message sent
// mid-turn must land between the agent text before and after it, and the agent's
// continuation must start a fresh bubble instead of growing the earlier one.
func TestUserMessageInterleavesInOrder(t *testing.T) {
	tr := NewTranscript()
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Narrator", Role: agent.RoleSeriesHost, Text: "Before the question."})
	tr.AppendUser("Alice", "What about X?")
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Narrator", Role: agent.RoleSeriesHost, Text: "After the question."})
	lines := tr.Snapshot()
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3: %+v", len(lines), lines)
	}
	if lines[0].Text != "Before the question." || lines[1].Speaker != "Alice" || lines[2].Text != "After the question." {
		t.Fatalf("order wrong: %+v", lines)
	}
	if lines[2].Text != "After the question." {
		t.Fatalf("agent continuation merged into the pre-message bubble: %q", lines[2].Text)
	}
}

// TestCloseTurnAttachesMetadata confirms turn-level sources/judgement, known
// only after the turn finishes, land on the open line and that the next turn
// does not merge into it.
func TestCloseTurnAttachesMetadata(t *testing.T) {
	tr := NewTranscript()
	tr.AppendAgentSegment(1, agent.TranscriptLine{Speaker: "Maya", Role: agent.RoleDiscussant, Text: "A strong claim."})
	tr.CloseTurn(1, []agent.TranscriptSource{{URL: "https://example.com"}}, "Needs evidence.")
	// Same speaker, new turn: must be a separate line, not a merge.
	tr.AppendAgentSegment(2, agent.TranscriptLine{Speaker: "Maya", Role: agent.RoleDiscussant, Text: "Another turn."})
	lines := tr.Snapshot()
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %+v", len(lines), lines)
	}
	if len(lines[0].Sources) != 1 || lines[0].JudgementComment != "Needs evidence." {
		t.Fatalf("metadata not attached: %+v", lines[0])
	}
}
