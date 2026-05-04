// Package server hosts the HTTP API for a debate run.
//
// The server always operates in TV-channel mode: one channels.json defines
// the available channels, debate.md files declare which channel they belong
// to, and each channel runs its own queue of debates sequentially while all
// channels run in parallel. Each channel has its own LiveStream + Encoder +
// HLS dir; channels with no assigned debates are listed as off-air.
//
// Endpoints:
//   GET  /api/topics                        — channel list (number, title, off-air, debates queue).
//   GET  /api/transcript?channel=<id>       — JSON snapshot of that channel's live transcript.
//   GET  /api/events[?channel=<id>]         — Server-Sent Events; channel filter is optional.
//   GET  /api/audio/<id>/stream             — chunked MP3 audio for that channel.
//   GET  /api/video/<id>/<file>             — HLS playlist + segments for that channel.
//   POST /api/messages?channel=<id>         — push a user message into that channel's orchestrator
//                                             (uses the viewer's `debate-bot-username` cookie).
//   GET  /api/me                            — return the viewer's username; issues + sets a cookie
//                                             on first request.
//   POST /api/me                            — change the viewer's username (body: {username}).
//   GET  /                                  — embedded web UI.
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

// Deps wires the server to the event bus and the registry that tracks every
// channel + its current orchestrator. Per-channel streaming resources
// (LiveStream, HLS dir) are reached through Sessions.ChannelResources(id).
type Deps struct {
	Bus      *eventbus.Bus
	Sessions *SessionRegistry
	Log      *slog.Logger
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
	s.mux.HandleFunc("GET /api/audio/", s.handleAudio)
	s.mux.HandleFunc("GET /api/video/", s.handleVideo)
	s.mux.HandleFunc("POST /api/messages", s.handleMessages)
	s.mux.HandleFunc("GET /api/me", s.handleGetMe)
	s.mux.HandleFunc("POST /api/me", s.handlePostMe)
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

// topicsResponse is the body of GET /api/topics — every channel with its
// current debate queue. The frontend renders the channel switcher from this.
type topicsResponse struct {
	Channels []ChannelInfo `json:"channels"`
}

func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(topicsResponse{
		Channels: s.d.Sessions.List(),
	})
}

// orchForRequest returns the orchestrator the request targets via
// ?channel=<id>. Returns nil when the channel is unknown or has no live
// orchestrator (off-air, between debates).
func (s *Server) orchForRequest(r *http.Request) *debate.Orchestrator {
	id := r.URL.Query().Get("channel")
	if id == "" {
		return nil
	}
	if res := s.d.Sessions.ChannelResources(id); res != nil {
		return res.Orch
	}
	return nil
}

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Live orchestrator: serve the in-memory snapshot — zero IO and the
	// freshest possible view of an in-progress debate.
	if cur := s.orchForRequest(r); cur != nil {
		writeTranscript(w, cur.Transcript.Snapshot())
		return
	}

	// No live orch: fall back to the channel's most-recently-aired debate's
	// sqlite file so a viewer who reloads after the debate ends still sees
	// the chat history.
	if path := s.dbPathForRequest(r); path != "" {
		lines, err := debate.LoadSnapshot(path)
		if err != nil {
			s.d.Log.Warn("transcript disk load failed", "path", path, "err", err)
			writeTranscript(w, nil)
			return
		}
		writeTranscript(w, lines)
		return
	}

	writeTranscript(w, nil)
}

// dbPathForRequest returns the sqlite path the request targets, derived from
// the channel-id query parameter. Empty string when no channel is requested
// or no debate has aired on that channel yet.
func (s *Server) dbPathForRequest(r *http.Request) string {
	id := r.URL.Query().Get("channel")
	if id == "" {
		return ""
	}
	if res := s.d.Sessions.ChannelResources(id); res != nil {
		return res.CurrentDBPath
	}
	return ""
}

func writeTranscript(w http.ResponseWriter, lines []agent.TranscriptLine) {
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
			"channel_id": m.ChannelID,
			"speaker":    m.Speaker, "role": string(m.Role), "side": m.Side,
			"text": m.Text, "done": m.Done,
		}}, true
	case debate.TickMsg:
		return eventEnvelope{"tick", map[string]any{
			"channel_id":   m.ChannelID,
			"elapsed_ms":   m.Elapsed.Milliseconds(),
			"remaining_ms": m.Remaining.Milliseconds(),
		}}, true
	case debate.PhaseMsg:
		return eventEnvelope{"phase", map[string]any{
			"channel_id": m.ChannelID,
			"phase":      m.Phase.String(),
		}}, true
	case debate.StatusMsg:
		return eventEnvelope{"status", map[string]any{
			"channel_id": m.ChannelID,
			"text":       m.Text,
		}}, true
	case debate.ErrorMsg:
		text := ""
		if m.Err != nil {
			text = m.Err.Error()
		}
		return eventEnvelope{"error", map[string]any{
			"channel_id": m.ChannelID,
			"text":       text,
		}}, true
	case debate.EndedMsg:
		return eventEnvelope{"ended", map[string]any{
			"channel_id":      m.ChannelID,
			"transcript_path": m.TranscriptPath,
			"audio_path":      m.AudioPath,
		}}, true
	case debate.TopicMsg:
		return eventEnvelope{"topic", map[string]any{
			"channel_id": m.ChannelID,
			"id":         m.ID,
			"title":      m.Title,
			"index":      m.Index,
			"total":      m.Total,
		}}, true
	}
	return eventEnvelope{}, false
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	channelFilter := r.URL.Query().Get("channel")
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
			// In parallel mode each event is stamped with its channel id; an
			// empty filter means "send everything" (sequential mode default).
			if channelFilter != "" {
				eid := debate.MsgChannelID(v)
				if eid != "" && eid != channelFilter {
					continue
				}
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
	live := s.liveStreamForRequest(r)
	if live == nil {
		http.Error(w, "no audio stream", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)

	ch, cancel := live.Subscribe(128)
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

// liveStreamForRequest picks the LiveStream the request targets from the
// /api/audio/<id>/stream URL.
func (s *Server) liveStreamForRequest(r *http.Request) *audio.LiveStream {
	const prefix = "/api/audio/"
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	rel = strings.TrimSuffix(rel, "/stream")
	if rel == "" || rel == "stream" {
		return nil
	}
	if res := s.d.Sessions.ChannelResources(rel); res != nil {
		return res.LiveStream
	}
	return nil
}

// handleVideo serves the HLS playlist + segments produced by the encoder.
// It refuses any path that would escape the configured HLS directory and only
// serves files whose extensions are recognised HLS artefacts.
//
// URL shape:
//
//	/api/video/<channel>/<file>   uses ChannelResources(channel).HLSDir
func (s *Server) handleVideo(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/video/"
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}

	hlsDir, file := s.resolveVideoTarget(rel)
	if hlsDir == "" {
		http.Error(w, "channel off-air", http.StatusNotFound)
		return
	}
	if file == "" || strings.ContainsAny(file, `/\`) {
		http.NotFound(w, r)
		return
	}

	switch {
	case strings.HasSuffix(file, ".m3u8"):
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasSuffix(file, ".ts"):
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "max-age=10")
	default:
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(hlsDir, file)
	// Final containment check after Join.
	clean := filepath.Clean(full)
	if !strings.HasPrefix(clean, filepath.Clean(hlsDir)+string(filepath.Separator)) &&
		clean != filepath.Clean(hlsDir) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// resolveVideoTarget splits "<channel>/<file>" into (HLSDir, file). Returns
// ("","") when the channel is unknown or off-air, or when no channel segment
// is present.
func (s *Server) resolveVideoTarget(rel string) (hlsDir, file string) {
	i := strings.Index(rel, "/")
	if i <= 0 {
		return "", ""
	}
	channelID := rel[:i]
	file = rel[i+1:]
	if res := s.d.Sessions.ChannelResources(channelID); res != nil {
		return res.HLSDir, file
	}
	return "", ""
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
	cur := s.orchForRequest(r)
	if cur == nil {
		http.Error(w, "no active debate", http.StatusServiceUnavailable)
		return
	}
	username := s.ensureUsername(w, r)
	cur.PushUserMessage(req.Text, username)
	w.WriteHeader(http.StatusNoContent)
}
