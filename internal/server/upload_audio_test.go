package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/storage"
	"github.com/sirily11/debate-bot/internal/stt"
)

func TestTranscriptionReserveSettleRoundTrip(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	const user = "user-1"
	if _, err := ps.Credit(ctx, user, 100, "test_grant", ""); err != nil {
		t.Fatalf("credit: %v", err)
	}

	ok, _, ledgerID, err := ps.ReserveWithLedgerID(ctx, user, "disc-1", 10, pointsReasonTranscription)
	if err != nil || !ok {
		t.Fatalf("reserve: ok=%v err=%v", ok, err)
	}
	bal, err := ps.Balance(ctx, user)
	if err != nil || bal != 90 {
		t.Fatalf("balance after reserve = %d (err %v), want 90", bal, err)
	}

	// Settle to an actual of 4 points with a real STT cost attached.
	newBal, err := ps.SettleReserved(ctx, user, "disc-1", ledgerID, 10, 4, pointsReasonTranscription, PointsUsageDetail{STTCostUSD: 0.12, CostUSD: 0.12})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if newBal != 96 {
		t.Fatalf("balance after settle = %d, want 96", newBal)
	}

	// The settled cost must aggregate into the usage dashboard's STT column.
	summary, err := ps.UsageSpendByDate(ctx, 1)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	day := summary.Days[len(summary.Days)-1]
	if day.STTCostUSD < 0.119 || day.STTCostUSD > 0.121 {
		t.Fatalf("stt cost = %v, want 0.12", day.STTCostUSD)
	}
}

func TestTranscriptionRefundRestoresBalance(t *testing.T) {
	ps, _ := newTestPointsStore(t)
	ctx := context.Background()
	const user = "user-2"
	if _, err := ps.Credit(ctx, user, 50, "test_grant", ""); err != nil {
		t.Fatalf("credit: %v", err)
	}
	ok, _, ledgerID, err := ps.ReserveWithLedgerID(ctx, user, "disc-2", 8, pointsReasonTranscription)
	if err != nil || !ok {
		t.Fatalf("reserve: ok=%v err=%v", ok, err)
	}
	// actual=0 refunds the hold in full.
	newBal, err := ps.SettleReserved(ctx, user, "disc-2", ledgerID, 8, 0, pointsReasonTranscription, PointsUsageDetail{})
	if err != nil {
		t.Fatalf("refund settle: %v", err)
	}
	if newBal != 50 {
		t.Fatalf("balance after refund = %d, want 50", newBal)
	}
}

func TestUploadAudioConfigResolvers(t *testing.T) {
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "cfg.db"), "", "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	appCfg, err := NewAppConfigStore(ds)
	if err != nil {
		t.Fatalf("app config: %v", err)
	}
	ctx := context.Background()
	s := &Server{d: Deps{AppConfig: appCfg, Env: &config.Env{STTProvider: "gemini", TranscribeModel: "gemini-2.5-flash"}}}

	if got := s.resolvedSTTProvider(ctx); got != stt.ProviderGemini {
		t.Fatalf("default provider = %q, want gemini", got)
	}
	if got := s.resolvedSTTGeminiModel(ctx); got != "gemini-2.5-flash" {
		t.Fatalf("default gemini model = %q, want env transcribe model", got)
	}

	// Admin overrides win over the env defaults.
	if err := appCfg.Set(ctx, appConfigKeySTTProvider, "azure"); err != nil {
		t.Fatalf("set provider: %v", err)
	}
	if err := appCfg.Set(ctx, appConfigKeySTTGeminiModel, "gemini-3.5-flash"); err != nil {
		t.Fatalf("set gemini model: %v", err)
	}
	if got := s.resolvedSTTProvider(ctx); got != stt.ProviderAzure {
		t.Fatalf("overridden provider = %q, want azure", got)
	}
	if got := s.resolvedSTTGeminiModel(ctx); got != "gemini-3.5-flash" {
		t.Fatalf("overridden gemini model = %q", got)
	}
}

func TestUploadAudioEntitlementGate(t *testing.T) {
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "gate.db"), "", "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	appCfg, err := NewAppConfigStore(ds)
	if err != nil {
		t.Fatalf("app config: %v", err)
	}
	perms, err := NewSubscriptionPermissionStore(ds)
	if err != nil {
		t.Fatalf("perm store: %v", err)
	}
	ps, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("points store: %v", err)
	}
	ctx := context.Background()
	env := &config.Env{MaxPodcastAudioUploadMB: 500}
	s := &Server{d: Deps{AppConfig: appCfg, Env: env, SubscriptionPermissions: perms, Points: ps}}

	// No permission rows at all → hard default denies everything.
	if s.uploadAudioAllowedForUser(ctx, "user-a") {
		t.Fatal("user without any permission class must be denied")
	}

	// A free class granting the feature (with a 100 MB tier cap) allows it.
	free := SubscriptionPermission{Permissions: Permissions{
		Features: PermissionFeatures{CanUploadOwnAudio: true},
		Limits:   PermissionLimits{MaxUploadAudioMB: 100},
	}}
	if err := perms.Create(ctx, &free); err != nil {
		t.Fatalf("create free class: %v", err)
	}
	if !s.uploadAudioAllowedForUser(ctx, "user-a") {
		t.Fatal("free class grants upload audio; user must be allowed")
	}
	if got := s.uploadAudioCapBytes(ctx, "user-a"); got != 100<<20 {
		t.Fatalf("cap = %d, want tier cap 100 MiB", got)
	}

	// Removing the tier permission denies the feature again.
	free.Permissions.Features.CanUploadOwnAudio = false
	if err := perms.Update(ctx, free.ID, &free); err != nil {
		t.Fatalf("update free class: %v", err)
	}
	if s.uploadAudioAllowedForUser(ctx, "user-a") {
		t.Fatal("tier without the permission must be denied")
	}
}

func TestCheckUploadKind(t *testing.T) {
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "kind.db"), "", "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	perms, err := NewSubscriptionPermissionStore(ds)
	if err != nil {
		t.Fatalf("perm store: %v", err)
	}
	ps, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("points store: %v", err)
	}
	free := SubscriptionPermission{Permissions: Permissions{Features: PermissionFeatures{CanUploadOwnAudio: true}}}
	if err := perms.Create(context.Background(), &free); err != nil {
		t.Fatalf("create free class: %v", err)
	}
	env := &config.Env{MaxPodcastAudioUploadMB: 500}
	s := &Server{d: Deps{Env: env, SubscriptionPermissions: perms, Points: ps}}
	ctx := context.Background()

	// Default kind keeps the small document ceiling.
	if got, msg, _ := s.checkUploadKind(ctx, "u", "", "application/pdf"); got != maxUploadBytes || msg != "" {
		t.Fatalf("default kind = (%d, %q)", got, msg)
	}
	// Podcast audio requires an audio MIME.
	if _, msg, code := s.checkUploadKind(ctx, "u", uploadKindPodcastAudio, "application/pdf"); msg == "" || code != 400 {
		t.Fatalf("non-audio MIME must be rejected, got (%q, %d)", msg, code)
	}
	// Valid audio upload gets the env-wide cap (tier cap unset).
	if got, msg, _ := s.checkUploadKind(ctx, "u", uploadKindPodcastAudio, "audio/mpeg"); msg != "" || got != 500<<20 {
		t.Fatalf("podcast audio = (%d, %q), want 500 MiB", got, msg)
	}
	// Unknown kinds are rejected outright.
	if _, msg, _ := s.checkUploadKind(ctx, "u", "mystery", "audio/mpeg"); msg == "" {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestUploadAudioTitle(t *testing.T) {
	cases := map[string]string{
		"my_podcast-episode_01.mp3": "my podcast episode 01",
		"Interview.m4a":             "Interview",
		"":                          "Uploaded audio",
		"...":                       "Uploaded audio",
	}
	for in, want := range cases {
		if got := uploadAudioTitle(in); got != want {
			t.Fatalf("uploadAudioTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUploadedAudioTranscriptSegmentUpdateAPI(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "transcript-edit.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	uploader, err := storage.New(ctx, storage.Config{
		Bucket:          "test-bucket",
		Region:          "auto",
		DownloadBaseURL: "https://media.example",
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
	})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	srv := New(Deps{Discussions: store, Uploader: uploader})

	plan := &config.DebateTopic{
		Title:                   "Uploaded interview",
		Type:                    config.ContentTypeUploadedAudio,
		Language:                "en-US",
		TotalMinutes:            1,
		Channel:                 "default",
		UploadedAudioKey:        "uploads/anonymous/audio.mp3",
		UploadedAudioDurationMS: 30_000,
		TranscriptSegments: []config.TranscriptSegment{
			{Speaker: "Speaker 1", OffsetMS: 10_000, DurationMS: 4_000, Text: "Original words"},
		},
	}
	discussion, err := store.Create(ctx, "anonymous", plan.Title, planResponse{Script: plan})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := `{"speaker":"Host","offset_ms":11250,"duration_ms":5250,"text":"Corrected words."}`
	req := httptest.NewRequest(http.MethodPatch,
		"/api/discussions/"+discussion.ID+"/transcript/segments/0", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated Discussion
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Script == nil || len(updated.Script.TranscriptSegments) != 1 {
		t.Fatalf("updated script = %+v", updated.Script)
	}
	segment := updated.Script.TranscriptSegments[0]
	if segment.Speaker != "Host" || segment.OffsetMS != 11_250 || segment.DurationMS != 5_250 || segment.Text != "Corrected words." {
		t.Fatalf("updated segment = %+v", segment)
	}
	if !strings.Contains(updated.Markdown, "offset_ms: 11250") || !strings.Contains(updated.Markdown, "Corrected words.") {
		t.Fatalf("updated markdown = %q", updated.Markdown)
	}

	playbackReq := httptest.NewRequest(http.MethodGet,
		"/api/discussions/"+discussion.ID+"/uploaded-audio", nil)
	playbackRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(playbackRec, playbackReq)
	if playbackRec.Code != http.StatusOK {
		t.Fatalf("playback status = %d body=%s", playbackRec.Code, playbackRec.Body.String())
	}
	var playback uploadedAudioPlaybackResponse
	if err := json.NewDecoder(playbackRec.Body).Decode(&playback); err != nil {
		t.Fatalf("decode playback: %v", err)
	}
	if playback.URL != "https://media.example/test-bucket/uploads/anonymous/audio.mp3" {
		t.Fatalf("playback url = %q", playback.URL)
	}

	badBody := `{"speaker":"Host","offset_ms":29000,"duration_ms":2000,"text":"Too late"}`
	badReq := httptest.NewRequest(http.MethodPatch,
		"/api/discussions/"+discussion.ID+"/transcript/segments/0", strings.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest || !strings.Contains(badRec.Body.String(), "exceeds") {
		t.Fatalf("invalid range status = %d body=%s", badRec.Code, badRec.Body.String())
	}
}

func TestTranscriptLanguageMajority(t *testing.T) {
	tr := &stt.Transcript{Phrases: []stt.Phrase{
		{Locale: "zh-CN"}, {Locale: "zh-CN"}, {Locale: "en-US"}, {Locale: ""},
	}}
	if got := transcriptLanguage(tr); got != "zh-CN" {
		t.Fatalf("language = %q, want zh-CN", got)
	}
	if got := transcriptLanguage(&stt.Transcript{}); got != "" {
		t.Fatalf("empty transcript language = %q, want empty", got)
	}
}

func TestTranscriptSpeakerName(t *testing.T) {
	if got := transcriptSpeakerName(0); got != "Speaker 1" {
		t.Fatalf("speaker 0 = %q", got)
	}
	if got := transcriptSpeakerName(3); got != "Speaker 3" {
		t.Fatalf("speaker 3 = %q", got)
	}
}

// TestPointsLedgerSTTColumnMigration recreates a pre-stt_cost_usd ledger and
// verifies NewPointsStore adds the column via ALTER (the CREATE TABLE IF NOT
// EXISTS path skips existing tables, so fresh-DB tests never exercise this).
func TestPointsLedgerSTTColumnMigration(t *testing.T) {
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "migrate.db"), "", "")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	ctx := context.Background()
	// Old-shape table without stt_cost_usd.
	if _, err := ds.db.ExecContext(ctx, `CREATE TABLE points_ledger (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL,
		discussion_id TEXT NOT NULL DEFAULT '',
		delta INTEGER NOT NULL,
		reason TEXT NOT NULL,
		cost_usd REAL NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		llm_cost_usd REAL NOT NULL DEFAULT 0,
		tts_cost_usd REAL NOT NULL DEFAULT 0,
		music_cost_usd REAL NOT NULL DEFAULT 0,
		rc_event_id TEXT NOT NULL DEFAULT '',
		balance_after INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	ps, err := NewPointsStore(ds)
	if err != nil {
		t.Fatalf("NewPointsStore over old schema: %v", err)
	}
	// Both the insert and the aggregation read the new column.
	if _, err := ps.Credit(ctx, "user-m", 10, "test_grant", ""); err != nil {
		t.Fatalf("credit after migration: %v", err)
	}
	if _, err := ps.UsageSpendByDate(ctx, 1); err != nil {
		t.Fatalf("usage read after migration: %v", err)
	}
}
