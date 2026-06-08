---
slug: code/internal/audio
title: Package internal/audio
description: Auto-generated go doc reference for the internal/audio package.
---

# Package `internal/audio`

_Generated with `go doc -all ./internal/audio`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package audio // import "github.com/sirily11/debate-bot/internal/audio"


CONSTANTS

const AudioBytesPerSec = 6000
    AudioBytesPerSec is the constant byte rate of the LiveStream output.
    Azure TTS sends audio-24khz-48kbitrate-mono-mp3 — 48 kbit/s = 6000 bytes/s —
    and ffmpeg's `-c copy` preserves that rate.


FUNCTIONS

func ConcatToMP3(outDir, outPath string, files []string) error
    ConcatToMP3 writes a concat-demuxer list file and runs

        ffmpeg -f concat -safe 0 -i <list> -c copy <outPath>

    All input files must share the same codec/rate/channels — guaranteed here
    because every turn uses the same Azure output format.

    File paths are resolved to absolute form before being written into the
    list. The concat demuxer interprets relative entries against the list
    file's directory (not the working directory), so a relative path like
    `out/session/.../turn_001.mp3` would otherwise be double-prefixed when
    `outDir` itself is also a relative path under that same tree.

func VerifyTools() error
    VerifyTools checks ffplay and ffmpeg are on PATH. Call once at startup.
    ffmpeg is required by both LiveStream (pacer) and end-of-run ConcatToMP3.
    ffplay is required by the CLI run cmd to play the live audio stream.


TYPES

type LiveStream struct {
	// Has unexported fields.
}
    LiveStream is a single-writer, many-reader MP3 broadcaster.

    Internally it pipes the writer side through `ffmpeg -re` so that bytes are
    emitted to subscribers paced at realtime. The pipeline writes per-turn TTS
    MP3 bytes to LiveStream; HTTP clients and the local CLI ffplay subscribe.

    All Azure TTS turns share the format audio-24khz-48kbitrate-mono-mp3, which
    is byte-concat safe (same property ConcatToMP3 relies on). Late joiners
    resync at the next MP3 frame.

func NewLiveStream(ctx context.Context, log *slog.Logger) (*LiveStream, error)
    NewLiveStream starts the ffmpeg pacer subprocess and a pump goroutine that
    fans stdout to subscribers. ffmpeg must be on PATH (VerifyTools).

func (l *LiveStream) BytesAhead() int64
    BytesAhead returns how many bytes the producer is ahead of playback.
    Used by the pipeline to delay text-event publishing so subtitles align with
    the audio the listener actually hears.

func (l *LiveStream) CloseInput() error
    CloseInput signals end-of-stream. The buffered pipe drains remaining bytes
    to ffmpeg's stdin, then closes it; the pump drains ffmpeg's output and
    closes subscriber channels. Call once when the orchestrator finishes.

func (l *LiveStream) Done() <-chan struct{}
    Done returns a channel closed when the pump exits (ffmpeg ended).

func (l *LiveStream) FirstWriteAt() time.Time
    FirstWriteAt returns the wall-clock instant the first non-empty Write
    arrived. Zero until any byte has been written. The pipeline uses this
    to anchor sidecar VTT timestamps to the same moment the encoder's pump
    observes "first real audio" — i.e. mp4 t=0 after StartOffset trim. Anchoring
    on the first sentence's synth-completion (the older approach) leaves the
    music-bed-only prefix unaccounted for and the first cue lands at 00:00 even
    though speech doesn't start for several seconds.

func (l *LiveStream) Subscribe(bufChunks int) (<-chan []byte, func())
    Subscribe returns a chunk channel and a cancel func. bufChunks is the number
    of buffered chunks per subscriber (not bytes). 64 is fine for browsers.

func (l *LiveStream) Write(p []byte) (int, error)
    Write forwards mp3 bytes to ffmpeg stdin via the in-process buffer. Safe for
    concurrent calls only if the caller serialises writes (the pipeline uses a
    single producer goroutine). Blocks once the buffer is full so the producer
    can never race more than ~inputBufferBytes ahead of playback.

type SentenceSplitter struct {
	MinChars int
	// Has unexported fields.
}
    SentenceSplitter accumulates streamed text deltas and emits complete
    sentences. A sentence ends when a terminator is followed by whitespace,
    newline, or end-of-stream. Terminator clusters (e.g. "...") are kept
    together.

    MinChars (when > 0) coalesces sentences below the threshold with the
    following sentence so the consumer (typically TTS) doesn't get a stream
    of single-character clips like "是。" / "不是。" — those would synthesize into
    ~0.5s audio bursts whose subtitle flickers past before viewers can read it.
    Setting MinChars to a small rune count (e.g. 6) keeps the host's "是。
    <clarifying clause>。" pattern in one clip while still breaking up genuinely
    long prose.

func (s *SentenceSplitter) Flush() []string
    Flush returns any remaining buffered text as a final sentence (if
    non-empty).

func (s *SentenceSplitter) Push(chunk string) []string
    Push adds a chunk and returns any complete sentences it produced.
```
