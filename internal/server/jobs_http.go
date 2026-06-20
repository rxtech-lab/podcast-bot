package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// jobScriptName / jobPriorsName are the filenames the handler saves
// uploads under, inside <UploadRoot>/<jobID>/. The runner reads them
// back at the same paths.
const (
	jobScriptName = "script.md"
	jobPriorsName = "priors.zip"
)

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
	Text     string `json:"text"`
	Username string `json:"username"`
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
	orch.PushUserMessage(req.Text, username)
	w.WriteHeader(http.StatusNoContent)
}

// handleJobList returns every job currently tracked by the registry.
// Useful for debugging; the SPA reads its own job by id.
func (s *Server) handleJobList(w http.ResponseWriter, _ *http.Request) {
	if s.d.Jobs == nil {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.d.Jobs.List())
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
	// When the finished video lives in S3, hand the client a fresh presigned
	// download link directly on the job snapshot so the dashboard can show a
	// download button without a redirect round-trip.
	if j.Status == JobDone && j.S3Key != "" && s.d.Uploader.Enabled() {
		if url, err := s.d.Uploader.PresignGet(r.Context(), j.S3Key, time.Hour); err == nil {
			j.DownloadURL = url
		}
	}
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
	// Prefer object storage: mint a fresh presigned URL each request (never
	// persisted, so it can't go stale) and redirect the browser to it.
	if j.S3Key != "" && s.d.Uploader.Enabled() {
		url, err := s.d.Uploader.PresignGet(r.Context(), j.S3Key, time.Hour)
		if err != nil {
			http.Error(w, "presign failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
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

func (s *Server) recoverJob(id string) *Job {
	if s.d.Jobs == nil || s.d.UploadRoot == "" {
		return nil
	}
	jobOutDir := filepath.Join(filepath.Dir(s.d.UploadRoot), "jobs", id)
	mp4Path := filepath.Join(jobOutDir, "video.mp4")
	archivePath := filepath.Join(jobOutDir, "archive.zip")

	mp4Info, mp4Err := os.Stat(mp4Path)
	archiveInfo, archiveErr := os.Stat(archivePath)
	if mp4Err != nil && archiveErr != nil {
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
		if topic, err := config.LoadTopic(filepath.Join(s.d.UploadRoot, id, jobScriptName)); err == nil {
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
	s.d.Jobs.AppendLog(id, "status", "done", nil)

	if recovered := s.d.Jobs.Get(id); recovered != nil {
		return recovered
	}
	return j
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
