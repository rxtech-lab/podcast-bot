package server

import (
	"context"
	"net/http"

	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/rxtech-lab/admin-generator/adminauth/oidc"
	"github.com/rxtech-lab/admin-generator/adminhttp"
)

// adminBasePath is where the schema-driven admin API is mounted. The Next.js
// dashboard's <AdminApp> talks to this prefix; every route under it is gated by
// the OIDC authenticator + per-resource RequireRole("admin").
const adminBasePath = "/admin"

// newAdminHandler builds the admin API handler: an OIDC authenticator against
// the rxlab-auth issuer plus a registry of the app-config, maintenance, and
// users resources. It returns an error when the issuer is unreachable so the
// caller can log and continue without the admin API.
func (s *Server) newAdminHandler(ctx context.Context) (http.Handler, error) {
	var authn adminhttp.Authenticator
	if s.e2eMode() {
		// E2E mode is already isolated to a disposable local database. Give the
		// browser harness a deterministic admin identity without contacting the
		// real OIDC issuer or accepting a test credential in normal deployments.
		authn = adminhttp.AuthenticatorFunc(func(*http.Request) (admin.Identity, error) {
			return &oidc.Claims{Subject: e2eUserID, Roles: []string{"admin"}}, nil
		})
	} else {
		opts := []oidc.Option{}
		if len(s.d.AdminAllowedClientIDs) > 0 {
			opts = append(opts, oidc.WithAllowedClientIDs(s.d.AdminAllowedClientIDs...))
		}
		var err error
		authn, err = oidc.New(ctx, s.d.AuthIssuer, opts...)
		if err != nil {
			return nil, err
		}
	}

	reg := admin.NewRegistry()
	if s.d.Points != nil {
		reg.Register(s.newUsageDashboardResource())
	}
	reg.Register(s.newAppConfigResource())
	reg.Register(s.newIAPProductsResource())
	if s.d.Maintenance != nil {
		reg.Register(s.newMaintenanceResource())
	}
	if s.d.Points != nil {
		reg.Register(s.newUsersResource())
	}
	if s.d.SubscriptionPermissions != nil {
		reg.Register(s.newSubscriptionPermissionsResource())
	}

	return adminhttp.New(reg,
		adminhttp.WithBasePath(adminBasePath),
		adminhttp.WithAuthenticator(authn),
		adminhttp.WithLogger(s.logger()),
	), nil
}
