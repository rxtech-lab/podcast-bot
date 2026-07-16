package videojob

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/subtitleutil"
	"github.com/sirily11/debate-bot/internal/video"
)

// uploadedAudioFetchTTL bounds the presigned GET used to stage the user's
// original upload onto the consuming pod.
const uploadedAudioFetchTTL = time.Hour

// runUploadedAudio publishes an uploaded-audio podcast: no orchestrator, no
// TTS. The user's original audio becomes the job's audio artifact, the plan's
// transcript segments become the WebVTT captions and the discussion
// transcript, and the usual post-generation summary/mindmap kick off from the
// same lines.
func runUploadedAudio(ctx context.Context, deps Deps, jobID string,
	topic *config.DebateTopic,
) error {
	logger := deps.Log.With("job", jobID, "type", topic.Type, "title", topic.Title)
	deps.Jobs.Update(jobID, func(j *server.Job) { j.Status = server.JobRunning })
	status := func(text string) {
		stamped := contentcreator.StampChannelID(contentcreator.StatusMsg{Text: text}, jobID)
		persistEvent(deps.Jobs, jobID, stamped)
		deps.Bus.Publish(stamped)
	}
	status("publishing uploaded audio (no synthesis)…")

	if deps.Uploader == nil || !deps.Uploader.Enabled() {
		return mq.Permanent(fmt.Errorf("uploaded-audio publish requires S3 storage"))
	}
	jobOutDir := filepath.Join(deps.Env.OutDir, "jobs", jobID)
	if err := os.MkdirAll(jobOutDir, 0o755); err != nil {
		return fmt.Errorf("create job dir: %w", err)
	}

	// Stage the original upload locally (streamed, never buffered in memory)
	// so ffprobe can read it and the artifact copy uploads from disk.
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(topic.UploadedAudioKey)), ".")
	if ext == "" {
		ext = "mp3"
	}
	localAudio := filepath.Join(jobOutDir, "uploaded-audio."+ext)
	if err := stageUploadedAudio(ctx, deps, topic.UploadedAudioKey, localAudio); err != nil {
		return err
	}
	defer os.Remove(localAudio)

	// The probed container duration is authoritative; the transcription-time
	// estimate is the fallback when ffprobe is unavailable.
	durationSecs := float64(topic.UploadedAudioDurationMS) / 1000
	if probed, err := video.ProbeDurationSeconds(localAudio); err == nil && probed > 0 {
		durationSecs = probed
	} else if err != nil {
		logger.Warn("probe uploaded audio duration failed; using transcript duration", "err", err)
	}
	status(fmt.Sprintf("audio staged · %s", formatJobClock(durationSecs)))

	// Captions: the plan's sentence-level segments, stripped of punctuation to
	// match the app's caption style.
	cues := uploadedAudioSubtitleCues(topic.TranscriptSegments)
	subtitlesPath := filepath.Join(jobOutDir, server.PodcastSubtitlesFilename)
	if err := contentcreator.WriteSubtitleCues(subtitlesPath, cues); err != nil {
		logger.Warn("write subtitles sidecar failed", "err", err)
	}

	// Copy the audio into the job artifact key so serving, lifecycle, and
	// sharing behave exactly like a generated podcast (the original upload
	// under uploads/ stays untouched).
	status("uploading to S3...")
	audioKey := deps.Uploader.Key(podcastAudioObjectName(jobID, ext))
	if err := deps.Uploader.Upload(ctx, localAudio, audioKey); err != nil {
		return fmt.Errorf("s3 audio upload: %w", err)
	}
	downloadURL := ""
	if url, err := deps.Uploader.DownloadURL(ctx, audioKey, time.Hour); err == nil {
		downloadURL = url
	} else {
		logger.Warn("s3 audio download url failed", "key", audioKey, "err", err)
	}
	var subtitlesS3Key string
	if info, statErr := os.Stat(subtitlesPath); statErr == nil && info.Size() > 0 {
		key := deps.Uploader.Key(podcastAudioObjectName(jobID, "vtt"))
		if err := deps.Uploader.Upload(ctx, subtitlesPath, key); err != nil {
			logger.Warn("s3 subtitles upload failed", "key", key, "err", err)
		} else {
			subtitlesS3Key = key
		}
	}
	status("uploaded to S3")

	// Transcript lines: merge consecutive same-speaker segments into one chat
	// line each so the transcript reads as turns, not caption fragments.
	lines := uploadedAudioTranscriptLines(topic.TranscriptSegments)
	if deps.Discussions != nil && deps.DiscussionID != "" {
		if err := deps.Discussions.ReplaceTranscript(ctx, deps.DiscussionID, lines); err != nil {
			logger.Warn("uploaded-audio transcript persist failed", "err", err)
		}
		if err := deps.Discussions.SetDurationSeconds(ctx, deps.DiscussionID, durationSecs); err != nil {
			logger.Warn("uploaded-audio duration persist failed", "err", err)
		}
	}

	// Settle the (flat) generation reservation. There is no synthesis cost;
	// transcription was billed separately at upload time.
	if deps.Points != nil && deps.DiscussionID != "" {
		if err := deps.Points.ChargeGeneration(ctx, deps.Env, deps.DiscussionID, server.PointsUsageDetail{}); err != nil {
			logger.Warn("uploaded-audio generation settle failed", "err", err)
		}
	}

	startSummaryGeneration(deps, jobID, topic, lines)
	startMindmapGeneration(deps, jobID, topic, lines)

	status("done")
	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Status = server.JobDone
		j.HasAudio = true
		j.AudioOnly = true
		j.AudioS3Key = audioKey
		j.SubtitlesS3Key = subtitlesS3Key
		j.DownloadURL = downloadURL
	})
	persistDiscussionResult(ctx, deps, server.DiscussionReady, downloadURL)
	server.PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, deps.DiscussionID, "Podcast ready", "podcast", "audio", "status")
	notifyPodcastReady(ctx, deps, logger)
	logger.Info("uploaded-audio publish finished",
		"segments", len(topic.TranscriptSegments),
		"lines", len(lines),
		"duration_secs", durationSecs,
	)
	return nil
}

// stageUploadedAudio streams the object behind key to localPath.
func stageUploadedAudio(ctx context.Context, deps Deps, key, localPath string) error {
	url, err := deps.Uploader.PresignGet(ctx, key, uploadedAudioFetchTTL)
	if err != nil || url == "" {
		return fmt.Errorf("presign uploaded audio: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch uploaded audio: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch uploaded audio: status %d", resp.StatusCode)
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	written, err := io.Copy(out, resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("stage uploaded audio: %w", err)
	}
	if written == 0 {
		return mq.Permanent(fmt.Errorf("uploaded audio object is empty"))
	}
	return nil
}

// uploadedAudioSubtitleCues converts transcript segments into VTT cues,
// applying the same punctuation strip the live pipeline uses so captions read
// identically across generated and uploaded podcasts.
func uploadedAudioSubtitleCues(segments []config.TranscriptSegment) []contentcreator.SubtitleCue {
	cues := make([]contentcreator.SubtitleCue, 0, len(segments))
	for _, seg := range segments {
		text := subtitleutil.StripPunct(seg.Text)
		if text == "" || seg.DurationMS <= 0 {
			continue
		}
		start := time.Duration(seg.OffsetMS) * time.Millisecond
		cues = append(cues, contentcreator.SubtitleCue{
			Start: start,
			End:   start + time.Duration(seg.DurationMS)*time.Millisecond,
			Text:  text,
		})
	}
	return contentcreator.ClampSubtitleCueOverlaps(cues)
}

// uploadedAudioTranscriptLines merges consecutive same-speaker segments into
// one transcript line per speaker turn, stamped with the turn's audio offset.
// Line At times are synthesized from a common base plus the audio offset so
// created_at ordering matches playback order.
func uploadedAudioTranscriptLines(segments []config.TranscriptSegment) []agent.TranscriptLine {
	base := time.Now()
	var lines []agent.TranscriptLine
	for _, seg := range segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		if n := len(lines); n > 0 && lines[n-1].Speaker == seg.Speaker {
			lines[n-1].Text = joinTranscriptText(lines[n-1].Text, text)
			continue
		}
		lines = append(lines, agent.TranscriptLine{
			Speaker:       seg.Speaker,
			Role:          agent.RoleDiscussant,
			Text:          text,
			At:            base.Add(time.Duration(seg.OffsetMS) * time.Millisecond),
			AudioOffsetMS: seg.OffsetMS,
		})
	}
	return lines
}

// joinTranscriptText concatenates two transcript pieces, inserting a space
// only between Latin-script boundaries (CJK text joins directly).
func joinTranscriptText(a, b string) string {
	if a == "" {
		return b
	}
	last := rune(0)
	for _, r := range a {
		last = r
	}
	first := rune(0)
	for _, r := range b {
		first = r
		break
	}
	if isCJKRune(last) || isCJKRune(first) {
		return a + b
	}
	return a + " " + b
}

func isCJKRune(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x3000 && r <= 0x303F,
		r >= 0xFF00 && r <= 0xFFEF,
		r >= 0x3040 && r <= 0x30FF,
		r >= 0xAC00 && r <= 0xD7AF:
		return true
	}
	return false
}

// formatJobClock renders seconds as M:SS or H:MM:SS for status lines.
func formatJobClock(seconds float64) string {
	total := int64(seconds)
	h, m, sec := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}
