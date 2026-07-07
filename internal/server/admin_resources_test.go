package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
	iaps := newTestIAPProductStore(t)
	product := IAPProduct{
		ProductID:        "points_500",
		StoreEnvironment: IAPStoreEnvironmentTest,
		ProductType:      IAPProductTypeConsumable,
		DisplayName:      "500 points",
		PointsGrant:      500,
		Enabled:          true,
	}
	if err := iaps.Create(context.Background(), &product); err != nil {
		t.Fatalf("create iap product: %v", err)
	}
	s := &Server{d: Deps{Points: ps, IAPProducts: iaps}}
	res := s.newUsersResource()
	ctx := context.Background()

	// Unknown product → validation error.
	if _, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"product": "nope"}); err == nil {
		t.Error("expected unknown-product rejection")
	}

	// Valid topup credits the user.
	resp, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"product": strconv.FormatInt(product.ID, 10)})
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
	if row["subscription_plan"] != "" || row["subscription_status"] != "" {
		t.Errorf("unexpected subscription fields = %#v", row)
	}
	if len(list.Items[0].Actions) != 1 || list.Items[0].Actions[0].Label != "Top up" {
		t.Errorf("expected a Top up row action, got %#v", list.Items[0].Actions)
	}
}

func TestUsersResourceShowsSubscriptionPlan(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	s := &Server{d: Deps{Points: ps}}
	res := s.newUsersResource()
	ctx := context.Background()

	product := IAPProduct{
		ProductID:          "app.rxlab.pro.monthly",
		StoreEnvironment:   IAPStoreEnvironmentAppStore,
		ProductType:        IAPProductTypeSubscription,
		DisplayName:        "Pro Monthly",
		SubscriptionPeriod: "ONE_MONTH",
	}
	if err := ps.RecordSubscription(ctx, "oauth:user-1", product, "active", "evt-sub", 0); err != nil {
		t.Fatalf("RecordSubscription: %v", err)
	}

	rawSchema, err := res.Schema(ctx, adminReq(""), admin.ActionView)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	table, ok := rawSchema.(*admin.TableSchema)
	if !ok {
		t.Fatalf("Schema = %T, want *admin.TableSchema", rawSchema)
	}
	if !tableHasColumn(table, "subscription_plan") || !tableHasColumn(table, "subscription_status") {
		t.Fatalf("users table missing subscription columns: %#v", table.Columns)
	}

	list, err := res.Fetch(ctx, adminReq(""), admin.ActionView, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("list items = %d, want 1", len(list.Items))
	}
	row, _ := list.Items[0].Data.(map[string]any)
	if row["subscription_plan"] != "Pro Monthly" || row["subscription_status"] != "active" {
		t.Fatalf("subscription row = %#v", row)
	}
}

func TestUsageDashboardResourceCustomPage(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)

	inserts := []struct {
		discussionID      string
		reason            string
		costUSD           float64
		promptTokens      int64
		completionTokens  int64
		totalTokens       int64
		llmCostUSD        float64
		ttsCostUSD        float64
		musicCostUSD      float64
		createdAtUnixMsec int64
	}{
		{
			discussionID:      "today-generation",
			reason:            pointsReasonGeneration,
			costUSD:           1.15,
			promptTokens:      100,
			completionTokens:  50,
			totalTokens:       150,
			llmCostUSD:        0.50,
			ttsCostUSD:        0.20,
			musicCostUSD:      0.10,
			createdAtUnixMsec: today.UnixMilli(),
		},
		{
			discussionID:      "today-image",
			reason:            pointsReasonImageGeneration,
			costUSD:           0.40,
			createdAtUnixMsec: today.UnixMilli(),
		},
		{
			discussionID:      "yesterday-summary",
			reason:            pointsReasonSummary,
			costUSD:           0.10,
			promptTokens:      10,
			completionTokens:  5,
			totalTokens:       15,
			llmCostUSD:        0.10,
			createdAtUnixMsec: yesterday.UnixMilli(),
		},
	}
	for _, row := range inserts {
		if _, err := ps.db.ExecContext(ctx, `INSERT INTO points_ledger
			(user_id, discussion_id, delta, reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
			 llm_cost_usd, tts_cost_usd, music_cost_usd, balance_after, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"admin-user", row.discussionID, -1, row.reason, row.costUSD, row.promptTokens, row.completionTokens, row.totalTokens,
			row.llmCostUSD, row.ttsCostUSD, row.musicCostUSD, 0, row.createdAtUnixMsec); err != nil {
			t.Fatalf("insert ledger row: %v", err)
		}
	}

	summary, err := ps.UsageSpendByDate(ctx, 2)
	if err != nil {
		t.Fatalf("UsageSpendByDate: %v", err)
	}
	if len(summary.Days) != 2 {
		t.Fatalf("days = %d, want 2", len(summary.Days))
	}
	last := summary.Days[1]
	if last.Date != today.Format("2006-01-02") || last.TotalTokens != 150 || last.ImageCostUSD != 0.40 || last.TTSCostUSD != 0.20 {
		t.Fatalf("today summary = %#v", last)
	}
	if last.OtherCostUSD < 0.349 || last.OtherCostUSD > 0.351 {
		t.Fatalf("today other cost = %v, want 0.35", last.OtherCostUSD)
	}

	s := &Server{d: Deps{Points: ps}}
	res := s.newUsageDashboardResource()
	info := res.Info(ctx, adminReq(""))
	if info.Type != admin.ResourceCustom || info.Name != "Usage Dashboard" {
		t.Fatalf("info = %#v", info)
	}

	raw, err := res.Schema(ctx, adminReq(""), admin.ActionView)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	page, ok := raw.(*admin.CustomResourcePage)
	if !ok {
		t.Fatalf("Schema = %T, want *admin.CustomResourcePage", raw)
	}
	if page.UIType != "custom" || len(page.Sections) < 3 {
		t.Fatalf("page = %#v", page)
	}
	stats := page.Sections[0].Statistics
	if len(stats) != 4 || stats[0].Value != "$1.65" || stats[1].Value != "165" {
		t.Fatalf("stats = %#v", stats)
	}
	spendChart := page.Sections[1].Children[0]
	if len(spendChart.Data) != 14 {
		t.Fatalf("spend chart days = %d, want 14", len(spendChart.Data))
	}
	todayRow := spendChart.Data[len(spendChart.Data)-1]
	if todayRow["date"] != today.Format("2006-01-02") || todayRow["image_cost_usd"] != 0.4 || todayRow["tts_cost_usd"] != 0.2 {
		t.Fatalf("today chart row = %#v", todayRow)
	}
}

func TestIAPProductsResourceCRUD(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	syncer := &fakeIAPProductSyncer{}
	s := &Server{d: Deps{IAPProducts: iaps, IAPProductSyncer: syncer}}
	res := s.newIAPProductsResource()
	ctx := context.Background()

	created, err := res.Act(ctx, adminReq(""), admin.ActionCreate, map[string]any{
		"product_id":        "app.rxlab.points1000",
		"store_environment": IAPStoreEnvironmentAppStore,
		"product_type":      IAPProductTypeConsumable,
		"display_name":      "1000 points",
		"points_grant":      float64(1000),
		"price_currency":    "usd",
		"price_minor_units": float64(199),
		"enabled":           true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	row := *created.Data.(*IAPProduct)
	if row.ID == 0 || row.PriceCurrency != "USD" || row.PointsGrant != 1000 {
		t.Fatalf("created row = %#v", row)
	}
	if row.SyncedAt == nil || row.LastSyncError != "" {
		t.Fatalf("sync fields = %#v", row)
	}
	if len(syncer.products) != 1 || syncer.products[0].ProductID != "app.rxlab.points1000" {
		t.Fatalf("synced products = %#v", syncer.products)
	}

	list, err := res.Fetch(ctx, adminReq(""), admin.ActionView, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}

	id := strconv.FormatInt(row.ID, 10)
	edited, err := res.Act(ctx, adminReq(id), admin.ActionEdit, map[string]any{
		"product_id":        "app.rxlab.points1000",
		"store_environment": IAPStoreEnvironmentAppStore,
		"product_type":      IAPProductTypeConsumable,
		"display_name":      "1000 points",
		"points_grant":      float64(1200),
		"enabled":           true,
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := edited.Data.(*IAPProduct); got.PointsGrant != 1200 || !got.Enabled {
		t.Fatalf("edited row = %#v", got)
	}
	if len(syncer.products) != 2 || syncer.products[1].ProductID != "app.rxlab.points1000" {
		t.Fatalf("edit sync products = %#v", syncer.products)
	}
	if _, err := res.Act(ctx, adminReq(id), admin.ActionDelete, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(syncer.deleted) != 1 || syncer.deleted[0].ProductID != "app.rxlab.points1000" {
		t.Fatalf("deleted products = %#v", syncer.deleted)
	}
}

func TestIAPProductsResourceKeepsRowWhenRevenueCatDeleteFails(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	syncer := &fakeIAPProductSyncer{}
	s := &Server{d: Deps{IAPProducts: iaps, IAPProductSyncer: syncer}}
	res := s.newIAPProductsResource()
	ctx := context.Background()

	created, err := res.Act(ctx, adminReq(""), admin.ActionCreate, map[string]any{
		"product_id":        "app.rxlab.points1000",
		"store_environment": IAPStoreEnvironmentAppStore,
		"product_type":      IAPProductTypeConsumable,
		"points_grant":      float64(1000),
		"enabled":           true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.Data.(*IAPProduct).ID
	syncer.err = errors.New("revenuecat delete failed")

	_, err = res.Act(ctx, adminReq(strconv.FormatInt(id, 10)), admin.ActionDelete, nil)
	if err == nil {
		t.Fatal("expected delete to fail")
	}
	if len(syncer.deleted) != 1 || syncer.deleted[0].ProductID != "app.rxlab.points1000" {
		t.Fatalf("deleted products = %#v", syncer.deleted)
	}
	got, getErr := iaps.Get(ctx, id)
	if getErr != nil {
		t.Fatalf("get after failed delete: %v", getErr)
	}
	if got == nil {
		t.Fatal("local product row was deleted after RevenueCat delete failed")
	}
}

func TestIAPProductsResourceDeletesRevenueCatBySharedProductID(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	syncer := &fakeIAPProductSyncer{}
	s := &Server{d: Deps{IAPProducts: iaps, IAPProductSyncer: syncer}}
	res := s.newIAPProductsResource()
	ctx := context.Background()

	p := IAPProduct{
		ProductID:        "plus_subscription",
		StoreEnvironment: IAPStoreEnvironmentAppStore,
		ProductType:      IAPProductTypeSubscription,
		DisplayName:      "Plus Subscription",
		PointsGrant:      1000,
		Enabled:          true,
	}
	if err := iaps.Create(ctx, &p); err != nil {
		t.Fatalf("create local row: %v", err)
	}
	if _, err := res.Act(ctx, adminReq(strconv.FormatInt(p.ID, 10)), admin.ActionDelete, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(syncer.deleted) != 1 || syncer.deleted[0].ProductID != "plus_subscription" {
		t.Fatalf("deleted products = %#v", syncer.deleted)
	}
	got, getErr := iaps.Get(ctx, p.ID)
	if getErr != nil {
		t.Fatalf("get after delete: %v", getErr)
	}
	if got != nil {
		t.Fatalf("local product row still exists after successful delete: %#v", got)
	}
}

func TestIAPProductsResourceShowsRevenueCatActionForSharedProductID(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	s := &Server{d: Deps{
		Env: &config.Env{
			RevenueCatProjectID: "proj_123",
			RevenueCatAppID:     "app_rc_123",
		},
		IAPProducts: iaps,
	}}
	res := s.newIAPProductsResource()
	ctx := context.Background()

	p := IAPProduct{
		ProductID:          "plus_subscription",
		StoreEnvironment:   IAPStoreEnvironmentAppStore,
		ProductType:        IAPProductTypeSubscription,
		DisplayName:        "Plus Subscription",
		PointsGrant:        100000,
		PriceCurrency:      "USD",
		PriceMinorUnits:    1,
		SubscriptionPeriod: "ONE_MONTH",
		Enabled:            true,
	}
	if err := iaps.Create(ctx, &p); err != nil {
		t.Fatalf("create product: %v", err)
	}

	list, err := res.Fetch(ctx, adminReq(""), admin.ActionView, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}
	var revenueCat *admin.ActionButton
	for i := range list.Items[0].Actions {
		if list.Items[0].Actions[i].Label == "RevenueCat" {
			revenueCat = &list.Items[0].Actions[i]
			break
		}
	}
	if revenueCat == nil {
		t.Fatalf("RevenueCat action missing: %#v", list.Items[0].Actions)
	}
	if revenueCat.Behavior != admin.BehaviorNavigate || revenueCat.Icon != "external-link" {
		t.Fatalf("RevenueCat action = %#v", revenueCat)
	}
	if !strings.Contains(revenueCat.OnClick, "app.revenuecat.com/projects/proj_123/apps/app_rc_123/products") ||
		!strings.Contains(revenueCat.OnClick, "search=plus_subscription") {
		t.Fatalf("RevenueCat link = %q", revenueCat.OnClick)
	}
}

func TestIAPProductsResourceCreateSchemaUsesSharedProductID(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	s := &Server{d: Deps{IAPProducts: iaps}}
	res := s.newIAPProductsResource()
	raw, err := res.Schema(context.Background(), adminReq(""), admin.ActionCreate)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	fs, ok := raw.(*admin.FormSchema)
	if !ok {
		t.Fatalf("Schema = %T, want *admin.FormSchema", raw)
	}
	if _, ok := fs.Schema.Properties.Get("revenuecat_product_id"); ok {
		t.Fatal("create schema should not expose a separate RevenueCat product id")
	}
	if _, ok := fs.Schema.Properties.Get("app_store_connect_id"); ok {
		t.Fatal("create schema should not expose a separate App Store Connect id")
	}
	hiddenFields := []string{
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
	for _, field := range hiddenFields {
		if _, ok := fs.Schema.Properties.Get(field); ok {
			t.Fatalf("create schema should hide metadata field %q", field)
		}
		if stringSliceContains(fs.Schema.Required, field) {
			t.Fatalf("create schema should not require hidden field %q: %#v", field, fs.Schema.Required)
		}
	}
	prop, ok := fs.Schema.Properties.Get("product_id")
	if !ok {
		t.Fatal("missing product_id schema property")
	}
	if prop.Title != "Shared product ID" {
		t.Fatalf("product_id title = %q, want Shared product ID", prop.Title)
	}
	if prop.Description == "" || strings.Contains(prop.Description, "App Store Connect") || !strings.Contains(prop.Description, "RevenueCat store_identifier") {
		t.Fatalf("product_id description = %q", prop.Description)
	}
	for _, field := range []string{
		"price_minor_units",
		"points_grant",
		"store_environment",
		"enabled",
	} {
		prop, ok := fs.Schema.Properties.Get(field)
		if !ok {
			t.Fatalf("missing schema property %q", field)
		}
		if prop.Description == "" {
			t.Fatalf("property %q missing description", field)
		}
	}
	if _, ok := fs.Schema.Properties.Get("subscription_period"); ok {
		t.Fatal("subscription_period should live in schema.then, not base properties")
	}
	if fs.Schema.If == nil || fs.Schema.Then == nil {
		t.Fatalf("subscription duration condition missing: if=%#v then=%#v", fs.Schema.If, fs.Schema.Then)
	}
	ifType, ok := fs.Schema.If.Properties.Get("product_type")
	if !ok || ifType.Const != IAPProductTypeSubscription {
		t.Fatalf("schema.if product_type = %#v", ifType)
	}
	duration, ok := fs.Schema.Then.Properties.Get("subscription_period")
	if !ok {
		t.Fatal("schema.then missing subscription_period")
	}
	if duration.Title != "Duration" || duration.Default != "ONE_MONTH" {
		t.Fatalf("subscription_period schema = %#v", duration)
	}
	if !stringSliceContains(fs.Schema.Then.Required, "subscription_period") {
		t.Fatalf("schema.then required = %#v", fs.Schema.Then.Required)
	}
	for _, field := range []string{
		"display_name",
		"price_currency",
		"price_minor_units",
		"enabled",
	} {
		if stringSliceContains(fs.Schema.Required, field) {
			t.Fatalf("create schema should not require optional field %q: %#v", field, fs.Schema.Required)
		}
	}
	order, _ := fs.UISchema["ui:order"].([]any)
	for _, field := range []string{"product_id", "display_name", "store_environment", "product_type", "points_grant", "price_currency", "price_minor_units", "enabled"} {
		if !anySliceContains(order, field) {
			t.Fatalf("ui:order missing %q: %#v", field, order)
		}
	}
	for _, field := range hiddenFields {
		if anySliceContains(order, field) {
			t.Fatalf("ui:order should not contain hidden field %q: %#v", field, order)
		}
	}
}

func TestIAPProductsResourceEditSchemaHidesAppStoreMetadata(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	s := &Server{d: Deps{IAPProducts: iaps}}
	res := s.newIAPProductsResource()
	raw, err := res.Schema(context.Background(), adminReq("1"), admin.ActionEdit)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	fs, ok := raw.(*admin.FormSchema)
	if !ok {
		t.Fatalf("Schema = %T, want *admin.FormSchema", raw)
	}
	for _, field := range []string{
		"app_store_price_point_id",
		"app_store_subscription_group_id",
		"app_store_locale",
		"subscription_display_name",
		"subscription_description",
		"app_store_review_note",
		"app_store_review_screenshot_url",
		"app_store_subscription_localization_id",
		"revenuecat_offering_id",
	} {
		if _, ok := fs.Schema.Properties.Get(field); ok {
			t.Fatalf("edit schema should hide metadata field %q", field)
		}
	}
}

func TestIAPProductsResourceRequiresSyncerForEnabledProduct(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	s := &Server{d: Deps{IAPProducts: iaps}}
	res := s.newIAPProductsResource()

	_, err := res.Act(context.Background(), adminReq(""), admin.ActionCreate, map[string]any{
		"product_id":        "app.rxlab.points1000",
		"store_environment": IAPStoreEnvironmentAppStore,
		"product_type":      IAPProductTypeConsumable,
		"points_grant":      float64(1000),
		"enabled":           true,
	})
	if err == nil {
		t.Fatal("expected enabled product creation without syncer to fail")
	}
	rows, _, listErr := iaps.List(context.Background(), 0, 20)
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after failed sync = %#v, want empty", rows)
	}
}

func TestIAPProductsResourceCreateRollsBackWhenSyncFails(t *testing.T) {
	iaps := newTestIAPProductStore(t)
	syncer := &fakeIAPProductSyncer{err: errors.New("sync failed")}
	s := &Server{d: Deps{IAPProducts: iaps, IAPProductSyncer: syncer}}
	res := s.newIAPProductsResource()

	_, err := res.Act(context.Background(), adminReq(""), admin.ActionCreate, map[string]any{
		"product_id":        "app.rxlab.points1000",
		"store_environment": IAPStoreEnvironmentAppStore,
		"product_type":      IAPProductTypeConsumable,
		"points_grant":      float64(1000),
		"enabled":           true,
	})
	if err == nil {
		t.Fatal("expected sync failure")
	}
	rows, _, listErr := iaps.List(context.Background(), 0, 20)
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after failed sync = %#v, want empty", rows)
	}
	if len(syncer.products) != 1 {
		t.Fatalf("sync attempts = %d, want 1", len(syncer.products))
	}
}

type fakeIAPProductSyncer struct {
	err      error
	products []IAPProduct
	deleted  []IAPProduct
}

func (f *fakeIAPProductSyncer) SyncIAPProduct(_ context.Context, p IAPProduct) (IAPProductSyncResult, error) {
	f.products = append(f.products, p)
	return IAPProductSyncResult{}, f.err
}

func (f *fakeIAPProductSyncer) DeleteIAPProduct(_ context.Context, p IAPProduct) error {
	f.deleted = append(f.deleted, p)
	return f.err
}

func anySliceContains(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func tableHasColumn(table *admin.TableSchema, name string) bool {
	for _, col := range table.Columns {
		if col.Name == name {
			return true
		}
	}
	return false
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
