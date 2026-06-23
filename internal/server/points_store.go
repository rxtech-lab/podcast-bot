package server

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// Ledger reasons. Debits are negative deltas; credits are positive.
const (
	pointsReasonPlanning   = "planning"
	pointsReasonGeneration = "generation"
	pointsReasonSignup     = "signup_grant"
	pointsReasonPurchase   = "purchase" // suffixed with the event type, e.g. "purchase:RENEWAL"
)

// PointsUsageDetail is the per-event usage snapshot stored alongside a debit so
// the full detailed usage stays in the DB even though only the points total is
// shown to the user.
type PointsUsageDetail struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	LLMCostUSD       float64
	LLMCostKnown     bool
	TTSCostUSD       float64
	MusicCostUSD     float64
	// CostUSD is the real provider cost the charge was derived from.
	CostUSD float64
}

// pointsForCost converts a real provider cost (USD) to points using the
// configured markup rate, rounding up so a non-zero cost is never free.
func pointsForCost(env *config.Env, costUSD float64) int64 {
	if env == nil || costUSD <= 0 || env.PointsPerUSDCost <= 0 {
		return 0
	}
	return int64(math.Ceil(costUSD * env.PointsPerUSDCost))
}

// pointsForUSD converts an arbitrary USD figure to points at the same rate,
// rounding up. Used for the pre-flight balance gates (estimated cost → points).
func pointsForUSD(env *config.Env, usd float64) int64 {
	return pointsForCost(env, usd)
}

// PointsStore owns the per-user points balance and the append-only ledger that
// records every charge and credit. It shares the discussion database handle so
// a debit can decrement the balance, append the ledger row, and bump the
// discussion's running points total in a single transaction.
//
// SQLite/libSQL runs with a single open connection (SetMaxOpenConns(1)), so
// writes are already serialized; every mutation here is additionally wrapped in
// a transaction and made conditional so a balance can never go negative and a
// charge can never be silently lost or double-applied.
type PointsStore struct {
	db *sql.DB
}

// NewPointsStore builds a PointsStore over the discussion store's database so
// the two share one connection and can transact across both tables. Returns nil
// when the discussion store is nil (points disabled).
func NewPointsStore(ds *DiscussionStore) (*PointsStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("points store requires a discussion store")
	}
	s := &PointsStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PointsStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS user_points_balance (
			user_id TEXT PRIMARY KEY,
			balance INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS points_ledger (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			discussion_id TEXT NOT NULL DEFAULT '',
			delta INTEGER NOT NULL,
			reason TEXT NOT NULL,
			cost_usd REAL NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			llm_cost_usd REAL NOT NULL DEFAULT 0,
			tts_cost_usd REAL NOT NULL DEFAULT 0,
			music_cost_usd REAL NOT NULL DEFAULT 0,
			rc_event_id TEXT NOT NULL DEFAULT '',
			balance_after INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS points_ledger_user_idx ON points_ledger(user_id, id)`,
		// One generation charge per discussion: the completion handler is
		// lazy-on-GET and may fire repeatedly, so the debit must apply once.
		`CREATE UNIQUE INDEX IF NOT EXISTS points_ledger_generation_uniq
			ON points_ledger(discussion_id) WHERE reason = 'generation'`,
		// One credit per RevenueCat event id (idempotent webhook deliveries).
		`CREATE UNIQUE INDEX IF NOT EXISTS points_ledger_rc_event_uniq
			ON points_ledger(rc_event_id) WHERE rc_event_id <> ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// Balance returns the user's current points balance (0 when no row exists).
func (s *PointsStore) Balance(ctx context.Context, userID string) (int64, error) {
	if s == nil {
		return 0, errors.New("points store is not configured")
	}
	var bal int64
	err := s.db.QueryRowContext(ctx, `SELECT balance FROM user_points_balance WHERE user_id = ?`, userID).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return bal, err
}

// DiscussionPoints returns the running points total charged to a discussion.
func (s *PointsStore) DiscussionPoints(ctx context.Context, id string) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var pts int64
	err := s.db.QueryRowContext(ctx, `SELECT points_charged FROM native_discussions WHERE id = ?`, id).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return pts, err
}

// PointsLedgerEntry is a user-facing ledger row for the points-history view:
// the signed balance change, the running balance after it, the reason, and when
// it happened. Token/cost detail is intentionally omitted — only points are
// surfaced to users.
type PointsLedgerEntry struct {
	ID           int64  `json:"id"`
	Delta        int64  `json:"delta"`
	Reason       string `json:"reason"`
	BalanceAfter int64  `json:"balance_after"`
	CreatedAt    int64  `json:"created_at"` // unix milliseconds
}

// History returns the user's most recent ledger entries, newest first, for the
// points-usage history view (which plots balance_after over time).
func (s *PointsStore) History(ctx context.Context, userID string, limit int) ([]PointsLedgerEntry, error) {
	if s == nil {
		return nil, errors.New("points store is not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, delta, reason, balance_after, created_at
		FROM points_ledger WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PointsLedgerEntry, 0, limit)
	for rows.Next() {
		var e PointsLedgerEntry
		if err := rows.Scan(&e.ID, &e.Delta, &e.Reason, &e.BalanceAfter, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnsureSignupGrant credits the configured starter balance the first time it is
// called for a user. Idempotent: keyed on a synthetic event id so repeated calls
// never double-grant. No-op when the grant is zero.
func (s *PointsStore) EnsureSignupGrant(ctx context.Context, userID string, grant int64) error {
	if s == nil || grant <= 0 {
		return nil
	}
	_, err := s.credit(ctx, userID, grant, pointsReasonSignup, "signup:"+userID)
	return err
}

// Credit adds points to a user's balance and appends a ledger row. When
// rcEventID is non-empty the credit is idempotent: a second call with the same
// id is a no-op (returns the unchanged balance).
func (s *PointsStore) Credit(ctx context.Context, userID string, points int64, reason, rcEventID string) (int64, error) {
	return s.credit(ctx, userID, points, reason, rcEventID)
}

func (s *PointsStore) credit(ctx context.Context, userID string, points int64, reason, rcEventID string) (int64, error) {
	if s == nil {
		return 0, errors.New("points store is not configured")
	}
	if points <= 0 {
		return s.Balance(ctx, userID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if rcEventID != "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM points_ledger WHERE rc_event_id = ?`, rcEventID).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			// Already processed — return the current balance unchanged. Read via
			// the open tx: a fresh DB query would deadlock since the pool holds a
			// single connection (SetMaxOpenConns(1)) already taken by this tx.
			return txBalance(ctx, tx, userID)
		}
	}

	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return 0, err
	}
	newBal := bal + points
	now := time.Now().UnixMilli()
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return 0, err
	}
	if err := insertLedger(ctx, tx, ledgerRow{
		userID:      userID,
		delta:       points,
		reason:      reason,
		rcEventID:   rcEventID,
		balanceAfter: newBal,
		createdAt:   now,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newBal, nil
}

// Reserve atomically holds `points` from the user's balance BEFORE chargeable
// work begins. It is strictly conditional: when the balance can't cover the
// reservation it makes no change and returns ok=false, so the caller can reject
// the request (402) before doing any work. This is the concurrency-safe gate —
// two simultaneous requests can never both pass, so the balance can't be
// overdrawn. For generation the reserved amount is also stored on the discussion
// (points_reserved) so the completion path, which runs in a separate goroutine,
// can reconcile against it.
func (s *PointsStore) Reserve(ctx context.Context, userID, discussionID string, points int64, kind string) (bool, int64, error) {
	if s == nil {
		return false, 0, errors.New("points store is not configured")
	}
	if points <= 0 {
		bal, err := s.Balance(ctx, userID)
		return true, bal, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()

	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return false, 0, err
	}
	if bal < points {
		// Insufficient — make no change so no work is started uncharged.
		return false, bal, nil
	}
	newBal := bal - points
	now := time.Now().UnixMilli()
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return false, 0, err
	}
	if err := insertLedger(ctx, tx, ledgerRow{
		userID:       userID,
		discussionID: discussionID,
		delta:        -points,
		reason:       "reserve:" + kind,
		balanceAfter: newBal,
		createdAt:    now,
	}); err != nil {
		return false, 0, err
	}
	if kind == pointsReasonGeneration && discussionID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_reserved = ?, updated_at = ? WHERE id = ?`,
			points, now, discussionID); err != nil {
			return false, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return true, newBal, nil
}

// settle reconciles a reservation against the actual cost within an open tx: it
// adjusts the balance by (reserved - actual) — refunding an over-reservation or
// charging the remainder — records a settlement ledger row, and adds the points
// ACTUALLY debited to the discussion's points_charged. The balance is clamped at
// zero, and points_charged is capped to the debited amount so a podcast can
// never display/store more points than were really taken (the invariant
// points_charged == sum of debits holds even when actual exceeds the balance).
func (s *PointsStore) settle(ctx context.Context, tx *sql.Tx, userID, discussionID string, reserved, actual int64, kind string, detail PointsUsageDetail, now int64) (int64, error) {
	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return 0, err
	}
	newBal := bal + (reserved - actual)
	if newBal < 0 {
		newBal = 0
	}
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return 0, err
	}
	if err := insertLedger(ctx, tx, ledgerRow{
		userID:       userID,
		discussionID: discussionID,
		delta:        newBal - bal,
		reason:       kind,
		detail:       detail,
		balanceAfter: newBal,
		createdAt:    now,
	}); err != nil {
		return 0, err
	}
	// Points truly removed across reserve+settle = (balance before reserve) - newBal
	// = (bal + reserved) - newBal. Without a clamp this equals `actual`; with a
	// clamp it's lower, so charging the raw `actual` would over-report.
	debited := bal + reserved - newBal
	if discussionID != "" && debited > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_charged = points_charged + ?, updated_at = ? WHERE id = ?`,
			debited, now, discussionID); err != nil {
			return 0, err
		}
	}
	return newBal, nil
}

// SettlePlanning reconciles a planning reservation synchronously (called once, in
// the same request/goroutine that reserved). Pass actual=0 to fully refund the
// reservation when the planning work failed.
func (s *PointsStore) SettlePlanning(ctx context.Context, userID, discussionID string, reserved, actual int64, detail PointsUsageDetail) (int64, error) {
	if s == nil {
		return 0, errors.New("points store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	newBal, err := s.settle(ctx, tx, userID, discussionID, reserved, actual, pointsReasonPlanning, detail, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newBal, nil
}

// SettleGeneration reconciles the generation reservation for a discussion. It is
// idempotent (safe to call from both the job-completion path and the lazy
// discussion-fetch path) and looks up the owner and reserved amount from the
// discussion row, so callers need only the discussion id and the actual cost.
func (s *PointsStore) SettleGeneration(ctx context.Context, discussionID string, actual int64, detail PointsUsageDetail) error {
	if s == nil || discussionID == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// One generation settlement per discussion.
	var settled int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM points_ledger WHERE discussion_id = ? AND reason = ?`, discussionID, pointsReasonGeneration).Scan(&settled); err != nil {
		return err
	}
	if settled > 0 {
		return nil
	}
	var owner string
	var reserved int64
	err = tx.QueryRowContext(ctx, `SELECT owner_user_id, points_reserved FROM native_discussions WHERE id = ?`, discussionID).Scan(&owner, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if _, err := s.settle(ctx, tx, owner, discussionID, reserved, actual, pointsReasonGeneration, detail, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_reserved = 0, updated_at = ? WHERE id = ?`, now, discussionID); err != nil {
		return err
	}
	return tx.Commit()
}

// GenerationPoints converts a finished run's real cost into the points to
// charge, applying the per-podcast minimum so a podcast is never free.
func (s *PointsStore) GenerationPoints(env *config.Env, costUSD float64) int64 {
	pts := pointsForCost(env, costUSD)
	if env != nil && pts < env.PointsMinPerPodcast {
		pts = env.PointsMinPerPodcast
	}
	return pts
}

// ChargeGeneration reconciles a generation reservation from a finished run's
// usage. Computes the points (with the per-podcast minimum) and settles
// idempotently. Safe to call from both the job-completion path and the lazy
// discussion-fetch path.
func (s *PointsStore) ChargeGeneration(ctx context.Context, env *config.Env, discussionID string, detail PointsUsageDetail) error {
	if s == nil {
		return nil
	}
	return s.SettleGeneration(ctx, discussionID, s.GenerationPoints(env, detail.CostUSD), detail)
}

// Refund credits points back to a user (e.g. a reservation whose work never
// started) and, for a generation refund, clears the discussion's held reserve.
func (s *PointsStore) Refund(ctx context.Context, userID, discussionID string, points int64, kind string) error {
	if s == nil || points <= 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return err
	}
	newBal := bal + points
	now := time.Now().UnixMilli()
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return err
	}
	if err := insertLedger(ctx, tx, ledgerRow{
		userID:       userID,
		discussionID: discussionID,
		delta:        points,
		reason:       "refund:" + kind,
		balanceAfter: newBal,
		createdAt:    now,
	}); err != nil {
		return err
	}
	if kind == pointsReasonGeneration && discussionID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_reserved = 0, updated_at = ? WHERE id = ?`, now, discussionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type ledgerRow struct {
	userID       string
	discussionID string
	delta        int64
	reason       string
	detail       PointsUsageDetail
	rcEventID    string
	balanceAfter int64
	createdAt    int64
}

func insertLedger(ctx context.Context, tx *sql.Tx, row ledgerRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO points_ledger
		(user_id, discussion_id, delta, reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
		 llm_cost_usd, tts_cost_usd, music_cost_usd, rc_event_id, balance_after, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.userID, row.discussionID, row.delta, row.reason, row.detail.CostUSD,
		row.detail.PromptTokens, row.detail.CompletionTokens, row.detail.TotalTokens,
		row.detail.LLMCostUSD, row.detail.TTSCostUSD, row.detail.MusicCostUSD,
		row.rcEventID, row.balanceAfter, row.createdAt)
	return err
}

func txBalance(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	var bal int64
	err := tx.QueryRowContext(ctx, `SELECT balance FROM user_points_balance WHERE user_id = ?`, userID).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return bal, err
}

func upsertBalance(ctx context.Context, tx *sql.Tx, userID string, balance, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO user_points_balance (user_id, balance, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET balance = excluded.balance, updated_at = excluded.updated_at`,
		userID, balance, now)
	return err
}
