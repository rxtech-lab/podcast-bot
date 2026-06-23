package server

import (
	"context"
	"fmt"
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
