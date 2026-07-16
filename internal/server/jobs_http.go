package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

// jobScriptName / jobPriorsName are the filenames the handler saves
// uploads under, inside <UploadRoot>/<jobID>/. The runner reads them
// back at the same paths.
const (
	jobScriptName = "script.md"
	jobPriorsName = "priors.zip"
)

const jobMessageMinInterval = 2 * time.Second

// handleJobSubmit accepts a multipart upload, registers a new pending
// job, stages the uploads on disk, and hands them off to the runner.
//
// Form fields:
//   - script    (required, file): the topic .md
//   - priors    (optional, file): zip archive of prior series generations
//   - soft_subs ("true"/"false"): mux a mov_text subtitle track
//   - burn_subs ("true"/"false"): hardcode subtitles (forces video re-encode)
//   - subtitle_languages (optional, repeated): translated soft-sub target codes
//
// Subtitle flags and a priors zip are gated to type=series at the runner
// level since the handler can't parse the .md frontmatter cheaply. The
// handler does enforce that one of the two file types we accept landed
// (script is mandatory).
func (s *Server) handleJobSubmit(w http.ResponseWriter, r *http.Request) {
	if s.d.SubmitJob == nil || s.d.Jobs == nil || s.d.UploadRoot == "" {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}

	// 256 MiB cap covers a generous priors zip; legitimate uploads are
	// usually a few hundred KB script + a few MB of generated PNGs.
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
		return
	}

	scriptF, scriptHeader, err := r.FormFile("script")
	if err != nil {
		http.Error(w, "script file is required (form field 'script')", http.StatusBadRequest)
		return
	}
	defer scriptF.Close()
	if !strings.HasSuffix(strings.ToLower(scriptHeader.Filename), ".md") {
		http.Error(w, "script must be a .md file", http.StatusBadRequest)
		return
	}

	jobID := newJobID()
	jobDir := filepath.Join(s.d.UploadRoot, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		http.Error(w, "create job dir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	scriptPath := filepath.Join(jobDir, jobScriptName)
	if err := saveUpload(scriptF, scriptPath); err != nil {
		http.Error(w, "save script: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var priorsPath string
	if pf, _, perr := r.FormFile("priors"); perr == nil {
		priorsPath = filepath.Join(jobDir, jobPriorsName)
		err := saveUpload(pf, priorsPath)
		pf.Close()
		if err != nil {
			http.Error(w, "save priors: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	sub := JobSubmission{
		ScriptPath:        scriptPath,
		PriorsZipPath:     priorsPath,
		SoftSubs:          formBool(r, "soft_subs"),
		BurnSubs:          formBool(r, "burn_subs"),
		Resolution:        strings.TrimSpace(r.FormValue("resolution")),
		SubtitleLanguages: formValues(r, "subtitle_languages"),
		AudioOnly:         formBool(r, "audio_only"),
	}

	if err := s.submitStaged(jobID, sub); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": jobID})
}

// submitStaged registers the job and hands its staged uploads to the runner,
// marking the job errored (and logging) when synchronous validation rejects
// it. Shared by the multipart and JSON submit paths.
func (s *Server) submitStaged(jobID string, sub JobSubmission) error {
	s.d.Jobs.Add(jobID)
	if err := s.d.SubmitJob(jobID, sub); err != nil {
		// Submission rejection is a synchronous failure (e.g. bad
		// frontmatter, subtitle flag on a non-series topic). Mark the
		// job errored so a follow-up GET surfaces the reason and the
		// upload directory is left in place for inspection.
		s.d.Jobs.Update(jobID, func(j *Job) {
			j.Status = JobError
			j.Error = err.Error()
		})
		s.d.Jobs.AppendLog(jobID, "error", err.Error(), nil)
		return err
	}
	return nil
}

// jobMessageReq is the body of POST /api/jobs/{id}/messages — a viewer
// participation message for a running video job.
type jobMessageReq struct {
	Text         string `json:"text"`
	Username     string `json:"username"`
	DiscussionID string `json:"discussion_id"`
	// ShareToken, when set, authorizes a signed-in participant who joined via a
	// share link to comment on a private discussion's live job.
	ShareToken string `json:"share_token"`
	// AudioKey is a voice message's durable storage key. The orchestrator still
	// only receives Text (the on-device transcript); the audio is persisted so
	// other participants can replay it. The key is validated against the sender
	// before use, and the playback URL is derived server-side — the client cannot
	// supply an arbitrary URL.
	AudioKey string `json:"audio_key"`
}

// handleJobMessage injects a viewer message into a running video job's
// orchestrator. This is the video-mode counterpart to stream-mode's
// POST /api/messages (which isn't mounted in video mode); the dashboard's
// "participate" box posts here. Returns 503 when the job has no live
// orchestrator (not started, finished, or unknown id).
func (s *Server) handleJobMessage(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	var req jobMessageReq
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	orch := s.d.Jobs.Orch(id)
	if orch == nil {
		http.Error(w, "no active job", http.StatusServiceUnavailable)
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = "viewer"
	}
	user := s.requestUser(r)
	if s.d.Discussions != nil {
		token := strings.TrimSpace(req.ShareToken)
		if token != "" && strings.TrimSpace(req.DiscussionID) != "" {
			if err := s.d.Discussions.AuthorizeShareParticipation(r.Context(), user.ID, req.DiscussionID, token); err != nil {
				writeDiscussionAccessError(w, err)
				return
			}
		} else if err := s.d.Discussions.AuthorizeJobParticipation(r.Context(), user.ID, req.DiscussionID, id); err != nil {
			writeDiscussionAccessError(w, err)
			return
		}
	}
	if retryAfter, ok := s.allowJobMessage(id, user, username); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds(retryAfter)))
		http.Error(w, "message rate limit: wait before sending another message", http.StatusTooManyRequests)
		return
	}
	audioKey := s.validatedAudioKey(user.ID, req.AudioKey)
	audioURL := s.voiceMessageAudioURL(r.Context(), audioKey)
	if s.d.Discussions != nil && strings.TrimSpace(req.DiscussionID) != "" {
		if err := s.d.Discussions.AppendLineVisibleWithToken(r.Context(), user.ID, req.DiscussionID, strings.TrimSpace(req.ShareToken), DiscussionLine{
			Speaker:  username,
			Role:     "user",
			Text:     req.Text,
			IsUser:   true,
			AudioURL: audioURL,
			AudioKey: audioKey,
		}); err != nil {
			writeDiscussionAccessError(w, err)
			return
		}
	}
	orch.PushUserMessageWithMetadata(req.Text, username, contentcreator.UserMessageMetadata{
		SenderUserID: user.ID,
		AudioURL:     audioURL,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) voiceMessageAudioURL(ctx context.Context, audioKey string) string {
	audioKey = strings.TrimSpace(audioKey)
	if audioKey == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return ""
	}
	url, err := s.d.Uploader.DownloadURL(ctx, audioKey, time.Hour)
	if err != nil {
		s.logger().Warn("voice message audio url failed", "key", audioKey, "err", err)
		return ""
	}
	return url
}

func (s *Server) allowJobMessage(jobID string, user requestUser, username string) (time.Duration, bool) {
	nowFn := s.jobMessageRateNow
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	key := jobMessageRateKey(jobID, user, username)

	s.jobMessageRateMu.Lock()
	defer s.jobMessageRateMu.Unlock()
	if s.jobMessageRateLast == nil {
		s.jobMessageRateLast = make(map[string]time.Time)
	}
	if last, ok := s.jobMessageRateLast[key]; ok {
		if elapsed := now.Sub(last); elapsed < jobMessageMinInterval {
			return jobMessageMinInterval - elapsed, false
		}
	}
	s.jobMessageRateLast[key] = now
	if len(s.jobMessageRateLast) > 1024 {
		for k, last := range s.jobMessageRateLast {
			if now.Sub(last) > 10*jobMessageMinInterval {
				delete(s.jobMessageRateLast, k)
			}
		}
	}
	return 0, true
}

func jobMessageRateKey(jobID string, user requestUser, username string) string {
	identity := strings.TrimSpace(user.ID)
	if identity == "" || identity == "anonymous" || identity == "service:dashboard" {
		if name := sanitizeUsername(username); name != "" {
			identity = identity + ":" + name
		}
	}
	return jobID + "\x00" + identity
}

func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	return int((d + time.Second - time.Nanosecond) / time.Second)
}

// handleJobStop force-stops generation for a running job. The runner still
// finalizes and uploads any audio/video already produced.
func (s *Server) handleJobStop(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	orch := s.d.Jobs.Orch(id)
	if orch == nil {
		http.Error(w, "no active job", http.StatusServiceUnavailable)
		return
	}
	if s.d.Discussions != nil {
		if err := s.d.Discussions.AuthorizeJobOwner(r.Context(), s.requestUser(r).ID, id); err != nil {
			writeDiscussionAccessError(w, err)
			return
		}
	}
	s.d.Jobs.AppendLog(id, "status", "force stop requested - finalising generated audio...", nil)
	orch.ForceStop()
	w.WriteHeader(http.StatusAccepted)
}

// handleJobList returns every job currently tracked by the registry.
// Useful for debugging; the SPA reads its own job by id.
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	items := s.d.Jobs.List()
	for i := range items {
		if label, ok := contentcreator.PhaseLabelFromString(items[i].Type, items[i].Phase, lang); ok {
			items[i].PhaseLabel = label
		}
		s.sanitizeJobUsage(&items[i])
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

// handleJobGet returns a single job snapshot. 404 when the id is
// unknown (which is also the response for an out-of-process restart
// since jobs aren't persisted).
func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	j := s.d.Jobs.Get(id)
	if j == nil {
		j = s.recoverJob(id)
		if j == nil {
			http.NotFound(w, r)
			return
		}
	}
	if url := s.jobDownloadURL(r.Context(), j); url != "" {
		j.DownloadURL = url
	}
	// Localize the phase label per request, mirroring the SSE/WS path. The
	// persisted job only stores the Traditional-default label, so derive it
	// from the phase string in the caller's negotiated language. j is a fresh
	// per-call copy (JobRegistry.Get builds it from the DB record), so mutating
	// it doesn't affect other clients.
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	if label, ok := contentcreator.PhaseLabelFromString(j.Type, j.Phase, lang); ok {
		j.PhaseLabel = label
	}
	s.sanitizeJobUsage(j)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j)
}

// handleJobVideo serves the job's rendered .mp4 once the job has
// reached JobDone. Returns 425 (Too Early) for in-flight jobs and 404
// when the asset doesn't exist (e.g. job errored before stitching).
func (s *Server) handleJobVideo(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	j := s.d.Jobs.Get(id)
	if j == nil {
		j = s.recoverJob(id)
		if j == nil {
			http.NotFound(w, r)
			return
		}
	}
	if j.Status != JobDone {
		http.Error(w, "video not ready", http.StatusTooEarly)
		return
	}
	if j.S3Key != "" && s.d.Uploader.Enabled() {
		url, err := s.d.Uploader.DownloadURL(r.Context(), j.S3Key, time.Hour)
		if err != nil {
			http.Error(w, "download url failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	// Audio-only (and failed-before-stitch) jobs have no video; don't fall
	// through to the S3 redirect, which keys off the generic S3Key field and
	// would otherwise hand back the .mp3.
	if !j.HasVideo {
		http.NotFound(w, r)
		return
	}
	if j.VideoPath == "" {
		http.Error(w, "video not ready", http.StatusTooEarly)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.mp4"`, jobDownloadStem(j)))
	http.ServeFile(w, r, j.VideoPath)
}

// handleJobAudio serves the job's rendered .mp3 for an audio-only job once it
// has reached JobDone. Mirrors handleJobVideo: 425 (Too Early) while in
// flight, S3 presigned redirect when the asset lives in object storage, and
// local file otherwise. 404 when the job produced no audio.
func (s *Server) handleJobAudio(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	j := s.d.Jobs.Get(id)
	if j == nil {
		j = s.recoverJob(id)
		if j == nil {
			http.NotFound(w, r)
			return
		}
	}
	if j.Status != JobDone || !j.HasAudio {
		http.Error(w, "audio not ready", http.StatusTooEarly)
		return
	}
	if j.AudioS3Key != "" && s.d.Uploader.Enabled() {
		url, err := s.d.Uploader.DownloadURL(r.Context(), j.AudioS3Key, time.Hour)
		if err != nil {
			http.Error(w, "download url failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	if j.AudioPath == "" {
		http.Error(w, "audio not ready", http.StatusTooEarly)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.mp3"`, jobDownloadStem(j)))
	http.ServeFile(w, r, j.AudioPath)
}

// handleJobTranscript returns a job's transcript as structured JSON. It merges
// native discussion rows, the per-job session.db, and any live orchestrator
// snapshot so reloads never lose lines just because one source is partial.
func (s *Server) handleJobTranscript(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	timer := newStationTimer()
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if lines, ok := s.translatedTranscriptLines(r.Context(), id, r.URL.Query().Get("language")); ok {
		writeDiscussionTranscript(w, lines)
		return
	}
	if orch := s.d.Jobs.Orch(id); orch != nil {
		t0 := time.Now()
		live := orch.Transcript.Snapshot()
		timer.mark("live_snapshot", t0)
		s.writeJobTranscriptTimed(w, s.mergedJobTranscriptTimed(r, id, live, timer), timer)
		return
	}
	if lines := s.mergedJobTranscriptTimed(r, id, nil, timer); len(lines) > 0 {
		s.writeJobTranscriptTimed(w, lines, timer)
		return
	}
	t0 := time.Now()
	job := s.d.Jobs.GetWithoutLogs(id)
	timer.mark("job_lookup", t0)
	if job == nil {
		t0 = time.Now()
		recovered := s.recoverJob(id)
		timer.mark("recover_job", t0)
		if recovered == nil {
			http.NotFound(w, r)
			return
		}
	}
	s.writeJobTranscriptTimed(w, nil, timer)
}

func (s *Server) writeJobTranscriptTimed(w http.ResponseWriter, lines []agent.TranscriptLine, timer *stationTimer) {
	t0 := time.Now()
	writeTranscript(w, lines)
	if timer != nil {
		timer.mark("write_json", t0)
		s.logStationTiming("jobs.transcript", len(lines), timer)
	}
}

func (s *Server) mergedJobTranscript(r *http.Request, jobID string, live []agent.TranscriptLine) []agent.TranscriptLine {
	return s.mergedJobTranscriptTimed(r, jobID, live, nil)
}

func (s *Server) mergedJobTranscriptTimed(r *http.Request, jobID string, live []agent.TranscriptLine, timer *stationTimer) []agent.TranscriptLine {
	t0 := time.Now()
	live = normalizedTranscriptLines(live)
	if timer != nil {
		timer.mark("normalize_live", t0)
	}
	t0 = time.Now()
	if disk := s.jobDiskTranscript(jobID); len(disk) > 0 {
		if timer != nil {
			timer.mark("disk_transcript", t0)
		}
		t0 = time.Now()
		out := appendTranscriptSuffix(disk, live)
		if timer != nil {
			timer.mark("append_suffix", t0)
		}
		return out
	}
	if timer != nil {
		timer.mark("disk_transcript", t0)
	}
	if len(live) > 0 {
		return live
	}
	t0 = time.Now()
	out := s.nativeDiscussionTranscript(r, jobID)
	if timer != nil {
		timer.mark("native_transcript", t0)
	}
	return out
}

func (s *Server) jobDiskTranscript(jobID string) []agent.TranscriptLine {
	jobDir := s.jobArtifactDir(jobID)
	if jobDir == "" {
		return nil
	}
	dbPath := filepath.Join(jobDir, "session.db")
	lines, err := contentcreator.LoadSnapshot(dbPath)
	if err != nil {
		if !errors.Is(err, contentcreator.ErrNoStore) {
			s.logger().Warn("job transcript disk load failed", "job", jobID, "path", dbPath, "err", err)
		}
		return nil
	}
	return normalizedTranscriptLines(lines)
}

func (s *Server) nativeDiscussionTranscript(r *http.Request, jobID string) []agent.TranscriptLine {
	if s.d.Discussions == nil {
		return nil
	}
	lines, err := s.d.Discussions.LinesByJob(r.Context(), jobID)
	if err != nil {
		s.logger().Warn("job transcript discussion db load failed", "job", jobID, "err", err)
		return nil
	}
	out := make([]agent.TranscriptLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, agent.TranscriptLine{
			Speaker:          strings.TrimSpace(line.Speaker),
			Role:             agent.Role(strings.TrimSpace(line.Role)),
			Side:             strings.TrimSpace(line.Side),
			Text:             line.Text,
			ImageURL:         line.ImageURL,
			Sources:          line.Sources,
			JudgementComment: line.JudgementComment,
			AudioOffsetMS:    line.StartMS,
		})
	}
	return normalizedTranscriptLines(out)
}

func normalizedTranscriptLines(lines []agent.TranscriptLine) []agent.TranscriptLine {
	out := make([]agent.TranscriptLine, 0, len(lines))
	for _, line := range lines {
		line.Speaker = strings.TrimSpace(line.Speaker)
		line.Side = strings.TrimSpace(line.Side)
		line.Text = strings.TrimSpace(line.Text)
		line.ImageURL = strings.TrimSpace(line.ImageURL)
		if line.Text == "" && line.ImageURL == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func appendTranscriptSuffix(base, live []agent.TranscriptLine) []agent.TranscriptLine {
	if len(base) == 0 {
		return append([]agent.TranscriptLine(nil), live...)
	}
	out := append([]agent.TranscriptLine(nil), base...)
	if len(live) == 0 {
		return out
	}
	overlap := transcriptOverlap(base, live)
	return append(out, live[overlap:]...)
}

func transcriptOverlap(base, next []agent.TranscriptLine) int {
	max := len(base)
	if len(next) < max {
		max = len(next)
	}
	for n := max; n > 0; n-- {
		if transcriptSequencesEqual(base[len(base)-n:], next[:n]) {
			return n
		}
	}
	return 0
}

func transcriptSequencesEqual(a, b []agent.TranscriptLine) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameTranscriptLine(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameTranscriptLine(a, b agent.TranscriptLine) bool {
	return strings.TrimSpace(a.Speaker) == strings.TrimSpace(b.Speaker) &&
		strings.TrimSpace(string(a.Role)) == strings.TrimSpace(string(b.Role)) &&
		strings.TrimSpace(a.Side) == strings.TrimSpace(b.Side) &&
		strings.TrimSpace(a.Text) == strings.TrimSpace(b.Text) &&
		strings.TrimSpace(a.ImageURL) == strings.TrimSpace(b.ImageURL)
}

// subtitlesS3URL resolves a presigned URL for a job's subtitles sidecar. It uses
// the persisted SubtitlesS3Key when set, otherwise falls back to the
// deterministic upload key (jobID.vtt) when that object actually exists in the
// bucket. The fallback is what lets captions survive a job whose record never
// stored the key (older jobs, or jobs recovered from disk) on deployments where
// the sidecar lives only in S3 — the case a public podcast viewed from another
// pod hits. Returns "" when no S3 object is available (caller falls back to the
// local sidecar).
func (s *Server) subtitlesS3URL(ctx context.Context, j *Job) string {
	if j == nil || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return ""
	}
	key := j.SubtitlesS3Key
	if key == "" {
		for _, name := range []string{
			path.Join(PodcastAudioDir, j.ID+".vtt"),
			j.ID + ".vtt",
		} {
			candidate := s.d.Uploader.Key(name)
			if info, err := s.d.Uploader.Head(ctx, candidate); err == nil && info.ContentLength > 0 {
				key = candidate
				break
			}
		}
	}
	if key == "" {
		return ""
	}
	url, err := s.d.Uploader.DownloadURL(ctx, key, time.Hour)
	if err != nil {
		s.logger().Warn("subtitles s3 download url failed", "job", j.ID, "key", key, "err", err)
		return ""
	}
	return url
}

// handleJobSubtitles serves the WebVTT sidecar the pipeline writes next to the
// run audio. Available for any finished job that produced one (audio-only feeds
// expose it as the captions track; video jobs already mux it into the mp4).
func (s *Server) handleJobSubtitles(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil || s.d.UploadRoot == "" {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	j := s.d.Jobs.Get(id)
	if j == nil {
		j = s.recoverJob(id)
		if j == nil {
			http.NotFound(w, r)
			return
		}
	}
	if j.Status != JobDone {
		http.Error(w, "subtitles not ready", http.StatusTooEarly)
		return
	}
	if captions, ok := s.translatedCaptions(r.Context(), id, r.URL.Query().Get("language")); ok {
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, captions)
		return
	}
	if captions, ok := s.uploadedAudioCaptionVTT(r.Context(), id); ok {
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, captions)
		return
	}
	if url := s.subtitlesS3URL(r.Context(), j); url != "" {
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	jobDir := s.jobArtifactDir(id)
	if jobDir == "" {
		http.NotFound(w, r)
		return
	}
	subPath := firstExistingNonEmpty(
		podcastSubtitlesPath(jobDir),
		legacyPodcastSubtitlesPath(jobDir),
	)
	if subPath == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(subPath); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeFile(w, r, subPath)
}

// handleJobSubtitlesLive serves the captions accumulated so far as WebVTT while
// a job is still generating, so a client streaming the live audio can show
// synced captions before the final sidecar exists. Unlike handleJobSubtitles it
// never returns 425: a running job yields cues-so-far (just the WEBVTT header
// early on), and once the run has ended it falls back to the written sidecar.
func (s *Server) handleJobSubtitlesLive(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if captions, ok := s.translatedCaptions(r.Context(), id, r.URL.Query().Get("language")); ok {
		_, _ = io.WriteString(w, captions)
		return
	}
	if captions, ok := s.uploadedAudioCaptionVTT(r.Context(), id); ok {
		_, _ = io.WriteString(w, captions)
		return
	}
	if orch := s.d.Jobs.Orch(id); orch != nil {
		_, _ = io.WriteString(w, contentcreator.FormatSubtitleCues(orch.LiveSubtitleCues()))
		return
	}
	// No running orchestrator. Prefer the shared-storage copy (durable across
	// pod recycles); fall back to the owner-local sidecar; else an empty
	// (header-only) WebVTT so the client always gets a valid document.
	if url := s.subtitlesS3URL(r.Context(), s.d.Jobs.Get(id)); url != "" {
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	if jobDir := s.jobArtifactDir(id); jobDir != "" {
		subPath := firstExistingNonEmpty(
			podcastSubtitlesPath(jobDir),
			legacyPodcastSubtitlesPath(jobDir),
		)
		if subPath != "" {
			http.ServeFile(w, r, subPath)
			return
		}
	}
	_, _ = io.WriteString(w, "WEBVTT\n\n")
}

// illustrationCueDTO is one client-facing entry of the audiobook illustration
// timeline. It intentionally omits the internal storage key.
type illustrationCueDTO struct {
	StartMS  int64  `json:"start_ms"`
	ImageURL string `json:"image_url"`
	Caption  string `json:"caption,omitempty"`
}

type illustrationsResponse struct {
	Illustrations []illustrationCueDTO `json:"illustrations"`
}

// handleJobIllustrations serves the canonical audiobook illustration timeline
// — the {start_ms, image_url, caption} array a player uses to switch artwork
// in sync with playback. Clients treat this as the only source of
// illustration timing; they never reconstruct it from transcript lines.
// Resolution order:
//  1. a running orchestrator's live timeline (cues recorded so far),
//  2. the illustrations.json sidecar (S3, then owner-local disk),
//  3. legacy synthesis from persisted discussion lines (timed start_ms rows;
//     else an even split over the optional ?duration_ms=).
//
// Non-audiobook jobs simply return an empty array.
func (s *Server) handleJobIllustrations(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if orch := s.d.Jobs.Orch(id); orch != nil {
		writeIllustrations(w, orch.AudioBookIllustrationTimeline())
		return
	}
	j := s.d.Jobs.Get(id)
	if j == nil {
		j = s.recoverJob(id)
	}
	if cues, ok := s.illustrationsFromSidecar(r.Context(), id, j); ok {
		writeIllustrations(w, cues)
		return
	}
	writeIllustrations(w, s.legacyIllustrations(r, id))
}

func writeIllustrations(w http.ResponseWriter, cues []contentcreator.IllustrationCue) {
	out := illustrationsResponse{Illustrations: make([]illustrationCueDTO, 0, len(cues))}
	for _, c := range cues {
		if strings.TrimSpace(c.ImageURL) == "" {
			continue
		}
		out.Illustrations = append(out.Illustrations, illustrationCueDTO{
			StartMS:  c.StartMS,
			ImageURL: c.ImageURL,
			Caption:  c.Caption,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// illustrationsFromSidecar loads the persisted illustrations.json for a
// finished job — the shared-storage copy first (durable across pod recycles),
// then the owner-local sidecar — and re-mints each cue's image URL from its
// durable key so a presigned URL stored at generation time can't expire out
// from under old audiobooks.
func (s *Server) illustrationsFromSidecar(ctx context.Context, id string, j *Job) ([]contentcreator.IllustrationCue, bool) {
	var raw []byte
	if s.d.Uploader != nil && s.d.Uploader.Enabled() {
		key := ""
		if j != nil {
			key = j.IllustrationsS3Key
		}
		if key == "" {
			candidate := s.d.Uploader.Key(path.Join(PodcastAudioDir, id+".illustrations.json"))
			if info, err := s.d.Uploader.Head(ctx, candidate); err == nil && info.ContentLength > 0 {
				key = candidate
			}
		}
		if key != "" {
			if data, err := s.d.Uploader.Download(ctx, key); err == nil && len(data) > 0 {
				raw = data
			} else if err != nil {
				s.logger().Warn("illustrations sidecar download failed", "job", id, "key", key, "err", err)
			}
		}
	}
	if raw == nil {
		if jobDir := s.jobArtifactDir(id); jobDir != "" {
			localPath := filepath.Join(jobDir, PodcastAudioDir, PodcastIllustrationsFilename)
			if data, err := os.ReadFile(localPath); err == nil && len(data) > 0 {
				raw = data
			}
		}
	}
	if raw == nil {
		return nil, false
	}
	var doc struct {
		Illustrations []contentcreator.IllustrationCue `json:"illustrations"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		s.logger().Warn("illustrations sidecar decode failed", "job", id, "err", err)
		return nil, false
	}
	if len(doc.Illustrations) == 0 {
		return nil, false
	}
	if s.d.Uploader != nil && s.d.Uploader.Enabled() {
		for i := range doc.Illustrations {
			key := strings.TrimSpace(doc.Illustrations[i].ImageKey)
			if key == "" {
				continue
			}
			if url, err := s.d.Uploader.DownloadURL(ctx, key, time.Hour); err == nil && url != "" {
				doc.Illustrations[i].ImageURL = url
			}
		}
	}
	return doc.Illustrations, true
}

// legacyIllustrations synthesizes a timeline for audiobooks generated before
// the sidecar existed, from the image lines persisted with the discussion.
func (s *Server) legacyIllustrations(r *http.Request, id string) []contentcreator.IllustrationCue {
	if s.d.Discussions == nil {
		return nil
	}
	lines, err := s.d.Discussions.LinesByJob(r.Context(), id)
	if err != nil {
		s.logger().Warn("legacy illustrations line load failed", "job", id, "err", err)
		return nil
	}
	var durationMS int64
	if v := strings.TrimSpace(r.URL.Query().Get("duration_ms")); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			durationMS = n
		}
	}
	return synthesizeIllustrationTimeline(lines, durationMS)
}

// synthesizeIllustrationTimeline builds a best-effort timeline from persisted
// discussion lines. Rows with a recorded start_ms (> 0 — 0 is the column
// default and means "unknown") are used as-is, with the transcript's first
// image anchored to 0 when it lacks one (the opening illustration is emitted
// at offset ~0, indistinguishable from the default). With no timed rows at
// all, the images are split evenly across durationMS, mirroring the legacy
// client slideshow; without a duration only the first image is returned so
// the artwork still opens on something.
func synthesizeIllustrationTimeline(lines []DiscussionLine, durationMS int64) []contentcreator.IllustrationCue {
	type entry struct {
		url     string
		startMS int64
		caption string
	}
	var entries []entry
	seen := map[string]bool{}
	for _, l := range lines {
		url := strings.TrimSpace(l.ImageURL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		entries = append(entries, entry{url: url, startMS: l.StartMS, caption: strings.TrimSpace(l.Text)})
	}
	if len(entries) == 0 {
		return nil
	}
	var timed []contentcreator.IllustrationCue
	for _, e := range entries {
		if e.startMS > 0 {
			timed = append(timed, contentcreator.IllustrationCue{StartMS: e.startMS, ImageURL: e.url, Caption: e.caption})
		}
	}
	if len(timed) > 0 {
		sort.Slice(timed, func(i, j int) bool { return timed[i].StartMS < timed[j].StartMS })
		if entries[0].startMS <= 0 {
			timed = append([]contentcreator.IllustrationCue{
				{StartMS: 0, ImageURL: entries[0].url, Caption: entries[0].caption},
			}, timed...)
		}
		return timed
	}
	if durationMS <= 0 {
		return []contentcreator.IllustrationCue{{StartMS: 0, ImageURL: entries[0].url, Caption: entries[0].caption}}
	}
	per := durationMS / int64(len(entries))
	out := make([]contentcreator.IllustrationCue, 0, len(entries))
	for i, e := range entries {
		out = append(out, contentcreator.IllustrationCue{StartMS: int64(i) * per, ImageURL: e.url, Caption: e.caption})
	}
	return out
}

// handleJobArchive serves the per-job zip of the persistent show
// directory. Only present for series jobs — non-series jobs return
// 404.
func (s *Server) handleJobArchive(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	j := s.d.Jobs.Get(id)
	if j == nil {
		j = s.recoverJob(id)
		if j == nil {
			http.NotFound(w, r)
			return
		}
	}
	if j.Status != JobDone || j.ArchivePath == "" {
		http.Error(w, "archive not ready", http.StatusTooEarly)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-archive.zip"`, jobDownloadStem(j)))
	http.ServeFile(w, r, j.ArchivePath)
}

// handleJobHLS serves the live HLS playlist + segments the encoder writes
// while a job runs, so the SPA can show a realtime preview of the video being
// generated. The encoder runs in archival/EVENT mode, so segments accumulate
// and the playlist keeps growing until the job finishes (then ENDLIST lands).
//
// The HLS dir mirrors the runner's layout: <OutDir>/jobs/<id>/hls, where
// OutDir is the parent of UploadRoot (same derivation recoverJob uses). The
// job id comes from the path so no registry lookup is needed — segments may be
// requested before the registry has caught up.
func (s *Server) handleJobHLS(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil || s.d.UploadRoot == "" {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	file := r.PathValue("file")
	if id == "" || file == "" || strings.ContainsAny(file, `/\`) || strings.Contains(file, "..") {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(file, ".m3u8"):
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasSuffix(file, ".ts"):
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "max-age=10")
	default:
		http.NotFound(w, r)
		return
	}
	hlsDir := filepath.Join(filepath.Dir(s.d.UploadRoot), "jobs", id, "hls")
	full := filepath.Join(hlsDir, file)
	// Final containment check after Join — defends against a crafted id/file.
	clean := filepath.Clean(full)
	if !strings.HasPrefix(clean, filepath.Clean(hlsDir)+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	// During warmup the playlist/segment may not exist yet; ServeFile 404s,
	// which the SPA's HLS player treats as "keep polling".
	http.ServeFile(w, r, full)
}

func saveUpload(src io.Reader, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, src)
	return err
}

func podcastAudioPath(jobDir string) string {
	return filepath.Join(jobDir, PodcastAudioDir, PodcastAudioFilename)
}

func legacyPodcastAudioPath(jobDir string) string {
	return filepath.Join(jobDir, PodcastAudioFilename)
}

func podcastSubtitlesPath(jobDir string) string {
	return filepath.Join(jobDir, PodcastAudioDir, PodcastSubtitlesFilename)
}

func legacyPodcastSubtitlesPath(jobDir string) string {
	return filepath.Join(jobDir, PodcastSubtitlesFilename)
}

func firstExistingNonEmpty(paths ...string) string {
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			return p
		}
	}
	return ""
}

func (s *Server) recoverJob(id string) *Job {
	if s.d.Jobs == nil || s.d.UploadRoot == "" {
		return nil
	}
	jobOutDir := s.jobArtifactDir(id)
	if jobOutDir == "" {
		return nil
	}
	mp4Path := filepath.Join(jobOutDir, "video.mp4")
	archivePath := filepath.Join(jobOutDir, "archive.zip")
	audioPath := podcastAudioPath(jobOutDir)
	legacyAudioPath := legacyPodcastAudioPath(jobOutDir)

	mp4Info, mp4Err := os.Stat(mp4Path)
	archiveInfo, archiveErr := os.Stat(archivePath)
	audioInfo, audioErr := os.Stat(audioPath)
	if audioErr != nil {
		audioPath = legacyAudioPath
		audioInfo, audioErr = os.Stat(audioPath)
	}
	if mp4Err != nil && archiveErr != nil && audioErr != nil {
		return nil
	}

	j := s.d.Jobs.Add(id)
	s.d.Jobs.Update(id, func(j *Job) {
		j.Status = JobDone
		if mp4Err == nil {
			j.VideoPath = mp4Path
			j.HasVideo = true
		}
		if archiveErr == nil {
			j.ArchivePath = archivePath
			j.HasArchive = true
		}
		// audio.mp3 (and no video.mp4) is the audio-only feed artefact.
		if audioErr == nil {
			j.AudioPath = audioPath
			j.HasAudio = true
			if mp4Err != nil {
				j.AudioOnly = true
			}
		}
		scriptPath := filepath.Join(filepath.Dir(filepath.Dir(jobOutDir)), "uploads", id, jobScriptName)
		if topic, err := config.LoadTopic(scriptPath); err == nil {
			j.Title = topic.Title
			j.Type = topic.Type
			j.Show = topic.Show
			j.Season = topic.Season
			j.Episode = topic.Episode
		}
	})
	if mp4Err == nil {
		s.d.Jobs.AppendLog(id, "status", fmt.Sprintf("recovered mp4 · %.1f MB",
			float64(mp4Info.Size())/(1024*1024)), nil)
	}
	if archiveErr == nil {
		s.d.Jobs.AppendLog(id, "status", fmt.Sprintf("recovered archive · %.1f MB",
			float64(archiveInfo.Size())/(1024*1024)), nil)
	}
	if audioErr == nil {
		s.d.Jobs.AppendLog(id, "status", fmt.Sprintf("recovered audio · %.1f MB",
			float64(audioInfo.Size())/(1024*1024)), nil)
	}
	s.d.Jobs.AppendLog(id, "status", "done", nil)

	if recovered := s.d.Jobs.Get(id); recovered != nil {
		return recovered
	}
	return j
}

func (s *Server) jobDownloadURL(ctx context.Context, j *Job) string {
	if j == nil || j.Status != JobDone || !s.d.Uploader.Enabled() {
		return ""
	}
	key := j.S3Key
	if j.AudioOnly {
		key = j.AudioS3Key
	}
	if key == "" {
		return ""
	}
	url, err := s.d.Uploader.DownloadURL(ctx, key, time.Hour)
	if err != nil {
		s.logger().Warn("job download url failed", "job", j.ID, "key", key, "err", err)
		return ""
	}
	return url
}

func (s *Server) jobArtifactDir(id string) string {
	for _, dir := range s.jobArtifactDirs(id) {
		if _, err := os.Stat(filepath.Join(dir, "video.mp4")); err == nil {
			return dir
		}
		if _, err := os.Stat(podcastAudioPath(dir)); err == nil {
			return dir
		}
		if _, err := os.Stat(legacyPodcastAudioPath(dir)); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "archive.zip")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "session.db")); err == nil {
			return dir
		}
		if _, err := os.Stat(podcastSubtitlesPath(dir)); err == nil {
			return dir
		}
		if _, err := os.Stat(legacyPodcastSubtitlesPath(dir)); err == nil {
			return dir
		}
	}
	return ""
}

func (s *Server) jobArtifactDirs(id string) []string {
	if id == "" {
		return nil
	}
	seen := map[string]bool{}
	add := func(dir string, out *[]string) {
		if dir == "" {
			return
		}
		clean := filepath.Clean(dir)
		if seen[clean] {
			return
		}
		seen[clean] = true
		*out = append(*out, clean)
	}

	var out []string
	if s.d.UploadRoot != "" {
		add(filepath.Join(filepath.Dir(s.d.UploadRoot), "jobs", id), &out)
	}
	if s.d.Env != nil {
		if s.d.Env.OutDir != "" {
			add(filepath.Join(s.d.Env.OutDir, "jobs", id), &out)
		}
		if s.d.Env.PersistentRoot != "" {
			add(filepath.Join(s.d.Env.PersistentRoot, "jobs", id), &out)
			matches, _ := filepath.Glob(filepath.Join(s.d.Env.PersistentRoot, "session-*", "jobs", id))
			for _, match := range matches {
				add(match, &out)
			}
		}
	}
	return out
}

func formBool(r *http.Request, name string) bool {
	v := strings.ToLower(strings.TrimSpace(r.FormValue(name)))
	return v == "true" || v == "1" || v == "on" || v == "yes"
}

func formValues(r *http.Request, name string) []string {
	if r.MultipartForm == nil || r.MultipartForm.Value == nil {
		return nil
	}
	raw := r.MultipartForm.Value[name]
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// newJobID returns a 16-hex-char random id. Collisions are not an
// in-process concern at this rate.
func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// jobDownloadStem produces a human-friendly filename stem for the
// browser's "Save as" dialog. Falls back to the id when no nicer
// metadata is available.
func jobDownloadStem(j *Job) string {
	if j.Show != "" && j.Season > 0 && j.Episode > 0 {
		return fmt.Sprintf("%s-s%02de%02d", slugify(j.Show), j.Season, j.Episode)
	}
	if j.Title != "" {
		return slugify(j.Title)
	}
	return j.ID
}

// slugify is a small filename-safe normaliser. Mirrors the cmd-side
// slugify but kept package-local so server doesn't depend on cmd/.
func slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '_' || r == '-':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "job"
	}
	return out
}
