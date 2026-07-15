package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// e2eAudioBookScript builds a valid audiobook DebateTopic JSON with
// chapterCount planned chapters. indices (1-based) marks the chapters THIS
// discussion narrated — the chapter-batch fixtures use it so the checklist UI
// sees done vs pending chapters without running a generation.
func e2eAudioBookScript(title string, chapterCount int, indices []int) string {
	chapters := make([]config.AudioBookChapter, 0, chapterCount)
	outline := make([]string, 0, chapterCount)
	for i := 1; i <= chapterCount; i++ {
		chapters = append(chapters, config.AudioBookChapter{
			Title:   fmt.Sprintf("Synthetic Chapter %d", i),
			Summary: fmt.Sprintf("What happens in synthetic chapter %d.", i),
		})
		outline = append(outline, fmt.Sprintf("%d. Synthetic Chapter %d — what happens in synthetic chapter %d.", i, i, i))
	}
	topic := &config.DebateTopic{
		Title:                   title,
		Type:                    config.ContentTypeAudioBook,
		Language:                "en-US",
		TotalMinutes:            1,
		SegmentMaxSeconds:       60,
		TTSProvider:             config.TTSProviderAzure,
		Resolution:              config.Resolution1080p,
		Channel:                 "default",
		AudioBookHost:           config.AgentSpec{Name: "Test Narrator", Model: "gpt-4o-mini"},
		AudioBookStyle:          config.AudioBookStyleAudioBook,
		AudioBookChapters:       chapters,
		AudioBookChapterIndices: indices,
		Background:              "Synthetic background for the end-to-end audiobook.",
		// Chapter batches re-render script.md at generation time, and
		// type=audio-book validation requires the `## Surface` chapter
		// outline — without it the generate-chapters request 400s.
		Surface: strings.Join(outline, "\n"),
	}
	b, _ := json.Marshal(topic)
	return string(b)
}

// e2eUploadedAudioScript builds a valid uploaded-audio DebateTopic JSON: no
// generative roster (the transcript defines the speakers), a durable audio key
// resolved by the E2E playback route, and five distinctly-worded caption
// segments the transcript-editor UI tests assert against.
func e2eUploadedAudioScript(title string) string {
	captions := []string{
		"E2E caption one",
		"E2E caption two",
		"E2E caption three",
		"E2E caption four",
		"E2E caption five",
	}
	speakers := []string{"Alice", "Bob"}
	segments := make([]config.TranscriptSegment, 0, len(captions))
	for i, text := range captions {
		segments = append(segments, config.TranscriptSegment{
			Speaker:    speakers[i%len(speakers)],
			OffsetMS:   int64(i) * 5000,
			DurationMS: 4000,
			Text:       text,
		})
	}
	topic := &config.DebateTopic{
		Title:                   title,
		Type:                    config.ContentTypeUploadedAudio,
		Language:                "en-US",
		Channel:                 "default",
		UploadedAudioKey:        "e2e/uploaded-audio.mp3",
		UploadedAudioDurationMS: 60_000,
		UploadedAudioSpeakers:   speakers,
		TranscriptSegments:      segments,
	}
	b, _ := json.Marshal(topic)
	return string(b)
}

// seedUploadedAudioRow inserts one uploaded-audio fixture. It stays in the
// planning state (transcript segments are only editable before generation)
// and carries its captions inside script_json, not native_discussion_lines.
func (s *DiscussionStore) seedUploadedAudioRow(ctx context.Context, id, owner, title string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, job_id, download_url, duration_seconds,
		 points_charged, visibility, published_at, cover_type, cover_gradient_start, cover_gradient_end,
		 script_json, markdown, sources_json, researched, plan_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'en-US', '', '', 0, 10, 'private', 0, 'gradient', '#6E8BFF', '#9B6EFF',
		 ?, ?, '[]', 0, 'default', ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		id, owner, title, title, string(DiscussionPlanning),
		e2eUploadedAudioScript(title), "# "+title+"\n\nSynthetic uploaded-audio plan markdown.",
		now, now)
	return err
}

// seedAudioBookRow inserts one audiobook fixture, optionally linked to a
// parent (referenceID) and placed into an album at the given position.
func (s *DiscussionStore) seedAudioBookRow(ctx context.Context, id, owner, title, status, scriptJSON, referenceID, albumID string, albumPosition int64) error {
	now := time.Now().UnixMilli()
	downloadURL := ""
	var duration float64
	jobID := ""
	if status == string(DiscussionReady) {
		downloadURL = fmt.Sprintf("https://e2e.local/audio/%s.mp3", id)
		duration = 48
		jobID = "e2e-job-" + id
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, job_id, download_url, duration_seconds,
		 points_charged, visibility, published_at, cover_type, cover_gradient_start, cover_gradient_end,
		 script_json, markdown, sources_json, researched, plan_template,
		 reference_discussion_id, album_id, album_position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'en-US', ?, ?, ?, 10, 'private', 0, 'gradient', '#6E8BFF', '#9B6EFF',
		 ?, ?, '[]', 0, 'default', ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		id, owner, title, title, status, jobID, downloadURL, duration,
		scriptJSON, "# "+title+"\n\nSynthetic audiobook plan markdown.",
		referenceID, albumID, albumPosition,
		now, now)
	return err
}

// seedAlbumRow inserts one native_albums fixture.
func (s *DiscussionStore) seedAlbumRow(ctx context.Context, id, owner, title, kind, rootDiscussionID string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_albums
		(id, owner_user_id, title, kind, root_discussion_id, cover_type, cover_gradient_start, cover_gradient_end, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'gradient', '#6E8BFF', '#9B6EFF', ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		id, owner, title, kind, rootDiscussionID, now, now)
	return err
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
//
// The UI suite runs its test classes in parallel against this one backend, so
// fixtures follow a strict convention: a fixture a test class mutates must be
// private to that class, and fixtures shared across classes must stay read-only.
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
		{"test-ready-summary", "test", "E2E Summary Podcast", string(DiscussionReady), "private", true, false, true},
		{"test-ongoing", "test", "E2E Ongoing Podcast", string(DiscussionGenerating), "private", false, false, false},
		{"test-plan", "test", "E2E Plan Podcast", string(DiscussionPlanning), "private", false, false, false},
		{"test-plan-voice", "test", "E2E Voice Plan Podcast", string(DiscussionPlanning), "private", false, false, false},
		{"test-plan-shortfall", "test", "E2E Shortfall Plan Podcast", string(DiscussionPlanning), "private", false, false, false},
		{"test2-private", "test2", "E2E Other Private", string(DiscussionReady), "private", true, false, true},
		{"test2-public", "test2", "E2E Other Public", string(DiscussionReady), "public", true, true, true},
		{"test-market-podcast", "test2", "E2E Market Podcast", string(DiscussionReady), "public", true, true, true},
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

	// Uploaded-audio fixture: a planning-stage station whose captions the
	// transcript-editor UI tests retime, save, and navigate.
	if err := s.seedUploadedAudioRow(ctx, "test-uploaded-audio", "test", "E2E Uploaded Audio"); err != nil {
		return fmt.Errorf("seed uploaded audio: %w", err)
	}

	// Audiobook chapter-batch + album fixtures: a 12-chapter plan whose root
	// generated chapters 1-3 and whose follow-up batch generated 4-5, both
	// grouped into one auto album. Chapters 6-12 stay pending so the checklist
	// UI has both locked and selectable rows (and more pending than the
	// 5-per-batch cap, to exercise the client-side selection limit).
	const audioBookChapterCount = 12
	if err := s.seedAlbumRow(ctx, "test-album", "test", "E2E Audiobook Album", albumKindAuto, "test-audiobook"); err != nil {
		return fmt.Errorf("seed album: %w", err)
	}
	if err := s.seedAudioBookRow(ctx, "test-audiobook", "test", "E2E Audiobook",
		string(DiscussionReady),
		e2eAudioBookScript("E2E Audiobook", audioBookChapterCount, []int{1, 2, 3}),
		"", "test-album", albumBatchPositionBase+1); err != nil {
		return fmt.Errorf("seed audiobook root: %w", err)
	}
	if err := s.seedAudioBookRow(ctx, "test-audiobook-part2", "test", "E2E Audiobook — Chapters 4-5",
		string(DiscussionReady),
		e2eAudioBookScript("E2E Audiobook — Chapters 4-5", audioBookChapterCount, []int{4, 5}),
		"test-audiobook", "test-album", albumBatchPositionBase+4); err != nil {
		return fmt.Errorf("seed audiobook batch: %w", err)
	}
	for _, id := range []string{"test-audiobook", "test-audiobook-part2"} {
		if err := s.seedTranscript(ctx, id); err != nil {
			return fmt.Errorf("seed transcript %s: %w", id, err)
		}
	}

	// Album marketplace fixtures. test-publish-album starts private so the UI
	// test can publish a selected member. test-market-album has one public and
	// one private member so a second user can verify public album filtering.
	if err := s.seedAlbumRow(ctx, "test-publish-album", "test", "E2E Publish Album", albumKindManual, ""); err != nil {
		return fmt.Errorf("seed publish album: %w", err)
	}
	if err := s.seedAudioBookRow(ctx, "test-publish-album-one", "test", "E2E Publish Album One",
		string(DiscussionReady),
		e2eAudioBookScript("E2E Publish Album One", 2, []int{1}),
		"", "test-publish-album", albumBatchPositionBase+1); err != nil {
		return fmt.Errorf("seed publish album one: %w", err)
	}
	if err := s.seedAudioBookRow(ctx, "test-publish-album-two", "test", "E2E Publish Album Two",
		string(DiscussionReady),
		e2eAudioBookScript("E2E Publish Album Two", 2, []int{2}),
		"", "test-publish-album", albumBatchPositionBase+2); err != nil {
		return fmt.Errorf("seed publish album two: %w", err)
	}
	if err := s.seedAlbumRow(ctx, "test-market-album", "test", "E2E Market Album", albumKindManual, ""); err != nil {
		return fmt.Errorf("seed market album: %w", err)
	}
	if err := s.seedAudioBookRow(ctx, "test-market-public", "test", "E2E Market Public Episode",
		string(DiscussionReady),
		e2eAudioBookScript("E2E Market Public Episode", 2, []int{1}),
		"", "test-market-album", albumBatchPositionBase+1); err != nil {
		return fmt.Errorf("seed market public episode: %w", err)
	}
	if err := s.seedAudioBookRow(ctx, "test-market-private", "test", "E2E Market Private Episode",
		string(DiscussionReady),
		e2eAudioBookScript("E2E Market Private Episode", 2, []int{2}),
		"", "test-market-album", albumBatchPositionBase+2); err != nil {
		return fmt.Errorf("seed market private episode: %w", err)
	}
	marketCover := DiscussionCover{Type: "gradient", GradientStart: "#6E8BFF", GradientEnd: "#9B6EFF"}
	if _, err := s.SetAlbumCover(ctx, "test", "test-market-album", marketCover); err != nil {
		return fmt.Errorf("publish market album cover: %w", err)
	}
	if _, err := s.SetVisibility(ctx, "test", "test-market-public", DiscussionPublic, marketCover); err != nil {
		return fmt.Errorf("publish market public episode: %w", err)
	}
	for _, id := range []string{"test-publish-album-one", "test-publish-album-two", "test-market-public", "test-market-private"} {
		if err := s.seedTranscript(ctx, id); err != nil {
			return fmt.Errorf("seed transcript %s: %w", id, err)
		}
	}
	return nil
}
