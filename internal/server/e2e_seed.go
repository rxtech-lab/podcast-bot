package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// e2eSeedDiscussant is one panelist used to build a seed script.
type e2eSeedDiscussant struct{ name, aspect string }

// e2eScript builds a valid DebateTopic JSON for a seeded podcast so the player,
// summary, and (for planning fixtures) generation can run against it.
func e2eScript(title string) string {
	topic := &config.DebateTopic{
		Title:             title,
		Type:              config.ContentTypeDiscussion,
		Language:          "en-US",
		TotalMinutes:      1,
		SegmentMaxSeconds: 60,
		TTSProvider:       config.TTSProviderAzure,
		Resolution:        config.Resolution1080p,
		Channel:           "default",
		Host:              config.AgentSpec{Name: "Test Host", Model: "gpt-4o-mini"},
		Discussants: []config.AgentSpec{
			{Name: "Alice", Model: "gpt-4o-mini", Aspect: "technical"},
			{Name: "Bob", Model: "gpt-4o-mini", Aspect: "economic"},
			{Name: "Carol", Model: "gpt-4o-mini", Aspect: "ethical"},
		},
		Commander:  config.AgentSpec{Model: "gpt-4o-mini"},
		Storage:    config.StoragePlaintext,
		Background: "Synthetic background for the end-to-end test podcast.",
	}
	b, _ := json.Marshal(topic)
	return string(b)
}

// seedDiscussionRow inserts one native_discussions fixture, relying on the
// schema column defaults for everything not set here.
func (s *DiscussionStore) seedDiscussionRow(ctx context.Context, id, owner, title, status, visibility string, ready, public bool) error {
	now := time.Now().UnixMilli()
	downloadURL := ""
	var duration float64
	jobID := ""
	switch status {
	case string(DiscussionReady):
		downloadURL = fmt.Sprintf("https://e2e.local/audio/%s.mp3", id)
		duration = 48
		jobID = "e2e-job-" + id
	case string(DiscussionGenerating):
		jobID = "e2e-job-" + id
	}
	var publishedAt int64
	if public {
		publishedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, job_id, download_url, duration_seconds,
		 points_charged, visibility, published_at, cover_type, cover_gradient_start, cover_gradient_end,
		 script_json, markdown, sources_json, researched, plan_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'en-US', ?, ?, ?, 10, ?, ?, 'gradient', '#6E8BFF', '#9B6EFF',
		 ?, ?, '[]', 0, 'default', ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		id, owner, title, title, status, jobID, downloadURL, duration,
		visibility, publishedAt,
		e2eScript(title), "# "+title+"\n\nSynthetic plan markdown.",
		now, now)
	return err
}

// seedTranscript inserts a few transcript lines so a ready podcast has content
// for the summary agent and the player transcript view.
func (s *DiscussionStore) seedTranscript(ctx context.Context, id string) error {
	lines := []struct{ speaker, role, text string }{
		{"Test Host", "host", "Welcome to this synthetic end-to-end test discussion."},
		{"Alice", "discussant", "From a technical angle, the system works as designed."},
		{"Bob", "discussant", "Economically, the trade-offs are acceptable for testing."},
		{"Carol", "discussant", "Ethically, transparency in testing is important."},
		{"Test Host", "host", "Thank you all. That concludes our test discussion."},
	}
	now := time.Now().UnixMilli()
	for i, l := range lines {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO native_discussion_lines
			(discussion_id, speaker, role, side, text, start_ms, is_user, created_at)
			VALUES (?, ?, ?, '', ?, ?, 0, ?)
			ON CONFLICT DO NOTHING`,
			id, l.speaker, l.role, l.text, int64(i)*5000, now); err != nil {
			return err
		}
	}
	return nil
}

// SeedE2E populates the database with the fixtures the iOS XCUITest suite relies
// on. It is idempotent (every insert is ON CONFLICT DO NOTHING) and only ever
// called in E2E mode. Owner ids are the plain strings "test" (the acting test
// user) and "test2" (a different owner, for join/visibility tests).
func (s *DiscussionStore) SeedE2E(ctx context.Context, points *PointsStore) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("discussion store not configured")
	}

	// Generous balances so the points gates never block a test run.
	for _, uid := range []string{"test", "test2"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO user_points_balance (user_id, balance, updated_at)
			VALUES (?, 1000000, ?)
			ON CONFLICT(user_id) DO UPDATE SET balance = excluded.balance, updated_at = excluded.updated_at`,
			uid, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("seed balance %s: %w", uid, err)
		}
	}

	type fixture struct {
		id, owner, title, status, visibility string
		ready, public, transcript            bool
	}
	fixtures := []fixture{
		{"test-ready", "test", "E2E Ready Podcast", string(DiscussionReady), "private", true, false, true},
		{"test-ongoing", "test", "E2E Ongoing Podcast", string(DiscussionGenerating), "private", false, false, false},
		{"test-plan", "test", "E2E Plan Podcast", string(DiscussionPlanning), "private", false, false, false},
		{"test-plan-voice", "test", "E2E Voice Plan Podcast", string(DiscussionPlanning), "private", false, false, false},
		{"test2-private", "test2", "E2E Other Private", string(DiscussionReady), "private", true, false, true},
		{"test2-public", "test2", "E2E Other Public", string(DiscussionReady), "public", true, true, true},
	}
	for _, f := range fixtures {
		if err := s.seedDiscussionRow(ctx, f.id, f.owner, f.title, f.status, f.visibility, f.ready, f.public); err != nil {
			return fmt.Errorf("seed discussion %s: %w", f.id, err)
		}
		if f.transcript {
			if err := s.seedTranscript(ctx, f.id); err != nil {
				return fmt.Errorf("seed transcript %s: %w", f.id, err)
			}
		}
	}
	return nil
}
