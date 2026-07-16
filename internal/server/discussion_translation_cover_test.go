package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func newTranslationCoverStore(t *testing.T) (*DiscussionStore, *Discussion) {
	t.Helper()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "translation-covers.db"), "", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d, err := store.Create(context.Background(), "cookie:Owner", "Source topic", planResponse{
		Script:   &config.DebateTopic{Title: "Source title", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "Source plan",
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	return store, d
}

func saveReadyTranslation(t *testing.T, store *DiscussionStore, id, language, title string) {
	t.Helper()
	if err := store.BeginTranslation(context.Background(), id, language, "test/model"); err != nil {
		t.Fatalf("begin translation: %v", err)
	}
	if err := store.SaveTranslation(context.Background(), id, language, DiscussionTranslationBundle{
		Language: language, Title: title,
	}, "test/model", SummaryUsage{}); err != nil {
		t.Fatalf("save translation: %v", err)
	}
}

func TestSetTranslationCoverStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store, d := newTranslationCoverStore(t)
	saveReadyTranslation(t, store, d.ID, "fr-FR", "Titre traduit")
	cover := DiscussionCover{Type: "gradient", GradientStart: "#ff0000", GradientEnd: "#0000ff"}

	if got, err := store.SetTranslationCover(ctx, "cookie:Owner", d.ID, "ja-JP", cover); err != nil || got != nil {
		t.Fatalf("missing translation should be a no-op: got=%+v err=%v", got, err)
	}
	if got, err := store.SetTranslationCover(ctx, "cookie:Intruder", d.ID, "fr-FR", cover); err != nil || got != nil {
		t.Fatalf("non-owner should be a no-op: got=%+v err=%v", got, err)
	}

	got, err := store.SetTranslationCover(ctx, "cookie:Owner", d.ID, "fr-FR", cover)
	if err != nil || got == nil || got.Cover != cover {
		t.Fatalf("set translation cover = %+v, err=%v", got, err)
	}
	items, err := store.ListTranslations(ctx, d.ID)
	if err != nil || len(items) != 1 || items[0].Cover == nil || *items[0].Cover != cover {
		t.Fatalf("translation meta should carry cover: items=%+v err=%v", items, err)
	}

	// Re-translating the language must keep its cover art.
	saveReadyTranslation(t, store, d.ID, "fr-FR", "Titre retraduit")
	after, err := store.TranslationFor(ctx, d.ID, "fr-FR")
	if err != nil || after == nil || after.Cover != cover || after.Bundle.Title != "Titre retraduit" {
		t.Fatalf("re-translation should preserve cover: got=%+v err=%v", after, err)
	}

	// An image cover with a durable key must not persist its presigned URL.
	imageCover := DiscussionCover{Type: "ai", ImageKey: "covers/owner/x.webp", ImageURL: "https://signed.example/x", Prompt: "art"}
	if got, err = store.SetTranslationCover(ctx, "cookie:Owner", d.ID, "fr-FR", imageCover); err != nil || got == nil {
		t.Fatalf("set image cover: got=%+v err=%v", got, err)
	}
	if got.Cover.ImageURL != "" || got.Cover.ImageKey != imageCover.ImageKey {
		t.Fatalf("stored image cover should be key-only: %+v", got.Cover)
	}

	// An empty cover clears the columns and drops the meta cover.
	if got, err = store.SetTranslationCover(ctx, "cookie:Owner", d.ID, "fr-FR", DiscussionCover{}); err != nil || got == nil || got.Cover.Valid() {
		t.Fatalf("clear translation cover: got=%+v err=%v", got, err)
	}
	items, err = store.ListTranslations(ctx, d.ID)
	if err != nil || len(items) != 1 || items[0].Cover != nil {
		t.Fatalf("cleared cover should vanish from meta: items=%+v err=%v", items, err)
	}
}

func TestDiscussionDetailAndTranslationsEndpointsCarryLanguageCover(t *testing.T) {
	ctx := context.Background()
	store, d := newTranslationCoverStore(t)
	defaultCover := DiscussionCover{Type: "gradient", GradientStart: "#111111", GradientEnd: "#777777"}
	if _, err := store.SetCover(ctx, "cookie:Owner", d.ID, defaultCover); err != nil {
		t.Fatalf("set default cover: %v", err)
	}
	saveReadyTranslation(t, store, d.ID, "fr-FR", "Titre traduit")
	frCover := DiscussionCover{Type: "gradient", GradientStart: "#ff0000", GradientEnd: "#0000ff"}
	if got, err := store.SetTranslationCover(ctx, "cookie:Owner", d.ID, "fr-FR", frCover); err != nil || got == nil {
		t.Fatalf("set translation cover: got=%+v err=%v", got, err)
	}

	srv := New(Deps{Discussions: store})
	request := func(path, acceptLanguage string, out any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", usernameCookie+"=Owner")
		if acceptLanguage != "" {
			req.Header.Set("Accept-Language", acceptLanguage)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
	}

	var translated Discussion
	request("/api/discussions/"+d.ID, "fr-FR", &translated)
	if translated.Cover != frCover {
		t.Fatalf("translated detail cover = %+v, want %+v", translated.Cover, frCover)
	}
	if len(translated.Translations) != 1 || translated.Translations[0].Cover == nil || *translated.Translations[0].Cover != frCover {
		t.Fatalf("detail translation metas should carry cover: %+v", translated.Translations)
	}

	var fallback Discussion
	request("/api/discussions/"+d.ID, "ja-JP", &fallback)
	if fallback.Cover != defaultCover {
		t.Fatalf("fallback detail cover = %+v, want %+v", fallback.Cover, defaultCover)
	}

	var translations translationsResponse
	request("/api/discussions/"+d.ID+"/translations", "", &translations)
	if len(translations.Translations) != 1 || translations.Translations[0].Cover == nil || *translations.Translations[0].Cover != frCover {
		t.Fatalf("translations endpoint should carry cover: %+v", translations.Translations)
	}
}

func TestDiscussionCoverSetWithLanguage(t *testing.T) {
	ctx := context.Background()
	store, d := newTranslationCoverStore(t)
	saveReadyTranslation(t, store, d.ID, "fr-FR", "Titre traduit")
	srv := New(Deps{Discussions: store})

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/discussions/"+d.ID+"/cover", strings.NewReader(body))
		req.Header.Set("Cookie", usernameCookie+"=Owner")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	frCover := `{"type":"gradient","gradient_start":"#ff0000","gradient_end":"#0000ff"}`
	if rec := patch(`{"cover":` + frCover + `,"language":"ja-JP"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing translation should 404: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := patch(`{"cover":` + frCover + `,"language":"fr-FR"}`); rec.Code != http.StatusOK {
		t.Fatalf("set fr cover: status=%d body=%s", rec.Code, rec.Body.String())
	}
	translation, err := store.TranslationFor(ctx, d.ID, "fr-FR")
	want := DiscussionCover{Type: "gradient", GradientStart: "#ff0000", GradientEnd: "#0000ff"}
	if err != nil || translation == nil || translation.Cover != want {
		t.Fatalf("fr cover not persisted: translation=%+v err=%v", translation, err)
	}

	// The main language's cover is the default cover: a PATCH targeting it
	// writes the discussion cover, not a translation row.
	mainCover := `{"type":"gradient","gradient_start":"#222222","gradient_end":"#888888"}`
	if rec := patch(`{"cover":` + mainCover + `,"language":"en-US"}`); rec.Code != http.StatusOK {
		t.Fatalf("set main-language cover: status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := store.Get(ctx, "cookie:Owner", d.ID)
	if err != nil || updated == nil || updated.Cover.GradientStart != "#222222" {
		t.Fatalf("main-language cover should hit the default cover: %+v err=%v", updated, err)
	}
}

func TestTranslationCoverPromptPrefersTranslatedTitle(t *testing.T) {
	d := &Discussion{Title: "Source title"}
	translated := &DiscussionTranslation{Bundle: DiscussionTranslationBundle{Title: "Titre traduit"}}
	if got := translationCoverPrompt(translated, d); !strings.Contains(got, "Titre traduit") {
		t.Fatalf("prompt should carry translated title: %q", got)
	}
	empty := &DiscussionTranslation{}
	if got := translationCoverPrompt(empty, d); !strings.Contains(got, "Source title") {
		t.Fatalf("prompt should fall back to source title: %q", got)
	}
}
