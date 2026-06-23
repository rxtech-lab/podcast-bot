package server

import (
	"context"
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
		{0.05, 67},   // ceil(67.0)
		{0.60, 804},  // 30-min podcast at observed cost
		{0.0001, 1},  // tiny cost still rounds up to 1
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
	bal, err = ps.SettlePlanning(ctx, owner, d.ID, 50, 20, PointsUsageDetail{CostUSD: 0.015})
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
