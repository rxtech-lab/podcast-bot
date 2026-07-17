package e2e

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// FakeLLM is an in-process, OpenAI-compatible chat-completions server used only
// in E2E mode. It never reaches a real provider: it inspects each request and
// returns deterministic responses that satisfy the three real callers —
//
//  1. the conversational planner (write_plan → show_plan → short ack),
//  2. the generation agents (plain dialogue text, no tool calls), and
//  3. the post-generation summarizer (write_summary_chunk → finalize_summary).
//
// It also serves GET /v1/models so the model catalog (and the iOS speaker-model
// picker) has a fixed roster to choose from.
type FakeLLM struct {
	srv *http.Server
	ln  net.Listener
}

// fakeModels is the fixed roster returned by GET /v1/models and offered to the
// iOS speaker-model picker. The first is the default the planner assigns.
var fakeModels = []string{"gpt-4o-mini", "gpt-4o", "claude-sonnet-4-6"}

// StartFakeLLM binds a loopback listener on a random port and starts serving.
// The returned baseURL already includes the "/v1" suffix the openai-go client
// expects. Call stop to shut it down.
func StartFakeLLM() (baseURL string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("fake llm listen: %w", err)
	}
	f := &FakeLLM{ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", f.handleChat)
	mux.HandleFunc("/v1/models", f.handleModels)
	mux.HandleFunc("/v1/embeddings", f.handleEmbeddings)
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(ln) }()

	baseURL = "http://" + ln.Addr().String() + "/v1"
	stop = func() { _ = f.srv.Close() }
	return baseURL, stop, nil
}

func (f *FakeLLM) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for _, id := range fakeModels {
		out.Data = append(out.Data, model{ID: id, Object: "model", OwnedBy: "e2e"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleEmbeddings serves a deterministic OpenAI-compatible embeddings
// endpoint. Each input is embedded by hashing its lowercased tokens into a
// fixed number of vector positions and L2-normalizing, so cosine similarity
// between two texts tracks their token overlap — E2E semantic-search
// assertions can rely on a query matching the seeded transcript text ranking
// first, not just on non-empty results.
func (f *FakeLLM) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input      json.RawMessage `json:"input"`
		Model      string          `json:"model"`
		Dimensions int             `json:"dimensions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var inputs []string
	var single string
	if json.Unmarshal(req.Input, &inputs) != nil && json.Unmarshal(req.Input, &single) == nil {
		inputs = []string{single}
	}
	dim := req.Dimensions
	if dim <= 0 {
		dim = 1536
	}
	type embedding struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	out := struct {
		Object string           `json:"object"`
		Data   []embedding      `json:"data"`
		Model  string           `json:"model"`
		Usage  map[string]int64 `json:"usage"`
	}{Object: "list", Model: req.Model, Usage: map[string]int64{"prompt_tokens": 8, "total_tokens": 8}}
	for i, text := range inputs {
		out.Data = append(out.Data, embedding{Object: "embedding", Index: i, Embedding: fakeEmbedding(text, dim)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// fakeEmbedding spreads each token over a handful of hashed positions and
// normalizes, giving overlapping texts overlapping support in the vector.
func fakeEmbedding(text string, dim int) []float32 {
	vec := make([]float32, dim)
	for _, token := range strings.Fields(strings.ToLower(text)) {
		token = strings.Trim(token, ".,!?;:\"'()[]{}")
		if token == "" {
			continue
		}
		h := fnv64(token)
		for k := 0; k < 4; k++ {
			h = h*1099511628211 + 14695981039346656037
			vec[h%uint64(dim)] += 1
		}
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		vec[0] = 1
		return vec
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= scale
	}
	return vec
}

func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// --- request parsing ---

type fakeChatReq struct {
	Stream         bool            `json:"stream"`
	Tools          []fakeTool      `json:"tools"`
	Messages       []fakeMsg       `json:"messages"`
	ResponseFormat json.RawMessage `json:"response_format"`
}

type fakeTool struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type fakeMsg struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []fakeTool      `json:"tool_calls"`
}

func (m fakeMsg) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	// string content
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	// array of parts: [{"type":"text","text":"..."}, ...]
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
		return b.String()
	}
	return ""
}

func (r fakeChatReq) toolNames() map[string]bool {
	out := map[string]bool{}
	for _, t := range r.Tools {
		if t.Function.Name != "" {
			out[t.Function.Name] = true
		}
	}
	return out
}

// toolMsgsContain reports whether any tool-role message AFTER the last user turn
// contains sub. Scoping to the current turn matters: the conversational planner's
// progression (write_plan → show_plan → ack) is detected from the tool results it
// has produced so far, but those results persist in the history across turns. A
// whole-history scan would see a previous turn's "Plan shown to the user" and make
// every follow-up turn short-circuit to the plain-text ack instead of producing a
// fresh plan.
func (r fakeChatReq) toolMsgsContain(sub string) bool {
	start := 0
	for i, m := range r.Messages {
		if m.Role == "user" {
			start = i + 1
		}
	}
	for _, m := range r.Messages[start:] {
		if m.Role == "tool" && strings.Contains(m.text(), sub) {
			return true
		}
	}
	return false
}

// hasImagePart reports whether the message content is a parts array carrying
// at least one image_url entry (the OpenAI multimodal shape).
func (m fakeMsg) hasImagePart() bool {
	if len(m.Content) == 0 {
		return false
	}
	var parts []struct {
		Type     string `json:"type"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "image_url" || p.ImageURL.URL != "" {
			return true
		}
	}
	return false
}

// hasUserImagePart reports whether any user turn carries an image part.
func (r fakeChatReq) hasUserImagePart() bool {
	for _, m := range r.Messages {
		if m.Role == "user" && m.hasImagePart() {
			return true
		}
	}
	return false
}

func (r fakeChatReq) allText() string {
	var b strings.Builder
	for _, m := range r.Messages {
		b.WriteString(m.text())
		b.WriteString("\n")
	}
	return b.String()
}

// latestUserText returns the prompt that started the current agent turn. QA
// intent must be derived from this message rather than allText: the global QA
// system prompt itself describes highlights, quotes, and podcast display tools,
// so scanning the whole request makes every list request look like a highlight
// request.
func (r fakeChatReq) latestUserText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].text()
		}
	}
	return ""
}

func (r fakeChatReq) wantsJSONObject() bool {
	return len(r.ResponseFormat) > 0 && strings.Contains(string(r.ResponseFormat), "json")
}

var discussantCountRe = regexp.MustCompile(`(?i)number of discussants:\s*(\d+)`)
var useExactlyRe = regexp.MustCompile(`use exactly (\d+) discussants`)

// discussantCount derives how many discussants the planner expects, preferring an
// explicit "use exactly N discussants" rejection (so the fake self-corrects) and
// falling back to the "Number of discussants: N" hint in the prompt, then 3.
func (r fakeChatReq) discussantCount() int {
	for _, m := range r.Messages {
		if m.Role == "tool" {
			if mm := useExactlyRe.FindStringSubmatch(m.text()); mm != nil {
				if n, err := strconv.Atoi(mm[1]); err == nil && n >= 2 && n <= 6 {
					return n
				}
			}
		}
	}
	if mm := discussantCountRe.FindStringSubmatch(r.allText()); mm != nil {
		if n, err := strconv.Atoi(mm[1]); err == nil && n >= 2 && n <= 6 {
			return n
		}
	}
	return 3
}

func (f *FakeLLM) handleChat(w http.ResponseWriter, r *http.Request) {
	var req fakeChatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tools := req.toolNames()

	if !req.Stream {
		f.respondNonStreaming(w, req)
		return
	}

	sw := newSSEWriter(w)
	defer sw.done()

	switch {
	case tools["search_content"]:
		f.streamQAChat(sw, req)
	case tools["write_plan"]:
		f.streamConversationalPlanner(sw, req)
	case tools["create_plan"]:
		f.streamCreatePlan(sw, req)
	case tools["write_summary_chunk"]:
		f.streamSummarizer(sw, req)
	case strings.Contains(req.allText(), "Create a concise slide deck JSON"):
		sw.text(e2eSummaryDeckJSON)
		sw.finish("stop")
	default:
		// Generation agents (host/discussants/etc.) and any other text call.
		sw.text("This is a synthetic end-to-end test reply. The discussion continues.")
		sw.finish("stop")
	}
	sw.usage()
}

// Image-probe reply markers. UI tests assert on these strings, so treat them
// as a stable contract.
const (
	ImageProbeSuccess = "E2E: I can see the attached image."
	ImageProbeFailure = "E2E ERROR: no image attachment reached the model."
)

// streamConversationalPlanner drives the iOS PlanConversationView loop:
// write_plan (round 0) → show_plan (round 1) → short acknowledgement (round 2).
func (f *FakeLLM) streamConversationalPlanner(sw *sseWriter, req fakeChatReq) {
	// Image-grounding probe: when the prompt references "this image" the fake
	// verifies the multimodal pipeline instead of planning — success when a
	// user turn actually carries an image part, a loud error when the image was
	// dropped on the way to the model. The UI test asserts on the reply text.
	if strings.Contains(strings.ToLower(req.allText()), "this image") {
		if req.hasUserImagePart() {
			sw.text(ImageProbeSuccess + " Tell me how you'd like the story to go.")
		} else {
			sw.text(ImageProbeFailure)
		}
		sw.finish("stop")
		return
	}
	switch {
	case req.toolMsgsContain("Plan shown to the user"):
		// Plan already visible — end the turn with a short plain-text ack.
		sw.text("The plan is ready above. Tell me what you'd like to change.")
		sw.finish("stop")
	case req.toolMsgsContain("Plan saved internally"):
		// write_plan succeeded; now reveal it.
		sw.toolCall(0, "call_show", "show_plan", "{}")
		sw.finish("tool_calls")
	default:
		// First attempt (or retry after a "use exactly N" rejection).
		sw.toolCall(0, "call_write", "write_plan", makeDraftJSON(req.discussantCount()))
		sw.finish("tool_calls")
	}
}

// QAAnswerText is the deterministic Q&A chat reply; UI tests assert on it.
const QAAnswerText = "E2E: Based on the podcast content, this is a synthetic grounded answer."

// streamQAChat drives the Q&A / global-chat agent loop deterministically. List
// requests search podcast metadata then render one batch grid; highlight/quote
// requests search content then render one batch highlight card. Other prompts
// end with the stable grounded answer used by persistence coverage.
func (f *FakeLLM) streamQAChat(sw *sseWriter, req fakeChatReq) {
	tools := req.toolNames()
	lower := strings.ToLower(req.latestUserText())
	wantsHighlights := tools["show_highlight_lines"] &&
		(strings.Contains(lower, "highlight") || strings.Contains(lower, "quote"))
	wantsPodcastGrid := tools["display_podcasts"] && strings.Contains(lower, "show") &&
		strings.Contains(lower, "podcast") && !wantsHighlights
	if wantsPodcastGrid && !req.toolMsgsContain("[podcasts]") {
		args, _ := json.Marshal(map[string]any{"query": "E2E"})
		sw.toolCall(0, "call_qa_search_podcasts", "search_podcasts", string(args))
		sw.finish("tool_calls")
		return
	}
	if wantsPodcastGrid && !req.toolMsgsContain("Podcast grid shown") {
		args, _ := json.Marshal(map[string]any{
			"discussion_ids": []string{"test-ready", "test-ready-summary"},
		})
		sw.toolCall(0, "call_qa_display", "display_podcasts", string(args))
		sw.finish("tool_calls")
		return
	}
	searched := req.toolMsgsContain("[search results]") || req.toolMsgsContain("No matching content")
	if !searched {
		args, _ := json.Marshal(map[string]any{"query": "e2e test content"})
		sw.toolCall(0, "call_qa_search", "search_content", string(args))
		sw.finish("tool_calls")
		return
	}
	if wantsHighlights && !req.toolMsgsContain("Highlight lines shown") {
		args, _ := json.Marshal(map[string]any{
			"highlights": []map[string]any{
				{"discussion_id": "test-ready", "start_ms": 0, "end_ms": 5_000, "quote": "Welcome to this synthetic end-to-end test discussion."},
				{"discussion_id": "test-ready-summary", "start_ms": 5_000, "end_ms": 10_000, "quote": "From a technical angle, the system works as designed."},
			},
		})
		sw.toolCall(0, "call_qa_highlights", "show_highlight_lines", string(args))
		sw.finish("tool_calls")
		return
	}
	sw.text(QAAnswerText)
	sw.finish("stop")
}

// streamCreatePlan satisfies the non-conversational agent loop (Generate/Improve)
// by emitting the single terminal create_plan call.
func (f *FakeLLM) streamCreatePlan(sw *sseWriter, req fakeChatReq) {
	sw.toolCall(0, "call_create", "create_plan", makeDraftJSON(req.discussantCount()))
	sw.finish("tool_calls")
}

// streamSummarizer writes one summary chunk then finalizes on the next round.
func (f *FakeLLM) streamSummarizer(sw *sseWriter, req fakeChatReq) {
	if req.toolMsgsContain("saved part") {
		sw.toolCall(0, "call_final", "finalize_summary", "{}")
		sw.finish("tool_calls")
		return
	}
	args, _ := json.Marshal(map[string]any{
		"part_index": 0,
		"markdown":   e2eSummaryMarkdown,
	})
	sw.toolCall(0, "call_chunk", "write_summary_chunk", string(args))
	sw.finish("tool_calls")
}

func (f *FakeLLM) respondNonStreaming(w http.ResponseWriter, req fakeChatReq) {
	content := "This is a synthetic end-to-end test reply."
	if req.wantsJSONObject() {
		// Generic empty object — callers (e.g. viewer.WantsToAsk) tolerate missing
		// fields, which default to their zero values (so the viewer never asks).
		content = "{}"
	}
	resp := map[string]any{
		"id":      "e2e",
		"object":  "chat.completion",
		"created": 0,
		"model":   "e2e-fake-model",
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 8, "total_tokens": 16},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// e2eSummaryMarkdown is the deterministic summary document the fake summarizer
// returns. It satisfies the renderer's flowchart constraint so the iOS summary
// view displays a real diagram.
const e2eSummaryMarkdown = "# E2E Test Summary\n\n" +
	"## Overview\n\nThis is a synthetic summary generated during end-to-end testing.\n\n" +
	"```mermaid\nflowchart TD\n  A[\"Topic\"] --> B[\"Discussion\"]\n  B --> C[\"Conclusion\"]\n```\n\n" +
	"## Participants & Positions\n\n### Test Host\n\nModerated the synthetic discussion.\n\n" +
	"## Points of Agreement and Disagreement\n\nThis is test content.\n\n" +
	"## Conclusion\n\nEnd-to-end summary generation works.\n"

const e2eSummaryDeckJSON = `{"title":"E2E Test Summary","subtitle":"Synthetic slide deck","slides":[{"title":"Overview","bullets":["Synthetic podcast summary generated for testing","The discussion uses deterministic fake content","The deck export path can render PPTX bytes"]},{"title":"Participants","bullets":["Test Host moderates the conversation","Speakers provide predictable transcript lines","The output stays concise"]},{"title":"Takeaway","bullets":["Summary generation works end to end","Slide generation uses the stored summary","Exported artifacts can be cached"]}]}`

// makeDraftJSON builds the planner `draft` JSON (title, background, host{name},
// discussants[]{name, aspect}) with exactly n discussants.
func makeDraftJSON(n int) string {
	if n < 2 {
		n = 2
	}
	if n > 6 {
		n = 6
	}
	names := []string{"Alice", "Bob", "Carol", "Dave", "Erin", "Frank"}
	aspects := []string{"technical", "economic", "ethical", "historical", "cultural", "political"}
	discussants := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		discussants = append(discussants, map[string]string{
			"name":   names[i],
			"aspect": aspects[i],
		})
	}
	draft := map[string]any{
		"title":       "E2E Test Discussion",
		"background":  "This is a synthetic background paragraph for the end-to-end test discussion. It frames a neutral topic so generation can run end to end.",
		"host":        map[string]string{"name": "Test Host"},
		"discussants": discussants,
	}
	b, _ := json.Marshal(draft)
	return string(b)
}

// --- SSE chunk writer ---

type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	return &sseWriter{w: w, flusher: fl}
}

func (s *sseWriter) emit(chunk map[string]any) {
	b, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *sseWriter) base(choice map[string]any) map[string]any {
	return map[string]any{
		"id":      "e2e",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "e2e-fake-model",
		"choices": []map[string]any{choice},
	}
}

func (s *sseWriter) text(content string) {
	s.emit(s.base(map[string]any{
		"index":         0,
		"delta":         map[string]any{"role": "assistant", "content": content},
		"finish_reason": nil,
	}))
}

func (s *sseWriter) toolCall(index int, id, name, args string) {
	s.emit(s.base(map[string]any{
		"index": 0,
		"delta": map[string]any{
			"tool_calls": []map[string]any{{
				"index":    index,
				"id":       id,
				"type":     "function",
				"function": map[string]any{"name": name, "arguments": args},
			}},
		},
		"finish_reason": nil,
	}))
}

func (s *sseWriter) finish(reason string) {
	s.emit(s.base(map[string]any{
		"index":         0,
		"delta":         map[string]any{},
		"finish_reason": reason,
	}))
}

// usage emits the final usage-only chunk (the client requests IncludeUsage).
func (s *sseWriter) usage() {
	s.emit(map[string]any{
		"id":      "e2e",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "e2e-fake-model",
		"choices": []map[string]any{},
		"usage":   map[string]any{"prompt_tokens": 16, "completion_tokens": 16, "total_tokens": 32},
	})
}

func (s *sseWriter) done() {
	_, _ = fmt.Fprint(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
