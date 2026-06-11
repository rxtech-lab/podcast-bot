# debate-bot

A multi-agent **live show generator** that turns Markdown scripts into broadcast-style
video + audio. Multiple LLM "agents" play out **debates**, **lateral-thinking puzzles**
(海龜湯), **panel discussions**, and **TV series** episodes, narrated with TTS and rendered
into a TV-channel UI (HLS) or a downloadable MP4.

It ships as a single Go binary that embeds a React (Vite) single-page app and orchestrates
the LLM, TTS, image, and music providers behind the scenes.

## Features

- **Multiple content types** — `debate`, `situation-puzzle`, `discussion`, `series`.
  Each `topic.md` declares its `type` in front-matter.
- **Two server modes:**
  - **stream** (default) — airs every queued topic over per-channel HLS video + MP3 audio,
    with a TV-tuner web UI. New `.md` files dropped into a watched folder are picked up live,
    no restart needed.
  - **video** — no channels; the browser uploads a `script.md` (and, for series, a zip of
    prior generations) and the server renders a downloadable `.mp4`.
- **Pluggable providers** — OpenAI-compatible chat endpoint, Azure / ElevenLabs TTS,
  Gemini (Lyria music + scene image generation).
- **MCP tools** — optional `mcp.json` lets agents call external Model Context Protocol tools.

## Requirements

| Tool | Version | Why |
|------|---------|-----|
| **Go** | 1.25+ | builds the backend (uses CGO for the SQLite driver) |
| **ffmpeg** + **ffplay** | recent | live-stream pacing, audio concat, playback (both must be on `PATH`) |
| **bun** | latest | installs & builds the React frontend |
| C toolchain (`gcc`/`clang`) | — | required because `mattn/go-sqlite3` is a CGO package |

API credentials (see [Environment](#environment)) for your chat / TTS / image / music providers.

## Setup

### 1. Clone & install toolchains

```bash
git clone https://github.com/sirily11/debate-bot.git
cd debate-bot

# macOS
brew install go ffmpeg oven-sh/bun/bun

# Debian/Ubuntu
sudo apt-get install -y golang ffmpeg build-essential
curl -fsSL https://bun.sh/install | bash
```

### 2. Configure environment

Copy the provided `.env` and fill in your keys:

```bash
cp .env .env.local   # or edit .env in place
```

`.env` is loaded automatically at startup (it takes precedence over your shell env).

### 3. Build

```bash
make build      # builds the frontend (bun) then the Go binary into ./bin/debate-bot
```

Or build the pieces individually:

```bash
make frontend   # bun install && bun run build  -> internal/server/web-dist
make backend    # go build -> bin/debate-bot
```

## Environment

Required vars (validated at startup — the process refuses to boot if any are missing):

| Var | Required | Description |
|-----|----------|-------------|
| `OPENAI_BASE_URL` | ✅ | OpenAI-compatible chat endpoint shared by host + agents |
| `OPENAI_API_KEY` | ✅ | API key for the chat endpoint |
| `HOST_MODEL` | ✅ | model id used by the host/moderator agent |
| `COMPRESSION_MODEL` | ✅ | model used to compress per-agent memory when it grows |
| `GEMINI_API_KEY` | ✅ | drives Lyria music + Gemini scene image generation |
| `COMPRESSION_BASE_URL` | — | defaults to `OPENAI_BASE_URL` |
| `COMPRESSION_API_KEY` | — | defaults to `OPENAI_API_KEY` |
| `SCENE_PLANNER_MODEL` | — | model for the visual-director pass; defaults to `HOST_MODEL` |
| `AZURE_SPEECH_KEY` / `AZURE_SPEECH_REGION` | when `tts_provider: azure` | Azure Speech credentials |
| `ELEVENLABS_API_KEY` | when `tts_provider: eleven` | ElevenLabs credentials |
| `OUT_DIR` | — | output root for audio/video/transcripts (default `./out`) |
| `SERIES_ROOT` | — | cross-run archive root for `series` episodes (default `OUT_DIR`) |
| `APP_PASSWORD` | — | if set, gate the web UI + API behind this password (same as `--password`) |

Provider-specific TTS keys are only required when a `topic.md` selects that provider.

## Running

### Stream mode (TV channels) — default

```bash
./bin/debate-bot server \
  --channel ./channels/channels.json \
  --content "./topics/*.md" \
  --addr :3000
```

Then open <http://localhost:3000>. Each `topic.md` front-matter must declare a `channel`
that matches an `id` in `channels.json`. The directory behind `--content` is auto-watched:
drop a new `.md` in and it airs without a restart.

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--content` | — | path or glob to topic `.md` file(s); repeatable |
| `--channel` | `./channels.json` | channel registry (`{id, number, title}` array) |
| `--mcp` | — | optional `mcp.json` for MCP tools |
| `--out` | `$OUT_DIR` | output directory override |
| `--addr` | `:3000` | HTTP listen address |
| `--password` | `$APP_PASSWORD` | gate the web UI + API behind a password (see below) |

### Password protection

Start the server with `--password` (or set `APP_PASSWORD`) to require a login:

```bash
./bin/debate-bot server --content "./topics/*.md" --password "hunter2"
# or
APP_PASSWORD=hunter2 ./bin/debate-bot server --content "./topics/*.md"
```

When a password is set, the SPA shows a login screen and every `/api/*` route
returns **401** until the browser signs in. Authentication is a cookie set by
`POST /api/login`, so the SSE event stream and HLS audio/video keep working
automatically. Omit the flag (the default) to leave the server open.

Both `--mode stream` and `--mode video` honour the password.

### Video mode (upload → MP4)

```bash
./bin/debate-bot server --mode video --addr :3000 --max-concurrency 2
```

Open the web UI and upload a `script.md`; the server renders an MP4 you can download.
`--max-concurrency` caps simultaneous renders.

### Dev (hot-reload frontend)

```bash
make dev   # Vite on :5173 (proxies /api), Go server on :8080
```

## Topic format

Each `topic.md` is YAML front-matter + Markdown body. Minimal `debate` example
(see `examples/topic.md` and `examples/discussion.md` for full samples):

```markdown
---
title: "AI 是否會取代程序員"
type: debate          # debate | situation-puzzle | discussion | series
language: zh-CN
channel: tech         # must match an id in channels.json
total_minutes: 30
segment_max_seconds: 60
affirmative:
  - { name: "Linda", model: "gpt-4o" }
negative:
  - { name: "Alice", model: "gpt-4o" }
judge: { model: "gpt-4o" }
---

## Background
...
```

## Make targets

| Target | Description |
|--------|-------------|
| `make build` | full production build (frontend + backend) |
| `make frontend` / `make backend` | build one half |
| `make run` | build then run the server |
| `make dev` | Vite + Go in parallel for development |
| `make gen-assets` | regenerate the embedded TV-studio background plates |
| `make series-smoke` / `make series-recap-smoke` | end-to-end series smoke tests |
| `make tidy` | `go mod tidy` + `bun install` |
| `make clean` | remove build artifacts |

## Docker

Build the image and run the server (stream mode):

```bash
docker build -t debate-bot .

docker run --rm -p 3000:3000 \
  --env-file .env \
  -v "$PWD/channels:/app/channels" \
  -v "$PWD/topics:/app/topics" \
  -v "$PWD/out:/app/out" \
  debate-bot \
  server --channel ./channels/channels.json --content "./topics/*.md" --addr :3000
```

For video mode, override the command:

```bash
docker run --rm -p 3000:3000 --env-file .env -v "$PWD/out:/app/out" \
  debate-bot server --mode video --addr :3000
```

The image bundles `ffmpeg`/`ffplay` and the compiled binary with the embedded web UI.
Mount `channels/`, your topics folder, and `out/` so config and generated media persist
outside the container.

## Output

Each run writes to `OUT_DIR/session-<timestamp>/` — per-channel HLS segments, the stitched
`debate.mp3`, `transcript.txt`, per-agent `memory/`, and `run.log`. Series episodes also
archive into `SERIES_ROOT/tv-series/<show>/...` for cross-episode recaps.
