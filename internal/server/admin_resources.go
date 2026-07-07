package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
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
// Usage dashboard (custom page resource)
// ---------------------------------------------------------------------------

type usageDashboardResource struct{ s *Server }

func (s *Server) newUsageDashboardResource() admin.Resource {
	return &usageDashboardResource{s: s}
}

func (r *usageDashboardResource) ID() string { return "usage-dashboard" }

func (r *usageDashboardResource) actionURL(req admin.Request, action admin.ActionType) string {
	return req.BasePath + "/resources/" + r.ID() + "/action?action=" + url.QueryEscape(string(action))
}

func (r *usageDashboardResource) authorize(ctx context.Context, req admin.Request, action admin.ActionType) error {
	return requireAdmin()(ctx, req.Identity, action)
}

func (r *usageDashboardResource) Info(_ context.Context, req admin.Request) admin.ResourceInfo {
	return admin.ResourceInfo{
		ID:            r.ID(),
		Name:          "Usage Dashboard",
		Description:   "Daily provider spend, tokens, voice, image, and media usage.",
		Icon:          "chart-no-axes-combined",
		Type:          admin.ResourceCustom,
		DataURL:       r.actionURL(req, admin.ActionView),
		DefaultAction: admin.ActionView,
		SupportedActions: []admin.ActionButton{{
			Type:       admin.ButtonSecondary,
			Label:      "Refresh",
			Icon:       "refresh-cw",
			Behavior:   admin.BehaviorNavigate,
			ActionType: admin.ActionView,
			OnClick:    req.BasePath + "/" + r.ID(),
		}},
	}
}

func (r *usageDashboardResource) Schema(ctx context.Context, req admin.Request, action admin.ActionType) (any, error) {
	if action == "" {
		action = admin.ActionView
	}
	if action != admin.ActionView {
		return nil, fmt.Errorf("%w: no schema for action %q", admin.ErrBadInput, action)
	}
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	if r.s == nil || r.s.d.Points == nil {
		return nil, fmt.Errorf("%w: points store unavailable", admin.ErrBadInput)
	}
	summary, err := r.s.d.Points.UsageSpendByDate(ctx, 14)
	if err != nil {
		return nil, err
	}
	page := usageSpendCustomPage(req, summary)
	return &page, nil
}

func (r *usageDashboardResource) Fetch(ctx context.Context, req admin.Request, action admin.ActionType, _ map[string]any) (*admin.ActionResponse, error) {
	if action == "" {
		action = admin.ActionView
	}
	if action != admin.ActionView {
		return nil, fmt.Errorf("%w: cannot fetch action %q", admin.ErrBadInput, action)
	}
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	return admin.Detail(map[string]any{}), nil
}

func (r *usageDashboardResource) Act(ctx context.Context, req admin.Request, action admin.ActionType, _ map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: action %q not supported by resource %q", admin.ErrBadInput, action, r.ID())
}

func usageSpendCustomPage(req admin.Request, summary UsageSpendSummary) admin.CustomResourcePage {
	rows := make([]map[string]any, 0, len(summary.Days))
	var totalCost, totalLLM, totalTTS, totalImage, totalMusic, totalOther float64
	var totalTokens, promptTokens, completionTokens int64
	for _, day := range summary.Days {
		rows = append(rows, map[string]any{
			"date":              day.Date,
			"total_cost_usd":    roundCents(day.TotalCostUSD),
			"llm_cost_usd":      roundCents(day.LLMCostUSD),
			"tts_cost_usd":      roundCents(day.TTSCostUSD),
			"image_cost_usd":    roundCents(day.ImageCostUSD),
			"music_cost_usd":    roundCents(day.MusicCostUSD),
			"other_cost_usd":    roundCents(day.OtherCostUSD),
			"prompt_tokens":     day.PromptTokens,
			"completion_tokens": day.CompletionTokens,
			"total_tokens":      day.TotalTokens,
		})
		totalCost += day.TotalCostUSD
		totalLLM += day.LLMCostUSD
		totalTTS += day.TTSCostUSD
		totalImage += day.ImageCostUSD
		totalMusic += day.MusicCostUSD
		totalOther += day.OtherCostUSD
		promptTokens += day.PromptTokens
		completionTokens += day.CompletionTokens
		totalTokens += day.TotalTokens
	}

	return admin.CustomResourcePage{
		UIType: "custom",
		Type:   admin.ActionView,
		ActionButtons: []admin.ActionButton{{
			Type:       admin.ButtonSecondary,
			Label:      "Refresh",
			Icon:       "refresh-cw",
			Behavior:   admin.BehaviorNavigate,
			ActionType: admin.ActionView,
			OnClick:    req.BasePath + "/usage-dashboard",
		}},
		Sections: []admin.CustomPageSection{
			{
				Type:        admin.CustomPageSectionStatistics,
				Title:       "Last 14 days",
				Description: "Private admin-only totals from the points ledger.",
				Statistics: []admin.Statistic{
					{Label: "Provider spend", Value: formatUSD(totalCost), Description: "Total metered cost"},
					{Label: "LLM tokens", Value: formatCompactInt(totalTokens), Description: fmt.Sprintf("%s total, %s prompt, %s completion", formatDelimitedInt(totalTokens), formatDelimitedInt(promptTokens), formatDelimitedInt(completionTokens))},
					{Label: "Azure voice spend", Value: formatUSD(totalTTS), Description: "Ledger TTS cost"},
					{Label: "Image gen spend", Value: formatUSD(totalImage), Description: "Image generation ledger rows"},
				},
			},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Daily spend by provider",
				Description: "Dates are grouped in UTC.",
				Children: []admin.Chart{{
					Type:  admin.ChartTypeBar,
					Title: "Provider cost",
					Data:  rows,
					XKey:  "date",
					Series: []admin.ChartSeries{
						{Key: "llm_cost_usd", Label: "LLM", Color: "#2563eb"},
						{Key: "tts_cost_usd", Label: "Azure voice", Color: "#16a34a"},
						{Key: "image_cost_usd", Label: "Image gen", Color: "#dc2626"},
						{Key: "music_cost_usd", Label: "Music", Color: "#9333ea"},
						{Key: "other_cost_usd", Label: "Other", Color: "#f59e0b"},
					},
				}},
			},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Daily token spend",
				Description: "Prompt, completion, and total tokens from metered LLM calls.",
				Children: []admin.Chart{{
					Type:  admin.ChartTypeLine,
					Title: "Tokens",
					Data:  rows,
					XKey:  "date",
					Series: []admin.ChartSeries{
						{Key: "prompt_tokens", Label: "Prompt", Color: "#2563eb"},
						{Key: "completion_tokens", Label: "Completion", Color: "#16a34a"},
						{Key: "total_tokens", Label: "Total", Color: "#dc2626"},
					},
				}},
			},
			{
				Type:  admin.CustomPageSectionText,
				Title: "Accounting notes",
				Body: fmt.Sprintf("LLM: %s\nAzure voice / TTS: %s\nImage generation: %s\nMusic: %s\nOther provider cost: %s",
					formatUSD(totalLLM), formatUSD(totalTTS), formatUSD(totalImage), formatUSD(totalMusic), formatUSD(totalOther)),
			},
		},
	}
}

func roundCents(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func formatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

func formatCompactInt(v int64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}

	type compactUnit struct {
		value  int64
		suffix string
	}
	for _, unit := range []compactUnit{
		{value: 1_000_000_000, suffix: "b"},
		{value: 1_000_000, suffix: "m"},
		{value: 1_000, suffix: "k"},
	} {
		if v >= unit.value {
			if v%unit.value == 0 {
				return sign + strconv.FormatInt(v/unit.value, 10) + unit.suffix
			}
			scaled := float64(v) / float64(unit.value)
			format := "%.1f"
			if scaled >= 100 {
				format = "%.0f"
			}
			return sign + strings.TrimSuffix(fmt.Sprintf(format, scaled), ".0") + unit.suffix
		}
	}
	return sign + formatDelimitedInt(v)
}

func formatDelimitedInt(v int64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	raw := strconv.FormatInt(v, 10)
	if len(raw) <= 3 {
		return sign + raw
	}
	var b strings.Builder
	b.Grow(len(raw) + (len(raw)-1)/3 + len(sign))
	b.WriteString(sign)
	for i, r := range raw {
		if i > 0 && (len(raw)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
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
		DataSource: newMaintenanceDataSource(s.d.Maintenance),
		CreateForm: maintenanceForm{},
		EditForm:   maintenanceForm{},
		Authorize:  requireAdmin(),
		Actions:    []admin.ActionType{admin.ActionView, admin.ActionCreate, admin.ActionEdit, admin.ActionDelete},
	})
}

// ---------------------------------------------------------------------------
// In-app purchase products (custom CRUD resource)
// ---------------------------------------------------------------------------

type iapProductsResource struct{ s *Server }

func (s *Server) newIAPProductsResource() admin.Resource { return &iapProductsResource{s: s} }

func (r *iapProductsResource) ID() string { return "iap-products" }

func (r *iapProductsResource) actionURL(req admin.Request, action admin.ActionType, dynamicPath string) string {
	u := req.BasePath + "/resources/iap-products/action?action=" + string(action)
	if dynamicPath != "" {
		u += "&dynamicPath=" + url.QueryEscape(dynamicPath)
	}
	return u
}

func (r *iapProductsResource) authorize(ctx context.Context, req admin.Request, action admin.ActionType) error {
	return requireAdmin()(ctx, req.Identity, action)
}

func (r *iapProductsResource) Info(_ context.Context, req admin.Request) admin.ResourceInfo {
	return admin.ResourceInfo{
		ID:            "iap-products",
		Name:          "In-App Products",
		Description:   "Product IDs, store environment, prices, and point grants used by purchase webhooks.",
		Icon:          "badge-dollar-sign",
		Type:          admin.ResourceTable,
		DataURL:       r.actionURL(req, admin.ActionView, ""),
		DefaultAction: admin.ActionView,
		SupportedActions: []admin.ActionButton{{
			Type:       admin.ButtonPrimary,
			Label:      "Create",
			Icon:       "plus",
			Behavior:   admin.BehaviorOpenSheet,
			ActionType: admin.ActionCreate,
			OnClick:    r.actionURL(req, admin.ActionCreate, ""),
		}},
	}
}

func (r *iapProductsResource) Schema(ctx context.Context, req admin.Request, action admin.ActionType) (any, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	switch action {
	case admin.ActionView:
		return &admin.TableSchema{
			UIType: "table",
			Type:   admin.ActionView,
			Columns: []admin.TableColumn{
				{Name: "id", Label: "ID", Type: "number", Pinned: true},
				{Name: "product_id", Label: "Shared Product ID", Type: "string", Pinned: true},
				{Name: "store_environment", Label: "Store", Type: "string", Format: "chip"},
				{Name: "product_type", Label: "Type", Type: "string"},
				{Name: "display_name", Label: "Name", Type: "string"},
				{Name: "points_grant", Label: "Points", Type: "number"},
				{Name: "price_currency", Label: "Currency", Type: "string"},
				{Name: "price_minor_units", Label: "Price", Type: "number"},
				{Name: "enabled", Label: "Enabled", Type: "boolean", Format: "boolean"},
				{Name: "created_at", Label: "Created", Type: "string", Format: "date-time"},
			},
		}, nil
	case admin.ActionCreate, admin.ActionEdit:
		label := "Create"
		if action == admin.ActionEdit {
			label = "Save"
		}
		fs, err := admin.FormSchemaFromModel(iapProductForm{}, action, label,
			r.actionURL(req, action, strings.Trim(req.DynamicPath, "/")))
		if err != nil {
			return nil, err
		}
		applyIAPProductSchema(fs, action)
		return fs, nil
	default:
		return nil, fmt.Errorf("%w: no schema for action %q", admin.ErrBadInput, action)
	}
}

func applyIAPProductSchema(fs *admin.FormSchema, action admin.ActionType) {
	if fs == nil || fs.Schema == nil || fs.Schema.Properties == nil {
		return
	}
	subscriptionPeriod, _ := fs.Schema.Properties.Get("subscription_period")
	pruneIAPProductSchema(fs)
	applyIAPProductSubscriptionCondition(fs, subscriptionPeriod)
	descriptions := map[string]string{
		"product_id":        "One shared store product identifier. The server sends this as the RevenueCat store_identifier; purchase webhooks must report the same value.",
		"store_environment": "Choose test_store for sandbox/test products or app_store for production App Store products. Webhooks are accepted only from the matching environment.",
		"product_type":      "Store product kind. Consumable products grant points on purchase; subscriptions are synced to RevenueCat only.",
		"display_name":      "Admin-facing product label. This is sent to RevenueCat as the display name.",
		"points_grant":      "Number of points credited to the user after a successful purchase or renewal webhook. Must be greater than 0 for fulfillment and admin top-ups.",
		"price_currency":    "Optional ISO 4217 currency code for admin visibility, such as USD. The App Store remains the source of truth for user-facing prices.",
		"price_minor_units": "Optional price in minor currency units for admin visibility, such as 199 for $1.99 USD. This does not by itself set the App Store price.",
		"enabled":           "Enabled products are synced to RevenueCat. Disabled products are saved as local drafts and ignored by webhooks.",
	}
	for name, desc := range descriptions {
		if p, ok := fs.Schema.Properties.Get(name); ok {
			p.Description = desc
			if name == "product_id" {
				p.Title = "Shared product ID"
			}
		}
	}
	if fs.UISchema == nil {
		fs.UISchema = admin.UISchema{}
	}
	if action == admin.ActionCreate {
		fs.UISchema["ui:order"] = []any{
			"product_id",
			"display_name",
			"store_environment",
			"product_type",
			"subscription_period",
			"points_grant",
			"price_currency",
			"price_minor_units",
			"enabled",
		}
		return
	}
	fs.UISchema["ui:order"] = []any{
		"product_id",
		"display_name",
		"store_environment",
		"product_type",
		"subscription_period",
		"points_grant",
		"price_currency",
		"price_minor_units",
		"enabled",
	}
}

func pruneIAPProductSchema(fs *admin.FormSchema) {
	pruned := []string{
		"app_store_price_point_id",
		"app_store_subscription_group_id",
		"app_store_locale",
		"subscription_display_name",
		"subscription_description",
		"app_store_review_note",
		"app_store_review_screenshot_url",
		"app_store_subscription_localization_id",
		"revenuecat_offering_id",
	}
	for _, name := range pruned {
		fs.Schema.Properties.Delete(name)
		delete(fs.UISchema, name)
	}
	fs.Schema.Required = withoutSchemaRequiredFields(fs.Schema.Required, pruned...)
}

func applyIAPProductSubscriptionCondition(fs *admin.FormSchema, subscriptionPeriod *jsonschema.Schema) {
	if subscriptionPeriod == nil {
		return
	}
	subscriptionPeriod.Title = "Duration"
	subscriptionPeriod.Description = "Subscription duration. Only shown for subscription products and synced to RevenueCat metadata."
	subscriptionPeriod.Default = "ONE_MONTH"
	// Keep the duration out of base properties so the form renderer only shows
	// it when product_type selects the subscription branch.
	fs.Schema.Properties.Delete("subscription_period")
	fs.Schema.Required = withoutSchemaRequiredFields(fs.Schema.Required, "subscription_period")
	ifProps := jsonschema.NewProperties()
	ifProps.Set("product_type", &jsonschema.Schema{Const: IAPProductTypeSubscription})
	thenProps := jsonschema.NewProperties()
	thenProps.Set("subscription_period", subscriptionPeriod)
	fs.Schema.If = &jsonschema.Schema{
		Properties: ifProps,
		Required:   []string{"product_type"},
	}
	fs.Schema.Then = &jsonschema.Schema{
		Properties: thenProps,
		Required:   []string{"subscription_period"},
	}
}

func withoutSchemaRequiredFields(required []string, names ...string) []string {
	if len(required) == 0 || len(names) == 0 {
		return required
	}
	drop := make(map[string]bool, len(names))
	for _, name := range names {
		drop[name] = true
	}
	filtered := required[:0]
	for _, name := range required {
		if !drop[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func (r *iapProductsResource) Fetch(ctx context.Context, req admin.Request, action admin.ActionType, _ map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	if r.s.d.IAPProducts == nil {
		return nil, fmt.Errorf("%w: iap product store unavailable", admin.ErrBadInput)
	}
	switch action {
	case admin.ActionView:
		limit := 20
		if l, err := strconv.Atoi(req.Query.Get("limit")); err == nil && l > 0 {
			limit = l
		}
		after, _ := strconv.ParseInt(req.Query.Get("after"), 10, 64)
		rows, next, err := r.s.d.IAPProducts.List(ctx, after, limit)
		if err != nil {
			return nil, err
		}
		items := make([]admin.Item, 0, len(rows))
		for _, row := range rows {
			id := strconv.FormatInt(row.ID, 10)
			actions := []admin.ActionButton{
				{
					Type:       admin.ButtonSecondary,
					Label:      "Edit",
					Icon:       "pencil",
					Behavior:   admin.BehaviorOpenSheet,
					ActionType: admin.ActionEdit,
					OnClick:    r.actionURL(req, admin.ActionEdit, id),
				},
			}
			if rcURL := r.revenueCatProductURL(row); rcURL != "" {
				actions = append(actions, admin.ActionButton{
					Type:       admin.ButtonInfo,
					Label:      "RevenueCat",
					Icon:       "external-link",
					Behavior:   admin.BehaviorNavigate,
					ActionType: admin.ActionView,
					OnClick:    rcURL,
				})
			}
			actions = append(actions, admin.ActionButton{
				Type:       admin.ButtonDanger,
				Label:      "Delete",
				Icon:       "trash-2",
				Behavior:   admin.BehaviorConfirmDialog,
				ActionType: admin.ActionDelete,
				OnClick:    r.actionURL(req, admin.ActionDelete, id),
			})
			items = append(items, admin.Item{
				Data:        row,
				DynamicPath: id,
				Actions:     actions,
			})
		}
		var nextURL *string
		if next > 0 {
			u := r.actionURL(req, admin.ActionView, "") + "&after=" + strconv.FormatInt(next, 10) + "&limit=" + strconv.Itoa(limit)
			nextURL = &u
		}
		return admin.Paginated(items, nil, nextURL, nil), nil
	case admin.ActionCreate:
		return admin.Detail(map[string]any{
			"store_environment": IAPStoreEnvironmentTest,
			"product_type":      IAPProductTypeConsumable,
			"enabled":           true,
		}), nil
	case admin.ActionEdit:
		id, err := strconv.ParseInt(strings.Trim(req.DynamicPath, "/"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing product id", admin.ErrBadInput)
		}
		p, err := r.s.d.IAPProducts.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("%w: product not found", admin.ErrBadInput)
		}
		return admin.Detail(p), nil
	default:
		return nil, fmt.Errorf("%w: cannot fetch action %q", admin.ErrBadInput, action)
	}
}

func (r *iapProductsResource) Act(ctx context.Context, req admin.Request, action admin.ActionType, data map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	if r.s.d.IAPProducts == nil {
		return nil, fmt.Errorf("%w: iap product store unavailable", admin.ErrBadInput)
	}
	switch action {
	case admin.ActionCreate:
		p := iapProductFromForm(data)
		if err := r.createAndSyncProduct(ctx, &p); err != nil {
			return nil, err
		}
		if saved, err := r.s.d.IAPProducts.Get(ctx, p.ID); err == nil && saved != nil {
			return admin.Detail(saved), nil
		}
		return admin.Detail(p), nil
	case admin.ActionEdit:
		id, err := strconv.ParseInt(strings.Trim(req.DynamicPath, "/"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing product id", admin.ErrBadInput)
		}
		existing, err := r.s.d.IAPProducts.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("%w: product not found", admin.ErrBadInput)
		}
		p := iapProductFromForm(data)
		if _, ok := data["subscription_period"]; !ok {
			p.SubscriptionPeriod = existing.SubscriptionPeriod
		}
		if err := r.s.d.IAPProducts.Update(ctx, id, &p); err != nil {
			return nil, err
		}
		p.ID = id
		if err := r.syncProduct(ctx, &p); err != nil {
			return nil, err
		}
		if saved, err := r.s.d.IAPProducts.Get(ctx, id); err == nil && saved != nil {
			return admin.Detail(saved), nil
		}
		return admin.Detail(p), nil
	case admin.ActionDelete:
		id, err := strconv.ParseInt(strings.Trim(req.DynamicPath, "/"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing product id", admin.ErrBadInput)
		}
		p, err := r.s.d.IAPProducts.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("%w: product not found", admin.ErrBadInput)
		}
		if err := r.deleteSyncedProduct(ctx, p); err != nil {
			return nil, err
		}
		if err := r.s.d.IAPProducts.Delete(ctx, id); err != nil {
			return nil, err
		}
		return admin.Detail(map[string]any{"deleted": true, "id": id}), nil
	default:
		return nil, fmt.Errorf("%w: cannot execute action %q", admin.ErrBadInput, action)
	}
}

func (r *iapProductsResource) revenueCatProductURL(row IAPProduct) string {
	if r == nil || r.s == nil || r.s.d.Env == nil {
		return ""
	}
	projectID := strings.TrimSpace(r.s.d.Env.RevenueCatProjectID)
	appID := strings.TrimSpace(r.s.d.Env.RevenueCatAppID)
	productID := strings.TrimSpace(row.ProductID)
	if projectID == "" || appID == "" || productID == "" {
		return ""
	}
	u := url.URL{
		Scheme: "https",
		Host:   "app.revenuecat.com",
		Path:   "/projects/" + url.PathEscape(projectID) + "/apps/" + url.PathEscape(appID) + "/products",
	}
	q := u.Query()
	q.Set("search", productID)
	u.RawQuery = q.Encode()
	return u.String()
}

func (r *iapProductsResource) deleteSyncedProduct(ctx context.Context, p *IAPProduct) error {
	if p == nil {
		return nil
	}
	if r.s.d.IAPProductSyncer == nil {
		return fmt.Errorf("%w: iap product sync unavailable; cannot delete RevenueCat product", admin.ErrBadInput)
	}
	return r.s.d.IAPProductSyncer.DeleteIAPProduct(ctx, *p)
}

func (r *iapProductsResource) createAndSyncProduct(ctx context.Context, p *IAPProduct) error {
	if p == nil {
		return errors.New("iap product is nil")
	}
	tx, err := r.s.d.IAPProducts.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := r.s.d.IAPProducts.create(ctx, tx, p); err != nil {
		return err
	}
	if !p.Enabled {
		return tx.Commit()
	}
	if r.s.d.IAPProductSyncer == nil {
		return fmt.Errorf("%w: iap product sync unavailable; set REVENUECAT_REST_API_KEY, REVENUECAT_PROJECT_ID, and REVENUECAT_APP_ID", admin.ErrBadInput)
	}
	result, err := r.s.d.IAPProductSyncer.SyncIAPProduct(ctx, *p)
	if err != nil {
		return err
	}
	if err := r.s.d.IAPProducts.markSyncResult(ctx, tx, p.ID, result, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *iapProductsResource) syncProduct(ctx context.Context, p *IAPProduct) error {
	if p == nil || !p.Enabled {
		return nil
	}
	if r.s.d.IAPProductSyncer == nil {
		err := fmt.Errorf("%w: iap product sync unavailable; set REVENUECAT_REST_API_KEY, REVENUECAT_PROJECT_ID, and REVENUECAT_APP_ID", admin.ErrBadInput)
		if p != nil && p.ID > 0 && r.s.d.IAPProducts != nil {
			_ = r.s.d.IAPProducts.MarkSyncResult(ctx, p.ID, IAPProductSyncResult{}, err)
		}
		return err
	}
	result, err := r.s.d.IAPProductSyncer.SyncIAPProduct(ctx, *p)
	if p.ID > 0 && r.s.d.IAPProducts != nil {
		if markErr := r.s.d.IAPProducts.MarkSyncResult(ctx, p.ID, result, err); markErr != nil && err == nil {
			err = markErr
		}
	}
	return err
}

// ---------------------------------------------------------------------------
// User management (custom resource: table + per-row "Manage" form)
// ---------------------------------------------------------------------------

// usersResource lists user balances and subscription plans and exposes a
// per-row "Manage" form that can re-assign the user's subscription class and/or
// top up their points. It is a hand-written admin.Resource because these are
// bespoke actions (change the recorded plan, credit points from a selected
// product) rather than generic CRUD updates, and the list is backed by the
// points store rather than a GORM model.
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
		Description:   "User balances and subscription plans. Top up a user by selecting a product.",
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
				{Name: "subscription_plan", Label: "Plan", Type: "string"},
				{Name: "subscription_status", Label: "Plan Status", Type: "string", Format: "chip"},
				{Name: "balance", Label: "Balance", Type: "number"},
			},
		}, nil
	case admin.ActionEdit:
		return r.manageSchema(ctx, req)
	default:
		return nil, fmt.Errorf("%w: no schema for action %q", admin.ErrBadInput, action)
	}
}

// manageSchema is the per-user management form: a subscription-class dropdown
// (free plus every IAP subscription product) for re-assigning the user's plan,
// and an optional product dropdown for topping up their points balance. Both
// are optional so an admin can change either independently.
func (r *usersResource) manageSchema(ctx context.Context, req admin.Request) (*admin.FormSchema, error) {
	userID := strings.Trim(req.DynamicPath, "/")
	fs, err := admin.FormSchemaFromModel(userManageForm{}, admin.ActionEdit, "Save",
		r.actionURL(req, admin.ActionEdit, userID))
	if err != nil {
		return nil, err
	}
	if p, ok := fs.Schema.Properties.Get("subscription_class"); ok {
		p.OneOf = r.s.subscriptionClassOptions(ctx)
		p.Description = "Set the user's subscription plan. This overrides the last webhook-derived plan; RevenueCat remains the source of truth and a later webhook may change it back."
	}
	balanceNote := ""
	if r.s.d.Points != nil && userID != "" {
		if bal, err := r.s.d.Points.Balance(ctx, userID); err == nil {
			balanceNote = fmt.Sprintf(" Current balance: %d points.", bal)
		}
	}
	if p, ok := fs.Schema.Properties.Get("product"); ok {
		p.Description = "Optional: select a product to grant its points to the user." + balanceNote
		// The field is optional and prefilled empty, so the empty value must be a
		// valid oneOf branch — otherwise AJV rejects "" ("must match exactly one
		// schema in oneOf"). Prepend an explicit "no top up" option.
		p.OneOf = append([]*jsonschema.Schema{{Const: "", Title: "— No top up —"}}, r.s.productOptions(ctx)...)
	}
	if fs.UISchema == nil {
		fs.UISchema = admin.UISchema{}
	}
	fs.UISchema["ui:order"] = []any{"subscription_class", "product"}
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
		// Prefill the subscription-class dropdown to the user's current plan (so a
		// no-op submit doesn't change it) and leave the topup product unselected.
		return admin.Detail(map[string]any{
			"subscription_class": r.currentClassValue(ctx, strings.Trim(req.DynamicPath, "/")),
			"product":            "",
		}), nil
	default:
		return nil, fmt.Errorf("%w: cannot fetch action %q", admin.ErrBadInput, action)
	}
}

// currentClassValue returns the dropdown value for the user's currently recorded
// subscription class ("free" when none is on file), matching the encoding used
// by subscriptionClassOptions.
func (r *usersResource) currentClassValue(ctx context.Context, userID string) string {
	if r.s.d.Points == nil || userID == "" {
		return "free"
	}
	sub, err := r.s.d.Points.Subscription(ctx, userID)
	if err != nil || sub == nil {
		return "free"
	}
	return encodeClassValue(sub.ProductID, sub.StoreEnvironment)
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
				"user_id":             row.UserID,
				"display_name":        row.DisplayName,
				"subscription_plan":   row.SubscriptionPlan,
				"subscription_status": row.SubscriptionStatus,
				"balance":             row.Balance,
			},
			Actions: []admin.ActionButton{{
				Type:       admin.ButtonPrimary,
				Label:      "Manage",
				Icon:       "settings",
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
	if r.s.d.Points == nil {
		return nil, fmt.Errorf("%w: points unavailable", admin.ErrBadInput)
	}

	result := map[string]any{"user_id": userID}

	// 1) Optionally re-assign the subscription class, but only when it differs
	// from what's on file so a topup-only submit doesn't disturb the plan.
	subscriptionClass := strings.TrimSpace(stringField(data, "subscription_class"))
	if subscriptionClass != "" && subscriptionClass != r.currentClassValue(ctx, userID) {
		if err := r.setSubscriptionClass(ctx, userID, subscriptionClass); err != nil {
			return nil, err
		}
		r.s.invalidateEntitlementsCache(ctx)
		result["subscription_class"] = classLabel(func() string {
			pid, _ := decodeClassValue(subscriptionClass)
			return pid
		}())
	}

	// 2) Optionally top up points.
	product := strings.TrimSpace(stringField(data, "product"))
	if product != "" {
		if r.s.d.IAPProducts == nil {
			return nil, fmt.Errorf("%w: products unavailable", admin.ErrBadInput)
		}
		productID, err := strconv.ParseInt(product, 10, 64)
		if err != nil || productID <= 0 {
			return nil, &admin.ValidationError{Fields: map[string]string{"product": "unknown product"}}
		}
		iapProduct, err := r.s.d.IAPProducts.Get(ctx, productID)
		if err != nil {
			return nil, err
		}
		if iapProduct == nil || !iapProduct.Enabled || iapProduct.PointsGrant <= 0 {
			return nil, &admin.ValidationError{Fields: map[string]string{"product": "unknown product"}}
		}
		if err := r.s.d.Points.EnsureUser(ctx, userID); err != nil {
			return nil, err
		}
		balance, err := r.s.d.Points.Credit(ctx, userID, iapProduct.PointsGrant, pointsReasonAdminTopup, randomEventID("admin_topup:"))
		if err != nil {
			return nil, err
		}
		result["product"] = iapProduct.ProductID
		result["product_id"] = iapProduct.ID
		result["granted"] = iapProduct.PointsGrant
		result["balance"] = balance
	}

	return admin.Detail(result), nil
}

// setSubscriptionClass overrides the user's recorded plan to the given class
// value (as encoded by subscriptionClassOptions). The free sentinel clears the
// subscription; any other value resolves the matching subscription product and
// records it as an active plan with no expiry.
func (r *usersResource) setSubscriptionClass(ctx context.Context, userID, classValue string) error {
	productID, storeEnv := decodeClassValue(classValue)
	if productID == "" {
		// Free / no-subscription class.
		return r.s.d.Points.ClearSubscription(ctx, userID)
	}
	if r.s.d.IAPProducts == nil {
		return fmt.Errorf("%w: products unavailable", admin.ErrBadInput)
	}
	product, err := r.s.d.IAPProducts.FindSubscription(ctx, productID, storeEnv)
	if err != nil {
		return err
	}
	if product == nil {
		return &admin.ValidationError{Fields: map[string]string{"subscription_class": "unknown subscription product"}}
	}
	// expires_at = 0 means "no known expiry" and is treated as active while the
	// status is active (see UserSubscription.Active). Each admin override is a
	// distinct ledger event, so use a fresh idempotency key.
	return r.s.d.Points.RecordSubscription(ctx, userID, *product, "active", randomEventID("admin_subscription:"), 0)
}

// userManageForm is the DTO reflected into the per-user management sheet. Both
// fields are optional and independent: changing subscription_class re-assigns
// the user's plan, and picking a product tops up their points. An admin may do
// either or both in one submit.
type userManageForm struct {
	SubscriptionClass string `json:"subscription_class" jsonschema:"title=Subscription class"`
	Product           string `json:"product,omitempty" jsonschema:"title=Top up product"`
}

// productOptions builds oneOf {const,title} entries from enabled DB catalog rows
// so the topup dropdown shows "<store> / <product id> (+N points)".
func (s *Server) productOptions(ctx context.Context) []*jsonschema.Schema {
	if s.d.IAPProducts == nil {
		return nil
	}
	products, err := s.d.IAPProducts.EnabledForTopup(ctx)
	if err != nil {
		return nil
	}
	opts := make([]*jsonschema.Schema, 0, len(products))
	for _, p := range products {
		label := p.ProductID
		if p.DisplayName != "" {
			label = p.DisplayName + " (" + p.ProductID + ")"
		}
		opts = append(opts, &jsonschema.Schema{
			Const: strconv.FormatInt(p.ID, 10),
			Title: fmt.Sprintf("%s / %s (+%d points)", p.StoreEnvironment, label, p.PointsGrant),
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
