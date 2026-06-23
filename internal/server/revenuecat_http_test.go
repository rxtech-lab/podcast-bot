package server

import (
	"context"
	"encoding/json"
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
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func assertWebhookJSONError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantError string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, wantStatus, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if body.Error != wantError {
		t.Fatalf("error = %q, want %q", body.Error, wantError)
	}
}

func assertWebhookCredit(t *testing.T, rec *httptest.ResponseRecorder, credited, balance int64, duplicate bool) {
	t.Helper()
	var body struct {
		Credited  int64 `json:"credited"`
		Balance   int64 `json:"balance"`
		Duplicate bool  `json:"duplicate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode credit body %q: %v", rec.Body.String(), err)
	}
	if body.Credited != credited || body.Balance != balance || body.Duplicate != duplicate {
		t.Fatalf("credit body = {credited:%d balance:%d duplicate:%t}, want {credited:%d balance:%d duplicate:%t}",
			body.Credited, body.Balance, body.Duplicate, credited, balance, duplicate)
	}
}

func registerWebhookUser(t *testing.T, ps *PointsStore, subject string) {
	t.Helper()
	if err := ps.EnsureUser(context.Background(), "oauth:"+subject); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
}

func TestRevenueCatWebhookCreditsAndIsIdempotent(t *testing.T) {
	srv, ps := newWebhookServer(t)
	ctx := context.Background()
	registerWebhookUser(t, ps, "sub123")
	body := `{"event":{"id":"evt-1","type":"INITIAL_PURCHASE","app_user_id":"sub123","product_id":"consumable"}}`

	rec := postWebhook(t, srv, "shh", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	assertWebhookCredit(t, rec, 1000, 1000, false)
	bal, err := ps.Balance(ctx, "oauth:sub123")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000", bal)
	}
	// Redelivered event must not double-credit.
	rec = postWebhook(t, srv, "shh", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rec.Code)
	}
	assertWebhookCredit(t, rec, 0, 1000, true)
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
	registerWebhookUser(t, ps, "sub5")
	body := `{"event":{"id":"evt-3","type":"RENEWAL","app_user_id":"sub5","product_id":"consumable"}}`
	if rec := postWebhook(t, srv, "Bearer shh", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	bal, _ := ps.Balance(context.Background(), "oauth:sub5")
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000", bal)
	}
}

func TestRevenueCatWebhookRejectsUnknownProduct(t *testing.T) {
	srv, ps := newWebhookServer(t)
	registerWebhookUser(t, ps, "sub6")
	body := `{"event":{"id":"evt-4","type":"INITIAL_PURCHASE","app_user_id":"sub6","product_id":"missing_product"}}`

	rec := postWebhook(t, srv, "shh", body)
	assertWebhookJSONError(t, rec, http.StatusBadRequest, "invalid_product_id")
	bal, _ := ps.Balance(context.Background(), "oauth:sub6")
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (rejected)", bal)
	}
}

func TestRevenueCatWebhookRejectsUnknownUser(t *testing.T) {
	srv, ps := newWebhookServer(t)
	body := `{"event":{"id":"evt-5","type":"INITIAL_PURCHASE","app_user_id":"missing-user","product_id":"consumable"}}`

	rec := postWebhook(t, srv, "shh", body)
	assertWebhookJSONError(t, rec, http.StatusBadRequest, "invalid_user_id")
	bal, _ := ps.Balance(context.Background(), "oauth:missing-user")
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (rejected)", bal)
	}
}

func TestRevenueCatWebhookRejectsInvalidEventType(t *testing.T) {
	srv, ps := newWebhookServer(t)
	registerWebhookUser(t, ps, "sub7")
	body := `{"event":{"id":"evt-6","type":"NOT_A_REVENUECAT_EVENT","app_user_id":"sub7","product_id":"consumable"}}`

	rec := postWebhook(t, srv, "shh", body)
	assertWebhookJSONError(t, rec, http.StatusBadRequest, "invalid_event_type")
	bal, _ := ps.Balance(context.Background(), "oauth:sub7")
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (rejected)", bal)
	}
}
