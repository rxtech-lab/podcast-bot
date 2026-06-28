package e2e

import (
	"encoding/json"
	"fmt"
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

// --- request parsing ---

type fakeChatReq struct {
	Stream         bool              `json:"stream"`
	Tools          []fakeTool        `json:"tools"`
	Messages       []fakeMsg         `json:"messages"`
	ResponseFormat json.RawMessage   `json:"response_format"`
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

func (r fakeChatReq) allText() string {
	var b strings.Builder
	for _, m := range r.Messages {
		b.WriteString(m.text())
		b.WriteString("\n")
	}
	return b.String()
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
	case tools["write_plan"]:
		f.streamConversationalPlanner(sw, req)
	case tools["create_plan"]:
		f.streamCreatePlan(sw, req)
	case tools["write_summary_chunk"]:
		f.streamSummarizer(sw, req)
	default:
		// Generation agents (host/discussants/etc.) and any other text call.
		sw.text("This is a synthetic end-to-end test reply. The discussion continues.")
		sw.finish("stop")
	}
	sw.usage()
}

// streamConversationalPlanner drives the iOS PlanConversationView loop:
// write_plan (round 0) → show_plan (round 1) → short acknowledgement (round 2).
func (f *FakeLLM) streamConversationalPlanner(sw *sseWriter, req fakeChatReq) {
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
