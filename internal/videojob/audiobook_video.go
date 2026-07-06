package videojob

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/server"
)

// scheduleAudioBookVideo publishes the queued pass that renders the
// audiobook's 1080p video (illustration slideshow + narration audio + soft
// captions) once the audio has finished streaming. The consumer
// (Server.RunAudioBookVideoRenderTask, via internal/jobworker) rebuilds
// every input from DB/disk/S3, renders, uploads, records the video key on
// the discussion, and pushes a notification — with retry on failure.
// No-op when there are no illustrations to show.
//
// The per-image animation/offset snapshot is persisted (locally and to
// object storage) BEFORE publishing so any consuming pod reproduces the
// live run's beat-accurate motion after this orchestrator is gone.
func scheduleAudioBookVideo(deps Deps, jobID string, sub server.JobSubmission,
	topic *config.DebateTopic, orch *contentcreator.Orchestrator, audioPath, jobOutDir string,
	recDone <-chan struct{},
) {
	logger := slog.Default().With("job", jobID)
	if deps.Log != nil {
		logger = deps.Log.With("job", jobID)
	}
	if deps.MQ == nil || orch == nil {
		logger.Info("audiobook video skipped", "queue_configured", deps.MQ != nil, "orchestrator_configured", orch != nil)
		return
	}
	imgs := orch.AudioBookImages()
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Beat < imgs[j].Beat })
	offsets := orch.AudioBookImageOffsets()
	paths, anims, starts, beats, skipped := snapshotAudioBookVideoImages(imgs, offsets)
	if len(paths) == 0 {
		// No illustrations → no slideshow to render. The audio + text doc still
		// stand on their own.
		logger.Info("audiobook video skipped", "reason", "no illustrations")
		return
	}
	if skipped > 0 {
		logger.Info("audiobook video: dropping beats whose scene marker never fired",
			"kept", len(paths), "skipped", skipped)
	}
	writeAudioBookVideoTimings(logger, jobOutDir, anims, starts, beats)
	uploadAudioBookVideoTimings(deps, logger, jobID, jobOutDir)

	if err := server.PublishAudioBookVideoRender(context.Background(), deps.Jobs, deps.MQ, jobID, deps.DiscussionID); err != nil {
		logger.Warn("audiobook video enqueue failed", "err", err)
	}
}

// uploadAudioBookVideoTimings stages the freshly written timings.json in
// object storage so a consuming pod other than this one can reproduce the
// beat-accurate motion. Best-effort — the consumer falls back to the
// illustrations timeline sidecar.
func uploadAudioBookVideoTimings(deps Deps, logger *slog.Logger, jobID, jobOutDir string) {
	if !deps.Uploader.Enabled() {
		return
	}
	local := filepath.Join(jobOutDir, "audiobook", "scenes", "timings.json")
	data, err := os.ReadFile(local)
	if err != nil || len(data) == 0 {
		return
	}
	key := deps.Uploader.Key(server.AudioBookTimingsObjectName(jobID))
	if err := deps.Uploader.UploadBytes(context.Background(), key, "application/json", data); err != nil {
		logger.Warn("audiobook video timings upload failed", "key", key, "err", err)
	}
}

// snapshotAudioBookVideoImages selects which illustrations go into the video
// and with what timing. imgs must be sorted by Beat; offsets is the per-beat
// audio position captured from the live run's scene markers.
//
// When any offsets were recorded, only beats whose marker actually fired are
// kept — a chapter-limited narration generates illustrations for the whole
// outline, but images past the narrated range have no place on this audio's
// timeline. (The previous all-or-nothing check discarded EVERY offset when
// one was missing, so the renderer even-split all images across the audio
// and the slideshow ran ahead of the narration.) When no offsets exist at
// all (legacy runs, no markers fired), every image is kept and starts is nil
// so the renderer falls back to its even split.
func snapshotAudioBookVideoImages(imgs []contentcreator.AudioBookImage, offsets map[int]float64,
) (paths, anims []string, starts []float64, beats []int, skipped int) {
	for _, im := range imgs {
		if im.Path == "" {
			continue
		}
		off, ok := offsets[im.Beat]
		if !ok && len(offsets) > 0 {
			skipped++
			continue
		}
		paths = append(paths, im.Path)
		anims = append(anims, im.Animation)
		starts = append(starts, off)
		beats = append(beats, im.Beat)
	}
	if len(offsets) == 0 {
		starts = nil
	}
	return paths, anims, starts, beats, skipped
}

// writeAudioBookVideoTimings persists the per-image animation + audio-offset
// snapshot next to the generated scene PNGs so the manual re-render endpoint
// can rebuild the same motion-timed video after the orchestrator is gone.
// beats identifies which narration-vN.png each entry describes — the
// re-render endpoint globs every scene PNG, so without it a filtered
// snapshot couldn't be matched back to files. offsets may be nil (unknown
// timing → even split on re-render).
func writeAudioBookVideoTimings(logger *slog.Logger, jobOutDir string, anims []string, offsets []float64, beats []int) {
	payload := struct {
		Animations   []string  `json:"animations"`
		ImageOffsets []float64 `json:"image_offsets,omitempty"`
		Beats        []int     `json:"beats,omitempty"`
	}{Animations: anims, ImageOffsets: offsets, Beats: beats}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn("audiobook video timings marshal failed", "err", err)
		return
	}
	path := filepath.Join(jobOutDir, "audiobook", "scenes", "timings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Warn("audiobook video timings dir failed", "err", err)
		return
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		logger.Warn("audiobook video timings write failed", "err", err)
	}
}
