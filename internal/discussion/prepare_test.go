package discussion

import "testing"

func TestDiscussionMusicUsesOneGeneratedBed(t *testing.T) {
	if got, want := len(bedSpecs), 1; got != want {
		t.Fatalf("discussion music bed specs = %d, want %d", got, want)
	}
	if bedSpecs[0].label == "" || bedSpecs[0].prompt == "" {
		t.Fatal("discussion music bed spec must keep a label and prompt")
	}
}
