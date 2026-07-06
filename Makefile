BINARY    := debate-bot
GO_PKG    := ./cmd/debate-bot
FRONTEND  := frontend
EMBED_DIR := internal/server/web-dist
BIN_DIR   := bin

# Docker image. The k8s cluster runs on linux/amd64, so images are always built
# for that platform regardless of the host arch (e.g. an Apple-silicon Mac).
IMAGE     := ghcr.io/rxtech-lab/podcast-bot
TAG       := test
PLATFORM  := linux/amd64
BUILDER   := debate-bot-builder

.PHONY: all build frontend backend run dev clean tidy gen-assets series-smoke series-recap-smoke \
        style-test style-golden style-font buildx-setup docker-build docker-push \
        e2e e2e-server rabbitmq-dev

all: build

# Full production build: bundle the React SPA into the embed directory,
# then build the Go binary that embeds it.
build: frontend backend

frontend:
	cd $(FRONTEND) && bun install && bun run build

backend:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(GO_PKG)

run: build
	./$(BIN_DIR)/$(BINARY)

# Dev: run Vite (with /api proxy) and the Go server in parallel.
# Frontend hot-reloads on http://localhost:5173; Go serves the API on :8080.
# Override the proxy target with VITE_API_PROXY_TARGET if your Go server is
# bound elsewhere.
dev:
	@echo "starting Go server on :8080 and Vite on :5173"
	@( cd $(FRONTEND) && bun run dev ) & \
	  go run $(GO_PKG) server --addr :8080 ; \
	  kill %1 2>/dev/null || true

tidy:
	go mod tidy
	cd $(FRONTEND) && bun install

# Start a local RabbitMQ (Homebrew) for the durable generation-job queue,
# then run the server with e.g.:
#   RABBITMQ_URL=amqp://guest:guest@127.0.0.1:5672/ make dev
# Without RABBITMQ_URL the server falls back to an in-process queue with the
# same retry semantics but no durability across restarts.
rabbitmq-dev:
	@command -v rabbitmq-server >/dev/null 2>&1 || brew install rabbitmq
	@brew services start rabbitmq
	@for i in $$(seq 1 60); do nc -z 127.0.0.1 5672 2>/dev/null && break; sleep 1; done
	@nc -z 127.0.0.1 5672 || { echo "rabbitmq did not become ready" >&2; exit 1; }
	@echo "rabbitmq ready on amqp://guest:guest@127.0.0.1:5672/"

# Regenerate the embedded TV-studio plates via the Vercel AI Gateway image
# endpoint. Reads OPENAI_API_KEY (vck_…) from .env. Run when you want a fresh
# look — the resulting PNGs are committed under internal/video/assets/.
gen-assets:
	go run ./cmd/gen-assets

# --- Docker (linux/amd64 for the k8s cluster) -------------------------------
# Create/boot a buildx builder capable of cross-building amd64 on any host.
# Idempotent: reuses the builder if it already exists.
buildx-setup:
	docker buildx inspect $(BUILDER) >/dev/null 2>&1 || \
	  docker buildx create --name $(BUILDER) --driver docker-container --bootstrap

# Build the amd64 image and load it into the local docker image store.
# Use this to smoke-test the image locally before pushing.
docker-build: buildx-setup
	docker buildx build --builder $(BUILDER) --platform $(PLATFORM) \
	  -t $(IMAGE):$(TAG) --load .

# Build the amd64 image and push it to the registry in one step.
# Override the tag with: make docker-push TAG=v1.2.3
docker-push: buildx-setup
	docker buildx build --builder $(BUILDER) --platform $(PLATFORM) \
	  -t $(IMAGE):$(TAG) --push .

clean:
	rm -rf $(BIN_DIR) $(EMBED_DIR) $(FRONTEND)/dist $(FRONTEND)/node_modules

# Run the single-episode series smoke (s01e01 by default). Writes the
# stitched mp4 to out/series-smoke/<show>-s01e01.mp4.
series-smoke:
	go run ./cmd/series-smoke

# Run the cross-episode smoke (s01e01 → s01e02). Validates the recap +
# image-reuse plumbing and writes out/series-recap-smoke/<show>-ep1-s01e01.mp4
# and <show>-ep2-s01e02.mp4.
series-recap-smoke:
	go run ./cmd/series-recap-smoke


dashboard-engine:
	go run ./cmd/debate-bot server --mode dashboard --addr :8000

# Fetch the pinned CJK font the style test renders with (8 MB, not committed —
# downloaded on demand and checksum-verified). Idempotent.
style-font:
	./scripts/fetch-style-font.sh

# Style regression guard: re-render one frame per content-type layout (debate,
# situation puzzle, series, discussion, audiobook) and assert it still matches
# the golden PNGs committed under smoke-test/. Fails the moment a renderer
# change shifts the pixels of any layout.
style-test: style-font
	go test ./internal/video -run TestStyleGolden

# Regenerate the smoke-test/ golden images after an intentional style change.
# Review the diff before committing the updated PNGs.
style-golden: style-font
	go test ./internal/video -run TestStyleGolden -update-golden

# --- End-to-end testing -----------------------------------------------------
# Full hermetic E2E run: build + launch the seeded backend (fake LLM/TTS, auth
# bypassed, local SQLite), then run the iOS XCUITest suite against it on the
# simulator, then tear down. Override the sim/port: make e2e E2E_SIMULATOR='iPhone 17'.
e2e:
	./scripts/e2e.sh

# Start just the hermetic, seeded backend in E2E mode and leave it running
# (Ctrl-C to stop). Use for manual API poking or pointing the iOS app at a local
# server. Override the port: make e2e-server E2E_PORT=8099.
e2e-server:
	./scripts/e2e-server.sh
