package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

type discussionAPITestEnv struct {
	ts     *httptest.Server
	store  *DiscussionStore
	openai *mockOpenAIStreamServer
}

func newDiscussionAPITestEnv(t *testing.T) *discussionAPITestEnv {
	t.Helper()
	openai := newMockOpenAIStreamServer(t)
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	bus := eventbus.New(nil)
	srv := New(Deps{
		Bus:         bus,
		Sessions:    NewSessionRegistry(),
		Discussions: store,
		Env: &config.Env{
			OpenAIBaseURL:     openai.URL(),
			OpenAIKey:         "test-key",
			HostModel:         "test-model",
			ScenePlannerModel: "test-model",
		},
		Log: slog.Default(),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		bus.Close()
		store.Close()
		openai.Close()
	})
	return &discussionAPITestEnv{ts: ts, store: store, openai: openai}
}

func TestDiscussionHistoryAPIWorksForPendingAndCompletedPlan(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	created := apiCreateDiscussion(t, env.ts.URL, "History while pending")

	pending := apiGetDiscussion(t, env.ts.URL, created.ID, 20)
	if pending.ID != created.ID {
		t.Fatalf("pending id = %q, want %q", pending.ID, created.ID)
	}
	if pending.Status != DiscussionPlanning {
		t.Fatalf("pending status = %q, want %q", pending.Status, DiscussionPlanning)
	}
	if len(pending.EditTurns) != 0 {
		t.Fatalf("pending edit turns = %+v, want none", pending.EditTurns)
	}

	env.openai.Enqueue(mockOpenAIResponse{Title: "Completed History Plan"})
	done := apiStreamPlanToDone(t, env.ts.URL, created.ID, "History while pending")
	if done.ID != created.ID {
		t.Fatalf("done id = %q, want %q", done.ID, created.ID)
	}
	if done.Script == nil || done.Script.Title != "Completed History Plan" {
		t.Fatalf("done script = %+v, want completed plan", done.Script)
	}

	completed := apiGetDiscussion(t, env.ts.URL, created.ID, 20)
	if completed.Script == nil || completed.Script.Title != "Completed History Plan" {
		t.Fatalf("completed script = %+v, want completed plan", completed.Script)
	}
	if len(completed.EditTurns) == 0 {
		t.Fatalf("completed history has no edit turns: %+v", completed)
	}
	if completed.EditTurns[0].Role != "plan" || completed.EditTurns[0].Text != "Current plan" {
		t.Fatalf("first completed edit turn = %+v, want Current plan", completed.EditTurns[0])
	}
}

func TestDiscussionPlanStreamDuplicateRequestWaitsForActivePlan(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	created := apiCreateDiscussion(t, env.ts.URL, "Duplicate plan request")

	releasePlan := make(chan struct{})
	env.openai.Enqueue(mockOpenAIResponse{Title: "Single Plan", Release: releasePlan})

	firstResp, firstReader := apiOpenPlanStream(t, env.ts.URL, created.ID, "Duplicate plan request")
	defer firstResp.Body.Close()
	drainSSEComment(t, firstReader)
	ev, data, err := readSSEEvent(firstReader)
	if err != nil {
		t.Fatalf("read first progress: %v", err)
	}
	if ev != "progress" {
		t.Fatalf("first event = %q data=%s, want progress", ev, data)
	}

	secondResp, secondReader := apiOpenPlanStream(t, env.ts.URL, created.ID, "Duplicate plan request")
	defer secondResp.Body.Close()
	drainSSEComment(t, secondReader)
	ev, data, err = readSSEEvent(secondReader)
	if err != nil {
		t.Fatalf("read duplicate progress: %v", err)
	}
	if ev != "progress" {
		t.Fatalf("duplicate first event = %q data=%s, want progress", ev, data)
	}

	close(releasePlan)
	firstDone := readDiscussionStreamDone(t, firstReader, nil)
	secondDone := readDiscussionStreamDone(t, secondReader, nil)
	if firstDone.Script == nil || firstDone.Script.Title != "Single Plan" {
		t.Fatalf("first done script = %+v, want Single Plan", firstDone.Script)
	}
	if secondDone.Script == nil || secondDone.Script.Title != "Single Plan" {
		t.Fatalf("second done script = %+v, want Single Plan", secondDone.Script)
	}
	if calls := env.openai.Calls(); calls != 1 {
		t.Fatalf("OpenAI calls = %d, want 1", calls)
	}
}

func TestDiscussionCreateFromPlanAPICopiesVisiblePlan(t *testing.T) {
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	srv := New(Deps{Discussions: store})

	source, err := store.Create(context.Background(), "oauth:owner", "market plan", planResponse{
		Script: &config.DebateTopic{
			Title:    "Market Plan",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
		},
		Markdown:   "Plan markdown",
		Sources:    []config.Source{{Title: "Source", URL: "https://example.com/source"}},
		Researched: true,
	})
	if err != nil {
		t.Fatalf("Create source: %v", err)
	}
	if _, err := store.SetVisibility(context.Background(), "oauth:owner", source.ID, DiscussionPublic, DiscussionCover{
		Type:          "gradient",
		GradientStart: "#111111",
		GradientEnd:   "#777777",
	}); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/discussions/"+source.ID+"/create/plan", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: usernameCookie, Value: "MarketViewer"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clone status = %d body=%s", rec.Code, rec.Body.String())
	}
	var clone Discussion
	if err := json.NewDecoder(rec.Body).Decode(&clone); err != nil {
		t.Fatalf("decode clone: %v", err)
	}
	if clone.ID == "" || clone.ID == source.ID {
		t.Fatalf("clone identity = %+v", clone)
	}
	persisted, err := store.Get(context.Background(), "cookie:MarketViewer", clone.ID)
	if err != nil {
		t.Fatalf("Get clone: %v", err)
	}
	if persisted == nil || persisted.OwnerUserID != "cookie:MarketViewer" {
		t.Fatalf("persisted clone = %+v", persisted)
	}
	if clone.Script == nil || clone.Script.Title != "Market Plan" || clone.Markdown != "Plan markdown" {
		t.Fatalf("clone plan = %+v", clone)
	}
	if clone.Visibility != DiscussionPrivate || clone.Status != DiscussionPlanning {
		t.Fatalf("clone visibility/status = %+v", clone)
	}
}

func TestDiscussionParentPodcastEndpointsRequireReady(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	srv := New(Deps{Discussions: store})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	owner := "anonymous"
	pending, err := store.Create(ctx, owner, "Pending parent", planResponse{
		Script: &config.DebateTopic{
			Title:    "Pending Parent",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
		},
		Markdown: "not ready yet",
	})
	if err != nil {
		t.Fatalf("Create pending: %v", err)
	}
	ready, err := store.Create(ctx, owner, "Ready parent", planResponse{
		Script: &config.DebateTopic{
			Title:    "Ready Parent",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
		},
		Markdown: "ready now",
	})
	if err != nil {
		t.Fatalf("Create ready: %v", err)
	}
	if err := store.SetJobResult(ctx, ready.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/discussions/parent-podcasts")
	if err != nil {
		t.Fatalf("list parent podcasts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("list status = %d body=%s", resp.StatusCode, raw)
	}
	var items []Discussion
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode parent podcasts: %v", err)
	}
	if len(items) != 1 || items[0].ID != ready.ID {
		t.Fatalf("parent podcasts = %+v, want only ready parent", items)
	}

	resp, err = http.Get(ts.URL + "/api/discussions/" + pending.ID + "/parent-podcast")
	if err != nil {
		t.Fatalf("get pending parent podcast: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pending parent status = %d body=%s, want 409", resp.StatusCode, raw)
	}

	resp, err = http.Get(ts.URL + "/api/discussions/" + ready.ID + "/parent-podcast")
	if err != nil {
		t.Fatalf("get ready parent podcast: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("ready parent status = %d body=%s", resp.StatusCode, raw)
	}
	var ref struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Topic string `json:"topic"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatalf("decode ready parent: %v", err)
	}
	if ref.ID != ready.ID || ref.Title != "Ready Parent" {
		t.Fatalf("ready parent reference = %+v", ref)
	}

	body := strings.NewReader(fmt.Sprintf(
		`{"form":{"prompt":{"topic":"Follow up"},"settings":{"language":"en-US"}},"reference_discussion_id":%q}`,
		pending.ID,
	))
	resp, err = http.Post(ts.URL+"/api/discussions", "application/json", body)
	if err != nil {
		t.Fatalf("create with pending reference: %v", err)
	}
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("create with pending reference status = %d body=%s, want 409", resp.StatusCode, raw)
	}
}

func TestDiscussionImproveStreamPersistsUserTurnBeforePlanFinishes(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	env.openai.Enqueue(mockOpenAIResponse{Title: "Original Plan"})
	created := apiCreateDiscussion(t, env.ts.URL, "Pending improvement")
	_ = apiStreamPlanToDone(t, env.ts.URL, created.ID, "Pending improvement")

	releaseImprove := make(chan struct{})
	env.openai.Enqueue(mockOpenAIResponse{Title: "Improved Plan", Release: releaseImprove})

	body := strings.NewReader(`{"instruction":"Make the plan sharper"}`)
	req, err := http.NewRequest(
		http.MethodPost,
		env.ts.URL+"/api/discussions/"+created.ID+"/improve/stream",
		body,
	)
	if err != nil {
		t.Fatalf("new improve request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("start improve stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("improve stream status = %d body=%s", resp.StatusCode, raw)
	}
	br := bufio.NewReader(resp.Body)
	drainSSEComment(t, br)

	ev, data, err := readSSEEvent(br)
	if err != nil {
		t.Fatalf("read first improve event: %v", err)
	}
	if ev != "progress" {
		t.Fatalf("first improve event = %q data=%s, want progress", ev, data)
	}
	var progress struct {
		Phase string `json:"phase"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(data, &progress); err != nil {
		t.Fatalf("decode progress %s: %v", data, err)
	}
	if progress.Text == "" {
		t.Fatalf("progress text is empty: %+v", progress)
	}

	pending := apiGetDiscussion(t, env.ts.URL, created.ID, 20)
	if !hasEditTurn(pending.EditTurns, "user", "Make the plan sharper") {
		t.Fatalf("pending history missing user turn: %+v", pending.EditTurns)
	}
	if hasEditTurn(pending.EditTurns, "plan", "Updated plan") {
		t.Fatalf("pending history already has updated plan turn: %+v", pending.EditTurns)
	}

	close(releaseImprove)
	events := []string{progress.Text}
	done := readDiscussionStreamDone(t, br, &events)
	if done.Script == nil || done.Script.Title != "Improved Plan" {
		t.Fatalf("done script = %+v, want Improved Plan", done.Script)
	}
	if !containsText(events, "Writing the plan") {
		t.Fatalf("stream progress events = %+v, want writing status", events)
	}

	completed := apiGetDiscussion(t, env.ts.URL, created.ID, 20)
	if !hasEditTurn(completed.EditTurns, "user", "Make the plan sharper") {
		t.Fatalf("completed history missing user turn: %+v", completed.EditTurns)
	}
	if !hasEditTurn(completed.EditTurns, "plan", "Updated plan") {
		t.Fatalf("completed history missing updated plan turn: %+v", completed.EditTurns)
	}
}

func apiCreateDiscussion(t *testing.T, baseURL, topic string) Discussion {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"form":{"prompt":{"topic":%q},"settings":{"language":"en-US"}}}`, topic))
	resp, err := http.Post(baseURL+"/api/discussions", "application/json", body)
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status = %d body=%s", resp.StatusCode, raw)
	}
	var d Discussion
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode create discussion: %v", err)
	}
	if d.ID == "" {
		t.Fatal("create discussion returned empty id")
	}
	return d
}

func apiGetDiscussion(t *testing.T, baseURL, id string, editLimit int) Discussion {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/discussions/%s?edit_limit=%d", baseURL, id, editLimit))
	if err != nil {
		t.Fatalf("get discussion: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("get discussion status = %d body=%s", resp.StatusCode, raw)
	}
	var d Discussion
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode discussion: %v", err)
	}
	return d
}

func apiStreamPlanToDone(t *testing.T, baseURL, id, topic string) Discussion {
	t.Helper()
	resp, br := apiOpenPlanStream(t, baseURL, id, topic)
	defer resp.Body.Close()
	drainSSEComment(t, br)
	return readDiscussionStreamDone(t, br, nil)
}

func apiOpenPlanStream(t *testing.T, baseURL, id, topic string) (*http.Response, *bufio.Reader) {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(
		`{"type":"discussion","topic":%q,"language":"en-US","discussants":2,"research":false}`,
		topic,
	))
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/discussions/"+id+"/plan/stream", body)
	if err != nil {
		t.Fatalf("new plan stream request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("plan stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("plan stream status = %d body=%s", resp.StatusCode, raw)
	}
	return resp, bufio.NewReader(resp.Body)
}

func readDiscussionStreamDone(t *testing.T, br *bufio.Reader, progressTexts *[]string) Discussion {
	t.Helper()
	for {
		ev, data, err := readSSEEvent(br)
		if err != nil {
			t.Fatalf("read discussion stream event: %v", err)
		}
		switch ev {
		case "progress":
			if progressTexts == nil {
				continue
			}
			var progress struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(data, &progress); err != nil {
				t.Fatalf("decode progress %s: %v", data, err)
			}
			*progressTexts = append(*progressTexts, progress.Text)
		case "done":
			var d Discussion
			if err := json.Unmarshal(data, &d); err != nil {
				t.Fatalf("decode done discussion %s: %v", data, err)
			}
			return d
		case "error":
			t.Fatalf("stream error event: %s", data)
		default:
			t.Fatalf("unexpected stream event %q data=%s", ev, data)
		}
	}
}

func drainSSEComment(t *testing.T, br *bufio.Reader) {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read initial sse comment: %v", err)
	}
	if !strings.HasPrefix(line, ":") {
		t.Fatalf("initial sse line = %q, want comment", line)
	}
	line, err = br.ReadString('\n')
	if err != nil {
		t.Fatalf("read blank after initial comment: %v", err)
	}
	if strings.TrimSpace(line) != "" {
		t.Fatalf("line after initial comment = %q, want blank", line)
	}
}

func hasEditTurn(turns []DiscussionEditTurn, role, text string) bool {
	for _, turn := range turns {
		if turn.Role == role && turn.Text == text {
			return true
		}
	}
	return false
}

func containsText(items []string, want string) bool {
	for _, item := range items {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}

type mockOpenAIResponse struct {
	Title   string
	Release <-chan struct{}
}

type mockOpenAIStreamServer struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	queue  []mockOpenAIResponse
	calls  int
}

func newMockOpenAIStreamServer(t *testing.T) *mockOpenAIStreamServer {
	t.Helper()
	m := &mockOpenAIStreamServer{t: t}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockOpenAIStreamServer) URL() string {
	return m.server.URL
}

func (m *mockOpenAIStreamServer) Close() {
	m.server.Close()
}

func (m *mockOpenAIStreamServer) Enqueue(resp mockOpenAIResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, resp)
}

func (m *mockOpenAIStreamServer) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *mockOpenAIStreamServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	resp, ok := m.pop()
	if !ok {
		http.Error(w, "no queued mock OpenAI response", http.StatusInternalServerError)
		return
	}
	if resp.Release != nil {
		select {
		case <-resp.Release:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	planArgs := mockPlanArgs(resp.Title)
	chunk := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "test-model",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    "call-create-plan",
					"type":  "function",
					"function": map[string]any{
						"name":      "create_plan",
						"arguments": planArgs,
					},
				}},
			},
		}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(chunk); err != nil {
		m.t.Errorf("encode mock OpenAI chunk: %v", err)
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", strings.TrimSpace(buf.String()))
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *mockOpenAIStreamServer) pop() (mockOpenAIResponse, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return mockOpenAIResponse{}, false
	}
	m.calls++
	resp := m.queue[0]
	m.queue = m.queue[1:]
	return resp, true
}

func mockPlanArgs(title string) string {
	if title == "" {
		title = "Mock Plan"
	}
	raw, _ := json.Marshal(map[string]any{
		"title":      title,
		"background": "A concise mocked background for backend API testing.",
		"host":       map[string]string{"name": "Host Morgan"},
		"discussants": []map[string]string{
			{"name": "Alex", "aspect": "technical"},
			{"name": "Blair", "aspect": "economic"},
		},
	})
	return string(raw)
}
