package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/planner"
)

// atoiDefault parses s as an int, returning def when s is empty or invalid.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// discussionCreateRequest creates an empty placeholder discussion so the client
// gets an id up front, then streams the plan into it via
// /api/discussions/{id}/plan/stream.
type discussionCreateRequest struct {
	Topic    string `json:"topic"`
	Language string `json:"language"`
}

type discussionImproveRequest struct {
	Instruction string               `json:"instruction"`
	Attachments []planner.Attachment `json:"attachments,omitempty"`
}

// discussionAddSourcesRequest carries links the user added in the sources sheet
// so the planner can re-research them and update the plan.
type discussionAddSourcesRequest struct {
	URLs []string `json:"urls"`
}

type discussionSourceSearchRequest struct {
	Query string `json:"query"`
}

type discussionSourceSearchResponse struct {
	Sources []config.Source `json:"sources"`
}

const (
	addSourcesBackgroundTimeout     = 5 * time.Minute
	discussionStreamRecoveryTimeout = 10 * time.Minute
)

type discussionGenerateRequest struct {
	VideoConfig videoConfigJSON `json:"videoConfig"`
	Language    string          `json:"language"`
}

func (s *Server) handleDiscussionList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	limit := atoiDefault(r.URL.Query().Get("limit"), 0)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	items, err := s.d.Discussions.List(r.Context(), user.ID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range items {
		s.applyDiscussionJobStatus(r, &items[i])
		s.applyDiscussionProgress(r.Context(), &items[i])
	}
	writeJSON(w, items)
}

func (s *Server) handleDiscussionGet(w http.ResponseWriter, r *http.Request) {
	editLimit := atoiDefault(r.URL.Query().Get("edit_limit"), 0)
	editBefore, _ := strconv.ParseInt(r.URL.Query().Get("edit_before"), 10, 64)
	if editLimit > 0 {
		user := s.requestUser(r)
		d, err := s.d.Discussions.GetWithEditTurnPage(r.Context(), user.ID, r.PathValue("id"), editLimit, editBefore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if d == nil {
			http.NotFound(w, r)
			return
		}
		s.applyDiscussionJobStatus(r, d)
		s.applyDiscussionProgress(r.Context(), d)
		writeJSON(w, d)
		return
	}
	d := s.getOwnedDiscussion(w, r)
	if d == nil {
		return
	}
	s.applyDiscussionJobStatus(r, d)
	s.applyDiscussionProgress(r.Context(), d)
	writeJSON(w, d)
}

// handleDiscussionCreate inserts an empty placeholder discussion (status
// "planning") and returns it immediately so the client can navigate to the plan
// page and stream the plan into it via /api/discussions/{id}/plan/stream. This
// decouples discussion creation from the multi-minute planning run: even if the
// stream drops, the discussion is already saved and recoverable in the library.
func (s *Server) handleDiscussionCreate(w http.ResponseWriter, r *http.Request) {
	var req discussionCreateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		http.Error(w, "topic is required", http.StatusBadRequest)
		return
	}
	d, err := s.d.Discussions.CreatePlaceholder(r.Context(), s.requestUser(r).ID, topic, strings.TrimSpace(req.Language))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (s *Server) handleDiscussionPlan(w http.ResponseWriter, r *http.Request) {
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), s.requestUser(r).ID, req.Topic, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (s *Server) handleDiscussionImprove(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionImproveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		http.Error(w, "instruction is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	res, err := p.Improve(r.Context(), d.Script, instruction, pastUserMessages(d.EditTurns), req.Attachments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "user", instruction)
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	// Append the plan snapshot before UpdatePlan reloads, so the returned
	// discussion already carries the new plan card in its edit-turn history.
	if err := s.d.Discussions.AppendPlanTurn(r.Context(), user.ID, id, "Updated plan", resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, id, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, updated)
}

// handleDiscussionPlanStream is the streaming twin of handleDiscussionPlan: it
// drafts a brand-new plan while emitting coarse progress steps over SSE, then
// sends the persisted discussion in a final "done" event.
func (s *Server) handleDiscussionPlanStream(w http.ResponseWriter, r *http.Request) {
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	p.WithProgress(func(ev planner.ProgressEvent) { _ = sse.send("progress", ev) })
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), s.requestUser(r).ID, req.Topic, resp)
	if err != nil {
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	_ = sse.send("done", d)
}

// handleDiscussionPlanStreamForID drafts the plan for an already-created
// placeholder discussion, emitting progress over SSE and persisting the plan
// into the existing row before sending the final "done" event. This is the
// streaming half of the create-then-plan flow: the client first POSTs
// /api/discussions to get an id, then streams the plan into it here.
func (s *Server) handleDiscussionPlanStreamForID(w http.ResponseWriter, r *http.Request) {
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
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		req.Topic = d.Topic
	}
	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	s.recordDiscussionProgress(workCtx, id, "plan", planner.ProgressEvent{Phase: "thinking", Text: "Researching & planning..."})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, id, "plan", ev)
		_ = sse.send("progress", ev)
	})
	res, err := p.Generate(workCtx, req)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	_ = s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Current plan", resp)
	s.clearDiscussionProgress(workCtx, id)
	_ = sse.send("done", updated)
}

// handleDiscussionImproveStream is the streaming twin of handleDiscussionImprove:
// it revises the plan while emitting progress steps over SSE, then sends the
// updated discussion in a final "done" event.
func (s *Server) handleDiscussionImproveStream(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionImproveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		http.Error(w, "instruction is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	s.recordDiscussionProgress(workCtx, id, "improve", planner.ProgressEvent{Phase: "thinking", Text: "Updating plan..."})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, id, "improve", ev)
		_ = sse.send("progress", ev)
	})
	if err := s.d.Discussions.AppendEditTurn(workCtx, user.ID, id, "user", instruction); err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	res, err := p.Improve(workCtx, d.Script, instruction, pastUserMessages(d.EditTurns), req.Attachments)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	// Append the plan snapshot before UpdatePlan reloads, so the "done" payload
	// already carries the new plan card in its edit-turn history.
	if err := s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Updated plan", resp); err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	s.clearDiscussionProgress(workCtx, id)
	_ = sse.send("done", updated)
}

// handleDiscussionAddSources scrapes the user-added links, merges them into the
// plan's sources, and re-runs the planner so the background reflects the new
// references — the "add a link, save, re-research" flow.
func (s *Server) handleDiscussionAddSources(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionAddSourcesRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	urls := cleanedSourceURLs(req.URLs)
	if len(urls) == 0 {
		http.Error(w, "at least one url is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	prev := *d.Script
	prev.Sources = append([]config.Source(nil), d.Sources...)
	urls = append([]string(nil), urls...)
	// Record the user's action up front so the chat history reflects it even if
	// the background re-research later fails.
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "user", addSourcesTurnText(urls))
	go s.updateDiscussionWithAddedSources(user.ID, id, prev, urls, p)
	writeJSON(w, d)
}

// handleDiscussionAddSourcesStream is the streaming source-update path used by
// the native client. It mirrors the edit stream contract: progress events while
// links are read and the plan is rewritten, then a terminal done/error event so
// the UI never waits on blind polling.
func (s *Server) handleDiscussionAddSourcesStream(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionAddSourcesRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	urls := cleanedSourceURLs(req.URLs)
	if len(urls) == 0 {
		http.Error(w, "at least one url is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	prev := *d.Script
	prev.Sources = append([]config.Source(nil), d.Sources...)

	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	s.recordDiscussionProgress(workCtx, id, "sources", planner.ProgressEvent{Phase: "read", Text: "Reading added sources..."})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, id, "sources", ev)
		_ = sse.send("progress", ev)
	})

	if err := s.d.Discussions.AppendEditTurn(workCtx, user.ID, id, "user", addSourcesTurnText(urls)); err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	res, err := p.AddSources(workCtx, &prev, urls)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	if err := s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Updated plan with added sources", resp); err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	s.clearDiscussionProgress(workCtx, id)
	_ = sse.send("done", updated)
}

// pastUserMessages pulls the text of prior "user" edit turns (oldest first) so
// the planner can revise a plan with the full editing conversation in view, not
// just the latest instruction. Plan-snapshot turns are skipped.
func pastUserMessages(turns []DiscussionEditTurn) []string {
	var out []string
	for _, t := range turns {
		if t.Role != "user" {
			continue
		}
		if text := strings.TrimSpace(t.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func (s *Server) applyDiscussionProgress(ctx context.Context, d *Discussion) {
	if d == nil || s.d.Progress == nil {
		return
	}
	d.Progress = s.d.Progress.Get(ctx, d.ID)
}

func (s *Server) recordDiscussionProgress(ctx context.Context, id, operation string, ev planner.ProgressEvent) {
	if s.d.Progress == nil {
		return
	}
	s.d.Progress.Set(ctx, id, DiscussionProgress{
		Active:    true,
		Operation: operation,
		Phase:     ev.Phase,
		Text:      ev.Text,
	})
}

func (s *Server) clearDiscussionProgress(ctx context.Context, id string) {
	if s.d.Progress != nil {
		s.d.Progress.Clear(ctx, id)
	}
}

func cleanedSourceURLs(raw []string) []string {
	urls := make([]string, 0, len(raw))
	for _, u := range raw {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// addSourcesTurnText renders the user-visible chat bubble for an add-sources
// action: a short header plus the links the user added.
func addSourcesTurnText(urls []string) string {
	var sb strings.Builder
	sb.WriteString("Added ")
	sb.WriteString(strconv.Itoa(len(urls)))
	sb.WriteString(" source")
	if len(urls) != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(":")
	for _, u := range urls {
		sb.WriteString("\n")
		sb.WriteString(u)
	}
	return sb.String()
}

func (s *Server) updateDiscussionWithAddedSources(owner, id string, prev config.DebateTopic, urls []string, p *planner.Planner) {
	ctx, cancel := context.WithTimeout(context.Background(), addSourcesBackgroundTimeout)
	defer cancel()
	res, err := p.AddSources(ctx, &prev, urls)
	if err != nil {
		s.logger().Warn("add sources background update failed", "discussion", id, "err", err)
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	updated, err := s.d.Discussions.UpdatePlan(ctx, owner, id, resp)
	if err != nil {
		s.logger().Warn("add sources plan update failed", "discussion", id, "err", err)
		return
	}
	if updated == nil {
		s.logger().Warn("add sources plan update target disappeared", "discussion", id)
		return
	}
	if err := s.d.Discussions.AppendPlanTurn(ctx, owner, id, "Updated plan with added sources", resp); err != nil {
		s.logger().Warn("add sources edit turn append failed", "discussion", id, "err", err)
	}
}

// handleDiscussionSearchSources searches Firecrawl for candidate web sources
// without mutating the discussion. The native client adds chosen results to
// its local link list, where the user can swipe-delete before saving.
func (s *Server) handleDiscussionSearchSources(w http.ResponseWriter, r *http.Request) {
	if d := s.getOwnedDiscussion(w, r); d == nil {
		return
	}
	var req discussionSourceSearchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	sources, err := p.SearchSources(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, discussionSourceSearchResponse{Sources: sources})
}

func (s *Server) handleDiscussionGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionGenerateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if lang := strings.TrimSpace(req.Language); lang != "" {
		next := *d.Script
		next.Language = lang
		md, err := next.RenderMarkdown()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sources := d.Sources
		if len(sources) == 0 {
			sources = next.Sources
		}
		updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, id, planResponse{
			Script:     &next,
			Markdown:   md,
			Sources:    sources,
			Researched: d.Researched,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if updated == nil || updated.Script == nil {
			http.NotFound(w, r)
			return
		}
		d = updated
	}
	jobID, err := s.submitJSONScript(d.Script, req.VideoConfig, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetJob(r.Context(), user.ID, id, jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, updated)
}

func (s *Server) handleDiscussionAppendLine(w http.ResponseWriter, r *http.Request) {
	var line DiscussionLine
	if !decodeJSONBody(w, r, &line) {
		return
	}
	if err := s.d.Discussions.AppendLine(r.Context(), s.requestUser(r).ID, r.PathValue("id"), line); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDiscussionDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.d.Discussions.Delete(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getOwnedDiscussion(w http.ResponseWriter, r *http.Request) *Discussion {
	d, err := s.d.Discussions.Get(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	if d == nil {
		http.NotFound(w, r)
		return nil
	}
	return d
}

func (s *Server) applyDiscussionJobStatus(r *http.Request, d *Discussion) {
	if d == nil || d.JobID == "" || s.d.Jobs == nil {
		return
	}
	j := s.d.Jobs.Get(d.JobID)
	if j == nil {
		j = s.recoverJob(d.JobID)
		if j == nil {
			if d.Status == DiscussionGenerating {
				d.Status = DiscussionFailed
				_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
			}
			return
		}
	}
	switch {
	case j.Status == JobDone:
		d.Status = DiscussionReady
		if url := s.jobDownloadURL(r.Context(), j); url != "" {
			d.DownloadURL = url
		} else if d.DownloadURL == "" && j.DownloadURL != "" {
			d.DownloadURL = j.DownloadURL
		}
		_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionReady, d.DownloadURL)
		if j.TotalTokens > 0 {
			d.PromptTokens = j.PromptTokens
			d.CompletionTokens = j.CompletionTokens
			d.TotalTokens = j.TotalTokens
			d.LLMCostUSD = j.LLMCostUSD
			d.LLMCostKnown = j.LLMCostKnown
			d.TTSCostUSD = j.TTSCostUSD
			d.MusicCostUSD = j.MusicCostUSD
			_ = s.d.Discussions.SetUsage(r.Context(), d.ID,
				j.PromptTokens, j.CompletionTokens, j.TotalTokens, j.LLMCostUSD, j.LLMCostKnown,
				j.TTSCostUSD, j.MusicCostUSD)
		}
	case j.Status == JobError:
		d.Status = DiscussionFailed
		_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
	}
}
