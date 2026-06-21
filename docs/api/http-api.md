---
slug: api/http-api
title: HTTP API Reference
description: REST + Server-Sent Events surface of the debate-bot server — channel routes, live audio/HLS streaming, viewer identity, and video-render jobs.
---

# HTTP API Reference

The `debate-bot` server (`internal/server`) exposes a single HTTP surface that
changes shape with its `mode`. **Stream mode** (default) mounts the live
channel, audio, and chat routes; **video mode** mounts the upload-and-render
job routes. Both modes also serve the embedded React SPA at `/` and share the
common routes below.

All JSON responses are `Content-Type: application/json`. The server uses Go's
`net/http` `ServeMux` pattern routing (method + path), so unmatched
method/path combinations return `405`/`404`.

## Common routes (both modes)

### `GET /api/config`
Returns the server mode so the SPA can pick which view to render.

```json
{ "mode": "stream" }   // or "video"
```

### `GET /api/me`
Returns the viewer's username, issuing and setting a `debate-bot-username`
cookie on first request.

### `POST /api/me`
Change the viewer's username.

```json
// request body
{ "username": "alice" }
```

### `GET /api/events`  (Server-Sent Events)
Subscribe to the live event stream. Optional `?channel=<id>` filters to one
channel; omitting it streams every channel. Emits a `: ok` comment on connect
and `: hb` heartbeats every 15s. Event tags and payloads:

| Event | Payload fields |
|-------|----------------|
| `transcript` | `channel_id`, `speaker`, `role`, `side`, `text`, `done` |
| `tick` | `channel_id`, `elapsed_ms`, `remaining_ms` |
| `phase` | `channel_id`, `phase`, `label` (display as-is), `type` |
| `status` | `channel_id`, `text` |
| `error` | `channel_id`, `text` |
| `ended` | `channel_id`, `transcript_path`, `audio_path` |
| `topic` | `channel_id`, `id`, `title`, `type`, `index`, `total`, `show`, `season`, `episode` |
| `topics_changed` | _(empty — clients re-fetch `/api/topics`)_ |

### `GET /api/debug`
Per-channel runtime snapshot — whether each channel is off-air, has a live
orchestrator, live stream, HLS dir, and DB path. Handy when `/api/messages`
returns `503` and you need to tell "off-air" from "stuck in setup".

## Stream-mode routes

### `GET /api/topics`
The channel switcher data — every channel with its current debate queue.

```json
{ "channels": [ { "id": "tech", "number": 1, "title": "Tech Channel", "off_air": false, "debates": [ … ], "current_debate_id": "…" } ] }
```

### `GET /api/transcript?channel=<id>`
JSON snapshot of a channel's transcript. Serves the in-memory snapshot when a
debate is live; otherwise falls back to the channel's most-recently-aired
sqlite file so a viewer who reloads after a show ends still sees history.
Returns an array of transcript lines:

```json
[ { "speaker": "Host", "role": "host", "side": "", "text": "…", "at": "2026-06-08T12:00:00Z" } ]
```

### `GET /api/audio/<channel>/stream`
Chunked `audio/mpeg` (MP3) live stream for the channel. Long-lived response;
`404` when the channel has no live audio.

### `GET /api/video/<channel>/<file>`
HLS playlist + segments for the channel. `<file>.m3u8` is served as
`application/vnd.apple.mpegurl` (no-cache); `<file>.ts` as `video/mp2t`
(`max-age=10`). Path traversal is rejected; `404` when the channel is off-air.

### `POST /api/messages?channel=<id>`
Push a viewer message into the channel's live orchestrator. Uses the viewer's
`debate-bot-username` cookie. Body is limited to 8 KiB.

```json
// request body
{ "text": "What about the economic angle?" }
```

Responses: `204 No Content` on success; `400` for empty/invalid body; `503`
("no active debate") when the channel has no live orchestrator.

## Video-mode routes

### `POST /api/jobs`  (multipart upload)
Submit a render job. Registers a pending job, stages uploads under
`<UploadRoot>/<jobID>/`, and hands off to the async runner.

Form fields:

| Field | Type | Notes |
|-------|------|-------|
| `script` | file (required) | the topic `.md` (must end `.md`) |
| `priors` | file (optional) | zip of prior series generations (series only) |
| `soft_subs` | `true`/`false` | mux a `mov_text` subtitle track |
| `burn_subs` | `true`/`false` | hardcode subtitles (forces re-encode) |
| `resolution` | string | output resolution override |
| `subtitle_languages` | repeated | translated soft-sub target codes |

Subtitle flags and a priors zip are gated to `type=series` at the runner level.
Success returns the job id; synchronous rejection (bad frontmatter, subtitle
flag on a non-series topic) returns `400` and marks the job errored.

```json
{ "id": "a1b2c3d4e5f60718" }
```

### `GET /api/jobs`
List every tracked job (debugging aid).

### `GET /api/jobs/{id}`
Single job snapshot. `404` for unknown ids (jobs aren't persisted across
restarts, though completed mp4/zip artifacts on disk are recovered on demand).
When S3 storage is configured, finished jobs include `download_url`; this is a
custom-domain URL when `S3_DOWNLOAD_BASE_URL` is set, otherwise a presigned S3
URL. Configure S3/R2 credentials with `S3_ACCESS_KEY_ID` and
`S3_SECRET_ACCESS_KEY`, or leave them empty to use the AWS SDK default
credential chain.

### `GET /api/jobs/{id}/video`
Download the rendered `.mp4` once the job reaches `done`. `425 Too Early` while
in flight; `404` if the asset doesn't exist. Sets a friendly
`Content-Disposition` filename derived from show/season/episode or title.

### `GET /api/jobs/{id}/audio`
Download the rendered `.mp3` for an audio-only job once it reaches `done`.
With S3 storage configured, this redirects to the S3/custom-domain download URL
instead of serving a local audio path.

### `GET /api/jobs/{id}/archive`
Download the per-job zip of the persistent show directory. **Series jobs only**
— non-series jobs return `404`. `425 Too Early` until the job is `done`.

## SPA

### `GET /`
Serves the embedded React single-page app (`internal/server/web-dist`) for any
unmatched path.
