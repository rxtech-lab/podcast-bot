package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/planner"
)

// planningStreamRequest starts or continues a conversational planning turn with
// a free-text user message (plus optional document attachments).
type planningStreamRequest struct {
	Prompt      string               `json:"prompt"`
	Language    string               `json:"language,omitempty"`
	Attachments []planner.Attachment `json:"attachments,omitempty"`
	Resume      bool                 `json:"resume,omitempty"`
}

// planningAnswerRequest answers (or skips) a pending question, resuming the loop.
type planningAnswerRequest struct {
	QuestionID string          `json:"question_id"`
	Action     string          `json:"action"` // "answered" | "rejected"
	Language   string          `json:"language,omitempty"`
	Answers    json.RawMessage `json:"answers,omitempty"`
}

// planningDonePayload is the terminal SSE "done" frame: the refreshed discussion
// (so existing recovery/polling keeps working) plus the rebuilt conversation.
type planningDonePayload struct {
	Discussion   *Discussion              `json:"discussion"`
	Conversation PlanningConversationView `json:"conversation"`
}

type planningEventWriter interface {
	send(event string, payload any) error
}

type planningStreamSink struct {
	ctx            context.Context
	store          *PlanningStreamStore
	runID          string
	conversationID string
}

const planningLoadActiveCheckTimeout = 300 * time.Millisecond

func e2ePlanningInsufficientBalancePrompt(prompt string) bool {
	return strings.Contains(strings.ToLower(prompt), "e2e insufficient balance")
}

func (s planningStreamSink) send(event string, payload any) error {
	_, err := s.store.Append(s.ctx, s.conversationID, s.runID, event, payload)
	return err
}

// handlePlanningConversationGet returns the persisted conversation for history
// rebuild on app launch. Returns an empty view when no conversation exists yet.
func (s *Server) handlePlanningConversationGet(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	user := s.requestUser(r)
	id := r.PathValue("id")
	phase := time.Now()
	exists, conv, turns, err := s.d.Planning.ConversationWithTurnsByDiscussion(r.Context(), user.ID, id)
	dbJoin := time.Since(phase)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	view := PlanningConversationView{Parts: []PlanningPart{}}
	var (
		partsBuild     time.Duration
		activeCheck    time.Duration
		turnCount      int
		activeTimedOut bool
	)
	if conv != nil {
		view.Conversation = conv
		turnCount = len(turns)
		phase = time.Now()
		view.Parts = planningConversationParts(turns)
		partsBuild = time.Since(phase)
		view.NeedsRun = planningConversationShouldAutoRun(conv, turns)
		phase = time.Now()
		var active *PlanningActiveStream
		var ok bool
		active, ok, activeTimedOut = s.planningActiveStreamForLoad(r.Context(), conv.ID)
		if ok {
			view.IsRunning = true
			view.ActiveStream = active.RunID
		}
		activeCheck = time.Since(phase)
	}
	writeStart := time.Now()
	writeJSON(w, view)
	writeDuration := time.Since(writeStart)
	conversationID := ""
	status := ""
	if conv != nil {
		conversationID = conv.ID
		status = string(conv.Status)
	}
	s.logger().Info("planning conversation load timing",
		"discussion", id,
		"conversation", conversationID,
		"status", status,
		"turns", turnCount,
		"parts", len(view.Parts),
		"needs_run", view.NeedsRun,
		"is_running", view.IsRunning,
		"db_join_ms", durMS(dbJoin),
		"parts_build_ms", durMS(partsBuild),
		"active_check_ms", durMS(activeCheck),
		"active_check_timed_out", activeTimedOut,
		"write_json_ms", durMS(writeDuration),
		"total_ms", durMS(time.Since(started)),
	)
}

func (s *Server) planningActiveStreamForLoad(ctx context.Context, conversationID string) (*PlanningActiveStream, bool, bool) {
	activeCtx, cancel := context.WithTimeout(ctx, planningLoadActiveCheckTimeout)
	defer cancel()
	active, ok := s.d.PlanningStreams.Active(activeCtx, conversationID)
	return active, ok, errors.Is(activeCtx.Err(), context.DeadlineExceeded)
}

func (s *Server) handlePlanningStreamResume(w http.ResponseWriter, r *http.Request) {
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
	conv, err := s.d.Planning.ConversationByDiscussion(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if conv == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.streamPlanningActiveRun(w, r, active.RunID)
}

// handlePlanningStream starts/continues the conversation with a user message and
// streams the agent's turn over SSE.
func (s *Server) handlePlanningStream(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
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
	p, err := planner.New(s.plannerEnv())
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planningStreamRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	s.logger().Info("planning stream request",
		"discussion", id,
		"resume", req.Resume,
		"prompt_chars", len(req.Prompt),
		"attachments", len(req.Attachments),
	)
	if s.e2eMode() && e2ePlanningInsufficientBalancePrompt(req.Prompt) {
		writeInsufficientPoints(w, 50, 0)
		return
	}
	conv, err := s.d.Planning.EnsureConversation(r.Context(), user.ID, id)
	if err != nil || conv == nil {
		http.Error(w, "could not start planning conversation", http.StatusInternalServerError)
		return
	}
	if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
		s.streamPlanningActiveRun(w, r, active.RunID)
		return
	}

	if req.Resume {
		turns, err := s.d.Planning.Turns(r.Context(), conv.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		needsRun := planningConversationShouldAutoRun(conv, turns)
		s.logger().Info("planning stream resume state",
			"discussion", id,
			"conversation", conv.ID,
			"status", conv.Status,
			"turns", len(turns),
			"last_role", planningLastTurnRole(turns),
			"needs_run", needsRun,
		)
		if !needsRun {
			sse := newSSEWriter(w)
			_ = sse.comment("ok")
			parts := planningConversationParts(turns)
			_ = sse.send("done", planningDonePayload{
				Discussion: d,
				Conversation: PlanningConversationView{
					Conversation: conv,
					Parts:        parts,
					NeedsRun:     false,
				},
			})
			s.logger().Info("planning stream resume short-circuited",
				"discussion", id,
				"conversation", conv.ID,
				"parts", len(parts),
				"elapsed_ms", time.Since(started).Milliseconds(),
			)
			return
		}
	} else {
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			prompt = strings.TrimSpace(d.Topic)
		}
		if prompt == "" {
			http.Error(w, "prompt is required", http.StatusBadRequest)
			return
		}
		req.Attachments = s.sanitizedAttachments(user.ID, req.Attachments)
		userText := planner.ConversationMessageText(prompt, req.Attachments, req.Language)
		if err := s.d.Planning.AppendTurn(r.Context(), conv.ID, planningTurnInput{
			Role:        "user",
			Text:        userText,
			Attachments: req.Attachments,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if s.d.PlanningStreams.Enabled() {
		if !s.claimPlanningRun(conv.ID) {
			if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
				s.streamPlanningActiveRun(w, r, active.RunID)
				return
			}
			http.Error(w, "a planning turn is already in progress", http.StatusConflict)
			return
		}
		active, ok := s.startStoredPlanningRun(w, r, user.ID, d, conv, p, req.Language)
		if !ok {
			s.releasePlanningRun(conv.ID)
			return
		}
		s.streamPlanningActiveRun(w, r, active.RunID)
		return
	}
	if !s.claimPlanningRun(conv.ID) {
		http.Error(w, "a planning turn is already in progress", http.StatusConflict)
		return
	}
	defer s.releasePlanningRun(conv.ID)
	s.runPlanningTurn(w, r, user.ID, d, conv, p, req.Language)
}

// handlePlanningAnswer records the user's answer to a pending question and
// resumes the agent loop over SSE. Idempotent on an already-answered question.
func (s *Server) handlePlanningAnswer(w http.ResponseWriter, r *http.Request) {
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
	var req planningAnswerRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	conv, err := s.d.Planning.ConversationByDiscussion(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if conv == nil {
		http.NotFound(w, r)
		return
	}
	if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
		s.streamPlanningActiveRun(w, r, active.RunID)
		return
	}
	p, err := planner.New(s.plannerEnv())
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	pending, err := s.d.Planning.PendingQuestion(r.Context(), conv.ID, strings.TrimSpace(req.QuestionID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pending == nil {
		// Already answered (or unknown) — idempotent no-op: return current state.
		turns, _ := s.d.Planning.Turns(r.Context(), conv.ID)
		writeJSON(w, planningDonePayload{Discussion: d, Conversation: PlanningConversationView{Conversation: conv, Parts: planningConversationParts(turns), NeedsRun: planningConversationShouldAutoRun(conv, turns)}})
		return
	}
	status := "answered"
	if strings.EqualFold(strings.TrimSpace(req.Action), "rejected") {
		status = "rejected"
	}
	answersJSON := "[]"
	if len(req.Answers) > 0 && json.Valid(req.Answers) {
		answersJSON = string(req.Answers)
	}
	if err := s.d.Planning.RecordAnswer(r.Context(), conv.ID, pending.QuestionID, answersJSON, status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Synthetic tool result turn closing the ask_question call so the rebuilt
	// history is a valid assistant(tool_calls)→tool pairing the model can resume.
	digest := planningAnswerDigest(status, answersJSON, req.Language)
	if err := s.d.Planning.AppendTurn(r.Context(), conv.ID, planningTurnInput{
		Role:       "tool",
		ToolCallID: pending.ToolCallID,
		ToolName:   "ask_question",
		ResultText: digest,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.d.PlanningStreams.Enabled() {
		if !s.claimPlanningRun(conv.ID) {
			if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
				s.streamPlanningActiveRun(w, r, active.RunID)
				return
			}
			http.Error(w, "a planning turn is already in progress", http.StatusConflict)
			return
		}
		active, ok := s.startStoredPlanningRun(w, r, user.ID, d, conv, p, req.Language)
		if !ok {
			s.releasePlanningRun(conv.ID)
			return
		}
		s.streamPlanningActiveRun(w, r, active.RunID)
		return
	}
	if !s.claimPlanningRun(conv.ID) {
		http.Error(w, "a planning turn is already in progress", http.StatusConflict)
		return
	}
	defer s.releasePlanningRun(conv.ID)
	s.runPlanningTurn(w, r, user.ID, d, conv, p, req.Language)
}

// runPlanningTurn reserves points, rebuilds the LLM history from persisted turns,
// runs one conversational turn streaming SSE, persists every turn as it happens,
// settles billing, and sends the terminal done/error frame. Shared by the
// stream (new message) and answer (resume) endpoints.
func (s *Server) runPlanningTurn(w http.ResponseWriter, r *http.Request, userID string, d *Discussion, conv *PlanningConversation, p *planner.Planner, languageOverride string) {
	started := time.Now()
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, userID, d.ID)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()

	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	s.recordDiscussionProgress(workCtx, d.ID, "planning", planner.ProgressEvent{Phase: "thinking", Text: "Thinking…"})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, d.ID, "planning", ev)
		_ = sse.send("progress", ev)
	})

	turns, err := s.d.Planning.Turns(workCtx, conv.ID)
	if err != nil {
		s.refundPlanning(workCtx, userID, d.ID, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, d.ID)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	history := planningMessagesForLLM(turns, s.uploadURLRefresher(workCtx))
	s.logger().Info("planning turn started",
		"discussion", d.ID,
		"conversation", conv.ID,
		"turns", len(turns),
		"history", len(history),
		"last_role", planningLastTurnRole(turns),
	)
	opts := planner.ConversationOptions{
		Type:             planningContentType(d, turns),
		Language:         planningLanguage(d, languageOverride),
		Channel:          planningChannel(d),
		Discussants:      planningDiscussants(d),
		Template:         planningTemplate(d, planningContentType(d, turns)),
		AgentModel:       planningAgentModel(d),
		ExistingSources:  d.Sources,
		ExistingPlan:     d.Script,
		ExistingMarkdown: d.Markdown,
	}
	emit := func(ev planner.ConvEvent) { s.handlePlanningConvEvent(workCtx, sse, userID, d.ID, conv.ID, ev) }
	paused, runErr := p.RunConversationTurn(workCtx, history, opts, emit)

	if runErr != nil {
		s.refundPlanning(workCtx, userID, d.ID, reserved, reserveLedgerID)
		_ = s.d.Planning.SetStatus(workCtx, conv.ID, PlanningConversationFailed)
		s.clearDiscussionProgress(workCtx, d.ID)
		_ = sse.send("error", map[string]string{"message": runErr.Error()})
		return
	}
	s.settlePlanningConversation(workCtx, userID, d.ID, conv, reserved, reserveLedgerID, meter)
	if paused {
		_ = s.d.Planning.SetStatus(workCtx, conv.ID, PlanningConversationAwaitingAnswer)
	} else {
		_ = s.d.Planning.SetStatus(workCtx, conv.ID, PlanningConversationActive)
	}
	s.clearDiscussionProgress(workCtx, d.ID)

	updated, _ := s.d.Discussions.Get(workCtx, userID, d.ID)
	if updated != nil {
		if total, err := s.pointsCharged(workCtx, d.ID); err == nil {
			updated.PointsCharged = total
		}
		s.sanitizeDiscussionUsage(updated)
	}
	convFresh, _ := s.d.Planning.ConversationByDiscussion(workCtx, userID, d.ID)
	finalTurns, _ := s.d.Planning.Turns(workCtx, conv.ID)
	_ = sse.send("done", planningDonePayload{
		Discussion:   updated,
		Conversation: PlanningConversationView{Conversation: convFresh, Parts: planningConversationParts(finalTurns), NeedsRun: planningConversationShouldAutoRun(convFresh, finalTurns)},
	})
	s.logger().Info("planning turn finished",
		"discussion", d.ID,
		"conversation", conv.ID,
		"paused", paused,
		"turns", len(finalTurns),
		"elapsed_ms", time.Since(started).Milliseconds(),
	)
}

// PlanningTurnPayload is the wire payload of a queued planning turn. Every
// SSE frame flows through the Redis planning stream store, so the consuming
// pod and the HTTP pod tailing the stream may differ. The reservation is
// carried so a terminal failure can refund it; the Redis Active record
// (keyed by RunID) is the distributed claim.
type PlanningTurnPayload struct {
	RunID           string `json:"run_id"`
	ConversationID  string `json:"conversation_id"`
	DiscussionID    string `json:"discussion_id"`
	UserID          string `json:"user_id"`
	Language        string `json:"language,omitempty"`
	Reserved        int64  `json:"reserved"`
	ReserveLedgerID int64  `json:"reserve_ledger_id"`
}

func (s *Server) startStoredPlanningRun(w http.ResponseWriter, r *http.Request, userID string, d *Discussion, conv *PlanningConversation, p *planner.Planner, languageOverride string) (*PlanningActiveStream, bool) {
	// A run published for this conversation may already be pending/running
	// (the in-process claim frees as soon as the task is enqueued); resume
	// its stream rather than double-starting.
	if existing, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok && existing != nil {
		return existing, true
	}
	if s.d.MQ == nil {
		http.Error(w, "planning queue is not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, userID, d.ID)
	if !ok {
		return nil, false
	}
	active := PlanningActiveStream{
		RunID:          newJobID(),
		ConversationID: conv.ID,
		DiscussionID:   d.ID,
		OwnerUserID:    userID,
		StartedAt:      time.Now(),
	}
	if err := s.d.PlanningStreams.SetActive(r.Context(), active); err != nil {
		s.refundPlanning(context.Background(), userID, d.ID, reserved, reserveLedgerID)
		http.Error(w, "planning stream recovery is unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	payload := PlanningTurnPayload{
		RunID:           active.RunID,
		ConversationID:  conv.ID,
		DiscussionID:    d.ID,
		UserID:          userID,
		Language:        languageOverride,
		Reserved:        reserved,
		ReserveLedgerID: reserveLedgerID,
	}
	// Release the in-process claim BEFORE publishing: with the in-process
	// queue fallback the consumer may pick the task up synchronously-fast
	// and needs to take the claim itself.
	s.releasePlanningRun(conv.ID)
	task, err := mq.NewTask(mq.TaskPlanningTurn, active.RunID, payload)
	if err == nil {
		err = s.d.MQ.Publish(r.Context(), mq.QueuePlanning, task)
	}
	if err != nil {
		s.d.PlanningStreams.ClearActive(context.Background(), conv.ID, active.RunID)
		s.refundPlanning(context.Background(), userID, d.ID, reserved, reserveLedgerID)
		s.logger().Error("planning turn enqueue failed", "discussion", d.ID, "conversation", conv.ID, "err", err)
		http.Error(w, "planning turn could not be enqueued", http.StatusServiceUnavailable)
		return nil, false
	}
	return &active, true
}

// RunPlanningTurnTask executes one queued planning-turn attempt. The Redis
// Active record is the distributed claim: a mismatched or expired record
// means this run was superseded or abandoned — the reservation is refunded
// and the delivery acked. A non-nil return is the attempt's failure; the
// dispatch layer schedules the retry (the Active record and reservation
// stay live across the backoff) or runs FailPlanningTurnTask.
func (s *Server) RunPlanningTurnTask(ctx context.Context, pl PlanningTurnPayload) error {
	started := time.Now()
	active, ok := s.d.PlanningStreams.Active(ctx, pl.ConversationID)
	if !ok || active == nil || active.RunID != pl.RunID {
		s.logger().Info("planning turn superseded or expired; refunding",
			"conversation", pl.ConversationID, "run", pl.RunID)
		s.refundPlanning(context.Background(), pl.UserID, pl.DiscussionID, pl.Reserved, pl.ReserveLedgerID)
		return nil
	}
	if !s.claimPlanningRun(pl.ConversationID) {
		s.logger().Info("planning turn already running in this process; skipping",
			"conversation", pl.ConversationID, "run", pl.RunID)
		return nil
	}
	defer s.releasePlanningRun(pl.ConversationID)

	workCtx, cancel := context.WithTimeout(ctx, discussionStreamRecoveryTimeout)
	defer cancel()

	d, err := s.d.Discussions.Get(workCtx, pl.UserID, pl.DiscussionID)
	if err != nil {
		return fmt.Errorf("load discussion: %w", err)
	}
	conv, err := s.d.Planning.ConversationByDiscussion(workCtx, pl.UserID, pl.DiscussionID)
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	if d == nil || conv == nil || conv.ID != pl.ConversationID {
		return mq.Permanent(fmt.Errorf("planning conversation %s not found", pl.ConversationID))
	}
	p, err := planner.New(s.plannerEnv())
	if err != nil {
		return fmt.Errorf("planner init: %w", err)
	}

	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	sink := planningStreamSink{
		ctx:            workCtx,
		store:          s.d.PlanningStreams,
		runID:          pl.RunID,
		conversationID: conv.ID,
	}
	_ = sink.send("progress", planner.ProgressEvent{Phase: "thinking", Text: "Thinking…"})
	s.recordDiscussionProgress(workCtx, d.ID, "planning", planner.ProgressEvent{Phase: "thinking", Text: "Thinking…"})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, d.ID, "planning", ev)
		_ = sink.send("progress", ev)
	})

	turns, err := s.d.Planning.Turns(workCtx, conv.ID)
	if err != nil {
		return fmt.Errorf("load turns: %w", err)
	}
	history := planningMessagesForLLM(turns, s.uploadURLRefresher(workCtx))
	s.logger().Info("stored planning turn started",
		"discussion", d.ID,
		"conversation", conv.ID,
		"run", pl.RunID,
		"turns", len(turns),
		"history", len(history),
		"last_role", planningLastTurnRole(turns),
	)
	opts := planner.ConversationOptions{
		Type:             planningContentType(d, turns),
		Language:         planningLanguage(d, pl.Language),
		Channel:          planningChannel(d),
		Discussants:      planningDiscussants(d),
		Template:         planningTemplate(d, planningContentType(d, turns)),
		AgentModel:       planningAgentModel(d),
		ExistingSources:  d.Sources,
		ExistingPlan:     d.Script,
		ExistingMarkdown: d.Markdown,
	}
	emit := func(ev planner.ConvEvent) { s.handlePlanningConvEvent(workCtx, sink, pl.UserID, d.ID, conv.ID, ev) }
	paused, runErr := p.RunConversationTurn(workCtx, history, opts, emit)

	if runErr != nil {
		// Turn persistence is append-only, so a retry continues from the
		// persisted prefix. Keep the Active record and the reservation
		// alive; the dispatch layer either retries or fails terminally.
		return runErr
	}
	s.settlePlanningConversation(workCtx, pl.UserID, d.ID, conv, pl.Reserved, pl.ReserveLedgerID, meter)
	if paused {
		_ = s.d.Planning.SetStatus(workCtx, conv.ID, PlanningConversationAwaitingAnswer)
	} else {
		_ = s.d.Planning.SetStatus(workCtx, conv.ID, PlanningConversationActive)
	}
	s.clearDiscussionProgress(workCtx, d.ID)

	updated, _ := s.d.Discussions.Get(workCtx, pl.UserID, d.ID)
	if updated != nil {
		if total, err := s.pointsCharged(workCtx, d.ID); err == nil {
			updated.PointsCharged = total
		}
		s.sanitizeDiscussionUsage(updated)
	}
	convFresh, _ := s.d.Planning.ConversationByDiscussion(workCtx, pl.UserID, d.ID)
	finalTurns, _ := s.d.Planning.Turns(workCtx, conv.ID)
	_ = sink.send("done", planningDonePayload{
		Discussion: updated,
		Conversation: PlanningConversationView{
			Conversation: convFresh,
			Parts:        planningConversationParts(finalTurns),
			NeedsRun:     planningConversationShouldAutoRun(convFresh, finalTurns),
		},
	})
	s.d.PlanningStreams.ClearActive(context.Background(), conv.ID, pl.RunID)
	s.logger().Info("stored planning turn finished",
		"discussion", d.ID,
		"conversation", conv.ID,
		"run", pl.RunID,
		"paused", paused,
		"turns", len(finalTurns),
		"elapsed_ms", time.Since(started).Milliseconds(),
	)
	return nil
}

// PlanningTurnRetrying surfaces a pending retry on the live stream so the
// user watching the SSE tail sees progress rather than silence through the
// backoff window. The Active record's TTL comfortably covers the backoff.
func (s *Server) PlanningTurnRetrying(pl PlanningTurnPayload, attempt int, delay time.Duration) {
	sink := planningStreamSink{
		ctx:            context.Background(),
		store:          s.d.PlanningStreams,
		runID:          pl.RunID,
		conversationID: pl.ConversationID,
	}
	_ = sink.send("progress", planner.ProgressEvent{
		Phase: "retrying",
		Text:  fmt.Sprintf("Retrying (attempt %d/%d)…", attempt+1, mq.MaxAttempts),
	})
}

// FailPlanningTurnTask is the terminal failure path of a queued planning
// turn: refund the reservation, mark the conversation failed, emit the
// error frame, and release the Active record.
func (s *Server) FailPlanningTurnTask(pl PlanningTurnPayload, cause error) {
	ctx := context.Background()
	msg := "planning turn failed"
	if cause != nil {
		msg = cause.Error()
	}
	s.refundPlanning(ctx, pl.UserID, pl.DiscussionID, pl.Reserved, pl.ReserveLedgerID)
	_ = s.d.Planning.SetStatus(ctx, pl.ConversationID, PlanningConversationFailed)
	s.clearDiscussionProgress(ctx, pl.DiscussionID)
	sink := planningStreamSink{
		ctx:            ctx,
		store:          s.d.PlanningStreams,
		runID:          pl.RunID,
		conversationID: pl.ConversationID,
	}
	_ = sink.send("error", map[string]string{"message": msg})
	s.d.PlanningStreams.ClearActive(ctx, pl.ConversationID, pl.RunID)
}

func (s *Server) streamPlanningActiveRun(w http.ResponseWriter, r *http.Request, runID string) {
	if !s.d.PlanningStreams.Enabled() || strings.TrimSpace(runID) == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	lastID := "0"
	for {
		frames, err := s.d.PlanningStreams.Read(r.Context(), runID, lastID, 2*time.Second, 32)
		if err != nil {
			if errors.Is(err, redis.Nil) {
				_ = sse.comment("ok")
				continue
			}
			if r.Context().Err() != nil {
				return
			}
			_ = sse.send("error", map[string]string{"message": err.Error()})
			return
		}
		if len(frames) == 0 {
			_ = sse.comment("ok")
			continue
		}
		for _, frame := range frames {
			lastID = frame.ID
			if err := sse.sendRaw(frame.Event, frame.Payload); err != nil {
				return
			}
			if frame.Event == "done" || frame.Event == "error" {
				return
			}
		}
	}
}

// handlePlanningConvEvent persists each conversational event and forwards it to
// the client over SSE, mapping onto the linda-style event names.
func (s *Server) handlePlanningConvEvent(ctx context.Context, sink planningEventWriter, userID, discID, convID string, ev planner.ConvEvent) {
	switch ev.Kind {
	case planner.ConvText:
		_ = sink.send("text-delta", map[string]string{"text": ev.Text})
	case planner.ConvToolStart:
		_ = sink.send("tool-input-start", map[string]string{"toolCallId": ev.ToolCallID, "toolName": ev.ToolName})
	case planner.ConvToolDelta:
		_ = sink.send("tool-input-delta", map[string]string{
			"toolCallId": ev.ToolCallID,
			"toolName":   ev.ToolName,
			"delta":      ev.Text,
		})
	case planner.ConvAssistant:
		_ = s.d.Planning.AppendTurn(ctx, convID, planningTurnInput{Role: "assistant", Text: ev.Text, ToolCalls: ev.Calls})
	case planner.ConvToolCall:
		_ = sink.send("tool-call", map[string]any{
			"toolCallId": ev.Call.ID,
			"toolName":   ev.Call.Name,
			"input":      rawJSONOrNull(ev.Call.Arguments),
		})
	case planner.ConvToolResult:
		if ev.Plan != nil {
			resp := planResponse{Script: ev.Plan.Script, Markdown: ev.Plan.Markdown, Sources: ev.Plan.Sources, Researched: ev.Plan.Researched}
			_, _ = s.d.Discussions.UpdatePlan(ctx, userID, discID, resp)
		}
		_ = s.d.Planning.AppendTurn(ctx, convID, planningTurnInput{
			Role:       "tool",
			ToolCallID: ev.Call.ID,
			ToolName:   ev.Call.Name,
			ResultText: ev.Output,
			IsError:    ev.IsError,
			Script:     planScript(ev.Plan),
			Sources:    planSources(ev.Plan),
			Markdown:   planMarkdown(ev.Plan),
		})
		_ = sink.send("tool-result", map[string]any{
			"toolCallId": ev.Call.ID,
			"toolName":   ev.Call.Name,
			"output":     ev.Output,
			"isError":    ev.IsError,
		})
	case planner.ConvPlan:
		if ev.Plan != nil {
			resp := planResponse{Script: ev.Plan.Script, Markdown: ev.Plan.Markdown, Sources: ev.Plan.Sources, Researched: ev.Plan.Researched}
			_, _ = s.d.Discussions.UpdatePlan(ctx, userID, discID, resp)
			_ = s.d.Planning.AppendTurn(ctx, convID, planningTurnInput{
				Role:       "tool",
				ToolCallID: ev.Call.ID,
				ToolName:   ev.Call.Name,
				ResultText: ev.Output,
				Script:     ev.Plan.Script,
				Sources:    ev.Plan.Sources,
				Markdown:   ev.Plan.Markdown,
			})
			_ = sink.send("plan", map[string]any{
				"toolCallId": ev.Call.ID,
				"toolName":   ev.Call.Name,
				"script":     ev.Plan.Script,
				"sources":    ev.Plan.Sources,
				"markdown":   ev.Plan.Markdown,
			})
		}
	case planner.ConvQuestion:
		questionID := newJobID()
		_ = s.d.Planning.AppendTurn(ctx, convID, planningTurnInput{
			Role:           "question",
			ToolCallID:     ev.Call.ID,
			ToolName:       ev.Call.Name,
			QuestionID:     questionID,
			QuestionsJSON:  ev.QuestionsJSON,
			QuestionStatus: "pending",
		})
		_ = sink.send("question_required", map[string]any{
			"questionId": questionID,
			"toolCallId": ev.Call.ID,
			"toolName":   ev.Call.Name,
			"questions":  rawJSONOrNull(ev.QuestionsJSON),
		})
	}
}

// settlePlanningConversation reconciles the turn's reservation against actual LLM
// usage, applying the one-time per-conversation point floor on the first turn so
// planning is never free even when the metered cost rounds to zero.
func (s *Server) settlePlanningConversation(ctx context.Context, userID, discID string, conv *PlanningConversation, reserved, reserveLedgerID int64, acc *usageAccumulator) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	sum := acc.snapshot()
	actual := pointsForCost(s.d.Env, sum.CostUSD)
	if conv != nil && !conv.FlatCharged {
		if floor := s.d.Env.PointsMinPerPlanningConversation; actual < floor {
			actual = floor
		}
		_ = s.d.Planning.MarkFlatCharged(ctx, conv.ID)
		conv.FlatCharged = true
	}
	detail := PointsUsageDetail{
		PromptTokens:     sum.PromptTokens,
		CompletionTokens: sum.CompletionTokens,
		TotalTokens:      sum.TotalTokens,
		LLMCostUSD:       sum.CostUSD,
		LLMCostKnown:     sum.CostKnown,
		CostUSD:          sum.CostUSD,
	}
	if _, err := s.d.Points.SettlePlanning(ctx, userID, discID, reserveLedgerID, reserved, actual, detail); err != nil {
		s.logger().Warn("planning conversation settle failed", "discussion", discID, "err", err)
	}
}

func (s *Server) claimPlanningRun(conversationID string) bool {
	s.planningRunMu.Lock()
	defer s.planningRunMu.Unlock()
	if s.planningRuns[conversationID] {
		return false
	}
	s.planningRuns[conversationID] = true
	return true
}

func (s *Server) releasePlanningRun(conversationID string) {
	s.planningRunMu.Lock()
	delete(s.planningRuns, conversationID)
	s.planningRunMu.Unlock()
}

// planningChannel returns the channel to carry forward when assembling a plan
// (preserving the discussion's existing channel), or "" to fall back to default.
func planningChannel(d *Discussion) string {
	if d != nil && d.Script != nil {
		return d.Script.Channel
	}
	return ""
}

func planningLanguage(d *Discussion, override string) string {
	if lang := strings.TrimSpace(override); lang != "" {
		return lang
	}
	if d != nil && strings.TrimSpace(d.Language) != "" {
		return strings.TrimSpace(d.Language)
	}
	return "en-US"
}

func planningDiscussants(d *Discussion) int {
	if d != nil && d.Script != nil && len(d.Script.Discussants) > 0 {
		return len(d.Script.Discussants)
	}
	return 0
}

func planningTemplate(d *Discussion, contentType string) string {
	if d == nil {
		return planner.DefaultTemplateID
	}
	tmpl := strings.TrimSpace(d.Template)
	if contentType == "" {
		contentType = config.ContentTypeDiscussion
	}
	if _, ok := planner.TemplateByID(contentType, tmpl); ok {
		if tmpl == "" {
			return planner.DefaultTemplateID
		}
		return tmpl
	}
	return planner.DefaultTemplateID
}

func planningContentType(d *Discussion, turns []planningTurnRow) string {
	if d != nil && d.Script != nil && strings.TrimSpace(d.Script.Type) != "" {
		return strings.TrimSpace(d.Script.Type)
	}
	for _, turn := range turns {
		text := strings.TrimSpace(turn.Text)
		if strings.Contains(text, "- Content type: "+config.ContentTypeAudioBook) ||
			strings.Contains(text, "Content type: "+config.ContentTypeAudioBook) {
			return config.ContentTypeAudioBook
		}
	}
	return config.ContentTypeDiscussion
}

func planningAgentModel(d *Discussion) string {
	if d == nil || d.Script == nil {
		return ""
	}
	if d.Script.Host.Model != "" {
		return d.Script.Host.Model
	}
	for _, a := range d.Script.Discussants {
		if a.Model != "" {
			return a.Model
		}
	}
	return ""
}

func planScript(res *planner.Result) *config.DebateTopic {
	if res == nil {
		return nil
	}
	return res.Script
}

func planSources(res *planner.Result) []config.Source {
	if res == nil {
		return nil
	}
	return res.Sources
}

func planMarkdown(res *planner.Result) string {
	if res == nil {
		return ""
	}
	return res.Markdown
}

// planningAnswerDigest renders the user's answer as the tool-result text the
// model sees on resume.
func planningAnswerDigest(status, answersJSON, language string) string {
	var sb strings.Builder
	if status == "rejected" {
		sb.WriteString("The user skipped the questions. Proceed with sensible assumptions and note any you made.")
	} else {
		sb.WriteString("The user answered: " + answersJSON)
	}
	if lang := strings.TrimSpace(language); lang != "" {
		sb.WriteString("\n\nCurrent plan settings:\n")
		sb.WriteString("- Language for all names and text: " + lang)
	}
	return sb.String()
}

// rawJSONOrNull returns the input as raw JSON when it is valid, else null, so an
// SSE payload never carries a malformed fragment.
func rawJSONOrNull(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" || !json.Valid([]byte(s)) {
		return json.RawMessage("null")
	}
	return json.RawMessage(s)
}

func planningLastTurnRole(turns []planningTurnRow) string {
	if len(turns) == 0 {
		return ""
	}
	return turns[len(turns)-1].Role
}
