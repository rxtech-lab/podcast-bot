package server

import (
	"net/http"
	"strings"
)

// withCORS allows the listed browser origins to call the API cross-origin.
// It echoes the request's Origin back (rather than "*") so credentialed
// requests are permitted, and short-circuits CORS preflight (OPTIONS) with
// 204 before any auth check runs — preflight requests carry no credentials.
//
// A single "*" entry in allowed disables the allow-list and reflects every
// origin (handy for local development; not recommended in production).
func withCORS(allowed []string, next http.Handler) http.Handler {
	allowAll := false
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
		}
		if o != "" {
			set[o] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			_, ok := set[origin]
			if allowAll || ok {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				h.Set("Access-Control-Max-Age", "600")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
