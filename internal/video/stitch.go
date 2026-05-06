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
	SoftSubs       bool
	BurnSubs       bool // ignored; see doc comment
	SubtitlesPath  string
	Language       string
	SubtitleTracks []SubtitleTrack
	StartOffset    time.Duration
}

// SubtitleTrack is one WebVTT input to mux into the stitched MP4.
type SubtitleTrack struct {
	Path     string
	Language string
	Default  bool
}

// hlsSegmentDuration is the segment length the encoder emits (see
// internal/video/encoder.go: hlsSegmentSec). Stitch rounds StartOffset
// down to a multiple of this so -ss + -c:v copy lands on a keyframe.
const hlsSegmentDuration = 2 * time.Second

// StitchMP4 muxes the HLS playlist at hlsDir/stream.m3u8 into a single
// .mp4 at outPath. Both video and audio are stream-copied straight from
// the HLS source — the encoder already mixed the music bed underneath
// every TTS turn into the HLS audio track, so the stitched mp4 carries
// the same sonic mix the live stream listener heard. No separate audio
// concat step is needed (or wanted: a sidecar `debate.mp3` would only
// have the dry TTS without the music bed, which is why the previous
// version's mp4 sounded unmusical compared to the live channel).
//
// Returns an error if the playlist is missing or ffmpeg exits non-zero.
func StitchMP4(hlsDir, outPath string, opts StitchOpts) error {
	args, err := buildStitchArgs(hlsDir, outPath, opts)
	if err != nil {
		return err
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildStitchArgs(hlsDir, outPath string, opts StitchOpts) ([]string, error) {
	playlist := filepath.Join(hlsDir, "stream.m3u8")
	if _, err := os.Stat(playlist); err != nil {
		return nil, fmt.Errorf("hls playlist missing: %w", err)
	}
	tracks := opts.SubtitleTracks
	if opts.SoftSubs && len(tracks) == 0 && opts.SubtitlesPath != "" {
		tracks = []SubtitleTrack{{
			Path:     opts.SubtitlesPath,
			Language: opts.Language,
			Default:  true,
		}}
	}
	if opts.SoftSubs && len(tracks) == 0 {
		return nil, fmt.Errorf("StitchOpts: SoftSubs requires at least one subtitle track")
	}
	if opts.SoftSubs {
		for _, track := range tracks {
			if track.Path == "" {
				return nil, fmt.Errorf("subtitle track path is empty")
			}
			if _, err := os.Stat(track.Path); err != nil {
				return nil, fmt.Errorf("subtitles file missing: %w", err)
			}
		}
	}

	// Round StartOffset down to the nearest HLS segment boundary so
	// `-ss` + `-c:v copy` lands on a keyframe instead of producing
	// a frozen-frame head while the decoder waits for the next IDR.
	// Dropping a few hundred ms of silence in trade is fine. The same
	// `-ss` also trims the audio track in lockstep, so video and music
	// stay in sync after the prep prefix is dropped.
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

	if opts.SoftSubs {
		for _, track := range tracks {
			args = append(args, "-i", track.Path)
		}
	}

	// Stream-copy both tracks: the renderer already painted any
	// burned-in captions into the HLS frames (when its
	// BurnInSeriesCaptions flag is set), and the encoder's audio
	// pipeline already produced AAC at the desired bitrate, so any
	// re-encode here would only degrade fidelity.
	args = append(args,
		"-map", "0:v",
		"-map", "0:a",
		"-c:v", "copy",
		"-c:a", "copy",
	)

	if opts.SoftSubs {
		for i := range tracks {
			args = append(args, "-map", fmt.Sprintf("%d:s", i+1))
		}
		args = append(args, "-c:s", "mov_text")
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
		for i, track := range tracks {
			iso, title := normalizeSubtitleLang(track.Language)
			disposition := "0"
			if track.Default {
				disposition = "default"
			}
			args = append(args,
				fmt.Sprintf("-metadata:s:s:%d", i), "language="+iso,
				fmt.Sprintf("-metadata:s:s:%d", i), "title="+title,
				fmt.Sprintf("-metadata:s:s:%d", i), "handler_name="+title,
				fmt.Sprintf("-disposition:s:%d", i), disposition,
			)
		}
	}

	args = append(args, outPath)
	return args, nil
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
	normal := strings.ReplaceAll(prefix, "_", "-")
	switch normal {
	case "zh-hans", "zh-cn", "zh-sg":
		return "zho", "Simplified Chinese"
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return "zho", "Traditional Chinese"
	}
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
	case "es", "spa":
		return "spa", "Spanish"
	case "fr", "fra", "fre":
		return "fra", "French"
	case "de", "deu", "ger":
		return "deu", "German"
	default:
		return "und", "Subtitles"
	}
}
