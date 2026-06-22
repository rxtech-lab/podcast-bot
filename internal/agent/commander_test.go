package agent

import "testing"

func TestCleanDirectorJSONStripsFencedJSON(t *testing.T) {
	raw := "```json\n{\"action\":\"keep\",\"music_index\":-1,\"reason\":\"stable\"}\n```"
	got := cleanDirectorJSON(raw)
	want := "{\"action\":\"keep\",\"music_index\":-1,\"reason\":\"stable\"}"
	if got != want {
		t.Fatalf("cleanDirectorJSON() = %q, want %q", got, want)
	}
}

func TestCleanDirectorJSONLeavesPlainJSON(t *testing.T) {
	raw := "  {\"action\":\"keep\"}  "
	got := cleanDirectorJSON(raw)
	if got != "{\"action\":\"keep\"}" {
		t.Fatalf("cleanDirectorJSON() = %q", got)
	}
}
