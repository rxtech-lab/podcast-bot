package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
)

type captionDownloadFormatDTO struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	FileExtension string `json:"file_extension"`
	ContentType   string `json:"content_type"`
}

type captionDownloadFormatsResponse struct {
	Formats []captionDownloadFormatDTO `json:"formats"`
}

type captionDownloadFormat struct {
	descriptor captionDownloadFormatDTO
	render     func([]byte) ([]byte, error)
}

// captionDownloadFormats is the single backend-owned format catalog. The iOS
// sheet renders this response generically, so adding another renderer here is
// enough to expose a future caption format without an app update.
var captionDownloadFormats = []captionDownloadFormat{
	{
		descriptor: captionDownloadFormatDTO{
			ID:            "vtt",
			DisplayName:   "WebVTT",
			FileExtension: "vtt",
			ContentType:   "text/vtt; charset=utf-8",
		},
		render: func(vtt []byte) ([]byte, error) { return vtt, nil },
	},
	{
		descriptor: captionDownloadFormatDTO{
			ID:            "srt",
			DisplayName:   "SubRip",
			FileExtension: "srt",
			ContentType:   "application/x-subrip; charset=utf-8",
		},
		render: webVTTToSRT,
	},
}

func (s *Server) handleCaptionDownloadFormats(w http.ResponseWriter, _ *http.Request) {
	formats := make([]captionDownloadFormatDTO, len(captionDownloadFormats))
	for i, format := range captionDownloadFormats {
		formats[i] = format.descriptor
	}
	writeJSON(w, captionDownloadFormatsResponse{Formats: formats})
}

func captionDownloadFormatFor(id string) (captionDownloadFormat, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, format := range captionDownloadFormats {
		if format.descriptor.ID == id {
			return format, true
		}
	}
	return captionDownloadFormat{}, false
}

func (s *Server) handleJobCaptionDownload(w http.ResponseWriter, r *http.Request) {
	if s.d.Jobs == nil || s.d.UploadRoot == "" {
		http.Error(w, "video mode not configured", http.StatusInternalServerError)
		return
	}
	format, ok := captionDownloadFormatFor(r.PathValue("format"))
	if !ok {
		http.Error(w, "unsupported caption format", http.StatusNotFound)
		return
	}

	id := r.PathValue("id")
	serve := func(vtt []byte) {
		body, err := format.render(vtt)
		if err != nil {
			s.logger().Error("caption format conversion failed", "job", id, "format", format.descriptor.ID, "err", err)
			http.Error(w, "caption conversion failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", format.descriptor.ContentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+"."+format.descriptor.FileExtension))
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}

	// A ready translation serves the download even when the job row is gone
	// (mirrors handleJobSubtitles/handleJobSubtitlesLive, which are keyed to
	// the discussion, not the registry); the job gates only the original VTT.
	if captions, ok := s.translatedCaptions(r.Context(), id, r.URL.Query().Get("language")); ok {
		serve([]byte(captions))
		return
	}

	job := s.d.Jobs.Get(id)
	if job == nil {
		job = s.recoverJob(id)
		if job == nil {
			http.NotFound(w, r)
			return
		}
	}
	if job.Status != JobDone {
		http.Error(w, "captions not ready", http.StatusTooEarly)
		return
	}

	vtt, err := s.loadJobCaptionVTT(r.Context(), job, r.URL.Query().Get("language"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.logger().Error("caption download source failed", "job", id, "format", format.descriptor.ID, "err", err)
		http.Error(w, "caption download failed", http.StatusInternalServerError)
		return
	}
	serve(vtt)
}

func (s *Server) loadJobCaptionVTT(ctx context.Context, job *Job, language string) ([]byte, error) {
	if captions, ok := s.translatedCaptions(ctx, job.ID, language); ok {
		return []byte(captions), nil
	}
	if captions, ok := s.uploadedAudioCaptionVTT(ctx, job.ID); ok {
		return []byte(captions), nil
	}

	if s.d.Uploader != nil && s.d.Uploader.Enabled() {
		key := strings.TrimSpace(job.SubtitlesS3Key)
		if key == "" {
			for _, name := range []string{
				path.Join(PodcastAudioDir, job.ID+".vtt"),
				job.ID + ".vtt",
			} {
				candidate := s.d.Uploader.Key(name)
				if info, err := s.d.Uploader.Head(ctx, candidate); err == nil && info.ContentLength > 0 {
					key = candidate
					break
				}
			}
		}
		if key != "" {
			data, err := s.d.Uploader.Download(ctx, key)
			if err != nil {
				return nil, err
			}
			if len(data) > 0 {
				return data, nil
			}
		}
	}

	jobDir := s.jobArtifactDir(job.ID)
	if jobDir == "" {
		return nil, os.ErrNotExist
	}
	subtitlesPath := firstExistingNonEmpty(
		podcastSubtitlesPath(jobDir),
		legacyPodcastSubtitlesPath(jobDir),
	)
	if subtitlesPath == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(subtitlesPath)
}

func webVTTToSRT(vtt []byte) ([]byte, error) {
	text := strings.TrimPrefix(string(vtt), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	blocks := strings.Split(text, "\n\n")

	var out strings.Builder
	cueNumber := 0
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		timingLine := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timingLine = i
				break
			}
		}
		if timingLine < 0 || timingLine+1 >= len(lines) {
			continue
		}
		parts := strings.SplitN(lines[timingLine], "-->", 2)
		if len(parts) != 2 {
			continue
		}
		start, startOK := srtTimestamp(parts[0])
		end, endOK := srtTimestamp(parts[1])
		if !startOK || !endOK {
			continue
		}
		caption := strings.TrimSpace(strings.Join(lines[timingLine+1:], "\r\n"))
		if caption == "" {
			continue
		}

		cueNumber++
		fmt.Fprintf(&out, "%d\r\n%s --> %s\r\n%s\r\n\r\n", cueNumber, start, end, caption)
	}
	if cueNumber == 0 && strings.Contains(text, "-->") {
		return nil, errors.New("no valid WebVTT cues")
	}
	return []byte(out.String()), nil
}

func srtTimestamp(value string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return "", false
	}
	timestamp := strings.Replace(fields[0], ",", ".", 1)
	parts := strings.Split(timestamp, ":")
	if len(parts) == 2 {
		parts = append([]string{"00"}, parts...)
	}
	if len(parts) != 3 || !strings.Contains(parts[2], ".") {
		return "", false
	}
	timestamp = strings.Join(parts, ":")
	dot := strings.LastIndexByte(timestamp, '.')
	if dot < 0 {
		return "", false
	}
	return timestamp[:dot] + "," + timestamp[dot+1:], true
}
