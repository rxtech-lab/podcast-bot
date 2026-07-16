package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// TestJobEventsProxyToOwnerPod is the regression for the multi-pod live-events
// bug: the frontends subscribe to /api/events?channel=<jobID> for a running
// job's transcript/phase/status, but only the owner pod's in-memory event bus
// has those events. A subscription that the load balancer sends to a non-owner
// pod must be reverse-proxied to the owner, otherwise the client sees nothing.
//
// Setup mirrors two pods sharing one job registry (the Turso role): pod-a owns
// the running job; the EventSource connects to pod-b, which must proxy to
// pod-a and relay an event published only on pod-a's bus.
func TestJobEventsProxyToOwnerPod(t *testing.T) {
	const jobID = "job-xyz"

	// Shared registry == shared Turso: both pods resolve the same owner.
	reg, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	reg.SetPodName("pod-a")
	reg.Add(jobID)
	reg.Update(jobID, func(j *Job) { j.Status = JobRunning })
	if got := reg.Get(jobID); got == nil || got.OwnerPod != "pod-a" {
		t.Fatalf("job owner = %+v, want OwnerPod=pod-a", got)
	}

	// Pod A (owner): its bus is the only one that will carry the job event.
	busA := eventbus.New(nil)
	t.Cleanup(busA.Close)
	srvA := New(Deps{
		Mode: ModeVideo, Bus: busA, Jobs: reg, Log: slog.Default(),
		PodName:     "pod-a",
		PeerHostFor: func(string) string { return "" }, // owner never proxies out
	})
	tsA := httptest.NewServer(srvA.Handler())
	t.Cleanup(tsA.Close)
	hostA := strings.TrimPrefix(tsA.URL, "http://")

	// Pod B (non-owner): proxies job traffic to pod-a via the headless DNS.
	busB := eventbus.New(nil)
	t.Cleanup(busB.Close)
	srvB := New(Deps{
		Mode: ModeVideo, Bus: busB, Jobs: reg, Log: slog.Default(),
		PodName: "pod-b",
		PeerHostFor: func(pod string) string {
			if pod == "pod-a" {
				return hostA
			}
			return ""
		},
	})
	tsB := httptest.NewServer(srvB.Handler())
	t.Cleanup(tsB.Close)

	// Subscribe through the NON-owner pod.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		tsB.URL+"/api/events?channel="+jobID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe via pod-b: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d, want 200", resp.StatusCode)
	}

	// Publish on the OWNER's bus repeatedly until the reader sees it — this
	// avoids racing the proxied subscription's bus registration on pod-a.
	got := make(chan string, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			ev, data, err := readSSEEvent(br)
			if err != nil {
				return
			}
			if ev == "status" {
				var payload struct {
					ChannelID string `json:"channel_id"`
					Text      string `json:"text"`
				}
				_ = json.Unmarshal(data, &payload)
				if payload.ChannelID == jobID {
					got <- payload.Text
					return
				}
			}
		}
	}()

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case text := <-got:
			if text != "owner-only-event" {
				t.Fatalf("event text = %q, want owner-only-event", text)
			}
			return // success: pod-b relayed pod-a's bus event
		case <-tick.C:
			busA.Publish(contentcreator.StatusMsg{ChannelID: jobID, Text: "owner-only-event"})
		case <-ctx.Done():
			t.Fatal("timed out waiting for owner event via non-owner pod")
		}
	}
}

func TestJobRouteTarget(t *testing.T) {
	cases := []struct {
		path, query   string
		wantID, wantS string
		wantOK        bool
	}{
		{"/api/jobs/abc/ws", "", "abc", "ws", true},
		{"/api/jobs/abc/hls/seg.ts", "", "abc", "hls/seg.ts", true},
		{"/api/jobs/abc/subtitles/live", "", "abc", "subtitles/live", true},
		{"/api/jobs/abc/captions/srt", "", "abc", "captions/srt", true},
		{"/api/events", "channel=abc", "abc", "events", true},
		{"/api/events", "", "", "", false},   // no channel -> not routable
		{"/api/jobs/abc", "", "", "", false}, // bare detail -> local
		{"/api/jobs", "", "", "", false},     // collection -> local
		{"/api/config", "", "", "", false},   // unrelated
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.path+"?"+c.query, nil)
		id, sub, ok := jobRouteTarget(r)
		if id != c.wantID || sub != c.wantS || ok != c.wantOK {
			t.Errorf("jobRouteTarget(%q?%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.path, c.query, id, sub, ok, c.wantID, c.wantS, c.wantOK)
		}
	}
}

func TestOwnerLocalWhenDone(t *testing.T) {
	cases := []struct {
		sub  string
		job  *Job
		want bool
	}{
		{"subtitles", &Job{}, true},
		{"subtitles/live", &Job{}, true},
		{"captions/srt", &Job{}, true},
		{"archive", &Job{}, true},
		{"transcript", &Job{}, true},
		{"hls/index.m3u8", &Job{}, true},
		{"video", &Job{S3Key: ""}, true},       // no shared mp4 -> proxy
		{"video", &Job{S3Key: "k.mp4"}, false}, // in S3 -> serve anywhere
		{"audio", &Job{AudioS3Key: ""}, true},  // no shared mp3 -> proxy
		{"audio", &Job{AudioS3Key: "k.mp3"}, false},
		{"events", &Job{}, false}, // no events after completion
		{"ws", &Job{}, false},
	}
	for _, c := range cases {
		if got := ownerLocalWhenDone(c.sub, c.job); got != c.want {
			t.Errorf("ownerLocalWhenDone(%q, %+v) = %v, want %v", c.sub, c.job, got, c.want)
		}
	}
}
