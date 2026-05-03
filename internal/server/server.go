// Package server hosts the HTTP API for a debate run.
//
// One server is shared across a sequential queue of topics: the underlying
// event bus, audio livestream and HLS encoder are reused, and the active
// orchestrator (the one whose transcript and chat sink the API exposes) is
// tracked via SessionRegistry.
//
// Endpoints:
//   GET  /api/topics            — JSON list of every queued topic + status.
//   GET  /api/transcript        — JSON snapshot of the current topic transcript.
//   GET  /api/events            — Server-Sent Events stream of live events.
//   GET  /api/audio/stream      — chunked MP3 audio stream of the live debate.
//   GET  /api/video/...         — HLS playlist + segments.
//   POST /api/messages          — push a user message into the live orchestrator.
//   GET  /                      — embedded web UI (Twitch-like viewer).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// Deps wires the server to shared (cross-topic) state plus the registry that
// tracks which orchestrator is currently running.
type Deps struct {
	Bus        *eventbus.Bus
	LiveStream *audio.LiveStream
	Sessions   *SessionRegistry
	Log        *slog.Logger
	// VideoHLSDir is the directory holding stream.m3u8 + segments. When empty,
	// the /api/video/* routes return 404.
	VideoHLSDir string
}

// Server is the HTTP front-end.
type Server struct {
	d   Deps
	mux *http.ServeMux
}

// New builds a Server with all routes mounted.
func New(d Deps) *Server {
	s := &Server{d: d, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/topics", s.handleTopics)
	s.mux.HandleFunc("GET /api/transcript", s.handleTranscript)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/audio/stream", s.handleAudio)
	s.mux.HandleFunc("GET /api/video/", s.handleVideo)
	s.mux.HandleFunc("POST /api/messages", s.handleMessages)
	s.mux.Handle("/", staticHandler())
	return s
}

// Handler exposes the underlying mux (useful for tests / custom mounting).
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe binds to addr and serves until ctx is cancelled. addr like
// ":8080" or "127.0.0.1:0" (random port). The actual bound address is returned
// via the started callback so callers can discover a random port.
func (s *Server) ListenAndServe(ctx context.Context, addr string, started func(*net.TCPAddr)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	if started != nil {
		started(ln.Addr().(*net.TCPAddr))
	}
	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// transcriptDTO is the JSON-serialisable form of an agent.TranscriptLine.
type transcriptDTO struct {
	Speaker string    `json:"speaker"`
	Role    string    `json:"role"`
	Side    string    `json:"side"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
}

func toDTO(l agent.TranscriptLine) transcriptDTO {
	return transcriptDTO{
		Speaker: l.Speaker, Role: string(l.Role), Side: l.Side,
		Text: l.Text, At: l.At,
	}
}

func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.d.Sessions.List())
}

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cur := s.d.Sessions.Current()
	if cur == nil {
		_ = json.NewEncoder(w).Encode([]transcriptDTO{})
		return
	}
	lines := cur.Transcript.Snapshot()
	out := make([]transcriptDTO, len(lines))
	for i, l := range lines {
		out[i] = toDTO(l)
	}
	_ = json.NewEncoder(w).Encode(out)
}

// eventEnvelope is the JSON shape emitted to SSE clients. The bus carries
// concrete debate.* event structs; we tag each with a string event name so
// browsers (and the TUI bridge) can dispatch on it.
type eventEnvelope struct {
	tag     string
	payload any
}

func envelope(v any) (eventEnvelope, bool) {
	switch m := v.(type) {
	case debate.TranscriptMsg:
		return eventEnvelope{"transcript", map[string]any{
			"speaker": m.Speaker, "role": string(m.Role), "side": m.Side,
			"text": m.Text, "done": m.Done,
		}}, true
	case debate.TickMsg:
		return eventEnvelope{"tick", map[string]any{
			"elapsed_ms":   m.Elapsed.Milliseconds(),
			"remaining_ms": m.Remaining.Milliseconds(),
		}}, true
	case debate.PhaseMsg:
		return eventEnvelope{"phase", map[string]any{"phase": m.Phase.String()}}, true
	case debate.StatusMsg:
		return eventEnvelope{"status", map[string]any{"text": m.Text}}, true
	case debate.ErrorMsg:
		text := ""
		if m.Err != nil {
			text = m.Err.Error()
		}
		return eventEnvelope{"error", map[string]any{"text": text}}, true
	case debate.EndedMsg:
		return eventEnvelope{"ended", map[string]any{
			"transcript_path": m.TranscriptPath, "audio_path": m.AudioPath,
		}}, true
	case debate.TopicMsg:
		return eventEnvelope{"topic", map[string]any{
			"id":    m.ID,
			"title": m.Title,
			"index": m.Index,
			"total": m.Total,
		}}, true
	}
	return eventEnvelope{}, false
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sse := newSSEWriter(w)
	ch, cancel := s.d.Bus.Subscribe(128)
	defer cancel()

	// Initial heartbeat — confirms the connection to the client.
	if err := sse.comment("ok"); err != nil {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sse.comment("hb"); err != nil {
				return
			}
		case v, ok := <-ch:
			if !ok {
				return
			}
			env, fine := envelope(v)
			if !fine {
				continue
			}
			if err := sse.send(env.tag, env.payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)

	ch, cancel := s.d.LiveStream.Subscribe(128)
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// handleVideo serves the HLS playlist + segments produced by the encoder.
// It refuses any path that would escape the configured HLS directory and only
// serves files whose extensions are recognised HLS artefacts.
func (s *Server) handleVideo(w http.ResponseWriter, r *http.Request) {
	if s.d.VideoHLSDir == "" {
		http.Error(w, "video not enabled", http.StatusNotFound)
		return
	}
	const prefix = "/api/video/"
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	if rel == "" || strings.Contains(rel, "..") || strings.ContainsAny(rel, "\\/") && !isAllowedSegmentPath(rel) {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(rel, ".m3u8"):
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasSuffix(rel, ".ts"):
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "max-age=10")
	default:
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.d.VideoHLSDir, rel)
	// Final containment check after Join.
	clean := filepath.Clean(full)
	if !strings.HasPrefix(clean, filepath.Clean(s.d.VideoHLSDir)+string(filepath.Separator)) &&
		clean != filepath.Clean(s.d.VideoHLSDir) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// isAllowedSegmentPath rejects nested paths; HLS files live flat in HLSDir.
func isAllowedSegmentPath(rel string) bool {
	return !strings.ContainsAny(rel, `/\`)
}

type postMessageReq struct {
	Text string `json:"text"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req postMessageReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	cur := s.d.Sessions.Current()
	if cur == nil {
		http.Error(w, "no active topic", http.StatusServiceUnavailable)
		return
	}
	cur.PushUserMessage(req.Text)
	w.WriteHeader(http.StatusNoContent)
}
