package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/planner"
	"github.com/sirily11/debate-bot/internal/qa"
)

// qaSystemPromptAllowance approximates the QA system prompt + tool schema
// character count for the compaction threshold check (the exact prompt lives
// in the qa package; the heuristic only needs the right order of magnitude).
const qaSystemPromptAllowance = 8_000

const qaTurnTimeout = 10 * time.Minute

// qaStreamRequest starts/continues a Q&A turn with a user message.
type qaStreamRequest struct {
	Prompt   string `json:"prompt"`
	Language string `json:"language,omitempty"`
	Resume   bool   `json:"resume,omitempty"`
}

// qaDonePayload is the terminal SSE "done" frame.
type qaDonePayload struct {
	Conversation QAConversationView `json:"conversation"`
}

func (s *Server) requireChatPermission(w http.ResponseWriter, r *http.Request) bool {
	// Hermetic E2E runs intentionally do not seed subscription permission rows.
	if s.e2eMode() {
		return true
	}
	allowed, err := s.chatAllowedForUser(r.Context(), s.requestUser(r).ID)
	if err != nil {
		http.Error(w, "could not resolve chat permission", http.StatusServiceUnavailable)
		return false
	}
	if !allowed {
		http.Error(w, "chat is not included in your subscription", http.StatusForbidden)
		return false
	}
	return true
}

// qaScopeFromRequest resolves which conversation a QA route addresses: the
// path's discussion (podcast Q&A) or, on the /api/chat routes, the caller's
// global chat. The returned Discussion is nil for global scope.
func (s *Server) qaScopeFromRequest(w http.ResponseWriter, r *http.Request) (*Discussion, string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		return nil, "", true
	}
	user := s.requestUser(r)
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, "", false
	}
	if d == nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	return d, d.ID, true
}

// handleQAConversationGet serves GET /api/discussions/{id}/qa and
// GET /api/chat: the persisted conversation for history rebuild. Unlike the
// planning equivalent it never 404s an existing podcast without a
// conversation — it returns an empty view the client can start chatting from.
func (s *Server) handleQAConversationGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireChatPermission(w, r) {
		return
	}
	user := s.requestUser(r)
	d, discussionID, ok := s.qaScopeFromRequest(w, r)
	if !ok {
		return
	}
	_ = d
	conv, err := s.d.QA.Conversation(r.Context(), user.ID, discussionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view := QAConversationView{Parts: []QAPart{}}
	if conv != nil {
		view.Conversation = conv
		turns, err := s.d.QA.Turns(r.Context(), conv.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		view.Parts = qaConversationParts(turns)
		view.NeedsRun = qaConversationNeedsRun(turns)
		if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
			view.IsRunning = true
			view.ActiveStream = active.RunID
		}
	}
	writeJSON(w, view)
}

// handleQAConversationClear serves DELETE /api/discussions/{id}/qa and
// DELETE /api/chat. It removes every message from the addressed conversation
// while preserving billing metadata, so clearing history cannot reset usage.
func (s *Server) handleQAConversationClear(w http.ResponseWriter, r *http.Request) {
	if !s.requireChatPermission(w, r) {
		return
	}
	user := s.requestUser(r)
	_, discussionID, ok := s.qaScopeFromRequest(w, r)
	if !ok {
		return
	}
	conv, err := s.d.QA.Conversation(r.Context(), user.ID, discussionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if conv == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok && active != nil {
		http.Error(w, "a chat turn is already in progress", http.StatusConflict)
		return
	}
	if s.qaRunIsActive(conv.ID) {
		http.Error(w, "a chat turn is already in progress", http.StatusConflict)
		return
	}
	if err := s.d.QA.ClearTurns(r.Context(), user.ID, discussionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleQAStreamResume serves GET /api/discussions/{id}/qa/stream and
// GET /api/chat/stream: reattach to an in-flight run. 204 when none.
func (s *Server) handleQAStreamResume(w http.ResponseWriter, r *http.Request) {
	if !s.requireChatPermission(w, r) {
		return
	}
	user := s.requestUser(r)
	_, discussionID, ok := s.qaScopeFromRequest(w, r)
	if !ok {
		return
	}
	conv, err := s.d.QA.Conversation(r.Context(), user.ID, discussionID)
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

// handleQAStream serves POST /api/discussions/{id}/qa/stream and
// POST /api/chat/stream: append the user message and stream the agent turn.
func (s *Server) handleQAStream(w http.ResponseWriter, r *http.Request) {
	if !s.requireChatPermission(w, r) {
		return
	}
	user := s.requestUser(r)
	d, discussionID, ok := s.qaScopeFromRequest(w, r)
	if !ok {
		return
	}
	if d != nil && d.Status != DiscussionReady {
		http.Error(w, "podcast is not ready yet", http.StatusConflict)
		return
	}
	var req qaStreamRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if s.e2eMode() && e2ePlanningInsufficientBalancePrompt(req.Prompt) {
		writeInsufficientPoints(w, 50, 0)
		return
	}
	conv, err := s.d.QA.EnsureConversation(r.Context(), user.ID, discussionID)
	if err != nil || conv == nil {
		http.Error(w, "could not start conversation", http.StatusInternalServerError)
		return
	}
	if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
		s.streamPlanningActiveRun(w, r, active.RunID)
		return
	}

	if req.Resume {
		turns, err := s.d.QA.Turns(r.Context(), conv.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !qaConversationNeedsRun(turns) {
			sse := newSSEWriter(w)
			_ = sse.comment("ok")
			_ = sse.send("done", qaDonePayload{Conversation: QAConversationView{
				Conversation: conv,
				Parts:        qaConversationParts(turns),
			}})
			return
		}
	} else {
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			http.Error(w, "prompt is required", http.StatusBadRequest)
			return
		}
		if err := s.d.QA.AppendTurn(r.Context(), conv.ID, qaTurnInput{Role: "user", Text: prompt}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	pointsRef := qaPointsRef(conv)
	if s.d.PlanningStreams.Enabled() {
		if !s.claimQARun(conv.ID) {
			if active, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok {
				s.streamPlanningActiveRun(w, r, active.RunID)
				return
			}
			http.Error(w, "a chat turn is already in progress", http.StatusConflict)
			return
		}
		active, ok := s.startStoredQARun(w, r, user.ID, conv, req.Language)
		if !ok {
			s.releaseQARun(conv.ID)
			return
		}
		s.streamPlanningActiveRun(w, r, active.RunID)
		return
	}
	if !s.claimQARun(conv.ID) {
		http.Error(w, "a chat turn is already in progress", http.StatusConflict)
		return
	}
	defer s.releaseQARun(conv.ID)

	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, pointsRef)
	if !ok {
		return
	}
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	workCtx, cancel := context.WithTimeout(context.Background(), qaTurnTimeout)
	defer cancel()
	if err := s.runQATurnCore(workCtx, sse, user.ID, conv, req.Language, reserved, reserveLedgerID); err != nil {
		_ = sse.send("error", map[string]string{"message": err.Error()})
	}
}

// qaPointsRef is the points-ledger reference for a conversation: the podcast
// for per-podcast Q&A, the conversation itself for the global chat (the
// ledger's discussion_id column is a free-text reference, not an FK).
func qaPointsRef(conv *QAConversation) string {
	if conv.DiscussionID != "" {
		return conv.DiscussionID
	}
	return conv.ID
}

// QATurnPayload is the wire payload of a queued Q&A turn, mirroring
// PlanningTurnPayload (the Redis Active record is the distributed claim).
type QATurnPayload struct {
	RunID           string `json:"run_id"`
	ConversationID  string `json:"conversation_id"`
	DiscussionID    string `json:"discussion_id,omitempty"`
	UserID          string `json:"user_id"`
	Language        string `json:"language,omitempty"`
	Reserved        int64  `json:"reserved"`
	ReserveLedgerID int64  `json:"reserve_ledger_id"`
}

func (s *Server) startStoredQARun(w http.ResponseWriter, r *http.Request, userID string, conv *QAConversation, language string) (*PlanningActiveStream, bool) {
	if existing, ok := s.d.PlanningStreams.Active(r.Context(), conv.ID); ok && existing != nil {
		return existing, true
	}
	if s.d.MQ == nil {
		http.Error(w, "chat queue is not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	pointsRef := qaPointsRef(conv)
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, userID, pointsRef)
	if !ok {
		return nil, false
	}
	active := PlanningActiveStream{
		RunID:          newJobID(),
		ConversationID: conv.ID,
		DiscussionID:   conv.DiscussionID,
		OwnerUserID:    userID,
		StartedAt:      time.Now(),
	}
	if err := s.d.PlanningStreams.SetActive(r.Context(), active); err != nil {
		s.refundPlanning(context.Background(), userID, pointsRef, reserved, reserveLedgerID)
		http.Error(w, "chat stream recovery is unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	payload := QATurnPayload{
		RunID:           active.RunID,
		ConversationID:  conv.ID,
		DiscussionID:    conv.DiscussionID,
		UserID:          userID,
		Language:        language,
		Reserved:        reserved,
		ReserveLedgerID: reserveLedgerID,
	}
	// Release the in-process claim BEFORE publishing (see startStoredPlanningRun).
	s.releaseQARun(conv.ID)
	task, err := mq.NewTask(mq.TaskQATurn, active.RunID, payload)
	if err == nil {
		err = s.d.MQ.Publish(r.Context(), mq.QueuePlanning, task)
	}
	if err != nil {
		s.d.PlanningStreams.ClearActive(context.Background(), conv.ID, active.RunID)
		s.refundPlanning(context.Background(), userID, pointsRef, reserved, reserveLedgerID)
		s.logger().Error("qa turn enqueue failed", "conversation", conv.ID, "err", err)
		http.Error(w, "chat turn could not be enqueued", http.StatusServiceUnavailable)
		return nil, false
	}
	return &active, true
}

// RunQATurnTask executes one queued Q&A turn attempt (see RunPlanningTurnTask
// for the claim/refund semantics it mirrors).
func (s *Server) RunQATurnTask(ctx context.Context, pl QATurnPayload) error {
	active, ok := s.d.PlanningStreams.Active(ctx, pl.ConversationID)
	if !ok || active == nil || active.RunID != pl.RunID {
		s.logger().Info("qa turn superseded or expired; refunding",
			"conversation", pl.ConversationID, "run", pl.RunID)
		s.refundPlanning(context.Background(), pl.UserID, qaPointsRefFromPayload(pl), pl.Reserved, pl.ReserveLedgerID)
		return nil
	}
	if !s.claimQARun(pl.ConversationID) {
		return nil
	}
	defer s.releaseQARun(pl.ConversationID)

	workCtx, cancel := context.WithTimeout(ctx, qaTurnTimeout)
	defer cancel()
	conv, err := s.d.QA.ConversationByID(workCtx, pl.UserID, pl.ConversationID)
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	if conv == nil {
		return mq.Permanent(fmt.Errorf("qa conversation %s not found", pl.ConversationID))
	}
	sink := planningStreamSink{
		ctx:            workCtx,
		store:          s.d.PlanningStreams,
		runID:          pl.RunID,
		conversationID: conv.ID,
	}
	_ = sink.send("progress", planner.ProgressEvent{Phase: "thinking", Text: "Thinking…"})
	if err := s.runQATurnCore(workCtx, sink, pl.UserID, conv, pl.Language, pl.Reserved, pl.ReserveLedgerID); err != nil {
		// Persistence is append-only; a retry resumes from the persisted
		// prefix. Keep the Active record + reservation across the backoff.
		return err
	}
	s.d.PlanningStreams.ClearActive(context.Background(), conv.ID, pl.RunID)
	return nil
}

func qaPointsRefFromPayload(pl QATurnPayload) string {
	if pl.DiscussionID != "" {
		return pl.DiscussionID
	}
	return pl.ConversationID
}

// QATurnRetrying surfaces a pending retry on the live stream.
func (s *Server) QATurnRetrying(pl QATurnPayload, attempt int, delay time.Duration) {
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

// FailQATurnTask is the terminal failure path of a queued Q&A turn.
func (s *Server) FailQATurnTask(pl QATurnPayload, cause error) {
	ctx := context.Background()
	msg := "chat turn failed"
	if cause != nil {
		msg = cause.Error()
	}
	s.refundPlanning(ctx, pl.UserID, qaPointsRefFromPayload(pl), pl.Reserved, pl.ReserveLedgerID)
	_ = s.d.QA.SetStatus(ctx, pl.ConversationID, QAConversationFailed)
	sink := planningStreamSink{
		ctx:            ctx,
		store:          s.d.PlanningStreams,
		runID:          pl.RunID,
		conversationID: pl.ConversationID,
	}
	_ = sink.send("error", map[string]string{"message": msg})
	s.d.PlanningStreams.ClearActive(ctx, pl.ConversationID, pl.RunID)
}

// runQATurnCore is the shared turn body for the inline-SSE and queued paths:
// compact the history if over budget, run the agent loop persisting each
// event, settle billing, and emit the terminal done frame. A non-nil return
// means the reservation is still held (the caller refunds or retries) —
// except that on success billing has been settled.
func (s *Server) runQATurnCore(ctx context.Context, sink planningEventWriter, userID string, conv *QAConversation, language string, reserved, reserveLedgerID int64) error {
	pointsRef := qaPointsRef(conv)
	turns, err := s.d.QA.ModelTurns(ctx, conv.ID)
	if err != nil {
		s.refundPlanning(ctx, userID, pointsRef, reserved, reserveLedgerID)
		return err
	}
	turns, history := s.compactQAHistory(ctx, conv, turns)

	opts, err := s.qaOptions(ctx, userID, conv, language)
	if err != nil {
		s.refundPlanning(ctx, userID, pointsRef, reserved, reserveLedgerID)
		return err
	}
	meter := &usageAccumulator{}
	client := llm.New(s.d.Env.OpenAIBaseURL, s.d.Env.OpenAIKey, s.resolvedQAModel(ctx)).
		WithUsageRecorder(meter.record).
		WithPricing(s.d.Env.LLMInputCostPerMillion, s.d.Env.LLMOutputCostPerMillion)
	retriever := &qaRetriever{s: s, owner: userID}
	emit := func(ev qa.ConvEvent) { s.handleQAConvEvent(ctx, sink, conv.ID, ev) }

	s.logger().Info("qa turn started",
		"conversation", conv.ID,
		"scope", opts.Scope,
		"turns", len(turns),
		"history", len(history),
	)
	if runErr := qa.RunTurn(ctx, client, retriever, history, opts, emit); runErr != nil {
		s.refundPlanning(ctx, userID, pointsRef, reserved, reserveLedgerID)
		_ = s.d.QA.SetStatus(ctx, conv.ID, QAConversationFailed)
		return runErr
	}
	s.settleQAConversation(ctx, userID, pointsRef, conv, reserved, reserveLedgerID, meter)
	_ = s.d.QA.SetStatus(ctx, conv.ID, QAConversationActive)

	convFresh, _ := s.d.QA.ConversationByID(ctx, userID, conv.ID)
	finalTurns, _ := s.d.QA.Turns(ctx, conv.ID)
	_ = sink.send("done", qaDonePayload{Conversation: QAConversationView{
		Conversation: convFresh,
		Parts:        qaConversationParts(finalTurns),
		NeedsRun:     qaConversationNeedsRun(finalTurns),
	}})
	return nil
}

// compactQAHistory applies the rolling context window: when the model view
// exceeds the budget, the evicted prefix is LLM-summarized into a summary
// turn and marked compacted. Graceful degradation — any failure returns the
// original history untouched.
func (s *Server) compactQAHistory(ctx context.Context, conv *QAConversation, turns []qaTurnRow) ([]qaTurnRow, []llm.Message) {
	history := qaMessagesForLLM(turns)
	if !qa.NeedsCompaction(history, qaSystemPromptAllowance) {
		return turns, history
	}
	// Model-view rows map 1:1 onto history messages, so a message boundary
	// index is also a turn index.
	boundary := qa.CompactionBoundary(history)
	if boundary <= 0 || boundary > len(turns) {
		return turns, history
	}
	compressClient := llm.New(s.d.Env.CompressionBaseURL, s.d.Env.CompressionKey, s.d.Env.CompressionModel)
	summary, err := qa.SummarizeEvicted(ctx, compressClient, history[:boundary])
	if err != nil {
		s.logger().Warn("qa compaction failed; continuing uncompacted", "conversation", conv.ID, "err", err)
		return turns, history
	}
	keepFromSeq := turns[boundary].Seq
	if err := s.d.QA.Compact(ctx, conv.ID, keepFromSeq, summary); err != nil {
		s.logger().Warn("qa compaction persist failed", "conversation", conv.ID, "err", err)
		return turns, history
	}
	fresh, err := s.d.QA.ModelTurns(ctx, conv.ID)
	if err != nil {
		return turns, history
	}
	s.logger().Info("qa history compacted",
		"conversation", conv.ID,
		"evicted", boundary,
		"kept", len(fresh),
	)
	return fresh, qaMessagesForLLM(fresh)
}

// qaOptions assembles the per-turn agent options for a conversation's scope.
func (s *Server) qaOptions(ctx context.Context, userID string, conv *QAConversation, language string) (qa.Options, error) {
	if conv.DiscussionID == "" {
		return qa.Options{Scope: qa.ScopeGlobal, Language: language, ConversationID: conv.ID}, nil
	}
	d, err := s.d.Discussions.Get(ctx, userID, conv.DiscussionID)
	if err != nil {
		return qa.Options{}, err
	}
	if d == nil {
		return qa.Options{}, fmt.Errorf("podcast %s not found", conv.DiscussionID)
	}
	opts := qa.Options{
		Scope:          qa.ScopePodcast,
		DiscussionID:   d.ID,
		ConversationID: conv.ID,
		Language:       language,
		PodcastTitle:   d.Title,
		PodcastTopic:   d.Topic,
	}
	if opts.Language == "" {
		opts.Language = d.Language
	}
	if doc, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypeSummary); err == nil && doc != nil && doc.Status == SummaryReadyState {
		opts.SummaryMarkdown = doc.Markdown
	}
	return opts, nil
}

// handleQAConvEvent persists each agent event and forwards it as an SSE frame,
// mirroring handlePlanningConvEvent's mapping plus the dedicated card frames.
func (s *Server) handleQAConvEvent(ctx context.Context, sink planningEventWriter, convID string, ev qa.ConvEvent) {
	switch ev.Kind {
	case qa.ConvText:
		_ = sink.send("text-delta", map[string]string{"text": ev.Text})
	case qa.ConvToolStart:
		_ = sink.send("tool-input-start", map[string]string{"toolCallId": ev.ToolCallID, "toolName": ev.ToolName})
	case qa.ConvToolDelta:
		_ = sink.send("tool-input-delta", map[string]string{
			"toolCallId": ev.ToolCallID,
			"toolName":   ev.ToolName,
			"delta":      ev.Text,
		})
	case qa.ConvAssistant:
		_ = s.d.QA.AppendTurn(ctx, convID, qaTurnInput{Role: "assistant", Text: ev.Text, ToolCalls: ev.Calls})
	case qa.ConvToolCall:
		_ = sink.send("tool-call", map[string]any{
			"toolCallId": ev.Call.ID,
			"toolName":   ev.Call.Name,
			"input":      rawJSONOrNull(ev.Call.Arguments),
		})
	case qa.ConvToolResult:
		_ = s.d.QA.AppendTurn(ctx, convID, qaTurnInput{
			Role:       "tool",
			ToolCallID: ev.Call.ID,
			ToolName:   ev.Call.Name,
			ResultText: ev.Output,
			IsError:    ev.IsError,
		})
		_ = sink.send("tool-result", map[string]any{
			"toolCallId": ev.Call.ID,
			"toolName":   ev.Call.Name,
			"output":     ev.Output,
			"isError":    ev.IsError,
		})
	case qa.ConvCard:
		payloadJSON := ""
		if ev.Card != nil {
			if b, err := json.Marshal(ev.Card); err == nil {
				payloadJSON = string(b)
			}
		}
		_ = s.d.QA.AppendTurn(ctx, convID, qaTurnInput{
			Role:        "tool",
			ToolCallID:  ev.Call.ID,
			ToolName:    ev.Call.Name,
			ResultText:  ev.Output,
			PayloadJSON: payloadJSON,
		})
		if ev.Card != nil {
			_ = sink.send(ev.Card.Kind, map[string]any{
				"toolCallId": ev.Call.ID,
				"toolName":   ev.Call.Name,
				"card":       ev.Card,
			})
		}
	}
}

// settleQAConversation reconciles the turn's reservation against actual LLM
// usage, applying the one-time per-conversation floor (same floor as
// planning) on the first turn.
func (s *Server) settleQAConversation(ctx context.Context, userID, pointsRef string, conv *QAConversation, reserved, reserveLedgerID int64, acc *usageAccumulator) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	sum := acc.snapshot()
	actual := pointsForCost(s.d.Env, sum.CostUSD)
	if conv != nil && !conv.FlatCharged {
		if floor := s.d.Env.PointsMinPerPlanningConversation; actual < floor {
			actual = floor
		}
		_ = s.d.QA.MarkFlatCharged(ctx, conv.ID)
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
	if _, err := s.d.Points.SettlePlanning(ctx, userID, pointsRef, reserveLedgerID, reserved, actual, detail); err != nil {
		s.logger().Warn("qa settle failed", "conversation", conv.ID, "err", err)
	}
	if conv != nil {
		_, _ = s.d.QA.exec(ctx, `UPDATE qa_conversations SET points_charged = points_charged + ? WHERE id = ?`, actual, conv.ID)
	}
}

// claimQARun / releaseQARun guard one in-process agent loop per conversation.
func (s *Server) claimQARun(conversationID string) bool {
	s.qaRunMu.Lock()
	defer s.qaRunMu.Unlock()
	if s.qaRuns == nil {
		s.qaRuns = map[string]bool{}
	}
	if s.qaRuns[conversationID] {
		return false
	}
	s.qaRuns[conversationID] = true
	return true
}

func (s *Server) releaseQARun(conversationID string) {
	s.qaRunMu.Lock()
	defer s.qaRunMu.Unlock()
	delete(s.qaRuns, conversationID)
}

func (s *Server) qaRunIsActive(conversationID string) bool {
	s.qaRunMu.Lock()
	defer s.qaRunMu.Unlock()
	return s.qaRuns[conversationID]
}

// --- retriever ---

// qaRetriever implements qa.Retriever over the server's stores, scoped to
// one owner so every lookup is ownership-checked.
type qaRetriever struct {
	s     *Server
	owner string
}

func (r *qaRetriever) SearchSummaries(ctx context.Context, discussionID, query string, limit int) ([]qa.SummaryInfo, error) {
	results, err := r.s.d.Discussions.SearchSummaryDocuments(ctx, r.owner, discussionID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]qa.SummaryInfo, 0, len(results))
	for _, result := range results {
		out = append(out, qa.SummaryInfo{
			DiscussionID: result.DiscussionID,
			PodcastTitle: result.Title,
			Markdown:     result.Markdown,
		})
	}
	return out, nil
}

func (r *qaRetriever) SearchContent(ctx context.Context, discussionID, query string, limit int) ([]qa.ContentHit, error) {
	if !r.s.SemanticSearchEnabled(ctx) {
		return nil, errors.New("semantic search is unavailable")
	}
	vec, err := r.s.embedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	model := r.s.resolvedEmbeddingModel(ctx)
	var hits []ChunkHit
	if discussionID != "" {
		hits, err = r.s.d.Embeddings.SearchDiscussion(ctx, r.owner, discussionID, vec, model, limit)
	} else {
		hits, err = r.s.d.Embeddings.SearchGlobal(ctx, r.owner, vec, model, limit)
	}
	if err != nil {
		return nil, err
	}
	titles := r.podcastTitles(ctx, hits)
	out := make([]qa.ContentHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, qa.ContentHit{
			DiscussionID: h.DiscussionID,
			PodcastTitle: titles[h.DiscussionID],
			Kind:         h.Kind,
			Text:         h.Text,
			Similarity:   h.Similarity,
			StartMS:      h.Meta.StartMS,
			EndMS:        h.Meta.EndMS,
			Speakers:     h.Meta.Speakers,
			SourceURL:    h.Meta.SourceURL,
			SourceTitle:  h.Meta.SourceTitle,
		})
	}
	return out, nil
}

func (r *qaRetriever) podcastTitles(ctx context.Context, hits []ChunkHit) map[string]string {
	ids := make([]string, 0, len(hits))
	seen := map[string]bool{}
	for _, h := range hits {
		if !seen[h.DiscussionID] {
			seen[h.DiscussionID] = true
			ids = append(ids, h.DiscussionID)
		}
	}
	titles := map[string]string{}
	if list, err := r.s.d.Discussions.ListByIDs(ctx, r.owner, ids); err == nil {
		for _, d := range list {
			titles[d.ID] = d.Title
		}
	}
	return titles
}

func (r *qaRetriever) SearchPodcasts(ctx context.Context, query string, limit int) ([]qa.PodcastInfo, error) {
	items, err := r.s.d.Discussions.Search(ctx, r.owner, query, limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]qa.PodcastInfo, 0, len(items))
	for i := range items {
		r.s.refreshDiscussionCoverURL(ctx, &items[i])
		out = append(out, qaPodcastInfo(&items[i]))
	}
	return out, nil
}

func (r *qaRetriever) GetPodcast(ctx context.Context, discussionID string) (*qa.PodcastInfo, error) {
	items, err := r.GetPodcasts(ctx, []string{discussionID})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func (r *qaRetriever) GetPodcasts(ctx context.Context, discussionIDs []string) ([]qa.PodcastInfo, error) {
	items, err := r.s.d.Discussions.ListByIDs(ctx, r.owner, discussionIDs)
	if err != nil {
		return nil, err
	}
	out := make([]qa.PodcastInfo, 0, len(items))
	for i := range items {
		r.s.refreshDiscussionCoverURL(ctx, &items[i])
		out = append(out, qaPodcastInfo(&items[i]))
	}
	return out, nil
}

func (r *qaRetriever) GetSources(ctx context.Context, discussionID string) ([]qa.SourceInfo, error) {
	d, err := r.s.d.Discussions.Get(ctx, r.owner, discussionID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errors.New("podcast not found")
	}
	out := make([]qa.SourceInfo, 0, len(d.Sources))
	for _, src := range d.Sources {
		out = append(out, qa.SourceInfo{Title: src.Title, URL: src.URL, Snippet: src.Snippet})
	}
	return out, nil
}

// transcriptCardMaxLines bounds a show_transcript card so a huge range stays
// renderable (and the tool result stays small).
const transcriptCardMaxLines = 60

func (r *qaRetriever) TranscriptRange(ctx context.Context, discussionID string, startMS, endMS int64) (*qa.TranscriptSlice, error) {
	lines, err := r.s.d.Discussions.Lines(ctx, r.owner, discussionID)
	if err != nil {
		return nil, err
	}
	title := ""
	if items, err := r.s.d.Discussions.ListByIDs(ctx, r.owner, []string{discussionID}); err == nil && len(items) > 0 {
		title = items[0].Title
	}
	slice := &qa.TranscriptSlice{DiscussionID: discussionID, Title: title, StartMS: startMS, EndMS: endMS}
	for _, l := range lines {
		if l.StartMS < startMS || l.StartMS > endMS || strings.TrimSpace(l.Text) == "" {
			continue
		}
		slice.Lines = append(slice.Lines, qa.TranscriptLine{Speaker: l.Speaker, Text: l.Text, StartMS: l.StartMS})
		if len(slice.Lines) >= transcriptCardMaxLines {
			break
		}
	}
	return slice, nil
}

func (r *qaRetriever) GetDocument(ctx context.Context, discussionID, documentType string) (*qa.DocumentInfo, error) {
	d, err := r.s.d.Discussions.Get(ctx, r.owner, discussionID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errors.New("podcast not found")
	}
	doc, err := r.s.d.Discussions.SummaryDocumentFor(ctx, discussionID, documentType)
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.Status != SummaryReadyState {
		return nil, nil
	}
	return &qa.DocumentInfo{DiscussionID: d.ID, Title: d.Title}, nil
}

func (r *qaRetriever) CreateAgentDocument(ctx context.Context, discussionID, conversationID,
	toolCallID, title, markdown string) (*qa.AgentDocumentInfo, error) {
	if r.s.d.AgentDocuments == nil {
		return nil, errors.New("agent documents are unavailable")
	}
	var linked *string
	if discussionID = strings.TrimSpace(discussionID); discussionID != "" {
		// A global-chat model cannot attach a document to another user's or a
		// merely-public podcast. Document linkage is always owner-scoped.
		d, err := r.s.d.Discussions.Get(ctx, r.owner, discussionID)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, errors.New("podcast not found")
		}
		linked = &d.ID
	}
	doc, err := r.s.d.AgentDocuments.Create(ctx, r.owner, linked, conversationID,
		toolCallID, title, markdown)
	if err != nil {
		return nil, err
	}
	info := &qa.AgentDocumentInfo{ID: doc.ID, Title: doc.Title, PodcastTitle: doc.PodcastTitle}
	if doc.DiscussionID != nil {
		info.DiscussionID = *doc.DiscussionID
	}
	return info, nil
}

func qaPodcastInfo(d *Discussion) qa.PodcastInfo {
	info := qa.PodcastInfo{
		ID:              d.ID,
		Title:           d.Title,
		Topic:           d.Topic,
		Status:          string(d.Status),
		Language:        d.Language,
		DurationSeconds: d.DurationSeconds,
		CreatedAt:       d.CreatedAt,
	}
	if d.Cover.Valid() {
		info.Cover = &qa.CoverInfo{
			Type:          d.Cover.Type,
			ImageURL:      d.Cover.ImageURL,
			GradientStart: d.Cover.GradientStart,
			GradientEnd:   d.Cover.GradientEnd,
		}
	}
	return info
}
