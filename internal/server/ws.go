package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/sirily11/debate-bot/internal/content_creator"
)

// wsFrame is the envelope sent to WebSocket clients — the same {event, data}
// shape the SSE layer produces, so the dashboard can share decoding logic.
type wsFrame struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// wsInbound is a message a participating viewer sends up the socket to join
// the discussion. type:"message" is the only inbound kind today.
type wsInbound struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Username string `json:"username"`
}

// handleJobWS upgrades to a WebSocket that streams a single job's events
// (transcript, phase, tick, status, and the agent-activity status the live
// diagram needs) and accepts inbound participation messages, injecting them
// into the running orchestrator via the same path stream-mode chat uses.
func (s *Server) handleJobWS(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}

	// Origin enforcement is handled by the CORS middleware / service-token
	// auth in front of this handler; accept the upgrade from any origin the
	// surrounding layers already let through.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Inbound reader: viewer participation messages → orchestrator.
	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var in wsInbound
			if json.Unmarshal(data, &in) != nil {
				continue
			}
			if in.Type != "message" || in.Text == "" {
				continue
			}
			if orch := s.d.Jobs.Orch(jobID); orch != nil {
				username := in.Username
				if username == "" {
					username = "viewer"
				}
				orch.PushUserMessage(in.Text, username)
			}
		}
	}()

	sub, unsub := s.d.Bus.Subscribe(128)
	defer unsub()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}
		case v, ok := <-sub:
			if !ok {
				return
			}
			if eid := contentcreator.MsgChannelID(v); eid != "" && eid != jobID {
				continue
			}
			env, fine := envelope(v)
			if !fine {
				continue
			}
			if err := writeJSONFrame(ctx, conn, env.tag, env.payload); err != nil {
				return
			}
		}
	}
}

func writeJSONFrame(ctx context.Context, conn *websocket.Conn, event string, payload any) error {
	b, err := json.Marshal(wsFrame{Event: event, Data: payload})
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}
