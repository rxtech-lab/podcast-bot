package server

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func testAudioBookPlan(chapterCount int) *config.DebateTopic {
	chapters := make([]config.AudioBookChapter, 0, chapterCount)
	titles := []string{"Origins", "Rising", "Turning", "Falling", "Aftermath", "Echoes", "Reunion", "Reckoning", "Twilight", "Dawn"}
	for i := 0; i < chapterCount; i++ {
		title := titles[i%len(titles)]
		chapters = append(chapters, config.AudioBookChapter{Title: title, Summary: "What happens in " + title + "."})
	}
	return &config.DebateTopic{
		Title:             "A Long Book",
		Type:              config.ContentTypeAudioBook,
		Language:          "en-US",
		TotalMinutes:      chapterCount * 8,
		SegmentMaxSeconds: 60,
		Channel:           "default",
		AudioBookHost:     config.AgentSpec{Name: "Narrator", Model: "openai/gpt-4o"},
		AudioBookStyle:    config.AudioBookStyleAudioBook,
		AudioBookChapters: chapters,
		Background:        "A concise overall summary.",
	}
}

func TestDeriveAudioBookBatchScript(t *testing.T) {
	root := testAudioBookPlan(10)
	batch, err := deriveAudioBookBatchScript(root, []int{8, 6, 7}, "Earlier chapters covered the origins.", false)
	if err != nil {
		t.Fatalf("derive batch: %v", err)
	}
	if got := len(batch.AudioBookChapters); got != 3 {
		t.Fatalf("batch chapters = %d, want 3", got)
	}
	if want := []int{6, 7, 8}; len(batch.AudioBookChapterIndices) != 3 ||
		batch.AudioBookChapterIndices[0] != want[0] || batch.AudioBookChapterIndices[2] != want[2] {
		t.Fatalf("batch indices = %v, want %v", batch.AudioBookChapterIndices, want)
	}
	if batch.TotalMinutes != 24 {
		t.Fatalf("batch minutes = %d, want 24", batch.TotalMinutes)
	}
	if !strings.Contains(batch.Surface, "### Chapter 6:") || !strings.Contains(batch.Surface, "### Chapter 8:") {
		t.Fatalf("batch outline should keep global chapter numbers:\n%s", batch.Surface)
	}
	if strings.Contains(batch.Surface, "### Chapter 1:") {
		t.Fatalf("batch outline should not renumber from 1:\n%s", batch.Surface)
	}
	if !strings.Contains(batch.Surface, "Previously narrated") || !strings.Contains(batch.Surface, "Earlier chapters covered the origins.") {
		t.Fatalf("batch outline should carry the previously-narrated block:\n%s", batch.Surface)
	}
	if !strings.Contains(batch.Title, "Chapters 6-8") {
		t.Fatalf("batch title should carry the chapter range, got %q", batch.Title)
	}
	// The root plan must be untouched.
	if len(root.AudioBookChapters) != 10 || len(root.AudioBookChapterIndices) != 0 {
		t.Fatalf("root plan mutated: %d chapters, indices %v", len(root.AudioBookChapters), root.AudioBookChapterIndices)
	}
}

func TestDeriveAudioBookBatchScriptFullSelectionKeepsTitle(t *testing.T) {
	root := testAudioBookPlan(3)
	batch, err := deriveAudioBookBatchScript(root, []int{1, 2, 3}, "", false)
	if err != nil {
		t.Fatalf("derive batch: %v", err)
	}
	if batch.Title != root.Title {
		t.Fatalf("full-selection batch title = %q, want %q", batch.Title, root.Title)
	}
	if batch.TotalMinutes != 24 {
		t.Fatalf("batch minutes = %d, want 24", batch.TotalMinutes)
	}
}

func TestDeriveAudioBookBatchScriptMinimumMinutes(t *testing.T) {
	root := testAudioBookPlan(6)
	batch, err := deriveAudioBookBatchScript(root, []int{1}, "", false)
	if err != nil {
		t.Fatalf("derive batch: %v", err)
	}
	if batch.TotalMinutes != 15 {
		t.Fatalf("single-chapter batch minutes = %d, want 15", batch.TotalMinutes)
	}
	e2e, err := deriveAudioBookBatchScript(root, []int{1}, "", true)
	if err != nil {
		t.Fatalf("derive e2e batch: %v", err)
	}
	if e2e.TotalMinutes != 1 {
		t.Fatalf("e2e batch minutes = %d, want 1", e2e.TotalMinutes)
	}
}

func TestDeriveAudioBookBatchScriptRejectsBadInput(t *testing.T) {
	root := testAudioBookPlan(4)
	if _, err := deriveAudioBookBatchScript(root, nil, "", false); err == nil {
		t.Fatalf("empty selection should fail")
	}
	if _, err := deriveAudioBookBatchScript(root, []int{5}, "", false); err == nil {
		t.Fatalf("out-of-range selection should fail")
	}
	notAudioBook := &config.DebateTopic{Type: config.ContentTypeDiscussion}
	if _, err := deriveAudioBookBatchScript(notAudioBook, []int{1}, "", false); err == nil {
		t.Fatalf("non-audiobook plan should fail")
	}
}

func statesFor(total int, done, generating []int) []audioBookChapterState {
	states := make([]audioBookChapterState, total)
	for i := range states {
		states[i] = audioBookChapterState{Index: i + 1, Status: chapterStatusPending}
	}
	for _, idx := range done {
		states[idx-1].Status = chapterStatusDone
	}
	for _, idx := range generating {
		states[idx-1].Status = chapterStatusGenerating
	}
	return states
}

func TestValidateChapterSelection(t *testing.T) {
	cases := []struct {
		name    string
		states  []audioBookChapterState
		sel     []int
		wantErr string
	}{
		{"empty", statesFor(10, nil, nil), nil, "select at least one chapter"},
		{"over cap", statesFor(10, nil, nil), []int{1, 2, 3, 4, 5, 6}, "at most 5 chapters"},
		{"out of range low", statesFor(10, nil, nil), []int{0}, "invalid chapter selection: 0"},
		{"out of range high", statesFor(10, nil, nil), []int{11}, "invalid chapter selection: 11"},
		{"duplicate", statesFor(10, nil, nil), []int{2, 2}, "invalid chapter selection: 2"},
		{"already done", statesFor(10, []int{3}, nil), []int{3}, "chapter 3 is already generated"},
		{"generating", statesFor(10, nil, []int{4}), []int{4}, "chapter 4 is currently generating"},
		{"valid", statesFor(10, []int{1, 2}, nil), []int{3, 4, 5}, ""},
		{"valid non-contiguous", statesFor(10, []int{1}, nil), []int{2, 9}, ""},
		{"valid max batch", statesFor(10, nil, nil), []int{1, 2, 3, 4, 5}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChapterSelection(tc.states, tc.sel)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestChapterClaim(t *testing.T) {
	ready := DiscussionReady
	// Batch child with recorded indices.
	batch := &Discussion{Status: ready, JobID: "job-1", Script: &config.DebateTopic{
		Type:                    config.ContentTypeAudioBook,
		AudioBookChapterIndices: []int{6, 7, 99},
	}}
	if got := chapterClaim(batch, 10); len(got) != 2 || got[0] != 6 || got[1] != 7 {
		t.Fatalf("batch claim = %v, want [6 7] (out-of-range dropped)", got)
	}
	// Legacy single-shot audiobook claims everything once its job started.
	legacy := &Discussion{Status: ready, JobID: "job-2", Script: &config.DebateTopic{Type: config.ContentTypeAudioBook}}
	if got := chapterClaim(legacy, 3); len(got) != 3 {
		t.Fatalf("legacy claim = %v, want all 3", got)
	}
	// Never generated → no claim.
	planning := &Discussion{Status: DiscussionPlanning, Script: &config.DebateTopic{Type: config.ContentTypeAudioBook, AudioBookChapterIndices: []int{1}}}
	if got := chapterClaim(planning, 3); len(got) != 0 {
		t.Fatalf("planning claim = %v, want none", got)
	}
	// Failed runs release their chapters.
	failed := &Discussion{Status: DiscussionFailed, JobID: "job-3", Script: &config.DebateTopic{Type: config.ContentTypeAudioBook, AudioBookChapterIndices: []int{2}}}
	if got := chapterClaim(failed, 3); len(got) != 0 {
		t.Fatalf("failed claim = %v, want none", got)
	}
}

func TestChapterRangeLabel(t *testing.T) {
	cases := []struct {
		sel  []int
		want string
	}{
		{[]int{4}, "Chapter 4"},
		{[]int{6, 7, 8}, "Chapters 6-8"},
		{[]int{6, 9}, "Chapters 6, 9"},
	}
	for _, tc := range cases {
		if got := chapterRangeLabel(tc.sel); got != tc.want {
			t.Fatalf("chapterRangeLabel(%v) = %q, want %q", tc.sel, got, tc.want)
		}
	}
}

func TestAlbumPositionFor(t *testing.T) {
	if got := albumPositionFor([]int{6, 7, 8}); got != 1006 {
		t.Fatalf("batch position = %d, want 1006", got)
	}
	if got := albumPositionFor(nil); got != 0 {
		t.Fatalf("non-batch position = %d, want 0", got)
	}
}
