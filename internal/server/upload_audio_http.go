package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/planner"
	"github.com/sirily11/debate-bot/internal/stt"
)

const uploadedAudioPlaybackTTL = time.Hour

// uploadAudioCreateRequest is the body of POST /api/discussions/upload-audio:
// the upload-audio precheck form values, posted verbatim by the client.
type uploadAudioCreateRequest struct {
	Form uploadAudioForm `json:"form"`
}

type uploadAudioForm struct {
	Audio struct {
		Key       string `json:"key"`
		Filename  string `json:"filename"`
		MIMEType  string `json:"mime_type"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"audio"`
	Settings struct {
		MaxSpeakers int `json:"max_speakers"`
	} `json:"settings"`
}

type uploadedAudioPlaybackResponse struct {
	URL string `json:"url"`
}

// uploadedAudioSegmentUpdateRequest is a complete replacement for one
// transcript segment. Pointer fields make omitted JSON values distinguishable
// from a valid zero start time.
type uploadedAudioSegmentUpdateRequest struct {
	Speaker    *string `json:"speaker"`
	OffsetMS   *int64  `json:"offset_ms"`
	DurationMS *int64  `json:"duration_ms"`
	Text       *string `json:"text"`
}

// handleDiscussionCreateUploadAudio creates a discussion around a user-uploaded
// audio file and enqueues its transcription. The discussion starts in the
// planning state; the transcribe task later writes the transcript plan and
// seeds the AI review turn, so the client lands on the plan chat immediately
// and watches transcription progress there.
func (s *Server) handleDiscussionCreateUploadAudio(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	if !s.uploadAudioAllowedForUser(r.Context(), user.ID) {
		http.Error(w, "upload own audio is not enabled for your account", http.StatusForbidden)
		return
	}
	if s.d.MQ == nil {
		http.Error(w, "transcription queue is not configured", http.StatusServiceUnavailable)
		return
	}
	var req uploadAudioCreateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	key := s.validatedAudioKey(user.ID, req.Form.Audio.Key)
	if key == "" {
		http.Error(w, "invalid audio key", http.StatusBadRequest)
		return
	}
	info, err := s.d.Uploader.Head(r.Context(), key)
	if err != nil {
		http.Error(w, "inspect audio: "+err.Error(), http.StatusBadGateway)
		return
	}
	if info.ContentLength <= 0 {
		http.Error(w, "uploaded audio is empty", http.StatusBadRequest)
		return
	}
	if info.ContentLength > s.uploadAudioCapBytes(r.Context(), user.ID) {
		http.Error(w, "audio file too large", http.StatusBadRequest)
		return
	}
	mimeType := normalizedUploadMIME(req.Form.Audio.MIMEType)
	if !isAudioMIME(mimeType) {
		mimeType = geminiAudioMIME(key)
	}

	title := uploadAudioTitle(req.Form.Audio.Filename)
	lang := "" // auto-detected by the transcriber
	d, err := s.d.Discussions.CreatePlaceholder(r.Context(), user.ID, title, lang, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	reserved, reserveLedgerID, ok := s.reserveTranscription(w, r, user.ID, d.ID, info.ContentLength)
	if !ok {
		return
	}
	payload := AudioTranscribePayload{
		DiscussionID:    d.ID,
		UserID:          user.ID,
		AudioKey:        key,
		MIMEType:        mimeType,
		SizeBytes:       info.ContentLength,
		MaxSpeakers:     stt.ClampMaxSpeakers(req.Form.Settings.MaxSpeakers),
		Reserved:        reserved,
		ReserveLedgerID: reserveLedgerID,
	}
	task, err := mq.NewTask(mq.TaskAudioTranscribe, d.ID, payload)
	if err == nil {
		err = s.d.MQ.Publish(r.Context(), mq.QueuePlanning, task)
	}
	if err != nil {
		s.refundTranscription(r.Context(), user.ID, d.ID, reserved, reserveLedgerID)
		s.logger().Error("audio transcribe enqueue failed", "discussion", d.ID, "err", err)
		http.Error(w, "transcription could not be started", http.StatusServiceUnavailable)
		return
	}
	s.recordDiscussionProgress(r.Context(), d.ID, "transcribe", planner.ProgressEvent{
		Phase: "transcribing",
		Text:  "Transcribing audio…",
	})
	// Attach the freshly recorded progress so the client's plan view opens
	// straight into its transcribing state.
	s.applyDiscussionProgress(r.Context(), d)
	writeJSON(w, d)
}

// handleUploadedAudioPlayback returns a short-lived URL for the original
// upload. The durable storage key remains server-only and the discussion owner
// is checked before a URL is minted.
func (s *Server) handleUploadedAudioPlayback(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	d, err := s.d.Discussions.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil || d.Script.Type != config.ContentTypeUploadedAudio ||
		strings.TrimSpace(d.Script.UploadedAudioKey) == "" {
		http.NotFound(w, r)
		return
	}
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		http.Error(w, "uploaded audio playback is not configured", http.StatusServiceUnavailable)
		return
	}
	url, err := s.d.Uploader.DownloadURL(r.Context(), d.Script.UploadedAudioKey, uploadedAudioPlaybackTTL)
	if err != nil || strings.TrimSpace(url) == "" {
		http.Error(w, "uploaded audio playback is unavailable", http.StatusBadGateway)
		return
	}
	writeJSON(w, uploadedAudioPlaybackResponse{URL: url})
}

// handleUploadedAudioSegmentUpdate persists a user's direct correction to one
// uploaded-audio transcript segment. Unlike the AI proofreading tools, this
// endpoint deliberately permits timing edits, but only while the plan is still
// editable and only within the known source-audio duration.
func (s *Server) handleUploadedAudioSegmentUpdate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	d, err := s.d.Discussions.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil || d.Script.Type != config.ContentTypeUploadedAudio {
		http.NotFound(w, r)
		return
	}
	if d.Status != DiscussionPlanning {
		http.Error(w, "the transcript can only be edited before generation", http.StatusConflict)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= len(d.Script.TranscriptSegments) {
		http.Error(w, "transcript segment index is out of range", http.StatusBadRequest)
		return
	}
	var req uploadedAudioSegmentUpdateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Speaker == nil || req.OffsetMS == nil || req.DurationMS == nil || req.Text == nil {
		http.Error(w, "speaker, offset_ms, duration_ms, and text are required", http.StatusBadRequest)
		return
	}
	segment := config.TranscriptSegment{
		Speaker:    strings.TrimSpace(*req.Speaker),
		OffsetMS:   *req.OffsetMS,
		DurationMS: *req.DurationMS,
		Text:       strings.TrimSpace(*req.Text),
	}
	if err := validateUploadedAudioSegmentEdit(segment, d.Script.UploadedAudioDurationMS); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	next := *d.Script
	next.TranscriptSegments = append([]config.TranscriptSegment(nil), d.Script.TranscriptSegments...)
	next.TranscriptSegments[index] = segment
	if err := config.ValidateTopic(&next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	markdown, err := next.RenderMarkdown()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := planResponse{
		Script:     &next,
		Markdown:   markdown,
		Sources:    d.Sources,
		Researched: d.Researched,
	}
	if err := s.d.Discussions.AppendPlanTurn(r.Context(), user.ID, d.ID, "Edited transcript", resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, d.ID, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

func validateUploadedAudioSegmentEdit(segment config.TranscriptSegment, audioDurationMS int64) error {
	if segment.Speaker == "" {
		return fmt.Errorf("speaker is required")
	}
	if segment.Text == "" {
		return fmt.Errorf("transcript content is required")
	}
	if segment.OffsetMS < 0 {
		return fmt.Errorf("start timestamp must not be negative")
	}
	if segment.DurationMS <= 0 {
		return fmt.Errorf("end timestamp must be after the start timestamp")
	}
	if audioDurationMS > 0 &&
		(segment.OffsetMS >= audioDurationMS || segment.DurationMS > audioDurationMS-segment.OffsetMS) {
		return fmt.Errorf("segment time range exceeds the uploaded audio duration")
	}
	return nil
}

// uploadAudioTitle derives a starter discussion title from the uploaded
// filename; the plan review chat can rename it later.
func uploadAudioTitle(filename string) string {
	base := strings.TrimSpace(filepath.Base(filename))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.NewReplacer("_", " ", "-", " ").Replace(base)
	base = strings.Join(strings.Fields(base), " ")
	if strings.Trim(base, ".") == "" {
		return "Uploaded audio"
	}
	return base
}

// uploadedAudioReviewPrompt is the seeded first user turn of the plan review
// conversation: it asks the agent to proofread the fresh transcript. Kept in
// English — the agent replies in the transcript's language per the planning
// system prompt.
func uploadedAudioReviewPrompt(segmentCount, speakerCount int, durationMS int64) string {
	return fmt.Sprintf(
		"The audio has been transcribed: %d segments, %d speaker(s), %s. "+
			"Review the transcript for likely transcription errors — misheard words, wrong homophones, "+
			"garbled names or terms, and segments attributed to the wrong speaker. "+
			"Propose fixes by calling update_plan with only the segments you change, then call show_plan. "+
			"If everything looks correct, say so and show the plan unchanged. "+
			"You may also suggest a better title and friendlier speaker names.",
		segmentCount, speakerCount, formatClockDuration(durationMS))
}

// formatClockDuration renders milliseconds as M:SS or H:MM:SS.
func formatClockDuration(ms int64) string {
	total := ms / 1000
	h, m, sec := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}
