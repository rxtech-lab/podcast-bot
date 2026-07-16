package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

type translationCreateRequest struct {
	TargetLanguage string `json:"target_language"`
}

type translationsResponse struct {
	MainLanguage string                      `json:"main_language"`
	Translations []DiscussionTranslationMeta `json:"translations"`
}

func (s *Server) handleDiscussionTranslations(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	items, err := s.d.Discussions.ListTranslations(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, translationsResponse{MainLanguage: d.Language, Translations: items})
}

func (s *Server) handleDiscussionTranslationCreate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	d, err := s.d.Discussions.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	if d.Status != DiscussionReady {
		http.Error(w, "podcast is not ready", http.StatusConflict)
		return
	}
	if !s.e2eMode() {
		ent, err := s.resolveEntitlements(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "could not resolve translation permission", http.StatusServiceUnavailable)
			return
		}
		if !ent.Features.CanTranslatePodcast {
			http.Error(w, "podcast translation is not allowed for this subscription", http.StatusForbidden)
			return
		}
	}
	var req translationCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid translation request", http.StatusBadRequest)
		return
	}
	meta, err := s.StartPodcastTranslation(r.Context(), d, req.TargetLanguage)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not configured") {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, meta)
}

func (s *Server) applyDiscussionTranslationPresentation(r *http.Request, d *Discussion) {
	if d == nil || s.d.Discussions == nil {
		return
	}
	d.MainLanguage = d.Language
	items, err := s.d.Discussions.ListTranslations(r.Context(), d.ID)
	if err == nil {
		d.Translations = items
	}
	target := discussionPresentationLanguage(r)
	if target == "" || strings.EqualFold(target, d.MainLanguage) {
		return
	}
	translation, err := s.d.Discussions.TranslationFor(r.Context(), d.ID, target)
	if err != nil || translation == nil || translation.Status != DiscussionTranslationReady {
		return
	}
	applyTranslationBundle(d, translation.Bundle)
	d.Translations = items
}

// discussionPresentationLanguage uses an explicit language selection when the
// client has made one. The initial detail request has no language query, so it
// follows the device preference already carried by Accept-Language.
func discussionPresentationLanguage(r *http.Request) string {
	if target := normalizeTranslationLanguage(r.URL.Query().Get("language")); target != "" {
		return target
	}
	return podcastLanguageFromAcceptLanguage(r.Header.Get("Accept-Language"))
}

// podcastLanguageFromAcceptLanguage maps the request's preferred presentation
// language to the canonical language codes used by podcast translations. Only
// the highest-priority language is considered: if that translation is not
// ready, list endpoints must fall back to the podcast's original title.
func podcastLanguageFromAcceptLanguage(header string) string {
	tags, _, err := language.ParseAcceptLanguage(strings.TrimSpace(header))
	if err != nil || len(tags) == 0 {
		return ""
	}
	tag := strings.ToLower(tags[0].String())
	switch {
	case strings.HasPrefix(tag, "en"):
		return "en-US"
	case strings.HasPrefix(tag, "zh"):
		if strings.Contains(tag, "hant") {
			return "zh-TW"
		}
		if strings.Contains(tag, "hans") {
			return "zh-CN"
		}
		if strings.Contains(tag, "-tw") ||
			strings.Contains(tag, "-hk") || strings.Contains(tag, "-mo") {
			return "zh-TW"
		}
		return "zh-CN"
	case strings.HasPrefix(tag, "ja"):
		return "ja-JP"
	case strings.HasPrefix(tag, "ko"):
		return "ko-KR"
	case strings.HasPrefix(tag, "es"):
		return "es-ES"
	case strings.HasPrefix(tag, "fr"):
		return "fr-FR"
	case strings.HasPrefix(tag, "de"):
		return "de-DE"
	default:
		return ""
	}
}

func (s *Server) applyDiscussionListTitleTranslations(r *http.Request, items []Discussion) {
	if len(items) == 0 || s.d.Discussions == nil {
		return
	}
	target := podcastLanguageFromAcceptLanguage(r.Header.Get("Accept-Language"))
	if target == "" {
		return
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		if !strings.EqualFold(target, items[i].Language) {
			ids = append(ids, items[i].ID)
		}
	}
	bundles, err := s.d.Discussions.ReadyTranslationBundles(r.Context(), ids, target)
	if err != nil {
		s.logger().Warn("discussion list translation lookup failed", "language", target, "err", err)
		return
	}
	for i := range items {
		bundle := bundles[items[i].ID]
		if title := strings.TrimSpace(bundle.Title); title != "" {
			items[i].Title = title
		}
	}
}

func (s *Server) translatedDocumentMarkdown(r *http.Request, discussionID, docType, original string) string {
	return s.translatedDocumentMarkdownFor(r.Context(), discussionID, docType,
		r.URL.Query().Get("language"), original)
}

func (s *Server) translatedDocumentMarkdownFor(ctx context.Context, discussionID, docType, language, original string) string {
	target := normalizeTranslationLanguage(language)
	if target == "" {
		return original
	}
	t, err := s.d.Discussions.TranslationFor(ctx, discussionID, target)
	if err == nil && t != nil && t.Status == DiscussionTranslationReady {
		switch normalizeDocType(docType) {
		case SummaryDocTypeSummary:
			if strings.TrimSpace(t.Bundle.SummaryMarkdown) != "" {
				return t.Bundle.SummaryMarkdown
			}
		case "text":
			if strings.TrimSpace(t.Bundle.TextMarkdown) != "" {
				return t.Bundle.TextMarkdown
			}
		}
	}
	return original
}

func (s *Server) translatedCaptions(ctx context.Context, jobID, language string) (string, bool) {
	target := normalizeTranslationLanguage(language)
	if target == "" || s.d.Discussions == nil {
		return "", false
	}
	t, err := s.d.Discussions.TranslationForJob(ctx, jobID, target)
	if err != nil || t == nil || t.Status != DiscussionTranslationReady || strings.TrimSpace(t.Bundle.CaptionsVTT) == "" {
		return "", false
	}
	return t.Bundle.CaptionsVTT, true
}

// translatedTranscriptLines returns the ready translation's transcript lines
// for a job when the requested presentation language has one; callers fall
// back to the original transcript otherwise.
func (s *Server) translatedTranscriptLines(ctx context.Context, jobID, language string) ([]DiscussionLine, bool) {
	target := normalizeTranslationLanguage(language)
	if target == "" || s.d.Discussions == nil {
		return nil, false
	}
	t, err := s.d.Discussions.TranslationForJob(ctx, jobID, target)
	if err != nil || t == nil || t.Status != DiscussionTranslationReady || len(t.Bundle.Lines) == 0 {
		return nil, false
	}
	return t.Bundle.Lines, true
}
