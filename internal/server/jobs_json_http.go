package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sirily11/debate-bot/internal/config"
)

// jobSubmitJSONRequest is the body of POST /api/jobs/json — a structured
// DebateTopic plus the same video-config knobs the multipart form carries.
// The dashboard sends this instead of building a .md client-side; the handler
// renders the topic to script.md and reuses the existing job pipeline.
type jobSubmitJSONRequest struct {
	Script      *config.DebateTopic `json:"script"`
	VideoConfig videoConfigJSON     `json:"videoConfig"`
}

type videoConfigJSON struct {
	SoftSubs          bool     `json:"soft_subs"`
	BurnSubs          bool     `json:"burn_subs"`
	Resolution        string   `json:"resolution"`
	SubtitleLanguages []string `json:"subtitle_languages"`
	AudioOnly         bool     `json:"audio_only"`
}

// handleJobSubmitJSON accepts a JSON DebateTopic, validates + renders it to a
// script.md under <UploadRoot>/<jobID>/, and submits it through the same path
// the multipart upload uses.
func (s *Server) handleJobSubmitJSON(w http.ResponseWriter, r *http.Request) {
	if s.d.SubmitJob == nil || s.d.Jobs == nil || s.d.UploadRoot == "" {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req jobSubmitJSONRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Script == nil {
		http.Error(w, "missing 'script'", http.StatusBadRequest)
		return
	}

	// Apply the same defaults LoadTopic would, then validate, so a topic
	// built in the dashboard is held to the identical contract as an upload.
	applyTopicDefaults(req.Script)
	if err := config.ValidateTopic(req.Script); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	md, err := req.Script.RenderMarkdown()
	if err != nil {
		http.Error(w, "render script: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jobID := newJobID()
	jobDir := filepath.Join(s.d.UploadRoot, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		http.Error(w, "create job dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	scriptPath := filepath.Join(jobDir, jobScriptName)
	if err := os.WriteFile(scriptPath, []byte(md), 0o644); err != nil {
		http.Error(w, "save script: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sub := JobSubmission{
		ScriptPath:        scriptPath,
		SoftSubs:          req.VideoConfig.SoftSubs,
		BurnSubs:          req.VideoConfig.BurnSubs,
		Resolution:        req.VideoConfig.Resolution,
		SubtitleLanguages: req.VideoConfig.SubtitleLanguages,
		AudioOnly:         req.VideoConfig.AudioOnly,
	}
	if err := s.submitStaged(jobID, sub); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": jobID})
}

// applyTopicDefaults fills the same zero-value defaults LoadTopic applies after
// parsing, so an in-memory topic validates and renders identically to a file.
func applyTopicDefaults(t *config.DebateTopic) {
	if t.Language == "" {
		t.Language = "en-US"
	}
	if t.SegmentMaxSeconds == 0 {
		t.SegmentMaxSeconds = 60
	}
	if t.TotalMinutes == 0 {
		t.TotalMinutes = 30
	}
	if t.TTSProvider == "" {
		t.TTSProvider = config.TTSProviderAzure
	}
	if t.Resolution == "" {
		t.Resolution = config.Resolution1080p
	}
	if t.Type == config.ContentTypeDiscussion && t.Storage == "" {
		t.Storage = config.StoragePlaintext
	}
}
