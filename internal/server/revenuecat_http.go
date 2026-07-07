package server

import (
	"context"
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
		ExpirationAt  int64  `json:"expiration_at_ms"`
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
	if s.d.Env == nil {
		http.Error(w, "webhook disabled", http.StatusServiceUnavailable)
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
	eventType := strings.ToUpper(strings.TrimSpace(ev.Type))
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
	switch eventType {
	case "INITIAL_PURCHASE", "RENEWAL", "NON_RENEWING_PURCHASE":
		productID := strings.TrimSpace(ev.ProductID)
		if s.d.IAPProducts == nil {
			s.logger().Warn("revenuecat webhook: product catalog unavailable", "product", productID)
			writeRevenueCatWebhookError(w, http.StatusBadRequest, "invalid_product_id")
			return
		}
		product, ok, err := s.d.IAPProducts.FindEnabled(r.Context(), productID, ev.EnvironmentID)
		if err != nil {
			s.logger().Error("revenuecat webhook product lookup failed", "product", productID, "err", err)
			http.Error(w, "product lookup failed", http.StatusInternalServerError)
			return
		}
		if !ok {
			s.logger().Warn("revenuecat webhook: rejected unconfigured product",
				"product", productID, "event_type", ev.Type, "store", ev.Store, "environment", ev.EnvironmentID)
			writeRevenueCatWebhookError(w, http.StatusBadRequest, "invalid_product_id")
			return
		}
		userID := "oauth:" + appUser
		exists, err := s.d.Points.UserExists(r.Context(), userID)
		if err != nil {
			s.logger().Error("revenuecat webhook user check failed", "event", ev.ID, "user", userID, "err", err)
			http.Error(w, "user check failed", http.StatusInternalServerError)
			return
		}
		if !exists {
			s.logger().Warn("revenuecat webhook: rejected unknown app_user_id",
				"event", ev.ID, "user", userID, "product", productID)
			writeRevenueCatWebhookError(w, http.StatusBadRequest, "invalid_user_id")
			return
		}
		reason := pointsReasonPurchase + ":" + eventType
		bal, applied, err := s.d.Points.CreditWithResult(r.Context(), userID, product.PointsGrant, reason, ev.ID)
		if err != nil {
			s.logger().Error("revenuecat webhook credit failed", "event", ev.ID, "user", userID, "err", err)
			http.Error(w, "credit failed", http.StatusInternalServerError)
			return
		}
		credited := product.PointsGrant
		if !applied {
			credited = 0
		}
		s.logger().Info("revenuecat webhook credited points",
			"event", ev.ID, "user", userID, "product", productID, "granted", credited, "balance", bal, "duplicate", !applied)
		s.recordRevenueCatSubscription(r.Context(), userID, product, eventType, ev.ID, ev.ExpirationAt)
		writeJSON(w, map[string]any{"ok": true, "credited": credited, "balance": bal, "duplicate": !applied})
	case "CANCELLATION", "EXPIRATION", "BILLING_ISSUE", "UNCANCELLATION", "PRODUCT_CHANGE":
		productID := strings.TrimSpace(ev.ProductID)
		userID := "oauth:" + appUser
		if productID != "" && s.d.IAPProducts != nil {
			exists := true
			if s.d.Points != nil {
				var err error
				exists, err = s.d.Points.UserExists(r.Context(), userID)
				if err != nil {
					s.logger().Warn("revenuecat webhook lifecycle user check failed", "event", ev.ID, "user", userID, "err", err)
					exists = false
				}
			}
			if !exists {
				writeJSON(w, map[string]any{"ok": true, "credited": 0})
				return
			}
			if product, ok, err := s.d.IAPProducts.FindEnabled(r.Context(), productID, ev.EnvironmentID); err != nil {
				s.logger().Warn("revenuecat webhook subscription lookup failed", "event", ev.ID, "product", productID, "err", err)
			} else if ok {
				s.recordRevenueCatSubscription(r.Context(), userID, product, eventType, ev.ID, ev.ExpirationAt)
			}
		}
		writeJSON(w, map[string]any{"ok": true, "credited": 0})
	default:
		writeRevenueCatWebhookError(w, http.StatusBadRequest, "invalid_event_type")
	}
}

func (s *Server) recordRevenueCatSubscription(ctx context.Context, userID string, product *IAPProduct, eventType, eventID string, expiresAtMS int64) {
	if s == nil || s.d.Points == nil || product == nil || product.ProductType != IAPProductTypeSubscription {
		return
	}
	status := revenueCatSubscriptionStatus(eventType)
	if status == "" {
		return
	}
	if err := s.d.Points.RecordSubscription(ctx, userID, *product, status, eventID, expiresAtMS); err != nil {
		s.logger().Warn("revenuecat webhook subscription record failed",
			"event", eventID, "user", userID, "product", product.ProductID, "err", err)
	}
}

func revenueCatSubscriptionStatus(eventType string) string {
	switch strings.ToUpper(strings.TrimSpace(eventType)) {
	case "INITIAL_PURCHASE", "RENEWAL", "NON_RENEWING_PURCHASE", "UNCANCELLATION", "PRODUCT_CHANGE":
		return "active"
	case "CANCELLATION":
		return "cancelled"
	case "EXPIRATION":
		return "expired"
	case "BILLING_ISSUE":
		return "billing_issue"
	default:
		return ""
	}
}

func writeRevenueCatWebhookError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
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
