package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/planner"
)

func newMetaServer(t *testing.T, env *config.Env, mcp *config.MCPConfig) *httptest.Server {
	t.Helper()
	bus := eventbus.New(nil)
	srv := New(Deps{
		Bus:      bus,
		Sessions: NewSessionRegistry(),
		Log:      slog.Default(),
		Env:      env,
		MCPCfg:   mcp,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		bus.Close()
	})
	return ts
}

func TestHandleModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openai/custom-host","object":"model","created":0,"owned_by":"test"},{"id":"anthropic/claude-sonnet-4-5","object":"model","created":0,"owned_by":"test"}]}`))
	}))
	t.Cleanup(upstream.Close)

	env := &config.Env{
		OpenAIBaseURL:     upstream.URL,
		OpenAIKey:         "test-key",
		HostModel:         "openai/custom-host",
		ScenePlannerModel: "openai/custom-host",
		CompressionModel:  "openai/gpt-4o-mini",
	}
	ts := newMetaServer(t, env, nil)

	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatalf("get models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Defaults.Host != "openai/custom-host" {
		t.Fatalf("defaults.host = %q, want openai/custom-host", out.Defaults.Host)
	}
	var sawEnvDefault, sawUpstream bool
	for _, m := range out.Models {
		if m.ID == "openai/custom-host" {
			sawEnvDefault = len(m.DefaultFor) == 2 && m.Provider == "openai"
		}
		if m.ID == "anthropic/claude-sonnet-4-5" {
			sawUpstream = m.Provider == "anthropic"
		}
	}
	if !sawEnvDefault {
		t.Error("env-default host model not present in upstream models list")
	}
	if !sawUpstream {
		t.Error("upstream model missing from models list")
	}
}

func TestHandleDiscussionTypes(t *testing.T) {
	ts := newMetaServer(t, nil, nil)

	resp, err := http.Get(ts.URL + "/api/discussion-types")
	if err != nil {
		t.Fatalf("get discussion types: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out discussionTypesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Types) != 1 {
		t.Fatalf("types length = %d, want 1", len(out.Types))
	}
	if out.Types[0].ID != config.ContentTypeDiscussion {
		t.Fatalf("type id = %q, want %q", out.Types[0].ID, config.ContentTypeDiscussion)
	}
}

func TestHandleTemplates(t *testing.T) {
	ts := newMetaServer(t, nil, nil)

	resp, err := http.Get(ts.URL + "/api/templates?type=discussion")
	if err != nil {
		t.Fatalf("get templates: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out templatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Templates) != 2 {
		t.Fatalf("templates length = %d, want 2", len(out.Templates))
	}
	if out.Templates[0].ID != planner.DefaultTemplateID {
		t.Fatalf("template id = %q, want %q", out.Templates[0].ID, planner.DefaultTemplateID)
	}
	if out.Templates[0].Type != config.ContentTypeDiscussion {
		t.Fatalf("template type = %q, want %q", out.Templates[0].Type, config.ContentTypeDiscussion)
	}
	if out.Templates[0].Schema == nil {
		t.Fatal("template schema is nil")
	}
}

func TestHandlePrecheckNewDiscussionForm(t *testing.T) {
	srv := New(Deps{Bus: eventbus.New(nil), Sessions: NewSessionRegistry(), Log: slog.Default()})
	req := httptest.NewRequest(http.MethodGet, "/api/precheck", nil)
	req.Header.Set("X-Client-Platform", "ios")
	req.Header.Set("X-Client-Version", "1.2.3")
	req.Header.Set("X-Client-Build", "42")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out precheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	form := out.NewDiscussion.Form
	if form.Schema["type"] != "object" {
		t.Fatalf("schema.type = %v, want object", form.Schema["type"])
	}
	props, ok := form.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties missing or wrong type: %#v", form.Schema["properties"])
	}
	for _, key := range []string{"prompt", "attachments", "reference", "settings"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("schema.properties missing %q", key)
		}
	}
	attachments := props["attachments"].(map[string]any)
	if attachments["type"] != "array" {
		t.Fatalf("attachments.type = %v, want array", attachments["type"])
	}
	reference := props["reference"].(map[string]any)
	referenceProps := reference["properties"].(map[string]any)
	if _, ok := referenceProps["discussion_id"]; !ok {
		t.Fatal("reference.properties missing discussion_id")
	}
	settings := props["settings"].(map[string]any)
	settingsProps := settings["properties"].(map[string]any)
	for _, key := range []string{"type", "template", "discussants", "language", "generate_cover"} {
		if _, ok := settingsProps[key]; !ok {
			t.Fatalf("settings.properties missing %q", key)
		}
	}
	prompt := props["prompt"].(map[string]any)
	promptProps := prompt["properties"].(map[string]any)
	if _, ok := promptProps["topic"]; !ok {
		t.Fatal("prompt.properties missing topic")
	}
	if len(form.Actions) != 0 {
		t.Fatalf("actions length = %d, want 0", len(form.Actions))
	}
	order, ok := form.UISchema["ui:order"].([]any)
	if !ok {
		t.Fatalf("ui:order missing or wrong type: %#v", form.UISchema["ui:order"])
	}
	if got := form.UISchema["ui:widget"]; got != nil {
		t.Fatalf("ui:widget = %#v, want nil", got)
	}
	wantOrder := []any{"prompt", "attachments", "reference", "settings"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("ui:order = %#v, want %#v", order, wantOrder)
	}
	attachmentsUI := form.UISchema["attachments"].(map[string]any)
	if attachmentsUI["ui:widget"] != "attachmentsPicker" {
		t.Fatalf("attachments.ui:widget = %v, want attachmentsPicker", attachmentsUI["ui:widget"])
	}
	attachmentsOpts := attachmentsUI["ui:options"].(map[string]any)
	if attachmentsOpts["deep_link"] != "debatepod://attachment-picker" {
		t.Fatalf("attachments deep_link = %v", attachmentsOpts["deep_link"])
	}
	settingsUI := form.UISchema["settings"].(map[string]any)
	if settingsUI["ui:objectTemplate"] != "card" {
		t.Fatalf("settings.ui:objectTemplate = %v, want card", settingsUI["ui:objectTemplate"])
	}
	languageUI := settingsUI["language"].(map[string]any)
	if languageUI["ui:widget"] != "glassMenu" {
		t.Fatalf("settings.language.ui:widget = %v, want glassMenu", languageUI["ui:widget"])
	}
	if labels := languageUI["ui:enumNames"].([]any); len(labels) == 0 {
		t.Fatal("settings.language.ui:enumNames empty")
	}
	referenceUI := form.UISchema["reference"].(map[string]any)
	pickerUI := referenceUI["discussion_id"].(map[string]any)
	if pickerUI["ui:widget"] != "discussionPicker" {
		t.Fatalf("reference.discussion_id.ui:widget = %v, want discussionPicker", pickerUI["ui:widget"])
	}
	pickerOpts := pickerUI["ui:options"].(map[string]any)
	if pickerOpts["deep_link"] != "debatepod://discussion-picker" {
		t.Fatalf("reference.discussion_id deep_link = %v", pickerOpts["deep_link"])
	}
}

func TestHandlePrecheckLocalizesFromAcceptLanguage(t *testing.T) {
	srv := New(Deps{Bus: eventbus.New(nil), Sessions: NewSessionRegistry(), Log: slog.Default()})
	req := httptest.NewRequest(http.MethodGet, "/api/precheck", nil)
	req.Header.Set("Accept-Language", "zh-HK,zh-Hant;q=0.9,en;q=0.1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out precheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.NewDiscussion.Form.Title != "新增頻道" {
		t.Fatalf("title = %q, want Traditional Chinese", out.NewDiscussion.Form.Title)
	}
	props := out.NewDiscussion.Form.Schema["properties"].(map[string]any)
	prompt := props["prompt"].(map[string]any)
	promptProps := prompt["properties"].(map[string]any)
	topic := promptProps["topic"].(map[string]any)
	if topic["title"] != "主題" {
		t.Fatalf("topic.title = %q, want Traditional Chinese", topic["title"])
	}
}

func TestHandlePrecheckOmitsIOSActionsForUnsupportedPlatform(t *testing.T) {
	srv := New(Deps{Bus: eventbus.New(nil), Sessions: NewSessionRegistry(), Log: slog.Default()})
	req := httptest.NewRequest(http.MethodGet, "/api/precheck", nil)
	req.Header.Set("X-Client-Platform", "web")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out precheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.NewDiscussion.Form.Actions) != 0 {
		t.Fatalf("actions length = %d, want 0", len(out.NewDiscussion.Form.Actions))
	}
}

func TestHandleTools(t *testing.T) {
	mcp := &config.MCPConfig{MCPServers: map[string]config.MCPServerConfig{
		"firecrawl": {URL: "https://example.com/mcp"},
	}}
	ts := newMetaServer(t, nil, mcp)

	resp, err := http.Get(ts.URL + "/api/tools")
	if err != nil {
		t.Fatalf("get tools: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out toolsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sawBuiltin, sawDataStore, sawMCP bool
	for _, tl := range out.Tools {
		switch tl.Name {
		case "take_note":
			sawBuiltin = true
		case "data_store":
			sawDataStore = true
		case "firecrawl":
			sawMCP = tl.Dynamic && tl.Source == "mcp"
		}
	}
	if !sawBuiltin {
		t.Error("built-in tool take_note missing")
	}
	if !sawDataStore {
		t.Error("discussion data_store tool missing")
	}
	if !sawMCP {
		t.Error("declared MCP server not listed as a dynamic tool")
	}
}
