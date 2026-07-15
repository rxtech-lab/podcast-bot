#!/usr/bin/env bash
#
# Start the backend in hermetic E2E mode and leave it running (foreground).
#
# This is the standalone counterpart to scripts/e2e.sh (which also runs the iOS
# XCUITests). Use it to bring up just the seeded server for manual API poking,
# for pointing the iOS app at a local backend during development, or for
# debugging the fixtures.
#
# E2E mode is fully hermetic: an in-process fake LLM replaces the real model
# endpoint, a fake TTS provider emits silent audio, auth is bypassed (every
# request resolves to the fixed user "test"), and the database is a freshly
# seeded local SQLite file. The config layer force-blanks TURSO_CONNECTION_URL /
# REDIS_URL in E2E mode, so a real cloud DB or cache can never be touched even if
# .env defines them.
#
# Usage:
#   scripts/e2e-server.sh              # fresh seed, listen on :8000, Ctrl-C to stop
#   E2E_PORT=8099 scripts/e2e-server.sh
#   E2E_KEEP_DB=1 scripts/e2e-server.sh   # reuse the existing DB (keep generated podcasts/summaries)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${E2E_PORT:-8000}"
BASE_URL="http://127.0.0.1:${PORT}"
DATA_ROOT="${E2E_DATA_ROOT:-/tmp/debate-bot-e2e}"

log() { printf '\033[1;34m[e2e]\033[0m %s\n' "$*"; }

export E2E_MODE=true
export E2E_DATA_ROOT="$DATA_ROOT"

if [ "${E2E_KEEP_DB:-}" = "1" ]; then
  log "reusing data root ${DATA_ROOT} (E2E_KEEP_DB=1)"
else
  log "wiping data root ${DATA_ROOT} for a fresh seed…"
  rm -rf "$DATA_ROOT"
fi

log "starting backend on ${BASE_URL} …"
log "auth is bypassed (fixed user 'test'); fake LLM + fake TTS; local SQLite"
log "seeded fixtures: test-ready, test-ongoing, test-plan, test-plan-voice, test-uploaded-audio, test2-private, test2-public"
log "health:    curl ${BASE_URL}/api/config"
log "press Ctrl-C to stop"

exec go run "${REPO_ROOT}/cmd/debate-bot" server --mode video --addr "127.0.0.1:${PORT}"
