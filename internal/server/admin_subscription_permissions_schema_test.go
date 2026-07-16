package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rxtech-lab/admin-generator/admin"
)

// TestSubscriptionPermissionFormIncludesUploadAudio pins the admin form schema
// to carry the upload-own-audio permission and the per-tier size cap, in the
// declared field order.
func TestSubscriptionPermissionFormIncludesUploadAudio(t *testing.T) {
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "schema.db"), "", "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	s := &Server{d: Deps{}}
	r := &subscriptionPermissionsResource{s: s}

	fs, err := admin.FormSchemaFromModel(subscriptionPermissionForm{}, admin.ActionEdit, "Save", "/x")
	if err != nil {
		t.Fatalf("FormSchemaFromModel: %v", err)
	}
	r.applySchema(context.Background(), fs)

	for _, field := range []string{"can_upload_own_audio", "max_upload_audio_mb"} {
		if _, ok := fs.Schema.Properties.Get(field); !ok {
			t.Fatalf("schema missing %q", field)
		}
	}
	order, _ := fs.UISchema["ui:order"].([]any)
	joined := make([]string, 0, len(order))
	for _, v := range order {
		if sv, ok := v.(string); ok {
			joined = append(joined, sv)
		}
	}
	orderStr := strings.Join(joined, ",")
	if !strings.Contains(orderStr, "can_generate_cover,can_upload_own_audio,max_upload_audio_mb,models_mode") {
		t.Fatalf("ui:order does not place the new fields after cover art: %s", orderStr)
	}
}
