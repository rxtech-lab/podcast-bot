#!/usr/bin/env bash
# Start the disposable dashboard backend used by the admin Playwright suite.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${E2E_PORT:-8000}"
DATA_ROOT="${E2E_DATA_ROOT:-/tmp/debate-bot-admin-e2e}"
BIN="${DATA_ROOT}.server-bin"

export E2E_MODE=true
export E2E_QUEUE_MODE=inline
export E2E_DATA_ROOT="$DATA_ROOT"

rm -rf "$DATA_ROOT"

go build -o "$BIN" "${REPO_ROOT}/cmd/debate-bot"

# Execute the compiled process directly so Playwright owns and terminates the
# actual listener. `go run` can leave its compiled child behind when the parent
# receives Playwright's shutdown signal.
exec "$BIN" server \
  --mode dashboard \
  --addr "127.0.0.1:${PORT}"
