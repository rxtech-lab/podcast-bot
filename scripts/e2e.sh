#!/usr/bin/env bash
#
# End-to-end test runner.
#
# Builds and launches the Go backend in hermetic E2E mode (fake LLM, fake TTS,
# local SQLite, seeded fixtures, auth bypassed), waits for it to come up, then
# runs the iOS XCUITest suite against it on the simulator, and finally tears the
# server down. Reproducible locally and in CI.
#
# Usage:
#   scripts/e2e.sh                 # build + run backend + run all UI tests
#   E2E_ONLY=backend scripts/e2e.sh   # just start the seeded backend and wait (Ctrl-C to stop)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${E2E_PORT:-8000}"
BASE_URL="http://127.0.0.1:${PORT}"
DATA_ROOT="${E2E_DATA_ROOT:-/tmp/debate-bot-e2e}"
SIMULATOR="${E2E_SIMULATOR:-iPhone 17 Pro}"
SCHEME="${E2E_SCHEME:-iOS}"
TEST_PLAN="${E2E_TEST_PLAN:-iosUITestPlan}"
IOS_PROJECT="${REPO_ROOT}/iOS/iOS.xcodeproj"
RESULT_BUNDLE="${E2E_RESULT_BUNDLE:-${DATA_ROOT}.xcresult}"
# Parallel XCUITest workers (simulator clones). XCTest distributes work at
# test-class granularity, so more workers than test classes buys nothing.
# E2E_WORKERS=1 restores fully serial execution.
WORKERS="${E2E_WORKERS:-3}"

log()  { printf '\033[1;34m[e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[e2e] %s\033[0m\n' "$*" >&2; exit 1; }

# --- Ensure RabbitMQ (real broker for the generation-job queue) --------------
# The E2E backend consumes generation jobs from a real RabbitMQ. Start one via
# Homebrew when nothing is listening on 5672 (idempotent on CI runners that
# keep the service around). Queue names carry a per-run prefix so successive
# runs sharing one broker never see each other's messages; prefixed queues
# auto-expire when idle.
if ! nc -z 127.0.0.1 5672 2>/dev/null; then
  command -v brew >/dev/null 2>&1 || fail "rabbitmq is not running and homebrew is unavailable to install it"
  log "starting rabbitmq via homebrew…"
  brew list rabbitmq >/dev/null 2>&1 || brew install rabbitmq
  brew services start rabbitmq
fi
log "waiting for rabbitmq on 127.0.0.1:5672…"
for _ in $(seq 1 60); do
  nc -z 127.0.0.1 5672 2>/dev/null && rabbit_ok=1 && break
  sleep 1
done
[ "${rabbit_ok:-}" = 1 ] || fail "rabbitmq did not become ready on port 5672"
export E2E_RABBITMQ_URL="${E2E_RABBITMQ_URL:-amqp://guest:guest@127.0.0.1:5672/}"
export MQ_QUEUE_PREFIX="e2e-$(date +%s)-$$-"
log "rabbitmq ready · queue prefix ${MQ_QUEUE_PREFIX}"

# A stale or concurrent server on the port would serve the wrong seed data to
# every simulator clone; fail fast instead of debugging cross-run mismatches.
if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
  fail "port ${PORT} already in use (another e2e run?) — set E2E_PORT to override"
fi

# --- Build the backend ------------------------------------------------------
log "building backend…"
BIN="$(mktemp -t debate-bot-e2e)"
( cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/debate-bot ) || fail "backend build failed"

# --- Start the backend (clean, seeded, hermetic) ----------------------------
log "wiping data root ${DATA_ROOT} for a fresh seed…"
rm -rf "$DATA_ROOT"

export E2E_MODE=true
export E2E_DATA_ROOT="$DATA_ROOT"

log "starting backend on ${BASE_URL}…"
"$BIN" server --mode video --addr "127.0.0.1:${PORT}" >"${DATA_ROOT}.server.log" 2>&1 &
SRV_PID=$!

cleanup() {
  log "stopping backend (pid ${SRV_PID})…"
  kill "$SRV_PID" 2>/dev/null || true
  wait "$SRV_PID" 2>/dev/null || true
  rm -f "$BIN"
}
trap cleanup EXIT

log "waiting for backend health…"
for _ in $(seq 1 120); do
  if curl -fsS "${BASE_URL}/api/config" >/dev/null 2>&1; then ok=1; break; fi
  if ! kill -0 "$SRV_PID" 2>/dev/null; then
    cat "${DATA_ROOT}.server.log" >&2 || true
    fail "backend exited during startup"
  fi
  sleep 0.5
done
[ "${ok:-}" = 1 ] || { cat "${DATA_ROOT}.server.log" >&2 || true; fail "backend did not become healthy"; }
log "backend healthy · seeded fixtures: test-ready, test-ready-summary, test-ongoing, test-plan, test-plan-voice, test2-private, test2-public"

if [ "${E2E_ONLY:-}" = "backend" ]; then
  log "E2E_ONLY=backend · backend running at ${BASE_URL}. Press Ctrl-C to stop."
  wait "$SRV_PID"
  exit 0
fi

# --- Run the iOS UI tests ---------------------------------------------------
log "running test plan '${TEST_PLAN}' on '${SIMULATOR}' with ${WORKERS} worker(s)…"
# xcodebuild forwards host env vars prefixed with TEST_RUNNER_ to the UI-test
# runner process (stripping the prefix), so the test reads E2E_API_BASE_URL and
# forwards it (plus E2E_TEST_MODE) to the app under test via launchEnvironment.
export TEST_RUNNER_E2E_API_BASE_URL="$BASE_URL"
rm -rf "$RESULT_BUNDLE"
PARALLEL_FLAGS=(-parallel-testing-enabled NO)
if [ "$WORKERS" -gt 1 ]; then
  PARALLEL_FLAGS=(-parallel-testing-enabled YES -parallel-testing-worker-count "$WORKERS")
fi
set -x
xcodebuild test \
  -project "$IOS_PROJECT" \
  -scheme "$SCHEME" \
  -testPlan "$TEST_PLAN" \
  -destination "platform=iOS Simulator,name=${SIMULATOR}" \
  "${PARALLEL_FLAGS[@]}" \
  -resultBundlePath "$RESULT_BUNDLE" \
  -skipMacroValidation \
  | tee "${DATA_ROOT}.xcodebuild.log"
set +x

log "done."
