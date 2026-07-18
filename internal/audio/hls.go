package audio

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// StartHLSAudio transcodes a LiveStream's realtime MP3 broadcast into an HLS
// audio rendition (stream.m3u8 + MPEG-TS segments) under dir, so a native
// player (AVPlayer on iOS) can stream the audio-only feed live while it is
// still generating — the audio sibling of the video encoder's live HLS.
//
// It subscribes to live like any other consumer and pipes the paced MP3 into an
// ffmpeg HLS muxer running in EVENT mode (segments accumulate, the playlist
// grows, and #EXT-X-ENDLIST is appended once the input closes), so the same
// playlist serves both the live edge and, after the run, on-demand playback.
//
// The returned wait func blocks until ffmpeg has finalized the playlist after
// the LiveStream closes; call it during finalization so the ENDLIST tag is in
// place before the job is marked done.
func StartHLSAudio(ctx context.Context, live *LiveStream, dir string, log *slog.Logger) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("hls mkdir: %w", err)
	}
	playlist := filepath.Join(dir, "stream.m3u8")
	segPattern := filepath.Join(dir, "seg_%05d.ts")

	// EVENT playlist + append_list keeps every segment for later on-demand
	// playback; independent_segments lets a client start mid-stream. The MP3 is
	// re-encoded to AAC (the codec HLS players expect) at a matching low bitrate.
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "quiet",
		"-f", "mp3",
		"-i", "pipe:0",
		"-c:a", "aac",
		"-b:a", "160k",
		"-ar", "48000",
		"-ac", "2",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_playlist_type", "event",
		"-hls_flags", "append_list+independent_segments",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", segPattern,
		playlist,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("hls ffmpeg stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hls ffmpeg: %w", err)
	}

	chunks, cancel := live.Subscribe(256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range chunks {
			if _, werr := stdin.Write(chunk); werr != nil {
				if log != nil {
					log.Warn("hls audio: ffmpeg stdin write failed", "err", werr)
				}
				break
			}
		}
		// EOF tells ffmpeg to flush the last segment and append #EXT-X-ENDLIST.
		_ = stdin.Close()
		_ = cmd.Wait()
		cancel()
	}()

	return func() { <-done }, nil
}
