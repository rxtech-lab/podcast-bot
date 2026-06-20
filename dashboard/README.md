# Debate Bot Dashboard

A standalone Next.js control plane for the debate-bot Go engine. Sign in with
RxAuth, author a discussion **project** (host / discussants / commander +
settings) through a two-panel form + React Flow diagram, let an agent draft the
script from a topic, then generate a video and watch it live.

The dashboard owns auth (RxAuth/AuthJS) and its own data (Turso + Drizzle). It
talks to the engine over REST; the engine is the rendering engine.

## Stack

- Next.js 15 (App Router), React 19
- NextAuth v5 (OIDC `rxlab` provider) for RxAuth
- Turso (libSQL) + Drizzle ORM — project storage (via **server actions**)
- React JSON Schema Form (`@rjsf`) — the script editor form
- React Flow (`@xyflow/react`) — the host/discussant/commander diagram
- hls.js — live video preview

## Setup

1. **Engine** — run debate-bot in video mode with the dashboard env set:

   ```bash
   # in the debate-bot repo root, add to .env:
   DASHBOARD_ORIGINS=http://localhost:3001
   DASHBOARD_SERVICE_TOKEN=<a-long-random-shared-secret>
   # optional S3 upload of finished videos (else served from disk):
   # S3_BUCKET=... S3_REGION=... S3_ENDPOINT=... S3_PREFIX=...

   go run ./cmd/debate-bot server --mode dashboard --addr :8080
   ```

   `--mode dashboard` is the dedicated API backend for this dashboard: same
   job pipeline as `--mode video` (JSON script submit, live WS/SSE, S3
   upload) but it does not serve the embedded TV SPA — `/` returns a small
   health JSON and the Next.js app is the only frontend.

2. **RxAuth client** — register a confidential OIDC client in rxlab-auth with
   redirect URI `http://localhost:3001/api/auth/callback/rxlab`.

3. **Dashboard env** — copy `.env.example` to `.env.local` and fill in the
   RxAuth client, the `DASHBOARD_SERVICE_TOKEN` (must match the engine), the
   `ENGINE_BASE_URL`, and Turso credentials.

4. **Database**:

   ```bash
   npm install
   npm run db:migrate      # apply db/migrations to Turso
   ```

5. **Run**:

   ```bash
   npm run dev             # http://localhost:3001
   ```

## How it talks to the engine

- All engine REST calls go through `lib/engine.ts` server-side, attaching
  `Authorization: Bearer ${DASHBOARD_SERVICE_TOKEN}` — the token never reaches
  the browser.
- Live updates (SSE), HLS segments, and the final video are proxied through
  `app/api/engine/*` route handlers (session-protected) so the browser only
  ever talks to the dashboard origin.

## User flow

`/login` → `/projects` → `/project/new` (pick discussion + topic → planning:
generate / improve / confirm) → `/project/new/[id]` (two-panel editor, autosaved)
→ `/project/[id]/generate` (video config → live view: HLS preview + log +
read-only diagram with realtime agent status + participate box → download link).
