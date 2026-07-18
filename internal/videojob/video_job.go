// Package videojob runs the upload-and-render flow for the modeVideo
// HTTP server: validate the user-supplied script.md, build a per-job
// orchestrator + encoder, run the pipeline, then stitch the resulting
// HLS into a downloadable .mp4 (and zip the persistent series archive).
//
// Lives in its own package — between cmd/debate-bot and content_creator
// — to break what would otherwise be an import cycle. content_creator
// already exports the orchestrator + series helpers; server holds the
// JobRegistry the HTTP layer reads. videojob is the glue that consumes
// both.
package videojob

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/audiobook"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/discussion"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/series"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/storage"
	"github.com/sirily11/debate-bot/internal/video"
)

// PodcastGeneratePayload is the wire payload of a queued generation run. It
// carries content (the script markdown, an S3 key for the priors zip)
// rather than local paths, because with a broker the consuming pod may not
// be the pod that accepted the upload.
type PodcastGeneratePayload struct {
	JobID          string `json:"job_id"`
	ScriptMarkdown string `json:"script_markdown"`
	// PriorsZipS3Key locates the staged series priors zip in object storage;
	// PriorsZipPath is the local-path fallback when S3 is not configured
	// (single-pod / dev, where publisher and consumer share a disk).
	PriorsZipS3Key    string   `json:"priors_zip_s3_key,omitempty"`
	PriorsZipPath     string   `json:"priors_zip_path,omitempty"`
	SoftSubs          bool     `json:"soft_subs,omitempty"`
	BurnSubs          bool     `json:"burn_subs,omitempty"`
	AudioOnly         bool     `json:"audio_only,omitempty"`
	Resolution        string   `json:"resolution,omitempty"`
	SubtitleLanguages []string `json:"subtitle_languages,omitempty"`
	DiscussionID      string   `json:"discussion_id,omitempty"`
}

// priorsZipObjectName is where Submit stages the series priors zip so any
// consuming pod can download it.
func priorsZipObjectName(jobID string) string {
	return path.Join("job-uploads", jobID, "priors.zip")
}

// recorderDrainTimeout bounds the wait for the audio.mp3 recorder to see the
// LiveStream close after the pipeline finishes. The `-re` pacer plus the
// session mixer can hold well over 30s of tail audio on long runs; a timeout
// here means the recorded file is truncated and every consumer downstream
// (S3 upload, video render, duration probe) ships a short artifact, so match
// the pipeline's cleanup hard cap rather than cutting early.
const recorderDrainTimeout = 90 * time.Second

// logSubtitleAudioSync compares the persisted subtitle timeline against the
// actual (ffprobe-measured) duration of the recorded audio and flags suspects.
// Purely observational — it never fails the job — so per-job logs surface
// caption/audio drift regressions that would otherwise ship silently.
func logSubtitleAudioSync(logger *slog.Logger, orch *contentcreator.Orchestrator, audioPath string) {
	if orch == nil {
		return
	}
	cues := orch.SubtitleCues()
	if len(cues) == 0 {
		return
	}
	audioSecs, err := video.ProbeDurationSeconds(audioPath)
	if err != nil {
		logger.Warn("subtitle/audio sync check skipped — probe failed", "path", audioPath, "err", err)
		return
	}
	first, last := cues[0], cues[len(cues)-1]
	logger.Info("subtitle/audio sync",
		"cues", len(cues),
		"first_cue_start_s", first.Start.Seconds(),
		"last_cue_end_s", last.End.Seconds(),
		"audio_duration_s", audioSecs)
	const overrunTolerance = 500 * time.Millisecond
	if overrun := last.End - time.Duration(audioSecs*float64(time.Second)); overrun > overrunTolerance {
		logger.Warn("subtitle/audio sync suspect — cues overrun the recorded audio",
			"overrun_s", overrun.Seconds())
	}
	if first.Start < 0 {
		logger.Warn("subtitle/audio sync suspect — negative first cue", "start_s", first.Start.Seconds())
	}
}

// waitForStableFile blocks until path's size stops changing for quiet (or max
// elapses / ctx is done). Guards downstream readers against a recorder that
// outlived its drain timeout and is still appending.
func waitForStableFile(ctx context.Context, logger *slog.Logger, path string, quiet, max time.Duration) {
	const poll = 500 * time.Millisecond
	deadline := time.Now().Add(max)
	lastSize := int64(-1)
	stableSince := time.Now()
	for {
		if info, err := os.Stat(path); err == nil {
			if info.Size() != lastSize {
				lastSize = info.Size()
				stableSince = time.Now()
			} else if time.Since(stableSince) >= quiet {
				return
			}
		}
		if time.Now().After(deadline) {
			logger.Warn("file did not stabilise before deadline — proceeding",
				"path", path, "size", lastSize, "max", max)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// Deps wires the runner to long-lived process state. Env is the
// LoadEnv-produced config (its OutDir is the session root, not the
// per-job dir — the runner appends jobs/<id>/). MCPCfg is forwarded
// to each per-job orchestrator; today most uploads run with empty mcp
// configs but the seam is here for future tools.
type Deps struct {
	Env         *config.Env
	MCPCfg      *config.MCPConfig
	Bus         *eventbus.Bus
	Jobs        *server.JobRegistry
	Discussions *server.DiscussionStore
	// Points, when set, reconciles the generation reservation against actual
	// usage at job completion so a finished podcast is charged immediately (not
	// lazily on a later discussion fetch). nil disables points charging.
	Points *server.PointsStore
	APNS   *server.APNSClient
	// MQ is the durable generation-job queue Submit publishes to; consumers
	// call RunFromTask.
	MQ           mq.Client
	Log          *slog.Logger
	DiscussionID string
	// Uploader, when enabled, pushes the finished mp4 to S3 after stitching.
	// nil / disabled leaves the video on local disk.
	Uploader *storage.Uploader
}

// Submit validates the request synchronously and enqueues the run.
// Returns nil on accept; returns an error when the upload
// is malformed (bad frontmatter, subtitle flag on non-series, etc.)
// so the HTTP layer can surface the reason.
//
// Validation runs upfront because:
//   - the SPA shows the error inline rather than after a long wait;
//   - the JobRegistry entry stays in JobError state with a descriptive
//     message, so a user retrying through the UI gets feedback fast.
//
// The actual heavy work (asset gen, ffmpeg, zip) runs through the
// process-wide goqueue worker pool so video mode cannot start
// unbounded parallel encoders.
func Submit(ctx context.Context, deps Deps, jobID string, sub server.JobSubmission) error {
	deps.DiscussionID = sub.DiscussionID
	if deps.MQ == nil {
		return errors.New("video job queue is not configured")
	}
	topic, err := config.LoadTopic(sub.ScriptPath)
	if err != nil {
		return fmt.Errorf("script.md: %w", err)
	}

	// Soft subs + translated tracks work for any type whose stage
	// emits a subtitles.vtt sidecar. Burn-in
	// captions are painted only by the series renderer (discussion
	// already burns its active-speaker caption natively), and the
	// priors zip is a series-only continuity feature. Reject mismatched
	// flags early with a clear message rather than silently ignoring
	// them.
	supportsSoftSubs := supportsSoftSubtitles(topic.Type)
	if !supportsSoftSubs {
		if sub.SoftSubs || len(sub.SubtitleLanguages) > 0 {
			return errors.New("subtitle options (soft_subs) are only valid for type=series, type=discussion, type=audio-book, or type=uploaded-audio")
		}
	}
	if topic.Type != config.ContentTypeSeries {
		if sub.BurnSubs {
			return errors.New("burn-in subtitles (burn_subs) are only valid for type=series")
		}
		if sub.PriorsZipPath != "" {
			return errors.New("priors zip is only valid for type=series")
		}
	}
	if topic.Type == config.ContentTypeAudioBook || topic.Type == config.ContentTypeUploadedAudio {
		sub.AudioOnly = true
	}
	if len(sub.SubtitleLanguages) > 0 && !sub.SoftSubs {
		return errors.New("translated subtitle languages require soft_subs=true")
	}
	if _, err := normalizeRequestedSubtitleLanguages(topic.Language, sub.SubtitleLanguages); err != nil {
		return err
	}

	// Puzzle uploads are not supported in video mode yet — the puzzle
	// prep code lives in cmd/debate-bot/puzzle.go and isn't yet
	// shared with this package. Debate + series cover the requested
	// feature.
	if topic.Type == config.ContentTypeSituationPuzzle {
		return errors.New("type=situation-puzzle is not supported in video mode yet")
	}

	// Audio-only produces no frames, so burn-in captions are meaningless.
	// Soft subs are still surfaced as the subtitles.vtt sidecar regardless of
	// the flag, so we don't require it here.
	if sub.AudioOnly && sub.BurnSubs {
		return errors.New("burn-in subtitles (burn_subs) are not applicable to audio-only feeds")
	}

	// Resolution override (UI form). Empty means "use the topic's
	// declared resolution" — keeps backward compatibility for users
	// who don't set the field. We deliberately do NOT expose 4K from
	// the UI; only the validated subset 720p/1080p is accepted here.
	switch sub.Resolution {
	case "", config.Resolution720p, config.Resolution1080p:
	default:
		return fmt.Errorf("resolution must be 720p or 1080p (got %q)", sub.Resolution)
	}
	if sub.Resolution != "" {
		topic.Resolution = sub.Resolution
	}

	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Title = topic.Title
		j.Type = topic.Type
		j.Show = topic.Show
		j.Season = topic.Season
		j.Episode = topic.Episode
	})
	deps.Jobs.AppendLog(jobID, "status",
		fmt.Sprintf("job queued · type=%s · resolution=%s", topic.Type, topic.Resolution), nil)

	// The payload carries the script's content and durable references, never
	// this pod's local paths: with a broker the run may be consumed by a
	// different pod than the one that staged the upload.
	scriptMarkdown, err := os.ReadFile(sub.ScriptPath)
	if err != nil {
		return fmt.Errorf("read script.md: %w", err)
	}
	payload := PodcastGeneratePayload{
		JobID:             jobID,
		ScriptMarkdown:    string(scriptMarkdown),
		SoftSubs:          sub.SoftSubs,
		BurnSubs:          sub.BurnSubs,
		AudioOnly:         sub.AudioOnly,
		Resolution:        sub.Resolution,
		SubtitleLanguages: sub.SubtitleLanguages,
		DiscussionID:      sub.DiscussionID,
	}
	if sub.PriorsZipPath != "" {
		if deps.Uploader.Enabled() {
			key := deps.Uploader.Key(priorsZipObjectName(jobID))
			if err := deps.Uploader.Upload(ctx, sub.PriorsZipPath, key); err != nil {
				return fmt.Errorf("stage priors zip to S3: %w", err)
			}
			payload.PriorsZipS3Key = key
		} else {
			payload.PriorsZipPath = sub.PriorsZipPath
		}
	}
	task, err := mq.NewTask(mq.TaskPodcastGenerate, jobID, payload)
	if err != nil {
		return fmt.Errorf("encode generation task: %w", err)
	}
	if err := deps.MQ.Publish(ctx, mq.QueueGeneration, task); err != nil {
		return fmt.Errorf("enqueue generation task: %w", err)
	}
	return nil
}

func supportsSoftSubtitles(contentType string) bool {
	switch contentType {
	case config.ContentTypeSeries,
		config.ContentTypeDiscussion,
		config.ContentTypeAudioBook,
		config.ContentTypeUploadedAudio,
		config.ContentTypeNews:
		return true
	default:
		return false
	}
}

// RunFromTask executes one queued generation attempt on the consuming pod:
// it materialises the payload back into local inputs (script.md, priors
// zip), re-derives the validated topic, and runs the render pipeline. The
// returned error is the attempt's failure (the dispatch layer decides
// retry vs terminal); nil means the job completed and was marked done.
func RunFromTask(ctx context.Context, deps Deps, p PodcastGeneratePayload) error {
	deps.DiscussionID = p.DiscussionID
	jobID := p.JobID
	jobOutDir := filepath.Join(deps.Env.OutDir, "jobs", jobID)
	if err := os.MkdirAll(jobOutDir, 0o755); err != nil {
		return fmt.Errorf("create job dir: %w", err)
	}
	scriptPath := filepath.Join(jobOutDir, "script.md")
	if err := os.WriteFile(scriptPath, []byte(p.ScriptMarkdown), 0o644); err != nil {
		return fmt.Errorf("stage script.md: %w", err)
	}
	topic, err := config.LoadTopic(scriptPath)
	if err != nil {
		// Submit already validated this script; a parse failure here is a
		// content problem no retry will fix.
		return mq.Permanent(fmt.Errorf("script.md: %w", err))
	}
	if p.Resolution != "" {
		topic.Resolution = p.Resolution
	}
	sub := server.JobSubmission{
		ScriptPath:        scriptPath,
		SoftSubs:          p.SoftSubs,
		BurnSubs:          p.BurnSubs,
		Resolution:        p.Resolution,
		SubtitleLanguages: p.SubtitleLanguages,
		AudioOnly:         p.AudioOnly || topic.Type == config.ContentTypeAudioBook || topic.Type == config.ContentTypeUploadedAudio,
		DiscussionID:      p.DiscussionID,
	}
	if p.PriorsZipS3Key != "" {
		data, err := deps.Uploader.Download(ctx, p.PriorsZipS3Key)
		if err != nil {
			return fmt.Errorf("download priors zip: %w", err)
		}
		local := filepath.Join(jobOutDir, "priors.zip")
		if err := os.WriteFile(local, data, 0o644); err != nil {
			return fmt.Errorf("stage priors zip: %w", err)
		}
		sub.PriorsZipPath = local
	} else if p.PriorsZipPath != "" {
		sub.PriorsZipPath = p.PriorsZipPath
	}
	return run(ctx, deps, jobID, sub, topic)
}

// MarkRetryScheduled records a failed non-final attempt: the job returns to
// pending (so the next attempt's claim succeeds) with a phase that tells
// clients a retry is coming. Deliberately does NOT touch the linked
// discussion — it only flips to failed on terminal failure.
func MarkRetryScheduled(deps Deps, jobID string, attempt int, delay time.Duration, cause error) {
	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Status = server.JobPending
		j.Phase = "retry-scheduled"
		j.PhaseLabel = fmt.Sprintf("Retrying (attempt %d/%d)", attempt+1, mq.MaxAttempts)
	})
	deps.Jobs.AppendLog(jobID, "status",
		fmt.Sprintf("attempt %d/%d failed: %v — retrying in %s", attempt, mq.MaxAttempts, cause, delay.Round(time.Second)), nil)
}

// FailTerminal marks the job errored after its final attempt and flips the
// linked discussion to failed (today's single-attempt failure behavior).
func FailTerminal(deps Deps, jobID string, err error) {
	deps.DiscussionID = discussionIDForJob(deps, jobID)
	fail(deps, jobID, deps.Log.With("job", jobID), err)
}

// discussionIDForJob resolves the linked discussion when deps.DiscussionID
// isn't already set (the terminal handler builds Deps outside a run).
func discussionIDForJob(deps Deps, jobID string) string {
	if deps.DiscussionID != "" {
		return deps.DiscussionID
	}
	if deps.Discussions == nil {
		return ""
	}
	if d, err := deps.Discussions.GetByJobID(context.Background(), jobID); err == nil && d != nil {
		return d.ID
	}
	return ""
}

// run is the long-running half of the submission. It assumes validation
// already passed. A non-nil return is the attempt's failure; the caller
// (jobworker dispatch) owns the retry-vs-terminal decision and the
// registry/discussion failure writes.
func run(ctx context.Context, deps Deps, jobID string,
	sub server.JobSubmission, topic *config.DebateTopic,
) error {
	// Uploaded-audio podcasts skip the whole synthesis pipeline: the audio
	// already exists, so publish is copy + captions + transcript + docs.
	if topic.Type == config.ContentTypeUploadedAudio {
		return runUploadedAudio(ctx, deps, jobID, topic)
	}
	logger := deps.Log.With("job", jobID, "type", topic.Type, "title", topic.Title)
	audioOnly := sub.AudioOnly
	// E2E mode always renders audio-only so the video encoder/stitch path (and its
	// image dependencies) never runs.
	if deps.Env.E2EMode {
		audioOnly = true
	}
	deps.Jobs.Update(jobID, func(j *server.Job) { j.Status = server.JobRunning })

	send := func(v any) {
		// Stamp jobID as channelID so existing SSE filtering /
		// envelope plumbing routes events to the right SPA client.
		stamped := contentcreator.StampChannelID(v, jobID)
		persistEvent(deps.Jobs, jobID, stamped)
		deps.Bus.Publish(stamped)
	}
	// status pushes a one-line progress note onto the SSE stream so
	// the SPA log shows job-runner milestones (priors extracted,
	// stitching, zipping, …) interleaved with orchestrator events.
	status := func(text string) {
		send(contentcreator.StatusMsg{Text: text})
	}
	status(fmt.Sprintf("job accepted · type=%s · resolution=%s", topic.Type, topic.Resolution))

	jobOutDir := filepath.Join(deps.Env.OutDir, "jobs", jobID)
	if err := os.MkdirAll(jobOutDir, 0o755); err != nil {
		return fmt.Errorf("create job dir: %w", err)
	}

	// Series-only: stage the optional priors zip into the persistent
	// archive root BEFORE PrepareEpisode walks SiblingEpisodeDirs.
	if topic.Type == config.ContentTypeSeries && sub.PriorsZipPath != "" {
		status("extracting priors zip…")
		if err := unzipPriors(sub.PriorsZipPath, deps.Env.PersistentRoot); err != nil {
			return fmt.Errorf("unzip priors: %w", err)
		}
		logger.Info("priors zip extracted",
			"src", sub.PriorsZipPath, "dst", deps.Env.PersistentRoot)
		status("priors archive ready")
	}

	// Per-job env: clone, override OutDir so all per-run artefacts
	// (debate.mp3, subtitles.vtt, transcript.txt, hls/) land in the
	// job's directory.
	jobEnv := *deps.Env
	jobEnv.OutDir = jobOutDir

	live, err := audio.NewLiveStream(ctx, logger)
	if err != nil {
		return fmt.Errorf("livestream: %w", err)
	}
	// Closed explicitly after orch.Run drains, BEFORE the stitch pass.
	// The encoder needs ffmpeg to finalise its HLS playlist (write
	// #EXT-X-ENDLIST + flush the last segment) before stitch reads
	// it; closing on function return via defer would let stitch race
	// with a still-running ffmpeg. The flag below tracks whether the
	// happy-path close already ran, so a panic / early return still
	// hits the safety-net defer.
	liveClosed := false
	encClosed := false
	defer func() {
		if !liveClosed {
			_ = live.CloseInput()
		}
	}()

	// Audio-only feeds skip the encoder, the render stages, and all image
	// generation entirely. The stage pointers stay nil; the asset-prep and
	// finalisation branches below switch on audioOnly to record the mixed
	// LiveStream audio instead of stitching HLS.
	var (
		enc             *video.Encoder
		seriesStage     *video.SeriesStage
		discussionStage *video.DiscussionStage
	)
	if !audioOnly {
		res := video.Resolution(topic.Resolution)
		enc, err = video.NewWithOptions(ctx, jobOutDir, res,
			// Archival mode disables the live HLS sliding window so every
			// segment survives long enough for the stitch pass to consume
			// it. Without this, episodes longer than ~12 s lose their
			// earliest segments to delete_segments before stitch runs.
			//
			// BurnInSeriesCaptions makes the renderer paint the spoken
			// sentence onto the scene as always-visible burned-in text.
			// Off by default — soft-sub clients toggle the .vtt sidecar
			// instead, leaving the imagery clean. The form's burn_subs
			// checkbox is the user-facing knob.
			video.Options{
				Archival:             true,
				BurnInSeriesCaptions: sub.BurnSubs,
			}, logger)
		if err != nil {
			return fmt.Errorf("encoder: %w", err)
		}
		defer func() {
			if !encClosed {
				_ = enc.Close()
			}
		}()
		enc.AttachAudio(ctx, live)

		// Spin up the per-content-type stage. Mirrors bootstrap's
		// "every channel runs every stage concurrently" pattern; only
		// the stage matching topic.Type ends up driving the encoder
		// since the others self-gate on TopicMsg.Type.
		debateStage := video.NewDebateChannelStage(enc, jobID)
		puzzleStage := video.NewPuzzleChannelStage(enc, jobID)
		seriesStage = video.NewSeriesChannelStage(enc, jobID)
		discussionStage = video.NewDiscussionChannelStage(enc, jobID)
		go debateStage.Run(ctx, deps.Bus)
		go puzzleStage.Run(ctx, deps.Bus)
		go seriesStage.Run(ctx, deps.Bus)
		go discussionStage.Run(ctx, deps.Bus)
	}

	// Audio-only: record the realtime LiveStream (mixed TTS + music bed) to
	// audio.mp3. Subscribe before any audio is produced so the recording
	// starts at the stream's t=0 (the same anchor the subtitles.vtt cues use).
	// The subscriber channel closes when CloseInput drains ffmpeg, after which
	// recDone fires.
	audioDir := filepath.Join(jobOutDir, server.PodcastAudioDir)
	audioPath := filepath.Join(audioDir, server.PodcastAudioFilename)
	var recDone chan struct{}
	var hlsWait func()
	if audioOnly {
		if err := os.MkdirAll(audioDir, 0o755); err != nil {
			return fmt.Errorf("create podcast audio dir: %w", err)
		}
		recFile, ferr := os.Create(audioPath)
		if ferr != nil {
			return fmt.Errorf("create audio file: %w", ferr)
		}
		chunks, _ := live.Subscribe(1024)
		recDone = make(chan struct{})
		go func() {
			defer close(recDone)
			defer recFile.Close()
			for chunk := range chunks {
				if _, werr := recFile.Write(chunk); werr != nil {
					logger.Warn("audio recorder write failed", "err", werr)
					return
				}
			}
		}()

		// Live HLS audio rendition: a second LiveStream subscriber feeds an
		// ffmpeg HLS muxer writing into the job's hls dir, so a native client
		// can stream the audio while it generates via GET
		// /api/jobs/{id}/hls/stream.m3u8 (served by handleJobHLS). Best-effort:
		// a failure here only disables live streaming, not the final download.
		if wait, herr := audio.StartHLSAudio(ctx, live, filepath.Join(jobOutDir, "hls"), logger); herr != nil {
			logger.Warn("audio-only live HLS disabled", "err", herr)
		} else {
			hlsWait = wait
		}
	}

	orch, err := contentcreator.New(&jobEnv, topic, deps.MCPCfg, send, logger, live)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	defer orch.Shutdown()
	orch.Tracker.SetUsageSnapshotCallback(func(usage llm.UsageSummary) {
		persistUsageSnapshot(context.Background(), deps, jobID, usage)
	})

	// Audio-only feeds have no stage to paint, so suppress all on-the-fly
	// image generation inside Run (today: the discussion director's background
	// generation). The director still crossfades the pre-generated music beds.
	if audioOnly {
		orch.SetDisableImages(true)
		orch.SetAudioOnly(true)
	}

	// Expose the live orchestrator so the WebSocket endpoint can inject
	// viewer participation messages into this in-flight run. Cleared when the
	// run exits so the entry never outlives the orchestrator.
	deps.Jobs.SetOrch(jobID, orch)
	defer deps.Jobs.ClearOrch(jobID)

	// Pre-activate the series stage so the brief window between
	// TopicMsg send and stage activation doesn't render through the
	// debate idle card (mirrors the stream-mode behavior). No stage
	// exists in audio-only mode.
	if topic.Type == config.ContentTypeSeries && !audioOnly {
		seriesStage.Preactivate()
	}

	send(buildTopicMsg(topic, jobID))

	if topic.Type == config.ContentTypeSeries {
		t0 := time.Now()
		if audioOnly {
			status("preparing series audio (recap, narration, music)…")
			series.PrepareEpisodeAudioOnly(ctx, logger, &jobEnv, topic, orch)
		} else {
			status("preparing series assets (recap, scenes, music)…")
			series.PrepareEpisode(ctx, logger, &jobEnv, seriesStage, topic, orch)
		}
		logger.Info("series asset prep done",
			"audio_only", audioOnly,
			"elapsed", time.Since(t0).Round(time.Millisecond))
		status(fmt.Sprintf("series assets ready (%s)",
			time.Since(t0).Round(time.Second)))
	}

	// Discussion-family shows (discussion + news) need their background
	// palette + music beds generated before the orchestrator runs (the
	// session bed must be installed pre-Run, and the stage paints the first
	// background as soon as it lands). Without this the show rendered over a
	// bare background with no imagery.
	var discussionMusic discussion.Music
	if (topic.Type == config.ContentTypeDiscussion || topic.Type == config.ContentTypeNews) && !jobEnv.E2EMode {
		t0 := time.Now()
		if audioOnly {
			status("preparing discussion audio (music)…")
			discussionMusic = discussion.PrepareAudioOnly(ctx, logger, jobOutDir, topic, orch, orch.RecordMusicGeneration)
		} else {
			status("preparing discussion assets (backgrounds, music)…")
			discussionMusic = discussion.PrepareAssets(ctx, logger, jobOutDir, discussionStage, topic, orch, orch.RecordMusicGeneration)
		}
		uploadRawPodcastMusic(ctx, deps, logger, jobID, discussionMusic)
		logger.Info("discussion asset prep done",
			"audio_only", audioOnly,
			"elapsed", time.Since(t0).Round(time.Millisecond))
		status(fmt.Sprintf("discussion assets ready (%s)",
			time.Since(t0).Round(time.Second)))
	}

	// Audiobook: generate the music bed + chapter stingers and the small set
	// of illustration images before the orchestrator runs, so the host's
	// prompt carries the scene/sound markers and the pipeline has the bed
	// installed under every turn.
	if topic.Type == config.ContentTypeAudioBook && !jobEnv.E2EMode {
		t0 := time.Now()
		status("preparing audiobook assets (music, illustrations)…")
		audiobook.PrepareAudio(ctx, logger, &jobEnv, topic, orch)
		audioBookID := strings.TrimSpace(deps.DiscussionID)
		if audioBookID == "" {
			audioBookID = jobID
		}
		audiobook.PrepareImages(ctx, logger, &jobEnv, topic, orch, deps.Uploader, audioBookID)
		logger.Info("audiobook asset prep done",
			"elapsed", time.Since(t0).Round(time.Millisecond))
		status(fmt.Sprintf("audiobook assets ready (%s)",
			time.Since(t0).Round(time.Second)))
	}

	status("running orchestrator…")
	tRun := time.Now()
	if err := orch.Run(ctx); err != nil {
		return fmt.Errorf("orch.Run: %w", err)
	}
	persistUsageSummary(ctx, deps, jobID, logger, orch)
	chargeGenerationPoints(ctx, deps, logger, orch)
	persistDiscussionTranscript(ctx, deps, logger, orch)
	// Auto-generate the podcast's summary document now that it has finished. Runs
	// in the background so finalisation (stitch/upload) isn't blocked; the client
	// is notified via summary_ready when it lands.
	startSummaryGeneration(deps, jobID, topic, orch.Transcript.Snapshot())
	startMindmapGeneration(deps, jobID, topic, orch.Transcript.Snapshot())
	status(fmt.Sprintf("orchestrator done (%s)",
		time.Since(tRun).Round(time.Second)))

	if topic.Type == config.ContentTypeSeries {
		status("archiving episode…")
		series.FinishEpisode(logger, &jobEnv, topic)
	}

	// Audio-only finalisation: signal end-of-stream and wait for the recorder
	// to flush the full mixed audio (ffmpeg -re paces out the buffered tail),
	// then publish audio.mp3 + the subtitles.vtt sidecar. No HLS, no stitch.
	if audioOnly {
		status("finalising audio output…")
		_ = live.CloseInput()
		liveClosed = true
		recorderDrained := recDone == nil
		if recDone != nil {
			select {
			case <-recDone:
				recorderDrained = true
			case <-time.After(recorderDrainTimeout):
				logger.Warn("audio recorder drain timed out — output may be truncated")
			case <-ctx.Done():
			}
		}
		// Let the HLS muxer flush its final segment + #EXT-X-ENDLIST so the
		// playlist is complete for on-demand playback after the job finishes.
		if hlsWait != nil {
			fin := make(chan struct{})
			go func() { hlsWait(); close(fin) }()
			select {
			case <-fin:
			case <-time.After(30 * time.Second):
				logger.Warn("audio HLS finalize timed out — playlist may lack ENDLIST")
			case <-ctx.Done():
			}
		}

		// If the recorder drain timed out above, the file may still be
		// growing; wait for it to stop changing before anything (S3 upload,
		// video render, duration probe) reads it.
		if !recorderDrained {
			waitForStableFile(ctx, logger, audioPath, 2*time.Second, 30*time.Second)
		}

		info, statErr := os.Stat(audioPath)
		if statErr != nil || info.Size() == 0 {
			return fmt.Errorf("audio output missing or empty: %v", statErr)
		}
		status(fmt.Sprintf("audio ready · %.1f MB", float64(info.Size())/(1024*1024)))
		subtitlesPath := stagePodcastSubtitles(jobOutDir, logger)
		logSubtitleAudioSync(logger, orch, audioPath)

		var s3Key string
		var downloadURL string
		audioPathForJob := audioPath
		if deps.Uploader.Enabled() {
			status("uploading to S3...")
			key := deps.Uploader.Key(podcastAudioObjectName(jobID, "mp3"))
			if err := deps.Uploader.Upload(ctx, audioPath, key); err != nil {
				logger.Error("s3 upload failed", "err", err)
				return fmt.Errorf("s3 audio upload: %w", err)
			} else {
				s3Key = key
				audioPathForJob = ""
				if url, err := deps.Uploader.DownloadURL(ctx, key, time.Hour); err == nil {
					downloadURL = url
				} else {
					logger.Warn("s3 audio download url failed", "key", key, "err", err)
				}
				// Audiobooks render a video post-pass that reads this local
				// audio.mp3, so keep the staged file for them (the render task
				// cleans up nothing critical — it's a per-job temp dir). Other
				// audio-only jobs remove it now that S3 has the copy.
				if topic.Type == config.ContentTypeAudioBook {
					audioPathForJob = audioPath
				} else {
					if err := os.Remove(audioPath); err != nil {
						logger.Warn("remove staged audio after S3 upload failed", "path", audioPath, "err", err)
					}
				}
				status("uploaded to S3")
			}
		}

		// Persist the subtitles sidecar to shared storage too, so the synced
		// captions survive this pod being recycled — the audio already does via
		// S3, but the VTT otherwise lives only on this pod's local disk. Keep the
		// local copy as a fallback and never fail the job over captions.
		var subtitlesS3Key string
		if deps.Uploader.Enabled() {
			if info, statErr := os.Stat(subtitlesPath); statErr == nil && info.Size() > 0 {
				key := deps.Uploader.Key(podcastAudioObjectName(jobID, "vtt"))
				if err := deps.Uploader.Upload(ctx, subtitlesPath, key); err != nil {
					logger.Warn("s3 subtitles upload failed", "key", key, "err", err)
				} else {
					subtitlesS3Key = key
				}
			}
		}

		// Audiobook illustration timeline sidecar: the canonical
		// {start_ms, image} array clients use to switch artwork in sync
		// with playback (served by /api/jobs/{id}/illustrations).
		// Best-effort like the subtitles sidecar.
		var illustrationsS3Key string
		if topic.Type == config.ContentTypeAudioBook {
			illustrationsS3Key = publishAudioBookIllustrations(ctx, deps, logger, jobID, jobOutDir, orch)
		}

		// Audiobook companion "text-based content": a readable book of the
		// narration with the generated illustrations inline. Best-effort.
		if topic.Type == config.ContentTypeAudioBook {
			status("building text-based content…")
			generateAudioBookTextDoc(ctx, deps, jobID, topic, orch, downloadURL)
		}

		if topic.Type == config.ContentTypeAudioBook {
			status("audio ready")
		} else {
			status("done")
		}
		deps.Jobs.Update(jobID, func(j *server.Job) {
			j.Status = server.JobDone
			j.AudioPath = audioPathForJob
			j.HasAudio = true
			j.AudioOnly = true
			j.AudioS3Key = s3Key
			j.SubtitlesS3Key = subtitlesS3Key
			j.IllustrationsS3Key = illustrationsS3Key
			j.DownloadURL = downloadURL
		})
		persistDiscussionResult(ctx, deps, server.DiscussionReady, downloadURL)
		if topic.Type == config.ContentTypeAudioBook {
			server.PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, deps.DiscussionID, "Audio ready", "audio", "status")
			notifyPodcastAudioReady(ctx, deps, logger)
		} else {
			server.PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, deps.DiscussionID, "Podcast ready", "podcast", "audio", "status")
			notifyPodcastReady(ctx, deps, logger)
		}

		// Audiobook video: render the 1080p video reusing the series renderer
		// now that the audio + illustrations + VTT are final, then surface it in
		// the context menu when done. Runs through the queue so encoders stay
		// bounded; the job is already marked done for audio playback.
		if topic.Type == config.ContentTypeAudioBook {
			scheduleAudioBookVideo(deps, jobID, sub, topic, orch, audioPath, jobOutDir, recDone)
		}
		return nil
	}

	// Finalise the encoder before stitching so ffmpeg flushes the
	// last segment and writes #EXT-X-ENDLIST. Without this, stitch
	// runs against a still-mutating playlist and either races on
	// segment deletion or produces a truncated mp4.
	//
	// Mirror the streaming pipeline's tail-grace behavior here: after
	// the orchestrator's own 20s producer-drain inside pipeline.Run,
	// the encoder still has realtime-paced audio + video frames
	// queued in its ffmpeg pipeline that haven't been muxed into the
	// final HLS segment yet. Closing immediately would truncate the
	// last sentence (and the music bed's natural fadeout) right at
	// the moment the show was about to end. Hold the live input open
	// for an extra grace window so the encoder consumes those tail
	// bytes at realtime; only then signal EOF and let ffmpeg flush.
	const encoderTailGrace = 20 * time.Second
	status(fmt.Sprintf("holding encoder for tail playback (%s)…",
		encoderTailGrace.Round(time.Second)))
	select {
	case <-time.After(encoderTailGrace):
	case <-ctx.Done():
	}
	status("finalising encoder output…")
	_ = live.CloseInput()
	liveClosed = true
	if err := enc.Close(); err != nil {
		logger.Warn("encoder close returned error", "err", err)
	}
	encClosed = true

	// Stitch the HLS playlist into the downloadable mp4. Both video
	// and audio come straight from HLS — the encoder already mixed
	// the music bed underneath every TTS turn, so the resulting mp4
	// matches what the live channel listener heard. Burn-in is
	// already baked into the HLS frames when sub.BurnSubs is set
	// (Encoder.Options.BurnInSeriesCaptions), so stitch only handles
	// soft-subs muxing + the front trim that drops the silent prep
	// prefix.
	mp4Path := filepath.Join(jobOutDir, "video.mp4")
	stitchOpts := video.StitchOpts{
		SoftSubs:    sub.SoftSubs,
		Language:    topic.Language,
		StartOffset: enc.AudioStartOffset(),
	}
	if topic.Type == config.ContentTypeSeries {
		stitchOpts.AudioFadeOut = 5 * time.Second
	}
	subPath := filepath.Join(jobOutDir, "subtitles.vtt")
	if stitchOpts.SoftSubs {
		if _, err := os.Stat(subPath); err != nil {
			logger.Warn("subtitles.vtt not produced — falling back to no subs",
				"path", subPath, "err", err)
			stitchOpts.SoftSubs = false
		} else {
			stitchOpts.SubtitlesPath = subPath
			stitchOpts.SubtitleTracks = []video.SubtitleTrack{{
				Path:     subPath,
				Language: topic.Language,
				Default:  true,
			}}
		}
	}
	if stitchOpts.SoftSubs && len(stitchOpts.SubtitleTracks) > 0 && len(sub.SubtitleLanguages) > 0 {
		langs, _ := normalizeRequestedSubtitleLanguages(topic.Language, sub.SubtitleLanguages)
		if len(langs) > 0 {
			status(fmt.Sprintf("translating subtitles (%d language%s)…",
				len(langs), pluralS(len(langs))))
			client := newSubtitleTranslator(deps.Env.CompressionBaseURL,
				deps.Env.CompressionKey, deps.Env.CompressionModel)
			tracks, err := subtitleTracksForJob(ctx, client, jobOutDir,
				topic.Language, orch.SubtitleCues(), sub.SubtitleLanguages)
			if err != nil {
				return fmt.Errorf("subtitle translation: %w", err)
			}
			stitchOpts.SubtitleTracks = append(stitchOpts.SubtitleTracks, tracks...)
			status(fmt.Sprintf("translated subtitle tracks ready (%d)", len(tracks)))
		}
	}
	stitchLabel := "stitching mp4"
	if stitchOpts.SoftSubs {
		stitchLabel += fmt.Sprintf(" (with %d soft subtitle track%s)",
			len(stitchOpts.SubtitleTracks), pluralS(len(stitchOpts.SubtitleTracks)))
	}
	if stitchOpts.StartOffset > 0 {
		stitchLabel += fmt.Sprintf(" (trimming %s prep)",
			stitchOpts.StartOffset.Round(time.Second))
	}
	if stitchOpts.AudioFadeOut > 0 {
		stitchLabel += fmt.Sprintf(" (audio fade-out %s)",
			stitchOpts.AudioFadeOut.Round(time.Second))
	}
	status(stitchLabel + "…")
	tStitch := time.Now()
	if err := video.StitchMP4(enc.HLSDir(), mp4Path, stitchOpts); err != nil {
		return fmt.Errorf("stitch mp4: %w", err)
	}
	logger.Info("video stitched", "path", mp4Path,
		"soft_subs", stitchOpts.SoftSubs,
		"burn_in_captions", sub.BurnSubs,
		"start_offset", stitchOpts.StartOffset.Round(time.Millisecond),
		"audio_fade_out", stitchOpts.AudioFadeOut.Round(time.Millisecond))
	if info, err := os.Stat(mp4Path); err == nil {
		status(fmt.Sprintf("mp4 ready · %.1f MB · %s",
			float64(info.Size())/(1024*1024),
			time.Since(tStitch).Round(time.Second)))
	} else {
		status("mp4 ready")
	}

	// Series-only: zip the persistent show archive so the user can
	// pass it back as priors for the next episode.
	var archivePath string
	if topic.Type == config.ContentTypeSeries {
		status("zipping series archive…")
		archivePath = filepath.Join(jobOutDir, "archive.zip")
		showRoot := contentcreator.ShowDir(deps.Env.PersistentRoot, topic.Show)
		// Zip relative to PersistentRoot so the archive's top-level
		// folder is `tv-series/<show>/...` — matches the layout
		// SiblingEpisodeDirs expects on the *next* run's unzip.
		if err := zipDir(deps.Env.PersistentRoot, showRoot, archivePath); err != nil {
			logger.Warn("zip archive failed", "path", archivePath, "err", err)
			archivePath = ""
			status("archive zip failed (download disabled)")
		} else {
			logger.Info("archive zipped", "path", archivePath)
			if info, err := os.Stat(archivePath); err == nil {
				status(fmt.Sprintf("archive ready · %.1f MB",
					float64(info.Size())/(1024*1024)))
			} else {
				status("archive ready")
			}
		}
	}

	// Upload the finished mp4 to object storage when configured, so the
	// dashboard can hand users a presigned download link instead of streaming
	// off the engine's disk. Failure is non-fatal — the local file remains
	// servable.
	var s3Key string
	var downloadURL string
	if deps.Uploader.Enabled() {
		status("uploading to S3...")
		key := deps.Uploader.Key(jobID + ".mp4")
		if err := deps.Uploader.Upload(ctx, mp4Path, key); err != nil {
			logger.Error("s3 upload failed", "err", err)
			status("S3 upload failed (serving from disk)")
		} else {
			s3Key = key
			if url, err := deps.Uploader.DownloadURL(ctx, key, time.Hour); err == nil {
				downloadURL = url
			} else {
				logger.Warn("s3 video download url failed", "key", key, "err", err)
			}
			status("uploaded to S3")
		}
	}

	status("done")

	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Status = server.JobDone
		j.VideoPath = mp4Path
		j.HasVideo = true
		j.S3Key = s3Key
		j.DownloadURL = downloadURL
		if archivePath != "" {
			j.ArchivePath = archivePath
			j.HasArchive = true
		}
	})
	persistDiscussionResult(ctx, deps, server.DiscussionReady, downloadURL)
	server.PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, deps.DiscussionID, "Podcast ready", "podcast", "video", "status")
	notifyPodcastReady(ctx, deps, logger)
	return nil
}

func podcastAudioObjectName(jobID, ext string) string {
	ext = strings.TrimPrefix(strings.TrimSpace(ext), ".")
	if ext == "" {
		ext = "mp3"
	}
	return path.Join(server.PodcastAudioDir, jobID+"."+ext)
}

func rawMusicObjectName(jobID, filename string) string {
	return path.Join(server.PodcastRawMusicObjectDir, jobID, filepath.Base(filename))
}

func podcastSubtitlesPath(jobOutDir string) string {
	return filepath.Join(jobOutDir, server.PodcastAudioDir, server.PodcastSubtitlesFilename)
}

func legacyPodcastSubtitlesPath(jobOutDir string) string {
	return filepath.Join(jobOutDir, server.PodcastSubtitlesFilename)
}

func existingPodcastSubtitlesPath(jobOutDir string) string {
	dst := podcastSubtitlesPath(jobOutDir)
	if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
		return dst
	}
	src := legacyPodcastSubtitlesPath(jobOutDir)
	if info, err := os.Stat(src); err == nil && info.Size() > 0 {
		return src
	}
	return dst
}

func stagePodcastSubtitles(jobOutDir string, log *slog.Logger) string {
	dst := podcastSubtitlesPath(jobOutDir)
	src := legacyPodcastSubtitlesPath(jobOutDir)
	if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
		return dst
	}
	if info, err := os.Stat(src); err != nil || info.Size() == 0 {
		return dst
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		log.Warn("podcast subtitles dir create failed", "path", filepath.Dir(dst), "err", err)
		return src
	}
	if err := os.Rename(src, dst); err == nil {
		return dst
	}
	if err := copyFile(src, dst); err != nil {
		log.Warn("podcast subtitles move failed", "src", src, "dst", dst, "err", err)
		return src
	}
	if err := os.Remove(src); err != nil {
		log.Warn("remove legacy subtitles after move failed", "path", src, "err", err)
	}
	return dst
}

func podcastIllustrationsPath(jobOutDir string) string {
	return filepath.Join(jobOutDir, server.PodcastAudioDir, server.PodcastIllustrationsFilename)
}

// publishAudioBookIllustrations writes the canonical illustration timeline
// (illustrations.json) next to the podcast audio and uploads it to shared
// storage, mirroring the subtitles sidecar. The sidecar is what clients use
// to switch artwork in sync with playback — they no longer reconstruct the
// timing from transcript lines. Returns the uploaded object key ("" when
// nothing was produced or the upload failed); never fails the job.
func publishAudioBookIllustrations(ctx context.Context, deps Deps, log *slog.Logger,
	jobID, jobOutDir string, orch *contentcreator.Orchestrator) string {
	cues := orch.AudioBookIllustrationTimeline()
	if len(cues) == 0 {
		return ""
	}
	payload, err := json.Marshal(struct {
		Illustrations []contentcreator.IllustrationCue `json:"illustrations"`
	}{cues})
	if err != nil {
		log.Warn("illustrations sidecar marshal failed", "err", err)
		return ""
	}
	localPath := podcastIllustrationsPath(jobOutDir)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		log.Warn("illustrations sidecar dir create failed", "path", filepath.Dir(localPath), "err", err)
	} else if err := os.WriteFile(localPath, payload, 0o644); err != nil {
		log.Warn("illustrations sidecar write failed", "path", localPath, "err", err)
	}
	if !deps.Uploader.Enabled() {
		return ""
	}
	key := deps.Uploader.Key(podcastAudioObjectName(jobID, "illustrations.json"))
	if err := deps.Uploader.UploadBytes(ctx, key, "application/json", payload); err != nil {
		log.Warn("illustrations sidecar upload failed", "key", key, "err", err)
		return ""
	}
	return key
}

func uploadRawPodcastMusic(ctx context.Context, deps Deps, log *slog.Logger, jobID string, music discussion.Music) {
	if !deps.Uploader.Enabled() {
		return
	}
	seen := map[string]bool{}
	paths := make([]string, 0, len(music.Beds)+len(music.Sounds))
	for _, p := range music.Beds {
		paths = append(paths, p)
	}
	paths = append(paths, music.Sounds...)
	for _, localPath := range paths {
		localPath = strings.TrimSpace(localPath)
		if localPath == "" || seen[localPath] {
			continue
		}
		seen[localPath] = true
		key := deps.Uploader.Key(rawMusicObjectName(jobID, filepath.Base(localPath)))
		if err := deps.Uploader.Upload(ctx, localPath, key); err != nil {
			log.Warn("raw podcast music upload failed", "path", localPath, "key", key, "err", err)
			continue
		}
		log.Info("raw podcast music uploaded", "path", localPath, "key", key)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func fail(deps Deps, jobID string, log *slog.Logger, err error) {
	log.Error("video job failed", "err", err)
	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Status = server.JobError
		j.Error = err.Error()
	})
	deps.Jobs.AppendLog(jobID, "error", err.Error(), nil)
	persistDiscussionResult(context.Background(), deps, server.DiscussionFailed, "")
}

func persistDiscussionTranscript(ctx context.Context, deps Deps, log *slog.Logger, orch *contentcreator.Orchestrator) {
	if deps.Discussions == nil || deps.DiscussionID == "" || orch == nil {
		return
	}
	if err := deps.Discussions.ReplaceTranscript(ctx, deps.DiscussionID, orch.Transcript.Snapshot()); err != nil {
		log.Warn("native discussion transcript persist failed",
			"discussion_id", deps.DiscussionID,
			"err", err,
		)
	}
}

func persistDiscussionResult(ctx context.Context, deps Deps, status server.DiscussionStatus, downloadURL string) {
	if deps.Discussions == nil || deps.DiscussionID == "" {
		return
	}
	_ = deps.Discussions.SetJobResult(ctx, deps.DiscussionID, status, downloadURL)
}

func notifyPodcastReady(ctx context.Context, deps Deps, log *slog.Logger) {
	if deps.APNS == nil || deps.Discussions == nil || deps.DiscussionID == "" {
		return
	}
	d, err := deps.Discussions.GetForNotification(ctx, deps.DiscussionID)
	if err != nil {
		log.Warn("podcast ready push discussion lookup failed", "discussion_id", deps.DiscussionID, "err", err)
		return
	}
	if d == nil {
		return
	}
	server.SendPushNotification(ctx, deps.Discussions, deps.APNS, d.OwnerUserID, server.PushNotification{
		Kind:         server.PushKindPodcastReady,
		DiscussionID: d.ID,
		Title:        "Podcast finished",
		Body:         pushDiscussionTitle(d, "Your podcast is ready to play."),
		URL:          server.DiscussionDeepLink(server.FrontendBaseURL(deps.Env), d.ID),
	}, log)
}

func notifyPodcastAudioReady(ctx context.Context, deps Deps, log *slog.Logger) {
	if deps.APNS == nil || deps.Discussions == nil || deps.DiscussionID == "" {
		return
	}
	d, err := deps.Discussions.GetForNotification(ctx, deps.DiscussionID)
	if err != nil {
		log.Warn("podcast audio ready push discussion lookup failed", "discussion_id", deps.DiscussionID, "err", err)
		return
	}
	if d == nil {
		return
	}
	server.SendPushNotification(ctx, deps.Discussions, deps.APNS, d.OwnerUserID, server.PushNotification{
		Kind:         server.PushKindPodcastAudioReady,
		DiscussionID: d.ID,
		Title:        "Audio ready",
		Body:         pushDiscussionTitle(d, "Your audiobook audio is ready to play."),
		URL:          server.DiscussionDeepLink(server.FrontendBaseURL(deps.Env), d.ID),
	}, log)
}

func pushDiscussionTitle(d *server.Discussion, fallback string) string {
	if d == nil {
		return fallback
	}
	if title := strings.TrimSpace(d.Title); title != "" {
		return title
	}
	if topic := strings.TrimSpace(d.Topic); topic != "" {
		return topic
	}
	return fallback
}

func persistUsageSummary(ctx context.Context, deps Deps, jobID string, log *slog.Logger, orch *contentcreator.Orchestrator) {
	if orch == nil {
		return
	}
	usage := orch.UsageSummary()
	if !hasUsage(usage) {
		return
	}
	log.Info("llm usage summary",
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"total_tokens", usage.TotalTokens,
		"cost_usd", usage.CostUSD,
		"cost_known", usage.CostKnown,
		"tts_chars", usage.TTSCharacters,
		"tts_cost_usd", usage.TTSCostUSD,
		"music_gens", usage.MusicGenerations,
		"music_cost_usd", usage.MusicCostUSD,
		"total_cost_usd", usage.TotalCostUSD())
	persistUsageSnapshot(ctx, deps, jobID, usage)
}

func persistUsageSnapshot(ctx context.Context, deps Deps, jobID string, usage llm.UsageSummary) {
	if !hasUsage(usage) {
		return
	}
	deps.Jobs.UpdateUsage(jobID,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		usage.CostUSD, usage.CostKnown, usage.TTSCostUSD, usage.MusicCostUSD)
	if deps.Discussions != nil && deps.DiscussionID != "" {
		_ = deps.Discussions.SetUsage(ctx, deps.DiscussionID,
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
			usage.CostUSD, usage.CostKnown, usage.TTSCostUSD, usage.MusicCostUSD)
	}
}

func hasUsage(usage llm.UsageSummary) bool {
	return usage.TotalTokens > 0 || usage.CostUSD > 0 ||
		usage.TTSCharacters > 0 || usage.TTSCostUSD > 0 ||
		usage.MusicGenerations > 0 || usage.MusicCostUSD > 0
}

// chargeGenerationPoints reconciles the points reservation made when the user
// started this discussion's generation against the run's actual cost, charging
// immediately on completion. Idempotent (SettleGeneration); a later discussion
// fetch reconciles the same way if this is skipped. Runs for both the video and
// audio-only finalization paths, and regardless of token count, so a generation
// reservation is always released.
func chargeGenerationPoints(ctx context.Context, deps Deps, log *slog.Logger, orch *contentcreator.Orchestrator) {
	if deps.Points == nil || deps.DiscussionID == "" || orch == nil {
		return
	}
	usage := orch.UsageSummary()
	detail := server.PointsUsageDetail{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		LLMCostUSD:       usage.CostUSD,
		LLMCostKnown:     usage.CostKnown,
		TTSCostUSD:       usage.TTSCostUSD,
		MusicCostUSD:     usage.MusicCostUSD,
		CostUSD:          usage.TotalCostUSD(),
	}
	if err := deps.Points.ChargeGeneration(ctx, deps.Env, deps.DiscussionID, detail); err != nil {
		log.Warn("generation settle failed", "discussion_id", deps.DiscussionID, "err", err)
	}
}

func persistEvent(jobs *server.JobRegistry, jobID string, v any) {
	switch m := v.(type) {
	case contentcreator.StatusMsg:
		jobs.AppendLog(jobID, "status", m.Text, m)
	case contentcreator.PhaseMsg:
		text := m.Label
		if text == "" {
			text = m.Phase.String()
		}
		jobs.Update(jobID, func(j *server.Job) {
			j.Phase = m.Phase.String()
			j.PhaseLabel = m.Label
		})
		jobs.AppendLog(jobID, "phase", text, m)
	case contentcreator.TranscriptMsg:
		if m.Done && m.Text != "" {
			prefix := m.Speaker
			if prefix != "" {
				prefix += ": "
			}
			jobs.AppendLog(jobID, "transcript", prefix+m.Text, m)
		}
	case contentcreator.ErrorMsg:
		if m.Err != nil {
			jobs.AppendLog(jobID, "error", m.Err.Error(), m)
		}
	case contentcreator.TickMsg:
		jobs.Update(jobID, func(j *server.Job) {
			j.ElapsedMS = m.Elapsed.Milliseconds()
			j.RemainingMS = m.Remaining.Milliseconds()
		})
	case contentcreator.TopicMsg:
		head := m.Title
		if m.Show != "" {
			head = fmt.Sprintf("%s · S%d E%d", m.Show, m.Season, m.Episode)
		}
		jobs.AppendLog(jobID, "topic", head, m)
	case contentcreator.EndedMsg:
		jobs.AppendLog(jobID, "ended", "orchestrator ended - finalising mp4...", m)
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// buildTopicMsg constructs the TopicMsg for a single-job video run.
// Mirrors the per-channel buildTopicMsg in cmd/debate-bot but kept
// here so the runner doesn't have to pull from cmd/.
func buildTopicMsg(topic *config.DebateTopic, jobID string) contentcreator.TopicMsg {
	msg := contentcreator.TopicMsg{
		ID:    jobID,
		Title: topic.Title,
		Type:  topic.Type,
		Index: 0,
		Total: 1,
	}
	switch topic.Type {
	case config.ContentTypeAudioBook:
		hostName := topic.AudioBookHost.Name
		if hostName == "" {
			hostName = "Narrator"
		}
		msg.AffNames = []string{hostName}
		msg.AffPosition = topic.Background
	case config.ContentTypeSeries:
		hostName := topic.SeriesHost.Name
		if hostName == "" {
			hostName = "Narrator"
		}
		msg.AffNames = []string{hostName}
		msg.AffPosition = topic.Surface
		msg.Show = topic.Show
		msg.Season = topic.Season
		msg.Episode = topic.Episode
	case config.ContentTypeDebate:
		for _, a := range topic.Affirmative {
			msg.AffNames = append(msg.AffNames, a.Name)
		}
		for _, n := range topic.Negative {
			msg.NegNames = append(msg.NegNames, n.Name)
		}
		msg.AffPosition = topic.AffirmativePos
		msg.NegPosition = topic.NegativePos
	case config.ContentTypeDiscussion:
		// Discussants populate the left panel; the moderator/host the right.
		for _, dsc := range topic.Discussants {
			msg.AffNames = append(msg.AffNames, dsc.Name)
		}
		hostName := topic.Host.Name
		if hostName == "" {
			hostName = "Host"
		}
		msg.NegNames = []string{hostName}
		msg.AffPosition = topic.Background
	case config.ContentTypeNews:
		// Commentators populate the left panel; the anchor the right.
		for _, dsc := range topic.Discussants {
			msg.AffNames = append(msg.AffNames, dsc.Name)
		}
		anchorName := topic.Host.Name
		if anchorName == "" {
			anchorName = "Anchor"
		}
		msg.NegNames = []string{anchorName}
		msg.AffPosition = topic.Background
	}
	return msg
}

// unzipPriors extracts src.zip into dst. Refuses any entry whose
// resolved path escapes dst — the upload is user-controlled and a
// crafted zip with `..` paths could otherwise overwrite arbitrary
// files. Existing files are overwritten (an upload re-extract is the
// canonical way to refresh history).
func unzipPriors(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir dst: %w", err)
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("abs dst: %w", err)
	}

	for _, f := range zr.File {
		// Strip any leading separators or `.` / `..` segments. Then
		// final containment check after Join.
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
			return fmt.Errorf("zip entry escapes dst: %q", f.Name)
		}
		target := filepath.Join(dstAbs, clean)
		if !strings.HasPrefix(target, dstAbs+string(filepath.Separator)) && target != dstAbs {
			return fmt.Errorf("zip entry outside dst after join: %q", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", target, err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("open %s: %w", target, err)
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return fmt.Errorf("copy %s: %w", f.Name, copyErr)
		}
	}
	return nil
}

// zipDir walks `srcRoot` and writes a zip at outPath. Each entry's
// path inside the zip is relative to `relativeTo` so the receiver can
// extract it back to the same parent directory and end up with an
// identical tree.
func zipDir(relativeTo, srcRoot, outPath string) error {
	if _, err := os.Stat(srcRoot); err != nil {
		return fmt.Errorf("zip src missing: %w", err)
	}
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()

	relativeAbs, err := filepath.Abs(relativeTo)
	if err != nil {
		return fmt.Errorf("abs relativeTo: %w", err)
	}

	return filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(relativeAbs, abs)
		if err != nil {
			return err
		}
		// Skip the root (relativeTo itself) — zip archives don't need
		// an explicit "." entry.
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			// Directory entries end in "/". Cosmetic — most readers
			// recreate dirs from file paths anyway, but it keeps the
			// listing tidy.
			_, werr := zw.Create(filepath.ToSlash(rel) + "/")
			return werr
		}
		w, werr := zw.Create(filepath.ToSlash(rel))
		if werr != nil {
			return werr
		}
		f, oerr := os.Open(path)
		if oerr != nil {
			return oerr
		}
		defer f.Close()
		_, cerr := io.Copy(w, f)
		return cerr
	})
}
