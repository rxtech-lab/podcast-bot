package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/rxtech-lab/admin-generator/adminauth/oidc"
	"github.com/sirily11/debate-bot/internal/config"
)

// requireAdmin is the authorization hook shared by every admin resource: the
// bearer token must carry the "admin" role (see rxlab-auth per-client roles).
func requireAdmin() func(context.Context, admin.Identity, admin.ActionType) error {
	return oidc.RequireRole("admin")
}

// ---------------------------------------------------------------------------
// App configuration (form resource)
// ---------------------------------------------------------------------------

type appConfigForm struct {
	DefaultHostModel string `json:"default_host_model" jsonschema:"title=Default generation model"`
}

// newAppConfigResource serves the single app-level configuration form. The
// default-model field is a dropdown populated live from the model catalog, and
// submitting persists the override (env stays the fallback).
func (s *Server) newAppConfigResource() admin.Resource {
	return admin.NewFormResource(admin.FormResourceConfig{
		ID:          "app-config",
		Name:        "App Config",
		Description: "Application-level settings. Overrides the env defaults.",
		Icon:        "settings",
		Authorize:   requireAdmin(),
		Schema: func(ctx context.Context, req admin.Request, _ admin.ActionType) (*admin.FormSchema, error) {
			fs, err := admin.FormSchemaFromModel(appConfigForm{}, admin.ActionEdit, "Save",
				req.BasePath+"/resources/app-config/action?action=edit")
			if err != nil {
				return nil, err
			}
			if p, ok := fs.Schema.Properties.Get("default_host_model"); ok {
				p.Description = "The default model used when generating new content. Fetched live from the gateway."
				p.OneOf = modelOptions(s.modelCatalog(ctx))
			}
			return fs, nil
		},
		Fetch: func(ctx context.Context, _ admin.Request, _ admin.ActionType, _ map[string]any) (*admin.ActionResponse, error) {
			d := s.resolvedModelDefaults(ctx)
			return admin.Detail(map[string]any{"default_host_model": d.Host}), nil
		},
		Act: func(ctx context.Context, _ admin.Request, _ admin.ActionType, data map[string]any) (*admin.ActionResponse, error) {
			model, _ := data["default_host_model"].(string)
			model = strings.TrimSpace(model)
			if model != "" && !s.modelExists(ctx, model) {
				return nil, fmt.Errorf("%w: unknown model %q", admin.ErrBadInput, model)
			}
			if s.d.AppConfig == nil {
				return nil, fmt.Errorf("%w: app config store unavailable", admin.ErrBadInput)
			}
			if err := s.d.AppConfig.Set(ctx, appConfigKeyDefaultHostModel, model); err != nil {
				return nil, err
			}
			return admin.Detail(map[string]any{"default_host_model": model}), nil
		},
	})
}

// modelOptions builds oneOf {const,title} entries so RJSF renders a labeled
// dropdown of model ids.
func modelOptions(models []config.ModelInfo) []*jsonschema.Schema {
	opts := make([]*jsonschema.Schema, 0, len(models))
	for _, m := range models {
		label := m.ID
		if m.Provider != "" {
			label = fmt.Sprintf("%s (%s)", m.ID, m.Provider)
		}
		opts = append(opts, &jsonschema.Schema{Const: m.ID, Title: label})
	}
	return opts
}

// modelExists reports whether id is in the live catalog.
func (s *Server) modelExists(ctx context.Context, id string) bool {
	for _, m := range s.modelCatalog(ctx) {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Scheduled maintenance (generic CRUD resource)
// ---------------------------------------------------------------------------

func (s *Server) newMaintenanceResource() admin.Resource {
	return admin.NewResource[Maintenance](admin.ResourceConfig[Maintenance]{
		ID:          "maintenance",
		Name:        "Maintenance",
		Description: "Schedule maintenance windows that pause the app and show a message to users.",
		Icon:        "wrench",
		// Wrapped DataSource enforces the write rules (single ongoing window,
		// no overlaps, ongoing => start now) the form tags can't express.
		DataSource:  newMaintenanceDataSource(s.d.Maintenance),
		CreateForm:  maintenanceForm{},
		EditForm:    maintenanceForm{},
		Authorize:   requireAdmin(),
		Actions:     []admin.ActionType{admin.ActionView, admin.ActionCreate, admin.ActionEdit, admin.ActionDelete},
	})
}

// ---------------------------------------------------------------------------
// User management + topup (custom resource: table + per-row topup form)
// ---------------------------------------------------------------------------

// usersResource lists user balances and exposes a per-row "Top up" form action.
// It is a hand-written admin.Resource because the topup is a bespoke action
// (credit points from a selected product) rather than a generic CRUD update,
// and the list is backed by the points store rather than a GORM model.
type usersResource struct{ s *Server }

func (s *Server) newUsersResource() admin.Resource { return &usersResource{s: s} }

func (r *usersResource) ID() string { return "users" }

func (r *usersResource) actionURL(req admin.Request, action admin.ActionType, dynamicPath string) string {
	u := req.BasePath + "/resources/users/action?action=" + string(action)
	if dynamicPath != "" {
		u += "&dynamicPath=" + dynamicPath
	}
	return u
}

func (r *usersResource) authorize(ctx context.Context, req admin.Request, action admin.ActionType) error {
	return requireAdmin()(ctx, req.Identity, action)
}

func (r *usersResource) Info(_ context.Context, req admin.Request) admin.ResourceInfo {
	return admin.ResourceInfo{
		ID:            "users",
		Name:          "Users",
		Description:   "User balances. Top up a user by selecting a product.",
		Icon:          "users",
		Type:          admin.ResourceTable,
		DataURL:       r.actionURL(req, admin.ActionView, ""),
		DefaultAction: admin.ActionView,
		// Non-nil (no top-level create): serializes to [] rather than null so the
		// UI's supportedActions.some(...) check does not crash.
		SupportedActions: []admin.ActionButton{},
	}
}

func (r *usersResource) Schema(ctx context.Context, req admin.Request, action admin.ActionType) (any, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	switch action {
	case admin.ActionView:
		return &admin.TableSchema{
			UIType: "table",
			Type:   admin.ActionView,
			Columns: []admin.TableColumn{
				{Name: "user_id", Label: "User ID", Type: "string", Pinned: true},
				{Name: "display_name", Label: "Name", Type: "string"},
				{Name: "balance", Label: "Balance", Type: "number"},
			},
		}, nil
	case admin.ActionEdit:
		return r.topupSchema(ctx, req)
	default:
		return nil, fmt.Errorf("%w: no schema for action %q", admin.ErrBadInput, action)
	}
}

// topupSchema is the per-user topup form: a product dropdown built from the
// configured product→grant map, with the user's current balance shown in the
// description.
func (r *usersResource) topupSchema(ctx context.Context, req admin.Request) (*admin.FormSchema, error) {
	userID := strings.Trim(req.DynamicPath, "/")
	fs, err := admin.FormSchemaFromModel(topupForm{}, admin.ActionEdit, "Top up",
		r.actionURL(req, admin.ActionEdit, userID))
	if err != nil {
		return nil, err
	}
	balanceNote := ""
	if r.s.d.Points != nil && userID != "" {
		if bal, err := r.s.d.Points.Balance(ctx, userID); err == nil {
			balanceNote = fmt.Sprintf(" Current balance: %d points.", bal)
		}
	}
	if p, ok := fs.Schema.Properties.Get("product"); ok {
		p.Description = "Select a product to grant its points to the user." + balanceNote
		p.OneOf = r.s.productOptions()
	}
	return fs, nil
}

func (r *usersResource) Fetch(ctx context.Context, req admin.Request, action admin.ActionType, _ map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	switch action {
	case admin.ActionView:
		return r.list(ctx, req)
	case admin.ActionEdit:
		// Prefill: no product preselected.
		return admin.Detail(map[string]any{"product": ""}), nil
	default:
		return nil, fmt.Errorf("%w: cannot fetch action %q", admin.ErrBadInput, action)
	}
}

func (r *usersResource) list(ctx context.Context, req admin.Request) (*admin.ActionResponse, error) {
	if r.s.d.Points == nil {
		return admin.Paginated(nil, nil, nil, nil), nil
	}
	limit := 20
	if l, err := strconv.Atoi(req.Query.Get("limit")); err == nil && l > 0 {
		limit = l
	}
	rows, next, err := r.s.d.Points.ListBalances(ctx, req.Query.Get("after"), limit)
	if err != nil {
		return nil, err
	}
	items := make([]admin.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, admin.Item{
			Data: map[string]any{
				"user_id":      row.UserID,
				"display_name": row.DisplayName,
				"balance":      row.Balance,
			},
			Actions: []admin.ActionButton{{
				Type:       admin.ButtonPrimary,
				Label:      "Top up",
				Icon:       "plus",
				Behavior:   admin.BehaviorOpenSheet,
				ActionType: admin.ActionEdit,
				OnClick:    r.actionURL(req, admin.ActionEdit, row.UserID),
			}},
		})
	}
	var nextURL *string
	if next != "" {
		u := r.actionURL(req, admin.ActionView, "") + "&after=" + next + "&limit=" + strconv.Itoa(limit)
		nextURL = &u
	}
	return admin.Paginated(items, nil, nextURL, nil), nil
}

func (r *usersResource) Act(ctx context.Context, req admin.Request, action admin.ActionType, data map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	if action != admin.ActionEdit {
		return nil, fmt.Errorf("%w: cannot execute action %q", admin.ErrBadInput, action)
	}
	userID := strings.Trim(req.DynamicPath, "/")
	if userID == "" {
		return nil, fmt.Errorf("%w: missing user id", admin.ErrBadInput)
	}
	product, _ := data["product"].(string)
	product = strings.TrimSpace(product)
	if product == "" {
		return nil, &admin.ValidationError{Fields: map[string]string{"product": "required"}}
	}
	if r.s.d.Env == nil {
		return nil, fmt.Errorf("%w: products unavailable", admin.ErrBadInput)
	}
	grant, ok := r.s.d.Env.PointsProductGrants[product]
	if !ok {
		return nil, &admin.ValidationError{Fields: map[string]string{"product": "unknown product"}}
	}
	if r.s.d.Points == nil {
		return nil, fmt.Errorf("%w: points unavailable", admin.ErrBadInput)
	}
	if err := r.s.d.Points.EnsureUser(ctx, userID); err != nil {
		return nil, err
	}
	balance, err := r.s.d.Points.Credit(ctx, userID, grant, pointsReasonAdminTopup, randomEventID("admin_topup:"))
	if err != nil {
		return nil, err
	}
	return admin.Detail(map[string]any{
		"user_id": userID,
		"product": product,
		"granted": grant,
		"balance": balance,
	}), nil
}

// topupForm is the DTO reflected into the topup form.
type topupForm struct {
	Product string `json:"product" jsonschema:"title=Product" validate:"required"`
}

// productOptions builds oneOf {const,title} entries from the configured product
// grants so the topup dropdown shows "<product id> (+N points)".
func (s *Server) productOptions() []*jsonschema.Schema {
	if s.d.Env == nil {
		return nil
	}
	ids := make([]string, 0, len(s.d.Env.PointsProductGrants))
	for id := range s.d.Env.PointsProductGrants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	opts := make([]*jsonschema.Schema, 0, len(ids))
	for _, id := range ids {
		opts = append(opts, &jsonschema.Schema{
			Const: id,
			Title: fmt.Sprintf("%s (+%d points)", id, s.d.Env.PointsProductGrants[id]),
		})
	}
	return opts
}

// randomEventID returns a unique idempotency key for an admin-initiated ledger
// entry (each admin topup is a distinct event).
func randomEventID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return prefix + "fallback"
	}
	return prefix + hex.EncodeToString(b)
}
