package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
)

// TestPhaseLabelLocalizedViaAcceptLanguage verifies the SSE phase event's label
// is translated to the connection's Accept-Language, re-derived per-connection
// from the broadcast PhaseMsg's (Type, Phase) rather than the stamped default.
func TestPhaseLabelLocalizedViaAcceptLanguage(t *testing.T) {
	cases := []struct {
		accept string
		want   string
	}{
		{"", "Discussion"},               // missing header → English fallback
		{"en-US,en;q=0.9", "Discussion"}, // English
		{"zh-CN", "讨论"},                   // Simplified
		{"zh-Hans", "讨论"},
		{"zh-TW", "討論"}, // Traditional
		{"zh-HK", "討論"},
		{"zh-Hant-HK", "討論"},
	}
	for _, c := range cases {
		t.Run("accept="+c.accept, func(t *testing.T) {
			env := newTestServer(t)
			ts := env.ts

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?channel=tech", nil)
			if c.accept != "" {
				req.Header.Set("Accept-Language", c.accept)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("subscribe events: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("events status: %d", resp.StatusCode)
			}
			br := bufio.NewReader(resp.Body)

			// Drain the initial `: ok` heartbeat; after it the subscription is live.
			if line, err := br.ReadString('\n'); err != nil || !strings.HasPrefix(line, ":") {
				t.Fatalf("expected initial heartbeat, got %q err=%v", line, err)
			}

			// Broadcast a discussion free-speech phase (Traditional default "討論").
			env.bus.Publish(contentcreator.StampChannelID(contentcreator.PhaseMsg{
				Phase: agent.PhaseFreeSpeech,
				Type:  config.ContentTypeDiscussion,
			}, "tech"))

			ev, data, err := readSSEEvent(br)
			if err != nil {
				t.Fatalf("read phase event: %v", err)
			}
			if ev != "phase" {
				t.Fatalf("event = %q, want phase", ev)
			}
			var payload struct {
				Label string `json:"label"`
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("unmarshal phase data: %v", err)
			}
			if payload.Label != c.want {
				t.Errorf("Accept-Language %q → label %q, want %q", c.accept, payload.Label, c.want)
			}
		})
	}
}
