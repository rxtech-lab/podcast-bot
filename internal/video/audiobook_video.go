package video

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RenderAudioBookVideo composes a finished audiobook into a downloadable
// video: the generated illustrations are shown as a slideshow timed evenly
// across the narration audio, scaled/letterboxed to the requested resolution,
// with the synced captions muxed in as a soft subtitle track. It runs after
// the audio + illustrations + VTT are final (a post-pass, not the live
// renderer), reusing the same ffmpeg toolchain the rest of the video package
// depends on.
//
// imagePaths must be non-empty and ordered by beat. vttPath may be "" to skip
// subtitles. The output is an H.264/AAC mp4 with +faststart for progressive
// playback.
func RenderAudioBookVideo(outPath, audioPath, vttPath string, imagePaths []string, res Resolution) error {
	return RenderAudioBookVideoWithOptions(outPath, audioPath, vttPath, imagePaths, res, AudioBookVideoOptions{})
}

// AudioBookVideoLine is one spoken transcript line available to the
// audiobook post-pass renderer.
type AudioBookVideoLine struct {
	Speaker string
	Text    string
}

// AudioBookVideoAvatar is a local speaker cutout image. Path should point to a
// PNG with alpha; missing paths are ignored and the renderer falls back to its
// generated geometric avatar.
type AudioBookVideoAvatar struct {
	Name string
	Path string
}

// AudioBookVideoOptions lets the audiobook post-pass pick a visual treatment
// from planner metadata. The zero value preserves the original illustration
// slideshow renderer.
type AudioBookVideoOptions struct {
	Style    string
	Title    string
	Language string
	Host     string
	Speakers []string
	Lines    []AudioBookVideoLine
	Avatars  []AudioBookVideoAvatar

	// Animations is the planner's per-image camera-move token list, parallel
	// to the imagePaths argument (stall / panleft / panright / pantop /
	// panbottom / zoomin / zoomout). Empty → a built-in fallback cycle.
	Animations []string
	// ImageOffsets is the audio-relative start time (seconds) of each image,
	// parallel to imagePaths — captured from the live run's scene-marker
	// timing. Empty or invalid → images split the duration evenly.
	ImageOffsets []float64
}

// RenderAudioBookVideoWithOptions composes a finished audiobook into a
// downloadable video. Narration-style audiobooks keep the original generated
// illustration slideshow; conversational-style audiobooks render each spoken
// transcript segment with left/right speaker avatars and a central content
// panel over the generated backgrounds.
func RenderAudioBookVideoWithOptions(outPath, audioPath, vttPath string, imagePaths []string, res Resolution, opts AudioBookVideoOptions) error {
	if len(imagePaths) == 0 {
		return fmt.Errorf("render audiobook video: no images")
	}
	if _, err := os.Stat(audioPath); err != nil {
		return fmt.Errorf("render audiobook video: audio missing: %w", err)
	}
	dur, err := probeDurationSeconds(audioPath)
	if err != nil || dur <= 0 {
		return fmt.Errorf("render audiobook video: probe audio duration: %w", err)
	}
	if isConversationalAudioBookStyle(opts.Style) {
		return renderConversationalAudioBookVideo(outPath, audioPath, vttPath, imagePaths, res, opts, dur)
	}
	// Narration style: Ken Burns pan/zoom over each illustration, timed to
	// the live run's scene-marker offsets. The static concat slideshow below
	// remains as a fallback if the frame-pipe render fails (e.g. an
	// undecodable image).
	if kbErr := renderKenBurnsAudioBookVideo(outPath, audioPath, vttPath, imagePaths, res, opts, dur); kbErr == nil {
		return nil
	} else {
		fmt.Fprintf(os.Stderr, "audiobook kenburns render failed, falling back to static slideshow: %v\n", kbErr)
	}
	perImage := dur / float64(len(imagePaths))

	// Build the concat-demuxer playlist: each image is held for perImage
	// seconds. The concat demuxer needs the final image's `file` line repeated
	// without a duration so its trailing segment is emitted.
	var list strings.Builder
	for _, p := range imagePaths {
		abs, aerr := filepath.Abs(p)
		if aerr != nil {
			abs = p
		}
		fmt.Fprintf(&list, "file '%s'\nduration %.3f\n", concatEscape(abs), perImage)
	}
	lastAbs, _ := filepath.Abs(imagePaths[len(imagePaths)-1])
	fmt.Fprintf(&list, "file '%s'\n", concatEscape(lastAbs))

	listPath := outPath + ".concat.txt"
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return fmt.Errorf("render audiobook video: write concat list: %w", err)
	}
	defer os.Remove(listPath)

	w, h := outputDims(res)
	vf := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=25,format=yuv420p",
		w, h, w, h)

	hasSubs := false
	if vttPath != "" {
		if _, serr := os.Stat(vttPath); serr == nil {
			hasSubs = true
		}
	}

	args := []string{"-y", "-f", "concat", "-safe", "0", "-i", listPath, "-i", audioPath}
	if hasSubs {
		args = append(args, "-i", vttPath)
	}
	args = append(args, "-map", "0:v", "-map", "1:a")
	if hasSubs {
		args = append(args, "-map", "2:s")
	}
	args = append(args,
		"-vf", vf,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-shortest",
	)
	if hasSubs {
		args = append(args, "-c:s", "mov_text")
		args = appendSubtitleTrackMetadata(args, []SubtitleTrack{{
			Language: opts.Language,
			Default:  true,
		}})
	}
	args = append(args, "-movflags", "+faststart", outPath)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("render audiobook video: ffmpeg: %w", err)
	}
	return nil
}

func isConversationalAudioBookStyle(style string) bool {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "conversational", "podcast", "meeting", "news":
		return true
	default:
		return false
	}
}

// probeDurationSeconds returns the container duration of a media file via
// ffprobe (shipped alongside ffmpeg).
func probeDurationSeconds(path string) (float64, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// concatEscape escapes single quotes for the concat demuxer's `file '...'`
// syntax, so paths with apostrophes don't break the playlist.
func concatEscape(p string) string {
	return strings.ReplaceAll(p, "'", `'\''`)
}
