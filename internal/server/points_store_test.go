package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func newTestPointsStore(t *testing.T) (*PointsStore, *DiscussionStore) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "points.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	ps, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}
	return ps, ds
}

func newPostgresTestPointsStore(t *testing.T) (*PointsStore, *DiscussionStore) {
	t.Helper()
	databaseURL := os.Getenv("POSTGRES_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("POSTGRES_TEST_DATABASE_URL is not set")
	}
	ds, err := NewDiscussionStore("", databaseURL, "")
	if err != nil {
		t.Fatalf("NewDiscussionStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	ps, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("NewPointsStore postgres: %v", err)
	}
	return ps, ds
}

func TestPointsForCostRoundsUp(t *testing.T) {
	env := &config.Env{PointsPerUSDCost: 1340}
	cases := []struct {
		cost float64
		want int64
	}{
		{0, 0},
		{0.05, 67},  // ceil(67.0)
		{0.60, 804}, // 30-min podcast at observed cost
		{0.0001, 1}, // tiny cost still rounds up to 1
	}
	for _, c := range cases {
		if got := pointsForCost(env, c.cost); got != c.want {
			t.Errorf("pointsForCost(%.4f) = %d, want %d", c.cost, got, c.want)
		}
	}
	// Disabled rate → no charge.
	if got := pointsForCost(&config.Env{PointsPerUSDCost: 0}, 1); got != 0 {
		t.Errorf("zero rate should yield 0 points, got %d", got)
	}
}

func TestCreditIsIdempotentOnEventID(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()

	bal, err := ps.Credit(ctx, "u1", 1000, "purchase:INITIAL_PURCHASE", "evt-1")
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance after first credit = %d, want 1000", bal)
	}
	// Same event id → no double credit.
	bal, err = ps.Credit(ctx, "u1", 1000, "purchase:INITIAL_PURCHASE", "evt-1")
	if err != nil {
		t.Fatalf("Credit (replay): %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance after replayed credit = %d, want 1000", bal)
	}
	// Different event id → credits again.
	bal, err = ps.Credit(ctx, "u1", 500, "purchase:RENEWAL", "evt-2")
	if err != nil {
		t.Fatalf("Credit (evt-2): %v", err)
	}
	if bal != 1500 {
		t.Fatalf("balance after second credit = %d, want 1500", bal)
	}
}

func TestReserveIsConditionalAndNeverNegative(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	if _, err := ps.Credit(ctx, "u1", 100, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	// Over-reservation fails atomically with no balance change.
	ok, bal, err := ps.Reserve(ctx, "u1", "", 120, pointsReasonPlanning)
	if err != nil {
		t.Fatalf("Reserve(over): %v", err)
	}
	if ok || bal != 100 {
		t.Fatalf("over-reserve = (ok=%v, bal=%d), want (false, 100)", ok, bal)
	}
	// A coverable reservation succeeds and holds the points.
	ok, bal, err = ps.Reserve(ctx, "u1", "", 30, pointsReasonPlanning)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !ok || bal != 70 {
		t.Fatalf("reserve = (ok=%v, bal=%d), want (true, 70)", ok, bal)
	}
}

func TestSettlePlanningRefundsRemainder(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 100, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	// Reserve 50, then the round only actually cost 20 → 30 is refunded.
	ok, bal, err := ps.Reserve(ctx, owner, d.ID, 50, pointsReasonPlanning)
	if err != nil || !ok || bal != 50 {
		t.Fatalf("Reserve = (ok=%v, bal=%d, err=%v), want (true, 50, nil)", ok, bal, err)
	}
	bal, err = ps.SettlePlanning(ctx, owner, d.ID, 0, 50, 20, PointsUsageDetail{CostUSD: 0.015})
	if err != nil {
		t.Fatalf("SettlePlanning: %v", err)
	}
	if bal != 80 { // 100 - 50 reserve + 30 refund = 80 (net -20)
		t.Fatalf("balance = %d, want 80 (only the 20 actual charged)", bal)
	}
	total, err := ps.DiscussionPoints(ctx, d.ID)
	if err != nil {
		t.Fatalf("DiscussionPoints: %v", err)
	}
	if total != 20 {
		t.Fatalf("points_charged = %d, want 20", total)
	}
}

func TestHistoryShowsOnlyPlanningHoldUntilSettled(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 100, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 50, pointsReasonPlanning); err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	page, err := ps.History(ctx, owner, 200, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	entries := page.Entries
	if len(entries) < 1 {
		t.Fatalf("History returned no entries")
	}
	if entries[0].Reason != "reserve:planning" || entries[0].Delta != -50 {
		t.Fatalf("latest entry = (%q, %d), want planning hold -50", entries[0].Reason, entries[0].Delta)
	}
}

func TestHistoryCollapsesSettledPlanningHoldIntoUsage(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 100, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 50, pointsReasonPlanning); err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, 0, 50, 20, PointsUsageDetail{CostUSD: 0.015}); err != nil {
		t.Fatalf("SettlePlanning: %v", err)
	}
	page, err := ps.History(ctx, owner, 200, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	entries := page.Entries
	if len(entries) != 2 {
		t.Fatalf("History returned %d entries, want 2: %#v", len(entries), entries)
	}
	if entries[0].Reason != "planning" || entries[0].Delta != -20 || entries[0].BalanceAfter != 80 {
		t.Fatalf("latest entry = (%q, %d, balance %d), want planning -20 balance 80",
			entries[0].Reason, entries[0].Delta, entries[0].BalanceAfter)
	}
	for _, entry := range entries {
		if entry.Reason == "reserve:planning" {
			t.Fatalf("settled planning hold should be hidden: %#v", entries)
		}
	}
}

func TestPostgresHistoryCollapsesSettledPlanningHoldIntoUsage(t *testing.T) {
	ps, ds := newPostgresTestPointsStore(t)
	ctx := context.Background()
	owner := "pg-points-owner-" + newJobID()
	d, err := ds.Create(ctx, owner, "postgres points history", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 100, "signup_grant", "signup:"+owner); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 50, pointsReasonPlanning); err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, 0, 50, 20, PointsUsageDetail{CostUSD: 0.015}); err != nil {
		t.Fatalf("SettlePlanning: %v", err)
	}

	page, err := ps.History(ctx, owner, 200, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("History returned %d entries, want 2: %#v", len(page.Entries), page.Entries)
	}
	if page.Entries[0].Reason != pointsReasonPlanning || page.Entries[0].Delta != -20 || page.Entries[0].BalanceAfter != 80 {
		t.Fatalf("latest entry = (%q, %d, balance %d), want planning -20 balance 80",
			page.Entries[0].Reason, page.Entries[0].Delta, page.Entries[0].BalanceAfter)
	}
	for _, entry := range page.Entries {
		if entry.Reason == "reserve:"+pointsReasonPlanning {
			t.Fatalf("settled planning hold should be hidden: %#v", page.Entries)
		}
	}
}

func TestHistoryCollapsesPlanningHoldOutsideInitialRawBatch(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 1000, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	ok, _, reserveLedgerID, err := ps.ReserveWithLedgerID(ctx, owner, "", 50, pointsReasonPlanning)
	if err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	for i := range 150 {
		if _, err := ps.Credit(ctx, owner, 1, "purchase:TEST", fmt.Sprintf("evt-gap-%03d", i)); err != nil {
			t.Fatalf("Credit gap %d: %v", i, err)
		}
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, reserveLedgerID, 50, 20, PointsUsageDetail{CostUSD: 0.015}); err != nil {
		t.Fatalf("SettlePlanning: %v", err)
	}

	// limit=1 makes the first raw batch 100 rows. The matching hold is older
	// than that, so History must explicitly hydrate it before projecting.
	page, err := ps.History(ctx, owner, 1, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("History returned %d entries, want 1: %#v", len(page.Entries), page.Entries)
	}
	if page.Entries[0].Reason != "planning" || page.Entries[0].Delta != -20 {
		t.Fatalf("latest entry = (%q, %d), want collapsed planning -20", page.Entries[0].Reason, page.Entries[0].Delta)
	}
}

func TestHistoryDoesNotDuplicateHydratedPlanningReserveOnLaterPage(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 1000, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	ok, _, reserveLedgerID, err := ps.ReserveWithLedgerID(ctx, owner, "", 50, pointsReasonPlanning)
	if err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	for i := range 150 {
		if _, err := ps.Credit(ctx, owner, 1, "purchase:TEST", fmt.Sprintf("evt-page-gap-%03d", i)); err != nil {
			t.Fatalf("Credit gap %d: %v", i, err)
		}
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, reserveLedgerID, 50, 20, PointsUsageDetail{CostUSD: 0.015}); err != nil {
		t.Fatalf("SettlePlanning: %v", err)
	}

	page, err := ps.History(ctx, owner, 50, 100)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	seen := map[int64]bool{}
	for _, entry := range page.Entries {
		if seen[entry.ID] {
			t.Fatalf("duplicate entry id %d in page: %#v", entry.ID, page.Entries)
		}
		seen[entry.ID] = true
		if entry.Reason == "reserve:planning" {
			t.Fatalf("settled planning hold should not be visible on later page: %#v", page.Entries)
		}
	}
}

func TestHistoryCollapsesSettledGenerationHoldIntoUsage(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 1000, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 804, pointsReasonGeneration); err != nil || !ok {
		t.Fatalf("Reserve generation = ok=%v err=%v", ok, err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 372, PointsUsageDetail{CostUSD: 0.28}); err != nil {
		t.Fatalf("SettleGeneration: %v", err)
	}

	page, err := ps.History(ctx, owner, 200, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("History returned %d entries, want 2: %#v", len(page.Entries), page.Entries)
	}
	if page.Entries[0].Reason != "generation" || page.Entries[0].Delta != -372 || page.Entries[0].BalanceAfter != 628 {
		t.Fatalf("latest entry = (%q, %d, balance %d), want collapsed generation -372 balance 628",
			page.Entries[0].Reason, page.Entries[0].Delta, page.Entries[0].BalanceAfter)
	}
	bal, err := ps.Balance(ctx, owner)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != page.Entries[0].BalanceAfter {
		t.Fatalf("balance = %d, want latest history balance_after %d", bal, page.Entries[0].BalanceAfter)
	}
	for _, entry := range page.Entries {
		if entry.Reason == "reserve:generation" {
			t.Fatalf("settled generation hold should be hidden: %#v", page.Entries)
		}
	}
}

func TestHistoryBalanceMatchesCollapsedPodcastRow(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := range 11 {
		if _, err := ps.Credit(ctx, owner, 1000, "purchase:TEST", fmt.Sprintf("purchase-%02d", i)); err != nil {
			t.Fatalf("Credit %d: %v", i, err)
		}
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, 0, 0, 13, PointsUsageDetail{CostUSD: 0.01}); err != nil {
		t.Fatalf("SettlePlanning 13: %v", err)
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, 0, 0, 8, PointsUsageDetail{CostUSD: 0.006}); err != nil {
		t.Fatalf("SettlePlanning 8: %v", err)
	}
	if ok, bal, err := ps.Reserve(ctx, owner, d.ID, 1433, pointsReasonGeneration); err != nil || !ok || bal != 9546 {
		t.Fatalf("Reserve generation = (ok=%v, bal=%d, err=%v), want (true, 9546, nil)", ok, bal, err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 372, PointsUsageDetail{CostUSD: 0.278}); err != nil {
		t.Fatalf("SettleGeneration: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `UPDATE user_points_balance SET balance = ? WHERE user_id = ?`, int64(9546), owner); err != nil {
		t.Fatalf("corrupt cached balance: %v", err)
	}

	page, err := ps.History(ctx, owner, 50, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatalf("History returned no entries")
	}
	latest := page.Entries[0]
	if latest.Reason != pointsReasonGeneration || latest.Delta != -372 || latest.BalanceAfter != 10607 {
		t.Fatalf("latest entry = (%q, %d, balance %d), want podcast -372 balance 10607",
			latest.Reason, latest.Delta, latest.BalanceAfter)
	}
	bal, err := ps.Balance(ctx, owner)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 10607 {
		t.Fatalf("balance = %d, want 10607", bal)
	}
	if bal != latest.BalanceAfter {
		t.Fatalf("balance = %d, want latest history balance_after %d", bal, latest.BalanceAfter)
	}
	var cached int64
	if err := ps.db.QueryRowContext(ctx, `SELECT balance FROM user_points_balance WHERE user_id = ?`, owner).Scan(&cached); err != nil {
		t.Fatalf("read cached balance: %v", err)
	}
	if cached != 10607 {
		t.Fatalf("repaired cached balance = %d, want 10607", cached)
	}
	for _, entry := range page.Entries {
		if entry.Reason == "reserve:generation" {
			t.Fatalf("settled generation hold should be hidden: %#v", page.Entries)
		}
	}
}

func TestPointsHistoryEndpointBalanceMatchesCollapsedPodcastRow(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "anonymous"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := range 11 {
		if _, err := ps.Credit(ctx, owner, 1000, "purchase:TEST", fmt.Sprintf("endpoint-purchase-%02d", i)); err != nil {
			t.Fatalf("Credit %d: %v", i, err)
		}
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, 0, 0, 13, PointsUsageDetail{}); err != nil {
		t.Fatalf("SettlePlanning 13: %v", err)
	}
	if _, err := ps.SettlePlanning(ctx, owner, d.ID, 0, 0, 8, PointsUsageDetail{}); err != nil {
		t.Fatalf("SettlePlanning 8: %v", err)
	}
	if ok, bal, err := ps.Reserve(ctx, owner, d.ID, 1433, pointsReasonGeneration); err != nil || !ok || bal != 9546 {
		t.Fatalf("Reserve generation = (ok=%v, bal=%d, err=%v), want (true, 9546, nil)", ok, bal, err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 372, PointsUsageDetail{}); err != nil {
		t.Fatalf("SettleGeneration: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `UPDATE user_points_balance SET balance = ? WHERE user_id = ?`, int64(9546), owner); err != nil {
		t.Fatalf("corrupt cached balance: %v", err)
	}

	srv := New(Deps{
		Mode:        ModeDashboard,
		Discussions: ds,
		Points:      ps,
		Env:         &config.Env{},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/points/history?limit=50&offset=0", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Balance int64               `json:"balance"`
		Entries []PointsLedgerEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode history body %q: %v", rec.Body.String(), err)
	}
	if body.Balance != 10607 {
		t.Fatalf("response balance = %d, want 10607", body.Balance)
	}
	if len(body.Entries) == 0 {
		t.Fatalf("response entries empty")
	}
	if body.Entries[0].Reason != pointsReasonGeneration || body.Entries[0].Delta != -372 || body.Entries[0].BalanceAfter != 10607 {
		t.Fatalf("latest response entry = (%q, %d, balance %d), want podcast -372 balance 10607",
			body.Entries[0].Reason, body.Entries[0].Delta, body.Entries[0].BalanceAfter)
	}
	if body.Balance != body.Entries[0].BalanceAfter {
		t.Fatalf("response balance = %d, want latest history balance_after %d", body.Balance, body.Entries[0].BalanceAfter)
	}
}

func TestSettleGenerationReconcilesAndIsIdempotent(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 1000, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	// Reserve an 804-pt estimate at generate time.
	ok, bal, err := ps.Reserve(ctx, owner, d.ID, 804, pointsReasonGeneration)
	if err != nil || !ok || bal != 196 {
		t.Fatalf("Reserve generation = (ok=%v, bal=%d, err=%v), want (true, 196, nil)", ok, bal, err)
	}
	// Actual cost came in lower (600) → 204 refunded.
	if err := ps.SettleGeneration(ctx, d.ID, 600, PointsUsageDetail{CostUSD: 0.45}); err != nil {
		t.Fatalf("SettleGeneration: %v", err)
	}
	bal, _ = ps.Balance(ctx, owner)
	if bal != 400 { // 196 + (804 - 600)
		t.Fatalf("balance = %d, want 400", bal)
	}
	// Idempotent: a second settle (lazy path firing again) does nothing.
	if err := ps.SettleGeneration(ctx, d.ID, 600, PointsUsageDetail{CostUSD: 0.45}); err != nil {
		t.Fatalf("SettleGeneration (replay): %v", err)
	}
	bal, _ = ps.Balance(ctx, owner)
	if bal != 400 {
		t.Fatalf("balance after replay = %d, want 400 (settled once)", bal)
	}
	total, _ := ps.DiscussionPoints(ctx, d.ID)
	if total != 600 {
		t.Fatalf("points_charged = %d, want 600", total)
	}
}

func TestSettleGenerationUpgradesIncompleteFirstSettlement(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 2000, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 804, pointsReasonGeneration); err != nil || !ok {
		t.Fatalf("Reserve generation = ok=%v err=%v", ok, err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 21, PointsUsageDetail{CostUSD: 0.015}); err != nil {
		t.Fatalf("SettleGeneration initial: %v", err)
	}
	total, _ := ps.DiscussionPoints(ctx, d.ID)
	if total != 21 {
		t.Fatalf("points_charged after initial settle = %d, want 21", total)
	}

	detail := PointsUsageDetail{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		LLMCostUSD:       0.015,
		LLMCostKnown:     true,
		TTSCostUSD:       0.30,
		MusicCostUSD:     0.356,
		CostUSD:          0.671,
	}
	if err := ps.SettleGeneration(ctx, d.ID, 900, detail); err != nil {
		t.Fatalf("SettleGeneration upgrade: %v", err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 900, detail); err != nil {
		t.Fatalf("SettleGeneration replay after upgrade: %v", err)
	}
	total, _ = ps.DiscussionPoints(ctx, d.ID)
	if total != 900 {
		t.Fatalf("points_charged after upgrade = %d, want 900", total)
	}
	bal, _ := ps.Balance(ctx, owner)
	if bal != 1100 {
		t.Fatalf("balance after upgrade = %d, want 1100", bal)
	}
	page, err := ps.History(ctx, owner, 200, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if page.Entries[0].Reason != pointsReasonGeneration || page.Entries[0].Delta != -879 || page.Entries[0].BalanceAfter != 1100 {
		t.Fatalf("latest history entry = (%q, %d, balance %d), want generation adjustment -879 balance 1100",
			page.Entries[0].Reason, page.Entries[0].Delta, page.Entries[0].BalanceAfter)
	}
	var adjustments int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM points_ledger WHERE discussion_id = ? AND reason = ?`,
		d.ID, pointsReasonGenerationAdjustment).Scan(&adjustments); err != nil {
		t.Fatalf("count adjustments: %v", err)
	}
	if adjustments != 1 {
		t.Fatalf("generation adjustment rows = %d, want 1", adjustments)
	}
}

func TestSettleGenerationUpgradeAfterLaterLedgerActivityKeepsBalance(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "anonymous"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 2000, "signup_grant", "signup:anonymous"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 804, pointsReasonGeneration); err != nil || !ok {
		t.Fatalf("Reserve generation = ok=%v err=%v", ok, err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 21, PointsUsageDetail{CostUSD: 0.015}); err != nil {
		t.Fatalf("SettleGeneration initial: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 100, "purchase:TEST", "later-credit"); err != nil {
		t.Fatalf("later Credit: %v", err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 900, PointsUsageDetail{CostUSD: 0.671}); err != nil {
		t.Fatalf("SettleGeneration upgrade: %v", err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 900, PointsUsageDetail{CostUSD: 0.671}); err != nil {
		t.Fatalf("SettleGeneration replay: %v", err)
	}

	bal, err := ps.Balance(ctx, owner)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1200 {
		t.Fatalf("balance after upgrade with later activity = %d, want 1200", bal)
	}
	total, err := ps.DiscussionPoints(ctx, d.ID)
	if err != nil {
		t.Fatalf("DiscussionPoints: %v", err)
	}
	if total != 900 {
		t.Fatalf("points_charged = %d, want 900", total)
	}
	var adjustments int
	if err := ps.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM points_ledger WHERE discussion_id = ? AND reason = ?`,
		d.ID, pointsReasonGenerationAdjustment).Scan(&adjustments); err != nil {
		t.Fatalf("count adjustments: %v", err)
	}
	if adjustments != 1 {
		t.Fatalf("generation adjustment rows = %d, want 1", adjustments)
	}

	page, err := ps.History(ctx, owner, 50, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatalf("History returned no entries")
	}
	if page.Entries[0].Reason != pointsReasonGeneration || page.Entries[0].Delta != -879 || page.Entries[0].BalanceAfter != 1200 {
		t.Fatalf("latest history entry = (%q, %d, balance %d), want generation adjustment -879 balance 1200",
			page.Entries[0].Reason, page.Entries[0].Delta, page.Entries[0].BalanceAfter)
	}

	srv := New(Deps{
		Mode:        ModeDashboard,
		Discussions: ds,
		Points:      ps,
		Env:         &config.Env{},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/points/history?limit=50&offset=0", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Balance int64               `json:"balance"`
		Entries []PointsLedgerEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode history body %q: %v", rec.Body.String(), err)
	}
	if body.Balance != 1200 {
		t.Fatalf("response balance = %d, want 1200", body.Balance)
	}
	if len(body.Entries) == 0 {
		t.Fatalf("response entries empty")
	}
	if body.Entries[0].Reason != pointsReasonGeneration || body.Entries[0].Delta != -879 || body.Entries[0].BalanceAfter != 1200 {
		t.Fatalf("latest response entry = (%q, %d, balance %d), want generation adjustment -879 balance 1200",
			body.Entries[0].Reason, body.Entries[0].Delta, body.Entries[0].BalanceAfter)
	}
}

func TestSettleCapsChargedToDebited(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 100, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	// Reserve 80 (balance → 20), then actual usage overshoots to 120.
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 80, pointsReasonGeneration); err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	if err := ps.SettleGeneration(ctx, d.ID, 120, PointsUsageDetail{CostUSD: 0.09}); err != nil {
		t.Fatalf("SettleGeneration: %v", err)
	}
	bal, _ := ps.Balance(ctx, owner)
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (never negative)", bal)
	}
	// Only 100 points existed, so at most 100 can be charged — not the 120 actual.
	total, _ := ps.DiscussionPoints(ctx, d.ID)
	if total != 100 {
		t.Fatalf("points_charged = %d, want 100 (capped to points actually debited)", total)
	}
}

func TestRepairLegacyGenerationOverchargesCapsHistoryToDiscussionPoints(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 12000, "purchase:TEST", "legacy-credit"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `INSERT INTO points_ledger
		(user_id, discussion_id, delta, reason, balance_after, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		owner, d.ID, int64(-11037), pointsReasonGeneration, int64(963), int64(2000)); err != nil {
		t.Fatalf("insert legacy generation row: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `UPDATE user_points_balance SET balance = ? WHERE user_id = ?`, int64(963), owner); err != nil {
		t.Fatalf("set legacy balance: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `UPDATE native_discussions SET points_charged = ? WHERE id = ?`, int64(1925), d.ID); err != nil {
		t.Fatalf("set discussion points: %v", err)
	}

	count, points, err := ps.RepairLegacyGenerationOvercharges(ctx)
	if err != nil {
		t.Fatalf("RepairLegacyGenerationOvercharges: %v", err)
	}
	if count != 1 || points != 9112 {
		t.Fatalf("repair = (%d discussions, %d points), want (1, 9112)", count, points)
	}
	page, err := ps.History(ctx, owner, 10, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatalf("History returned no entries")
	}
	if page.Entries[0].Reason != pointsReasonGeneration || page.Entries[0].Delta != -1925 || page.Entries[0].BalanceAfter != 10075 {
		t.Fatalf("latest history entry = (%q, %d, balance %d), want generation -1925 balance 10075",
			page.Entries[0].Reason, page.Entries[0].Delta, page.Entries[0].BalanceAfter)
	}
	bal, err := ps.Balance(ctx, owner)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 10075 {
		t.Fatalf("balance = %d, want 10075", bal)
	}
	count, points, err = ps.RepairLegacyGenerationOvercharges(ctx)
	if err != nil {
		t.Fatalf("RepairLegacyGenerationOvercharges replay: %v", err)
	}
	if count != 0 || points != 0 {
		t.Fatalf("repair replay = (%d discussions, %d points), want no-op", count, points)
	}
}

func TestRepairLegacyGenerationOverchargesShiftsLaterBalances(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 12000, "purchase:TEST", "legacy-shift-credit"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `INSERT INTO points_ledger
		(user_id, discussion_id, delta, reason, balance_after, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		owner, d.ID, int64(-11037), pointsReasonGeneration, int64(963), int64(2000)); err != nil {
		t.Fatalf("insert legacy generation row: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `UPDATE user_points_balance SET balance = ? WHERE user_id = ?`, int64(963), owner); err != nil {
		t.Fatalf("set legacy balance: %v", err)
	}
	if _, err := ps.db.ExecContext(ctx, `UPDATE native_discussions SET points_charged = ? WHERE id = ?`, int64(1925), d.ID); err != nil {
		t.Fatalf("set discussion points: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 1000, "purchase:TEST", "legacy-later-credit"); err != nil {
		t.Fatalf("later Credit: %v", err)
	}

	if _, _, err := ps.RepairLegacyGenerationOvercharges(ctx); err != nil {
		t.Fatalf("RepairLegacyGenerationOvercharges: %v", err)
	}
	page, err := ps.History(ctx, owner, 10, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Entries) < 2 {
		t.Fatalf("History returned %d entries, want at least 2: %#v", len(page.Entries), page.Entries)
	}
	if page.Entries[0].Reason != "purchase:TEST" || page.Entries[0].Delta != 1000 || page.Entries[0].BalanceAfter != 11075 {
		t.Fatalf("latest history entry = (%q, %d, balance %d), want later purchase +1000 balance 11075",
			page.Entries[0].Reason, page.Entries[0].Delta, page.Entries[0].BalanceAfter)
	}
	if page.Entries[1].Reason != pointsReasonGeneration || page.Entries[1].Delta != -1925 || page.Entries[1].BalanceAfter != 10075 {
		t.Fatalf("generation history entry = (%q, %d, balance %d), want generation -1925 balance 10075",
			page.Entries[1].Reason, page.Entries[1].Delta, page.Entries[1].BalanceAfter)
	}
	bal, err := ps.Balance(ctx, owner)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 11075 {
		t.Fatalf("balance = %d, want 11075", bal)
	}
}

func TestRefundGenerationReleasesHold(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	const owner = "u1"
	d, err := ds.Create(ctx, owner, "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 1000, "signup_grant", "signup:u1"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if ok, _, err := ps.Reserve(ctx, owner, d.ID, 804, pointsReasonGeneration); err != nil || !ok {
		t.Fatalf("Reserve = ok=%v err=%v", ok, err)
	}
	// Job failed to start → release the whole reservation.
	if err := ps.Refund(ctx, owner, d.ID, 804, pointsReasonGeneration); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	bal, _ := ps.Balance(ctx, owner)
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000 (fully refunded)", bal)
	}
}

func TestGenerationPointsAppliesMinimum(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	env := &config.Env{PointsPerUSDCost: 1340, PointsMinPerPodcast: 1}
	if got := ps.GenerationPoints(env, 0.60); got != 804 {
		t.Errorf("GenerationPoints(0.60) = %d, want 804", got)
	}
	if got := ps.GenerationPoints(env, 0); got != 1 {
		t.Errorf("GenerationPoints(0) = %d, want 1 (minimum)", got)
	}
}

func TestEnsureSignupGrantOnce(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	if err := ps.EnsureSignupGrant(ctx, "u1", 250); err != nil {
		t.Fatalf("EnsureSignupGrant: %v", err)
	}
	if err := ps.EnsureSignupGrant(ctx, "u1", 250); err != nil {
		t.Fatalf("EnsureSignupGrant (replay): %v", err)
	}
	bal, err := ps.Balance(ctx, "u1")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 250 {
		t.Fatalf("balance = %d, want 250 (granted once)", bal)
	}
}

func TestHistoryPaginatesProjectedEntries(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	eventIDs := []string{"evt-page-a", "evt-page-b", "evt-page-c", "evt-page-d", "evt-page-e"}
	for i, pts := range []int64{10, 20, 30, 40, 50} {
		if _, err := ps.Credit(ctx, "u1", pts, "purchase:TEST", eventIDs[i]); err != nil {
			t.Fatalf("Credit %d: %v", i, err)
		}
	}

	page, err := ps.History(ctx, "u1", 2, 0)
	if err != nil {
		t.Fatalf("History page 1: %v", err)
	}
	if !page.HasMore || len(page.Entries) != 2 || page.Entries[0].Delta != 50 || page.Entries[1].Delta != 40 {
		t.Fatalf("page 1 = %+v, want 50/40 with has_more", page)
	}

	page, err = ps.History(ctx, "u1", 2, 2)
	if err != nil {
		t.Fatalf("History page 2: %v", err)
	}
	if !page.HasMore || len(page.Entries) != 2 || page.Entries[0].Delta != 30 || page.Entries[1].Delta != 20 {
		t.Fatalf("page 2 = %+v, want 30/20 with has_more", page)
	}

	page, err = ps.History(ctx, "u1", 2, 4)
	if err != nil {
		t.Fatalf("History page 3: %v", err)
	}
	if page.HasMore || len(page.Entries) != 1 || page.Entries[0].Delta != 10 {
		t.Fatalf("page 3 = %+v, want 10 without has_more", page)
	}
}
