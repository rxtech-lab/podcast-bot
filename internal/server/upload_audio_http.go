package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/planner"
	"github.com/sirily11/debate-bot/internal/stt"
	"github.com/sirily11/debate-bot/internal/subtitleutil"
	"github.com/sirily11/debate-bot/internal/tts"
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

type uploadedAudioSegmentBatchUpdateRequest struct {
	Updates []uploadedAudioSegmentBatchUpdate `json:"updates"`
}

type uploadedAudioSegmentBatchUpdate struct {
	Index      *int    `json:"index"`
	Speaker    *string `json:"speaker"`
	OffsetMS   *int64  `json:"offset_ms"`
	DurationMS *int64  `json:"duration_ms"`
	Text       *string `json:"text"`
}

type uploadedAudioSpeakerAddRequest struct {
	Name string `json:"name"`
}

type uploadedAudioSpeakerRenameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
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
		Filename:        strings.TrimSpace(filepath.Base(req.Form.Audio.Filename)),
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
	if s.e2eMode() {
		// Hermetic E2E runs have no S3; the fixture's audio is a silent MP3
		// served by the E2E-only route registered in server.go.
		writeJSON(w, uploadedAudioPlaybackResponse{URL: "http://" + r.Host + "/api/e2e/uploaded-audio.mp3"})
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

// handleE2EUploadedAudio serves the silent MP3 backing every uploaded-audio
// fixture in hermetic E2E mode. Registered only when E2E mode is on; unauthenticated
// because AVPlayer fetches media URLs without Authorization headers.
func (s *Server) handleE2EUploadedAudio(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "audio/mpeg")
	_, _ = w.Write(tts.SilenceMP3(65 * time.Second))
}

// handleUploadedAudioSegmentUpdate persists a user's direct correction to one
// uploaded-audio transcript segment. Unlike the AI proofreading tools, this
// endpoint deliberately permits timing edits while reviewing the plan and
// after publication, always bounded by the known source-audio duration.
func (s *Server) handleUploadedAudioSegmentUpdate(w http.ResponseWriter, r *http.Request) {
	d, ok := s.editableUploadedAudioDiscussion(w, r)
	if !ok {
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
	next.UploadedAudioSpeakers = config.UploadedAudioSpeakerNames(&next)
	s.writeUploadedAudioPlanUpdate(w, r, d, &next, "Edited transcript")
}

// handleUploadedAudioSegmentBatchUpdate applies every caption correction to
// one in-memory plan snapshot, then persists that snapshot with one discussion
// UPDATE. This avoids one HTTP round trip and one full script_json rewrite per
// caption when the retiming editor saves several changes together.
func (s *Server) handleUploadedAudioSegmentBatchUpdate(w http.ResponseWriter, r *http.Request) {
	d, ok := s.editableUploadedAudioDiscussion(w, r)
	if !ok {
		return
	}
	var req uploadedAudioSegmentBatchUpdateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.Updates) == 0 {
		http.Error(w, "at least one transcript segment update is required", http.StatusBadRequest)
		return
	}

	next := *d.Script
	next.TranscriptSegments = append([]config.TranscriptSegment(nil), d.Script.TranscriptSegments...)
	seen := make(map[int]struct{}, len(req.Updates))
	for position, update := range req.Updates {
		if update.Index == nil {
			http.Error(w, fmt.Sprintf("updates[%d].index is required", position), http.StatusBadRequest)
			return
		}
		index := *update.Index
		if index < 0 || index >= len(next.TranscriptSegments) {
			http.Error(w, fmt.Sprintf("updates[%d].index is out of range", position), http.StatusBadRequest)
			return
		}
		if _, exists := seen[index]; exists {
			http.Error(w, fmt.Sprintf("transcript segment index %d is duplicated", index), http.StatusBadRequest)
			return
		}
		seen[index] = struct{}{}
		if update.Speaker == nil || update.OffsetMS == nil || update.DurationMS == nil || update.Text == nil {
			http.Error(w, fmt.Sprintf(
				"updates[%d] requires speaker, offset_ms, duration_ms, and text", position,
			), http.StatusBadRequest)
			return
		}
		segment := config.TranscriptSegment{
			Speaker:    strings.TrimSpace(*update.Speaker),
			OffsetMS:   *update.OffsetMS,
			DurationMS: *update.DurationMS,
			Text:       strings.TrimSpace(*update.Text),
		}
		if err := validateUploadedAudioSegmentEdit(segment, next.UploadedAudioDurationMS); err != nil {
			http.Error(w, fmt.Sprintf("updates[%d]: %v", position, err), http.StatusBadRequest)
			return
		}
		next.TranscriptSegments[index] = segment
	}
	next.UploadedAudioSpeakers = config.UploadedAudioSpeakerNames(&next)
	s.writeUploadedAudioPlanUpdate(w, r, d, &next, "Edited transcript")
}

// handleUploadedAudioSpeakerAdd persists an additional speaker option without
// assigning it to a transcript segment yet. The editor can then select it while
// correcting diarization mistakes.
func (s *Server) handleUploadedAudioSpeakerAdd(w http.ResponseWriter, r *http.Request) {
	d, ok := s.editableUploadedAudioDiscussion(w, r)
	if !ok {
		return
	}
	var req uploadedAudioSpeakerAddRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateUploadedAudioSpeakerName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, existing := range config.UploadedAudioSpeakerNames(d.Script) {
		if strings.EqualFold(existing, name) {
			http.Error(w, "speaker already exists", http.StatusConflict)
			return
		}
	}
	next := *d.Script
	next.UploadedAudioSpeakers = append(config.UploadedAudioSpeakerNames(d.Script), name)
	s.writeUploadedAudioPlanUpdate(w, r, d, &next, "Added speaker")
}

// handleUploadedAudioSpeakerRename renames a speaker everywhere in the plan,
// including every transcript segment attributed to that recognized speaker.
func (s *Server) handleUploadedAudioSpeakerRename(w http.ResponseWriter, r *http.Request) {
	d, ok := s.editableUploadedAudioDiscussion(w, r)
	if !ok {
		return
	}
	var req uploadedAudioSpeakerRenameRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	from := strings.TrimSpace(req.From)
	to := strings.TrimSpace(req.To)
	if err := validateUploadedAudioSpeakerName(from); err != nil {
		http.Error(w, "current "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateUploadedAudioSpeakerName(to); err != nil {
		http.Error(w, "new "+err.Error(), http.StatusBadRequest)
		return
	}

	next := *d.Script
	next.UploadedAudioSpeakers = append([]string(nil), config.UploadedAudioSpeakerNames(d.Script)...)
	next.TranscriptSegments = append([]config.TranscriptSegment(nil), d.Script.TranscriptSegments...)
	found := false
	for i, name := range next.UploadedAudioSpeakers {
		if strings.EqualFold(strings.TrimSpace(name), from) {
			next.UploadedAudioSpeakers[i] = to
			found = true
		}
	}
	for i := range next.TranscriptSegments {
		if strings.EqualFold(strings.TrimSpace(next.TranscriptSegments[i].Speaker), from) {
			next.TranscriptSegments[i].Speaker = to
			found = true
		}
	}
	if !found {
		http.Error(w, "speaker not found", http.StatusNotFound)
		return
	}
	next.UploadedAudioSpeakers = config.UploadedAudioSpeakerNames(&next)
	s.writeUploadedAudioPlanUpdate(w, r, d, &next, "Renamed speaker")
}

func (s *Server) editableUploadedAudioDiscussion(w http.ResponseWriter, r *http.Request) (*Discussion, bool) {
	user := s.requestUser(r)
	d, err := s.d.Discussions.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if d == nil || d.Script == nil || d.Script.Type != config.ContentTypeUploadedAudio {
		http.NotFound(w, r)
		return nil, false
	}
	if d.Status != DiscussionPlanning && d.Status != DiscussionReady {
		http.Error(w, "the uploaded-audio transcript can only be edited before generation or after publishing", http.StatusConflict)
		return nil, false
	}
	return d, true
}

func validateUploadedAudioSpeakerName(name string) error {
	if name == "" {
		return fmt.Errorf("speaker name is required")
	}
	if len([]rune(name)) > 100 {
		return fmt.Errorf("speaker name is too long")
	}
	return nil
}

func (s *Server) writeUploadedAudioPlanUpdate(
	w http.ResponseWriter,
	r *http.Request,
	d *Discussion,
	next *config.DebateTopic,
	label string,
) {
	if err := config.ValidateTopic(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	markdown, err := next.RenderMarkdown()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := planResponse{Script: next, Markdown: markdown, Sources: d.Sources, Researched: d.Researched}
	user := s.requestUser(r)
	updated, err := s.d.Discussions.UpdateUploadedAudioPlan(r.Context(), user.ID, d.ID, label, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	if d.Status == DiscussionReady {
		lines := uploadedAudioEditedTranscriptLines(next.TranscriptSegments, d.CreatedAt)
		if err := s.d.Discussions.ReplaceTranscript(r.Context(), d.ID, lines); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if refreshed, err := s.d.Discussions.Get(r.Context(), user.ID, d.ID); err == nil && refreshed != nil {
			updated = refreshed
		}
		PublishDiscussionResourceUpdated(s.d.Bus, s.d.Env, d.JobID, d.ID, "Captions updated", "captions")
	}
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

// uploadedAudioCaptionVTT renders published uploaded-audio captions from the
// durable transcript plan. This keeps post-publication edits authoritative
// without relying on the immutable sidecar produced by the original job run.
func (s *Server) uploadedAudioCaptionVTT(ctx context.Context, jobID string) (string, bool) {
	if s.d.Discussions == nil || strings.TrimSpace(jobID) == "" {
		return "", false
	}
	d, err := s.d.Discussions.GetByJobID(ctx, jobID)
	if err != nil || d == nil || d.Status != DiscussionReady || d.Script == nil ||
		d.Script.Type != config.ContentTypeUploadedAudio || len(d.Script.TranscriptSegments) == 0 {
		return "", false
	}
	cues := make([]contentcreator.SubtitleCue, 0, len(d.Script.TranscriptSegments))
	for _, segment := range d.Script.TranscriptSegments {
		text := subtitleutil.StripPunct(segment.Text)
		if text == "" || segment.DurationMS <= 0 {
			continue
		}
		start := time.Duration(segment.OffsetMS) * time.Millisecond
		cues = append(cues, contentcreator.SubtitleCue{
			Start: start,
			End:   start + time.Duration(segment.DurationMS)*time.Millisecond,
			Text:  text,
		})
	}
	return contentcreator.FormatSubtitleCues(contentcreator.ClampSubtitleCueOverlaps(cues)), true
}

func uploadedAudioEditedTranscriptLines(segments []config.TranscriptSegment, createdAt time.Time) []agent.TranscriptLine {
	base := createdAt
	if base.IsZero() {
		base = time.Now()
	}
	lines := make([]agent.TranscriptLine, 0, len(segments))
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		if n := len(lines); n > 0 && lines[n-1].Speaker == segment.Speaker {
			lines[n-1].Text = joinUploadedAudioTranscriptText(lines[n-1].Text, text)
			continue
		}
		lines = append(lines, agent.TranscriptLine{
			Speaker:       segment.Speaker,
			Role:          agent.RoleDiscussant,
			Text:          text,
			At:            base.Add(time.Duration(segment.OffsetMS) * time.Millisecond),
			AudioOffsetMS: segment.OffsetMS,
		})
	}
	return lines
}

func joinUploadedAudioTranscriptText(a, b string) string {
	if a == "" {
		return b
	}
	var last rune
	for _, r := range a {
		last = r
	}
	var first rune
	for _, r := range b {
		first = r
		break
	}
	if uploadedAudioCJKRune(last) || uploadedAudioCJKRune(first) {
		return a + b
	}
	return a + " " + b
}

func uploadedAudioCJKRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x3000 && r <= 0x303F) ||
		(r >= 0xFF00 && r <= 0xFFEF) ||
		(r >= 0x3040 && r <= 0x30FF) ||
		(r >= 0xAC00 && r <= 0xD7AF)
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
			"Keep every correction inside that segment's fixed time range; never move, split, or redistribute clauses or sentences between segment indices. "+
			"Propose fixes by calling update_plan with only the segments you change, then call show_plan. "+
			"The current title is derived from the uploaded file's name — always replace it with a generated episode title "+
			"describing the audio content, fewer than 10 words, in the transcript's language. "+
			"Also make sure the plan's language is the audio's spoken language (BCP-47, e.g. \"en-US\" or \"zh-CN\"); "+
			"set it in update_plan if it is missing or wrong. "+
			"If the transcript itself looks correct, still set the generated title and language, then show the plan. "+
			"You may also suggest friendlier speaker names.",
		segmentCount, speakerCount, formatClockDuration(durationMS))
}

// uploadedAudioReviewTurn persists the user's original upload as the visible
// conversation item while keeping the proofreading instruction available only
// to the planning model. planningUserDisplayText strips a settings-only turn,
// leaving an attachment-only user bubble in the client.
func uploadedAudioReviewTurn(pl AudioTranscribePayload, d *Discussion, segmentCount, speakerCount int, durationMS int64) planningTurnInput {
	title := ""
	if d != nil {
		title = d.Topic
		if strings.TrimSpace(title) == "" {
			title = d.Title
		}
	}
	attachment := uploadedAudioPlanningAttachment(pl.Filename, title, pl.MIMEType, pl.AudioKey)
	return planningTurnInput{
		Role: "user",
		Text: "Current plan settings:\n" +
			"- The visible user input is the attached original audio.\n\n" +
			uploadedAudioReviewPrompt(segmentCount, speakerCount, durationMS),
		Attachments: []planner.Attachment{attachment},
		OpID:        "transcript-review:" + pl.DiscussionID,
	}
}

func uploadedAudioPlanningAttachment(filename, fallbackTitle, mimeType, key string) planner.Attachment {
	filename = strings.TrimSpace(filepath.Base(filename))
	if filename == "" || filename == "." {
		filename = strings.TrimSpace(fallbackTitle)
		if filename == "" {
			filename = "Uploaded audio"
		}
		if ext := strings.ToLower(filepath.Ext(key)); ext != "" && !strings.HasSuffix(strings.ToLower(filename), ext) {
			filename += ext
		}
	}
	mimeType = normalizedUploadMIME(mimeType)
	if !isAudioMIME(mimeType) {
		mimeType = geminiAudioMIME(key)
	}
	return planner.Attachment{
		Filename: filename,
		MIMEType: mimeType,
		Key:      strings.TrimSpace(key),
	}
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
