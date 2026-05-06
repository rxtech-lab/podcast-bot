package video

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StitchOpts configures how StitchMP4 invokes ffmpeg.
//
// SoftSubs muxes SubtitlesPath into the output as a `mov_text` subtitle
// track (toggleable in players that support soft subs). Compatible with
// stream copy.
//
// BurnSubs is no longer applied at stitch time — the renderer paints
// captions directly into the HLS frames when its own
// BurnInSeriesCaptions flag is set, so re-applying ffmpeg's subtitles
// filter would double up. The field is kept for ABI continuity but
// ignored. Set Encoder Options.BurnInSeriesCaptions instead.
//
// SubtitlesPath is the .vtt sidecar (only used when SoftSubs is set;
// required in that case).
//
// Language is the BCP-47 tag stamped on the soft-sub track metadata
// (default "und" when blank). Ignored unless SoftSubs.
//
// StartOffset trims the front of the stitched mp4 by this many
// wall-clock seconds, dropping the silent prep prefix that the
// encoder accumulates before the show actually starts speaking.
// Zero (default) keeps the full HLS timeline. Stitch rounds the
// offset down to the nearest HLS segment boundary so -c:v copy can
// seek without an out-of-keyframe freeze.
type StitchOpts struct {
	SoftSubs      bool
	BurnSubs      bool // ignored; see doc comment
	SubtitlesPath string
	Language      string
	StartOffset   time.Duration
}

// hlsSegmentDuration is the segment length the encoder emits (see
// internal/video/encoder.go: hlsSegmentSec). Stitch rounds StartOffset
// down to a multiple of this so -ss + -c:v copy lands on a keyframe.
const hlsSegmentDuration = 2 * time.Second

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
	if opts.SoftSubs && opts.SubtitlesPath == "" {
		return fmt.Errorf("StitchOpts: SoftSubs requires SubtitlesPath")
	}
	if opts.SoftSubs {
		if _, err := os.Stat(opts.SubtitlesPath); err != nil {
			return fmt.Errorf("subtitles file missing: %w", err)
		}
	}

	// Round StartOffset down to the nearest HLS segment boundary so
	// `-ss` + `-c:v copy` lands on a keyframe instead of producing
	// a frozen-frame head while the decoder waits for the next IDR.
	// Dropping a few hundred ms of silence in trade is fine.
	startOffset := opts.StartOffset
	if startOffset > 0 {
		startOffset = (startOffset / hlsSegmentDuration) * hlsSegmentDuration
	}

	args := []string{"-y"}
	if startOffset > 0 {
		// `-ss` BEFORE `-i` is fast/keyframe seek; ffmpeg understands
		// HLS playlists and skips whole segments up to the offset.
		args = append(args, "-ss", formatSeconds(startOffset))
	}
	args = append(args, "-i", playlist)

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

	// Video is always copied — the renderer already paints any
	// burned-in captions into the HLS frames (when its
	// BurnInSeriesCaptions flag is set), so a re-encode here would
	// only double up.
	args = append(args, "-c:v", "copy")

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
		// AVPlayer (iOS / macOS QuickTime) needs three things to display
		// a soft-sub track in the picker with its proper name:
		//   1. ISO 639-2/T 3-letter language code on the track's mp4
		//      box (BCP-47 like "zh-CN" gets stored as "und" here, which
		//      is why the picker showed an empty entry).
		//   2. A human-readable `title` so the picker label isn't blank
		//      even when the device's locale doesn't autoresolve the
		//      language code.
		//   3. `default` disposition so AVPlayer's "Auto (Recommended)"
		//      actually picks this track instead of falling back to off.
		iso, title := normalizeSubtitleLang(opts.Language)
		args = append(args,
			"-metadata:s:s:0", "language="+iso,
			"-metadata:s:s:0", "title="+title,
			"-metadata:s:s:0", "handler_name="+title,
			"-disposition:s:0", "default",
		)
	}

	if hasAudio {
		args = append(args, "-shortest")
	}
	args = append(args, outPath)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// formatSeconds renders a Duration as a plain decimal-seconds string
// for ffmpeg's `-ss`. Trailing zeros are not trimmed (ffmpeg parses
// both forms) — keeps the formatting deterministic for tests.
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

// normalizeSubtitleLang maps a topic.Language value (typically a BCP-47
// tag like "zh-CN" / "en-US" or the ISO 639-1 prefix on its own) to the
// pair (iso639_2/T code, display title) that the soft-sub mp4 track
// needs. mov_text in mp4 stores the language as a 3-letter ISO 639-2
// code; AVPlayer's subtitle picker reads that code to pick a localized
// label, but only when the code is recognized — `und` (the fallback)
// renders as an empty row in the picker, which is the bug the user
// reported.
//
// Mapping is intentionally tiny — the project ships Chinese / English
// content with the occasional CJK extension, and an explicit table is
// easier to audit than a runtime BCP-47 library. Unrecognized inputs
// fall back to `und` + the generic "Subtitles" label so the picker at
// least shows a non-empty row instead of the blank entry.
func normalizeSubtitleLang(raw string) (iso, title string) {
	prefix := strings.ToLower(strings.TrimSpace(raw))
	// Take the first segment before `-` / `_` so "zh-CN", "zh_TW",
	// "cmn-Hans" all collapse to their language-family key.
	if i := strings.IndexAny(prefix, "-_"); i >= 0 {
		prefix = prefix[:i]
	}
	switch prefix {
	case "zh", "cmn", "yue", "zho", "chi":
		return "zho", "Chinese"
	case "en", "eng":
		return "eng", "English"
	case "ja", "jpn":
		return "jpn", "Japanese"
	case "ko", "kor":
		return "kor", "Korean"
	default:
		return "und", "Subtitles"
	}
}
