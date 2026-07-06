package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/rxtech-lab/admin-generator/adminauth/oidc"

	"github.com/sirily11/debate-bot/internal/config"
)

// adminReq builds a Request carrying an admin-role identity so the resources'
// requireAdmin() authorization passes without the network OIDC layer.
func adminReq(dynamicPath string) admin.Request {
	return admin.Request{
		Identity:    &oidc.Claims{Roles: []string{"admin"}},
		BasePath:    adminBasePath,
		DynamicPath: dynamicPath,
		Query:       url.Values{},
	}
}

func TestMaintenanceMiddleware(t *testing.T) {
	ms := newTestMaintenanceStore(t)
	s := &Server{d: Deps{Maintenance: ms}}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	h := s.withMaintenance(next)

	// No active window → everything passes.
	for _, p := range []string{"/api/discussions", "/api/config", "/admin/resources"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("no-maintenance %s = %d, want 200", p, rec.Code)
		}
	}

	// Activate a window.
	ms.db.Create(&Maintenance{Title: "t", Message: "back soon", Status: MaintenanceStatusOngoing, StartAt: time.Now().Add(-time.Minute)})

	cases := []struct {
		path string
		want int
	}{
		{"/api/discussions", http.StatusServiceUnavailable}, // blocked
		{"/api/config", http.StatusOK},                      // allowlisted
		{"/api/precheck", http.StatusOK},                    // allowlisted
		{"/api/revenuecat/webhook", http.StatusOK},          // allowlisted
		{"/admin/resources", http.StatusOK},                 // admin API stays up
		{"/healthz", http.StatusOK},                         // non-/api path
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.want {
			t.Errorf("maintenance %s = %d, want %d", c.path, rec.Code, c.want)
		}
	}
}

func TestAppConfigResource(t *testing.T) {
	ac, _ := newTestAppConfigStore(t)
	env := &config.Env{HostModel: "env/host"}
	s := &Server{d: Deps{Env: env, AppConfig: ac}}
	res := s.newAppConfigResource()
	ctx := context.Background()
	req := adminReq("")

	// Schema (requested for the view action) must be a form typed as edit so the
	// UI prefills + submits.
	raw, err := res.Schema(ctx, req, admin.ActionView)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	fs, ok := raw.(*admin.FormSchema)
	if !ok || fs.Type != admin.ActionEdit {
		t.Fatalf("Schema = %T type=%v, want *FormSchema/edit", raw, fs.Type)
	}
	if _, ok := fs.Schema.Properties.Get("default_host_model"); !ok {
		t.Fatal("form missing default_host_model property")
	}

	// Prefill returns the current effective host model.
	got, err := res.Fetch(ctx, req, admin.ActionEdit, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if m, _ := got.Data.(map[string]any); m["default_host_model"] != "env/host" {
		t.Errorf("Fetch = %#v", got.Data)
	}

	// Submitting an unknown model is rejected (not in the empty catalog).
	if _, err := res.Act(ctx, req, admin.ActionEdit, map[string]any{"default_host_model": "bogus/model"}); err == nil {
		t.Error("expected unknown-model rejection")
	}

	// Clearing (empty) is allowed and persists.
	if _, err := res.Act(ctx, req, admin.ActionEdit, map[string]any{"default_host_model": ""}); err != nil {
		t.Fatalf("Act clear: %v", err)
	}
	if v, ok, _ := ac.Get(ctx, appConfigKeyDefaultHostModel); !ok || v != "" {
		t.Errorf("cleared value = %q ok=%v", v, ok)
	}
}

func TestUsersResourceTopup(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	env := &config.Env{PointsProductGrants: map[string]int64{"points_500": 500}}
	s := &Server{d: Deps{Env: env, Points: ps}}
	res := s.newUsersResource()
	ctx := context.Background()

	// Unknown product → validation error.
	if _, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"product": "nope"}); err == nil {
		t.Error("expected unknown-product rejection")
	}

	// Valid topup credits the user.
	resp, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"product": "points_500"})
	if err != nil {
		t.Fatalf("Act topup: %v", err)
	}
	if m, _ := resp.Data.(map[string]any); m["balance"] != int64(500) {
		t.Fatalf("topup balance = %#v, want 500", resp.Data)
	}

	// The user now appears in the list with the credited balance.
	list, err := res.Fetch(ctx, adminReq(""), admin.ActionView, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("list items = %d, want 1", len(list.Items))
	}
	row, _ := list.Items[0].Data.(map[string]any)
	if row["user_id"] != "u1" || row["balance"] != int64(500) {
		t.Errorf("row = %#v", row)
	}
	if len(list.Items[0].Actions) != 1 || list.Items[0].Actions[0].Label != "Top up" {
		t.Errorf("expected a Top up row action, got %#v", list.Items[0].Actions)
	}
}

func TestMaintenanceResourceCRUD(t *testing.T) {
	ms := newTestMaintenanceStore(t)
	s := &Server{d: Deps{Maintenance: ms}}
	res := s.newMaintenanceResource()
	ctx := context.Background()

	// Create a window (start_at arrives as an ISO string, exercising the
	// DTO→model time.Time conversion through gormds).
	created, err := res.Act(ctx, adminReq(""), admin.ActionCreate, map[string]any{
		"title":    "Upgrade",
		"message":  "Back at 3pm",
		"status":   MaintenanceStatusScheduled,
		"start_at": "2026-07-07T15:00:00Z",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if m, _ := created.Data.(map[string]any); m["title"] != "Upgrade" {
		t.Fatalf("created = %#v", created.Data)
	}

	// List shows it with edit + delete row actions.
	list, err := res.Fetch(ctx, adminReq(""), admin.ActionView, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}
}

func TestAdminResourceForbidsNonAdmin(t *testing.T) {
	ac, _ := newTestAppConfigStore(t)
	s := &Server{d: Deps{Env: &config.Env{}, AppConfig: ac}}
	res := s.newAppConfigResource()
	// Identity without the admin role.
	req := admin.Request{Identity: &oidc.Claims{Roles: []string{"user"}}, BasePath: adminBasePath, Query: url.Values{}}
	if _, err := res.Schema(context.Background(), req, admin.ActionView); err != admin.ErrForbidden {
		t.Errorf("non-admin Schema = %v, want ErrForbidden", err)
	}
}
