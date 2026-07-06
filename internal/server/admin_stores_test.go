package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/sirily11/debate-bot/internal/config"
)

func newTestAppConfigStore(t *testing.T) (*AppConfigStore, *DiscussionStore) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "appcfg.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	ac, err := NewAppConfigStore(ds)
	if err != nil {
		t.Fatalf("NewAppConfigStore: %v", err)
	}
	return ac, ds
}

func TestAppConfigStoreGetSet(t *testing.T) {
	ac, _ := newTestAppConfigStore(t)
	ctx := context.Background()

	if _, ok, err := ac.Get(ctx, appConfigKeyDefaultHostModel); err != nil || ok {
		t.Fatalf("expected no value initially, got ok=%v err=%v", ok, err)
	}
	if err := ac.Set(ctx, appConfigKeyDefaultHostModel, "provider/model-a"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := ac.Get(ctx, appConfigKeyDefaultHostModel)
	if err != nil || !ok || v != "provider/model-a" {
		t.Fatalf("Get after Set = %q ok=%v err=%v", v, ok, err)
	}
	// Upsert overwrites.
	if err := ac.Set(ctx, appConfigKeyDefaultHostModel, "provider/model-b"); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	if v, _, _ := ac.Get(ctx, appConfigKeyDefaultHostModel); v != "provider/model-b" {
		t.Fatalf("Get after re-Set = %q", v)
	}
}

func TestResolvedModelDefaultsOverride(t *testing.T) {
	ac, _ := newTestAppConfigStore(t)
	env := &config.Env{HostModel: "env/host", ScenePlannerModel: "env/host"}
	s := &Server{d: Deps{Env: env, AppConfig: ac}}
	ctx := context.Background()

	// No override → env defaults.
	if d := s.resolvedModelDefaults(ctx); d.Host != "env/host" {
		t.Fatalf("without override Host = %q, want env/host", d.Host)
	}
	// Override moves both host and (env-linked) scene planner.
	if err := ac.Set(ctx, appConfigKeyDefaultHostModel, "admin/model"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	d := s.resolvedModelDefaults(ctx)
	if d.Host != "admin/model" {
		t.Errorf("Host = %q, want admin/model", d.Host)
	}
	if d.ScenePlanner != "admin/model" {
		t.Errorf("ScenePlanner = %q, want admin/model (env-linked)", d.ScenePlanner)
	}

	// plannerEnv returns a copy with the override applied, leaving the base Env
	// untouched.
	pe := s.plannerEnv()
	if pe.HostModel != "admin/model" {
		t.Errorf("plannerEnv HostModel = %q", pe.HostModel)
	}
	if env.HostModel != "env/host" {
		t.Errorf("base Env mutated: HostModel = %q", env.HostModel)
	}
}

func TestMaintenanceDataSourceCreateRules(t *testing.T) {
	ms := newTestMaintenanceStore(t)
	ds := newMaintenanceDataSource(ms)
	ctx := context.Background()

	// Ongoing create forces StartAt to ~now regardless of the submitted start.
	submitted := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	ongoing := &Maintenance{Title: "a", Message: "m", Status: MaintenanceStatusOngoing, StartAt: submitted}
	if err := ds.Create(ctx, ongoing); err != nil {
		t.Fatalf("create ongoing: %v", err)
	}
	if time.Since(ongoing.StartAt) > time.Minute {
		t.Errorf("ongoing StartAt not reset to now: %v", ongoing.StartAt)
	}

	// A second ongoing window is rejected — as a bad-input error so the admin UI
	// shows the message (400) instead of a generic 500.
	second := &Maintenance{Title: "b", Message: "m", Status: MaintenanceStatusOngoing, StartAt: time.Now()}
	err := ds.Create(ctx, second)
	if err == nil {
		t.Error("expected second ongoing window to be rejected")
	} else if !errors.Is(err, admin.ErrBadInput) {
		t.Errorf("expected ErrBadInput, got %v", err)
	}

	// A scheduled window overlapping the ongoing one is rejected (ongoing is
	// open-ended from now, so any later scheduled window overlaps it).
	overlap := &Maintenance{Title: "c", Message: "m", Status: MaintenanceStatusScheduled, StartAt: time.Now().Add(time.Hour)}
	if err := ds.Create(ctx, overlap); err == nil {
		t.Error("expected overlapping scheduled window to be rejected")
	}
}

func TestMaintenanceDataSourceOverlap(t *testing.T) {
	ms := newTestMaintenanceStore(t)
	ds := newMaintenanceDataSource(ms)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// Bounded scheduled window [base, base+2h].
	first := &Maintenance{Title: "a", Message: "m", Status: MaintenanceStatusScheduled, StartAt: base, EndAt: ptrTime(base.Add(2 * time.Hour))}
	if err := ds.Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}

	// Overlapping window [base+1h, base+3h] is rejected.
	over := &Maintenance{Title: "b", Message: "m", Status: MaintenanceStatusScheduled, StartAt: base.Add(time.Hour), EndAt: ptrTime(base.Add(3 * time.Hour))}
	if err := ds.Create(ctx, over); err == nil {
		t.Error("expected overlapping window to be rejected")
	}

	// A clearly-separated later window [base+3h, base+4h] (gap after first.end at
	// base+2h) does not overlap and is accepted.
	after := &Maintenance{Title: "c", Message: "m", Status: MaintenanceStatusScheduled, StartAt: base.Add(3 * time.Hour), EndAt: ptrTime(base.Add(4 * time.Hour))}
	if err := ds.Create(ctx, after); err != nil {
		t.Errorf("non-overlapping window should be accepted: %v", err)
	}

	// Editing the first window to also cover the "after" window is rejected.
	_, err := ds.Update(ctx, fmt.Sprintf("%d", first.ID), map[string]any{
		"end_at": base.Add(5 * time.Hour).Format(time.RFC3339),
	})
	if err == nil {
		t.Error("expected edit extending into another window to be rejected")
	}
}

func newTestMaintenanceStore(t *testing.T) *MaintenanceStore {
	t.Helper()
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	ms, err := NewMaintenanceStore(jobs.db)
	if err != nil {
		t.Fatalf("NewMaintenanceStore: %v", err)
	}
	return ms
}

func TestMaintenanceActive(t *testing.T) {
	ms := newTestMaintenanceStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	// No windows → not active.
	if _, ok := ms.Active(ctx, now); ok {
		t.Fatal("expected no active window")
	}

	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	// Scheduled never pauses the app — even once its start time has passed.
	ms.db.Create(&Maintenance{Title: "later", Message: "m", Status: MaintenanceStatusScheduled, StartAt: future})
	ms.db.Create(&Maintenance{Title: "started-but-scheduled", Message: "m", Status: MaintenanceStatusScheduled, StartAt: past})
	if _, ok := ms.Active(ctx, now); ok {
		t.Fatal("scheduled window should not be active regardless of start time")
	}

	// Ongoing → active regardless of its start time.
	ms.db.Create(&Maintenance{Title: "now", Message: "down for maintenance", Status: MaintenanceStatusOngoing, StartAt: future})
	m, ok := ms.Active(ctx, now)
	if !ok || m.Message != "down for maintenance" {
		t.Fatalf("expected active ongoing window, ok=%v m=%+v", ok, m)
	}

	// Ongoing stays active even past an elapsed EndAt — only "finished" stops it.
	ms.db.Model(m).Update("end_at", ptrTime(past.Add(-time.Hour)))
	if _, ok := ms.Active(ctx, now); !ok {
		t.Fatal("ongoing window past its EndAt should still be active")
	}

	// Finished → not active.
	ms.db.Model(m).Update("status", MaintenanceStatusFinished)
	if _, ok := ms.Active(ctx, now); ok {
		t.Fatal("finished window should not be active")
	}
}

func TestMaintenanceUpcoming(t *testing.T) {
	ms := newTestMaintenanceStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	// No windows → nothing upcoming.
	if _, ok := ms.Upcoming(ctx, now); ok {
		t.Fatal("expected no upcoming window")
	}

	// An active (already-started) window is not "upcoming".
	ms.db.Create(&Maintenance{Title: "now", Message: "ongoing", Status: MaintenanceStatusOngoing, StartAt: now.Add(-time.Hour)})
	if _, ok := ms.Upcoming(ctx, now); ok {
		t.Fatal("started window should not be upcoming")
	}

	// Two future windows → the soonest wins.
	ms.db.Create(&Maintenance{Title: "far", Message: "far", Status: MaintenanceStatusScheduled, StartAt: now.Add(3 * time.Hour)})
	ms.db.Create(&Maintenance{Title: "soon", Message: "soon", Status: MaintenanceStatusScheduled, StartAt: now.Add(time.Hour)})
	m, ok := ms.Upcoming(ctx, now)
	if !ok || m.Message != "soon" {
		t.Fatalf("expected soonest upcoming window, ok=%v m=%+v", ok, m)
	}

	// A finished future window is ignored.
	ms.db.Model(m).Update("status", MaintenanceStatusFinished)
	m, ok = ms.Upcoming(ctx, now)
	if !ok || m.Message != "far" {
		t.Fatalf("expected next upcoming window after finishing the soonest, ok=%v m=%+v", ok, m)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestPointsListBalances(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	for _, u := range []string{"u1", "u2", "u3"} {
		if _, err := ps.Credit(ctx, u, 100, pointsReasonAdminTopup, "seed:"+u); err != nil {
			t.Fatalf("Credit %s: %v", u, err)
		}
	}
	// First page of 2, then the remainder.
	rows, next, err := ps.ListBalances(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListBalances: %v", err)
	}
	if len(rows) != 2 || next != "u2" {
		t.Fatalf("page 1 = %+v next=%q", rows, next)
	}
	if rows[0].UserID != "u1" || rows[0].Balance != 100 {
		t.Errorf("row0 = %+v", rows[0])
	}
	rows2, next2, err := ps.ListBalances(ctx, next, 2)
	if err != nil {
		t.Fatalf("ListBalances page 2: %v", err)
	}
	if len(rows2) != 1 || next2 != "" || rows2[0].UserID != "u3" {
		t.Fatalf("page 2 = %+v next=%q", rows2, next2)
	}
}
