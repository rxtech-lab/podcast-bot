package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// pointsEnabled reports whether the points economy is wired (store + env). When
// false, every gate/charge/sanitize below is a no-op so non-points deployments
// (tests, dashboard-only) keep their previous behaviour.
func (s *Server) pointsEnabled() bool {
	return s.d.Points != nil && s.d.Env != nil
}

// usageAccumulator sums the LLM usage of a single planning round so it can be
// converted to points once the round finishes. Safe for concurrent use; the
// planner records on its own goroutine.
type usageAccumulator struct {
	mu  sync.Mutex
	sum llm.UsageSummary
}

func (a *usageAccumulator) record(u llm.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sum.PromptTokens += u.PromptTokens
	a.sum.CompletionTokens += u.CompletionTokens
	a.sum.TotalTokens += u.TotalTokens
	if u.CostKnown {
		a.sum.CostUSD += u.CostUSD
		a.sum.CostKnown = true
	}
}

func (a *usageAccumulator) snapshot() llm.UsageSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sum
}

// userBalance returns the user's current balance, first crediting the optional
// signup grant. Returns 0 with no error when points is disabled.
func (s *Server) userBalance(ctx context.Context, userID string) (int64, error) {
	if !s.pointsEnabled() {
		return 0, nil
	}
	if err := s.d.Points.EnsureUser(ctx, userID); err != nil {
		return 0, err
	}
	if g := s.d.Env.PointsSignupGrant; g > 0 {
		_ = s.d.Points.EnsureSignupGrant(ctx, userID, g)
	}
	return s.d.Points.Balance(ctx, userID)
}

// pointsCharged returns the running points total for a discussion (0 when
// points is disabled), used to reflect a fresh charge in the response.
func (s *Server) pointsCharged(ctx context.Context, id string) (int64, error) {
	if !s.pointsEnabled() {
		return 0, nil
	}
	return s.d.Points.DiscussionPoints(ctx, id)
}

// generationEstimatePoints is the points a full podcast of the script's target
// duration is expected to cost — the amount required up front so a run can never
// deplete mid-generation.
func generationEstimatePoints(env *config.Env, script *config.DebateTopic) int64 {
	minutes := 30.0
	if script != nil && script.TotalMinutes > 0 {
		minutes = float64(script.TotalMinutes)
	}
	est := pointsForUSD(env, minutes*env.PointsEstCostPerMinuteUSD)
	if est < env.PointsMinPerPodcast {
		est = env.PointsMinPerPodcast
	}
	return est
}

// reserveGeneration atomically holds the estimated full-podcast cost before the
// job is submitted, so a run never starts uncharged and concurrent requests
// can't overdraw. Returns the reserved amount and ok; writes 402 and returns
// ok=false when the balance is short. Reserved 0 / ok=true when points is off.
func (s *Server) reserveGeneration(w http.ResponseWriter, r *http.Request, userID, discID string, script *config.DebateTopic) (int64, bool) {
	if !s.pointsEnabled() {
		return 0, true
	}
	required := generationEstimatePoints(s.d.Env, script)
	ok, bal, err := s.d.Points.Reserve(r.Context(), userID, discID, required, pointsReasonGeneration)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, false
	}
	if !ok {
		writeInsufficientPoints(w, required, bal)
		return 0, false
	}
	return required, true
}

// reservePlanning atomically holds a small estimate before a planning / improve
// / add-sources round runs (so planning is never free and the gate is
// concurrency-safe). Returns the reserved amount, reserve ledger id, and ok;
// writes 402 when short.
// The caller MUST later settlePlanning (on success) or refundPlanning (on
// failure) to reconcile/release the hold.
func (s *Server) reservePlanning(w http.ResponseWriter, r *http.Request, userID, discID string) (int64, int64, bool) {
	if !s.pointsEnabled() {
		return 0, 0, true
	}
	required := pointsForUSD(s.d.Env, s.d.Env.PointsPlanGateUSD)
	if required <= 0 {
		return 0, 0, true
	}
	ok, bal, reserveLedgerID, err := s.d.Points.ReserveWithLedgerID(r.Context(), userID, discID, required, pointsReasonPlanning)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, 0, false
	}
	if !ok {
		writeInsufficientPoints(w, required, bal)
		return 0, 0, false
	}
	return required, reserveLedgerID, true
}

// settlePlanning reconciles a planning reservation against the round's actual
// LLM usage, refunding the unused remainder and adding the actual to the
// discussion's points total. Best-effort: failures are logged, not surfaced.
func (s *Server) settlePlanning(ctx context.Context, userID, discID string, reserved, reserveLedgerID int64, acc *usageAccumulator) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	sum := acc.snapshot()
	actual := pointsForCost(s.d.Env, sum.CostUSD)
	detail := PointsUsageDetail{
		PromptTokens:     sum.PromptTokens,
		CompletionTokens: sum.CompletionTokens,
		TotalTokens:      sum.TotalTokens,
		LLMCostUSD:       sum.CostUSD,
		LLMCostKnown:     sum.CostKnown,
		CostUSD:          sum.CostUSD,
	}
	if _, err := s.d.Points.SettlePlanning(ctx, userID, discID, reserveLedgerID, reserved, actual, detail); err != nil {
		s.logger().Warn("planning settle failed", "discussion", discID, "err", err)
	}
}

// settleFlatPlanning charges the full reserved amount for a planning-class action
// that has no itemised LLM meter (e.g. Firecrawl source search, billed as a flat
// fee). No refund — the reserved fee is the price of the call.
func (s *Server) settleFlatPlanning(ctx context.Context, userID, discID string, reserved, reserveLedgerID int64) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	if _, err := s.d.Points.SettlePlanning(ctx, userID, discID, reserveLedgerID, reserved, reserved, PointsUsageDetail{}); err != nil {
		s.logger().Warn("flat planning settle failed", "discussion", discID, "err", err)
	}
}

// refundPlanning releases a planning reservation in full when the round failed
// before producing chargeable work.
func (s *Server) refundPlanning(ctx context.Context, userID, discID string, reserved, reserveLedgerID int64) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	if _, err := s.d.Points.SettlePlanning(ctx, userID, discID, reserveLedgerID, reserved, 0, PointsUsageDetail{}); err != nil {
		s.logger().Warn("planning refund failed", "discussion", discID, "err", err)
	}
}

// refundGeneration releases a generation reservation when the job never started.
func (s *Server) refundGeneration(ctx context.Context, userID, discID string, reserved int64) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	if err := s.d.Points.Refund(ctx, userID, discID, reserved, pointsReasonGeneration); err != nil {
		s.logger().Warn("generation refund failed", "discussion", discID, "err", err)
	}
}

// sanitizeJobUsage strips a job snapshot's detailed token/cost figures and the
// "usage" log line before it is returned to a client, so the old USD usage
// summary can never leak through the job status path. No-op when points is off.
func (s *Server) sanitizeJobUsage(j *Job) {
	if j == nil || !s.pointsEnabled() {
		return
	}
	j.PromptTokens, j.CompletionTokens, j.TotalTokens = 0, 0, 0
	j.LLMCostUSD, j.LLMCostKnown = 0, false
	j.TTSCostUSD, j.MusicCostUSD = 0, 0
	if len(j.Logs) > 0 {
		filtered := make([]JobLog, 0, len(j.Logs))
		for _, lg := range j.Logs {
			if lg.Kind == "usage" {
				continue
			}
			filtered = append(filtered, lg)
		}
		j.Logs = filtered
	}
}

// sanitizeDiscussionUsage hides the detailed token/cost breakdown from clients,
// leaving only PointsCharged. No-op when points is disabled.
func (s *Server) sanitizeDiscussionUsage(d *Discussion) {
	if d == nil || !s.pointsEnabled() {
		return
	}
	d.PromptTokens, d.CompletionTokens, d.TotalTokens = 0, 0, 0
	d.LLMCostUSD, d.LLMCostKnown = 0, false
	d.TTSCostUSD, d.MusicCostUSD = 0, 0
}

func writeInsufficientPoints(w http.ResponseWriter, required, balance int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":           "insufficient_points",
		"required_points": required,
		"balance":         balance,
	})
}

// handlePointsBalance returns the signed-in user's current points balance.
func (s *Server) handlePointsBalance(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	bal, err := s.userBalance(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"balance": bal})
}

// handlePointsHistory returns the current balance plus the user's recent ledger
// entries (newest first) for the points-usage history view.
func (s *Server) handlePointsHistory(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	bal, err := s.userBalance(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries := []PointsLedgerEntry{}
	hasMore := false
	if s.pointsEnabled() {
		limit := atoiDefault(r.URL.Query().Get("limit"), 0)
		offset := atoiDefault(r.URL.Query().Get("offset"), 0)
		page, err := s.d.Points.History(r.Context(), user.ID, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entries = page.Entries
		hasMore = page.HasMore
	}
	writeJSON(w, map[string]any{"balance": bal, "entries": entries, "has_more": hasMore})
}
