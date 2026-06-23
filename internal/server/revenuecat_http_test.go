package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func newWebhookServer(t *testing.T) (*Server, *PointsStore) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "wh.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	ps, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}
	srv := New(Deps{
		Mode:        ModeDashboard,
		Discussions: ds,
		Points:      ps,
		Env: &config.Env{
			RevenueCatWebhookAuth: "shh",
			PointsProductGrants:   map[string]int64{"consumable": 1000},
		},
	})
	return srv, ps
}

func postWebhook(t *testing.T, srv *Server, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/revenuecat/webhook", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	srv.handleRevenueCatWebhook(rec, req)
	return rec
}

func TestRevenueCatWebhookCreditsAndIsIdempotent(t *testing.T) {
	srv, ps := newWebhookServer(t)
	ctx := context.Background()
	body := `{"event":{"id":"evt-1","type":"INITIAL_PURCHASE","app_user_id":"sub123","product_id":"consumable"}}`

	if rec := postWebhook(t, srv, "shh", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	bal, err := ps.Balance(ctx, "oauth:sub123")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000", bal)
	}
	// Redelivered event must not double-credit.
	if rec := postWebhook(t, srv, "shh", body); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rec.Code)
	}
	bal, _ = ps.Balance(ctx, "oauth:sub123")
	if bal != 1000 {
		t.Fatalf("balance after replay = %d, want 1000", bal)
	}
}

func TestRevenueCatWebhookRejectsBadSecret(t *testing.T) {
	srv, ps := newWebhookServer(t)
	body := `{"event":{"id":"evt-2","type":"INITIAL_PURCHASE","app_user_id":"sub9","product_id":"consumable"}}`
	rec := postWebhook(t, srv, "wrong", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	bal, _ := ps.Balance(context.Background(), "oauth:sub9")
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (rejected)", bal)
	}
}

func TestRevenueCatWebhookBearerSecretAccepted(t *testing.T) {
	srv, ps := newWebhookServer(t)
	body := `{"event":{"id":"evt-3","type":"RENEWAL","app_user_id":"sub5","product_id":"consumable"}}`
	if rec := postWebhook(t, srv, "Bearer shh", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	bal, _ := ps.Balance(context.Background(), "oauth:sub5")
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000", bal)
	}
}
