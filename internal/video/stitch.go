package video

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StitchOpts configures how StitchMP4 invokes ffmpeg.
//
// SoftSubs muxes SubtitlesPath into the output as a `mov_text` subtitle
// track (toggleable in players that support soft subs). Compatible with
// stream copy.
//
// BurnSubs hardcodes SubtitlesPath into the video frames via the
// `subtitles` filter; forces a video re-encode (libx264) since the
// pixels themselves change.
//
// Both flags can be set simultaneously — the result is a re-encoded
// video with burned-in captions plus a soft-sub track for players that
// can toggle it. SubtitlesPath is required when either flag is set.
//
// Language is the BCP-47 tag stamped on the soft-sub track metadata
// (default "und" when blank). Ignored unless SoftSubs.
type StitchOpts struct {
	SoftSubs      bool
	BurnSubs      bool
	SubtitlesPath string
	Language      string
}

// StitchMP4 runs ffmpeg to combine the HLS playlist at hlsDir/stream.m3u8
// with audioPath into a single .mp4 at outPath. When opts is empty, the
// behavior matches the historical helper in cmd/series-recap-smoke
// (stream-copy video, AAC audio, -shortest).
//
// Returns an error if the playlist is missing or ffmpeg exits non-zero.
// audioPath is optional — when missing, the resulting mp4 has no audio
// track.
func StitchMP4(hlsDir, audioPath, outPath string, opts StitchOpts) error {
	playlist := filepath.Join(hlsDir, "stream.m3u8")
	if _, err := os.Stat(playlist); err != nil {
		return fmt.Errorf("hls playlist missing: %w", err)
	}
	if (opts.SoftSubs || opts.BurnSubs) && opts.SubtitlesPath == "" {
		return fmt.Errorf("StitchOpts: SoftSubs/BurnSubs require SubtitlesPath")
	}
	if opts.SubtitlesPath != "" {
		if _, err := os.Stat(opts.SubtitlesPath); err != nil {
			return fmt.Errorf("subtitles file missing: %w", err)
		}
	}

	args := []string{"-y", "-i", playlist}

	hasAudio := false
	if audioPath != "" {
		if _, err := os.Stat(audioPath); err == nil {
			args = append(args, "-i", audioPath)
			hasAudio = true
		}
	}

	if opts.SoftSubs {
		args = append(args, "-i", opts.SubtitlesPath)
	}

	// Video codec: copy by default, libx264 when we need to burn subs.
	if opts.BurnSubs {
		// Escape the path for ffmpeg's subtitles filter — colons and
		// backslashes need escaping, and the filter wants forward slashes.
		args = append(args,
			"-c:v", "libx264",
			"-preset", "medium",
			"-crf", "20",
			"-vf", "subtitles="+escapeSubtitlesFilter(opts.SubtitlesPath),
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	if hasAudio {
		args = append(args, "-c:a", "aac")
	} else {
		args = append(args, "-an")
	}

	if opts.SoftSubs {
		// Soft-sub track: mov_text is the standard mp4 container codec.
		// Stream index of the subtitle input depends on whether audio was
		// added: video is input 0, audio (if present) is input 1, subs
		// are last. Map all video + audio + the sub stream explicitly so
		// ffmpeg doesn't auto-skip the subs.
		subInputIdx := 1
		if hasAudio {
			subInputIdx = 2
		}
		args = append(args,
			"-map", "0:v",
		)
		if hasAudio {
			args = append(args, "-map", "1:a")
		}
		args = append(args,
			"-map", fmt.Sprintf("%d:s", subInputIdx),
			"-c:s", "mov_text",
		)
		lang := opts.Language
		if lang == "" {
			lang = "und"
		}
		args = append(args, "-metadata:s:s:0", "language="+lang)
	}

	if hasAudio {
		args = append(args, "-shortest")
	}
	args = append(args, outPath)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// escapeSubtitlesFilter quotes a path for use inside ffmpeg's
// `-vf subtitles=...` filter expression. ffmpeg treats `:` as a filter
// arg separator and `\` as an escape; both need escaping. On Windows the
// drive letter colon is the canonical breakage but Unix paths can also
// contain colons in unusual setups. Wrapping in single quotes is the
// most portable form ffmpeg accepts.
func escapeSubtitlesFilter(p string) string {
	// Replace single quotes with the escape sequence ffmpeg expects
	// inside a single-quoted filter string.
	p = strings.ReplaceAll(p, `'`, `'\''`)
	return "'" + p + "'"
}
