package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// revenueCatWebhook is the subset of RevenueCat's webhook payload we consume.
// See https://www.revenuecat.com/docs/integrations/webhooks/event-types.
type revenueCatWebhook struct {
	Event struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		AppUserID     string `json:"app_user_id"`
		ProductID     string `json:"product_id"`
		Store         string `json:"store"`
		EnvironmentID string `json:"environment"`
	} `json:"event"`
}

// handleRevenueCatWebhook credits points when a user buys or renews a product.
// It authenticates with the REVENUECAT_WEBHOOK_AUTH shared secret (sent by
// RevenueCat in the Authorization header) and is idempotent on the event id so
// redelivered events never double-credit.
//
// The backend keys a user as "oauth:<subject>", and the iOS app sets its
// RevenueCat app_user_id to that same OAuth subject, so the credited user is
// "oauth:" + event.app_user_id.
func (s *Server) handleRevenueCatWebhook(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if !s.pointsEnabled() {
		http.Error(w, "points not enabled", http.StatusServiceUnavailable)
		return
	}
	secret := s.d.Env.RevenueCatWebhookAuth
	if secret == "" {
		http.Error(w, "webhook disabled", http.StatusServiceUnavailable)
		return
	}
	if !authHeaderEquals(r, secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var payload revenueCatWebhook
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	ev := payload.Event
	appUser := strings.TrimSpace(ev.AppUserID)
	if ev.ID == "" || appUser == "" {
		http.Error(w, "missing event id or app_user_id", http.StatusBadRequest)
		return
	}

	// Only actual paid purchase/renewal events grant a fresh point package.
	// Other lifecycle events are acknowledged with 200 (so RevenueCat stops
	// retrying) but credit nothing:
	//   - UNCANCELLATION just re-enables auto-renew; no new charge occurred.
	//   - PRODUCT_CHANGE is a tier switch that needs a safe delta calc, not a
	//     blind full grant; until that exists it must not credit.
	//   - CANCELLATION / EXPIRATION / BILLING_ISSUE / etc. never grant.
	switch strings.ToUpper(ev.Type) {
	case "INITIAL_PURCHASE", "RENEWAL", "NON_RENEWING_PURCHASE":
		grant := s.d.Env.PointsProductGrants[ev.ProductID]
		if grant <= 0 {
			s.logger().Warn("revenuecat webhook: no grant configured for product",
				"product", ev.ProductID, "event_type", ev.Type)
			writeJSON(w, map[string]any{"ok": true, "credited": 0})
			return
		}
		userID := "oauth:" + appUser
		reason := pointsReasonPurchase + ":" + strings.ToUpper(ev.Type)
		bal, err := s.d.Points.Credit(r.Context(), userID, grant, reason, ev.ID)
		if err != nil {
			s.logger().Error("revenuecat webhook credit failed", "event", ev.ID, "user", userID, "err", err)
			http.Error(w, "credit failed", http.StatusInternalServerError)
			return
		}
		s.logger().Info("revenuecat webhook credited points",
			"event", ev.ID, "user", userID, "product", ev.ProductID, "granted", grant, "balance", bal)
		writeJSON(w, map[string]any{"ok": true, "credited": grant, "balance": bal})
	default:
		writeJSON(w, map[string]any{"ok": true, "credited": 0})
	}
}

// authHeaderEquals reports whether the request's Authorization header matches
// the expected secret, using a constant-time compare. RevenueCat sends the
// configured value verbatim (it may or may not include a scheme prefix), so we
// also accept a "Bearer <secret>" form.
func authHeaderEquals(r *http.Request, secret string) bool {
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1 {
		return true
	}
	if tok := bearerToken(r); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) == 1
	}
	return false
}
