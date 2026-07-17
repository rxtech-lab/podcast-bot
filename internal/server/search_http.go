package server

import (
	"net/http"
	"strings"
)

const (
	semanticSearchDefaultLimit = 30
	semanticSearchMaxLimit     = 100
	// semanticSearchMaxPerPodcast caps how many matches one podcast may
	// contribute to a grouped global result.
	semanticSearchMaxPerPodcast = 3
)

type semanticSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// SemanticMatch is one matched chunk surfaced to the client: the original
// chunk text, its similarity score, and enough anchoring metadata to jump to
// the moment (transcript) or the source document.
type SemanticMatch struct {
	Kind        string   `json:"kind"` // "transcript" | "source"
	Text        string   `json:"text"`
	Similarity  float64  `json:"similarity"`
	StartMS     int64    `json:"start_ms,omitempty"`
	EndMS       int64    `json:"end_ms,omitempty"`
	Speakers    []string `json:"speakers,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	SourceTitle string   `json:"source_title,omitempty"`
}

// SemanticSearchGroup is one podcast with its best-matching passages.
type SemanticSearchGroup struct {
	Discussion Discussion      `json:"discussion"`
	Matches    []SemanticMatch `json:"matches"`
}

type semanticSearchResponse struct {
	Enabled bool                  `json:"enabled"`
	Results []SemanticSearchGroup `json:"results"`
}

type discussionSearchResponse struct {
	Enabled bool            `json:"enabled"`
	Matches []SemanticMatch `json:"matches"`
}

func semanticMatchFromHit(h ChunkHit) SemanticMatch {
	return SemanticMatch{
		Kind:        h.Kind,
		Text:        h.Text,
		Similarity:  h.Similarity,
		StartMS:     h.Meta.StartMS,
		EndMS:       h.Meta.EndMS,
		Speakers:    h.Meta.Speakers,
		SourceURL:   h.Meta.SourceURL,
		SourceTitle: h.Meta.SourceTitle,
	}
}

// handleSemanticSearch serves POST /api/search/semantic: a global semantic
// search over the caller's indexed podcast content, grouped by podcast with
// the matched text + similarity per hit. Returns enabled=false (200) when
// embeddings are unconfigured so the client can show a graceful empty state.
func (s *Server) handleSemanticSearch(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req semanticSearchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	if !s.SemanticSearchEnabled(r.Context()) {
		writeJSON(w, semanticSearchResponse{Enabled: false, Results: []SemanticSearchGroup{}})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = semanticSearchDefaultLimit
	}
	if limit > semanticSearchMaxLimit {
		limit = semanticSearchMaxLimit
	}
	vec, err := s.embedQuery(r.Context(), query)
	if err != nil {
		http.Error(w, "search unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	model := s.resolvedEmbeddingModel(r.Context())
	hits, err := s.d.Embeddings.SearchGlobal(r.Context(), user.ID, vec, model, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Group hits by podcast, preserving best-score order: the first hit for a
	// podcast fixes that podcast's rank, later hits append up to the cap.
	order := make([]string, 0)
	matchesByID := map[string][]SemanticMatch{}
	for _, h := range hits {
		if len(matchesByID[h.DiscussionID]) == 0 {
			order = append(order, h.DiscussionID)
		} else if len(matchesByID[h.DiscussionID]) >= semanticSearchMaxPerPodcast {
			continue
		}
		matchesByID[h.DiscussionID] = append(matchesByID[h.DiscussionID], semanticMatchFromHit(h))
	}
	discussions, err := s.d.Discussions.ListByIDs(r.Context(), user.ID, order)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareDiscussionListRows(r, discussions, nil)
	results := make([]SemanticSearchGroup, 0, len(discussions))
	for _, d := range discussions {
		results = append(results, SemanticSearchGroup{
			Discussion: d,
			Matches:    matchesByID[d.ID],
		})
	}
	writeJSON(w, semanticSearchResponse{Enabled: true, Results: results})
}

// handleDiscussionSemanticSearch serves POST /api/discussions/{id}/search:
// semantic search scoped to one owned podcast's transcript + sources.
func (s *Server) handleDiscussionSemanticSearch(w http.ResponseWriter, r *http.Request) {
	d := s.getOwnedDiscussion(w, r)
	if d == nil {
		return
	}
	user := s.requestUser(r)
	var req semanticSearchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	if !s.SemanticSearchEnabled(r.Context()) {
		writeJSON(w, discussionSearchResponse{Enabled: false, Matches: []SemanticMatch{}})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = semanticSearchDefaultLimit
	}
	if limit > semanticSearchMaxLimit {
		limit = semanticSearchMaxLimit
	}
	vec, err := s.embedQuery(r.Context(), query)
	if err != nil {
		http.Error(w, "search unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	model := s.resolvedEmbeddingModel(r.Context())
	hits, err := s.d.Embeddings.SearchDiscussion(r.Context(), user.ID, d.ID, vec, model, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	matches := make([]SemanticMatch, 0, len(hits))
	for _, h := range hits {
		matches = append(matches, semanticMatchFromHit(h))
	}
	writeJSON(w, discussionSearchResponse{Enabled: true, Matches: matches})
}
