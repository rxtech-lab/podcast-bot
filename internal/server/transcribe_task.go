package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/planner"
	"github.com/sirily11/debate-bot/internal/stt"
)

// AudioTranscribePayload is the queued transcription job for an uploaded-audio
// discussion. Reserved/ReserveLedgerID carry the points hold across the queue
// so the consuming pod can settle or refund it.
type AudioTranscribePayload struct {
	DiscussionID    string `json:"discussion_id"`
	UserID          string `json:"user_id"`
	AudioKey        string `json:"audio_key"`
	Filename        string `json:"filename,omitempty"`
	MIMEType        string `json:"mime_type"`
	SizeBytes       int64  `json:"size_bytes"`
	MaxSpeakers     int    `json:"max_speakers"`
	Reserved        int64  `json:"reserved"`
	ReserveLedgerID int64  `json:"reserve_ledger_id"`
}

// transcribeAudioTTL bounds the presigned GET handed to the STT provider —
// generous because Azure streams the object itself while transcribing.
const transcribeAudioTTL = 2 * time.Hour

// RunAudioTranscribeTask executes one queued transcription attempt: it runs
// the configured STT provider over the uploaded audio, converts the result to
// sentence-level transcript segments, stores them as the discussion's plan,
// settles the points hold, and seeds the AI review turn. A non-nil return is
// the attempt's failure; the dispatch layer retries or calls
// FailAudioTranscribeTask.
func (s *Server) RunAudioTranscribeTask(ctx context.Context, pl AudioTranscribePayload) error {
	started := time.Now()
	d, err := s.d.Discussions.Get(ctx, pl.UserID, pl.DiscussionID)
	if err != nil {
		return fmt.Errorf("load discussion: %w", err)
	}
	if d == nil {
		return mq.Permanent(fmt.Errorf("discussion %s not found", pl.DiscussionID))
	}
	// Idempotency across redeliveries: a stored transcript means a previous
	// attempt already finished the whole pipeline.
	if d.Script != nil && len(d.Script.TranscriptSegments) > 0 {
		return nil
	}
	provider, err := s.sttProvider(ctx)
	if err != nil {
		return mq.Permanent(err)
	}
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return mq.Permanent(fmt.Errorf("transcription requires S3 storage"))
	}
	audioURL, err := s.d.Uploader.PresignGet(ctx, pl.AudioKey, transcribeAudioTTL)
	if err != nil || audioURL == "" {
		return fmt.Errorf("presign audio: %w", err)
	}

	s.recordDiscussionProgress(ctx, d.ID, "transcribe", planner.ProgressEvent{
		Phase: "transcribing",
		Text:  fmt.Sprintf("Transcribing audio with %s…", provider.Name()),
	})
	tr, err := provider.Transcribe(ctx, stt.Request{
		AudioURL:    audioURL,
		MIME:        pl.MIMEType,
		SizeBytes:   pl.SizeBytes,
		MaxSpeakers: pl.MaxSpeakers,
	})
	if err != nil {
		return fmt.Errorf("transcribe (%s): %w", provider.Name(), err)
	}
	cues := stt.SentenceCues(tr)
	if len(cues) == 0 {
		return mq.Permanent(fmt.Errorf("no intelligible speech found in the audio"))
	}

	segments := make([]config.TranscriptSegment, 0, len(cues))
	speakers := map[int]bool{}
	for _, c := range cues {
		speakers[c.Speaker] = true
		segments = append(segments, config.TranscriptSegment{
			Speaker:    transcriptSpeakerName(c.Speaker),
			OffsetMS:   c.StartMS,
			DurationMS: c.EndMS - c.StartMS,
			Text:       c.Text,
		})
	}
	durationMS := tr.DurationMS
	if last := cues[len(cues)-1]; durationMS < last.EndMS {
		durationMS = last.EndMS
	}

	topic := &config.DebateTopic{
		Title:                    d.Topic,
		Type:                     config.ContentTypeUploadedAudio,
		Language:                 transcriptLanguage(tr),
		TotalMinutes:             int(math.Ceil(float64(durationMS) / 60000)),
		Channel:                  "default",
		UploadedAudioKey:         pl.AudioKey,
		UploadedAudioDurationMS:  durationMS,
		UploadedAudioMaxSpeakers: pl.MaxSpeakers,
		TranscriptSegments:       segments,
	}
	topic.UploadedAudioSpeakers = config.UploadedAudioSpeakerNames(topic)
	if topic.TotalMinutes < 1 {
		topic.TotalMinutes = 1
	}
	resp := planResponse{Script: topic}
	if _, err := s.d.Discussions.UpdatePlan(ctx, pl.UserID, d.ID, resp); err != nil {
		return fmt.Errorf("store transcript plan: %w", err)
	}
	if err := s.d.Discussions.AppendPlanTurn(ctx, pl.UserID, d.ID, "Transcription complete", resp); err != nil {
		s.logger().Warn("append transcription plan turn failed", "discussion", d.ID, "err", err)
	}

	hours := float64(durationMS) / 3_600_000
	costUSD := hours * s.sttCostPerHour(provider.Name())
	s.settleTranscription(ctx, pl.UserID, d.ID, pl.Reserved, pl.ReserveLedgerID, costUSD)

	// Seed the AI review turn: with the newest turn authored by the user the
	// plan view's auto-run fires the assistant's transcript review when opened.
	if s.d.Planning != nil {
		if conv, err := s.d.Planning.EnsureConversation(ctx, pl.UserID, d.ID); err == nil {
			if err := s.d.Planning.AppendTurn(ctx, conv.ID, uploadedAudioReviewTurn(
				pl, d, len(segments), len(speakers), durationMS,
			)); err != nil {
				s.logger().Warn("seed transcript review turn failed", "discussion", d.ID, "err", err)
			}
		} else {
			s.logger().Warn("ensure planning conversation failed", "discussion", d.ID, "err", err)
		}
	}

	s.clearDiscussionProgress(ctx, d.ID)
	s.publishDiscussionResourceUpdated("", d.ID, "Transcription complete", "plan")
	s.logger().Info("audio transcription finished",
		"discussion", d.ID,
		"provider", provider.Name(),
		"segments", len(segments),
		"speakers", len(speakers),
		"audio_ms", durationMS,
		"elapsed_ms", time.Since(started).Milliseconds(),
	)
	return nil
}

// AudioTranscribeRetrying surfaces a pending retry on the discussion progress
// line so the plan view shows the backoff instead of silence.
func (s *Server) AudioTranscribeRetrying(pl AudioTranscribePayload, attempt int, delay time.Duration) {
	s.recordDiscussionProgress(context.Background(), pl.DiscussionID, "transcribe", planner.ProgressEvent{
		Phase: "retrying",
		Text:  fmt.Sprintf("Transcription retrying (attempt %d/%d)…", attempt+1, mq.MaxAttempts),
	})
}

// FailAudioTranscribeTask is the terminal failure path of a queued
// transcription: refund the points hold, mark the discussion failed so the
// client leaves the transcribing state, and notify open clients.
func (s *Server) FailAudioTranscribeTask(pl AudioTranscribePayload, cause error) {
	ctx := context.Background()
	msg := "transcription failed"
	if cause != nil {
		msg = cause.Error()
	}
	s.refundTranscription(ctx, pl.UserID, pl.DiscussionID, pl.Reserved, pl.ReserveLedgerID)
	if err := s.d.Discussions.SetJobResult(ctx, pl.DiscussionID, DiscussionFailed, ""); err != nil {
		s.logger().Warn("mark transcription failure", "discussion", pl.DiscussionID, "err", err)
	}
	s.clearDiscussionProgress(ctx, pl.DiscussionID)
	s.publishDiscussionResourceUpdated("", pl.DiscussionID, "Transcription failed", "plan")
	s.logger().Error("audio transcription failed terminally", "discussion", pl.DiscussionID, "err", msg)
}

// transcriptSpeakerName renders a diarization speaker id as the display name
// stored in transcript segments. Unknown (0) collapses to Speaker 1.
func transcriptSpeakerName(speaker int) string {
	if speaker < 1 {
		speaker = 1
	}
	return fmt.Sprintf("Speaker %d", speaker)
}

// transcriptLanguage picks the plan language from the transcript's detected
// locales (majority wins); empty when the provider reports none.
func transcriptLanguage(tr *stt.Transcript) string {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, p := range tr.Phrases {
		loc := strings.TrimSpace(p.Locale)
		if loc == "" {
			continue
		}
		counts[loc]++
		if counts[loc] > bestN {
			best, bestN = loc, counts[loc]
		}
	}
	return best
}
