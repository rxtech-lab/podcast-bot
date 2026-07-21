package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func audioBookDraftArgs(t *testing.T, chapters []map[string]any) string {
	t.Helper()
	args := map[string]any{
		"title":           "The Test Book",
		"style":           "audiobook",
		"overall_summary": "A summary.",
		"narrator":        map[string]any{"name": "Evelyn", "gender": "female"},
		"speakers": []map[string]any{
			{"name": "Captain Reyes", "gender": "male", "description": "gruff sea captain"},
		},
		"chapters": chapters,
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal draft args: %v", err)
	}
	return string(b)
}

func audioBookTestSession(source string, store func(context.Context, int, []byte) (string, error)) *conversationSession {
	return &conversationSession{
		planner: &Planner{env: &config.Env{}},
		opts: ConversationOptions{
			Type:                config.ContentTypeAudioBook,
			Language:            "en-US",
			AgentModel:          "test-model",
			AudioBookSource:     source,
			StoreChapterContent: store,
		},
	}
}

func TestAssembleAudioBookPlanSplitsAndStores(t *testing.T) {
	var storedChapters []int
	var storedBytes [][]byte
	store := func(_ context.Context, chapter int, content []byte) (string, error) {
		storedChapters = append(storedChapters, chapter)
		storedBytes = append(storedBytes, content)
		return fmt.Sprintf("audiobooks/d1/chapters/%02d-feed.md", chapter), nil
	}
	s := audioBookTestSession(splitTestSource, store)
	args := audioBookDraftArgs(t, []map[string]any{
		{"title": "Foreword", "summary": "s", "start_index": 1},
		{"title": "The Road", "summary": "s", "start_index": 2},
		{"title": "The Clinic", "summary": "s", "start_index": 4},
	})
	res, note, err := s.assembleAudioBookPlan(context.Background(), args)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	topic := res.Script
	if len(topic.AudioBookChapters) != 3 {
		t.Fatalf("chapters = %d, want 3", len(topic.AudioBookChapters))
	}
	for i, ch := range topic.AudioBookChapters {
		if ch.ContentKey == "" || ch.ContentChars == 0 || ch.StartMarker == "" {
			t.Fatalf("chapter %d missing content fields: %+v", i+1, ch)
		}
	}
	if len(storedChapters) != 3 || storedChapters[0] != 1 || storedChapters[2] != 3 {
		t.Fatalf("stored chapters = %v", storedChapters)
	}
	if !strings.Contains(string(storedBytes[2]), "Doctor Mira") {
		t.Fatalf("chapter 3 stored content wrong: %q", storedBytes[2])
	}
	if !strings.Contains(note, "split into 3 chapters") {
		t.Fatalf("note should describe the split: %q", note)
	}
	// The re-rendered markdown must round-trip the new fields.
	if !strings.Contains(res.Markdown, "content_key") {
		t.Fatalf("plan markdown missing content_key:\n%s", res.Markdown)
	}
}

func TestAssembleAudioBookPlanRejectsMissingMarkers(t *testing.T) {
	s := audioBookTestSession(splitTestSource, nil)
	args := audioBookDraftArgs(t, []map[string]any{
		{"title": "One", "summary": "s"},
		{"title": "Two", "summary": "s"},
	})
	_, _, err := s.assembleAudioBookPlan(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "start_index") {
		t.Fatalf("expected a start_index nudge error, got %v", err)
	}
}

func TestAssembleAudioBookPlanBadMarkerIsRetryableToolError(t *testing.T) {
	s := audioBookTestSession(splitTestSource, nil)
	args := audioBookDraftArgs(t, []map[string]any{
		{"title": "One", "summary": "s", "start_index": 4},
		{"title": "Two", "summary": "s", "start_index": 2},
	})
	_, _, err := s.assembleAudioBookPlan(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "strictly increasing") {
		t.Fatalf("expected monotonicity error, got %v", err)
	}
	if _, ok := err.(*audioBookSplitError); !ok {
		t.Fatalf("error should be model-facing, got %T", err)
	}
}

func TestAssembleAudioBookPlanStoreFailureFallsBackToOutline(t *testing.T) {
	calls := 0
	store := func(_ context.Context, chapter int, _ []byte) (string, error) {
		calls++
		if chapter == 2 {
			return "", fmt.Errorf("bucket unavailable")
		}
		return fmt.Sprintf("audiobooks/d1/chapters/%02d-feed.md", chapter), nil
	}
	s := audioBookTestSession(splitTestSource, store)
	args := audioBookDraftArgs(t, []map[string]any{
		{"title": "One", "summary": "s", "start_index": 1},
		{"title": "Two", "summary": "s", "start_index": 4},
	})
	res, _, err := s.assembleAudioBookPlan(context.Background(), args)
	if err != nil {
		t.Fatalf("storage failure must not reject the plan: %v", err)
	}
	for i, ch := range res.Script.AudioBookChapters {
		if ch.ContentKey != "" {
			t.Fatalf("chapter %d should have no content key after a storage failure (all-or-nothing), got %q", i+1, ch.ContentKey)
		}
	}
}

func TestAssembleAudioBookPlanWithoutSourceKeepsLegacyPath(t *testing.T) {
	s := audioBookTestSession("", nil)
	args := audioBookDraftArgs(t, []map[string]any{
		{"title": "One", "summary": "s"},
	})
	res, note, err := s.assembleAudioBookPlan(context.Background(), args)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if note != "" {
		t.Fatalf("no-source plans should have no note, got %q", note)
	}
	if res.Script.AudioBookChapters[0].ContentKey != "" {
		t.Fatal("no-source plan must not carry content keys")
	}
}

func TestMergeExtractedCastAppendsMissingSpeakers(t *testing.T) {
	topic := &config.DebateTopic{
		AudioBookHost: config.AgentSpec{Name: "Evelyn"},
		AudioBookSpeakers: []config.AudioBookSpeaker{
			{Name: "Captain Reyes", Gender: "male"},
		},
	}
	mergeExtractedCast(topic, []extractedCharacter{
		{Name: "Captain Reyes", Gender: "male", Description: "dup — must not be re-added"},
		{Name: "Evelyn", Gender: "female", Description: "narrator — must be skipped"},
		{Name: "Doctor Mira", Gender: "female", Description: "brisk clinician"},
	}, "test-model")
	if len(topic.AudioBookSpeakers) != 2 {
		t.Fatalf("speakers = %d, want 2 (%+v)", len(topic.AudioBookSpeakers), topic.AudioBookSpeakers)
	}
	added := topic.AudioBookSpeakers[1]
	if added.Name != "Doctor Mira" || added.Gender != "female" || added.Model != "test-model" {
		t.Fatalf("merged speaker wrong: %+v", added)
	}
}
