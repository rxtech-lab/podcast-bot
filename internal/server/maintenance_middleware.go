package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// maintenanceInfo is the client-facing shape describing an active maintenance
// window, surfaced both in the 503 body (for blocked routes) and inline in
// GET /api/config and GET /api/precheck so clients can show a banner/message.
type maintenanceInfo struct {
	// ID identifies the window so clients can de-duplicate a scheduled heads-up
	// (show it once) while still always showing an active/ongoing pause.
	ID      uint       `json:"id"`
	Title   string     `json:"title,omitempty"`
	Message string     `json:"message"`
	StartAt time.Time  `json:"start_at"`
	EndAt   *time.Time `json:"end_at,omitempty"`
	// Active is true when the window is currently pausing the app (within
	// [StartAt, EndAt]); false when it is an upcoming scheduled window.
	Active bool `json:"active"`
}

func maintenanceInfoFrom(m *Maintenance, active bool) *maintenanceInfo {
	if m == nil {
		return nil
	}
	return &maintenanceInfo{
		ID:      m.ID,
		Title:   m.Title,
		Message: m.Message,
		StartAt: m.StartAt,
		EndAt:   m.EndAt,
		Active:  active,
	}
}

// currentMaintenance returns the active maintenance window (Active=true) as
// client-facing info, or nil when the app is not paused. This drives the 503
// gate, so it never reports upcoming windows.
func (s *Server) currentMaintenance(r *http.Request) *maintenanceInfo {
	if s.d.Maintenance == nil {
		return nil
	}
	if m, ok := s.d.Maintenance.Active(r.Context(), time.Now()); ok {
		return maintenanceInfoFrom(m, true)
	}
	return nil
}

// relevantMaintenance returns the window clients should surface: the active one
// (Active=true) if the app is paused, otherwise the next upcoming scheduled one
// (Active=false) so clients can warn users ahead of a planned pause. Used by the
// allowlisted /api/config and /api/precheck responses, not the 503 gate.
func (s *Server) relevantMaintenance(r *http.Request) *maintenanceInfo {
	if s.d.Maintenance == nil {
		return nil
	}
	now := time.Now()
	if m, ok := s.d.Maintenance.Active(r.Context(), now); ok {
		return maintenanceInfoFrom(m, true)
	}
	if m, ok := s.d.Maintenance.Upcoming(r.Context(), now); ok {
		return maintenanceInfoFrom(m, false)
	}
	return nil
}

// maintenanceAllowlisted reports whether a path stays reachable during an active
// maintenance window: everything outside /api/ (the admin API, static assets),
// plus the endpoints clients need to observe the pause and for the operator to
// end it.
func maintenanceAllowlisted(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return true // /admin/*, static, health-at-root, etc.
	}
	switch path {
	case "/api/config", "/api/precheck", "/api/login", "/api/logout", "/api/revenuecat/webhook":
		return true
	}
	return false
}

// withMaintenance hard-blocks all /api/* routes with 503 while a maintenance
// window is active, except the allowlist above. It wraps OUTSIDE the auth
// middleware so the maintenance message reaches unauthenticated clients too.
func (s *Server) withMaintenance(next http.Handler) http.Handler {
	if s.d.Maintenance == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if maintenanceAllowlisted(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if info := s.currentMaintenance(r); info != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"maintenance": info})
			return
		}
		next.ServeHTTP(w, r)
	})
}
