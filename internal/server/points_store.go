package server

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// Ledger reasons. Debits are negative deltas; credits are positive.
const (
	pointsReasonPlanning             = "planning"
	pointsReasonGeneration           = "generation"
	pointsReasonGenerationAdjustment = "generation_adjustment"
	pointsReasonImageGeneration      = "image_generation"
	pointsReasonSummary              = "summary"
	pointsReasonSignup               = "signup_grant"
	pointsReasonPurchase             = "purchase" // suffixed with the event type, e.g. "purchase:RENEWAL"
	pointsReasonAdminTopup           = "admin_topup"
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
	db *sqlDB
}

type DailyUsageSpend struct {
	Date             string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	LLMCostUSD       float64
	TTSCostUSD       float64
	ImageCostUSD     float64
	MusicCostUSD     float64
	OtherCostUSD     float64
	TotalCostUSD     float64
}

type UsageSpendSummary struct {
	Days []DailyUsageSpend
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
	if _, _, err := s.RepairLegacyGenerationOvercharges(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// UsageSpendByDate returns private admin-only usage details grouped by UTC day.
// User-facing APIs intentionally avoid these raw token and cost fields.
func (s *PointsStore) UsageSpendByDate(ctx context.Context, days int) (UsageSpendSummary, error) {
	if s == nil {
		return UsageSpendSummary{}, errors.New("points store is not configured")
	}
	if days <= 0 {
		days = 14
	}
	if days > 90 {
		days = 90
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	rows, err := s.db.QueryContext(ctx, `SELECT reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
			llm_cost_usd, tts_cost_usd, music_cost_usd, created_at
		FROM points_ledger
		WHERE created_at >= ?
		ORDER BY created_at ASC`, start.UnixMilli())
	if err != nil {
		return UsageSpendSummary{}, err
	}
	defer rows.Close()

	byDate := make(map[string]*DailyUsageSpend, days)
	ordered := make([]string, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		ordered = append(ordered, date)
		byDate[date] = &DailyUsageSpend{Date: date}
	}

	for rows.Next() {
		var reason string
		var costUSD, llmCostUSD, ttsCostUSD, musicCostUSD float64
		var promptTokens, completionTokens, totalTokens int64
		var createdAt int64
		if err := rows.Scan(&reason, &costUSD, &promptTokens, &completionTokens, &totalTokens, &llmCostUSD, &ttsCostUSD, &musicCostUSD, &createdAt); err != nil {
			return UsageSpendSummary{}, err
		}
		date := time.UnixMilli(createdAt).UTC().Format("2006-01-02")
		day := byDate[date]
		if day == nil {
			continue
		}
		day.PromptTokens += promptTokens
		day.CompletionTokens += completionTokens
		day.TotalTokens += totalTokens
		day.LLMCostUSD += llmCostUSD
		day.TTSCostUSD += ttsCostUSD
		day.MusicCostUSD += musicCostUSD
		day.TotalCostUSD += costUSD
		if reason == pointsReasonImageGeneration {
			day.ImageCostUSD += costUSD
		}
		knownCost := llmCostUSD + ttsCostUSD + musicCostUSD + dayCostForReason(reason, costUSD, pointsReasonImageGeneration)
		if other := costUSD - knownCost; other > 0 {
			day.OtherCostUSD += other
		}
	}
	if err := rows.Err(); err != nil {
		return UsageSpendSummary{}, err
	}

	out := UsageSpendSummary{Days: make([]DailyUsageSpend, 0, len(ordered))}
	for _, date := range ordered {
		out.Days = append(out.Days, *byDate[date])
	}
	return out, nil
}

func dayCostForReason(reason string, costUSD float64, target string) float64 {
	if reason == target {
		return costUSD
	}
	return 0
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
		`CREATE INDEX IF NOT EXISTS points_ledger_history_match_idx
			ON points_ledger(user_id, reason, discussion_id, id)`,
		`CREATE TABLE IF NOT EXISTS user_subscriptions (
			user_id TEXT PRIMARY KEY,
			product_id TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			store_environment TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			subscription_period TEXT NOT NULL DEFAULT '',
			expires_at INTEGER NOT NULL DEFAULT 0,
			last_event_id TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
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

// UserBalance is one row of the admin user-management list: a user id, their
// optional creator display name, and their current points balance.
type UserBalance struct {
	UserID             string `json:"user_id"`
	DisplayName        string `json:"display_name"`
	Balance            int64  `json:"balance"`
	SubscriptionPlan   string `json:"subscription_plan"`
	SubscriptionStatus string `json:"subscription_status"`
}

// ListBalances returns a cursor-paginated page of user balances ordered by user
// id. Pass the last user id of the previous page as after (empty for the first
// page). It left-joins creator_profiles for a friendly display name. The
// returned nextCursor is empty when the page is the last one.
func (s *PointsStore) ListBalances(ctx context.Context, after string, limit int) (rows []UserBalance, nextCursor string, err error) {
	if s == nil {
		return nil, "", errors.New("points store is not configured")
	}
	if limit <= 0 {
		limit = 20
	}
	// Fetch one extra row to detect whether another page follows.
	result, err := s.db.QueryContext(ctx, `SELECT b.user_id, COALESCE(p.display_name, ''), b.balance,
			COALESCE(NULLIF(us.display_name, ''), us.product_id, '') AS subscription_plan,
			COALESCE(us.status, '') AS subscription_status
		FROM user_points_balance b
		LEFT JOIN creator_profiles p ON p.user_id = b.user_id
		LEFT JOIN user_subscriptions us ON us.user_id = b.user_id
		WHERE (? = '' OR b.user_id > ?)
		ORDER BY b.user_id ASC
		LIMIT ?`, after, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer result.Close()
	for result.Next() {
		var r UserBalance
		if err := result.Scan(&r.UserID, &r.DisplayName, &r.Balance, &r.SubscriptionPlan, &r.SubscriptionStatus); err != nil {
			return nil, "", err
		}
		rows = append(rows, r)
	}
	if err := result.Err(); err != nil {
		return nil, "", err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		nextCursor = rows[len(rows)-1].UserID
	}
	return rows, nextCursor, nil
}

// Balance returns the user's current points balance (0 when no row exists).
func (s *PointsStore) Balance(ctx context.Context, userID string) (int64, error) {
	if s == nil {
		return 0, errors.New("points store is not configured")
	}
	var bal int64
	err := s.db.QueryRowContext(ctx, `SELECT balance FROM user_points_balance WHERE user_id = ?`, userID).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		bal = 0
	} else if err != nil {
		return 0, err
	}
	ledgerBal, ok, err := s.latestLedgerBalance(ctx, userID)
	if err != nil {
		return 0, err
	}
	if !ok || ledgerBal == bal {
		return bal, nil
	}
	if err := s.repairBalance(ctx, userID, ledgerBal); err != nil {
		return 0, err
	}
	return ledgerBal, nil
}

func (s *PointsStore) latestLedgerBalance(ctx context.Context, userID string) (int64, bool, error) {
	var bal int64
	err := s.db.QueryRowContext(ctx, `SELECT balance_after FROM points_ledger WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return bal, true, nil
}

func (s *PointsStore) repairBalance(ctx context.Context, userID string, balance int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_points_balance (user_id, balance, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET balance = excluded.balance, updated_at = excluded.updated_at`,
		userID, balance, time.Now().UnixMilli())
	return err
}

type legacyGenerationOvercharge struct {
	discussionID  string
	userID        string
	pointsCharged int64
	debited       int64
}

// RepairLegacyGenerationOvercharges fixes rows written by older settlement code
// that debited more generation points in the ledger than the discussion's
// recorded charge. It is idempotent and preserves later ledger balances by
// shifting every subsequent balance_after by the repaired excess.
func (s *PointsStore) RepairLegacyGenerationOvercharges(ctx context.Context) (int, int64, error) {
	if s == nil {
		return 0, 0, errors.New("points store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT d.id, d.owner_user_id, d.points_charged, -COALESCE(SUM(l.delta), 0) AS debited
		FROM native_discussions d
		JOIN points_ledger l ON l.discussion_id = d.id AND l.user_id = d.owner_user_id
		WHERE d.points_charged > 0
			AND l.reason IN (?, ?, ?)
		GROUP BY d.id, d.owner_user_id, d.points_charged
		HAVING -COALESCE(SUM(l.delta), 0) > d.points_charged`,
		"reserve:"+pointsReasonGeneration, pointsReasonGeneration, pointsReasonGenerationAdjustment)
	if err != nil {
		return 0, 0, err
	}
	var repairs []legacyGenerationOvercharge
	for rows.Next() {
		var r legacyGenerationOvercharge
		if err := rows.Scan(&r.discussionID, &r.userID, &r.pointsCharged, &r.debited); err != nil {
			rows.Close()
			return 0, 0, err
		}
		repairs = append(repairs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}

	var total int64
	seenUsers := map[string]bool{}
	for _, r := range repairs {
		excess := r.debited - r.pointsCharged
		if excess <= 0 {
			continue
		}
		repaired, err := s.repairLegacyGenerationOvercharge(ctx, tx, r, excess)
		if err != nil {
			return 0, 0, err
		}
		total += repaired
		seenUsers[r.userID] = true
	}
	for userID := range seenUsers {
		if err := repairCachedBalanceFromLatestLedger(ctx, tx, userID); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(repairs), total, nil
}

func (s *PointsStore) repairLegacyGenerationOvercharge(ctx context.Context, tx *sqlTx, r legacyGenerationOvercharge, excess int64) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, delta
		FROM points_ledger
		WHERE user_id = ? AND discussion_id = ?
			AND reason IN (?, ?, ?)
			AND delta < 0
		ORDER BY id DESC`,
		r.userID, r.discussionID, "reserve:"+pointsReasonGeneration, pointsReasonGeneration, pointsReasonGenerationAdjustment)
	if err != nil {
		return 0, err
	}
	type adjustment struct {
		ledgerID int64
		points   int64
	}
	var adjustments []adjustment
	remaining := excess
	for rows.Next() {
		var ledgerID, delta int64
		if err := rows.Scan(&ledgerID, &delta); err != nil {
			rows.Close()
			return 0, err
		}
		points := -delta
		if points > remaining {
			points = remaining
		}
		adjustments = append(adjustments, adjustment{ledgerID: ledgerID, points: points})
		remaining -= points
		if remaining == 0 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if remaining != 0 {
		return 0, errors.New("legacy generation repair could not find enough debits")
	}
	var repaired int64
	for _, adj := range adjustments {
		if _, err := tx.ExecContext(ctx, `UPDATE points_ledger
			SET delta = delta + ?
			WHERE id = ?`, adj.points, adj.ledgerID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE points_ledger
			SET balance_after = balance_after + ?
			WHERE user_id = ? AND id >= ?`, adj.points, r.userID, adj.ledgerID); err != nil {
			return 0, err
		}
		repaired += adj.points
	}
	return repaired, nil
}

// EnsureUser registers a signed-in user with a zero balance if they have never
// held or spent points. This gives external purchase webhooks a local user
// existence check without granting any free balance.
func (s *PointsStore) EnsureUser(ctx context.Context, userID string) error {
	if s == nil {
		return errors.New("points store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_points_balance (user_id, balance, updated_at)
		VALUES (?, 0, ?)
		ON CONFLICT(user_id) DO NOTHING`, userID, time.Now().UnixMilli())
	return err
}

// UserExists reports whether the backend has seen this user before through the
// points ledger/balance table or an owned discussion.
func (s *PointsStore) UserExists(ctx context.Context, userID string) (bool, error) {
	if s == nil {
		return false, errors.New("points store is not configured")
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM user_points_balance WHERE user_id = ?) OR
		EXISTS(SELECT 1 FROM native_discussions WHERE owner_user_id = ?)`,
		userID, userID).Scan(&exists)
	return exists != 0, err
}

// RecordSubscription stores the latest subscription plan state visible in the
// admin users table. RevenueCat remains the source of truth; this projection is
// only the backend's last webhook-derived view.
func (s *PointsStore) RecordSubscription(ctx context.Context, userID string, product IAPProduct, status, eventID string, expiresAtMS int64) error {
	if s == nil {
		return errors.New("points store is not configured")
	}
	if product.ProductType != IAPProductTypeSubscription {
		return nil
	}
	userID = strings.TrimSpace(userID)
	status = strings.TrimSpace(status)
	if userID == "" || status == "" {
		return nil
	}
	if err := s.EnsureUser(ctx, userID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_subscriptions
		(user_id, product_id, display_name, store_environment, status, subscription_period, expires_at, last_event_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			product_id = excluded.product_id,
			display_name = excluded.display_name,
			store_environment = excluded.store_environment,
			status = excluded.status,
			subscription_period = excluded.subscription_period,
			expires_at = excluded.expires_at,
			last_event_id = excluded.last_event_id,
			updated_at = excluded.updated_at`,
		userID, product.ProductID, product.DisplayName, product.StoreEnvironment, status,
		product.SubscriptionPeriod, expiresAtMS, eventID, now)
	return err
}

// ClearSubscription removes a user's recorded subscription plan, dropping them
// back to the free class. Used by the admin users table to set a user to "No
// subscription (free)". RevenueCat remains the source of truth; a later webhook
// may re-create this projection.
func (s *PointsStore) ClearSubscription(ctx context.Context, userID string) error {
	if s == nil {
		return errors.New("points store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_subscriptions WHERE user_id = ?`, userID)
	return err
}

// UserSubscription is the backend's last webhook-derived view of a user's
// subscription plan, read from user_subscriptions. It is the join input for
// resolving a user's entitlements.
type UserSubscription struct {
	ProductID          string
	DisplayName        string
	StoreEnvironment   string
	Status             string
	SubscriptionPeriod string
	ExpiresAt          int64 // unix milliseconds
}

// Active reports whether the subscription grants access right now: status
// "active" and not past its expiry (expiry 0 means no known expiry, treated as
// active while status is active).
func (u UserSubscription) Active(nowMillis int64) bool {
	if strings.TrimSpace(u.Status) != "active" {
		return false
	}
	return u.ExpiresAt == 0 || u.ExpiresAt > nowMillis
}

// Subscription returns the user's recorded subscription, or nil when none is on
// file.
func (s *PointsStore) Subscription(ctx context.Context, userID string) (*UserSubscription, error) {
	if s == nil {
		return nil, errors.New("points store is not configured")
	}
	var u UserSubscription
	err := s.db.QueryRowContext(ctx, `SELECT product_id, display_name, store_environment, status, subscription_period, expires_at
		FROM user_subscriptions WHERE user_id = ?`, strings.TrimSpace(userID)).
		Scan(&u.ProductID, &u.DisplayName, &u.StoreEnvironment, &u.Status, &u.SubscriptionPeriod, &u.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
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
	DiscussionID string `json:"-"`
}

type PointsHistoryPage struct {
	Entries []PointsLedgerEntry
	HasMore bool
}

// History returns the user's most recent user-facing ledger entries, newest
// first, for the points-usage history view (which plots balance_after over
// time). The raw ledger keeps planning reservations and settlements separate for
// accounting; this projection collapses a settled planning hold into one
// "planning" usage row. An unsettled hold is still shown as "reserve:planning".
func (s *PointsStore) History(ctx context.Context, userID string, limit, offset int) (PointsHistoryPage, error) {
	return s.history(ctx, userID, limit, offset, nil)
}

func (s *PointsStore) history(ctx context.Context, userID string, limit, offset int, timer *pointsHistoryTimer) (PointsHistoryPage, error) {
	if s == nil {
		return PointsHistoryPage{}, errors.New("points store is not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if timer != nil {
		timer.normalizedLim = limit
		timer.normalizedOff = offset
	}
	target := offset + limit + 1
	batchSize := pointsHistoryRawBatchSize(limit)
	if timer != nil {
		timer.target = target
		timer.batchSize = batchSize
	}
	rawOffset := 0
	raw := []PointsLedgerEntry{}
	rawIDs := map[int64]bool{}
	for {
		batchStart := time.Now()
		batch, err := s.pointsLedgerBatch(ctx, userID, batchSize, rawOffset)
		timer.mark("ledgerBatch", batchStart)
		if timer != nil {
			timer.rawBatches++
			timer.rawFetched += len(batch)
		}
		if err != nil {
			return PointsHistoryPage{}, err
		}
		for _, entry := range batch {
			if rawIDs[entry.ID] {
				continue
			}
			raw = append(raw, entry)
			rawIDs[entry.ID] = true
		}
		if timer != nil {
			timer.rawUnique = len(raw)
		}
		resolveStart := time.Now()
		raw, err = s.resolvePointsHistoryWindow(ctx, userID, raw, rawIDs, timer)
		timer.mark("resolveWindow", resolveStart)
		if err != nil {
			return PointsHistoryPage{}, err
		}
		if timer != nil {
			timer.rawUnique = len(raw)
		}
		projectStart := time.Now()
		projected := projectPointsHistory(raw)
		timer.mark("project", projectStart)
		if timer != nil {
			timer.projected = len(projected)
		}
		if len(projected) >= target || len(batch) < batchSize {
			pageStart := time.Now()
			page := paginatePointsHistory(projected, limit, offset)
			timer.mark("paginate", pageStart)
			return page, nil
		}
		rawOffset += len(batch)
	}
}

func pointsHistoryRawBatchSize(displayLimit int) int {
	size := displayLimit * 2
	if size < 100 {
		return 100
	}
	if size > 1000 {
		return 1000
	}
	return size
}

func (s *PointsStore) pointsLedgerBatch(ctx context.Context, userID string, limit, offset int) ([]PointsLedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, discussion_id, delta, reason, balance_after, created_at
		FROM points_ledger WHERE user_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PointsLedgerEntry, 0, limit)
	for rows.Next() {
		var e PointsLedgerEntry
		if err := rows.Scan(&e.ID, &e.DiscussionID, &e.Delta, &e.Reason, &e.BalanceAfter, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PointsStore) resolvePointsHistoryWindow(ctx context.Context, userID string, raw []PointsLedgerEntry, seen map[int64]bool, timer *pointsHistoryTimer) ([]PointsLedgerEntry, error) {
	added := false
	lookupStart := time.Now()
	reserves, lookups, err := s.matchingReserves(ctx, userID, raw)
	timer.mark("reserveLookup", lookupStart)
	if err != nil {
		return nil, err
	}
	if timer != nil {
		timer.reserveLookups += lookups
	}
	for _, entry := range raw {
		reserve, ok := reserves[entry.ID]
		if !ok || seen[reserve.ID] {
			continue
		}
		if timer != nil {
			timer.reserveMatches++
		}
		raw = append(raw, reserve)
		seen[reserve.ID] = true
		added = true
	}
	if added {
		sort.Slice(raw, func(i, j int) bool { return raw[i].ID > raw[j].ID })
	}
	return raw, nil
}

type pointsHistoryReserveKey struct {
	settlementID        int64
	settlementReason    string
	reserveDiscussionID string
	priority            int
}

func (s *PointsStore) matchingReserves(ctx context.Context, userID string, raw []PointsLedgerEntry) (map[int64]PointsLedgerEntry, int, error) {
	keys := make([]pointsHistoryReserveKey, 0)
	settlements := 0
	for _, entry := range raw {
		if !collapsibleSettlementReason(entry.Reason) || entry.DiscussionID == "" {
			continue
		}
		settlements++
		keys = append(keys, pointsHistoryReserveKey{
			settlementID:        entry.ID,
			settlementReason:    entry.Reason,
			reserveDiscussionID: entry.DiscussionID,
			priority:            0,
		})
		if entry.Reason == pointsReasonPlanning {
			keys = append(keys, pointsHistoryReserveKey{
				settlementID:        entry.ID,
				settlementReason:    entry.Reason,
				reserveDiscussionID: "",
				priority:            1,
			})
		}
	}
	if len(keys) == 0 {
		return map[int64]PointsLedgerEntry{}, 0, nil
	}

	var values strings.Builder
	args := make([]any, 0, len(keys)*4+1)
	for i, key := range keys {
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteString("(?, ?, ?, ?)")
		args = append(args, key.settlementID, key.settlementReason, key.reserveDiscussionID, key.priority)
	}
	args = append(args, userID)

	rows, err := s.db.QueryContext(ctx, `WITH settlement_keys(settlement_id, settlement_reason, reserve_discussion_id, priority) AS (
			VALUES `+values.String()+`
		), ranked AS (
			SELECT
				sk.settlement_id,
				r.id,
				r.discussion_id,
				r.delta,
				r.reason,
				r.balance_after,
				r.created_at,
				ROW_NUMBER() OVER (
					PARTITION BY sk.settlement_id
					ORDER BY sk.priority ASC, r.id DESC
				) AS rn
			FROM settlement_keys sk
			JOIN points_ledger r
				ON r.user_id = ?
				AND r.reason = 'reserve:' || sk.settlement_reason
				AND r.discussion_id = sk.reserve_discussion_id
				AND r.id < sk.settlement_id
			WHERE NOT EXISTS (
				SELECT 1 FROM points_ledger p
				WHERE p.user_id = r.user_id
					AND p.reason = sk.settlement_reason
					AND p.discussion_id = r.discussion_id
					AND p.id > r.id
					AND p.id < sk.settlement_id
			)
		)
		SELECT settlement_id, id, discussion_id, delta, reason, balance_after, created_at
		FROM ranked
		WHERE rn = 1`, args...)
	if err != nil {
		return nil, settlements, err
	}
	defer rows.Close()

	out := make(map[int64]PointsLedgerEntry)
	for rows.Next() {
		var settlementID int64
		var reserve PointsLedgerEntry
		if err := rows.Scan(
			&settlementID,
			&reserve.ID,
			&reserve.DiscussionID,
			&reserve.Delta,
			&reserve.Reason,
			&reserve.BalanceAfter,
			&reserve.CreatedAt,
		); err != nil {
			return nil, settlements, err
		}
		out[settlementID] = reserve
	}
	if err := rows.Err(); err != nil {
		return nil, settlements, err
	}
	return out, settlements, nil
}

func collapsibleSettlementReason(reason string) bool {
	return reason == pointsReasonPlanning || reason == pointsReasonGeneration ||
		reason == pointsReasonImageGeneration || reason == pointsReasonSummary
}

func collapsibleReserveKind(reason string) (string, bool) {
	switch reason {
	case "reserve:" + pointsReasonPlanning:
		return pointsReasonPlanning, true
	case "reserve:" + pointsReasonGeneration:
		return pointsReasonGeneration, true
	case "reserve:" + pointsReasonImageGeneration:
		return pointsReasonImageGeneration, true
	case "reserve:" + pointsReasonSummary:
		return pointsReasonSummary, true
	default:
		return "", false
	}
}

func collapseKey(kind, discussionID string) string {
	return kind + "\x00" + discussionID
}

func projectPointsHistory(rawNewestFirst []PointsLedgerEntry) []PointsLedgerEntry {
	type outputEntry struct {
		entry  PointsLedgerEntry
		hidden bool
	}
	out := make([]outputEntry, 0, len(rawNewestFirst))
	pendingHolds := map[string][]int{}
	var pendingPlanningHoldsWithoutDiscussion []int

	for i := len(rawNewestFirst) - 1; i >= 0; i-- {
		entry := rawNewestFirst[i]
		out = append(out, outputEntry{entry: entry})
		outIndex := len(out) - 1

		if kind, ok := collapsibleReserveKind(entry.Reason); ok {
			if entry.DiscussionID != "" {
				key := collapseKey(kind, entry.DiscussionID)
				pendingHolds[key] = append(pendingHolds[key], outIndex)
			} else if kind == pointsReasonPlanning {
				pendingPlanningHoldsWithoutDiscussion = append(pendingPlanningHoldsWithoutDiscussion, outIndex)
			}
			continue
		}

		if collapsibleSettlementReason(entry.Reason) {
			holdIndex, ok := popFirstHoldIndex(pendingHolds, collapseKey(entry.Reason, entry.DiscussionID))
			if !ok && entry.Reason == pointsReasonPlanning && len(pendingPlanningHoldsWithoutDiscussion) > 0 {
				holdIndex = pendingPlanningHoldsWithoutDiscussion[0]
				pendingPlanningHoldsWithoutDiscussion = pendingPlanningHoldsWithoutDiscussion[1:]
				ok = true
			}
			if ok {
				out[holdIndex].hidden = true
				out[outIndex].entry.Delta += out[holdIndex].entry.Delta
			}
		}
	}

	projected := make([]PointsLedgerEntry, 0, len(out))
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].hidden {
			continue
		}
		if out[i].entry.Reason == pointsReasonGenerationAdjustment {
			out[i].entry.Reason = pointsReasonGeneration
		}
		projected = append(projected, out[i].entry)
	}
	return projected
}

func paginatePointsHistory(entries []PointsLedgerEntry, limit, offset int) PointsHistoryPage {
	if offset >= len(entries) {
		return PointsHistoryPage{Entries: []PointsLedgerEntry{}}
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	page := append([]PointsLedgerEntry(nil), entries[offset:end]...)
	return PointsHistoryPage{
		Entries: page,
		HasMore: end < len(entries),
	}
}

func popFirstHoldIndex(holds map[string][]int, discussionID string) (int, bool) {
	if discussionID == "" {
		return 0, false
	}
	queue := holds[discussionID]
	if len(queue) == 0 {
		return 0, false
	}
	idx := queue[0]
	if len(queue) == 1 {
		delete(holds, discussionID)
	} else {
		holds[discussionID] = queue[1:]
	}
	return idx, true
}

// EnsureSignupGrant credits the configured starter balance the first time it is
// called for a user. Idempotent: keyed on a synthetic event id so repeated calls
// never double-grant. No-op when the grant is zero.
func (s *PointsStore) EnsureSignupGrant(ctx context.Context, userID string, grant int64) error {
	if s == nil || grant <= 0 {
		return nil
	}
	_, _, err := s.credit(ctx, userID, grant, pointsReasonSignup, "signup:"+userID)
	return err
}

// Credit adds points to a user's balance and appends a ledger row. When
// rcEventID is non-empty the credit is idempotent: a second call with the same
// id is a no-op (returns the unchanged balance).
func (s *PointsStore) Credit(ctx context.Context, userID string, points int64, reason, rcEventID string) (int64, error) {
	bal, _, err := s.CreditWithResult(ctx, userID, points, reason, rcEventID)
	return bal, err
}

// CreditWithResult is Credit plus an applied flag. applied=false means the
// event id had already been processed, so the returned balance is unchanged.
func (s *PointsStore) CreditWithResult(ctx context.Context, userID string, points int64, reason, rcEventID string) (balance int64, applied bool, err error) {
	return s.credit(ctx, userID, points, reason, rcEventID)
}

func (s *PointsStore) credit(ctx context.Context, userID string, points int64, reason, rcEventID string) (int64, bool, error) {
	if s == nil {
		return 0, false, errors.New("points store is not configured")
	}
	if points <= 0 {
		bal, err := s.Balance(ctx, userID)
		return bal, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	if rcEventID != "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM points_ledger WHERE rc_event_id = ?`, rcEventID).Scan(&n); err != nil {
			return 0, false, err
		}
		if n > 0 {
			// Already processed — return the current balance unchanged. Read via
			// the open tx: a fresh DB query would deadlock since the pool holds a
			// single connection (SetMaxOpenConns(1)) already taken by this tx.
			bal, err := txBalance(ctx, tx, userID)
			return bal, false, err
		}
	}

	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return 0, false, err
	}
	newBal := bal + points
	now := time.Now().UnixMilli()
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return 0, false, err
	}
	if _, err := insertLedger(ctx, tx, ledgerRow{
		userID:       userID,
		delta:        points,
		reason:       reason,
		rcEventID:    rcEventID,
		balanceAfter: newBal,
		createdAt:    now,
	}); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return newBal, true, nil
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
	ok, bal, _, err := s.ReserveWithLedgerID(ctx, userID, discussionID, points, kind)
	return ok, bal, err
}

func (s *PointsStore) ReserveWithLedgerID(ctx context.Context, userID, discussionID string, points int64, kind string) (bool, int64, int64, error) {
	if s == nil {
		return false, 0, 0, errors.New("points store is not configured")
	}
	if points <= 0 {
		bal, err := s.Balance(ctx, userID)
		return true, bal, 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, 0, err
	}
	defer tx.Rollback()

	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return false, 0, 0, err
	}
	if bal < points {
		// Insufficient — make no change so no work is started uncharged.
		return false, bal, 0, nil
	}
	newBal := bal - points
	now := time.Now().UnixMilli()
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return false, 0, 0, err
	}
	reserveLedgerID, err := insertLedger(ctx, tx, ledgerRow{
		userID:       userID,
		discussionID: discussionID,
		delta:        -points,
		reason:       "reserve:" + kind,
		balanceAfter: newBal,
		createdAt:    now,
	})
	if err != nil {
		return false, 0, 0, err
	}
	if kind == pointsReasonGeneration && discussionID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_reserved = ?, updated_at = ? WHERE id = ?`,
			points, now, discussionID); err != nil {
			return false, 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, 0, 0, err
	}
	return true, newBal, reserveLedgerID, nil
}

// settle reconciles a reservation against the actual cost within an open tx: it
// adjusts the balance by (reserved - actual) — refunding an over-reservation or
// charging the remainder — records a settlement ledger row, and adds the points
// ACTUALLY debited to the discussion's points_charged. The balance is clamped at
// zero, and points_charged is capped to the debited amount so a podcast can
// never display/store more points than were really taken (the invariant
// points_charged == sum of debits holds even when actual exceeds the balance).
func (s *PointsStore) settle(ctx context.Context, tx *sqlTx, userID, discussionID string, reserved, actual int64, kind string, detail PointsUsageDetail, now int64) (int64, error) {
	newBal, _, err := s.settleWithDebited(ctx, tx, userID, discussionID, reserved, actual, kind, detail, now)
	return newBal, err
}

func (s *PointsStore) settleWithDebited(ctx context.Context, tx *sqlTx, userID, discussionID string, reserved, actual int64, kind string, detail PointsUsageDetail, now int64) (int64, int64, error) {
	bal, err := txBalance(ctx, tx, userID)
	if err != nil {
		return 0, 0, err
	}
	newBal := bal + (reserved - actual)
	if newBal < 0 {
		newBal = 0
	}
	if err := upsertBalance(ctx, tx, userID, newBal, now); err != nil {
		return 0, 0, err
	}
	if _, err := insertLedger(ctx, tx, ledgerRow{
		userID:       userID,
		discussionID: discussionID,
		delta:        newBal - bal,
		reason:       kind,
		detail:       detail,
		balanceAfter: newBal,
		createdAt:    now,
	}); err != nil {
		return 0, 0, err
	}
	// Points truly removed across reserve+settle = (balance before reserve) - newBal
	// = (bal + reserved) - newBal. Without a clamp this equals `actual`; with a
	// clamp it's lower, so charging the raw `actual` would over-report.
	debited := bal + reserved - newBal
	if discussionID != "" && debited > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_charged = points_charged + ?, updated_at = ? WHERE id = ?`,
			debited, now, discussionID); err != nil {
			return 0, 0, err
		}
	}
	return newBal, debited, nil
}

// SettlePlanning reconciles a planning reservation synchronously (called once, in
// the same request/goroutine that reserved). Pass actual=0 to fully refund the
// reservation when the planning work failed.
func (s *PointsStore) SettlePlanning(ctx context.Context, userID, discussionID string, reserveLedgerID, reserved, actual int64, detail PointsUsageDetail) (int64, error) {
	return s.SettleReserved(ctx, userID, discussionID, reserveLedgerID, reserved, actual, pointsReasonPlanning, detail)
}

func (s *PointsStore) SettleReserved(ctx context.Context, userID, discussionID string, reserveLedgerID, reserved, actual int64, kind string, detail PointsUsageDetail) (int64, error) {
	if s == nil {
		return 0, errors.New("points store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if reserveLedgerID > 0 && discussionID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE points_ledger
			SET discussion_id = ?
			WHERE id = ? AND user_id = ? AND reason = ?`,
			discussionID, reserveLedgerID, userID, "reserve:"+kind); err != nil {
			return 0, err
		}
	}
	newBal, err := s.settle(ctx, tx, userID, discussionID, reserved, actual, kind, detail, time.Now().UnixMilli())
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
	return retryTransientDBConnection(ctx, func() error {
		return s.settleGenerationOnce(ctx, discussionID, actual, detail)
	})
}

func (s *PointsStore) settleGenerationOnce(ctx context.Context, discussionID string, actual int64, detail PointsUsageDetail) error {
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
		if err := s.upgradeGenerationSettlement(ctx, tx, discussionID, actual, detail); err != nil {
			return err
		}
		return tx.Commit()
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
	if reserved <= 0 {
		return nil
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

func (s *PointsStore) upgradeGenerationSettlement(ctx context.Context, tx *sqlTx, discussionID string, actual int64, detail PointsUsageDetail) error {
	if actual <= 0 {
		return nil
	}
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM native_discussions WHERE id = ?`, discussionID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ledgerID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM points_ledger
		WHERE discussion_id = ? AND reason = ?
		ORDER BY id DESC LIMIT 1`, discussionID, pointsReasonGeneration).Scan(&ledgerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var previouslyDebited int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(-SUM(delta), 0)
		FROM points_ledger
		WHERE discussion_id = ? AND reason IN (?, ?, ?)`,
		discussionID, "reserve:"+pointsReasonGeneration, pointsReasonGeneration, pointsReasonGenerationAdjustment).Scan(&previouslyDebited); err != nil {
		return err
	}
	if previouslyDebited < 0 {
		previouslyDebited = 0
	}
	missing := actual - previouslyDebited
	if missing <= 0 {
		return nil
	}
	bal, err := txBalance(ctx, tx, owner)
	if err != nil {
		return err
	}
	debited := missing
	if debited > bal {
		debited = bal
	}
	now := time.Now().UnixMilli()
	if debited <= 0 {
		_, err = tx.ExecContext(ctx, `UPDATE points_ledger SET
			cost_usd = ?, prompt_tokens = ?, completion_tokens = ?, total_tokens = ?,
			llm_cost_usd = ?, tts_cost_usd = ?, music_cost_usd = ?
			WHERE id = ?`,
			detail.CostUSD, detail.PromptTokens, detail.CompletionTokens, detail.TotalTokens,
			detail.LLMCostUSD, detail.TTSCostUSD, detail.MusicCostUSD, ledgerID)
		if err != nil {
			return err
		}
		return nil
	}
	newBal := bal - debited
	if err := upsertBalance(ctx, tx, owner, newBal, now); err != nil {
		return err
	}
	if _, err := insertLedger(ctx, tx, ledgerRow{
		userID:       owner,
		discussionID: discussionID,
		delta:        -debited,
		reason:       pointsReasonGenerationAdjustment,
		detail:       detail,
		balanceAfter: newBal,
		createdAt:    now,
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_charged = points_charged + ?, updated_at = ? WHERE id = ?`,
		debited, now, discussionID); err != nil {
		return err
	}
	return nil
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

// ChargeGeneration reconciles an active generation reservation from a finished
// run's usage. Computes the points (with the per-podcast minimum) and settles
// idempotently. Safe to call from both the job-completion path and the lazy
// discussion-fetch path; if no generation reservation exists, no charge is
// created.
func (s *PointsStore) ChargeGeneration(ctx context.Context, env *config.Env, discussionID string, detail PointsUsageDetail) error {
	if s == nil {
		return nil
	}
	return s.SettleGeneration(ctx, discussionID, s.GenerationPoints(env, detail.CostUSD), detail)
}

func (s *PointsStore) ChargeGenerationKnown(ctx context.Context, env *config.Env, owner, discussionID string, reserved, currentCharged int64, detail PointsUsageDetail) (int64, error) {
	if s == nil {
		return currentCharged, nil
	}
	actual := s.GenerationPoints(env, detail.CostUSD)
	return s.SettleGenerationKnown(ctx, owner, discussionID, reserved, currentCharged, actual, detail)
}

func (s *PointsStore) SettleGenerationKnown(ctx context.Context, owner, discussionID string, reserved, currentCharged, actual int64, detail PointsUsageDetail) (int64, error) {
	if s == nil || discussionID == "" {
		return currentCharged, nil
	}
	charged := currentCharged
	err := retryTransientDBConnection(ctx, func() error {
		next, err := s.settleGenerationKnownOnce(ctx, owner, discussionID, reserved, currentCharged, actual, detail)
		if err != nil {
			return err
		}
		charged = next
		return nil
	})
	return charged, err
}

func (s *PointsStore) settleGenerationKnownOnce(ctx context.Context, owner, discussionID string, reserved, currentCharged, actual int64, detail PointsUsageDetail) (int64, error) {
	if strings.TrimSpace(owner) == "" || reserved <= 0 {
		return currentCharged, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return currentCharged, err
	}
	defer tx.Rollback()

	var settled int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM points_ledger WHERE discussion_id = ? AND reason = ?`, discussionID, pointsReasonGeneration).Scan(&settled); err != nil {
		return currentCharged, err
	}
	if settled > 0 {
		if err := s.upgradeGenerationSettlement(ctx, tx, discussionID, actual, detail); err != nil {
			return currentCharged, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_reserved = 0, updated_at = ? WHERE id = ?`, time.Now().UnixMilli(), discussionID); err != nil {
			return currentCharged, err
		}
		var charged int64
		if err := tx.QueryRowContext(ctx, `SELECT points_charged FROM native_discussions WHERE id = ?`, discussionID).Scan(&charged); err != nil {
			return currentCharged, err
		}
		if err := tx.Commit(); err != nil {
			return currentCharged, err
		}
		return charged, nil
	}

	now := time.Now().UnixMilli()
	_, debited, err := s.settleWithDebited(ctx, tx, owner, discussionID, reserved, actual, pointsReasonGeneration, detail, now)
	if err != nil {
		return currentCharged, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_discussions SET points_reserved = 0, updated_at = ? WHERE id = ?`, now, discussionID); err != nil {
		return currentCharged, err
	}
	if err := tx.Commit(); err != nil {
		return currentCharged, err
	}
	return currentCharged + debited, nil
}

// SummaryPoints converts a summary run's real cost into points at the standard
// markup. Unlike generation there is no per-podcast minimum — a summary is
// charged purely on its metered usage.
func (s *PointsStore) SummaryPoints(env *config.Env, costUSD float64) int64 {
	return pointsForCost(env, costUSD)
}

// ReserveSummary atomically holds the estimated summary cost against the
// creator's balance before the summary agent runs, so summary generation is
// never free and can't overdraw. Returns the reserved amount, the reserve ledger
// id (for settlement), and ok=false (with no change) when the balance is short.
// Reserved 0 / ok=true when points is disabled or the estimate is zero — the
// caller then runs the summary without a hold and settles to actual.
func (s *PointsStore) ReserveSummary(ctx context.Context, env *config.Env, userID, discussionID string) (reserved, reserveLedgerID int64, ok bool, err error) {
	if s == nil {
		return 0, 0, true, nil
	}
	required := int64(0)
	if env != nil {
		required = pointsForUSD(env, env.PointsSummaryEstUSD)
	}
	if required <= 0 {
		return 0, 0, true, nil
	}
	accepted, _, ledgerID, err := s.ReserveWithLedgerID(ctx, userID, discussionID, required, pointsReasonSummary)
	if err != nil {
		return 0, 0, false, err
	}
	if !accepted {
		return 0, 0, false, nil
	}
	return required, ledgerID, true, nil
}

// SettleSummary reconciles a summary reservation against the run's actual usage,
// refunding the unused remainder and adding the actual points to the
// discussion's running total. Pass actual=0 to fully refund when the summary
// failed before producing chargeable work.
func (s *PointsStore) SettleSummary(ctx context.Context, userID, discussionID string, reserveLedgerID, reserved, actual int64, detail PointsUsageDetail) error {
	if s == nil || reserved <= 0 {
		return nil
	}
	_, err := s.SettleReserved(ctx, userID, discussionID, reserveLedgerID, reserved, actual, pointsReasonSummary, detail)
	return err
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
	if _, err := insertLedger(ctx, tx, ledgerRow{
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

func insertLedger(ctx context.Context, tx *sqlTx, row ledgerRow) (int64, error) {
	if tx.kind == databasePostgres {
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO points_ledger
			(user_id, discussion_id, delta, reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
			 llm_cost_usd, tts_cost_usd, music_cost_usd, rc_event_id, balance_after, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			row.userID, row.discussionID, row.delta, row.reason, row.detail.CostUSD,
			row.detail.PromptTokens, row.detail.CompletionTokens, row.detail.TotalTokens,
			row.detail.LLMCostUSD, row.detail.TTSCostUSD, row.detail.MusicCostUSD,
			row.rcEventID, row.balanceAfter, row.createdAt).Scan(&id)
		return id, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO points_ledger
		(user_id, discussion_id, delta, reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
		 llm_cost_usd, tts_cost_usd, music_cost_usd, rc_event_id, balance_after, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.userID, row.discussionID, row.delta, row.reason, row.detail.CostUSD,
		row.detail.PromptTokens, row.detail.CompletionTokens, row.detail.TotalTokens,
		row.detail.LLMCostUSD, row.detail.TTSCostUSD, row.detail.MusicCostUSD,
		row.rcEventID, row.balanceAfter, row.createdAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func txBalance(ctx context.Context, tx *sqlTx, userID string) (int64, error) {
	var bal int64
	err := tx.QueryRowContext(ctx, `SELECT balance FROM user_points_balance WHERE user_id = ?`, userID).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return bal, err
}

func repairCachedBalanceFromLatestLedger(ctx context.Context, tx *sqlTx, userID string) error {
	var bal int64
	err := tx.QueryRowContext(ctx, `SELECT balance_after FROM points_ledger WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID).Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return upsertBalance(ctx, tx, userID, bal, time.Now().UnixMilli())
}

func upsertBalance(ctx context.Context, tx *sqlTx, userID string, balance, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO user_points_balance (user_id, balance, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET balance = excluded.balance, updated_at = excluded.updated_at`,
		userID, balance, now)
	return err
}
