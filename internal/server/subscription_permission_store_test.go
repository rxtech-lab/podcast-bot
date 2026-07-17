package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/sirily11/debate-bot/internal/config"
)

func newTestPermissionStores(t *testing.T) (*SubscriptionPermissionStore, *PointsStore) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "perms.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	sp, err := NewSubscriptionPermissionStore(ds)
	if err != nil {
		t.Fatalf("NewSubscriptionPermissionStore: %v", err)
	}
	points, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}
	return sp, points
}

func fullPermissions() Permissions {
	return Permissions{
		Studios:  PermissionStudios{Discussion: true, AudioBook: true, Album: true},
		Features: PermissionFeatures{CanUseChat: true, CanPublishPodcast: true, CanGenerateVideo: true, CanExportToNotion: true},
		Models:   PermissionRule{Mode: PermissionModeAll},
		Voices:   PermissionRule{Mode: PermissionModeOnly, Allow: []string{"en-US-AvaNeural"}},
	}
}

func TestSubscriptionPermissionStoreCRUD(t *testing.T) {
	sp, _ := newTestPermissionStores(t)
	ctx := context.Background()

	// Free class (empty sentinel).
	free := SubscriptionPermission{Permissions: Permissions{Studios: PermissionStudios{Discussion: true}}}
	if err := sp.Create(ctx, &free); err != nil {
		t.Fatalf("create free: %v", err)
	}
	// A paid class.
	paid := SubscriptionPermission{ProductID: "pro.monthly", StoreEnvironment: "test_store", Permissions: fullPermissions()}
	if err := sp.Create(ctx, &paid); err != nil {
		t.Fatalf("create paid: %v", err)
	}

	got, err := sp.GetForClass(ctx, "pro.monthly", "test_store")
	if err != nil || got == nil {
		t.Fatalf("GetForClass: %v got=%v", err, got)
	}
	if !got.Permissions.Features.CanUseChat || !got.Permissions.Features.CanGenerateVideo || !got.Permissions.Voices.Allows("en-US-AvaNeural") {
		t.Fatalf("paid perms not round-tripped: %+v", got.Permissions)
	}
	if got.Permissions.Voices.Allows("zh-CN-XiaoxiaoNeural") {
		t.Fatalf("voice allowlist should reject non-whitelisted voice")
	}

	gotFree, err := sp.GetFree(ctx)
	if err != nil || gotFree == nil {
		t.Fatalf("GetFree: %v got=%v", err, gotFree)
	}
	if !gotFree.Permissions.Studios.Discussion || gotFree.Permissions.Studios.Album {
		t.Fatalf("free perms not round-tripped: %+v", gotFree.Permissions)
	}

	// Update the paid class.
	paid.Permissions.Features.CanGenerateVideo = false
	if err := sp.Update(ctx, paid.ID, &paid); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = sp.GetForClass(ctx, "pro.monthly", "test_store")
	if got.Permissions.Features.CanGenerateVideo {
		t.Fatalf("update did not persist")
	}

	rows, _, err := sp.List(ctx, 0, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("List: %v len=%d", err, len(rows))
	}

	if err := sp.Delete(ctx, paid.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := sp.GetForClass(ctx, "pro.monthly", "test_store"); got != nil {
		t.Fatalf("expected deleted class to be gone")
	}
}

func TestResolveEntitlements(t *testing.T) {
	sp, points := newTestPermissionStores(t)
	ctx := context.Background()
	s := &Server{d: Deps{SubscriptionPermissions: sp, Points: points}}

	// No config at all → hard default (nothing allowed).
	perms, err := s.resolveEntitlements(ctx, "oauth:nobody")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if perms.Studios.Discussion || perms.Models.Mode != PermissionModeOnly {
		t.Fatalf("expected all-false default, got %+v", perms)
	}

	// Configure a free class + a paid class.
	freeRow := SubscriptionPermission{Permissions: Permissions{Studios: PermissionStudios{Discussion: true}}}
	if err := sp.Create(ctx, &freeRow); err != nil {
		t.Fatalf("create free: %v", err)
	}
	paidRow := SubscriptionPermission{ProductID: "pro.monthly", StoreEnvironment: "test_store", Permissions: fullPermissions()}
	if err := sp.Create(ctx, &paidRow); err != nil {
		t.Fatalf("create paid: %v", err)
	}

	// No subscription → free class.
	perms, _ = s.resolveEntitlements(ctx, "oauth:free-user")
	if !perms.Studios.Discussion || perms.Studios.Album {
		t.Fatalf("expected free class, got %+v", perms)
	}

	product := IAPProduct{ProductID: "pro.monthly", StoreEnvironment: "test_store", ProductType: IAPProductTypeSubscription, DisplayName: "Pro"}

	// Active subscription → paid class.
	if err := points.RecordSubscription(ctx, "oauth:pro-user", product, "active", "evt-1", time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("record active: %v", err)
	}
	perms, _ = s.resolveEntitlements(ctx, "oauth:pro-user")
	if !perms.Studios.Album || !perms.Features.CanExportToNotion {
		t.Fatalf("expected paid class for active sub, got %+v", perms)
	}

	// Expired subscription → falls back to free class.
	if err := points.RecordSubscription(ctx, "oauth:expired-user", product, "active", "evt-2", time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("record expired: %v", err)
	}
	perms, _ = s.resolveEntitlements(ctx, "oauth:expired-user")
	if perms.Studios.Album {
		t.Fatalf("expired sub should fall back to free class, got %+v", perms)
	}
}

func TestSubscriptionPermissionFormConditionalAllowlists(t *testing.T) {
	s := &Server{d: Deps{Env: &config.Env{}}}
	r := &subscriptionPermissionsResource{s: s}
	fs, err := admin.FormSchemaFromModel(subscriptionPermissionForm{}, admin.ActionCreate, "Create", "http://example/create")
	if err != nil {
		t.Fatalf("FormSchemaFromModel: %v", err)
	}
	r.applySchema(context.Background(), fs)

	// The allowlists render only in "only" mode, so they are pulled out of the base
	// properties and injected via draft-07 `dependencies` keyed on the mode field.
	if _, ok := fs.Schema.Properties.Get("models_allow"); ok {
		t.Fatalf("models_allow must be conditional (in dependencies), not a base property")
	}
	if _, ok := fs.Schema.Properties.Get("voices_allow"); ok {
		t.Fatalf("voices_allow must be conditional (in dependencies), not a base property")
	}

	deps, ok := fs.Schema.Extras["dependencies"].(map[string]any)
	if !ok {
		t.Fatalf("expected dependencies in Extras, got %#v", fs.Schema.Extras["dependencies"])
	}
	for _, tc := range []struct{ mode, allow string }{
		{"models_mode", "models_allow"},
		{"voices_mode", "voices_allow"},
	} {
		dep, ok := deps[tc.mode].(map[string]any)
		if !ok {
			t.Fatalf("missing dependency for %s", tc.mode)
		}
		branches, ok := dep["oneOf"].([]any)
		if !ok || len(branches) != 2 {
			t.Fatalf("%s dependency must have 2 oneOf branches, got %#v", tc.mode, dep["oneOf"])
		}
		// The "only" branch injects the allowlist array. Its item schema MUST use a
		// flat `enum`, never a labeled `oneOf: [{const,title}]`: the live catalog is
		// hundreds of entries, and a per-option subschema makes RJSF's AJV compile a
		// validation function large enough to overflow the browser's call stack
		// ("Maximum call stack size exceeded"). An enum compiles to one check.
		onlyBranch, _ := branches[1].(map[string]any)
		props, _ := onlyBranch["properties"].(map[string]any)
		allowSchema, ok := props[tc.allow].(*jsonschema.Schema)
		if !ok {
			t.Fatalf("%s only-branch missing %s schema, got %#v", tc.mode, tc.allow, props[tc.allow])
		}
		if allowSchema.Items == nil {
			t.Fatalf("%s must define item schema", tc.allow)
		}
		if allowSchema.Items.OneOf != nil {
			t.Fatalf("%s items must use enum, not oneOf (oneOf overflows the browser AJV stack)", tc.allow)
		}
	}

	// The optional studio/feature toggles must NOT be required: the reflector marks
	// non-pointer bools required, but an unchecked box is absent from the form
	// data, so a required field trips AJV's "must have required property".
	for _, name := range fs.Schema.Required {
		if strings.HasPrefix(name, "studio_") || strings.HasPrefix(name, "can_") {
			t.Fatalf("optional toggle %q must not be required", name)
		}
	}
	foundClass := false
	for _, name := range fs.Schema.Required {
		if name == "subscription_class" {
			foundClass = true
		}
	}
	if !foundClass {
		t.Fatalf("subscription_class must remain required, got %#v", fs.Schema.Required)
	}

	// additionalProperties must be ABSENT (not false, not true): false rejects the
	// dependency-injected fields, true makes RJSF render the add-key editor. Absent
	// lets AJV allow the injected fields while keeping the editor hidden.
	if fs.Schema.AdditionalProperties != nil {
		t.Fatalf("additionalProperties must be absent, got %#v", fs.Schema.AdditionalProperties)
	}

	raw, err := json.Marshal(fs)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	if !strings.Contains(string(raw), `"dependencies"`) ||
		!strings.Contains(string(raw), `"models_allow"`) ||
		!strings.Contains(string(raw), `"voices_allow"`) {
		t.Fatalf("marshaled schema missing dependencies/allow fields: %s", raw)
	}
	if strings.Contains(string(raw), `"additionalProperties"`) {
		t.Fatalf("marshaled schema must not emit additionalProperties: %s", raw)
	}
}

func TestApplyEntitlementsGraysOutGatedActions(t *testing.T) {
	ent := Permissions{
		Features: PermissionFeatures{CanUseChat: false, CanGenerateVideo: false, CanExportToNotion: true, CanGeneratePPT: false},
	}
	items := []discussionUIActionItem{
		actionItem("chat", "Chat", "", "", "", true, "select", "link"),
		actionItem("open-qa", "Ask", "", "", "", true, "open-sheet", "link"),
		actionItem("generate-video", "Generate Video", "", "", "", true, "request", "link"),
		actionItem("export-notion", "Export to Notion", "", "", "", true, "open-sheet", "link"),
		actionItem("download-pptx", "PPTX", "", "", "", true, "download", "link"),
		actionItem("open-summary", "Summary", "", "", "", true, "open-sheet", "link"),
	}
	got := applyEntitlements(items, ent)
	byID := map[string]bool{}
	for _, it := range got {
		byID[it.ID] = it.Enabled
	}
	if byID["generate-video"] {
		t.Fatalf("generate-video should be disabled")
	}
	if byID["chat"] || byID["open-qa"] {
		t.Fatalf("global and podcast chat should be disabled")
	}
	if !byID["export-notion"] {
		t.Fatalf("export-notion should stay enabled")
	}
	if byID["download-pptx"] {
		t.Fatalf("download-pptx should be disabled")
	}
	if !byID["open-summary"] {
		t.Fatalf("non-gated open-summary must be untouched")
	}
}

func TestRequireChatPermission(t *testing.T) {
	sp, points := newTestPermissionStores(t)
	s := &Server{d: Deps{SubscriptionPermissions: sp, Points: points}}
	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)

	rec := httptest.NewRecorder()
	if s.requireChatPermission(rec, req) {
		t.Fatal("chat should be denied without a granted permission class")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want 403", rec.Code)
	}

	free := SubscriptionPermission{Permissions: Permissions{Features: PermissionFeatures{CanUseChat: true}}}
	if err := sp.Create(req.Context(), &free); err != nil {
		t.Fatalf("create free permission: %v", err)
	}
	rec = httptest.NewRecorder()
	if !s.requireChatPermission(rec, req) {
		t.Fatalf("chat should be allowed when granted; status=%d body=%s", rec.Code, rec.Body.String())
	}
}
