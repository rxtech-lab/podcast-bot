package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestPodcastLanguageFromAcceptLanguage(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{header: "fr-CA,en;q=0.9", want: "fr-FR"},
		{header: "zh-Hant-HK,zh-Hans;q=0.9", want: "zh-TW"},
		{header: "zh-Hans-HK,zh-Hant;q=0.9", want: "zh-CN"},
		{header: "zh-SG", want: "zh-CN"},
		{header: "ja", want: "ja-JP"},
		{header: "ar-SA,en;q=0.9", want: ""},
		{header: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			if got := podcastLanguageFromAcceptLanguage(tt.header); got != tt.want {
				t.Fatalf("podcastLanguageFromAcceptLanguage(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestDiscussionAndMarketplaceListsTranslateTitleFromAcceptLanguage(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "list-translations.db"), "", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	d, err := store.Create(ctx, "cookie:Owner", "Source topic", planResponse{
		Script:   &config.DebateTopic{Title: "Source title", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "Source plan",
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/source.mp3"); err != nil {
		t.Fatalf("mark discussion ready: %v", err)
	}
	if _, err := store.SetVisibility(ctx, "cookie:Owner", d.ID, DiscussionPublic, DiscussionCover{
		Type: "gradient", GradientStart: "#111111", GradientEnd: "#777777",
	}); err != nil {
		t.Fatalf("publish discussion: %v", err)
	}
	if err := store.BeginTranslation(ctx, d.ID, "fr-FR", "test/model"); err != nil {
		t.Fatalf("begin translation: %v", err)
	}
	if err := store.SaveTranslation(ctx, d.ID, "fr-FR", DiscussionTranslationBundle{
		Language: "fr-FR", Title: "Titre traduit", Markdown: "Plan traduit",
		Script: &config.DebateTopic{Title: "Titre lourd"},
	}, "test/model", SummaryUsage{}); err != nil {
		t.Fatalf("save translation: %v", err)
	}
	bundles, err := store.ReadyTranslationBundles(ctx, []string{d.ID, d.ID, "missing"}, "fr-FR")
	if err != nil || len(bundles) != 1 || bundles[d.ID].Title != "Titre traduit" {
		t.Fatalf("ready translation bundles = %+v, err=%v", bundles, err)
	}

	srv := New(Deps{Discussions: store})
	requestList := func(path, cookie, acceptLanguage string) []Discussion {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", usernameCookie+"="+cookie)
		if acceptLanguage != "" {
			req.Header.Set("Accept-Language", acceptLanguage)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var rows []Discussion
		if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
		return rows
	}

	owned := requestList("/api/discussions", "Owner", "fr-CA,en;q=0.9")
	if len(owned) != 1 || owned[0].Title != "Titre traduit" {
		t.Fatalf("translated discussion list = %+v", owned)
	}
	if owned[0].Script != nil || owned[0].Markdown != "" {
		t.Fatalf("translated discussion list leaked heavy bundle fields: %+v", owned[0])
	}

	market := requestList("/api/market/stations", "Viewer", "fr-FR")
	if len(market) != 1 || market[0].Title != "Titre traduit" {
		t.Fatalf("translated marketplace list = %+v", market)
	}

	fallback := requestList("/api/market/stations", "Viewer", "ja-JP,fr-FR;q=0.9")
	if len(fallback) != 1 || fallback[0].Title != "Source title" {
		t.Fatalf("marketplace fallback list = %+v", fallback)
	}
	original := requestList("/api/market/stations", "Viewer", "")
	if len(original) != 1 || original[0].Title != "Source title" {
		t.Fatalf("marketplace original list = %+v", original)
	}
}
