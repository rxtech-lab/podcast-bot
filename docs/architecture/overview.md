---
slug: architecture/overview
title: Architecture Overview
description: High-level design of debate-bot — a multi-agent live-show content engine that runs AI debates, lateral-thinking puzzles, and serialized episodes as parallel TV channels with live audio + HLS video.
---

# Architecture Overview

`debate-bot` is a Go application that turns markdown topic files into **live,
multi-agent shows** — affirmative-vs-negative debates, 海龜湯 lateral-thinking
puzzles ("situation puzzles"), and serialized multi-episode series. It runs as
a long-lived HTTP server in **TV-channel mode**: a `channels.json` defines the
channels, each topic `.md` declares which channel it belongs to, and every
channel runs its own queue of shows sequentially while all channels run in
parallel. Each channel owns a live audio stream, an ffmpeg encoder, and an HLS
output directory; a browser tunes between channels through an embedded React
SPA.

The module path is `github.com/sirily11/debate-bot` (Go 1.25).

## Two run modes

| Mode | Surface | Purpose |
|------|---------|---------|
| `stream` (default) | `/api/topics`, `/api/transcript`, `/api/events`, `/api/audio/*`, `/api/video/*`, `/api/messages` | Live TV channels — debates/puzzles air in real time with streamed MP3 audio + HLS video and viewer chat. |
| `video` | `/api/jobs/*` | Upload-and-render — POST a `script.md` (+ optional priors), receive a downloadable mp4 and, for series, a zip archive. |

Both modes serve the same embedded SPA from `internal/server/web-dist`.

## Request / data flow (stream mode)

```
topic .md files ──► watcher ──► channel queue ──┐
   (channels.json assigns each topic a channel)  │
                                                  ▼
                              content_creator.Orchestrator (one per show)
                                                  │
        ┌─────────────────────────────┬──────────┼───────────────┬───────────────┐
        ▼                             ▼          ▼                ▼               ▼
   agent (LLM speakers)          llm client   tts.Provider    memory store    tools / mcp
   debate / puzzle / series       (OpenAI-     (Azure /        (compaction)    (function calls)
   turn planners                  compatible)   ElevenLabs)
        │                                          │
        ▼                                          ▼
   transcript lines ──► eventbus (pub/sub) ──► server SSE (/api/events)
                                          └──► audio.LiveStream ──► /api/audio/<ch>/stream (MP3)
                                          └──► video encoder ──► HLS segments ──► /api/video/<ch>/<file>
```

The **eventbus** (`internal/eventbus`) is a tiny in-memory pub/sub. The
orchestrator publishes typed events (`TranscriptMsg`, `TickMsg`, `PhaseMsg`,
`StatusMsg`, `TopicMsg`, `EndedMsg`, …); the HTTP server fans them out to
browsers over Server-Sent Events, while audio and video sinks consume the same
stream to drive the live MP3 and HLS outputs.

## Packages

| Package | Responsibility |
|---------|----------------|
| `cmd/debate-bot` | CLI entry point. Subcommand `server` (alias `run`) boots channels, watcher, and HTTP server. `cmd/*-smoke` are render/encoder smoke harnesses. |
| `internal/server` | HTTP API: channel routes, SSE, live audio/HLS, viewer identity (cookie), and `video`-mode job routes. |
| `internal/content_creator` | The orchestrators — debate, situation-puzzle, and series — plus planners, turn loop, transcript store, subtitle generation, and the render pipeline. The heart of the system. |
| `internal/agent` | LLM-backed show participants (host, debaters, puzzle players) and the transcript model. |
| `internal/llm` | Thin client over an OpenAI-compatible chat API (configurable base URL / model). |
| `internal/config` | Loads `.env`, `channels.json`, and per-topic frontmatter (`type`, `channel`, …). |
| `internal/tts` | Text-to-speech providers (Azure, ElevenLabs) and SSML construction. |
| `internal/audio` | `LiveStream` — the per-channel streamed MP3 buffer browsers subscribe to. |
| `internal/video` | Long-lived ffmpeg encoder that bakes the live show into HLS; scene/plate rendering, transitions, movement. |
| `internal/videojob` | The `video`-mode upload-and-render job runner. |
| `internal/series` | Wires the orchestrator to the series stage (recaps, cross-episode image reuse). |
| `internal/memory` | Conversation memory store + compressor (context compaction for long shows). |
| `internal/eventbus` | In-memory pub/sub for orchestrator events. |
| `internal/watcher` | Filesystem watcher — hot-loads new topic `.md` files into channel queues at runtime. |
| `internal/mcp` / `internal/tools` | Model Context Protocol client + the tool registry agents can call. |
| `internal/subtitleutil` | Rendering-agnostic subtitle helpers shared across renderers. |
| `internal/util` | Small shared helpers. |

## Content types

Each topic `.md` declares a `type` in its frontmatter:

- **`debate`** — multi-agent affirmative-vs-negative debate with phased
  structure (opening, free debate, closing) and a host.
- **`situation-puzzle`** — 海龜湯 lateral-thinking puzzle; the host knows the
  hidden truth and players ask yes/no questions.
- **Series** — serialized episodes (`s01e01` → `s01e02`) that carry forward a
  recap and reuse generated scene images across episodes.

Unknown types abort startup with a clear error.

## Configuration & environment

Configured via flags plus a `.env` file (see `cmd/debate-bot` usage):

- LLM: `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `HOST_MODEL`,
  `COMPRESSION_BASE_URL` / `COMPRESSION_API_KEY` / `COMPRESSION_MODEL`.
- Media: `GEMINI_API_KEY` (Lyria music + Gemini scene image generation),
  `AZURE_SPEECH_KEY` / `AZURE_SPEECH_REGION` (Azure TTS), `ELEVENLABS_API_KEY`
  (ElevenLabs TTS).
- Output: `OUT_DIR` (default `./out`).

## Build & run

```bash
make build        # bundle the React SPA into the embed dir, then build the Go binary
make dev          # Vite on :5173 + Go server on :8080 (hot reload)
./bin/debate-bot server --channel ./channels.json --content "./topics/*.md" --addr :8080
```

See the [HTTP API reference](../api/http-api.md) for the full endpoint surface
and the [Go package reference](../code/internal-content_creator.generated.md)
for exported APIs.
