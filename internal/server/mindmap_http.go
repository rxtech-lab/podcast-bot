package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/summarizer"
)

// MindmapDocument is the full mindmap payload served by the content endpoint.
// Unlike SummaryDocument the body is a typed node tree, never JSON-in-a-string.
type MindmapDocument struct {
	DocType     string                  `json:"doc_type"`
	Status      SummaryStatus           `json:"status"`
	Mindmap     *summarizer.MindmapSpec `json:"mindmap,omitempty"`
	GeneratedAt *time.Time              `json:"generated_at,omitempty"`
}

func mindmapDocumentFrom(doc *SummaryDocument) (*MindmapDocument, error) {
	out := &MindmapDocument{
		DocType:     SummaryDocTypeMindmap,
		Status:      doc.Status,
		GeneratedAt: doc.GeneratedAt,
	}
	if doc.Status != SummaryReadyState {
		return out, nil
	}
	var spec summarizer.MindmapSpec
	if err := json.Unmarshal([]byte(doc.Markdown), &spec); err != nil {
		return nil, err
	}
	out.Mindmap = &spec
	return out, nil
}

// summaryMarkdownWithMindmapLink appends a "view the mindmap" link to the
// summary body when the discussion has a ready mindmap. The debatepod:// deep
// link is intercepted by the app's summary view to open the mindmap sheet;
// exports that need something richer (Notion) embed a rendered SVG instead.
// Injected on read like the "listen again" link, never stored; idempotent.
func (s *Server) summaryMarkdownWithMindmapLink(ctx context.Context, d *Discussion, markdown string) string {
	if !discussionSupportsMindmap(d) {
		return markdown
	}
	status, exists, err := s.d.Discussions.SummaryStatusFor(ctx, d.ID, SummaryDocTypeMindmap)
	if err != nil || !exists || status != SummaryReadyState {
		return markdown
	}
	link := fmt.Sprintf("🧠 [View the mindmap](%s)", discussionActionLink(d.ID, "sheet", "mindmap"))
	if strings.Contains(markdown, link) {
		return markdown
	}
	body := strings.TrimRight(markdown, "\n")
	if body == "" {
		return link + "\n"
	}
	return body + "\n\n" + link + "\n"
}

// handleDiscussionMindmap serves the mindmap node tree for a discussion the
// requester can see. Returns 404 when no mindmap exists yet.
func (s *Server) handleDiscussionMindmap(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	visible, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if visible == nil {
		http.NotFound(w, r)
		return
	}
	target := normalizeTranslationLanguage(r.URL.Query().Get("language"))
	if target != "" {
		translation, err := s.d.Discussions.TranslationFor(r.Context(), id, target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if translation != nil && translation.Status == DiscussionTranslationReady && translation.Bundle.Mindmap != nil {
			generatedAt := translation.GeneratedAt
			writeJSON(w, &MindmapDocument{
				DocType: SummaryDocTypeMindmap, Status: SummaryReadyState,
				Mindmap: translation.Bundle.Mindmap, GeneratedAt: &generatedAt,
			})
			return
		}
	}
	doc, err := s.d.Discussions.SummaryDocumentFor(r.Context(), id, SummaryDocTypeMindmap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if doc == nil {
		http.NotFound(w, r)
		return
	}
	out, err := mindmapDocumentFrom(doc)
	if err != nil {
		http.Error(w, "stored mindmap is corrupted", http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

// handleDiscussionMindmapGenerate lets the discussion owner manually start or
// retry mindmap generation after the podcast is ready. Only discussion-type
// podcasts have mindmaps. Returns the refreshed discussion detail so clients
// can immediately render the pending menu state.
func (s *Server) handleDiscussionMindmapGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	if d.Status != DiscussionReady {
		http.Error(w, "discussion is not ready", http.StatusConflict)
		return
	}
	if !discussionSupportsMindmap(d) {
		http.Error(w, "mindmap generation is only available for discussions", http.StatusConflict)
		return
	}
	input := SummaryGenerationInputFromDiscussion(d)
	if _, err := StartMindmapGeneration(r.Context(), SummaryGenerationDeps{
		Env:         s.d.Env,
		Bus:         s.d.Bus,
		Discussions: s.d.Discussions,
		Points:      s.d.Points,
		APNS:        s.apns,
		Log:         s.logger(),
		MQ:          s.d.MQ,
	}, input); err != nil {
		if errors.Is(err, ErrSummaryNoTranscript) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionSummaryMeta(r.Context(), updated)
	s.applyDiscussionMindmapMeta(r.Context(), updated)
	writeJSON(w, updated)
}

// handleDiscussionMindmapSave persists an owner's edited mindmap tree. The
// whole tree is replaced (last write wins); edits are rejected while a
// generation is in flight and validated against the user-edit limits.
func (s *Server) handleDiscussionMindmapSave(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	if !d.IsOwner {
		http.Error(w, "only the owner can edit the mindmap", http.StatusForbidden)
		return
	}
	status, exists, err := s.d.Discussions.SummaryStatusFor(r.Context(), id, SummaryDocTypeMindmap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	if status == SummaryGenerating {
		http.Error(w, "mindmap is being generated", http.StatusConflict)
		return
	}
	var body struct {
		Mindmap *summarizer.MindmapSpec `json:"mindmap"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.Mindmap != nil {
		body.Mindmap.Version = 1
	}
	if err := summarizer.ValidateMindmapSpec(body.Mindmap, true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, err := json.Marshal(body.Mindmap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.d.Discussions.UpdateSummaryMarkdown(r.Context(), id, SummaryDocTypeMindmap, string(data)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	PublishDiscussionResourceUpdated(s.d.Bus, s.d.Env, d.JobID, d.ID, "Mindmap updated", "mindmap")
	doc, err := s.d.Discussions.SummaryDocumentFor(r.Context(), id, SummaryDocTypeMindmap)
	if err != nil || doc == nil {
		http.Error(w, "failed to reload mindmap", http.StatusInternalServerError)
		return
	}
	out, err := mindmapDocumentFrom(doc)
	if err != nil {
		http.Error(w, "stored mindmap is corrupted", http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}
