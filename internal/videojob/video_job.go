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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/discussion"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/series"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/storage"
	"github.com/sirily11/debate-bot/internal/video"
)

// Queue is the small subset of goqueue.Queue that Submit needs.
type Queue interface {
	Add(context.Context, func(context.Context))
}

// Deps wires the runner to long-lived process state. Env is the
// LoadEnv-produced config (its OutDir is the session root, not the
// per-job dir — the runner appends jobs/<id>/). MCPCfg is forwarded
// to each per-job orchestrator; today most uploads run with empty mcp
// configs but the seam is here for future tools.
type Deps struct {
	Env    *config.Env
	MCPCfg *config.MCPConfig
	Bus    *eventbus.Bus
	Jobs   *server.JobRegistry
	Queue  Queue
	Log    *slog.Logger
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
	if deps.Queue == nil {
		return errors.New("video job queue is not configured")
	}
	topic, err := config.LoadTopic(sub.ScriptPath)
	if err != nil {
		return fmt.Errorf("script.md: %w", err)
	}

	// Soft subs + translated tracks work for any type whose stage
	// emits a subtitles.vtt sidecar — series and discussion. Burn-in
	// captions are painted only by the series renderer (discussion
	// already burns its active-speaker caption natively), and the
	// priors zip is a series-only continuity feature. Reject mismatched
	// flags early with a clear message rather than silently ignoring
	// them.
	supportsSoftSubs := topic.Type == config.ContentTypeSeries ||
		topic.Type == config.ContentTypeDiscussion
	if !supportsSoftSubs {
		if sub.SoftSubs || len(sub.SubtitleLanguages) > 0 {
			return errors.New("subtitle options (soft_subs) are only valid for type=series or type=discussion")
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

	deps.Queue.Add(ctx, func(runCtx context.Context) {
		defer func() {
			if v := recover(); v != nil {
				fail(deps, jobID, deps.Log.With("job", jobID),
					fmt.Errorf("video job panic: %v", v))
			}
		}()
		run(runCtx, deps, jobID, sub, topic)
	})
	return nil
}

// run is the long-running half of the submission. It assumes
// validation already passed; failures here update the registry to
// JobError but don't propagate.
func run(ctx context.Context, deps Deps, jobID string,
	sub server.JobSubmission, topic *config.DebateTopic,
) {
	logger := deps.Log.With("job", jobID, "type", topic.Type, "title", topic.Title)
	audioOnly := sub.AudioOnly
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
		fail(deps, jobID, logger, fmt.Errorf("create job dir: %w", err))
		return
	}

	// Series-only: stage the optional priors zip into the persistent
	// archive root BEFORE PrepareEpisode walks SiblingEpisodeDirs.
	if topic.Type == config.ContentTypeSeries && sub.PriorsZipPath != "" {
		status("extracting priors zip…")
		if err := unzipPriors(sub.PriorsZipPath, deps.Env.PersistentRoot); err != nil {
			fail(deps, jobID, logger, fmt.Errorf("unzip priors: %w", err))
			return
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
		fail(deps, jobID, logger, fmt.Errorf("livestream: %w", err))
		return
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
			fail(deps, jobID, logger, fmt.Errorf("encoder: %w", err))
			return
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
	audioPath := filepath.Join(jobOutDir, "audio.mp3")
	var recDone chan struct{}
	if audioOnly {
		recFile, ferr := os.Create(audioPath)
		if ferr != nil {
			fail(deps, jobID, logger, fmt.Errorf("create audio file: %w", ferr))
			return
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
	}

	orch, err := contentcreator.New(&jobEnv, topic, deps.MCPCfg, send, logger, live)
	if err != nil {
		fail(deps, jobID, logger, fmt.Errorf("orchestrator: %w", err))
		return
	}
	defer orch.Shutdown()

	// Audio-only feeds have no stage to paint, so suppress all on-the-fly
	// image generation inside Run (today: the discussion director's background
	// generation). The director still crossfades the pre-generated music beds.
	if audioOnly {
		orch.SetDisableImages(true)
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

	// Discussion needs its background palette + music beds generated before
	// the orchestrator runs (the session bed must be installed pre-Run, and
	// the stage paints the first background as soon as it lands). Without this
	// the show rendered over a bare background with no imagery.
	if topic.Type == config.ContentTypeDiscussion {
		t0 := time.Now()
		if audioOnly {
			status("preparing discussion audio (music)…")
			discussion.PrepareAudioOnly(ctx, logger, jobOutDir, topic, orch)
		} else {
			status("preparing discussion assets (backgrounds, music)…")
			discussion.PrepareAssets(ctx, logger, jobOutDir, discussionStage, topic, orch)
		}
		logger.Info("discussion asset prep done",
			"audio_only", audioOnly,
			"elapsed", time.Since(t0).Round(time.Millisecond))
		status(fmt.Sprintf("discussion assets ready (%s)",
			time.Since(t0).Round(time.Second)))
	}

	status("running orchestrator…")
	tRun := time.Now()
	if err := orch.Run(ctx); err != nil {
		fail(deps, jobID, logger, fmt.Errorf("orch.Run: %w", err))
		return
	}
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
		if recDone != nil {
			select {
			case <-recDone:
			case <-time.After(30 * time.Second):
				logger.Warn("audio recorder drain timed out — output may be truncated")
			case <-ctx.Done():
			}
		}

		info, statErr := os.Stat(audioPath)
		if statErr != nil || info.Size() == 0 {
			fail(deps, jobID, logger, fmt.Errorf("audio output missing or empty: %v", statErr))
			return
		}
		status(fmt.Sprintf("audio ready · %.1f MB", float64(info.Size())/(1024*1024)))

		var s3Key string
		if deps.Uploader.Enabled() {
			status("uploading to S3...")
			key := deps.Uploader.Key(jobID + ".mp3")
			if err := deps.Uploader.Upload(ctx, audioPath, key); err != nil {
				logger.Error("s3 upload failed", "err", err)
				status("S3 upload failed (serving from disk)")
			} else {
				s3Key = key
				status("uploaded to S3")
			}
		}

		status("done")
		deps.Jobs.Update(jobID, func(j *server.Job) {
			j.Status = server.JobDone
			j.AudioPath = audioPath
			j.HasAudio = true
			j.AudioOnly = true
			j.AudioS3Key = s3Key
		})
		return
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
				fail(deps, jobID, logger, fmt.Errorf("subtitle translation: %w", err))
				return
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
		fail(deps, jobID, logger, fmt.Errorf("stitch mp4: %w", err))
		return
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
	if deps.Uploader.Enabled() {
		status("uploading to S3...")
		key := deps.Uploader.Key(jobID + ".mp4")
		if err := deps.Uploader.Upload(ctx, mp4Path, key); err != nil {
			logger.Error("s3 upload failed", "err", err)
			status("S3 upload failed (serving from disk)")
		} else {
			s3Key = key
			status("uploaded to S3")
		}
	}

	status("done")

	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Status = server.JobDone
		j.VideoPath = mp4Path
		j.HasVideo = true
		j.S3Key = s3Key
		if archivePath != "" {
			j.ArchivePath = archivePath
			j.HasArchive = true
		}
	})
}

func fail(deps Deps, jobID string, log *slog.Logger, err error) {
	log.Error("video job failed", "err", err)
	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Status = server.JobError
		j.Error = err.Error()
	})
	deps.Jobs.AppendLog(jobID, "error", err.Error(), nil)
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
