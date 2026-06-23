package server

import (
	"context"
	"errors"
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
	// GenerateCover, when true, kicks off background AI cover-art generation for
	// the new discussion. The placeholder is returned immediately; the cover is
	// filled in asynchronously and picked up the next time the discussion is
	// fetched (e.g. when the player opens). CoverPrompt overrides the default
	// prompt derived from the topic.
	GenerateCover bool   `json:"generate_cover"`
	CoverPrompt   string `json:"cover_prompt"`
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
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	visibility := DiscussionVisibility(strings.TrimSpace(r.URL.Query().Get("visibility")))
	if visibility != "" && visibility != DiscussionPrivate && visibility != DiscussionPublic {
		http.Error(w, "invalid visibility", http.StatusBadRequest)
		return
	}
	var items []Discussion
	var err error
	if query != "" && visibility != "" {
		items, err = s.d.Discussions.SearchByVisibility(r.Context(), user.ID, query, visibility, limit, offset)
	} else if query != "" {
		items, err = s.d.Discussions.Search(r.Context(), user.ID, query, limit, offset)
	} else if visibility != "" {
		items, err = s.d.Discussions.ListByVisibility(r.Context(), user.ID, visibility, limit, offset)
	} else {
		items, err = s.d.Discussions.List(r.Context(), user.ID, limit, offset)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range items {
		s.applyDiscussionJobStatus(r, &items[i])
		s.refreshDiscussionCoverURL(r.Context(), &items[i])
		s.sanitizeDiscussionUsage(&items[i])
	}
	s.applyDiscussionProgresses(r.Context(), items)
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
		s.refreshDiscussionCoverURL(r.Context(), d)
		s.sanitizeDiscussionUsage(d)
		writeJSON(w, d)
		return
	}
	d := s.getOwnedDiscussion(w, r)
	if d == nil {
		return
	}
	s.applyDiscussionJobStatus(r, d)
	s.applyDiscussionProgress(r.Context(), d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
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
	if req.GenerateCover {
		s.startBackgroundCoverGeneration(s.requestUser(r).ID, d.ID, strings.TrimSpace(req.CoverPrompt), topic)
	}
	writeJSON(w, d)
}

// startBackgroundCoverGeneration reserves points and spawns a goroutine that
// generates AI cover art for a discussion, persisting it when ready. It is
// fire-and-forget: the caller has already returned the discussion, so failures
// (including insufficient points or storage being disabled) are logged and the
// reservation refunded rather than surfaced to the client.
func (s *Server) startBackgroundCoverGeneration(userID, discID, prompt, topic string) {
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		s.logger().Warn("skipping background cover generation: storage disabled", "discussion", discID)
		return
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Square podcast cover artwork for " + strings.TrimSpace(topic)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), coverGenerationBackgroundTimeout)
		defer cancel()
		reserved, reserveLedgerID, ok := s.reserveImageGenerationBackground(ctx, userID, discID)
		if !ok {
			return
		}
		cover, err := s.generateStationCover(ctx, userID, discID, prompt)
		if err != nil {
			s.refundImageGeneration(ctx, userID, discID, reserved, reserveLedgerID)
			s.logger().Warn("background cover generation failed", "discussion", discID, "err", err)
			return
		}
		if _, err := s.d.Discussions.SetCover(ctx, userID, discID, cover); err != nil {
			s.refundImageGeneration(ctx, userID, discID, reserved, reserveLedgerID)
			s.logger().Warn("background cover persist failed", "discussion", discID, "err", err)
			return
		}
		s.settleImageGeneration(ctx, userID, discID, reserved, reserveLedgerID)
	}()
}

// handleDiscussionCoverSet persists a cover (gradient, uploaded image, or a
// previously generated AI image) on an owned discussion without changing its
// visibility, so any discussion can carry cover art.
func (s *Server) handleDiscussionCoverSet(w http.ResponseWriter, r *http.Request) {
	var req marketCoverSetRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	updated, err := s.d.Discussions.SetCover(r.Context(), s.requestUser(r).ID, r.PathValue("id"), req.Cover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, updated)
	s.applyDiscussionProgress(r.Context(), updated)
	s.refreshDiscussionCoverURL(r.Context(), updated)
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

func (s *Server) handleDiscussionCreateFromPlan(w http.ResponseWriter, r *http.Request) {
	d, err := s.d.Discussions.CreateFromVisiblePlan(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "plan id is required") || strings.Contains(err.Error(), "source plan is not available") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) handleDiscussionPlan(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Reserve before the chargeable planner call; refund if it fails.
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, "")
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), user.ID, req.Topic, resp)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Reconcile to actual usage against the now-created discussion so the points
	// are never orphaned from the podcast total.
	s.settlePlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID, meter)
	if total, err := s.pointsCharged(r.Context(), d.ID); err == nil {
		d.PointsCharged = total
	}
	s.sanitizeDiscussionUsage(d)
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
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	res, err := p.Improve(r.Context(), d.Script, instruction, pastUserMessages(d.EditTurns), req.Attachments)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, id, reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.settlePlanning(r.Context(), user.ID, id, reserved, reserveLedgerID, meter)
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
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

// handleDiscussionPlanStream is the streaming twin of handleDiscussionPlan: it
// drafts a brand-new plan while emitting coarse progress steps over SSE, then
// sends the persisted discussion in a final "done" event.
func (s *Server) handleDiscussionPlanStream(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Reserve before SSE starts so a 402 is delivered as an HTTP status.
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, "")
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	p.WithProgress(func(ev planner.ProgressEvent) { _ = sse.send("progress", ev) })
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), user.ID, req.Topic, resp)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	s.settlePlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID, meter)
	if total, err := s.pointsCharged(r.Context(), d.ID); err == nil {
		d.PointsCharged = total
	}
	s.sanitizeDiscussionUsage(d)
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
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
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
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	_ = s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Current plan", resp)
	s.settlePlanning(workCtx, user.ID, id, reserved, reserveLedgerID, meter)
	if total, err := s.pointsCharged(workCtx, id); err == nil {
		updated.PointsCharged = total
	}
	s.clearDiscussionProgress(workCtx, id)
	s.sanitizeDiscussionUsage(updated)
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
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
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
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	res, err := p.Improve(workCtx, d.Script, instruction, pastUserMessages(d.EditTurns), req.Attachments)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	// Plan work succeeded — reconcile the reservation to actual usage now, before
	// the persistence steps, so the charge is recorded even if a later write fails.
	s.settlePlanning(workCtx, user.ID, id, reserved, reserveLedgerID, meter)
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
	if total, err := s.pointsCharged(workCtx, id); err == nil {
		updated.PointsCharged = total
	}
	s.clearDiscussionProgress(workCtx, id)
	s.sanitizeDiscussionUsage(updated)
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
	// Reserve BEFORE launching the background re-research, since the handler
	// returns immediately and can't reject afterwards. The goroutine settles to
	// actual usage on success, or refunds on failure.
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	prev := *d.Script
	prev.Sources = append([]config.Source(nil), d.Sources...)
	urls = append([]string(nil), urls...)
	// Record the user's action up front so the chat history reflects it even if
	// the background re-research later fails.
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "user", addSourcesTurnText(urls))
	go s.updateDiscussionWithAddedSources(user.ID, id, prev, urls, p, meter, reserved, reserveLedgerID)
	s.sanitizeDiscussionUsage(d)
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
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
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
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	res, err := p.AddSources(workCtx, &prev, urls)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	s.settlePlanning(workCtx, user.ID, id, reserved, reserveLedgerID, meter)
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
	if total, err := s.pointsCharged(workCtx, id); err == nil {
		updated.PointsCharged = total
	}
	s.clearDiscussionProgress(workCtx, id)
	s.sanitizeDiscussionUsage(updated)
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

func (s *Server) applyDiscussionProgresses(ctx context.Context, items []Discussion) {
	if len(items) == 0 || s.d.Progress == nil {
		return
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	progress := s.d.Progress.GetMany(ctx, ids)
	for i := range items {
		items[i].Progress = progress[items[i].ID]
	}
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

func (s *Server) updateDiscussionWithAddedSources(owner, id string, prev config.DebateTopic, urls []string, p *planner.Planner, meter *usageAccumulator, reserved, reserveLedgerID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), addSourcesBackgroundTimeout)
	defer cancel()
	res, err := p.AddSources(ctx, &prev, urls)
	if err != nil {
		// Release the held reservation since no chargeable work landed.
		s.refundPlanning(ctx, owner, id, reserved, reserveLedgerID)
		s.logger().Warn("add sources background update failed", "discussion", id, "err", err)
		return
	}
	// Reconcile the reservation to actual usage now that the async run succeeded.
	s.settlePlanning(ctx, owner, id, reserved, reserveLedgerID, meter)
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
//
// This hits the paid Firecrawl search API, so it is metered like planning: a
// flat search fee is reserved before the call (402 when the balance is short)
// and charged on success / refunded on failure. Firecrawl cost isn't itemised,
// so the reserved fee is charged in full as the flat actual.
func (s *Server) handleDiscussionSearchSources(w http.ResponseWriter, r *http.Request) {
	d := s.getOwnedDiscussion(w, r)
	if d == nil {
		return
	}
	user := s.requestUser(r)
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
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, d.ID)
	if !ok {
		return
	}
	sources, err := p.SearchSources(r.Context(), query)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.settleFlatPlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID)
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
	// Atomically reserve enough points to cover a full podcast of this duration
	// BEFORE submitting the job, so a run never starts uncharged and two
	// concurrent requests can't overdraw. Reconciled to actual usage at job
	// completion; refunded here if the job fails to start.
	reserved, ok := s.reserveGeneration(w, r, user.ID, id, d.Script)
	if !ok {
		return
	}
	jobID, err := s.submitJSONScript(d.Script, req.VideoConfig, id)
	if err != nil {
		s.refundGeneration(r.Context(), user.ID, id, reserved)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetJob(r.Context(), user.ID, id, jobID)
	if err != nil {
		s.refundGeneration(r.Context(), user.ID, id, reserved)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

func (s *Server) handleDiscussionAppendLine(w http.ResponseWriter, r *http.Request) {
	var line DiscussionLine
	if !decodeJSONBody(w, r, &line) {
		return
	}
	if err := s.d.Discussions.AppendLineVisible(r.Context(), s.requestUser(r).ID, r.PathValue("id"), line); err != nil {
		writeDiscussionAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDiscussionAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDiscussionNotVisible):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errDiscussionForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
		if jobHasBillableUsage(j) {
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
		// Reconcile the generation reservation against actual usage. This is a
		// lazy fallback (the job-completion path also reconciles); both call the
		// idempotent SettleGeneration so the charge applies exactly once. Use the
		// discussion's persisted usage when a recovered/done job has lost its
		// usage fields.
		if s.pointsEnabled() {
			if detail, ok := generationUsageDetail(j, d); ok {
				if err := s.d.Points.ChargeGeneration(r.Context(), s.d.Env, d.ID, detail); err != nil {
					s.logger().Warn("generation settle failed", "discussion", d.ID, "err", err)
				}
				if total, err := s.d.Points.DiscussionPoints(r.Context(), d.ID); err == nil {
					d.PointsCharged = total
				}
			}
		}
	case j.Status == JobError:
		d.Status = DiscussionFailed
		_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
	}
}

func generationUsageDetail(j *Job, d *Discussion) (PointsUsageDetail, bool) {
	if jobHasBillableUsage(j) {
		return PointsUsageDetail{
			PromptTokens:     j.PromptTokens,
			CompletionTokens: j.CompletionTokens,
			TotalTokens:      j.TotalTokens,
			LLMCostUSD:       j.LLMCostUSD,
			LLMCostKnown:     j.LLMCostKnown,
			TTSCostUSD:       j.TTSCostUSD,
			MusicCostUSD:     j.MusicCostUSD,
			CostUSD:          j.LLMCostUSD + j.TTSCostUSD + j.MusicCostUSD,
		}, true
	}
	if !discussionHasBillableUsage(d) {
		return PointsUsageDetail{}, false
	}
	return PointsUsageDetail{
		PromptTokens:     d.PromptTokens,
		CompletionTokens: d.CompletionTokens,
		TotalTokens:      d.TotalTokens,
		LLMCostUSD:       d.LLMCostUSD,
		LLMCostKnown:     d.LLMCostKnown,
		TTSCostUSD:       d.TTSCostUSD,
		MusicCostUSD:     d.MusicCostUSD,
		CostUSD:          d.LLMCostUSD + d.TTSCostUSD + d.MusicCostUSD,
	}, true
}

func jobHasBillableUsage(j *Job) bool {
	return j != nil && (j.TotalTokens > 0 || j.LLMCostUSD > 0 || j.TTSCostUSD > 0 || j.MusicCostUSD > 0)
}

func discussionHasBillableUsage(d *Discussion) bool {
	return d != nil && (d.TotalTokens > 0 || d.LLMCostUSD > 0 || d.TTSCostUSD > 0 || d.MusicCostUSD > 0)
}
