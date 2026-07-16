package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

func TestDiscussionTranslationStoreLifecycle(t *testing.T) {
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "translations.db"), "", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	d, err := store.Create(context.Background(), "owner", "Source topic", planResponse{
		Script:   &config.DebateTopic{Title: "Source title", Language: "en-US"},
		Markdown: "Source plan",
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	if err := store.BeginTranslation(context.Background(), d.ID, "fr-FR", "test/model"); err != nil {
		t.Fatalf("begin translation: %v", err)
	}
	claimed, err := store.ClaimTranslationRun(context.Background(), d.ID, "fr-FR", 1, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim translation: claimed=%v err=%v", claimed, err)
	}
	bundle := DiscussionTranslationBundle{
		Language: "fr-FR", Title: "Titre", Topic: "Sujet", Markdown: "Plan traduit",
		SummaryMarkdown: "Résumé", CaptionsVTT: "WEBVTT\n\n00:00.000 --> 00:01.000\nBonjour\n",
		Mindmap: &summarizer.MindmapSpec{Root: &summarizer.MindmapNode{ID: "root", Title: "Idée"}},
	}
	usage := SummaryUsage{PromptTokens: 10, CompletionTokens: 7, TotalTokens: 17, LLMCostUSD: 0.01}
	if err := store.SaveTranslation(context.Background(), d.ID, "fr-FR", bundle, "test/model", usage); err != nil {
		t.Fatalf("save translation: %v", err)
	}

	got, err := store.TranslationFor(context.Background(), d.ID, "fr-FR")
	if err != nil || got == nil {
		t.Fatalf("load translation: got=%v err=%v", got, err)
	}
	if got.Status != DiscussionTranslationReady || got.Bundle.Title != "Titre" || got.Usage.TotalTokens != 17 {
		t.Fatalf("unexpected saved translation: %+v", got)
	}
	items, err := store.ListTranslations(context.Background(), d.ID)
	if err != nil || len(items) != 1 || !items[0].Available || items[0].Pending {
		t.Fatalf("unexpected translation metadata: items=%+v err=%v", items, err)
	}
}

func TestApplyTranslationBundleFallsBackFieldByField(t *testing.T) {
	d := &Discussion{Language: "en-US", Title: "Original title", Topic: "Original topic", Markdown: "Original plan"}
	applyTranslationBundle(d, DiscussionTranslationBundle{
		Language: "ja-JP",
		Title:    "翻訳タイトル",
		// Empty topic and markdown deliberately exercise source-language fallback.
	})
	if d.MainLanguage != "en-US" || d.Language != "ja-JP" {
		t.Fatalf("language metadata not applied: %+v", d)
	}
	if d.Title != "翻訳タイトル" || d.Topic != "Original topic" || d.Markdown != "Original plan" {
		t.Fatalf("field fallback was not preserved: %+v", d)
	}
}

func TestCollectVTTTranslationSlotsPreservesTiming(t *testing.T) {
	vtt := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\nHello world\n\n2\n00:00:03.000 --> 00:00:04.000\nNext line\n"
	var slots []translationSlot
	collectVTTTranslationSlots(&vtt, &slots)
	if len(slots) != 2 {
		t.Fatalf("caption slots=%d, want 2", len(slots))
	}
	for _, slot := range slots {
		slot.Apply("translated:" + slot.ID)
	}
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:02.500") ||
		!strings.Contains(vtt, "00:00:03.000 --> 00:00:04.000") {
		t.Fatalf("caption timing changed: %s", vtt)
	}
	if strings.Contains(vtt, "Hello world") || strings.Contains(vtt, "Next line") {
		t.Fatalf("caption text was not replaced: %s", vtt)
	}
}

func TestAppendTranslationSlotsChunksLargeDocuments(t *testing.T) {
	original := strings.Repeat("段落 content with words\n", 700)
	value := original
	var slots []translationSlot
	appendTranslationSlots(&slots, "document", &value)
	if len(slots) < 2 {
		t.Fatalf("large document was not chunked: %d slots", len(slots))
	}
	for i, slot := range slots {
		if len([]rune(slot.Text)) > 6_000 {
			t.Fatalf("slot %d exceeds rune limit", i)
		}
		slot.Apply("[translated]" + slot.Text)
	}
	if strings.Count(value, "[translated]") != len(slots) || !strings.Contains(value, "段落") {
		t.Fatalf("translated chunks were not reassembled correctly")
	}
}

func TestTranslationBundleSlotsMutateReturnedScalarFields(t *testing.T) {
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "translation-slots.db"), "", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	d, err := store.Create(context.Background(), "owner", "Source topic", planResponse{
		Script: &config.DebateTopic{
			Title: "Source script title", Language: "en-US",
			Host: config.AgentSpec{Name: "Source host", Aspect: "Source host aspect"},
			AudioBookSpeakers: []config.AudioBookSpeaker{{
				Name: "Source character", Description: "Source character description",
			}},
			TranscriptSegments: []config.TranscriptSegment{{
				Speaker: "Source uploaded speaker", Text: "Source uploaded transcript",
			}},
		},
		Markdown: "Source plan",
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	if err := store.SaveSummary(context.Background(), d.ID, SummaryDocTypeSummary,
		"Source summary", "test/model", SummaryUsage{}); err != nil {
		t.Fatalf("save summary: %v", err)
	}
	d.Lines = []DiscussionLine{{Speaker: "Host", Text: "Source transcript"}}

	s := &Server{d: Deps{Discussions: store}}
	bundle, slots, err := s.translationBundle(context.Background(), d, "ja-JP")
	if err != nil {
		t.Fatalf("build translation bundle: %v", err)
	}
	for _, slot := range slots {
		slot.Apply("translated:" + slot.ID)
	}

	if bundle.Title != "translated:title" {
		t.Fatalf("title = %q, want translated scalar field", bundle.Title)
	}
	if bundle.SummaryMarkdown != "translated:summary" {
		t.Fatalf("summary = %q, want translated scalar field", bundle.SummaryMarkdown)
	}
	if len(bundle.Lines) != 1 || bundle.Lines[0].Text != "translated:line.0.text" {
		t.Fatalf("transcript was not translated: %+v", bundle.Lines)
	}
	if bundle.Lines[0].Speaker != "translated:line.0.speaker" {
		t.Fatalf("transcript speaker = %q, want translated speaker", bundle.Lines[0].Speaker)
	}
	if bundle.Script.Host.Name != "translated:plan.host.name" {
		t.Fatalf("host name = %q, want translated speaker", bundle.Script.Host.Name)
	}
	if bundle.Script.AudioBookSpeakers[0].Name != "translated:plan.speaker.0.name" {
		t.Fatalf("character name = %q, want translated speaker", bundle.Script.AudioBookSpeakers[0].Name)
	}
	if bundle.Script.TranscriptSegments[0].Speaker != "translated:plan.segment.0.speaker" {
		t.Fatalf("uploaded transcript speaker = %q, want translated speaker", bundle.Script.TranscriptSegments[0].Speaker)
	}
}

func TestSupportedPodcastLanguages(t *testing.T) {
	for _, language := range []string{"en-US", "zh-CN", "zh-TW", "ja-JP", "ko-KR", "es-ES", "fr-FR", "de-DE"} {
		if !supportedPodcastLanguage(language) {
			t.Errorf("expected %s to be supported", language)
		}
	}
	for _, language := range []string{"", "en", "xx-YY"} {
		if supportedPodcastLanguage(language) {
			t.Errorf("expected %q to be rejected", language)
		}
	}
}

func TestTranslationPermissionGatesServerDrivenAction(t *testing.T) {
	without := Permissions{}
	gated, allowed := without.allowsAction("translate-podcast")
	if !gated || allowed {
		t.Fatalf("translation should be gated and disabled without permission: gated=%v allowed=%v", gated, allowed)
	}
	with := Permissions{Features: PermissionFeatures{CanTranslatePodcast: true}}
	gated, allowed = with.allowsAction("translate-podcast")
	if !gated || !allowed {
		t.Fatalf("translation should be enabled with permission: gated=%v allowed=%v", gated, allowed)
	}
}
