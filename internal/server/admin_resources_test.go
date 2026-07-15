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

func TestAdminHandlerMountsInE2EModeWithoutIssuer(t *testing.T) {
	s := New(Deps{
		Mode: ModeDashboard,
		Env:  &config.Env{E2EMode: true},
	})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/resources", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/resources = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"app-config"`) {
		t.Fatalf("admin resources missing app-config: %s", rec.Body.String())
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
	if len(list.Items[0].Actions) != 1 || list.Items[0].Actions[0].Label != "Manage" {
		t.Errorf("expected a Manage row action, got %#v", list.Items[0].Actions)
	}
	if list.Items[0].DynamicPath != "u1" {
		t.Errorf("row dynamic path = %q, want u1", list.Items[0].DynamicPath)
	}
}

func TestUsersResourceDetailDashboard(t *testing.T) {
	ps, ds := newTestPointsStore(t)
	ctx := context.Background()
	owner := "oauth:user-detail"
	now := time.Now().UTC()

	if _, err := ps.Credit(ctx, owner, 1_000, "purchase:INITIAL_PURCHASE", "purchase-detail-1"); err != nil {
		t.Fatalf("purchase credit: %v", err)
	}
	if _, err := ps.Credit(ctx, owner, 500, pointsReasonAdminTopup, "admin-detail-1"); err != nil {
		t.Fatalf("admin credit: %v", err)
	}
	if _, err := ds.db.ExecContext(ctx, `INSERT INTO creator_profiles
		(user_id, display_name, username, avatar_url, updated_at) VALUES (?, ?, ?, ?, ?)`,
		owner, "Detail User", "detail", "https://example.com/avatar.png", now.UnixMilli()); err != nil {
		t.Fatalf("insert creator profile: %v", err)
	}

	discussion, err := ds.Create(ctx, owner, "Dashboard discussion", planResponse{Script: &config.DebateTopic{
		Title: "Dashboard discussion", Type: config.ContentTypeDiscussion,
		Host: config.AgentSpec{Name: "Host", Model: "model-a"},
		Discussants: []config.AgentSpec{
			{Name: "Alice", Model: "model-b"},
			{Name: "Bob", Model: "model-a"},
		},
	}})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	for speaker, voice := range map[string]string{
		"Host":  "en-US-AvaMultilingualNeural",
		"Alice": "en-US-AvaMultilingualNeural",
		"Bob":   "en-US-GuyNeural",
	} {
		if _, err := ds.SetSpeakerVoice(ctx, owner, discussion.ID, speaker, voice); err != nil {
			t.Fatalf("set %s voice: %v", speaker, err)
		}
	}
	if _, err := ds.SetJob(ctx, owner, discussion.ID, "job-detail"); err != nil {
		t.Fatalf("set job: %v", err)
	}
	if err := ds.SetJobResultAndUsage(ctx, discussion.ID, DiscussionReady, "", PointsUsageDetail{
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, LLMCostUSD: 0.4, LLMCostKnown: true, TTSCostUSD: 0.2,
	}); err != nil {
		t.Fatalf("set ready result: %v", err)
	}
	audioBook, err := ds.Create(ctx, owner, "Dashboard audiobook", planResponse{Script: &config.DebateTopic{
		Title: "Dashboard audiobook", Type: config.ContentTypeAudioBook,
		AudioBookHost: config.AgentSpec{Name: "Narrator", Model: "model-a"},
	}})
	if err != nil {
		t.Fatalf("create audiobook: %v", err)
	}
	if _, err := ds.SetJob(ctx, owner, audioBook.ID, "job-audio-detail"); err != nil {
		t.Fatalf("set audiobook job: %v", err)
	}
	if err := ds.SetJobResult(ctx, audioBook.ID, DiscussionFailed, ""); err != nil {
		t.Fatalf("set audiobook failed: %v", err)
	}

	for _, row := range []struct {
		discussionID string
		reason       string
		cost         float64
		prompt       int64
		completion   int64
		total        int64
		llm          float64
		tts          float64
	}{
		{discussionID: discussion.ID, reason: pointsReasonGeneration, cost: 0.6, prompt: 100, completion: 50, total: 150, llm: 0.4, tts: 0.2},
		{discussionID: discussion.ID, reason: pointsReasonImageGeneration, cost: 0.1},
	} {
		if _, err := ps.db.ExecContext(ctx, `INSERT INTO points_ledger
			(user_id, discussion_id, delta, reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
			 llm_cost_usd, tts_cost_usd, balance_after, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			owner, row.discussionID, -1, row.reason, row.cost, row.prompt, row.completion, row.total,
			row.llm, row.tts, 1_498, now.UnixMilli()); err != nil {
			t.Fatalf("insert usage ledger row: %v", err)
		}
	}

	s := &Server{d: Deps{Points: ps}}
	res := s.newUsersResource()
	// Next.js can preserve encodeURIComponent output in its catch-all slug, so
	// exercise the same oauth%3A... dynamic path seen by the deployed admin.
	encodedOwner := "oauth%3Auser-detail"
	raw, err := res.Schema(ctx, adminReq(encodedOwner), admin.ActionView)
	if err != nil {
		t.Fatalf("detail schema: %v", err)
	}
	page, ok := raw.(*admin.CustomResourcePage)
	if !ok {
		t.Fatalf("detail schema = %T, want *admin.CustomResourcePage", raw)
	}
	if page.UIType != "custom" || len(page.Sections) != 7 {
		t.Fatalf("detail page = %#v", page)
	}
	stats := page.Sections[0].Statistics
	if len(stats) != 6 || stats[1].Value != "2" || stats[2].Value != "150" || stats[4].Value != "1" || stats[5].Value != "1,500" {
		t.Fatalf("detail stats = %#v", stats)
	}
	if page.Sections[0].Title != "Detail User" || !strings.Contains(page.Sections[1].Body, owner) {
		t.Fatalf("detail identity sections = %#v / %#v", page.Sections[0], page.Sections[1])
	}
	if len(page.ActionButtons) != 3 || !strings.Contains(page.ActionButtons[1].OnClick, "action=edit") {
		t.Fatalf("detail actions = %#v", page.ActionButtons)
	}
	acted, err := res.Act(ctx, adminReq(encodedOwner), admin.ActionEdit, map[string]any{})
	if err != nil {
		t.Fatalf("encoded user action: %v", err)
	}
	if result, _ := acted.Data.(map[string]any); result["user_id"] != owner {
		t.Fatalf("encoded action user = %#v, want %q", acted.Data, owner)
	}

	var typeChart, modelChart, voiceChart *admin.Chart
	for i := range page.Sections {
		for j := range page.Sections[i].Children {
			chart := &page.Sections[i].Children[j]
			switch chart.Title {
			case "Podcasts by type":
				typeChart = chart
			case "Models":
				modelChart = chart
			case "Azure voices":
				voiceChart = chart
			}
		}
	}
	if typeChart == nil || len(typeChart.Data) != 2 {
		t.Fatalf("type chart = %#v", typeChart)
	}
	if modelChart == nil || len(modelChart.Data) != 2 || modelChart.Data[0]["model"] != "model-a" || modelChart.Data[0]["count"] != int64(3) {
		t.Fatalf("model chart = %#v", modelChart)
	}
	if voiceChart == nil || len(voiceChart.Data) != 2 || voiceChart.Data[0]["voice"] != "en-US-AvaMultilingualNeural" || voiceChart.Data[0]["count"] != int64(2) {
		t.Fatalf("voice chart = %#v", voiceChart)
	}

	if _, err := res.Schema(ctx, adminReq("missing-user"), admin.ActionView); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("missing user error = %v, want ErrNotFound", err)
	}
}

func TestUsersResourceChangeSubscriptionClass(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	iaps := newTestIAPProductStore(t)
	sub := IAPProduct{
		ProductID:        "pro_monthly",
		StoreEnvironment: IAPStoreEnvironmentTest,
		ProductType:      IAPProductTypeSubscription,
		DisplayName:      "Pro",
		PointsGrant:      0,
		Enabled:          true,
	}
	if err := iaps.Create(context.Background(), &sub); err != nil {
		t.Fatalf("create iap product: %v", err)
	}
	s := &Server{d: Deps{Points: ps, IAPProducts: iaps}}
	res := s.newUsersResource()
	ctx := context.Background()

	classValue := encodeClassValue(sub.ProductID, sub.StoreEnvironment)

	// Assign the subscription class to a user with no prior plan.
	if _, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"subscription_class": classValue}); err != nil {
		t.Fatalf("Act set subscription: %v", err)
	}
	got, err := ps.Subscription(ctx, "u1")
	if err != nil || got == nil {
		t.Fatalf("Subscription after set: %v (%#v)", err, got)
	}
	if got.ProductID != "pro_monthly" || got.Status != "active" || !got.Active(0) {
		t.Fatalf("recorded subscription = %#v", got)
	}

	// The prefill now reflects the current class.
	prefill, err := res.Fetch(ctx, adminReq("u1"), admin.ActionEdit, nil)
	if err != nil {
		t.Fatalf("Fetch prefill: %v", err)
	}
	if m, _ := prefill.Data.(map[string]any); m["subscription_class"] != classValue {
		t.Fatalf("prefill class = %#v, want %q", prefill.Data, classValue)
	}

	// Unknown subscription class → validation error.
	if _, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"subscription_class": "test_store|does_not_exist"}); err == nil {
		t.Error("expected unknown-subscription rejection")
	}

	// Setting the free class clears the subscription.
	if _, err := res.Act(ctx, adminReq("u1"), admin.ActionEdit, map[string]any{"subscription_class": "free"}); err != nil {
		t.Fatalf("Act clear subscription: %v", err)
	}
	if got, err := ps.Subscription(ctx, "u1"); err != nil || got != nil {
		t.Fatalf("Subscription after clear = %#v (err %v), want nil", got, err)
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
	if len(stats) != 5 || stats[0].Value != "$1.65" || stats[1].Value != "165" {
		t.Fatalf("stats = %#v", stats)
	}
	if stats[4].Label != "Speech-to-text spend" || stats[4].Value != "$0.00" {
		t.Fatalf("stt stat = %#v", stats[4])
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

func TestUsageDashboardTokenFormatting(t *testing.T) {
	cases := []struct {
		value       int64
		wantCompact string
		wantExact   string
	}{
		{value: 165, wantCompact: "165", wantExact: "165"},
		{value: 1_200, wantCompact: "1.2k", wantExact: "1,200"},
		{value: 12_000, wantCompact: "12k", wantExact: "12,000"},
		{value: 1_200_000, wantCompact: "1.2m", wantExact: "1,200,000"},
		{value: 1_200_000_000, wantCompact: "1.2b", wantExact: "1,200,000,000"},
	}

	for _, tc := range cases {
		if got := formatCompactInt(tc.value); got != tc.wantCompact {
			t.Errorf("formatCompactInt(%d) = %q, want %q", tc.value, got, tc.wantCompact)
		}
		if got := formatDelimitedInt(tc.value); got != tc.wantExact {
			t.Errorf("formatDelimitedInt(%d) = %q, want %q", tc.value, got, tc.wantExact)
		}
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

// TestAppConfigGeminiModelConditional pins the app-config schema shape: the
// Gemini transcription model lives only inside a draft-07 dependencies oneOf
// keyed on stt_provider, so the form shows it only when Gemini is selected.
// Saving with the field absent (Azure selected) must preserve the stored model.
func TestAppConfigGeminiModelConditional(t *testing.T) {
	ac, _ := newTestAppConfigStore(t)
	env := &config.Env{HostModel: "env/host", TranscribeModel: "gemini-2.5-flash"}
	s := &Server{d: Deps{Env: env, AppConfig: ac}}
	res := s.newAppConfigResource()
	ctx := context.Background()

	raw, err := res.Schema(ctx, adminReq(""), admin.ActionView)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	fs := raw.(*admin.FormSchema)
	if _, ok := fs.Schema.Properties.Get("stt_gemini_model"); ok {
		t.Fatal("stt_gemini_model must not be a base property (dependency-injected only)")
	}
	deps, ok := fs.Schema.Extras["dependencies"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing dependencies extras: %#v", fs.Schema.Extras)
	}
	branch, ok := deps["stt_provider"].(map[string]any)
	if !ok {
		t.Fatalf("dependencies missing stt_provider branch: %#v", deps)
	}
	oneOf, ok := branch["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("stt_provider dependency oneOf = %#v", branch["oneOf"])
	}
	if fs.Schema.AdditionalProperties != nil {
		t.Fatal("additionalProperties must be removed for dependency-injected fields")
	}

	// Save with Gemini selected: the model persists.
	if _, err := res.Act(ctx, adminReq(""), admin.ActionEdit, map[string]any{
		"stt_provider": "gemini", "stt_gemini_model": "gemini-2.5-pro",
	}); err != nil {
		t.Fatalf("Act gemini: %v", err)
	}
	if got := s.resolvedSTTGeminiModel(ctx); got != "gemini-2.5-pro" {
		t.Fatalf("stored gemini model = %q", got)
	}
	// Save with Azure selected (field not rendered → absent): stored model kept.
	if _, err := res.Act(ctx, adminReq(""), admin.ActionEdit, map[string]any{
		"stt_provider": "azure",
	}); err != nil {
		t.Fatalf("Act azure: %v", err)
	}
	if got := s.resolvedSTTGeminiModel(ctx); got != "gemini-2.5-pro" {
		t.Fatalf("gemini model after azure save = %q, want preserved", got)
	}
}
