package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// testEnv bundles every handle a server test might need. Returned by
// newTestServer so call sites can pull only what they need.
type testEnv struct {
	ts       *httptest.Server
	bus      *eventbus.Bus
	orch     *contentcreator.Orchestrator
	sessions *SessionRegistry
	dbPath   string
}

func TestTranscriptEnvelopeIncludesUserMessageMetadata(t *testing.T) {
	env, ok := envelope(contentcreator.TranscriptMsg{
		ChannelID:     "job-a",
		Speaker:       "Alice",
		Role:          "user",
		Text:          "voice note",
		Done:          true,
		IsUserMessage: true,
		SenderUserID:  "oauth:user-1",
		AudioURL:      "https://media.example/voice.m4a",
	}, contentcreator.LangEN)
	if !ok {
		t.Fatal("envelope returned ok=false")
	}
	if env.tag != "transcript" {
		t.Fatalf("event tag = %q, want transcript", env.tag)
	}
	payload, ok := env.payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", env.payload)
	}
	if got := payload["isUserMessage"]; got != true {
		t.Fatalf("isUserMessage = %#v, want true", got)
	}
	if got := payload["sender_user_id"]; got != "oauth:user-1" {
		t.Fatalf("sender_user_id = %#v, want oauth:user-1", got)
	}
	if got := payload["audio_url"]; got != "https://media.example/voice.m4a" {
		t.Fatalf("audio_url = %#v, want signed audio url", got)
	}
}

// newTestServer wires up a real Bus + SessionRegistry + Server, and registers
// a single non-off-air channel "tech" backed by a NewForTest orchestrator
// whose Send is wrapped to publish channel-stamped events on the bus
// (mirroring runtime.channelSend in cmd/debate-bot/main.go). Each call gets
// its own per-debate sqlite file so the persistence flow is exercised end
// to end.
func newTestServer(t *testing.T) *testEnv {
	t.Helper()
	bus := eventbus.New(nil)
	sessions := NewSessionRegistry()

	const channelID = "tech"
	send := func(v any) {
		bus.Publish(contentcreator.StampChannelID(v, channelID))
	}

	dbPath := filepath.Join(t.TempDir(), "session.db")
	store, err := contentcreator.OpenStore(dbPath, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	orch := contentcreator.NewForTest(send, store)

	// hlsDir non-empty so the channel is *not* off-air; LiveStream stays nil
	// because /api/audio is not exercised here.
	sessions.RegisterChannel(channelID, 1, "Tech Channel", t.TempDir(), nil)
	sessions.SeedChannelDebates(channelID, []Session{
		{ID: "demo", Title: "demo debate", Status: StatusRunning, DBPath: dbPath},
	})
	sessions.SetCurrentOrch(channelID, "demo", orch)

	srv := New(Deps{Bus: bus, Sessions: sessions, Log: slog.Default()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		bus.Close()
	})
	return &testEnv{ts: ts, bus: bus, orch: orch, sessions: sessions, dbPath: dbPath}
}

// readSSEEvent reads exactly one `event: <name>\ndata: <json>\n\n` block from
// the SSE stream. Returns event name, raw JSON data, or an error.
func readSSEEvent(br *bufio.Reader) (string, []byte, error) {
	var ev string
	var data bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Empty line terminates the event — return what we have if it's
			// non-empty, otherwise keep reading (e.g. heartbeat comments).
			if ev != "" || data.Len() > 0 {
				return ev, data.Bytes(), nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment / heartbeat — skip.
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			ev = rest
		} else if rest, ok := strings.CutPrefix(line, "data: "); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(rest)
		}
	}
}

// TestPostMessageDeliveredViaSSE is the regression for the user-reported bug:
// posting to /api/messages?channel=tech must surface as a transcript event on
// /api/events?channel=tech. If this fails, the message stream is broken.
func TestPostMessageDeliveredViaSSE(t *testing.T) {
	env := newTestServer(t)
	ts := env.ts

	// Subscribe to SSE first so we don't race the publish.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?channel=tech", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status: %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)

	// The very first thing the server writes is `: ok` (heartbeat comment).
	// Drain it so the next read lands on a real event.
	if line, err := br.ReadString('\n'); err != nil || !strings.HasPrefix(line, ":") {
		t.Fatalf("expected initial heartbeat, got %q err=%v", line, err)
	}
	// Trailing blank line after the heartbeat.
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read blank after heartbeat: %v", err)
	}

	// Drive the POST in a goroutine so the SSE reader (this goroutine) is
	// already blocked on the read when the publish fans out.
	postDone := make(chan error, 1)
	go func() {
		body := strings.NewReader(`{"text":"hello channel"}`)
		preq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/messages?channel=tech", body)
		preq.Header.Set("Content-Type", "application/json")
		presp, err := http.DefaultClient.Do(preq)
		if err != nil {
			postDone <- err
			return
		}
		defer presp.Body.Close()
		if presp.StatusCode != http.StatusNoContent {
			b, _ := io.ReadAll(presp.Body)
			postDone <- &postErr{status: presp.StatusCode, body: string(b)}
			return
		}
		postDone <- nil
	}()

	ev, data, err := readSSEEvent(br)
	if err != nil {
		t.Fatalf("read sse event: %v", err)
	}
	if ev != "transcript" {
		t.Fatalf("expected transcript event, got %q (data=%s)", ev, data)
	}
	var got struct {
		ChannelID string `json:"channel_id"`
		Speaker   string `json:"speaker"`
		Role      string `json:"role"`
		Text      string `json:"text"`
		Done      bool   `json:"done"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal data %s: %v", data, err)
	}
	if got.ChannelID != "tech" {
		t.Errorf("channel_id = %q, want tech", got.ChannelID)
	}
	if got.Role != "user" {
		t.Errorf("role = %q, want user", got.Role)
	}
	if got.Text != "hello channel" {
		t.Errorf("text = %q, want hello channel", got.Text)
	}
	if !got.Done {
		t.Errorf("done = false, want true")
	}
	if got.Speaker == "" {
		t.Errorf("speaker is empty — ensureUsername should have minted one")
	}

	if err := <-postDone; err != nil {
		t.Fatalf("post: %v", err)
	}
}

// TestPostMessageStoredInTranscript verifies the historical-message API
// (/api/transcript?channel=X) returns the user's posted message after a
// page reload.
func TestPostMessageStoredInTranscript(t *testing.T) {
	env := newTestServer(t)
	ts := env.ts

	body := strings.NewReader(`{"text":"hello world"}`)
	preq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/messages?channel=tech", body)
	preq.Header.Set("Content-Type", "application/json")
	presp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	io.Copy(io.Discard, presp.Body)
	presp.Body.Close()
	if presp.StatusCode != http.StatusNoContent {
		t.Fatalf("post status: %d", presp.StatusCode)
	}

	tresp, err := http.Get(ts.URL + "/api/transcript?channel=tech")
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	defer tresp.Body.Close()
	if tresp.StatusCode != http.StatusOK {
		t.Fatalf("transcript status: %d", tresp.StatusCode)
	}
	var lines []struct {
		Speaker string `json:"speaker"`
		Role    string `json:"role"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(tresp.Body).Decode(&lines); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("transcript len = %d, want 1 (lines=%+v)", len(lines), lines)
	}
	if lines[0].Role != "user" || lines[0].Text != "hello world" {
		t.Errorf("transcript line = %+v, want role=user text=hello world", lines[0])
	}
}

// TestPostMessageWithoutChannelReturns503 — POST with no channel param can't
// resolve an orchestrator. Today's behavior is 503; the test pins it so we
// don't accidentally start swallowing messages with no error.
func TestPostMessageWithoutChannelReturns503(t *testing.T) {
	env := newTestServer(t)
	ts := env.ts

	body := strings.NewReader(`{"text":"orphan"}`)
	preq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/messages", body)
	preq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

// TestPostMessageUsesCookieUsername — when the request carries a
// `debate-bot-username` cookie, the live transcript event must use that name
// as the speaker (instead of minting a new random one).
func TestPostMessageUsesCookieUsername(t *testing.T) {
	env := newTestServer(t)
	ts := env.ts

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?channel=tech", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	_, _ = br.ReadString('\n')
	_, _ = br.ReadString('\n')

	postDone := make(chan error, 1)
	go func() {
		body := strings.NewReader(`{"text":"named hello"}`)
		preq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/messages?channel=tech", body)
		preq.Header.Set("Content-Type", "application/json")
		preq.AddCookie(&http.Cookie{Name: "debate-bot-username", Value: "alice"})
		presp, err := http.DefaultClient.Do(preq)
		if err != nil {
			postDone <- err
			return
		}
		presp.Body.Close()
		postDone <- nil
	}()

	ev, data, err := readSSEEvent(br)
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}
	if ev != "transcript" {
		t.Fatalf("expected transcript, got %q", ev)
	}
	var got struct {
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Speaker != "alice" {
		t.Errorf("speaker = %q, want alice", got.Speaker)
	}
	if got.Text != "named hello" {
		t.Errorf("text = %q", got.Text)
	}
	if err := <-postDone; err != nil {
		t.Fatalf("post: %v", err)
	}
}

// TestTranscriptHistoryEmptyChannel — GET /api/transcript with no/unknown
// channel should return [] not 404 so the frontend renders gracefully when
// tuning to an off-air channel.
func TestTranscriptHistoryEmptyChannel(t *testing.T) {
	env := newTestServer(t)
	ts := env.ts

	for _, q := range []string{"", "?channel=nonexistent"} {
		resp, err := http.Get(ts.URL + "/api/transcript" + q)
		if err != nil {
			t.Fatalf("get %s: %v", q, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status for %q = %d, want 200", q, resp.StatusCode)
		}
		var lines []any
		if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil {
			t.Errorf("decode for %q: %v", q, err)
		}
		resp.Body.Close()
		if len(lines) != 0 {
			t.Errorf("lines for %q = %v, want []", q, lines)
		}
	}
}

// TestMultipleMessagesPreserveOrder — two messages posted in order must arrive
// over SSE in the same order, and both must end up in the transcript snapshot.
func TestMultipleMessagesPreserveOrder(t *testing.T) {
	env := newTestServer(t)
	ts := env.ts

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?channel=tech", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	_, _ = br.ReadString('\n')
	_, _ = br.ReadString('\n')

	postOne := func(text string) {
		body := strings.NewReader(`{"text":"` + text + `"}`)
		preq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/messages?channel=tech", body)
		preq.Header.Set("Content-Type", "application/json")
		presp, err := http.DefaultClient.Do(preq)
		if err != nil {
			t.Fatalf("post %q: %v", text, err)
		}
		presp.Body.Close()
	}

	go func() {
		postOne("first")
		postOne("second")
	}()

	for _, want := range []string{"first", "second"} {
		ev, data, err := readSSEEvent(br)
		if err != nil {
			t.Fatalf("read sse for %q: %v", want, err)
		}
		if ev != "transcript" {
			t.Fatalf("event = %q, want transcript", ev)
		}
		var got struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Text != want {
			t.Errorf("text = %q, want %q", got.Text, want)
		}
	}

	tresp, err := http.Get(ts.URL + "/api/transcript?channel=tech")
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	defer tresp.Body.Close()
	var lines []struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(tresp.Body).Decode(&lines); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if len(lines) != 2 || lines[0].Text != "first" || lines[1].Text != "second" {
		t.Errorf("transcript = %+v, want [first, second]", lines)
	}
}

// TestUserAndHostMessagesPersistAcrossReload — the user's request: post a
// user message to a channel, have the host (an AI agent) emit a turn into the
// same transcript, then "reload" via GET /api/transcript and confirm both
// lines come back in order. Also exercises the disk-fallback path by
// clearing the live orchestrator before the second read so the server has to
// load from sqlite rather than the in-memory snapshot.
func TestUserAndHostMessagesPersistAcrossReload(t *testing.T) {
	env := newTestServer(t)

	// 1. User posts a message via the public API. This exercises the same
	//    cookie-driven username flow the browser uses.
	body := strings.NewReader(`{"text":"why does this matter?"}`)
	preq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/messages?channel=tech", body)
	preq.Header.Set("Content-Type", "application/json")
	preq.AddCookie(&http.Cookie{Name: "debate-bot-username", Value: "viewer42"})
	presp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusNoContent {
		t.Fatalf("post status: %d", presp.StatusCode)
	}

	// 2. Host (AI agent) writes its own turn directly into the transcript —
	//    same path the pipeline uses when a turn finishes streaming. The
	//    NewForTest orchestrator shares a Transcript with the store, so this
	//    line lands in sqlite too.
	hostLine := agent.TranscriptLine{
		Speaker: "Host",
		Role:    "host",
		Text:    "great question — let's hear from the affirmative.",
		At:      time.Now(),
	}
	env.orch.Transcript.AppendLine(hostLine)

	// 3. First reload — orchestrator still live, served from in-memory snapshot.
	got := fetchTranscript(t, env.ts.URL+"/api/transcript?channel=tech")
	if len(got) != 2 {
		t.Fatalf("live snapshot len = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Role != "user" || got[0].Speaker != "viewer42" || got[0].Text != "why does this matter?" {
		t.Errorf("live[0] = %+v, want user/viewer42 line", got[0])
	}
	if got[1].Role != "host" || got[1].Speaker != "Host" || !strings.Contains(got[1].Text, "great question") {
		t.Errorf("live[1] = %+v, want host line", got[1])
	}

	// 4. Simulate the debate ending: clear the live orchestrator. The DB
	//    file stays put (its handle is still open via the test cleanup),
	//    so /api/transcript should now read from disk.
	env.sessions.SetCurrentOrch("tech", "", nil)

	// Sanity: the DBPath is still latched so the disk-fallback path has a
	// file to open.
	if res := env.sessions.ChannelResources("tech"); res == nil || res.CurrentDBPath != env.dbPath {
		t.Fatalf("CurrentDBPath after orch cleared = %v, want %s", res, env.dbPath)
	}

	// 5. Second reload — no live orch, must come from sqlite.
	got = fetchTranscript(t, env.ts.URL+"/api/transcript?channel=tech")
	if len(got) != 2 {
		t.Fatalf("disk snapshot len = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Role != "user" || got[0].Speaker != "viewer42" || got[0].Text != "why does this matter?" {
		t.Errorf("disk[0] = %+v, want user/viewer42 line from disk", got[0])
	}
	if got[1].Role != "host" || got[1].Speaker != "Host" || !strings.Contains(got[1].Text, "great question") {
		t.Errorf("disk[1] = %+v, want host line from disk", got[1])
	}
}

// transcriptLine is the JSON shape /api/transcript returns. Local to keep the
// dependency direction one-way (test → server), avoiding a cyclic import on
// the unexported transcriptDTO.
type transcriptLine struct {
	Speaker string `json:"speaker"`
	Role    string `json:"role"`
	Side    string `json:"side"`
	Text    string `json:"text"`
}

func fetchTranscript(t *testing.T, url string) []transcriptLine {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status for %s = %d", url, resp.StatusCode)
	}
	var out []transcriptLine
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
}

// TestSSEFiltersOtherChannels — events stamped with channel A must not reach
// a subscriber filtering by channel B.
func TestSSEFiltersOtherChannels(t *testing.T) {
	env := newTestServer(t)
	ts, bus := env.ts, env.bus

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?channel=other", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	// Discard the initial heartbeat block.
	_, _ = br.ReadString('\n')
	_, _ = br.ReadString('\n')

	// Publish an event tagged for "tech" — the "other"-filtered subscriber
	// should NOT receive it. We expect the read to time out.
	go func() {
		// Give the subscriber a tick to be ready.
		time.Sleep(50 * time.Millisecond)
		bus.Publish(contentcreator.StampChannelID(
			contentcreator.TranscriptMsg{Speaker: "ghost", Role: "user", Text: "wrong-channel", Done: true},
			"tech"))
	}()

	type result struct {
		ev   string
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		ev, data, err := readSSEEvent(br)
		done <- result{ev: ev, data: data, err: err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			// Context timeout closing the body is the expected outcome.
			return
		}
		t.Fatalf("unexpected event leaked across channel filter: %s %s", r.ev, r.data)
	case <-time.After(500 * time.Millisecond):
		// Good: nothing leaked. Cancel the context to clean up.
		cancel()
	}
}

// postErr is a tiny error type so postDone can carry status + body without an
// extra channel.
type postErr struct {
	status int
	body   string
}

func (e *postErr) Error() string {
	return "post returned " + http.StatusText(e.status) + ": " + e.body
}
