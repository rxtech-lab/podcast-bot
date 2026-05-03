package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sseWriter wraps an http.ResponseWriter for Server-Sent Events.
// One sseWriter per request; not safe for concurrent writes.
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, rc: http.NewResponseController(w)}
}

// send writes one SSE event (event: <name>\ndata: <json>\n\n) and flushes.
func (s *sseWriter) send(event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return s.rc.Flush()
}

// comment writes a heartbeat comment (`: <text>\n\n`). Used to keep proxies
// alive during quiet stretches.
func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	return s.rc.Flush()
}
