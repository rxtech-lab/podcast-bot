package server

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/sirily11/debate-bot/internal/config"
)

// ---------------------------------------------------------------------------
// Subscription permissions (custom CRUD resource)
//
// Maps each subscription class (an IAP subscription product, plus a free /
// no-subscription sentinel) to a nested permission object. The form is
// flattened into grouped fields and reassembled into nested JSON in Act.
// ---------------------------------------------------------------------------

// subscriptionPermissionForm is the DTO reflected into the create/edit form.
// The nested Permissions object is flattened here so admin-generator renders a
// reliable flat form; Act reassembles it.
type subscriptionPermissionForm struct {
	SubscriptionClass string `json:"subscription_class" jsonschema:"title=Subscription class" validate:"required"`

	StudioDiscussion bool `json:"studio_discussion" jsonschema:"title=Studio · Discussion (multi-host talk show)"`
	StudioAudioBook  bool `json:"studio_audio_book" jsonschema:"title=Studio · Audiobook (single-narrator reading)"`
	StudioAlbum      bool `json:"studio_album" jsonschema:"title=Studio · Album (multi-episode podcast series)"`

	CanPublishPodcast        bool `json:"can_publish_podcast" jsonschema:"title=Publish podcast to the public catalog"`
	CanSharePodcastPrivately bool `json:"can_share_privately" jsonschema:"title=Share podcast via private link"`
	CanGenerateVideo         bool `json:"can_generate_video" jsonschema:"title=Generate video from an episode"`
	CanGenerateSummary       bool `json:"can_generate_summary" jsonschema:"title=Generate text summary"`
	CanExportToNotion        bool `json:"can_export_notion" jsonschema:"title=Export to Notion"`
	CanGeneratePPT           bool `json:"can_generate_ppt" jsonschema:"title=Generate slide deck (PPT)"`
	CanGenerateMindmap       bool `json:"can_generate_mindmap" jsonschema:"title=Generate mindmap"`
	CanGenerateCoverWithAI   bool `json:"can_generate_cover" jsonschema:"title=Generate cover art with AI"`
	CanUploadOwnAudio        bool `json:"can_upload_own_audio" jsonschema:"title=Upload own audio as a podcast"`

	MaxUploadAudioMB int64 `json:"max_upload_audio_mb" jsonschema:"title=Max audio upload size (MB)"`

	ModelsMode  string   `json:"models_mode" jsonschema:"title=Models,enum=all,enum=only,default=all"`
	ModelsAllow []string `json:"models_allow,omitempty" jsonschema:"title=Allowed models"`
	VoicesMode  string   `json:"voices_mode" jsonschema:"title=Voices,enum=all,enum=only,default=all"`
	VoicesAllow []string `json:"voices_allow,omitempty" jsonschema:"title=Allowed voices"`
}

type subscriptionPermissionsResource struct{ s *Server }

func (s *Server) newSubscriptionPermissionsResource() admin.Resource {
	return &subscriptionPermissionsResource{s: s}
}

func (r *subscriptionPermissionsResource) ID() string { return "subscription-permissions" }

func (r *subscriptionPermissionsResource) actionURL(req admin.Request, action admin.ActionType, dynamicPath string) string {
	u := req.BasePath + "/resources/subscription-permissions/action?action=" + string(action)
	if dynamicPath != "" {
		u += "&dynamicPath=" + url.QueryEscape(dynamicPath)
	}
	return u
}

func (r *subscriptionPermissionsResource) authorize(ctx context.Context, req admin.Request, action admin.ActionType) error {
	return requireAdmin()(ctx, req.Identity, action)
}

func (r *subscriptionPermissionsResource) Info(_ context.Context, req admin.Request) admin.ResourceInfo {
	return admin.ResourceInfo{
		ID:            r.ID(),
		Name:          "Subscription Permissions",
		Description:   "Per-subscription-class feature, studio, model, and voice permissions. Choose a subscription product (or the free class) and grant access.",
		Icon:          "shield-check",
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

func (r *subscriptionPermissionsResource) Schema(ctx context.Context, req admin.Request, action admin.ActionType) (any, error) {
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
				{Name: "class_label", Label: "Subscription class", Type: "string", Pinned: true},
				{Name: "store_environment", Label: "Store", Type: "string", Format: "chip"},
				{Name: "summary", Label: "Grants", Type: "string"},
				{Name: "created_at", Label: "Created", Type: "string", Format: "date-time"},
			},
		}, nil
	case admin.ActionCreate, admin.ActionEdit:
		label := "Create"
		if action == admin.ActionEdit {
			label = "Save"
		}
		fs, err := admin.FormSchemaFromModel(subscriptionPermissionForm{}, action, label,
			r.actionURL(req, action, strings.Trim(req.DynamicPath, "/")))
		if err != nil {
			return nil, err
		}
		r.applySchema(ctx, fs)
		return fs, nil
	default:
		return nil, fmt.Errorf("%w: no schema for action %q", admin.ErrBadInput, action)
	}
}

func (r *subscriptionPermissionsResource) applySchema(ctx context.Context, fs *admin.FormSchema) {
	if fs == nil || fs.Schema == nil || fs.Schema.Properties == nil {
		return
	}
	if p, ok := fs.Schema.Properties.Get("subscription_class"); ok {
		p.OneOf = r.s.subscriptionClassOptions(ctx)
		p.Description = "Pick the subscription product this permission set applies to, or the free / no-subscription class. Read live from the IAP catalog."
	}

	// Helper text under each toggle. "Studio" is the in-app creation surface;
	// these three flags decide which creation modes the studio offers this class.
	// The rest gate what a user can do with a finished episode.
	setPropDescription(fs, "studio_discussion", "Allow creating a Discussion in the studio: a multi-host talk show where AI voices debate or converse about the source material.")
	setPropDescription(fs, "studio_audio_book", "Allow creating an Audiobook in the studio: a single narrator reads the source document straight through.")
	setPropDescription(fs, "studio_album", "Allow creating an Album in the studio: a multi-episode podcast series grouped under one show.")
	setPropDescription(fs, "can_publish_podcast", "Allow publishing an episode to the public podcast catalog where anyone can discover it.")
	setPropDescription(fs, "can_share_privately", "Allow sharing an episode through a private, unlisted link instead of publishing it publicly.")
	setPropDescription(fs, "can_generate_video", "Allow rendering a shareable video (audio + visuals) from an episode.")
	setPropDescription(fs, "can_generate_summary", "Allow generating a written text summary of an episode.")
	setPropDescription(fs, "can_export_notion", "Allow exporting an episode or its summary to the user's Notion workspace.")
	setPropDescription(fs, "can_generate_ppt", "Allow generating a slide deck (PowerPoint) from an episode.")
	setPropDescription(fs, "can_generate_mindmap", "Allow generating a mindmap of an episode's key points.")
	setPropDescription(fs, "can_generate_cover", "Allow generating cover artwork for an episode or album with AI.")
	setPropDescription(fs, "can_upload_own_audio", "Allow creating a podcast from the user's own uploaded audio (server-side transcription). Also requires the global App Config toggle.")
	setPropDescription(fs, "max_upload_audio_mb", "Largest audio file this tier may upload, in MB. 0 uses the server-wide default cap.")

	// The reflector marks every non-pointer bool as `required`, but the studio and
	// feature toggles are optional: an unchecked box is simply absent from the
	// submitted data, which would otherwise trip AJV's "must have required
	// property" for each. Drop them from `required` so unchecked == false (which
	// is exactly how permissionsFromForm reads a missing key). Only
	// subscription_class is genuinely required; the mode selectors always carry a
	// default value so they stay present regardless.
	fs.Schema.Required = withoutSchemaRequiredFields(fs.Schema.Required,
		"studio_discussion", "studio_audio_book", "studio_album",
		"can_publish_podcast", "can_share_privately", "can_generate_video",
		"can_generate_summary", "can_export_notion", "can_generate_ppt",
		"can_generate_mindmap", "can_generate_cover", "can_upload_own_audio",
		"max_upload_audio_mb",
	)

	// Each allowlist array only makes sense in "only" mode, so pull it out of the
	// base properties and inject it via draft-07 `dependencies` keyed on the mode
	// selector — RJSF then renders the picker only when its mode = "only".
	// (invopop/jsonschema has no typed `dependencies` field, so it goes via Extras.)
	deps := map[string]any{}
	if p, ok := fs.Schema.Properties.Get("models_allow"); ok {
		if p.Items == nil {
			p.Items = &jsonschema.Schema{Type: "string"}
		}
		// Emit the picker options as a flat `enum`, NOT `oneOf: [{const,title}]`.
		// The live catalog is ~300+ models; a labeled oneOf inlines one subschema
		// per option, and RJSF's AJV instance (allErrors:true) compiles that into
		// a single giant validation function that overflows the browser's V8 call
		// stack ("Maximum call stack size exceeded"), while Node's larger stack
		// tolerates it. An enum compiles to one membership check. Labels are lost,
		// but the id already carries the provider (e.g. "openai/gpt-4o"), so the
		// bare value is self-descriptive.
		p.Items.Enum = modelIDEnum(r.s.modelCatalog(ctx))
		// Leave UniqueItems unset: with it, RJSF treats an enum-item array as a
		// multi-select checkbox list. We want the standard array widget (an
		// "Add" button plus one dropdown per row), so keep it a plain array.
		p.Description = "Whitelist of model ids the user may pick. Add one row per allowed model."
		fs.Schema.Properties.Delete("models_allow")
		fs.Schema.Required = withoutSchemaRequiredFields(fs.Schema.Required, "models_allow")
		deps["models_mode"] = modeDependency("models_mode", "models_allow", p)
	}
	if p, ok := fs.Schema.Properties.Get("voices_allow"); ok {
		if p.Items == nil {
			p.Items = &jsonschema.Schema{Type: "string"}
		}
		// Flat `enum` rather than a labeled `oneOf` — see models_allow above. The
		// voice roster is ~450+ entries; inlining a subschema per voice is what
		// blows the browser's stack. The ShortName already encodes the locale
		// (e.g. "en-US-JennyNeural"), so dropping the label costs little.
		p.Items.Enum = r.s.voiceShortNameEnum(ctx)
		p.Description = "Whitelist of Azure voice ShortNames the user may pick. Add one row per allowed voice."
		fs.Schema.Properties.Delete("voices_allow")
		fs.Schema.Required = withoutSchemaRequiredFields(fs.Schema.Required, "voices_allow")
		deps["voices_mode"] = modeDependency("voices_mode", "voices_allow", p)
	}
	if len(deps) > 0 {
		if fs.Schema.Extras == nil {
			fs.Schema.Extras = map[string]any{}
		}
		fs.Schema.Extras["dependencies"] = deps
		// Remove the reflector's default `additionalProperties: false` entirely
		// (set it absent, NOT true). Draft-07's additionalProperties can't see
		// fields injected via `dependencies`, so `false` rejects the allowlist in
		// "only" mode ("must NOT have additional properties"). But setting it to
		// `true` makes RJSF's canExpand render a free-form add-key/value editor.
		// Absent is the sweet spot: AJV allows the injected fields (draft-07's
		// default) and canExpand returns false, so no editor — exactly how RJSF's
		// own `dependencies` examples are written.
		fs.Schema.AdditionalProperties = nil
	}

	if fs.UISchema == nil {
		fs.UISchema = admin.UISchema{}
	}
	// The trailing "*" wildcard lets RJSF accept the dependency-injected allowlist
	// fields (which appear only in "only" mode) without tripping its "ui:order
	// does not contain property" check. They can't be listed explicitly because
	// RJSF also rejects order entries absent from the currently resolved schema.
	fs.UISchema["ui:order"] = []any{
		"subscription_class",
		"studio_discussion", "studio_audio_book", "studio_album",
		"can_publish_podcast", "can_share_privately", "can_generate_video",
		"can_generate_summary", "can_export_notion", "can_generate_ppt",
		"can_generate_mindmap", "can_generate_cover", "can_upload_own_audio",
		"max_upload_audio_mb",
		"models_mode",
		"voices_mode",
		"*",
	}
}

func (r *subscriptionPermissionsResource) Fetch(ctx context.Context, req admin.Request, action admin.ActionType, _ map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	if r.s.d.SubscriptionPermissions == nil {
		return nil, fmt.Errorf("%w: subscription permission store unavailable", admin.ErrBadInput)
	}
	switch action {
	case admin.ActionView:
		limit := 20
		if l, err := strconv.Atoi(req.Query.Get("limit")); err == nil && l > 0 {
			limit = l
		}
		after, _ := strconv.ParseInt(req.Query.Get("after"), 10, 64)
		rows, next, err := r.s.d.SubscriptionPermissions.List(ctx, after, limit)
		if err != nil {
			return nil, err
		}
		items := make([]admin.Item, 0, len(rows))
		for _, row := range rows {
			id := strconv.FormatInt(row.ID, 10)
			items = append(items, admin.Item{
				Data: map[string]any{
					"id":                row.ID,
					"class_label":       classLabel(row.ProductID),
					"store_environment": row.StoreEnvironment,
					"summary":           permissionsSummary(row.Permissions),
					"created_at":        row.CreatedAt,
				},
				DynamicPath: id,
				Actions: []admin.ActionButton{
					{
						Type:       admin.ButtonSecondary,
						Label:      "Edit",
						Icon:       "pencil",
						Behavior:   admin.BehaviorOpenSheet,
						ActionType: admin.ActionEdit,
						OnClick:    r.actionURL(req, admin.ActionEdit, id),
					},
					{
						Type:       admin.ButtonDanger,
						Label:      "Delete",
						Icon:       "trash-2",
						Behavior:   admin.BehaviorConfirmDialog,
						ActionType: admin.ActionDelete,
						OnClick:    r.actionURL(req, admin.ActionDelete, id),
					},
				},
			})
		}
		var nextURL *string
		if next > 0 {
			u := r.actionURL(req, admin.ActionView, "") + "&after=" + strconv.FormatInt(next, 10) + "&limit=" + strconv.Itoa(limit)
			nextURL = &u
		}
		return admin.Paginated(items, nil, nextURL, nil), nil
	case admin.ActionCreate:
		// Sensible default for a new (typically paid) class: grant everything.
		return admin.Detail(map[string]any{
			"studio_discussion": true, "studio_audio_book": true, "studio_album": true,
			"can_publish_podcast": true, "can_share_privately": true, "can_generate_video": true,
			"can_generate_summary": true, "can_export_notion": true, "can_generate_ppt": true,
			"can_generate_mindmap": true, "can_generate_cover": true, "can_upload_own_audio": true,
			"max_upload_audio_mb": int64(0),
			"models_mode":         PermissionModeAll, "voices_mode": PermissionModeAll,
		}), nil
	case admin.ActionEdit:
		id, err := strconv.ParseInt(strings.Trim(req.DynamicPath, "/"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing permission id", admin.ErrBadInput)
		}
		row, err := r.s.d.SubscriptionPermissions.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, fmt.Errorf("%w: permission not found", admin.ErrBadInput)
		}
		return admin.Detail(formFromRow(row)), nil
	default:
		return nil, fmt.Errorf("%w: cannot fetch action %q", admin.ErrBadInput, action)
	}
}

func (r *subscriptionPermissionsResource) Act(ctx context.Context, req admin.Request, action admin.ActionType, data map[string]any) (*admin.ActionResponse, error) {
	if err := r.authorize(ctx, req, action); err != nil {
		return nil, err
	}
	if r.s.d.SubscriptionPermissions == nil {
		return nil, fmt.Errorf("%w: subscription permission store unavailable", admin.ErrBadInput)
	}
	switch action {
	case admin.ActionCreate:
		productID, storeEnv := decodeClassValue(stringField(data, "subscription_class"))
		if existing, err := r.s.d.SubscriptionPermissions.GetForClass(ctx, productID, storeEnv); err != nil {
			return nil, err
		} else if existing != nil {
			return nil, &admin.ValidationError{Fields: map[string]string{"subscription_class": "this class already has permissions; edit it instead"}}
		}
		row := SubscriptionPermission{
			ProductID:        productID,
			StoreEnvironment: storeEnv,
			Permissions:      permissionsFromForm(data),
		}
		if err := r.s.d.SubscriptionPermissions.Create(ctx, &row); err != nil {
			return nil, err
		}
		r.s.invalidateEntitlementsCache(ctx)
		return admin.Detail(formFromRow(&row)), nil
	case admin.ActionEdit:
		id, err := strconv.ParseInt(strings.Trim(req.DynamicPath, "/"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing permission id", admin.ErrBadInput)
		}
		existing, err := r.s.d.SubscriptionPermissions.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("%w: permission not found", admin.ErrBadInput)
		}
		productID, storeEnv := decodeClassValue(stringField(data, "subscription_class"))
		perms := permissionsFromForm(data)
		// The allowlists are dependency-injected, so when the matching mode = "all"
		// the field isn't rendered and is absent from the submission. Preserve the
		// stored value in that case rather than clobbering it with an empty list. A
		// present (even empty) key means the admin edited the picker, so honor it.
		if _, ok := data["models_allow"]; !ok {
			perms.Models.Allow = existing.Permissions.Models.Allow
		}
		if _, ok := data["voices_allow"]; !ok {
			perms.Voices.Allow = existing.Permissions.Voices.Allow
		}
		row := SubscriptionPermission{
			ProductID:        productID,
			StoreEnvironment: storeEnv,
			Permissions:      perms,
		}
		if err := r.s.d.SubscriptionPermissions.Update(ctx, id, &row); err != nil {
			return nil, err
		}
		r.s.invalidateEntitlementsCache(ctx)
		return admin.Detail(formFromRow(&row)), nil
	case admin.ActionDelete:
		id, err := strconv.ParseInt(strings.Trim(req.DynamicPath, "/"), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing permission id", admin.ErrBadInput)
		}
		if err := r.s.d.SubscriptionPermissions.Delete(ctx, id); err != nil {
			return nil, err
		}
		r.s.invalidateEntitlementsCache(ctx)
		return admin.Detail(map[string]any{"deleted": true, "id": id}), nil
	default:
		return nil, fmt.Errorf("%w: cannot execute action %q", admin.ErrBadInput, action)
	}
}

// modeDependency builds a draft-07 schema `dependencies` entry keyed on a mode
// selector: a oneOf whose "all" branch adds nothing and whose "only" branch
// injects the allowlist array field. RJSF matches the branch to the current mode
// value, so the picker renders only in "only" mode.
// setPropDescription sets the gray helper text rendered under a form field, if
// that field is present in the schema.
func setPropDescription(fs *admin.FormSchema, field, description string) {
	if p, ok := fs.Schema.Properties.Get(field); ok {
		p.Description = description
	}
}

func modeDependency(modeField, allowField string, allowSchema *jsonschema.Schema) map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{
				"properties": map[string]any{
					modeField: map[string]any{"enum": []string{PermissionModeAll}},
				},
			},
			map[string]any{
				"properties": map[string]any{
					modeField:  map[string]any{"enum": []string{PermissionModeOnly}},
					allowField: allowSchema,
				},
			},
		},
	}
}

// subscriptionClassOptions builds the class dropdown: the free sentinel plus
// every enabled/known IAP subscription product.
func (s *Server) subscriptionClassOptions(ctx context.Context) []*jsonschema.Schema {
	opts := []*jsonschema.Schema{{Const: "free", Title: "No subscription (free)"}}
	if s.d.IAPProducts == nil {
		return opts
	}
	rows, _, err := s.d.IAPProducts.List(ctx, 0, 500)
	if err != nil {
		return opts
	}
	for _, p := range rows {
		if p.ProductType != IAPProductTypeSubscription {
			continue
		}
		label := p.ProductID
		if strings.TrimSpace(p.DisplayName) != "" {
			label = p.DisplayName + " (" + p.ProductID + ")"
		}
		opts = append(opts, &jsonschema.Schema{
			Const: encodeClassValue(p.ProductID, p.StoreEnvironment),
			Title: fmt.Sprintf("%s [%s]", label, p.StoreEnvironment),
		})
	}
	return opts
}

// modelIDEnum returns the live model ids as a flat enum ([]any of strings). See
// applySchema for why this is an enum and not a labeled oneOf.
func modelIDEnum(models []config.ModelInfo) []any {
	out := make([]any, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

// voiceShortNameEnum returns the catalog voice ShortNames as a flat enum. See
// applySchema for why this is an enum and not a labeled oneOf.
func (s *Server) voiceShortNameEnum(ctx context.Context) []any {
	voices := s.catalogVoices(ctx)
	out := make([]any, 0, len(voices))
	for _, v := range voices {
		out = append(out, v.ShortName)
	}
	return out
}

// encodeClassValue packs a class key into a single dropdown value.
func encodeClassValue(productID, storeEnv string) string {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return "free"
	}
	return normalizeIAPStoreEnvironment(storeEnv) + "|" + productID
}

// decodeClassValue unpacks a dropdown value into product id + store environment.
// "free" (or empty) yields the free-class empty sentinel.
func decodeClassValue(v string) (productID, storeEnv string) {
	v = strings.TrimSpace(v)
	if v == "" || v == "free" {
		return "", ""
	}
	if i := strings.Index(v, "|"); i >= 0 {
		return strings.TrimSpace(v[i+1:]), strings.TrimSpace(v[:i])
	}
	return v, ""
}

func classLabel(productID string) string {
	if strings.TrimSpace(productID) == "" {
		return "Free (no subscription)"
	}
	return productID
}

// permissionsSummary is a compact human-readable grant summary for the table.
func permissionsSummary(p Permissions) string {
	features := 0
	for _, on := range []bool{
		p.Features.CanPublishPodcast, p.Features.CanSharePodcastPrivately, p.Features.CanGenerateVideo,
		p.Features.CanGenerateSummary, p.Features.CanExportToNotion, p.Features.CanGeneratePPT,
		p.Features.CanGenerateMindmap, p.Features.CanGenerateCoverWithAI,
	} {
		if on {
			features++
		}
	}
	return fmt.Sprintf("%d/8 features · models: %s · voices: %s", features, p.Models.Mode, p.Voices.Mode)
}

// formFromRow flattens a stored row back into the form field map for editing.
func formFromRow(row *SubscriptionPermission) map[string]any {
	p := row.Permissions
	return map[string]any{
		"subscription_class":   encodeClassValue(row.ProductID, row.StoreEnvironment),
		"studio_discussion":    p.Studios.Discussion,
		"studio_audio_book":    p.Studios.AudioBook,
		"studio_album":         p.Studios.Album,
		"can_publish_podcast":  p.Features.CanPublishPodcast,
		"can_share_privately":  p.Features.CanSharePodcastPrivately,
		"can_generate_video":   p.Features.CanGenerateVideo,
		"can_generate_summary": p.Features.CanGenerateSummary,
		"can_export_notion":    p.Features.CanExportToNotion,
		"can_generate_ppt":     p.Features.CanGeneratePPT,
		"can_generate_mindmap": p.Features.CanGenerateMindmap,
		"can_generate_cover":   p.Features.CanGenerateCoverWithAI,
		"can_upload_own_audio": p.Features.CanUploadOwnAudio,
		"max_upload_audio_mb":  p.Limits.MaxUploadAudioMB,
		"models_mode":          p.Models.Mode,
		"models_allow":         p.Models.Allow,
		"voices_mode":          p.Voices.Mode,
		"voices_allow":         p.Voices.Allow,
	}
}

// permissionsFromForm reassembles the flattened form fields into the nested
// permission object.
func permissionsFromForm(data map[string]any) Permissions {
	return Permissions{
		Studios: PermissionStudios{
			Discussion: boolField(data, "studio_discussion", false),
			AudioBook:  boolField(data, "studio_audio_book", false),
			Album:      boolField(data, "studio_album", false),
		},
		Features: PermissionFeatures{
			CanPublishPodcast:        boolField(data, "can_publish_podcast", false),
			CanSharePodcastPrivately: boolField(data, "can_share_privately", false),
			CanGenerateVideo:         boolField(data, "can_generate_video", false),
			CanGenerateSummary:       boolField(data, "can_generate_summary", false),
			CanExportToNotion:        boolField(data, "can_export_notion", false),
			CanGeneratePPT:           boolField(data, "can_generate_ppt", false),
			CanGenerateMindmap:       boolField(data, "can_generate_mindmap", false),
			CanGenerateCoverWithAI:   boolField(data, "can_generate_cover", false),
			CanUploadOwnAudio:        boolField(data, "can_upload_own_audio", false),
		},
		Models: PermissionRule{Mode: stringField(data, "models_mode"), Allow: stringSliceField(data, "models_allow")},
		Voices: PermissionRule{Mode: stringField(data, "voices_mode"), Allow: stringSliceField(data, "voices_allow")},
		Limits: PermissionLimits{MaxUploadAudioMB: max(int64Field(data, "max_upload_audio_mb"), 0)},
	}
}

// stringSliceField reads a []string from form data, tolerating the []any shape
// JSON decoding produces.
func stringSliceField(data map[string]any, key string) []string {
	// The allowlists render as a plain array widget (no uniqueItems), so the
	// submitted list may contain blanks/duplicates — dedupe while preserving the
	// admin's chosen order.
	seen := map[string]struct{}{}
	appendUnique := func(out []string, s string) []string {
		s = strings.TrimSpace(s)
		if s == "" {
			return out
		}
		if _, dup := seen[s]; dup {
			return out
		}
		seen[s] = struct{}{}
		return append(out, s)
	}
	switch v := data[key].(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = appendUnique(out, s)
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = appendUnique(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}
